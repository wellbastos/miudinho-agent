package telemetry

import (
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
	u, _ := url.Parse(t.BaseURL)
	u.Path = "/" + t.Path
	q := u.Query()
	q.Set(t.Param, qstr)
	q.Set("since", "10m")
	u.RawQuery = q.Encode()

	req, _ := http.NewRequest("GET", u.String(), nil)
	resp, err := t.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return map[string]any{"status": resp.StatusCode}, fmt.Errorf("tempo decode error: %w", err)
	}
	if resp.StatusCode >= 300 {
		return out, fmt.Errorf("tempo status=%d", resp.StatusCode)
	}
	return out, nil
}
