package googlechat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSendPostsExpectedPayload(t *testing.T) {
	var gotContentType string
	var gotPayload messagePayload

	client := &Client{
		webhookURL: "https://chat.example/webhook",
		httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotContentType = r.Header.Get("Content-Type")
			if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{}`)),
			}, nil
		})},
	}

	if err := client.Send(context.Background(), "incident escalated"); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if gotContentType != "application/json" {
		t.Fatalf("unexpected content type: %q", gotContentType)
	}
	if gotPayload.Text != "incident escalated" {
		t.Fatalf("unexpected payload text: %q", gotPayload.Text)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
