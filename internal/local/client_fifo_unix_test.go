//go:build unix

package local

import (
	"context"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A non-regular file would otherwise be opened like any other: a FIFO blocks
// the read forever, and the plan cannot be interrupted.
func TestClient_RefusesANonRegularFile(t *testing.T) {
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
