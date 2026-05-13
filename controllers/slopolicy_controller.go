package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	sre "github.com/wellbastos/miudinho-agent/api/v1alpha1"
	"github.com/wellbastos/miudinho-agent/internal/config"
	"github.com/wellbastos/miudinho-agent/internal/telemetry"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type PromQueryAPI interface {
	Query(query string) (map[string]any, error)
}

type sloTarget struct {
	namespace string
	service   string
	job       string
	labels    map[string]string
}

type SLOPolicyReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Config   config.AppConfig
	Recorder record.EventRecorder
	PromAPI  PromQueryAPI
}

func (r *SLOPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sre.SLOPolicy{}).
		Complete(r)
}

func (r *SLOPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	slo := &sre.SLOPolicy{}
	if err := r.Get(ctx, req.NamespacedName, slo); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	interval := time.Duration(slo.Spec.ScheduleSeconds) * time.Second
	if interval <= 0 {
		interval = 120 * time.Second
	}

	prom := r.PromAPI
	if prom == nil {
		prom = telemetry.NewPromClient(r.Config.Observability.PromURL)
	}

	targets, err := r.discoverTargets(ctx, slo, req.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if len(targets) == 0 {
		return ctrl.Result{RequeueAfter: interval}, nil
	}

	erThr := slo.Spec.Signals.Http5xx.ErrorRateThresholdPct
	if erThr == 0 {
		erThr = 1.0
	}
	slopeThr := slo.Spec.Signals.Http5xx.SlopeThreshold
	min5xx := slo.Spec.Signals.Http5xx.Min5xxRPS
	if min5xx == 0 {
		min5xx = 0.1
	}

	for _, target := range targets {
		q5xx := fmt.Sprintf(`sum(rate(http_requests_total{namespace="%s",service="%s",job="%s",status=~"5.."}[5m]))`, target.namespace, target.service, target.job)
		qtot := fmt.Sprintf(`sum(rate(http_requests_total{namespace="%s",service="%s",job="%s"}[5m]))`, target.namespace, target.service, target.job)
		qer := fmt.Sprintf(`100*(%s)/clamp_min((%s),1)`, q5xx, qtot)
		qslope := fmt.Sprintf(`deriv((%s)[30m:1m])`, q5xx)

		erResp, err := prom.Query(qer)
		if err != nil {
			return ctrl.Result{}, err
		}
		rpsResp, err := prom.Query(q5xx)
		if err != nil {
			return ctrl.Result{}, err
		}
		slopeResp, err := prom.Query(qslope)
		if err != nil {
			return ctrl.Result{}, err
		}

		er := promScalar(erResp)
		rps := promScalar(rpsResp)
		slope := promScalar(slopeResp)

		risk := er >= erThr && rps >= min5xx && slope > slopeThr
		if !risk {
			continue
		}

		fp := fingerprint(fmt.Sprintf("slo|%s|%s|%s|%s", target.namespace, target.service, target.job, slo.Name))
		name := "pi-pred-" + fp[:12]

		labels := map[string]string{
			"miudinho.o11y.io/source":      "predictive",
			"miudinho.o11y.io/fingerprint": fp,
			"miudinho.o11y.io/policy":      slo.Name,
			"app.kubernetes.io/name":       target.service,
		}
		for key, value := range target.labels {
			labels[key] = value
		}

		pi := &sre.PredictiveIncident{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: target.namespace,
				Labels:    labels,
			},
			Spec: sre.PredictiveIncidentSpec{
				Source:      sre.SourcePredictive,
				Fingerprint: fp,
				Severity:    "warning",
				Title:       "Predictive risk detected",
				Description: "Service shows predictive error trend",
				Identity: sre.IncidentIdentity{
					Namespace: target.namespace,
					Service:   target.service,
					Job:       target.job,
				},
				Signals: map[string]any{
					"error_rate_pct": er,
					"five_xx_rps":    rps,
					"five_xx_slope":  slope,
				},
			},
		}

		current := &sre.PredictiveIncident{}
		err = r.Get(ctx, client.ObjectKey{Namespace: target.namespace, Name: name}, current)
		if client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
		if err != nil {
			if err := r.Create(ctx, pi); err != nil {
				return ctrl.Result{}, err
			}
		} else {
			current.Spec = pi.Spec
			current.Labels = pi.Labels
			if err := r.Update(ctx, current); err != nil {
				return ctrl.Result{}, err
			}
		}
		if r.Recorder != nil {
			r.Recorder.Eventf(slo, "Normal", "PredictiveIncidentCreated", "Predictive incident %s evaluated as risky", name)
		}
	}

	slo.Status.LastRunTime = time.Now().Format(time.RFC3339)
	slo.Status.ObservedGeneration = slo.Generation
	if err := r.Status().Update(ctx, slo); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: interval}, nil
}

func (r *SLOPolicyReconciler) discoverTargets(ctx context.Context, slo *sre.SLOPolicy, defaultNamespace string) ([]sloTarget, error) {
	targetNamespace := strings.TrimSpace(slo.Spec.Service.Namespace)
	explicitService := strings.TrimSpace(slo.Spec.Service.Service)
	explicitJob := strings.TrimSpace(slo.Spec.Service.Job)
	if targetNamespace == "" {
		targetNamespace = defaultNamespace
	}

	if explicitService != "" {
		if namespaceExcluded(targetNamespace, slo.Spec.ExcludedNamespaces) {
			return nil, nil
		}
		return []sloTarget{{
			namespace: targetNamespace,
			service:   explicitService,
			job:       defaultString(explicitJob, explicitService),
		}}, nil
	}

	var services corev1.ServiceList
	opts := []client.ListOption{}
	if targetNamespace != "" {
		opts = append(opts, client.InNamespace(targetNamespace))
	}
	if len(slo.Spec.Service.MatchLabels) > 0 {
		opts = append(opts, client.MatchingLabels(slo.Spec.Service.MatchLabels))
	}
	if err := r.List(ctx, &services, opts...); err != nil {
		return nil, err
	}

	targets := make([]sloTarget, 0, len(services.Items))
	for i := range services.Items {
		svc := &services.Items[i]
		if svc.Name == "kubernetes" {
			continue
		}
		if namespaceExcluded(svc.Namespace, slo.Spec.ExcludedNamespaces) {
			continue
		}
		targets = append(targets, sloTarget{
			namespace: svc.Namespace,
			service:   svc.Name,
			job:       defaultString(explicitJob, inferServiceJob(svc)),
			labels:    copyLabels(svc.Labels),
		})
	}
	return targets, nil
}

func namespaceExcluded(namespace string, excluded []string) bool {
	namespace = strings.TrimSpace(namespace)
	for _, item := range excluded {
		if namespace == strings.TrimSpace(item) {
			return true
		}
	}
	return false
}

func inferServiceJob(svc *corev1.Service) string {
	if svc == nil {
		return ""
	}
	for _, candidate := range []string{
		svc.Labels["job"],
		svc.Labels["app.kubernetes.io/name"],
		svc.Labels["app"],
		svc.Name,
	} {
		if strings.TrimSpace(candidate) != "" {
			return strings.TrimSpace(candidate)
		}
	}
	return ""
}

func copyLabels(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(fallback)
}

func promScalar(resp map[string]any) float64 {
	data, _ := resp["data"].(map[string]any)
	res, _ := data["result"].([]any)
	if len(res) == 0 {
		return 0
	}
	item, _ := res[0].(map[string]any)
	val, _ := item["value"].([]any)
	if len(val) < 2 {
		return 0
	}
	s, _ := val[1].(string)
	var f float64
	if _, err := fmt.Sscanf(s, "%f", &f); err != nil {
		return 0
	}
	return f
}

func fingerprint(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
