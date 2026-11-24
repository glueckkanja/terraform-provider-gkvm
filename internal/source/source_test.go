package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fastRetries keeps the retry tests quick without changing the logic.
func fastRetries(t *testing.T) {
	t.Helper()
	base, max := retryBaseDelay, retryMaxDelay
	retryBaseDelay, retryMaxDelay = time.Millisecond, 2*time.Millisecond
	t.Cleanup(func() { retryBaseDelay, retryMaxDelay = base, max })
}

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

// A wrong endpoint is the most likely misconfiguration, and the transport says
// exactly what is wrong. Flattening that into "connection error" sent the user
// looking at their token instead.
func TestGet_ReportsTheTransportCauseWithoutTheURL(t *testing.T) {
	fastRetries(t)
	// A server that is closed immediately leaves a port nothing listens on.
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	client := closed.Client()
	base := closed.URL
	closed.Close()

	r := &Requester{
		Platform:   "GitHub",
		Subject:    "owner/repo",
		BaseURL:    base,
		Headers:    map[string]string{"Authorization": "Bearer super-secret-token"},
		HTTPClient: client,
	}

	_, err := r.Get(context.Background(), r.Base()+"/repos/owner/repo/contents", "application/json")
	if err == nil {
		t.Fatal("expected an error against a closed port, got nil")
	}
	if !strings.Contains(err.Error(), "refused") {
		t.Errorf("error does not name the transport cause: %v", err)
	}
	// The cause must not drag the request URL or the credential along.
	if strings.Contains(err.Error(), "/repos/owner/repo/contents") {
		t.Errorf("error echoes the request URL: %v", err)
	}
	if strings.Contains(err.Error(), "super-secret-token") {
		t.Errorf("credential leaked in error message: %v", err)
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
	fastRetries(t)
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

func TestGet_RetriesServerErrorsThenSucceeds(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	r := newTestRequester(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("payload"))
	}))

	body, err := r.Get(context.Background(), r.Base()+"/anything", "application/json")
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if string(body) != "payload" {
		t.Errorf("body = %q, want %q", body, "payload")
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("calls = %d, want 3", got)
	}
}

func TestGet_DoesNotRetryPermanentStatuses(t *testing.T) {
	fastRetries(t)
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		var calls atomic.Int32
		r := newTestRequester(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			http.Error(w, "nope", status)
		}))
		if _, err := r.Get(context.Background(), r.Base()+"/anything", "application/json"); err == nil {
			t.Fatalf("status %d: expected error", status)
		}
		if got := calls.Load(); got != 1 {
			t.Errorf("status %d: calls = %d, want 1", status, got)
		}
	}
}

func TestGet_RetriesThrottlingAndHonoursRetryAfter(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	r := newTestRequester(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))

	if _, err := r.Get(context.Background(), r.Base()+"/anything", "application/json"); err != nil {
		t.Fatalf("expected success after throttling, got %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("calls = %d, want 2", got)
	}
}

func TestGet_GivesUpAfterMaxAttempts(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	r := newTestRequester(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "boom", http.StatusBadGateway)
	}))

	_, err := r.Get(context.Background(), r.Base()+"/anything", "application/json")
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("expected the last 502 to be reported, got %v", err)
	}
	if got := calls.Load(); int(got) != maxAttempts {
		t.Errorf("calls = %d, want %d", got, maxAttempts)
	}
}

func TestGet_RetryStopsOnCancelledContext(t *testing.T) {
	retryBaseDelay, retryMaxDelay = time.Hour, time.Hour
	t.Cleanup(func() { retryBaseDelay, retryMaxDelay = 500*time.Millisecond, 4*time.Second })
	var calls atomic.Int32
	r := newTestRequester(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "boom", http.StatusServiceUnavailable)
	}))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for calls.Load() == 0 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	if _, err := r.Get(ctx, r.Base()+"/anything", "application/json"); err == nil {
		t.Fatal("expected a cancellation error")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("calls = %d, want 1 (the backoff must not outlive the context)", got)
	}
}

// header builds the response headers classifyThrottle reads.
func header(pairs map[string]string) http.Header {
	h := http.Header{}
	for name, value := range pairs {
		h.Set(name, value)
	}
	return h
}

func TestClassifyThrottle(t *testing.T) {
	// Far enough out that the reset path cannot be mistaken for the bound.
	soon := time.Now().Add(30 * time.Second).Unix()
	late := time.Now().Add(45 * time.Minute).Unix()

	tests := []struct {
		name        string
		status      int
		header      http.Header
		wantLimited bool
		wantWait    time.Duration // checked only when exact
		exactWait   bool
	}{
		{
			name:        "429 without any hint is throttling",
			status:      http.StatusTooManyRequests,
			header:      header(nil),
			wantLimited: true,
		},
		{
			name:        "429 with Retry-After uses it",
			status:      http.StatusTooManyRequests,
			header:      header(map[string]string{"Retry-After": "7"}),
			wantLimited: true,
			wantWait:    7 * time.Second,
			exactWait:   true,
		},
		// The case this exists for: only one kind of 403 is worth retrying.
		{
			name:        "403 with Retry-After is a rate limit",
			status:      http.StatusForbidden,
			header:      header(map[string]string{"Retry-After": "12"}),
			wantLimited: true,
			wantWait:    12 * time.Second,
			exactWait:   true,
		},
		{
			name:        "403 with an exhausted GitHub counter is a rate limit",
			status:      http.StatusForbidden,
			header:      header(map[string]string{"X-RateLimit-Remaining": "0"}),
			wantLimited: true,
		},
		{
			name:        "403 with an exhausted GitLab counter is a rate limit",
			status:      http.StatusForbidden,
			header:      header(map[string]string{"RateLimit-Remaining": "0"}),
			wantLimited: true,
		},
		{
			name:        "403 with requests left is a permission problem",
			status:      http.StatusForbidden,
			header:      header(map[string]string{"X-RateLimit-Remaining": "4231"}),
			wantLimited: false,
		},
		{
			name:        "bare 403 is a permission problem",
			status:      http.StatusForbidden,
			header:      header(nil),
			wantLimited: false,
		},
		{
			name:        "404 is never throttling",
			status:      http.StatusNotFound,
			header:      header(map[string]string{"Retry-After": "5"}),
			wantLimited: false,
		},
		{
			name:        "503 is handled as a server error, not a limit",
			status:      http.StatusServiceUnavailable,
			header:      header(map[string]string{"Retry-After": "5"}),
			wantLimited: false,
		},
		{
			name:        "a reset within the bound is waited out",
			status:      http.StatusForbidden,
			header:      header(map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(soon, 10)}),
			wantLimited: true,
		},
		{
			name:        "a reset far out is still throttling",
			status:      http.StatusTooManyRequests,
			header:      header(map[string]string{"X-RateLimit-Reset": strconv.FormatInt(late, 10)}),
			wantLimited: true,
		},
		{
			name:        "a long Retry-After is reported verbatim",
			status:      http.StatusForbidden,
			header:      header(map[string]string{"Retry-After": "600"}),
			wantLimited: true,
			wantWait:    600 * time.Second,
			exactWait:   true,
		},
		// A reset in the past is a skewed clock or a stale proxy header. Taking
		// it at face value would fire all five attempts within milliseconds at
		// an active limiter.
		{
			name:        "a reset already in the past backs off instead",
			status:      http.StatusTooManyRequests,
			header:      header(map[string]string{"X-RateLimit-Reset": strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10)}),
			wantLimited: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyThrottle(tt.status, tt.header, 1)
			if got.limited != tt.wantLimited {
				t.Errorf("limited = %v, want %v", got.limited, tt.wantLimited)
			}
			if tt.exactWait && got.wait != tt.wantWait {
				t.Errorf("wait = %v, want %v", got.wait, tt.wantWait)
			}
			if tt.wantLimited && !tt.exactWait && got.wait <= 0 {
				t.Errorf("wait = %v, want a positive delay", got.wait)
			}
		})
	}
}

func TestGet_RetriesSecondaryRateLimitThenSucceeds(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	r := newTestRequester(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			// A secondary rate limit: a 403 asking for a wait, not a denial.
			w.Header().Set("Retry-After", "0")
			http.Error(w, "You have exceeded a secondary rate limit", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))

	if _, err := r.Get(context.Background(), r.Base()+"/anything", "application/json"); err != nil {
		t.Fatalf("expected success after the limit lifted, got %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("calls = %d, want 2", got)
	}
}

func TestGet_DoesNotRetryALimitThatOutlastsTheRead(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	r := newTestRequester(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "1800")
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
	}))

	_, err := r.Get(context.Background(), r.Base()+"/anything", "application/json")
	if err == nil {
		t.Fatal("expected an error")
	}
	// One attempt, not five: the answer will not change inside the read.
	if got := calls.Load(); got != 1 {
		t.Errorf("calls = %d, want 1", got)
	}
	if !strings.Contains(err.Error(), "30m0s") {
		t.Errorf("error %q does not say how long the limit lasts", err)
	}
}

// A rate-limited 403 used to be reported as a permission problem. This pins
// the wording of both kinds so they cannot be confused again.
func TestGet_RateLimited403DoesNotBlameTheToken(t *testing.T) {
	fastRetries(t)

	limited := newTestRequester(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "1800")
		http.Error(w, "secondary rate limit", http.StatusForbidden)
	}))
	_, err := limited.Get(context.Background(), limited.Base()+"/anything", "application/json")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("rate-limited 403 error %q does not name the rate limit", err)
	}
	if strings.Contains(err.Error(), "token permissions") {
		t.Errorf("rate-limited 403 error %q blames the token", err)
	}

	denied := newTestRequester(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Resource not accessible by integration", http.StatusForbidden)
	}))
	_, err = denied.Get(context.Background(), denied.Base()+"/anything", "application/json")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "token permissions") {
		t.Errorf("permission 403 error %q no longer points at the token", err)
	}
	if strings.Contains(err.Error(), "rate limited") {
		t.Errorf("permission 403 error %q claims a rate limit", err)
	}
}

// The budget belongs to the read, not to the request: before this it was a
// per-attempt cap, so a read of 36 files could wait out the cap 36 times.
func TestRateLimitBudget_IsSharedAcrossRequests(t *testing.T) {
	ctx := WithRateLimitBudget(context.Background(), 10*time.Second)

	if !takeWait(ctx, 6*time.Second).fits {
		t.Fatal("first wait did not fit an empty budget")
	}
	state := takeWait(ctx, 6*time.Second)
	if state.fits {
		t.Error("second wait fit a budget with 4s left")
	}
	// The remainder is reported so a refusal can say which reason applied.
	if state.available != 4*time.Second {
		t.Errorf("available = %v, want 4s", state.available)
	}
	if !takeWait(ctx, 4*time.Second).fits {
		t.Error("a wait matching the remainder was refused")
	}
	if takeWait(ctx, time.Millisecond).fits {
		t.Error("a wait fit an exhausted budget")
	}
}

func TestRateLimitBudget_FallsBackToThePerRequestCap(t *testing.T) {
	// Without a budget in the context — a Ping, say — the per-attempt cap
	// applies so a lone request is still bounded.
	ctx := context.Background()
	if !takeWait(ctx, rateLimitWaitLimit).fits {
		t.Error("a wait at the cap was refused")
	}
	if takeWait(ctx, rateLimitWaitLimit+time.Second).fits {
		t.Error("a wait past the cap was allowed")
	}
}

// A read is bounded as a whole: once the budget is gone the next throttled
// request is reported rather than waited out.
func TestGet_StopsRetryingWhenTheReadBudgetIsSpent(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	r := newTestRequester(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "2")
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))

	// Three seconds covers one 2 s wait and no more.
	ctx := WithRateLimitBudget(context.Background(), 3*time.Second)
	start := time.Now()
	_, err := r.Get(ctx, r.Base()+"/anything", "application/json")
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("calls = %d, want 2 (one retry, then the budget is spent)", got)
	}
	// 3 s of budget, one 2 s wait: anything near the old per-request bound
	// (4 x 2 s) has to fail here, so the margin is small on purpose.
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("read took %v, want under 4s — the budget did not bound it", elapsed)
	}
	// 2 s was taken, 1 s is left, so the refusal names both numbers.
	if !strings.Contains(err.Error(), "more than the 1s this read may still wait") {
		t.Errorf("error %q does not say how much budget is left", err)
	}
}

// The last attempt must not promise a sixth one.
func TestGet_FinalRateLimitErrorDoesNotPromiseARetry(t *testing.T) {
	fastRetries(t)
	r := newTestRequester(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		http.Error(w, "secondary rate limit", http.StatusForbidden)
	}))

	_, err := r.Get(context.Background(), r.Base()+"/anything", "application/json")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "gave up after 5 attempts") {
		t.Errorf("error %q does not say it gave up", err)
	}
	if strings.Contains(err.Error(), "attempt 6") {
		t.Errorf("error %q promises an attempt that never happens", err)
	}
}

// A budget spent to the last second is a different thing to act on than a
// limit that is simply too long, so the two refusals read differently.
func TestGet_SaysWhenTheBudgetIsFullySpent(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	r := newTestRequester(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "2")
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))

	// Exactly one 2 s wait fits; the next request finds nothing left.
	ctx := WithRateLimitBudget(context.Background(), 2*time.Second)
	_, err := r.Get(ctx, r.Base()+"/anything", "application/json")
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("calls = %d, want 2", got)
	}
	if !strings.Contains(err.Error(), "already spent its waiting budget") {
		t.Errorf("error %q does not say the budget is spent", err)
	}
}

// The reachability probe runs once per provider instance, which is once per
// unit, so it must never sleep out a limit: it only answers "reachable".
func TestGet_AProbeWithNoBudgetFailsAtOnce(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	r := newTestRequester(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "20")
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))

	ctx := WithRateLimitBudget(context.Background(), 0)
	_, err := r.Get(ctx, r.Base()+"/anything", "application/json")
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("calls = %d, want 1", got)
	}
	if !strings.Contains(err.Error(), "does not wait a limit out") {
		t.Errorf("error %q does not explain that this check never waits", err)
	}
	// It must not claim an earlier request spent the budget: there was none.
	if strings.Contains(err.Error(), "already spent") {
		t.Errorf("error %q blames requests that never happened", err)
	}
}

// A limit asking for no wait is a free retry. Refusing it for want of budget
// would spend nothing and still fail — which is what the probe used to do.
func TestGet_TakesAFreeRetryEvenWithNoBudget(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	r := newTestRequester(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))

	ctx := WithRateLimitBudget(context.Background(), 0)
	if _, err := r.Get(ctx, r.Base()+"/anything", "application/json"); err != nil {
		t.Fatalf("a free retry was refused: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("calls = %d, want 2", got)
	}
}

// The probe budget clears a concurrency burst — many processes starting at
// once — without being able to sit out a real limit.
func TestProbeWaitBudget_CoversABurstButNotALimit(t *testing.T) {
	ctx := WithRateLimitBudget(context.Background(), ProbeWaitBudget)
	if !takeWait(ctx, 2*time.Second).fits {
		t.Error("a two second burst wait did not fit the probe budget")
	}
	if takeWait(ctx, 60*time.Second).fits {
		t.Error("a minute-long limit fit the probe budget")
	}
	if ProbeWaitBudget >= ReadWaitBudget {
		t.Errorf("ProbeWaitBudget %v is not smaller than ReadWaitBudget %v", ProbeWaitBudget, ReadWaitBudget)
	}
}

// The read budget is shared by requests that run concurrently — a profile read
// of many files does exactly that — so the bound has to hold across them.
func TestRateLimitBudget_HoldsUnderConcurrency(t *testing.T) {
	const (
		readers = 20
		share   = 100 * time.Millisecond
	)
	// Room for exactly five shares, asked for by twenty readers at once.
	ctx := WithRateLimitBudget(context.Background(), 5*share)

	var granted atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if takeWait(ctx, share).fits {
				granted.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := granted.Load(); got != 5 {
		t.Errorf("granted = %d, want exactly 5 — the budget was over- or under-spent", got)
	}
	if takeWait(ctx, time.Nanosecond).fits {
		t.Error("a wait fit a budget that twenty readers had just emptied")
	}
}

// Concurrent Gets sharing one budget must not each sleep the bound. This is the
// shape of a profile read: many requests, one limit.
func TestGet_ConcurrentReadsShareOneBudget(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	r := newTestRequester(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "1")
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))

	// Two seconds for the whole read, whatever the ten readers ask for.
	ctx := WithRateLimitBudget(context.Background(), 2*time.Second)
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.Get(ctx, r.Base()+"/anything", "application/json")
		}()
	}
	wg.Wait()

	// Two seconds of budget covers two 1 s waits in total. Without sharing,
	// ten readers would each take their own, so ~4 s of wall clock; the bound
	// is set just above the shared figure to catch that.
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("read took %v, want under 3s — the budget was not shared", elapsed)
	}
	t.Logf("10 concurrent readers, %d requests, %v elapsed", calls.Load(), time.Since(start).Round(time.Millisecond))
}

// A proxy that sends -1 for "no limit" must not turn every permission 403 into
// a rate limit — that is the mis-blame this classification exists to remove.
func TestClassifyThrottle_NegativeCounterIsNotExhausted(t *testing.T) {
	for _, value := range []string{"-1", "1"} {
		got := classifyThrottle(http.StatusForbidden, header(map[string]string{"X-RateLimit-Remaining": value}), 1)
		if got.limited {
			t.Errorf("remaining=%s was read as throttling", value)
		}
	}
	if got := classifyThrottle(http.StatusForbidden, header(map[string]string{"X-RateLimit-Remaining": "0"}), 1); !got.limited {
		t.Error("remaining=0 was not read as throttling")
	}
}
