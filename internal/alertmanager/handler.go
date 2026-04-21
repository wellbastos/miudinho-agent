package alertmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	sre "github.com/wellbastos/miudinho-agent/api/v1alpha1"
	appmetrics "github.com/wellbastos/miudinho-agent/internal/metrics"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Handler struct {
	client client.Client
}

type webhookPayload struct {
	Alerts []webhookAlert `json:"alerts"`
}

type webhookAlert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     string            `json:"startsAt"`
	EndsAt       string            `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

func NewHandler(c client.Client) *Handler {
	return &Handler{client: c}
}

func (h *Handler) HandleAlerts(w http.ResponseWriter, r *http.Request) {
	startedAt := time.Now()
	result := "accepted"
	defer func() {
		appmetrics.RecordAlertWebhookRequest(result, time.Since(startedAt))
	}()

	if r.Method != http.MethodPost {
		result = "method_not_allowed"
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload webhookPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		result = "bad_request"
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	var failed []string
	for _, alert := range payload.Alerts {
		if err := h.upsertIncident(r.Context(), alert); err != nil {
			failed = append(failed, err.Error())
		}
	}

	if len(failed) > 0 {
		result = "partial_error"
		http.Error(w, fmt.Sprintf("failed to persist %d alerts", len(failed)), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) upsertIncident(ctx context.Context, alert webhookAlert) error {
	if h.client == nil {
		return fmt.Errorf("kubernetes client is not configured")
	}

	ns := firstNonEmpty(
		alert.Labels["namespace"],
		alert.Labels["kubernetes_namespace"],
		alert.Labels["k8s.namespace.name"],
		"default",
	)
	service := firstNonEmpty(alert.Labels["service"], alert.Labels["app"], alert.Labels["job"])
	job := alert.Labels["job"]
	pod := firstNonEmpty(alert.Labels["pod"], alert.Labels["pod_name"])
	deployment := firstNonEmpty(alert.Labels["deployment"], alert.Labels["app_kubernetes_io_name"])

	fp := alert.Fingerprint
	if fp == "" {
		fp = fingerprintForAlert(alert)
	}

	name := incidentNameForFingerprint(fp)
	desired := &sre.PredictiveIncident{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				"sre.o11y.io/source":      string(sre.SourceAlertmanager),
				"sre.o11y.io/fingerprint": fp,
			},
		},
		Spec: sre.PredictiveIncidentSpec{
			Source:      sre.SourceAlertmanager,
			Fingerprint: fp,
			Severity:    alert.Labels["severity"],
			Title:       firstNonEmpty(alert.Annotations["summary"], alert.Labels["alertname"], "Alertmanager incident"),
			Description: firstNonEmpty(alert.Annotations["description"], alert.Annotations["message"], alert.Labels["alertname"]),
			Identity: sre.IncidentIdentity{
				Namespace:  ns,
				Service:    service,
				Job:        job,
				Pod:        pod,
				Deployment: deployment,
			},
			Alert: map[string]any{
				"status":       alert.Status,
				"labels":       alert.Labels,
				"annotations":  alert.Annotations,
				"startsAt":     alert.StartsAt,
				"endsAt":       alert.EndsAt,
				"generatorURL": alert.GeneratorURL,
			},
		},
	}

	current := &sre.PredictiveIncident{}
	err := h.client.Get(ctx, client.ObjectKey{Name: name, Namespace: ns}, current)
	if apierrors.IsNotFound(err) {
		if err := h.client.Create(ctx, desired); err != nil {
			return err
		}
		return h.syncResolvedStatus(ctx, client.ObjectKey{Name: name, Namespace: ns}, alert, desired.Spec)
	}
	if err != nil {
		return err
	}

	current.Spec = desired.Spec
	if current.Labels == nil {
		current.Labels = map[string]string{}
	}
	for key, value := range desired.Labels {
		current.Labels[key] = value
	}
	if err := h.client.Update(ctx, current); err != nil {
		return err
	}
	return h.syncResolvedStatus(ctx, client.ObjectKey{Name: name, Namespace: ns}, alert, current.Spec)
}

func (h *Handler) syncResolvedStatus(ctx context.Context, key client.ObjectKey, alert webhookAlert, spec sre.PredictiveIncidentSpec) error {
	if !isResolvedAlert(alert.Status) {
		return nil
	}

	current := &sre.PredictiveIncident{}
	if err := h.client.Get(ctx, key, current); err != nil {
		return err
	}
	if current.Status.Phase == sre.PhaseResolved {
		return nil
	}

	current.Status.Phase = sre.PhaseResolved
	current.Status.LastUpdateTime = time.Now().UTC().Format(time.RFC3339)
	if err := h.client.Status().Update(ctx, current); err != nil {
		return err
	}

	appmetrics.RecordResolvedAlert(string(spec.Source), spec.Identity.Namespace, spec.Identity.Service, spec.Severity)
	return nil
}

func isResolvedAlert(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), "resolved")
}

func incidentNameForFingerprint(fp string) string {
	fp = strings.TrimSpace(fp)
	if fp == "" {
		fp = fingerprintForAlert(webhookAlert{})
	}
	if len(fp) > 12 {
		fp = fp[:12]
	}
	return "pi-am-" + fp
}

func fingerprintForAlert(alert webhookAlert) string {
	base := strings.Join([]string{
		alert.Labels["alertname"],
		alert.Labels["namespace"],
		alert.Labels["service"],
		alert.Labels["job"],
		alert.StartsAt,
	}, "|")
	if strings.Trim(base, "|") == "" {
		base = time.Now().UTC().Format(time.RFC3339Nano)
	}
	sum := sha256.Sum256([]byte(base))
	return hex.EncodeToString(sum[:])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
