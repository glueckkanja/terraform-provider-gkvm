// Package github reads repository content through the GitHub REST API. It
// serves GitHub.com, GitHub Enterprise Cloud (including data residency) and
// GitHub Enterprise Server — they differ only in the API endpoint.
package github

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/glueckkanja/terraform-provider-gkvm/internal/source"
)

// DefaultBaseURL is the REST API endpoint of GitHub.com.
const DefaultBaseURL = "https://api.github.com"

// apiVersion pins the REST API version GitHub.com serves. Enterprise Server
// ignores the header, so sending it is harmless there.
const apiVersion = "2022-11-28"

// Client fetches content from a GitHub repository.
type Client struct {
	Token string
	Repo  string // "owner/repo"
	Ref   string // branch, tag, or commit SHA

	// BaseURL is the REST API endpoint. Empty means DefaultBaseURL.
	BaseURL string

	// HTTPClient overrides the shared client; set only in tests.
	HTTPClient *http.Client
}

var _ source.Client = (*Client)(nil)

// NormalizeBaseURL turns what a user is likely to paste into the endpoint the
// REST API actually lives at:
//
//	""                              -> https://api.github.com
//	https://api.github.com          -> unchanged
//	https://api.SUBDOMAIN.ghe.com   -> unchanged   (Enterprise Cloud, data residency)
//	https://HOSTNAME                -> https://HOSTNAME/api/v3   (Enterprise Server)
//	https://HOSTNAME/api/v3         -> unchanged
//
// Enterprise Cloud publishes the API on an "api." hostname, Enterprise Server
// under the /api/v3 path of the web host. Anything that already carries a path
// is left alone, so an unusual deployment or a proxy can be addressed exactly.
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
	if strings.HasPrefix(u.Host, "api.") {
		return trimmed
	}
	return trimmed + "/api/v3"
}

// ValidateRepo checks the "owner/repo" form GitHub requires.
func ValidateRepo(repo string) error {
	segments, err := source.SplitRepoSegments(repo)
	if err == nil && len(segments) != 2 {
		err = fmt.Errorf("%q has %d path segments", repo, len(segments))
	}
	if err != nil {
		return fmt.Errorf("invalid repository %q for platform \"github\": must be \"owner/repo\" using only alphanumeric characters, hyphens, underscores, and dots (%s)", repo, err)
	}
	return nil
}

// CLIHost returns the hostname to pass to "gh auth token --hostname" for an
// endpoint. The gh CLI stores credentials per deployment under its web
// hostname: the API host without the leading "api." label
// ("api.github.com" -> "github.com", "api.SUBDOMAIN.ghe.com" ->
// "SUBDOMAIN.ghe.com"). Path-based endpoints such as "https://HOSTNAME/api/v3"
// already carry the web hostname.
func CLIHost(baseURL string) string {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	host := source.Host(baseURL)
	return strings.TrimPrefix(host, "api.")
}

// ValidateConfig checks the client configuration before any request is made.
func (c *Client) ValidateConfig() error {
	if c.BaseURL != "" {
		if err := source.ValidateBaseURL(c.BaseURL); err != nil {
			return err
		}
	}
	if err := ValidateRepo(c.Repo); err != nil {
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

// Subject returns the repository this client reads.
func (c *Client) Subject() string {
	return c.Repo
}

// Reference returns the git ref this client reads.
func (c *Client) Reference() string {
	return c.Ref
}

// Ping validates connectivity by fetching the repository root directory listing.
func (c *Client) Ping() error {
	_, err := c.ListDirectory("")
	return err
}

// ListDirectory returns the contents of a directory path within the repository.
// Pass an empty string for the repository root.
func (c *Client) ListDirectory(path string) ([]source.Entry, error) {
	body, err := c.requester().Get(c.contentsURL(path), "application/vnd.github+json")
	if err != nil {
		return nil, err
	}

	var raw []struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Type string `json:"type"` // "file" or "dir"
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parsing directory listing: %w", err)
	}

	entries := make([]source.Entry, 0, len(raw))
	for _, e := range raw {
		entries = append(entries, source.Entry{Name: e.Name, Path: e.Path, IsDir: e.Type == "dir"})
	}
	return entries, nil
}

// FetchFile returns the raw content of a file, addressed by its repository path.
//
// The file is read through the Contents API on the configured endpoint using
// the raw media type, rather than by following the download_url the API
// returns. That keeps every request on the one configured host: GitHub.com
// serves raw content from raw.githubusercontent.com, while each Enterprise
// deployment serves it from a host of its own, so following download_url would
// mean guessing a second hostname per deployment and widening the set of hosts
// the token may be sent to.
func (c *Client) FetchFile(path string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("empty file path")
	}
	return c.requester().Get(c.contentsURL(path), "application/vnd.github.raw")
}

func (c *Client) requester() *source.Requester {
	headers := map[string]string{"X-GitHub-Api-Version": apiVersion}
	if c.Token != "" {
		headers["Authorization"] = "Bearer " + c.Token
	}
	return &source.Requester{
		Platform:   "GitHub",
		Subject:    c.Repo,
		BaseURL:    c.Endpoint(),
		Headers:    headers,
		HTTPClient: c.HTTPClient,
	}
}

func (c *Client) contentsURL(path string) string {
	u := fmt.Sprintf("%s/repos/%s/contents", c.Endpoint(), c.Repo)
	if path != "" {
		u += "/" + source.EscapeSegments(path)
	}
	if c.Ref != "" {
		u += "?ref=" + url.QueryEscape(c.Ref)
	}
	return u
}
