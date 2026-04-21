package metrics

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	registerOnce sync.Once

	reconcileTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "miudinho_agent_reconcile_total",
			Help: "Total de reconciliacoes processadas pelo operator.",
		},
		[]string{"controller", "source", "phase", "result"},
	)

	reconcileDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "miudinho_agent_reconcile_duration_seconds",
			Help:    "Duracao das reconciliacoes do operator.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"controller", "source", "result"},
	)

	alertWebhookRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "miudinho_agent_alertmanager_webhook_requests_total",
			Help: "Total de requests processadas pelo webhook inbound do Alertmanager.",
		},
		[]string{"result"},
	)

	alertWebhookRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "miudinho_agent_alertmanager_webhook_request_duration_seconds",
			Help:    "Duracao das requests do webhook inbound do Alertmanager.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"result"},
	)

	resolvedAlertsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "miudinho_agent_resolved_alerts_total",
			Help: "Total de alertas resolvidos recebidos e persistidos pelo operator.",
		},
		[]string{"source", "namespace", "service", "severity"},
	)
)

func init() {
	register()
}

func register() {
	registerOnce.Do(func() {
		ctrlmetrics.Registry.MustRegister(
			reconcileTotal,
			reconcileDuration,
			alertWebhookRequestsTotal,
			alertWebhookRequestDuration,
			resolvedAlertsTotal,
		)
	})
}

func RecordReconcile(controller, source, phase, result string, duration time.Duration) {
	register()
	reconcileTotal.WithLabelValues(
		sanitize(controller, "unknown"),
		sanitize(source, "unknown"),
		sanitize(phase, "unknown"),
		sanitize(result, "success"),
	).Inc()
	reconcileDuration.WithLabelValues(
		sanitize(controller, "unknown"),
		sanitize(source, "unknown"),
		sanitize(result, "success"),
	).Observe(duration.Seconds())
}

func RecordAlertWebhookRequest(result string, duration time.Duration) {
	register()
	alertWebhookRequestsTotal.WithLabelValues(sanitize(result, "accepted")).Inc()
	alertWebhookRequestDuration.WithLabelValues(sanitize(result, "accepted")).Observe(duration.Seconds())
}

func RecordResolvedAlert(source, namespace, service, severity string) {
	register()
	resolvedAlertsTotal.WithLabelValues(
		sanitize(source, "unknown"),
		sanitize(namespace, "default"),
		sanitize(service, "unknown"),
		sanitize(severity, "unknown"),
	).Inc()
}

func ResolvedAlertsTotalForTest(source, namespace, service, severity string) prometheus.Counter {
	register()
	return resolvedAlertsTotal.WithLabelValues(
		sanitize(source, "unknown"),
		sanitize(namespace, "default"),
		sanitize(service, "unknown"),
		sanitize(severity, "unknown"),
	)
}

func ReconcileDurationMetricForTest(controller, source, result string) prometheus.Observer {
	register()
	return reconcileDuration.WithLabelValues(
		sanitize(controller, "unknown"),
		sanitize(source, "unknown"),
		sanitize(result, "success"),
	)
}

func ReconcileDurationSampleSumForTest(controller, source, result string) float64 {
	register()
	metric := &dto.Metric{}
	observer, ok := ReconcileDurationMetricForTest(controller, source, result).(prometheus.Metric)
	if !ok {
		return 0
	}
	if err := observer.Write(metric); err != nil || metric.Histogram == nil || metric.Histogram.SampleSum == nil {
		return 0
	}
	return *metric.Histogram.SampleSum
}

func sanitize(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
