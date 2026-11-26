// Package rollout holds the rollout rules as pure functions (ADR-0004): which nodes go in
// which wave, what a node's state means for a rollout, and whether a wave's health gate
// lets the rollout promote. No Kubernetes client, no NATS, no clock: the Rollout
// controller feeds them.
package rollout

import (
	"cmp"
	"hash/fnv"
	"slices"
	"time"

	fleetv1alpha1 "github.com/NilsRuiz/orbit/operator/api/v1alpha1"
)

// PlanWaves splits nodes into waves. percents are cumulative (spec.waves, last == 100):
// wave i ends with ceil(percents[i] × len(nodes) / 100) nodes updated, so a small first
// wave still reaches one node. Waves may be empty; the result has one entry per percent.
//
// Nodes are ordered by an FNV-1a hash of salt + "/" + name, ties broken by name. The plan
// does not depend on the input order, is not alphabetical, and a node joining the fleet
// leaves the relative order of the others unchanged. The Rollout controller uses the
// rollout name as salt, so every release of a Rollout starts with the same canaries.
func PlanWaves(nodes []string, percents []int32, salt string) [][]string {
	type ranked struct {
		name string
		hash uint64
	}
	order := make([]ranked, len(nodes))
	for i, n := range nodes {
		order[i] = ranked{n, rank(salt, n)}
	}
	slices.SortFunc(order, func(a, b ranked) int {
		return cmp.Or(cmp.Compare(a.hash, b.hash), cmp.Compare(a.name, b.name))
	})

	waves := make([][]string, len(percents))
	start := 0
	for i, p := range percents {
		end := min(ceilPercent(len(nodes), p), len(nodes))
		end = max(end, start) // percents never decrease (CRD validation); stay safe anyway
		waves[i] = make([]string, 0, end-start)
		for _, r := range order[start:end] {
			waves[i] = append(waves[i], r.name)
		}
		start = end
	}
	return waves
}

func rank(salt, name string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(salt + "/" + name)) // hash.Hash.Write never fails
	return h.Sum64()
}

// ceilPercent is ceil(n × p / 100) in integers.
func ceilPercent(n int, p int32) int {
	return (n*int(p) + 99) / 100
}

// Result is what a node's state means for one rollout.
type Result string

// Result values.
const (
	// Succeeded: idle on the target version.
	Succeeded Result = "Succeeded"
	// Failed: the agent rolled back from this rollout's version.
	Failed Result = "Failed"
	// InProgress: reachable, not on the target yet.
	InProgress Result = "InProgress"
	// Pending: unreachable (out of range) and not known to have succeeded or failed.
	// Not a failure.
	Pending Result = "Pending"
)

// Classify tells where a node stands in the rollout rolloutID to version target. A known
// outcome (success or failure) stands even when the node is out of range now.
func Classify(s fleetv1alpha1.EdgeNodeStatus, target, rolloutID string) Result {
	switch {
	case s.Phase == "rolled-back" && s.RolloutID == rolloutID:
		return Failed
	case s.Phase == "idle" && s.CurrentVersion == target:
		return Succeeded
	case s.Reachability != fleetv1alpha1.Reachable:
		return Pending
	default:
		return InProgress
	}
}

// Decision is the health gate's verdict for the wave in progress.
type Decision string

// Decision values.
const (
	// Wait: not enough results yet.
	Wait Decision = "Wait"
	// Promote: the gate passes; the next wave may start (after its pause).
	Promote Decision = "Promote"
	// Halt: too many failures; the rollout stops and reverts the wave.
	Halt Decision = "Halt"
)

// Gate is the verdict and the counts it was based on.
type Gate struct {
	Decision                               Decision
	Succeeded, Failed, InProgress, Pending int32
	// TimedOut: reachable nodes still in progress at the timeout were counted as failed.
	TimedOut bool
}

// EvaluateGate applies the health gate to the results of every node of the waves started
// so far, elapsed after the current wave started.
//
//   - Pending (unreachable) nodes never count: out of range is not a failure.
//   - Halt as soon as failed / (succeeded + failed + inProgress) > maxFailurePercent: the
//     gate cannot pass even if every node in progress succeeds.
//   - Promote once no reachable node is in progress, if failed / (succeeded + failed) <=
//     maxFailurePercent.
//   - At the timeout, reachable nodes still in progress are stuck and count as failed.
//   - With no result at all but pending nodes, wait, even after the timeout: promoting
//     would be blind, halting would treat being out of range as failing.
func EvaluateGate(results []Result, maxFailurePercent int32, elapsed, timeout time.Duration) Gate {
	var g Gate
	for _, r := range results {
		switch r {
		case Succeeded:
			g.Succeeded++
		case Failed:
			g.Failed++
		case InProgress:
			g.InProgress++
		case Pending:
			g.Pending++
		}
	}
	tooMany := func(failed, total int32) bool {
		return int64(failed)*100 > int64(maxFailurePercent)*int64(total)
	}

	failed, inProgress := g.Failed, g.InProgress
	if tooMany(failed, g.Succeeded+failed+inProgress) {
		g.Decision = Halt
		return g
	}
	if inProgress > 0 {
		if elapsed < timeout {
			g.Decision = Wait
			return g
		}
		g.TimedOut = true
		failed += inProgress
	}
	switch {
	case g.Succeeded+failed == 0 && g.Pending > 0:
		g.Decision = Wait
	case tooMany(failed, g.Succeeded+failed):
		g.Decision = Halt
	default:
		g.Decision = Promote
	}
	return g
}
