// Package gitlab reads repository content through the GitLab REST API (v4). It
// serves GitLab.com as well as self-managed and dedicated instances — they
// differ only in the API endpoint.
package gitlab

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/glueckkanja/terraform-provider-gkvm/internal/source"
)

// DefaultBaseURL is the REST API endpoint of GitLab.com.
const DefaultBaseURL = "https://gitlab.com/api/v4"

// PrivateTokenHeader carries personal, group and project access tokens.
const PrivateTokenHeader = "PRIVATE-TOKEN"

// JobTokenHeader carries a CI job token (CI_JOB_TOKEN). GitLab rejects a job
// token sent as a private token, so the two cannot share one header.
const JobTokenHeader = "JOB-TOKEN"

// treePageSize is the maximum page size the GitLab tree endpoint accepts. The
// endpoint paginates at 20 entries by default, which would silently truncate a
// profile directory, so every listing is paged explicitly.
const treePageSize = 100

// maxTreePages bounds the paging loop; 100 pages is 10.000 entries.
const maxTreePages = 100

// Client fetches content from a GitLab project.
type Client struct {
	Token string
	// TokenHeader is PrivateTokenHeader (default) or JobTokenHeader.
	TokenHeader string
	// Project is the path with namespace, e.g. "group/subgroup/project".
	Project string
	Ref     string // branch, tag, or commit SHA

	// BaseURL is the REST API endpoint. Empty means DefaultBaseURL.
	BaseURL string

	// HTTPClient overrides the shared client; set only in tests.
	HTTPClient *http.Client
}

var _ source.Client = (*Client)(nil)

// NormalizeBaseURL turns what a user is likely to paste into the endpoint the
// REST API actually lives at:
//
//	""                          -> https://gitlab.com/api/v4
//	https://gitlab.example.com  -> https://gitlab.example.com/api/v4
//	https://gitlab.example.com/api/v4 -> unchanged
//
// Anything that already carries a path is left alone, so an instance served
// under a subpath or behind a proxy can be addressed exactly.
func NormalizeBaseURL(raw string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	if trimmed == "" {
		return DefaultBaseURL
	}

	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		return trimmed
	}
	if u.Path != "" {
		return trimmed
	}
	return trimmed + "/api/v4"
}

// ValidateProject checks the "namespace/project" form GitLab requires.
func ValidateProject(project string) error {
	// GitLab nests groups, so two or more segments are valid:
	// "group/project", "group/subgroup/project".
	segments, err := source.SplitRepoSegments(project)
	if err == nil && len(segments) < 2 {
		err = fmt.Errorf("%q has no namespace", project)
	}
	if err != nil {
		return fmt.Errorf("invalid repository %q for platform \"gitlab\": must be a project path with namespace such as \"group/project\" or \"group/subgroup/project\", using only alphanumeric characters, hyphens, underscores, and dots (%s)", project, err)
	}
	return nil
}

// CLIHost returns the hostname to pass to "glab auth token --hostname" for an
// endpoint. The glab CLI stores credentials per instance under its web
// hostname, which is the API host itself.
func CLIHost(baseURL string) string {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return source.Host(baseURL)
}

// ValidateConfig checks the client configuration before any request is made.
func (c *Client) ValidateConfig() error {
	if c.BaseURL != "" {
		if err := source.ValidateBaseURL(c.BaseURL); err != nil {
			return err
		}
	}
	if err := ValidateProject(c.Project); err != nil {
		return err
	}
	return source.ValidateRef(c.Ref)
}

// Endpoint returns the API endpoint in use.
func (c *Client) Endpoint() string {
	if c.BaseURL == "" {
		return DefaultBaseURL
	}
	return strings.TrimRight(c.BaseURL, "/")
}

// Subject returns the project this client reads.
func (c *Client) Subject() string {
	return c.Project
}

// Reference returns the git ref this client reads.
func (c *Client) Reference() string {
	return c.Ref
}

// Ping validates connectivity by fetching the project root directory listing.
func (c *Client) Ping() error {
	_, err := c.ListDirectory("")
	return err
}

// ListDirectory returns the contents of a directory path within the project.
// Pass an empty string for the repository root.
func (c *Client) ListDirectory(path string) ([]source.Entry, error) {
	var entries []source.Entry

	for page := 1; page <= maxTreePages; page++ {
		body, err := c.requester().Get(c.treeURL(path, page), "application/json")
		if err != nil {
			return nil, err
		}

		var raw []struct {
			Name string `json:"name"`
			Path string `json:"path"`
			Type string `json:"type"` // "blob" or "tree"
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, fmt.Errorf("parsing directory listing: %w", err)
		}

		for _, e := range raw {
			entries = append(entries, source.Entry{Name: e.Name, Path: e.Path, IsDir: e.Type == "tree"})
		}

		// A short page is the last page; GitLab reports the next page in a
		// header, but a short page is the same signal without reading headers.
		if len(raw) < treePageSize {
			return entries, nil
		}
	}

	return nil, fmt.Errorf("directory listing for %s exceeded %d pages", c.Project, maxTreePages)
}

// FetchFile returns the raw content of a file, addressed by its project path.
func (c *Client) FetchFile(path string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("empty file path")
	}
	// The raw endpoint answers with file bytes of any type, so the request must
	// not claim to want JSON; a strict gateway in front of the instance would
	// answer 406.
	return c.requester().Get(c.rawFileURL(path), "*/*")
}

func (c *Client) requester() *source.Requester {
	headers := map[string]string{}
	if c.Token != "" {
		header := c.TokenHeader
		if header == "" {
			header = PrivateTokenHeader
		}
		headers[header] = c.Token
	}
	return &source.Requester{
		Platform:   "GitLab",
		Subject:    c.Project,
		BaseURL:    c.Endpoint(),
		Headers:    headers,
		HTTPClient: c.HTTPClient,
	}
}

// projectRoute is the project-scoped API prefix. GitLab addresses a project by
// its path with namespace URL-encoded into a single path parameter, so the
// separators become %2F.
func (c *Client) projectRoute() string {
	return fmt.Sprintf("%s/projects/%s", c.Endpoint(), source.EscapeWhole(c.Project))
}

func (c *Client) treeURL(path string, page int) string {
	query := url.Values{}
	query.Set("per_page", fmt.Sprintf("%d", treePageSize))
	query.Set("page", fmt.Sprintf("%d", page))
	if c.Ref != "" {
		query.Set("ref", c.Ref)
	}
	if path != "" {
		query.Set("path", path)
	}
	return fmt.Sprintf("%s/repository/tree?%s", c.projectRoute(), query.Encode())
}

func (c *Client) rawFileURL(path string) string {
	u := fmt.Sprintf("%s/repository/files/%s/raw", c.projectRoute(), source.EscapeWhole(path))
	if c.Ref != "" {
		u += "?ref=" + url.QueryEscape(c.Ref)
	}
	return u
}
