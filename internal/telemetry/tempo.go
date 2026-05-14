package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type TempoClient struct {
	BaseURL string
	Path    string
	Param   string
	http    *http.Client
}

func NewTempoClient(base, path, param string) *TempoClient {
	return &TempoClient{BaseURL: base, Path: path, Param: param, http: &http.Client{Timeout: 20 * time.Second}}
}

func (t *TempoClient) Search(qstr string) (map[string]any, error) {
	return t.SearchContext(context.Background(), qstr)
}

func (t *TempoClient) SearchContext(ctx context.Context, qstr string) (map[string]any, error) {
	u, err := url.Parse(t.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("tempo: invalid base URL: %w", err)
	}
	u.Path = "/" + t.Path
	q := u.Query()
	q.Set(t.Param, qstr)
	q.Set("since", "10m")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("tempo: build request: %w", err)
	}

	resp, err := t.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return map[string]any{"status": resp.StatusCode}, fmt.Errorf("tempo decode error: %w", err)
	}
	if resp.StatusCode >= 300 {
		return out, fmt.Errorf("tempo status=%d", resp.StatusCode)
	}
	return out, nil
}
