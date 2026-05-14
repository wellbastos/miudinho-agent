package alertmanager

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	sre "github.com/wellbastos/miudinho-agent/api/v1alpha1"
	"github.com/wellbastos/miudinho-agent/internal/incidents"
	appmetrics "github.com/wellbastos/miudinho-agent/internal/metrics"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestHandleAlertsCreatesIncidents(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sre.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&sre.PredictiveIncident{}).WithObjects().Build()
	handler := NewHandler(cl, nil)

	payload := webhookPayload{
		Alerts: []webhookAlert{
			{
				Status:      "firing",
				Fingerprint: "abc123abc123abc123",
				Labels: map[string]string{
					"alertname": "HighErrorRate",
					"namespace": "default",
					"service":   "checkout",
					"job":       "checkout",
					"severity":  "critical",
				},
				Annotations: map[string]string{
					"summary":     "High error rate",
					"description": "service is failing",
				},
			},
			{
				Status:      "firing",
				Fingerprint: "def456def456def456",
				Labels: map[string]string{
					"alertname": "PodCrashLooping",
					"namespace": "default",
					"service":   "payments",
					"job":       "payments",
				},
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.HandleAlerts(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d", rec.Code)
	}

	list := &sre.PredictiveIncidentList{}
	if err := cl.List(context.Background(), list, client.InNamespace("default")); err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("expected 2 incidents, got %d", len(list.Items))
	}
}

func TestHandleAlertsUpdatesExistingIncident(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sre.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}

	labels := map[string]string{
		"alertname": "HighErrorRate",
		"namespace": "default",
		"service":   "checkout",
		"job":       "checkout",
	}
	incidentName := incidents.IncidentNameForFingerprint(incidents.CanonicalFingerprint(incidents.ObservedAlert{Labels: labels}))

	existing := &sre.PredictiveIncident{
		ObjectMeta: metav1.ObjectMeta{
			Name:      incidentName,
			Namespace: "default",
		},
		Spec: sre.PredictiveIncidentSpec{
			Source:   sre.SourceAlertmanager,
			Identity: sre.IncidentIdentity{Namespace: "default", Service: "checkout"},
			Title:    "old",
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&sre.PredictiveIncident{}).WithObjects(existing).Build()
	handler := NewHandler(cl, nil)

	payload := webhookPayload{
		Alerts: []webhookAlert{{
			Status:      "firing",
			Fingerprint: "abc123abc123999999",
			Labels:      labels,
			Annotations: map[string]string{
				"summary": "new summary",
			},
		}},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.HandleAlerts(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d", rec.Code)
	}

	got := &sre.PredictiveIncident{}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: incidentName, Namespace: "default"}, got); err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.Spec.Title != "new summary" {
		t.Fatalf("expected title update, got %q", got.Spec.Title)
	}
}

func TestIncidentNameForFingerprintHandlesShortAndEmptyValues(t *testing.T) {
	if got := incidents.IncidentNameForFingerprint("abc"); got != "pi-am-abc" {
		t.Fatalf("unexpected short fingerprint name: %q", got)
	}
	if got := incidents.IncidentNameForFingerprint("abcdefghijklmnop"); got != "pi-am-abcdefghijkl" {
		t.Fatalf("unexpected trimmed fingerprint name: %q", got)
	}
	if got := incidents.IncidentNameForFingerprint(""); got == "pi-am-" {
		t.Fatalf("expected generated name for empty fingerprint, got %q", got)
	}
}

func TestHandleAlertsAcceptsShortFingerprint(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sre.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&sre.PredictiveIncident{}).Build()
	handler := NewHandler(cl, nil)

	payload := webhookPayload{
		Alerts: []webhookAlert{{
			Status:      "firing",
			Fingerprint: "abc",
			Labels: map[string]string{
				"alertname": "HighErrorRate",
				"namespace": "default",
			},
		}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.HandleAlerts(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d", rec.Code)
	}
}

func TestHandleAlertsMarksResolvedIncidentAndIncrementsMetric(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sre.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}

	labels := map[string]string{
		"alertname": "HighErrorRate",
		"namespace": "default",
		"service":   "checkout",
		"job":       "checkout",
		"severity":  "critical",
	}
	incidentName := incidents.IncidentNameForFingerprint(incidents.CanonicalFingerprint(incidents.ObservedAlert{Labels: labels}))

	existing := &sre.PredictiveIncident{
		ObjectMeta: metav1.ObjectMeta{
			Name:      incidentName,
			Namespace: "default",
		},
		Spec: sre.PredictiveIncidentSpec{
			Source:   sre.SourceAlertmanager,
			Severity: "critical",
			Identity: sre.IncidentIdentity{Namespace: "default", Service: "checkout"},
		},
		Status: sre.PredictiveIncidentStatus{
			Phase: sre.PhaseEnriched,
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&sre.PredictiveIncident{}).WithObjects(existing).Build()
	handler := NewHandler(cl, nil)

	before := testutil.ToFloat64(appmetrics.ResolvedAlertsTotalForTest("alertmanager", "default", "checkout", "critical"))

	payload := webhookPayload{
		Alerts: []webhookAlert{{
			Status:      "resolved",
			Fingerprint: "abc123abc123999999",
			Labels:      labels,
		}},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.HandleAlerts(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d", rec.Code)
	}

	got := &sre.PredictiveIncident{}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: incidentName, Namespace: "default"}, got); err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.Status.Phase != sre.PhaseResolved {
		t.Fatalf("expected resolved phase, got %s", got.Status.Phase)
	}

	after := testutil.ToFloat64(appmetrics.ResolvedAlertsTotalForTest("alertmanager", "default", "checkout", "critical"))
	if after != before+1 {
		t.Fatalf("expected resolved alert metric increment, got before=%v after=%v", before, after)
	}
}

func TestHandleFakeAlertCreatesSyntheticIncident(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sre.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&sre.PredictiveIncident{}).Build()
	handler := NewHandler(cl, nil)

	body := bytes.NewReader([]byte(`{"namespace":"o11y","service":"checkout","github_repository":"apps-checkout-test"}`))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/test/fake-alert", body)
	rec := httptest.NewRecorder()
	handler.HandleFakeAlert(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d", rec.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["incident_name"] == "" {
		t.Fatalf("expected incident_name in response, got %#v", resp)
	}

	list := &sre.PredictiveIncidentList{}
	if err := cl.List(context.Background(), list, client.InNamespace("o11y")); err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected 1 incident, got %d", len(list.Items))
	}
	got := list.Items[0]
	if got.Spec.Identity.Service != "checkout" {
		t.Fatalf("expected checkout service, got %q", got.Spec.Identity.Service)
	}
	labels, ok := got.Spec.Alert["labels"].(map[string]any)
	if !ok {
		t.Fatalf("expected alert labels map, got %#v", got.Spec.Alert["labels"])
	}
	if labels["github_repository"] != "apps-checkout-test" {
		t.Fatalf("expected github_repository label, got %#v", labels["github_repository"])
	}
}
