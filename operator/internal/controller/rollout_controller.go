package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	fleetv1alpha1 "github.com/NilsRuiz/orbit/operator/api/v1alpha1"
	"github.com/NilsRuiz/orbit/operator/internal/desired"
	"github.com/NilsRuiz/orbit/operator/internal/fleet"
	"github.com/NilsRuiz/orbit/operator/internal/rollout"
)

// DesiredStore writes nodes' desired states (internal/desired.Store).
type DesiredStore interface {
	Ensure(ctx context.Context, node, version, rolloutID, fallbackPrevious string) (bool, error)
	Revert(ctx context.Context, node, rolloutID string) (bool, error)
}

// conflictRetry is how soon a reconcile retries after a concurrent desired-state write.
const conflictRetry = time.Second

// RolloutReconciler moves a fleet to spec.targetVersion wave by wave (ADR-0004):
//
//	Pending ─► Progressing(wave i) ─► gate ─┬─ Promote ─► soak (pauseFor) ─► wave i+1 … ─► Succeeded
//	                                        └─ Halt ─► revert wave i ─► Halted
//
// Every reconcile recomputes the plan and the gate from the cluster and the bucket, so it
// can resume anywhere; the status keeps only where the run is (wave, timers).
type RolloutReconciler struct {
	client.Client
	// APIReader reads the Rollout uncached. The cache may not hold this controller's own
	// last status write yet: acting on it once re-wrote a bad version over a revert.
	APIReader client.Reader
	Recorder  events.EventRecorder
	// Store returns the desired-state store; it may fail until the hub's bucket exists.
	Store func(context.Context) (DesiredStore, error)
	// Now is the hub clock; time.Now by default.
	Now func() time.Time
}

// +kubebuilder:rbac:groups=fleet.orbit.io,resources=rollouts,verbs=get;list;watch
// +kubebuilder:rbac:groups=fleet.orbit.io,resources=rollouts/status,verbs=get;patch

// Reconcile advances the rollout as far as the gate allows.
func (r *RolloutReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ro := &fleetv1alpha1.Rollout{}
	if err := r.APIReader.Get(ctx, req.NamespacedName, ro); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	run := &rolloutRun{r: r, ro: ro, st: ro.Status.DeepCopy(), now: r.now()}
	res, err := run.reconcile(ctx)
	if perr := r.patchStatus(ctx, ro, run.st); perr != nil {
		return ctrl.Result{}, errors.Join(err, perr)
	}
	if errors.Is(err, desired.ErrConflict) {
		logf.FromContext(ctx).Info("desired state changed concurrently, retrying", "err", err.Error())
		return ctrl.Result{RequeueAfter: conflictRetry}, nil
	}
	return res, err
}

func (r *RolloutReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *RolloutReconciler) patchStatus(ctx context.Context, ro *fleetv1alpha1.Rollout, next *fleetv1alpha1.RolloutStatus) error {
	if equality.Semantic.DeepEqual(ro.Status, *next) {
		return nil
	}
	base := ro.DeepCopy()
	ro.Status = *next
	if err := r.Status().Patch(ctx, ro, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("patch rollout status: %w", err)
	}
	return nil
}

// rolloutRun is one reconcile of one rollout.
type rolloutRun struct {
	r   *RolloutReconciler
	ro  *fleetv1alpha1.Rollout
	st  *fleetv1alpha1.RolloutStatus
	now time.Time
}

func (x *rolloutRun) reconcile(ctx context.Context) (ctrl.Result, error) {
	spec := x.ro.Spec
	x.st.ObservedGeneration = x.ro.Generation
	// A new target (or fleet) is a new run, from the first wave, with its own id so that
	// nodes' reports about earlier runs are not counted.
	if x.st.RolloutID == "" || x.st.TargetVersion != spec.TargetVersion || x.st.FleetRef != spec.FleetRef {
		*x.st = fleetv1alpha1.RolloutStatus{
			ObservedGeneration: x.ro.Generation,
			RolloutID:          fmt.Sprintf("%s-g%d", x.ro.Name, x.ro.Generation),
			TargetVersion:      spec.TargetVersion,
			FleetRef:           spec.FleetRef,
			Phase:              fleetv1alpha1.RolloutPending,
			Conditions:         x.st.Conditions,
		}
	}
	if x.st.Phase == fleetv1alpha1.RolloutSucceeded || x.st.Phase == fleetv1alpha1.RolloutHalted {
		return ctrl.Result{}, nil // final until targetVersion or fleetRef change
	}

	nodes, ok, err := x.fleetNodes(ctx)
	if err != nil || !ok {
		return ctrl.Result{}, err
	}
	names := make([]string, 0, len(nodes))
	for name := range nodes {
		names = append(names, name)
	}
	percents := make([]int32, len(spec.Waves))
	for i, w := range spec.Waves {
		percents[i] = w.Percent
	}
	plan := rollout.PlanWaves(names, percents, x.ro.Name)
	last := len(plan) - 1
	// Waves may be edited during a run: stay within the current list.
	x.st.CurrentWave = min(x.st.CurrentWave, int32(last))
	x.st.Nodes = int32(len(names))

	if x.st.WaveStartTime == nil {
		x.startWave(int(x.st.CurrentWave), plan)
	}

	for {
		cur := int(x.st.CurrentWave)
		if x.st.SoakUntil != nil {
			if spec.Paused {
				x.paused()
				return ctrl.Result{}, nil
			}
			if wait := x.st.SoakUntil.Sub(x.now); wait > 0 {
				x.progressing(fleetv1alpha1.ReasonProgressing, fmt.Sprintf("wave %d passed, soaking until the next wave", cur))
				return ctrl.Result{RequeueAfter: wait}, nil
			}
			x.st.SoakUntil = nil
			x.st.CurrentWave++
			cur++
			x.startWave(cur, plan)
		}

		started := startedNodes(plan, cur)
		gate, nextDeadline := x.evaluate(started, nodes)

		if gate.Decision == rollout.Halt {
			// Halting is allowed even when paused: safety comes before a manual hold.
			return ctrl.Result{}, x.halt(ctx, plan[cur], gate)
		}
		if spec.Paused {
			x.paused()
			return ctrl.Result{}, nil
		}
		if err := x.ensure(ctx, started, nodes); err != nil {
			return ctrl.Result{}, err
		}

		if gate.Decision == rollout.Wait {
			x.progressing(fleetv1alpha1.ReasonProgressing, fmt.Sprintf("wave %d: waiting for %d nodes in progress, %d pending", cur, gate.InProgress, gate.Pending))
			if nextDeadline.IsZero() {
				return ctrl.Result{}, nil // only pending nodes: their EdgeNode changes wake us
			}
			return ctrl.Result{RequeueAfter: max(nextDeadline.Sub(x.now), time.Second)}, nil
		}

		// Promote.
		if cur < last {
			x.event(corev1.EventTypeNormal, "WavePromoted", "Promote", "Wave %d passed the health gate: %d updated, %d failed, %d pending",
				cur, gate.Succeeded, gate.Failed, gate.Pending)
			pause := time.Duration(0)
			if p := spec.Waves[cur].PauseFor; p != nil {
				pause = p.Duration
			}
			x.st.SoakUntil = &metav1.Time{Time: x.now.Add(pause)}
			continue
		}
		if gate.Pending > 0 {
			// Out of range is not a failure, but the run is not done until they update.
			x.progressing(fleetv1alpha1.ReasonWaitingPending, fmt.Sprintf("%d nodes out of range still to update", gate.Pending))
			return ctrl.Result{}, nil
		}
		x.st.Phase = fleetv1alpha1.RolloutSucceeded
		x.setReady(metav1.ConditionTrue, fleetv1alpha1.ReasonRolloutComplete,
			fmt.Sprintf("%d nodes on %s", gate.Succeeded, spec.TargetVersion))
		x.event(corev1.EventTypeNormal, "Succeeded", "Complete", "%d nodes on %s", gate.Succeeded, spec.TargetVersion)
		return ctrl.Result{}, nil
	}
}

// fleetNodes returns the fleet's EdgeNodes by name, or ok false (status updated) if the
// fleet cannot be resolved yet. The Fleet watch wakes the rollout when that changes.
func (x *rolloutRun) fleetNodes(ctx context.Context) (map[string]*fleetv1alpha1.EdgeNode, bool, error) {
	f := &fleetv1alpha1.Fleet{}
	if err := x.r.Get(ctx, client.ObjectKey{Name: x.ro.Spec.FleetRef}, f); err != nil {
		if apierrors.IsNotFound(err) {
			x.st.Phase = fleetv1alpha1.RolloutPending
			x.setReady(metav1.ConditionFalse, fleetv1alpha1.ReasonFleetNotFound, fmt.Sprintf("fleet %q not found", x.ro.Spec.FleetRef))
			return nil, false, nil
		}
		return nil, false, err
	}
	sel, err := metav1.LabelSelectorAsSelector(&f.Spec.Selector)
	if err != nil {
		x.st.Phase = fleetv1alpha1.RolloutPending
		x.setReady(metav1.ConditionFalse, fleetv1alpha1.ReasonFleetNotFound, fmt.Sprintf("fleet %q has an invalid selector: %v", f.Name, err))
		return nil, false, nil
	}
	list := &fleetv1alpha1.EdgeNodeList{}
	if err := x.r.List(ctx, list, client.MatchingLabelsSelector{Selector: sel}); err != nil {
		return nil, false, fmt.Errorf("list edge nodes: %w", err)
	}
	nodes := make(map[string]*fleetv1alpha1.EdgeNode, len(list.Items))
	for i := range list.Items {
		nodes[list.Items[i].Name] = &list.Items[i]
	}
	return nodes, true, nil
}

func (x *rolloutRun) startWave(cur int, plan [][]string) {
	x.st.Phase = fleetv1alpha1.RolloutProgressing
	x.st.WaveStartTime = &metav1.Time{Time: x.now}
	x.event(corev1.EventTypeNormal, "WaveStarted", "Start", "Wave %d of %d started: %d nodes to %s",
		cur, len(plan), len(plan[cur]), x.ro.Spec.TargetVersion)
}

// startedNodes lists the nodes of waves 0..cur: the gate is cumulative (internal/rollout).
func startedNodes(plan [][]string, cur int) []string {
	var out []string
	for _, w := range plan[:cur+1] {
		out = append(out, w...)
	}
	return out
}

// evaluate classifies the started nodes and applies the gate. It also returns the
// earliest deadline of the nodes still in progress (zero if none), to requeue for it.
func (x *rolloutRun) evaluate(started []string, nodes map[string]*fleetv1alpha1.EdgeNode) (rollout.Gate, time.Time) {
	results := make([]rollout.Result, 0, len(started))
	var next time.Time
	for _, n := range started {
		st := nodes[n].Status
		res := rollout.Classify(st, x.ro.Spec.TargetVersion, x.st.RolloutID)
		if res == rollout.InProgress {
			var since time.Time
			if st.ReachableSince != nil {
				since = st.ReachableSince.Time
			}
			deadline := rollout.Deadline(x.st.WaveStartTime.Time, since, x.ro.Spec.HealthGate.Timeout.Duration)
			switch {
			case !x.now.Before(deadline):
				res = rollout.AtDeadline(st, x.st.RolloutID)
			case next.IsZero() || deadline.Before(next):
				next = deadline
			}
		}
		results = append(results, res)
	}
	gate := rollout.EvaluateGate(results, x.ro.Spec.HealthGate.MaxFailurePercent)
	x.st.Updated, x.st.Failed, x.st.Pending = gate.Succeeded, gate.Failed, gate.Pending
	return gate, next
}

// ensure writes the target to every started node (idempotent: unchanged values are not
// rewritten). Unreachable nodes are written too: they apply it when back in range.
func (x *rolloutRun) ensure(ctx context.Context, started []string, nodes map[string]*fleetv1alpha1.EdgeNode) error {
	store, err := x.r.Store(ctx)
	if err != nil {
		return fmt.Errorf("desired-state store: %w", err)
	}
	for _, n := range started {
		if _, err := store.Ensure(ctx, n, x.ro.Spec.TargetVersion, x.st.RolloutID, nodes[n].Status.CurrentVersion); err != nil {
			return err
		}
	}
	return nil
}

// halt reverts the current wave; earlier waves passed their gate and keep the target.
func (x *rolloutRun) halt(ctx context.Context, wave []string, gate rollout.Gate) error {
	store, err := x.r.Store(ctx)
	if err != nil {
		return fmt.Errorf("desired-state store: %w", err)
	}
	reverted := 0
	for _, n := range wave {
		changed, err := store.Revert(ctx, n, x.st.RolloutID)
		if err != nil {
			return err // retried: Revert is idempotent
		}
		if changed {
			reverted++
		}
	}
	cur := x.st.CurrentWave
	msg := fmt.Sprintf("wave %d failed the health gate: %d failed, %d stuck past the timeout, %d updated, %d in progress; %d nodes reverted",
		cur, gate.Failed, gate.Stuck, gate.Succeeded, gate.InProgress, reverted)
	x.st.Phase = fleetv1alpha1.RolloutHalted
	x.st.SoakUntil = nil
	x.setReady(metav1.ConditionFalse, fleetv1alpha1.ReasonGateFailed, msg)
	x.event(corev1.EventTypeWarning, "Halted", "Halt", "%s", msg)
	logf.FromContext(ctx).Info("rollout halted", "rollout", x.ro.Name, "rolloutId", x.st.RolloutID, "wave", cur,
		"failed", gate.Failed, "reverted", reverted)
	return nil
}

func (x *rolloutRun) paused() {
	if x.st.Phase != fleetv1alpha1.RolloutPaused {
		x.event(corev1.EventTypeNormal, "Paused", "Pause", "Promotion paused at wave %d (spec.paused)", x.st.CurrentWave)
	}
	x.st.Phase = fleetv1alpha1.RolloutPaused
	x.setReady(metav1.ConditionFalse, fleetv1alpha1.ReasonPaused, "spec.paused: no promotion, no new desired states")
}

func (x *rolloutRun) progressing(reason, msg string) {
	x.st.Phase = fleetv1alpha1.RolloutProgressing
	x.setReady(metav1.ConditionFalse, reason, msg)
}

func (x *rolloutRun) setReady(status metav1.ConditionStatus, reason, msg string) {
	meta.SetStatusCondition(&x.st.Conditions, metav1.Condition{
		Type: fleetv1alpha1.RolloutReady, Status: status, Reason: reason, Message: msg,
		ObservedGeneration: x.ro.Generation,
	})
}

func (x *rolloutRun) event(eventType, reason, action, note string, args ...any) {
	x.r.Recorder.Eventf(x.ro, nil, eventType, reason, action, note, args...)
}

// SetupWithManager registers the reconciler. A rollout wakes on its own spec changes, on
// relevant changes of the EdgeNodes its fleet selects (before or after the change), on
// its fleet changing, and on RequeueAfter for gate timeouts and soak ends.
func (r *RolloutReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c := mgr.GetClient()
	byNode := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		return rolloutsFor(ctx, c, func(f *fleetv1alpha1.Fleet) bool { return fleet.Selects(f, obj.GetLabels()) })
	})
	byFleet := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		return rolloutsFor(ctx, c, func(f *fleetv1alpha1.Fleet) bool { return f.Name == obj.GetName() })
	})
	nodeChanged := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			old, okOld := e.ObjectOld.(*fleetv1alpha1.EdgeNode)
			updated, okNew := e.ObjectNew.(*fleetv1alpha1.EdgeNode)
			return okOld && okNew && rollout.RelevantNodeChange(old, updated)
		},
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&fleetv1alpha1.Rollout{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&fleetv1alpha1.EdgeNode{}, byNode, builder.WithPredicates(nodeChanged)).
		// Fleet status changes often (counts); only its creation, deletion and spec matter.
		Watches(&fleetv1alpha1.Fleet{}, byFleet, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named("rollout").
		Complete(r)
}

// rolloutsFor lists the rollouts whose fleet matches.
func rolloutsFor(ctx context.Context, c client.Client, match func(*fleetv1alpha1.Fleet) bool) []reconcile.Request {
	rollouts := &fleetv1alpha1.RolloutList{}
	if err := c.List(ctx, rollouts); err != nil {
		logf.FromContext(ctx).Error(err, "list rollouts")
		return nil
	}
	var reqs []reconcile.Request
	for i := range rollouts.Items {
		ro := &rollouts.Items[i]
		f := &fleetv1alpha1.Fleet{}
		if err := c.Get(ctx, client.ObjectKey{Name: ro.Spec.FleetRef}, f); err != nil {
			f.Name = ro.Spec.FleetRef // a missing fleet still matches by name
		}
		if match(f) {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(ro)})
		}
	}
	return reqs
}
