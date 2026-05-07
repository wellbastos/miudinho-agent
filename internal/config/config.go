package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type HTTPConfig struct {
	AlertWebhookAddr string
}

type ObservabilityConfig struct {
	PromURL                   string
	TempoURL                  string
	TempoPredictivePath       string
	TempoPredictiveQueryParam string
	AlertmanagerOutboundURL   string
	AlertmanagerAPIURL        string
}

type LLMConfig struct {
	RoutingMode    string
	OllamaBaseURL  string
	OllamaModel    string
	GeminiBaseURL  string
	GeminiModel    string
	GoogleAPIKey   string
	SystemPrompt   string
	ApproverPrompt string
}

type GitHubConfig struct {
	APIURL      string
	Token       string
	Owner       string
	RepoPrefix  string
	ProductName string
	N2Teams     []string
}

type NotificationsConfig struct {
	GoogleChatIncidentsWebhookURL string
}

type AlertPollingConfig struct {
	Interval time.Duration
	Sources  []string
}

type ExecutionConfig struct {
	ExecuteActions  bool
	AutoObserveOnly bool
	ObserveOnlyTTL  time.Duration
	LeaderElection  bool
}

type AppConfig struct {
	HTTP          HTTPConfig
	Observability ObservabilityConfig
	LLM           LLMConfig
	GitHub        GitHubConfig
	Notifications NotificationsConfig
	AlertPolling  AlertPollingConfig
	Execution     ExecutionConfig
}

func LoadFromEnv() AppConfig {
	return AppConfig{
		HTTP: HTTPConfig{
			AlertWebhookAddr: getenv("ALERT_WEBHOOK_ADDR", ":8090"),
		},
		Observability: ObservabilityConfig{
			PromURL:                   getenv("PROM_URL", "http://thanos-query.o11y.svc.cluster.local:10901"),
			TempoURL:                  getenv("TEMPO_URL", "http://tempo.o11y.svc.cluster.local:3100"),
			TempoPredictivePath:       getenv("TEMPO_PREDICTIVE_PATH", "api/search"),
			TempoPredictiveQueryParam: getenv("TEMPO_PREDICTIVE_QUERY_PARAM", "q"),
			AlertmanagerOutboundURL:   strings.TrimSpace(os.Getenv("ALERTMANAGER_OUTBOUND_URL")),
			AlertmanagerAPIURL:        strings.TrimSpace(os.Getenv("ALERTMANAGER_API_URL")),
		},
		LLM: LLMConfig{
			RoutingMode:    getenv("LLM_ROUTING_MODE", "ollama_only"),
			OllamaBaseURL:  getenv("OLLAMA_BASE_URL", "http://ollama.o11y.svc.cluster.local:11434"),
			OllamaModel:    getenv("OLLAMA_MODEL", "llama3.1:8b"),
			GeminiBaseURL:  getenv("GEMINI_BASE_URL", "https://generativelanguage.googleapis.com"),
			GeminiModel:    getenv("GEMINI_MODEL", "gemini-1.5-pro"),
			GoogleAPIKey:   strings.TrimSpace(os.Getenv("GOOGLE_API_KEY")),
			SystemPrompt:   getenv("SYSTEM_PROMPT", defaultSystemPrompt()),
			ApproverPrompt: getenv("APPROVER_PROMPT", defaultApproverPrompt()),
		},
		GitHub: GitHubConfig{
			APIURL:      getenv("GITHUB_API_URL", "https://api.github.com"),
			Token:       strings.TrimSpace(os.Getenv("GITHUB_TOKEN")),
			Owner:       strings.TrimSpace(os.Getenv("GITHUB_OWNER")),
			RepoPrefix:  getenv("GITHUB_REPOSITORY_PREFIX", "apps-"),
			ProductName: strings.TrimSpace(os.Getenv("GITHUB_PRODUCT_NAME")),
			N2Teams:     splitCSV(getenv("GITHUB_N2_TEAMS", "sre-editor,sre-viewer,sre-admin")),
		},
		Notifications: NotificationsConfig{
			GoogleChatIncidentsWebhookURL: strings.TrimSpace(os.Getenv("GOOGLE_CHAT_INCIDENTS_WEBHOOK_URL")),
		},
		AlertPolling: AlertPollingConfig{
			Interval: parseDuration(getenv("ALERT_POLL_INTERVAL", "30s"), 30*time.Second),
			Sources:  splitCSV(getenv("ALERT_SOURCES_ENABLED", "prometheus,alertmanager")),
		},
		Execution: ExecutionConfig{
			ExecuteActions:  os.Getenv("EXECUTE_ACTIONS") == "true",
			AutoObserveOnly: os.Getenv("AUTO_OBSERVE_ONLY") != "false",
			ObserveOnlyTTL:  parseSeconds(getenv("OBSERVE_ONLY_TTL_SECONDS", "300"), 300*time.Second),
			LeaderElection:  os.Getenv("LEADER_ELECTION") == "true",
		},
	}
}

func getenv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func parseSeconds(raw string, fallback time.Duration) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

func parseDuration(raw string, fallback time.Duration) time.Duration {
	duration, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || duration <= 0 {
		return fallback
	}
	return duration
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

func defaultSystemPrompt() string {
	return `Você é um Agente SRE de produção. Responda sempre em JSON válido com classification, confidence, summary, evidence, prom_queries, actions, rollback_or_next_steps, escalation. Para predictive, priorize low-risk. Confidence < 0.70 => sem mudanças.`
}

func defaultApproverPrompt() string {
	return `Você é o Change Approver. Responda somente JSON com approved, risk_level, reasons, required_changes. Bloqueie ações arriscadas e qualquer confidence < 0.70.`
}
