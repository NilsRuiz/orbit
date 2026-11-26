package controller

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	fleetv1alpha1 "github.com/NilsRuiz/orbit/operator/api/v1alpha1"
)

// countingReconciler counts reconciles per fleet, to check which EdgeNode writes wake
// which fleets.
type countingReconciler struct {
	reconcile.Reconciler
	mu sync.Mutex
	n  map[string]int
}

func (c *countingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	c.mu.Lock()
	c.n[req.Name]++
	c.mu.Unlock()
	return c.Reconciler.Reconcile(ctx, req)
}

func (c *countingReconciler) count(fleet string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n[fleet]
}

// startFleetManager runs a manager with only the Fleet controller, as in production:
// cached client, EdgeNode watch and mapping. It stops at the end of the test.
func startFleetManager(t *testing.T) *countingReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := fleetv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		// One manager per test: controller names would collide otherwise.
		Controller: config.Controller{SkipNameValidation: new(true)},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := &countingReconciler{Reconciler: &FleetReconciler{Client: mgr.GetClient()}, n: map[string]int{}}
	if err := setupFleetController(mgr, mgr.GetClient(), rec); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() {
		if err := mgr.Start(ctx); err != nil {
			t.Errorf("manager: %v", err)
		}
	})
	t.Cleanup(func() { cancel(); wg.Wait() })
	return rec
}

// testLabel returns a label key unique to the test: fleets and nodes of other tests
// share the API server and must not be selected.
func testLabel(t *testing.T) string {
	return "orbit.io/" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
}

func createFleet(t *testing.T, name string, sel metav1.LabelSelector) {
	t.Helper()
	f := &fleetv1alpha1.Fleet{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: fleetv1alpha1.FleetSpec{Selector: sel}}
	if err := k8s.Create(context.Background(), f); err != nil {
		t.Fatal(err)
	}
}

func createNode(t *testing.T, name string, labels map[string]string, reach fleetv1alpha1.Reachability) {
	t.Helper()
	en := &fleetv1alpha1.EdgeNode{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
	if err := k8s.Create(context.Background(), en); err != nil {
		t.Fatal(err)
	}
	if reach != "" {
		setReachability(t, name, reach)
	}
}

func setReachability(t *testing.T, name string, reach fleetv1alpha1.Reachability) {
	t.Helper()
	en := &fleetv1alpha1.EdgeNode{}
	if err := k8s.Get(context.Background(), client.ObjectKey{Name: name}, en); err != nil {
		t.Fatal(err)
	}
	en.Status.Reachability = reach
	if err := k8s.Status().Update(context.Background(), en); err != nil {
		t.Fatal(err)
	}
}

func fleetStatus(t *testing.T, name string) fleetv1alpha1.FleetStatus {
	t.Helper()
	f := &fleetv1alpha1.Fleet{}
	if err := k8s.Get(context.Background(), client.ObjectKey{Name: name}, f); err != nil {
		t.Fatal(err)
	}
	return f.Status
}

// waitCounts waits until the fleet reports want (nodes, reachable, unreachable).
func waitCounts(t *testing.T, fleet string, want [3]int32) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		s := fleetStatus(t, fleet)
		got := [3]int32{s.Nodes, s.Reachable, s.Unreachable}
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("fleet %s: counts (nodes, reachable, unreachable) = %v, want %v", fleet, got, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestFleetCountsFollowEdgeNodes(t *testing.T) {
	rec := startFleetManager(t)
	key := testLabel(t)
	alpha, beta, gamma := "counts-alpha", "counts-beta", "counts-gamma"
	createFleet(t, alpha, metav1.LabelSelector{MatchLabels: map[string]string{key: "alpha"}})
	createFleet(t, beta, metav1.LabelSelector{MatchLabels: map[string]string{key: "beta"}})
	createFleet(t, gamma, metav1.LabelSelector{MatchLabels: map[string]string{key: "gamma"}})
	waitCounts(t, alpha, [3]int32{0, 0, 0})
	waitCounts(t, gamma, [3]int32{0, 0, 0})

	createNode(t, "counts-1", map[string]string{key: "alpha"}, fleetv1alpha1.Reachable)
	createNode(t, "counts-2", map[string]string{key: "alpha"}, fleetv1alpha1.Reachable)
	createNode(t, "counts-3", map[string]string{key: "beta"}, "") // never seen
	waitCounts(t, alpha, [3]int32{2, 2, 0})
	waitCounts(t, beta, [3]int32{1, 0, 1})

	s := fleetStatus(t, alpha)
	if c := meta.FindStatusCondition(s.Conditions, fleetv1alpha1.FleetReady); c == nil || c.Status != metav1.ConditionTrue {
		t.Errorf("Ready condition = %+v, want True", c)
	}
	if s.ObservedGeneration != 1 {
		t.Errorf("observedGeneration = %d, want 1", s.ObservedGeneration)
	}

	t.Run("relabel moves the node between fleets", func(t *testing.T) {
		// Both the fleet the node leaves and the one it joins update; no other fleet wakes.
		gammaBefore := rec.count(gamma)
		en := &fleetv1alpha1.EdgeNode{}
		if err := k8s.Get(context.Background(), client.ObjectKey{Name: "counts-2"}, en); err != nil {
			t.Fatal(err)
		}
		en.Labels[key] = "beta"
		if err := k8s.Update(context.Background(), en); err != nil {
			t.Fatal(err)
		}
		waitCounts(t, alpha, [3]int32{1, 1, 0})
		waitCounts(t, beta, [3]int32{2, 1, 1})
		if n := rec.count(gamma) - gammaBefore; n != 0 {
			t.Errorf("fleet %s reconciled %d times for a node it never selected", gamma, n)
		}
	})

	t.Run("node becomes unreachable", func(t *testing.T) {
		setReachability(t, "counts-1", fleetv1alpha1.Unreachable)
		waitCounts(t, alpha, [3]int32{1, 0, 1})
	})

	t.Run("node is deleted", func(t *testing.T) {
		if err := k8s.Delete(context.Background(), &fleetv1alpha1.EdgeNode{ObjectMeta: metav1.ObjectMeta{Name: "counts-1"}}); err != nil {
			t.Fatal(err)
		}
		waitCounts(t, alpha, [3]int32{0, 0, 0})
	})

	t.Run("selector edited", func(t *testing.T) {
		f := &fleetv1alpha1.Fleet{}
		if err := k8s.Get(context.Background(), client.ObjectKey{Name: alpha}, f); err != nil {
			t.Fatal(err)
		}
		f.Spec.Selector = metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
			{Key: key, Operator: metav1.LabelSelectorOpIn, Values: []string{"alpha", "beta"}},
		}}
		if err := k8s.Update(context.Background(), f); err != nil {
			t.Fatal(err)
		}
		waitCounts(t, alpha, [3]int32{2, 1, 1})
		if g := fleetStatus(t, alpha).ObservedGeneration; g != 2 {
			t.Errorf("observedGeneration = %d, want 2", g)
		}
	})
}

func TestFleetInvalidSelector(t *testing.T) {
	startFleetManager(t)
	name := "invalid-selector"
	// The CRD schema accepts it; only the selector parser rejects an empty In list.
	createFleet(t, name, metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
		{Key: testLabel(t), Operator: metav1.LabelSelectorOpIn},
	}})
	deadline := time.Now().Add(10 * time.Second)
	for {
		c := meta.FindStatusCondition(fleetStatus(t, name).Conditions, fleetv1alpha1.FleetReady)
		if c != nil {
			if c.Status != metav1.ConditionFalse || c.Reason != fleetv1alpha1.ReasonInvalidSelector {
				t.Errorf("Ready condition = %+v, want False/%s", c, fleetv1alpha1.ReasonInvalidSelector)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no Ready condition on a fleet with an invalid selector")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestFleetIgnoresLastSeenWrites(t *testing.T) {
	rec := startFleetManager(t)
	key := testLabel(t)
	createFleet(t, "lastseen", metav1.LabelSelector{MatchLabels: map[string]string{key: "x"}})
	createNode(t, "lastseen-1", map[string]string{key: "x"}, fleetv1alpha1.Reachable)
	waitCounts(t, "lastseen", [3]int32{1, 1, 0})

	// Let the reconciles of the setup settle, then write lastSeen as the ingester does.
	time.Sleep(time.Second)
	before := rec.count("lastseen")
	for i := range 5 {
		en := &fleetv1alpha1.EdgeNode{}
		if err := k8s.Get(context.Background(), client.ObjectKey{Name: "lastseen-1"}, en); err != nil {
			t.Fatal(err)
		}
		en.Status.LastSeen = &metav1.Time{Time: time.Now().Add(time.Duration(i) * time.Minute)}
		if err := k8s.Status().Update(context.Background(), en); err != nil {
			t.Fatal(err)
		}
	}
	// A relevant write afterwards proves the watch is live, and bounds the wait.
	setReachability(t, "lastseen-1", fleetv1alpha1.Unreachable)
	waitCounts(t, "lastseen", [3]int32{1, 0, 1})

	if got := rec.count("lastseen") - before; got != 1 {
		t.Errorf("%d reconciles for 5 lastSeen writes and 1 reachability change, want 1", got)
	}
}
