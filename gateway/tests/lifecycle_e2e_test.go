// Package e2e holds the gated end-to-end proof that the whole workspace
// lifecycle stack works: a real hearth-agent against real rootless Podman, a
// real gateway (gRPC + HTTP) in-process, and the REST API driving a busybox
// workspace whose container and volume are then observed directly with Podman.
//
// It runs ONLY when HEARTH_E2E=1 and Podman is reachable at
// HEARTH_PODMAN_SOCKET. Without the gate it t.Skips and never fails, so the
// default `go test ./...` stays hermetic.
package e2e

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dayski-47/hearth/gateway/internal/agentclient"
	"github.com/dayski-47/hearth/gateway/internal/agentregistry"
	"github.com/dayski-47/hearth/gateway/internal/auth"
	"github.com/dayski-47/hearth/gateway/internal/config"
	"github.com/dayski-47/hearth/gateway/internal/grpcserver"
	hv1 "github.com/dayski-47/hearth/gateway/internal/hearth/v1"
	"github.com/dayski-47/hearth/gateway/internal/httpapi"
	"github.com/dayski-47/hearth/gateway/internal/password"
	"github.com/dayski-47/hearth/gateway/internal/store"
	"github.com/dayski-47/hearth/gateway/internal/store/gen"
	"github.com/dayski-47/hearth/gateway/internal/tlsutil"
	"github.com/dayski-47/hearth/gateway/internal/workspaces"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	adminUser = "admin"
	adminPass = "correct-horse-battery-staple"
	e2eHostID = "e2e"
	wsImage   = "docker.io/library/busybox:stable"
)

// repoRoot is two levels up from gateway/tests.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

// freePort asks the kernel for an unused 127.0.0.1 TCP port and returns it as a
// host:port string. There is a small window between close and re-bind, which is
// acceptable for a gated local test.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// syncBuf is an io.Writer safe for concurrent writes from the agent's stdout
// and stderr pipes.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// podmanRunner shells out to the podman CLI pinned to the test's socket.
type podmanRunner struct {
	t      *testing.T
	socket string
}

func (p podmanRunner) run(args ...string) (string, error) {
	cmd := exec.Command("podman", args...)
	cmd.Env = append(os.Environ(), "CONTAINER_HOST=unix://"+p.socket)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (p podmanRunner) mustRun(args ...string) string {
	p.t.Helper()
	out, err := p.run(args...)
	if err != nil {
		p.t.Fatalf("podman %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func TestLifecycleE2E(t *testing.T) {
	if os.Getenv("HEARTH_E2E") != "1" {
		t.Skip("skipping end-to-end lifecycle test: set HEARTH_E2E=1 (and HEARTH_PODMAN_SOCKET) to run")
	}
	socket := os.Getenv("HEARTH_PODMAN_SOCKET")
	if socket == "" {
		t.Skip("skipping end-to-end lifecycle test: HEARTH_PODMAN_SOCKET is required (the host also runs Docker)")
	}
	if _, err := os.Stat(socket); err != nil {
		t.Skipf("skipping end-to-end lifecycle test: podman socket %s not reachable: %v", socket, err)
	}
	root := repoRoot(t)
	agentBin := filepath.Join(root, "target", "debug", "hearth-agent")
	if _, err := os.Stat(agentBin); err != nil {
		t.Skipf("skipping end-to-end lifecycle test: agent binary %s not built: %v", agentBin, err)
	}

	podman := podmanRunner{t: t, socket: socket}
	if out, err := podman.run("info", "--format", "{{.Host.RemoteSocket.Path}}"); err != nil {
		t.Skipf("skipping end-to-end lifecycle test: podman info failed: %v\n%s", err, out)
	}

	ctx := context.Background()

	// --- 1. Postgres + migrations + admin ---------------------------------
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
	hash, err := password.Hash(adminPass)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Queries().UpsertUser(ctx, gen.UpsertUserParams{Username: adminUser, PasswordHash: hash}); err != nil {
		t.Fatal(err)
	}

	// --- 2. Fresh CA + leaf certs ---------------------------------------
	certsDir := filepath.Join(root, "deploy", "certs")
	genCA := exec.Command("bash", filepath.Join(certsDir, "gen-ca.sh"))
	genCA.Dir = certsDir
	if out, err := genCA.CombinedOutput(); err != nil {
		t.Fatalf("gen-ca.sh: %v\n%s", err, out)
	}
	caPEM := filepath.Join(certsDir, "ca.pem")
	gatewayCert := filepath.Join(certsDir, "gateway.pem")
	gatewayKey := filepath.Join(certsDir, "gateway-key.pem")
	agentCert := filepath.Join(certsDir, "agent.pem")
	agentKey := filepath.Join(certsDir, "agent-key.pem")

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// --- 3. Gateway gRPC server (in-process) ----------------------------
	reg := agentregistry.NewInMemory()
	grpcTLS, err := tlsutil.ServerConfig(caPEM, gatewayCert, gatewayKey)
	if err != nil {
		t.Fatal(err)
	}
	gs := grpcserver.New(reg, storePersistence{st: st}, grpcTLS, logger)
	grpcLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = gs.Serve(grpcLis) }()
	t.Cleanup(gs.Stop)
	gatewayGRPCAddr := grpcLis.Addr().String()

	// --- 4. Gateway HTTP server (in-process) ---------------------------
	agentTLS, err := tlsutil.ClientConfig(caPEM, gatewayCert, gatewayKey, "hearth-agent")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		ListenAddr:        "127.0.0.1:0",
		PublicURL:         "http://localhost",
		SessionSecret:     []byte("0123456789abcdef0123456789abcdef"),
		AdminUser:         adminUser,
		AdminPasswordHash: hash,
		Workspace: config.WorkspaceDefaults{
			Image:       wsImage,
			Network:     "none", // avoids needing the hearth-egress network
			CPUMillis:   1000,
			MemoryBytes: 256 << 20,
			Pids:        128,
			DiskBytes:   0,
			UserNS:      "keep-id",
		},
	}
	mgr := auth.NewManager(st.Queries(), cfg.SessionSecret, logger)
	authH := auth.NewHandlers(mgr, st.Queries(), mgr, auth.Config{
		AdminUser: cfg.AdminUser, AdminHash: cfg.AdminPasswordHash, SecureCookie: false,
	}, logger)
	dialer := agentDialer{tls: agentTLS}
	wsSvc := workspaces.NewService(st.Queries(), reg, dialer, cfg.Workspace, logger)
	httpSrv := httptest.NewServer(httpapi.New(cfg, st, logger, authH, wsSvc, nil).Handler())
	t.Cleanup(httpSrv.Close)

	// --- 5. Real hearth-agent against real Podman ---------------------
	agentAddr := freePort(t)
	agentLog := &syncBuf{}
	agentCmd := exec.Command(agentBin)
	agentCmd.Env = append(os.Environ(),
		"HEARTH_HOST_ID="+e2eHostID,
		"HEARTH_AGENT_GRPC_LISTEN_ADDR="+agentAddr,
		"HEARTH_AGENT_ADDR=https://"+agentAddr,
		"HEARTH_GATEWAY_GRPC_ADDR=https://"+gatewayGRPCAddr,
		"HEARTH_WORKSPACE_ADDR=https://127.0.0.1:1",
		"HEARTH_TLS_CA="+caPEM,
		"HEARTH_AGENT_TLS_CERT="+agentCert,
		"HEARTH_AGENT_TLS_KEY="+agentKey,
		"HEARTH_PODMAN_SOCKET="+socket,
		"RUST_LOG=info",
	)
	agentCmd.Stdout = agentLog
	agentCmd.Stderr = agentLog
	if err := agentCmd.Start(); err != nil {
		t.Fatalf("start agent: %v", err)
	}
	t.Cleanup(func() {
		if agentCmd.Process != nil {
			_ = agentCmd.Process.Kill()
			_, _ = agentCmd.Process.Wait()
		}
		t.Logf("---- hearth-agent log ----\n%s\n--------------------------", agentLog.String())
	})

	// Belt-and-braces sweep, registered before the create call: the agent may
	// have made a container/volume even if a later assertion fails before the
	// workspace-specific cleanup below is registered.
	t.Cleanup(func() {
		out, _ := podman.run("ps", "-aq", "--filter", "label=hearth.workspace")
		for _, id := range strings.Fields(out) {
			_, _ = podman.run("rm", "-f", id)
		}
		out, _ = podman.run("volume", "ls", "--filter", "name=hearth-ws-", "--format", "{{.Name}}")
		for _, v := range strings.Fields(out) {
			_, _ = podman.run("volume", "rm", "-f", v)
		}
	})

	// --- 6. Wait for the agent to register as ready ------------------
	deadline := time.Now().Add(90 * time.Second)
	for {
		ready := false
		for _, a := range reg.List() {
			if a.ID == e2eHostID && a.Status == "ready" {
				ready = true
			}
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent did not register within timeout\n---- agent log ----\n%s", agentLog.String())
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Log("agent registered as ready")

	// --- 7. HTTP client helpers -----------------------------------
	client := httpSrv.Client()
	var sessionCookie *http.Cookie
	do := func(method, path, body string) *http.Response {
		t.Helper()
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, httpSrv.URL+path, r)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Hearth-CSRF", "1")
		if sessionCookie != nil {
			req.AddCookie(sessionCookie)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		return resp
	}
	decode := func(resp *http.Response) map[string]any {
		t.Helper()
		var m map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		_ = resp.Body.Close()
		return m
	}

	// --- 8. Login -----------------------------------------------
	resp := do(http.MethodPost, "/api/auth/login",
		`{"username":"`+adminUser+`","password":"`+adminPass+`"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == "hearth_session" {
			sessionCookie = ck
		}
	}
	_ = resp.Body.Close()
	if sessionCookie == nil {
		t.Fatal("login returned no session cookie")
	}

	// --- 9. Create the workspace --------------------------------
	resp = do(http.MethodPost, "/api/workspaces",
		`{"name":"e2e","image":"`+wsImage+`"}`)
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("create workspace: %d\n%s\n---- agent log ----\n%s", resp.StatusCode, body, agentLog.String())
	}
	created := decode(resp)
	wsID, _ := created["id"].(string)
	if wsID == "" {
		t.Fatalf("create returned no id: %v", created)
	}
	if created["state"] != "running" {
		t.Fatalf("created state = %v, want running", created["state"])
	}
	container := "hearth-ws-" + wsID
	volume := "hearth-ws-" + wsID
	t.Logf("workspace %s created, state=running", wsID)

	// Belt-and-braces cleanup for the container/volume this test creates.
	t.Cleanup(func() {
		_, _ = podman.run("rm", "-f", container)
		_, _ = podman.run("volume", "rm", "-f", volume)
	})

	// --- 10. Confirm with Podman the container is really Up --------
	state := podman.mustRun("inspect", "-f", "{{.State.Status}}", container)
	if state != "running" {
		t.Fatalf("podman reports container %s state=%q, want running", container, state)
	}
	names := podman.mustRun("ps", "--filter", "label=hearth.workspace",
		"--filter", "name="+container, "--format", "{{.Names}} {{.State}}")
	if !strings.Contains(names, container) {
		t.Fatalf("podman ps (label=hearth.workspace) did not list %s: %q", container, names)
	}
	t.Logf("podman confirms container Up: %s", names)

	volLine := podman.mustRun("volume", "ls", "--filter", "name="+volume, "--format", "{{.Name}}")
	if !strings.Contains(volLine, volume) {
		t.Fatalf("podman volume %s not present: %q", volume, volLine)
	}
	t.Logf("podman confirms volume present: %s", volLine)

	// --- 11. Stop -------------------------------------------
	resp = do(http.MethodPost, "/api/workspaces/"+wsID+"/stop", "")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("stop workspace: %d\n%s", resp.StatusCode, body)
	}
	stopped := decode(resp)
	if stopped["state"] != "stopped" {
		t.Fatalf("stopped state = %v, want stopped", stopped["state"])
	}
	state = podman.mustRun("inspect", "-f", "{{.State.Status}}", container)
	if state == "running" {
		t.Fatalf("podman reports container %s still running after stop", container)
	}
	t.Logf("podman confirms container stopped: state=%q", state)

	// --- 12. Destroy ---------------------------------------
	resp = do(http.MethodDelete, "/api/workspaces/"+wsID, "")
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("destroy workspace: %d\n%s", resp.StatusCode, body)
	}
	_ = resp.Body.Close()

	if out, err := podman.run("container", "exists", container); err == nil {
		t.Fatalf("container %s still exists after destroy: %q", container, out)
	}
	if out, err := podman.run("volume", "exists", volume); err == nil {
		t.Fatalf("volume %s still exists after destroy: %q", volume, out)
	}
	t.Log("podman confirms container and volume are gone after destroy")

	// --- 13. The row is gone from the API too -------------
	resp = do(http.MethodGet, "/api/workspaces/"+wsID, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get after destroy: %d, want 404", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

// --- production wiring adapters (mirror of cmd/hearth-gateway/main.go) ------

type agentDialer struct{ tls *tls.Config }

func (d agentDialer) Dial(addr string) (hv1.AgentClient, io.Closer, error) {
	return agentclient.Dial(addr, d.tls)
}

type storePersistence struct{ st *store.Store }

func (p storePersistence) UpsertAgent(ctx context.Context, id, advertiseAddr, workspaceAddr string, capacity agentregistry.Capacity) error {
	raw, err := json.Marshal(capacity)
	if err != nil {
		return err
	}
	_, err = p.st.Queries().UpsertAgent(ctx, gen.UpsertAgentParams{
		ID: id, AdvertiseAddr: advertiseAddr, WorkspaceAddr: workspaceAddr, Capacity: raw,
	})
	return err
}

func (p storePersistence) TouchHeartbeat(ctx context.Context, id string) error {
	return p.st.Queries().TouchAgentHeartbeat(ctx, id)
}

func (p storePersistence) SetStatus(ctx context.Context, id, status string) error {
	return p.st.Queries().SetAgentStatus(ctx, gen.SetAgentStatusParams{ID: id, Status: status})
}
