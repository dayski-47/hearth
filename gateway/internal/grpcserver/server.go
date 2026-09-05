// Package grpcserver serves the gateway's inbound control RPCs to agents.
package grpcserver

import (
	"context"
	"crypto/tls"
	"log/slog"

	"github.com/dayski-47/hearth/gateway/internal/agentregistry"
	hv1 "github.com/dayski-47/hearth/gateway/internal/hearth/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// AgentPersistence is the durable record the control handlers write through to
// after updating the in-memory registry. The registry is the hot path; a
// persistence error is logged, not fatal.
type AgentPersistence interface {
	UpsertAgent(ctx context.Context, id, advertiseAddr string, cap agentregistry.Capacity) error
	TouchHeartbeat(ctx context.Context, id string) error
	SetStatus(ctx context.Context, id, status string) error
}

type control struct {
	hv1.UnimplementedGatewayControlServer
	reg    *agentregistry.Registry
	store  AgentPersistence
	logger *slog.Logger
}

func (c *control) RegisterAgent(ctx context.Context, req *hv1.RegisterRequest) (*hv1.RegisterResponse, error) {
	capacity := agentregistry.Capacity{}
	if pc := req.GetCapacity(); pc != nil {
		capacity.CPUMillis = pc.GetCpuMillis()
		capacity.MemoryBytes = pc.GetMemoryBytes()
	}
	if err := c.reg.Register(ctx, req.GetHostId(), req.GetAdvertiseAddr(), capacity); err != nil {
		return nil, err
	}
	if c.store != nil {
		if err := c.store.UpsertAgent(ctx, req.GetHostId(), req.GetAdvertiseAddr(), capacity); err != nil {
			c.log().WarnContext(ctx, "persist agent registration failed", "host_id", req.GetHostId(), "error", err)
		}
	}
	c.log().InfoContext(ctx, "agent registered", "host_id", req.GetHostId(), "advertise_addr", req.GetAdvertiseAddr())
	return &hv1.RegisterResponse{HeartbeatIntervalSeconds: 10}, nil
}

func (c *control) AgentHeartbeat(ctx context.Context, req *hv1.HeartbeatRequest) (*hv1.HeartbeatResponse, error) {
	if err := c.reg.Heartbeat(ctx, req.GetHostId()); err != nil {
		return nil, err
	}
	if c.store != nil {
		if err := c.store.TouchHeartbeat(ctx, req.GetHostId()); err != nil {
			c.log().WarnContext(ctx, "persist agent heartbeat failed", "host_id", req.GetHostId(), "error", err)
		}
	}
	return &hv1.HeartbeatResponse{}, nil
}

func (c *control) log() *slog.Logger {
	if c.logger != nil {
		return c.logger
	}
	return slog.Default()
}

// New builds the gateway's gRPC server with mTLS credentials and the
// GatewayControl service registered.
func New(reg *agentregistry.Registry, persistence AgentPersistence, tlsCfg *tls.Config, logger *slog.Logger) *grpc.Server {
	s := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsCfg)))
	hv1.RegisterGatewayControlServer(s, &control{reg: reg, store: persistence, logger: logger})
	return s
}
