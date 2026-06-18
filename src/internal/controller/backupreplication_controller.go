// Copyright 2026 Defense Unicorns
// SPDX-License-Identifier: AGPL-3.0-or-later OR LicenseRef-Defense-Unicorns-Commercial

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	snapbackv1alpha1 "github.com/defenseunicorns/snapback/api/v1alpha1"
	velerov1 "github.com/defenseunicorns/snapback/api/velero/v1"
	"github.com/defenseunicorns/snapback/internal/batching"
	"github.com/defenseunicorns/snapback/internal/objstore"
	"github.com/defenseunicorns/snapback/internal/peat"
	"github.com/defenseunicorns/snapback/internal/staging"
)

// requeueAfterTransient is how long to wait before retrying transient errors so
// a stub/unavailable backend does not hot-loop.
const requeueAfterTransient = 30 * time.Second

// BackupReplicationReconciler drives the per-backup replication state machine:
// Pending -> Staging -> Transmitting -> Replicated (or Failed/Skipped).
type BackupReplicationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=snapback.uds.dev,resources=backupreplications,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=snapback.uds.dev,resources=backupreplications/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=snapback.uds.dev,resources=backupreplications/finalizers,verbs=update
// +kubebuilder:rbac:groups=snapback.uds.dev,resources=replicationpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=snapback.uds.dev,resources=replicationpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=velero.io,resources=backupstoragelocations,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile advances one BackupReplication toward Replicated.
func (r *BackupReplicationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	var br snapbackv1alpha1.BackupReplication
	if err := r.Get(ctx, req.NamespacedName, &br); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// TODO(M3): add a finalizer to release outbox files when a BackupReplication
	// is deleted before its bundles complete.
	if !br.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	switch br.Status.Phase {
	case snapbackv1alpha1.PhaseReplicated,
		snapbackv1alpha1.PhaseFailed,
		snapbackv1alpha1.PhaseSkipped:
		return ctrl.Result{}, nil // terminal
	}

	var policy snapbackv1alpha1.ReplicationPolicy
	if err := r.Get(ctx, types.NamespacedName{Name: br.Spec.PolicyName}, &policy); err != nil {
		if apierrors.IsNotFound(err) {
			return r.degrade(ctx, &br, "PolicyNotFound",
				fmt.Sprintf("ReplicationPolicy %q not found", br.Spec.PolicyName))
		}
		return ctrl.Result{}, err
	}
	if policy.Spec.Paused {
		l.V(1).Info("policy paused, skipping", "policy", policy.Name)
		return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
	}

	return r.replicate(ctx, &br, &policy)
}

// replicate runs one pass of enumerate -> stage -> batch -> send -> wait.
// It is safe to call repeatedly: peat's idempotent bundle_id makes re-sends
// no-ops, and staging is overwrite-safe.
func (r *BackupReplicationReconciler) replicate(ctx context.Context, br *snapbackv1alpha1.BackupReplication, policy *snapbackv1alpha1.ReplicationPolicy) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	if br.Status.StartedAt == nil {
		now := metav1.Now()
		br.Status.StartedAt = &now
	}
	r.setPhase(br, snapbackv1alpha1.PhaseStaging)

	// 1. Resolve object-store access from the BSL.
	cfg, err := r.resolveStore(ctx, br, policy)
	if err != nil {
		return r.degrade(ctx, br, "ResolveStoreFailed", err.Error())
	}
	store, err := objstore.New(cfg)
	if err != nil {
		return r.degrade(ctx, br, "StoreInitFailed", err.Error())
	}
	defer store.Close()

	// 2. Enumerate the delta object set (metadata + new Kopia/Restic objects).
	keys, err := r.enumerateDelta(ctx, store, br)
	if err != nil {
		return r.transientErr(ctx, br, "EnumerateFailed", err)
	}
	if len(keys) == 0 {
		// Nothing to ship (e.g. snapshot-only backup with no FSB data).
		r.setPhase(br, snapbackv1alpha1.PhaseSkipped)
		apimeta.SetStatusCondition(&br.Status.Conditions, metav1.Condition{
			Type: "Ready", Status: metav1.ConditionFalse, Reason: "NothingToReplicate",
			Message:            "no replicable objects found for backup (snapshot-only?)",
			ObservedGeneration: br.Generation,
		})
		return ctrl.Result{}, r.Status().Update(ctx, br)
	}

	// 3. Stage each object into the peat outbox, computing size + sha256.
	stager := staging.New(policy.Spec.Peat.OutboxRoot, policy.Spec.Peat.OutboxPath)
	var specs []peat.FileSpec
	var total uint64
	for _, k := range keys {
		fs, serr := stager.StageObject(ctx, store, k)
		if serr != nil {
			return r.transientErr(ctx, br, "StageFailed", serr)
		}
		specs = append(specs, fs)
		total += fs.Size
	}

	// 4. Batch under peat's per-bundle caps.
	batches := batching.Pack(specs, int(policy.Spec.BundleFileLimit), uint64(policy.Spec.BundleByteLimit))
	br.Status.ObjectCount = int64(len(specs))
	br.Status.TotalBytes = int64(total)
	r.setPhase(br, snapbackv1alpha1.PhaseTransmitting)
	if err := r.Status().Update(ctx, br); err != nil {
		return ctrl.Result{}, err
	}

	// 5. Hand bundles to peat and wait for terminal status.
	pc, err := peat.New(policy.Spec.Peat.Address)
	if err != nil {
		return r.transientErr(ctx, br, "PeatDialFailed", err)
	}
	defer pc.Close()

	if err := pc.EnsurePeer(ctx, policy.Spec.Target.EndpointID, policy.Spec.Target.Addresses, policy.Spec.Target.RelayURL); err != nil {
		return r.transientErr(ctx, br, "PeerConnectFailed", err)
	}

	// v1 uses AllNodes: in a single-destination topology the source peers only
	// with the destination (via EnsurePeer above), so AllNodes targets exactly it.
	// NodeList is unusable without a populated peat node registry — see the
	// internal/peat Scope docs. (M3 finding.)
	scope := peat.Scope{AllNodes: true}
	prio := mapPriority(policy.Spec.Priority)

	bundleStatuses := make([]snapbackv1alpha1.BundleStatus, 0, len(batches))
	allDone := true
	for i, b := range batches {
		bundleID := fmt.Sprintf("%s-%04d", br.Name, i)
		bs := snapbackv1alpha1.BundleStatus{
			BundleID:  bundleID,
			FileCount: int32(len(b.Files)),
			Bytes:     int64(b.Bytes),
			Phase:     snapbackv1alpha1.BundlePending,
		}

		if _, err := pc.SendAttachments(ctx, bundleID, b.Files, scope, prio); err != nil {
			return r.transientErr(ctx, br, "SendFailed", err)
		}
		// WaitBundle blocks until terminal. TODO(M3): make this incremental and
		// time-boxed (RequeueAfter) so reconciles don't block on long DDIL waits,
		// and re-send on NOT_FOUND after a peat-node restart.
		progs, err := pc.WaitBundle(ctx, bundleID)
		if err != nil {
			return r.transientErr(ctx, br, "WaitFailed", err)
		}
		bs.Phase, bs.BytesTransferred, bs.DistributionIDs = summarize(progs)
		if bs.Phase != snapbackv1alpha1.BundleCompleted {
			allDone = false
		}
		bundleStatuses = append(bundleStatuses, bs)
	}
	br.Status.Bundles = bundleStatuses

	if !allDone {
		return ctrl.Result{RequeueAfter: requeueAfterTransient}, r.Status().Update(ctx, br)
	}

	// 6. All bundles COMPLETED (sender-side). Commit the ledger and finish.
	if err := r.commitLedger(ctx, store, br, keys); err != nil {
		return r.transientErr(ctx, br, "LedgerCommitFailed", err)
	}
	now := metav1.Now()
	br.Status.CompletedAt = &now
	r.setPhase(br, snapbackv1alpha1.PhaseReplicated)
	apimeta.SetStatusCondition(&br.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionTrue, Reason: "Replicated",
		Message:            "all bundles transmitted to destination peer",
		ObservedGeneration: br.Generation,
	})
	l.Info("backup replicated", "backup", br.Spec.BackupName, "objects", br.Status.ObjectCount, "bytes", br.Status.TotalBytes)
	return ctrl.Result{}, r.Status().Update(ctx, br)
}

// resolveStore builds object-store config from the Velero BSL and credentials.
func (r *BackupReplicationReconciler) resolveStore(ctx context.Context, br *snapbackv1alpha1.BackupReplication, policy *snapbackv1alpha1.ReplicationPolicy) (objstore.Config, error) {
	var bsl velerov1.BackupStorageLocation
	if err := r.Get(ctx, types.NamespacedName{Name: br.Spec.StorageLocation, Namespace: br.Namespace}, &bsl); err != nil {
		return objstore.Config{}, fmt.Errorf("get BSL %q: %w", br.Spec.StorageLocation, err)
	}
	if bsl.Spec.ObjectStorage == nil {
		return objstore.Config{}, fmt.Errorf("BSL %q has no objectStorage", bsl.Name)
	}

	cfg := objstore.Config{
		Bucket: bsl.Spec.ObjectStorage.Bucket,
		Prefix: bsl.Spec.ObjectStorage.Prefix,
	}
	if c := bsl.Spec.Config; c != nil {
		cfg.Region = c["region"]
		cfg.Endpoint = c["s3Url"]
		cfg.ForcePathStyle = c["s3ForcePathStyle"] == "true"
	}
	// Policy overrides.
	if os := policy.Spec.ObjectStore; true {
		if os.Endpoint != "" {
			cfg.Endpoint = os.Endpoint
		}
		if os.Region != "" {
			cfg.Region = os.Region
		}
		cfg.InsecureTLS = os.InsecureSkipTLSVerify
	}

	// Credentials: policy override wins, else the BSL's credential ref. Velero
	// stores them as an AWS shared-credentials file in the referenced secret key.
	var credBytes []byte
	credRef := policy.Spec.ObjectStore.CredentialsSecretRef
	if credRef != nil && credRef.Name != "" {
		ns := credRef.Namespace
		if ns == "" {
			ns = br.Namespace
		}
		b, err := r.getSecretKey(ctx, ns, credRef.Name, credRef.Key)
		if err != nil {
			return objstore.Config{}, err
		}
		credBytes = b
	} else if bsl.Spec.Credential != nil {
		b, err := r.getSecretKey(ctx, br.Namespace, bsl.Spec.Credential.Name, bsl.Spec.Credential.Key)
		if err != nil {
			return objstore.Config{}, err
		}
		credBytes = b
	}
	if len(credBytes) > 0 {
		profile := ""
		if c := bsl.Spec.Config; c != nil {
			profile = c["profile"]
		}
		ak, sk, token, err := objstore.ParseAWSCredentials(credBytes, profile)
		if err != nil {
			return objstore.Config{}, fmt.Errorf("BSL %q credentials: %w", bsl.Name, err)
		}
		cfg.AccessKeyID, cfg.SecretAccessKey, cfg.SessionToken = ak, sk, token
	}
	return cfg, nil
}

func (r *BackupReplicationReconciler) getSecretKey(ctx context.Context, ns, name, key string) ([]byte, error) {
	var s corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &s); err != nil {
		return nil, fmt.Errorf("get secret %s/%s: %w", ns, name, err)
	}
	v, ok := s.Data[key]
	if !ok {
		return nil, fmt.Errorf("secret %s/%s missing key %q", ns, name, key)
	}
	return v, nil
}

// ledgerEntry is one replicated object recorded in the per-BSL ledger.
type ledgerEntry struct {
	ETag string `json:"etag"`
	Size int64  `json:"size"`
}

func ledgerKey(bsl string) string { return fmt.Sprintf("snapback/ledger/%s.json", bsl) }

// enumerateDelta lists the backup's metadata objects plus the Kopia/Restic
// objects not already in the per-BSL ledger.
func (r *BackupReplicationReconciler) enumerateDelta(ctx context.Context, store objstore.Store, br *snapbackv1alpha1.BackupReplication) ([]string, error) {
	body, _, err := store.GetLedger(ctx, ledgerKey(br.Spec.StorageLocation))
	if err != nil {
		return nil, fmt.Errorf("load ledger: %w", err)
	}
	ledger := map[string]ledgerEntry{}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &ledger); err != nil {
			return nil, fmt.Errorf("parse ledger: %w", err)
		}
	}

	var keys []string
	// Backup metadata objects are always shipped (small, per-backup).
	metaObjs, err := store.List(ctx, fmt.Sprintf("backups/%s/", br.Spec.BackupName))
	if err != nil {
		return nil, fmt.Errorf("list backup metadata: %w", err)
	}
	for _, o := range metaObjs {
		keys = append(keys, o.Key)
	}
	// Kopia (and Restic) repo objects: ship only what the ledger hasn't seen.
	for _, prefix := range []string{"kopia/", "restic/"} {
		objs, err := store.List(ctx, prefix)
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", prefix, err)
		}
		for _, o := range objs {
			if e, seen := ledger[o.Key]; seen && e.ETag == o.ETag {
				continue
			}
			keys = append(keys, o.Key)
		}
	}
	return keys, nil
}

// commitLedger records the shipped keys so future backups don't re-ship them.
func (r *BackupReplicationReconciler) commitLedger(ctx context.Context, store objstore.Store, br *snapbackv1alpha1.BackupReplication, keys []string) error {
	key := ledgerKey(br.Spec.StorageLocation)
	body, etag, err := store.GetLedger(ctx, key)
	if err != nil {
		return err
	}
	ledger := map[string]ledgerEntry{}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &ledger); err != nil {
			return err
		}
	}
	for _, k := range keys {
		// TODO(M3): carry the real ETag/size from enumeration into the ledger.
		ledger[k] = ledgerEntry{}
	}
	out, err := json.Marshal(ledger)
	if err != nil {
		return err
	}
	_, err = store.PutLedger(ctx, key, out, etag)
	return err
}

func (r *BackupReplicationReconciler) setPhase(br *snapbackv1alpha1.BackupReplication, p snapbackv1alpha1.ReplicationPhase) {
	br.Status.Phase = p
	br.Status.ObservedGeneration = br.Generation
}

// degrade records a (likely durable) failure and stops fast requeues.
func (r *BackupReplicationReconciler) degrade(ctx context.Context, br *snapbackv1alpha1.BackupReplication, reason, msg string) (ctrl.Result, error) {
	br.Status.LastError = msg
	apimeta.SetStatusCondition(&br.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionFalse, Reason: reason, Message: msg,
		ObservedGeneration: br.Generation,
	})
	if err := r.Status().Update(ctx, br); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
}

// transientErr records a retryable failure and requeues with backoff.
func (r *BackupReplicationReconciler) transientErr(ctx context.Context, br *snapbackv1alpha1.BackupReplication, reason string, cause error) (ctrl.Result, error) {
	br.Status.LastError = cause.Error()
	br.Status.RetryCount++
	apimeta.SetStatusCondition(&br.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionFalse, Reason: reason, Message: cause.Error(),
		ObservedGeneration: br.Generation,
	})
	if uerr := r.Status().Update(ctx, br); uerr != nil {
		return ctrl.Result{}, uerr
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
}

func mapPriority(p string) peat.Priority {
	switch p {
	case "Low":
		return peat.PriorityLow
	case "Routine":
		return peat.PriorityRoutine
	case "Priority":
		return peat.PriorityPriority
	case "Critical":
		return peat.PriorityCritical
	default:
		return peat.PriorityBulk
	}
}

// summarize folds per-distribution progress into a bundle-level status.
func summarize(progs []peat.Progress) (snapbackv1alpha1.BundlePhase, int64, []string) {
	var bytes int64
	ids := make([]string, 0, len(progs))
	allCompleted := true
	anyFailed := false
	for _, p := range progs {
		bytes += int64(p.BytesTransferred)
		ids = append(ids, p.DistributionID)
		if p.Status != peat.StatusCompleted {
			allCompleted = false
		}
		if p.Status == peat.StatusFailed || p.Status == peat.StatusCancelled {
			anyFailed = true
		}
	}
	switch {
	case anyFailed:
		return snapbackv1alpha1.BundleFailed, bytes, ids
	case allCompleted && len(progs) > 0:
		return snapbackv1alpha1.BundleCompleted, bytes, ids
	default:
		return snapbackv1alpha1.BundleInProgress, bytes, ids
	}
}

// SetupWithManager wires the BackupReplication reconciler.
func (r *BackupReplicationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&snapbackv1alpha1.BackupReplication{}).
		Named("backupreplication").
		Complete(r)
}
