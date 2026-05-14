package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type PromClient struct {
	BaseURL string
	http    *http.Client
}

func NewPromClient(base string) *PromClient {
	return &PromClient{BaseURL: base, http: &http.Client{Timeout: 15 * time.Second}}
}

func (p *PromClient) Query(query string) (map[string]any, error) {
	return p.QueryContext(context.Background(), query)
}

func (p *PromClient) QueryContext(ctx context.Context, query string) (map[string]any, error) {
	u, err := url.Parse(p.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("prom: invalid base URL: %w", err)
	}
	u.Path = "/api/v1/query"
	q := u.Query()
	q.Set("query", query)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("prom: build request: %w", err)
	}

	resp, err := p.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return out, fmt.Errorf("prom status=%d", resp.StatusCode)
	}
	return out, nil
}
