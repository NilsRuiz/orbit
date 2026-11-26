// Package ingest turns what agents send to the hub into EdgeNode objects (ADR-0007):
// heartbeats register nodes and refresh their state, status events from the ORBIT_STATUS
// stream are applied in order through a durable consumer.
package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fleetv1alpha1 "github.com/NilsRuiz/orbit/operator/api/v1alpha1"
	"github.com/NilsRuiz/orbit/operator/internal/edgenode"
)

// NATS names shared with the agent (agent/README.md, "Messages") and
// hack/scripts/init-hub.sh.
const (
	HeartbeatSubjects = "orbit.edge.*.heartbeat"
	StatusStream      = "ORBIT_STATUS"
	// ConsumerName is the operator's durable consumer on StatusStream.
	ConsumerName = "orbit-operator"
)

// nakDelay is how long a status event waits before redelivery when it could not be
// applied (API server unavailable, conflict storm).
const nakDelay = 5 * time.Second

// Heartbeat is the agent's periodic heartbeat (agent transport.Heartbeat).
type Heartbeat struct {
	NodeID    string            `json:"nodeId"`
	Current   string            `json:"current"`
	Phase     string            `json:"phase"`
	RolloutID string            `json:"rolloutId,omitempty"`
	Labels    map[string]string `json:"labels"`
	Time      time.Time         `json:"time"`
}

// Status is a status event, one per agent state transition (agent reconcile.Status).
type Status struct {
	NodeID    string    `json:"nodeId"`
	Phase     string    `json:"phase"`
	Current   string    `json:"current"`
	Target    string    `json:"target,omitempty"`
	RolloutID string    `json:"rolloutId,omitempty"`
	Message   string    `json:"message,omitempty"`
	Time      time.Time `json:"time"`
}

// Ingester subscribes to agent messages and writes EdgeNodes. It is a manager Runnable
// that runs only in the leader replica, so a single writer applies the status stream.
type Ingester struct {
	NC       *nats.Conn
	Client   client.Client
	Recorder events.EventRecorder
	Log      *slog.Logger
	// Now is the hub clock (time.Now by default): lastSeen uses reception time, never
	// the agent's clock.
	Now func() time.Time
	// SetupRetry is the delay between attempts to create the status consumer (5 s by
	// default), e.g. while the stream does not exist yet.
	SetupRetry time.Duration
}

// NeedLeaderElection keeps the ingester in the leader replica only.
func (in *Ingester) NeedLeaderElection() bool { return true }

// Start subscribes to heartbeats and consumes status events until ctx is done.
func (in *Ingester) Start(ctx context.Context) error {
	if in.Now == nil {
		in.Now = time.Now
	}
	if in.SetupRetry == 0 {
		in.SetupRetry = 5 * time.Second
	}

	sub, err := in.NC.Subscribe(HeartbeatSubjects, func(m *nats.Msg) { in.onHeartbeat(ctx, m) })
	if err != nil {
		return fmt.Errorf("subscribe %s: %w", HeartbeatSubjects, err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	cons, err := in.statusConsumer(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	cc, err := cons.Consume(func(m jetstream.Msg) { in.onStatus(ctx, m) },
		jetstream.ConsumeErrHandler(func(_ jetstream.ConsumeContext, err error) {
			in.Log.Warn("status consumer error", "err", err)
		}))
	if err != nil {
		return fmt.Errorf("consume %s: %w", StatusStream, err)
	}
	in.Log.Info("ingesting agent messages", "heartbeats", HeartbeatSubjects, "stream", StatusStream, "consumer", ConsumerName)
	<-ctx.Done()
	// Drain lets the handler finish the message in flight, so it is acked, not redelivered.
	cc.Drain()
	<-cc.Closed()
	return nil
}

// statusConsumer creates (or finds) the durable consumer, retrying until the stream
// exists: the hub may come up after the operator.
func (in *Ingester) statusConsumer(ctx context.Context) (jetstream.Consumer, error) {
	js, err := jetstream.New(in.NC)
	if err != nil {
		return nil, fmt.Errorf("jetstream: %w", err)
	}
	for {
		cons, err := js.CreateOrUpdateConsumer(ctx, StatusStream, jetstream.ConsumerConfig{
			Durable:   ConsumerName,
			AckPolicy: jetstream.AckExplicitPolicy,
			// A new consumer replays the stream: events already applied are skipped
			// through status.observedStatusSeq.
			DeliverPolicy: jetstream.DeliverAllPolicy,
		})
		if err == nil {
			return cons, nil
		}
		in.Log.Warn("status consumer not ready, retrying", "stream", StatusStream, "err", err, "retry", in.SetupRetry)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(in.SetupRetry):
		}
	}
}

// nodeFromSubject returns <node> in orbit.edge.<node>.<kind>.
func nodeFromSubject(subject string) string {
	parts := strings.Split(subject, ".")
	if len(parts) != 4 {
		return ""
	}
	return parts[2]
}

// checkNode accepts a message only if it names a valid node and the subject matches the
// payload: a node must not speak for another one (ADR-0007, trust limits).
func checkNode(subject, payloadID string) (string, error) {
	id := nodeFromSubject(subject)
	if !edgenode.ValidNodeID(id) {
		return "", fmt.Errorf("node ID %q is not a DNS-1123 label", id)
	}
	if payloadID != id {
		return "", fmt.Errorf("payload nodeId %q does not match subject node %q", payloadID, id)
	}
	return id, nil
}

func (in *Ingester) onHeartbeat(ctx context.Context, m *nats.Msg) {
	var hb Heartbeat
	if err := json.Unmarshal(m.Data, &hb); err != nil {
		in.Log.Warn("dropping heartbeat: invalid JSON", "subject", m.Subject, "err", err)
		return
	}
	id, err := checkNode(m.Subject, hb.NodeID)
	if err != nil {
		in.Log.Warn("dropping heartbeat", "subject", m.Subject, "err", err)
		return
	}
	if err := in.applyHeartbeat(ctx, id, hb, in.Now()); err != nil {
		// The next heartbeat retries: no need to queue this one.
		in.Log.Warn("apply heartbeat", "node", id, "err", err)
	}
}

func (in *Ingester) applyHeartbeat(ctx context.Context, id string, hb Heartbeat, seen time.Time) error {
	if !edgenode.ValidPhase(hb.Phase) {
		in.Log.Warn("ignoring invalid phase in heartbeat", "node", id, "phase", hb.Phase)
		hb.Phase = ""
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		en, err := in.register(ctx, id, hb.Labels)
		if err != nil {
			return err
		}
		if err := in.syncLabels(ctx, en, hb.Labels); err != nil {
			return err
		}
		next := en.Status.DeepCopy()
		if hb.Current != "" {
			next.CurrentVersion = hb.Current
		}
		if hb.Phase != "" {
			next.Phase = hb.Phase
		}
		next.RolloutID = hb.RolloutID
		return in.writeStatus(ctx, en, next, seen)
	})
}

// register returns the EdgeNode named id, creating it with the reported labels on first
// contact. EdgeNodes are never deleted by the operator: silence means out of range.
func (in *Ingester) register(ctx context.Context, id string, reported map[string]string) (*fleetv1alpha1.EdgeNode, error) {
	en := &fleetv1alpha1.EdgeNode{}
	err := in.Client.Get(ctx, client.ObjectKey{Name: id}, en)
	if err == nil || !apierrors.IsNotFound(err) {
		return en, err
	}
	labels, _, rejected := edgenode.SyncLabels(nil, reported)
	en = &fleetv1alpha1.EdgeNode{ObjectMeta: metav1.ObjectMeta{Name: id, Labels: labels}}
	if err := in.Client.Create(ctx, en); err != nil {
		// AlreadyExists with a stale cache: the next heartbeat sees the object.
		return nil, fmt.Errorf("create EdgeNode: %w", err)
	}
	in.Log.Info("registered edge node", "node", id, "labels", labels)
	in.Recorder.Eventf(en, nil, corev1.EventTypeNormal, edgenode.ReasonRegistered, "Register",
		"Registered from first heartbeat with labels %v", labels)
	in.reportRejected(en, rejected)
	return en, nil
}

// syncLabels makes the orbit.io/ labels follow the agent, leaving other labels alone.
func (in *Ingester) syncLabels(ctx context.Context, en *fleetv1alpha1.EdgeNode, reported map[string]string) error {
	labels, changes, rejected := edgenode.SyncLabels(en.Labels, reported)
	if len(changes) == 0 {
		if len(rejected) > 0 {
			in.Log.Warn("ignoring reported labels", "node", en.Name, "labels", rejected)
		}
		return nil
	}
	base := en.DeepCopy()
	en.Labels = labels
	if err := in.Client.Patch(ctx, en, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("patch labels: %w", err)
	}
	// Labels decide which fleets, and so which rollouts, a node gets: keep a trail.
	in.Log.Info("edge node labels changed", "node", en.Name, "changes", changes)
	in.Recorder.Eventf(en, nil, corev1.EventTypeNormal, edgenode.ReasonLabelsChanged, "SyncLabels",
		"Labels reported by the agent: %s", strings.Join(changes, " "))
	in.reportRejected(en, rejected)
	return nil
}

func (in *Ingester) reportRejected(en *fleetv1alpha1.EdgeNode, rejected []string) {
	if len(rejected) == 0 {
		return
	}
	in.Log.Warn("ignoring reported labels", "node", en.Name, "labels", rejected)
	in.Recorder.Eventf(en, nil, corev1.EventTypeWarning, edgenode.ReasonLabelsRejected, "SyncLabels",
		"Ignored labels outside %s or with invalid syntax: %s", edgenode.LabelPrefix, strings.Join(rejected, " "))
}

// writeStatus writes next if it differs from the stored status, or if lastSeen is due
// under the write budget. A write always carries a fresh lastSeen.
func (in *Ingester) writeStatus(ctx context.Context, en *fleetv1alpha1.EdgeNode, next *fleetv1alpha1.EdgeNodeStatus, seen time.Time) error {
	prev := en.Status
	var prevSeen time.Time
	if prev.LastSeen != nil {
		prevSeen = prev.LastSeen.Time
	}
	ci := en.Spec.ContactInterval.Duration
	if seen.After(prevSeen) {
		next.LastSeen = &metav1.Time{Time: seen}
	}
	next.Reachability, _ = edgenode.Reachability(lastSeenOf(next), ci, in.Now())

	changed := !equality.Semantic.DeepEqual(withLastSeen(prev, nil), withLastSeen(*next, nil))
	if !changed && !edgenode.ShouldWriteLastSeen(prevSeen, seen, ci) {
		return nil
	}
	base := en.DeepCopy()
	en.Status = *next
	if err := in.Client.Status().Patch(ctx, en, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("patch status: %w", err)
	}
	// A node never seen before is on its first contact, not back in contact, even if the
	// controller already marked it Unreachable.
	if prev.Reachability == fleetv1alpha1.Unreachable && next.Reachability == fleetv1alpha1.Reachable && !prevSeen.IsZero() {
		in.Log.Info("edge node back in contact", "node", en.Name, "silentFor", seen.Sub(prevSeen).Round(time.Second).String())
		in.Recorder.Eventf(en, nil, corev1.EventTypeNormal, edgenode.ReasonContactRestored, "Contact",
			"Back in contact after %s", seen.Sub(prevSeen).Round(time.Second))
	}
	return nil
}

func lastSeenOf(s *fleetv1alpha1.EdgeNodeStatus) time.Time {
	if s.LastSeen == nil {
		return time.Time{}
	}
	return s.LastSeen.Time
}

func withLastSeen(s fleetv1alpha1.EdgeNodeStatus, t *metav1.Time) fleetv1alpha1.EdgeNodeStatus {
	s.LastSeen = t
	return s
}

func (in *Ingester) onStatus(ctx context.Context, m jetstream.Msg) {
	meta, err := m.Metadata()
	if err != nil {
		in.Log.Warn("dropping status event: no JetStream metadata", "subject", m.Subject(), "err", err)
		_ = m.Term()
		return
	}
	log := in.Log.With("subject", m.Subject(), "seq", meta.Sequence.Stream)
	var st Status
	if err := json.Unmarshal(m.Data(), &st); err != nil {
		log.Warn("dropping status event: invalid JSON", "err", err)
		_ = m.Term()
		return
	}
	id, err := checkNode(m.Subject(), st.NodeID)
	if err != nil {
		log.Warn("dropping status event", "err", err)
		_ = m.Term()
		return
	}
	// The stream timestamp is the hub's clock when the event was stored.
	if err := in.applyStatus(ctx, id, st, int64(meta.Sequence.Stream), meta.Timestamp); err != nil {
		log.Warn("apply status event, will retry", "node", id, "err", err, "retry", nakDelay)
		_ = m.NakWithDelay(nakDelay)
		return
	}
	if err := m.Ack(); err != nil {
		log.Warn("ack status event", "err", err)
	}
}

func (in *Ingester) applyStatus(ctx context.Context, id string, st Status, seq int64, stored time.Time) error {
	if !edgenode.ValidPhase(st.Phase) {
		in.Log.Warn("ignoring invalid phase in status event", "node", id, "phase", st.Phase)
		st.Phase = ""
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		en := &fleetv1alpha1.EdgeNode{}
		if err := in.Client.Get(ctx, client.ObjectKey{Name: id}, en); err != nil {
			if apierrors.IsNotFound(err) {
				// Only heartbeats register nodes; the heartbeat also carries this state.
				in.Log.Debug("status event for unknown node, dropped", "node", id, "seq", seq)
				return nil
			}
			return err
		}
		if seq <= en.Status.ObservedStatusSeq {
			return nil // replay
		}
		next := en.Status.DeepCopy()
		next.ObservedStatusSeq = seq
		// An event stored before the last contact is older than the state that contact
		// reported (a replayed backlog after an outage): only record that it was seen.
		fresh := en.Status.LastSeen == nil || stored.After(en.Status.LastSeen.Time)
		if fresh {
			if st.Current != "" {
				next.CurrentVersion = st.Current
			}
			if st.Phase != "" {
				next.Phase = st.Phase
			}
			next.RolloutID = st.RolloutID
		}
		if err := in.writeStatus(ctx, en, next, stored); err != nil {
			return err
		}
		if fresh && st.Phase == "rolled-back" {
			in.Log.Warn("edge node rolled back", "node", id, "rolloutId", st.RolloutID, "target", st.Target, "message", st.Message)
			in.Recorder.Eventf(en, nil, corev1.EventTypeWarning, edgenode.ReasonRolledBack, "Rollback",
				"Rolled back from %s to %s (rollout %q): %s", st.Target, st.Current, st.RolloutID, st.Message)
		}
		return nil
	})
}
