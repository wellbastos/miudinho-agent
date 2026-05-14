package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type PolicySelector struct {
	// +kubebuilder:validation:MaxLength=63
	Namespace string `json:"namespace,omitempty"`
	// +kubebuilder:validation:MaxItems=128
	ExcludedNamespaces []string `json:"excludedNamespaces,omitempty"`
	// +kubebuilder:validation:MaxProperties=32
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
	// +kubebuilder:validation:MaxItems=16
	Severities []string `json:"severities,omitempty"`
}

type GuardrailsSpec struct {
	// +kubebuilder:validation:Minimum=0
	MinAvailableReplicas int `json:"minAvailableReplicas,omitempty"`
	// +kubebuilder:validation:Minimum=0
	RestartCooldownSeconds int `json:"restartCooldownSeconds,omitempty"`
	// +kubebuilder:validation:MaxItems=64
	BlockIfReasons []string `json:"blockIfReasons,omitempty"`
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:Enum=observeOnly;escalate;restartPod;rolloutRestartDeployment
	AllowedActions []string `json:"allowedActions,omitempty"`
}

type RuleWhen struct {
	// +kubebuilder:validation:Enum=alertmanager;prometheus;predictive;k8s-event;manual
	Source string `json:"source,omitempty"`
	// +kubebuilder:validation:MaxLength=128
	Classification string `json:"classification,omitempty"`
}

type PolicyAction struct {
	// +kubebuilder:validation:Enum=observeOnly;escalate;restartPod;rolloutRestartDeployment
	Type string `json:"type"`
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Args map[string]any `json:"args,omitempty"`
}

type PolicyRule struct {
	When RuleWhen `json:"when"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	Actions []PolicyAction `json:"actions"`
}

type AutoRemediationPolicySpec struct {
	Selector   PolicySelector `json:"selector"`
	Guardrails GuardrailsSpec `json:"guardrails,omitempty"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	Rules []PolicyRule `json:"rules"`
}

type AutoRemediationPolicyStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=arp
type AutoRemediationPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              AutoRemediationPolicySpec   `json:"spec,omitempty"`
	Status            AutoRemediationPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type AutoRemediationPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AutoRemediationPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AutoRemediationPolicy{}, &AutoRemediationPolicyList{})
}
