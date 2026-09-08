package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dayski-47/hearth/gateway/internal/agentregistry"
	"github.com/dayski-47/hearth/gateway/internal/auth"
	"github.com/dayski-47/hearth/gateway/internal/config"
	hv1 "github.com/dayski-47/hearth/gateway/internal/hearth/v1"
	"github.com/dayski-47/hearth/gateway/internal/httpapi"
	"github.com/dayski-47/hearth/gateway/internal/password"
	"github.com/dayski-47/hearth/gateway/internal/store"
	"github.com/dayski-47/hearth/gateway/internal/store/gen"
	"github.com/dayski-47/hearth/gateway/internal/workspaces"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/grpc"
)

// --- stub registry: one ready agent --------------------------------------

type stubRegistry struct{}

func (stubRegistry) Pick(context.Context) (agentregistry.Agent, error) {
	return agentregistry.Agent{ID: "h1", AdvertiseAddr: "unused"}, nil
}

func (stubRegistry) Addr(id string) (string, bool) {
	if id == "h1" {
		return "unused", true
	}
	return "", false
}

// --- fake agent client: scripted responses, no real agent ----------------
//
// startFn / stopFn override the happy-path scripts when set, so a test can
// make a transition fail and reset it afterwards.

type scriptedAgent struct {
	startFn func(*hv1.WorkspaceRef) (*hv1.Workspace, error)
	stopFn  func(*hv1.WorkspaceRef) (*hv1.Workspace, error)
}

func (a *scriptedAgent) CreateWorkspace(_ context.Context, in *hv1.CreateWorkspaceRequest, _ ...grpc.CallOption) (*hv1.Workspace, error) {
	return &hv1.Workspace{WorkspaceId: in.WorkspaceId, State: hv1.WorkspaceState_RUNNING, ContainerId: "cid-1"}, nil
}

func (a *scriptedAgent) StartWorkspace(_ context.Context, in *hv1.WorkspaceRef, _ ...grpc.CallOption) (*hv1.Workspace, error) {
	if a.startFn != nil {
		return a.startFn(in)
	}
	return &hv1.Workspace{WorkspaceId: in.WorkspaceId, State: hv1.WorkspaceState_RUNNING}, nil
}

func (a *scriptedAgent) StopWorkspace(_ context.Context, in *hv1.WorkspaceRef, _ ...grpc.CallOption) (*hv1.Workspace, error) {
	if a.stopFn != nil {
		return a.stopFn(in)
	}
	return &hv1.Workspace{WorkspaceId: in.WorkspaceId, State: hv1.WorkspaceState_STOPPED}, nil
}

func (a *scriptedAgent) DestroyWorkspace(_ context.Context, _ *hv1.WorkspaceRef, _ ...grpc.CallOption) (*hv1.DestroyResponse, error) {
	return &hv1.DestroyResponse{}, nil
}

func (a *scriptedAgent) GetWorkspace(_ context.Context, in *hv1.WorkspaceRef, _ ...grpc.CallOption) (*hv1.Workspace, error) {
	return &hv1.Workspace{WorkspaceId: in.WorkspaceId, State: hv1.WorkspaceState_RUNNING}, nil
}

type stubDialer struct{ agent *scriptedAgent }

func (d stubDialer) Dial(string) (hv1.AgentClient, io.Closer, error) {
	return d.agent, io.NopCloser(nil), nil
}

// --- server + login helpers ---------------------------------------------

func newWorkspaceTestServer(t *testing.T) (*httptest.Server, *http.Cookie, *scriptedAgent) {
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
	// workspaces.agent_id is an FK into agents; the stub registry places on "h1".
	if _, err := st.Queries().UpsertAgent(ctx, gen.UpsertAgentParams{
		ID: "h1", AdvertiseAddr: "unused", Capacity: []byte("{}"),
	}); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{
		ListenAddr:        "127.0.0.1:0",
		PublicURL:         "http://localhost",
		SessionSecret:     []byte("0123456789abcdef0123456789abcdef"),
		AdminUser:         "admin",
		AdminPasswordHash: hash,
		Workspace: config.WorkspaceDefaults{
			Image: "default:latest", Network: "egress", UserNS: "keep-id",
		},
	}
	mgr := auth.NewManager(st.Queries(), cfg.SessionSecret, logger)
	authH := auth.NewHandlers(mgr, st.Queries(), mgr, auth.Config{
		AdminUser: cfg.AdminUser, AdminHash: cfg.AdminPasswordHash, SecureCookie: false,
	}, logger)
	agent := &scriptedAgent{}
	svc := workspaces.NewService(st.Queries(), stubRegistry{}, stubDialer{agent: agent}, cfg.Workspace, logger)

	srv := httptest.NewServer(httpapi.New(cfg, st, logger, authH, svc, nil, nil).Handler())
	t.Cleanup(srv.Close)

	resp := post(t, srv.Client(), srv.URL+"/api/auth/login", `{"username":"admin","password":"correct-horse"}`, "1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
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
	return srv, cookie, agent
}

func TestWorkspaceCRUDOverHTTP(t *testing.T) {
	srv, cookie, agent := newWorkspaceTestServer(t)
	c := srv.Client()

	do := func(method, path, body string, withAuth bool) *http.Response {
		t.Helper()
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, srv.URL+path, r)
		req.Header.Set("Content-Type", "application/json")
		if withAuth {
			req.AddCookie(cookie)
			req.Header.Set("X-Hearth-CSRF", "1")
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	decode := func(resp *http.Response, v any) {
		t.Helper()
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatalf("decode: %v", err)
		}
		_ = resp.Body.Close()
	}

	// POST /api/workspaces {"name":"proj"} -> 201, state running, id set
	resp := do(http.MethodPost, "/api/workspaces", `{"name":"proj"}`, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	var created map[string]any
	decode(resp, &created)
	if created["state"] != "running" {
		t.Fatalf("created state = %v, want running", created["state"])
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("created id empty: %v", created)
	}
	if created["host_id"] != "h1" {
		t.Fatalf("created host_id = %v, want h1", created["host_id"])
	}

	// GET /api/workspaces -> 200, one entry
	resp = do(http.MethodGet, "/api/workspaces", "", true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d", resp.StatusCode)
	}
	var listed struct {
		Workspaces []map[string]any `json:"workspaces"`
	}
	decode(resp, &listed)
	if len(listed.Workspaces) != 1 || listed.Workspaces[0]["id"] != id {
		t.Fatalf("list = %v", listed.Workspaces)
	}

	// GET /api/workspaces/{id} -> 200, same id
	resp = do(http.MethodGet, "/api/workspaces/"+id, "", true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get: %d", resp.StatusCode)
	}
	var got map[string]any
	decode(resp, &got)
	if got["id"] != id {
		t.Fatalf("get id = %v, want %v", got["id"], id)
	}

	// GET /api/workspaces/{randomUUID} -> 404
	resp = do(http.MethodGet, "/api/workspaces/00000000-0000-0000-0000-000000009999", "", true)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get missing: %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// POST /api/workspaces/{id}/start -> 200, state running
	resp = do(http.MethodPost, "/api/workspaces/"+id+"/start", "", true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start: %d", resp.StatusCode)
	}
	var started map[string]any
	decode(resp, &started)
	if started["state"] != "running" {
		t.Fatalf("started state = %v, want running", started["state"])
	}

	// A failed start -> 502 whose body shows state "error", not the stale row.
	agent.startFn = func(*hv1.WorkspaceRef) (*hv1.Workspace, error) {
		return nil, errors.New("agent boom")
	}
	resp = do(http.MethodPost, "/api/workspaces/"+id+"/start", "", true)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("failed start: %d, want 502", resp.StatusCode)
	}
	var failedStart map[string]any
	decode(resp, &failedStart)
	if failedStart["state"] != "error" {
		t.Fatalf("failed start body state = %v, want error", failedStart["state"])
	}
	agent.startFn = nil

	// A failed stop (agent reports ERROR) -> 502, body state "error".
	agent.stopFn = func(in *hv1.WorkspaceRef) (*hv1.Workspace, error) {
		return &hv1.Workspace{WorkspaceId: in.WorkspaceId, State: hv1.WorkspaceState_ERROR, Message: "kaput"}, nil
	}
	resp = do(http.MethodPost, "/api/workspaces/"+id+"/stop", "", true)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("failed stop: %d, want 502", resp.StatusCode)
	}
	var failedStop map[string]any
	decode(resp, &failedStop)
	if failedStop["state"] != "error" {
		t.Fatalf("failed stop body state = %v, want error", failedStop["state"])
	}
	agent.stopFn = nil

	// POST /api/workspaces/{id}/stop -> 200, state stopped
	resp = do(http.MethodPost, "/api/workspaces/"+id+"/stop", "", true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: %d", resp.StatusCode)
	}
	var stopped map[string]any
	decode(resp, &stopped)
	if stopped["state"] != "stopped" {
		t.Fatalf("stopped state = %v, want stopped", stopped["state"])
	}

	// DELETE /api/workspaces/{id} -> 204
	resp = do(http.MethodDelete, "/api/workspaces/"+id, "", true)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// GET /api/workspaces/{id} -> 404 (gone)
	resp = do(http.MethodGet, "/api/workspaces/"+id, "", true)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete: %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// unauthenticated POST (CSRF header present, no cookie) -> 401
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/workspaces", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("X-Hearth-CSRF", "1")
	r, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated create: %d", r.StatusCode)
	}
	_ = r.Body.Close()

	// POST with no CSRF header (cookie present) -> 403
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/workspaces", strings.NewReader(`{"name":"x"}`))
	req.AddCookie(cookie)
	r, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("create without csrf: %d", r.StatusCode)
	}
	_ = r.Body.Close()

	// POST {"name":""} -> 400
	resp = do(http.MethodPost, "/api/workspaces", `{"name":""}`, true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("create blank name: %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}
