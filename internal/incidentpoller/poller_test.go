package incidentpoller

import (
	"context"
	"testing"

	sre "github.com/wellbastos/miudinho-agent/api/v1alpha1"
	"github.com/wellbastos/miudinho-agent/internal/incidents"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPollerCreatesIncidentFromPrometheus(t *testing.T) {
	poller, cl := newPollerForTest(t, stubAlertSource{name: "prometheus", alerts: []incidents.ObservedAlert{
		{
			Source: "prometheus",
			Status: "firing",
			Labels: map[string]string{
				"alertname": "HighErrorRate",
				"namespace": "default",
				"service":   "checkout",
				"job":       "checkout",
				"severity":  "critical",
			},
		},
	}})

	if err := poller.Sync(context.Background()); err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}

	var list sre.PredictiveIncidentList
	if err := cl.List(context.Background(), &list); err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected 1 incident, got %d", len(list.Items))
	}
	if got := list.Items[0].Spec.Alert["observed_source"]; got != "prometheus" {
		t.Fatalf("expected prometheus observed source, got %#v", got)
	}
}

func TestPollerCreatesIncidentFromAlertmanagerAPI(t *testing.T) {
	poller, cl := newPollerForTest(t, stubAlertSource{name: "alertmanager", alerts: []incidents.ObservedAlert{
		{
			Source:      "alertmanager",
			Status:      "active",
			Fingerprint: "abc123abc123",
			Labels: map[string]string{
				"alertname": "PodCrashLooping",
				"namespace": "default",
				"service":   "payments",
			},
		},
	}})

	if err := poller.Sync(context.Background()); err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}

	got := &sre.PredictiveIncident{}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "pi-am-abc123abc123", Namespace: "default"}, got); err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.Spec.Identity.Service != "payments" {
		t.Fatalf("expected payments service, got %q", got.Spec.Identity.Service)
	}
}

func TestPollerDeduplicatesAcrossSources(t *testing.T) {
	alert := incidents.ObservedAlert{
		Status:      "firing",
		Fingerprint: "same123same123",
		Labels: map[string]string{
			"alertname": "HighLatency",
			"namespace": "default",
			"service":   "api",
		},
	}
	poller, cl := newPollerForTest(t,
		stubAlertSource{name: "prometheus", alerts: []incidents.ObservedAlert{{Source: "prometheus", Status: alert.Status, Fingerprint: alert.Fingerprint, Labels: alert.Labels}}},
		stubAlertSource{name: "alertmanager", alerts: []incidents.ObservedAlert{{Source: "alertmanager", Status: alert.Status, Fingerprint: alert.Fingerprint, Labels: alert.Labels}}},
	)

	if err := poller.Sync(context.Background()); err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}

	var list sre.PredictiveIncidentList
	if err := cl.List(context.Background(), &list); err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected 1 deduplicated incident, got %d", len(list.Items))
	}
}

func TestPollerResolvesMissingAlert(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sre.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}

	existing := &sre.PredictiveIncident{
		ObjectMeta: metav1.ObjectMeta{Name: "pi-am-abc123abc123", Namespace: "default"},
		Spec: sre.PredictiveIncidentSpec{
			Source:      sre.SourceAlertmanager,
			Fingerprint: "abc123abc123",
			Identity:    sre.IncidentIdentity{Namespace: "default", Service: "checkout"},
			Alert: map[string]any{
				"managed_by": "poller",
			},
		},
		Status: sre.PredictiveIncidentStatus{
			Phase: sre.PhaseEnriched,
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&sre.PredictiveIncident{}).
		WithObjects(existing).
		Build()

	poller := &Poller{client: cl, sources: []AlertSource{stubAlertSource{name: "prometheus"}}}
	if err := poller.Sync(context.Background()); err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}

	got := &sre.PredictiveIncident{}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(existing), got); err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.Status.Phase != sre.PhaseResolved {
		t.Fatalf("expected resolved phase, got %s", got.Status.Phase)
	}
}

func newPollerForTest(t *testing.T, sources ...AlertSource) (*Poller, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := sre.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&sre.PredictiveIncident{}).
		Build()
	return &Poller{client: cl, interval: 30, sources: sources}, cl
}

type stubAlertSource struct {
	name   string
	alerts []incidents.ObservedAlert
	err    error
}

func (s stubAlertSource) Name() string { return s.name }

func (s stubAlertSource) ListAlerts(context.Context) ([]incidents.ObservedAlert, error) {
	return s.alerts, s.err
}
