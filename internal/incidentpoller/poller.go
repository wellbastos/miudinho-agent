package incidentpoller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	sre "github.com/wellbastos/miudinho-agent/api/v1alpha1"
	"github.com/wellbastos/miudinho-agent/internal/config"
	"github.com/wellbastos/miudinho-agent/internal/incidents"
	appmetrics "github.com/wellbastos/miudinho-agent/internal/metrics"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type AlertSource interface {
	Name() string
	ListAlerts(ctx context.Context) ([]incidents.ObservedAlert, error)
}

type Poller struct {
	client     client.Client
	interval   time.Duration
	sources    []AlertSource
	ignoredNS  map[string]bool
}

func New(c client.Client, cfg config.AppConfig) *Poller {
	log := ctrl.Log.WithName("incidentpoller")
	sources := make([]AlertSource, 0, 2)
	if sourceEnabled(cfg.AlertPolling.Sources, "prometheus") && strings.TrimSpace(cfg.Observability.PromURL) != "" {
		log.Info("prometheus polling enabled", "url", cfg.Observability.PromURL)
		sources = append(sources, NewPrometheusSource(cfg.Observability.PromURL))
	} else {
		log.Info("prometheus polling disabled", "sourceInConfig", sourceEnabled(cfg.AlertPolling.Sources, "prometheus"), "promUrl", cfg.Observability.PromURL)
	}
	if sourceEnabled(cfg.AlertPolling.Sources, "alertmanager") && strings.TrimSpace(cfg.Observability.AlertmanagerAPIURL) != "" {
		log.Info("alertmanager polling enabled", "url", cfg.Observability.AlertmanagerAPIURL)
		sources = append(sources, NewAlertmanagerSource(cfg.Observability.AlertmanagerAPIURL))
	} else {
		log.Info("alertmanager polling disabled", "sourceInConfig", sourceEnabled(cfg.AlertPolling.Sources, "alertmanager"), "alertmanagerApiUrl", cfg.Observability.AlertmanagerAPIURL)
	}
	ignoredNS := incidents.IgnoredNamespaceSet(cfg.AlertPolling.IgnoredNamespaces)
	if len(ignoredNS) > 0 {
		log.Info("namespace filter configured", "ignoredNamespaces", cfg.AlertPolling.IgnoredNamespaces)
	}
	return &Poller{
		client:    c,
		interval:  cfg.AlertPolling.Interval,
		sources:   sources,
		ignoredNS: ignoredNS,
	}
}

func (p *Poller) Start(ctx context.Context) error {
	log := ctrl.Log.WithName("incidentpoller")
	if len(p.sources) == 0 {
		log.Info("no alert sources configured, poller is inactive")
		<-ctx.Done()
		return nil
	}

	log.Info("starting alert poller", "sources", len(p.sources), "interval", p.interval)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	if err := p.Sync(ctx); err != nil {
		log.Error(err, "initial poll sync failed")
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := p.Sync(ctx); err != nil {
				log.Error(err, "poll sync failed")
			}
		}
	}
}

func (p *Poller) NeedLeaderElection() bool {
	return false
}

func (p *Poller) Sync(ctx context.Context) error {
	log := ctrl.Log.WithName("incidentpoller")
	active := map[string]incidents.ObservedAlert{}
	deduplicated := 0

	var pollErrors []error
	for _, source := range p.sources {
		startedAt := time.Now()
		alerts, err := source.ListAlerts(ctx)
		result := "success"
		if err != nil {
			result = "error"
			appmetrics.RecordAlertPoll(source.Name(), result, time.Since(startedAt), 0)
			log.Error(err, "failed to list alerts from source", "source", source.Name())
			pollErrors = append(pollErrors, fmt.Errorf("source %s: %w", source.Name(), err))
			continue
		}
		filtered := 0
		log.Info("polled alerts from source", "source", source.Name(), "count", len(alerts))
		appmetrics.RecordAlertPoll(source.Name(), result, time.Since(startedAt), len(alerts))
		for _, alert := range alerts {
			if incidents.IsIgnoredAlert(alert.Labels, p.ignoredNS) {
				filtered++
				continue
			}
			fp := incidents.CanonicalFingerprint(alert)
			if existing, ok := active[fp]; ok {
				active[fp] = mergeObservedAlerts(existing, alert)
				deduplicated++
				continue
			}
			active[fp] = alert
		}
		if filtered > 0 {
			log.V(1).Info("alerts filtered by namespace", "source", source.Name(), "filtered", filtered)
		}
	}

	if len(pollErrors) == len(p.sources) {
		return fmt.Errorf("all alert sources failed: %v", pollErrors)
	}

	appmetrics.RecordAlertDeduplicated(deduplicated)

	created := 0
	updated := 0
	upsertErrors := 0
	for _, alert := range active {
		_, wasCreated, err := incidents.UpsertObservedAlert(ctx, p.client, alert, "poller")
		if err != nil {
			upsertErrors++
			log.Error(err, "failed to upsert incident — skipping this alert",
				"alertname", alert.Labels["alertname"],
				"namespace", alert.Labels["namespace"],
			)
			appmetrics.RecordPolledIncidentSync("error")
			continue // Continua processando os outros alertas
		}
		if wasCreated {
			created++
			log.Info("incident created from poll", "alertname", alert.Labels["alertname"], "namespace", alert.Labels["namespace"], "source", alert.Source)
			appmetrics.RecordPolledIncidentSync("created")
		} else {
			updated++
			appmetrics.RecordPolledIncidentSync("updated")
		}
	}

	if created > 0 || updated > 0 || deduplicated > 0 || upsertErrors > 0 {
		log.Info("sync complete", "active", len(active), "created", created, "updated", updated, "deduplicated", deduplicated, "errors", upsertErrors)
	}

	return p.resolveMissing(ctx, active)
}

func (p *Poller) resolveMissing(ctx context.Context, active map[string]incidents.ObservedAlert) error {
	log := ctrl.Log.WithName("incidentpoller")
	var list sre.PredictiveIncidentList
	if err := p.client.List(ctx, &list); err != nil {
		log.Error(err, "failed to list predictive incidents for resolve-missing check")
		return err
	}

	var resolveErrors int
	for i := range list.Items {
		pi := &list.Items[i]
		if pi.Spec.Source != sre.SourceAlertmanager || !incidents.IsPollerManaged(pi) {
			continue
		}
		if _, ok := active[pi.Spec.Fingerprint]; ok {
			continue
		}
		log.Info("resolving incident no longer in active alerts", "incident", pi.Name, "namespace", pi.Namespace)
		if err := incidents.ResolveObservedIncident(ctx, p.client, client.ObjectKeyFromObject(pi), "poller"); err != nil {
			resolveErrors++
			log.Error(err, "failed to resolve incident — skipping", "incident", pi.Name)
			appmetrics.RecordPolledIncidentSync("error")
			continue // Continua tentando resolver os outros incidents
		}
		appmetrics.RecordPolledIncidentSync("resolved")
	}
	if resolveErrors > 0 {
		log.Info("resolve-missing completed with errors", "errors", resolveErrors)
	}
	return nil
}

func sourceEnabled(sources []string, target string) bool {
	for _, source := range sources {
		if strings.EqualFold(strings.TrimSpace(source), target) {
			return true
		}
	}
	return false
}

func mergeObservedAlerts(current, incoming incidents.ObservedAlert) incidents.ObservedAlert {
	merged := current
	if merged.Source == "" || merged.Source == "prometheus" {
		merged.Source = incoming.Source
	}
	if merged.Status == "" || strings.EqualFold(merged.Status, "pending") {
		merged.Status = incoming.Status
	}
	if merged.StartsAt == "" {
		merged.StartsAt = incoming.StartsAt
	}
	if merged.EndsAt == "" {
		merged.EndsAt = incoming.EndsAt
	}
	if merged.GeneratorURL == "" {
		merged.GeneratorURL = incoming.GeneratorURL
	}
	if merged.Fingerprint == "" {
		merged.Fingerprint = incoming.Fingerprint
	}
	merged.Labels = mergeStringMaps(merged.Labels, incoming.Labels)
	merged.Annotations = mergeStringMaps(merged.Annotations, incoming.Annotations)

	sources := []string{current.Source}
	if incoming.Source != "" && !strings.EqualFold(current.Source, incoming.Source) {
		sources = append(sources, incoming.Source)
	}
	if len(sources) > 1 {
		if merged.Annotations == nil {
			merged.Annotations = map[string]string{}
		}
		merged.Annotations["miudinho_sources"] = strings.Join(sources, ",")
	}
	return merged
}

func mergeStringMaps(left, right map[string]string) map[string]string {
	if len(left) == 0 && len(right) == 0 {
		return nil
	}
	out := map[string]string{}
	for key, value := range left {
		out[key] = value
	}
	for key, value := range right {
		if _, exists := out[key]; !exists || out[key] == "" {
			out[key] = value
		}
	}
	return out
}

type PrometheusSource struct {
	baseURL string
	client  *http.Client
}

func NewPrometheusSource(baseURL string) *PrometheusSource {
	return &PrometheusSource{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		client:  &http.Client{Timeout: 15 * time.Second},
	}
}

func (s *PrometheusSource) Name() string { return "prometheus" }

func (s *PrometheusSource) ListAlerts(ctx context.Context) ([]incidents.ObservedAlert, error) {
	if s.baseURL == "" {
		return nil, nil
	}
	endpoint := s.baseURL + "/api/v1/alerts"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		// Diagnóstico específico para resposta HTTP/2 em conexão HTTP/1.x
		// Indica que o endpoint está na porta gRPC (ex: 10901) ao invés da porta HTTP REST (ex: 10902)
		if strings.Contains(err.Error(), "malformed HTTP response") {
			return nil, fmt.Errorf("prometheus endpoint %s parece ser gRPC/HTTP2 — verifique se a porta está correta (porta HTTP REST, não gRPC): %w", endpoint, err)
		}
		return nil, fmt.Errorf("prometheus request to %s failed: %w", endpoint, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("prometheus alerts endpoint %s returned status=%d", endpoint, resp.StatusCode)
	}

	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Alerts []struct {
				State       string            `json:"state"`
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
				ActiveAt    string            `json:"activeAt"`
				Value       string            `json:"value"`
			} `json:"alerts"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	out := make([]incidents.ObservedAlert, 0, len(payload.Data.Alerts))
	for _, alert := range payload.Data.Alerts {
		if strings.EqualFold(alert.State, "inactive") || strings.EqualFold(alert.State, "resolved") {
			continue
		}
		out = append(out, incidents.ObservedAlert{
			Source:      "prometheus",
			Status:      alert.State,
			Labels:      alert.Labels,
			Annotations: alert.Annotations,
			StartsAt:    alert.ActiveAt,
		})
	}
	return out, nil
}

type AlertmanagerSource struct {
	apiURL string
	client *http.Client
}

func NewAlertmanagerSource(apiURL string) *AlertmanagerSource {
	return &AlertmanagerSource{
		apiURL: strings.TrimSpace(apiURL),
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (s *AlertmanagerSource) Name() string { return "alertmanager" }

func (s *AlertmanagerSource) ListAlerts(ctx context.Context) ([]incidents.ObservedAlert, error) {
	if s.apiURL == "" {
		return nil, nil
	}

	apiURL := s.apiURL
	if parsed, err := url.Parse(apiURL); err == nil && parsed.Path == "" {
		apiURL = strings.TrimRight(apiURL, "/") + "/api/v2/alerts"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("alertmanager api failed: status=%d", resp.StatusCode)
	}

	var payload []struct {
		Annotations  map[string]string `json:"annotations"`
		StartsAt     string            `json:"startsAt"`
		EndsAt       string            `json:"endsAt"`
		GeneratorURL string            `json:"generatorURL"`
		Fingerprint  string            `json:"fingerprint"`
		Labels       map[string]string `json:"labels"`
		Status       struct {
			State string `json:"state"`
		} `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	out := make([]incidents.ObservedAlert, 0, len(payload))
	for _, alert := range payload {
		if strings.EqualFold(alert.Status.State, "resolved") || strings.EqualFold(alert.Status.State, "suppressed") {
			continue
		}
		out = append(out, incidents.ObservedAlert{
			Source:       "alertmanager",
			Status:       alert.Status.State,
			Labels:       alert.Labels,
			Annotations:  alert.Annotations,
			StartsAt:     alert.StartsAt,
			EndsAt:       alert.EndsAt,
			GeneratorURL: alert.GeneratorURL,
			Fingerprint:  alert.Fingerprint,
		})
	}
	return out, nil
}
