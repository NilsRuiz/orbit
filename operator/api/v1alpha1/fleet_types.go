package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// FleetSpec selects the edge nodes that form the fleet.
type FleetSpec struct {
	// selector matches EdgeNode labels, e.g. orbit.io/constellation: alpha.
	// +required
	Selector metav1.LabelSelector `json:"selector"`
}

// FleetStatus counts the fleet's nodes by reachability.
type FleetStatus struct {
	// observedGeneration is the spec generation these counts were computed for.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// nodes is the number of EdgeNodes the selector matches.
	// +optional
	Nodes int32 `json:"nodes"`
	// reachable counts the matching nodes in contact with the hub.
	// +optional
	Reachable int32 `json:"reachable"`
	// unreachable counts the other matching nodes, including nodes never seen. Out of
	// range is the normal case for a satellite, not a failure.
	// +optional
	Unreachable int32 `json:"unreachable"`

	// conditions: Ready is False when the selector is invalid.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Fleet condition types and reasons.
const (
	FleetReady             = "Ready"
	ReasonSelectorResolved = "SelectorResolved"
	ReasonInvalidSelector  = "InvalidSelector"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Nodes",type=integer,JSONPath=`.status.nodes`
// +kubebuilder:printcolumn:name="Reachable",type=integer,JSONPath=`.status.reachable`
// +kubebuilder:printcolumn:name="Unreachable",type=integer,JSONPath=`.status.unreachable`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`

// Fleet is a label-selected group of edge nodes that rollouts target. Declared in Git.
type Fleet struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec FleetSpec `json:"spec"`

	// +optional
	Status FleetStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// FleetList contains a list of Fleet.
type FleetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Fleet `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Fleet{}, &FleetList{})
		return nil
	})
}
