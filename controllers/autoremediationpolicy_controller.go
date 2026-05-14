package controllers

import (
	"context"
	"strings"
	"time"

	sre "github.com/wellbastos/miudinho-agent/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type AutoRemediationPolicyReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

func (r *AutoRemediationPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sre.AutoRemediationPolicy{}).
		Complete(r)
}

func (r *AutoRemediationPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("autoRemediationPolicy", req.String())

	pol := &sre.AutoRemediationPolicy{}
	if err := r.Get(ctx, req.NamespacedName, pol); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	ruleCount := len(pol.Spec.Rules)
	actionTypes := collectActionTypes(pol)

	logger.Info("reconciling auto-remediation policy",
		"generation", pol.Generation,
		"rules", ruleCount,
		"actionTypes", actionTypes,
		"selector", map[string]any{
			"namespace":   pol.Spec.Selector.Namespace,
			"matchLabels": pol.Spec.Selector.MatchLabels,
			"severities":  pol.Spec.Selector.Severities,
		},
		"guardrails", map[string]any{
			"minAvailableReplicas":   pol.Spec.Guardrails.MinAvailableReplicas,
			"restartCooldownSeconds": pol.Spec.Guardrails.RestartCooldownSeconds,
			"allowedActions":         pol.Spec.Guardrails.AllowedActions,
			"blockIfReasons":         pol.Spec.Guardrails.BlockIfReasons,
		},
	)

	if ruleCount == 0 {
		logger.Info("auto-remediation policy has no rules — no incidents will be matched by this policy")
		if r.Recorder != nil {
			r.Recorder.Eventf(pol, "Warning", "NoRules", "Policy has no rules defined; no incidents will be matched")
		}
	} else {
		// Log each rule for visibility
		for i, rule := range pol.Spec.Rules {
			acts := make([]string, 0, len(rule.Actions))
			for _, a := range rule.Actions {
				acts = append(acts, a.Type)
			}
			logger.Info("policy rule active",
				"ruleIndex", i,
				"whenSource", rule.When.Source,
				"whenClassification", rule.When.Classification,
				"actions", strings.Join(acts, ","),
			)
		}
		if r.Recorder != nil {
			r.Recorder.Eventf(pol, "Normal", "Active",
				"Policy active with %d rule(s), actions: %s", ruleCount, strings.Join(actionTypes, ","))
		}
	}

	pol.Status.ObservedGeneration = pol.Generation
	if err := r.Status().Update(ctx, pol); err != nil {
		logger.Error(err, "failed to update auto-remediation policy status")
		return ctrl.Result{}, err
	}

	logger.Info("auto-remediation policy reconciled", "generation", pol.Generation, "rules", ruleCount)
	return ctrl.Result{RequeueAfter: 10 * time.Minute}, nil
}

func collectActionTypes(pol *sre.AutoRemediationPolicy) []string {
	seen := map[string]struct{}{}
	for _, rule := range pol.Spec.Rules {
		for _, act := range rule.Actions {
			seen[act.Type] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	return out
}
