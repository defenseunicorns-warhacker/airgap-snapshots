// Copyright 2026 Defense Unicorns
// SPDX-License-Identifier: AGPL-3.0-or-later OR LicenseRef-Defense-Unicorns-Commercial

package controller

import (
	"context"
	"fmt"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	snapbackv1alpha1 "github.com/defenseunicorns/snapback/api/v1alpha1"
	"github.com/defenseunicorns/snapback/internal/importer"
	"github.com/defenseunicorns/snapback/internal/manifest"
	"github.com/defenseunicorns/snapback/internal/objstore"
	"github.com/defenseunicorns/snapback/internal/peat"
)

// ImportOptions is the destination-role configuration shared by the manifest
// bridge and the BackupImport reconciler. It comes from manager flags (chart
// values), not a CR — the per-backup CR carries only the manifest pointer.
type ImportOptions struct {
	// PeatAddress is the local peat sidecar gRPC endpoint.
	PeatAddress string
	// InboxRoot is the peat --attachment-inbox mount path.
	InboxRoot string
	// ManifestCollection is the peat document collection manifests live in.
	ManifestCollection string
	// Namespace is where BackupImport work items are created.
	Namespace string
	// PollInterval is how often the bridge polls the manifest collection.
	PollInterval time.Duration
	// DestStore describes the destination object store (a Velero BSL bucket).
	DestStore objstore.Config
	// SourceEndpointID / SourceAddresses / SourceRelayURL describe the source
	// peat peer for the reverse ConnectPeer (so the receiver can dial back and
	// pull). Empty SourceEndpointID means peering is bootstrapped manually.
	SourceEndpointID string
	SourceAddresses  []string
	SourceRelayURL   string
}

// ManifestBridge polls the peat snapback_replications collection and ensures a
// BackupImport work item exists for each committed manifest. It also issues the
// reverse ConnectPeer (destination -> source) so the receiver can dial back and
// pull blobs. Manager Runnable; role=destination only.
//
// Polling (vs Subscribe streaming) keeps the peat client unary-only; DDIL
// latencies are minutes-to-days, so a poll interval of tens of seconds is
// ample. PutDocument/CreateOrUpdate are idempotent, so re-polling is free.
type ManifestBridge struct {
	client.Client
	Opts ImportOptions
}

// NeedLeaderElection ties the bridge to the leader so a future multi-replica
// deployment runs exactly one poller. With leader election disabled (the
// single-replica default) it runs unconditionally.
func (b *ManifestBridge) NeedLeaderElection() bool { return true }

// Start runs the poll loop until ctx is cancelled.
func (b *ManifestBridge) Start(ctx context.Context) error {
	l := log.FromContext(ctx).WithName("manifest-bridge")
	pc, err := peat.New(b.Opts.PeatAddress)
	if err != nil {
		return fmt.Errorf("manifest bridge: dial peat: %w", err)
	}
	defer pc.Close()

	l.Info("starting", "collection", b.Opts.ManifestCollection, "interval", b.Opts.PollInterval.String())
	t := time.NewTicker(b.Opts.PollInterval)
	defer t.Stop()
	b.poll(ctx, pc) // initial pass without waiting a full interval
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			b.poll(ctx, pc)
		}
	}
}

func (b *ManifestBridge) poll(ctx context.Context, pc peat.Client) {
	l := log.FromContext(ctx).WithName("manifest-bridge")
	// Reverse peering is idempotent on the peat side; (re)issuing it each poll
	// recovers from a peat sidecar that wasn't ready at startup.
	b.ensureReversePeer(ctx, pc)

	ids, err := pc.ListDocuments(ctx, b.Opts.ManifestCollection)
	if err != nil {
		l.V(1).Info("list documents failed (will retry)", "err", err.Error())
		return
	}
	for _, id := range ids {
		data, found, err := pc.GetDocument(ctx, b.Opts.ManifestCollection, id)
		if err != nil {
			l.V(1).Info("get document failed", "doc", id, "err", err.Error())
			continue
		}
		if !found {
			continue
		}
		m, err := manifest.Parse(data)
		if err != nil {
			l.V(1).Info("skip unparseable manifest", "doc", id, "err", err.Error())
			continue
		}
		if !m.Complete {
			continue // wait for the source to commit
		}
		if err := b.ensureBackupImport(ctx, m); err != nil {
			l.Error(err, "ensure BackupImport failed", "doc", id)
		}
	}
}

// ensureReversePeer issues the destination -> source ConnectPeer so the
// receiver can dial back and pull blobs. Best-effort and idempotent; an empty
// SourceEndpointID means peering is bootstrapped out-of-band.
func (b *ManifestBridge) ensureReversePeer(ctx context.Context, pc peat.Client) {
	if b.Opts.SourceEndpointID == "" {
		return
	}
	if err := pc.EnsurePeer(ctx, b.Opts.SourceEndpointID, b.Opts.SourceAddresses, b.Opts.SourceRelayURL); err != nil {
		log.FromContext(ctx).WithName("manifest-bridge").V(1).Info("reverse ConnectPeer pending", "err", err.Error())
	}
}

func (b *ManifestBridge) ensureBackupImport(ctx context.Context, m *manifest.Manifest) error {
	bi := &snapbackv1alpha1.BackupImport{
		ObjectMeta: metav1.ObjectMeta{Name: importName(m.StorageLocation, m.BackupName), Namespace: b.Opts.Namespace},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, b.Client, bi, func() error {
		bi.Spec.BackupName = m.BackupName
		bi.Spec.StorageLocation = m.StorageLocation
		bi.Spec.ManifestDocID = manifest.DocID(m.StorageLocation, m.BackupName)
		return nil
	})
	return err
}

// importName derives a DNS-1123 BackupImport name. Velero backup names and BSL
// names are already DNS-safe; the BSL prefix keeps same-named backups from
// different BSLs distinct.
func importName(storageLocation, backupName string) string {
	n := fmt.Sprintf("%s-%s", storageLocation, backupName)
	if len(n) > 253 {
		n = n[:253]
	}
	return n
}

// BackupImportReconciler reconstructs replicated backups on the destination:
// fetch the manifest from the local peat document plane, verify each landed
// inbox file, and write it to the destination object store (velero-backup.json
// written last so the backup commits atomically). role=destination only.
type BackupImportReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Opts   ImportOptions
}

// +kubebuilder:rbac:groups=snapback.uds.dev,resources=backupimports,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=snapback.uds.dev,resources=backupimports/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=snapback.uds.dev,resources=backupimports/finalizers,verbs=update

// Reconcile advances one BackupImport toward Imported.
func (r *BackupImportReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	var bi snapbackv1alpha1.BackupImport
	if err := r.Get(ctx, req.NamespacedName, &bi); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !bi.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	switch bi.Status.Phase {
	case snapbackv1alpha1.ImportImported, snapbackv1alpha1.ImportFailed:
		return ctrl.Result{}, nil // terminal
	}

	// 1. Fetch the manifest from the local peat document plane.
	pc, err := peat.New(r.Opts.PeatAddress)
	if err != nil {
		return r.importTransient(ctx, &bi, "PeatDialFailed", err)
	}
	defer pc.Close()

	data, found, err := pc.GetDocument(ctx, r.Opts.ManifestCollection, bi.Spec.ManifestDocID)
	if err != nil {
		return r.importTransient(ctx, &bi, "ManifestFetchFailed", err)
	}
	if !found {
		r.setImportPhase(&bi, snapbackv1alpha1.ImportPending)
		return r.importWait(ctx, &bi, "ManifestNotSynced", "manifest document not yet synced from source")
	}
	m, err := manifest.Parse(data)
	if err != nil {
		// Unsupported schema / unparseable manifest is terminal: surface it
		// rather than retry-loop forever on data we can't act on.
		return r.importFailed(ctx, &bi, "ManifestParseFailed", err)
	}
	if !m.Complete {
		r.setImportPhase(&bi, snapbackv1alpha1.ImportPending)
		return r.importWait(ctx, &bi, "ManifestIncomplete", "source has not committed the manifest yet")
	}

	// 2. Build the destination object store.
	store, err := objstore.New(r.Opts.DestStore)
	if err != nil {
		return r.importTransient(ctx, &bi, "DestStoreInitFailed", err)
	}
	defer store.Close()

	// 3. Run one idempotent import pass.
	if bi.Status.StartedAt == nil {
		now := metav1.Now()
		bi.Status.StartedAt = &now
	}
	r.setImportPhase(&bi, snapbackv1alpha1.ImportImporting)
	bi.Status.FilesTotal = int64(len(m.Files))

	im := &importer.Importer{InboxRoot: r.Opts.InboxRoot, Store: store}
	res, err := im.Import(ctx, m)
	if err != nil {
		return r.importTransient(ctx, &bi, "ImportFailed", err)
	}
	bi.Status.FilesImported = int64(res.FilesImported)
	bi.Status.BytesImported = res.BytesImported

	if !res.Committed {
		// Some files haven't landed yet (DDIL); keep waiting without failing.
		apimeta.SetStatusCondition(&bi.Status.Conditions, metav1.Condition{
			Type: "Ready", Status: metav1.ConditionFalse, Reason: "AwaitingBytes",
			Message:            fmt.Sprintf("%d/%d files imported, %d pending", res.FilesImported, len(m.Files), res.Pending),
			ObservedGeneration: bi.Generation,
		})
		if err := r.Status().Update(ctx, &bi); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
	}

	// 4. Committed: the discovery object was written last, so destination Velero
	// can now auto-discover the backup. A verified import is a stronger signal
	// than peat's sender-side COMPLETED ("transmitted").
	now := metav1.Now()
	bi.Status.CompletedAt = &now
	bi.Status.Committed = true
	r.setImportPhase(&bi, snapbackv1alpha1.ImportImported)
	apimeta.SetStatusCondition(&bi.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionTrue, Reason: "Imported",
		Message:            "backup reconstructed into destination store and committed",
		ObservedGeneration: bi.Generation,
	})
	l.Info("backup imported", "backup", bi.Spec.BackupName, "files", res.FilesImported, "bytes", res.BytesImported)
	return ctrl.Result{}, r.Status().Update(ctx, &bi)
}

func (r *BackupImportReconciler) setImportPhase(bi *snapbackv1alpha1.BackupImport, p snapbackv1alpha1.ImportPhase) {
	bi.Status.Phase = p
	bi.Status.ObservedGeneration = bi.Generation
}

// importWait records a non-error "still waiting" condition and requeues.
func (r *BackupImportReconciler) importWait(ctx context.Context, bi *snapbackv1alpha1.BackupImport, reason, msg string) (ctrl.Result, error) {
	apimeta.SetStatusCondition(&bi.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionFalse, Reason: reason, Message: msg,
		ObservedGeneration: bi.Generation,
	})
	if err := r.Status().Update(ctx, bi); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
}

// importTransient records a retryable failure and requeues with backoff.
func (r *BackupImportReconciler) importTransient(ctx context.Context, bi *snapbackv1alpha1.BackupImport, reason string, cause error) (ctrl.Result, error) {
	bi.Status.LastError = cause.Error()
	bi.Status.RetryCount++
	apimeta.SetStatusCondition(&bi.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionFalse, Reason: reason, Message: cause.Error(),
		ObservedGeneration: bi.Generation,
	})
	if uerr := r.Status().Update(ctx, bi); uerr != nil {
		return ctrl.Result{}, uerr
	}
	return ctrl.Result{RequeueAfter: requeueAfterTransient}, nil
}

// importFailed records a terminal failure.
func (r *BackupImportReconciler) importFailed(ctx context.Context, bi *snapbackv1alpha1.BackupImport, reason string, cause error) (ctrl.Result, error) {
	bi.Status.LastError = cause.Error()
	r.setImportPhase(bi, snapbackv1alpha1.ImportFailed)
	apimeta.SetStatusCondition(&bi.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionFalse, Reason: reason, Message: cause.Error(),
		ObservedGeneration: bi.Generation,
	})
	return ctrl.Result{}, r.Status().Update(ctx, bi)
}

// SetupWithManager wires the BackupImport reconciler.
func (r *BackupImportReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&snapbackv1alpha1.BackupImport{}).
		Named("backupimport").
		Complete(r)
}
