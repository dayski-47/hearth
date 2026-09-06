package config

import (
	"net/netip"
	"testing"
)

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func validEnv() map[string]string {
	return map[string]string{
		"HEARTH_LISTEN_ADDR":         "0.0.0.0:8080",
		"HEARTH_PUBLIC_URL":          "http://localhost:8080",
		"HEARTH_ALLOWED_ORIGIN":      "http://localhost:8080",
		"HEARTH_SESSION_SECRET":      "0123456789abcdef0123456789abcdef",
		"HEARTH_ADMIN_USER":          "admin",
		"HEARTH_ADMIN_PASSWORD_HASH": "$argon2id$v=19$m=65536,t=3,p=2$abc$def",
		"DATABASE_URL":               "postgres://h:h@localhost:5432/h?sslmode=disable",
		"HEARTH_GRPC_LISTEN_ADDR":    "0.0.0.0:9090",
		"HEARTH_HOST_ID":             "local",
		"HEARTH_TLS_CA":              "deploy/certs/ca.pem",
		"HEARTH_TLS_CERT":            "deploy/certs/gateway.pem",
		"HEARTH_TLS_KEY":             "deploy/certs/gateway-key.pem",
	}
}

func TestLoadValid(t *testing.T) {
	setEnv(t, validEnv())
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.AdminUser != "admin" || c.GRPCListenAddr != "0.0.0.0:9090" {
		t.Fatalf("unexpected config: %+v", c)
	}
	if len(c.SessionSecret) < 32 {
		t.Fatalf("session secret too short")
	}
}

func TestLoadMissingRequired(t *testing.T) {
	env := validEnv()
	delete(env, "DATABASE_URL")
	setEnv(t, env)
	if _, err := Load(); err == nil {
		t.Fatal("expected error when DATABASE_URL missing")
	}
}

func TestLoadRejectsNonNumericInt(t *testing.T) {
	env := validEnv()
	env["HEARTH_WORKSPACE_PIDS"] = "lots"
	setEnv(t, env)
	if _, err := Load(); err == nil {
		t.Fatal("expected error for non-numeric HEARTH_WORKSPACE_PIDS")
	}
}

func TestLoadShortSecret(t *testing.T) {
	env := validEnv()
	env["HEARTH_SESSION_SECRET"] = "tooshort"
	setEnv(t, env)
	if _, err := Load(); err == nil {
		t.Fatal("expected error for short session secret")
	}
}

// A hash the verifier cannot parse would silently reject every login, so it has
// to fail at startup rather than at 3am.
func TestLoadRejectsMalformedAdminHash(t *testing.T) {
	for _, bad := range []string{
		"not-a-hash",
		"$argon2i$v=19$m=65536,t=3,p=2$abc$def",
		"$argon2id$v=13$m=65536,t=3,p=2$abc$def",
		"$argon2id$v=19$m=bogus,t=3,p=2$abc$def",
		"$argon2id$v=19$m=65536,t=3,p=2$!!!$def",
	} {
		t.Run(bad, func(t *testing.T) {
			env := validEnv()
			env["HEARTH_ADMIN_PASSWORD_HASH"] = bad
			setEnv(t, env)
			if _, err := Load(); err == nil {
				t.Fatalf("expected a config error for %q", bad)
			}
		})
	}
}

func TestLoadTrustedProxies(t *testing.T) {
	env := validEnv()
	env["HEARTH_TRUSTED_PROXIES"] = " 10.0.0.0/8 , 192.168.1.5 , ::1 "
	setEnv(t, env)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.TrustedProxies) != 3 {
		t.Fatalf("got %v", c.TrustedProxies)
	}
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"10.4.5.6", true},
		{"192.168.1.5", true},
		{"192.168.1.6", false},
		{"::1", true},
		{"203.0.113.1", false},
	} {
		got := false
		for _, p := range c.TrustedProxies {
			if p.Contains(netip.MustParseAddr(tc.addr)) {
				got = true
			}
		}
		if got != tc.want {
			t.Fatalf("%s covered=%v, want %v (prefixes %v)", tc.addr, got, tc.want, c.TrustedProxies)
		}
	}
}

func TestLoadDefaultsToNoTrustedProxies(t *testing.T) {
	setEnv(t, validEnv())
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.TrustedProxies) != 0 {
		t.Fatalf("expected no trusted proxies by default, got %v", c.TrustedProxies)
	}
}

func TestLoadRejectsMalformedTrustedProxy(t *testing.T) {
	env := validEnv()
	env["HEARTH_TRUSTED_PROXIES"] = "10.0.0.0/8,not-an-ip"
	setEnv(t, env)
	if _, err := Load(); err == nil {
		t.Fatal("expected a config error for a malformed trusted proxy")
	}
}
