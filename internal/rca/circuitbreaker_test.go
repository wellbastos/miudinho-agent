package rca

import (
	"testing"
	"time"
)

func TestGetenvReturnsFallbackWhenUnset(t *testing.T) {
	t.Setenv("RCA_TEST_ENV", "")

	if got := getenv("RCA_TEST_ENV", "fallback"); got != "fallback" {
		t.Fatalf("expected fallback, got %q", got)
	}
}

func TestGetenvReturnsValueWhenSet(t *testing.T) {
	t.Setenv("RCA_TEST_ENV", "configured")

	if got := getenv("RCA_TEST_ENV", "fallback"); got != "configured" {
		t.Fatalf("expected configured value, got %q", got)
	}
}

func TestNewCircuitBreakerUsesConfiguredTTL(t *testing.T) {
	t.Setenv("OBSERVE_ONLY_TTL_SECONDS", "7")
	t.Setenv("AUTO_OBSERVE_ONLY", "true")

	cb := NewCircuitBreaker()
	if cb.ttl != 7*time.Second {
		t.Fatalf("expected ttl 7s, got %s", cb.ttl)
	}
}

func TestCircuitBreakerObserveOnlyWindow(t *testing.T) {
	t.Setenv("OBSERVE_ONLY_TTL_SECONDS", "1")
	t.Setenv("AUTO_OBSERVE_ONLY", "true")

	cb := NewCircuitBreaker()
	cb.SetObserveOnly("upstream unhealthy")

	observeOnly, reason := cb.ObserveOnly()
	if !observeOnly {
		t.Fatal("expected observe-only mode to be active")
	}
	if reason != "upstream unhealthy" {
		t.Fatalf("expected reason to be preserved, got %q", reason)
	}

	snapshot := cb.Snapshot()
	if got, ok := snapshot["observeOnly"].(bool); !ok || !got {
		t.Fatalf("expected snapshot observeOnly=true, got %#v", snapshot["observeOnly"])
	}
	if got := snapshot["lastReason"]; got != "upstream unhealthy" {
		t.Fatalf("expected snapshot reason, got %#v", got)
	}
}

func TestCircuitBreakerDisabledIgnoresObserveOnly(t *testing.T) {
	t.Setenv("AUTO_OBSERVE_ONLY", "false")
	t.Setenv("OBSERVE_ONLY_TTL_SECONDS", "60")

	cb := NewCircuitBreaker()
	cb.SetObserveOnly("should be ignored")

	observeOnly, reason := cb.ObserveOnly()
	if observeOnly {
		t.Fatal("expected observe-only mode to stay disabled")
	}
	if reason != "" {
		t.Fatalf("expected empty reason when disabled, got %q", reason)
	}
}

func TestCircuitBreakerTracksProviderHealth(t *testing.T) {
	cb := NewCircuitBreaker()
	cb.SetOllamaHealth(false)
	cb.SetGeminiHealth(true)

	snapshot := cb.Snapshot()
	if got := snapshot["lastOllamaOK"]; got != false {
		t.Fatalf("expected lastOllamaOK=false, got %#v", got)
	}
	if got := snapshot["lastGeminiOK"]; got != true {
		t.Fatalf("expected lastGeminiOK=true, got %#v", got)
	}
}
