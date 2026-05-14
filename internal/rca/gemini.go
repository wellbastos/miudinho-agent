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

type GeminiClient struct {
	BaseURL string
	Model   string
	APIKey  string
	HTTP    *http.Client
}

func NewGeminiClient(cfg config.LLMConfig) *GeminiClient {
	return &GeminiClient{
		BaseURL: cfg.GeminiBaseURL,
		Model:   cfg.GeminiModel,
		APIKey:  cfg.GoogleAPIKey,
		HTTP:    &http.Client{Timeout: 20 * time.Second},
	}
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiGenerationConfig struct {
	ResponseMimeType string `json:"responseMimeType,omitempty"`
}

type geminiGenerateRequest struct {
	Contents         []geminiContent        `json:"contents"`
	GenerationConfig *geminiGenerationConfig `json:"generationConfig,omitempty"`
}

type geminiGenerateResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

func (g *GeminiClient) endpoint() string {
	return fmt.Sprintf("%s/v1beta/models/%s:generateContent", g.BaseURL, g.Model)
}

func (g *GeminiClient) newRequest(ctx context.Context, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("gemini: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.APIKey)
	return req, nil
}

func (g *GeminiClient) Healthcheck(ctx context.Context) error {
	if g.APIKey == "" {
		return fmt.Errorf("missing GOOGLE_API_KEY")
	}

	body := geminiGenerateRequest{
		Contents: []geminiContent{{Parts: []geminiPart{{Text: "Respond only with OK"}}}},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("gemini: marshal healthcheck body: %w", err)
	}

	req, err := g.newRequest(ctx, raw)
	if err != nil {
		return err
	}

	resp, err := g.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("gemini health status=%d", resp.StatusCode)
	}
	return nil
}

func (g *GeminiClient) Decide(ctx context.Context, systemPrompt string, input map[string]any) (string, error) {
	if g.APIKey == "" {
		return "", fmt.Errorf("missing GOOGLE_API_KEY")
	}

	payload := map[string]any{"system": systemPrompt, "input": input}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("gemini: marshal decide payload: %w", err)
	}
	body := geminiGenerateRequest{
		Contents:         []geminiContent{{Parts: []geminiPart{{Text: string(b)}}}},
		GenerationConfig: &geminiGenerationConfig{ResponseMimeType: "application/json"},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("gemini: marshal decide body: %w", err)
	}

	req, err := g.newRequest(ctx, raw)
	if err != nil {
		return "", err
	}

	resp, err := g.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("gemini decide status=%d", resp.StatusCode)
	}

	var out geminiGenerateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Candidates) == 0 || len(out.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("gemini empty response")
	}
	return out.Candidates[0].Content.Parts[0].Text, nil
}

func (g *GeminiClient) Approve(ctx context.Context, approverPrompt string, input map[string]any) (string, error) {
	if g.APIKey == "" {
		return "", fmt.Errorf("missing GOOGLE_API_KEY")
	}

	payload := map[string]any{"system": approverPrompt, "input": input}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("gemini: marshal approve payload: %w", err)
	}
	body := geminiGenerateRequest{
		Contents:         []geminiContent{{Parts: []geminiPart{{Text: string(b)}}}},
		GenerationConfig: &geminiGenerationConfig{ResponseMimeType: "application/json"},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("gemini: marshal approve body: %w", err)
	}

	req, err := g.newRequest(ctx, raw)
	if err != nil {
		return "", err
	}

	resp, err := g.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("gemini approve status=%d", resp.StatusCode)
	}

	var out geminiGenerateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Candidates) == 0 || len(out.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("gemini empty approval response")
	}
	return out.Candidates[0].Content.Parts[0].Text, nil
}
