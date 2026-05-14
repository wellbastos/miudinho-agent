package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	sre "github.com/wellbastos/miudinho-agent/api/v1alpha1"
	"github.com/wellbastos/miudinho-agent/internal/alertmanager"
	"github.com/wellbastos/miudinho-agent/internal/config"
	"github.com/wellbastos/miudinho-agent/internal/githubissues"
	appmetrics "github.com/wellbastos/miudinho-agent/internal/metrics"
	"github.com/wellbastos/miudinho-agent/internal/rca"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type PredictiveIncidentReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	Config      config.AppConfig
	Recorder    record.EventRecorder
	Evidence    IncidentEvidenceCollector
	Policies    IncidentPolicyResolver
	DecisionSvc IncidentDecisionService
	Actions     IncidentActionExecutor
	Notifier    IncidentNotifier
}

type githubissuesCommenter interface {
	TeamMentions() []string
}

func (r *PredictiveIncidentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sre.PredictiveIncident{}).
		Complete(r)
}

func (r *PredictiveIncidentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	startedAt := time.Now()
	logger := log.FromContext(ctx).WithValues("predictiveIncident", req.NamespacedName.String())
	ctx = log.IntoContext(ctx, logger)
	source := "unknown"
	phase := "unknown"
	result := "success"
	defer func() {
		appmetrics.RecordReconcile("predictiveincident", source, phase, result, time.Since(startedAt))
	}()

	if r.Config.HTTP.AlertWebhookAddr == "" {
		r.Config = config.LoadFromEnv()
	}
	r.ensureDefaults()

	pi := &sre.PredictiveIncident{}
	if err := r.Get(ctx, req.NamespacedName, pi); err != nil {
		if client.IgnoreNotFound(err) == nil {
			result = "not_found"
			return ctrl.Result{}, nil
		}
		result = "error"
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	source = string(pi.Spec.Source)
	logger.Info("reconciling predictive incident", "source", source, "phase", pi.Status.Phase, "severity", pi.Spec.Severity)

	if pi.Status.Phase == "" {
		pi.Status.Phase = sre.PhaseNew
	}
	previousPhase := pi.Status.Phase
	previousActionCount := len(pi.Status.Actions)
	previousBlockedDetails := pi.Status.BlockedDetails

	evidence, evidenceErr := r.Evidence.Collect(ctx, pi)
	if len(evidence) > 0 {
		pi.Status.Evidence = evidence
	}

	policy, err := r.Policies.Resolve(ctx, pi)
	if err != nil {
		phase = string(pi.Status.Phase)
		result = "error"
		logger.Error(err, "failed to resolve auto-remediation policy")
		return ctrl.Result{}, err
	}
	if policy == nil {
		logger.Info("no auto-remediation policy matched incident")
	} else {
		logger.Info("auto-remediation policy matched incident", "policy", client.ObjectKeyFromObject(policy).String())
	}

	eval, evalErr := r.DecisionSvc.Evaluate(ctx, pi, policy)
	if evalErr != nil {
		appendBlockedDetail(pi, "decision_error: "+evalErr.Error())
		logger.Error(evalErr, "incident decision evaluation failed")
	}
	if evidenceErr != nil {
		appendBlockedDetail(pi, "evidence_error: "+evidenceErr.Error())
		logger.Error(evidenceErr, "incident evidence collection failed")
	}

	if eval.Decision != nil {
		approvalApproved := false
		approvalRisk := ""
		if eval.Approval != nil {
			approvalApproved = eval.Approval.Approved
			approvalRisk = eval.Approval.RiskLevel
		}
		logger.Info("incident decision evaluated",
			"classification", eval.Decision.Classification,
			"confidence", eval.Decision.Confidence,
			"observeOnly", eval.ObserveOnly,
			"approvalApproved", approvalApproved,
			"approvalRisk", approvalRisk,
		)
		pi.Status.RCA = sre.RCAStatus{
			Classification: eval.Decision.Classification,
			Confidence:     eval.Decision.Confidence,
			Summary:        eval.Decision.Summary,
			Details: map[string]any{
				"approval":          eval.Approval,
				"observeOnly":       eval.ObserveOnly,
				"observeOnlyReason": eval.ObserveReason,
				"engine":            eval.EngineSnapshot,
			},
		}
	}

	r.applyBasePhase(pi, policy, eval)

	acted, actionErr := r.Actions.Execute(ctx, pi, policy, eval)
	if actionErr != nil {
		appendBlockedDetail(pi, "action_error: "+actionErr.Error())
		pi.Status.Phase = sre.PhaseBlocked
		pi.Status.BlockedReason = defaultIfEmpty(pi.Status.BlockedReason, "action_error")
		logger.Error(actionErr, "incident action execution failed")
	}
	// Se nenhuma ação foi executada e não há erro, bloqueia para escalação.
	// Aplica a todos os sources (alertmanager, prometheus, predictive) — não apenas alertmanager.
	if !acted && actionErr == nil && pi.Status.Phase == sre.PhaseEnriched {
		pi.Status.Phase = sre.PhaseBlocked
		pi.Status.BlockedReason = defaultIfEmpty(pi.Status.BlockedReason, "no_safe_action")
		pi.Status.BlockedDetails = appendStatusDetail(pi.Status.BlockedDetails, "no policy matched or actions are disabled")
		logger.Info("incident blocked because no safe action was available", "reason", pi.Status.BlockedReason, "source", source)
	}

	shouldEscalate := shouldEscalateIssue(pi, eval.Decision, pi.Status.Phase == sre.PhaseEscalated)
	if pi.Status.Phase == sre.PhaseBlocked {
		shouldEscalate = true
	}
	if err := r.Notifier.Sync(ctx, pi, eval.Decision, shouldEscalate); err != nil {
		appendBlockedDetail(pi, "notification_error: "+err.Error())
		logger.Error(err, "incident notification sync failed", "shouldEscalate", shouldEscalate)
	}

	pi.Status.ObservedGeneration = pi.Generation
	pi.Status.LastUpdateTime = time.Now().Format(time.RFC3339)
	if err := r.Status().Update(ctx, pi); err != nil {
		phase = string(pi.Status.Phase)
		result = "error"
		logger.Error(err, "failed to update predictive incident status", "phase", pi.Status.Phase)
		return ctrl.Result{}, err
	}
	phase = string(pi.Status.Phase)
	logger.Info("predictive incident reconciled", "phase", phase, "actions", len(pi.Status.Actions), "blockedReason", pi.Status.BlockedReason)

	r.recordPhaseEvent(pi, previousPhase, previousActionCount, previousBlockedDetails)

	if pi.Status.Phase == sre.PhaseBlocked {
		return ctrl.Result{RequeueAfter: 2 * time.Minute}, nil
	}
	return ctrl.Result{RequeueAfter: 3 * time.Minute}, nil
}

func (r *PredictiveIncidentReconciler) ensureDefaults() {
	if r.Evidence == nil {
		r.Evidence = &DefaultIncidentEvidenceCollector{Config: r.Config}
	}
	if r.Policies == nil {
		r.Policies = &DefaultIncidentPolicyResolver{Client: r.Client}
	}
	if r.DecisionSvc == nil {
		r.DecisionSvc = &DefaultIncidentDecisionService{Config: r.Config}
	}
	if r.Actions == nil {
		r.Actions = &DefaultIncidentActionExecutor{Client: r.Client, Config: r.Config}
	}
	if r.Notifier == nil {
		r.Notifier = &DefaultIncidentNotifier{
			GitHub: githubissues.New(r.Config.GitHub),
			Alert:  nil,
			Chat:   nil,
		}
	}
	if notifier, ok := r.Notifier.(*DefaultIncidentNotifier); ok && notifier.Alert == nil {
		notifier.Alert = alertmanager.NewClient(r.Config.Observability)
	}
	if notifier, ok := r.Notifier.(*DefaultIncidentNotifier); ok && notifier.Chat == nil {
		notifier.Chat = NewGoogleChatClient(r.Config.Notifications)
	}
}

func (r *PredictiveIncidentReconciler) applyBasePhase(pi *sre.PredictiveIncident, policy *sre.AutoRemediationPolicy, eval IncidentEvaluation) {
	switch {
	case eval.ObserveOnly:
		pi.Status.Phase = sre.PhaseBlocked
		pi.Status.BlockedReason = "observe_only"
		pi.Status.BlockedDetails = appendStatusDetail(pi.Status.BlockedDetails, eval.ObserveReason)
	case eval.Approval != nil && !eval.Approval.Approved:
		pi.Status.Phase = sre.PhaseBlocked
		pi.Status.BlockedReason = "approval_denied"
		pi.Status.BlockedDetails = appendStatusDetail(pi.Status.BlockedDetails, strings.Join(eval.Approval.Reasons, "; "))
	case policy == nil || !r.Config.Execution.ExecuteActions:
		pi.Status.Phase = sre.PhaseEnriched
	default:
		pi.Status.Phase = sre.PhaseEnriched
	}
}

func (r *PredictiveIncidentReconciler) recordPhaseEvent(pi *sre.PredictiveIncident, previousPhase sre.IncidentPhase, previousActionCount int, previousBlockedDetails string) {
	if r.Recorder == nil {
		return
	}
	if !shouldRecordEvent(previousPhase, pi.Status.Phase, previousActionCount, len(pi.Status.Actions), previousBlockedDetails, pi.Status.BlockedDetails) {
		return
	}
	r.Recorder.Eventf(pi, "Normal", string(pi.Status.Phase), "Incident phase updated to %s", pi.Status.Phase)
}

func shouldRecordEvent(previousPhase, currentPhase sre.IncidentPhase, previousActionCount, currentActionCount int, previousBlockedDetails, currentBlockedDetails string) bool {
	return previousPhase != currentPhase || previousActionCount != currentActionCount || previousBlockedDetails != currentBlockedDetails
}

func appendBlockedDetail(pi *sre.PredictiveIncident, next string) {
	pi.Status.BlockedDetails = appendStatusDetail(pi.Status.BlockedDetails, next)
}

func shouldCloseIssue(pi *sre.PredictiveIncident) bool {
	return pi.Status.Phase == sre.PhaseMitigated || pi.Status.Phase == sre.PhaseResolved
}

func shouldEscalateIssue(pi *sre.PredictiveIncident, decision *rca.Decision, fallback bool) bool {
	if fallback || pi.Status.Phase == sre.PhaseEscalated {
		return true
	}
	if decision == nil {
		return false
	}
	needed, _ := decision.Escalation["needed"].(bool)
	return needed
}

func buildIssueTitle(pi *sre.PredictiveIncident) string {
	if pi.Spec.Title != "" {
		return fmt.Sprintf("[%s] %s", pi.Spec.Identity.Namespace, pi.Spec.Title)
	}
	return fmt.Sprintf("[%s] Incident %s", pi.Spec.Identity.Namespace, pi.Name)
}

func buildIssueBody(pi *sre.PredictiveIncident, decision *rca.Decision) string {
	lines := []string{
		"## Incident",
		fmt.Sprintf("- Nome: `%s`", pi.Name),
		fmt.Sprintf("- Namespace: `%s`", pi.Spec.Identity.Namespace),
		fmt.Sprintf("- Serviço: `%s`", pi.Spec.Identity.Service),
		fmt.Sprintf("- Fonte: `%s`", pi.Spec.Source),
		fmt.Sprintf("- Severidade: `%s`", pi.Spec.Severity),
		fmt.Sprintf("- Fingerprint: `%s`", pi.Spec.Fingerprint),
		"",
		"## Descrição",
		pi.Spec.Description,
		"",
		"## RCA",
		fmt.Sprintf("- Classificação: `%s`", pi.Status.RCA.Classification),
		fmt.Sprintf("- Confiança: `%.2f`", pi.Status.RCA.Confidence),
		fmt.Sprintf("- Resumo: %s", pi.Status.RCA.Summary),
	}
	if decision != nil && len(decision.Rollback) > 0 {
		lines = append(lines, "", "## Próximos passos")
		for _, step := range decision.Rollback {
			lines = append(lines, "- "+step)
		}
	}
	return strings.Join(lines, "\n")
}

func buildIssueLabels(pi *sre.PredictiveIncident) []string {
	labels := []string{"miudinho-agent", "incident", "source/" + string(pi.Spec.Source)}
	if pi.Spec.Severity != "" {
		labels = append(labels, "severity/"+strings.ToLower(pi.Spec.Severity))
	}
	return labels
}

func buildEscalationComment(gh githubissuesCommenter, pi *sre.PredictiveIncident, decision *rca.Decision) string {
	mentions := gh.TeamMentions()
	reason := "operator requested escalation"
	if pi.Status.BlockedReason != "" {
		reason = pi.Status.BlockedReason
	}
	if decision != nil {
		if needed, _ := decision.Escalation["needed"].(bool); needed {
			if r, ok := decision.Escalation["reason"].(string); ok && r != "" {
				reason = r
			}
		}
	}

	lines := []string{
		"Escalação automática para N2.",
		"",
		"Times acionados: " + strings.Join(mentions, " "),
		"",
		"Motivo: " + reason,
	}
	return strings.Join(lines, "\n")
}

func buildEscalationSummary(pi *sre.PredictiveIncident, decision *rca.Decision) string {
	reason := pi.Status.BlockedReason
	if reason == "" {
		reason = "operator escalation"
	}
	if decision != nil {
		if r, ok := decision.Escalation["reason"].(string); ok && r != "" {
			reason = r
		}
	}

	lines := []string{
		fmt.Sprintf("Incident `%s` escalated to N2.", pi.Name),
		fmt.Sprintf("Reason: %s", reason),
		fmt.Sprintf("Namespace: %s", pi.Spec.Identity.Namespace),
		fmt.Sprintf("Service: %s", defaultIfEmpty(pi.Spec.Identity.Service, "unknown")),
	}
	if pi.Status.GitHub.URL != "" {
		lines = append(lines, "GitHub issue: "+pi.Status.GitHub.URL)
	}
	return strings.Join(lines, "\n")
}

func appendStatusDetail(current, next string) string {
	if next == "" {
		return current
	}
	if current == "" {
		return next
	}
	return current + "; " + next
}

func defaultIfEmpty(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
