package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type SLOServiceRef struct {
	// +kubebuilder:validation:MaxLength=63
	Namespace string `json:"namespace,omitempty"`
	// +kubebuilder:validation:MaxLength=63
	Service string `json:"service,omitempty"`
	// +kubebuilder:validation:MaxLength=63
	Job string `json:"job,omitempty"`
	// +kubebuilder:validation:MaxProperties=32
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
}

type SLOObjective struct {
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	Target float64 `json:"target"`
	// +kubebuilder:validation:Pattern=`^[0-9]+[smhd]$`
	Window string `json:"window"`
}

type SLOHTTP5xxSignals struct {
	// +kubebuilder:validation:Minimum=0
	ErrorRateThresholdPct float64 `json:"errorRateThresholdPct,omitempty"`
	// +kubebuilder:validation:Minimum=0
	SlopeThreshold float64 `json:"slopeThreshold,omitempty"`
	// +kubebuilder:validation:Minimum=0
	Min5xxRPS float64 `json:"min5xxRPS,omitempty"`
}

type SLOOOMSignals struct {
	// +kubebuilder:validation:Minimum=1
	TimeToOomThresholdSeconds int `json:"timeToOomThresholdSeconds,omitempty"`
}

type SLOLatencySignals struct {
	Enabled bool `json:"enabled,omitempty"`
}

type SLOSignals struct {
	Http5xx SLOHTTP5xxSignals `json:"http5xx,omitempty"`
	OOM     SLOOOMSignals     `json:"oom,omitempty"`
	Latency SLOLatencySignals `json:"latency,omitempty"`
}

type SLOPolicySpec struct {
	Service SLOServiceRef `json:"service"`
	// +kubebuilder:validation:MaxItems=128
	ExcludedNamespaces []string     `json:"excludedNamespaces,omitempty"`
	Objective          SLOObjective `json:"objective"`
	Signals            SLOSignals   `json:"signals"`
	// +kubebuilder:validation:Minimum=30
	ScheduleSeconds int `json:"scheduleSeconds,omitempty"`
}

type SLOPolicyStatus struct {
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
	LastRunTime        string `json:"lastRunTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=slo
type SLOPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SLOPolicySpec   `json:"spec,omitempty"`
	Status            SLOPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type SLOPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SLOPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SLOPolicy{}, &SLOPolicyList{})
}
