// Copyright 2026 Defense Unicorns
// SPDX-License-Identifier: AGPL-3.0-or-later OR LicenseRef-Defense-Unicorns-Commercial

// Package peat is the client seam between Snapback's controllers and the
// co-located peat-node sidecar (peat.sidecar.v1.PeatSidecar gRPC API).
//
// Controllers depend only on the Client interface so the real gRPC client can
// be wired in during the M3 transport milestone without touching reconcile
// logic, and so the controllers are unit-testable with a fake.
//
// The real implementation will be generated from proto/sidecar.proto (see
// `make proto`) and dialed in New(). Until then, New() returns a stub whose
// methods return ErrNotImplemented.
package peat

import "context"

// Priority maps to peat AttachmentPriority. v1 records but does not enforce
// cross-class preemption.
type Priority string

const (
	PriorityBulk     Priority = "Bulk"
	PriorityLow      Priority = "Low"
	PriorityRoutine  Priority = "Routine"
	PriorityPriority Priority = "Priority"
	PriorityCritical Priority = "Critical"
)

// DistributionStatus mirrors peat's DistributionStatus. v1 emits COMPLETED or
// FAILED only; PARTIAL is reserved for v2. COMPLETED is sender-side: "the
// destination connected and pulled all bytes from us," NOT "durably written."
type DistributionStatus string

const (
	StatusPending    DistributionStatus = "Pending"
	StatusInProgress DistributionStatus = "InProgress"
	StatusCompleted  DistributionStatus = "Completed"
	StatusFailed     DistributionStatus = "Failed"
	StatusCancelled  DistributionStatus = "Cancelled"
)

// Terminal reports whether a status will not change further.
func (s DistributionStatus) Terminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}

// FileSpec is one file in a SendAttachments bundle. The caller asserts Size and
// SHA256; peat verifies both during ingest. RelativePath is resolved under the
// allowlisted RootName; it must not be absolute or contain "..".
type FileSpec struct {
	RootName     string
	RelativePath string
	Size         uint64
	SHA256       [32]byte // raw bytes, not hex
	ContentType  string
	DisplayName  string
}

// Scope targets a distribution.
//
// v1 uses AllNodes: distribute to every reachable peer. In a single-destination
// DR topology the source peers only with the destination, so AllNodes == "send
// to the destination" — and it works without a populated node registry.
//
// NodeList is kept for larger meshes but is NOT usable in a minimal setup: peat
// resolves NodeList ids via its node registry (CRDT `nodes` docs), NOT via
// known_peers, so ids that aren't published there fail with "discovery grace
// expired: peer never connected" even when the peer is actively connected — this
// holds for both node-ids and endpoint-ids. (M3 finding.) To use NodeList later,
// populate the node registry (agent watcher, or Snapback PutDocument node docs).
type Scope struct {
	// AllNodes distributes to every reachable peer (the v1 choice).
	AllNodes bool
	// NodeIDs is a NodeListScope (future; needs node-registry resolution).
	NodeIDs []string
}

// AttachmentHandle is peat's per-file handle returned by SendAttachments.
type AttachmentHandle struct {
	FileIndex      uint32
	BlobToken      string
	DistributionID string
}

// SendResult is the SendAttachments response.
type SendResult struct {
	BundleID string
	Handles  []AttachmentHandle
}

// Progress is a point-in-time view of a distribution's transfer.
type Progress struct {
	DistributionID   string
	Status           DistributionStatus
	BytesTransferred uint64
	BytesTotal       uint64
	Err              string
}

// NodeStatus is the peat-node lifecycle/identity snapshot from GetStatus.
type NodeStatus struct {
	NodeID         string
	EndpointAddr   string
	SyncActive     bool
	ConnectedPeers uint32
	Phase          string
}

// Client is the subset of the peat sidecar API Snapback uses.
type Client interface {
	// Status returns the local node's identity and sync state.
	Status(ctx context.Context) (NodeStatus, error)

	// EnsurePeer establishes (idempotently) a connection to the destination peer.
	EnsurePeer(ctx context.Context, endpointID string, addresses []string, relayURL string) error

	// SendAttachments ingests files and queues distribution. bundleID is
	// caller-supplied for idempotency; re-submitting an identical bundle is a
	// no-op returning existing handles.
	SendAttachments(ctx context.Context, bundleID string, files []FileSpec, scope Scope, priority Priority) (SendResult, error)

	// GetDistribution returns the current status of one distribution.
	GetDistribution(ctx context.Context, distributionID string) (Progress, error)

	// WaitBundle blocks until every distribution in the bundle reaches a
	// terminal state (or ctx is done). On a peat-node restart it may return a
	// not-found error; callers should re-SendAttachments (idempotent).
	WaitBundle(ctx context.Context, bundleID string) ([]Progress, error)

	// Close releases the underlying connection.
	Close() error
}

// New is implemented by the Connect/JSON client in connect.go.
