// Package workspaces orchestrates workspace lifecycle across the store and the
// owning agent. The store is the source of truth; the agent is driven and then
// the store is converged to what it reported.
package workspaces

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"

	"github.com/dayski-47/hearth/gateway/internal/agentregistry"
	"github.com/dayski-47/hearth/gateway/internal/config"
	hv1 "github.com/dayski-47/hearth/gateway/internal/hearth/v1"
	"github.com/dayski-47/hearth/gateway/internal/store"
	"github.com/dayski-47/hearth/gateway/internal/store/gen"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	// ErrNoName is returned by Create when the workspace name is blank.
	ErrNoName = errors.New("workspaces: name is required")
	// ErrNotFound is returned when a workspace does not exist for the owner.
	ErrNotFound = errors.New("workspaces: not found")
	// ErrNoAgent is returned by Create when the registry has no agent to place on.
	ErrNoAgent = errors.New("workspaces: no agent available")
	// ErrAgentCall is returned when an agent RPC fails or reports an unusable
	// state; the row is left parked in "error".
	ErrAgentCall = errors.New("workspaces: agent call failed")
)

// Store is the subset of *gen.Queries the service calls. *gen.Queries satisfies it.
type Store interface {
	CreateWorkspace(context.Context, gen.CreateWorkspaceParams) (gen.Workspace, error)
	GetWorkspaceForOwner(context.Context, gen.GetWorkspaceForOwnerParams) (gen.Workspace, error)
	ListWorkspacesForOwner(context.Context, pgtype.UUID) ([]gen.Workspace, error)
	SetWorkspacePlacement(context.Context, gen.SetWorkspacePlacementParams) error
	SetWorkspaceState(context.Context, gen.SetWorkspaceStateParams) error
	DeleteWorkspace(context.Context, pgtype.UUID) error
	AppendWorkspaceEvent(context.Context, gen.AppendWorkspaceEventParams) error
}

// Registry places workspaces on agents and resolves an agent's address by id.
// *agentregistry.Registry satisfies it.
type Registry interface {
	Pick(context.Context) (agentregistry.Agent, error)
	Addr(id string) (string, bool)
}

// Dialer opens a client to an agent at addr. The production impl closes over the
// mTLS *tls.Config and calls agentclient.Dial.
type Dialer interface {
	Dial(addr string) (hv1.AgentClient, io.Closer, error)
}

// Service drives the workspace create/start/stop/destroy state machine.
type Service struct {
	st     Store
	reg    Registry
	dial   Dialer
	def    config.WorkspaceDefaults
	logger *slog.Logger
}

// NewService builds a Service over the given store, registry, dialer and defaults.
func NewService(st Store, reg Registry, dial Dialer, def config.WorkspaceDefaults, logger *slog.Logger) *Service {
	return &Service{st: st, reg: reg, dial: dial, def: def, logger: logger}
}

func (s *Service) event(ctx context.Context, id pgtype.UUID, kind string, detail any) {
	raw, _ := json.Marshal(detail)
	if err := s.st.AppendWorkspaceEvent(ctx, gen.AppendWorkspaceEventParams{WorkspaceID: id, Kind: kind, Detail: raw}); err != nil {
		s.logger.WarnContext(ctx, "append workspace event failed", "workspace_id", store.UUIDString(id), "kind", kind, "error", err)
	}
}

func (s *Service) limits() *hv1.ResourceLimits {
	return &hv1.ResourceLimits{
		CpuMillis:   s.def.CPUMillis,
		MemoryBytes: s.def.MemoryBytes,
		Pids:        s.def.Pids,
		DiskBytes:   s.def.DiskBytes,
	}
}

// Create picks an agent, writes a "creating" row, drives the agent's
// CreateWorkspace, and converges the row to "running" or, on any failure, to
// "error" with an event and ErrAgentCall.
func (s *Service) Create(ctx context.Context, ownerID pgtype.UUID, name, image string) (gen.Workspace, error) {
	if strings.TrimSpace(name) == "" {
		return gen.Workspace{}, ErrNoName
	}
	if image == "" {
		image = s.def.Image
	}
	agent, err := s.reg.Pick(ctx)
	if err != nil {
		return gen.Workspace{}, ErrNoAgent
	}
	host := agent.ID
	ws, err := s.st.CreateWorkspace(ctx, gen.CreateWorkspaceParams{
		OwnerID: ownerID, Name: name, Image: image, AgentID: &host,
	})
	if err != nil {
		return gen.Workspace{}, err
	}
	s.event(ctx, ws.ID, "created", map[string]string{"image": image, "agent_id": host})

	fail := func(reason string) (gen.Workspace, error) {
		_ = s.st.SetWorkspaceState(ctx, gen.SetWorkspaceStateParams{ID: ws.ID, State: "error"})
		s.event(ctx, ws.ID, "error", map[string]string{"reason": reason})
		s.logger.WarnContext(ctx, "workspace operation failed", "workspace_id", store.UUIDString(ws.ID), "reason", reason)
		ws.State = "error"
		return ws, ErrAgentCall
	}

	client, closer, err := s.dial.Dial(agent.AdvertiseAddr)
	if err != nil {
		s.logger.ErrorContext(ctx, "dial agent failed", "workspace_id", store.UUIDString(ws.ID), "agent_id", host, "error", err)
		return fail("dial agent: " + err.Error())
	}
	defer closer.Close()

	resp, err := client.CreateWorkspace(ctx, &hv1.CreateWorkspaceRequest{
		WorkspaceId: store.UUIDString(ws.ID), Image: image, Limits: s.limits(),
		Network: s.def.Network, Userns: s.def.UserNS,
	})
	if err != nil {
		s.logger.ErrorContext(ctx, "agent CreateWorkspace failed", "workspace_id", store.UUIDString(ws.ID), "error", err)
		return fail("agent CreateWorkspace: " + err.Error())
	}
	if resp.State != hv1.WorkspaceState_RUNNING {
		return fail("agent reported " + resp.State.String() + ": " + resp.Message)
	}

	cid := resp.ContainerId
	if err := s.st.SetWorkspacePlacement(ctx, gen.SetWorkspacePlacementParams{
		ID: ws.ID, ContainerID: &cid, State: "running",
	}); err != nil {
		return gen.Workspace{}, err
	}
	s.event(ctx, ws.ID, "running", map[string]string{"container_id": cid})
	s.logger.InfoContext(ctx, "workspace running", "workspace_id", store.UUIDString(ws.ID), "container_id", cid)
	ws.State, ws.ContainerID = "running", &cid
	return ws, nil
}

// Get returns the owner's workspace, or ErrNotFound.
func (s *Service) Get(ctx context.Context, ownerID, id pgtype.UUID) (gen.Workspace, error) {
	ws, err := s.st.GetWorkspaceForOwner(ctx, gen.GetWorkspaceForOwnerParams{ID: id, OwnerID: ownerID})
	if err != nil {
		return gen.Workspace{}, ErrNotFound
	}
	return ws, nil
}

// List returns all of the owner's workspaces.
func (s *Service) List(ctx context.Context, ownerID pgtype.UUID) ([]gen.Workspace, error) {
	return s.st.ListWorkspacesForOwner(ctx, ownerID)
}

// Start drives the owning agent's StartWorkspace and converges the row.
func (s *Service) Start(ctx context.Context, ownerID, id pgtype.UUID) (gen.Workspace, error) {
	return s.drive(ctx, ownerID, id, "StartWorkspace", func(c hv1.AgentClient) (*hv1.Workspace, error) {
		return c.StartWorkspace(ctx, &hv1.WorkspaceRef{WorkspaceId: store.UUIDString(id)})
	})
}

// Stop drives the owning agent's StopWorkspace and converges the row.
func (s *Service) Stop(ctx context.Context, ownerID, id pgtype.UUID) (gen.Workspace, error) {
	return s.drive(ctx, ownerID, id, "StopWorkspace", func(c hv1.AgentClient) (*hv1.Workspace, error) {
		return c.StopWorkspace(ctx, &hv1.WorkspaceRef{WorkspaceId: store.UUIDString(id)})
	})
}

// Destroy tells the owning agent to tear the workspace down and then deletes the
// row. A "deleting" event is written before the agent call; any agent failure
// parks the row in "error" and returns ErrAgentCall without deleting it.
func (s *Service) Destroy(ctx context.Context, ownerID, id pgtype.UUID) error {
	ws, err := s.st.GetWorkspaceForOwner(ctx, gen.GetWorkspaceForOwnerParams{ID: id, OwnerID: ownerID})
	if err != nil {
		return ErrNotFound
	}
	_ = s.st.SetWorkspaceState(ctx, gen.SetWorkspaceStateParams{ID: id, State: "deleting"})
	s.event(ctx, id, "deleting", nil)
	s.logger.InfoContext(ctx, "workspace deleting", "workspace_id", store.UUIDString(id))

	if ws.AgentID != nil {
		addr, ok := s.reg.Addr(*ws.AgentID)
		if !ok {
			return s.parkErr(ctx, id, "agent "+*ws.AgentID+" not in registry")
		}
		client, closer, derr := s.dial.Dial(addr)
		if derr != nil {
			return s.parkErr(ctx, id, "dial agent: "+derr.Error())
		}
		defer closer.Close()
		if _, cerr := client.DestroyWorkspace(ctx, &hv1.WorkspaceRef{WorkspaceId: store.UUIDString(id)}); cerr != nil {
			return s.parkErr(ctx, id, "agent DestroyWorkspace: "+cerr.Error())
		}
	}
	if err := s.st.DeleteWorkspace(ctx, id); err != nil {
		return err
	}
	s.logger.InfoContext(ctx, "workspace destroyed", "workspace_id", store.UUIDString(id))
	return nil
}

// parkErr sets the row to error, records the reason, and returns ErrAgentCall.
func (s *Service) parkErr(ctx context.Context, id pgtype.UUID, reason string) error {
	_ = s.st.SetWorkspaceState(ctx, gen.SetWorkspaceStateParams{ID: id, State: "error"})
	s.event(ctx, id, "error", map[string]string{"reason": reason})
	s.logger.WarnContext(ctx, "workspace operation failed", "workspace_id", store.UUIDString(id), "reason", reason)
	return ErrAgentCall
}

// drive dials the workspace's agent, runs call, maps the reported state onto the
// row, writes an event, and returns the refreshed row. Shared by Start/Stop.
func (s *Service) drive(ctx context.Context, ownerID, id pgtype.UUID, kind string,
	call func(hv1.AgentClient) (*hv1.Workspace, error)) (gen.Workspace, error) {

	ws, err := s.st.GetWorkspaceForOwner(ctx, gen.GetWorkspaceForOwnerParams{ID: id, OwnerID: ownerID})
	if err != nil {
		return gen.Workspace{}, ErrNotFound
	}

	// fail parks the row in "error" and returns the row with a matching State,
	// so the caller (and the HTTP 502 body) sees "error", not the stale
	// pre-transition state. Mirrors Create's fail() closure.
	fail := func(reason string) (gen.Workspace, error) {
		err := s.parkErr(ctx, id, reason)
		ws.State = "error"
		return ws, err
	}

	if ws.AgentID == nil {
		return fail("workspace has no agent")
	}
	addr, ok := s.reg.Addr(*ws.AgentID)
	if !ok {
		return fail("agent " + *ws.AgentID + " not in registry")
	}
	client, closer, derr := s.dial.Dial(addr)
	if derr != nil {
		return fail("dial agent: " + derr.Error())
	}
	defer closer.Close()

	resp, cerr := call(client)
	if cerr != nil {
		return fail("agent " + kind + ": " + cerr.Error())
	}
	want, known := map[hv1.WorkspaceState]string{
		hv1.WorkspaceState_RUNNING: "running",
		hv1.WorkspaceState_STOPPED: "stopped",
	}[resp.State]
	if !known {
		return fail("agent " + kind + " reported " + resp.State.String() + ": " + resp.Message)
	}
	if err := s.st.SetWorkspaceState(ctx, gen.SetWorkspaceStateParams{ID: id, State: want}); err != nil {
		return gen.Workspace{}, err
	}
	s.event(ctx, id, want, nil)
	s.logger.InfoContext(ctx, "workspace "+want, "workspace_id", store.UUIDString(id))
	ws.State = want
	return ws, nil
}
