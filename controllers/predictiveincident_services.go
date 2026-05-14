package controllers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	sre "github.com/wellbastos/miudinho-agent/api/v1alpha1"
	"github.com/wellbastos/miudinho-agent/internal/alertmanager"
	"github.com/wellbastos/miudinho-agent/internal/config"
	"github.com/wellbastos/miudinho-agent/internal/githubissues"
	"github.com/wellbastos/miudinho-agent/internal/googlechat"
	appmetrics "github.com/wellbastos/miudinho-agent/internal/metrics"
	"github.com/wellbastos/miudinho-agent/internal/rca"
	"github.com/wellbastos/miudinho-agent/internal/telemetry"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
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

type GitHubIssueClient interface {
	Enabled() bool
	Repository(defaultProduct string) string
	TeamMentions() []string
	TeamSlugs() []string
	CreateIssue(ctx context.Context, repo, title, body string, labels []string) (*githubissues.Issue, error)
	CloseIssue(ctx context.Context, repo string, number int) error
	ReopenIssue(ctx context.Context, repo string, number int) error
	AddComment(ctx context.Context, repo string, number int, body string) error
	// UpdateIssue atualiza o corpo de uma issue existente (usado para refletir RCA melhorado).
	UpdateIssue(ctx context.Context, repo string, number int, body string) error
	// FindOpenIssue busca uma issue aberta pelo label fp-{hash}.
	// Retorna nil, nil quando não há issue aberta.
	FindOpenIssue(ctx context.Context, repo, fpLabel string) (*githubissues.Issue, error)
	// SearchIssueByTitle busca issues (abertas ou fechadas) pelo título exato via Search API.
	// Usado como fallback quando a issue não tem o label fp- (issues antigas).
	SearchIssueByTitle(ctx context.Context, repo, title string) (*githubissues.Issue, error)
}

type EscalationAlertClient interface {
	Enabled() bool
	Send(ctx context.Context, alerts []alertmanager.Alert) error
}

type GoogleChatClient interface {
	Enabled() bool
	Send(ctx context.Context, text string) error
}

type DefaultIncidentEvidenceCollector struct {
	Config config.AppConfig
}

func (c *DefaultIncidentEvidenceCollector) Collect(ctx context.Context, pi *sre.PredictiveIncident) ([]sre.EvidenceItem, error) {
	prom := telemetry.NewPromClient(c.Config.Observability.PromURL)
	tempo := telemetry.NewTempoClient(c.Config.Observability.TempoURL, c.Config.Observability.TempoPredictivePath, c.Config.Observability.TempoPredictiveQueryParam)
	loki := telemetry.NewLokiClient(
		c.Config.Observability.LokiURL,
		c.Config.Observability.LokiUsername,
		c.Config.Observability.LokiPassword,
		c.Config.Observability.LokiToken,
		c.Config.Observability.LokiTenantID,
	)

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
	// Pula a busca no Tempo quando o serviço não foi resolvido — evita queries
	// com service.name="unknown" que são ruidosas e desperdiçam recursos.
	resolvedSvc := pi.Spec.Identity.Service
	if resolvedSvc != "" && resolvedSvc != "unknown" && pi.Spec.Identity.Namespace != "" {
		q := fmt.Sprintf(`service.name="%s" AND k8s.namespace.name="%s" AND (status=error OR timeout OR "deadline exceeded")`, resolvedSvc, pi.Spec.Identity.Namespace)
		// Deadline curto para não bloquear o reconciler quando Tempo está lento.
		tempoCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		rt, err := tempo.SearchContext(tempoCtx, q)
		cancel()
		if err == nil {
			tempoEvidence["search"] = rt
		} else {
			errs = append(errs, fmt.Errorf("tempo search: %w", err))
			tempoEvidence["error"] = err.Error()
		}
	}

	lokiEvidence := map[string]any{}
	if loki.Enabled() && pi.Spec.Identity.Namespace != "" {
		svc := incidentService(pi)
		pod := pi.Spec.Identity.Pod
		lines, meta, err := loki.QueryServiceLogs(ctx, pi.Spec.Identity.Namespace, svc, pod, 100, alertStartsAt(pi))
		if err != nil {
			errs = append(errs, fmt.Errorf("loki query: %w", err))
			lokiEvidence["error"] = err.Error()
		} else if len(lines) > 0 {
			lokiEvidence["lines"] = lines
			lokiEvidence["count"] = len(lines)
			if meta != nil {
				lokiEvidence["query"] = meta["query"]
				lokiEvidence["selector"] = meta["selector"]
			}
		} else {
			lokiEvidence["lines"] = []string{}
			lokiEvidence["count"] = 0
		}
	}

	items := []sre.EvidenceItem{
		{Kind: "prometheus", Summary: "Thanos queries for service health", Ref: "PROM_URL", Data: promEvidence},
		{Kind: "tempo", Summary: "Tempo search hint", Ref: "TEMPO_URL", Data: tempoEvidence},
	}
	if loki.Enabled() {
		items = append(items, sre.EvidenceItem{
			Kind:    "loki",
			Summary: fmt.Sprintf("Pod logs (últimas 100 linhas de %s/%s)", pi.Spec.Identity.Namespace, incidentService(pi)),
			Ref:     "LOKI_URL",
			Data:    lokiEvidence,
		})
	}
	return items, errors.Join(errs...)
}

type DefaultIncidentPolicyResolver struct {
	Client client.Client
}

func (r *DefaultIncidentPolicyResolver) Resolve(ctx context.Context, pi *sre.PredictiveIncident) (*sre.AutoRemediationPolicy, error) {
	var pols sre.AutoRemediationPolicyList
	if err := r.Client.List(ctx, &pols); err != nil {
		return nil, err
	}
	for i := range pols.Items {
		p := &pols.Items[i]
		if namespaceExcluded(pi.Spec.Identity.Namespace, p.Spec.Selector.ExcludedNamespaces) {
			continue
		}
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
		if len(p.Spec.Selector.MatchLabels) > 0 {
			if !matchesIncidentLabels(pi, p.Spec.Selector.MatchLabels) {
				continue
			}
		}
		return p, nil
	}
	return nil, nil
}

func matchesIncidentLabels(pi *sre.PredictiveIncident, selector map[string]string) bool {
	if len(selector) == 0 {
		return true
	}
	if len(pi.Labels) == 0 {
		return false
	}
	for key, want := range selector {
		if got, ok := pi.Labels[key]; !ok || got != want {
			return false
		}
	}
	return true
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

// isInfrastructureAction retorna true para ações que modificam infraestrutura (requerem execute_actions=true).
// Ações de notificação/status como "escalate" e "observeOnly" são sempre permitidas.
func isInfrastructureAction(actionType string) bool {
	switch actionType {
	case "restartPod", "rolloutRestartDeployment":
		return true
	default:
		return false
	}
}

func (e *DefaultIncidentActionExecutor) Execute(ctx context.Context, pi *sre.PredictiveIncident, policy *sre.AutoRemediationPolicy, eval IncidentEvaluation) (bool, error) {
	logger := log.FromContext(ctx).WithValues("component", "incident-action-executor")
	if policy == nil {
		logger.Info("skipping incident actions", "reason", "no_policy")
		return false, nil
	}
	if eval.ObserveOnly {
		logger.Info("skipping incident actions", "reason", "observe_only", "observeOnlyReason", eval.ObserveReason)
		return false, nil
	}
	if eval.Approval == nil {
		logger.Info("skipping incident actions", "reason", "approval_missing")
		return false, nil
	}
	if !eval.Approval.Approved {
		logger.Info("skipping incident actions", "reason", "approval_denied", "riskLevel", eval.Approval.RiskLevel, "approvalReasons", eval.Approval.Reasons)
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
			// Ações de infraestrutura requerem execute_actions=true
			if isInfrastructureAction(act.Type) && !e.Config.Execution.ExecuteActions {
				logger.Info("skipping infrastructure action", "action", act.Type, "reason", "execute_actions_disabled")
				continue
			}

			logger.Info("incident action selected", "action", act.Type, "policy", client.ObjectKeyFromObject(policy).String(), "incident", pi.Name)
			switch act.Type {
			case "observeOnly":
				pi.Status.Phase = sre.PhaseBlocked
				pi.Status.BlockedReason = "policy_observe_only"
				logger.Info("incident action completed", "action", act.Type, "resultPhase", pi.Status.Phase)
				return true, nil

			case "escalate":
				// Ação de notificação — sempre permitida independente de execute_actions
				pi.Status.Phase = sre.PhaseEscalated
				pi.Status.Actions = append(pi.Status.Actions, sre.ActionStatus{
					Name:       "escalate",
					Tool:       "policy_escalate",
					Args:       map[string]any{"incident": pi.Name, "policy": policy.Name},
					Result:     map[string]any{"phase": string(sre.PhaseEscalated)},
					ExecutedAt: time.Now().Format(time.RFC3339),
				})
				logger.Info("incident action completed", "action", act.Type, "tool", "policy_escalate", "resultPhase", pi.Status.Phase)
				return true, nil

			case "restartPod":
				if pi.Spec.Identity.Pod == "" {
					logger.Info("skipping incident action", "action", act.Type, "reason", "pod_missing")
					continue
				}
				pod := &corev1.Pod{}
				pod.Name = pi.Spec.Identity.Pod
				pod.Namespace = pi.Spec.Identity.Namespace
				logger.Info("executing incident action", "action", act.Type, "tool", "k8s_delete_pod", "namespace", pod.Namespace, "pod", pod.Name)
				if err := e.Client.Delete(ctx, pod); err != nil {
					logger.Error(err, "incident action failed", "action", act.Type, "tool", "k8s_delete_pod", "namespace", pod.Namespace, "pod", pod.Name)
					return false, err
				}
				pi.Status.Actions = append(pi.Status.Actions, sre.ActionStatus{Name: "restart-pod", Tool: "k8s_delete_pod", Args: map[string]any{"namespace": pi.Spec.Identity.Namespace, "pod": pi.Spec.Identity.Pod}, Result: map[string]any{"ok": true}, ExecutedAt: time.Now().Format(time.RFC3339)})
				pi.Status.Phase = sre.PhaseMitigated
				logger.Info("incident action completed", "action", act.Type, "tool", "k8s_delete_pod", "namespace", pod.Namespace, "pod", pod.Name, "resultPhase", pi.Status.Phase)
				return true, nil

			case "rolloutRestartDeployment":
				if pi.Spec.Identity.Deployment == "" {
					logger.Info("skipping incident action", "action", act.Type, "reason", "deployment_missing")
					continue
				}
				dep := &appsv1.Deployment{}
				if err := e.Client.Get(ctx, client.ObjectKey{Namespace: pi.Spec.Identity.Namespace, Name: pi.Spec.Identity.Deployment}, dep); err != nil {
					logger.Error(err, "incident action failed", "action", act.Type, "tool", "k8s_get_deployment", "namespace", pi.Spec.Identity.Namespace, "deployment", pi.Spec.Identity.Deployment)
					return false, err
				}
				if dep.Spec.Template.Annotations == nil {
					dep.Spec.Template.Annotations = map[string]string{}
				}
				dep.Spec.Template.Annotations["miudinho-agent/restartedAt"] = fmt.Sprintf("%d", time.Now().Unix())
				logger.Info("executing incident action", "action", act.Type, "tool", "k8s_patch_deployment", "namespace", dep.Namespace, "deployment", dep.Name)
				if err := e.Client.Update(ctx, dep); err != nil {
					logger.Error(err, "incident action failed", "action", act.Type, "tool", "k8s_patch_deployment", "namespace", dep.Namespace, "deployment", dep.Name)
					return false, err
				}
				pi.Status.Actions = append(pi.Status.Actions, sre.ActionStatus{Name: "rollout-restart", Tool: "k8s_patch_deployment", Args: map[string]any{"namespace": pi.Spec.Identity.Namespace, "deployment": pi.Spec.Identity.Deployment}, Result: map[string]any{"ok": true}, ExecutedAt: time.Now().Format(time.RFC3339)})
				pi.Status.Phase = sre.PhaseMitigated
				logger.Info("incident action completed", "action", act.Type, "tool", "k8s_patch_deployment", "namespace", dep.Namespace, "deployment", dep.Name, "resultPhase", pi.Status.Phase)
				return true, nil

			default:
				logger.Info("skipping incident action", "action", act.Type, "reason", "unknown_action_type")
			}
		}
	}
	logger.Info("no incident action matched policy rules")
	return false, nil
}

type DefaultIncidentNotifier struct {
	GitHub GitHubIssueClient
	Alert  EscalationAlertClient
	Chat   GoogleChatClient
}

func (n *DefaultIncidentNotifier) Sync(ctx context.Context, pi *sre.PredictiveIncident, decision *rca.Decision, shouldEscalate bool) error {
	logger := log.FromContext(ctx).WithValues("component", "incident-notifier", "shouldEscalate", shouldEscalate)
	logger.Info("syncing incident notifications")
	var errs []error
	if err := syncGitHubIssue(ctx, pi, n.GitHub, decision, shouldEscalate); err != nil {
		logger.Error(err, "github notification sync failed")
		errs = append(errs, fmt.Errorf("github: %w", err))
	}
	// Envia alerta ao Alertmanager assim que o issue GitHub for criado (independente de escalação)
	if err := syncGitHubIncidentAlert(ctx, pi, n.Alert); err != nil {
		logger.Error(err, "alertmanager github incident alert failed")
		errs = append(errs, fmt.Errorf("alertmanager-github: %w", err))
	}
	if err := syncEscalationAlert(ctx, pi, n.Alert, decision, shouldEscalate); err != nil {
		logger.Error(err, "alertmanager escalation notification failed")
		errs = append(errs, fmt.Errorf("alertmanager: %w", err))
	}
	if err := syncGoogleChatEscalation(ctx, pi, n.Chat, decision, shouldEscalate); err != nil {
		logger.Error(err, "google chat escalation notification failed")
		errs = append(errs, fmt.Errorf("googlechat: %w", err))
	}
	return errors.Join(errs...)
}

func syncGitHubIssue(ctx context.Context, pi *sre.PredictiveIncident, gh GitHubIssueClient, decision *rca.Decision, shouldEscalate bool) error {
	logger := log.FromContext(ctx).WithValues("component", "github-issue-sync")
	if gh == nil || !gh.Enabled() {
		logger.V(1).Info("skipping github issue sync", "reason", "disabled")
		return nil
	}

	repo := issueRepositoryForIncident(pi, gh)
	if pi.Status.GitHub.Repository != "" {
		repo = pi.Status.GitHub.Repository
	}
	if pi.Status.GitHub.Number == 0 {
		// Antes de criar uma nova issue, verifica se já existe uma issue para o mesmo problema.
		// Fase 1: busca pelo label fp-{hash} (issues novas com fingerprint)
		// Fase 2: busca pelo título exato via Search API (issues antigas sem o label)
		existing := findExistingIssue(ctx, gh, repo, pi, logger)

		if existing != nil {
			if err := adoptExistingIssue(ctx, gh, repo, pi, existing, decision, logger); err != nil {
				return err
			}
		} else {
			logger.Info("executing incident action", "action", "open-github-issue", "tool", "github_create_issue", "repository", repo)
			issue, err := gh.CreateIssue(ctx, repo, buildIssueTitle(pi), buildIssueBody(pi, decision), buildIssueLabels(pi))
			if err != nil {
				logger.Error(err, "incident action failed", "action", "open-github-issue", "tool", "github_create_issue", "repository", repo)
				return err
			}
			pi.Status.GitHub.Repository = repo
			pi.Status.GitHub.Number = issue.Number
			pi.Status.GitHub.URL = issue.HTMLURL
			pi.Status.GitHub.State = issue.State
			pi.Status.GitHub.LastSyncTime = time.Now().Format(time.RFC3339)
			pi.Status.GitHub.LastRCAClassification = pi.Status.RCA.Classification
			pi.Status.Actions = append(pi.Status.Actions, sre.ActionStatus{
				Name:       "open-github-issue",
				Tool:       "github_create_issue",
				Args:       map[string]any{"repository": repo},
				Result:     map[string]any{"number": issue.Number, "url": issue.HTMLURL},
				ExecutedAt: time.Now().Format(time.RFC3339),
			})
			logger.Info("incident action completed", "action", "open-github-issue", "tool", "github_create_issue", "repository", repo, "issueNumber", issue.Number, "url", issue.HTMLURL)
		}
	}

	// Se o RCA melhorou desde a última vez que o body da issue foi escrito, atualiza o body
	// para refletir a análise mais recente (ex: investigation → cpu_pressure com logs do Loki).
	rcaImproved := pi.Status.GitHub.Number > 0 &&
		pi.Status.RCA.Classification != "" &&
		pi.Status.RCA.Classification != pi.Status.GitHub.LastRCAClassification
	if rcaImproved {
		logger.Info("executing incident action", "action", "update-github-issue-body", "tool", "github_update_issue",
			"repository", repo, "issueNumber", pi.Status.GitHub.Number,
			"previousClassification", pi.Status.GitHub.LastRCAClassification,
			"newClassification", pi.Status.RCA.Classification,
		)
		if err := gh.UpdateIssue(ctx, repo, pi.Status.GitHub.Number, buildIssueBody(pi, decision)); err != nil {
			logger.Error(err, "incident action failed", "action", "update-github-issue-body", "tool", "github_update_issue",
				"repository", repo, "issueNumber", pi.Status.GitHub.Number)
		} else {
			pi.Status.GitHub.LastRCAClassification = pi.Status.RCA.Classification
			pi.Status.GitHub.LastSyncTime = time.Now().Format(time.RFC3339)
			logger.Info("incident action completed", "action", "update-github-issue-body", "tool", "github_update_issue",
				"repository", repo, "issueNumber", pi.Status.GitHub.Number,
				"classification", pi.Status.RCA.Classification)
		}
	}

	if shouldEscalate && !pi.Status.GitHub.Escalated && pi.Status.GitHub.Number > 0 {
		logger.Info("executing incident action", "action", "escalate-github-issue", "tool", "github_issue_comment", "repository", repo, "issueNumber", pi.Status.GitHub.Number)
		if err := gh.AddComment(ctx, repo, pi.Status.GitHub.Number, buildEscalationComment(gh, pi, decision)); err != nil {
			logger.Error(err, "incident action failed", "action", "escalate-github-issue", "tool", "github_issue_comment", "repository", repo, "issueNumber", pi.Status.GitHub.Number)
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
		logger.Info("incident action completed", "action", "escalate-github-issue", "tool", "github_issue_comment", "repository", repo, "issueNumber", pi.Status.GitHub.Number, "teams", gh.TeamSlugs())
	}

	if shouldCloseIssue(pi) && pi.Status.GitHub.Number > 0 && pi.Status.GitHub.State != "closed" {
		logger.Info("executing incident action", "action", "close-github-issue", "tool", "github_close_issue", "repository", repo, "issueNumber", pi.Status.GitHub.Number)
		if err := gh.CloseIssue(ctx, repo, pi.Status.GitHub.Number); err != nil {
			logger.Error(err, "incident action failed", "action", "close-github-issue", "tool", "github_close_issue", "repository", repo, "issueNumber", pi.Status.GitHub.Number)
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
		logger.Info("incident action completed", "action", "close-github-issue", "tool", "github_close_issue", "repository", repo, "issueNumber", pi.Status.GitHub.Number)
	}

	return nil
}

// syncGitHubIncidentAlert envia um alerta ao Alertmanager sempre que um issue GitHub for criado para o incident.
// O alerta é enviado uma única vez (idempotente via GitHubAlertSent) e inclui o ID do issue GitHub.
func syncGitHubIncidentAlert(ctx context.Context, pi *sre.PredictiveIncident, am EscalationAlertClient) error {
	logger := log.FromContext(ctx).WithValues("component", "github-incident-alert")

	if am == nil || !am.Enabled() {
		logger.V(1).Info("skipping github incident alert", "reason", "alertmanager_disabled")
		return nil
	}
	if pi.Status.GitHub.Number == 0 {
		logger.V(1).Info("skipping github incident alert", "reason", "no_github_issue_yet")
		return nil
	}
	if pi.Status.Alerting.GitHubAlertSent {
		logger.V(1).Info("skipping github incident alert", "reason", "already_sent", "issueNumber", pi.Status.GitHub.Number)
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	alert := alertmanager.Alert{
		Labels: map[string]string{
			"alertname":           "MiudinhoAgentIncidentRegistered",
			"severity":            defaultIfEmpty(strings.ToLower(pi.Spec.Severity), "warning"),
			"namespace":           pi.Spec.Identity.Namespace,
			"service":             defaultIfEmpty(pi.Spec.Identity.Service, "unknown"),
			"source":              string(pi.Spec.Source),
			"incident_name":       pi.Name,
			"incident_phase":      string(pi.Status.Phase),
			"github_issue_number": fmt.Sprintf("%d", pi.Status.GitHub.Number),
			"github_repository":   pi.Status.GitHub.Repository,
			"managed_by":          "miudinho-agent",
		},
		Annotations: map[string]string{
			"summary":          fmt.Sprintf("Incident %s registered as GitHub issue #%d", pi.Name, pi.Status.GitHub.Number),
			"description":      defaultIfEmpty(pi.Spec.Description, pi.Spec.Title),
			"github_issue_url": pi.Status.GitHub.URL,
			"fingerprint":      pi.Spec.Fingerprint,
		},
		StartsAt:     now,
		GeneratorURL: pi.Status.GitHub.URL,
	}

	logger.Info("executing incident action", "action", "send-github-incident-alert",
		"tool", "alertmanager_send_alert",
		"issueNumber", pi.Status.GitHub.Number,
		"issueURL", pi.Status.GitHub.URL,
	)
	if err := am.Send(ctx, []alertmanager.Alert{alert}); err != nil {
		appmetrics.RecordEscalationNotification("alertmanager_github", "error")
		logger.Error(err, "incident action failed", "action", "send-github-incident-alert",
			"tool", "alertmanager_send_alert",
			"issueNumber", pi.Status.GitHub.Number,
		)
		return err
	}
	appmetrics.RecordEscalationNotification("alertmanager_github", "success")

	pi.Status.Alerting.GitHubAlertSent = true
	pi.Status.Alerting.GitHubAlertSentAt = now
	pi.Status.Actions = append(pi.Status.Actions, sre.ActionStatus{
		Name:       "send-github-incident-alert",
		Tool:       "alertmanager_send_alert",
		Args:       map[string]any{"incident": pi.Name, "issueNumber": pi.Status.GitHub.Number},
		Result:     map[string]any{"sent": true, "issueURL": pi.Status.GitHub.URL},
		ExecutedAt: now,
	})
	logger.Info("incident action completed", "action", "send-github-incident-alert",
		"tool", "alertmanager_send_alert",
		"issueNumber", pi.Status.GitHub.Number,
		"issueURL", pi.Status.GitHub.URL,
	)
	return nil
}

func syncEscalationAlert(ctx context.Context, pi *sre.PredictiveIncident, am EscalationAlertClient, decision *rca.Decision, shouldEscalate bool) error {
	if am == nil || !am.Enabled() || !shouldEscalate || pi.Status.Alerting.EscalationSent {
		log.FromContext(ctx).V(1).Info("skipping alertmanager escalation sync", "enabled", am != nil && am.Enabled(), "shouldEscalate", shouldEscalate, "alreadySent", pi.Status.Alerting.EscalationSent)
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

	log.FromContext(ctx).Info("executing incident action", "action", "send-alertmanager-escalation", "tool", "alertmanager_send_alert", "target", "n2")
	if err := am.Send(ctx, []alertmanager.Alert{alert}); err != nil {
		appmetrics.RecordEscalationNotification("alertmanager", "error")
		log.FromContext(ctx).Error(err, "incident action failed", "action", "send-alertmanager-escalation", "tool", "alertmanager_send_alert", "target", "n2")
		return err
	}
	appmetrics.RecordEscalationNotification("alertmanager", "success")

	pi.Status.Alerting.EscalationSent = true
	pi.Status.Alerting.LastSentTime = now
	pi.Status.Actions = append(pi.Status.Actions, sre.ActionStatus{
		Name:       "send-alertmanager-escalation",
		Tool:       "alertmanager_send_alert",
		Args:       map[string]any{"incident": pi.Name, "target": "n2"},
		Result:     map[string]any{"sent": true},
		ExecutedAt: now,
	})
	log.FromContext(ctx).Info("incident action completed", "action", "send-alertmanager-escalation", "tool", "alertmanager_send_alert", "target", "n2")
	return nil
}

func syncGoogleChatEscalation(ctx context.Context, pi *sre.PredictiveIncident, gc GoogleChatClient, decision *rca.Decision, shouldEscalate bool) error {
	if gc == nil || !gc.Enabled() || !shouldEscalate || pi.Status.Chat.EscalationSent {
		log.FromContext(ctx).V(1).Info("skipping google chat escalation sync", "enabled", gc != nil && gc.Enabled(), "shouldEscalate", shouldEscalate, "alreadySent", pi.Status.Chat.EscalationSent)
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	log.FromContext(ctx).Info("executing incident action", "action", "send-google-chat-escalation", "tool", "google_chat_send_message", "channel", "incidents-sre")
	if err := gc.Send(ctx, buildGoogleChatEscalationMessage(pi, decision)); err != nil {
		appmetrics.RecordEscalationNotification("google_chat", "error")
		log.FromContext(ctx).Error(err, "incident action failed", "action", "send-google-chat-escalation", "tool", "google_chat_send_message", "channel", "incidents-sre")
		return err
	}
	appmetrics.RecordEscalationNotification("google_chat", "success")

	pi.Status.Chat.EscalationSent = true
	pi.Status.Chat.LastSentTime = now
	pi.Status.Actions = append(pi.Status.Actions, sre.ActionStatus{
		Name:       "send-google-chat-escalation",
		Tool:       "google_chat_send_message",
		Args:       map[string]any{"incident": pi.Name, "channel": "incidents-sre"},
		Result:     map[string]any{"sent": true},
		ExecutedAt: now,
	})
	log.FromContext(ctx).Info("incident action completed", "action", "send-google-chat-escalation", "tool", "google_chat_send_message", "channel", "incidents-sre")
	return nil
}

// findExistingIssue procura uma issue existente para o mesmo problema em duas fases:
// 1. Por label fp-{12chars} (issues novas com fingerprint)
// 2. Por título exato via Search API (fallback para issues antigas sem o label)
func findExistingIssue(ctx context.Context, gh GitHubIssueClient, repo string, pi *sre.PredictiveIncident, logger logr.Logger) *githubissues.Issue {
	// Fase 1: busca por label de fingerprint (O(1), sem rate limit especial)
	if fpLabel := issueFingerprintLabel(pi); fpLabel != "" {
		existing, err := gh.FindOpenIssue(ctx, repo, fpLabel)
		if err != nil {
			logger.V(1).Info("label search failed — trying title search", "error", err, "fpLabel", fpLabel)
		} else if existing != nil {
			logger.Info("found existing issue by fingerprint label", "issueNumber", existing.Number, "state", existing.State, "fpLabel", fpLabel)
			return existing
		}
	}

	// Fase 2: busca por título exato (cobre issues sem o label fp- — criadas antes do sistema de fingerprint)
	title := buildIssueTitle(pi)
	existing, err := gh.SearchIssueByTitle(ctx, repo, title)
	if err != nil {
		logger.V(1).Info("title search failed", "error", err, "title", title)
		return nil
	}
	if existing != nil {
		logger.Info("found existing issue by title", "issueNumber", existing.Number, "state", existing.State, "title", title)
	}
	return existing
}

// adoptExistingIssue adota uma issue existente para o incident:
//   - Atualiza o status do incident com os dados da issue
//   - Reabre a issue se estiver fechada (problema recorreu)
//   - Adiciona um comentário de recorrência
func adoptExistingIssue(ctx context.Context, gh GitHubIssueClient, repo string, pi *sre.PredictiveIncident, issue *githubissues.Issue, decision *rca.Decision, logger logr.Logger) error {
	pi.Status.GitHub.Repository = repo
	pi.Status.GitHub.Number = issue.Number
	pi.Status.GitHub.URL = issue.HTMLURL
	pi.Status.GitHub.State = issue.State
	pi.Status.GitHub.LastSyncTime = time.Now().Format(time.RFC3339)
	pi.Status.Actions = append(pi.Status.Actions, sre.ActionStatus{
		Name:       "adopt-github-issue",
		Tool:       "github_find_issue",
		Args:       map[string]any{"repository": repo},
		Result:     map[string]any{"number": issue.Number, "url": issue.HTMLURL, "state": issue.State},
		ExecutedAt: time.Now().Format(time.RFC3339),
	})
	logger.Info("adopting existing github issue", "issueNumber", issue.Number, "state", issue.State, "url", issue.HTMLURL)

	// Reabre a issue se estiver fechada — o problema recorreu
	if strings.EqualFold(issue.State, "closed") {
		logger.Info("reopening closed github issue — incident recurred", "issueNumber", issue.Number)
		if err := gh.ReopenIssue(ctx, repo, issue.Number); err != nil {
			logger.Error(err, "failed to reopen github issue", "issueNumber", issue.Number)
			// Não retorna erro — continua para adicionar comentário mesmo assim
		} else {
			pi.Status.GitHub.State = "open"
		}
	}

	// Adiciona comentário de recorrência
	comment := buildRecurrenceComment(pi, decision)
	if err := gh.AddComment(ctx, repo, issue.Number, comment); err != nil {
		logger.Error(err, "failed to add recurrence comment to github issue", "issueNumber", issue.Number)
		// Não retorna erro — issue já foi adotada com sucesso
	}
	return nil
}

// buildRecurrenceComment monta o comentário adicionado na issue quando um incident recorre.
func buildRecurrenceComment(pi *sre.PredictiveIncident, decision *rca.Decision) string {
	lines := []string{
		"## 🔄 Recorrência detectada",
		"",
		fmt.Sprintf("O incident `%s` foi detectado novamente.", pi.Name),
		"",
		"### Contexto",
		fmt.Sprintf("- **Namespace**: `%s`", pi.Spec.Identity.Namespace),
		fmt.Sprintf("- **Serviço**: `%s`", pi.Spec.Identity.Service),
		fmt.Sprintf("- **Severidade**: `%s`", defaultIfEmpty(pi.Spec.Severity, "unknown")),
		fmt.Sprintf("- **Fingerprint**: `%s`", pi.Spec.Fingerprint),
		fmt.Sprintf("- **Data**: `%s`", time.Now().UTC().Format(time.RFC3339)),
	}
	if decision != nil && decision.Summary != "" {
		lines = append(lines, "", "### RCA", decision.Summary)
	}
	return strings.Join(lines, "\n")
}

func issueRepositoryForIncident(pi *sre.PredictiveIncident, gh GitHubIssueClient) string {
	if repo := strings.TrimSpace(githubRepositoryLabel(pi)); repo != "" {
		return repo
	}
	return gh.Repository(pi.Spec.Identity.Service)
}

func githubRepositoryLabel(pi *sre.PredictiveIncident) string {
	if pi == nil || pi.Spec.Alert == nil {
		return ""
	}
	labels, ok := pi.Spec.Alert["labels"].(map[string]any)
	if ok {
		if repo, ok := labels["github_repository"].(string); ok {
			return strings.TrimSpace(repo)
		}
	}
	typedLabels, ok := pi.Spec.Alert["labels"].(map[string]string)
	if ok {
		return strings.TrimSpace(typedLabels["github_repository"])
	}
	return ""
}

func buildGoogleChatEscalationMessage(pi *sre.PredictiveIncident, decision *rca.Decision) string {
	reason := pi.Status.BlockedReason
	if reason == "" {
		reason = "automatic remediation failed"
	}
	if decision != nil {
		if r, ok := decision.Escalation["reason"].(string); ok && r != "" {
			reason = r
		}
	}
	return strings.Join([]string{
		"*Miudinho Agent escalated an incident to N2*",
		"",
		fmt.Sprintf("Alert: %s", defaultIfEmpty(pi.Spec.Title, pi.Name)),
		fmt.Sprintf("Namespace: %s", defaultIfEmpty(pi.Spec.Identity.Namespace, "default")),
		fmt.Sprintf("Service: %s", defaultIfEmpty(pi.Spec.Identity.Service, "unknown")),
		fmt.Sprintf("Severity: %s", defaultIfEmpty(pi.Spec.Severity, "warning")),
		fmt.Sprintf("Phase: %s", defaultIfEmpty(string(pi.Status.Phase), "unknown")),
		fmt.Sprintf("Reason: %s", reason),
		fmt.Sprintf("GitHub issue: %s", defaultIfEmpty(pi.Status.GitHub.URL, "not created")),
	}, "\n")
}

func NewGoogleChatClient(cfg config.NotificationsConfig) GoogleChatClient {
	return googlechat.New(cfg)
}

// alertStartsAt extrai o timestamp startsAt do alerta armazenado em spec.alert["startsAt"].
// Retorna zero time se não encontrado ou não parseável.
func alertStartsAt(pi *sre.PredictiveIncident) time.Time {
	if pi.Spec.Alert == nil {
		return time.Time{}
	}
	raw, ok := pi.Spec.Alert["startsAt"].(string)
	if !ok || raw == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return t
}

// lokiEvidenceEmpty retorna true quando Loki está presente na lista de evidências
// mas não retornou nenhuma linha de log (count == 0). Retorna false se Loki não está
// na lista (desabilitado) ou se há linhas.
func lokiEvidenceEmpty(evidence []sre.EvidenceItem) bool {
	for _, item := range evidence {
		if item.Kind == "loki" {
			count, _ := item.Data["count"].(int)
			return count == 0
		}
	}
	return false
}
