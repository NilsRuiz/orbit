// Package controller holds the operator's reconcilers.
package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	fleetv1alpha1 "github.com/NilsRuiz/orbit/operator/api/v1alpha1"
	"github.com/NilsRuiz/orbit/operator/internal/edgenode"
)

// EdgeNodeReconciler marks EdgeNodes Unreachable once they have been silent for
// 2 × spec.contactInterval. Contact itself (Reachable, lastSeen) is written by the
// ingester (internal/ingest), which reads agent messages.
type EdgeNodeReconciler struct {
	client.Client
	Recorder events.EventRecorder
	// Now is the hub clock; time.Now by default.
	Now func() time.Time
}

// The ingester shares these permissions: it creates EdgeNodes, patches their labels and
// status, and records events.
// +kubebuilder:rbac:groups=fleet.orbit.io,resources=edgenodes,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups=fleet.orbit.io,resources=edgenodes/status,verbs=get;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile recomputes reachability and requeues for when the node would turn
// Unreachable. Each write by the ingester triggers a new reconcile, which pushes the
// deadline back.
func (r *EdgeNodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	en := &fleetv1alpha1.EdgeNode{}
	if err := r.Get(ctx, req.NamespacedName, en); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	var lastSeen time.Time
	if en.Status.LastSeen != nil {
		lastSeen = en.Status.LastSeen.Time
	}
	reach, after := edgenode.Reachability(lastSeen, en.Spec.ContactInterval.Duration, now())
	if reach == en.Status.Reachability {
		return ctrl.Result{RequeueAfter: after}, nil
	}

	base := en.DeepCopy()
	prev := en.Status.Reachability
	en.Status.Reachability = reach
	// Optimistic lock: if the ingester wrote a fresh contact meanwhile, the conflict
	// requeues and the next pass sees it.
	if err := r.Status().Patch(ctx, en, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch reachability: %w", err)
	}
	if prev == fleetv1alpha1.Reachable && reach == fleetv1alpha1.Unreachable {
		// Not a failure: a satellite out of range is the normal case.
		silent := now().Sub(lastSeen).Round(time.Second)
		logf.FromContext(ctx).Info("edge node out of contact", "node", en.Name, "silentFor", silent.String())
		r.Recorder.Eventf(en, nil, corev1.EventTypeNormal, edgenode.ReasonContactLost, "Contact",
			"No contact for %s (more than 2 × contactInterval %s)", silent, en.Spec.ContactInterval.Duration)
	}
	return ctrl.Result{RequeueAfter: after}, nil
}

// SetupWithManager registers the reconciler with the manager.
func (r *EdgeNodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&fleetv1alpha1.EdgeNode{}).
		Named("edgenode").
		Complete(r)
}
