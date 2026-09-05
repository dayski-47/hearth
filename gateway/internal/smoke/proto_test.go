package smoke_test

import (
	"testing"

	hv1 "github.com/dayski-47/hearth/gateway/internal/hearth/v1"
)

func TestProtoTypesExist(t *testing.T) {
	ws := &hv1.Workspace{WorkspaceId: "w1", State: hv1.WorkspaceState_RUNNING}
	if ws.GetState() != hv1.WorkspaceState_RUNNING {
		t.Fatalf("state mismatch")
	}

	reg := &hv1.RegisterRequest{HostId: "h1"}
	if reg.GetHostId() != "h1" {
		t.Fatalf("host id mismatch")
	}

	_ = &hv1.CreateWorkspaceRequest{}
}
