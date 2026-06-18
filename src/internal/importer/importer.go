// Copyright 2026 Defense Unicorns
// SPDX-License-Identifier: AGPL-3.0-or-later OR LicenseRef-Defense-Unicorns-Commercial

// Package importer reconstructs a replicated backup's original BSL object-key
// layout on the destination, from peat's flattened inbox plus the CRDT manifest
// (DESIGN §3.5, §12).
//
// peat writes each received attachment to inbox/<distributionID>/<basename>,
// discarding the source bucket-key structure. The importer reads the manifest
// (which maps each shipped object's distributionID+basename back to its original
// BSL key + sha256 + size), verifies each landed file, and writes it to a
// destination object store laid out as a Velero BSL. The discovery object
// (velero-backup.json) is written LAST so destination Velero's backup-sync
// controller never observes a half-written backup.
package importer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/defenseunicorns/snapback/internal/manifest"
	"github.com/defenseunicorns/snapback/internal/objstore"
)

// veleroDiscoveryObject is the basename Velero's backup-sync controller keys
// backup discovery off of. Writing it last commits the backup atomically.
const veleroDiscoveryObject = "velero-backup.json"

// errNotLanded marks an inbox file that exists but is the wrong size — i.e. its
// bytes have not fully arrived yet. The caller retries rather than failing.
var errNotLanded = errors.New("inbox file not fully landed")

// Importer reconstructs one backup at a time from inboxRoot into Store.
type Importer struct {
	// InboxRoot is the peat --attachment-inbox mount path.
	InboxRoot string
	// Store is the destination object store (a Velero BSL bucket).
	Store objstore.Store
}

// Result summarizes one import pass.
type Result struct {
	// FilesImported is the count of manifest files now present + verified in the
	// destination store (already-present + written-this-pass).
	FilesImported int
	// BytesImported is the bytes written to the destination store this pass.
	BytesImported int64
	// Pending is the count of files whose bytes have not landed/verified yet.
	Pending int
	// Committed is true once the discovery object has been written last, making
	// the backup discoverable by destination Velero.
	Committed bool
}

// Import runs one idempotent pass. Files already present in the destination
// store are skipped. Files whose bytes have not yet landed on the inbox are
// counted as Pending (the caller requeues). The discovery object(s) are written
// only after every other file is present, so a partially-imported backup is
// never made discoverable.
func (im *Importer) Import(ctx context.Context, m *manifest.Manifest) (Result, error) {
	var res Result
	var idx *inboxIndex // lazily built for the fallback locator

	var commit []manifest.FileEntry
	for _, e := range m.Files {
		if path.Base(e.Key) == veleroDiscoveryObject {
			commit = append(commit, e) // defer discovery objects to the commit phase
			continue
		}
		done, n, err := im.importOne(ctx, e, &idx)
		if err != nil {
			return res, err
		}
		if !done {
			res.Pending++
			continue
		}
		res.FilesImported++
		res.BytesImported += n
	}

	// Commit phase: only once every data file is present do we write the
	// discovery object(s), making the backup atomically visible to Velero.
	if res.Pending > 0 {
		return res, nil
	}
	for _, e := range commit {
		done, n, err := im.importOne(ctx, e, &idx)
		if err != nil {
			return res, err
		}
		if !done {
			res.Pending++
			return res, nil
		}
		res.FilesImported++
		res.BytesImported += n
	}
	res.Committed = res.Pending == 0
	return res, nil
}

// importOne ensures entry e is present + verified in the destination store.
// done=true means the object is now in the store (already present, or written
// this pass); done=false means its bytes have not landed on the inbox yet.
func (im *Importer) importOne(ctx context.Context, e manifest.FileEntry, idx **inboxIndex) (done bool, written int64, err error) {
	// Idempotent skip: already in the dest store at the expected size.
	if info, ok, serr := im.Store.Stat(ctx, e.Key); serr != nil {
		return false, 0, fmt.Errorf("stat dest %q: %w", e.Key, serr)
	} else if ok && info.Size == e.Size {
		return true, 0, nil
	}

	src, ok, lerr := im.locate(e, idx)
	if lerr != nil {
		return false, 0, lerr
	}
	if !ok {
		return false, 0, nil // bytes not landed yet
	}

	// Verify before writing: never publish unverified bytes to the dest BSL.
	if verr := verify(src, e); verr != nil {
		if errors.Is(verr, errNotLanded) {
			return false, 0, nil // partial file, still arriving
		}
		return false, 0, verr // size-correct but sha mismatch => corruption
	}

	f, err := os.Open(src)
	if err != nil {
		return false, 0, fmt.Errorf("open inbox %q: %w", src, err)
	}
	defer f.Close()
	if err := im.Store.Put(ctx, e.Key, f, e.Size, ""); err != nil {
		return false, 0, fmt.Errorf("put dest %q: %w", e.Key, err)
	}
	return true, e.Size, nil
}

// locate finds the inbox path for an entry. Primary: inbox/<distributionID>/
// <basename> (peat's flattened layout). Fallback: scan the inbox for a file with
// the same basename whose sha256+size match — covers per-bundle distribution ids
// or any layout drift, disambiguating by content.
func (im *Importer) locate(e manifest.FileEntry, idx **inboxIndex) (string, bool, error) {
	if e.DistributionID != "" {
		if primary := filepath.Join(im.InboxRoot, e.DistributionID, e.Basename); fileExists(primary) {
			return primary, true, nil
		}
	}
	if *idx == nil {
		built, err := newInboxIndex(im.InboxRoot)
		if err != nil {
			return "", false, err
		}
		*idx = built
	}
	for _, cand := range (*idx).byBasename[e.Basename] {
		if sum, n, err := hashFile(cand); err == nil && n == e.Size && sum == e.SHA256 {
			return cand, true, nil
		}
	}
	return "", false, nil
}

// inboxIndex maps basename -> candidate paths for the fallback locator. Built
// once per import pass.
type inboxIndex struct {
	byBasename map[string][]string
}

func newInboxIndex(root string) (*inboxIndex, error) {
	idx := &inboxIndex{byBasename: map[string][]string{}}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil // inbox dir may not exist before the first pull
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		b := filepath.Base(p)
		idx.byBasename[b] = append(idx.byBasename[b], p)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan inbox %q: %w", root, err)
	}
	return idx, nil
}

// verify checks an inbox file's size and sha256 against the manifest entry.
// A size mismatch yields errNotLanded (still arriving); a correct size with a
// sha mismatch is a hard corruption error.
func verify(p string, e manifest.FileEntry) error {
	sum, n, err := hashFile(p)
	if err != nil {
		return fmt.Errorf("hash inbox %q: %w", p, err)
	}
	if n != e.Size {
		return fmt.Errorf("%w: %q size %d != manifest %d", errNotLanded, p, n, e.Size)
	}
	if sum != e.SHA256 {
		return fmt.Errorf("sha256 mismatch for %q: inbox %s != manifest %s", e.Key, sum, e.SHA256)
	}
	return nil
}

func hashFile(p string) (sum string, size int64, err error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
