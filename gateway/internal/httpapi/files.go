package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	hv1 "github.com/dayski-47/hearth/gateway/internal/hearth/v1"
	"github.com/dayski-47/hearth/gateway/internal/store"
	"github.com/dayski-47/hearth/gateway/internal/wsresolve"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type FileDialer interface {
	Dial(addr string) (hv1.WorkspaceIoClient, io.Closer, error)
}

// FileDeps serves GET/PUT/POST/DELETE .../files*, translating each to a
// WorkspaceIo file RPC. It is nil when the gateway is built without a workspace
// service, in which case the routes are not mounted.
type FileDeps struct {
	Resolver wsresolve.Resolver
	Dial     FileDialer
	Logger   *slog.Logger
}

// client authorizes the request against its workspace and opens a gRPC client
// to that workspace's host. When ok is false the HTTP response is already
// written. The op is allowed on a running or a stopped workspace: reading and
// editing files does not need the container up.
func (d *FileDeps) client(w http.ResponseWriter, r *http.Request) (hv1.WorkspaceIoClient, io.Closer, string, bool) {
	ws, addr, ok := d.Resolver.Resolve(w, r, "running", "stopped")
	if !ok {
		return nil, nil, "", false
	}
	c, closer, err := d.Dial.Dial(addr)
	if err != nil {
		if d.Logger != nil {
			d.Logger.ErrorContext(r.Context(), "files: dial workspace failed", "err", err, "addr", addr)
		}
		http.Error(w, "workspace host unavailable", http.StatusServiceUnavailable)
		return nil, nil, "", false
	}
	return c, closer, store.UUIDString(ws.ID), true
}

// grpcToHTTP maps a workspace RPC error onto the closest HTTP status.
func grpcToHTTP(w http.ResponseWriter, err error) {
	switch status.Code(err) {
	case codes.NotFound:
		http.Error(w, "not found", http.StatusNotFound)
	case codes.InvalidArgument:
		http.Error(w, "bad request", http.StatusBadRequest)
	case codes.AlreadyExists:
		http.Error(w, "already exists", http.StatusConflict)
	case codes.PermissionDenied:
		http.Error(w, "permission denied", http.StatusForbidden)
	default:
		http.Error(w, "workspace call failed", http.StatusBadGateway)
	}
}

func (d *FileDeps) list(w http.ResponseWriter, r *http.Request) {
	c, closer, wid, ok := d.client(w, r)
	if !ok {
		return
	}
	defer closer.Close()
	resp, err := c.ListDir(r.Context(), &hv1.ListDirRequest{WorkspaceId: wid, Path: r.URL.Query().Get("path")})
	if err != nil {
		grpcToHTTP(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": resp.Entries})
}

func (d *FileDeps) readContent(w http.ResponseWriter, r *http.Request) {
	c, closer, wid, ok := d.client(w, r)
	if !ok {
		return
	}
	defer closer.Close()
	stream, err := c.ReadFile(r.Context(), &hv1.ReadFileRequest{WorkspaceId: wid, Path: r.URL.Query().Get("path")})
	if err != nil {
		grpcToHTTP(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	// Workspace file content is arbitrary bytes the user put there; never let a
	// browser sniff it into something executable.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	wroteHeader := false
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			if !wroteHeader {
				w.WriteHeader(http.StatusOK)
			}
			return
		}
		if err != nil {
			if !wroteHeader {
				grpcToHTTP(w, err)
			}
			return
		}
		if !wroteHeader {
			w.WriteHeader(http.StatusOK)
			wroteHeader = true
		}
		if _, werr := w.Write(chunk.Data); werr != nil {
			return
		}
	}
}

func (d *FileDeps) writeContent(w http.ResponseWriter, r *http.Request) {
	c, closer, wid, ok := d.client(w, r)
	if !ok {
		return
	}
	defer closer.Close()
	stream, err := c.WriteFile(r.Context())
	if err != nil {
		grpcToHTTP(w, err)
		return
	}
	path := r.URL.Query().Get("path")
	if err := stream.Send(&hv1.WriteFileFrame{Msg: &hv1.WriteFileFrame_Init{
		Init: &hv1.WriteFileInit{WorkspaceId: wid, Path: path},
	}}); err != nil {
		reportSendFailure(w, stream)
		return
	}
	buf := make([]byte, 64*1024)
	for {
		n, rerr := r.Body.Read(buf)
		if n > 0 {
			if serr := stream.Send(&hv1.WriteFileFrame{Msg: &hv1.WriteFileFrame_Data{Data: buf[:n]}}); serr != nil {
				reportSendFailure(w, stream)
				return
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		grpcToHTTP(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bytes_written": resp.BytesWritten})
}

// reportSendFailure maps the real error behind a failed client-stream Send. On
// a client-streaming RPC, Send returns a bare io.EOF once the server has closed
// the stream; the status that actually explains why is delivered by
// CloseAndRecv, so pull it from there rather than mapping the io.EOF.
func reportSendFailure(w http.ResponseWriter, stream hv1.WorkspaceIo_WriteFileClient) {
	if _, rerr := stream.CloseAndRecv(); rerr != nil {
		grpcToHTTP(w, rerr)
		return
	}
	http.Error(w, "workspace call failed", http.StatusBadGateway)
}

func (d *FileDeps) create(w http.ResponseWriter, r *http.Request) {
	c, closer, wid, ok := d.client(w, r)
	if !ok {
		return
	}
	defer closer.Close()
	_, err := c.CreateNode(r.Context(), &hv1.CreateNodeRequest{
		WorkspaceId: wid, Path: r.URL.Query().Get("path"), IsDir: r.URL.Query().Get("dir") == "true",
	})
	if err != nil {
		grpcToHTTP(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (d *FileDeps) rename(w http.ResponseWriter, r *http.Request) {
	c, closer, wid, ok := d.client(w, r)
	if !ok {
		return
	}
	defer closer.Close()
	var body struct{ From, To string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if _, err := c.RenameNode(r.Context(), &hv1.RenameNodeRequest{WorkspaceId: wid, From: body.From, To: body.To}); err != nil {
		grpcToHTTP(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (d *FileDeps) del(w http.ResponseWriter, r *http.Request) {
	c, closer, wid, ok := d.client(w, r)
	if !ok {
		return
	}
	defer closer.Close()
	if _, err := c.DeleteNode(r.Context(), &hv1.DeleteNodeRequest{WorkspaceId: wid, Path: r.URL.Query().Get("path")}); err != nil {
		grpcToHTTP(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
