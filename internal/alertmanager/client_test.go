package alertmanager

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSendNoopsWhenDisabled(t *testing.T) {
	client := &Client{}
	if err := client.Send(context.Background(), []Alert{{Labels: map[string]string{"alertname": "test"}}}); err != nil {
		t.Fatalf("expected disabled client to noop, got error: %v", err)
	}
}

func TestSendPostsAlerts(t *testing.T) {
	var received []Alert

	client := &Client{
		baseURL: "https://alertmanager.example/api/v2/alerts",
		httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", r.Method)
			}
			if ct := r.Header.Get("Content-Type"); ct != "application/json" {
				t.Fatalf("unexpected content-type: %q", ct)
			}
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			return &http.Response{
				StatusCode: http.StatusAccepted,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		})},
	}

	alerts := []Alert{{
		Labels:      map[string]string{"alertname": "HighErrorRate"},
		Annotations: map[string]string{"summary": "incident"},
	}}
	if err := client.Send(context.Background(), alerts); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	if len(received) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(received))
	}
	if got := received[0].Labels["alertname"]; got != "HighErrorRate" {
		t.Fatalf("unexpected alert label: %q", got)
	}
}

func TestSendReturnsErrorOnNon2xx(t *testing.T) {
	client := &Client{
		baseURL: "https://alertmanager.example/api/v2/alerts",
		httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("bad request")),
			}, nil
		})},
	}

	if err := client.Send(context.Background(), []Alert{{Labels: map[string]string{"alertname": "test"}}}); err == nil {
		t.Fatal("expected error for non-2xx response")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
