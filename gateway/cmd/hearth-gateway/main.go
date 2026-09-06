package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dayski-47/hearth/gateway/internal/agentregistry"
	"github.com/dayski-47/hearth/gateway/internal/auth"
	"github.com/dayski-47/hearth/gateway/internal/config"
	"github.com/dayski-47/hearth/gateway/internal/grpcserver"
	"github.com/dayski-47/hearth/gateway/internal/httpapi"
	"github.com/dayski-47/hearth/gateway/internal/password"
	"github.com/dayski-47/hearth/gateway/internal/store"
	"github.com/dayski-47/hearth/gateway/internal/store/gen"
	"github.com/dayski-47/hearth/gateway/internal/tlsutil"
	"golang.org/x/sync/errgroup"
)

const agentTTL = 30 * time.Second

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	var err error
	switch cmd {
	case "serve":
		err = runServe()
	case "hash-password":
		err = runHashPassword()
	default:
		fmt.Fprintln(os.Stderr, "unknown command:", cmd)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runServe() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx, logger); err != nil {
		return err
	}
	if _, err := st.Queries().UpsertUser(ctx, gen.UpsertUserParams{
		Username: cfg.AdminUser, PasswordHash: cfg.AdminPasswordHash,
	}); err != nil {
		return err
	}

	authMgr := auth.NewManager(st.Queries(), cfg.SessionSecret, logger)
	authH := auth.NewHandlers(authMgr, st.Queries(), authMgr, auth.Config{
		AdminUser:    cfg.AdminUser,
		AdminHash:    cfg.AdminPasswordHash,
		SecureCookie: strings.HasPrefix(cfg.PublicURL, "https://"),
	}, logger)

	reg := agentregistry.NewInMemory()
	// Rebuild the liveness cache from the durable record on boot, preserving the
	// persisted status/heartbeat/capacity so a "lost" host is not silently
	// revived to "ready" (and Pick-able) by a gateway restart.
	if agents, err := st.Queries().ListAgents(ctx); err == nil {
		for _, a := range agents {
			var capacity agentregistry.Capacity
			if len(a.Capacity) > 0 {
				if err := json.Unmarshal(a.Capacity, &capacity); err != nil {
					logger.Warn("decode persisted agent capacity failed", "host_id", a.ID, "error", err)
				}
			}
			reg.Restore(a.ID, a.AdvertiseAddr, a.Status, capacity, a.LastHeartbeatAt.Time)
		}
	} else {
		logger.Warn("rebuild registry from db failed", "error", err)
	}

	persistence := storeAgentPersistence{st: st}
	grpcTLS, err := tlsutil.ServerConfig(cfg.TLS.CA, cfg.TLS.Cert, cfg.TLS.Key)
	if err != nil {
		return err
	}
	gs := grpcserver.New(reg, persistence, grpcTLS, logger)
	lis, err := net.Listen("tcp", cfg.GRPCListenAddr)
	if err != nil {
		return err
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return httpapi.New(cfg, st, logger, reg, authH).Run(gctx) })
	g.Go(func() error {
		go func() { <-gctx.Done(); gs.GracefulStop() }()
		logger.Info("gateway grpc listening", "addr", cfg.GRPCListenAddr)
		return gs.Serve(lis)
	})
	g.Go(func() error {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-gctx.Done():
				return nil
			case now := <-t.C:
				for _, id := range reg.Sweep(gctx, now, agentTTL) {
					if err := persistence.SetStatus(gctx, id, "lost"); err != nil {
						logger.Warn("persist agent lost failed", "host_id", id, "error", err)
					}
					logger.Warn("agent lost", "host_id", id)
				}
			}
		}
	})
	g.Go(func() error {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-gctx.Done():
				return nil
			case <-t.C:
				if n, err := st.Queries().DeleteExpiredSessions(gctx); err != nil {
					logger.Warn("prune expired sessions failed", "error", err)
				} else if n > 0 {
					logger.Info("pruned expired sessions", "count", n)
				}
				authH.PruneLimiter()
			}
		}
	})
	return g.Wait()
}

// storeAgentPersistence adapts *store.Store to grpcserver.AgentPersistence.
type storeAgentPersistence struct{ st *store.Store }

func (p storeAgentPersistence) UpsertAgent(ctx context.Context, id, advertiseAddr string, capacity agentregistry.Capacity) error {
	raw, err := json.Marshal(capacity)
	if err != nil {
		return err
	}
	_, err = p.st.Queries().UpsertAgent(ctx, gen.UpsertAgentParams{
		ID: id, AdvertiseAddr: advertiseAddr, Capacity: raw,
	})
	return err
}

func (p storeAgentPersistence) TouchHeartbeat(ctx context.Context, id string) error {
	return p.st.Queries().TouchAgentHeartbeat(ctx, id)
}

func (p storeAgentPersistence) SetStatus(ctx context.Context, id, status string) error {
	return p.st.Queries().SetAgentStatus(ctx, gen.SetAgentStatusParams{ID: id, Status: status})
}

func runHashPassword() error {
	fmt.Fprint(os.Stderr, "password: ")
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil {
		return err
	}
	h, err := password.Hash(strings.TrimRight(line, "\r\n"))
	if err != nil {
		return err
	}
	fmt.Println(h)
	return nil
}
