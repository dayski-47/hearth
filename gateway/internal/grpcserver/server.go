// Package grpcserver serves the gateway's inbound control RPCs to agents.
package grpcserver

import (
	"context"
	"crypto/tls"
	"log/slog"
	"strings"

	"github.com/dayski-47/hearth/gateway/internal/agentregistry"
	hv1 "github.com/dayski-47/hearth/gateway/internal/hearth/v1"
	"github.com/dayski-47/hearth/gateway/internal/reqid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// agentLeafCN is the common name every hearth-agent leaf certificate carries.
// A future hearth-workspace leaf minted from the same CA must not be able to
// call the control plane, so we bind on it here. Per-host CN binding is a
// Plan 2 concern and deliberately not attempted now.
const agentLeafCN = "hearth-agent"

// AgentPersistence is the durable record the control handlers write through to
// after updating the in-memory registry. The registry is the hot path; a
// persistence error is logged, not fatal.
type AgentPersistence interface {
	UpsertAgent(ctx context.Context, id, advertiseAddr, workspaceAddr string, cap agentregistry.Capacity) error
	TouchHeartbeat(ctx context.Context, id string) error
	SetStatus(ctx context.Context, id, status string) error
}

type control struct {
	hv1.UnimplementedGatewayControlServer
	reg    *agentregistry.Registry
	store  AgentPersistence
	logger *slog.Logger
}

// requireAgentIdentity rejects any caller whose verified TLS leaf certificate
// does not carry the hearth-agent common name.
func requireAgentIdentity(ctx context.Context) error {
	p, ok := peer.FromContext(ctx)
	if !ok || p.AuthInfo == nil {
		return status.Error(codes.PermissionDenied, "client identity required")
	}
	ti, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return status.Error(codes.PermissionDenied, "mutual TLS required")
	}
	chains := ti.State.VerifiedChains
	if len(chains) == 0 || len(chains[0]) == 0 {
		return status.Error(codes.PermissionDenied, "no verified client certificate")
	}
	if chains[0][0].Subject.CommonName != agentLeafCN {
		return status.Error(codes.PermissionDenied, "client certificate not authorized for the control plane")
	}
	return nil
}

func (c *control) RegisterAgent(ctx context.Context, req *hv1.RegisterRequest) (*hv1.RegisterResponse, error) {
	if err := requireAgentIdentity(ctx); err != nil {
		return nil, err
	}
	capacity := agentregistry.Capacity{}
	if pc := req.GetCapacity(); pc != nil {
		capacity.CPUMillis = pc.GetCpuMillis()
		capacity.MemoryBytes = pc.GetMemoryBytes()
	}
	if err := c.reg.Register(ctx, req.GetHostId(), req.GetAdvertiseAddr(), req.GetWorkspaceAddr(), capacity); err != nil {
		return nil, status.Error(codes.Internal, "register agent failed")
	}
	if err := c.store.UpsertAgent(ctx, req.GetHostId(), req.GetAdvertiseAddr(), req.GetWorkspaceAddr(), capacity); err != nil {
		c.log().WarnContext(ctx, "persist agent registration failed",
			"request_id", reqid.FromContext(ctx), "host_id", req.GetHostId(), "error", err)
	}
	c.log().InfoContext(ctx, "agent registered",
		"request_id", reqid.FromContext(ctx),
		"host_id", req.GetHostId(), "advertise_addr", req.GetAdvertiseAddr())
	return &hv1.RegisterResponse{HeartbeatIntervalSeconds: 10}, nil
}

func (c *control) AgentHeartbeat(ctx context.Context, req *hv1.HeartbeatRequest) (*hv1.HeartbeatResponse, error) {
	if err := requireAgentIdentity(ctx); err != nil {
		return nil, err
	}
	if err := c.reg.Heartbeat(ctx, req.GetHostId()); err != nil {
		if strings.Contains(err.Error(), "unknown agent") {
			return nil, status.Error(codes.FailedPrecondition, "agent not registered")
		}
		return nil, status.Error(codes.Internal, "heartbeat failed")
	}
	if err := c.store.TouchHeartbeat(ctx, req.GetHostId()); err != nil {
		c.log().WarnContext(ctx, "persist agent heartbeat failed",
			"request_id", reqid.FromContext(ctx), "host_id", req.GetHostId(), "error", err)
	}
	c.log().InfoContext(ctx, "agent heartbeat",
		"request_id", reqid.FromContext(ctx), "host_id", req.GetHostId())
	return &hv1.HeartbeatResponse{}, nil
}

func (c *control) log() *slog.Logger {
	if c.logger != nil {
		return c.logger
	}
	return slog.Default()
}

// requestIDInterceptor pulls x-request-id from incoming metadata (generating one
// when absent) and puts it on the handler context so log lines can correlate.
func requestIDInterceptor(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	id := ""
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vs := md.Get("x-request-id"); len(vs) > 0 {
			id = vs[0]
		}
	}
	if id == "" {
		id = reqid.New()
	}
	return handler(reqid.WithID(ctx, id), req)
}

// New builds the gateway's gRPC server with mTLS credentials and the
// GatewayControl service registered.
func New(reg *agentregistry.Registry, persistence AgentPersistence, tlsCfg *tls.Config, logger *slog.Logger) *grpc.Server {
	s := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(tlsCfg)),
		grpc.UnaryInterceptor(requestIDInterceptor),
	)
	hv1.RegisterGatewayControlServer(s, &control{reg: reg, store: persistence, logger: logger})
	return s
}
