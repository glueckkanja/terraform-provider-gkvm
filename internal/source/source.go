// Package source holds what every repository backend has in common: the shape
// of a directory entry, the interface the data sources consume, and an HTTP
// requester that is pinned to exactly one API endpoint.
package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// MaxResponseBytes limits the size of API responses to prevent OOM from
// malicious or unexpectedly large payloads (10 MB).
const MaxResponseBytes = 10 * 1024 * 1024

// maxRedirects bounds a same-host redirect chain.
const maxRedirects = 5

// sharedHTTPClient is used by every backend unless a test injects its own.
var sharedHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
}

// Entry is one item of a repository directory listing, normalized across
// backends (GitHub reports "file"/"dir", GitLab reports "blob"/"tree").
type Entry struct {
	Name  string
	Path  string
	IsDir bool
}

// Client reads files out of one repository at one ref.
type Client interface {
	// Endpoint returns the API endpoint in use, for error messages.
	Endpoint() string
	// Subject returns the repository or project this client reads.
	Subject() string
	// Reference returns the git ref this client reads.
	Reference() string
	// ValidateConfig checks the configuration before any request is made. It is
	// part of the interface rather than an optional extra so a backend added
	// later cannot reach the network with an unvalidated repository or ref.
	ValidateConfig() error
	// Ping verifies connectivity and credentials against the repository root.
	Ping(ctx context.Context) error
	// ListDirectory lists a directory; the empty path means the repository root.
	ListDirectory(ctx context.Context, path string) ([]Entry, error)
	// FetchFile returns the raw bytes of a file, addressed by repository path.
	FetchFile(ctx context.Context, path string) ([]byte, error)
}

// ValidateBaseURL checks that a user-supplied API endpoint is well formed and
// safe to send credentials to. HTTPS is required: the token travels in a
// request header, and every supported deployment serves its API over TLS. A
// proxy is configured through the standard HTTPS_PROXY environment variable,
// not by downgrading the endpoint.
func ValidateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid base_url %q: %w", raw, err)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("invalid base_url %q: must use the https scheme", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("invalid base_url %q: missing host", raw)
	}
	if u.User != nil {
		return fmt.Errorf("invalid base_url %q: must not embed credentials", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid base_url %q: must not contain a query string or fragment", raw)
	}
	return nil
}

// segmentPattern is the character set GitHub and GitLab allow in the segments
// of a repository path.
var segmentPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// SplitRepoSegments validates the shape of a repository path and returns its
// segments. Beyond the character set, a segment must not be a relative path
// element: "." and ".." consist of allowed characters, so a pattern match alone
// would accept "../other-repo" and let the path climb out of its route.
func SplitRepoSegments(value string) ([]string, error) {
	if value == "" {
		return nil, fmt.Errorf("must not be empty")
	}
	segments := strings.Split(value, "/")
	for _, segment := range segments {
		if segment == "." || segment == ".." {
			return nil, fmt.Errorf("%q is a relative path element", segment)
		}
		if !segmentPattern.MatchString(segment) {
			return nil, fmt.Errorf("%q is not a valid path segment", segment)
		}
	}
	return segments, nil
}

// ValidateRef checks that a git ref cannot break out of the URL it is placed in.
func ValidateRef(ref string) error {
	if ref != "" && strings.ContainsAny(ref, " \t\n\r&?#") {
		return fmt.Errorf("invalid ref %q: must not contain whitespace or URL-special characters (&, ?, #)", ref)
	}
	return nil
}

// ValidatePath checks that a repository path stays inside the repository.
func ValidatePath(path string) error {
	if path != "" && (strings.Contains(path, "..") || strings.HasPrefix(path, "/")) {
		return fmt.Errorf("invalid path %q: must not contain path traversal (..) or start with /", path)
	}
	return nil
}

// Host returns the host of an endpoint including any port, or "" if it cannot
// be parsed. Use it to compare request targets, never as a CLI hostname.
func Host(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return u.Host
}

// Hostname returns the bare hostname of an endpoint, without a port. Both
// platform CLIs key their credential store by bare hostname, so a port must
// not travel into "--hostname".
func Hostname(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// HasPath reports whether an endpoint carries a URL path, which distinguishes
// a path-based deployment from one that publishes the API on its own host.
func HasPath(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	return strings.Trim(u.Path, "/") != ""
}

// EscapeSegments percent-encodes every segment of a repository path while
// keeping the separators intact, so a path can never break out of its route.
func EscapeSegments(p string) string {
	segments := strings.Split(p, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return strings.Join(segments, "/")
}

// EscapeWhole percent-encodes a value that has to survive as a single path
// parameter, separators included — GitLab addresses projects and files that
// way ("group/project" becomes "group%2Fproject").
func EscapeWhole(p string) string {
	return strings.ReplaceAll(url.PathEscape(p), "/", "%2F")
}

// errOffEndpoint marks a request or redirect that would leave the configured
// endpoint. It is wrapped by net/http when a redirect is refused, so it is
// matched with errors.Is rather than compared.
var errOffEndpoint = errors.New("target is not the configured endpoint")

// Requester performs GET requests against exactly one API endpoint.
type Requester struct {
	// Platform names the backend in error messages ("GitHub", "GitLab").
	Platform string
	// Subject names the repository or project in error messages.
	Subject string
	// BaseURL is the API endpoint; every request must stay on its host.
	BaseURL string
	// Headers are sent with every request, credentials included. They are safe
	// to hold here because Get refuses to leave BaseURL's host.
	Headers map[string]string
	// HTTPClient overrides the shared client; set only in tests.
	HTTPClient *http.Client
}

// Base returns the endpoint without a trailing slash.
func (r *Requester) Base() string {
	return strings.TrimRight(r.BaseURL, "/")
}

// httpClient returns a copy of the configured client with the redirect policy
// applied. The policy has to live on the client, and the client may be injected
// by a test, so it is attached per request rather than once at construction.
func (r *Requester) httpClient() *http.Client {
	base := sharedHTTPClient
	if r.HTTPClient != nil {
		base = r.HTTPClient
	}
	client := *base
	client.CheckRedirect = r.checkRedirect
	return &client
}

// checkRedirect keeps a redirect chain on the configured endpoint.
//
// Without it the endpoint check would only cover the first request: net/http
// follows redirects by default, and while it drops the Authorization header on
// a cross-host redirect, its list of sensitive headers covers only
// Authorization, Www-Authenticate, Cookie and Cookie2. GitLab authenticates
// with PRIVATE-TOKEN and JOB-TOKEN, which are not on that list and would be
// copied verbatim to whatever host a redirect names.
func (r *Requester) checkRedirect(req *http.Request, via []*http.Request) error {
	if !r.onEndpoint(req.URL.String()) {
		return fmt.Errorf("redirect to %s://%s: %w", req.URL.Scheme, req.URL.Host, errOffEndpoint)
	}
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects on %s", maxRedirects, r.Base())
	}
	return nil
}

// onEndpoint reports whether a URL points at the configured endpoint.
func (r *Requester) onEndpoint(rawURL string) bool {
	base, err := url.Parse(r.Base())
	if err != nil {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Scheme, base.Scheme) && strings.EqualFold(u.Host, base.Host)
}

// Get fetches a URL on the configured endpoint and returns the response body.
func (r *Requester) Get(ctx context.Context, requestURL, accept string) ([]byte, error) {
	// Security: every request must stay on the configured endpoint. Request
	// URLs are built by the backends themselves, so this cannot fail today; it
	// is defence in depth against a future code path that takes a URL from an
	// API response, which a compromised deployment or a MITM could point at an
	// internal address (SSRF) or at a host that would collect the credentials.
	if !r.onEndpoint(requestURL) {
		return nil, fmt.Errorf("refusing to request %q: not the configured endpoint %s", requestURL, r.Base())
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Accept", accept)
	for name, value := range r.Headers {
		req.Header.Set(name, value)
	}

	resp, err := r.httpClient().Do(req)
	if err != nil {
		// A refused redirect is a distinct, actionable condition and must not be
		// flattened into the generic connection error below.
		if errors.Is(err, errOffEndpoint) {
			return nil, fmt.Errorf("%s API request for %s was redirected away from the configured endpoint %s and was refused: %w",
				r.Platform, r.Subject, r.Base(), err)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("%s API request to %s for %s was cancelled: %w", r.Platform, r.Base(), r.Subject, ctxErr)
		}
		// Sanitize: report what the user can act on, never the credentials or
		// the full URL.
		return nil, fmt.Errorf("%s API request to %s for %s failed: connection error (check network, endpoint and token validity)",
			r.Platform, r.Base(), r.Subject)
	}
	defer func() { _ = resp.Body.Close() }()

	// Limit response body to prevent OOM from unexpectedly large payloads. One
	// byte past the limit is read so the cap can be reported: a truncated YAML
	// profile is often still valid YAML, which would silently drop whatever
	// alert rules fell off the end.
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading response for %s: %w", r.Subject, err)
	}
	if len(body) > MaxResponseBytes {
		return nil, fmt.Errorf("response for %s from %s exceeds the %d byte limit and was not read",
			r.Subject, r.Base(), MaxResponseBytes)
	}

	if resp.StatusCode != http.StatusOK {
		// Sanitize: include status code and subject but NOT the response body,
		// which may carry tokens or internal details from proxies.
		hint := ""
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			hint = " (check that the token is valid, unexpired, and issued by this endpoint)"
		case http.StatusForbidden:
			hint = " (check token permissions — read access to the repository contents is required)"
		case http.StatusNotFound:
			hint = " (check that the endpoint, repository, ref, and path exist — a token without read access also reports 404)"
		}
		return nil, fmt.Errorf("%s API at %s returned HTTP %d for %s%s",
			r.Platform, r.Base(), resp.StatusCode, r.Subject, hint)
	}

	return body, nil
}
