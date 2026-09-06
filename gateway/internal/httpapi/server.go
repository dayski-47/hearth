package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/dayski-47/hearth/gateway/internal/agentregistry"
	"github.com/dayski-47/hearth/gateway/internal/auth"
	"github.com/dayski-47/hearth/gateway/internal/config"
	"github.com/dayski-47/hearth/gateway/internal/store"
	"github.com/go-chi/chi/v5"
)

type Server struct {
	cfg    *config.Config
	st     *store.Store
	logger *slog.Logger
	// reg is the live agent registry. Unused by Phase 1 handlers; retained for
	// workspace placement in Plan 2.
	reg  *agentregistry.Registry
	auth *auth.Handlers
}

func New(cfg *config.Config, st *store.Store, logger *slog.Logger, reg *agentregistry.Registry, authH *auth.Handlers) *Server {
	return &Server{cfg: cfg, st: st, logger: logger, reg: reg, auth: authH}
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(RequestID)
	r.Use(AccessLog(s.logger))
	r.Use(Recover(s.logger))
	r.Get("/healthz", HealthHandler(s.st))

	// Login is the one route under /api that cannot require a session. It sits
	// outside the subtree below so that subtree can be authenticated as a
	// whole, and it still takes the CSRF header: it is state-changing and sets
	// a cookie.
	r.With(auth.RequireCSRFHeader).Post("/api/auth/login", s.auth.Login)

	// Everything under /api is authenticated by default. A new route added here
	// is guarded unless it is deliberately registered as a sibling above.
	r.Route("/api", func(r chi.Router) {
		r.Use(auth.RequireCSRFHeader)
		r.Use(s.auth.RequireSession())
		r.Post("/auth/logout", s.auth.Logout)
		r.Get("/auth/me", s.auth.Me)
	})
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
