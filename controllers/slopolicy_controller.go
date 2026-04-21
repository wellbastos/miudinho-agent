package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	sre "github.com/wellbastos/miudinho-agent/api/v1alpha1"
	"github.com/wellbastos/miudinho-agent/internal/config"
	"github.com/wellbastos/miudinho-agent/internal/telemetry"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type SLOPolicyReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Config   config.AppConfig
	Recorder record.EventRecorder
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

	prom := telemetry.NewPromClient(r.Config.Observability.PromURL)

	ns := slo.Spec.Service.Namespace
	if ns == "" {
		ns = req.Namespace
	}
	svc := slo.Spec.Service.Service
	job := slo.Spec.Service.Job
	if svc == "" || job == "" {
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

	q5xx := fmt.Sprintf(`sum(rate(http_requests_total{namespace="%s",service="%s",job="%s",status=~"5.."}[5m]))`, ns, svc, job)
	qtot := fmt.Sprintf(`sum(rate(http_requests_total{namespace="%s",service="%s",job="%s"}[5m]))`, ns, svc, job)
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

	if risk {
		fp := fingerprint(fmt.Sprintf("slo|%s|%s|%s|%s", ns, svc, job, slo.Name))
		name := "pi-pred-" + fp[:12]

		pi := &sre.PredictiveIncident{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
				Labels: map[string]string{
					"sre.o11y.io/source":      "predictive",
					"sre.o11y.io/fingerprint": fp,
				},
			},
			Spec: sre.PredictiveIncidentSpec{
				Source:      sre.SourcePredictive,
				Fingerprint: fp,
				Severity:    "warning",
				Title:       "Predictive risk detected",
				Description: "Service shows predictive error trend",
				Identity:    sre.IncidentIdentity{Namespace: ns, Service: svc, Job: job},
				Signals: map[string]any{
					"error_rate_pct": er,
					"five_xx_rps":    rps,
					"five_xx_slope":  slope,
				},
			},
		}

		current := &sre.PredictiveIncident{}
		err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, current)
		if client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
		if err != nil {
			if err := r.Create(ctx, pi); err != nil {
				return ctrl.Result{}, err
			}
		} else {
			current.Spec = pi.Spec
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
