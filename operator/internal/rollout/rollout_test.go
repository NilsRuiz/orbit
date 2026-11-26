package rollout

import (
	"fmt"
	"slices"
	"testing"
	"time"

	fleetv1alpha1 "github.com/NilsRuiz/orbit/operator/api/v1alpha1"
)

func names(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("sat-%d", i+1)
	}
	return out
}

func sizes(plan [][]string) []int {
	out := make([]int, len(plan))
	for i, w := range plan {
		out[i] = len(w)
	}
	return out
}

func TestPlanWavesSizes(t *testing.T) {
	tests := []struct {
		name     string
		nodes    int
		percents []int32
		want     []int // nodes per wave (not cumulative)
	}{
		{"empty fleet", 0, []int32{10, 50, 100}, []int{0, 0, 0}},
		// Rounding up: a 10 % wave always reaches at least one node, a real canary.
		{"one node", 1, []int32{10, 50, 100}, []int{1, 0, 0}},
		{"three nodes", 3, []int32{10, 50, 100}, []int{1, 1, 1}},
		{"ten nodes", 10, []int32{10, 50, 100}, []int{1, 4, 5}},
		{"ceil", 7, []int32{10, 50, 100}, []int{1, 3, 3}}, // 0.7→1, 3.5→4, 7
		{"single wave", 5, []int32{100}, []int{5}},
		{"repeated percent", 10, []int32{50, 50, 100}, []int{5, 0, 5}},
		{"large fleet", 1000, []int32{1, 10, 100}, []int{10, 90, 900}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := PlanWaves(names(tt.nodes), tt.percents, "payload")
			if got := sizes(plan); !slices.Equal(got, tt.want) {
				t.Errorf("wave sizes = %v, want %v", got, tt.want)
			}
			var all []string
			for _, w := range plan {
				all = append(all, w...)
			}
			slices.Sort(all)
			want := names(tt.nodes)
			slices.Sort(want)
			if !slices.Equal(all, want) {
				t.Errorf("every node must be in exactly one wave: got %v", all)
			}
		})
	}
}

func TestPlanWavesIsDeterministic(t *testing.T) {
	nodes := names(20)
	percents := []int32{10, 50, 100}
	want := PlanWaves(nodes, percents, "payload")

	shuffled := slices.Clone(nodes)
	slices.Reverse(shuffled)
	if got := PlanWaves(shuffled, percents, "payload"); !equalPlans(got, want) {
		t.Errorf("input order changed the plan:\n got %v\nwant %v", got, want)
	}
	if !slices.Equal(nodes, names(20)) {
		t.Error("PlanWaves modified its input")
	}
	// Not alphabetical: the hash spreads canaries over the fleet.
	if slices.Equal(want[0], []string{"sat-1", "sat-10"}) || slices.Equal(want[0], []string{"sat-1", "sat-2"}) {
		t.Errorf("first wave %v looks sorted by name", want[0])
	}
	// Another rollout (salt) orders the fleet differently.
	if other := PlanWaves(nodes, percents, "other"); equalPlans(other, want) {
		t.Errorf("salt has no effect: %v", other)
	}
}

// TestPlanWavesIsStable checks that a node joining the fleet does not reshuffle it: the
// others keep their relative order, so at most the nodes at a wave boundary move by one
// wave.
func TestPlanWavesIsStable(t *testing.T) {
	percents := []int32{10, 50, 100}
	before := order(PlanWaves(names(20), percents, "payload"))
	after := order(PlanWaves(append(names(20), "sat-new"), percents, "payload"))
	after = slices.DeleteFunc(after, func(n string) bool { return n == "sat-new" })
	if !slices.Equal(before, after) {
		t.Errorf("adding a node changed the order of the others:\nbefore %v\nafter  %v", before, after)
	}
}

func order(plan [][]string) []string {
	var out []string
	for _, w := range plan {
		out = append(out, w...)
	}
	return out
}

func equalPlans(a, b [][]string) bool {
	return slices.EqualFunc(a, b, func(x, y []string) bool { return slices.Equal(x, y) })
}

func TestClassify(t *testing.T) {
	const target, id = "1.2.0", "payload"
	st := func(reach fleetv1alpha1.Reachability, phase, current, rolloutID string) fleetv1alpha1.EdgeNodeStatus {
		return fleetv1alpha1.EdgeNodeStatus{Reachability: reach, Phase: phase, CurrentVersion: current, RolloutID: rolloutID}
	}
	r, u := fleetv1alpha1.Reachable, fleetv1alpha1.Unreachable
	tests := []struct {
		name   string
		status fleetv1alpha1.EdgeNodeStatus
		want   Result
	}{
		{"idle on target", st(r, "idle", target, id), Succeeded},
		{"already on target before the rollout", st(r, "idle", target, ""), Succeeded},
		{"succeeded then out of range", st(u, "idle", target, id), Succeeded},
		{"rolled back by this rollout", st(r, "rolled-back", "1.1.0", id), Failed},
		{"failure known, then out of range", st(u, "rolled-back", "1.1.0", id), Failed},
		// A rollback from an earlier rollout says nothing about this version.
		{"rolled back by another rollout", st(r, "rolled-back", "1.1.0", "older"), InProgress},
		{"applying", st(r, "applying", "1.1.0", id), InProgress},
		{"verifying the target", st(r, "verifying", "1.1.0", id), InProgress},
		{"not started yet", st(r, "idle", "1.1.0", ""), InProgress},
		{"out of range, not updated", st(u, "idle", "1.1.0", ""), Pending},
		{"never seen", st("", "", "", ""), Pending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.status, target, id); got != tt.want {
				t.Errorf("Classify = %s, want %s", got, tt.want)
			}
		})
	}
}

func results(s, f, ip, p int, stuck ...int) []Result {
	out := make([]Result, 0, s+f+ip+p)
	for range s {
		out = append(out, Succeeded)
	}
	for range f {
		out = append(out, Failed)
	}
	for range ip {
		out = append(out, InProgress)
	}
	for range p {
		out = append(out, Pending)
	}
	for _, n := range stuck {
		for range n {
			out = append(out, Stuck)
		}
	}
	return out
}

func TestEvaluateGate(t *testing.T) {
	tests := []struct {
		name     string
		results  []Result
		max      int32
		want     Decision
		timedOut bool
	}{
		{"all succeeded", results(3, 0, 0, 0), 0, Promote, false},
		{"waiting for reachable nodes", results(1, 0, 2, 0), 0, Wait, false},
		// max 0 %: the first failure halts, without waiting for the others.
		{"zero tolerance, one failure", results(5, 1, 4, 0), 0, Halt, false},
		{"100 % tolerance never halts", results(0, 5, 0, 0), 100, Promote, false},
		// Exactly at the threshold is accepted: failed / decided <= max.
		{"ratio equal to max", results(3, 1, 0, 0), 25, Promote, false},
		{"ratio above max", results(2, 1, 0, 0), 25, Halt, false},
		// 1 failure out of 10 reachable is 10 % even if the 8 in progress succeed: under
		// 20 %, so wait. 3 failures exceed 20 % whatever the others do: halt now.
		{"can still pass", results(1, 1, 8, 0), 20, Wait, false},
		{"cannot pass anymore", results(1, 3, 6, 0), 20, Halt, false},
		// Unreachable nodes are pending, not failed.
		{"pending do not count", results(2, 0, 0, 5), 0, Promote, false},
		// No evidence: promoting would be blind, halting would treat range as failure.
		{"all pending", results(0, 0, 0, 4), 0, Wait, false},
		// Nodes past their deadline (Deadline) are stuck: they count as failed.
		{"stuck node", results(3, 0, 0, 0, 1), 0, Halt, true},
		{"stuck within tolerance", results(9, 0, 0, 0, 1), 10, Promote, true},
		{"only stuck nodes", results(0, 0, 0, 3, 2), 0, Halt, true},
		{"stuck and others in progress", results(9, 0, 5, 0, 1), 10, Wait, true},
		// Nothing to verify (empty fleet): nothing blocks promotion.
		{"no nodes", nil, 0, Promote, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := EvaluateGate(tt.results, tt.max)
			if g.Decision != tt.want || g.TimedOut != tt.timedOut {
				t.Errorf("EvaluateGate = %+v, want %s (timedOut %v)", g, tt.want, tt.timedOut)
			}
		})
	}
}

func TestEvaluateGateCounts(t *testing.T) {
	g := EvaluateGate(results(2, 1, 3, 4, 1), 50)
	if g.Succeeded != 2 || g.Failed != 1 || g.InProgress != 3 || g.Pending != 4 || g.Stuck != 1 {
		t.Errorf("counts = %+v", g)
	}
}

// TestDeadline: a node back in range gets the full timeout from its return, not from the
// wave start, or every satellite returning late would look stuck.
func TestDeadline(t *testing.T) {
	start := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	const timeout = 10 * time.Minute
	tests := []struct {
		name           string
		reachableSince time.Time
		want           time.Time
	}{
		{"reachable since before the wave", start.Add(-time.Hour), start.Add(timeout)},
		{"never recorded", time.Time{}, start.Add(timeout)},
		{"back in range hours later", start.Add(3 * time.Hour), start.Add(3*time.Hour + timeout)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Deadline(start, tt.reachableSince, timeout); !got.Equal(tt.want) {
				t.Errorf("Deadline = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRelevantNodeChange(t *testing.T) {
	base := fleetv1alpha1.EdgeNode{Status: fleetv1alpha1.EdgeNodeStatus{
		Reachability: fleetv1alpha1.Reachable, Phase: "idle", CurrentVersion: "1.0.0", RolloutID: "r-g1",
	}}
	base.Labels = map[string]string{"orbit.io/constellation": "alpha"}
	change := func(f func(*fleetv1alpha1.EdgeNode)) fleetv1alpha1.EdgeNode {
		n := *base.DeepCopy()
		f(&n)
		return n
	}
	tests := []struct {
		name    string
		updated fleetv1alpha1.EdgeNode
		want    bool
	}{
		{"phase", change(func(n *fleetv1alpha1.EdgeNode) { n.Status.Phase = "applying" }), true},
		{"version", change(func(n *fleetv1alpha1.EdgeNode) { n.Status.CurrentVersion = "1.1.0" }), true},
		{"rollout id", change(func(n *fleetv1alpha1.EdgeNode) { n.Status.RolloutID = "r-g2" }), true},
		{"reachability", change(func(n *fleetv1alpha1.EdgeNode) { n.Status.Reachability = fleetv1alpha1.Unreachable }), true},
		{"labels", change(func(n *fleetv1alpha1.EdgeNode) { n.Labels = nil }), true},
		{"lastSeen only", change(func(n *fleetv1alpha1.EdgeNode) { n.Status.ObservedStatusSeq = 9 }), false},
		{"nothing", base, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RelevantNodeChange(&base, &tt.updated); got != tt.want {
				t.Errorf("RelevantNodeChange = %v, want %v", got, tt.want)
			}
		})
	}
}
