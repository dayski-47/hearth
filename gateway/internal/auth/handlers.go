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
	"slices"
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

// maxVerifyInFlight caps concurrent argon2id verifications. Each one costs
// 64 MiB, so an unauthenticated flood would otherwise exhaust the gateway's
// memory before any rate limit could matter.
const maxVerifyInFlight = 4

// verifyWait is how long a login waits for a verify slot before shedding load.
const verifyWait = 2 * time.Second

type Config struct {
	AdminUser    string
	AdminHash    string
	SecureCookie bool
	// TrustedProxies are the peers whose X-Forwarded-For header is believed.
	TrustedProxies []netip.Prefix
}

type Handlers struct {
	sc        SessionCreator
	users     UserLookup
	mgr       *Manager
	logger    *slog.Logger
	cfg       Config
	limiter   *loginLimiter
	decoyHash string
	// verifySem bounds concurrent argon2 work; verifyWait is how long a request
	// waits for a slot. Both are constants outside tests.
	verifySem  chan struct{}
	verifyWait time.Duration
}

// NewHandlers wires the auth endpoints. mgr may be nil in unit tests that do
// not exercise RequireSession().
func NewHandlers(sc SessionCreator, users UserLookup, mgr *Manager, cfg Config, logger *slog.Logger) *Handlers {
	// A well-formed hash of a value nobody knows, so a login for an unknown
	// username still runs one argon2 verify and takes the same time as a real
	// one.
	rb := make([]byte, 16)
	if _, err := rand.Read(rb); err != nil {
		panic("auth: read random bytes for the decoy hash: " + err.Error())
	}
	decoy, err := password.Hash(hex.EncodeToString(rb))
	if err != nil {
		panic("auth: build the decoy hash: " + err.Error())
	}
	return &Handlers{
		sc: sc, users: users, mgr: mgr, logger: logger, cfg: cfg,
		limiter:    newLoginLimiter(5, time.Minute),
		decoyHash:  decoy,
		verifySem:  make(chan struct{}, maxVerifyInFlight),
		verifyWait: verifyWait,
	}
}

// RequireSession authenticates the session cookie, attaches the user to the
// request context, and re-issues the cookie with a fresh Max-Age. The session
// row slides server-side on every request, so without this the browser would
// drop the cookie a fixed sessionTTL after login however active the user was.
func (h *Handlers) RequireSession() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(cookieName)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			u, err := h.mgr.Authenticate(r.Context(), c.Value)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			// The session id is stable, so the same value is re-sent; only the
			// expiry moves. Skip re-issue on logout — it will clear the cookie.
			if !strings.HasSuffix(r.URL.Path, "/auth/logout") {
				http.SetCookie(w, h.cookie(c.Value, int(sessionTTL.Seconds())))
			}
			next.ServeHTTP(w, r.WithContext(ContextWithUser(r.Context(), u)))
		})
	}
}

func (h *Handlers) PruneLimiter() { h.limiter.prune() }

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r, h.cfg.TrustedProxies)
	// NOTE: a 503 from the verify cap still spends a rate-limit token;
	// acceptable — the alternative lets an attacker probe cap state for free.
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
	select {
	case h.verifySem <- struct{}{}:
	case <-time.After(h.verifyWait):
		h.logger.WarnContext(r.Context(), "login: verify capacity exhausted", "client_ip", ip)
		writeError(w, http.StatusServiceUnavailable, "busy")
		return
	}
	ok, err := func() (bool, error) {
		defer func() { <-h.verifySem }()
		return password.Verify(hash, req.Password)
	}()
	if err != nil {
		// Startup validates the configured hash, so this only fires for the
		// decoy or a hash changed underneath a running gateway. Either way it
		// would otherwise be an unexplained wall of 401s.
		h.logger.ErrorContext(r.Context(), "login: password verify error", "error", err)
	}
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

// clientIP identifies the caller for rate limiting and session records.
//
// X-Forwarded-For is only believed when the socket peer is one of the
// configured trusted proxies, and then only its rightmost entry — the hop that
// proxy itself observed. Everything to the left is supplied by the client and
// would otherwise let one attacker mint a fresh limiter key per request.
func clientIP(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if len(trusted) == 0 {
		return host
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap().WithZone("")
	if !slices.ContainsFunc(trusted, func(p netip.Prefix) bool { return p.Contains(peer) }) {
		return host
	}
	xff := r.Header.Get("X-Forwarded-For")
	if i := strings.LastIndexByte(xff, ','); i >= 0 {
		xff = xff[i+1:]
	}
	if v := strings.TrimSpace(xff); v != "" {
		// NOTE: with a chain of trusted proxies this is the innermost proxy,
		// so clients behind it share one limiter key (over-limiting, never
		// bypass).
		if _, err := netip.ParseAddr(v); err != nil {
			return host // the socket peer; a garbled forwarded hop is not a key
		}
		return v
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
