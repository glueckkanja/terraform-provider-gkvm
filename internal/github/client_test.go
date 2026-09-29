package github

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeBaseURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty defaults to github.com", "", DefaultBaseURL},
		{"github.com unchanged", "https://api.github.com", "https://api.github.com"},
		{"trailing slash trimmed", "https://api.github.com/", "https://api.github.com"},
		{"data residency unchanged", "https://api.example.ghe.com", "https://api.example.ghe.com"},
		// The host a data residency tenant hands out is the web one. Appending
		// the Enterprise Server path to it would 404.
		{"data residency web host corrected", "https://example.ghe.com", "https://api.example.ghe.com"},
		{"data residency trailing slash", "https://example.ghe.com/", "https://api.example.ghe.com"},
		{"data residency mistaken api/v3 corrected", "https://example.ghe.com/api/v3", "https://api.example.ghe.com"},
		{"data residency mixed case", "https://Example.GHE.com", "https://api.Example.GHE.com"},
		{"enterprise server gets api/v3", "https://ghe.example.com", "https://ghe.example.com/api/v3"},
		{"explicit path kept", "https://ghe.example.com/api/v3", "https://ghe.example.com/api/v3"},
		{"proxy subpath kept", "https://proxy.example.com/github", "https://proxy.example.com/github"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeBaseURL(tt.in); got != tt.want {
				t.Errorf("NormalizeBaseURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCLIHost(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", "github.com"},
		{"https://api.github.com", "github.com"},
		{"https://api.example.ghe.com", "example.ghe.com"},
		{"https://ghe.example.com/api/v3", "ghe.example.com"},
		{"https://apiserver.example.com/api/v3", "apiserver.example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := CLIHost(tt.in); got != tt.want {
				t.Errorf("CLIHost(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name    string
		client  Client
		wantErr bool
	}{
		{"valid", Client{Repo: "owner/repo", Ref: "main"}, false},
		{"valid with dots", Client{Repo: "my.org/my-repo_v2", Ref: "v1.0.0"}, false},
		{"valid sha", Client{Repo: "owner/repo", Ref: "abc123def456"}, false},
		{"valid enterprise endpoint", Client{Repo: "owner/repo", Ref: "main", BaseURL: "https://ghe.example.com/api/v3"}, false},
		{"invalid repo spaces", Client{Repo: "owner/ repo"}, true},
		{"invalid repo traversal", Client{Repo: "../etc/passwd"}, true},
		// "." and ".." are made of allowed characters, so a character-class
		// match alone used to accept them as an owner.
		{"invalid repo parent segment", Client{Repo: "../other-repo"}, true},
		{"invalid repo current segment", Client{Repo: "./repo"}, true},
		{"invalid repo nested", Client{Repo: "group/sub/project"}, true},
		{"invalid ref newline", Client{Repo: "owner/repo", Ref: "main\ninjection"}, true},
		{"invalid ref query", Client{Repo: "owner/repo", Ref: "main&foo=bar"}, true},
		{"invalid endpoint scheme", Client{Repo: "owner/repo", BaseURL: "http://ghe.example.com"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.client.ValidateConfig()
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// newMockClient creates a Client wired to a test HTTP server.
func newMockClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client := &Client{
		Token:      "test-token",
		Repo:       "test/repo",
		Ref:        "main",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	}
	return client, server
}

func TestListDirectory_Success(t *testing.T) {
	var gotPath, gotQuery, gotAccept, gotAuth string

	client, _ := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		gotAccept, gotAuth = r.Header.Get("Accept"), r.Header.Get("Authorization")

		payload := []map[string]string{
			{"name": "firewall.yaml", "path": "defaults/firewall.yaml", "type": "file"},
			{"name": "subdir", "path": "defaults/subdir", "type": "dir"},
		}
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Errorf("encode: %v", err)
		}
	}))

	entries, err := client.ListDirectory("defaults")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Name != "firewall.yaml" || entries[0].IsDir {
		t.Errorf("first entry = %+v, want the file", entries[0])
	}
	if !entries[1].IsDir {
		t.Errorf("second entry = %+v, want a directory", entries[1])
	}
	if gotPath != "/repos/test/repo/contents/defaults" {
		t.Errorf("request path = %q", gotPath)
	}
	if gotQuery != "ref=main" {
		t.Errorf("request query = %q, want ref=main", gotQuery)
	}
	if gotAccept != "application/vnd.github+json" {
		t.Errorf("Accept = %q", gotAccept)
	}
	// The endpoint is the configured one, so the token belongs on the wire.
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q, want the configured token", gotAuth)
	}
}

func TestFetchFile_UsesRawMediaTypeOnTheConfiguredEndpoint(t *testing.T) {
	var gotPath, gotAccept, gotHost string

	client, server := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAccept, gotHost = r.URL.Path, r.Header.Get("Accept"), r.Host
		_, _ = w.Write([]byte("metric_alerts: {}\n"))
	}))

	content, err := client.FetchFile("defaults/firewall.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(content), "metric_alerts") {
		t.Errorf("content = %q", content)
	}
	if gotAccept != "application/vnd.github.raw" {
		t.Errorf("Accept = %q, want the raw media type", gotAccept)
	}
	if gotPath != "/repos/test/repo/contents/defaults/firewall.yaml" {
		t.Errorf("request path = %q", gotPath)
	}
	// No second host: raw content is served by the configured endpoint, which
	// is what makes Enterprise deployments work.
	if want := strings.TrimPrefix(server.URL, "http://"); gotHost != want {
		t.Errorf("request host = %q, want %q", gotHost, want)
	}
}

func TestFetchFile_EmptyPath(t *testing.T) {
	client := &Client{Repo: "o/r"}
	if _, err := client.FetchFile(""); err == nil {
		t.Error("expected error for empty path, got nil")
	}
}

// A path that looks like a URL must stay a path: it is escaped into the
// contents route and can never redirect the request to another host.
func TestFetchFile_PathCannotRedirectRequest(t *testing.T) {
	var gotHost, gotPath string
	client, server := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost, gotPath = r.Host, r.URL.Path
		_, _ = w.Write([]byte("x"))
	}))

	if _, err := client.FetchFile("169.254.169.254/latest/meta-data"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := strings.TrimPrefix(server.URL, "http://"); gotHost != want {
		t.Errorf("request host = %q, want %q", gotHost, want)
	}
	if !strings.HasPrefix(gotPath, "/repos/test/repo/contents/") {
		t.Errorf("request path = %q, want it inside the contents route", gotPath)
	}
}

func TestListDirectory_MalformedJSON(t *testing.T) {
	client, _ := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json at all {{{"))
	}))

	if _, err := client.ListDirectory("defaults"); err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestPing(t *testing.T) {
	ok, _ := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := json.NewEncoder(w).Encode([]map[string]string{}); err != nil {
			t.Errorf("encode: %v", err)
		}
	}))
	if err := ok.Ping(); err != nil {
		t.Errorf("Ping() unexpected error: %v", err)
	}

	bad, _ := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	if err := bad.Ping(); err == nil {
		t.Error("expected error from Ping, got nil")
	}
}

func TestEndpointAndSubject(t *testing.T) {
	c := &Client{Repo: "owner/repo", Ref: "v1"}
	if c.Endpoint() != DefaultBaseURL {
		t.Errorf("Endpoint() = %q, want the default", c.Endpoint())
	}
	if c.Subject() != "owner/repo" || c.Reference() != "v1" {
		t.Errorf("Subject()/Reference() = %q/%q", c.Subject(), c.Reference())
	}
}
