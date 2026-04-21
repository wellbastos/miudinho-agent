package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type SLOServiceRef struct {
	Namespace string `json:"namespace,omitempty"`
	Service   string `json:"service,omitempty"`
	Job       string `json:"job,omitempty"`
}

type SLOObjective struct {
	Target float64 `json:"target"`
	Window string  `json:"window"`
}

type SLOSignals struct {
	Http5xx struct {
		ErrorRateThresholdPct float64 `json:"errorRateThresholdPct,omitempty"`
		SlopeThreshold        float64 `json:"slopeThreshold,omitempty"`
		Min5xxRPS             float64 `json:"min5xxRPS,omitempty"`
	} `json:"http5xx,omitempty"`

	OOM struct {
		TimeToOomThresholdSeconds int `json:"timeToOomThresholdSeconds,omitempty"`
	} `json:"oom,omitempty"`

	Latency struct {
		Enabled bool `json:"enabled,omitempty"`
	} `json:"latency,omitempty"`
}

type SLOPolicySpec struct {
	Service         SLOServiceRef `json:"service"`
	Objective       SLOObjective  `json:"objective"`
	Signals         SLOSignals    `json:"signals"`
	ScheduleSeconds int           `json:"scheduleSeconds,omitempty"`
}

type SLOPolicyStatus struct {
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
	LastRunTime        string `json:"lastRunTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
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
