package source

import (
	"net/http"
	"net/http/httptest"
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

	body, err := r.Get(r.Base()+"/anything", "application/json")
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
		if _, err := r.Get(u, "application/json"); err == nil {
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

			_, err := r.Get(r.Base()+"/anything", "application/json")
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
