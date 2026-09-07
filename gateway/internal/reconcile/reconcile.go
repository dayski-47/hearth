// Package reconcile converges the workspace table against what the owning
// agents actually report. One Converge call is one pass; the gateway runs it
// on a ticker.
package reconcile

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"time"

	hv1 "github.com/dayski-47/hearth/gateway/internal/hearth/v1"
	"github.com/dayski-47/hearth/gateway/internal/store"
	"github.com/dayski-47/hearth/gateway/internal/store/gen"
	"github.com/jackc/pgx/v5/pgtype"
)

// agentRPCTimeout bounds a single gateway->agent call so one wedged Podman
// socket cannot stall the whole reconcile pass.
const agentRPCTimeout = 20 * time.Second

// Store is the subset of *gen.Queries the reconciler calls. *gen.Queries
// satisfies it.
type Store interface {
	ListReconcilableWorkspaces(context.Context) ([]gen.Workspace, error)
	SetWorkspaceState(context.Context, gen.SetWorkspaceStateParams) error
	AppendWorkspaceEvent(context.Context, gen.AppendWorkspaceEventParams) error
	MarkAgentWorkspacesUnknown(context.Context, *string) ([]pgtype.UUID, error)
}

// Dialer opens a client to an agent at addr. The production impl closes over
// the mTLS *tls.Config and calls agentclient.Dial.
type Dialer interface {
	Dial(addr string) (hv1.AgentClient, io.Closer, error)
}

// Registry resolves an agent's address by id and lists the agents that have
// gone silent. *agentregistry.Registry satisfies it.
type Registry interface {
	Addr(id string) (string, bool)
	LostAgents() []string
}

// Deps are the collaborators for one Converge pass.
type Deps struct {
	Store  Store
	Dial   Dialer
	Reg    Registry
	Logger *slog.Logger
}

var protoToState = map[hv1.WorkspaceState]string{
	hv1.WorkspaceState_RUNNING: "running",
	hv1.WorkspaceState_STOPPED: "stopped",
	hv1.WorkspaceState_ERROR:   "error",
}

// Converge runs one reconciliation pass: every lost agent's workspaces are
// marked unknown in bulk, then each remaining reconcilable row is polled from
// its owning agent and any drift is written back with a "reconciled" event. A
// dial or RPC failure leaves the row alone for the next pass; only a failure to
// list the reconcilable rows is returned as an error.
func Converge(ctx context.Context, d Deps) error {
	lost := map[string]bool{}
	for _, id := range d.Reg.LostAgents() {
		lost[id] = true
		aid := id
		marked, err := d.Store.MarkAgentWorkspacesUnknown(ctx, &aid)
		if err != nil {
			d.Logger.WarnContext(ctx, "mark agent workspaces unknown failed", "agent_id", id, "error", err)
			continue
		}
		if len(marked) == 0 {
			continue
		}
		// Every state change gets an event, bulk mark included.
		detail, _ := json.Marshal(map[string]string{"agent_id": id})
		for _, wsID := range marked {
			if err := d.Store.AppendWorkspaceEvent(ctx, gen.AppendWorkspaceEventParams{
				WorkspaceID: wsID, Kind: "agent_lost", Detail: detail,
			}); err != nil {
				d.Logger.WarnContext(ctx, "append agent_lost event failed", "workspace_id", store.UUIDString(wsID), "error", err)
			}
		}
		d.Logger.WarnContext(ctx, "agent lost, workspaces marked unknown", "agent_id", id, "count", len(marked))
	}

	rows, err := d.Store.ListReconcilableWorkspaces(ctx)
	if err != nil {
		return err
	}
	for _, ws := range rows {
		if ws.AgentID == nil || lost[*ws.AgentID] {
			continue
		}
		addr, ok := d.Reg.Addr(*ws.AgentID)
		if !ok {
			continue
		}
		client, closer, err := d.Dial.Dial(addr)
		if err != nil {
			d.Logger.WarnContext(ctx, "reconcile dial failed", "agent_id", *ws.AgentID, "error", err)
			continue
		}
		rpcCtx, cancel := context.WithTimeout(ctx, agentRPCTimeout)
		got, err := client.GetWorkspace(rpcCtx, &hv1.WorkspaceRef{WorkspaceId: store.UUIDString(ws.ID)})
		cancel()
		closer.Close()
		if err != nil {
			d.Logger.WarnContext(ctx, "reconcile GetWorkspace failed", "workspace_id", store.UUIDString(ws.ID), "error", err)
			continue
		}
		want, known := protoToState[got.State]
		if !known || want == ws.State {
			continue
		}
		if err := d.Store.SetWorkspaceState(ctx, gen.SetWorkspaceStateParams{ID: ws.ID, State: want}); err != nil {
			d.Logger.WarnContext(ctx, "reconcile SetWorkspaceState failed", "workspace_id", store.UUIDString(ws.ID), "error", err)
			continue
		}
		detail, _ := json.Marshal(map[string]string{"from": ws.State, "to": want})
		_ = d.Store.AppendWorkspaceEvent(ctx, gen.AppendWorkspaceEventParams{WorkspaceID: ws.ID, Kind: "reconciled", Detail: detail})
		d.Logger.InfoContext(ctx, "workspace reconciled", "workspace_id", store.UUIDString(ws.ID), "from", ws.State, "to", want)
	}
	return nil
}
