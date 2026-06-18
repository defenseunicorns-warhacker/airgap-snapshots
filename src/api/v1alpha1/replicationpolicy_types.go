package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SecretKeyRef references a key within a Secret.
type SecretKeyRef struct {
	// Name of the Secret.
	Name string `json:"name"`
	// Key within the Secret's data.
	Key string `json:"key"`
	// Namespace of the Secret. Defaults to the Velero namespace when empty.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// TargetSpec describes the destination peat-node peer to replicate to.
type TargetSpec struct {
	// EndpointID is the destination's hex-encoded Iroh endpoint ID. Operative field:
	// used for ConnectPeer (peering). v1 distributes with AllNodes scope, so the
	// source must peer only with intended destinations.
	EndpointID string `json:"endpointID"`
	// NodeID is the destination peat-node --node-id. Informational in v1 (scope is
	// AllNodes); relevant only for future NodeList targeting once peat's node
	// registry is populated.
	// +optional
	NodeID string `json:"nodeID,omitempty"`
	// Addresses are direct UDP host:port addresses for the destination peat-node.
	// Pin the destination's --iroh-udp-port so these stay stable.
	// +optional
	Addresses []string `json:"addresses,omitempty"`
	// RelayURL is an optional Iroh relay URL for NAT traversal.
	// +optional
	RelayURL string `json:"relayURL,omitempty"`
}

// PeatSpec describes how Snapback talks to its co-located peat-node sidecar.
type PeatSpec struct {
	// Address of the local peat-node gRPC endpoint.
	// +kubebuilder:default="localhost:50051"
	Address string `json:"address,omitempty"`
	// OutboxRoot is the peat --attachment-root name files are staged under.
	// +kubebuilder:default="outbox"
	OutboxRoot string `json:"outboxRoot,omitempty"`
	// OutboxPath is the filesystem mount path of that root inside this pod.
	// +kubebuilder:default="/var/lib/peat/outbox"
	OutboxPath string `json:"outboxPath,omitempty"`
}

// ObjectStoreSpec optionally overrides how Snapback reads the BSL bucket.
// When fields are empty, values are derived from the Velero BackupStorageLocation
// CR and its referenced credentials Secret.
type ObjectStoreSpec struct {
	// Endpoint overrides the object-store endpoint (e.g. https://minio.velero.svc:9000).
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
	// Region overrides the object-store region.
	// +optional
	Region string `json:"region,omitempty"`
	// CABundleSecretRef points at a CA bundle for TLS verification.
	// +optional
	CABundleSecretRef *SecretKeyRef `json:"caBundleSecretRef,omitempty"`
	// InsecureSkipTLSVerify disables TLS verification (discouraged).
	// +optional
	InsecureSkipTLSVerify bool `json:"insecureSkipTLSVerify,omitempty"`
	// CredentialsSecretRef overrides the BSL-derived credentials.
	// +optional
	CredentialsSecretRef *SecretKeyRef `json:"credentialsSecretRef,omitempty"`
}

// ReplicationPolicySpec defines what to replicate and where to.
type ReplicationPolicySpec struct {
	// Paused stops new replication work while true.
	// +optional
	Paused bool `json:"paused,omitempty"`

	// BackupStorageLocations selects source BSLs by name.
	// +optional
	BackupStorageLocations []string `json:"backupStorageLocations,omitempty"`
	// BackupStorageLocationSelector selects source BSLs by label.
	// +optional
	BackupStorageLocationSelector *metav1.LabelSelector `json:"backupStorageLocationSelector,omitempty"`
	// BackupSelector selects which Velero Backups qualify (empty = all).
	// +optional
	BackupSelector *metav1.LabelSelector `json:"backupSelector,omitempty"`

	// Target is the destination peat peer.
	Target TargetSpec `json:"target"`

	// Priority is the peat AttachmentPriority for these transfers.
	// +kubebuilder:validation:Enum=Bulk;Low;Routine;Priority;Critical
	// +kubebuilder:default=Bulk
	Priority string `json:"priority,omitempty"`

	// BundleByteLimit caps the total bytes per SendAttachments bundle.
	// Must be <= peat --attachment-max-bundle-bytes (default 1 GiB).
	// +kubebuilder:default=1073741824
	BundleByteLimit int64 `json:"bundleByteLimit,omitempty"`
	// BundleFileLimit caps the file count per bundle.
	// Must be <= peat --attachment-max-files-per-bundle (default 64).
	// +kubebuilder:default=64
	BundleFileLimit int32 `json:"bundleFileLimit,omitempty"`
	// MaxConcurrentBundles caps in-flight distributions.
	// Must be <= peat --attachment-max-concurrent-distributions (default 4).
	// +kubebuilder:default=4
	MaxConcurrentBundles int32 `json:"maxConcurrentBundles,omitempty"`

	// StaleAfter is how long to wait on a non-terminal bundle before re-issuing
	// SendAttachments (idempotent via deterministic bundle_id). Tune to expected
	// DDIL outage windows.
	// +optional
	StaleAfter *metav1.Duration `json:"staleAfter,omitempty"`

	// Peat configures the local peat-node sidecar connection.
	// +optional
	Peat PeatSpec `json:"peat,omitempty"`

	// ObjectStore optionally overrides BSL-derived object-store access.
	// +optional
	ObjectStore ObjectStoreSpec `json:"objectStore,omitempty"`
}

// ReplicationPolicyStatus is the observed state of a ReplicationPolicy.
type ReplicationPolicyStatus struct {
	// Conditions represent the latest observations (Ready, Degraded).
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// BackupsReplicated is the cumulative count of backups fully replicated.
	// +optional
	BackupsReplicated int64 `json:"backupsReplicated,omitempty"`
	// BytesReplicated is the cumulative bytes replicated.
	// +optional
	BytesReplicated int64 `json:"bytesReplicated,omitempty"`
	// ObservedGeneration is the last spec generation reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=rpol
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Paused",type=boolean,JSONPath=`.spec.paused`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.target.nodeID`
// +kubebuilder:printcolumn:name="Backups",type=integer,JSONPath=`.status.backupsReplicated`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ReplicationPolicy declares cross-cluster Velero backup replication via peat.
type ReplicationPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ReplicationPolicySpec   `json:"spec,omitempty"`
	Status ReplicationPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ReplicationPolicyList contains a list of ReplicationPolicy.
type ReplicationPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ReplicationPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ReplicationPolicy{}, &ReplicationPolicyList{})
}
