package auth

import "net/http"

// RequireCSRFHeader rejects state-changing requests that do not carry the
// X-Hearth-CSRF header. A cross-site form cannot set a custom header, and the
// SameSite=Lax cookie covers top-level navigations.
func RequireCSRFHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if r.Header.Get("X-Hearth-CSRF") == "" {
				writeError(w, http.StatusForbidden, "missing csrf header")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
