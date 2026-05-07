package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type IncidentSource string

const (
	SourceAlertmanager IncidentSource = "alertmanager"
	SourcePredictive   IncidentSource = "predictive"
	SourceK8sEvent     IncidentSource = "k8s-event"
	SourceManual       IncidentSource = "manual"
)

type IncidentIdentity struct {
	Namespace  string `json:"namespace"`
	Service    string `json:"service,omitempty"`
	Job        string `json:"job,omitempty"`
	Pod        string `json:"pod,omitempty"`
	Deployment string `json:"deployment,omitempty"`
}

type PredictiveIncidentSpec struct {
	Source      IncidentSource   `json:"source"`
	Fingerprint string           `json:"fingerprint,omitempty"`
	Severity    string           `json:"severity,omitempty"`
	Title       string           `json:"title,omitempty"`
	Description string           `json:"description,omitempty"`
	Identity    IncidentIdentity `json:"identity"`
	Alert       map[string]any   `json:"alert,omitempty"`
	Signals     map[string]any   `json:"signals,omitempty"`
}

type EvidenceItem struct {
	Kind    string         `json:"kind,omitempty"`
	Summary string         `json:"summary,omitempty"`
	Ref     string         `json:"ref,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

type RCAStatus struct {
	Classification string         `json:"classification,omitempty"`
	Confidence     float64        `json:"confidence,omitempty"`
	Summary        string         `json:"summary,omitempty"`
	Details        map[string]any `json:"details,omitempty"`
}

type GitHubIssueStatus struct {
	Repository     string   `json:"repository,omitempty"`
	Number         int      `json:"number,omitempty"`
	URL            string   `json:"url,omitempty"`
	State          string   `json:"state,omitempty"`
	Escalated      bool     `json:"escalated,omitempty"`
	EscalatedTeams []string `json:"escalatedTeams,omitempty"`
	LastSyncTime   string   `json:"lastSyncTime,omitempty"`
	ClosedAt       string   `json:"closedAt,omitempty"`
}

type AlertmanagerStatus struct {
	EscalationSent bool   `json:"escalationSent,omitempty"`
	LastSentTime   string `json:"lastSentTime,omitempty"`
}

type GoogleChatStatus struct {
	EscalationSent bool   `json:"escalationSent,omitempty"`
	LastSentTime   string `json:"lastSentTime,omitempty"`
}

type ActionStatus struct {
	Name       string         `json:"name,omitempty"`
	Tool       string         `json:"tool,omitempty"`
	Args       map[string]any `json:"args,omitempty"`
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
	Phase              IncidentPhase `json:"phase,omitempty"`
	ObservedGeneration int64         `json:"observedGeneration,omitempty"`
	LastUpdateTime     string        `json:"lastUpdateTime,omitempty"`

	Evidence []EvidenceItem     `json:"evidence,omitempty"`
	RCA      RCAStatus          `json:"rca,omitempty"`
	Actions  []ActionStatus     `json:"actions,omitempty"`
	GitHub   GitHubIssueStatus  `json:"github,omitempty"`
	Alerting AlertmanagerStatus `json:"alerting,omitempty"`
	Chat     GoogleChatStatus   `json:"chat,omitempty"`

	BlockedReason  string `json:"blockedReason,omitempty"`
	BlockedDetails string `json:"blockedDetails,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
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
