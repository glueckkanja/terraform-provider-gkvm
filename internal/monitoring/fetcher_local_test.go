package monitoring_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glueckkanja/terraform-provider-gkvm/internal/local"
	"github.com/glueckkanja/terraform-provider-gkvm/internal/monitoring"
)

// The whole read path over the real local backend, not a stub: it proves a
// directory on disk satisfies the contract the fetcher expects.
func TestFetchProfiles_OverTheLocalBackend(t *testing.T) {
	root := t.TempDir()
	write(t, root, "defaults/vm.yaml", `
metric_alerts:
  cpu:
    threshold: 80
log_alerts:
  heartbeat:
    severity: 2
`)
	write(t, root, "defaults/storage.yaml", `
metric_alerts:
  capacity:
    threshold: 90
`)
	// All skipped: .yaml in the named directory and nothing else.
	write(t, root, "defaults/README.md", "not a profile\n")
	write(t, root, "defaults/notes.txt", "not a profile\n")
	write(t, root, "defaults/nested/deep.yaml", "metric_alerts: {}\n")
	write(t, root, "other/elsewhere.yaml", "metric_alerts: {}\n")

	client := &local.Client{Dir: root, Repository: "owner/repo", Ref: "main"}
	profiles, err := monitoring.FetchProfiles(context.Background(), client, "defaults")
	if err != nil {
		t.Fatalf("FetchProfiles: %v", err)
	}

	if len(profiles) != 2 {
		t.Fatalf("profiles = %v, want vm and storage only", keys(profiles))
	}
	for _, name := range []string{"vm", "storage"} {
		if _, ok := profiles[name]; !ok {
			t.Errorf("profile %q is missing", name)
		}
	}

	// The data source hands these to jsondecode(), so the JSON shape is part
	// of the contract.
	var vm struct {
		MetricAlerts map[string]struct {
			Threshold int `json:"threshold"`
		} `json:"metric_alerts"`
		LogAlerts map[string]struct {
			Severity int `json:"severity"`
		} `json:"log_alerts"`
	}
	if err := json.Unmarshal([]byte(profiles["vm"]), &vm); err != nil {
		t.Fatalf("profile vm is not valid JSON: %v", err)
	}
	if vm.MetricAlerts["cpu"].Threshold != 80 {
		t.Errorf("vm metric_alerts.cpu.threshold = %d, want 80", vm.MetricAlerts["cpu"].Threshold)
	}
	if vm.LogAlerts["heartbeat"].Severity != 2 {
		t.Errorf("vm log_alerts.heartbeat.severity = %d, want 2", vm.LogAlerts["heartbeat"].Severity)
	}
}

// A directory that exists but shipped nothing: a silent empty result would
// drop every default alert, and a missing alert is invisible in a plan.
func TestFetchProfiles_EmptyLocalDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "defaults"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	_, err := monitoring.FetchProfiles(context.Background(), &local.Client{Dir: root}, "defaults")
	if err == nil {
		t.Fatal("an empty profile directory was accepted")
	}
	if !strings.Contains(err.Error(), "no YAML profiles found") {
		t.Errorf("error %q does not say the directory held no profiles", err)
	}
}

func TestFetchProfiles_MissingLocalDirectory(t *testing.T) {
	_, err := monitoring.FetchProfiles(context.Background(), &local.Client{Dir: t.TempDir()}, "defaults")
	if err == nil {
		t.Fatal("a missing profile directory was not reported")
	}
	if !strings.Contains(err.Error(), "listing profiles directory") {
		t.Errorf("error %q does not say the listing failed", err)
	}
}

func write(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func keys(m map[string]string) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	return names
}
