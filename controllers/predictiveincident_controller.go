package controllers

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	sre "github.com/wellbastos/miudinho-agent/api/v1alpha1"
	"github.com/wellbastos/miudinho-agent/internal/alertmanager"
	"github.com/wellbastos/miudinho-agent/internal/githubissues"
	"github.com/wellbastos/miudinho-agent/internal/rca"
	"github.com/wellbastos/miudinho-agent/internal/telemetry"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type PredictiveIncidentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *PredictiveIncidentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sre.PredictiveIncident{}).
		Complete(r)
}

func (r *PredictiveIncidentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	pi := &sre.PredictiveIncident{}
	if err := r.Get(ctx, req.NamespacedName, pi); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if pi.Status.Phase == "" {
		pi.Status.Phase = sre.PhaseNew
	}

	prom := telemetry.NewPromClient(getenv("PROM_URL", "http://thanos-query.o11y.svc.cluster.local:10901"))
	tempo := telemetry.NewTempoClient(getenv("TEMPO_URL", "http://tempo.o11y.svc.cluster.local:3100"), getenv("TEMPO_PREDICTIVE_PATH", "api/search"), getenv("TEMPO_PREDICTIVE_QUERY_PARAM", "q"))

	promEvidence := map[string]any{}
	if pi.Spec.Identity.Service != "" && pi.Spec.Identity.Job != "" {
		ns, svc, job := pi.Spec.Identity.Namespace, pi.Spec.Identity.Service, pi.Spec.Identity.Job
		q5xx := fmt.Sprintf(`sum(rate(http_requests_total{namespace="%s",service="%s",job="%s",status=~"5.."}[5m]))`, ns, svc, job)
		qtot := fmt.Sprintf(`sum(rate(http_requests_total{namespace="%s",service="%s",job="%s"}[5m]))`, ns, svc, job)
		qer := fmt.Sprintf(`100*(%s)/clamp_min((%s),1)`, q5xx, qtot)
		if r1, err := prom.Query(qer); err == nil {
			promEvidence["error_rate_pct"] = r1
		}
		if r2, err := prom.Query(q5xx); err == nil {
			promEvidence["five_xx_rps"] = r2
		}
	}

	tempoEvidence := map[string]any{}
	if pi.Spec.Identity.Service != "" && pi.Spec.Identity.Namespace != "" {
		q := fmt.Sprintf(`service.name="%s" AND k8s.namespace.name="%s" AND (status=error OR timeout OR "deadline exceeded")`, pi.Spec.Identity.Service, pi.Spec.Identity.Namespace)
		if rt, err := tempo.Search(q); err == nil {
			tempoEvidence["search"] = rt
		} else {
			tempoEvidence["error"] = err.Error()
		}
	}

	pi.Status.Evidence = upsertEvidence(pi.Status.Evidence, sre.EvidenceItem{Kind: "prometheus", Summary: "Thanos queries for service health", Ref: "PROM_URL", Data: promEvidence})
	pi.Status.Evidence = upsertEvidence(pi.Status.Evidence, sre.EvidenceItem{Kind: "tempo", Summary: "Tempo search hint", Ref: "TEMPO_URL", Data: tempoEvidence})

	policy, _ := r.selectPolicy(ctx, pi)

	engine := rca.NewEngine(systemPrompt(), approverPrompt())
	hctx, cancel := rca.WithTimeout()
	defer cancel()
	engine.Healthcheck(hctx)

	decision, _ := engine.Analyze(hctx, map[string]any{
		"source": string(pi.Spec.Source),
		"identity": map[string]any{
			"namespace":  pi.Spec.Identity.Namespace,
			"service":    pi.Spec.Identity.Service,
			"job":        pi.Spec.Identity.Job,
			"pod":        pi.Spec.Identity.Pod,
			"deployment": pi.Spec.Identity.Deployment,
		},
		"signals":  pi.Spec.Signals,
		"evidence": pi.Status.Evidence,
	})

	approval, _ := engine.Approve(hctx, decision, map[string]any{
		"policy": policy,
		"incident": map[string]any{
			"name":      pi.Name,
			"namespace": pi.Namespace,
		},
	})

	observeOnly, observeReason := engine.ObserveOnly()
	if observeOnly {
		pi.Status.Phase = sre.PhaseBlocked
		pi.Status.BlockedReason = "observe_only"
		pi.Status.BlockedDetails = observeReason
	}

	pi.Status.RCA = sre.RCAStatus{
		Classification: decision.Classification,
		Confidence:     decision.Confidence,
		Summary:        decision.Summary,
		Details: map[string]any{
			"approval":          approval,
			"observeOnly":       observeOnly,
			"observeOnlyReason": observeReason,
			"engine":            engine.Snapshot(),
		},
	}

	issueClient := githubissues.NewFromEnv()
	alertClient := alertmanager.NewClientFromEnv()
	if err := r.syncGitHubIssue(ctx, pi, issueClient, decision, shouldEscalateIssue(pi, decision, observeOnly || !approval.Approved)); err != nil {
		pi.Status.BlockedDetails = appendStatusDetail(pi.Status.BlockedDetails, "github_issue_sync_error: "+err.Error())
	}
	if err := r.syncEscalationAlert(ctx, pi, alertClient, decision, shouldEscalateIssue(pi, decision, observeOnly || !approval.Approved)); err != nil {
		pi.Status.BlockedDetails = appendStatusDetail(pi.Status.BlockedDetails, "alertmanager_sync_error: "+err.Error())
	}

	if observeOnly || !approval.Approved || policy == nil || os.Getenv("EXECUTE_ACTIONS") != "true" {
		pi.Status.Phase = sre.PhaseEnriched
		pi.Status.ObservedGeneration = pi.Generation
		pi.Status.LastUpdateTime = time.Now().Format(time.RFC3339)
		_ = r.Status().Update(ctx, pi)
		return ctrl.Result{RequeueAfter: 2 * time.Minute}, nil
	}

	acted := false
	for _, rule := range policy.Spec.Rules {
		if rule.When.Source != "" && rule.When.Source != string(pi.Spec.Source) {
			continue
		}
		if rule.When.Classification != "" && rule.When.Classification != decision.Classification {
			continue
		}
		for _, act := range rule.Actions {
			switch act.Type {
			case "observeOnly":
			case "escalate":
				pi.Status.Phase = sre.PhaseEscalated
				acted = true
			case "restartPod":
				if pi.Spec.Identity.Pod == "" {
					continue
				}
				pod := &corev1.Pod{}
				pod.Name = pi.Spec.Identity.Pod
				pod.Namespace = pi.Spec.Identity.Namespace
				if err := r.Delete(ctx, pod); err == nil {
					pi.Status.Actions = append(pi.Status.Actions, sre.ActionStatus{Name: "restart-pod", Tool: "k8s_delete_pod", Args: map[string]any{"namespace": pi.Spec.Identity.Namespace, "pod": pi.Spec.Identity.Pod}, Result: map[string]any{"ok": true}, ExecutedAt: time.Now().Format(time.RFC3339)})
					pi.Status.Phase = sre.PhaseMitigated
					acted = true
				}
			case "rolloutRestartDeployment":
				if pi.Spec.Identity.Deployment == "" {
					continue
				}
				dep := &appsv1.Deployment{}
				if err := r.Get(ctx, client.ObjectKey{Namespace: pi.Spec.Identity.Namespace, Name: pi.Spec.Identity.Deployment}, dep); err == nil {
					if dep.Spec.Template.Annotations == nil {
						dep.Spec.Template.Annotations = map[string]string{}
					}
					dep.Spec.Template.Annotations["miudinho-agent/restartedAt"] = fmt.Sprintf("%d", time.Now().Unix())
					if err := r.Update(ctx, dep); err == nil {
						pi.Status.Actions = append(pi.Status.Actions, sre.ActionStatus{Name: "rollout-restart", Tool: "k8s_patch_deployment", Args: map[string]any{"namespace": pi.Spec.Identity.Namespace, "deployment": pi.Spec.Identity.Deployment}, Result: map[string]any{"ok": true}, ExecutedAt: time.Now().Format(time.RFC3339)})
						pi.Status.Phase = sre.PhaseMitigated
						acted = true
					}
				}
			}
		}
		if acted {
			break
		}
	}

	if !acted && pi.Status.Phase == sre.PhaseNew {
		pi.Status.Phase = sre.PhaseEnriched
	}

	pi.Status.ObservedGeneration = pi.Generation
	pi.Status.LastUpdateTime = time.Now().Format(time.RFC3339)
	if err := r.syncGitHubIssue(ctx, pi, issueClient, decision, shouldEscalateIssue(pi, decision, pi.Status.Phase == sre.PhaseEscalated)); err != nil {
		pi.Status.BlockedDetails = appendStatusDetail(pi.Status.BlockedDetails, "github_issue_sync_error: "+err.Error())
	}
	if err := r.syncEscalationAlert(ctx, pi, alertClient, decision, shouldEscalateIssue(pi, decision, pi.Status.Phase == sre.PhaseEscalated)); err != nil {
		pi.Status.BlockedDetails = appendStatusDetail(pi.Status.BlockedDetails, "alertmanager_sync_error: "+err.Error())
	}
	_ = r.Status().Update(ctx, pi)

	return ctrl.Result{RequeueAfter: 3 * time.Minute}, nil
}

func (r *PredictiveIncidentReconciler) selectPolicy(ctx context.Context, pi *sre.PredictiveIncident) (*sre.AutoRemediationPolicy, error) {
	var pols sre.AutoRemediationPolicyList
	if err := r.List(ctx, &pols, client.InNamespace(pi.Spec.Identity.Namespace)); err != nil {
		return nil, err
	}
	for i := range pols.Items {
		p := &pols.Items[i]
		if p.Spec.Selector.Namespace != "" && p.Spec.Selector.Namespace != pi.Spec.Identity.Namespace {
			continue
		}
		if len(p.Spec.Selector.Severities) > 0 && pi.Spec.Severity != "" {
			ok := false
			for _, s := range p.Spec.Selector.Severities {
				if s == pi.Spec.Severity {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		return p, nil
	}
	return nil, nil
}

func upsertEvidence(list []sre.EvidenceItem, ev sre.EvidenceItem) []sre.EvidenceItem {
	out := make([]sre.EvidenceItem, 0, len(list)+1)
	replaced := false
	for _, x := range list {
		if x.Kind == ev.Kind {
			out = append(out, ev)
			replaced = true
		} else {
			out = append(out, x)
		}
	}
	if !replaced {
		out = append(out, ev)
	}
	return out
}

func (r *PredictiveIncidentReconciler) syncGitHubIssue(ctx context.Context, pi *sre.PredictiveIncident, gh *githubissues.Client, decision *rca.Decision, shouldEscalate bool) error {
	if gh == nil || !gh.Enabled() {
		return nil
	}

	repo := gh.Repository(pi.Spec.Identity.Service)
	if pi.Status.GitHub.Repository != "" {
		repo = pi.Status.GitHub.Repository
	}
	if pi.Status.GitHub.Number == 0 {
		issue, err := gh.CreateIssue(ctx, repo, buildIssueTitle(pi), buildIssueBody(pi, decision), buildIssueLabels(pi))
		if err != nil {
			return err
		}
		pi.Status.GitHub.Repository = repo
		pi.Status.GitHub.Number = issue.Number
		pi.Status.GitHub.URL = issue.HTMLURL
		pi.Status.GitHub.State = issue.State
		pi.Status.GitHub.LastSyncTime = time.Now().Format(time.RFC3339)
		pi.Status.Actions = append(pi.Status.Actions, sre.ActionStatus{
			Name:       "open-github-issue",
			Tool:       "github_create_issue",
			Args:       map[string]any{"repository": repo},
			Result:     map[string]any{"number": issue.Number, "url": issue.HTMLURL},
			ExecutedAt: time.Now().Format(time.RFC3339),
		})
	}

	if shouldEscalate && !pi.Status.GitHub.Escalated && pi.Status.GitHub.Number > 0 {
		if err := gh.AddComment(ctx, repo, pi.Status.GitHub.Number, buildEscalationComment(gh, pi, decision)); err != nil {
			return err
		}
		pi.Status.GitHub.Escalated = true
		pi.Status.GitHub.EscalatedTeams = gh.TeamSlugs()
		pi.Status.GitHub.LastSyncTime = time.Now().Format(time.RFC3339)
		pi.Status.Actions = append(pi.Status.Actions, sre.ActionStatus{
			Name:       "escalate-github-issue",
			Tool:       "github_issue_comment",
			Args:       map[string]any{"repository": repo, "issueNumber": pi.Status.GitHub.Number},
			Result:     map[string]any{"teams": gh.TeamSlugs()},
			ExecutedAt: time.Now().Format(time.RFC3339),
		})
	}

	if shouldCloseIssue(pi) && pi.Status.GitHub.Number > 0 && pi.Status.GitHub.State != "closed" {
		if err := gh.CloseIssue(ctx, repo, pi.Status.GitHub.Number); err != nil {
			return err
		}
		pi.Status.GitHub.State = "closed"
		pi.Status.GitHub.ClosedAt = time.Now().Format(time.RFC3339)
		pi.Status.GitHub.LastSyncTime = pi.Status.GitHub.ClosedAt
		pi.Status.Actions = append(pi.Status.Actions, sre.ActionStatus{
			Name:       "close-github-issue",
			Tool:       "github_close_issue",
			Args:       map[string]any{"repository": repo, "issueNumber": pi.Status.GitHub.Number},
			Result:     map[string]any{"state": "closed"},
			ExecutedAt: time.Now().Format(time.RFC3339),
		})
	}

	return nil
}

func shouldCloseIssue(pi *sre.PredictiveIncident) bool {
	return pi.Status.Phase == sre.PhaseMitigated || pi.Status.Phase == sre.PhaseResolved
}

func (r *PredictiveIncidentReconciler) syncEscalationAlert(ctx context.Context, pi *sre.PredictiveIncident, am *alertmanager.Client, decision *rca.Decision, shouldEscalate bool) error {
	if am == nil || !am.Enabled() || !shouldEscalate || pi.Status.Alerting.EscalationSent {
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	alert := alertmanager.Alert{
		Labels: map[string]string{
			"alertname":           "MiudinhoAgentEscalatedIncident",
			"severity":            defaultIfEmpty(strings.ToLower(pi.Spec.Severity), "warning"),
			"namespace":           pi.Spec.Identity.Namespace,
			"service":             defaultIfEmpty(pi.Spec.Identity.Service, "unknown"),
			"source":              string(pi.Spec.Source),
			"incident_name":       pi.Name,
			"incident_phase":      string(pi.Status.Phase),
			"escalation_target":   "n2",
			"managed_by":          "miudinho-agent",
			"github_issue_number": fmt.Sprintf("%d", pi.Status.GitHub.Number),
		},
		Annotations: map[string]string{
			"summary":          defaultIfEmpty(pi.Status.RCA.Summary, pi.Spec.Title),
			"description":      buildEscalationSummary(pi, decision),
			"github_issue_url": pi.Status.GitHub.URL,
		},
		StartsAt:     now,
		GeneratorURL: pi.Status.GitHub.URL,
	}

	if err := am.Send(ctx, []alertmanager.Alert{alert}); err != nil {
		return err
	}

	pi.Status.Alerting.EscalationSent = true
	pi.Status.Alerting.LastSentTime = now
	pi.Status.Actions = append(pi.Status.Actions, sre.ActionStatus{
		Name:       "send-alertmanager-escalation",
		Tool:       "alertmanager_send_alert",
		Args:       map[string]any{"incident": pi.Name, "target": "n2"},
		Result:     map[string]any{"sent": true},
		ExecutedAt: now,
	})
	return nil
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

func buildEscalationComment(gh *githubissues.Client, pi *sre.PredictiveIncident, decision *rca.Decision) string {
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

func systemPrompt() string {
	return getenv("SYSTEM_PROMPT", `Você é um Agente SRE de produção. Responda sempre em JSON válido com classification, confidence, summary, evidence, prom_queries, actions, rollback_or_next_steps, escalation. Para predictive, priorize low-risk. Confidence < 0.70 => sem mudanças.`)
}

func approverPrompt() string {
	return getenv("APPROVER_PROMPT", `Você é o Change Approver. Responda somente JSON com approved, risk_level, reasons, required_changes. Bloqueie ações arriscadas e qualquer confidence < 0.70.`)
}
