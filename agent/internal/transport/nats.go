// Package transport connects the agent to its local NATS leaf node (ADR-0002).
package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/NilsRuiz/orbit/agent/internal/reconcile"
)

// DesiredBucket is the hub KV bucket holding one desired state per node (key = node ID).
const DesiredBucket = "orbit-desired"

// InboxPrefix is the node's own prefix for reply subjects (core requests and JetStream
// API replies), instead of the shared "_INBOX". A node's credentials allow it to subscribe
// only under its own prefix, so it cannot read another node's replies (ADR-0010).
func InboxPrefix(nodeID string) string { return "_INBOX_" + nodeID }

// StatusSubject is where a node publishes its status events (stream ORBIT_STATUS).
func StatusSubject(nodeID string) string { return fmt.Sprintf("orbit.edge.%s.status", nodeID) }

// HeartbeatSubject is where a node publishes its core NATS liveness heartbeats.
func HeartbeatSubject(nodeID string) string { return fmt.Sprintf("orbit.edge.%s.heartbeat", nodeID) }

// NATS is the agent's transport: desired-state watch, status reports and heartbeats.
type NATS struct {
	nodeID string
	nc     *nats.Conn
	js     jetstream.JetStream
	log    *slog.Logger
}

// Connect retries forever: losing the link is the normal case on the edge, not an error.
// jsDomain is the hub's JetStream domain ("hub"); empty when talking to the hub directly.
func Connect(url, nodeID, credsFile, jsDomain string, log *slog.Logger) (*NATS, error) {
	opts := []nats.Option{
		nats.Name("orbit-agent-" + nodeID),
		nats.CustomInboxPrefix(InboxPrefix(nodeID)),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2 * time.Second),
		nats.RetryOnFailedConnect(true),
		nats.ReconnectBufSize(8 * 1024 * 1024), // buffer outgoing messages while disconnected
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			log.Warn("nats disconnected", "err", err)
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			log.Info("nats reconnected", "url", c.ConnectedUrl())
		}),
		nats.ErrorHandler(asyncErrorHandler(log)),
	}
	if credsFile != "" {
		opts = append(opts, nats.UserCredentials(credsFile))
	}
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}
	var js jetstream.JetStream
	if jsDomain != "" {
		js, err = jetstream.NewWithDomain(nc, jsDomain)
	} else {
		js, err = jetstream.New(nc)
	}
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream: %w", err)
	}
	return &NATS{nodeID: nodeID, nc: nc, js: js, log: log}, nil
}

// asyncErrorHandler logs errors that nats.go reports only asynchronously: slow consumers,
// permission violations, inactive JetStream consumers (e.g. a KV watch whose heartbeats
// stopped). Without it, nats.go prints them as plain text on stderr, outside the
// structured logs.
func asyncErrorHandler(log *slog.Logger) nats.ErrHandler {
	return func(_ *nats.Conn, sub *nats.Subscription, err error) {
		attrs := []any{"err", err}
		if sub != nil {
			attrs = append(attrs, "subject", sub.Subject)
			if dropped, derr := sub.Dropped(); derr == nil && dropped > 0 {
				attrs = append(attrs, "dropped", dropped)
			}
		}
		log.Warn("nats async error", attrs...)
	}
}

// Close drains the connection (flushes pending messages). Best effort on shutdown: a
// failure is logged, not returned.
func (n *NATS) Close() {
	if err := n.nc.Drain(); err != nil {
		n.log.Warn("nats drain on close", "err", err)
	}
}

// WatchDesired streams desired states for this node. The KV bucket is created by the hub;
// we wait for it if it does not exist yet. The channel is closed when ctx ends.
func (n *NATS) WatchDesired(ctx context.Context) (<-chan reconcile.Desired, error) {
	kv, err := n.waitBucket(ctx)
	if err != nil {
		return nil, err
	}
	w, err := kv.Watch(ctx, n.nodeID)
	if err != nil {
		return nil, fmt.Errorf("watch %s/%s: %w", DesiredBucket, n.nodeID, err)
	}

	out := make(chan reconcile.Desired, 1)
	go func() {
		defer close(out)
		defer w.Stop() //nolint:errcheck
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-w.Updates():
				if !ok {
					return
				}
				if e == nil { // marker: initial values delivered
					continue
				}
				if e.Operation() != jetstream.KeyValuePut {
					continue
				}
				var d reconcile.Desired
				if err := json.Unmarshal(e.Value(), &d); err != nil {
					n.log.Error("invalid desired state, ignoring", "err", err, "revision", e.Revision())
					continue
				}
				// Keep only the latest value: drop a stale pending one if the reconciler is busy.
				select {
				case <-out:
				default:
				}
				out <- d
			}
		}
	}()
	return out, nil
}

func (n *NATS) waitBucket(ctx context.Context) (jetstream.KeyValue, error) {
	for {
		kv, err := n.js.KeyValue(ctx, DesiredBucket)
		if err == nil {
			return kv, nil
		}
		if !errors.Is(err, jetstream.ErrBucketNotFound) {
			n.log.Debug("desired bucket not reachable yet", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// Report publishes a status event to JetStream (stream ORBIT_STATUS on the hub).
func (n *NATS) Report(ctx context.Context, s reconcile.Status) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err = n.js.Publish(ctx, StatusSubject(n.nodeID), b)
	return err
}

// Heartbeat is the liveness message the operator builds EdgeNode objects from (ADR-0007).
// Its JSON form is a contract with the operator: see "Messages" in agent/README.md.
type Heartbeat struct {
	NodeID    string            `json:"nodeId"`
	Current   string            `json:"current"`
	Phase     string            `json:"phase"`
	RolloutID string            `json:"rolloutId,omitempty"`
	Labels    map[string]string `json:"labels"`
	Time      time.Time         `json:"time"`
}

// Heartbeat publishes hb, fire-and-forget on core NATS: only the latest one matters, and a
// heartbeat lost while the link is down is simply superseded by the next one. NodeID and
// Time are filled in here.
func (n *NATS) Heartbeat(hb Heartbeat) error {
	hb.NodeID = n.nodeID
	hb.Time = time.Now().UTC()
	if hb.Labels == nil {
		hb.Labels = map[string]string{} // encode {} rather than null
	}
	b, err := json.Marshal(hb)
	if err != nil {
		return fmt.Errorf("encode heartbeat: %w", err)
	}
	return n.nc.Publish(HeartbeatSubject(n.nodeID), b)
}
