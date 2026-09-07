package httpapi_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dayski-47/hearth/gateway/internal/auth"
	"github.com/dayski-47/hearth/gateway/internal/config"
	"github.com/dayski-47/hearth/gateway/internal/httpapi"
	"github.com/dayski-47/hearth/gateway/internal/password"
	"github.com/dayski-47/hearth/gateway/internal/store"
	"github.com/dayski-47/hearth/gateway/internal/store/gen"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16",
		tcpostgres.WithDatabase("hearth"), tcpostgres.WithUsername("hearth"), tcpostgres.WithPassword("hearth"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Terminate(ctx) })
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx, nil); err != nil {
		t.Fatal(err)
	}

	hash, err := password.Hash("correct-horse")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Queries().UpsertUser(ctx, gen.UpsertUserParams{Username: "admin", PasswordHash: hash}); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{
		ListenAddr:        "127.0.0.1:0",
		PublicURL:         "http://localhost",
		SessionSecret:     []byte("0123456789abcdef0123456789abcdef"),
		AdminUser:         "admin",
		AdminPasswordHash: hash,
	}
	mgr := auth.NewManager(st.Queries(), cfg.SessionSecret, logger)
	authH := auth.NewHandlers(mgr, st.Queries(), mgr, auth.Config{
		AdminUser: cfg.AdminUser, AdminHash: cfg.AdminPasswordHash, SecureCookie: false,
	}, logger)

	srv := httptest.NewServer(httpapi.New(cfg, st, logger, nil, authH, nil).Handler())
	t.Cleanup(srv.Close)
	return srv, hash
}

func TestAuthEndToEnd(t *testing.T) {
	srv, _ := newTestServer(t)
	c := srv.Client()

	// login without the CSRF header -> 403, before any credential is checked
	resp := post(t, c, srv.URL+"/api/auth/login", `{"username":"admin","password":"correct-horse"}`, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("login without csrf: %d", resp.StatusCode)
	}

	// wrong password -> 401
	resp = post(t, c, srv.URL+"/api/auth/login", `{"username":"admin","password":"nope"}`, "1")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad login: %d", resp.StatusCode)
	}

	// correct -> 200 + cookie
	resp = post(t, c, srv.URL+"/api/auth/login", `{"username":"admin","password":"correct-horse"}`, "1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("good login: %d", resp.StatusCode)
	}
	var cookie *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == "hearth_session" {
			cookie = ck
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie")
	}

	// me without cookie -> 401
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/auth/me", nil)
	r, _ := c.Do(req)
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me unauthenticated: %d", r.StatusCode)
	}

	// any other /api path is guarded too: unauthenticated gets 401, not 404
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/not-a-route-yet", nil)
	r, _ = c.Do(req)
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/api is not deny-by-default: %d", r.StatusCode)
	}

	// me with cookie -> 200
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/auth/me", nil)
	req.AddCookie(cookie)
	r, _ = c.Do(req)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("me authenticated: %d", r.StatusCode)
	}

	// logout without CSRF header -> 403
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/auth/logout", nil)
	req.AddCookie(cookie)
	r, _ = c.Do(req)
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("logout without csrf: %d", r.StatusCode)
	}

	// logout with CSRF header -> 204
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/auth/logout", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-Hearth-CSRF", "1")
	r, _ = c.Do(req)
	if r.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: %d", r.StatusCode)
	}

	// cookie is now dead -> me 401
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/auth/me", nil)
	req.AddCookie(cookie)
	r, _ = c.Do(req)
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me after logout: %d", r.StatusCode)
	}
}

func post(t *testing.T, c *http.Client, url, body, csrf string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		req.Header.Set("X-Hearth-CSRF", csrf)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
