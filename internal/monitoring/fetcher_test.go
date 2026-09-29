package monitoring

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/glueckkanja/terraform-provider-gkvm/internal/source"
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

// fakeClient is a source.Client backed by an in-memory repository, so the
// fetcher is exercised independently of any backend's URL shapes.
type fakeClient struct {
	entries   []source.Entry
	files     map[string][]byte
	listErr   error
	fetchErr  error
	fetchedAt []string
}

func (f *fakeClient) Endpoint() string  { return "https://api.example.test" }
func (f *fakeClient) Subject() string   { return "test/repo" }
func (f *fakeClient) Reference() string { return "main" }
func (f *fakeClient) Ping() error       { return f.listErr }

func (f *fakeClient) ListDirectory(string) ([]source.Entry, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.entries, nil
}

func (f *fakeClient) FetchFile(path string) ([]byte, error) {
	f.fetchedAt = append(f.fetchedAt, path)
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	content, ok := f.files[path]
	if !ok {
		return nil, fmt.Errorf("no such file %q", path)
	}
	return content, nil
}

func TestFetchProfiles_ParsesYAMLAndSkipsNonProfiles(t *testing.T) {
	client := &fakeClient{
		entries: []source.Entry{
			{Name: "firewall.yaml", Path: "defaults/firewall.yaml"},
			{Name: "README.md", Path: "defaults/README.md"},
			{Name: "subdir", Path: "defaults/subdir", IsDir: true},
			{Name: "empty.yaml", Path: "defaults/empty.yaml"},
		},
		files: map[string][]byte{
			"defaults/firewall.yaml": []byte("metric_alerts:\n  deny_rate:\n    threshold: 10\n"),
			"defaults/empty.yaml":    []byte("{}\n"),
		},
	}

	profiles, err := FetchProfiles(client, "defaults")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2: %v", len(profiles), profiles)
	}

	var firewall struct {
		MetricAlerts map[string]any `json:"metric_alerts"`
		LogAlerts    map[string]any `json:"log_alerts"`
	}
	if err := json.Unmarshal([]byte(profiles["firewall"]), &firewall); err != nil {
		t.Fatalf("profile is not valid JSON: %v", err)
	}
	if len(firewall.MetricAlerts) != 1 {
		t.Errorf("metric_alerts = %v, want one rule", firewall.MetricAlerts)
	}
	// Absent sections are normalized to empty objects so consumers can index them.
	if firewall.LogAlerts == nil {
		t.Error("log_alerts = null, want an empty object")
	}

	// Files are addressed by repository path, never by a URL from the listing.
	for _, path := range client.fetchedAt {
		if !strings.HasPrefix(path, "defaults/") {
			t.Errorf("fetched %q, want a repository path", path)
		}
	}
}

// An entry without a path still resolves: the directory and name are enough.
func TestFetchProfiles_FallsBackToDirectoryAndName(t *testing.T) {
	client := &fakeClient{
		entries: []source.Entry{{Name: "firewall.yaml"}},
		files:   map[string][]byte{"defaults/firewall.yaml": []byte("{}\n")},
	}

	if _, err := FetchProfiles(client, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(client.fetchedAt) != 1 || client.fetchedAt[0] != "defaults/firewall.yaml" {
		t.Errorf("fetched %v, want defaults/firewall.yaml", client.fetchedAt)
	}
}

func TestFetchProfiles_InvalidPath(t *testing.T) {
	if _, err := FetchProfiles(&fakeClient{}, "../etc"); err == nil {
		t.Fatal("expected error for invalid path, got nil")
	}
}

func TestFetchProfiles_ListDirectoryError(t *testing.T) {
	client := &fakeClient{listErr: fmt.Errorf("HTTP %d", http.StatusNotFound)}

	_, err := FetchProfiles(client, "")
	if err == nil {
		t.Fatal("expected error when directory listing fails, got nil")
	}
	if !strings.Contains(err.Error(), "listing profiles directory") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestFetchProfiles_FetchError(t *testing.T) {
	client := &fakeClient{
		entries:  []source.Entry{{Name: "firewall.yaml", Path: "defaults/firewall.yaml"}},
		fetchErr: fmt.Errorf("HTTP %d", http.StatusForbidden),
	}

	_, err := FetchProfiles(client, "")
	if err == nil || !strings.Contains(err.Error(), "fetching profile firewall") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFetchProfiles_MalformedYAML(t *testing.T) {
	client := &fakeClient{
		entries: []source.Entry{{Name: "firewall.yaml", Path: "defaults/firewall.yaml"}},
		files:   map[string][]byte{"defaults/firewall.yaml": []byte("metric_alerts: [unclosed\n")},
	}

	_, err := FetchProfiles(client, "")
	if err == nil || !strings.Contains(err.Error(), "parsing profile firewall") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFetchProfiles_EmptyDirectory(t *testing.T) {
	client := &fakeClient{
		entries: []source.Entry{{Name: "README.md", Path: "defaults/README.md"}},
	}

	_, err := FetchProfiles(client, "")
	if err == nil {
		t.Fatal("expected error for a directory without profiles, got nil")
	}
	// The message has to name the endpoint and ref: a wrong ref or a wrong
	// endpoint is the usual cause, and both are invisible otherwise.
	for _, want := range []string{"no YAML profiles found", "main", "https://api.example.test"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}
