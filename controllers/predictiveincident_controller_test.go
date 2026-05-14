package controllers

import (
	"context"
	"errors"
	"strings"
	"testing"

	sre "github.com/wellbastos/miudinho-agent/api/v1alpha1"
	"github.com/wellbastos/miudinho-agent/internal/alertmanager"
	"github.com/wellbastos/miudinho-agent/internal/config"
	"github.com/wellbastos/miudinho-agent/internal/githubissues"
	"github.com/wellbastos/miudinho-agent/internal/rca"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPredictiveIncidentReconcileKeepsBlockedPhaseWhenObserveOnly(t *testing.T) {
	scheme := newTestScheme(t)
	incident := &sre.PredictiveIncident{
		ObjectMeta: metav1.ObjectMeta{Name: "pi-1", Namespace: "default"},
		Spec: sre.PredictiveIncidentSpec{
			Source:   sre.SourceAlertmanager,
			Severity: "warning",
			Identity: sre.IncidentIdentity{Namespace: "default", Service: "payments", Job: "payments"},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&sre.PredictiveIncident{}).
		WithObjects(incident).
		Build()

	r := &PredictiveIncidentReconciler{
		Client: cl,
		Scheme: scheme,
		Config: config.AppConfig{
			Execution: config.ExecutionConfig{AutoObserveOnly: true, ObserveOnlyTTL: 300},
		},
		Evidence: stubEvidenceCollector{},
		Policies: stubPolicyResolver{},
		DecisionSvc: stubDecisionService{
			eval: IncidentEvaluation{
				Decision:      &rca.Decision{Classification: "cpu", Confidence: 0.2, Summary: "observe"},
				Approval:      &rca.Approval{Approved: false, Reasons: []string{"observe-only"}},
				ObserveOnly:   true,
				ObserveReason: "llm unhealthy",
			},
		},
		Actions:  stubActionExecutor{},
		Notifier: stubNotifier{},
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(incident)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	got := &sre.PredictiveIncident{}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(incident), got); err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.Status.Phase != sre.PhaseBlocked {
		t.Fatalf("expected blocked phase, got %s", got.Status.Phase)
	}
	if got.Status.BlockedReason != "observe_only" {
		t.Fatalf("expected observe_only reason, got %q", got.Status.BlockedReason)
	}
}

func TestPredictiveIncidentReconcileMarksApprovalDeniedAsBlocked(t *testing.T) {
	scheme := newTestScheme(t)
	incident := &sre.PredictiveIncident{
		ObjectMeta: metav1.ObjectMeta{Name: "pi-2", Namespace: "default"},
		Spec: sre.PredictiveIncidentSpec{
			Source:   sre.SourcePredictive,
			Severity: "critical",
			Identity: sre.IncidentIdentity{Namespace: "default", Service: "checkout", Job: "checkout"},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&sre.PredictiveIncident{}).
		WithObjects(incident).
		Build()

	r := &PredictiveIncidentReconciler{
		Client: cl,
		Scheme: scheme,
		Config: config.AppConfig{
			Execution: config.ExecutionConfig{ExecuteActions: true, AutoObserveOnly: true, ObserveOnlyTTL: 300},
		},
		Evidence: stubEvidenceCollector{},
		Policies: stubPolicyResolver{policy: &sre.AutoRemediationPolicy{}},
		DecisionSvc: stubDecisionService{
			eval: IncidentEvaluation{
				Decision: &rca.Decision{Classification: "http-5xx", Confidence: 0.9, Summary: "needs approval"},
				Approval: &rca.Approval{Approved: false, Reasons: []string{"manual review required"}},
			},
		},
		Actions:  stubActionExecutor{},
		Notifier: stubNotifier{},
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(incident)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	got := &sre.PredictiveIncident{}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(incident), got); err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.Status.Phase != sre.PhaseBlocked {
		t.Fatalf("expected blocked phase, got %s", got.Status.Phase)
	}
	if got.Status.BlockedReason != "approval_denied" {
		t.Fatalf("expected approval_denied reason, got %q", got.Status.BlockedReason)
	}
}

func TestPredictiveIncidentReconcileEscalatesAlertmanagerWithoutSafeAction(t *testing.T) {
	scheme := newTestScheme(t)
	incident := &sre.PredictiveIncident{
		ObjectMeta: metav1.ObjectMeta{Name: "pi-unsafe", Namespace: "default"},
		Spec: sre.PredictiveIncidentSpec{
			Source:   sre.SourceAlertmanager,
			Severity: "critical",
			Identity: sre.IncidentIdentity{Namespace: "default", Service: "checkout", Job: "checkout"},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&sre.PredictiveIncident{}).
		WithObjects(incident).
		Build()

	r := &PredictiveIncidentReconciler{
		Client: cl,
		Scheme: scheme,
		Config: config.AppConfig{
			Execution: config.ExecutionConfig{ExecuteActions: false},
		},
		Evidence: stubEvidenceCollector{},
		// Política configurada mas execute_actions=false → razão deve ser "actions_disabled"
		Policies: stubPolicyResolver{policy: &sre.AutoRemediationPolicy{}},
		DecisionSvc: stubDecisionService{
			eval: IncidentEvaluation{
				Decision: &rca.Decision{Classification: "http-5xx", Confidence: 0.95, Summary: "restart candidate"},
				Approval: &rca.Approval{Approved: true},
			},
		},
		Actions:  stubActionExecutor{},
		Notifier: stubNotifier{},
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(incident)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	got := &sre.PredictiveIncident{}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(incident), got); err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.Status.Phase != sre.PhaseBlocked {
		t.Fatalf("expected blocked phase, got %s", got.Status.Phase)
	}
	if got.Status.BlockedReason != "actions_disabled" {
		t.Fatalf("expected actions_disabled reason, got %q", got.Status.BlockedReason)
	}
}

func TestMatchIncidentLabels(t *testing.T) {
	pi := &sre.PredictiveIncident{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				"app":  "checkout",
				"team": "payments",
			},
		},
	}

	if !matchesIncidentLabels(pi, map[string]string{"app": "checkout"}) {
		t.Fatal("expected label selector to match")
	}
	if matchesIncidentLabels(pi, map[string]string{"app": "catalog"}) {
		t.Fatal("expected label selector mismatch")
	}
}

func TestPolicyResolverRespectsMatchLabels(t *testing.T) {
	scheme := newTestScheme(t)
	policy := &sre.AutoRemediationPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy"},
		Spec: sre.AutoRemediationPolicySpec{
			Selector: sre.PolicySelector{
				Namespace:   "default",
				MatchLabels: map[string]string{"app": "checkout"},
			},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy).Build()
	resolver := &DefaultIncidentPolicyResolver{Client: cl}

	matchPI := &sre.PredictiveIncident{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Labels: map[string]string{"app": "checkout"}},
		Spec:       sre.PredictiveIncidentSpec{Identity: sre.IncidentIdentity{Namespace: "default"}},
	}
	got, err := resolver.Resolve(context.Background(), matchPI)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if got == nil {
		t.Fatal("expected policy match")
	}

	noMatchPI := &sre.PredictiveIncident{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Labels: map[string]string{"app": "catalog"}},
		Spec:       sre.PredictiveIncidentSpec{Identity: sre.IncidentIdentity{Namespace: "default"}},
	}
	got, err = resolver.Resolve(context.Background(), noMatchPI)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if got != nil {
		t.Fatal("expected no policy match")
	}
}

func TestPolicyResolverSkipsExcludedNamespaces(t *testing.T) {
	scheme := newTestScheme(t)
	policy := &sre.AutoRemediationPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "global-policy"},
		Spec: sre.AutoRemediationPolicySpec{
			Selector: sre.PolicySelector{
				ExcludedNamespaces: []string{"kube-system"},
			},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy).Build()
	resolver := &DefaultIncidentPolicyResolver{Client: cl}

	excludedPI := &sre.PredictiveIncident{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system"},
		Spec:       sre.PredictiveIncidentSpec{Identity: sre.IncidentIdentity{Namespace: "kube-system"}},
	}
	got, err := resolver.Resolve(context.Background(), excludedPI)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if got != nil {
		t.Fatal("expected excluded namespace to skip policy")
	}

	appPI := &sre.PredictiveIncident{
		ObjectMeta: metav1.ObjectMeta{Namespace: "apps"},
		Spec:       sre.PredictiveIncidentSpec{Identity: sre.IncidentIdentity{Namespace: "apps"}},
	}
	got, err = resolver.Resolve(context.Background(), appPI)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if got == nil {
		t.Fatal("expected global policy match")
	}
}

func TestNotifierSyncAggregatesErrors(t *testing.T) {
	pi := &sre.PredictiveIncident{
		ObjectMeta: metav1.ObjectMeta{Name: "pi-3", Namespace: "default"},
		Spec: sre.PredictiveIncidentSpec{
			Source:   sre.SourceAlertmanager,
			Identity: sre.IncidentIdentity{Namespace: "default", Service: "checkout"},
			Severity: "warning",
		},
		Status: sre.PredictiveIncidentStatus{
			Phase: sre.PhaseEscalated,
			RCA:   sre.RCAStatus{Summary: "summary"},
			GitHub: sre.GitHubIssueStatus{
				Number: 1,
			},
		},
	}
	notifier := &DefaultIncidentNotifier{
		GitHub: &githubErrorClient{},
		Alert:  &alertErrorClient{},
		Chat:   &chatErrorClient{},
	}
	err := notifier.Sync(context.Background(), pi, &rca.Decision{Escalation: map[string]any{"needed": true}}, true)
	if err == nil {
		t.Fatal("expected aggregated error")
	}
	if !strings.Contains(err.Error(), "github:") || !strings.Contains(err.Error(), "alertmanager:") || !strings.Contains(err.Error(), "googlechat:") {
		t.Fatalf("expected both error sources, got %v", err)
	}
}

func TestIssueRepositoryUsesAlertLabelWhenPresent(t *testing.T) {
	pi := &sre.PredictiveIncident{
		Spec: sre.PredictiveIncidentSpec{
			Identity: sre.IncidentIdentity{Service: "checkout"},
			Alert: map[string]any{
				"labels": map[string]any{
					"github_repository": "custom-repo",
				},
			},
		},
	}
	gh := &githubSuccessClient{}

	if got := issueRepositoryForIncident(pi, gh); got != "custom-repo" {
		t.Fatalf("expected custom repo from alert label, got %q", got)
	}
}

func TestSyncGoogleChatEscalationMarksStatus(t *testing.T) {
	pi := &sre.PredictiveIncident{
		ObjectMeta: metav1.ObjectMeta{Name: "pi-4", Namespace: "default"},
		Spec: sre.PredictiveIncidentSpec{
			Title:    "HighErrorRate",
			Severity: "critical",
			Identity: sre.IncidentIdentity{Namespace: "default", Service: "checkout"},
		},
		Status: sre.PredictiveIncidentStatus{
			Phase:         sre.PhaseBlocked,
			BlockedReason: "approval_denied",
		},
	}
	chat := &chatSuccessClient{}

	if err := syncGoogleChatEscalation(context.Background(), pi, chat, &rca.Decision{Escalation: map[string]any{"reason": "llm_unavailable"}}, true); err != nil {
		t.Fatalf("syncGoogleChatEscalation returned error: %v", err)
	}
	if !pi.Status.Chat.EscalationSent {
		t.Fatal("expected chat escalation to be marked as sent")
	}
	if len(chat.messages) != 1 {
		t.Fatalf("expected 1 chat message, got %d", len(chat.messages))
	}
}

func TestShouldRecordEvent(t *testing.T) {
	if shouldRecordEvent(sre.PhaseEnriched, sre.PhaseEnriched, 1, 1, "", "") {
		t.Fatal("expected no event when nothing changed")
	}
	if !shouldRecordEvent(sre.PhaseEnriched, sre.PhaseBlocked, 1, 1, "", "") {
		t.Fatal("expected event when phase changes")
	}
	if !shouldRecordEvent(sre.PhaseEnriched, sre.PhaseEnriched, 1, 2, "", "") {
		t.Fatal("expected event when actions change")
	}
	if !shouldRecordEvent(sre.PhaseEnriched, sre.PhaseEnriched, 1, 1, "", "integration failed") {
		t.Fatal("expected event when blocked details change")
	}
}

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := sre.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}
	return scheme
}

type stubEvidenceCollector struct{}

func (stubEvidenceCollector) Collect(context.Context, *sre.PredictiveIncident) ([]sre.EvidenceItem, error) {
	return []sre.EvidenceItem{{Kind: "prometheus", Summary: "ok"}}, nil
}

type stubPolicyResolver struct {
	policy *sre.AutoRemediationPolicy
}

func (s stubPolicyResolver) Resolve(context.Context, *sre.PredictiveIncident) (*sre.AutoRemediationPolicy, error) {
	return s.policy, nil
}

type stubDecisionService struct {
	eval IncidentEvaluation
	err  error
}

func (s stubDecisionService) Evaluate(context.Context, *sre.PredictiveIncident, *sre.AutoRemediationPolicy) (IncidentEvaluation, error) {
	return s.eval, s.err
}

type stubActionExecutor struct {
	acted bool
	err   error
}

func (s stubActionExecutor) Execute(context.Context, *sre.PredictiveIncident, *sre.AutoRemediationPolicy, IncidentEvaluation) (bool, error) {
	return s.acted, s.err
}

type stubNotifier struct {
	err error
}

func (s stubNotifier) Sync(context.Context, *sre.PredictiveIncident, *rca.Decision, bool) error {
	return s.err
}

type githubErrorClient struct{}

func (*githubErrorClient) Enabled() bool            { return true }
func (*githubErrorClient) Repository(string) string { return "apps-checkout" }
func (*githubErrorClient) CreateIssue(context.Context, string, string, string, []string) (*githubissues.Issue, error) {
	return nil, errors.New("create failed")
}
func (*githubErrorClient) AddComment(context.Context, string, int, string) error {
	return errors.New("comment failed")
}
func (*githubErrorClient) CloseIssue(context.Context, string, int) error {
	return errors.New("close failed")
}
func (*githubErrorClient) ReopenIssue(context.Context, string, int) error {
	return errors.New("reopen failed")
}
func (*githubErrorClient) UpdateIssue(context.Context, string, int, string) error {
	return errors.New("update failed")
}
func (*githubErrorClient) FindOpenIssue(context.Context, string, string) (*githubissues.Issue, error) {
	return nil, errors.New("find failed")
}
func (*githubErrorClient) SearchIssueByTitle(context.Context, string, string) (*githubissues.Issue, error) {
	return nil, errors.New("search failed")
}
func (*githubErrorClient) TeamSlugs() []string    { return []string{"sre"} }
func (*githubErrorClient) TeamMentions() []string { return []string{"@org/sre"} }

type alertErrorClient struct{}

func (*alertErrorClient) Enabled() bool { return true }
func (*alertErrorClient) Send(context.Context, []alertmanager.Alert) error {
	return errors.New("send failed")
}

type chatErrorClient struct{}

func (*chatErrorClient) Enabled() bool { return true }
func (*chatErrorClient) Send(context.Context, string) error {
	return errors.New("send failed")
}

type chatSuccessClient struct {
	messages []string
}

func (*chatSuccessClient) Enabled() bool { return true }
func (c *chatSuccessClient) Send(_ context.Context, text string) error {
	c.messages = append(c.messages, text)
	return nil
}

type githubSuccessClient struct{}

func (*githubSuccessClient) Enabled() bool            { return true }
func (*githubSuccessClient) Repository(string) string { return "apps-checkout" }
func (*githubSuccessClient) CreateIssue(context.Context, string, string, string, []string) (*githubissues.Issue, error) {
	return &githubissues.Issue{}, nil
}
func (*githubSuccessClient) AddComment(context.Context, string, int, string) error { return nil }
func (*githubSuccessClient) CloseIssue(context.Context, string, int) error         { return nil }
func (*githubSuccessClient) ReopenIssue(context.Context, string, int) error        { return nil }
func (*githubSuccessClient) UpdateIssue(context.Context, string, int, string) error { return nil }
func (*githubSuccessClient) FindOpenIssue(context.Context, string, string) (*githubissues.Issue, error) {
	return nil, nil
}
func (*githubSuccessClient) SearchIssueByTitle(context.Context, string, string) (*githubissues.Issue, error) {
	return nil, nil
}
func (*githubSuccessClient) TeamSlugs() []string {
	return []string{"sre-editor", "sre-viewer", "sre-admin"}
}
func (*githubSuccessClient) TeamMentions() []string {
	return []string{"@org/sre-editor", "@org/sre-viewer", "@org/sre-admin"}
}
