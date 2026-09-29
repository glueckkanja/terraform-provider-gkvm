package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
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
		{"empty defaults to gitlab.com", "", DefaultBaseURL},
		{"self managed gets api/v4", "https://gitlab.example.com", "https://gitlab.example.com/api/v4"},
		{"trailing slash trimmed", "https://gitlab.example.com/", "https://gitlab.example.com/api/v4"},
		{"explicit path kept", "https://gitlab.example.com/api/v4", "https://gitlab.example.com/api/v4"},
		{"subpath install kept", "https://example.com/gitlab/api/v4", "https://example.com/gitlab/api/v4"},
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
	if got := CLIHost(""); got != "gitlab.com" {
		t.Errorf("CLIHost(\"\") = %q", got)
	}
	if got := CLIHost("https://gitlab.example.com/api/v4"); got != "gitlab.example.com" {
		t.Errorf("CLIHost() = %q", got)
	}
	// A port belongs in the request, never in the CLI hostname.
	if got := CLIHost("https://gitlab.example.com:8443/api/v4"); got != "gitlab.example.com" {
		t.Errorf("CLIHost() with a port = %q, want the bare hostname", got)
	}
}

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name    string
		client  Client
		wantErr bool
	}{
		{"group and project", Client{Project: "group/project", Ref: "main"}, false},
		{"nested subgroups", Client{Project: "group/sub/deeper/project", Ref: "main"}, false},
		{"dots and dashes", Client{Project: "my.group/my-project_v2", Ref: "v1.0.0"}, false},
		{"valid endpoint", Client{Project: "group/project", BaseURL: "https://gitlab.example.com/api/v4"}, false},
		{"no namespace", Client{Project: "project"}, true},
		{"spaces", Client{Project: "group/ project"}, true},
		{"traversal", Client{Project: "../etc/passwd"}, true},
		{"bad ref", Client{Project: "group/project", Ref: "main&x=1"}, true},
		{"http endpoint", Client{Project: "group/project", BaseURL: "http://gitlab.example.com"}, true},
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

func newMockClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client := &Client{
		Token:      "test-token",
		Project:    "group/sub/project",
		Ref:        "main",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	}
	return client, server
}

func TestListDirectory_EncodesProjectAndSendsPrivateToken(t *testing.T) {
	var gotEscapedPath, gotQuery, gotPrivate, gotJob string

	client, _ := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEscapedPath, gotQuery = r.URL.EscapedPath(), r.URL.Query().Encode()
		gotPrivate, gotJob = r.Header.Get(PrivateTokenHeader), r.Header.Get(JobTokenHeader)

		payload := []map[string]string{
			{"name": "firewall.yaml", "path": "defaults/firewall.yaml", "type": "blob"},
			{"name": "subdir", "path": "defaults/subdir", "type": "tree"},
		}
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Errorf("encode: %v", err)
		}
	}))

	entries, err := client.ListDirectory(context.Background(), "defaults")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 || entries[0].IsDir || !entries[1].IsDir {
		t.Errorf("entries = %+v, want one blob and one tree", entries)
	}
	// GitLab addresses a project as one URL-encoded path parameter.
	if want := "/projects/group%2Fsub%2Fproject/repository/tree"; gotEscapedPath != want {
		t.Errorf("request path = %q, want %q", gotEscapedPath, want)
	}
	if !strings.Contains(gotQuery, "ref=main") || !strings.Contains(gotQuery, "path=defaults") {
		t.Errorf("request query = %q", gotQuery)
	}
	if !strings.Contains(gotQuery, "per_page=100") {
		t.Errorf("request query = %q, want an explicit page size", gotQuery)
	}
	if gotPrivate != "test-token" {
		t.Errorf("%s = %q, want the configured token", PrivateTokenHeader, gotPrivate)
	}
	if gotJob != "" {
		t.Errorf("%s = %q, want it unset for a private token", JobTokenHeader, gotJob)
	}
}

// GitLab paginates the tree endpoint at 20 entries by default, which would
// silently truncate a profile directory. Every page must be read.
func TestListDirectory_ReadsEveryPage(t *testing.T) {
	var pagesServed int

	client, _ := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pagesServed++
		page := r.URL.Query().Get("page")

		var payload []map[string]string
		switch page {
		case "1":
			for i := 0; i < treePageSize; i++ {
				name := fmt.Sprintf("profile%03d.yaml", i)
				payload = append(payload, map[string]string{"name": name, "path": "defaults/" + name, "type": "blob"})
			}
		case "2":
			payload = []map[string]string{
				{"name": "last.yaml", "path": "defaults/last.yaml", "type": "blob"},
			}
		default:
			t.Errorf("unexpected page %q", page)
		}

		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Errorf("encode: %v", err)
		}
	}))

	entries, err := client.ListDirectory(context.Background(), "defaults")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != treePageSize+1 {
		t.Errorf("got %d entries, want %d", len(entries), treePageSize+1)
	}
	if pagesServed != 2 {
		t.Errorf("served %d pages, want 2", pagesServed)
	}
}

func TestFetchFile_EncodesPathAndStaysOnEndpoint(t *testing.T) {
	var gotEscapedPath, gotRef, gotHost, gotAccept string

	client, server := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEscapedPath, gotRef, gotHost = r.URL.EscapedPath(), r.URL.Query().Get("ref"), r.Host
		gotAccept = r.Header.Get("Accept")
		_, _ = w.Write([]byte("metric_alerts: {}\n"))
	}))

	content, err := client.FetchFile(context.Background(), "defaults/firewall.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(content), "metric_alerts") {
		t.Errorf("content = %q", content)
	}
	if want := "/projects/group%2Fsub%2Fproject/repository/files/defaults%2Ffirewall.yaml/raw"; gotEscapedPath != want {
		t.Errorf("request path = %q, want %q", gotEscapedPath, want)
	}
	if gotRef != "main" {
		t.Errorf("ref = %q", gotRef)
	}
	if want := strings.TrimPrefix(server.URL, "http://"); gotHost != want {
		t.Errorf("request host = %q, want %q", gotHost, want)
	}
	// The raw endpoint returns file bytes, so the request must not claim JSON.
	if gotAccept != "*/*" {
		t.Errorf("Accept = %q, want */*", gotAccept)
	}
}

func TestFetchFile_JobTokenUsesItsOwnHeader(t *testing.T) {
	var gotPrivate, gotJob string

	client, _ := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPrivate, gotJob = r.Header.Get(PrivateTokenHeader), r.Header.Get(JobTokenHeader)
		_, _ = w.Write([]byte("x"))
	}))
	client.TokenHeader = JobTokenHeader

	if _, err := client.FetchFile(context.Background(), "defaults/firewall.yaml"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotJob != "test-token" {
		t.Errorf("%s = %q, want the job token", JobTokenHeader, gotJob)
	}
	if gotPrivate != "" {
		t.Errorf("%s = %q, want it unset for a job token", PrivateTokenHeader, gotPrivate)
	}
}

func TestFetchFile_EmptyPath(t *testing.T) {
	client := &Client{Project: "group/project"}
	if _, err := client.FetchFile(context.Background(), ""); err == nil {
		t.Error("expected error for empty path, got nil")
	}
}

func TestPing(t *testing.T) {
	bad, _ := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	if err := bad.Ping(context.Background()); err == nil {
		t.Error("expected error from Ping, got nil")
	}
}

func TestEndpointSubjectReference(t *testing.T) {
	c := &Client{Project: "group/project", Ref: "v1"}
	if c.Endpoint() != DefaultBaseURL {
		t.Errorf("Endpoint() = %q, want the default", c.Endpoint())
	}
	if c.Subject() != "group/project" || c.Reference() != "v1" {
		t.Errorf("Subject()/Reference() = %q/%q", c.Subject(), c.Reference())
	}
}
