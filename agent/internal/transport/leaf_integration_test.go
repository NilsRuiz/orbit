package transport

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"

	"github.com/NilsRuiz/orbit/agent/internal/payload"
	"github.com/NilsRuiz/orbit/agent/internal/reconcile"
	"github.com/NilsRuiz/orbit/agent/internal/state"
)

// convergenceBound is the maximum accepted time between the leaf↔hub link coming back
// and the agent running the version published during the cut (#16: 10–15 s measured).
const convergenceBound = 20 * time.Second

// linkProxy plays the "space link" between a leaf node and the hub: a TCP relay that
// can be cut (open connections closed, new ones refused) and restored.
type linkProxy struct {
	ln     net.Listener
	target string

	mu    sync.Mutex
	down  bool
	conns map[net.Conn]struct{}
	wg    sync.WaitGroup
}

func newLinkProxy(t *testing.T, target string) *linkProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &linkProxy{ln: ln, target: target, conns: map[net.Conn]struct{}{}}
	p.wg.Add(1)
	go p.acceptLoop()
	t.Cleanup(p.close)
	return p
}

func (p *linkProxy) addr() string { return p.ln.Addr().String() }

func (p *linkProxy) acceptLoop() {
	defer p.wg.Done()
	for {
		in, err := p.ln.Accept()
		if err != nil {
			return // listener closed
		}
		p.mu.Lock()
		if p.down {
			p.mu.Unlock()
			_ = in.Close() // link cut: refuse
			continue
		}
		out, err := net.Dial("tcp", p.target)
		if err != nil {
			p.mu.Unlock()
			_ = in.Close()
			continue
		}
		p.conns[in], p.conns[out] = struct{}{}, struct{}{}
		p.wg.Add(2)
		p.mu.Unlock()
		go p.pipe(out, in)
		go p.pipe(in, out)
	}
}

// pipe copies src to dst; when either side fails, both are closed.
func (p *linkProxy) pipe(dst, src net.Conn) {
	defer p.wg.Done()
	_, _ = io.Copy(dst, src)
	_ = dst.Close()
	_ = src.Close()
	p.mu.Lock()
	delete(p.conns, dst)
	delete(p.conns, src)
	p.mu.Unlock()
}

func (p *linkProxy) cut() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.down = true
	for c := range p.conns {
		_ = c.Close()
	}
}

func (p *linkProxy) restore() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.down = false
}

func (p *linkProxy) close() {
	_ = p.ln.Close()
	p.cut()
	p.wg.Wait()
}

// freePort reserves a random local port for a listener the server API does not expose.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

func runServer(t *testing.T, opts *server.Options) *server.Server {
	t.Helper()
	opts.Host, opts.Port, opts.NoLog, opts.NoSigs = "127.0.0.1", -1, true, true
	s, err := server.NewServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	go s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatal("nats server not ready")
	}
	t.Cleanup(s.Shutdown)
	return s
}

func eventually(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return cond()
}

// TestConvergenceAfterLeafLinkCut reproduces the edge topology (agent → leaf → hub) and
// bounds the time the agent needs to converge after the leaf↔hub link comes back, while
// the agent's own connection to its leaf stays open the whole time.
func TestConvergenceAfterLeafLinkCut(t *testing.T) {
	const (
		node    = "sat-1"
		cutTime = 12 * time.Second // longer than the ordered consumer's 2×5 s activity check
	)

	// Hub: JetStream in domain "hub", accepting leaf connections (hack/compose/nats/hub.conf).
	// The hub advertises the proxy as its leafnode URL: otherwise the leaf learns the hub's
	// real address from its INFO and reconnects directly, bypassing the link (in compose
	// that address sits on the cut network, so the issue does not show there).
	leafPort := freePort(t)
	link := newLinkProxy(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(leafPort)))
	hub := runServer(t, &server.Options{
		JetStream:       true,
		JetStreamDomain: "hub",
		StoreDir:        t.TempDir(),
		LeafNode:        server.LeafNodeOpts{Host: "127.0.0.1", Port: leafPort, Advertise: link.addr()},
	})

	// Edge leaf node: no JetStream, bridges to the hub through the link.
	remote, err := url.Parse("nats-leaf://" + link.addr())
	if err != nil {
		t.Fatal(err)
	}
	leaf := runServer(t, &server.Options{
		LeafNode: server.LeafNodeOpts{Remotes: []*server.RemoteLeafOpts{{URLs: []*url.URL{remote}}}},
	})
	if !eventually(t, 10*time.Second, func() bool { return hub.NumLeafNodes() == 1 }) {
		t.Fatal("leaf node never connected to the hub")
	}

	kv, _ := hubSetup(t, hub.ClientURL())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Agent: talks only to its local leaf, reaches the hub's JetStream through the domain.
	nc, err := Connect(leaf.ClientURL(), node, "", "hub", log)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	store, err := state.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Load("1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	sim := payload.Sim{}
	rec := reconcile.New(reconcile.Config{NodeID: node, VerifyTimeout: 5 * time.Second, VerifyInterval: 50 * time.Millisecond},
		st, store, sim, sim, nc, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	desired, err := nc.WatchDesired(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var watchClosed atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for d := range desired {
			_ = rec.Reconcile(ctx, d)
		}
		watchClosed.Store(true)
	}()
	defer func() { cancel(); <-done }()

	put := func(v string) {
		t.Helper()
		if _, err := kv.Put(ctx, node, []byte(`{"version":"`+v+`","rolloutId":"r-test"}`)); err != nil {
			t.Fatal(err)
		}
	}
	runs := func(v string) func() bool {
		return func() bool {
			s := rec.State()
			return s.Phase == state.PhaseIdle && s.Current == v
		}
	}

	// Baseline through the leaf, link up.
	put("1.1.0")
	if !eventually(t, 10*time.Second, runs("1.1.0")) {
		t.Fatalf("no convergence on 1.1.0 with the link up: state=%+v", rec.State())
	}

	// Cut the link and publish a new version on the hub while the node is out of range.
	cutAt := time.Now()
	link.cut()
	if !eventually(t, 5*time.Second, func() bool { return hub.NumLeafNodes() == 0 }) {
		t.Fatal("hub still sees the leaf after the link cut")
	}
	put("1.2.0")
	time.Sleep(time.Until(cutAt.Add(cutTime)))
	if s := rec.State(); s.Current != "1.1.0" {
		t.Fatalf("agent changed version while the link was cut: state=%+v", s)
	}

	link.restore()
	restoredAt := time.Now()
	ok := eventually(t, convergenceBound, runs("1.2.0"))
	elapsed := time.Since(restoredAt)
	if !ok {
		t.Fatalf("agent did not converge on 1.2.0 within %s of the link restore (state=%+v, watch closed=%v, leaf connected=%v)",
			convergenceBound, rec.State(), watchClosed.Load(), hub.NumLeafNodes() == 1)
	}
	t.Logf("converged %s after the link restore (cut lasted %s)", elapsed.Round(100*time.Millisecond), cutTime)

	// The agent ↔ leaf connection must not have been affected by the leaf ↔ hub cut.
	if !nc.nc.IsConnected() || nc.nc.Stats().Reconnects != 0 {
		t.Fatalf("agent connection to its leaf was disturbed: connected=%v reconnects=%d",
			nc.nc.IsConnected(), nc.nc.Stats().Reconnects)
	}
}
