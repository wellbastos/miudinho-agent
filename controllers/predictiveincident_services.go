package controllers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	sre "github.com/wellbastos/miudinho-agent/api/v1alpha1"
	"github.com/wellbastos/miudinho-agent/internal/alertmanager"
	"github.com/wellbastos/miudinho-agent/internal/config"
	"github.com/wellbastos/miudinho-agent/internal/githubissues"
	"github.com/wellbastos/miudinho-agent/internal/rca"
	"github.com/wellbastos/miudinho-agent/internal/telemetry"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type IncidentEvaluation struct {
	Decision       *rca.Decision
	Approval       *rca.Approval
	ObserveOnly    bool
	ObserveReason  string
	EngineSnapshot map[string]any
}

type IncidentEvidenceCollector interface {
	Collect(ctx context.Context, pi *sre.PredictiveIncident) ([]sre.EvidenceItem, error)
}

type IncidentPolicyResolver interface {
	Resolve(ctx context.Context, pi *sre.PredictiveIncident) (*sre.AutoRemediationPolicy, error)
}

type IncidentDecisionService interface {
	Evaluate(ctx context.Context, pi *sre.PredictiveIncident, policy *sre.AutoRemediationPolicy) (IncidentEvaluation, error)
}

type IncidentActionExecutor interface {
	Execute(ctx context.Context, pi *sre.PredictiveIncident, policy *sre.AutoRemediationPolicy, eval IncidentEvaluation) (bool, error)
}

type IncidentNotifier interface {
	Sync(ctx context.Context, pi *sre.PredictiveIncident, decision *rca.Decision, shouldEscalate bool) error
}

type DefaultIncidentEvidenceCollector struct {
	Config config.AppConfig
}

func (c *DefaultIncidentEvidenceCollector) Collect(ctx context.Context, pi *sre.PredictiveIncident) ([]sre.EvidenceItem, error) {
	prom := telemetry.NewPromClient(c.Config.Observability.PromURL)
	tempo := telemetry.NewTempoClient(c.Config.Observability.TempoURL, c.Config.Observability.TempoPredictivePath, c.Config.Observability.TempoPredictiveQueryParam)

	var errs []error
	promEvidence := map[string]any{}
	if pi.Spec.Identity.Service != "" && pi.Spec.Identity.Job != "" {
		ns, svc, job := pi.Spec.Identity.Namespace, pi.Spec.Identity.Service, pi.Spec.Identity.Job
		q5xx := fmt.Sprintf(`sum(rate(http_requests_total{namespace="%s",service="%s",job="%s",status=~"5.."}[5m]))`, ns, svc, job)
		qtot := fmt.Sprintf(`sum(rate(http_requests_total{namespace="%s",service="%s",job="%s"}[5m]))`, ns, svc, job)
		qer := fmt.Sprintf(`100*(%s)/clamp_min((%s),1)`, q5xx, qtot)
		if r1, err := prom.Query(qer); err == nil {
			promEvidence["error_rate_pct"] = r1
		} else {
			errs = append(errs, fmt.Errorf("prometheus error_rate query: %w", err))
			promEvidence["error"] = err.Error()
		}
		if r2, err := prom.Query(q5xx); err == nil {
			promEvidence["five_xx_rps"] = r2
		} else {
			errs = append(errs, fmt.Errorf("prometheus 5xx query: %w", err))
			promEvidence["five_xx_rps_error"] = err.Error()
		}
	}

	tempoEvidence := map[string]any{}
	if pi.Spec.Identity.Service != "" && pi.Spec.Identity.Namespace != "" {
		q := fmt.Sprintf(`service.name="%s" AND k8s.namespace.name="%s" AND (status=error OR timeout OR "deadline exceeded")`, pi.Spec.Identity.Service, pi.Spec.Identity.Namespace)
		if rt, err := tempo.Search(q); err == nil {
			tempoEvidence["search"] = rt
		} else {
			errs = append(errs, fmt.Errorf("tempo search: %w", err))
			tempoEvidence["error"] = err.Error()
		}
	}

	return []sre.EvidenceItem{
		{Kind: "prometheus", Summary: "Thanos queries for service health", Ref: "PROM_URL", Data: promEvidence},
		{Kind: "tempo", Summary: "Tempo search hint", Ref: "TEMPO_URL", Data: tempoEvidence},
	}, errors.Join(errs...)
}

type DefaultIncidentPolicyResolver struct {
	Client client.Client
}

func (r *DefaultIncidentPolicyResolver) Resolve(ctx context.Context, pi *sre.PredictiveIncident) (*sre.AutoRemediationPolicy, error) {
	var pols sre.AutoRemediationPolicyList
	if err := r.Client.List(ctx, &pols, client.InNamespace(pi.Spec.Identity.Namespace)); err != nil {
		return nil, err
	}
	for i := range pols.Items {
		p := &pols.Items[i]
		if p.Spec.Selector.Namespace != "" && p.Spec.Selector.Namespace != pi.Spec.Identity.Namespace {
			continue
		}
		if len(p.Spec.Selector.Severities) > 0 && pi.Spec.Severity != "" {
			matched := false
			for _, s := range p.Spec.Selector.Severities {
				if s == pi.Spec.Severity {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		return p, nil
	}
	return nil, nil
}

type DefaultIncidentDecisionService struct {
	Config config.AppConfig
}

func (s *DefaultIncidentDecisionService) Evaluate(ctx context.Context, pi *sre.PredictiveIncident, policy *sre.AutoRemediationPolicy) (IncidentEvaluation, error) {
	engine := rca.NewEngine(s.Config)
	hctx, cancel := rca.WithTimeout()
	defer cancel()

	engine.Healthcheck(hctx)
	decision, analyzeErr := engine.Analyze(hctx, map[string]any{
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

	approval, approveErr := engine.Approve(hctx, decision, map[string]any{
		"policy": policy,
		"incident": map[string]any{
			"name":      pi.Name,
			"namespace": pi.Namespace,
		},
	})

	observeOnly, observeReason := engine.ObserveOnly()
	return IncidentEvaluation{
		Decision:       decision,
		Approval:       approval,
		ObserveOnly:    observeOnly,
		ObserveReason:  observeReason,
		EngineSnapshot: engine.Snapshot(),
	}, errors.Join(analyzeErr, approveErr)
}

type DefaultIncidentActionExecutor struct {
	Client client.Client
	Config config.AppConfig
}

func (e *DefaultIncidentActionExecutor) Execute(ctx context.Context, pi *sre.PredictiveIncident, policy *sre.AutoRemediationPolicy, eval IncidentEvaluation) (bool, error) {
	if policy == nil || !e.Config.Execution.ExecuteActions || eval.ObserveOnly || eval.Approval == nil || !eval.Approval.Approved {
		return false, nil
	}

	for _, rule := range policy.Spec.Rules {
		if rule.When.Source != "" && rule.When.Source != string(pi.Spec.Source) {
			continue
		}
		if eval.Decision != nil && rule.When.Classification != "" && rule.When.Classification != eval.Decision.Classification {
			continue
		}
		for _, act := range rule.Actions {
			switch act.Type {
			case "observeOnly":
				pi.Status.Phase = sre.PhaseBlocked
				pi.Status.BlockedReason = "policy_observe_only"
				return true, nil
			case "escalate":
				pi.Status.Phase = sre.PhaseEscalated
				pi.Status.Actions = append(pi.Status.Actions, sre.ActionStatus{
					Name:       "escalate",
					Tool:       "policy_escalate",
					Args:       map[string]any{"incident": pi.Name},
					Result:     map[string]any{"phase": string(sre.PhaseEscalated)},
					ExecutedAt: time.Now().Format(time.RFC3339),
				})
				return true, nil
			case "restartPod":
				if pi.Spec.Identity.Pod == "" {
					continue
				}
				pod := &corev1.Pod{}
				pod.Name = pi.Spec.Identity.Pod
				pod.Namespace = pi.Spec.Identity.Namespace
				if err := e.Client.Delete(ctx, pod); err != nil {
					return false, err
				}
				pi.Status.Actions = append(pi.Status.Actions, sre.ActionStatus{Name: "restart-pod", Tool: "k8s_delete_pod", Args: map[string]any{"namespace": pi.Spec.Identity.Namespace, "pod": pi.Spec.Identity.Pod}, Result: map[string]any{"ok": true}, ExecutedAt: time.Now().Format(time.RFC3339)})
				pi.Status.Phase = sre.PhaseMitigated
				return true, nil
			case "rolloutRestartDeployment":
				if pi.Spec.Identity.Deployment == "" {
					continue
				}
				dep := &appsv1.Deployment{}
				if err := e.Client.Get(ctx, client.ObjectKey{Namespace: pi.Spec.Identity.Namespace, Name: pi.Spec.Identity.Deployment}, dep); err != nil {
					return false, err
				}
				if dep.Spec.Template.Annotations == nil {
					dep.Spec.Template.Annotations = map[string]string{}
				}
				dep.Spec.Template.Annotations["miudinho-agent/restartedAt"] = fmt.Sprintf("%d", time.Now().Unix())
				if err := e.Client.Update(ctx, dep); err != nil {
					return false, err
				}
				pi.Status.Actions = append(pi.Status.Actions, sre.ActionStatus{Name: "rollout-restart", Tool: "k8s_patch_deployment", Args: map[string]any{"namespace": pi.Spec.Identity.Namespace, "deployment": pi.Spec.Identity.Deployment}, Result: map[string]any{"ok": true}, ExecutedAt: time.Now().Format(time.RFC3339)})
				pi.Status.Phase = sre.PhaseMitigated
				return true, nil
			}
		}
	}
	return false, nil
}

type DefaultIncidentNotifier struct {
	GitHub *githubissues.Client
	Alert  *alertmanager.Client
}

func (n *DefaultIncidentNotifier) Sync(ctx context.Context, pi *sre.PredictiveIncident, decision *rca.Decision, shouldEscalate bool) error {
	if err := syncGitHubIssue(ctx, pi, n.GitHub, decision, shouldEscalate); err != nil {
		return err
	}
	if err := syncEscalationAlert(ctx, pi, n.Alert, decision, shouldEscalate); err != nil {
		return err
	}
	return nil
}

func syncGitHubIssue(ctx context.Context, pi *sre.PredictiveIncident, gh *githubissues.Client, decision *rca.Decision, shouldEscalate bool) error {
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

func syncEscalationAlert(ctx context.Context, pi *sre.PredictiveIncident, am *alertmanager.Client, decision *rca.Decision, shouldEscalate bool) error {
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
