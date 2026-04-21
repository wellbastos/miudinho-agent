package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type PolicySelector struct {
	Namespace   string            `json:"namespace,omitempty"`
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
	Severities  []string          `json:"severities,omitempty"`
}

type GuardrailsSpec struct {
	MinAvailableReplicas   int      `json:"minAvailableReplicas,omitempty"`
	RestartCooldownSeconds int      `json:"restartCooldownSeconds,omitempty"`
	BlockIfReasons         []string `json:"blockIfReasons,omitempty"`
	AllowedActions         []string `json:"allowedActions,omitempty"`
}

type RuleWhen struct {
	Source         string `json:"source,omitempty"`
	Classification string `json:"classification,omitempty"`
}

type PolicyAction struct {
	Type string         `json:"type"`
	Args map[string]any `json:"args,omitempty"`
}

type PolicyRule struct {
	When    RuleWhen       `json:"when"`
	Actions []PolicyAction `json:"actions"`
}

type AutoRemediationPolicySpec struct {
	Selector   PolicySelector `json:"selector"`
	Guardrails GuardrailsSpec `json:"guardrails,omitempty"`
	Rules      []PolicyRule   `json:"rules"`
}

type AutoRemediationPolicyStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
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
