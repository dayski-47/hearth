package auth

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestRequireSessionNoCookie(t *testing.T) {
	m := testManager(t, newFakeQueries())
	h := RequireSession(m, slog.New(slog.NewTextHandler(io.Discard, nil)))(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestRequireSessionBadCookie(t *testing.T) {
	m := testManager(t, newFakeQueries())
	h := RequireSession(m, slog.New(slog.NewTextHandler(io.Discard, nil)))(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "garbage"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestRequireSessionHappyPath(t *testing.T) {
	f := newFakeQueries()
	m := testManager(t, f)
	cookie, _ := m.Create(context.Background(), f.user.ID, "ua", netip.Addr{})

	var seen string
	h := RequireSession(m, slog.New(slog.NewTextHandler(io.Discard, nil)))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if u, ok := UserFromContext(r.Context()); ok {
				seen = u.Username
			}
			w.WriteHeader(http.StatusOK)
		}))
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || seen != "admin" {
		t.Fatalf("code=%d seen=%q", rec.Code, seen)
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}
