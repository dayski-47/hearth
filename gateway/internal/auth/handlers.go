package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/dayski-47/hearth/gateway/internal/password"
	"github.com/dayski-47/hearth/gateway/internal/store/gen"
	"github.com/jackc/pgx/v5/pgtype"
)

// SessionCreator is the part of *Manager the handlers call directly.
type SessionCreator interface {
	Create(ctx context.Context, userID pgtype.UUID, userAgent string, ip netip.Addr) (string, error)
	Destroy(ctx context.Context, cookieValue string)
}

// UserLookup resolves the configured admin username to its row. *gen.Queries
// satisfies it.
type UserLookup interface {
	GetUserByUsername(ctx context.Context, username string) (gen.User, error)
}

type Config struct {
	AdminUser    string
	AdminHash    string
	SecureCookie bool
}

type Handlers struct {
	sc        SessionCreator
	users     UserLookup
	mgr       *Manager
	logger    *slog.Logger
	cfg       Config
	limiter   *loginLimiter
	decoyHash string
}

// NewHandlers wires the auth endpoints. mgr may be nil in unit tests that do
// not exercise RequireSession().
func NewHandlers(sc SessionCreator, users UserLookup, mgr *Manager, cfg Config, logger *slog.Logger) *Handlers {
	// A well-formed hash of a value nobody knows, so a login for an unknown
	// username still runs one argon2 verify and takes the same time as a real
	// one.
	rb := make([]byte, 16)
	_, _ = rand.Read(rb)
	decoy, _ := password.Hash(hex.EncodeToString(rb))
	return &Handlers{
		sc: sc, users: users, mgr: mgr, logger: logger, cfg: cfg,
		limiter:   newLoginLimiter(5, time.Minute),
		decoyHash: decoy,
	}
}

func (h *Handlers) RequireSession() func(http.Handler) http.Handler {
	return RequireSession(h.mgr, h.logger)
}

func (h *Handlers) PruneLimiter() { h.limiter.prune() }

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !h.limiter.allow(ip) {
		writeError(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}

	hash := h.cfg.AdminHash
	if req.Username != h.cfg.AdminUser {
		hash = h.decoyHash
	}
	ok, _ := password.Verify(hash, req.Password)
	if !ok || req.Username != h.cfg.AdminUser {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	u, err := h.users.GetUserByUsername(r.Context(), h.cfg.AdminUser)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "login: admin user row missing", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	cookieValue, err := h.sc.Create(r.Context(), u.ID, r.UserAgent(), parseIP(ip))
	if err != nil {
		h.logger.ErrorContext(r.Context(), "login: create session", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	http.SetCookie(w, h.cookie(cookieValue, int(sessionTTL.Seconds())))
	writeJSON(w, http.StatusOK, map[string]string{"username": u.Username})
}

func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		h.sc.Destroy(r.Context(), c.Value)
	}
	http.SetCookie(w, h.cookie("", -1))
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) Me(w http.ResponseWriter, r *http.Request) {
	u, ok := UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"username": u.Username})
}

func (h *Handlers) cookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     cookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.cfg.SecureCookie,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	}
}

// clientIP prefers the first X-Forwarded-For hop (the gateway sits behind the
// Caddy reverse proxy from deploy/) and falls back to the socket address.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first, _, ok := strings.Cut(xff, ","); ok {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func parseIP(s string) netip.Addr {
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}
	}
	return a
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
