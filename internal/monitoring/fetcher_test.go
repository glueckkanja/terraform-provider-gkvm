package monitoring

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glueckkanja/terraform-provider-gkvm/internal/github"
)

func TestValidatePath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"empty", "", false},
		{"valid simple", "defaults", false},
		{"valid nested", "configs/monitoring", false},
		{"traversal", "../secrets", true},
		{"absolute", "/etc/passwd", true},
		{"traversal nested", "a/../b", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePath(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePath(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
		})
	}
}

// newTestClient creates a github.Client wired to a plain-HTTP test server.
func newTestClient(t *testing.T, handler http.Handler) (*github.Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client := &github.Client{
		Repo:           "test/repo",
		Ref:            "main",
		TestBaseURL:    server.URL,
		TestHTTPClient: server.Client(),
	}
	return client, server
}

func TestFetchProfiles_InvalidPath(t *testing.T) {
	client := &github.Client{Repo: "o/r"}
	_, err := FetchProfiles(client, "../etc")
	if err == nil {
		t.Fatal("expected error for invalid path, got nil")
	}
}

func TestFetchProfiles_ListDirectoryError(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))

	_, err := FetchProfiles(client, "")
	if err == nil {
		t.Fatal("expected error when directory listing fails, got nil")
	}
	if !strings.Contains(err.Error(), "listing profiles directory") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestFetchProfiles_EmptyDirectory(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No YAML files — only a README
		entries := []map[string]string{
			{"name": "README.md", "type": "file", "download_url": ""},
		}
		if err := json.NewEncoder(w).Encode(entries); err != nil {
			t.Errorf("encode: %v", err)
		}
	}))

	_, err := FetchProfiles(client, "")
	if err == nil {
		t.Fatal("expected error for empty YAML directory, got nil")
	}
	if !strings.Contains(err.Error(), "no YAML profiles found") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestFetchProfiles_MissingDownloadURL(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entries := []map[string]string{
			{"name": "firewall.yaml", "type": "file", "download_url": ""},
		}
		if err := json.NewEncoder(w).Encode(entries); err != nil {
			t.Errorf("encode: %v", err)
		}
	}))

	_, err := FetchProfiles(client, "")
	if err == nil {
		t.Fatal("expected error for missing download_url, got nil")
	}
	if !strings.Contains(err.Error(), "no download_url") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestFetchProfiles_SSRFGuardOnDownloadURL(t *testing.T) {
	// The directory listing returns a download_url pointing to our test server
	// (not a trusted GitHub domain). FetchFile must reject it.
	var serverURL string
	client, server := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/contents/"):
			downloadURL := fmt.Sprintf("%s/files/firewall.yaml", serverURL)
			entries := []map[string]string{
				{"name": "firewall.yaml", "type": "file", "download_url": downloadURL},
			}
			if err := json.NewEncoder(w).Encode(entries); err != nil {
				t.Errorf("encode: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	serverURL = server.URL

	_, err := FetchProfiles(client, "")
	if err == nil {
		t.Fatal("expected SSRF guard error, got nil")
	}
	if !strings.Contains(err.Error(), "untrusted download_url") {
		t.Errorf("expected SSRF guard message, got: %v", err)
	}
}
