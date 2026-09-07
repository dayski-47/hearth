// Package ws bridges a browser WebSocket to the workspace service's
// OpenTerminal gRPC stream.
package ws

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/coder/websocket"
	"github.com/dayski-47/hearth/gateway/internal/auth"
	hv1 "github.com/dayski-47/hearth/gateway/internal/hearth/v1"
	"github.com/dayski-47/hearth/gateway/internal/store"
	"github.com/dayski-47/hearth/gateway/internal/store/gen"
	"github.com/go-chi/chi/v5"
)

type WSStore interface {
	GetWorkspaceForOwner(context.Context, gen.GetWorkspaceForOwnerParams) (gen.Workspace, error)
}
type WSRegistry interface {
	WorkspaceAddr(id string) (string, bool)
}
type WSDialer interface {
	Dial(addr string) (hv1.WorkspaceIoClient, io.Closer, error)
}

type Deps struct {
	Store         WSStore
	Reg           WSRegistry
	Dial          WSDialer
	AllowedOrigin string
	Logger        *slog.Logger
}

func hostOf(origin string) string {
	if u, err := url.Parse(origin); err == nil && u.Host != "" {
		return u.Host
	}
	return origin
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 && n < 100000 {
		return n
	}
	return def
}

func (d Deps) Terminal(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id, err := store.ParseUUID(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "bad workspace id", http.StatusBadRequest)
		return
	}
	ws, err := d.Store.GetWorkspaceForOwner(r.Context(), gen.GetWorkspaceForOwnerParams{ID: id, OwnerID: u.ID})
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if ws.State != "running" {
		http.Error(w, "workspace is not running", http.StatusConflict)
		return
	}
	if ws.AgentID == nil {
		http.Error(w, "workspace has no host", http.StatusConflict)
		return
	}
	addr, ok := d.Reg.WorkspaceAddr(*ws.AgentID)
	if !ok {
		http.Error(w, "workspace host unavailable", http.StatusServiceUnavailable)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{hostOf(d.AllowedOrigin)},
	})
	if err != nil {
		return // Accept already wrote the response (403 on origin mismatch)
	}
	defer conn.CloseNow()
	ctx := r.Context()

	client, closer, err := d.Dial.Dial(addr)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "dial workspace")
		return
	}
	defer closer.Close()

	stream, err := client.OpenTerminal(ctx)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "open terminal")
		return
	}

	shell := r.URL.Query().Get("shell")
	cols := atoiDefault(r.URL.Query().Get("cols"), 80)
	rows := atoiDefault(r.URL.Query().Get("rows"), 24)
	if err := stream.Send(&hv1.TerminalClientFrame{Msg: &hv1.TerminalClientFrame_Init{
		Init: &hv1.TerminalInit{
			WorkspaceId: store.UUIDString(id), Shell: shell,
			Cols: uint32(cols), Rows: uint32(rows),
		},
	}}); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "send init")
		return
	}

	// gRPC -> WS
	go func() {
		for {
			f, err := stream.Recv()
			if err != nil {
				_ = conn.Close(websocket.StatusNormalClosure, "stream ended")
				return
			}
			switch m := f.Msg.(type) {
			case *hv1.TerminalServerFrame_Stdout:
				if conn.Write(ctx, websocket.MessageBinary, m.Stdout) != nil {
					return
				}
			case *hv1.TerminalServerFrame_Exit:
				_ = conn.Close(websocket.StatusNormalClosure, "exit "+strconv.Itoa(int(m.Exit.ExitCode)))
				return
			case *hv1.TerminalServerFrame_Ready:
				// nothing to forward
			}
		}
	}()

	// WS -> gRPC
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			_ = stream.CloseSend()
			return
		}
		if typ != websocket.MessageBinary || len(data) == 0 {
			continue
		}
		switch data[0] {
		case 0x00:
			_ = stream.Send(&hv1.TerminalClientFrame{Msg: &hv1.TerminalClientFrame_Stdin{Stdin: data[1:]}})
		case 0x01:
			if len(data) >= 5 {
				c := binary.BigEndian.Uint16(data[1:3])
				rw := binary.BigEndian.Uint16(data[3:5])
				_ = stream.Send(&hv1.TerminalClientFrame{Msg: &hv1.TerminalClientFrame_Resize{
					Resize: &hv1.TerminalResize{Cols: uint32(c), Rows: uint32(rw)},
				}})
			}
		}
	}
}
