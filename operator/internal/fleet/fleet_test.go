package fleet

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fleetv1alpha1 "github.com/NilsRuiz/orbit/operator/api/v1alpha1"
)

func node(reach fleetv1alpha1.Reachability, labels map[string]string) fleetv1alpha1.EdgeNode {
	return fleetv1alpha1.EdgeNode{
		ObjectMeta: metav1.ObjectMeta{Labels: labels},
		Status:     fleetv1alpha1.EdgeNodeStatus{Reachability: reach},
	}
}

func TestCount(t *testing.T) {
	tests := []struct {
		name  string
		nodes []fleetv1alpha1.EdgeNode
		want  Counts
	}{
		{"empty", nil, Counts{}},
		{"mixed", []fleetv1alpha1.EdgeNode{
			node(fleetv1alpha1.Reachable, nil),
			node(fleetv1alpha1.Reachable, nil),
			node(fleetv1alpha1.Unreachable, nil),
		}, Counts{Nodes: 3, Reachable: 2, Unreachable: 1}},
		// A node registered but never seen has no reachability yet: not reachable.
		{"never seen counts as unreachable", []fleetv1alpha1.EdgeNode{
			node("", nil),
		}, Counts{Nodes: 1, Unreachable: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Count(tt.nodes); got != tt.want {
				t.Errorf("Count = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestRelevantChange(t *testing.T) {
	base := node(fleetv1alpha1.Reachable, map[string]string{"orbit.io/constellation": "alpha"})
	base.Status.LastSeen = &metav1.Time{Time: time.Unix(0, 0)}
	base.Status.CurrentVersion = "1.0.0"

	relabelled := *base.DeepCopy()
	relabelled.Labels = map[string]string{"orbit.io/constellation": "beta"}

	unreachable := *base.DeepCopy()
	unreachable.Status.Reachability = fleetv1alpha1.Unreachable

	seen := *base.DeepCopy()
	seen.Status.LastSeen = &metav1.Time{Time: time.Unix(60, 0)}

	upgraded := *base.DeepCopy()
	upgraded.Status.CurrentVersion = "1.1.0"

	tests := []struct {
		name     string
		old, new fleetv1alpha1.EdgeNode
		want     bool
	}{
		{"labels changed", base, relabelled, true},
		{"reachability changed", base, unreachable, true},
		// The ingester writes lastSeen about once a minute per node: no fleet cares.
		{"lastSeen only", base, seen, false},
		{"version only", base, upgraded, false},
		{"no change", base, base, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RelevantChange(&tt.old, &tt.new); got != tt.want {
				t.Errorf("RelevantChange = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSelects(t *testing.T) {
	alpha := map[string]string{"orbit.io/constellation": "alpha"}
	tests := []struct {
		name   string
		sel    metav1.LabelSelector
		labels map[string]string
		want   bool
	}{
		{"match", metav1.LabelSelector{MatchLabels: alpha}, alpha, true},
		{"no match", metav1.LabelSelector{MatchLabels: alpha}, map[string]string{"orbit.io/constellation": "beta"}, false},
		{"no labels", metav1.LabelSelector{MatchLabels: alpha}, nil, false},
		// An empty selector selects the whole fleet of nodes.
		{"empty selector", metav1.LabelSelector{}, nil, true},
		// An invalid selector selects nothing; the Fleet reports it in its condition.
		{"invalid selector", metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
			{Key: "orbit.io/constellation", Operator: metav1.LabelSelectorOpIn},
		}}, alpha, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fleetv1alpha1.Fleet{Spec: fleetv1alpha1.FleetSpec{Selector: tt.sel}}
			if got := Selects(f, tt.labels); got != tt.want {
				t.Errorf("Selects = %v, want %v", got, tt.want)
			}
		})
	}
}
