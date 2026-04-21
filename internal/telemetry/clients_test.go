package telemetry

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPromQueryBuildsExpectedRequest(t *testing.T) {
	var gotPath string
	var gotQuery string

	client := NewPromClient("https://prom.example")
	client.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("query")
		respBody, err := json.Marshal(map[string]any{
			"status": "success",
			"data":   map[string]any{"result": []any{}},
		})
		if err != nil {
			t.Fatalf("marshal response body: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(string(respBody))),
		}, nil
	})}
	resp, err := client.Query(`sum(rate(http_requests_total[5m]))`)
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}

	if gotPath != "/api/v1/query" {
		t.Fatalf("unexpected path: %q", gotPath)
	}
	if gotQuery != `sum(rate(http_requests_total[5m]))` {
		t.Fatalf("unexpected query param: %q", gotQuery)
	}
	if resp["status"] != "success" {
		t.Fatalf("unexpected response payload: %#v", resp)
	}
}

func TestTempoSearchBuildsExpectedRequest(t *testing.T) {
	var gotPath string
	var gotQuery string
	var gotSince string

	client := NewTempoClient("https://tempo.example", "api/search", "q")
	client.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("q")
		gotSince = r.URL.Query().Get("since")
		respBody, err := json.Marshal(map[string]any{
			"traces": []any{},
		})
		if err != nil {
			t.Fatalf("marshal response body: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(string(respBody))),
		}, nil
	})}
	resp, err := client.Search(`service.name="api"`)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}

	if gotPath != "/api/search" {
		t.Fatalf("unexpected path: %q", gotPath)
	}
	if gotQuery != `service.name="api"` {
		t.Fatalf("unexpected search query: %q", gotQuery)
	}
	if gotSince != "10m" {
		t.Fatalf("unexpected since param: %q", gotSince)
	}
	if _, ok := resp["traces"]; !ok {
		t.Fatalf("unexpected response payload: %#v", resp)
	}
}

func TestPromQueryReturnsErrorOnNon2xx(t *testing.T) {
	client := NewPromClient("https://prom.example")
	client.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return responseWithJSON(http.StatusBadGateway, map[string]any{"status": "error"})
	})}
	if _, err := client.Query("up"); err == nil {
		t.Fatal("expected error for non-2xx Prometheus response")
	}
}

func TestTempoSearchReturnsErrorOnNon2xx(t *testing.T) {
	client := NewTempoClient("https://tempo.example", "api/search", "q")
	client.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return responseWithJSON(http.StatusBadGateway, map[string]any{"status": "error"})
	})}
	if _, err := client.Search("up"); err == nil {
		t.Fatal("expected error for non-2xx Tempo response")
	}
}

func responseWithJSON(statusCode int, payload map[string]any) (*http.Response, error) {
	respBody, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: statusCode,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(respBody))),
	}, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
