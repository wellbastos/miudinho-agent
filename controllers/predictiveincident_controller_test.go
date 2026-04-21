package controllers

import (
	"context"
	"testing"

	sre "github.com/wellbastos/miudinho-agent/api/v1alpha1"
	"github.com/wellbastos/miudinho-agent/internal/config"
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
