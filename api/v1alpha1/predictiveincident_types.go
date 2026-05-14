package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type IncidentSource string

const (
	SourceAlertmanager IncidentSource = "alertmanager"
	SourcePrometheus   IncidentSource = "prometheus"
	SourcePredictive   IncidentSource = "predictive"
	SourceK8sEvent     IncidentSource = "k8s-event"
	SourceManual       IncidentSource = "manual"
)

type IncidentIdentity struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Namespace string `json:"namespace"`
	// +kubebuilder:validation:MaxLength=63
	Service string `json:"service,omitempty"`
	// +kubebuilder:validation:MaxLength=63
	Job string `json:"job,omitempty"`
	// +kubebuilder:validation:MaxLength=253
	Pod string `json:"pod,omitempty"`
	// +kubebuilder:validation:MaxLength=63
	Deployment string `json:"deployment,omitempty"`
}

type PredictiveIncidentSpec struct {
	// +kubebuilder:validation:Enum=alertmanager;prometheus;predictive;k8s-event;manual
	Source IncidentSource `json:"source"`
	// +kubebuilder:validation:MaxLength=128
	Fingerprint string `json:"fingerprint,omitempty"`
	// +kubebuilder:validation:Enum=critical;high;warning;info;low
	Severity string `json:"severity,omitempty"`
	// +kubebuilder:validation:MaxLength=256
	Title string `json:"title,omitempty"`
	// +kubebuilder:validation:MaxLength=4096
	Description string           `json:"description,omitempty"`
	Identity    IncidentIdentity `json:"identity"`
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Alert map[string]any `json:"alert,omitempty"`
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Signals map[string]any `json:"signals,omitempty"`
}

type EvidenceItem struct {
	// +kubebuilder:validation:MaxLength=64
	Kind string `json:"kind,omitempty"`
	// +kubebuilder:validation:MaxLength=512
	Summary string `json:"summary,omitempty"`
	// +kubebuilder:validation:MaxLength=256
	Ref string `json:"ref,omitempty"`
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Data map[string]any `json:"data,omitempty"`
}

type RCAStatus struct {
	// +kubebuilder:validation:MaxLength=128
	Classification string `json:"classification,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	Confidence float64 `json:"confidence,omitempty"`
	// +kubebuilder:validation:MaxLength=4096
	Summary string `json:"summary,omitempty"`
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Details map[string]any `json:"details,omitempty"`
}

type GitHubIssueStatus struct {
	// +kubebuilder:validation:MaxLength=128
	Repository string `json:"repository,omitempty"`
	// +kubebuilder:validation:Minimum=0
	Number int `json:"number,omitempty"`
	// +kubebuilder:validation:MaxLength=512
	URL string `json:"url,omitempty"`
	// +kubebuilder:validation:Enum=open;closed
	State     string `json:"state,omitempty"`
	Escalated bool   `json:"escalated,omitempty"`
	// +kubebuilder:validation:MaxItems=32
	EscalatedTeams []string `json:"escalatedTeams,omitempty"`
	LastSyncTime   string   `json:"lastSyncTime,omitempty"`
	ClosedAt       string   `json:"closedAt,omitempty"`
	// +kubebuilder:validation:MaxLength=128
	LastRCAClassification string `json:"lastRCAClassification,omitempty"`
}

type AlertmanagerStatus struct {
	EscalationSent    bool   `json:"escalationSent,omitempty"`
	LastSentTime      string `json:"lastSentTime,omitempty"`
	GitHubAlertSent   bool   `json:"githubAlertSent,omitempty"`
	GitHubAlertSentAt string `json:"githubAlertSentAt,omitempty"`
}

type GoogleChatStatus struct {
	EscalationSent bool   `json:"escalationSent,omitempty"`
	LastSentTime   string `json:"lastSentTime,omitempty"`
}

type ActionStatus struct {
	// +kubebuilder:validation:MaxLength=128
	Name string `json:"name,omitempty"`
	// +kubebuilder:validation:MaxLength=128
	Tool string `json:"tool,omitempty"`
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Args map[string]any `json:"args,omitempty"`
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Result     map[string]any `json:"result,omitempty"`
	ExecutedAt string         `json:"executedAt,omitempty"`
}

type IncidentPhase string

const (
	PhaseNew       IncidentPhase = "New"
	PhaseEnriched  IncidentPhase = "Enriched"
	PhaseMitigated IncidentPhase = "Mitigated"
	PhaseBlocked   IncidentPhase = "Blocked"
	PhaseEscalated IncidentPhase = "Escalated"
	PhaseResolved  IncidentPhase = "Resolved"
)

type PredictiveIncidentStatus struct {
	// +kubebuilder:validation:Enum=New;Enriched;Mitigated;Blocked;Escalated;Resolved
	Phase              IncidentPhase `json:"phase,omitempty"`
	ObservedGeneration int64         `json:"observedGeneration,omitempty"`
	LastUpdateTime     string        `json:"lastUpdateTime,omitempty"`

	// +kubebuilder:validation:MaxItems=32
	Evidence []EvidenceItem `json:"evidence,omitempty"`
	RCA      RCAStatus      `json:"rca,omitempty"`
	// +kubebuilder:validation:MaxItems=64
	Actions  []ActionStatus     `json:"actions,omitempty"`
	GitHub   GitHubIssueStatus  `json:"github,omitempty"`
	Alerting AlertmanagerStatus `json:"alerting,omitempty"`
	Chat     GoogleChatStatus   `json:"chat,omitempty"`

	// +kubebuilder:validation:MaxLength=128
	BlockedReason string `json:"blockedReason,omitempty"`
	// +kubebuilder:validation:MaxLength=4096
	BlockedDetails string `json:"blockedDetails,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Severity",type="string",JSONPath=".spec.severity"
// +kubebuilder:printcolumn:name="Source",type="string",JSONPath=".spec.source"
// +kubebuilder:printcolumn:name="Confidence",type="number",JSONPath=".status.rca.confidence",format="float"
// +kubebuilder:printcolumn:name="Service",type="string",JSONPath=".spec.identity.service"
// +kubebuilder:printcolumn:name="Namespace",type="string",JSONPath=".spec.identity.namespace"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:resource:scope=Namespaced,shortName=pi,shortName=pinc
type PredictiveIncident struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PredictiveIncidentSpec   `json:"spec,omitempty"`
	Status            PredictiveIncidentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PredictiveIncidentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PredictiveIncident `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PredictiveIncident{}, &PredictiveIncidentList{})
}
