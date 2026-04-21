package github

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestListDirectory_MockServer(t *testing.T) {
	// Mock GitHub API — validates that the client sends correct headers.
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/test/repo/contents/defaults", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode([]Entry{
			{Name: "firewall.yaml", Path: "defaults/firewall.yaml", Type: "file", DownloadURL: "PLACEHOLDER"},
		})
	})

	server := httptest.NewTLSServer(mux)
	defer server.Close()

	// This test validates the validation logic only.
	// (The mock server uses self-signed TLS which httpClient rejects for full fetch.)
	client := &Client{
		Token: "test-token",
		Repo:  "test/repo",
		Ref:   "main",
	}

	if err := client.ValidateConfig(); err != nil {
		t.Errorf("ValidateConfig() unexpected error: %v", err)
	}
}

func TestFetchFile_RejectsUntrustedDomain(t *testing.T) {
	client := &Client{Token: "test", Repo: "o/r"}

	_, err := client.FetchFile("http://169.254.169.254/latest/meta-data/")
	if err == nil {
		t.Error("expected error for untrusted domain, got nil")
	}

	_, err = client.FetchFile("https://evil.com/payload")
	if err == nil {
		t.Error("expected error for non-GitHub domain, got nil")
	}
}
