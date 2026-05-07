package googlechat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wellbastos/miudinho-agent/internal/config"
)

type Client struct {
	webhookURL string
	httpClient *http.Client
}

type messagePayload struct {
	Text string `json:"text"`
}

func New(cfg config.NotificationsConfig) *Client {
	return &Client{
		webhookURL: strings.TrimSpace(cfg.GoogleChatIncidentsWebhookURL),
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *Client) Enabled() bool {
	return c != nil && c.webhookURL != ""
}

func (c *Client) Send(ctx context.Context, text string) error {
	if !c.Enabled() {
		return nil
	}

	raw, err := json.Marshal(messagePayload{Text: text})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.webhookURL, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("google chat send failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
