package auth

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/dayski-47/hearth/gateway/internal/store/gen"
)

const cookieName = "hearth_session"

type ctxKey int

const userKey ctxKey = 0

// UserFromContext returns the user attached by RequireSession.
func UserFromContext(ctx context.Context) (*gen.User, bool) {
	u, ok := ctx.Value(userKey).(*gen.User)
	return u, ok
}

// RequireSession authenticates the hearth_session cookie and attaches the user
// to the request context, or responds 401.
func RequireSession(m *Manager, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(cookieName)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			u, err := m.Authenticate(r.Context(), c.Value)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
		})
	}
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
}
