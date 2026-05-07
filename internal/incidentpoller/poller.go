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
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type AlertSource interface {
	Name() string
	ListAlerts(ctx context.Context) ([]incidents.ObservedAlert, error)
}

type Poller struct {
	client   client.Client
	interval time.Duration
	sources  []AlertSource
}

func New(c client.Client, cfg config.AppConfig) *Poller {
	sources := make([]AlertSource, 0, 2)
	if sourceEnabled(cfg.AlertPolling.Sources, "prometheus") && strings.TrimSpace(cfg.Observability.PromURL) != "" {
		sources = append(sources, NewPrometheusSource(cfg.Observability.PromURL))
	}
	if sourceEnabled(cfg.AlertPolling.Sources, "alertmanager") && strings.TrimSpace(cfg.Observability.AlertmanagerAPIURL) != "" {
		sources = append(sources, NewAlertmanagerSource(cfg.Observability.AlertmanagerAPIURL))
	}
	return &Poller{
		client:   c,
		interval: cfg.AlertPolling.Interval,
		sources:  sources,
	}
}

func (p *Poller) Start(ctx context.Context) error {
	if len(p.sources) == 0 {
		<-ctx.Done()
		return nil
	}

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	_ = p.Sync(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			_ = p.Sync(ctx)
		}
	}
}

func (p *Poller) NeedLeaderElection() bool {
	return false
}

func (p *Poller) Sync(ctx context.Context) error {
	active := map[string]incidents.ObservedAlert{}
	deduplicated := 0

	for _, source := range p.sources {
		startedAt := time.Now()
		alerts, err := source.ListAlerts(ctx)
		result := "success"
		if err != nil {
			result = "error"
			appmetrics.RecordAlertPoll(source.Name(), result, time.Since(startedAt), 0)
			return err
		}
		appmetrics.RecordAlertPoll(source.Name(), result, time.Since(startedAt), len(alerts))
		for _, alert := range alerts {
			fp := incidents.CanonicalFingerprint(alert)
			if existing, ok := active[fp]; ok {
				active[fp] = mergeObservedAlerts(existing, alert)
				deduplicated++
				continue
			}
			active[fp] = alert
		}
	}

	appmetrics.RecordAlertDeduplicated(deduplicated)

	for _, alert := range active {
		_, created, err := incidents.UpsertObservedAlert(ctx, p.client, alert, "poller")
		if err != nil {
			return err
		}
		if created {
			appmetrics.RecordPolledIncidentSync("created")
		} else {
			appmetrics.RecordPolledIncidentSync("updated")
		}
	}

	return p.resolveMissing(ctx, active)
}

func (p *Poller) resolveMissing(ctx context.Context, active map[string]incidents.ObservedAlert) error {
	var list sre.PredictiveIncidentList
	if err := p.client.List(ctx, &list); err != nil {
		return err
	}

	for i := range list.Items {
		pi := &list.Items[i]
		if pi.Spec.Source != sre.SourceAlertmanager || !incidents.IsPollerManaged(pi) {
			continue
		}
		if _, ok := active[pi.Spec.Fingerprint]; ok {
			continue
		}
		if err := incidents.ResolveObservedIncident(ctx, p.client, client.ObjectKeyFromObject(pi), "poller"); err != nil {
			return err
		}
		appmetrics.RecordPolledIncidentSync("resolved")
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/api/v1/alerts", nil)
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
		return nil, fmt.Errorf("prometheus alerts failed: status=%d", resp.StatusCode)
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
