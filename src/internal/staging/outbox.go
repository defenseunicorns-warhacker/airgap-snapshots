// Package staging streams BSL objects into the peat-node outbox volume and
// produces the peat.FileSpec (with size + sha256 computed in a single pass)
// that SendAttachments needs.
//
// This package is fully implemented (it needs only a filesystem + an
// objstore.Store), so M3 can exercise it against a real bucket without further
// scaffolding.
package staging

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/defenseunicorns/snapback/internal/objstore"
	"github.com/defenseunicorns/snapback/internal/peat"
)

// Stager writes objects into an allowlisted peat attachment root.
type Stager struct {
	// RootName is the peat --attachment-root name (e.g. "outbox").
	RootName string
	// RootPath is the filesystem mount path of that root in this pod.
	RootPath string
}

// New returns a Stager for the named root mounted at rootPath.
func New(rootName, rootPath string) *Stager {
	return &Stager{RootName: rootName, RootPath: rootPath}
}

// safeRelPath validates that key is a safe relative path inside the root.
// Mirrors peat's own checks (no absolute, no "..", no leading slash) so we fail
// before the RPC, not after.
func safeRelPath(key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("staging: empty object key")
	}
	if strings.HasPrefix(key, "/") {
		return "", fmt.Errorf("staging: key must be relative: %q", key)
	}
	clean := path.Clean(key)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("staging: key escapes root: %q", key)
	}
	return clean, nil
}

// StageObject streams the object at key from store into the outbox at a path
// mirroring key, computing its size and sha256 in the same pass, and returns
// the peat.FileSpec describing it.
func (s *Stager) StageObject(ctx context.Context, store objstore.Store, key string) (peat.FileSpec, error) {
	rel, err := safeRelPath(key)
	if err != nil {
		return peat.FileSpec{}, err
	}
	dst := filepath.Join(s.RootPath, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return peat.FileSpec{}, fmt.Errorf("staging: mkdir for %q: %w", key, err)
	}

	rc, err := store.Open(ctx, key)
	if err != nil {
		return peat.FileSpec{}, fmt.Errorf("staging: open %q: %w", key, err)
	}
	defer rc.Close()

	// Write to a temp file then rename, so a crash never leaves a partial file
	// that looks complete to peat.
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".staging-*")
	if err != nil {
		return peat.FileSpec{}, fmt.Errorf("staging: temp for %q: %w", key, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), rc)
	if err != nil {
		tmp.Close()
		return peat.FileSpec{}, fmt.Errorf("staging: copy %q: %w", key, err)
	}
	if err := tmp.Close(); err != nil {
		return peat.FileSpec{}, fmt.Errorf("staging: close %q: %w", key, err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return peat.FileSpec{}, fmt.Errorf("staging: rename %q: %w", key, err)
	}

	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return peat.FileSpec{
		RootName:     s.RootName,
		RelativePath: rel,
		Size:         uint64(n),
		SHA256:       sum,
		DisplayName:  path.Base(rel),
	}, nil
}

// Release removes a staged file once its bundle has reached COMPLETED. Callers
// must NOT release before terminal status: peat serves bytes from these files
// on pull, and the destination may be offline for a long DDIL window.
func (s *Stager) Release(relPath string) error {
	rel, err := safeRelPath(relPath)
	if err != nil {
		return err
	}
	return os.Remove(filepath.Join(s.RootPath, filepath.FromSlash(rel)))
}
