package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// EdgeNodeSpec is the expected behaviour of an edge node.
type EdgeNodeSpec struct {
	// contactInterval is the expected interval between two contact windows (e.g. one orbit
	// ≈ 95m for LEO). A node silent for twice this interval becomes Unreachable.
	// +kubebuilder:default="5m"
	// +optional
	ContactInterval metav1.Duration `json:"contactInterval,omitzero"`
}

// Reachability tells whether the hub has heard from the node recently enough.
// Unreachable is not a failure: a satellite out of range is the normal case.
// +kubebuilder:validation:Enum=Reachable;Unreachable
type Reachability string

// Reachability values.
const (
	Reachable   Reachability = "Reachable"
	Unreachable Reachability = "Unreachable"
)

// EdgeNodeStatus is what the hub last learned from the node's agent.
type EdgeNodeStatus struct {
	// reachability is derived from lastSeen and spec.contactInterval.
	// +optional
	Reachability Reachability `json:"reachability,omitempty"`

	// lastSeen is the time of the last heartbeat or status event.
	// +optional
	LastSeen *metav1.Time `json:"lastSeen,omitempty"`

	// currentVersion is the version the agent runs (its last known-good version).
	// +optional
	CurrentVersion string `json:"currentVersion,omitempty"`

	// phase is the agent's state machine phase.
	// +kubebuilder:validation:Enum=idle;applying;verifying;rolled-back
	// +optional
	Phase string `json:"phase,omitempty"`

	// rolloutId is the rollout that produced the node's current desired state.
	// +optional
	RolloutID string `json:"rolloutId,omitempty"`

	// conditions represent the latest observations of the node.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=en
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.status.currentVersion`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Reach",type=string,JSONPath=`.status.reachability`
// +kubebuilder:printcolumn:name="LastSeen",type=date,JSONPath=`.status.lastSeen`

// EdgeNode mirrors one edge node (one agent) on the hub. Created by the operator from
// heartbeats; never edited by hand.
type EdgeNode struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +kubebuilder:default={}
	// +optional
	Spec EdgeNodeSpec `json:"spec,omitzero"`

	// +optional
	Status EdgeNodeStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// EdgeNodeList contains a list of EdgeNode.
type EdgeNodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []EdgeNode `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &EdgeNode{}, &EdgeNodeList{})
		return nil
	})
}
