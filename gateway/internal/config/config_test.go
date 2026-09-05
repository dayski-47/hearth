package config

import (
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

func TestLoadShortSecret(t *testing.T) {
	env := validEnv()
	env["HEARTH_SESSION_SECRET"] = "tooshort"
	setEnv(t, env)
	if _, err := Load(); err == nil {
		t.Fatal("expected error for short session secret")
	}
}
