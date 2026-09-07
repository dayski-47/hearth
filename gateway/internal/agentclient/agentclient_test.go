package agentclient_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/dayski-47/hearth/gateway/internal/agentclient"
	hv1 "github.com/dayski-47/hearth/gateway/internal/hearth/v1"
	"github.com/dayski-47/hearth/gateway/internal/tlsutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	caPath          = "../../../deploy/certs/ca.pem"
	agentCertPath   = "../../../deploy/certs/agent.pem"
	agentKeyPath    = "../../../deploy/certs/agent-key.pem"
	gatewayCertPath = "../../../deploy/certs/gateway.pem"
	gatewayKeyPath  = "../../../deploy/certs/gateway-key.pem"
)

// fakeAgent implements hv1.AgentServer; only GetWorkspace is interesting.
type fakeAgent struct{ hv1.UnimplementedAgentServer }

func (fakeAgent) GetWorkspace(_ context.Context, r *hv1.WorkspaceRef) (*hv1.Workspace, error) {
	return &hv1.Workspace{WorkspaceId: r.WorkspaceId, State: hv1.WorkspaceState_RUNNING, HostId: "h1"}, nil
}

func TestDialAndCall(t *testing.T) {
	srvTLS, err := tlsutil.ServerConfig(caPath, agentCertPath, agentKeyPath)
	if err != nil {
		t.Skip("run `just certs`: ", err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := grpc.NewServer(grpc.Creds(credentials.NewTLS(srvTLS)))
	hv1.RegisterAgentServer(gs, fakeAgent{})
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	cliTLS, err := tlsutil.ClientConfig(caPath, gatewayCertPath, gatewayKeyPath, "hearth-agent")
	if err != nil {
		t.Fatal(err)
	}
	c, closer, err := agentclient.Dial("https://"+lis.Addr().String(), cliTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ws, err := c.GetWorkspace(ctx, &hv1.WorkspaceRef{WorkspaceId: "w1"})
	if err != nil {
		t.Fatal(err)
	}
	if ws.State != hv1.WorkspaceState_RUNNING {
		t.Fatalf("state %v", ws.State)
	}
	if ws.WorkspaceId != "w1" {
		t.Fatalf("workspace id %q", ws.WorkspaceId)
	}
}
