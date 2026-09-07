// Package config loads gateway configuration from the environment.
package config

import (
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dayski-47/hearth/gateway/internal/password"
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
	WebDir            string
	// TrustedProxies are the peers whose X-Forwarded-For header may be
	// believed. Empty means the gateway is reached directly and the header is
	// ignored entirely.
	TrustedProxies []netip.Prefix
	TLS            TLSPaths
	Workspace      WorkspaceDefaults
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
		WebDir:            reqDefault("HEARTH_WEB_DIR", "web"),
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

	// A hash the verifier cannot parse would silently reject every login, so
	// reject it at startup instead. Verifying the empty password against a
	// well-formed hash returns (false, nil); only a malformed one errors.
	if c.AdminPasswordHash != "" {
		if _, err := password.Verify(c.AdminPasswordHash, ""); err != nil {
			m.add("HEARTH_ADMIN_PASSWORD_HASH: " + err.Error())
		}
	}

	c.TrustedProxies = prefixList(m, "HEARTH_TRUSTED_PROXIES")

	c.Workspace = WorkspaceDefaults{
		Image:       reqDefault("HEARTH_WORKSPACE_IMAGE", "ghcr.io/dayski-47/hearth-workspace-base:latest"),
		Network:     reqDefault("HEARTH_WORKSPACE_NETWORK", "egress"),
		CPUMillis:   uint32(intDefault(m, "HEARTH_WORKSPACE_CPU", 2000)),
		MemoryBytes: uint64(intDefault(m, "HEARTH_WORKSPACE_MEMORY", 2<<30)),
		Pids:        uint32(intDefault(m, "HEARTH_WORKSPACE_PIDS", 512)),
		DiskBytes:   uint64(intDefault(m, "HEARTH_WORKSPACE_DISK", 5<<30)),
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
func intDefault(m *multiErr, key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		m.add(key + ": " + err.Error())
		return def
	}
	return n
}

// prefixList parses a comma-separated list of IP addresses and/or CIDRs. A bare
// address becomes a single-host prefix. An unset value yields no prefixes.
func prefixList(m *multiErr, key string) []netip.Prefix {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	var out []netip.Prefix
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if p, err := netip.ParsePrefix(part); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(part)
		if err != nil {
			m.add(key + ": " + part + " is not an IP address or CIDR")
			continue
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out
}

type multiErr struct{ msgs []string }

func (e *multiErr) add(s string) { e.msgs = append(e.msgs, s) }
func (e *multiErr) err() error {
	if len(e.msgs) == 0 {
		return nil
	}
	return fmt.Errorf("config: %s", strings.Join(e.msgs, "; "))
}
