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

	alertPollRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "miudinho_agent_alert_poll_requests_total",
			Help: "Total de polls de alertas executados por fonte.",
		},
		[]string{"source", "result"},
	)

	alertPollRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "miudinho_agent_alert_poll_request_duration_seconds",
			Help:    "Duracao dos polls de alertas por fonte.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"source", "result"},
	)

	alertsCollectedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "miudinho_agent_alerts_collected_total",
			Help: "Total de alertas coletados por fonte.",
		},
		[]string{"source"},
	)

	alertsDeduplicatedTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "miudinho_agent_alerts_deduplicated_total",
			Help: "Total de alertas descartados por deduplicacao entre fontes.",
		},
	)

	polledIncidentsSyncedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "miudinho_agent_polled_incidents_synced_total",
			Help: "Total de incidentes sincronizados pelo poller por resultado.",
		},
		[]string{"result"},
	)

	escalationNotificationsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "miudinho_agent_escalation_notifications_total",
			Help: "Total de notificacoes de escalacao enviadas por destino.",
		},
		[]string{"target", "result"},
	)

	llmRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "miudinho_agent_llm_requests_total",
			Help: "Total de requisicoes enviadas aos provedores LLM.",
		},
		[]string{"provider", "operation", "result"},
	)

	llmRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "miudinho_agent_llm_request_duration_seconds",
			Help:    "Duracao das requisicoes aos provedores LLM.",
			Buckets: []float64{0.5, 1, 2, 5, 10, 15, 20, 30},
		},
		[]string{"provider", "operation"},
	)

	llmConfidence = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "miudinho_agent_llm_confidence",
			Help:    "Distribuicao de confidence retornado pelo LLM nas decisoes.",
			Buckets: []float64{0.0, 0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0},
		},
		[]string{"provider"},
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
			alertPollRequestsTotal,
			alertPollRequestDuration,
			alertsCollectedTotal,
			alertsDeduplicatedTotal,
			polledIncidentsSyncedTotal,
			escalationNotificationsTotal,
			llmRequestsTotal,
			llmRequestDuration,
			llmConfidence,
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

func RecordAlertPoll(source, result string, duration time.Duration, collected int) {
	register()
	alertPollRequestsTotal.WithLabelValues(sanitize(source, "unknown"), sanitize(result, "success")).Inc()
	alertPollRequestDuration.WithLabelValues(sanitize(source, "unknown"), sanitize(result, "success")).Observe(duration.Seconds())
	if collected > 0 {
		alertsCollectedTotal.WithLabelValues(sanitize(source, "unknown")).Add(float64(collected))
	}
}

func RecordAlertDeduplicated(count int) {
	register()
	if count > 0 {
		alertsDeduplicatedTotal.Add(float64(count))
	}
}

func RecordPolledIncidentSync(result string) {
	register()
	polledIncidentsSyncedTotal.WithLabelValues(sanitize(result, "updated")).Inc()
}

func RecordEscalationNotification(target, result string) {
	register()
	escalationNotificationsTotal.WithLabelValues(sanitize(target, "unknown"), sanitize(result, "success")).Inc()
}

func RecordLLMRequest(provider, operation, result string, duration time.Duration) {
	register()
	llmRequestsTotal.WithLabelValues(
		sanitize(provider, "unknown"),
		sanitize(operation, "decide"),
		sanitize(result, "success"),
	).Inc()
	llmRequestDuration.WithLabelValues(
		sanitize(provider, "unknown"),
		sanitize(operation, "decide"),
	).Observe(duration.Seconds())
}

func RecordLLMConfidence(provider string, confidence float64) {
	register()
	llmConfidence.WithLabelValues(sanitize(provider, "unknown")).Observe(confidence)
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
