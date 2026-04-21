package alertmanager

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sre "github.com/wellbastos/miudinho-agent/api/v1alpha1"
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

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects().Build()
	handler := NewHandler(cl)

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

	existing := &sre.PredictiveIncident{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pi-am-abc123abc123",
			Namespace: "default",
		},
		Spec: sre.PredictiveIncidentSpec{
			Source:   sre.SourceAlertmanager,
			Identity: sre.IncidentIdentity{Namespace: "default", Service: "checkout"},
			Title:    "old",
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	handler := NewHandler(cl)

	payload := webhookPayload{
		Alerts: []webhookAlert{{
			Status:      "firing",
			Fingerprint: "abc123abc123999999",
			Labels: map[string]string{
				"alertname": "HighErrorRate",
				"namespace": "default",
				"service":   "checkout",
				"job":       "checkout",
			},
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
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "pi-am-abc123abc123", Namespace: "default"}, got); err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.Spec.Title != "new summary" {
		t.Fatalf("expected title update, got %q", got.Spec.Title)
	}
}

func TestIncidentNameForFingerprintHandlesShortAndEmptyValues(t *testing.T) {
	if got := incidentNameForFingerprint("abc"); got != "pi-am-abc" {
		t.Fatalf("unexpected short fingerprint name: %q", got)
	}
	if got := incidentNameForFingerprint("abcdefghijklmnop"); got != "pi-am-abcdefghijkl" {
		t.Fatalf("unexpected trimmed fingerprint name: %q", got)
	}
	if got := incidentNameForFingerprint(""); got == "pi-am-" {
		t.Fatalf("expected generated name for empty fingerprint, got %q", got)
	}
}

func TestHandleAlertsAcceptsShortFingerprint(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sre.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()
	handler := NewHandler(cl)

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
