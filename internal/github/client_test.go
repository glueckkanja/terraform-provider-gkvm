package github

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name    string
		client  Client
		wantErr bool
	}{
		{"valid", Client{Repo: "owner/repo", Ref: "main"}, false},
		{"valid with dots", Client{Repo: "my.org/my-repo_v2", Ref: "v1.0.0"}, false},
		{"valid sha", Client{Repo: "owner/repo", Ref: "abc123def456"}, false},
		{"invalid repo spaces", Client{Repo: "owner/ repo"}, true},
		{"invalid repo traversal", Client{Repo: "../etc/passwd"}, true},
		{"invalid ref newline", Client{Repo: "owner/repo", Ref: "main\ninjection"}, true},
		{"invalid ref query", Client{Repo: "owner/repo", Ref: "main&foo=bar"}, true},
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

func TestFetchFile_RejectsUntrustedDomain(t *testing.T) {
	client := &Client{Token: "test", Repo: "o/r"}

	_, err := client.FetchFile("http://169.254.169.254/latest/meta-data/")
	if err == nil {
		t.Error("expected error for metadata endpoint, got nil")
	}

	_, err = client.FetchFile("https://evil.com/payload")
	if err == nil {
		t.Error("expected error for non-GitHub domain, got nil")
	}

	_, err = client.FetchFile("https://api.github.com.evil.com/steal")
	if err == nil {
		t.Error("expected error for domain spoofing attempt, got nil")
	}
}

// newMockClient creates a Client wired to a test HTTP server.
// The test server uses plain HTTP; TestHTTPClient is replaced with the server's
// own client so no TLS is required.
func newMockClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client := &Client{
		Token:          "test-token",
		Repo:           "test/repo",
		Ref:            "main",
		TestBaseURL:    server.URL,
		TestHTTPClient: server.Client(),
	}
	return client, server
}

func TestListDirectory_Success(t *testing.T) {
	want := []Entry{
		{Name: "firewall.yaml", Path: "defaults/firewall.yaml", Type: "file", DownloadURL: "https://raw.githubusercontent.com/test/repo/main/defaults/firewall.yaml"},
		{Name: "subdir", Path: "defaults/subdir", Type: "dir", DownloadURL: ""},
	}

	client, _ := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Auth header is not sent to non-GitHub domains (by design); don't check it here.
		if err := json.NewEncoder(w).Encode(want); err != nil {
			t.Errorf("encode: %v", err)
		}
	}))

	entries, err := client.ListDirectory("defaults")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("got %d entries, want 2", len(entries))
	}
	if entries[0].Name != "firewall.yaml" {
		t.Errorf("got name %q, want firewall.yaml", entries[0].Name)
	}
}

func TestListDirectory_HTTPErrors(t *testing.T) {
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
			client, _ := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "error", tt.statusCode)
			}))

			_, err := client.ListDirectory("defaults")
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantSubstr)
			}
		})
	}
}

func TestListDirectory_MalformedJSON(t *testing.T) {
	client, _ := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json at all {{{"))
	}))

	_, err := client.ListDirectory("defaults")
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestListDirectory_TokenNotSentToNonGitHubServer(t *testing.T) {
	// isGitHubDomain must return false for the test server URL, so the
	// Authorization header must be absent (SSRF token-leak prevention).
	client, _ := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			http.Error(w, "token should not be sent to non-GitHub host", http.StatusInternalServerError)
			return
		}
		if err := json.NewEncoder(w).Encode([]Entry{}); err != nil {
			t.Errorf("encode: %v", err)
		}
	}))

	// Empty result triggers "no YAML profiles found" via FetchProfiles, but here
	// we just confirm ListDirectory itself doesn't error (and didn't send the token).
	_, err := client.ListDirectory("defaults")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDoRequest_TokenNotLeakedInError(t *testing.T) {
	client, _ := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	client.Token = "super-secret-token"

	_, err := client.ListDirectory("defaults")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if strings.Contains(err.Error(), "super-secret-token") {
		t.Errorf("token leaked in error message: %v", err)
	}
}

func TestPing_Success(t *testing.T) {
	client, _ := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewEncoder(w).Encode([]Entry{}); err != nil {
			t.Errorf("encode: %v", err)
		}
	}))

	if err := client.Ping(); err != nil {
		t.Errorf("Ping() unexpected error: %v", err)
	}
}

func TestPing_Failure(t *testing.T) {
	client, _ := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))

	if err := client.Ping(); err == nil {
		t.Error("expected error from Ping, got nil")
	}
}
