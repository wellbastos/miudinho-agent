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

type geminiGenerateRequest struct {
	Contents []geminiContent `json:"contents"`
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
	return fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", g.BaseURL, g.Model, g.APIKey)
}

func (g *GeminiClient) Healthcheck(ctx context.Context) error {
	if g.APIKey == "" {
		return fmt.Errorf("missing GOOGLE_API_KEY")
	}

	body := geminiGenerateRequest{
		Contents: []geminiContent{{Parts: []geminiPart{{Text: "Respond only with OK"}}}},
	}
	raw, _ := json.Marshal(body)

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint(), bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")

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
	b, _ := json.Marshal(payload)
	body := geminiGenerateRequest{Contents: []geminiContent{{Parts: []geminiPart{{Text: string(b)}}}}}
	raw, _ := json.Marshal(body)

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint(), bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")

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
	b, _ := json.Marshal(payload)
	body := geminiGenerateRequest{Contents: []geminiContent{{Parts: []geminiPart{{Text: string(b)}}}}}
	raw, _ := json.Marshal(body)

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint(), bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")

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
