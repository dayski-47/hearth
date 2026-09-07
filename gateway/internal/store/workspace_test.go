package store_test

import (
	"context"
	"testing"

	"github.com/dayski-47/hearth/gateway/internal/store/gen"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestWorkspaceLifecycleQueries(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	u, err := s.Queries().UpsertUser(ctx, gen.UpsertUserParams{Username: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}

	host := "h1"
	// an agent row so the FK on workspaces.agent_id is satisfiable
	if _, err := s.Queries().UpsertAgent(ctx, gen.UpsertAgentParams{ID: host, AdvertiseAddr: "a", Capacity: []byte("{}")}); err != nil {
		t.Fatal(err)
	}

	ws, err := s.Queries().CreateWorkspace(ctx, gen.CreateWorkspaceParams{
		OwnerID: u.ID, Name: "proj", Image: "busybox:stable", AgentID: &host,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ws.State != "creating" {
		t.Fatalf("state %q", ws.State)
	}

	cid := "cid-1"
	if err := s.Queries().SetWorkspacePlacement(ctx, gen.SetWorkspacePlacementParams{
		ID: ws.ID, ContainerID: &cid, State: "running",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Queries().GetWorkspaceForOwner(ctx, gen.GetWorkspaceForOwnerParams{ID: ws.ID, OwnerID: u.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "running" || got.ContainerID == nil || *got.ContainerID != "cid-1" || !got.LastStartedAt.Valid {
		t.Fatalf("placement not applied: %+v", got)
	}

	// wrong owner cannot see it
	other := pgtype.UUID{Bytes: [16]byte{9}, Valid: true}
	if _, err := s.Queries().GetWorkspaceForOwner(ctx, gen.GetWorkspaceForOwnerParams{ID: ws.ID, OwnerID: other}); err == nil {
		t.Fatal("expected not-found for a different owner")
	}

	if err := s.Queries().AppendWorkspaceEvent(ctx, gen.AppendWorkspaceEventParams{
		WorkspaceID: ws.ID, Kind: "running", Detail: []byte(`{"container_id":"cid-1"}`),
	}); err != nil {
		t.Fatal(err)
	}

	recon, err := s.Queries().ListReconcilableWorkspaces(ctx)
	if err != nil || len(recon) != 1 {
		t.Fatalf("reconcilable=%d err=%v", len(recon), err)
	}

	n, err := s.Queries().MarkAgentWorkspacesUnknown(ctx, &host)
	if err != nil || n != 1 {
		t.Fatalf("marked=%d err=%v", n, err)
	}

	if err := s.Queries().DeleteWorkspace(ctx, ws.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Queries().GetWorkspaceForOwner(ctx, gen.GetWorkspaceForOwnerParams{ID: ws.ID, OwnerID: u.ID}); err == nil {
		t.Fatal("expected not-found after delete")
	}
}
