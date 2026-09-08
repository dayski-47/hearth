package ws

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/coder/websocket"
	hv1 "github.com/dayski-47/hearth/gateway/internal/hearth/v1"
	"github.com/dayski-47/hearth/gateway/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Events streams a workspace's file-change events to the browser. The socket is
// server->browser only, so unlike the terminal bridge it uses conn.CloseRead:
// there is no client frame to read, and CloseRead still handles pings and the
// close handshake and gives a context that cancels when the browser goes away.
func (d Deps) Events(w http.ResponseWriter, r *http.Request) {
	wksp, addr, ok := d.Resolver.Resolve(w, r, "running", "stopped")
	if !ok {
		return
	}
	wid := store.UUIDString(wksp.ID)

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{hostOf(d.AllowedOrigin)},
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := conn.CloseRead(r.Context())

	client, closer, err := d.Dial.Dial(addr)
	if err != nil {
		d.logErr(ctx, "events: dial workspace failed", "err", err, "workspace_id", wid, "addr", addr)
		_ = conn.Close(websocket.StatusInternalError, "dial workspace")
		return
	}
	defer closer.Close()

	stream, err := client.WatchChanges(ctx, &hv1.WatchRequest{WorkspaceId: wid})
	if err != nil {
		d.logErr(ctx, "events: open stream failed", "err", err, "workspace_id", wid)
		_ = conn.Close(websocket.StatusInternalError, "open watch")
		return
	}

	for {
		ev, err := stream.Recv()
		if err != nil {
			if d.Logger != nil && !errors.Is(err, io.EOF) &&
				status.Code(err) != codes.Canceled && ctx.Err() == nil {
				d.Logger.WarnContext(ctx, "events: stream ended", "err", err, "workspace_id", wid)
			}
			_ = conn.Close(websocket.StatusNormalClosure, "stream ended")
			return
		}
		msg, merr := json.Marshal(map[string]any{"path": ev.Path, "kind": ev.Kind.String()})
		if merr != nil {
			continue
		}
		if conn.Write(ctx, websocket.MessageText, msg) != nil {
			return
		}
	}
}
