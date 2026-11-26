package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	fleetv1alpha1 "github.com/NilsRuiz/orbit/operator/api/v1alpha1"
	"github.com/NilsRuiz/orbit/operator/internal/desired"
	"github.com/NilsRuiz/orbit/operator/internal/rollout"
)

const (
	badVersion = "6.6.6"
	// hangVersion never converges: the agent reports applying, and keeps talking.
	hangVersion = "7.7.7"
)

// startKV runs an in-process NATS hub with the orbit-desired bucket.
func startKV(t *testing.T) jetstream.KeyValue {
	t.Helper()
	s, err := server.NewServer(&server.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	go s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatal("nats server not ready")
	}
	t.Cleanup(s.Shutdown)
	nc, err := nats.Connect(s.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	kv, err := js.CreateKeyValue(context.Background(), jetstream.KeyValueConfig{Bucket: desired.Bucket, History: 5})
	if err != nil {
		t.Fatal(err)
	}
	kvServers.Store(kv, js)
	return kv
}

// simAgents plays the agents: each desired state written to the bucket moves the node's
// EdgeNode status as the real agent and ingester would. badVersion rolls back. An
// offline node ignores the bucket until it comes back, then applies the latest value.
type simAgents struct {
	t       *testing.T
	mu      sync.Mutex
	offline map[string]bool
	latest  map[string]desired.Value
}

func startSimAgents(t *testing.T, kv jetstream.KeyValue, offline ...string) *simAgents {
	t.Helper()
	sim := &simAgents{t: t, offline: map[string]bool{}, latest: map[string]desired.Value{}}
	for _, n := range offline {
		sim.offline[n] = true
	}
	ctx, cancel := context.WithCancel(context.Background())
	w, err := kv.WatchAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		for e := range w.Updates() {
			if e == nil || e.Operation() != jetstream.KeyValuePut {
				continue
			}
			var v desired.Value
			if err := json.Unmarshal(e.Value(), &v); err != nil {
				continue
			}
			sim.mu.Lock()
			sim.latest[e.Key()] = v
			off := sim.offline[e.Key()]
			sim.mu.Unlock()
			if !off {
				sim.apply(e.Key(), v)
			}
		}
	})
	t.Cleanup(func() { cancel(); _ = w.Stop(); wg.Wait() })
	return sim
}

func (s *simAgents) apply(node string, v desired.Value) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		en := &fleetv1alpha1.EdgeNode{}
		if err := k8s.Get(context.Background(), client.ObjectKey{Name: node}, en); err != nil {
			return err
		}
		en.Status.Reachability = fleetv1alpha1.Reachable
		en.Status.LastSeen = &metav1.Time{Time: time.Now()} // as the ingester writes it
		if en.Status.RolloutID != v.RolloutID {
			en.Status.RolloutIDSince = &metav1.Time{Time: time.Now()}
		}
		en.Status.RolloutID = v.RolloutID
		switch v.Version {
		case badVersion:
			en.Status.Phase = "rolled-back" // dead man's switch: current is unchanged
		case hangVersion:
			en.Status.Phase = "applying"
		default:
			en.Status.Phase, en.Status.CurrentVersion = "idle", v.Version
		}
		return k8s.Status().Update(context.Background(), en)
	})
	if err != nil {
		s.t.Errorf("sim agent %s: %v", node, err)
	}
}

// online brings an offline node back in range: it reports, then applies the latest value.
func (s *simAgents) online(node string) {
	s.mu.Lock()
	s.offline[node] = false
	v, ok := s.latest[node]
	s.mu.Unlock()
	// As the ingester reports a return in range: Reachable, with a new contact period.
	en := &fleetv1alpha1.EdgeNode{}
	if err := k8s.Get(context.Background(), client.ObjectKey{Name: node}, en); err != nil {
		s.t.Fatal(err)
	}
	en.Status.Reachability = fleetv1alpha1.Reachable
	en.Status.ReachableSince = &metav1.Time{Time: time.Now()}
	if err := k8s.Status().Update(context.Background(), en); err != nil {
		s.t.Fatal(err)
	}
	if ok {
		s.apply(node, v)
	}
}

// resume ends a short link cut: the node never became Unreachable (that takes
// 2 × contactInterval), so its contact period does not restart; it just applies the
// latest desired state.
func (s *simAgents) resume(node string) {
	s.mu.Lock()
	s.offline[node] = false
	v, ok := s.latest[node]
	s.mu.Unlock()
	if ok {
		s.apply(node, v)
	}
}

// startRolloutManager runs a manager with the Rollout controller against kv.
func startRolloutManager(t *testing.T, kv jetstream.KeyValue) *events.FakeRecorder {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := fleetv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		Controller:             config.Controller{SkipNameValidation: new(true)},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := events.NewFakeRecorder(1000)
	st, err := desired.Open(context.Background(), kvJetStream(t, kv))
	if err != nil {
		t.Fatal(err)
	}
	r := &RolloutReconciler{
		Client:    mgr.GetClient(),
		APIReader: mgr.GetAPIReader(),
		Recorder:  rec,
		Store:     func(context.Context) (DesiredStore, error) { return st, nil },
	}
	if err := r.SetupWithManager(mgr); err != nil {
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

// kvJetStream returns a JetStream context on the server behind kv.
var kvServers sync.Map // jetstream.KeyValue -> jetstream.JetStream

func kvJetStream(t *testing.T, kv jetstream.KeyValue) jetstream.JetStream {
	t.Helper()
	js, ok := kvServers.Load(kv)
	if !ok {
		t.Fatal("unknown KV: create it with startKV")
	}
	return js.(jetstream.JetStream)
}

// fleetWithNodes creates a Fleet and n EdgeNodes on version 1.0.0, idle and reachable
// unless listed in unreachable. Names are unique to the test.
func fleetWithNodes(t *testing.T, n int, unreachable ...string) (fleet string, nodes []string) {
	t.Helper()
	key := testLabel(t)
	prefix := strings.TrimPrefix(key, "orbit.io/")
	fleet = prefix
	createFleet(t, fleet, metav1.LabelSelector{MatchLabels: map[string]string{key: "x"}})
	for i := range n {
		name := fmt.Sprintf("%s-%d", prefix, i+1)
		nodes = append(nodes, name)
		en := &fleetv1alpha1.EdgeNode{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{key: "x"}}}
		if err := k8s.Create(context.Background(), en); err != nil {
			t.Fatal(err)
		}
		reach := fleetv1alpha1.Reachable
		if slices.Contains(unreachable, name) {
			reach = fleetv1alpha1.Unreachable
		}
		en.Status = fleetv1alpha1.EdgeNodeStatus{Reachability: reach, Phase: "idle", CurrentVersion: "1.0.0",
			LastSeen: &metav1.Time{Time: time.Now()}}
		if err := k8s.Status().Update(context.Background(), en); err != nil {
			t.Fatal(err)
		}
	}
	return fleet, nodes
}

func createRollout(t *testing.T, name, fleet, target string, percents []int32, paused bool) {
	t.Helper()
	ro := &fleetv1alpha1.Rollout{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: fleetv1alpha1.RolloutSpec{
			FleetRef: fleet, TargetVersion: target, Paused: paused,
			HealthGate: fleetv1alpha1.HealthGate{Timeout: metav1.Duration{Duration: 2 * time.Second}},
		},
	}
	for _, p := range percents {
		ro.Spec.Waves = append(ro.Spec.Waves, fleetv1alpha1.Wave{Percent: p})
	}
	if err := k8s.Create(context.Background(), ro); err != nil {
		t.Fatal(err)
	}
}

func rolloutStatus(t *testing.T, name string) fleetv1alpha1.RolloutStatus {
	t.Helper()
	ro := &fleetv1alpha1.Rollout{}
	if err := k8s.Get(context.Background(), client.ObjectKey{Name: name}, ro); err != nil {
		t.Fatal(err)
	}
	return ro.Status
}

func waitRollout(t *testing.T, name, what string, cond func(fleetv1alpha1.RolloutStatus) bool) fleetv1alpha1.RolloutStatus {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		s := rolloutStatus(t, name)
		if cond(s) {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("rollout %s: timed out waiting for %s; status %+v", name, what, s)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func desiredOf(t *testing.T, kv jetstream.KeyValue, node string) (desired.Value, bool) {
	t.Helper()
	e, err := kv.Get(context.Background(), node)
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return desired.Value{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	var v desired.Value
	if err := json.Unmarshal(e.Value(), &v); err != nil {
		t.Fatal(err)
	}
	return v, true
}

func eventReasons(rec *events.FakeRecorder) []string {
	var out []string
	for {
		select {
		case e := <-rec.Events:
			f := strings.Fields(e)
			if len(f) >= 2 {
				out = append(out, f[1])
			}
		default:
			return out
		}
	}
}

func phaseIs(p fleetv1alpha1.RolloutPhase) func(fleetv1alpha1.RolloutStatus) bool {
	return func(s fleetv1alpha1.RolloutStatus) bool { return s.Phase == p }
}

func TestRolloutGoodVersionSucceeds(t *testing.T) {
	kv := startKV(t)
	rec := startRolloutManager(t, kv)
	startSimAgents(t, kv)
	fleet, nodes := fleetWithNodes(t, 10)
	createRollout(t, "good", fleet, "1.1.0", []int32{10, 50, 100}, false)

	s := waitRollout(t, "good", "Succeeded", phaseIs(fleetv1alpha1.RolloutSucceeded))
	if s.Updated != 10 || s.Failed != 0 || s.Pending != 0 || s.CurrentWave != 2 || s.RolloutID != "good-g1" {
		t.Errorf("status = %+v", s)
	}
	if c := meta.FindStatusCondition(s.Conditions, fleetv1alpha1.RolloutReady); c == nil || c.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %+v, want True", c)
	}
	for _, n := range nodes {
		if v, _ := desiredOf(t, kv, n); v.Version != "1.1.0" || v.RolloutID != "good-g1" || v.PreviousVersion != "1.0.0" {
			t.Errorf("desired state of %s = %+v", n, v)
		}
	}
	reasons := eventReasons(rec)
	for _, want := range []string{"WaveStarted", "WavePromoted", "Succeeded"} {
		if !slices.Contains(reasons, want) {
			t.Errorf("events %v, want %s", reasons, want)
		}
	}
}

func TestRolloutBadVersionHaltsAtFirstWave(t *testing.T) {
	kv := startKV(t)
	rec := startRolloutManager(t, kv)
	startSimAgents(t, kv)
	fleet, nodes := fleetWithNodes(t, 10)
	createRollout(t, "bad", fleet, badVersion, []int32{10, 50, 100}, false)

	s := waitRollout(t, "bad", "Halted", phaseIs(fleetv1alpha1.RolloutHalted))
	if s.CurrentWave != 0 || s.Failed != 1 || s.Updated != 0 {
		t.Errorf("status = %+v", s)
	}
	if c := meta.FindStatusCondition(s.Conditions, fleetv1alpha1.RolloutReady); c == nil || c.Reason != fleetv1alpha1.ReasonGateFailed {
		t.Errorf("Ready = %+v, want reason %s", c, fleetv1alpha1.ReasonGateFailed)
	}

	canary := rollout.PlanWaves(nodes, []int32{10, 50, 100}, "bad")[0][0]
	for _, n := range nodes {
		v, ok := desiredOf(t, kv, n)
		switch {
		case n == canary:
			// Reverted to the version it ran before the rollout.
			if v != (desired.Value{Version: "1.0.0", RolloutID: "bad-g1" + desired.RevertSuffix}) {
				t.Errorf("canary %s desired state = %+v, want the revert", n, v)
			}
		case ok:
			t.Errorf("node %s of a later wave was written: %+v", n, v)
		}
	}
	if reasons := eventReasons(rec); !slices.Contains(reasons, "Halted") {
		t.Errorf("events %v, want Halted", reasons)
	}
}

func TestRolloutUnreachableNodeStaysPending(t *testing.T) {
	kv := startKV(t)
	startRolloutManager(t, kv)
	key := strings.TrimPrefix(testLabel(t), "orbit.io/")
	// Pick the offline node in the first wave: the plan is deterministic.
	names := make([]string, 0, 4)
	for i := range 4 {
		names = append(names, fmt.Sprintf("%s-%d", key, i+1))
	}
	offline := rollout.PlanWaves(names, []int32{50, 100}, "pending")[0][0]
	sim := startSimAgents(t, kv, offline)
	fleet, _ := fleetWithNodes(t, 4, offline)
	createRollout(t, "pending", fleet, "1.1.0", []int32{50, 100}, false)

	// The gate promotes wave 0 without the unreachable node, and the last wave does not
	// complete while it is pending, even after the gate timeout (2 s).
	waitRollout(t, "pending", "last wave waiting", func(s fleetv1alpha1.RolloutStatus) bool {
		c := meta.FindStatusCondition(s.Conditions, fleetv1alpha1.RolloutReady)
		return s.CurrentWave == 1 && c != nil && c.Reason == fleetv1alpha1.ReasonWaitingPending
	})
	time.Sleep(3 * time.Second)
	s := rolloutStatus(t, "pending")
	if s.Phase != fleetv1alpha1.RolloutProgressing || s.Pending != 1 || s.Failed != 0 || s.Updated != 3 {
		t.Errorf("status with a node out of range = %+v", s)
	}
	if v, _ := desiredOf(t, kv, offline); v.Version != "1.1.0" {
		t.Errorf("the offline node's desired state was not written: %+v", v)
	}

	sim.online(offline)
	s = waitRollout(t, "pending", "Succeeded", phaseIs(fleetv1alpha1.RolloutSucceeded))
	if s.Updated != 4 || s.Pending != 0 {
		t.Errorf("status = %+v", s)
	}
}

func TestRolloutPauseHoldsAndResumes(t *testing.T) {
	kv := startKV(t)
	startRolloutManager(t, kv)
	startSimAgents(t, kv)
	fleet, nodes := fleetWithNodes(t, 3)
	createRollout(t, "paused", fleet, "1.1.0", []int32{50, 100}, true)

	waitRollout(t, "paused", "Paused", phaseIs(fleetv1alpha1.RolloutPaused))
	time.Sleep(500 * time.Millisecond)
	for _, n := range nodes {
		if v, ok := desiredOf(t, kv, n); ok {
			t.Errorf("paused rollout wrote %s: %+v", n, v)
		}
	}

	ro := &fleetv1alpha1.Rollout{}
	if err := k8s.Get(context.Background(), client.ObjectKey{Name: "paused"}, ro); err != nil {
		t.Fatal(err)
	}
	ro.Spec.Paused = false
	if err := k8s.Update(context.Background(), ro); err != nil {
		t.Fatal(err)
	}
	// Unpausing changes the generation, not the run: same rollout id.
	s := waitRollout(t, "paused", "Succeeded", phaseIs(fleetv1alpha1.RolloutSucceeded))
	if s.RolloutID != "paused-g1" || s.Updated != 3 {
		t.Errorf("status = %+v", s)
	}
}

func TestRolloutNewTargetStartsANewRun(t *testing.T) {
	kv := startKV(t)
	startRolloutManager(t, kv)
	startSimAgents(t, kv)
	fleet, nodes := fleetWithNodes(t, 3)
	createRollout(t, "release", fleet, "1.1.0", []int32{100}, false)
	waitRollout(t, "release", "first run Succeeded", phaseIs(fleetv1alpha1.RolloutSucceeded))

	ro := &fleetv1alpha1.Rollout{}
	if err := k8s.Get(context.Background(), client.ObjectKey{Name: "release"}, ro); err != nil {
		t.Fatal(err)
	}
	ro.Spec.TargetVersion = "1.2.0"
	if err := k8s.Update(context.Background(), ro); err != nil {
		t.Fatal(err)
	}
	s := waitRollout(t, "release", "second run Succeeded", func(s fleetv1alpha1.RolloutStatus) bool {
		return s.RolloutID == "release-g2" && s.Phase == fleetv1alpha1.RolloutSucceeded
	})
	if s.TargetVersion != "1.2.0" || s.Updated != 3 {
		t.Errorf("status = %+v", s)
	}
	for _, n := range nodes {
		if v, _ := desiredOf(t, kv, n); v.Version != "1.2.0" || v.PreviousVersion != "1.1.0" {
			t.Errorf("desired state of %s = %+v", n, v)
		}
	}
}

func TestRolloutWaitsForItsFleet(t *testing.T) {
	kv := startKV(t)
	startRolloutManager(t, kv)
	startSimAgents(t, kv)
	createRollout(t, "early", "fleet-created-later", "1.1.0", []int32{100}, false)

	waitRollout(t, "early", "FleetNotFound", func(s fleetv1alpha1.RolloutStatus) bool {
		c := meta.FindStatusCondition(s.Conditions, fleetv1alpha1.RolloutReady)
		return s.Phase == fleetv1alpha1.RolloutPending && c != nil && c.Reason == fleetv1alpha1.ReasonFleetNotFound
	})
	// Creating the fleet wakes the rollout (Fleet watch), no polling.
	createFleet(t, "fleet-created-later", metav1.LabelSelector{MatchLabels: map[string]string{testLabel(t): "x"}})
	waitRollout(t, "early", "Succeeded on an empty fleet", phaseIs(fleetv1alpha1.RolloutSucceeded))
}

func TestRolloutStuckNodeHalts(t *testing.T) {
	kv := startKV(t)
	startRolloutManager(t, kv)
	startSimAgents(t, kv)
	fleet, _ := fleetWithNodes(t, 2)
	createRollout(t, "stuck", fleet, hangVersion, []int32{50, 100}, false)

	// The canary keeps reporting "applying" past the gate timeout (2 s): stuck, failed.
	s := waitRollout(t, "stuck", "Halted", phaseIs(fleetv1alpha1.RolloutHalted))
	c := meta.FindStatusCondition(s.Conditions, fleetv1alpha1.RolloutReady)
	if s.CurrentWave != 0 || c == nil || !strings.Contains(c.Message, "1 stuck") {
		t.Errorf("status = %+v", s)
	}
}

// TestRolloutSilentNodeIsNotStuck: a node that left range just before the wave (still
// Reachable: Unreachable takes 2 × contactInterval) must not halt the rollout at the
// gate timeout. Seen on the k3d demo.
func TestRolloutSilentNodeIsNotStuck(t *testing.T) {
	kv := startKV(t)
	startRolloutManager(t, kv)
	key := strings.TrimPrefix(testLabel(t), "orbit.io/")
	names := []string{key + "-1", key + "-2"}
	silent := rollout.PlanWaves(names, []int32{50, 100}, "silent")[0][0]
	sim := startSimAgents(t, kv, silent) // ignores the bucket, but is still Reachable
	fleet, _ := fleetWithNodes(t, 2)
	en := &fleetv1alpha1.EdgeNode{}
	if err := k8s.Get(context.Background(), client.ObjectKey{Name: silent}, en); err != nil {
		t.Fatal(err)
	}
	en.Status.LastSeen = &metav1.Time{Time: time.Now().Add(-30 * time.Second)}
	if err := k8s.Status().Update(context.Background(), en); err != nil {
		t.Fatal(err)
	}
	createRollout(t, "silent", fleet, "1.1.0", []int32{50, 100}, false)

	time.Sleep(4 * time.Second) // 2 × the gate timeout
	if s := rolloutStatus(t, "silent"); s.Phase != fleetv1alpha1.RolloutProgressing || s.Pending != 1 {
		t.Errorf("silent canary: status = %+v, want Progressing with 1 pending", s)
	}
	sim.online(silent)
	waitRollout(t, "silent", "Succeeded", phaseIs(fleetv1alpha1.RolloutSucceeded))
}

// TestRolloutShortCutAfterTheTimeout: a node cut for less than 2 × contactInterval stays
// Reachable. Back after the gate timeout, it starts applying: it must get the full
// timeout from then, not be stuck at once and reverted mid-apply. Seen on the k3d demo.
func TestRolloutShortCutAfterTheTimeout(t *testing.T) {
	kv := startKV(t)
	startRolloutManager(t, kv)
	key := strings.TrimPrefix(testLabel(t), "orbit.io/")
	names := []string{key + "-1", key + "-2"}
	canary := rollout.PlanWaves(names, []int32{50, 100}, "shortcut")[0][0]
	sim := startSimAgents(t, kv, canary)
	fleet, _ := fleetWithNodes(t, 2)
	createRollout(t, "shortcut", fleet, hangVersion, []int32{50, 100}, false)

	time.Sleep(3 * time.Second) // past the gate timeout (2 s), the canary silent
	if s := rolloutStatus(t, "shortcut"); s.Phase != fleetv1alpha1.RolloutProgressing {
		t.Fatalf("before the canary is back: status = %+v", s)
	}
	sim.resume(canary) // starts applying, and hangs
	time.Sleep(time.Second)
	if s := rolloutStatus(t, "shortcut"); s.Phase != fleetv1alpha1.RolloutProgressing {
		t.Errorf("canary applying for 1 s of its 2 s timeout: status = %+v, want Progressing", s)
	}
	// It does hang: stuck once its own timeout has passed.
	waitRollout(t, "shortcut", "Halted", phaseIs(fleetv1alpha1.RolloutHalted))
}
