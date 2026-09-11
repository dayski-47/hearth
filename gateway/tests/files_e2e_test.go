package e2e

import (
	"context"
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
	"testing"
	"time"

	"github.com/dayski-47/hearth/gateway/internal/agentregistry"
	"github.com/dayski-47/hearth/gateway/internal/auth"
	"github.com/dayski-47/hearth/gateway/internal/config"
	"github.com/dayski-47/hearth/gateway/internal/grpcserver"
	"github.com/dayski-47/hearth/gateway/internal/httpapi"
	"github.com/dayski-47/hearth/gateway/internal/password"
	"github.com/dayski-47/hearth/gateway/internal/store"
	"github.com/dayski-47/hearth/gateway/internal/store/gen"
	"github.com/dayski-47/hearth/gateway/internal/tlsutil"
	"github.com/dayski-47/hearth/gateway/internal/workspaces"
	"github.com/dayski-47/hearth/gateway/internal/wsresolve"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestFilesE2E boots the whole file path against real Podman: a real
// hearth-workspace reading and writing the container volume directly, a real
// hearth-agent, and an in-process gateway (gRPC + HTTP). It creates a busybox
// workspace over the REST API, then writes, lists, reads, renames, and deletes
// a file through GET/PUT/POST/DELETE .../files*, checks a traversal attempt is
// a 400, and checks that an upload cut short mid-body never lands.
//
// It runs ONLY when HEARTH_E2E=1, HEARTH_PODMAN_SOCKET points at a reachable
// socket, and both Rust binaries are built. Without the gate it t.Skips.
func TestFilesE2E(t *testing.T) {
	if os.Getenv("HEARTH_E2E") != "1" {
		t.Skip("skipping end-to-end file test: set HEARTH_E2E=1 (and HEARTH_PODMAN_SOCKET) to run")
	}
	socket := os.Getenv("HEARTH_PODMAN_SOCKET")
	if socket == "" {
		t.Skip("skipping end-to-end file test: HEARTH_PODMAN_SOCKET is required (the host also runs Docker)")
	}
	if _, err := os.Stat(socket); err != nil {
		t.Skipf("skipping end-to-end file test: podman socket %s not reachable: %v", socket, err)
	}
	root := repoRoot(t)
	agentBin := filepath.Join(root, "target", "debug", "hearth-agent")
	if _, err := os.Stat(agentBin); err != nil {
		t.Skipf("skipping end-to-end file test: agent binary %s not built: %v", agentBin, err)
	}
	wsBin := filepath.Join(root, "target", "debug", "hearth-workspace")
	if _, err := os.Stat(wsBin); err != nil {
		t.Skipf("skipping end-to-end file test: workspace binary %s not built: %v", wsBin, err)
	}

	podman := podmanRunner{t: t, socket: socket}
	if out, err := podman.run("info", "--format", "{{.Host.RemoteSocket.Path}}"); err != nil {
		t.Skipf("skipping end-to-end file test: podman info failed: %v\n%s", err, out)
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
	wsSvc := workspaces.NewService(st.Queries(), reg, agentDialer{tls: agentTLS}, cfg.Workspace, nil, logger)
	files := &httpapi.FileDeps{
		Resolver: wsresolve.Resolver{Store: st.Queries(), Reg: reg},
		Dial:     wsDialer{tls: wsClientTLS},
		Logger:   logger,
	}
	httpSrv := httptest.NewServer(httpapi.New(cfg, st, logger, authH, wsSvc, nil, files).Handler())
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
	// entryNamed returns the listing entry with the given name, or nil.
	entryNamed := func(listing map[string]any, name string) map[string]any {
		t.Helper()
		raw, _ := listing["entries"].([]any)
		for _, e := range raw {
			m, _ := e.(map[string]any)
			if m["name"] == name {
				return m
			}
		}
		return nil
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
		`{"name":"files-e2e","image":"`+wsImage+`"}`)
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

	base := "/api/workspaces/" + wsID + "/files"

	// --- 11. Create the parent directory ---------------------
	// A write never makes intermediate directories, so the folder that will
	// hold the file has to exist first.
	resp = do(http.MethodPost, base+"?path=notes&dir=true", "")
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("create dir notes: %d\n%s", resp.StatusCode, body)
	}
	_ = resp.Body.Close()

	// --- 12. Write a file -----------------------------------
	resp = do(http.MethodPut, base+"/content?path=notes/todo.txt", "buy milk")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("write notes/todo.txt: %d\n%s", resp.StatusCode, body)
	}
	if bw := decode(resp)["bytes_written"]; bw != float64(8) {
		t.Fatalf("bytes_written = %v, want 8", bw)
	}

	// --- 13. List the directory ----------------------------
	resp = do(http.MethodGet, base+"?path=notes", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list notes: %d", resp.StatusCode)
	}
	todo := entryNamed(decode(resp), "todo.txt")
	if todo == nil {
		t.Fatal("todo.txt missing from the notes listing")
	}
	if todo["size"] != float64(8) {
		t.Fatalf("todo.txt size = %v, want 8", todo["size"])
	}
	if isDir, _ := todo["is_dir"].(bool); isDir {
		t.Fatal("todo.txt reported as a directory")
	}

	// --- 14. Read the file back ----------------------------
	resp = do(http.MethodGet, base+"/content?path=notes/todo.txt", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("read notes/todo.txt: %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("read content-type = %q, want application/octet-stream", ct)
	}
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(got) != "buy milk" {
		t.Fatalf("read body = %q, want %q", got, "buy milk")
	}

	// --- 15. A traversal attempt is a 400 ------------------
	// The openat2 sandbox refuses a path with "..", the workspace returns
	// InvalidArgument, and the gateway maps that to 400.
	resp = do(http.MethodGet, base+"/content?path=../../../etc/hostname", "")
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("traversal read status = %d, want 400\n%s", resp.StatusCode, body)
	}

	// --- 16. An upload cut short mid-body never lands ------
	// Start a PUT, push a few KB into the request body, then cancel the
	// request context before the body is finished. The gateway's WriteFile
	// gRPC stream is bound to the request context, so the cancel tears the
	// call down mid-upload; the workspace leaves its abort flag set and
	// unlinks the temp file rather than renaming it into place.
	abortCtx, abortCancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	partReq, _ := http.NewRequestWithContext(abortCtx, http.MethodPut,
		httpSrv.URL+base+"/content?path=notes/partial.txt", pr)
	partReq.Header.Set("X-Hearth-CSRF", "1")
	partReq.AddCookie(sessionCookie)
	partDone := make(chan struct{})
	go func() {
		defer close(partDone)
		if r, err := client.Do(partReq); err == nil {
			_ = r.Body.Close()
		}
	}()
	if _, err := pw.Write(make([]byte, 8<<10)); err != nil {
		t.Fatalf("feed partial upload body: %v", err)
	}
	time.Sleep(500 * time.Millisecond) // let the frame reach the workspace
	abortCancel()
	_ = pw.CloseWithError(context.Canceled)
	<-partDone

	resp = do(http.MethodGet, base+"?path=notes", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list notes after the aborted upload: %d", resp.StatusCode)
	}
	if entryNamed(decode(resp), "partial.txt") != nil {
		t.Fatal("the aborted upload left partial.txt behind")
	}

	// --- 17. Rename ---------------------------------------
	resp = do(http.MethodPost, base+"/rename", `{"from":"notes/todo.txt","to":"notes/done.txt"}`)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("rename: %d\n%s", resp.StatusCode, body)
	}
	_ = resp.Body.Close()
	resp = do(http.MethodGet, base+"?path=notes", "")
	listing := decode(resp)
	if entryNamed(listing, "done.txt") == nil {
		t.Fatal("done.txt missing after the rename")
	}
	if entryNamed(listing, "todo.txt") != nil {
		t.Fatal("todo.txt still present after the rename")
	}

	// --- 18. Delete --------------------------------------
	resp = do(http.MethodDelete, base+"?path=notes", "")
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("delete notes: %d\n%s", resp.StatusCode, body)
	}
	_ = resp.Body.Close()
	resp = do(http.MethodGet, base+"?path=/", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list root after delete: %d", resp.StatusCode)
	}
	if entryNamed(decode(resp), "notes") != nil {
		t.Fatal("notes still listed after the delete")
	}
}
