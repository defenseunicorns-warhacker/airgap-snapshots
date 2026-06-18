// Copyright 2026 Defense Unicorns
// SPDX-License-Identifier: AGPL-3.0-or-later OR LicenseRef-Defense-Unicorns-Commercial

package controller

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	snapbackv1alpha1 "github.com/defenseunicorns/snapback/api/v1alpha1"
	velerov1 "github.com/defenseunicorns/snapback/api/velero/v1"
)

// BackupReconciler watches Velero Backups and, when a backup completes and
// matches a ReplicationPolicy, ensures a BackupReplication work item exists.
type BackupReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=velero.io,resources=backups,verbs=get;list;watch
// +kubebuilder:rbac:groups=velero.io,resources=backupstoragelocations,verbs=get;list;watch
// +kubebuilder:rbac:groups=snapback.uds.dev,resources=replicationpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=snapback.uds.dev,resources=backupreplications,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile ensures a BackupReplication exists for each completed, eligible backup.
func (r *BackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	var backup velerov1.Backup
	if err := r.Get(ctx, req.NamespacedName, &backup); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Only completed backups are replicated. Other phases are ignored; the
	// backup will re-trigger reconcile when it transitions.
	if backup.Status.Phase != velerov1.BackupPhaseCompleted {
		return ctrl.Result{}, nil
	}

	policy, err := r.matchPolicy(ctx, &backup)
	if err != nil {
		return ctrl.Result{}, err
	}
	if policy == nil {
		l.V(1).Info("no matching ReplicationPolicy", "backup", backup.Name)
		return ctrl.Result{}, nil
	}

	br := &snapbackv1alpha1.BackupReplication{
		ObjectMeta: metav1.ObjectMeta{
			Name:      backup.Name,
			Namespace: backup.Namespace,
		},
	}
	res, err := controllerutil.CreateOrUpdate(ctx, r.Client, br, func() error {
		br.Spec.BackupName = backup.Name
		br.Spec.PolicyName = policy.Name
		br.Spec.StorageLocation = backup.Spec.StorageLocation
		// GC the BackupReplication when the Backup is deleted.
		return controllerutil.SetControllerReference(&backup, br, r.Scheme)
	})
	if err != nil {
		return ctrl.Result{}, err
	}
	if res != controllerutil.OperationResultNone {
		l.Info("ensured BackupReplication", "backup", backup.Name, "policy", policy.Name, "op", res)
	}
	return ctrl.Result{}, nil
}

// matchPolicy returns the first non-paused ReplicationPolicy matching the backup,
// or nil. v1 supports a single destination per backup; multi-target is future work.
func (r *BackupReconciler) matchPolicy(ctx context.Context, backup *velerov1.Backup) (*snapbackv1alpha1.ReplicationPolicy, error) {
	var policies snapbackv1alpha1.ReplicationPolicyList
	if err := r.List(ctx, &policies); err != nil {
		return nil, err
	}
	for i := range policies.Items {
		p := &policies.Items[i]
		if p.Spec.Paused {
			continue
		}
		if !matchesBSL(p, backup.Spec.StorageLocation) {
			continue
		}
		ok, err := matchesBackup(p, backup)
		if err != nil {
			return nil, err
		}
		if ok {
			return p, nil
		}
	}
	return nil, nil
}

func matchesBSL(p *snapbackv1alpha1.ReplicationPolicy, bsl string) bool {
	if len(p.Spec.BackupStorageLocations) == 0 {
		// TODO(M3): support BackupStorageLocationSelector by looking up BSL labels.
		return true
	}
	for _, name := range p.Spec.BackupStorageLocations {
		if name == bsl {
			return true
		}
	}
	return false
}

func matchesBackup(p *snapbackv1alpha1.ReplicationPolicy, backup *velerov1.Backup) (bool, error) {
	if p.Spec.BackupSelector == nil {
		return true, nil
	}
	sel, err := metav1.LabelSelectorAsSelector(p.Spec.BackupSelector)
	if err != nil {
		return false, err
	}
	return sel.Matches(labels.Set(backup.Labels)), nil
}

// SetupWithManager wires the Backup watch.
func (r *BackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&velerov1.Backup{}).
		Named("backup").
		Complete(r)
}
