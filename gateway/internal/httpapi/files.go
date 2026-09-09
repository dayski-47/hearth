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

// grpcToHTTP maps a workspace RPC error onto the closest HTTP status and writes
// the gRPC status message as a JSON {"error": ...} body, so the browser can
// show why a file operation failed (too large, escapes the workspace, is a
// directory, ...). The status message is written by the workspace service.
func grpcToHTTP(w http.ResponseWriter, err error) {
	st := status.Convert(err)
	code := http.StatusBadGateway
	switch st.Code() {
	case codes.NotFound:
		code = http.StatusNotFound
	case codes.InvalidArgument:
		code = http.StatusBadRequest
	case codes.AlreadyExists:
		code = http.StatusConflict
	case codes.PermissionDenied:
		code = http.StatusForbidden
	}
	msg := st.Message()
	if msg == "" {
		msg = "workspace call failed"
	}
	writeJSONError(w, code, msg)
}

// fileNode is the gateway's explicit shape for a directory entry. Marshalling
// the protobuf Node directly drops is_dir, size, and modified_unix whenever
// they are zero, so the browser cannot tell a 0-byte file from a directory.
type fileNode struct {
	Path         string `json:"path"`
	Name         string `json:"name"`
	IsDir        bool   `json:"is_dir"`
	Size         uint64 `json:"size"`
	ModifiedUnix int64  `json:"modified_unix"`
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
	entries := make([]fileNode, 0, len(resp.Entries))
	for _, n := range resp.Entries {
		entries = append(entries, fileNode{
			Path: n.Path, Name: n.Name, IsDir: n.IsDir, Size: n.Size, ModifiedUnix: n.ModifiedUnix,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
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
	// The body arrived in full: tell the workspace so it commits the temp. A
	// body read error above returns without this frame, which is what makes
	// the workspace discard a partial upload.
	if serr := stream.Send(&hv1.WriteFileFrame{Msg: &hv1.WriteFileFrame_End{End: &hv1.WriteFileEnd{}}}); serr != nil {
		reportSendFailure(w, stream)
		return
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
