package e2e

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
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
	"github.com/dayski-47/hearth/gateway/internal/workspaceclient"
	"github.com/dayski-47/hearth/gateway/internal/workspaces"
	"github.com/dayski-47/hearth/gateway/internal/ws"
	"github.com/dayski-47/hearth/gateway/internal/wsresolve"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// wsDialer adapts the gateway↔workspace mTLS config and workspaceclient.Dial to
// ws.WSDialer, exactly as cmd/hearth-gateway/main.go does.
type wsDialer struct{ tls *tls.Config }

func (d wsDialer) Dial(addr string) (hv1.WorkspaceIoClient, io.Closer, error) {
	return workspaceclient.Dial(addr, d.tls)
}

// TestTerminalE2E boots the whole terminal path against real Podman: a
// real hearth-workspace, a real hearth-agent, and an in-process gateway (gRPC +
// HTTP). It creates a busybox workspace over the REST API, opens the browser
// terminal WebSocket, and checks that `echo` round-trips through the container
// shell.
//
// It runs ONLY when HEARTH_E2E=1, HEARTH_PODMAN_SOCKET points at a reachable
// socket, and both Rust binaries are built. Without the gate it t.Skips.
func TestTerminalE2E(t *testing.T) {
	if os.Getenv("HEARTH_E2E") != "1" {
		t.Skip("skipping end-to-end terminal test: set HEARTH_E2E=1 (and HEARTH_PODMAN_SOCKET) to run")
	}
	socket := os.Getenv("HEARTH_PODMAN_SOCKET")
	if socket == "" {
		t.Skip("skipping end-to-end terminal test: HEARTH_PODMAN_SOCKET is required (the host also runs Docker)")
	}
	if _, err := os.Stat(socket); err != nil {
		t.Skipf("skipping end-to-end terminal test: podman socket %s not reachable: %v", socket, err)
	}
	root := repoRoot(t)
	agentBin := filepath.Join(root, "target", "debug", "hearth-agent")
	if _, err := os.Stat(agentBin); err != nil {
		t.Skipf("skipping end-to-end terminal test: agent binary %s not built: %v", agentBin, err)
	}
	wsBin := filepath.Join(root, "target", "debug", "hearth-workspace")
	if _, err := os.Stat(wsBin); err != nil {
		t.Skipf("skipping end-to-end terminal test: workspace binary %s not built: %v", wsBin, err)
	}

	podman := podmanRunner{t: t, socket: socket}
	if out, err := podman.run("info", "--format", "{{.Host.RemoteSocket.Path}}"); err != nil {
		t.Skipf("skipping end-to-end terminal test: podman info failed: %v\n%s", err, out)
	}

	ctx := context.Background()

	// --- 1. Postgres + migrations + admin --------------------------------
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

	// --- 2. Fresh CA + leaf certs --------------------------------------
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

	// --- 3. Gateway gRPC server (in-process) --------------------------
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

	// --- 4. Gateway HTTP server (in-process) -------------------------
	agentTLS, err := tlsutil.ClientConfig(caPEM, gatewayCert, gatewayKey, "hearth-agent")
	if err != nil {
		t.Fatal(err)
	}
	wsClientTLS, err := tlsutil.ClientConfig(caPEM, gatewayCert, gatewayKey, "hearth-workspace")
	if err != nil {
		t.Fatal(err)
	}

	// The terminal handler checks the WebSocket's Origin header against
	// cfg.AllowedOrigin, so the two have to agree. We create the listener up
	// front to learn the port, pin AllowedOrigin to that origin, build the
	// handler, then hand the listener to httptest.Server before starting it.
	httpLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	originURL := "http://" + httpLis.Addr().String()

	cfg := &config.Config{
		ListenAddr:        "127.0.0.1:0",
		PublicURL:         originURL,
		AllowedOrigin:     originURL,
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
	wsSvc := workspaces.NewService(st.Queries(), reg, agentDialer{tls: agentTLS}, cfg.Workspace, logger)
	term := &ws.Deps{
		Resolver:      wsresolve.Resolver{Store: st.Queries(), Reg: reg},
		Dial:          wsDialer{tls: wsClientTLS},
		AllowedOrigin: cfg.AllowedOrigin,
		Logger:        logger,
	}
	httpSrv := &httptest.Server{
		Listener: httpLis,
		Config:   &http.Server{Handler: httpapi.New(cfg, st, logger, authH, wsSvc, term, nil).Handler()},
	}
	httpSrv.Start()
	t.Cleanup(httpSrv.Close)

	// --- 5. Real hearth-workspace against real Podman ---------------
	// Started before the agent so its listen address is known and can be
	// advertised into the registry via the agent's HEARTH_WORKSPACE_ADDR.
	wsAddr := freePort(t)
	wsLog := &syncBuf{}
	wsCmd := exec.Command(wsBin)
	wsCmd.Env = append(os.Environ(),
		"HEARTH_WORKSPACE_GRPC_LISTEN_ADDR="+wsAddr,
		"HEARTH_TLS_CA="+caPEM,
		"HEARTH_WORKSPACE_TLS_CERT="+filepath.Join(certsDir, "workspace.pem"),
		"HEARTH_WORKSPACE_TLS_KEY="+filepath.Join(certsDir, "workspace-key.pem"),
		"HEARTH_PODMAN_SOCKET="+socket,
		"RUST_LOG=info",
	)
	wsCmd.Stdout = wsLog
	wsCmd.Stderr = wsLog
	if err := wsCmd.Start(); err != nil {
		t.Fatalf("start workspace service: %v", err)
	}
	t.Cleanup(func() {
		if wsCmd.Process != nil {
			_ = wsCmd.Process.Kill()
			_, _ = wsCmd.Process.Wait()
		}
		t.Logf("---- hearth-workspace log ----\n%s\n-----------------------------", wsLog.String())
	})

	// Wait for the workspace service to bind. It pings Podman first and only
	// then listens, so a successful TCP dial means it is serving.
	wsReady := false
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		if c, derr := net.DialTimeout("tcp", wsAddr, time.Second); derr == nil {
			_ = c.Close()
			wsReady = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !wsReady {
		t.Fatalf("workspace service did not start listening on %s\n---- workspace log ----\n%s", wsAddr, wsLog.String())
	}

	// --- 6. Real hearth-agent against real Podman ------------------
	agentAddr := freePort(t)
	agentLog := &syncBuf{}
	agentCmd := exec.Command(agentBin)
	agentCmd.Env = append(os.Environ(),
		"HEARTH_HOST_ID="+e2eHostID,
		"HEARTH_AGENT_GRPC_LISTEN_ADDR="+agentAddr,
		"HEARTH_AGENT_ADDR=https://"+agentAddr,
		"HEARTH_GATEWAY_GRPC_ADDR=https://"+gatewayGRPCAddr,
		"HEARTH_WORKSPACE_ADDR=https://"+wsAddr,
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

	// --- 7. Wait for the agent to register as ready, advertising the
	//        real workspace address --------------------------------
	deadline := time.Now().Add(90 * time.Second)
	for {
		ready := false
		for _, a := range reg.List() {
			if a.ID == e2eHostID && a.Status == "ready" {
				ready = true
			}
		}
		waddr, waok := reg.WorkspaceAddr(e2eHostID)
		if ready && waok && waddr == "https://"+wsAddr {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent did not register within timeout\n---- agent log ----\n%s", agentLog.String())
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Log("agent registered as ready, workspace address advertised")

	// --- 8. HTTP client helpers ----------------------------------
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

	// --- 9. Login ----------------------------------------------
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

	// --- 10. Create the workspace -----------------------------
	resp = do(http.MethodPost, "/api/workspaces",
		`{"name":"term-e2e","image":"`+wsImage+`"}`)
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

	// --- 11. Terminal round-trip over the browser WebSocket -------
	wsURL := "ws://" + httpLis.Addr().String() + "/api/workspaces/" + wsID + "/terminal?cols=80&rows=24"
	dialCtx, dialCancel := context.WithTimeout(ctx, 15*time.Second)
	defer dialCancel()
	c, resp, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Cookie": {sessionCookie.String()},
			"Origin": {originURL},
		},
	})
	if err != nil {
		t.Fatalf("dial terminal websocket: %v (status %s)\n---- agent log ----\n%s\n---- workspace log ----\n%s",
			err, statusOf(resp), agentLog.String(), wsLog.String())
	}
	t.Cleanup(func() { _ = c.CloseNow() })

	// A stdin frame is a 0x00 tag byte followed by the bytes to write.
	if err := c.Write(ctx, websocket.MessageBinary, append([]byte{0x00}, []byte("echo hearthE2E\n")...)); err != nil {
		t.Fatalf("write echo command: %v", err)
	}

	readCtx, readCancel := context.WithTimeout(ctx, 15*time.Second)
	defer readCancel()
	var acc []byte
	for {
		typ, data, err := c.Read(readCtx)
		if err != nil {
			t.Fatalf("did not see echo output before timeout: %v\ngot so far: %q\n---- agent log ----\n%s\n---- workspace log ----\n%s",
				err, acc, agentLog.String(), wsLog.String())
		}
		if typ == websocket.MessageBinary {
			acc = append(acc, data...)
		}
		if strings.Contains(string(acc), "hearthE2E") {
			break
		}
	}
	t.Logf("terminal echoed back: %q", strings.TrimSpace(string(acc)))

	// Ask the shell to exit; the server should then close the socket.
	if err := c.Write(ctx, websocket.MessageBinary, append([]byte{0x00}, []byte("exit\n")...)); err != nil {
		t.Fatalf("write exit command: %v", err)
	}
	closeCtx, closeCancel := context.WithTimeout(ctx, 10*time.Second)
	defer closeCancel()
	for {
		_, _, rerr := c.Read(closeCtx)
		if rerr == nil {
			continue // still draining trailing output
		}
		cleanClose := errors.Is(rerr, io.EOF) ||
			websocket.CloseStatus(rerr) != -1 ||
			errors.Is(rerr, net.ErrClosed)
		if cleanClose {
			t.Logf("terminal socket closed after exit: %v", rerr)
			break
		}
		if closeCtx.Err() != nil {
			t.Errorf("terminal socket did not close within 10s after exit: %v\n---- agent log ----\n%s\n---- workspace log ----\n%s",
				rerr, agentLog.String(), wsLog.String())
			break
		}
		// Some other transient read error: keep waiting until the deadline.
	}
	_ = c.Close(websocket.StatusNormalClosure, "")
}

// statusOf renders the HTTP status of a WebSocket handshake response, tolerating
// the nil response that Dial returns when it never reached the server.
func statusOf(resp *http.Response) string {
	if resp == nil {
		return "no response"
	}
	return resp.Status
}
