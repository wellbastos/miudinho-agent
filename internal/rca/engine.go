package rca

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/wellbastos/miudinho-agent/internal/config"
	appmetrics "github.com/wellbastos/miudinho-agent/internal/metrics"
)

type Decision struct {
	Classification string         `json:"classification"`
	Confidence     float64        `json:"confidence"`
	Summary        string         `json:"summary"`
	Evidence       []string       `json:"evidence"`
	PromQueries    []string       `json:"prom_queries"`
	Actions        []Action       `json:"actions"`
	Rollback       []string       `json:"rollback_or_next_steps"`
	Escalation     map[string]any `json:"escalation"`
}

// UnmarshalJSON implementa um parser tolerante a falhas para a resposta do LLM.
// O Gemini ocasionalmente retorna campos como objetos em vez de strings simples
// (ex: `classification: {"category": "oom_kill"}` em vez de `classification: "oom_kill"`).
// Este método normaliza esses casos sem falhar.
func (d *Decision) UnmarshalJSON(b []byte) error {
	type raw struct {
		Classification json.RawMessage `json:"classification"`
		Confidence     json.RawMessage `json:"confidence"`
		Summary        json.RawMessage `json:"summary"`
		Evidence       json.RawMessage `json:"evidence"`
		PromQueries    json.RawMessage `json:"prom_queries"`
		Actions        json.RawMessage `json:"actions"`
		Rollback       json.RawMessage `json:"rollback_or_next_steps"`
		Escalation     json.RawMessage `json:"escalation"`
	}
	var r raw
	if err := json.Unmarshal(b, &r); err != nil {
		return err
	}

	d.Classification = flexString(r.Classification, "unknown")
	d.Summary = flexString(r.Summary, "")

	if len(r.Confidence) > 0 {
		_ = json.Unmarshal(r.Confidence, &d.Confidence)
	}
	if len(r.Evidence) > 0 {
		_ = json.Unmarshal(r.Evidence, &d.Evidence)
	}
	if len(r.PromQueries) > 0 {
		_ = json.Unmarshal(r.PromQueries, &d.PromQueries)
	}
	if len(r.Actions) > 0 {
		_ = json.Unmarshal(r.Actions, &d.Actions)
	}
	if len(r.Rollback) > 0 {
		_ = json.Unmarshal(r.Rollback, &d.Rollback)
	}
	if len(r.Escalation) > 0 {
		_ = json.Unmarshal(r.Escalation, &d.Escalation)
	}
	return nil
}

// flexString extrai um valor string de um json.RawMessage mesmo quando o LLM
// retornou um objeto em vez de uma string simples.
// Prioridade: string literal → campo "type"/"category"/"name"/"value" → primeira string encontrada.
func flexString(raw json.RawMessage, fallback string) string {
	if len(raw) == 0 {
		return fallback
	}
	// Caso 1: já é uma string JSON
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s != "" {
			return s
		}
		return fallback
	}
	// Caso 2: é um objeto — tenta extrair campo semântico preferencial
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err == nil {
		for _, key := range []string{"type", "category", "name", "value", "label", "classification"} {
			if v, ok := m[key].(string); ok && v != "" {
				return v
			}
		}
		// Retorna a primeira string encontrada
		for _, v := range m {
			if sv, ok := v.(string); ok && sv != "" {
				return sv
			}
		}
	}
	return fallback
}

type Action struct {
	Tool   string         `json:"tool"`
	Args   map[string]any `json:"args"`
	Name   string         `json:"name,omitempty"`
	Reason string         `json:"reason,omitempty"`
}

type Approval struct {
	Approved        bool     `json:"approved"`
	RiskLevel       string   `json:"risk_level"`
	Reasons         []string `json:"reasons"`
	RequiredChanges []string `json:"required_changes"`
}

type Engine struct {
	Execute        bool
	RoutingMode    string
	Ollama         *OllamaClient
	Gemini         *GeminiClient
	CB             *CircuitBreaker
	SystemPrompt   string
	ApproverPrompt string
}

func NewEngine(cfg config.AppConfig) *Engine {
	return &Engine{
		Execute:        cfg.Execution.ExecuteActions,
		RoutingMode:    cfg.LLM.RoutingMode,
		Ollama:         NewOllamaClient(cfg.LLM),
		Gemini:         NewGeminiClient(cfg.LLM),
		CB:             NewCircuitBreakerWithConfig(cfg.Execution),
		SystemPrompt:   cfg.LLM.SystemPrompt,
		ApproverPrompt: cfg.LLM.ApproverPrompt,
	}
}

func (e *Engine) Healthcheck(ctx context.Context) {
	mode := e.RoutingMode

	needsOllama := mode == "ollama_only" ||
		mode == "ollama_decide_gemini_approve" ||
		mode == "gemini_decide_ollama_approve" ||
		mode == "ollama_primary_gemini_fallback" ||
		mode == "gemini_primary_ollama_fallback"

	needsGemini := mode == "gemini_only" ||
		mode == "ollama_decide_gemini_approve" ||
		mode == "gemini_decide_ollama_approve" ||
		mode == "ollama_primary_gemini_fallback" ||
		mode == "gemini_primary_ollama_fallback"

	if needsOllama {
		if err := e.Ollama.Healthcheck(ctx); err != nil {
			e.CB.SetOllamaHealth(false)
			if mode == "ollama_only" || mode == "ollama_decide_gemini_approve" {
				e.CB.SetObserveOnly("ollama unhealthy: " + err.Error())
			}
		} else {
			e.CB.SetOllamaHealth(true)
		}
	}

	if needsGemini {
		if err := e.Gemini.Healthcheck(ctx); err != nil {
			e.CB.SetGeminiHealth(false)
			if mode == "gemini_only" || mode == "gemini_decide_ollama_approve" {
				e.CB.SetObserveOnly("gemini unhealthy: " + err.Error())
			}
		} else {
			e.CB.SetGeminiHealth(true)
		}
	}
}

func (e *Engine) ObserveOnly() (bool, string) {
	return e.CB.ObserveOnly()
}

func (e *Engine) Snapshot() map[string]any {
	s := e.CB.Snapshot()
	s["routingMode"] = e.RoutingMode
	return s
}

func (e *Engine) Analyze(ctx context.Context, input map[string]any) (*Decision, error) {
	switch e.RoutingMode {
	case "ollama_only":
		return e.analyzeWithOllama(ctx, input)
	case "gemini_only":
		return e.analyzeWithGemini(ctx, input)
	case "ollama_decide_gemini_approve":
		return e.analyzeWithOllama(ctx, input)
	case "gemini_decide_ollama_approve":
		return e.analyzeWithGemini(ctx, input)
	case "ollama_primary_gemini_fallback":
		if d, err := e.analyzeWithOllama(ctx, input); err == nil {
			return d, nil
		}
		e.CB.SetObserveOnly("ollama primary failed; using gemini fallback")
		return e.analyzeWithGemini(ctx, input)
	case "gemini_primary_ollama_fallback":
		if d, err := e.analyzeWithGemini(ctx, input); err == nil {
			return d, nil
		}
		e.CB.SetObserveOnly("gemini primary failed; using ollama fallback")
		return e.analyzeWithOllama(ctx, input)
	default:
		return e.analyzeWithOllama(ctx, input)
	}
}

func (e *Engine) Approve(ctx context.Context, decision *Decision, extra map[string]any) (*Approval, error) {
	switch e.RoutingMode {
	case "ollama_only", "gemini_only":
		if decision.Confidence < 0.70 {
			return &Approval{Approved: false, RiskLevel: "high", Reasons: []string{"confidence < 0.70"}, RequiredChanges: []string{"observe-only"}}, nil
		}
		return &Approval{Approved: true, RiskLevel: "medium", Reasons: []string{"single provider mode"}, RequiredChanges: []string{}}, nil

	case "ollama_decide_gemini_approve":
		return e.approveWithGemini(ctx, decision, extra)

	case "gemini_decide_ollama_approve":
		return e.approveWithOllama(ctx, decision, extra)

	case "ollama_primary_gemini_fallback", "gemini_primary_ollama_fallback":
		if decision.Confidence < 0.70 {
			return &Approval{Approved: false, RiskLevel: "high", Reasons: []string{"confidence < 0.70"}, RequiredChanges: []string{"observe-only"}}, nil
		}
		return &Approval{Approved: true, RiskLevel: "medium", Reasons: []string{"fallback mode"}, RequiredChanges: []string{}}, nil

	default:
		return &Approval{Approved: false, RiskLevel: "high", Reasons: []string{"unknown routing mode"}, RequiredChanges: []string{"observe-only"}}, nil
	}
}

func (e *Engine) analyzeWithOllama(ctx context.Context, input map[string]any) (*Decision, error) {
	startedAt := time.Now()
	raw, err := e.Ollama.Decide(ctx, e.SystemPrompt, input)
	if err != nil {
		appmetrics.RecordLLMRequest("ollama", "decide", "error", time.Since(startedAt))
		e.CB.SetOllamaHealth(false)
		e.CB.SetObserveOnly("ollama decide failed: " + err.Error())
		return fallbackDecision("ollama_failure", err), err
	}
	e.CB.SetOllamaHealth(true)

	var out Decision
	if err := json.Unmarshal([]byte(extractJSON(raw)), &out); err != nil {
		appmetrics.RecordLLMRequest("ollama", "decide", "invalid_json", time.Since(startedAt))
		e.CB.SetObserveOnly("ollama returned invalid json")
		return fallbackDecision("ollama_invalid_json", err), err
	}
	appmetrics.RecordLLMRequest("ollama", "decide", "success", time.Since(startedAt))
	appmetrics.RecordLLMConfidence("ollama", out.Confidence)
	if out.Escalation == nil {
		out.Escalation = map[string]any{"needed": false, "reason": ""}
	}
	return &out, nil
}

func (e *Engine) analyzeWithGemini(ctx context.Context, input map[string]any) (*Decision, error) {
	startedAt := time.Now()
	raw, err := e.Gemini.Decide(ctx, e.SystemPrompt, input)
	if err != nil {
		appmetrics.RecordLLMRequest("gemini", "decide", "error", time.Since(startedAt))
		e.CB.SetGeminiHealth(false)
		e.CB.SetObserveOnly("gemini decide failed: " + err.Error())
		return fallbackDecision("gemini_failure", err), err
	}
	e.CB.SetGeminiHealth(true)

	var out Decision
	if err := json.Unmarshal([]byte(extractJSON(raw)), &out); err != nil {
		appmetrics.RecordLLMRequest("gemini", "decide", "invalid_json", time.Since(startedAt))
		e.CB.SetObserveOnly("gemini returned invalid json")
		return fallbackDecision("gemini_invalid_json", err), err
	}
	appmetrics.RecordLLMRequest("gemini", "decide", "success", time.Since(startedAt))
	appmetrics.RecordLLMConfidence("gemini", out.Confidence)
	if out.Escalation == nil {
		out.Escalation = map[string]any{"needed": false, "reason": ""}
	}
	return &out, nil
}

func (e *Engine) approveWithGemini(ctx context.Context, decision *Decision, extra map[string]any) (*Approval, error) {
	startedAt := time.Now()
	payload := map[string]any{"decision": decision, "context": extra}
	raw, err := e.Gemini.Approve(ctx, e.ApproverPrompt, payload)
	if err != nil {
		appmetrics.RecordLLMRequest("gemini", "approve", "error", time.Since(startedAt))
		e.CB.SetGeminiHealth(false)
		e.CB.SetObserveOnly("gemini approve failed: " + err.Error())
		return &Approval{Approved: false, RiskLevel: "high", Reasons: []string{"gemini approve failed", err.Error()}, RequiredChanges: []string{"observe-only"}}, err
	}
	e.CB.SetGeminiHealth(true)

	var out Approval
	if err := json.Unmarshal([]byte(extractJSON(raw)), &out); err != nil {
		appmetrics.RecordLLMRequest("gemini", "approve", "invalid_json", time.Since(startedAt))
		e.CB.SetObserveOnly("gemini returned invalid approval json")
		return &Approval{Approved: false, RiskLevel: "high", Reasons: []string{"gemini invalid approval json"}, RequiredChanges: []string{"observe-only"}}, err
	}
	appmetrics.RecordLLMRequest("gemini", "approve", "success", time.Since(startedAt))
	return &out, nil
}

func (e *Engine) approveWithOllama(ctx context.Context, decision *Decision, extra map[string]any) (*Approval, error) {
	startedAt := time.Now()
	payload := map[string]any{"decision": decision, "context": extra, "instruction": e.ApproverPrompt}
	raw, err := e.Ollama.Decide(ctx, "", payload)
	if err != nil {
		appmetrics.RecordLLMRequest("ollama", "approve", "error", time.Since(startedAt))
		e.CB.SetOllamaHealth(false)
		e.CB.SetObserveOnly("ollama approve failed: " + err.Error())
		return &Approval{Approved: false, RiskLevel: "high", Reasons: []string{"ollama approve failed", err.Error()}, RequiredChanges: []string{"observe-only"}}, err
	}
	e.CB.SetOllamaHealth(true)

	var out Approval
	if err := json.Unmarshal([]byte(extractJSON(raw)), &out); err != nil {
		appmetrics.RecordLLMRequest("ollama", "approve", "invalid_json", time.Since(startedAt))
		e.CB.SetObserveOnly("ollama returned invalid approval json")
		return &Approval{Approved: false, RiskLevel: "high", Reasons: []string{"ollama invalid approval json"}, RequiredChanges: []string{"observe-only"}}, err
	}
	appmetrics.RecordLLMRequest("ollama", "approve", "success", time.Since(startedAt))
	return &out, nil
}

// extractJSON remove fences de markdown (```json ... ``` ou ``` ... ```) e
// extrai o objeto/array JSON mais externo do texto retornado pelo LLM.
// Isso garante que respostas com prose extra ou code fences ainda sejam parseadas.
func extractJSON(raw string) string {
	s := strings.TrimSpace(raw)

	// Remove fences: ```json ... ``` ou ``` ... ```
	if idx := strings.Index(s, "```"); idx != -1 {
		// pula a linha da abertura
		start := strings.Index(s[idx:], "\n")
		if start != -1 {
			s = s[idx+start+1:]
		}
		// remove a linha do fechamento
		if end := strings.LastIndex(s, "```"); end != -1 {
			s = strings.TrimSpace(s[:end])
		}
	}

	// Encontra o primeiro { ou [ e o último } ou ] correspondente
	first := strings.IndexAny(s, "{[")
	if first == -1 {
		return raw // nada encontrado, devolve original
	}
	open := rune(s[first])
	close := '}'
	if open == '[' {
		close = ']'
	}
	last := strings.LastIndexByte(s, byte(close))
	if last == -1 || last < first {
		return raw
	}
	return strings.TrimSpace(s[first : last+1])
}

func fallbackDecision(classification string, err error) *Decision {
	return &Decision{
		Classification: classification,
		Confidence:     0.0,
		Summary:        "LLM unavailable; forcing observe-only",
		Evidence:       []string{err.Error()},
		PromQueries:    []string{},
		Actions:        []Action{},
		Rollback:       []string{"observe-only until LLM health recovers"},
		Escalation:     map[string]any{"needed": true, "reason": "llm_unavailable"},
	}
}

func WithTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 25*time.Second)
}
