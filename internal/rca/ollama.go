package rca

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/wellbastos/miudinho-agent/internal/config"
)

type OllamaClient struct {
	BaseURL string
	Model   string
	HTTP    *http.Client
}

func NewOllamaClient(cfg config.LLMConfig) *OllamaClient {
	return &OllamaClient{
		BaseURL: cfg.OllamaBaseURL,
		Model:   cfg.OllamaModel,
		HTTP:    &http.Client{Timeout: 20 * time.Second},
	}
}

type ollamaGenerateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

type ollamaGenerateResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
}

func (o *OllamaClient) Healthcheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.BaseURL+"/api/tags", nil)
	if err != nil {
		return fmt.Errorf("ollama: build healthcheck request: %w", err)
	}
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ollama health status=%d", resp.StatusCode)
	}
	return nil
}

func (o *OllamaClient) Decide(ctx context.Context, systemPrompt string, input map[string]any) (string, error) {
	payload := map[string]any{
		"system": systemPrompt,
		"input":  input,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("ollama: marshal payload: %w", err)
	}

	body := ollamaGenerateRequest{
		Model:  o.Model,
		Prompt: string(b),
		Stream: false,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("ollama: marshal body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/api/generate", bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("ollama: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("ollama generate status=%d", resp.StatusCode)
	}

	var out ollamaGenerateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Response, nil
}
