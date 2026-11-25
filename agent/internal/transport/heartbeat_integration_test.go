package transport

import (
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"testing"
	"time"
)

// TestHeartbeatPayload pins the heartbeat contract the operator relies on (ADR-0007,
// "Messages" in agent/README.md): field names and types on the wire.
func TestHeartbeatPayload(t *testing.T) {
	srv := startServer(t)
	n, err := Connect(srv.ClientURL(), "sat-1", "", "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()

	sub, err := n.nc.SubscribeSync(HeartbeatSubject("sat-1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := n.nc.Flush(); err != nil {
		t.Fatal(err)
	}

	before := time.Now().UTC().Add(-time.Second)
	labels := map[string]string{"orbit.io/constellation": "alpha"}
	if err := n.Heartbeat(Heartbeat{Current: "1.2.0", Phase: "verifying", RolloutID: "r-1", Labels: labels}); err != nil {
		t.Fatal(err)
	}
	msg, err := sub.NextMsg(2 * time.Second)
	if err != nil {
		t.Fatalf("no heartbeat received: %v", err)
	}

	// Decode loosely, as a consumer in another module would.
	var got struct {
		NodeID    string            `json:"nodeId"`
		Current   string            `json:"current"`
		Phase     string            `json:"phase"`
		RolloutID string            `json:"rolloutId"`
		Labels    map[string]string `json:"labels"`
		Time      time.Time         `json:"time"`
	}
	if err := json.Unmarshal(msg.Data, &got); err != nil {
		t.Fatalf("payload is not the expected JSON: %v (%s)", err, msg.Data)
	}
	if got.NodeID != "sat-1" || got.Current != "1.2.0" || got.Phase != "verifying" || got.RolloutID != "r-1" {
		t.Errorf("payload = %+v", got)
	}
	if !maps.Equal(got.Labels, labels) {
		t.Errorf("labels = %v, want %v", got.Labels, labels)
	}
	if got.Time.Before(before) || got.Time.After(time.Now().UTC().Add(time.Second)) {
		t.Errorf("time = %v, want now", got.Time)
	}
}

// TestHeartbeatWithoutLabels keeps old-style nodes (no ORBIT_NODE_LABELS) valid: labels
// are an empty object, never null, so consumers can range over them safely.
func TestHeartbeatWithoutLabels(t *testing.T) {
	srv := startServer(t)
	n, err := Connect(srv.ClientURL(), "sat-2", "", "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	sub, err := n.nc.SubscribeSync(HeartbeatSubject("sat-2"))
	if err != nil {
		t.Fatal(err)
	}
	_ = n.nc.Flush()
	if err := n.Heartbeat(Heartbeat{Current: "1.0.0", Phase: "idle"}); err != nil {
		t.Fatal(err)
	}
	msg, err := sub.NextMsg(2 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(msg.Data, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["labels"]) != "{}" {
		t.Errorf("labels = %s, want {}", raw["labels"])
	}
	if _, ok := raw["rolloutId"]; ok {
		t.Errorf("rolloutId should be omitted when empty: %s", msg.Data)
	}
}
