// Copyright 2026 Defense Unicorns
// SPDX-License-Identifier: AGPL-3.0-or-later OR LicenseRef-Defense-Unicorns-Commercial

// Package objstore is the client seam for reading a Velero Backup Storage
// Location (BSL) bucket as object storage, and for persisting the per-BSL
// replication ledger.
//
// Controllers depend only on the Store interface. The real S3/MinIO
// implementation lives in minio.go (minio-go); New() returns it.
//
// Key contract: the data keys — the List prefix, each returned ObjectInfo.Key,
// the Open/Put/Stat key — are **relative to Config.Prefix**. The implementation
// prepends Config.Prefix for the wire call and strips it from listed keys, so
// keys round-trip cleanly between List -> Open. This matters for Velero: a BSL
// with prefix "backups" stores real objects at "backups/backups/<name>/..." and
// "backups/kopia/<ns>/...", so callers pass the logical key ("backups/<name>/")
// and the store handles the prefix nesting.
//
// The ledger keys (GetLedger/PutLedger) are the exception: they are
// **bucket-absolute** (NOT under Config.Prefix). Velero validates a BSL by
// listing the top-level directories under its prefix and rejects any it doesn't
// recognize, so the ledger must live outside every prefixed BSL's view (at the
// bucket root) to avoid marking the BSL "Unavailable".
package objstore

import (
	"context"
	"io"
	"time"
)

// ObjectInfo describes one object in the bucket. Key is relative to Config.Prefix.
type ObjectInfo struct {
	Key          string
	Size         int64
	ETag         string
	LastModified time.Time
}

// Config describes how to reach a BSL bucket. Populated from the Velero BSL CR
// (provider config + credentials secret), with optional ReplicationPolicy
// overrides.
type Config struct {
	Endpoint        string // BSL config.s3Url (required; scheme decides TLS)
	Region          string
	Bucket          string
	Prefix          string // BSL prefix (no trailing slash); prepended to all keys
	ForcePathStyle  bool   // s3ForcePathStyle (MinIO)
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	CABundle        []byte
	InsecureTLS     bool
}

// Store reads a BSL bucket and persists the replication ledger. All key
// arguments and returned keys are relative to Config.Prefix (see package doc).
type Store interface {
	// List returns objects whose key starts with prefix (relative to Config.Prefix).
	List(ctx context.Context, prefix string) ([]ObjectInfo, error)
	// Open streams the object at key (relative to Config.Prefix).
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	// Put writes size bytes from r to key (relative to Config.Prefix). Used by
	// the destination importer to reconstruct the BSL layout; the source path
	// does not call it. contentType may be "".
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// Stat returns the object's info and true if it exists at key (relative to
	// Config.Prefix), or a zero ObjectInfo and false if absent.
	Stat(ctx context.Context, key string) (ObjectInfo, bool, error)
	// GetLedger reads the ledger object, returning its body and ETag. The key is
	// bucket-absolute (NOT under Config.Prefix; see the package doc). A missing
	// ledger returns (nil, "", nil).
	GetLedger(ctx context.Context, key string) (body []byte, etag string, err error)
	// PutLedger writes the ledger object (bucket-absolute key) with an optimistic
	// If-Match on etag ("" means create-if-absent). Returns the new ETag. A
	// precondition failure (concurrent writer) is reported as ErrLedgerConflict.
	PutLedger(ctx context.Context, key string, body []byte, etag string) (newETag string, err error)
	// Close releases resources.
	Close() error
}
