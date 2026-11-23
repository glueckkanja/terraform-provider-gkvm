// Package local reads profiles from a directory on disk instead of from a
// repository API: no requests, no rate limit, no token.
package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/glueckkanja/terraform-provider-gkvm/internal/source"
)

// Platform is the value of the provider's platform attribute for this backend.
const Platform = "local"

var _ source.Client = &Client{}

// Client reads files out of one directory tree.
type Client struct {
	// Dir is the absolute path of the directory that stands in for the
	// repository root.
	Dir string
	// Repository and Ref are for error messages only. Nothing here verifies
	// the claim: a directory carries no provenance.
	Repository string
	Ref        string
}

// Endpoint returns the directory being read, for error messages.
func (c *Client) Endpoint() string { return c.Dir }

// Subject returns the repository the directory is meant to hold, falling back
// to the directory itself when no repository was configured.
func (c *Client) Subject() string {
	if c.Repository != "" {
		return c.Repository
	}
	return c.Dir
}

// Reference returns the ref the directory is meant to hold.
func (c *Client) Reference() string { return c.Ref }

// ValidateConfig checks the directory path before anything is read. The path
// must be absolute: a reader's working directory differs per unit.
func (c *Client) ValidateConfig() error {
	if strings.TrimSpace(c.Dir) == "" {
		return fmt.Errorf("the local profile directory must not be empty")
	}
	if !filepath.IsAbs(c.Dir) {
		return fmt.Errorf("local profile directory %q must be an absolute path — a relative path resolves against the working directory of whoever reads the data source, which differs per unit", c.Dir)
	}
	if err := source.ValidateRef(c.Ref); err != nil {
		return err
	}
	return nil
}

// Ping verifies the directory exists and is readable.
func (c *Client) Ping(_ context.Context) error {
	info, err := os.Stat(c.Dir)
	if err != nil {
		return fmt.Errorf("local profile directory %q cannot be read: %w", c.Dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("local profile directory %q is not a directory", c.Dir)
	}
	// Stat succeeds on a directory the process may not enter; a listing is
	// the cheapest proof that it is traversable.
	if _, err := os.ReadDir(c.Dir); err != nil {
		return fmt.Errorf("local profile directory %q cannot be listed: %w", c.Dir, err)
	}
	return nil
}

// ListDirectory lists a directory below the root; the empty path means the root.
func (c *Client) ListDirectory(_ context.Context, path string) ([]source.Entry, error) {
	target, err := c.resolve(path)
	if err != nil {
		return nil, err
	}

	items, err := os.ReadDir(target)
	if err != nil {
		return nil, fmt.Errorf("listing %q below %s: %w", path, c.Dir, err)
	}

	entries := make([]source.Entry, 0, len(items))
	for _, item := range items {
		// Repository form, slash separated: this is what the caller passes
		// back to FetchFile and what the API backends report.
		entryPath := item.Name()
		if path != "" {
			entryPath = strings.TrimSuffix(path, "/") + "/" + item.Name()
		}
		entries = append(entries, source.Entry{
			Name:  item.Name(),
			Path:  entryPath,
			IsDir: item.IsDir(),
		})
	}
	return entries, nil
}

// FetchFile returns the bytes of one file below the root.
func (c *Client) FetchFile(_ context.Context, path string) ([]byte, error) {
	target, err := c.resolve(path)
	if err != nil {
		return nil, err
	}

	// The kind is checked before opening, not after: opening a FIFO blocks
	// until a writer appears, so a Stat on the handle comes too late.
	info, err := os.Lstat(target)
	if err != nil {
		return nil, fmt.Errorf("reading %q below %s: %w", path, c.Dir, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("reading %q below %s: it is %s, not a regular file", path, c.Dir, kindOf(info.Mode()))
	}

	file, err := os.Open(target) //nolint:gosec // target is confined to c.Dir by resolve
	if err != nil {
		return nil, fmt.Errorf("reading %q below %s: %w", path, c.Dir, err)
	}
	defer func() { _ = file.Close() }()

	// The cap the API backends apply. One byte past it is read so the limit
	// can be reported: a truncated profile is often still valid YAML.
	body, err := io.ReadAll(io.LimitReader(file, source.MaxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading %q below %s: %w", path, c.Dir, err)
	}
	if len(body) > source.MaxResponseBytes {
		return nil, fmt.Errorf("%q below %s exceeds the %d byte limit and was not read", path, c.Dir, source.MaxResponseBytes)
	}
	return body, nil
}

// resolve turns a repository-style path into an absolute path proven to stay
// inside the root. Three checks: ValidatePath catches every caller today, the
// Rel check is defence in depth, and the symlink pass catches a harmless path
// with a target outside.
func (c *Client) resolve(path string) (string, error) {
	if err := source.ValidatePath(path); err != nil {
		return "", err
	}

	root, err := filepath.EvalSymlinks(c.Dir)
	if err != nil {
		return "", fmt.Errorf("local profile directory %q cannot be resolved: %w", c.Dir, err)
	}

	target := root
	if path != "" {
		// A repository path is slash separated whatever the filesystem uses.
		target = filepath.Join(append([]string{root}, strings.Split(path, "/")...)...)
	}

	if err := contained(root, target); err != nil {
		return "", err
	}

	// Non-existence is not a containment problem: let the caller's read report
	// it with its errno. Any other failure means the guard could not run.
	resolved, err := filepath.EvalSymlinks(target)
	if errors.Is(err, fs.ErrNotExist) {
		return target, nil
	}
	if err != nil {
		return "", fmt.Errorf("%q below %s cannot be resolved: %w", path, c.Dir, err)
	}
	if err := contained(root, resolved); err != nil {
		return "", fmt.Errorf("%q below %s resolves outside the directory: %w", path, c.Dir, err)
	}
	return resolved, nil
}

// kindOf names a non-regular file for an error message.
func kindOf(mode fs.FileMode) string {
	switch {
	case mode.IsDir():
		return "a directory"
	case mode&fs.ModeSymlink != 0:
		return "a symlink"
	case mode&fs.ModeNamedPipe != 0:
		return "a named pipe"
	case mode&fs.ModeSocket != 0:
		return "a socket"
	case mode&fs.ModeDevice != 0:
		return "a device"
	}
	return "not a regular file"
}

// contained reports whether target is root or sits below it.
func contained(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return fmt.Errorf("cannot place %q relative to %q: %w", target, root, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes the local profile directory")
	}
	return nil
}
