package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dayski-47/hearth/gateway/internal/agentclient"
	"github.com/dayski-47/hearth/gateway/internal/agentregistry"
	"github.com/dayski-47/hearth/gateway/internal/auth"
	"github.com/dayski-47/hearth/gateway/internal/config"
	"github.com/dayski-47/hearth/gateway/internal/grpcserver"
	hv1 "github.com/dayski-47/hearth/gateway/internal/hearth/v1"
	"github.com/dayski-47/hearth/gateway/internal/httpapi"
	"github.com/dayski-47/hearth/gateway/internal/password"
	"github.com/dayski-47/hearth/gateway/internal/reconcile"
	"github.com/dayski-47/hearth/gateway/internal/reqid"
	"github.com/dayski-47/hearth/gateway/internal/store"
	"github.com/dayski-47/hearth/gateway/internal/store/gen"
	"github.com/dayski-47/hearth/gateway/internal/tlsutil"
	"github.com/dayski-47/hearth/gateway/internal/workspaceclient"
	"github.com/dayski-47/hearth/gateway/internal/workspaces"
	"github.com/dayski-47/hearth/gateway/internal/ws"
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

	logger := slog.New(reqid.Handler(slog.NewJSONHandler(os.Stdout, nil)))
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
		AdminUser:      cfg.AdminUser,
		AdminHash:      cfg.AdminPasswordHash,
		SecureCookie:   strings.HasPrefix(cfg.PublicURL, "https://"),
		TrustedProxies: cfg.TrustedProxies,
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
			reg.Restore(a.ID, a.AdvertiseAddr, a.WorkspaceAddr, a.Status, capacity, a.LastHeartbeatAt.Time)
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

	agentTLS, err := tlsutil.ClientConfig(cfg.TLS.CA, cfg.TLS.Cert, cfg.TLS.Key, "hearth-agent")
	if err != nil {
		return err
	}
	dialer := agentDialer{tls: agentTLS}
	wsSvc := workspaces.NewService(st.Queries(), reg, dialer, cfg.Workspace, logger)

	wsTLS, err := tlsutil.ClientConfig(cfg.TLS.CA, cfg.TLS.Cert, cfg.TLS.Key, "hearth-workspace")
	if err != nil {
		return err
	}
	term := &ws.Deps{
		Store:         st.Queries(),
		Reg:           reg,
		Dial:          wsDialer{tls: wsTLS},
		AllowedOrigin: cfg.AllowedOrigin,
		Logger:        logger,
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return httpapi.New(cfg, st, logger, authH, wsSvc, term).Run(gctx) })
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
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-gctx.Done():
				return nil
			case <-t.C:
				// Bound the pass so a wedged agent cannot stall the ticker.
				passCtx, cancel := context.WithTimeout(gctx, 2*time.Minute)
				err := reconcile.Converge(passCtx, reconcile.Deps{
					Store: st.Queries(), Dial: dialer, Reg: reg, Logger: logger,
				})
				cancel()
				if err != nil {
					logger.Warn("reconcile pass failed", "error", err)
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

// agentDialer is the production workspaces.Dialer: it opens an mTLS Agent
// client to the agent at addr.
type agentDialer struct{ tls *tls.Config }

func (d agentDialer) Dial(addr string) (hv1.AgentClient, io.Closer, error) {
	return agentclient.Dial(addr, d.tls)
}

// wsDialer adapts the mTLS config and workspaceclient.Dial to ws.WSDialer.
type wsDialer struct{ tls *tls.Config }

func (d wsDialer) Dial(addr string) (hv1.WorkspaceIoClient, io.Closer, error) {
	return workspaceclient.Dial(addr, d.tls)
}

// storeAgentPersistence adapts *store.Store to grpcserver.AgentPersistence.
type storeAgentPersistence struct{ st *store.Store }

func (p storeAgentPersistence) UpsertAgent(ctx context.Context, id, advertiseAddr, workspaceAddr string, capacity agentregistry.Capacity) error {
	raw, err := json.Marshal(capacity)
	if err != nil {
		return err
	}
	_, err = p.st.Queries().UpsertAgent(ctx, gen.UpsertAgentParams{
		ID: id, AdvertiseAddr: advertiseAddr, WorkspaceAddr: workspaceAddr, Capacity: raw,
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
