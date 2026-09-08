package wsresolve_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dayski-47/hearth/gateway/internal/auth"
	"github.com/dayski-47/hearth/gateway/internal/store"
	"github.com/dayski-47/hearth/gateway/internal/store/gen"
	"github.com/dayski-47/hearth/gateway/internal/wsresolve"
	"github.com/go-chi/chi/v5"
)

type fakeStore struct {
	ws  gen.Workspace
	err error
}

func (f fakeStore) GetWorkspaceForOwner(context.Context, gen.GetWorkspaceForOwnerParams) (gen.Workspace, error) {
	return f.ws, f.err
}

type fakeReg struct {
	addr string
	ok   bool
}

func (f fakeReg) WorkspaceAddr(string) (string, bool) { return f.addr, f.ok }

func ptr(s string) *string { return &s }

func run(t *testing.T, rv wsresolve.Resolver, withUser bool, id string, allowed ...string) int {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/{id}", func(w http.ResponseWriter, req *http.Request) {
		_, _, ok := rv.Resolve(w, req, allowed...)
		if ok {
			w.WriteHeader(http.StatusOK)
		}
	})
	req := httptest.NewRequest(http.MethodGet, "/"+id, nil)
	if withUser {
		uid, _ := store.ParseUUID("11111111-1111-1111-1111-111111111111")
		req = req.WithContext(auth.ContextWithUser(req.Context(), &gen.User{ID: uid}))
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec.Code
}

func TestResolve(t *testing.T) {
	uid, _ := store.ParseUUID("11111111-1111-1111-1111-111111111111")
	running := gen.Workspace{ID: uid, State: "running", AgentID: ptr("h1")}

	okRV := wsresolve.Resolver{Store: fakeStore{ws: running}, Reg: fakeReg{addr: "https://h1:9092", ok: true}}

	if got := run(t, okRV, false, "11111111-1111-1111-1111-111111111111", "running"); got != http.StatusUnauthorized {
		t.Fatalf("no user: got %d", got)
	}
	if got := run(t, okRV, true, "not-a-uuid", "running"); got != http.StatusBadRequest {
		t.Fatalf("bad id: got %d", got)
	}
	if got := run(t, wsresolve.Resolver{Store: fakeStore{err: errors.New("db down")}, Reg: fakeReg{}}, true,
		"11111111-1111-1111-1111-111111111111", "running"); got != http.StatusNotFound {
		t.Fatalf("store error: got %d", got)
	}
	if got := run(t, okRV, true, "11111111-1111-1111-1111-111111111111", "stopped"); got != http.StatusConflict {
		t.Fatalf("wrong state: got %d", got)
	}
	if got := run(t, wsresolve.Resolver{Store: fakeStore{ws: running}, Reg: fakeReg{ok: false}}, true,
		"11111111-1111-1111-1111-111111111111", "running"); got != http.StatusServiceUnavailable {
		t.Fatalf("host down: got %d", got)
	}
	if got := run(t, okRV, true, "11111111-1111-1111-1111-111111111111", "running"); got != http.StatusOK {
		t.Fatalf("happy path: got %d", got)
	}
}
