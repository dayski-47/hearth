package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/dayski-47/hearth/gateway/internal/auth"
	"github.com/dayski-47/hearth/gateway/internal/config"
	"github.com/dayski-47/hearth/gateway/internal/store"
	"github.com/dayski-47/hearth/gateway/internal/workspaces"
	"github.com/go-chi/chi/v5"
)

type Server struct {
	cfg    *config.Config
	st     *store.Store
	logger *slog.Logger
	auth   *auth.Handlers
	// ws is nil when the server is built without a workspace service; the
	// /workspaces routes are then simply not mounted.
	ws *workspaceHandlers
}

func New(cfg *config.Config, st *store.Store, logger *slog.Logger, authH *auth.Handlers, wsSvc *workspaces.Service) *Server {
	s := &Server{cfg: cfg, st: st, logger: logger, auth: authH}
	if wsSvc != nil {
		s.ws = &workspaceHandlers{svc: wsSvc, logger: logger}
	}
	return s
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
		if s.ws != nil {
			r.Route("/workspaces", func(r chi.Router) {
				r.Post("/", s.ws.create)
				r.Get("/", s.ws.list)
				r.Get("/{id}", s.ws.get)
				r.Post("/{id}/start", s.ws.start)
				r.Post("/{id}/stop", s.ws.stop)
				r.Delete("/{id}", s.ws.destroy)
			})
		}
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
