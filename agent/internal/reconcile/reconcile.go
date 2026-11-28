// Package reconcile holds the agent's core logic: converge the node to a desired version,
// verify it, and roll back locally if verification fails (dead man's switch, ADR-0004).
// It has no NATS dependency so it can be unit-tested in isolation.
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/NilsRuiz/orbit/agent/internal/state"
)

// Desired is what the hub wants this node to run (value of its key in orbit-desired).
type Desired struct {
	Version   string `json:"version"`
	RolloutID string `json:"rolloutId,omitempty"`
}

// Applier switches the payload workload to a version. The real implementation will patch
// the local Flux OCIRepository tag on k3s; the simulator only pretends.
type Applier interface {
	Apply(ctx context.Context, version string) error
}

// HealthChecker reports whether the payload is healthy on the given version. When it is
// not, the error says why: NotReady(reason) for the payload's state (a version still on its
// way, a pod never ready), any other error for a check that failed. The rollback reports
// the last NotReady reason, else the last error.
type HealthChecker interface {
	Healthy(ctx context.Context, version string) (bool, error)
}

// notReady is a HealthChecker's reason why the payload is not healthy yet, as opposed to
// an error while checking (an API call that failed).
type notReady struct{ reason string }

func (e notReady) Error() string { return e.reason }

// NotReady says why the payload is not healthy yet. A rollback reports the last such
// reason in preference to later check errors, which say nothing about the payload.
func NotReady(reason string) error { return notReady{reason} }

// IsNotReady reports whether err is a NotReady reason.
func IsNotReady(err error) bool { return errors.As(err, new(notReady)) }

// Store persists state between restarts.
type Store interface {
	Save(state.State) error
}

// Status is reported to the hub after each transition.
type Status struct {
	NodeID    string      `json:"nodeId"`
	Phase     state.Phase `json:"phase"`
	Current   string      `json:"current"`
	Target    string      `json:"target,omitempty"`
	RolloutID string      `json:"rolloutId,omitempty"`
	Message   string      `json:"message,omitempty"`
	Time      time.Time   `json:"time"`
}

// Reporter sends status updates (best effort; delivery guarantees belong to the transport).
type Reporter interface {
	Report(ctx context.Context, s Status) error
}

// Config tunes the reconciler.
type Config struct {
	NodeID string
	// VerifyTimeout is the dead man's switch deadline after an apply.
	VerifyTimeout time.Duration
	// VerifyInterval is how often health is polled during verification.
	VerifyInterval time.Duration
}

// Reconciler converges the node to desired versions. Reconcile and Resume must be called
// from a single goroutine; State is safe for concurrent use.
type Reconciler struct {
	cfg      Config
	st       state.State
	store    Store
	applier  Applier
	health   HealthChecker
	reporter Reporter
	log      *slog.Logger
	// snapshot is read by other goroutines (heartbeat); st is owned by the reconcile loop.
	snapshot atomic.Pointer[state.State]
}

// New returns a Reconciler starting from the persisted state initial.
func New(cfg Config, initial state.State, store Store, a Applier, h HealthChecker, r Reporter, log *slog.Logger) *Reconciler {
	if cfg.VerifyInterval <= 0 {
		cfg.VerifyInterval = time.Second
	}
	rec := &Reconciler{cfg: cfg, st: initial, store: store, applier: a, health: h, reporter: r, log: log}
	rec.snapshot.Store(&initial)
	return rec
}

// State returns a copy of the last persisted state. Safe for concurrent use.
func (r *Reconciler) State() state.State { return *r.snapshot.Load() }

// Resume finishes a transition interrupted by a crash or reboot: if we died while
// applying/verifying, the pending version is not trusted and we go back to Current.
func (r *Reconciler) Resume(ctx context.Context) error {
	switch r.st.Phase {
	case state.PhaseApplying, state.PhaseVerifying:
		r.log.Warn("interrupted transition found at boot, rolling back", "pending", r.st.Pending, "current", r.st.Current)
		return r.rollback(ctx, "interrupted transition at boot")
	default:
		return nil
	}
}

// Reconcile converges the node towards d. It blocks until the transition is finished
// (healthy or rolled back) or ctx is cancelled.
func (r *Reconciler) Reconcile(ctx context.Context, d Desired) error {
	if d.Version == "" {
		return fmt.Errorf("desired version is empty")
	}
	if d.Version == r.st.Current {
		r.log.Debug("already on desired version", "version", d.Version)
		return nil
	}
	if d.Version == r.st.LastFailed {
		r.log.Info("desired version previously failed verification, waiting for a new desired state", "version", d.Version)
		return nil
	}

	r.st.Pending = d.Version
	r.st.RolloutID = d.RolloutID
	if err := r.transition(ctx, state.PhaseApplying, "applying "+d.Version); err != nil {
		return err
	}

	if err := r.applier.Apply(ctx, d.Version); err != nil {
		return r.rollback(ctx, "apply failed: "+err.Error())
	}

	if err := r.transition(ctx, state.PhaseVerifying, "verifying "+d.Version); err != nil {
		return err
	}

	if ok, why := r.verify(ctx, d.Version); !ok {
		if ctx.Err() != nil {
			return ctx.Err() // shutting down: Resume() will roll back on next boot
		}
		reason := fmt.Sprintf("not healthy within %s", r.cfg.VerifyTimeout)
		if why != nil {
			reason += ": " + why.Error()
		}
		return r.rollback(ctx, reason)
	}

	r.st.Previous = r.st.Current
	r.st.Current = d.Version
	r.st.Pending = ""
	r.st.LastFailed = ""
	return r.transition(ctx, state.PhaseIdle, "healthy on "+d.Version)
}

// verify polls health until healthy or the dead man's switch fires. When it fires, it
// returns the last reason the health checker gave (nil if none).
func (r *Reconciler) verify(ctx context.Context, version string) (bool, error) {
	deadline, cancel := context.WithTimeout(ctx, r.cfg.VerifyTimeout)
	defer cancel()
	ticker := time.NewTicker(r.cfg.VerifyInterval)
	defer ticker.Stop()

	// The last reason the payload is not healthy (NotReady), and the last error while
	// checking. A check failing near the deadline (cut short, rate-limited) says nothing
	// about the payload: the reason wins over it.
	var reason, checkErr error
	for {
		ok, err := r.health.Healthy(deadline, version)
		if ok {
			return true, nil
		}
		switch {
		case IsNotReady(err):
			reason = err
			r.log.Debug("not healthy yet", "version", version, "reason", err)
		case err != nil && deadline.Err() == nil:
			checkErr = err
			r.log.Debug("health check failed", "version", version, "err", err)
		}
		select {
		case <-deadline.Done():
			if reason != nil {
				return false, reason
			}
			return false, checkErr
		case <-ticker.C:
		}
	}
}

func (r *Reconciler) rollback(ctx context.Context, reason string) error {
	failed := r.st.Pending
	r.log.Warn("rolling back", "failed", failed, "to", r.st.Current, "reason", reason)
	if err := r.applier.Apply(ctx, r.st.Current); err != nil {
		// Nothing more we can do locally; report and keep the state for the next attempt.
		r.log.Error("rollback apply failed", "err", err)
	}
	r.st.LastFailed = failed
	r.st.Pending = ""
	return r.transition(ctx, state.PhaseRolledBack, reason)
}

// transition persists the new phase *before* reporting, so the disk is the source of truth.
func (r *Reconciler) transition(ctx context.Context, p state.Phase, msg string) error {
	r.st.Phase = p
	if err := r.store.Save(r.st); err != nil {
		return fmt.Errorf("persist state: %w", err)
	}
	snap := r.st
	r.snapshot.Store(&snap)
	s := Status{
		NodeID:    r.cfg.NodeID,
		Phase:     p,
		Current:   r.st.Current,
		Target:    r.st.Pending,
		RolloutID: r.st.RolloutID,
		Message:   msg,
		Time:      time.Now().UTC(),
	}
	if err := r.reporter.Report(ctx, s); err != nil {
		r.log.Warn("status report failed (will be visible on next report)", "err", err)
	}
	r.log.Info("transition", "phase", p, "current", r.st.Current, "message", msg)
	return nil
}
