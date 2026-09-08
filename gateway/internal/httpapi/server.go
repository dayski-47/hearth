package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/dayski-47/hearth/gateway/internal/auth"
	"github.com/dayski-47/hearth/gateway/internal/config"
	"github.com/dayski-47/hearth/gateway/internal/store"
	"github.com/dayski-47/hearth/gateway/internal/workspaces"
	"github.com/dayski-47/hearth/gateway/internal/ws"
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
	// term is nil when the server is built without the terminal bridge; the
	// /workspaces/{id}/terminal route is then simply not mounted.
	term *ws.Deps
	// files is nil when the server is built without the workspace file surface;
	// the /workspaces/{id}/files* routes are then simply not mounted.
	files *FileDeps
}

func New(cfg *config.Config, st *store.Store, logger *slog.Logger, authH *auth.Handlers, wsSvc *workspaces.Service, term *ws.Deps, files *FileDeps) *Server {
	s := &Server{cfg: cfg, st: st, logger: logger, auth: authH, term: term, files: files}
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
				if s.term != nil {
					r.Get("/{id}/terminal", s.term.Terminal)
					r.Get("/{id}/events", s.term.Events)
				}
				if s.files != nil {
					r.Get("/{id}/files", s.files.list)
					r.Get("/{id}/files/content", s.files.readContent)
					r.Put("/{id}/files/content", s.files.writeContent)
					r.Post("/{id}/files", s.files.create)
					r.Post("/{id}/files/rename", s.files.rename)
					r.Delete("/{id}/files", s.files.del)
				}
			})
		}
	})

	webDir := s.cfg.WebDir
	if webDir == "" {
		webDir = "web"
	}
	fs := http.FileServer(http.Dir(webDir))
	r.Handle("/*", spaFallback(webDir, fs))

	return r
}

// spaFallback serves the requested path if it names a real file under dir,
// otherwise index.html, so a browser refresh on a client-side route still
// loads the app. Chi matches /healthz and /api/* first, so those never reach
// here.
func spaFallback(dir string, fs http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			fs.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(dir, "index.html"))
	}
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
