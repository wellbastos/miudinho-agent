package incidents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	sre "github.com/wellbastos/miudinho-agent/api/v1alpha1"
	appmetrics "github.com/wellbastos/miudinho-agent/internal/metrics"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
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

// LabelSafeFingerprint trunca o fingerprint para 63 caracteres — limite máximo de labels Kubernetes.
// O Spec.Fingerprint mantém o hash completo para matching exato; use esta função apenas em labels.
func LabelSafeFingerprint(fp string) string {
	if len(fp) > 63 {
		return fp[:63]
	}
	return fp
}

// labelSafeFingerprint é o alias interno para uso dentro do pacote.
func labelSafeFingerprint(fp string) string { return LabelSafeFingerprint(fp) }

// volatileLabels são campos que mudam quando um pod é reiniciado ou um replicaset
// é rotacionado — não devem fazer parte da identidade do problema.
var volatileLabels = map[string]bool{
	"pod":         true,
	"pod_name":    true,
	"replicaset":  true,
	"replica_set": true,
}

// CanonicalFingerprint calcula um fingerprint estável baseado na *identidade do problema*,
// não na instância do alerta. Campos efêmeros como pod e replicaset são excluídos
// intencionalmente: se o pod é reiniciado (novo nome), o problema é o mesmo.
// O fingerprint do Alertmanager (que inclui todos os labels, inclusive pod) é ignorado.
func CanonicalFingerprint(alert ObservedAlert) string {
	base := strings.Join([]string{
		alert.Labels["alertname"],
		firstNonEmpty(alert.Labels["namespace"], alert.Labels["kubernetes_namespace"], alert.Labels["k8s.namespace.name"]),
		firstNonEmpty(
			alert.Labels["service"],
			alert.Labels["app"],
			alert.Labels["app_kubernetes_io_name"],
			alert.Labels["kubernetes_name"],
			alert.Labels["deployment"],
			alert.Labels["statefulset"],
			alert.Labels["daemonset"],
		),
		firstNonEmpty(alert.Labels["deployment"], alert.Labels["app_kubernetes_io_name"], alert.Labels["kubernetes_name"]),
	}, "|")

	// Quando os campos canônicos estão todos vazios, usa todos os labels disponíveis
	// exceto os voláteis — evita criar incidents duplicados por rotação de pods.
	if strings.Trim(base, "|") == "" {
		keys := make([]string, 0, len(alert.Labels))
		for k := range alert.Labels {
			if !volatileLabels[k] {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+alert.Labels[k])
		}
		base = strings.Join(parts, ",")
		if base == "" {
			base = "unknown-alert"
		}
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
	return UpsertObservedAlertWithIgnore(ctx, c, alert, managedBy, nil)
}

// UpsertObservedAlertWithIgnore é como UpsertObservedAlert mas ignora namespaces na lista.
func UpsertObservedAlertWithIgnore(ctx context.Context, c client.Client, alert ObservedAlert, managedBy string, ignoredNS map[string]bool) (client.ObjectKey, bool, error) {
	if c == nil {
		return client.ObjectKey{}, false, fmt.Errorf("kubernetes client is not configured")
	}

	ns := firstNonEmpty(
		alert.Labels["namespace"],
		alert.Labels["kubernetes_namespace"],
		alert.Labels["k8s.namespace.name"],
		"default",
	)

	// Bloqueia criação de incidents para namespaces na lista de ignorados.
	if len(ignoredNS) > 0 && ignoredNS[ns] {
		return client.ObjectKey{}, false, nil
	}

	// Tenta extrair o nome do serviço/workload em ordem de especificidade.
	// Alertas do kube-state-metrics e do Prometheus Operator usam labels diferentes
	// dependendo do tipo de workload monitorado.
	service := firstNonEmpty(
		alert.Labels["service"],
		alert.Labels["app"],
		alert.Labels["app_kubernetes_io_name"],
		alert.Labels["kubernetes_name"],
		alert.Labels["deployment"],
		alert.Labels["statefulset"],
		alert.Labels["daemonset"],
		alert.Labels["container"],
		alert.Labels["job"],
	)
	job := alert.Labels["job"]
	if job == "" {
		job = service
	}
	pod := firstNonEmpty(alert.Labels["pod"], alert.Labels["pod_name"])
	deployment := firstNonEmpty(alert.Labels["deployment"], alert.Labels["app_kubernetes_io_name"], alert.Labels["kubernetes_name"])
	fp := CanonicalFingerprint(alert)
	name := IncidentNameForFingerprint(fp)
	now := time.Now().UTC().Format(time.RFC3339)

	incidentSource := resolveIncidentSource(alert.Source)
	desired := &sre.PredictiveIncident{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				"miudinho.o11y.io/source":      string(incidentSource),
				"miudinho.o11y.io/fingerprint": labelSafeFingerprint(fp),
			},
		},
		Spec: sre.PredictiveIncidentSpec{
			Source:      incidentSource,
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

	objKey := client.ObjectKey{Name: name, Namespace: ns}

	// Tenta criar primeiro; se o objeto já existir, faz upsert com retry em conflito.
	current := &sre.PredictiveIncident{}
	err := c.Get(ctx, objKey, current)
	if apierrors.IsNotFound(err) {
		if err := c.Create(ctx, desired); err != nil {
			return client.ObjectKey{}, false, err
		}
		return objKey, true, syncResolvedStatus(ctx, c, objKey, alert, desired.Spec)
	}
	if err != nil {
		return client.ObjectKey{}, false, err
	}

	// Objeto já existe — atualiza com retry em caso de conflito (optimistic lock).
	updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &sre.PredictiveIncident{}
		if err := c.Get(ctx, objKey, fresh); err != nil {
			return err
		}
		fresh.Spec = desired.Spec
		if fresh.Labels == nil {
			fresh.Labels = map[string]string{}
		}
		for k, v := range desired.Labels {
			fresh.Labels[k] = v
		}
		return c.Update(ctx, fresh)
	})
	if updateErr != nil {
		return client.ObjectKey{}, false, updateErr
	}
	return objKey, false, syncResolvedStatus(ctx, c, objKey, alert, desired.Spec)
}

func ResolveObservedIncident(ctx context.Context, c client.Client, key client.ObjectKey, source string) error {
	var resolvedSpec sre.PredictiveIncidentSpec

	// Atualiza o Spec com retry em caso de conflito (optimistic lock).
	updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &sre.PredictiveIncident{}
		if err := c.Get(ctx, key, current); err != nil {
			return err
		}
		if current.Status.Phase == sre.PhaseResolved {
			// Já resolvido por outra goroutine — não é erro.
			resolvedSpec = current.Spec
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
		resolvedSpec = current.Spec
		return nil
	})
	if updateErr != nil {
		return updateErr
	}
	return syncResolvedStatus(ctx, c, key, ObservedAlert{Status: "resolved"}, resolvedSpec)
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

	// Atualiza status com retry em caso de conflito (optimistic lock).
	updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &sre.PredictiveIncident{}
		if err := c.Get(ctx, key, current); err != nil {
			return err
		}
		if current.Status.Phase == sre.PhaseResolved {
			return nil
		}
		current.Status.Phase = sre.PhaseResolved
		current.Status.LastUpdateTime = time.Now().UTC().Format(time.RFC3339)
		return c.Status().Update(ctx, current)
	})
	if updateErr != nil {
		return updateErr
	}

	appmetrics.RecordResolvedAlert(string(spec.Source), spec.Identity.Namespace, spec.Identity.Service, spec.Severity)
	return nil
}

func isResolvedAlert(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), "resolved")
}

// resolveIncidentSource mapeia o source observado para o IncidentSource correto do CRD.
func resolveIncidentSource(observedSource string) sre.IncidentSource {
	switch strings.ToLower(strings.TrimSpace(observedSource)) {
	case "prometheus":
		return sre.SourcePrometheus
	case "alertmanager":
		return sre.SourceAlertmanager
	default:
		return sre.SourceAlertmanager
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// AlertNamespace extrai o namespace de um mapa de labels de alerta,
// respeitando os aliases usados por diferentes stacks de observabilidade.
func AlertNamespace(labels map[string]string) string {
	return firstNonEmpty(
		labels["namespace"],
		labels["kubernetes_namespace"],
		labels["k8s.namespace.name"],
	)
}

// IgnoredNamespaceSet converte uma lista de namespaces em um set para lookup O(1).
func IgnoredNamespaceSet(namespaces []string) map[string]bool {
	set := make(map[string]bool, len(namespaces))
	for _, ns := range namespaces {
		ns = strings.TrimSpace(ns)
		if ns != "" {
			set[ns] = true
		}
	}
	return set
}

// IsIgnoredAlert retorna true se o alerta pertence a um namespace que deve ser ignorado.
func IsIgnoredAlert(labels map[string]string, ignoredNS map[string]bool) bool {
	if len(ignoredNS) == 0 {
		return false
	}
	return ignoredNS[AlertNamespace(labels)]
}
