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

// Handlers.RequireSession must re-issue the cookie so the browser's copy keeps
// pace with the rolling expiry the session row already gets.
func TestHandlersRequireSessionReissuesCookie(t *testing.T) {
	f := newFakeQueries()
	m := testManager(t, f)
	cookie, _ := m.Create(context.Background(), f.user.ID, "ua", netip.Addr{})
	h := testHandlers(t, &fakeSessions{})
	h.mgr = m
	h.cfg.SecureCookie = true

	var seen string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, ok := UserFromContext(r.Context()); ok {
			seen = u.Username
		}
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	rec := httptest.NewRecorder()
	h.RequireSession()(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || seen != "admin" {
		t.Fatalf("code=%d seen=%q", rec.Code, seen)
	}
	cs := rec.Result().Cookies()
	if len(cs) != 1 {
		t.Fatalf("want exactly one Set-Cookie, got %+v", cs)
	}
	got := cs[0]
	if got.Name != cookieName || got.Value != cookie {
		t.Fatalf("the session value must be re-sent unchanged: %+v", got)
	}
	if got.MaxAge != int(sessionTTL.Seconds()) {
		t.Fatalf("MaxAge = %d, want %d", got.MaxAge, int(sessionTTL.Seconds()))
	}
	if !got.HttpOnly || !got.Secure || got.SameSite != http.SameSiteLaxMode {
		t.Fatalf("re-issued cookie lost its flags: %+v", got)
	}
}

func TestHandlersRequireSessionRejectsBadCookie(t *testing.T) {
	h := testHandlers(t, &fakeSessions{})
	h.mgr = testManager(t, newFakeQueries())
	for _, tc := range []struct {
		name   string
		cookie *http.Cookie
	}{
		{"no cookie", nil},
		{"garbage cookie", &http.Cookie{Name: cookieName, Value: "garbage"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
			if tc.cookie != nil {
				req.AddCookie(tc.cookie)
			}
			rec := httptest.NewRecorder()
			h.RequireSession()(okHandler()).ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("got %d", rec.Code)
			}
			if len(rec.Result().Cookies()) != 0 {
				t.Fatal("no cookie should be issued on a rejected request")
			}
		})
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}
