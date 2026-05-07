package incidents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	sre "github.com/wellbastos/miudinho-agent/api/v1alpha1"
	appmetrics "github.com/wellbastos/miudinho-agent/internal/metrics"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ObservedAlert struct {
	Source       string
	Status       string
	Labels       map[string]string
	Annotations  map[string]string
	StartsAt     string
	EndsAt       string
	GeneratorURL string
	Fingerprint  string
}

func CanonicalFingerprint(alert ObservedAlert) string {
	fp := strings.TrimSpace(alert.Fingerprint)
	if fp != "" {
		return fp
	}
	base := strings.Join([]string{
		alert.Labels["alertname"],
		firstNonEmpty(alert.Labels["namespace"], alert.Labels["kubernetes_namespace"], alert.Labels["k8s.namespace.name"]),
		firstNonEmpty(alert.Labels["service"], alert.Labels["app"], alert.Labels["job"]),
		alert.Labels["job"],
		firstNonEmpty(alert.Labels["pod"], alert.Labels["pod_name"]),
		firstNonEmpty(alert.Labels["deployment"], alert.Labels["app_kubernetes_io_name"]),
	}, "|")
	if strings.Trim(base, "|") == "" {
		base = time.Now().UTC().Format(time.RFC3339Nano)
	}
	sum := sha256.Sum256([]byte(base))
	return hex.EncodeToString(sum[:])
}

func IncidentNameForFingerprint(fp string) string {
	fp = strings.TrimSpace(fp)
	if fp == "" {
		fp = CanonicalFingerprint(ObservedAlert{})
	}
	if len(fp) > 12 {
		fp = fp[:12]
	}
	return "pi-am-" + fp
}

func UpsertObservedAlert(ctx context.Context, c client.Client, alert ObservedAlert, managedBy string) (client.ObjectKey, bool, error) {
	if c == nil {
		return client.ObjectKey{}, false, fmt.Errorf("kubernetes client is not configured")
	}

	ns := firstNonEmpty(
		alert.Labels["namespace"],
		alert.Labels["kubernetes_namespace"],
		alert.Labels["k8s.namespace.name"],
		"default",
	)
	service := firstNonEmpty(alert.Labels["service"], alert.Labels["app"], alert.Labels["job"])
	job := alert.Labels["job"]
	if job == "" {
		job = service
	}
	pod := firstNonEmpty(alert.Labels["pod"], alert.Labels["pod_name"])
	deployment := firstNonEmpty(alert.Labels["deployment"], alert.Labels["app_kubernetes_io_name"])
	fp := CanonicalFingerprint(alert)
	name := IncidentNameForFingerprint(fp)
	now := time.Now().UTC().Format(time.RFC3339)

	desired := &sre.PredictiveIncident{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				"miudinho.o11y.io/source":      string(sre.SourceAlertmanager),
				"miudinho.o11y.io/fingerprint": fp,
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
				"status":            alert.Status,
				"labels":            alert.Labels,
				"annotations":       alert.Annotations,
				"startsAt":          alert.StartsAt,
				"endsAt":            alert.EndsAt,
				"generatorURL":      alert.GeneratorURL,
				"observed_source":   alert.Source,
				"observed_status":   alert.Status,
				"lastObservedTime":  now,
				"managed_by":        managedBy,
				"resolvedByPolling": managedBy == "poller",
			},
		},
	}

	current := &sre.PredictiveIncident{}
	key := client.ObjectKey{Name: name, Namespace: ns}
	err := c.Get(ctx, key, current)
	if apierrors.IsNotFound(err) {
		if err := c.Create(ctx, desired); err != nil {
			return client.ObjectKey{}, false, err
		}
		return key, true, syncResolvedStatus(ctx, c, key, alert, desired.Spec)
	}
	if err != nil {
		return client.ObjectKey{}, false, err
	}

	current.Spec = desired.Spec
	if current.Labels == nil {
		current.Labels = map[string]string{}
	}
	for key, value := range desired.Labels {
		current.Labels[key] = value
	}
	if err := c.Update(ctx, current); err != nil {
		return client.ObjectKey{}, false, err
	}
	return key, false, syncResolvedStatus(ctx, c, key, alert, current.Spec)
}

func ResolveObservedIncident(ctx context.Context, c client.Client, key client.ObjectKey, source string) error {
	current := &sre.PredictiveIncident{}
	if err := c.Get(ctx, key, current); err != nil {
		return err
	}
	if current.Status.Phase == sre.PhaseResolved {
		return nil
	}
	if current.Spec.Alert == nil {
		current.Spec.Alert = map[string]any{}
	}
	current.Spec.Alert["observed_source"] = source
	current.Spec.Alert["observed_status"] = "resolved"
	current.Spec.Alert["lastObservedTime"] = time.Now().UTC().Format(time.RFC3339)
	if err := c.Update(ctx, current); err != nil {
		return err
	}
	return syncResolvedStatus(ctx, c, key, ObservedAlert{Status: "resolved"}, current.Spec)
}

func IsPollerManaged(pi *sre.PredictiveIncident) bool {
	if pi == nil {
		return false
	}
	if pi.Spec.Alert == nil {
		return false
	}
	managedBy, _ := pi.Spec.Alert["managed_by"].(string)
	return managedBy == "poller"
}

func syncResolvedStatus(ctx context.Context, c client.Client, key client.ObjectKey, alert ObservedAlert, spec sre.PredictiveIncidentSpec) error {
	if !isResolvedAlert(alert.Status) {
		return nil
	}

	current := &sre.PredictiveIncident{}
	if err := c.Get(ctx, key, current); err != nil {
		return err
	}
	if current.Status.Phase == sre.PhaseResolved {
		return nil
	}

	current.Status.Phase = sre.PhaseResolved
	current.Status.LastUpdateTime = time.Now().UTC().Format(time.RFC3339)
	if err := c.Status().Update(ctx, current); err != nil {
		return err
	}

	appmetrics.RecordResolvedAlert(string(spec.Source), spec.Identity.Namespace, spec.Identity.Service, spec.Severity)
	return nil
}

func isResolvedAlert(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), "resolved")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
