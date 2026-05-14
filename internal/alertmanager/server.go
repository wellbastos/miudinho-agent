package alertmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/wellbastos/miudinho-agent/internal/config"
	"github.com/wellbastos/miudinho-agent/internal/rca"
)

type Server struct {
	addr   string
	server *http.Server
}

func NewServer(cfg config.AppConfig, handler *Handler, engine *rca.Engine) *Server {
	mux := http.NewServeMux()

	token := cfg.HTTP.WebhookToken
	authMiddleware := newAuthMiddleware(token)

	mux.Handle("/api/v1/alerts", authMiddleware(http.HandlerFunc(handler.HandleAlerts)))
	mux.Handle("/api/v1/test/fake-alert", authMiddleware(http.HandlerFunc(handler.HandleFakeAlert)))

	// /healthz: apenas verifica se o processo está vivo (sem chamadas externas a LLMs)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	// /engine-status: mostra estado do circuit breaker e saúde dos LLMs (não usado como liveness)
	mux.HandleFunc("/engine-status", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := rca.WithTimeout()
		defer cancel()
		engine.Healthcheck(ctx)
		w.Header().Set("Content-Type", "application/json")
		_ = writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"engine": engine.Snapshot(),
		})
	})

	return &Server{
		addr: cfg.HTTP.AlertWebhookAddr,
		server: &http.Server{
			Addr:              cfg.HTTP.AlertWebhookAddr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
	}
}

// newAuthMiddleware retorna um middleware que valida o header Authorization: Bearer <token>.
// Se token estiver vazio, a autenticação é desabilitada (modo compatível com versões anteriores).
func newAuthMiddleware(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token == "" {
				next.ServeHTTP(w, r)
				return
			}
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != token {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s *Server) Start(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.server.Shutdown(shutdownCtx)
	case err := <-errCh:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("webhook server on %s failed: %w", s.addr, err)
	}
}

func (s *Server) NeedLeaderElection() bool {
	return false
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) error {
	w.WriteHeader(statusCode)
	return json.NewEncoder(w).Encode(payload)
}
