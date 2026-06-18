// Package objstore is the client seam for reading a Velero Backup Storage
// Location (BSL) bucket as object storage, and for persisting the per-BSL
// replication ledger.
//
// Controllers depend only on the Store interface. The real S3/MinIO
// implementation lives in minio.go (minio-go); New() returns it.
//
// Key contract: every key that crosses this interface — the List prefix, each
// returned ObjectInfo.Key, the Open key, and the ledger keys — is **relative to
// Config.Prefix**. The implementation prepends Config.Prefix for the wire call
// and strips it from listed keys, so keys round-trip cleanly between List ->
// Open and List -> ledger. This matters for Velero: a BSL with prefix
// "backups" stores real objects at "backups/backups/<name>/..." and
// "backups/kopia/<ns>/...", so callers pass the logical key ("backups/<name>/")
// and the store handles the prefix nesting.
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
	// GetLedger reads the ledger object, returning its body and ETag.
	// A missing ledger returns (nil, "", nil).
	GetLedger(ctx context.Context, key string) (body []byte, etag string, err error)
	// PutLedger writes the ledger object with an optimistic If-Match on etag
	// ("" means create-if-absent). Returns the new ETag. A precondition failure
	// (concurrent writer) is reported as ErrLedgerConflict.
	PutLedger(ctx context.Context, key string, body []byte, etag string) (newETag string, err error)
	// Close releases resources.
	Close() error
}
