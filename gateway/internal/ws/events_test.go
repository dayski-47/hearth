package ws_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dayski-47/hearth/gateway/internal/auth"
	hv1 "github.com/dayski-47/hearth/gateway/internal/hearth/v1"
	"github.com/dayski-47/hearth/gateway/internal/store"
	"github.com/dayski-47/hearth/gateway/internal/store/gen"
	"github.com/dayski-47/hearth/gateway/internal/tlsutil"
	"github.com/dayski-47/hearth/gateway/internal/ws"
	"github.com/dayski-47/hearth/gateway/internal/wsresolve"
	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// watchIo implements the WorkspaceIo WatchChanges contract: it sends two
// FileEvents for a.txt and then returns, ending the stream.
type watchIo struct {
	hv1.UnimplementedWorkspaceIoServer
}

func (watchIo) WatchChanges(_ *hv1.WatchRequest, stream grpc.ServerStreamingServer[hv1.FileEvent]) error {
	for _, ev := range []*hv1.FileEvent{
		{Path: "a.txt", Kind: hv1.FileEvent_CREATED},
		{Path: "a.txt", Kind: hv1.FileEvent_MODIFIED},
	} {
		if err := stream.Send(ev); err != nil {
			return err
		}
	}
	return nil
}

// startWatchWorkspace stands up an in-process WorkspaceIo server on dev certs
// whose WatchChanges streams two file events.
func startWatchWorkspace(t *testing.T) string {
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
	hv1.RegisterWorkspaceIoServer(gs, watchIo{})
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	return lis.Addr().String()
}

func newEventsBridge(t *testing.T) *httptest.Server {
	t.Helper()
	addr := startWatchWorkspace(t)
	cliTLS, err := tlsutil.ClientConfig(caPath, gatewayCertPath, gatewayKeyPath, "hearth-workspace")
	if err != nil {
		t.Fatal(err)
	}
	deps := ws.Deps{
		Resolver:      wsresolve.Resolver{Store: fakeStore{agentID: "agent-1"}, Reg: fakeRegistry{addr: addr}},
		Dial:          tlsDialer{cfg: cliTLS},
		AllowedOrigin: testOrigin,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
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
		r.Get("/{id}/events", deps.Events)
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func eventsURL(base, id string) string {
	return strings.Replace(base, "http://", "ws://", 1) + "/api/workspaces/" + id + "/events"
}

func TestEventsBridgeForwards(t *testing.T) {
	srv := newEventsBridge(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, eventsURL(srv.URL, runningID), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	want := []string{
		`{"path":"a.txt","kind":"CREATED"}`,
		`{"path":"a.txt","kind":"MODIFIED"}`,
	}
	for i, w := range want {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if typ != websocket.MessageText || string(data) != w {
			t.Fatalf("frame %d: got %v %q, want text %q", i, typ, data, w)
		}
	}
	if _, _, err := conn.Read(ctx); err == nil {
		t.Fatal("expected the socket to close once the stream ended")
	}
}

func TestEventsRejectsWrongOwner(t *testing.T) {
	srv := newEventsBridge(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, resp, err := websocket.Dial(ctx, eventsURL(srv.URL, missingID), nil)
	if err == nil {
		conn.CloseNow()
		t.Fatal("expected dial to fail")
	}
	if resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %v", resp)
	}
}

func TestEventsRejectsCrossOrigin(t *testing.T) {
	srv := newEventsBridge(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, resp, err := websocket.Dial(ctx, eventsURL(srv.URL, runningID), &websocket.DialOptions{
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
