// Package fleet holds the Fleet rules as pure functions: membership, counting members and
// deciding which EdgeNode changes matter to fleets.
package fleet

import (
	"maps"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	fleetv1alpha1 "github.com/NilsRuiz/orbit/operator/api/v1alpha1"
)

// Counts are a fleet's members by reachability.
type Counts struct {
	Nodes, Reachable, Unreachable int32
}

// Count counts nodes by reachability. A node never seen (no reachability yet) counts as
// Unreachable, so Nodes == Reachable + Unreachable.
func Count(nodes []fleetv1alpha1.EdgeNode) Counts {
	var c Counts
	for i := range nodes {
		c.Nodes++
		if nodes[i].Status.Reachability == fleetv1alpha1.Reachable {
			c.Reachable++
		} else {
			c.Unreachable++
		}
	}
	return c
}

// RelevantChange reports whether an EdgeNode update can change a fleet's status: its
// labels (membership) or its reachability (counts). Other writes, lastSeen above all,
// are ignored so they do not wake fleets once a minute per node.
func RelevantChange(old, updated *fleetv1alpha1.EdgeNode) bool {
	return !maps.Equal(old.Labels, updated.Labels) ||
		old.Status.Reachability != updated.Status.Reachability
}

// Selects reports whether the fleet's selector matches a node with these labels. An
// invalid selector selects nothing: the Fleet controller reports it in its condition.
func Selects(f *fleetv1alpha1.Fleet, nodeLabels map[string]string) bool {
	sel, err := metav1.LabelSelectorAsSelector(&f.Spec.Selector)
	return err == nil && sel.Matches(labels.Set(nodeLabels))
}
