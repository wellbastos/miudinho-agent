package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// LokiClient consulta logs via Loki HTTP API (LogQL).
// Suporta Basic Auth (username+password), Bearer token e multi-tenant (X-Scope-OrgID).
type LokiClient struct {
	BaseURL  string
	Username string
	Password string
	Token    string
	TenantID string
	http     *http.Client
}

func NewLokiClient(base, username, password, token, tenantID string) *LokiClient {
	return &LokiClient{
		BaseURL:  strings.TrimRight(strings.TrimSpace(base), "/"),
		Username: strings.TrimSpace(username),
		Password: strings.TrimSpace(password),
		Token:    strings.TrimSpace(token),
		TenantID: strings.TrimSpace(tenantID),
		http:     &http.Client{Timeout: 20 * time.Second},
	}
}

func (l *LokiClient) Enabled() bool {
	return l != nil && l.BaseURL != ""
}

// lokiResponse é o formato padrão da Loki HTTP API v1.
type lokiResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Stream map[string]string `json:"stream"`
			Values [][]string        `json:"values"` // [timestampNs, logLine]
		} `json:"result"`
	} `json:"data"`
}

// QueryLogs busca até limit linhas de log via LogQL no período informado (lookback).
// Retorna as linhas mais recentes, com o log mais novo primeiro.
func (l *LokiClient) QueryLogs(ctx context.Context, query string, limit int, lookback time.Duration) ([]string, error) {
	if !l.Enabled() {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	if lookback <= 0 {
		lookback = 15 * time.Minute
	}
	now := time.Now().UTC()
	return l.queryLogsInWindow(ctx, query, limit, now.Add(-lookback), now)
}

// QueryLogsInWindow busca até limit linhas de log via LogQL entre start e end.
// Retorna as linhas mais recentes, com o log mais novo primeiro.
func (l *LokiClient) QueryLogsInWindow(ctx context.Context, query string, limit int, start, end time.Time) ([]string, error) {
	if !l.Enabled() {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	return l.queryLogsInWindow(ctx, query, limit, start, end)
}

func (l *LokiClient) queryLogsInWindow(ctx context.Context, query string, limit int, start, end time.Time) ([]string, error) {
	u, err := url.Parse(l.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("loki: invalid base URL: %w", err)
	}
	u.Path = u.Path + "/loki/api/v1/query_range"

	q := u.Query()
	q.Set("query", query)
	q.Set("start", fmt.Sprintf("%d", start.UTC().UnixNano()))
	q.Set("end", fmt.Sprintf("%d", end.UTC().UnixNano()))
	q.Set("limit", fmt.Sprintf("%d", limit))
	q.Set("direction", "backward") // mais recentes primeiro
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("loki: build request: %w", err)
	}
	l.setAuth(req)

	resp, err := l.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("loki: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("loki: status=%d url=%s", resp.StatusCode, u.String())
	}

	var payload lokiResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("loki: decode response: %w", err)
	}

	lines := make([]string, 0, limit)
	for _, stream := range payload.Data.Result {
		for _, entry := range stream.Values {
			if len(entry) >= 2 && len(lines) < limit {
				lines = append(lines, entry[1])
			}
		}
	}
	return lines, nil
}

// QueryServiceLogs busca logs de um serviço/namespace filtrando por padrões de erro.
// Usa alertStartsAt como âncora da janela temporal quando disponível, garantindo que a
// consulta cubra o período do incidente mesmo que o reconciler execute com atraso.
// Tenta seletores progressivamente menos restritivos até obter resultados.
func (l *LokiClient) QueryServiceLogs(ctx context.Context, namespace, service, pod string, limit int, alertStartsAt time.Time) ([]string, map[string]string, error) {
	if !l.Enabled() {
		return nil, nil, nil
	}

	now := time.Now().UTC()
	var start, end time.Time
	if !alertStartsAt.IsZero() {
		// Janela ancorada no início do alerta: 5 min antes até 30 min depois (limitado a now).
		start = alertStartsAt.Add(-5 * time.Minute)
		end = alertStartsAt.Add(30 * time.Minute)
		if end.After(now) {
			end = now
		}
	} else {
		start = now.Add(-15 * time.Minute)
		end = now
	}

	// Monta seletores em ordem de especificidade:
	// 1. Namespace + pod específico
	// 2. Namespace + app/service label
	// 3. Apenas namespace
	queries := buildLokiQueries(namespace, service, pod)

	for _, attempt := range queries {
		lines, err := l.queryLogsInWindow(ctx, attempt.query, limit, start, end)
		if err != nil {
			continue
		}
		if len(lines) > 0 {
			return lines, map[string]string{
				"query":    attempt.query,
				"selector": attempt.label,
			}, nil
		}
	}
	return nil, nil, nil
}

type lokiQueryAttempt struct {
	query string
	label string
}

// buildLokiQueries retorna candidatos de query LogQL do mais para o menos específico.
// Para cada seletor, busca primeiro com filtro de erro, depois sem filtro.
func buildLokiQueries(namespace, service, pod string) []lokiQueryAttempt {
	if namespace == "" {
		return nil
	}

	// Filtro de padrões de erro comuns em logs de aplicação
	errorFilter := ` |~ "(?i)(error|fatal|panic|exception|fail|timeout|refused|denied|crash|oom|killed|signal)"`

	var attempts []lokiQueryAttempt

	// Pod específico (mais preciso)
	if pod != "" {
		base := fmt.Sprintf(`{namespace="%s", pod="%s"}`, namespace, pod)
		attempts = append(attempts,
			lokiQueryAttempt{base + errorFilter, "pod+error"},
			lokiQueryAttempt{base, "pod"},
		)
	}

	// Por nome de app/service
	if service != "" && service != "unknown" {
		baseApp := fmt.Sprintf(`{namespace="%s", app="%s"}`, namespace, service)
		attempts = append(attempts,
			lokiQueryAttempt{baseApp + errorFilter, "app+error"},
			lokiQueryAttempt{baseApp, "app"},
		)

		// Fallback com label kubernetes_app_name
		baseName := fmt.Sprintf(`{namespace="%s", app_kubernetes_io_name="%s"}`, namespace, service)
		attempts = append(attempts,
			lokiQueryAttempt{baseName + errorFilter, "k8s_name+error"},
			lokiQueryAttempt{baseName, "k8s_name"},
		)
	}

	// Apenas namespace (mais amplo)
	baseNS := fmt.Sprintf(`{namespace="%s"}`, namespace)
	attempts = append(attempts,
		lokiQueryAttempt{baseNS + errorFilter, "namespace+error"},
	)

	return attempts
}

func (l *LokiClient) setAuth(req *http.Request) {
	if l.Token != "" {
		req.Header.Set("Authorization", "Bearer "+l.Token)
	} else if l.Username != "" {
		req.SetBasicAuth(l.Username, l.Password)
	}
	if l.TenantID != "" {
		req.Header.Set("X-Scope-OrgID", l.TenantID)
	}
}
