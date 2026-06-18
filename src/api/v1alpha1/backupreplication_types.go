// Copyright 2026 Defense Unicorns
// SPDX-License-Identifier: AGPL-3.0-or-later OR LicenseRef-Defense-Unicorns-Commercial

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ReplicationPhase is the lifecycle phase of a BackupReplication.
// +kubebuilder:validation:Enum=Pending;Staging;Transmitting;Replicated;Failed;Skipped
type ReplicationPhase string

const (
	// PhasePending — created, not yet acted on.
	PhasePending ReplicationPhase = "Pending"
	// PhaseStaging — enumerating and streaming BSL objects into the outbox.
	PhaseStaging ReplicationPhase = "Staging"
	// PhaseTransmitting — bundles handed to peat, awaiting terminal status.
	PhaseTransmitting ReplicationPhase = "Transmitting"
	// PhaseReplicated — every bundle reached COMPLETED (sender-side).
	PhaseReplicated ReplicationPhase = "Replicated"
	// PhaseFailed — terminal failure after exhausting retries.
	PhaseFailed ReplicationPhase = "Failed"
	// PhaseSkipped — backup not eligible (e.g. snapshot-only, no FSB data).
	PhaseSkipped ReplicationPhase = "Skipped"
)

// BundlePhase mirrors peat's DistributionStatus for a single bundle.
// +kubebuilder:validation:Enum=Pending;InProgress;Completed;Failed;Cancelled
type BundlePhase string

const (
	BundlePending    BundlePhase = "Pending"
	BundleInProgress BundlePhase = "InProgress"
	BundleCompleted  BundlePhase = "Completed"
	BundleFailed     BundlePhase = "Failed"
	BundleCancelled  BundlePhase = "Cancelled"
)

// BundleStatus tracks one SendAttachments bundle.
type BundleStatus struct {
	// BundleID is the deterministic peat bundle_id for idempotent re-sends.
	BundleID string `json:"bundleID"`
	// FileCount is the number of FileSpecs in this bundle.
	FileCount int32 `json:"fileCount"`
	// Bytes is the total declared bytes in this bundle.
	Bytes int64 `json:"bytes"`
	// Phase is the bundle's current state.
	Phase BundlePhase `json:"phase"`
	// DistributionIDs are the peat distribution IDs (one per file).
	// +optional
	DistributionIDs []string `json:"distributionIDs,omitempty"`
	// BytesTransferred is the sender-side served byte count.
	// +optional
	BytesTransferred int64 `json:"bytesTransferred,omitempty"`
}

// BackupReplicationSpec is the desired replication of one Velero Backup.
type BackupReplicationSpec struct {
	// BackupName is the Velero Backup to replicate.
	BackupName string `json:"backupName"`
	// PolicyName is the ReplicationPolicy governing this replication.
	PolicyName string `json:"policyName"`
	// StorageLocation is the Velero BackupStorageLocation the backup lives in.
	StorageLocation string `json:"storageLocation"`
}

// BackupReplicationStatus is the observed replication state.
type BackupReplicationStatus struct {
	// Phase is the high-level lifecycle phase.
	// +optional
	Phase ReplicationPhase `json:"phase,omitempty"`
	// Conditions are detailed observations.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// ObjectCount is the number of BSL objects in this replication's delta.
	// +optional
	ObjectCount int64 `json:"objectCount,omitempty"`
	// TotalBytes is the total bytes to transfer for this delta.
	// +optional
	TotalBytes int64 `json:"totalBytes,omitempty"`
	// BytesTransferred is the sender-side served byte count across bundles.
	// +optional
	BytesTransferred int64 `json:"bytesTransferred,omitempty"`
	// Bundles tracks per-bundle progress.
	// +optional
	Bundles []BundleStatus `json:"bundles,omitempty"`
	// StartedAt is when staging began.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// CompletedAt is when all bundles reached COMPLETED.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	// RetryCount is the number of reconcile-driven re-sends.
	// +optional
	RetryCount int32 `json:"retryCount,omitempty"`
	// ObservedGeneration is the last spec generation reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// LastError is the most recent error message, if any.
	// +optional
	LastError string `json:"lastError,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=brep
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Backup",type=string,JSONPath=`.spec.backupName`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Objects",type=integer,JSONPath=`.status.objectCount`
// +kubebuilder:printcolumn:name="Bytes",type=integer,JSONPath=`.status.totalBytes`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// BackupReplication is the unit of work and audit for replicating one Backup.
type BackupReplication struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BackupReplicationSpec   `json:"spec,omitempty"`
	Status BackupReplicationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BackupReplicationList contains a list of BackupReplication.
type BackupReplicationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BackupReplication `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BackupReplication{}, &BackupReplicationList{})
}
