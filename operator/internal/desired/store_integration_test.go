package desired

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// hub starts an in-process NATS server with the orbit-desired bucket (as
// hack/scripts/init-hub.sh creates it) and returns the store and the raw bucket.
func hub(t *testing.T) (*Store, jetstream.KeyValue) {
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
	kv, err := js.CreateKeyValue(context.Background(), jetstream.KeyValueConfig{Bucket: Bucket, History: 5})
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(context.Background(), js)
	if err != nil {
		t.Fatal(err)
	}
	return st, kv
}

func get(t *testing.T, kv jetstream.KeyValue, node string) (Value, uint64) {
	t.Helper()
	e, err := kv.Get(context.Background(), node)
	if err != nil {
		t.Fatal(err)
	}
	var v Value
	if err := json.Unmarshal(e.Value(), &v); err != nil {
		t.Fatal(err)
	}
	return v, e.Revision()
}

// Open creates the bucket (TestLazyCreatesTheBucket), but it cannot without JetStream: the
// error must surface, not a nil store.
func TestOpenWithoutJetStream(t *testing.T) {
	s, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatal("nats server not ready")
	}
	defer s.Shutdown()
	nc, err := nats.Connect(s.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, _ := jetstream.New(nc)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if st, err := Open(ctx, js); err == nil || st != nil {
		t.Fatalf("Open without JetStream = %v, %v; want an error", st, err)
	}
}

func TestEnsureAndRevert(t *testing.T) {
	st, kv := hub(t)
	ctx := context.Background()
	if _, err := kv.Put(ctx, "sat-1", []byte(`{"version":"1.1.0","rolloutId":"manual"}`)); err != nil {
		t.Fatal(err)
	}

	changed, err := st.Ensure(ctx, "sat-1", "1.2.0", "payload-g1", "1.0.0")
	if err != nil || !changed {
		t.Fatalf("Ensure = %v, %v", changed, err)
	}
	v, rev := get(t, kv, "sat-1")
	if v != (Value{Version: "1.2.0", RolloutID: "payload-g1", PreviousVersion: "1.1.0"}) {
		t.Errorf("after Ensure: %+v", v)
	}

	// Idempotent: a second reconcile writes nothing.
	if changed, err := st.Ensure(ctx, "sat-1", "1.2.0", "payload-g1", "1.0.0"); err != nil || changed {
		t.Errorf("second Ensure = %v, %v; want no write", changed, err)
	}
	if _, rev2 := get(t, kv, "sat-1"); rev2 != rev {
		t.Errorf("revision moved from %d to %d on an idempotent Ensure", rev, rev2)
	}

	if changed, err := st.Revert(ctx, "sat-1", "payload-g1"); err != nil || !changed {
		t.Fatalf("Revert = %v, %v", changed, err)
	}
	if v, _ := get(t, kv, "sat-1"); v != (Value{Version: "1.1.0", RolloutID: "payload-g1" + RevertSuffix}) {
		t.Errorf("after Revert: %+v", v)
	}
	if changed, err := st.Revert(ctx, "sat-1", "payload-g1"); err != nil || changed {
		t.Errorf("second Revert = %v, %v; want no write", changed, err)
	}
}

func TestEnsureNewNodeAndRevertDeletes(t *testing.T) {
	st, kv := hub(t)
	ctx := context.Background()
	if _, err := st.Ensure(ctx, "sat-2", "1.2.0", "payload-g1", ""); err != nil {
		t.Fatal(err)
	}
	if v, _ := get(t, kv, "sat-2"); v != (Value{Version: "1.2.0", RolloutID: "payload-g1"}) {
		t.Errorf("after Ensure: %+v", v)
	}
	if changed, err := st.Revert(ctx, "sat-2", "payload-g1"); err != nil || !changed {
		t.Fatalf("Revert = %v, %v", changed, err)
	}
	if _, err := kv.Get(ctx, "sat-2"); !errors.Is(err, jetstream.ErrKeyNotFound) {
		t.Errorf("key still there after reverting a node with no previous version: %v", err)
	}
	// Reverting a missing key is a no-op.
	if changed, err := st.Revert(ctx, "sat-3", "payload-g1"); err != nil || changed {
		t.Errorf("Revert(missing) = %v, %v", changed, err)
	}
}

// TestWriteDetectsConcurrentWrites: a value written between the read and the write (a
// human running `make desired`, another replica) is never overwritten blindly.
func TestWriteDetectsConcurrentWrites(t *testing.T) {
	st, kv := hub(t)
	ctx := context.Background()
	if _, err := kv.Put(ctx, "sat-1", []byte(`{"version":"1.1.0"}`)); err != nil {
		t.Fatal(err)
	}
	stale, err := st.Get(ctx, "sat-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kv.Put(ctx, "sat-1", []byte(`{"version":"9.9.9","rolloutId":"manual"}`)); err != nil {
		t.Fatal(err)
	}
	err = st.put(ctx, "sat-1", stale, Value{Version: "1.2.0", RolloutID: "payload-g1"})
	if !errors.Is(err, ErrConflict) {
		t.Errorf("put with a stale revision: err = %v, want ErrConflict", err)
	}
	if v, _ := get(t, kv, "sat-1"); v.Version != "9.9.9" {
		t.Errorf("concurrent write was overwritten: %+v", v)
	}

	// Same for a key created meanwhile, and for a delete.
	missing, _ := st.Get(ctx, "sat-9")
	if _, err := kv.Put(ctx, "sat-9", []byte(`{"version":"1.0.0"}`)); err != nil {
		t.Fatal(err)
	}
	if err := st.put(ctx, "sat-9", missing, Value{Version: "1.2.0"}); !errors.Is(err, ErrConflict) {
		t.Errorf("create over a new key: err = %v, want ErrConflict", err)
	}
	if err := st.delete(ctx, "sat-1", stale); !errors.Is(err, ErrConflict) {
		t.Errorf("delete with a stale revision: err = %v, want ErrConflict", err)
	}
}

// TestValueDecodesAsAgentDesired pins the contract with the agent (agent/README.md,
// "Messages"): the agent decodes version and rolloutId and ignores previousVersion.
func TestValueDecodesAsAgentDesired(t *testing.T) {
	b, err := json.Marshal(Value{Version: "1.2.0", RolloutID: "payload-g1", PreviousVersion: "1.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	var agent struct {
		Version   string `json:"version"`
		RolloutID string `json:"rolloutId,omitempty"`
	}
	if err := json.Unmarshal(b, &agent); err != nil || agent.Version != "1.2.0" || agent.RolloutID != "payload-g1" {
		t.Errorf("agent decodes %s as %+v (%v)", b, agent, err)
	}
}

// TestEnsureOverInvalidValue: a value the agent itself would ignore (not JSON) is
// replaced, with the version the node reports as the one to revert to.
func TestEnsureOverInvalidValue(t *testing.T) {
	st, kv := hub(t)
	ctx := context.Background()
	if _, err := kv.Put(ctx, "sat-1", []byte("not json")); err != nil {
		t.Fatal(err)
	}
	if changed, err := st.Ensure(ctx, "sat-1", "1.2.0", "payload-g1", "1.0.0"); err != nil || !changed {
		t.Fatalf("Ensure = %v, %v", changed, err)
	}
	if v, _ := get(t, kv, "sat-1"); v != (Value{Version: "1.2.0", RolloutID: "payload-g1", PreviousVersion: "1.0.0"}) {
		t.Errorf("after Ensure: %+v", v)
	}
}

func TestLazyCreatesTheBucket(t *testing.T) {
	s, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatal("nats server not ready")
	}
	defer s.Shutdown()
	nc, err := nats.Connect(s.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, _ := jetstream.New(nc)
	ctx := context.Background()

	// A fresh hub has no bucket: the operator owns it and creates it.
	l := &Lazy{JS: js}
	st, err := l.Store(ctx)
	if err != nil {
		t.Fatalf("Store on a fresh hub: %v", err)
	}
	kv, err := js.KeyValue(ctx, Bucket)
	if err != nil {
		t.Fatalf("bucket not created: %v", err)
	}
	if status, err := kv.Status(ctx); err != nil || status.History() != 5 {
		t.Errorf("bucket history = %v (%v), want 5", status.History(), err)
	}
	if again, _ := l.Store(ctx); again != st {
		t.Error("Store reopened the bucket instead of reusing it")
	}
}

func TestOpenKeepsAnExistingBucket(t *testing.T) {
	s, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatal("nats server not ready")
	}
	defer s.Shutdown()
	nc, err := nats.Connect(s.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, _ := jetstream.New(nc)
	ctx := context.Background()

	// Created beforehand, as by hack/scripts/init-hub.sh, with a node's desired state.
	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: Bucket, History: 5})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kv.Put(ctx, "sat-1", []byte(`{"version":"1.1.0","rolloutId":"payload-g1"}`)); err != nil {
		t.Fatal(err)
	}
	st, err := Open(ctx, js)
	if err != nil {
		t.Fatalf("Open on an existing bucket: %v", err)
	}
	e, err := st.Get(ctx, "sat-1")
	if err != nil || !e.Exists || e.Version != "1.1.0" {
		t.Errorf("entry after Open = %+v (%v), want the existing 1.1.0", e, err)
	}
}
