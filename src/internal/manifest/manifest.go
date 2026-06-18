// Copyright 2026 Defense Unicorns
// SPDX-License-Identifier: AGPL-3.0-or-later OR LicenseRef-Defense-Unicorns-Commercial

// Package manifest defines the CRDT document Snapback writes per replication
// (the "manifest plane", DESIGN §3.5) and the destination importer consumes.
//
// Two planes carry a replication: bytes move via peat attachments, metadata via
// peat CRDT documents. peat flattens each attachment to
// inbox/<distributionID>/<basename> on the receiver, discarding the original
// bucket-key layout. The manifest carries the mapping the importer needs to
// reconstruct that layout into a destination object store so destination Velero
// can auto-discover the backups. It is a peat document (PutDocument), synced
// over the same mesh, partition-tolerant.
package manifest

import (
	"encoding/json"
	"fmt"
)

// Collection is the peat document collection replication manifests live in.
const Collection = "snapback_replications"

// SchemaVersion is the manifest wire-schema version. Bump on breaking changes;
// the importer rejects versions it does not understand rather than guessing.
const SchemaVersion = 1

// FileEntry maps one shipped object to where its bytes land on the inbox and
// where they must be reconstructed on the destination store.
type FileEntry struct {
	// Key is the original BSL object key, relative to the source BSL prefix
	// (e.g. "backups/<name>/velero-backup.json"). The importer writes the
	// verified file to this key under the destination prefix.
	Key string `json:"key"`
	// Basename is the name peat flattens the file to on the inbox; together with
	// DistributionID it forms the primary inbox locator
	// inbox/<distributionID>/<basename>.
	Basename string `json:"basename"`
	// SHA256 is the hex-encoded content hash, verified on import.
	SHA256 string `json:"sha256"`
	// Size is the file size in bytes, verified on import.
	Size int64 `json:"size"`
	// DistributionID is the peat distribution the file shipped in.
	DistributionID string `json:"distributionId"`
	// BlobToken is the Iroh content address (BLAKE3); diagnostic / fallback.
	BlobToken string `json:"blobToken,omitempty"`
}

// Manifest is the per-backup replication manifest document. Its document id is
// DocID(storageLocation, backupName).
type Manifest struct {
	// SchemaVersion is the schema this document was written with.
	SchemaVersion int `json:"schemaVersion"`
	// BackupName is the Velero backup this manifest reconstructs.
	BackupName string `json:"backupName"`
	// StorageLocation is the source Velero BackupStorageLocation name.
	StorageLocation string `json:"storageLocation"`
	// BSLPrefix is the source BSL prefix the keys are relative to (informational;
	// the importer writes under its own --dest-prefix, which need not match).
	BSLPrefix string `json:"bslPrefix,omitempty"`
	// ObjectCount and TotalBytes summarize the shipped delta.
	ObjectCount int   `json:"objectCount"`
	TotalBytes  int64 `json:"totalBytes"`
	// CompletedAt is when the source marked the replication COMPLETED (RFC3339).
	CompletedAt string `json:"completedAt,omitempty"`
	// Complete is the commit flag: the importer ignores a manifest until true,
	// so a partially-written manifest is never acted on.
	Complete bool `json:"complete"`
	// Files is the per-object reconstruction map.
	Files []FileEntry `json:"files"`
}

// DocID returns the manifest document id for a backup. Namespacing by BSL keeps
// identically-named backups in different BSLs distinct.
func DocID(storageLocation, backupName string) string {
	return storageLocation + "/" + backupName
}

// Marshal serializes a manifest to the JSON bytes carried in PutDocument.
func (m *Manifest) Marshal() ([]byte, error) {
	return json.Marshal(m)
}

// Parse deserializes a manifest document and rejects unknown schema versions.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest: parse: %w", err)
	}
	if m.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("manifest: unsupported schemaVersion %d (want %d)", m.SchemaVersion, SchemaVersion)
	}
	return &m, nil
}
