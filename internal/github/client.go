package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// maxResponseBytes limits the size of GitHub API responses to prevent OOM from
// malicious or unexpectedly large payloads (10 MB).
const maxResponseBytes = 10 * 1024 * 1024

// httpClient is a shared HTTP client with sensible timeouts.
var httpClient = &http.Client{
	Timeout: 30 * time.Second,
}

// repoPattern validates the owner/repo format (alphanumeric, hyphens, underscores, dots).
var repoPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+$`)

// Client fetches content from a GitHub repository.
type Client struct {
	Token string
	Repo  string // "owner/repo"
	Ref   string // branch, tag, or commit SHA

	// TestBaseURL and TestHTTPClient override the defaults; set only in tests.
	TestBaseURL    string
	TestHTTPClient *http.Client
}

// Entry represents a single item from the GitHub Contents API.
type Entry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Type        string `json:"type"` // "file" or "dir"
	DownloadURL string `json:"download_url"`
}

// ValidateConfig checks that Repo and Ref are safe before making requests.
func (c *Client) ValidateConfig() error {
	if !repoPattern.MatchString(c.Repo) {
		return fmt.Errorf("invalid github_repo format %q: must be \"owner/repo\" using only alphanumeric characters, hyphens, underscores, and dots", c.Repo)
	}
	if c.Ref != "" && strings.ContainsAny(c.Ref, " \t\n\r&?#") {
		return fmt.Errorf("invalid github_ref %q: must not contain whitespace or URL-special characters (&, ?, #)", c.Ref)
	}
	return nil
}

// Ping validates connectivity by fetching the repository root directory listing.
func (c *Client) Ping() error {
	_, err := c.ListDirectory("")
	return err
}

// ListDirectory returns the contents of a directory path within the repository.
// Pass an empty string for the repository root.
func (c *Client) ListDirectory(path string) ([]Entry, error) {
	apiURL := c.buildURL(path)

	body, err := c.doRequest(apiURL, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}

	var entries []Entry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("parsing directory listing: %w", err)
	}

	return entries, nil
}

// FetchFile downloads the raw content of a file from a trusted GitHub URL.
// The URL must point to raw.githubusercontent.com or objects.githubusercontent.com.
func (c *Client) FetchFile(rawURL string) ([]byte, error) {
	// Security: validate that the URL points to a trusted GitHub domain.
	// The download_url comes from the GitHub Contents API response. A compromised
	// repo or MITM could inject arbitrary URLs, leading to SSRF (e.g., fetching
	// cloud metadata at 169.254.169.254 or internal network resources).
	if !strings.HasPrefix(rawURL, "https://raw.githubusercontent.com/") &&
		!strings.HasPrefix(rawURL, "https://objects.githubusercontent.com/") {
		return nil, fmt.Errorf("untrusted download_url %q: must be from raw.githubusercontent.com or objects.githubusercontent.com", rawURL)
	}
	return c.doRequest(rawURL, "application/octet-stream")
}

func (c *Client) apiBase() string {
	if c.TestBaseURL != "" {
		return c.TestBaseURL
	}
	return "https://api.github.com"
}

func (c *Client) getHTTPClient() *http.Client {
	if c.TestHTTPClient != nil {
		return c.TestHTTPClient
	}
	return httpClient
}

func (c *Client) buildURL(path string) string {
	base := fmt.Sprintf("%s/repos/%s/contents", c.apiBase(), c.Repo)
	if path != "" {
		base += "/" + path
	}
	if c.Ref != "" {
		base += "?ref=" + url.QueryEscape(c.Ref)
	}
	return base
}

// isGitHubDomain checks that a URL points to a trusted GitHub domain.
func isGitHubDomain(rawURL string) bool {
	return strings.HasPrefix(rawURL, "https://api.github.com/") ||
		strings.HasPrefix(rawURL, "https://raw.githubusercontent.com/") ||
		strings.HasPrefix(rawURL, "https://objects.githubusercontent.com/")
}

func (c *Client) doRequest(requestURL, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	// Security: only attach the Bearer token to known GitHub domains.
	// This prevents token leakage if a URL somehow points elsewhere.
	if c.Token != "" && isGitHubDomain(requestURL) {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.getHTTPClient().Do(req)
	if err != nil {
		// Sanitize error — do not leak token or full URL details in error messages.
		return nil, fmt.Errorf("GitHub API request to %s failed: connection error (check network and token validity)", c.Repo)
	}
	defer func() { _ = resp.Body.Close() }()

	// Limit response body to prevent OOM from unexpectedly large payloads.
	limitedReader := io.LimitReader(resp.Body, maxResponseBytes)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("reading response from %s: %w", c.Repo, err)
	}

	if resp.StatusCode != http.StatusOK {
		// Sanitize: include status code and repo but NOT the full response body,
		// which may contain tokens or internal details from proxies/GitHub.
		hint := ""
		switch resp.StatusCode {
		case 401:
			hint = " (check that your GitHub token is valid and not expired)"
		case 403:
			hint = " (check token permissions — needs 'contents: read' on the repository)"
		case 404:
			hint = " (check that the repository, ref, and path exist)"
		}
		return nil, fmt.Errorf("GitHub API returned HTTP %d for %s (ref: %s)%s",
			resp.StatusCode, c.Repo, c.Ref, hint)
	}

	return body, nil
}
