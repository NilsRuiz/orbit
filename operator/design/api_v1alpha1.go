//go:build ignore

// Design draft of the fleet.orbit.io/v1alpha1 API. Not compiled: copy these fields into
// the kubebuilder-generated api/v1alpha1/*_types.go files.
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ---------------------------------------------------------------- EdgeNode

type EdgeNodeSpec struct {
	// Expected interval between two contact windows (e.g. one orbit ≈ 95m for LEO).
	// Used to decide when a silent node becomes Unreachable.
	// +kubebuilder:default="5m"
	ContactInterval metav1.Duration `json:"contactInterval,omitempty"`
}

// +kubebuilder:validation:Enum=Reachable;Unreachable
type Reachability string

type EdgeNodeStatus struct {
	Reachability   Reachability `json:"reachability,omitempty"`
	LastSeen       *metav1.Time `json:"lastSeen,omitempty"`
	CurrentVersion string       `json:"currentVersion,omitempty"`
	// Agent phase: idle | applying | verifying | rolled-back
	Phase      string             `json:"phase,omitempty"`
	RolloutID  string             `json:"rolloutId,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=en
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.status.currentVersion`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Reach",type=string,JSONPath=`.status.reachability`
// +kubebuilder:printcolumn:name="LastSeen",type=date,JSONPath=`.status.lastSeen`
type EdgeNode struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              EdgeNodeSpec   `json:"spec,omitempty"`
	Status            EdgeNodeStatus `json:"status,omitempty"`
}

// ---------------------------------------------------------------- Fleet

type FleetSpec struct {
	// Selects EdgeNodes by label, e.g. orbit.io/constellation: alpha
	Selector metav1.LabelSelector `json:"selector"`
}

type FleetStatus struct {
	Nodes       int `json:"nodes"`
	Reachable   int `json:"reachable"`
	Unreachable int `json:"unreachable"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
type Fleet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              FleetSpec   `json:"spec"`
	Status            FleetStatus `json:"status,omitempty"`
}

// ---------------------------------------------------------------- Rollout

type Wave struct {
	// Cumulative share of the fleet updated at the end of this wave.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	Percent int32 `json:"percent"`
	// Soak time after the gate passes, before starting the next wave.
	PauseFor *metav1.Duration `json:"pauseFor,omitempty"`
}

type HealthGate struct {
	// +kubebuilder:default=0
	// +kubebuilder:validation:Maximum=100
	MaxFailurePercent int32 `json:"maxFailurePercent,omitempty"`
	// How long to wait for reachable nodes of a wave to report.
	// +kubebuilder:default="10m"
	Timeout metav1.Duration `json:"timeout,omitempty"`
}

type RolloutSpec struct {
	FleetRef      string `json:"fleetRef"`
	TargetVersion string `json:"targetVersion"`
	// +kubebuilder:validation:MinItems=1
	Waves      []Wave     `json:"waves"`
	HealthGate HealthGate `json:"healthGate,omitempty"`
	// Pause stops promotion without reverting (manual hold).
	Paused bool `json:"paused,omitempty"`
}

// +kubebuilder:validation:Enum=Pending;Progressing;Paused;Halted;Succeeded
type RolloutPhase string

type RolloutStatus struct {
	Phase       RolloutPhase       `json:"phase,omitempty"`
	CurrentWave int32              `json:"currentWave,omitempty"`
	Updated     int32              `json:"updated,omitempty"`
	Failed      int32              `json:"failed,omitempty"`
	Pending     int32              `json:"pending,omitempty"` // unreachable, not yet applied
	Conditions  []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=ro
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.targetVersion`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Wave",type=integer,JSONPath=`.status.currentWave`
type Rollout struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              RolloutSpec   `json:"spec"`
	Status            RolloutStatus `json:"status,omitempty"`
}
