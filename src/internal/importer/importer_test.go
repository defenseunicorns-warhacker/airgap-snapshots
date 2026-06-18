package importer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/defenseunicorns/snapback/internal/manifest"
	"github.com/defenseunicorns/snapback/internal/objstore"
)

// memStore is an in-memory objstore.Store recording Put order so tests can
// assert the discovery object is written last.
type memStore struct {
	objs map[string][]byte
	puts []string
}

func newMemStore() *memStore { return &memStore{objs: map[string][]byte{}} }

func (m *memStore) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.objs[key] = b
	m.puts = append(m.puts, key)
	return nil
}

func (m *memStore) Stat(_ context.Context, key string) (objstore.ObjectInfo, bool, error) {
	b, ok := m.objs[key]
	if !ok {
		return objstore.ObjectInfo{}, false, nil
	}
	return objstore.ObjectInfo{Key: key, Size: int64(len(b))}, true, nil
}

func (m *memStore) List(context.Context, string) ([]objstore.ObjectInfo, error) { return nil, nil }
func (m *memStore) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("not used")
}
func (m *memStore) GetLedger(context.Context, string) ([]byte, string, error) { return nil, "", nil }
func (m *memStore) PutLedger(context.Context, string, []byte, string) (string, error) {
	return "", nil
}
func (m *memStore) Close() error { return nil }

func shaHex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func writeInbox(t *testing.T, root, distID, basename string, content []byte) {
	t.Helper()
	dir := filepath.Join(root, distID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, basename), content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func entry(key, distID string, content []byte) manifest.FileEntry {
	return manifest.FileEntry{
		Key:            key,
		Basename:       filepath.Base(key),
		SHA256:         shaHex(content),
		Size:           int64(len(content)),
		DistributionID: distID,
	}
}

func TestImport_HappyPath_CommitObjectWrittenLast(t *testing.T) {
	inbox := t.TempDir()
	data1 := []byte("the resource tarball bytes")
	data2 := []byte("a kopia pack blob")
	meta := []byte(`{"version":1}`)
	writeInbox(t, inbox, "d1", "b1.tar.gz", data1)
	writeInbox(t, inbox, "d2", "pack0", data2)
	writeInbox(t, inbox, "d3", "velero-backup.json", meta)

	m := &manifest.Manifest{
		SchemaVersion: manifest.SchemaVersion, Complete: true,
		Files: []manifest.FileEntry{
			entry("backups/b/b1.tar.gz", "d1", data1),
			entry("kopia/ns/pack0", "d2", data2),
			entry("backups/b/velero-backup.json", "d3", meta),
		},
	}
	store := newMemStore()
	im := &Importer{InboxRoot: inbox, Store: store}

	res, err := im.Import(context.Background(), m)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !res.Committed || res.Pending != 0 || res.FilesImported != 3 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if len(store.puts) != 3 {
		t.Fatalf("want 3 puts, got %v", store.puts)
	}
	// The discovery object must be the final write so the backup commits atomically.
	if last := store.puts[len(store.puts)-1]; last != "backups/b/velero-backup.json" {
		t.Fatalf("discovery object not written last: order=%v", store.puts)
	}
	if string(store.objs["kopia/ns/pack0"]) != string(data2) {
		t.Fatalf("pack blob content not reconstructed at original key")
	}
}

func TestImport_PendingDataFileGatesCommit(t *testing.T) {
	inbox := t.TempDir()
	data1 := []byte("present data")
	meta := []byte(`{"version":1}`)
	writeInbox(t, inbox, "d1", "b1.tar.gz", data1)
	// data2 deliberately NOT landed. velero-backup.json IS present, to prove the
	// commit is gated on data files, not on its own presence.
	writeInbox(t, inbox, "d3", "velero-backup.json", meta)
	data2 := []byte("not yet landed")

	m := &manifest.Manifest{
		SchemaVersion: manifest.SchemaVersion, Complete: true,
		Files: []manifest.FileEntry{
			entry("backups/b/b1.tar.gz", "d1", data1),
			entry("kopia/ns/pack0", "d2", data2),
			entry("backups/b/velero-backup.json", "d3", meta),
		},
	}
	store := newMemStore()
	im := &Importer{InboxRoot: inbox, Store: store}

	res, err := im.Import(context.Background(), m)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Committed {
		t.Fatal("must not commit while a data file is pending")
	}
	if res.Pending != 1 {
		t.Fatalf("want 1 pending, got %d", res.Pending)
	}
	if _, ok := store.objs["backups/b/velero-backup.json"]; ok {
		t.Fatal("discovery object must not be written while data is pending")
	}
}

func TestImport_FallbackLocatorByContent(t *testing.T) {
	inbox := t.TempDir()
	data := []byte("located by sha not by distribution id")
	meta := []byte(`{"version":1}`)
	// File landed under a DIFFERENT directory than the manifest's distributionId
	// (simulating per-bundle dist ids / layout drift): primary path misses,
	// fallback must find it by basename + sha.
	writeInbox(t, inbox, "unexpected-dir", "pack0", data)
	writeInbox(t, inbox, "d3", "velero-backup.json", meta)

	m := &manifest.Manifest{
		SchemaVersion: manifest.SchemaVersion, Complete: true,
		Files: []manifest.FileEntry{
			entry("kopia/ns/pack0", "d-expected", data),
			entry("backups/b/velero-backup.json", "d3", meta),
		},
	}
	store := newMemStore()
	im := &Importer{InboxRoot: inbox, Store: store}

	res, err := im.Import(context.Background(), m)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !res.Committed || res.FilesImported != 2 {
		t.Fatalf("fallback locate failed: %+v", res)
	}
}

func TestImport_CorruptionIsHardError(t *testing.T) {
	inbox := t.TempDir()
	want := []byte("the correct twelve")
	got := []byte("the corrupt twelve") // same length, different content
	if len(want) != len(got) {
		t.Fatalf("test setup: lengths must match to exercise sha (not size) mismatch")
	}
	writeInbox(t, inbox, "d1", "pack0", got)

	m := &manifest.Manifest{
		SchemaVersion: manifest.SchemaVersion, Complete: true,
		Files: []manifest.FileEntry{entry("kopia/ns/pack0", "d1", want)},
	}
	im := &Importer{InboxRoot: inbox, Store: newMemStore()}

	if _, err := im.Import(context.Background(), m); err == nil {
		t.Fatal("expected a corruption error on sha mismatch with correct size")
	}
}

func TestImport_Idempotent(t *testing.T) {
	inbox := t.TempDir()
	data := []byte("idempotent data")
	meta := []byte(`{"v":1}`)
	writeInbox(t, inbox, "d1", "pack0", data)
	writeInbox(t, inbox, "d3", "velero-backup.json", meta)

	m := &manifest.Manifest{
		SchemaVersion: manifest.SchemaVersion, Complete: true,
		Files: []manifest.FileEntry{
			entry("kopia/ns/pack0", "d1", data),
			entry("backups/b/velero-backup.json", "d3", meta),
		},
	}
	store := newMemStore()
	im := &Importer{InboxRoot: inbox, Store: store}

	if _, err := im.Import(context.Background(), m); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	putsAfterFirst := len(store.puts)
	res, err := im.Import(context.Background(), m)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if !res.Committed {
		t.Fatal("second pass should still report committed")
	}
	if len(store.puts) != putsAfterFirst {
		t.Fatalf("second pass re-wrote objects (not idempotent): %v", store.puts)
	}
}
