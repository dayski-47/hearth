package auth

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

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
