package rca

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type OllamaClient struct {
	BaseURL string
	Model   string
	HTTP    *http.Client
}

func NewOllamaClient() *OllamaClient {
	return &OllamaClient{
		BaseURL: getenv("OLLAMA_BASE_URL", "http://ollama.o11y.svc.cluster.local:11434"),
		Model:   getenv("OLLAMA_MODEL", "llama3.1:8b"),
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
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, o.BaseURL+"/api/tags", nil)
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
	b, _ := json.Marshal(payload)

	body := ollamaGenerateRequest{
		Model:  o.Model,
		Prompt: string(b),
		Stream: false,
	}
	raw, _ := json.Marshal(body)

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/api/generate", bytes.NewReader(raw))
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
