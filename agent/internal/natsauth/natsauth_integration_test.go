package natsauth

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/NilsRuiz/orbit/agent/internal/reconcile"
	"github.com/NilsRuiz/orbit/agent/internal/state"
	"github.com/NilsRuiz/orbit/agent/internal/transport"
)

// The fleet's topology, authenticated as in production (ADR-0010): a hub in operator mode
// with JetStream in domain "hub", and one leaf server per node, connected with that node's
// credentials. The agent's real transport talks to its local leaf, unauthenticated, as on
// an edge cluster.

type fleet struct {
	t     *testing.T
	keys  Keys
	hub   *server.Server
	admin *nats.Conn // orbit-operator's credentials, straight to the hub
	js    jetstream.JetStream
	leafs map[string]*server.Server
}

func newFleet(t *testing.T, nodes ...string) *fleet {
	t.Helper()
	keys, err := NewKeys()
	if err != nil {
		t.Fatal(err)
	}
	f := &fleet{t: t, keys: keys, leafs: map[string]*server.Server{}}

	opJWT, err := keys.OperatorJWT()
	if err != nil {
		t.Fatal(err)
	}
	op, err := jwt.DecodeOperatorClaims(opJWT)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &server.MemAccResolver{}
	for _, kp := range []struct {
		pub func() (string, error)
		jwt func() (string, error)
	}{{keys.System.PublicKey, keys.SystemJWT}, {keys.Account.PublicKey, keys.AccountJWT}} {
		pub, err := kp.pub()
		if err != nil {
			t.Fatal(err)
		}
		token, err := kp.jwt()
		if err != nil {
			t.Fatal(err)
		}
		if err := resolver.Store(pub, token); err != nil {
			t.Fatal(err)
		}
	}
	sys, _ := keys.System.PublicKey()
	leafPort := freePort(t)
	f.hub = runServer(t, &server.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, JetStreamDomain: "hub", StoreDir: t.TempDir(),
		LeafNode:         server.LeafNodeOpts{Host: "127.0.0.1", Port: leafPort},
		TrustedOperators: []*jwt.OperatorClaims{op}, SystemAccount: sys, AccountResolver: resolver,
	})

	creds, err := keys.OperatorCreds()
	if err != nil {
		t.Fatal(err)
	}
	f.admin, err = nats.Connect(f.hub.ClientURL(), nats.UserCredentials(writeFile(t, "operator.creds", creds)))
	if err != nil {
		t.Fatalf("operator connects to the hub: %v", err)
	}
	t.Cleanup(f.admin.Close)
	if f.js, err = jetstream.New(f.admin); err != nil {
		t.Fatal(err)
	}
	// What orbit-operator creates (operator/internal/desired, operator/internal/ingest).
	ctx := context.Background()
	if _, err := f.js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: transport.DesiredBucket, History: 5}); err != nil {
		t.Fatalf("operator creates the KV bucket: %v", err)
	}
	if _, err := f.js.CreateStream(ctx, jetstream.StreamConfig{Name: "ORBIT_STATUS", Subjects: []string{"orbit.edge.*.status"}}); err != nil {
		t.Fatalf("operator creates the status stream: %v", err)
	}

	leafURL, err := url.Parse("nats-leaf://127.0.0.1:" + strconv.Itoa(leafPort))
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		creds, err := keys.NodeCreds(node)
		if err != nil {
			t.Fatal(err)
		}
		f.leafs[node] = runServer(t, &server.Options{
			Host: "127.0.0.1", Port: -1, ServerName: node + "-leaf",
			LeafNode: server.LeafNodeOpts{Remotes: []*server.RemoteLeafOpts{{
				URLs: []*url.URL{leafURL}, Credentials: writeFile(t, node+".creds", creds),
			}}},
		})
	}
	if !eventually(10*time.Second, func() bool { return f.hub.NumLeafNodes() == len(nodes) }) {
		t.Fatalf("%d of %d leaf nodes connected", f.hub.NumLeafNodes(), len(nodes))
	}
	return f
}

// agent connects the node's real transport to its local leaf, as orbit-agent does.
func (f *fleet) agent(node string) *transport.NATS {
	f.t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	n, err := transport.Connect(f.leafs[node].ClientURL(), node, "", "hub", log)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(n.Close)
	return n
}

// raw is a plain client on the node's leaf: what anything running on that node can do.
func (f *fleet) raw(node string, opts ...nats.Option) *nats.Conn {
	f.t.Helper()
	nc, err := nats.Connect(f.leafs[node].ClientURL(), opts...)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(nc.Close)
	return nc
}

func (f *fleet) putDesired(node, version string) {
	f.t.Helper()
	kv, err := f.js.KeyValue(context.Background(), transport.DesiredBucket)
	if err != nil {
		f.t.Fatal(err)
	}
	b, _ := json.Marshal(reconcile.Desired{Version: version, RolloutID: "r-1"})
	if _, err := kv.Put(context.Background(), node, b); err != nil {
		f.t.Fatal(err)
	}
}

// statusSubjects lists the subjects of the stored status events.
func (f *fleet) statusSubjects() []string {
	f.t.Helper()
	ctx := context.Background()
	stream, err := f.js.Stream(ctx, "ORBIT_STATUS")
	if err != nil {
		f.t.Fatal(err)
	}
	info, err := stream.Info(ctx, jetstream.WithSubjectFilter(">"))
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for s := range info.State.Subjects {
		out = append(out, s)
	}
	return out
}

func TestNodeCredentialsLetTheAgentWork(t *testing.T) {
	f := newFleet(t, "sat-1")
	agent := f.agent("sat-1")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Its desired state, through a KV watch in the hub's domain.
	desired, err := agent.WatchDesired(ctx)
	if err != nil {
		t.Fatalf("WatchDesired: %v", err)
	}
	f.putDesired("sat-1", "1.1.0")
	select {
	case d := <-desired:
		if d.Version != "1.1.0" {
			t.Errorf("desired = %+v, want 1.1.0", d)
		}
	case <-ctx.Done():
		t.Fatal("sat-1 never received its desired state")
	}

	// Its status events, stored in the hub's stream (JetStream publish with an ack).
	if err := agent.Report(ctx, reconcile.Status{NodeID: "sat-1", Phase: state.PhaseIdle, Current: "1.1.0"}); err != nil {
		t.Fatalf("Report: %v", err)
	}
	if got := f.statusSubjects(); len(got) != 1 || got[0] != "orbit.edge.sat-1.status" {
		t.Errorf("stored status subjects = %v, want [orbit.edge.sat-1.status]", got)
	}

	// Its heartbeats, received by the operator.
	sub, err := f.admin.SubscribeSync("orbit.edge.*.heartbeat")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.admin.Flush(); err != nil {
		t.Fatal(err)
	}
	if !eventually(5*time.Second, func() bool {
		_ = agent.Heartbeat(transport.Heartbeat{Current: "1.1.0", Phase: "idle"})
		m, err := sub.NextMsg(200 * time.Millisecond)
		return err == nil && m.Subject == "orbit.edge.sat-1.heartbeat"
	}) {
		t.Fatal("the operator never received sat-1's heartbeat")
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }() // only freed for the server to take
	return l.Addr().(*net.TCPAddr).Port
}

func writeFile(t *testing.T, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func runServer(t *testing.T, opts *server.Options) *server.Server {
	t.Helper()
	opts.NoLog, opts.NoSigs = true, true
	s, err := server.NewServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	go s.Start()
	if !s.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats server not ready")
	}
	t.Cleanup(s.Shutdown)
	return s
}

func eventually(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
	return true
}

// Each refusal below is paired with a control that works through the same path: a denial
// proves nothing if the path itself is broken.

func TestNodeCannotSpeakForAnotherNode(t *testing.T) {
	f := newFleet(t, "sat-1", "sat-2")
	sat1 := f.raw("sat-1", nats.CustomInboxPrefix(transport.InboxPrefix("sat-1")))
	js, err := jetstream.NewWithDomain(sat1, "hub")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Heartbeats: its own arrive, another node's never do.
	sub, err := f.admin.SubscribeSync("orbit.edge.*.heartbeat")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.admin.Flush(); err != nil {
		t.Fatal(err)
	}
	if !eventually(5*time.Second, func() bool {
		_ = sat1.Publish("orbit.edge.sat-1.heartbeat", []byte("{}"))
		m, err := sub.NextMsg(200 * time.Millisecond)
		return err == nil && m.Subject == "orbit.edge.sat-1.heartbeat"
	}) {
		t.Fatal("control: sat-1's own heartbeat never arrived")
	}
	for range 5 {
		_ = sat1.Publish("orbit.edge.sat-2.heartbeat", []byte("{}"))
	}
	_ = sat1.Flush()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		m, err := sub.NextMsg(200 * time.Millisecond)
		if err == nil && m.Subject != "orbit.edge.sat-1.heartbeat" {
			t.Fatalf("the hub received %s from sat-1", m.Subject)
		}
	}

	// Status events: its own are stored, another node's are not.
	if _, err := js.Publish(ctx, "orbit.edge.sat-1.status", []byte("{}")); err != nil {
		t.Fatalf("control: sat-1 stores its own status: %v", err)
	}
	pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := js.Publish(pctx, "orbit.edge.sat-2.status", []byte("{}")); err == nil {
		t.Error("sat-1 stored a status event for sat-2")
	}
	if got := f.statusSubjects(); len(got) != 1 || got[0] != "orbit.edge.sat-1.status" {
		t.Errorf("stored status subjects = %v, want only sat-1's", got)
	}
}

func TestNodeCannotReadAnotherNodesDesiredState(t *testing.T) {
	f := newFleet(t, "sat-1")
	f.putDesired("sat-1", "1.1.0")
	f.putDesired("sat-2", "6.6.6")
	sat1 := f.raw("sat-1", nats.CustomInboxPrefix(transport.InboxPrefix("sat-1")))
	js, err := jetstream.NewWithDomain(sat1, "hub")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	kv, err := js.KeyValue(ctx, transport.DesiredBucket)
	if err != nil {
		t.Fatalf("control: sat-1 opens the bucket: %v", err)
	}

	got := func(key string) (string, bool) {
		w, err := kv.Watch(ctx, key)
		if err != nil {
			return "", false
		}
		defer w.Stop() //nolint:errcheck
		timeout := time.After(2 * time.Second)
		for {
			select {
			case e := <-w.Updates():
				if e != nil {
					return string(e.Value()), true
				}
			case <-timeout:
				return "", false
			}
		}
	}
	if v, ok := got("sat-1"); !ok || !strings.Contains(v, "1.1.0") {
		t.Fatalf("control: sat-1 watches its own key: %q, %v", v, ok)
	}
	for _, key := range []string{"sat-2", "*", ">"} {
		if v, ok := got(key); ok {
			t.Errorf("sat-1 watched key %q and got %s", key, v)
		}
	}
	gctx, gcancel := context.WithTimeout(ctx, 2*time.Second)
	defer gcancel()
	if e, err := kv.Get(gctx, "sat-2"); err == nil {
		t.Errorf("sat-1 read sat-2's desired state: %s", e.Value())
	}
}

func TestNodeCannotEavesdrop(t *testing.T) {
	f := newFleet(t, "sat-1", "sat-2")
	spy := f.raw("sat-1")
	var subs []*nats.Subscription
	for _, subject := range []string{"orbit.edge.>", "_INBOX.>", transport.InboxPrefix("sat-2") + ".>", "$KV.>", "$JS.>"} {
		s, err := spy.SubscribeSync(subject)
		if err != nil {
			t.Fatal(err)
		}
		subs = append(subs, s)
	}
	if err := spy.Flush(); err != nil {
		t.Fatal(err)
	}

	// sat-2 works normally: desired state, status, heartbeats.
	agent := f.agent("sat-2")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	desired, err := agent.WatchDesired(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.putDesired("sat-2", "1.2.0")
	select {
	case <-desired:
	case <-ctx.Done():
		t.Fatal("control: sat-2 never received its desired state")
	}
	if err := agent.Report(ctx, reconcile.Status{NodeID: "sat-2", Phase: state.PhaseIdle, Current: "1.2.0"}); err != nil {
		t.Fatalf("control: sat-2 reports: %v", err)
	}
	for range 5 {
		_ = agent.Heartbeat(transport.Heartbeat{Current: "1.2.0", Phase: "idle"})
	}
	// The operator talks too: its replies and the KV updates must not reach sat-1.
	f.putDesired("sat-2", "1.3.0")

	time.Sleep(time.Second)
	for _, s := range subs {
		if m, err := s.NextMsg(100 * time.Millisecond); err == nil {
			t.Errorf("sat-1 subscribed to %s and received %s", s.Subject, m.Subject)
		}
	}
}

func TestDefaultInboxPrefixIsRefused(t *testing.T) {
	f := newFleet(t, "sat-1")
	// Without the node's prefix, replies go to "_INBOX.…", which sat-1 may not receive: the
	// agent must use transport.InboxPrefix (it does: transport.Connect).
	nc := f.raw("sat-1")
	js, err := jetstream.NewWithDomain(nc, "hub")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := js.KeyValue(ctx, transport.DesiredBucket); err == nil {
		t.Error("a JetStream call with the default inbox prefix got its reply")
	}
}
