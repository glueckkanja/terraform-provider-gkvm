package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestValidateBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"github.com", "https://api.github.com", false},
		{"ghe.com data residency", "https://api.example.ghe.com", false},
		{"enterprise server path", "https://ghe.example.com/api/v3", false},
		{"gitlab self managed", "https://gitlab.example.com/api/v4", false},
		{"http rejected", "http://api.github.com", true},
		{"no scheme", "api.github.com", true},
		{"no host", "https://", true},
		{"embedded credentials", "https://user:pass@ghe.example.com", true},
		{"query string", "https://ghe.example.com/api/v3?x=1", true},
		{"fragment", "https://ghe.example.com/api/v3#frag", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBaseURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateBaseURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

func TestValidateRefAndPath(t *testing.T) {
	if err := ValidateRef("main"); err != nil {
		t.Errorf("unexpected error for valid ref: %v", err)
	}
	if err := ValidateRef("main&x=1"); err == nil {
		t.Error("expected error for ref with URL-special characters")
	}
	if err := ValidatePath("defaults"); err != nil {
		t.Errorf("unexpected error for valid path: %v", err)
	}
	if err := ValidatePath("../secrets"); err == nil {
		t.Error("expected error for traversal path")
	}
	if err := ValidatePath("/etc/passwd"); err == nil {
		t.Error("expected error for absolute path")
	}
}

func TestSplitRepoSegments(t *testing.T) {
	tests := []struct {
		in      string
		wantLen int
		wantErr bool
	}{
		{"owner/repo", 2, false},
		{"group/sub/project", 3, false},
		{"my.org/my-repo_v2", 2, false},
		{"", 0, true},
		{"../other-repo", 0, true},
		{"./repo", 0, true},
		{"owner/../other", 0, true},
		{"owner//repo", 0, true},
		{"owner/ repo", 0, true},
		{"owner/repo?x=1", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			segments, err := SplitRepoSegments(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("SplitRepoSegments(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if err == nil && len(segments) != tt.wantLen {
				t.Errorf("got %d segments, want %d", len(segments), tt.wantLen)
			}
		})
	}
}

func TestHostnameAndHasPath(t *testing.T) {
	tests := []struct {
		in           string
		wantHost     string
		wantHostname string
		wantHasPath  bool
	}{
		{"https://api.github.com", "api.github.com", "api.github.com", false},
		{"https://ghe.example.com/api/v3", "ghe.example.com", "ghe.example.com", true},
		// A port belongs in a request target but never in a CLI hostname.
		{"https://gitlab.example.com:8443/api/v4", "gitlab.example.com:8443", "gitlab.example.com", true},
		{"https://gitlab.example.com:8443", "gitlab.example.com:8443", "gitlab.example.com", false},
		{"https://ghe.example.com/", "ghe.example.com", "ghe.example.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := Host(tt.in); got != tt.wantHost {
				t.Errorf("Host() = %q, want %q", got, tt.wantHost)
			}
			if got := Hostname(tt.in); got != tt.wantHostname {
				t.Errorf("Hostname() = %q, want %q", got, tt.wantHostname)
			}
			if got := HasPath(tt.in); got != tt.wantHasPath {
				t.Errorf("HasPath() = %v, want %v", got, tt.wantHasPath)
			}
		})
	}
}

// net/http follows redirects by default and drops only Authorization,
// Www-Authenticate, Cookie and Cookie2 across hosts. GitLab authenticates with
// PRIVATE-TOKEN, which is not on that list, so a redirect the endpoint chooses
// would otherwise hand the credential to any host it names.
func TestGet_RefusesRedirectOffTheEndpoint(t *testing.T) {
	var leakedPrivate, leakedJob, leakedAuth string
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		leakedAuth = req.Header.Get("Authorization")
		leakedPrivate = req.Header.Get("PRIVATE-TOKEN")
		leakedJob = req.Header.Get("JOB-TOKEN")
		_, _ = w.Write([]byte("attacker payload"))
	}))
	t.Cleanup(foreign.Close)

	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, foreign.URL+"/steal", http.StatusFound)
	}))
	t.Cleanup(endpoint.Close)

	r := &Requester{
		Platform: "GitLab",
		Subject:  "group/project",
		BaseURL:  endpoint.URL,
		Headers: map[string]string{
			"Authorization": "Bearer gh-secret",
			"PRIVATE-TOKEN": "glpat-secret",
			"JOB-TOKEN":     "job-secret",
		},
		HTTPClient: endpoint.Client(),
	}

	body, err := r.Get(context.Background(), r.Base()+"/anything", "application/json")

	// The credentials are checked first: a leak is the consequence that matters,
	// and it must be reported even if the call somehow returned no error.
	for name, got := range map[string]string{"Authorization": leakedAuth, "PRIVATE-TOKEN": leakedPrivate, "JOB-TOKEN": leakedJob} {
		if got != "" {
			t.Errorf("%s reached the foreign host: %q", name, got)
		}
	}
	if err == nil {
		t.Fatalf("expected the redirect to be refused, got body %q", body)
	}
	if !strings.Contains(err.Error(), "redirected away from the configured endpoint") {
		t.Errorf("error does not explain the refusal: %v", err)
	}
	if body != nil {
		t.Errorf("body = %q, want nothing from the redirect target", body)
	}
}

// The same leak with a genuinely different hostname. net/http strips
// Authorization across domains but its sensitive-header list covers only
// Authorization, Www-Authenticate, Cookie and Cookie2, so GitLab's
// PRIVATE-TOKEN survives the hop. The endpoint pin is what stops it; the
// hostname differs from the listener's 127.0.0.1 while resolving to it, so the
// target is genuinely reachable and genuinely a different host to net/http.
func TestGet_RefusesRedirectToAnotherHostname(t *testing.T) {
	var (
		reached bool
		target  string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/elsewhere" {
			reached = true
			_, _ = w.Write([]byte("attacker payload"))
			return
		}
		http.Redirect(w, req, target, http.StatusFound)
	}))
	t.Cleanup(server.Close)
	target = "http://localhost:" + portOf(t, server.URL) + "/elsewhere"

	r := &Requester{
		Platform:   "GitLab",
		Subject:    "group/project",
		BaseURL:    server.URL,
		Headers:    map[string]string{"PRIVATE-TOKEN": "glpat-secret"},
		HTTPClient: server.Client(),
	}

	if _, err := r.Get(context.Background(), r.Base()+"/anything", "application/json"); err == nil {
		t.Fatal("expected the redirect to be refused, got nil")
	}
	if reached {
		t.Error("the request followed the redirect to the other hostname")
	}
}

func portOf(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return u.Port()
}

// A redirect that stays on the endpoint is normal (a renamed repository, a
// canonical path) and must still be followed.
func TestGet_FollowsRedirectOnTheEndpoint(t *testing.T) {
	var sawPrivate string
	mux := http.NewServeMux()
	mux.HandleFunc("/moved", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/final", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, req *http.Request) {
		sawPrivate = req.Header.Get("PRIVATE-TOKEN")
		_, _ = w.Write([]byte("ok"))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	r := &Requester{
		Platform:   "GitLab",
		Subject:    "group/project",
		BaseURL:    server.URL,
		Headers:    map[string]string{"PRIVATE-TOKEN": "glpat-secret"},
		HTTPClient: server.Client(),
	}

	body, err := r.Get(context.Background(), r.Base()+"/moved", "application/json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(body) != "ok" {
		t.Errorf("body = %q", body)
	}
	if sawPrivate != "glpat-secret" {
		t.Errorf("PRIVATE-TOKEN = %q, want it kept on the endpoint", sawPrivate)
	}
}

// A body cut at the size cap is often still parseable — a truncated YAML
// profile would silently drop the alert rules past the cut — so the cap has to
// be an error, not a shorter result.
func TestGet_RejectsOversizedResponse(t *testing.T) {
	r := newTestRequester(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		chunk := strings.Repeat("a", 1024*1024)
		for i := 0; i < 11; i++ {
			_, _ = w.Write([]byte(chunk))
		}
	}))

	_, err := r.Get(context.Background(), r.Base()+"/big", "application/json")
	if err == nil {
		t.Fatal("expected an error for an oversized response, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds the") {
		t.Errorf("error does not name the limit: %v", err)
	}
}

func TestGet_HonoursContextCancellation(t *testing.T) {
	r := newTestRequester(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := r.Get(ctx, r.Base()+"/anything", "application/json")
	if err == nil {
		t.Fatal("expected an error for a cancelled context, got nil")
	}
	if !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("error does not report the cancellation: %v", err)
	}
}

func TestEscaping(t *testing.T) {
	if got := EscapeSegments("defaults/a b.yaml"); got != "defaults/a%20b.yaml" {
		t.Errorf("EscapeSegments = %q", got)
	}
	if got := EscapeWhole("group/sub/project"); got != "group%2Fsub%2Fproject" {
		t.Errorf("EscapeWhole = %q", got)
	}
}

func newTestRequester(t *testing.T, handler http.Handler) *Requester {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return &Requester{
		Platform:   "Test",
		Subject:    "test/repo",
		BaseURL:    server.URL,
		Headers:    map[string]string{"Authorization": "Bearer test-token"},
		HTTPClient: server.Client(),
	}
}

func TestGet_SendsHeadersToConfiguredEndpoint(t *testing.T) {
	var gotAuth, gotAccept string
	r := newTestRequester(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotAuth = req.Header.Get("Authorization")
		gotAccept = req.Header.Get("Accept")
		_, _ = w.Write([]byte("ok"))
	}))

	body, err := r.Get(context.Background(), r.Base()+"/anything", "application/json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(body) != "ok" {
		t.Errorf("body = %q, want %q", body, "ok")
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q, want the configured credential", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q", gotAccept)
	}
}

func TestGet_RefusesForeignHost(t *testing.T) {
	r := newTestRequester(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	foreign := []string{
		"http://169.254.169.254/latest/meta-data/",
		"https://evil.example/payload",
		"https://api.github.com.evil.example/steal",
	}
	for _, u := range foreign {
		if _, err := r.Get(context.Background(), u, "application/json"); err == nil {
			t.Errorf("expected refusal for %q, got nil", u)
		}
	}
}

func TestGet_HTTPErrorsAndTokenSafety(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantSubstr string
	}{
		{"unauthorized", http.StatusUnauthorized, "401"},
		{"forbidden", http.StatusForbidden, "403"},
		{"not found", http.StatusNotFound, "404"},
		{"server error", http.StatusInternalServerError, "500"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newTestRequester(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "error body with super-secret-token", tt.statusCode)
			}))
			r.Headers = map[string]string{"Authorization": "Bearer super-secret-token"}

			_, err := r.Get(context.Background(), r.Base()+"/anything", "application/json")
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantSubstr)
			}
			if strings.Contains(err.Error(), "super-secret-token") {
				t.Errorf("credential leaked in error message: %v", err)
			}
		})
	}
}
