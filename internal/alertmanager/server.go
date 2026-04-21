package alertmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/wellbastos/miudinho-agent/internal/config"
	"github.com/wellbastos/miudinho-agent/internal/rca"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
)

type Server struct {
	addr   string
	server *http.Server
}

func NewServer(cfg config.AppConfig, handler *Handler, engine *rca.Engine) *Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/alerts", handler.HandleAlerts)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := rca.WithTimeout()
		defer cancel()
		engine.Healthcheck(ctx)
		if err := healthz.Ping(r); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
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
