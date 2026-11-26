package controller

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	fleetv1alpha1 "github.com/NilsRuiz/orbit/operator/api/v1alpha1"
	"github.com/NilsRuiz/orbit/operator/internal/fleet"
)

// FleetReconciler resolves a Fleet's selector and reports its members by reachability.
type FleetReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=fleet.orbit.io,resources=fleets,verbs=get;list;watch
// +kubebuilder:rbac:groups=fleet.orbit.io,resources=fleets/status,verbs=get;patch

// Reconcile counts the EdgeNodes the selector matches. An invalid selector is reported
// in the Ready condition rather than retried: only a spec change can fix it.
func (r *FleetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	f := &fleetv1alpha1.Fleet{}
	if err := r.Get(ctx, req.NamespacedName, f); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	next := f.Status.DeepCopy()
	next.ObservedGeneration = f.Generation

	sel, err := metav1.LabelSelectorAsSelector(&f.Spec.Selector)
	if err != nil {
		next.Nodes, next.Reachable, next.Unreachable = 0, 0, 0
		meta.SetStatusCondition(&next.Conditions, metav1.Condition{
			Type: fleetv1alpha1.FleetReady, Status: metav1.ConditionFalse,
			Reason: fleetv1alpha1.ReasonInvalidSelector, Message: err.Error(),
			ObservedGeneration: f.Generation,
		})
		logf.FromContext(ctx).Info("invalid fleet selector", "fleet", f.Name, "err", err.Error())
		return ctrl.Result{}, r.patchStatus(ctx, f, next)
	}

	nodes := &fleetv1alpha1.EdgeNodeList{}
	if err := r.List(ctx, nodes, client.MatchingLabelsSelector{Selector: sel}); err != nil {
		return ctrl.Result{}, fmt.Errorf("list edge nodes: %w", err)
	}
	c := fleet.Count(nodes.Items)
	next.Nodes, next.Reachable, next.Unreachable = c.Nodes, c.Reachable, c.Unreachable
	meta.SetStatusCondition(&next.Conditions, metav1.Condition{
		Type: fleetv1alpha1.FleetReady, Status: metav1.ConditionTrue,
		Reason: fleetv1alpha1.ReasonSelectorResolved, Message: fmt.Sprintf("selector %q", sel.String()),
		ObservedGeneration: f.Generation,
	})
	return ctrl.Result{}, r.patchStatus(ctx, f, next)
}

// patchStatus writes next only if it changed: most reconciles find the counts unchanged.
func (r *FleetReconciler) patchStatus(ctx context.Context, f *fleetv1alpha1.Fleet, next *fleetv1alpha1.FleetStatus) error {
	if equality.Semantic.DeepEqual(f.Status, *next) {
		return nil
	}
	base := f.DeepCopy()
	f.Status = *next
	if err := r.Status().Patch(ctx, f, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("patch fleet status: %w", err)
	}
	return nil
}

// SetupWithManager registers the reconciler with the manager.
func (r *FleetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return setupFleetController(mgr, r.Client, r)
}

// setupFleetController wires rec (the FleetReconciler, or a wrapper in tests).
//
// An EdgeNode change wakes the fleets that select the node. On an update, the map
// function runs on both the old and the new object, so a relabelled node wakes the fleet
// it leaves as well as the one it joins.
func setupFleetController(mgr ctrl.Manager, c client.Client, rec reconcile.Reconciler) error {
	selectingFleets := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		fleets := &fleetv1alpha1.FleetList{}
		if err := c.List(ctx, fleets); err != nil {
			logf.FromContext(ctx).Error(err, "list fleets for an EdgeNode change")
			return nil
		}
		var reqs []reconcile.Request
		for i := range fleets.Items {
			if fleet.Selects(&fleets.Items[i], obj.GetLabels()) {
				reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&fleets.Items[i])})
			}
		}
		return reqs
	})
	edgeNodeChanged := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			old, okOld := e.ObjectOld.(*fleetv1alpha1.EdgeNode)
			updated, okNew := e.ObjectNew.(*fleetv1alpha1.EdgeNode)
			return okOld && okNew && fleet.RelevantChange(old, updated)
		},
	}
	return ctrl.NewControllerManagedBy(mgr).
		// Status writes bump no generation: the controller does not wake itself up.
		For(&fleetv1alpha1.Fleet{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&fleetv1alpha1.EdgeNode{}, selectingFleets, builder.WithPredicates(edgeNodeChanged)).
		Named("fleet").
		Complete(rec)
}
