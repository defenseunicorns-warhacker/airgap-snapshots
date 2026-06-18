// Copyright 2026 Defense Unicorns
// SPDX-License-Identifier: AGPL-3.0-or-later OR LicenseRef-Defense-Unicorns-Commercial

package v1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BackupPhase is a subset of Velero's backup phases.
type BackupPhase string

const (
	BackupPhaseNew              BackupPhase = "New"
	BackupPhaseInProgress       BackupPhase = "InProgress"
	BackupPhaseFinalizing       BackupPhase = "Finalizing"
	BackupPhaseCompleted        BackupPhase = "Completed"
	BackupPhasePartiallyFailed  BackupPhase = "PartiallyFailed"
	BackupPhaseFailed           BackupPhase = "Failed"
	BackupPhaseFailedValidation BackupPhase = "FailedValidation"
	BackupPhaseDeleting         BackupPhase = "Deleting"
)

// BackupSpec is a subset of velero.io/v1 Backup spec.
type BackupSpec struct {
	// StorageLocation is the BackupStorageLocation this backup is stored in.
	// +optional
	StorageLocation string `json:"storageLocation,omitempty"`
	// IncludedNamespaces are the namespaces included in the backup.
	// +optional
	IncludedNamespaces []string `json:"includedNamespaces,omitempty"`
	// DefaultVolumesToFsBackup indicates File System Backup is the default.
	// +optional
	DefaultVolumesToFsBackup *bool `json:"defaultVolumesToFsBackup,omitempty"`
	// SnapshotVolumes indicates volume snapshots are taken.
	// +optional
	SnapshotVolumes *bool `json:"snapshotVolumes,omitempty"`
}

// BackupStatus is a subset of velero.io/v1 Backup status.
type BackupStatus struct {
	// Phase is the current backup phase.
	// +optional
	Phase BackupPhase `json:"phase,omitempty"`
	// StartTimestamp records when the backup started.
	// +optional
	StartTimestamp *metav1.Time `json:"startTimestamp,omitempty"`
	// CompletionTimestamp records when the backup finished.
	// +optional
	CompletionTimestamp *metav1.Time `json:"completionTimestamp,omitempty"`
	// Expiration is when the backup is eligible for garbage collection.
	// +optional
	Expiration *metav1.Time `json:"expiration,omitempty"`
}

// +kubebuilder:object:root=true

// Backup is a subset of velero.io/v1 Backup.
type Backup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BackupSpec   `json:"spec,omitempty"`
	Status BackupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BackupList contains a list of Backup.
type BackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Backup `json:"items"`
}

// ObjectStorageLocation describes the bucket/prefix for a BSL.
type ObjectStorageLocation struct {
	// Bucket is the object-storage bucket.
	Bucket string `json:"bucket"`
	// Prefix is the key prefix within the bucket.
	// +optional
	Prefix string `json:"prefix,omitempty"`
	// CACert is an optional CA bundle for TLS verification.
	// +optional
	CACert []byte `json:"caCert,omitempty"`
}

// StorageType is the storage backing for a BSL.
type StorageType struct {
	// ObjectStorage describes object-store backing.
	// +optional
	ObjectStorage *ObjectStorageLocation `json:"objectStorage,omitempty"`
}

// BackupStorageLocationSpec is a subset of velero.io/v1 BSL spec.
type BackupStorageLocationSpec struct {
	// Provider is the object-store provider (aws, gcp, azure, ...).
	Provider string `json:"provider"`
	// StorageType is the storage backing (object storage).
	StorageType `json:",inline"`
	// Config carries provider-specific settings (region, s3Url, s3ForcePathStyle, ...).
	// +optional
	Config map[string]string `json:"config,omitempty"`
	// Credential references the Secret key holding object-store credentials.
	// +optional
	Credential *corev1.SecretKeySelector `json:"credential,omitempty"`
	// Default marks the default BSL.
	// +optional
	Default bool `json:"default,omitempty"`
}

// BackupStorageLocationStatus is a subset of velero.io/v1 BSL status.
type BackupStorageLocationStatus struct {
	// Phase is the BSL availability phase (Available/Unavailable).
	// +optional
	Phase string `json:"phase,omitempty"`
}

// +kubebuilder:object:root=true

// BackupStorageLocation is a subset of velero.io/v1 BackupStorageLocation.
type BackupStorageLocation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BackupStorageLocationSpec   `json:"spec,omitempty"`
	Status BackupStorageLocationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BackupStorageLocationList contains a list of BackupStorageLocation.
type BackupStorageLocationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BackupStorageLocation `json:"items"`
}

func init() {
	SchemeBuilder.Register(
		&Backup{}, &BackupList{},
		&BackupStorageLocation{}, &BackupStorageLocationList{},
	)
}
