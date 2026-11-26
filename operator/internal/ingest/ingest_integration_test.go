package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1alpha1 "github.com/NilsRuiz/orbit/operator/api/v1alpha1"
	"github.com/NilsRuiz/orbit/operator/internal/testenv"
)

var k8s client.Client

func TestMain(m *testing.M) {
	cfg, stop, err := testenv.Start()
	if err != nil {
		println("envtest:", err.Error())
		os.Exit(1)
	}
	scheme := runtime.NewScheme()
	if err := fleetv1alpha1.AddToScheme(scheme); err != nil {
		println("scheme:", err.Error())
		os.Exit(1)
	}
	if k8s, err = client.New(cfg, client.Options{Scheme: scheme}); err != nil {
		println("client:", err.Error())
		os.Exit(1)
	}
	code := m.Run()
	stop()
	os.Exit(code)
}

// hub is an in-process NATS server with the ORBIT_STATUS stream, playing the hub.
type hub struct {
	url string
	nc  *nats.Conn
	js  jetstream.JetStream
}

func startHub(t *testing.T) *hub {
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
	// Same stream as hack/scripts/init-hub.sh.
	if _, err := js.CreateStream(context.Background(), jetstream.StreamConfig{
		Name: StatusStream, Subjects: []string{"orbit.edge.*.status"},
	}); err != nil {
		t.Fatal(err)
	}
	return &hub{url: s.ClientURL(), nc: nc, js: js}
}

// clock is a settable time source shared with the ingester.
type clock struct{ ns atomic.Int64 }

func newClock(t time.Time) *clock { c := &clock{}; c.set(t); return c }
func (c *clock) set(t time.Time)  { c.ns.Store(t.UnixNano()) }
func (c *clock) now() time.Time   { return time.Unix(0, c.ns.Load()).UTC() }

// run starts an ingester against h and returns it with its event recorder. It stops at
// the end of the test, or earlier with the returned stop function.
func run(t *testing.T, h *hub, clk *clock) (stop func(), rec *events.FakeRecorder) {
	t.Helper()
	nc, err := nats.Connect(h.url)
	if err != nil {
		t.Fatal(err)
	}
	rec = events.NewFakeRecorder(100)
	in := &Ingester{
		NC:         nc,
		Client:     k8s,
		Recorder:   rec,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:        clk.now,
		SetupRetry: 100 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() {
		if err := in.Start(ctx); err != nil {
			t.Errorf("Start: %v", err)
		}
	})
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			wg.Wait()
			nc.Close()
		})
	}
	t.Cleanup(stop)
	return stop, rec
}

func heartbeat(t *testing.T, h *hub, subjectNode string, hb Heartbeat) {
	t.Helper()
	b, err := json.Marshal(hb)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.nc.Publish("orbit.edge."+subjectNode+".heartbeat", b); err != nil {
		t.Fatal(err)
	}
}

func status(t *testing.T, h *hub, st Status) uint64 {
	t.Helper()
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	ack, err := h.js.Publish(context.Background(), "orbit.edge."+st.NodeID+".status", b)
	if err != nil {
		t.Fatal(err)
	}
	return ack.Sequence
}

// nodeName returns a node ID unique to this test: all tests share one API server.
func nodeName(t *testing.T, suffix string) string {
	return strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")) + "-" + suffix
}

func get(t *testing.T, name string) (*fleetv1alpha1.EdgeNode, bool) {
	t.Helper()
	var en fleetv1alpha1.EdgeNode
	err := k8s.Get(context.Background(), client.ObjectKey{Name: name}, &en)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return &en, true
}

// eventually polls cond for up to 5 s. While the ingester subscribes, early heartbeats are
// lost (core NATS), so callers that publish heartbeats do it inside cond.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// registered sends hb for name until the EdgeNode exists, and returns it.
func registered(t *testing.T, h *hub, name string, hb Heartbeat) *fleetv1alpha1.EdgeNode {
	t.Helper()
	hb.NodeID = name
	var en *fleetv1alpha1.EdgeNode
	eventually(t, "EdgeNode "+name, func() bool {
		heartbeat(t, h, name, hb)
		var ok bool
		en, ok = get(t, name)
		return ok
	})
	return en
}

// barrier makes sure every heartbeat published before it was processed: heartbeats are
// handled in order, so once a sentinel node is registered, earlier ones were too.
func barrier(t *testing.T, h *hub) {
	t.Helper()
	registered(t, h, fmt.Sprintf("barrier-%d", time.Now().UnixNano()), Heartbeat{Current: "0.0.0", Phase: "idle"})
}

func drainEvents(rec *events.FakeRecorder) []string {
	var out []string
	for {
		select {
		case e := <-rec.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

func hasEvent(evs []string, prefix string) bool {
	for _, e := range evs {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

var t0 = time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)

func TestHeartbeatRegistersNode(t *testing.T) {
	h := startHub(t)
	_, rec := run(t, h, newClock(t0))
	name := nodeName(t, "sat")

	en := registered(t, h, name, Heartbeat{
		Current: "1.2.0", Phase: "verifying", RolloutID: "r-1",
		Labels: map[string]string{"orbit.io/constellation": "alpha"},
	})

	if want := map[string]string{"orbit.io/constellation": "alpha"}; !maps.Equal(en.Labels, want) {
		t.Errorf("labels = %v, want %v", en.Labels, want)
	}
	eventually(t, "status", func() bool { en, _ = get(t, name); return en.Status.LastSeen != nil })
	s := en.Status
	if s.CurrentVersion != "1.2.0" || s.Phase != "verifying" || s.RolloutID != "r-1" || s.Reachability != fleetv1alpha1.Reachable {
		t.Errorf("status = %+v", s)
	}
	// lastSeen is the hub's reception time, not the agent's clock.
	if !s.LastSeen.Time.Equal(t0) {
		t.Errorf("lastSeen = %v, want %v", s.LastSeen, t0)
	}
	if evs := drainEvents(rec); !hasEvent(evs, "Normal Registered") {
		t.Errorf("events = %q, want Registered", evs)
	}
}

func TestHeartbeatKeepsHumanLabels(t *testing.T) {
	h := startHub(t)
	name := nodeName(t, "sat")
	if err := k8s.Create(context.Background(), &fleetv1alpha1.EdgeNode{ObjectMeta: metav1.ObjectMeta{
		Name:   name,
		Labels: map[string]string{"team": "ops", "orbit.io/constellation": "alpha", "orbit.io/old": "x"},
	}}); err != nil {
		t.Fatal(err)
	}
	_, rec := run(t, h, newClock(t0))

	want := map[string]string{"team": "ops", "orbit.io/constellation": "beta"}
	eventually(t, "labels synced", func() bool {
		heartbeat(t, h, name, Heartbeat{NodeID: name, Current: "1.0.0", Phase: "idle",
			Labels: map[string]string{"orbit.io/constellation": "beta", "team": "evil"}})
		en, _ := get(t, name)
		return maps.Equal(en.Labels, want)
	})
	if evs := drainEvents(rec); !hasEvent(evs, "Normal LabelsChanged") {
		t.Errorf("events = %q, want LabelsChanged", evs)
	}
}

func TestHeartbeatIgnoresInvalidMessages(t *testing.T) {
	h := startHub(t)
	run(t, h, newClock(t0))
	spoofed, invalid, garbage := nodeName(t, "spoofed"), "Not_A_Name", nodeName(t, "garbage")
	barrier(t, h) // the ingester is subscribed: the messages below are not lost

	// A node speaking for another one, an ID that cannot name an object, and non-JSON.
	heartbeat(t, h, spoofed, Heartbeat{NodeID: nodeName(t, "other"), Current: "1.0.0"})
	heartbeat(t, h, invalid, Heartbeat{NodeID: invalid, Current: "1.0.0"})
	if err := h.nc.Publish("orbit.edge."+garbage+".heartbeat", []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	barrier(t, h)

	for _, n := range []string{spoofed, nodeName(t, "other"), garbage} {
		if _, ok := get(t, n); ok {
			t.Errorf("EdgeNode %s was created from an invalid heartbeat", n)
		}
	}
}

func TestHeartbeatWriteBudget(t *testing.T) {
	h := startHub(t)
	clk := newClock(t0)
	run(t, h, clk)
	name := nodeName(t, "sat")
	hb := Heartbeat{NodeID: name, Current: "1.0.0", Phase: "idle"}

	en := registered(t, h, name, hb)
	eventually(t, "status", func() bool { en, _ = get(t, name); return en.Status.LastSeen != nil })
	rv := en.ResourceVersion

	// Nothing changed and lastSeen is 10 s old: no write.
	clk.set(t0.Add(10 * time.Second))
	heartbeat(t, h, name, hb)
	barrier(t, h)
	if en, _ = get(t, name); en.ResourceVersion != rv {
		t.Errorf("heartbeat 10s later wrote the EdgeNode (lastSeen %v)", en.Status.LastSeen)
	}

	// A minute later, lastSeen is refreshed.
	clk.set(t0.Add(time.Minute))
	heartbeat(t, h, name, hb)
	barrier(t, h)
	if en, _ = get(t, name); !en.Status.LastSeen.Time.Equal(t0.Add(time.Minute)) {
		t.Errorf("lastSeen = %v, want %v", en.Status.LastSeen, t0.Add(time.Minute))
	}

	// Any other change is written at once, with a fresh lastSeen.
	clk.set(t0.Add(70 * time.Second))
	heartbeat(t, h, name, Heartbeat{NodeID: name, Current: "1.0.0", Phase: "applying"})
	barrier(t, h)
	if en, _ = get(t, name); en.Status.Phase != "applying" || !en.Status.LastSeen.Time.Equal(t0.Add(70*time.Second)) {
		t.Errorf("status = %+v", en.Status)
	}
}

func TestHeartbeatRestoresReachability(t *testing.T) {
	h := startHub(t)
	name := nodeName(t, "sat")
	en := &fleetv1alpha1.EdgeNode{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := k8s.Create(context.Background(), en); err != nil {
		t.Fatal(err)
	}
	en.Status = fleetv1alpha1.EdgeNodeStatus{
		Reachability: fleetv1alpha1.Unreachable, CurrentVersion: "1.0.0", Phase: "idle",
		LastSeen: &metav1.Time{Time: t0.Add(-time.Hour)},
	}
	if err := k8s.Status().Update(context.Background(), en); err != nil {
		t.Fatal(err)
	}
	_, rec := run(t, h, newClock(t0))

	eventually(t, "Reachable", func() bool {
		heartbeat(t, h, name, Heartbeat{NodeID: name, Current: "1.0.0", Phase: "idle"})
		en, _ := get(t, name)
		return en.Status.Reachability == fleetv1alpha1.Reachable
	})
	if evs := drainEvents(rec); !hasEvent(evs, "Normal ContactRestored") {
		t.Errorf("events = %q, want ContactRestored", evs)
	}
}

func TestStatusEvents(t *testing.T) {
	h := startHub(t)
	// Status events apply only if stored after lastSeen: receive the heartbeat "an hour
	// ago" (stream timestamps are real time).
	_, rec := run(t, h, newClock(time.Now().Add(-time.Hour)))
	name := nodeName(t, "sat")
	registered(t, h, name, Heartbeat{Current: "1.0.0", Phase: "idle"})

	status(t, h, Status{NodeID: name, Phase: "applying", Current: "1.0.0", Target: "1.1.0", RolloutID: "r-1"})
	seq := status(t, h, Status{NodeID: name, Phase: "rolled-back", Current: "1.0.0", RolloutID: "r-1", Message: "unhealthy"})

	var en *fleetv1alpha1.EdgeNode
	eventually(t, "status applied", func() bool {
		en, _ = get(t, name)
		return en.Status.ObservedStatusSeq == int64(seq)
	})
	if en.Status.Phase != "rolled-back" || en.Status.RolloutID != "r-1" {
		t.Errorf("status = %+v", en.Status)
	}
	evs := drainEvents(rec)
	if !hasEvent(evs, "Warning RolledBack") {
		t.Errorf("events = %q, want RolledBack", evs)
	}
}

func TestStatusForUnknownNodeIsDropped(t *testing.T) {
	h := startHub(t)
	run(t, h, newClock(t0))
	name := nodeName(t, "ghost")
	status(t, h, Status{NodeID: name, Phase: "idle", Current: "1.0.0"})
	waitConsumed(t, h)
	if _, ok := get(t, name); ok {
		t.Error("a status event created an EdgeNode: only heartbeats register nodes (ADR-0007)")
	}
}

// waitConsumed waits until the operator consumer has acknowledged every status event.
func waitConsumed(t *testing.T, h *hub) {
	t.Helper()
	eventually(t, "status events consumed", func() bool {
		c, err := h.js.Consumer(context.Background(), StatusStream, ConsumerName)
		if err != nil {
			return false
		}
		info, err := c.Info(context.Background())
		return err == nil && info.NumPending == 0 && info.NumAckPending == 0 && info.NumRedelivered == 0
	})
}

func TestStatusReplayIsIdempotent(t *testing.T) {
	h := startHub(t)
	clk := newClock(time.Now().Add(-time.Hour))
	stop, _ := run(t, h, clk)
	name := nodeName(t, "sat")
	registered(t, h, name, Heartbeat{Current: "1.0.0", Phase: "idle"})
	status(t, h, Status{NodeID: name, Phase: "applying", Current: "1.0.0", RolloutID: "r-1"})
	seq := status(t, h, Status{NodeID: name, Phase: "idle", Current: "1.1.0", RolloutID: "r-1"})
	waitConsumed(t, h)
	stop()

	// The node moved on since (a newer state, as a heartbeat would report). Clear
	// lastSeen so that only observedStatusSeq stands between the replay and this state.
	en, _ := get(t, name)
	if en.Status.ObservedStatusSeq != int64(seq) {
		t.Fatalf("observedStatusSeq = %d, want %d", en.Status.ObservedStatusSeq, seq)
	}
	en.Status.Phase, en.Status.CurrentVersion, en.Status.LastSeen = "verifying", "1.2.0", nil
	if err := k8s.Status().Update(context.Background(), en); err != nil {
		t.Fatal(err)
	}

	// Simulate a lost consumer (deleted, or a fresh hub): the next operator replays the
	// whole stream. The EdgeNode must not go back to an older state.
	if err := h.js.DeleteConsumer(context.Background(), StatusStream, ConsumerName); err != nil {
		t.Fatal(err)
	}
	run(t, h, clk)
	waitConsumed(t, h)

	en, _ = get(t, name)
	if en.Status.Phase != "verifying" || en.Status.CurrentVersion != "1.2.0" || en.Status.ObservedStatusSeq != int64(seq) {
		t.Errorf("status after replay = %+v", en.Status)
	}
}

func TestStatusConsumerResumesAfterRestart(t *testing.T) {
	h := startHub(t)
	clk := newClock(time.Now().Add(-time.Hour))
	stop, _ := run(t, h, clk)
	name := nodeName(t, "sat")
	registered(t, h, name, Heartbeat{Current: "1.0.0", Phase: "idle"})
	waitConsumed(t, h)
	stop()

	// Published while no operator runs: the durable consumer keeps its position.
	seq := status(t, h, Status{NodeID: name, Phase: "verifying", Current: "1.0.0", Target: "1.1.0", RolloutID: "r-2"})
	run(t, h, clk)

	eventually(t, "event published during the restart", func() bool {
		en, _ := get(t, name)
		return en.Status.ObservedStatusSeq == int64(seq) && en.Status.Phase == "verifying"
	})
}

func TestStatusOlderThanLastSeenOnlyAdvancesSequence(t *testing.T) {
	h := startHub(t)
	// The heartbeat is received "in an hour": any status event stored now is older than
	// the state the heartbeat reported, and must not overwrite it.
	run(t, h, newClock(time.Now().Add(time.Hour)))
	name := nodeName(t, "sat")
	registered(t, h, name, Heartbeat{Current: "1.1.0", Phase: "idle"})
	seq := status(t, h, Status{NodeID: name, Phase: "applying", Current: "1.0.0", Target: "1.1.0"})
	waitConsumed(t, h)

	en, _ := get(t, name)
	if en.Status.Phase != "idle" || en.Status.CurrentVersion != "1.1.0" || en.Status.ObservedStatusSeq != int64(seq) {
		t.Errorf("status = %+v", en.Status)
	}
}

// TestFirstContactIsNotARestore covers a race found on the local fleet: the EdgeNode
// controller can mark a just-registered node Unreachable (never seen) before the
// ingester writes its first status. That first contact is not "back in contact".
func TestFirstContactIsNotARestore(t *testing.T) {
	h := startHub(t)
	name := nodeName(t, "sat")
	en := &fleetv1alpha1.EdgeNode{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := k8s.Create(context.Background(), en); err != nil {
		t.Fatal(err)
	}
	en.Status.Reachability = fleetv1alpha1.Unreachable // as the controller sets it, no lastSeen
	if err := k8s.Status().Update(context.Background(), en); err != nil {
		t.Fatal(err)
	}
	_, rec := run(t, h, newClock(t0))

	eventually(t, "Reachable", func() bool {
		heartbeat(t, h, name, Heartbeat{NodeID: name, Current: "1.0.0", Phase: "idle"})
		en, _ := get(t, name)
		return en.Status.Reachability == fleetv1alpha1.Reachable
	})
	if evs := drainEvents(rec); hasEvent(evs, "Normal ContactRestored") {
		t.Errorf("events = %q, want no ContactRestored on first contact", evs)
	}
}
