package controllers

import (
	"context"
	"fmt"
	"testing"

	sre "github.com/wellbastos/miudinho-agent/api/v1alpha1"
	"github.com/wellbastos/miudinho-agent/internal/config"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSLOPolicyReconcileDiscoversServicesByLabels(t *testing.T) {
	scheme := runtime.NewScheme()
	mustAddScheme(t, scheme)

	slo := &sre.SLOPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "global-slo", Namespace: "o11y"},
		Spec: sre.SLOPolicySpec{
			Service: sre.SLOServiceRef{
				Namespace:   "apps",
				MatchLabels: map[string]string{"miudinho.o11y.io/enabled": "true"},
			},
			Signals:         sloSignals(1.0, 0.0, 0.1),
			ScheduleSeconds: 120,
		},
	}
	checkout := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "checkout",
			Namespace: "apps",
			Labels: map[string]string{
				"miudinho.o11y.io/enabled": "true",
				"team":                "payments",
			},
		},
	}
	catalog := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "catalog",
			Namespace: "apps",
			Labels: map[string]string{
				"miudinho.o11y.io/enabled": "false",
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&sre.SLOPolicy{}, &sre.PredictiveIncident{}).
		WithObjects(slo, checkout, catalog).
		Build()

	r := &SLOPolicyReconciler{
		Client:  cl,
		Scheme:  scheme,
		Config:  config.AppConfig{},
		PromAPI: fakePromQueryAPI{value: 2.0},
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(slo)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var incidents sre.PredictiveIncidentList
	if err := cl.List(context.Background(), &incidents, client.InNamespace("apps")); err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(incidents.Items) != 1 {
		t.Fatalf("expected 1 predictive incident, got %d", len(incidents.Items))
	}
	got := incidents.Items[0]
	if got.Spec.Identity.Service != "checkout" {
		t.Fatalf("expected checkout incident, got %s", got.Spec.Identity.Service)
	}
	if got.Labels["team"] != "payments" {
		t.Fatalf("expected propagated service label, got %q", got.Labels["team"])
	}
}

func TestSLOPolicyReconcileDefaultsJobToServiceName(t *testing.T) {
	scheme := runtime.NewScheme()
	mustAddScheme(t, scheme)

	slo := &sre.SLOPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "global-slo", Namespace: "o11y"},
		Spec: sre.SLOPolicySpec{
			Service: sre.SLOServiceRef{
				Namespace:   "apps",
				MatchLabels: map[string]string{"app": "checkout"},
			},
			Signals:         sloSignals(1.0, 0.0, 0.1),
			ScheduleSeconds: 120,
		},
	}
	checkout := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "checkout",
			Namespace: "apps",
			Labels:    map[string]string{"app": "checkout"},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&sre.SLOPolicy{}, &sre.PredictiveIncident{}).
		WithObjects(slo, checkout).
		Build()

	r := &SLOPolicyReconciler{
		Client:  cl,
		Scheme:  scheme,
		Config:  config.AppConfig{},
		PromAPI: fakePromQueryAPI{value: 2.0},
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(slo)}); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var incidents sre.PredictiveIncidentList
	if err := cl.List(context.Background(), &incidents, client.InNamespace("apps")); err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(incidents.Items) != 1 {
		t.Fatalf("expected 1 predictive incident, got %d", len(incidents.Items))
	}
	if incidents.Items[0].Spec.Identity.Job != "checkout" {
		t.Fatalf("expected job to default to service name, got %q", incidents.Items[0].Spec.Identity.Job)
	}
}

func sloSignals(errorRate, slope, min5xx float64) sre.SLOSignals {
	var signals sre.SLOSignals
	signals.Http5xx.ErrorRateThresholdPct = errorRate
	signals.Http5xx.SlopeThreshold = slope
	signals.Http5xx.Min5xxRPS = min5xx
	return signals
}

func mustAddScheme(t *testing.T, scheme *runtime.Scheme) {
	t.Helper()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("corev1.AddToScheme returned error: %v", err)
	}
	if err := sre.AddToScheme(scheme); err != nil {
		t.Fatalf("sre.AddToScheme returned error: %v", err)
	}
}

type fakePromQueryAPI struct {
	value float64
}

func (f fakePromQueryAPI) Query(string) (map[string]any, error) {
	return map[string]any{
		"data": map[string]any{
			"result": []any{
				map[string]any{
					"value": []any{0, fmt.Sprintf("%.1f", f.value)},
				},
			},
		},
	}, nil
}
