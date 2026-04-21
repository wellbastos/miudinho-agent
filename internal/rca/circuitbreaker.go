package rca

import (
	"sync"
	"time"

	"github.com/wellbastos/miudinho-agent/internal/config"
)

type CircuitBreaker struct {
	mu              sync.Mutex
	observeOnlyTill time.Time
	lastReason      string
	lastOllamaOK    bool
	lastGeminiOK    bool
	ttl             time.Duration
	enabled         bool
}

func NewCircuitBreaker() *CircuitBreaker {
	return NewCircuitBreakerWithConfig(config.LoadFromEnv().Execution)
}

func NewCircuitBreakerWithConfig(cfg config.ExecutionConfig) *CircuitBreaker {
	return &CircuitBreaker{
		lastOllamaOK: true,
		lastGeminiOK: true,
		ttl:          cfg.ObserveOnlyTTL,
		enabled:      cfg.AutoObserveOnly,
	}
}

func (c *CircuitBreaker) SetObserveOnly(reason string) {
	if !c.enabled {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	until := time.Now().Add(c.ttl)
	if until.After(c.observeOnlyTill) {
		c.observeOnlyTill = until
	}
	c.lastReason = reason
}

func (c *CircuitBreaker) ObserveOnly() (bool, string) {
	if !c.enabled {
		return false, ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if time.Now().Before(c.observeOnlyTill) {
		return true, c.lastReason
	}
	return false, ""
}

func (c *CircuitBreaker) SetOllamaHealth(ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastOllamaOK = ok
}

func (c *CircuitBreaker) SetGeminiHealth(ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastGeminiOK = ok
}

func (c *CircuitBreaker) Snapshot() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()

	return map[string]any{
		"observeOnly":      time.Now().Before(c.observeOnlyTill),
		"observeOnlyUntil": c.observeOnlyTill.Format(time.RFC3339),
		"lastReason":       c.lastReason,
		"lastOllamaOK":     c.lastOllamaOK,
		"lastGeminiOK":     c.lastGeminiOK,
	}
}
