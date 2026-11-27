package reconcile

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/NilsRuiz/orbit/agent/internal/state"
)

type fakeApplier struct {
	applied []string
	failOn  string
}

func (f *fakeApplier) Apply(_ context.Context, v string) error {
	f.applied = append(f.applied, v)
	if v == f.failOn {
		return errors.New("boom")
	}
	return nil
}

type fakeHealth struct {
	unhealthy string
	reason    string // why unhealthy is not healthy; empty: no reason given
}

func (f fakeHealth) Healthy(_ context.Context, v string) (bool, error) {
	if v == f.unhealthy && f.reason != "" {
		return false, errors.New(f.reason)
	}
	return v != f.unhealthy, nil
}

type memStore struct{ saved []state.State }

func (m *memStore) Save(s state.State) error { m.saved = append(m.saved, s); return nil }

type memReporter struct{ statuses []Status }

func (m *memReporter) Report(_ context.Context, s Status) error {
	m.statuses = append(m.statuses, s)
	return nil
}

func newTest(initial state.State, a *fakeApplier, h fakeHealth) (*Reconciler, *memReporter) {
	rep := &memReporter{}
	cfg := Config{NodeID: "sat-test", VerifyTimeout: 50 * time.Millisecond, VerifyInterval: 5 * time.Millisecond}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(cfg, initial, &memStore{}, a, h, rep, log), rep
}

func phases(rep *memReporter) []state.Phase {
	var out []state.Phase
	for _, s := range rep.statuses {
		out = append(out, s.Phase)
	}
	return out
}

func TestReconcile_HealthyUpgrade(t *testing.T) {
	a := &fakeApplier{}
	r, rep := newTest(state.State{Current: "1.0.0", Phase: state.PhaseIdle}, a, fakeHealth{})

	if err := r.Reconcile(context.Background(), Desired{Version: "1.1.0"}); err != nil {
		t.Fatal(err)
	}
	st := r.State()
	if st.Current != "1.1.0" || st.Previous != "1.0.0" || st.Phase != state.PhaseIdle {
		t.Fatalf("unexpected state: %+v", st)
	}
	want := []state.Phase{state.PhaseApplying, state.PhaseVerifying, state.PhaseIdle}
	if got := phases(rep); !equal(got, want) {
		t.Fatalf("phases = %v, want %v", got, want)
	}
}

func TestReconcile_UnhealthyRollsBack(t *testing.T) {
	a := &fakeApplier{}
	r, rep := newTest(state.State{Current: "1.0.0", Phase: state.PhaseIdle}, a, fakeHealth{unhealthy: "6.6.6"})

	if err := r.Reconcile(context.Background(), Desired{Version: "6.6.6"}); err != nil {
		t.Fatal(err)
	}
	st := r.State()
	if st.Current != "1.0.0" || st.Phase != state.PhaseRolledBack || st.LastFailed != "6.6.6" {
		t.Fatalf("unexpected state: %+v", st)
	}
	if last := a.applied[len(a.applied)-1]; last != "1.0.0" {
		t.Fatalf("last applied = %s, want rollback to 1.0.0", last)
	}
	if got := phases(rep); got[len(got)-1] != state.PhaseRolledBack {
		t.Fatalf("phases = %v", got)
	}

	// Same bad desired state again must not loop.
	n := len(a.applied)
	if err := r.Reconcile(context.Background(), Desired{Version: "6.6.6"}); err != nil {
		t.Fatal(err)
	}
	if len(a.applied) != n {
		t.Fatalf("agent retried a version that already failed")
	}
}

func TestReconcile_RollbackReportsWhyUnhealthy(t *testing.T) {
	h := fakeHealth{unhealthy: "6.6.6", reason: "deployment: 1 old replicas pending termination"}
	r, rep := newTest(state.State{Current: "1.0.0", Phase: state.PhaseIdle}, &fakeApplier{}, h)

	if err := r.Reconcile(context.Background(), Desired{Version: "6.6.6"}); err != nil {
		t.Fatal(err)
	}
	last := rep.statuses[len(rep.statuses)-1]
	want := "not healthy within 50ms: deployment: 1 old replicas pending termination"
	if last.Phase != state.PhaseRolledBack || last.Message != want {
		t.Fatalf("last status = %s %q, want rolled-back %q", last.Phase, last.Message, want)
	}
}

func TestReconcile_ApplyErrorRollsBack(t *testing.T) {
	a := &fakeApplier{failOn: "2.0.0"}
	r, _ := newTest(state.State{Current: "1.0.0", Phase: state.PhaseIdle}, a, fakeHealth{})

	if err := r.Reconcile(context.Background(), Desired{Version: "2.0.0"}); err != nil {
		t.Fatal(err)
	}
	if st := r.State(); st.Current != "1.0.0" || st.Phase != state.PhaseRolledBack {
		t.Fatalf("unexpected state: %+v", st)
	}
}

func TestResume_InterruptedTransitionRollsBack(t *testing.T) {
	a := &fakeApplier{}
	r, _ := newTest(state.State{Current: "1.0.0", Pending: "1.1.0", Phase: state.PhaseVerifying}, a, fakeHealth{})

	if err := r.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := r.State(); st.Current != "1.0.0" || st.Phase != state.PhaseRolledBack || st.Pending != "" {
		t.Fatalf("unexpected state: %+v", st)
	}
}

func TestReconcile_NoopWhenAlreadyCurrent(t *testing.T) {
	a := &fakeApplier{}
	r, rep := newTest(state.State{Current: "1.0.0", Phase: state.PhaseIdle}, a, fakeHealth{})

	if err := r.Reconcile(context.Background(), Desired{Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if len(a.applied) != 0 || len(rep.statuses) != 0 {
		t.Fatal("expected no-op")
	}
}

func equal(a, b []state.Phase) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
