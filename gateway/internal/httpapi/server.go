package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/dayski-47/hearth/gateway/internal/config"
	"github.com/dayski-47/hearth/gateway/internal/store"
	"github.com/go-chi/chi/v5"
)

type Server struct {
	cfg    *config.Config
	st     *store.Store
	logger *slog.Logger
}

func New(cfg *config.Config, st *store.Store, logger *slog.Logger) *Server {
	return &Server{cfg: cfg, st: st, logger: logger}
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(RequestID)
	r.Use(AccessLog(s.logger))
	r.Use(Recover(s.logger))
	r.Get("/healthz", HealthHandler(s.st))
	return r
}

func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.ListenAddr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	s.logger.Info("gateway http listening", "addr", s.cfg.ListenAddr)
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
