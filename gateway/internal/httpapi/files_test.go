package httpapi

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dayski-47/hearth/gateway/internal/auth"
	hv1 "github.com/dayski-47/hearth/gateway/internal/hearth/v1"
	"github.com/dayski-47/hearth/gateway/internal/store"
	"github.com/dayski-47/hearth/gateway/internal/store/gen"
	"github.com/dayski-47/hearth/gateway/internal/tlsutil"
	"github.com/dayski-47/hearth/gateway/internal/workspaceclient"
	"github.com/dayski-47/hearth/gateway/internal/wsresolve"
	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

const (
	caPath          = "../../../deploy/certs/ca.pem"
	gatewayCertPath = "../../../deploy/certs/gateway.pem"
	gatewayKeyPath  = "../../../deploy/certs/gateway-key.pem"
	wsCertPath      = "../../../deploy/certs/workspace.pem"
	wsKeyPath       = "../../../deploy/certs/workspace-key.pem"

	fileOwnerID   = "11111111-1111-1111-1111-111111111111"
	fileRunningID = "22222222-2222-2222-2222-222222222222"
	fileStoppedID = "33333333-3333-3333-3333-333333333333"
	fileMissingID = "44444444-4444-4444-4444-444444444444"
)

// memFS is an in-process WorkspaceIo server whose file methods act on an
// in-memory map keyed by path. A path containing ".." is rejected the way the
// real service's openat2(RESOLVE_BENEATH) would.
type memFS struct {
	hv1.UnimplementedWorkspaceIoServer
	files map[string][]byte
}

func newMemFS() *memFS { return &memFS{files: map[string][]byte{}} }

func beneath(path string) error {
	if strings.Contains(path, "..") {
		return status.Error(codes.InvalidArgument, "path escapes workspace")
	}
	return nil
}

func (m *memFS) ListDir(_ context.Context, req *hv1.ListDirRequest) (*hv1.ListDirResponse, error) {
	if err := beneath(req.Path); err != nil {
		return nil, err
	}
	var entries []*hv1.Node
	for p, b := range m.files {
		name := p
		if i := strings.LastIndex(p, "/"); i >= 0 {
			name = p[i+1:]
		}
		entries = append(entries, &hv1.Node{Path: p, Name: name, Size: uint64(len(b))})
	}
	return &hv1.ListDirResponse{Entries: entries}, nil
}

func (m *memFS) ReadFile(req *hv1.ReadFileRequest, stream grpc.ServerStreamingServer[hv1.FileChunk]) error {
	if err := beneath(req.Path); err != nil {
		return err
	}
	b, ok := m.files[req.Path]
	if !ok {
		return status.Error(codes.NotFound, "no such file")
	}
	return stream.Send(&hv1.FileChunk{Data: b})
}

func (m *memFS) WriteFile(stream grpc.ClientStreamingServer[hv1.WriteFileFrame, hv1.WriteFileResponse]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	init := first.GetInit()
	if init == nil {
		return status.Error(codes.InvalidArgument, "first frame must be init")
	}
	if err := beneath(init.Path); err != nil {
		return err
	}
	var buf []byte
	for {
		frame, err := stream.Recv()
		if err == io.EOF {
			m.files[init.Path] = buf
			return stream.SendAndClose(&hv1.WriteFileResponse{BytesWritten: uint64(len(buf))})
		}
		if err != nil {
			return err
		}
		buf = append(buf, frame.GetData()...)
	}
}

func (m *memFS) CreateNode(_ context.Context, req *hv1.CreateNodeRequest) (*hv1.Node, error) {
	if err := beneath(req.Path); err != nil {
		return nil, err
	}
	if _, ok := m.files[req.Path]; ok {
		return nil, status.Error(codes.AlreadyExists, "exists")
	}
	m.files[req.Path] = nil
	return &hv1.Node{Path: req.Path, IsDir: req.IsDir}, nil
}

func (m *memFS) DeleteNode(_ context.Context, req *hv1.DeleteNodeRequest) (*hv1.DeleteNodeResponse, error) {
	if err := beneath(req.Path); err != nil {
		return nil, err
	}
	if _, ok := m.files[req.Path]; !ok {
		return nil, status.Error(codes.NotFound, "no such file")
	}
	delete(m.files, req.Path)
	return &hv1.DeleteNodeResponse{}, nil
}

func (m *memFS) RenameNode(_ context.Context, req *hv1.RenameNodeRequest) (*hv1.Node, error) {
	if err := beneath(req.From); err != nil {
		return nil, err
	}
	if err := beneath(req.To); err != nil {
		return nil, err
	}
	b, ok := m.files[req.From]
	if !ok {
		return nil, status.Error(codes.NotFound, "no such file")
	}
	delete(m.files, req.From)
	m.files[req.To] = b
	return &hv1.Node{Path: req.To}, nil
}

func startWorkspaceIo(t *testing.T, impl hv1.WorkspaceIoServer) string {
	t.Helper()
	srvTLS, err := tlsutil.ServerConfig(caPath, wsCertPath, wsKeyPath)
	if err != nil {
		t.Skip("run `just certs`: ", err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := grpc.NewServer(grpc.Creds(credentials.NewTLS(srvTLS)))
	hv1.RegisterWorkspaceIoServer(gs, impl)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	return lis.Addr().String()
}

type fileStore struct{}

func (fileStore) GetWorkspaceForOwner(_ context.Context, p gen.GetWorkspaceForOwnerParams) (gen.Workspace, error) {
	agent := "agent-1"
	switch store.UUIDString(p.ID) {
	case fileRunningID:
		return gen.Workspace{ID: p.ID, OwnerID: p.OwnerID, State: "running", AgentID: &agent}, nil
	case fileStoppedID:
		return gen.Workspace{ID: p.ID, OwnerID: p.OwnerID, State: "stopped", AgentID: &agent}, nil
	default:
		return gen.Workspace{}, io.EOF // stands in for pgx.ErrNoRows
	}
}

type fileRegistry struct{ addr string }

func (f fileRegistry) WorkspaceAddr(string) (string, bool) { return f.addr, true }

type fileDialer struct{ cfg *tls.Config }

func (d fileDialer) Dial(addr string) (hv1.WorkspaceIoClient, io.Closer, error) {
	return workspaceclient.Dial(addr, d.cfg)
}

func newFilesServer(t *testing.T) (*httptest.Server, *memFS) {
	t.Helper()
	fs := newMemFS()
	addr := startWorkspaceIo(t, fs)
	cliTLS, err := tlsutil.ClientConfig(caPath, gatewayCertPath, gatewayKeyPath, "hearth-workspace")
	if err != nil {
		t.Fatal(err)
	}
	deps := &FileDeps{
		Resolver: wsresolve.Resolver{Store: fileStore{}, Reg: fileRegistry{addr: addr}},
		Dial:     fileDialer{cfg: cliTLS},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	owner, err := store.ParseUUID(fileOwnerID)
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
		r.Get("/{id}/files", deps.list)
		r.Get("/{id}/files/content", deps.readContent)
		r.Put("/{id}/files/content", deps.writeContent)
		r.Post("/{id}/files", deps.create)
		r.Post("/{id}/files/rename", deps.rename)
		r.Delete("/{id}/files", deps.del)
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, fs
}

func filesURL(base, id, suffix string) string {
	return base + "/api/workspaces/" + id + "/files" + suffix
}

func do(t *testing.T, method, url string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestFilesWriteThenRead(t *testing.T) {
	srv, _ := newFilesServer(t)

	resp := do(t, http.MethodPut, filesURL(srv.URL, fileRunningID, "/content?path=a.txt"), strings.NewReader("hi"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("write: want 200, got %d", resp.StatusCode)
	}
	var wr struct {
		BytesWritten uint64 `json:"bytes_written"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wr); err != nil {
		t.Fatal(err)
	}
	if wr.BytesWritten != 2 {
		t.Fatalf("bytes_written: want 2, got %d", wr.BytesWritten)
	}

	rd := do(t, http.MethodGet, filesURL(srv.URL, fileRunningID, "/content?path=a.txt"), nil)
	defer rd.Body.Close()
	if rd.StatusCode != http.StatusOK {
		t.Fatalf("read: want 200, got %d", rd.StatusCode)
	}
	got, _ := io.ReadAll(rd.Body)
	if string(got) != "hi" {
		t.Fatalf("read body: want %q, got %q", "hi", got)
	}
}

func TestFilesList(t *testing.T) {
	srv, mem := newFilesServer(t)
	mem.files["a.txt"] = []byte("x")

	resp := do(t, http.MethodGet, filesURL(srv.URL, fileRunningID, "?path=/"), nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var out struct {
		Entries []struct {
			Path string `json:"path"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Entries) != 1 || out.Entries[0].Path != "a.txt" {
		t.Fatalf("entries: got %+v", out.Entries)
	}
}

func TestFilesReadMissing(t *testing.T) {
	srv, _ := newFilesServer(t)
	resp := do(t, http.MethodGet, filesURL(srv.URL, fileRunningID, "/content?path=nope"), nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

func TestFilesBadPath(t *testing.T) {
	srv, _ := newFilesServer(t)
	resp := do(t, http.MethodGet, filesURL(srv.URL, fileRunningID, "/content?path=../x"), nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

func TestFilesRenameAndDelete(t *testing.T) {
	srv, mem := newFilesServer(t)
	mem.files["from.txt"] = []byte("data")

	rn := do(t, http.MethodPost, filesURL(srv.URL, fileRunningID, "/rename"), strings.NewReader(`{"from":"from.txt","to":"to.txt"}`))
	defer rn.Body.Close()
	if rn.StatusCode != http.StatusOK {
		t.Fatalf("rename: want 200, got %d", rn.StatusCode)
	}
	if _, ok := mem.files["to.txt"]; !ok {
		t.Fatal("rename: to.txt missing")
	}

	del := do(t, http.MethodDelete, filesURL(srv.URL, fileRunningID, "?path=to.txt"), nil)
	defer del.Body.Close()
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: want 204, got %d", del.StatusCode)
	}
	if _, ok := mem.files["to.txt"]; ok {
		t.Fatal("delete: to.txt still present")
	}
}

func TestFilesWrongOwner(t *testing.T) {
	srv, _ := newFilesServer(t)
	resp := do(t, http.MethodGet, filesURL(srv.URL, fileMissingID, "/content?path=a.txt"), nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

func TestFilesStoppedWorkspaceAllowed(t *testing.T) {
	srv, _ := newFilesServer(t)

	resp := do(t, http.MethodPut, filesURL(srv.URL, fileStoppedID, "/content?path=b.txt"), strings.NewReader("bye"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("write on stopped: want 200, got %d", resp.StatusCode)
	}

	rd := do(t, http.MethodGet, filesURL(srv.URL, fileStoppedID, "/content?path=b.txt"), nil)
	defer rd.Body.Close()
	got, _ := io.ReadAll(rd.Body)
	if string(got) != "bye" {
		t.Fatalf("read: want %q, got %q", "bye", got)
	}
}
