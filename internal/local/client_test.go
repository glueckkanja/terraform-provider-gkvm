package local

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/glueckkanja/terraform-provider-gkvm/internal/source"
)

// newDir builds a directory tree from a path-to-content map. Keys are slash
// separated so the tests read like repository paths.
func newDir(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatalf("mkdir for %s: %v", path, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	// t.TempDir can sit under a symlink (/var on macOS); resolve for compares.
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolving temp dir: %v", err)
	}
	return resolved
}

func TestClient_ListsAndFetches(t *testing.T) {
	root := newDir(t, map[string]string{
		"defaults/vm.yaml":          "metric_alerts: {}\n",
		"defaults/storage.yaml":     "log_alerts: {}\n",
		"defaults/nested/deep.yaml": "metric_alerts: {}\n",
		"other/ignored.yaml":        "metric_alerts: {}\n",
	})
	c := &Client{Dir: root, Repository: "owner/repo", Ref: "main"}

	if err := c.ValidateConfig(); err != nil {
		t.Fatalf("ValidateConfig: %v", err)
	}
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	entries, err := c.ListDirectory(context.Background(), "defaults")
	if err != nil {
		t.Fatalf("ListDirectory: %v", err)
	}

	got := map[string]bool{}
	for _, e := range entries {
		got[e.Path] = e.IsDir
	}
	want := map[string]bool{
		"defaults/nested":       true,
		"defaults/storage.yaml": false,
		"defaults/vm.yaml":      false,
	}
	if len(got) != len(want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	for path, isDir := range want {
		dir, ok := got[path]
		if !ok {
			t.Errorf("listing is missing %s", path)
			continue
		}
		if dir != isDir {
			t.Errorf("%s IsDir = %v, want %v", path, dir, isDir)
		}
	}

	// The paths a listing reports must be the paths FetchFile accepts.
	body, err := c.FetchFile(context.Background(), "defaults/vm.yaml")
	if err != nil {
		t.Fatalf("FetchFile: %v", err)
	}
	if string(body) != "metric_alerts: {}\n" {
		t.Errorf("content = %q", body)
	}
}

func TestClient_ListsTheRootOnAnEmptyPath(t *testing.T) {
	root := newDir(t, map[string]string{"top.yaml": "a: 1\n"})
	c := &Client{Dir: root}

	entries, err := c.ListDirectory(context.Background(), "")
	if err != nil {
		t.Fatalf("ListDirectory: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "top.yaml" || entries[0].Name != "top.yaml" {
		t.Fatalf("entries = %+v, want one entry at top.yaml", entries)
	}
}

func TestClient_IdentityForErrorMessages(t *testing.T) {
	root := newDir(t, nil)

	withRepo := &Client{Dir: root, Repository: "owner/repo", Ref: "v1"}
	if got := withRepo.Subject(); got != "owner/repo" {
		t.Errorf("Subject() = %q, want the configured repository", got)
	}
	if got := withRepo.Reference(); got != "v1" {
		t.Errorf("Reference() = %q, want v1", got)
	}
	if got := withRepo.Endpoint(); got != root {
		t.Errorf("Endpoint() = %q, want the directory", got)
	}

	// Without a repository there is still something useful to name.
	bare := &Client{Dir: root}
	if got := bare.Subject(); got != root {
		t.Errorf("Subject() = %q, want the directory", got)
	}
}

func TestClient_RejectsBadConfiguration(t *testing.T) {
	tests := []struct {
		name       string
		client     *Client
		wantSubstr string
	}{
		{"empty directory", &Client{Dir: ""}, "must not be empty"},
		{"blank directory", &Client{Dir: "   "}, "must not be empty"},
		{"relative directory", &Client{Dir: filepath.Join("relative", "path")}, "absolute path"},
		{"ref with a query separator", &Client{Dir: string(filepath.Separator) + "tmp", Ref: "main?x=1"}, "invalid ref"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.client.ValidateConfig()
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Errorf("error %q does not mention %q", err, tt.wantSubstr)
			}
		})
	}
}

func TestClient_PingReportsWhatIsWrong(t *testing.T) {
	root := newDir(t, map[string]string{"file.yaml": "a: 1\n"})

	missing := &Client{Dir: filepath.Join(root, "nope")}
	if err := missing.Ping(context.Background()); err == nil || !strings.Contains(err.Error(), "cannot be read") {
		t.Fatalf("missing directory: got %v", err)
	}

	notADir := &Client{Dir: filepath.Join(root, "file.yaml")}
	if err := notADir.Ping(context.Background()); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("file instead of directory: got %v", err)
	}
}

func TestClient_RefusesPathTraversal(t *testing.T) {
	root := newDir(t, map[string]string{"defaults/vm.yaml": "a: 1\n"})
	// A sibling of the root, so a successful escape would actually read it.
	secret := filepath.Join(filepath.Dir(root), "secret.yaml")
	if err := os.WriteFile(secret, []byte("stolen: true\n"), 0o600); err != nil {
		t.Fatalf("write sibling: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(secret) })

	c := &Client{Dir: root}
	for _, path := range []string{
		"../secret.yaml",
		"defaults/../../secret.yaml",
		"/etc/hosts",
		"..",
	} {
		if _, err := c.FetchFile(context.Background(), path); err == nil {
			t.Errorf("FetchFile(%q) was allowed", path)
		}
		if _, err := c.ListDirectory(context.Background(), path); err == nil {
			t.Errorf("ListDirectory(%q) was allowed", path)
		}
	}
}

func TestClient_RefusesSymlinkOutOfTheDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}

	root := newDir(t, map[string]string{"defaults/vm.yaml": "a: 1\n"})
	outside := filepath.Join(filepath.Dir(root), "outside.yaml")
	if err := os.WriteFile(outside, []byte("stolen: true\n"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	// Every path element is harmless; only the target reaches out.
	link := filepath.Join(root, "defaults", "escape.yaml")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	c := &Client{Dir: root}
	body, err := c.FetchFile(context.Background(), "defaults/escape.yaml")
	if err == nil {
		t.Fatalf("the escaping symlink was followed and returned %q", body)
	}
	if !strings.Contains(err.Error(), "outside the directory") {
		t.Errorf("error %q does not say the path left the directory", err)
	}
}

func TestClient_FollowsSymlinkInsideTheDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}

	root := newDir(t, map[string]string{"real/vm.yaml": "metric_alerts: {}\n"})
	link := filepath.Join(root, "defaults")
	if err := os.Symlink(filepath.Join(root, "real"), link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	c := &Client{Dir: root}
	// A bundle may assemble its tree with links; one staying inside is fine.
	body, err := c.FetchFile(context.Background(), "defaults/vm.yaml")
	if err != nil {
		t.Fatalf("FetchFile through an internal symlink: %v", err)
	}
	if string(body) != "metric_alerts: {}\n" {
		t.Errorf("content = %q", body)
	}
}

func TestClient_ReportsMissingAndWrongKind(t *testing.T) {
	root := newDir(t, map[string]string{"defaults/vm.yaml": "a: 1\n"})
	c := &Client{Dir: root}

	if _, err := c.FetchFile(context.Background(), "defaults/absent.yaml"); err == nil {
		t.Error("a missing file was not reported")
	}
	if _, err := c.FetchFile(context.Background(), "defaults"); err == nil {
		t.Error("a directory was returned as a file")
	}
	if _, err := c.ListDirectory(context.Background(), "defaults/vm.yaml"); err == nil {
		t.Error("a file was listed as a directory")
	}
}

func TestClient_EnforcesTheSizeCap(t *testing.T) {
	root := t.TempDir()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolving temp dir: %v", err)
	}
	big := filepath.Join(resolved, "big.yaml")
	if err := os.WriteFile(big, make([]byte, source.MaxResponseBytes+1), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	c := &Client{Dir: resolved}
	_, err = c.FetchFile(context.Background(), "big.yaml")
	if err == nil {
		t.Fatal("an oversized file was read")
	}
	// Truncated YAML often still parses, so the read refuses instead.
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error %q does not name the limit", err)
	}
}

func TestClient_SatisfiesTheSourceInterface(t *testing.T) {
	var c source.Client = &Client{Dir: string(filepath.Separator) + "tmp"}
	if c.Reference() != "" {
		t.Errorf("Reference() = %q, want empty", c.Reference())
	}
}

// Unit tested on its own: ValidatePath rejects every traversal first, so no
// input through resolve reaches this guard. A guard with no test is lost quietly.
func TestContained(t *testing.T) {
	root := string(filepath.Separator) + filepath.Join("srv", "bundle")

	for _, target := range []string{
		root,
		filepath.Join(root, "defaults"),
		filepath.Join(root, "defaults", "vm.yaml"),
	} {
		if err := contained(root, target); err != nil {
			t.Errorf("contained(%q) = %v, want nil", target, err)
		}
	}

	for _, target := range []string{
		filepath.Dir(root),
		filepath.Join(filepath.Dir(root), "other"),
		string(filepath.Separator) + "etc",
	} {
		if err := contained(root, target); err == nil {
			t.Errorf("contained(%q) was allowed", target)
		}
	}

	// A sibling sharing the root's name prefix is outside it.
	if err := contained(root, root+"-other"); err == nil {
		t.Error("a sibling sharing the root's prefix was allowed")
	}
}

// A non-regular file would otherwise be opened like any other: a FIFO blocks
// the read forever, and the plan cannot be interrupted.
func TestClient_RefusesANonRegularFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no FIFOs on Windows")
	}

	root := newDir(t, map[string]string{"defaults/vm.yaml": "a: 1\n"})
	fifo := filepath.Join(root, "defaults", "pipe.yaml")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("cannot create a FIFO here: %v", err)
	}

	c := &Client{Dir: root}
	// A listing reports it like any other entry, so the refusal has to happen
	// on the read.
	done := make(chan error, 1)
	go func() {
		_, err := c.FetchFile(context.Background(), "defaults/pipe.yaml")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a FIFO was read as a profile")
		}
		if !strings.Contains(err.Error(), "named pipe") {
			t.Errorf("error %q does not name what it found", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("FetchFile blocked on a FIFO")
	}
}

// EvalSymlinks fails for reasons other than non-existence. Treating them all
// as "not there" would skip the containment check that follows it.
func TestClient_ReportsAnUnresolvablePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}

	root := newDir(t, map[string]string{"defaults/vm.yaml": "a: 1\n"})
	loop := filepath.Join(root, "defaults", "loop.yaml")
	if err := os.Symlink("loop.yaml", loop); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	c := &Client{Dir: root}
	_, err := c.FetchFile(context.Background(), "defaults/loop.yaml")
	if err == nil {
		t.Fatal("a symlink loop was read")
	}
	if !strings.Contains(err.Error(), "cannot be resolved") {
		t.Errorf("error %q does not report an unresolvable path", err)
	}
}
