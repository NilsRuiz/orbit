// Package edgenode holds the EdgeNode rules of ADR-0007 as pure functions: reachability,
// the lastSeen write budget and ownership of orbit.io/ labels. No I/O, no clock.
package edgenode

import (
	"maps"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"

	fleetv1alpha1 "github.com/NilsRuiz/orbit/operator/api/v1alpha1"
)

// LabelPrefix is the only label prefix the operator owns on EdgeNodes. The agent reports
// these labels; every other label belongs to humans.
const LabelPrefix = "orbit.io/"

// lastSeenBudget is the longest a stored lastSeen may lag behind the last contact.
const lastSeenBudget = time.Minute

// UnreachableAfter is how long a node may stay silent before it is Unreachable.
func UnreachableAfter(contactInterval time.Duration) time.Duration {
	return 2 * contactInterval
}

// Reachability tells whether a node last seen at lastSeen is still Reachable at now, and
// if so, after how long it would become Unreachable without news. A zero lastSeen means
// never seen.
func Reachability(lastSeen time.Time, contactInterval time.Duration, now time.Time) (fleetv1alpha1.Reachability, time.Duration) {
	if lastSeen.IsZero() {
		return fleetv1alpha1.Unreachable, 0
	}
	threshold := UnreachableAfter(contactInterval)
	left := lastSeen.Add(threshold).Sub(now)
	if left <= 0 {
		return fleetv1alpha1.Unreachable, 0
	}
	// Bound the delay: a lastSeen in the future (hub clock stepped back) would otherwise
	// postpone the check indefinitely.
	return fleetv1alpha1.Reachable, min(left, threshold)
}

// ShouldWriteLastSeen applies the write budget: write lastSeen on first contact, then at
// most once per budget. The budget shrinks for short contact intervals so a stored
// lastSeen never lags enough to make a talking node look Unreachable.
func ShouldWriteLastSeen(prev, seen time.Time, contactInterval time.Duration) bool {
	if prev.IsZero() {
		return true
	}
	budget := min(lastSeenBudget, UnreachableAfter(contactInterval)/4)
	return seen.Sub(prev) >= budget
}

// SyncLabels returns the labels an EdgeNode should carry given the labels its agent
// reports. orbit.io/ labels follow the agent (added, changed, removed); other labels are
// kept as they are. Reported labels outside orbit.io/ or with invalid syntax are
// rejected, never applied. changes ("+k=v", "~k=v", "-k") and rejected are sorted.
// current is not modified.
func SyncLabels(current, reported map[string]string) (labels map[string]string, changes, rejected []string) {
	labels = make(map[string]string, len(current)+len(reported))
	for k, v := range current {
		if !strings.HasPrefix(k, LabelPrefix) {
			labels[k] = v
		}
	}
	for _, k := range slices.Sorted(maps.Keys(reported)) {
		v := reported[k]
		if !strings.HasPrefix(k, LabelPrefix) ||
			len(validation.IsQualifiedName(k)) > 0 || len(validation.IsValidLabelValue(v)) > 0 {
			rejected = append(rejected, k)
			continue
		}
		labels[k] = v
		switch old, ok := current[k]; {
		case !ok:
			changes = append(changes, "+"+k+"="+v)
		case old != v:
			changes = append(changes, "~"+k+"="+v)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(current)) {
		if _, kept := labels[k]; strings.HasPrefix(k, LabelPrefix) && !kept {
			changes = append(changes, "-"+k)
		}
	}
	slices.Sort(changes)
	return labels, changes, rejected
}

// ValidNodeID reports whether id can name an EdgeNode (DNS-1123 label). The agent enforces
// the same rule at start-up; the operator checks again because the hub trusts no input.
func ValidNodeID(id string) bool {
	return len(validation.IsDNS1123Label(id)) == 0
}

// Kubernetes event reasons recorded on EdgeNodes.
const (
	ReasonRegistered      = "Registered"
	ReasonLabelsChanged   = "LabelsChanged"
	ReasonLabelsRejected  = "LabelsRejected"
	ReasonContactLost     = "ContactLost"
	ReasonContactRestored = "ContactRestored"
	ReasonRolledBack      = "RolledBack"
)

// ValidPhase reports whether p is a phase of the agent's state machine (the
// status.phase enum). An empty phase is valid: old agents do not report one.
func ValidPhase(p string) bool {
	switch p {
	case "", "idle", "applying", "verifying", "rolled-back":
		return true
	}
	return false
}
