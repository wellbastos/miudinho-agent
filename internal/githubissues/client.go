package githubissues

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/wellbastos/miudinho-agent/internal/config"
)

var slugSanitizer = regexp.MustCompile(`[^a-z0-9-]+`)

type Client struct {
	baseURL    string
	httpClient *http.Client
	token      string
	owner      string
	repoPrefix string
	product    string
	teams      []string
}

type Issue struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Title   string `json:"title"`
}

func New(cfg config.GitHubConfig) *Client {
	return &Client{
		baseURL: cfg.APIURL,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		token:      cfg.Token,
		owner:      cfg.Owner,
		repoPrefix: cfg.RepoPrefix,
		product:    cfg.ProductName,
		teams:      cfg.N2Teams,
	}
}

func NewFromEnv() *Client {
	return New(config.LoadFromEnv().GitHub)
}

func (c *Client) Enabled() bool {
	return c != nil && c.token != "" && c.owner != ""
}

func (c *Client) Repository(defaultProduct string) string {
	product := sanitizeSlug(c.product)
	if product == "" {
		product = sanitizeSlug(defaultProduct)
	}
	if product == "" {
		product = "unknown"
	}
	return sanitizeRepositoryPrefix(c.repoPrefix) + product
}

func (c *Client) TeamMentions() []string {
	out := make([]string, 0, len(c.teams))
	for _, team := range c.teams {
		team = sanitizeSlug(team)
		if team == "" {
			continue
		}
		out = append(out, "@"+c.owner+"/"+team)
	}
	return out
}

func (c *Client) TeamSlugs() []string {
	out := make([]string, 0, len(c.teams))
	for _, team := range c.teams {
		team = sanitizeSlug(team)
		if team == "" {
			continue
		}
		out = append(out, team)
	}
	return out
}

func (c *Client) CreateIssue(ctx context.Context, repo, title, body string, labels []string) (*Issue, error) {
	payload := map[string]any{
		"title":  title,
		"body":   body,
		"labels": labels,
	}
	var issue Issue
	if err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/issues", c.owner, repo), payload, &issue); err != nil {
		return nil, err
	}
	return &issue, nil
}

func (c *Client) CloseIssue(ctx context.Context, repo string, number int) error {
	payload := map[string]any{"state": "closed"}
	return c.doJSON(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/%s/issues/%d", c.owner, repo, number), payload, nil)
}

// UpdateIssue atualiza o corpo (body) de uma issue existente.
func (c *Client) UpdateIssue(ctx context.Context, repo string, number int, body string) error {
	payload := map[string]any{"body": body}
	return c.doJSON(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/%s/issues/%d", c.owner, repo, number), payload, nil)
}

func (c *Client) AddComment(ctx context.Context, repo string, number int, body string) error {
	payload := map[string]any{"body": body}
	return c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/issues/%d/comments", c.owner, repo, number), payload, nil)
}

// FindOpenIssue busca a primeira issue aberta no repositório que tenha o label fpLabel.
// Retorna nil sem erro quando nenhuma issue é encontrada.
func (c *Client) FindOpenIssue(ctx context.Context, repo, fpLabel string) (*Issue, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues?labels=%s&state=open&per_page=1",
		c.owner, repo, url.QueryEscape(fpLabel))
	var issues []Issue
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &issues); err != nil {
		return nil, err
	}
	if len(issues) == 0 {
		return nil, nil
	}
	return &issues[0], nil
}

// SearchIssueByTitle usa a GitHub Search API para buscar issues (abertas ou fechadas)
// pelo título exato. Retorna a issue mais recente encontrada, ou nil.
// Usado como fallback quando a busca por label não encontra resultado (issues antigas sem label fp-).
func (c *Client) SearchIssueByTitle(ctx context.Context, repo, title string) (*Issue, error) {
	q := url.QueryEscape(fmt.Sprintf("repo:%s/%s \"%s\" is:issue", c.owner, repo, title))
	path := "/search/issues?q=" + q + "&per_page=1&sort=updated&order=desc"
	var result struct {
		Items []Issue `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	if len(result.Items) == 0 {
		return nil, nil
	}
	return &result.Items[0], nil
}

// ReopenIssue reabre uma issue fechada.
func (c *Client) ReopenIssue(ctx context.Context, repo string, number int) error {
	payload := map[string]any{"state": "open"}
	return c.doJSON(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/%s/issues/%d", c.owner, repo, number), payload, nil)
}

func (c *Client) doJSON(ctx context.Context, method, path string, payload any, out any) error {
	if !c.Enabled() {
		return fmt.Errorf("github integration is disabled")
	}

	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.baseURL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("github api %s %s failed: status=%d body=%s", method, path, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	if out == nil || len(respBody) == 0 {
		return nil
	}
	return json.Unmarshal(respBody, out)
}

func sanitizeSlug(in string) string {
	s := strings.ToLower(strings.TrimSpace(in))
	s = strings.ReplaceAll(s, "_", "-")
	s = slugSanitizer.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

func splitCSV(in string) []string {
	parts := strings.Split(in, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func sanitizeRepositoryPrefix(in string) string {
	prefix := strings.ToLower(strings.TrimSpace(in))
	prefix = strings.ReplaceAll(prefix, "_", "-")
	prefix = slugSanitizer.ReplaceAllString(prefix, "-")
	prefix = strings.Trim(prefix, "-")
	if prefix == "" {
		return ""
	}
	return prefix + "-"
}
