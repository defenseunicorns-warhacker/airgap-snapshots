// Copyright 2026 Defense Unicorns
// SPDX-License-Identifier: AGPL-3.0-or-later OR LicenseRef-Defense-Unicorns-Commercial

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ImportPhase is the lifecycle phase of a BackupImport (destination side).
// +kubebuilder:validation:Enum=Pending;Importing;Imported;Failed
type ImportPhase string

const (
	// ImportPending — created, manifest not yet fetched/complete.
	ImportPending ImportPhase = "Pending"
	// ImportImporting — verifying landed inbox files and writing them to the
	// destination object store; awaiting bytes still in flight.
	ImportImporting ImportPhase = "Importing"
	// ImportImported — every file verified + written and the discovery object
	// (velero-backup.json) committed last; the backup is visible to dest Velero.
	ImportImported ImportPhase = "Imported"
	// ImportFailed — terminal failure (e.g. unsupported manifest schema).
	ImportFailed ImportPhase = "Failed"
)

// BackupImportSpec is the desired import of one replicated backup on the
// destination. Created by the manifest poll-bridge from a snapback_replications
// document; the reconciler fetches the manifest itself so the CR stays small
// (no large file list in etcd).
type BackupImportSpec struct {
	// BackupName is the Velero backup being reconstructed.
	BackupName string `json:"backupName"`
	// StorageLocation is the source BackupStorageLocation the backup came from.
	StorageLocation string `json:"storageLocation"`
	// ManifestDocID is the peat document id of the replication manifest in the
	// snapback_replications collection.
	ManifestDocID string `json:"manifestDocID"`
}

// BackupImportStatus is the observed import state.
type BackupImportStatus struct {
	// Phase is the high-level lifecycle phase.
	// +optional
	Phase ImportPhase `json:"phase,omitempty"`
	// Conditions are detailed observations (Ready, Degraded).
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// FilesTotal is the number of files the manifest declares.
	// +optional
	FilesTotal int64 `json:"filesTotal,omitempty"`
	// FilesImported is the number verified + written to the destination store.
	// +optional
	FilesImported int64 `json:"filesImported,omitempty"`
	// BytesImported is the total bytes written to the destination store.
	// +optional
	BytesImported int64 `json:"bytesImported,omitempty"`
	// Committed is true once the discovery object (velero-backup.json) has been
	// written last, making the backup discoverable by destination Velero.
	// +optional
	Committed bool `json:"committed,omitempty"`
	// StartedAt is when the first import pass began.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// CompletedAt is when the backup was fully imported and committed.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	// RetryCount is the number of reconcile-driven retries.
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
// +kubebuilder:resource:scope=Namespaced,shortName=bimp
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Backup",type=string,JSONPath=`.spec.backupName`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Imported",type=integer,JSONPath=`.status.filesImported`
// +kubebuilder:printcolumn:name="Committed",type=boolean,JSONPath=`.status.committed`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// BackupImport is the destination-side unit of work and audit for reconstructing
// one replicated backup into the destination object store.
type BackupImport struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BackupImportSpec   `json:"spec,omitempty"`
	Status BackupImportStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BackupImportList contains a list of BackupImport.
type BackupImportList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BackupImport `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BackupImport{}, &BackupImportList{})
}
