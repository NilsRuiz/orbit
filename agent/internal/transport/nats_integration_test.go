package transport

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/orbit-fleet/orbit/agent/internal/payload"
	"github.com/orbit-fleet/orbit/agent/internal/reconcile"
	"github.com/orbit-fleet/orbit/agent/internal/state"
)

// startServer runs an in-process NATS server with JetStream, playing the hub.
func startServer(t *testing.T) *server.Server {
	t.Helper()
	s, err := server.NewServer(&server.Options{
		Host:      "127.0.0.1",
		Port:      -1, // random
		JetStream: true,
		StoreDir:  t.TempDir(),
		NoLog:     true,
		NoSigs:    true,
	})
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

// hubSetup creates what the operator owns: the desired KV bucket and the status stream.
func hubSetup(t *testing.T, url string) (jetstream.KeyValue, jetstream.Stream) {
	t.Helper()
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, _ := jetstream.New(nc)
	ctx := context.Background()
	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: DesiredBucket})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := js.CreateStream(ctx, jetstream.StreamConfig{Name: "ORBIT_STATUS", Subjects: []string{"orbit.edge.*.status"}})
	if err != nil {
		t.Fatal(err)
	}
	return kv, stream
}

func waitPhase(t *testing.T, stream jetstream.Stream, want state.Phase, current string) {
	t.Helper()
	cons, err := stream.OrderedConsumer(context.Background(), jetstream.OrderedConsumerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		msg, err := cons.Next(jetstream.FetchMaxWait(time.Second))
		if err != nil {
			continue
		}
		var s reconcile.Status
		_ = json.Unmarshal(msg.Data(), &s)
		if s.Phase == want && s.Current == current {
			return
		}
	}
	t.Fatalf("no status with phase=%s current=%s", want, current)
}

func TestAgentEndToEnd(t *testing.T) {
	srv := startServer(t)
	kv, stream := hubSetup(t, srv.ClientURL())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	nc, err := Connect(srv.ClientURL(), "sat-1", "", "", log)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	store, _ := state.NewStore(t.TempDir())
	st, _ := store.Load("1.0.0")
	sim := payload.Sim{BadVersions: []string{"6.6.6"}}
	rec := reconcile.New(reconcile.Config{NodeID: "sat-1", VerifyTimeout: time.Second, VerifyInterval: 50 * time.Millisecond},
		st, store, sim, sim, nc, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	desired, err := nc.WatchDesired(ctx)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for d := range desired {
			_ = rec.Reconcile(ctx, d)
		}
	}()

	put := func(v string) {
		if _, err := kv.Put(ctx, "sat-1", []byte(`{"version":"`+v+`","rolloutId":"r-test"}`)); err != nil {
			t.Fatal(err)
		}
	}

	put("1.1.0")
	waitPhase(t, stream, state.PhaseIdle, "1.1.0")

	put("6.6.6") // bad version: the dead man's switch must roll back to 1.1.0
	waitPhase(t, stream, state.PhaseRolledBack, "1.1.0")

	// Persisted state survives a restart.
	reloaded, _ := store.Load("ignored")
	if reloaded.Current != "1.1.0" || reloaded.LastFailed != "6.6.6" {
		t.Fatalf("persisted state = %+v", reloaded)
	}
}
