// Package desired reads and writes the orbit-desired JetStream KV bucket, where each
// node's agent watches its desired state (key = node ID). Writes are compare-and-swap on
// the KV revision, so a concurrent writer is detected instead of overwritten.
//
// The value also records the version to restore if the rollout is halted
// (previousVersion), so the bucket carries its own revert trail.
package desired

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/nats-io/nats.go/jetstream"
)

// Bucket is the desired-state KV bucket, created by the hub (hack/scripts/init-hub.sh).
const Bucket = "orbit-desired"

// RevertSuffix marks a value written by a revert: rolloutId "<id>-revert". The agent
// reports it, so `kubectl get en` shows which nodes were reverted, and a second revert
// does not recognise the value as its own.
const RevertSuffix = "-revert"

// ErrConflict means the entry changed between read and write. The caller retries from
// a fresh read (the Rollout controller requeues).
var ErrConflict = errors.New("desired state changed concurrently")

// Value is a node's desired state. The agent reads version and rolloutId
// (agent reconcile.Desired) and ignores the rest.
type Value struct {
	Version   string `json:"version"`
	RolloutID string `json:"rolloutId,omitempty"`
	// PreviousVersion is what the node should go back to if this rollout is halted.
	PreviousVersion string `json:"previousVersion,omitempty"`
}

// Entry is a value with its KV revision. Exists is false when the key has no value.
type Entry struct {
	Value
	Revision uint64
	Exists   bool
}

// Store is the operator's access to the bucket.
type Store struct {
	kv jetstream.KeyValue
}

// Open binds to the existing bucket.
func Open(ctx context.Context, js jetstream.JetStream) (*Store, error) {
	kv, err := js.KeyValue(ctx, Bucket)
	if err != nil {
		return nil, fmt.Errorf("open KV %s: %w", Bucket, err)
	}
	return &Store{kv: kv}, nil
}

// Get returns the node's entry. A missing or deleted key gives Exists false.
func (s *Store) Get(ctx context.Context, node string) (Entry, error) {
	e, err := s.kv.Get(ctx, node)
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return Entry{}, nil
	}
	if err != nil {
		return Entry{}, fmt.Errorf("get %s/%s: %w", Bucket, node, err)
	}
	var v Value
	if err := json.Unmarshal(e.Value(), &v); err != nil {
		// Not ours to fix: a write by this operator replaces it, with the previous
		// version unknown.
		return Entry{Revision: e.Revision(), Exists: true}, nil
	}
	return Entry{Value: v, Revision: e.Revision(), Exists: true}, nil
}

// Ensure makes the node's desired state version for rolloutID, unless it already is or
// rolloutID already reverted the node.
// fallbackPrevious (the version the node reports) is recorded as the version to revert
// to when the bucket held nothing for the node. It reports whether it wrote.
func (s *Store) Ensure(ctx context.Context, node, version, rolloutID, fallbackPrevious string) (bool, error) {
	e, err := s.Get(ctx, node)
	if err != nil {
		return false, err
	}
	v, write := planEnsure(e, version, rolloutID, fallbackPrevious)
	if !write {
		return false, nil
	}
	return true, s.put(ctx, node, e, v)
}

// Revert restores the version the node had before rolloutID, if its desired state is
// still the one rolloutID wrote. Without a previous version the key is deleted, leaving
// the node on the version it runs. It reports whether it wrote.
func (s *Store) Revert(ctx context.Context, node, rolloutID string) (bool, error) {
	e, err := s.Get(ctx, node)
	if err != nil {
		return false, err
	}
	switch action, v := planRevert(e, rolloutID); action {
	case revertPut:
		return true, s.put(ctx, node, e, v)
	case revertDelete:
		return true, s.delete(ctx, node, e)
	default:
		return false, nil
	}
}

func planEnsure(e Entry, version, rolloutID, fallbackPrevious string) (Value, bool) {
	if e.Exists && e.Version == version && e.RolloutID == rolloutID {
		return e.Value, false
	}
	// A run that reverted the node never pushes its version again: a halted run is final.
	if e.Exists && e.RolloutID == rolloutID+RevertSuffix {
		return e.Value, false
	}
	previous := fallbackPrevious
	if e.Exists && e.Version != "" {
		previous = e.Version
	}
	return Value{Version: version, RolloutID: rolloutID, PreviousVersion: previous}, true
}

type revertAction int

const (
	revertNone revertAction = iota
	revertPut
	revertDelete
)

func (a revertAction) String() string {
	return [...]string{"none", "put", "delete"}[a]
}

func planRevert(e Entry, rolloutID string) (revertAction, Value) {
	if !e.Exists || e.RolloutID != rolloutID || strings.HasSuffix(e.RolloutID, RevertSuffix) {
		return revertNone, Value{}
	}
	if e.PreviousVersion == "" {
		return revertDelete, Value{}
	}
	return revertPut, Value{Version: e.PreviousVersion, RolloutID: rolloutID + RevertSuffix}
}

// put writes v if the key is still at e's revision (or still missing).
func (s *Store) put(ctx context.Context, node string, e Entry, v Value) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode desired state: %w", err)
	}
	if e.Exists {
		_, err = s.kv.Update(ctx, node, b, e.Revision)
	} else {
		_, err = s.kv.Create(ctx, node, b)
	}
	return casError(node, err)
}

func (s *Store) delete(ctx context.Context, node string, e Entry) error {
	return casError(node, s.kv.Delete(ctx, node, jetstream.LastRevision(e.Revision)))
}

func casError(node string, err error) error {
	var apiErr *jetstream.APIError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, jetstream.ErrKeyExists),
		errors.As(err, &apiErr) && apiErr.ErrorCode == jetstream.JSErrCodeStreamWrongLastSequence:
		return fmt.Errorf("%s/%s: %w", Bucket, node, ErrConflict)
	default:
		return fmt.Errorf("write %s/%s: %w", Bucket, node, err)
	}
}

// Lazy opens the bucket on first use and keeps it: the operator may start before the hub
// has created the bucket. Safe for concurrent use.
type Lazy struct {
	JS jetstream.JetStream

	mu    sync.Mutex
	store *Store
}

// Store returns the opened store, opening it if needed.
func (l *Lazy) Store(ctx context.Context) (*Store, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.store == nil {
		s, err := Open(ctx, l.JS)
		if err != nil {
			return nil, err
		}
		l.store = s
	}
	return l.store, nil
}
