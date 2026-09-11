// Package ws bridges a browser WebSocket to the workspace service's
// OpenTerminal gRPC stream.
package ws

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/coder/websocket"
	"github.com/dayski-47/hearth/gateway/internal/activity"
	hv1 "github.com/dayski-47/hearth/gateway/internal/hearth/v1"
	"github.com/dayski-47/hearth/gateway/internal/store"
	"github.com/dayski-47/hearth/gateway/internal/wsresolve"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type WSDialer interface {
	Dial(addr string) (hv1.WorkspaceIoClient, io.Closer, error)
}

type Deps struct {
	Resolver      wsresolve.Resolver
	Dial          WSDialer
	AllowedOrigin string
	Logger        *slog.Logger
	Activity      *activity.Tracker
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

func (d Deps) logErr(ctx context.Context, msg string, args ...any) {
	if d.Logger != nil {
		d.Logger.ErrorContext(ctx, msg, args...)
	}
}

func (d Deps) Terminal(w http.ResponseWriter, r *http.Request) {
	wksp, addr, ok := d.Resolver.Resolve(w, r, "running")
	if !ok {
		return
	}
	wid := store.UUIDString(wksp.ID)
	d.Activity.Touch(wid)

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
		d.logErr(ctx, "terminal: dial workspace service failed", "err", err, "workspace_id", wid, "addr", addr)
		_ = conn.Close(websocket.StatusInternalError, "dial workspace")
		return
	}
	defer closer.Close()

	stream, err := client.OpenTerminal(ctx)
	if err != nil {
		d.logErr(ctx, "terminal: open stream failed", "err", err, "workspace_id", wid)
		_ = conn.Close(websocket.StatusInternalError, "open terminal")
		return
	}

	shell := r.URL.Query().Get("shell")
	cols := atoiDefault(r.URL.Query().Get("cols"), 80)
	rows := atoiDefault(r.URL.Query().Get("rows"), 24)
	if err := stream.Send(&hv1.TerminalClientFrame{Msg: &hv1.TerminalClientFrame_Init{
		Init: &hv1.TerminalInit{
			WorkspaceId: wid, Shell: shell,
			Cols: uint32(cols), Rows: uint32(rows),
		},
	}}); err != nil {
		d.logErr(ctx, "terminal: send init failed", "err", err, "workspace_id", wid)
		_ = conn.Close(websocket.StatusInternalError, "send init")
		return
	}

	// gRPC -> WS
	go func() {
		for {
			f, err := stream.Recv()
			if err != nil {
				// A clean end is EOF (shell exited, stream closed) or a
				// cancelled context (the request goroutine tore down first).
				if d.Logger != nil && !errors.Is(err, io.EOF) &&
					status.Code(err) != codes.Canceled && ctx.Err() == nil {
					d.Logger.WarnContext(ctx, "terminal: stream ended", "err", err, "workspace_id", wid)
				}
				_ = conn.Close(websocket.StatusNormalClosure, "stream ended")
				return
			}
			switch m := f.Msg.(type) {
			case *hv1.TerminalServerFrame_Stdout:
				d.Activity.Touch(wid)
				if conn.Write(ctx, websocket.MessageBinary, m.Stdout) != nil {
					return
				}
			case *hv1.TerminalServerFrame_Exit:
				_ = conn.Close(websocket.StatusNormalClosure, "exit "+strconv.Itoa(int(m.Exit.ExitCode)))
				return
			case *hv1.TerminalServerFrame_Ready:
				payload := `{"t":"ready","resumed":false}`
				if m.Ready.Resumed {
					payload = `{"t":"ready","resumed":true}`
				}
				if conn.Write(ctx, websocket.MessageText, []byte(payload)) != nil {
					return
				}
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
		d.Activity.Touch(wid)
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
