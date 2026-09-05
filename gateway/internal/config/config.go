// Package config loads gateway configuration from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type TLSPaths struct {
	CA   string
	Cert string
	Key  string
}

type WorkspaceDefaults struct {
	Image       string
	Network     string
	CPUMillis   uint32
	MemoryBytes uint64
	Pids        uint32
	DiskBytes   uint64
	IdleTimeout time.Duration
	UserNS      string
}

type Config struct {
	ListenAddr        string
	PublicURL         string
	AllowedOrigin     string
	SessionSecret     []byte
	AdminUser         string
	AdminPasswordHash string
	DatabaseURL       string
	GRPCListenAddr    string
	HostID            string
	TLS               TLSPaths
	Workspace         WorkspaceDefaults
}

func Load() (*Config, error) {
	m := &multiErr{}
	c := &Config{
		ListenAddr:        req(m, "HEARTH_LISTEN_ADDR"),
		PublicURL:         req(m, "HEARTH_PUBLIC_URL"),
		AllowedOrigin:     req(m, "HEARTH_ALLOWED_ORIGIN"),
		AdminUser:         req(m, "HEARTH_ADMIN_USER"),
		AdminPasswordHash: req(m, "HEARTH_ADMIN_PASSWORD_HASH"),
		DatabaseURL:       req(m, "DATABASE_URL"),
		GRPCListenAddr:    req(m, "HEARTH_GRPC_LISTEN_ADDR"),
		HostID:            reqDefault("HEARTH_HOST_ID", "local"),
		TLS: TLSPaths{
			CA:   req(m, "HEARTH_TLS_CA"),
			Cert: req(m, "HEARTH_TLS_CERT"),
			Key:  req(m, "HEARTH_TLS_KEY"),
		},
	}
	secret := req(m, "HEARTH_SESSION_SECRET")
	if len(secret) < 32 {
		m.add("HEARTH_SESSION_SECRET must be at least 32 bytes")
	}
	c.SessionSecret = []byte(secret)

	c.Workspace = WorkspaceDefaults{
		Image:       reqDefault("HEARTH_WORKSPACE_IMAGE", "ghcr.io/dayski-47/hearth-workspace-base:latest"),
		Network:     reqDefault("HEARTH_WORKSPACE_NETWORK", "egress"),
		CPUMillis:   uint32(intDefault("HEARTH_WORKSPACE_CPU", 2000)),
		MemoryBytes: uint64(intDefault("HEARTH_WORKSPACE_MEMORY", 2<<30)),
		Pids:        uint32(intDefault("HEARTH_WORKSPACE_PIDS", 512)),
		DiskBytes:   uint64(intDefault("HEARTH_WORKSPACE_DISK", 5<<30)),
		UserNS:      reqDefault("HEARTH_USERNS", "keep-id"),
	}
	d, err := time.ParseDuration(reqDefault("HEARTH_WORKSPACE_IDLE_TIMEOUT", "30m"))
	if err != nil {
		m.add("HEARTH_WORKSPACE_IDLE_TIMEOUT: " + err.Error())
	}
	c.Workspace.IdleTimeout = d

	if c.Workspace.Network != "egress" && c.Workspace.Network != "none" {
		m.add(`HEARTH_WORKSPACE_NETWORK must be "egress" or "none"`)
	}
	if m.err() != nil {
		return nil, m.err()
	}
	return c, nil
}

func req(m *multiErr, key string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		m.add(key + " is required")
	}
	return v
}
func reqDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
func intDefault(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

type multiErr struct{ msgs []string }

func (e *multiErr) add(s string) { e.msgs = append(e.msgs, s) }
func (e *multiErr) err() error {
	if len(e.msgs) == 0 {
		return nil
	}
	return fmt.Errorf("config: %s", strings.Join(e.msgs, "; "))
}
