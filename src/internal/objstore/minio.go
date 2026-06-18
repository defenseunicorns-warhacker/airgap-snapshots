// Copyright 2026 Defense Unicorns
// SPDX-License-Identifier: AGPL-3.0-or-later OR LicenseRef-Defense-Unicorns-Commercial

package objstore

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ErrLedgerConflict is returned by PutLedger when the optimistic precondition
// (If-Match / If-None-Match) fails — i.e. another writer changed the ledger
// since it was read. Callers re-read and retry. Under leader election there is
// a single writer, so this is rare; it is a safety net, not a hot path.
var ErrLedgerConflict = errors.New("objstore: ledger precondition failed (concurrent write)")

// New constructs a Store backed by minio-go for cfg.
func New(cfg Config) (Store, error) {
	endpoint, secure := normalizeEndpoint(cfg.Endpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("objstore: empty endpoint (BSL config.s3Url is not set)")
	}
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("objstore: empty bucket")
	}

	opts := &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, cfg.SessionToken),
		Secure: secure,
		Region: cfg.Region,
	}
	if cfg.ForcePathStyle {
		opts.BucketLookup = minio.BucketLookupPath
	}
	if secure && (cfg.InsecureTLS || len(cfg.CABundle) > 0) {
		transport, err := minio.DefaultTransport(true)
		if err != nil {
			return nil, fmt.Errorf("objstore: build transport: %w", err)
		}
		tlsCfg := &tls.Config{InsecureSkipVerify: cfg.InsecureTLS} //nolint:gosec // operator-controlled, opt-in only
		if len(cfg.CABundle) > 0 {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(cfg.CABundle) {
				return nil, fmt.Errorf("objstore: CABundle contained no valid certificates")
			}
			tlsCfg.RootCAs = pool
		}
		transport.TLSClientConfig = tlsCfg
		opts.Transport = transport
	}

	client, err := minio.New(endpoint, opts)
	if err != nil {
		return nil, fmt.Errorf("objstore: init minio client: %w", err)
	}
	return &minioStore{client: client, bucket: cfg.Bucket, prefix: strings.Trim(cfg.Prefix, "/")}, nil
}

// normalizeEndpoint splits a BSL s3Url into a minio-go host:port + secure flag.
// A UDS BSL always sets config.s3Url (S3-compatible storage, e.g. in-cluster
// MinIO), so an empty endpoint is a misconfiguration, not an AWS-cloud default.
//   - ""                              -> ("", false) (caller errors)
//   - "http://minio.svc:9000"         -> ("minio.svc:9000", false)
//   - "https://s3.example.com"        -> ("s3.example.com", true)
//   - "minio.svc:9000" (no scheme)    -> ("minio.svc:9000", false)
func normalizeEndpoint(raw string) (host string, secure bool) {
	if raw == "" {
		return "", false
	}
	if strings.Contains(raw, "://") {
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			return u.Host, u.Scheme == "https"
		}
	}
	// Bare host:port — assume plaintext (the common MinIO in-cluster case).
	return strings.TrimRight(raw, "/"), false
}

type minioStore struct {
	client *minio.Client
	bucket string
	prefix string // trimmed, no leading/trailing slash
}

// fullKey joins the BSL prefix with a caller key (relative to Config.Prefix).
func (s *minioStore) fullKey(rel string) string {
	rel = strings.TrimLeft(rel, "/")
	if s.prefix == "" {
		return rel
	}
	return s.prefix + "/" + rel
}

// relKey strips the BSL prefix from a full bucket key, inverting fullKey.
func (s *minioStore) relKey(full string) string {
	if s.prefix == "" {
		return full
	}
	return strings.TrimPrefix(full, s.prefix+"/")
}

func (s *minioStore) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	listPrefix := s.fullKey(prefix)
	var out []ObjectInfo
	for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    listPrefix,
		Recursive: true,
	}) {
		if obj.Err != nil {
			return nil, fmt.Errorf("objstore: list %q: %w", listPrefix, obj.Err)
		}
		out = append(out, ObjectInfo{
			Key:          s.relKey(obj.Key),
			Size:         obj.Size,
			ETag:         strings.Trim(obj.ETag, `"`),
			LastModified: obj.LastModified,
		})
	}
	return out, nil
}

func (s *minioStore) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	// GetObject is lazy: it does not hit the network until the first Read, which
	// is exactly what the stager wants (stream straight into the outbox).
	obj, err := s.client.GetObject(ctx, s.bucket, s.fullKey(key), minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("objstore: open %q: %w", key, err)
	}
	return obj, nil
}

func (s *minioStore) GetLedger(ctx context.Context, key string) ([]byte, string, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, s.fullKey(key), minio.GetObjectOptions{})
	if err != nil {
		return nil, "", fmt.Errorf("objstore: get ledger %q: %w", key, err)
	}
	defer obj.Close()

	// Stat surfaces a missing object; GetObject itself is lazy.
	stat, err := obj.Stat()
	if err != nil {
		if isNotFound(err) {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("objstore: stat ledger %q: %w", key, err)
	}
	body, err := io.ReadAll(obj)
	if err != nil {
		return nil, "", fmt.Errorf("objstore: read ledger %q: %w", key, err)
	}
	return body, strings.Trim(stat.ETag, `"`), nil
}

func (s *minioStore) PutLedger(ctx context.Context, key string, body []byte, etag string) (string, error) {
	opts := minio.PutObjectOptions{ContentType: "application/json"}
	// Optimistic concurrency via conditional PUT (MinIO/modern-S3 extension):
	//   etag == "" -> If-None-Match: * (create only if absent)
	//   etag != "" -> If-Match: "<etag>" (overwrite only if unchanged)
	if etag == "" {
		opts.SetMatchETagExcept("*")
	} else {
		opts.SetMatchETag(etag)
	}
	info, err := s.client.PutObject(ctx, s.bucket, s.fullKey(key), bytes.NewReader(body), int64(len(body)), opts)
	if err != nil {
		if isPreconditionFailed(err) {
			return "", ErrLedgerConflict
		}
		return "", fmt.Errorf("objstore: put ledger %q: %w", key, err)
	}
	return strings.Trim(info.ETag, `"`), nil
}

func (s *minioStore) Close() error { return nil }

func isNotFound(err error) bool {
	r := minio.ToErrorResponse(err)
	return r.Code == "NoSuchKey" || r.Code == "NoSuchBucket" || r.StatusCode == http.StatusNotFound
}

func isPreconditionFailed(err error) bool {
	r := minio.ToErrorResponse(err)
	return r.Code == "PreconditionFailed" || r.StatusCode == http.StatusPreconditionFailed
}
