package ws_test

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dayski-47/hearth/gateway/internal/activity"
	"github.com/dayski-47/hearth/gateway/internal/auth"
	hv1 "github.com/dayski-47/hearth/gateway/internal/hearth/v1"
	"github.com/dayski-47/hearth/gateway/internal/store"
	"github.com/dayski-47/hearth/gateway/internal/store/gen"
	"github.com/dayski-47/hearth/gateway/internal/tlsutil"
	"github.com/dayski-47/hearth/gateway/internal/workspaceclient"
	"github.com/dayski-47/hearth/gateway/internal/ws"
	"github.com/dayski-47/hearth/gateway/internal/wsresolve"
	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	caPath            = "../../../deploy/certs/ca.pem"
	gatewayCertPath   = "../../../deploy/certs/gateway.pem"
	gatewayKeyPath    = "../../../deploy/certs/gateway-key.pem"
	workspaceCertPath = "../../../deploy/certs/workspace.pem"
	workspaceKeyPath  = "../../../deploy/certs/workspace-key.pem"

	ownerID    = "11111111-1111-1111-1111-111111111111"
	runningID  = "22222222-2222-2222-2222-222222222222"
	stoppedID  = "33333333-3333-3333-3333-333333333333"
	missingID  = "44444444-4444-4444-4444-444444444444"
	testOrigin = "http://localhost:5173"
)

// echoTerminal implements the WorkspaceIo OpenTerminal contract: it reads the
// init frame, answers with Ready, echoes every stdin chunk back as stdout, and
// sends Exit{0} once the client half-closes. resumed sets the flag on the Ready
// frame so tests can drive both the fresh and the re-attached path.
type echoTerminal struct {
	hv1.UnimplementedWorkspaceIoServer
	resumed bool
}

func (e echoTerminal) OpenTerminal(stream grpc.BidiStreamingServer[hv1.TerminalClientFrame, hv1.TerminalServerFrame]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if first.GetInit() == nil {
		return io.ErrUnexpectedEOF
	}
	if err := stream.Send(&hv1.TerminalServerFrame{Msg: &hv1.TerminalServerFrame_Ready{Ready: &hv1.TerminalReady{Resumed: e.resumed}}}); err != nil {
		return err
	}
	for {
		f, err := stream.Recv()
		if err == io.EOF {
			return stream.Send(&hv1.TerminalServerFrame{Msg: &hv1.TerminalServerFrame_Exit{Exit: &hv1.TerminalExit{ExitCode: 0}}})
		}
		if err != nil {
			return err
		}
		if in, ok := f.Msg.(*hv1.TerminalClientFrame_Stdin); ok {
			if err := stream.Send(&hv1.TerminalServerFrame{Msg: &hv1.TerminalServerFrame_Stdout{Stdout: in.Stdin}}); err != nil {
				return err
			}
		}
	}
}

// startEchoWorkspace stands up an in-process WorkspaceIo server on dev certs.
func startEchoWorkspace(t *testing.T, resumed bool) string {
	t.Helper()
	return startWorkspaceServer(t, echoTerminal{resumed: resumed})
}

// startWorkspaceServer stands up an in-process WorkspaceIo server backed by
// an arbitrary implementation, on dev certs.
func startWorkspaceServer(t *testing.T, srv hv1.WorkspaceIoServer) string {
	t.Helper()
	srvTLS, err := tlsutil.ServerConfig(caPath, workspaceCertPath, workspaceKeyPath)
	if err != nil {
		t.Skip("run `just certs`: ", err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := grpc.NewServer(grpc.Creds(credentials.NewTLS(srvTLS)))
	hv1.RegisterWorkspaceIoServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	return lis.Addr().String()
}

// pushTerminal answers Init with Ready and then immediately pushes one
// unprompted Stdout frame, as a long-running build's output would arrive
// with no corresponding keystroke from the client. It then blocks until the
// stream is torn down, so the test controls the connection's lifetime.
type pushTerminal struct {
	hv1.UnimplementedWorkspaceIoServer
}

func (p pushTerminal) OpenTerminal(stream grpc.BidiStreamingServer[hv1.TerminalClientFrame, hv1.TerminalServerFrame]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if first.GetInit() == nil {
		return io.ErrUnexpectedEOF
	}
	if err := stream.Send(&hv1.TerminalServerFrame{Msg: &hv1.TerminalServerFrame_Ready{Ready: &hv1.TerminalReady{}}}); err != nil {
		return err
	}
	if err := stream.Send(&hv1.TerminalServerFrame{Msg: &hv1.TerminalServerFrame_Stdout{Stdout: []byte("building...")}}); err != nil {
		return err
	}
	<-stream.Context().Done()
	return stream.Context().Err()
}

type fakeStore struct{ agentID string }

func (f fakeStore) GetWorkspaceForOwner(_ context.Context, p gen.GetWorkspaceForOwnerParams) (gen.Workspace, error) {
	agent := f.agentID
	switch store.UUIDString(p.ID) {
	case runningID:
		return gen.Workspace{ID: p.ID, OwnerID: p.OwnerID, State: "running", AgentID: &agent}, nil
	case stoppedID:
		return gen.Workspace{ID: p.ID, OwnerID: p.OwnerID, State: "stopped", AgentID: &agent}, nil
	default:
		return gen.Workspace{}, io.EOF // stands in for pgx.ErrNoRows
	}
}

type fakeRegistry struct{ addr string }

func (f fakeRegistry) WorkspaceAddr(string) (string, bool) { return f.addr, true }

type tlsDialer struct{ cfg *tls.Config }

func (d tlsDialer) Dial(addr string) (hv1.WorkspaceIoClient, io.Closer, error) {
	return workspaceclient.Dial(addr, d.cfg)
}

func newBridge(t *testing.T) *httptest.Server {
	t.Helper()
	return newBridgeResumed(t, false)
}

func newBridgeResumed(t *testing.T, resumed bool) *httptest.Server {
	t.Helper()
	return newBridgeResumedWithActivity(t, resumed, activity.NewTracker())
}

func newBridgeResumedWithActivity(t *testing.T, resumed bool, tr *activity.Tracker) *httptest.Server {
	t.Helper()
	addr := startEchoWorkspace(t, resumed)
	return newBridgeToAddr(t, addr, tr)
}

// newBridgeToAddr wires up the gateway-side bridge (ws.Deps and its router)
// against an already-running WorkspaceIo server at addr. Shared by
// newBridgeResumedWithActivity (an echo backend) and tests that need a
// backend with different behavior (e.g. pushTerminal).
func newBridgeToAddr(t *testing.T, addr string, tr *activity.Tracker) *httptest.Server {
	t.Helper()
	cliTLS, err := tlsutil.ClientConfig(caPath, gatewayCertPath, gatewayKeyPath, "hearth-workspace")
	if err != nil {
		t.Fatal(err)
	}
	fs := fakeStore{agentID: "agent-1"}
	fr := fakeRegistry{addr: addr}
	deps := ws.Deps{
		Resolver:      wsresolve.Resolver{Store: fs, Reg: fr},
		Dial:          tlsDialer{cfg: cliTLS},
		AllowedOrigin: testOrigin,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Activity:      tr,
	}
	owner, err := store.ParseUUID(ownerID)
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Route("/api/workspaces", func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				ctx := auth.ContextWithUser(req.Context(), &gen.User{ID: owner})
				next.ServeHTTP(w, req.WithContext(ctx))
			})
		})
		r.Get("/{id}/terminal", deps.Terminal)
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func wsURL(base, id string) string {
	return strings.Replace(base, "http://", "ws://", 1) + "/api/workspaces/" + id + "/terminal?cols=80&rows=24"
}

func TestTerminalBridgeEchoes(t *testing.T) {
	srv := newBridge(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL(srv.URL, runningID), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	if err := conn.Write(ctx, websocket.MessageBinary, append([]byte{0x00}, []byte("ping")...)); err != nil {
		t.Fatalf("write: %v", err)
	}

	// The bridge forwards the workspace's Ready frame as a text control
	// frame before any echoed output.
	typ, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read ready: %v", err)
	}
	if typ != websocket.MessageText || string(data) != `{"t":"ready","resumed":false}` {
		t.Fatalf("got %v %q, want text %q", typ, data, `{"t":"ready","resumed":false}`)
	}

	typ, data, err = conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if typ != websocket.MessageBinary || string(data) != "ping" {
		t.Fatalf("got %v %q, want binary %q", typ, data, "ping")
	}
	if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestTerminalBridgeForwardsReady(t *testing.T) {
	cases := []struct {
		name    string
		resumed bool
		want    string
	}{
		{"fresh", false, `{"t":"ready","resumed":false}`},
		{"resumed", true, `{"t":"ready","resumed":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newBridgeResumed(t, tc.resumed)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			conn, _, err := websocket.Dial(ctx, wsURL(srv.URL, runningID), nil)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer conn.CloseNow()

			typ, data, err := conn.Read(ctx)
			if err != nil {
				t.Fatalf("read ready: %v", err)
			}
			if typ != websocket.MessageText {
				t.Fatalf("got frame type %v, want text", typ)
			}
			if string(data) != tc.want {
				t.Fatalf("got %q, want %q", data, tc.want)
			}

			if err := conn.Write(ctx, websocket.MessageBinary, append([]byte{0x00}, []byte("hi")...)); err != nil {
				t.Fatalf("write: %v", err)
			}
			typ, data, err = conn.Read(ctx)
			if err != nil {
				t.Fatalf("read echo: %v", err)
			}
			if typ != websocket.MessageBinary || string(data) != "hi" {
				t.Fatalf("got %v %q, want binary %q", typ, data, "hi")
			}
			if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
				t.Fatalf("close: %v", err)
			}
		})
	}
}

func TestTerminalTouchesActivityOnConnectAndOnEachFrame(t *testing.T) {
	tr := activity.NewTracker()
	srv := newBridgeResumedWithActivity(t, false, tr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL(srv.URL, runningID), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	// Read the Ready frame so the connection is fully established before
	// checking that connecting alone already touched the tracker.
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatalf("read ready: %v", err)
	}
	before, ok := tr.IdleFor(runningID, time.Now())
	if !ok {
		t.Fatal("expected activity touch on terminal connect")
	}

	if err := conn.Write(ctx, websocket.MessageBinary, append([]byte{0x00}, []byte("hi")...)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := conn.Read(ctx); err != nil { // the echo of "hi"
		t.Fatalf("read echo: %v", err)
	}
	after, ok := tr.IdleFor(runningID, time.Now())
	if !ok {
		t.Fatal("expected activity still tracked after a frame")
	}
	if after > before {
		t.Fatalf("idle-for grew after a frame (before=%v after=%v), want a fresh touch", before, after)
	}
}

func TestTerminalTouchesActivityOnOutboundStdout(t *testing.T) {
	// A monotonically increasing fake clock lets us tell touches apart by
	// exactly when they happened, rather than by wall-clock jitter.
	var n int64
	tr := activity.NewTrackerWithClock(func() time.Time {
		return time.Unix(atomic.AddInt64(&n, 1), 0)
	})

	addr := startWorkspaceServer(t, pushTerminal{})
	srv := newBridgeToAddr(t, addr, tr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL(srv.URL, runningID), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatalf("read ready: %v", err)
	}

	// pushTerminal sends this stdout frame on its own, with no client input
	// ever having been written on this connection.
	typ, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if typ != websocket.MessageBinary || string(data) != "building..." {
		t.Fatalf("got %v %q, want binary %q", typ, data, "building...")
	}

	last := time.Unix(atomic.LoadInt64(&n), 0)
	idle, ok := tr.IdleFor(runningID, last)
	if !ok {
		t.Fatal("expected an activity touch from the outbound stdout frame")
	}
	if idle != 0 {
		t.Fatalf("idle = %v after an unprompted server stdout frame, want 0 (a fresh touch on outbound data, not just on connect)", idle)
	}
}

func TestTerminalRejectsWrongOwner(t *testing.T) {
	srv := newBridge(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, resp, err := websocket.Dial(ctx, wsURL(srv.URL, missingID), nil)
	if err == nil {
		conn.CloseNow()
		t.Fatal("expected dial to fail")
	}
	if resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %v", resp)
	}
}

func TestTerminalRejectsStoppedWorkspace(t *testing.T) {
	srv := newBridge(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, resp, err := websocket.Dial(ctx, wsURL(srv.URL, stoppedID), nil)
	if err == nil {
		conn.CloseNow()
		t.Fatal("expected dial to fail")
	}
	if resp == nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("want 409, got %v", resp)
	}
}

func TestTerminalRejectsCrossOrigin(t *testing.T) {
	srv := newBridge(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, resp, err := websocket.Dial(ctx, wsURL(srv.URL, runningID), &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {"http://evil.example"}},
	})
	if err == nil {
		conn.CloseNow()
		t.Fatal("expected handshake to fail on origin mismatch")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403, got %v", resp)
	}
}
