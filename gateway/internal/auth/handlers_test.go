package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/dayski-47/hearth/gateway/internal/password"
	"github.com/dayski-47/hearth/gateway/internal/store/gen"
	"github.com/jackc/pgx/v5/pgtype"
)

type fakeSessions struct {
	created   int
	destroyed int
	value     string
	err       error
}

func (f *fakeSessions) Create(_ context.Context, _ pgtype.UUID, _ string, _ netip.Addr) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.created++
	return f.value, nil
}
func (f *fakeSessions) Destroy(_ context.Context, _ string) { f.destroyed++ }

type fakeUsers struct{ u gen.User }

func (f fakeUsers) GetUserByUsername(_ context.Context, name string) (gen.User, error) {
	if name != f.u.Username {
		return gen.User{}, context.Canceled
	}
	return f.u, nil
}

func testHandlers(t *testing.T, sc SessionCreator) *Handlers {
	t.Helper()
	hash, err := password.Hash("correct-horse")
	if err != nil {
		t.Fatal(err)
	}
	uid := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	users := fakeUsers{u: gen.User{ID: uid, Username: "admin", PasswordHash: hash}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewHandlers(sc, users, nil, Config{AdminUser: "admin", AdminHash: hash}, logger)
}

func TestLoginRejectsBadPassword(t *testing.T) {
	sc := &fakeSessions{value: "cookie-value"}
	h := testHandlers(t, sc)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"wrong"}`))
	h.Login(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", rec.Code)
	}
	if sc.created != 0 {
		t.Fatal("no session should be created")
	}
}

func TestLoginRejectsUnknownUser(t *testing.T) {
	h := testHandlers(t, &fakeSessions{value: "x"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"root","password":"correct-horse"}`))
	h.Login(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestLoginSuccessSetsCookie(t *testing.T) {
	sc := &fakeSessions{value: "signed-cookie"}
	h := testHandlers(t, sc)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"correct-horse"}`))
	h.Login(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != cookieName || cookies[0].Value != "signed-cookie" {
		t.Fatalf("cookie not set: %+v", cookies)
	}
	if !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie flags wrong: %+v", cookies[0])
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["username"] != "admin" {
		t.Fatalf("body: %v", body)
	}
}

func TestLoginRateLimited(t *testing.T) {
	h := testHandlers(t, &fakeSessions{value: "x"})
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"wrong"}`))
		req.RemoteAddr = "10.0.0.9:1234"
		h.Login(rec, req)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"wrong"}`))
	req.RemoteAddr = "10.0.0.9:1234"
	h.Login(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("got %d, want 429", rec.Code)
	}
}

// A spoofed X-Forwarded-For must not mint a fresh limiter key per request when
// the peer is not a trusted proxy: that is the login rate limit bypass.
func TestLoginIgnoresUntrustedForwardedFor(t *testing.T) {
	h := testHandlers(t, &fakeSessions{value: "x"})
	var last int
	for i := 0; i < 20; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"wrong"}`))
		req.RemoteAddr = "203.0.113.7:5555"
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("10.9.%d.%d", i, i))
		h.Login(rec, req)
		last = rec.Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("last of 20 spoofed-XFF attempts got %d, want 429", last)
	}
	if len(h.limiter.hits) != 1 {
		t.Fatalf("limiter tracked %d keys, want 1 (the peer address)", len(h.limiter.hits))
	}
}

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("192.168.1.5/32")}
	cases := []struct {
		name    string
		remote  string
		xff     string
		trusted []netip.Prefix
		want    string
	}{
		{"no trusted proxies ignores xff", "203.0.113.7:5555", "1.2.3.4", nil, "203.0.113.7"},
		{"untrusted peer ignores xff", "203.0.113.7:5555", "1.2.3.4", trusted, "203.0.113.7"},
		{"trusted peer takes the rightmost hop", "10.1.2.3:5555", "1.2.3.4, 5.6.7.8", trusted, "5.6.7.8"},
		{"trusted peer with a single hop", "192.168.1.5:5555", "  5.6.7.8  ", trusted, "5.6.7.8"},
		{"trusted peer without xff falls back", "10.1.2.3:5555", "", trusted, "10.1.2.3"},
		{"trusted peer with an empty last hop falls back", "10.1.2.3:5555", "1.2.3.4,   ", trusted, "10.1.2.3"},
		{"malformed remote addr", "not-an-addr", "1.2.3.4", trusted, "not-an-addr"},
		{"ipv6 trusted peer", "[::1]:5555", "9.9.9.9", []netip.Prefix{netip.MustParsePrefix("::1/128")}, "9.9.9.9"},
		{"trusted peer with invalid forwarded ip falls back to socket peer", "10.1.2.3:5555", "not-an-ip", trusted, "10.1.2.3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
			req.RemoteAddr = tc.remote
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := clientIP(req, tc.trusted); got != tc.want {
				t.Fatalf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

// With every verify slot held, a further login sheds load instead of queueing
// another 64 MiB argon2 allocation.
func TestLoginShedsLoadWhenVerifySlotsAreFull(t *testing.T) {
	h := testHandlers(t, &fakeSessions{value: "x"})
	h.verifyWait = 10 * time.Millisecond
	if cap(h.verifySem) != maxVerifyInFlight {
		t.Fatalf("semaphore cap = %d, want %d", cap(h.verifySem), maxVerifyInFlight)
	}
	for i := 0; i < maxVerifyInFlight; i++ {
		h.verifySem <- struct{}{}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"correct-horse"}`))
	req.RemoteAddr = "10.0.0.9:1"
	h.Login(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503 when all verify slots are held", rec.Code)
	}
}

// A completed login must hand its verify slot back.
func TestLoginReleasesVerifySlot(t *testing.T) {
	h := testHandlers(t, &fakeSessions{value: "x"})
	for _, pw := range []string{"wrong", "correct-horse"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"`+pw+`"}`))
		req.RemoteAddr = "10.0.0.9:1"
		h.Login(rec, req)
		if len(h.verifySem) != 0 {
			t.Fatalf("slot still held after a %q login", pw)
		}
	}
}

func TestLogoutClearsCookie(t *testing.T) {
	sc := &fakeSessions{}
	h := testHandlers(t, sc)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "whatever"})
	h.Logout(rec, req)
	if rec.Code != http.StatusNoContent || sc.destroyed != 1 {
		t.Fatalf("code=%d destroyed=%d", rec.Code, sc.destroyed)
	}
	c := rec.Result().Cookies()
	if len(c) != 1 || c[0].MaxAge >= 0 {
		t.Fatalf("cookie not cleared: %+v", c)
	}
}

func TestLogoutWithMiddlewareOnlyClears(t *testing.T) {
	f := newFakeQueries()
	m := testManager(t, f)
	cookie, _ := m.Create(context.Background(), f.user.ID, "ua", netip.Addr{})
	sc := &fakeSessions{}
	h := testHandlers(t, sc)
	h.mgr = m

	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	rec := httptest.NewRecorder()
	logoutHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.Logout(w, r)
	})
	h.RequireSession()(logoutHandler).ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("code=%d", rec.Code)
	}
	c := rec.Result().Cookies()
	if len(c) != 1 {
		t.Fatalf("want exactly one Set-Cookie, got %d: %+v", len(c), c)
	}
	if c[0].MaxAge >= 0 {
		t.Fatalf("cookie should be cleared (MaxAge < 0), got %d", c[0].MaxAge)
	}
}

func TestMeReturnsContextUser(t *testing.T) {
	h := testHandlers(t, &fakeSessions{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	ctx := context.WithValue(req.Context(), userKey, &gen.User{Username: "admin"})
	h.Me(rec, req.WithContext(ctx))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "admin") {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}
