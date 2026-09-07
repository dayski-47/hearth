package workspaceclient_test

import (
	"context"
	"net"
	"testing"
	"time"

	hv1 "github.com/dayski-47/hearth/gateway/internal/hearth/v1"
	"github.com/dayski-47/hearth/gateway/internal/tlsutil"
	"github.com/dayski-47/hearth/gateway/internal/workspaceclient"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	caPath            = "../../../deploy/certs/ca.pem"
	gatewayCertPath   = "../../../deploy/certs/gateway.pem"
	gatewayKeyPath    = "../../../deploy/certs/gateway-key.pem"
	workspaceCertPath = "../../../deploy/certs/workspace.pem"
	workspaceKeyPath  = "../../../deploy/certs/workspace-key.pem"
)

// fakeWS implements hv1.WorkspaceIoServer; only ListDir is interesting.
type fakeWS struct {
	hv1.UnimplementedWorkspaceIoServer
}

func (fakeWS) ListDir(_ context.Context, r *hv1.ListDirRequest) (*hv1.ListDirResponse, error) {
	return &hv1.ListDirResponse{
		Entries: []*hv1.Node{
			{Path: "/x", Name: "x"},
		},
	}, nil
}

func TestDialAndCall(t *testing.T) {
	srvTLS, err := tlsutil.ServerConfig(caPath, workspaceCertPath, workspaceKeyPath)
	if err != nil {
		t.Skip("run `just certs`: ", err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := grpc.NewServer(grpc.Creds(credentials.NewTLS(srvTLS)))
	hv1.RegisterWorkspaceIoServer(gs, fakeWS{})
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	cliTLS, err := tlsutil.ClientConfig(caPath, gatewayCertPath, gatewayKeyPath, "hearth-workspace")
	if err != nil {
		t.Fatal(err)
	}
	c, closer, err := workspaceclient.Dial(lis.Addr().String(), cliTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := c.ListDir(ctx, &hv1.ListDirRequest{WorkspaceId: "w1", Path: "/"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(resp.Entries))
	}
	if resp.Entries[0].Path != "/x" {
		t.Fatalf("entry path %q", resp.Entries[0].Path)
	}
	if resp.Entries[0].Name != "x" {
		t.Fatalf("entry name %q", resp.Entries[0].Name)
	}
}
