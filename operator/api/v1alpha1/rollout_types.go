package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Wave is one step of a rollout.
type Wave struct {
	// percent is the cumulative share of the fleet updated at the end of this wave.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +required
	Percent int32 `json:"percent"`

	// pauseFor is the soak time after the gate passes, before the next wave starts.
	// +optional
	PauseFor *metav1.Duration `json:"pauseFor,omitempty"`
}

// HealthGate decides whether a wave may be promoted.
type HealthGate struct {
	// maxFailurePercent is the highest accepted failed / (succeeded + failed) ratio.
	// Unreachable nodes are pending, not failed.
	// +kubebuilder:default=0
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +optional
	MaxFailurePercent int32 `json:"maxFailurePercent"`

	// timeout is how long to wait for the reachable nodes of a wave to report.
	// +kubebuilder:default="10m"
	// +optional
	Timeout metav1.Duration `json:"timeout,omitzero"`
}

// RolloutSpec is the intent, declared in Git: which version, to which fleet, how fast.
type RolloutSpec struct {
	// fleetRef is the name of the target Fleet.
	// +kubebuilder:validation:MinLength=1
	// +required
	FleetRef string `json:"fleetRef"`

	// targetVersion is the version every node of the fleet should run.
	// +kubebuilder:validation:MinLength=1
	// +required
	TargetVersion string `json:"targetVersion"`

	// waves are applied in order; their cumulative percents must not decrease, and the last
	// one must reach 100.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=10
	// +kubebuilder:validation:XValidation:rule="self.map(w, w.percent).isSorted()",message="wave percents must not decrease"
	// +kubebuilder:validation:XValidation:rule="self[size(self) - 1].percent == 100",message="the last wave must reach 100 percent"
	// +required
	Waves []Wave `json:"waves"`

	// +kubebuilder:default={}
	// +optional
	HealthGate HealthGate `json:"healthGate,omitzero"`

	// paused stops promotion without reverting (manual hold).
	// +optional
	Paused bool `json:"paused,omitempty"`
}

// RolloutPhase is the lifecycle of a rollout.
// +kubebuilder:validation:Enum=Pending;Progressing;Paused;Halted;Succeeded
type RolloutPhase string

// RolloutPhase values.
const (
	RolloutPending     RolloutPhase = "Pending"
	RolloutProgressing RolloutPhase = "Progressing"
	RolloutPaused      RolloutPhase = "Paused"
	RolloutHalted      RolloutPhase = "Halted"
	RolloutSucceeded   RolloutPhase = "Succeeded"
)

// RolloutStatus is the operator's progress report.
type RolloutStatus struct {
	// +optional
	Phase RolloutPhase `json:"phase,omitempty"`
	// currentWave is the index (0-based) of the wave in progress.
	// +optional
	CurrentWave int32 `json:"currentWave"`
	// +optional
	Updated int32 `json:"updated"`
	// +optional
	Failed int32 `json:"failed"`
	// pending counts nodes of started waves that are unreachable and not yet updated.
	// +optional
	Pending int32 `json:"pending"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=ro
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.targetVersion`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Wave",type=integer,JSONPath=`.status.currentWave`

// Rollout moves a fleet to a target version in waves, gated on agent health (ADR-0004).
type Rollout struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec RolloutSpec `json:"spec"`

	// +optional
	Status RolloutStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// RolloutList contains a list of Rollout.
type RolloutList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Rollout `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Rollout{}, &RolloutList{})
		return nil
	})
}
