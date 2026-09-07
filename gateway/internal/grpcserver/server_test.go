package grpcserver_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/dayski-47/hearth/gateway/internal/agentregistry"
	"github.com/dayski-47/hearth/gateway/internal/grpcserver"
	hv1 "github.com/dayski-47/hearth/gateway/internal/hearth/v1"
	"github.com/dayski-47/hearth/gateway/internal/tlsutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

const (
	caPath      = "../../../deploy/certs/ca.pem"
	gatewayCert = "../../../deploy/certs/gateway.pem"
	gatewayKey  = "../../../deploy/certs/gateway-key.pem"
	agentCert   = "../../../deploy/certs/agent.pem"
	agentKey    = "../../../deploy/certs/agent-key.pem"
)

type noopPersistence struct{}

func (noopPersistence) UpsertAgent(context.Context, string, string, string, agentregistry.Capacity) error {
	return nil
}
func (noopPersistence) TouchHeartbeat(context.Context, string) error { return nil }
func (noopPersistence) SetStatus(context.Context, string, string) error {
	return nil
}

func startServer(t *testing.T, reg *agentregistry.Registry) net.Addr {
	return startServerLogged(t, reg, nil)
}

func startServerLogged(t *testing.T, reg *agentregistry.Registry, logger *slog.Logger) net.Addr {
	t.Helper()
	srvTLS, err := tlsutil.ServerConfig(caPath, gatewayCert, gatewayKey)
	if err != nil {
		t.Skip("run `just certs` first: ", err)
	}
	gs := grpcserver.New(reg, noopPersistence{}, srvTLS, logger)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	return lis.Addr()
}

// caSignedLeaf mints a client leaf with the given CN, signed by the local dev CA.
func caSignedLeaf(t *testing.T, cn string) tls.Certificate {
	t.Helper()
	caCertPEM, err := os.ReadFile(caPath)
	if err != nil {
		t.Skip("run `just certs` first: ", err)
	}
	caKeyPEM, err := os.ReadFile("../../../deploy/certs/ca-key.pem")
	if err != nil {
		t.Skip("run `just certs` first: ", err)
	}
	blk, _ := pem.Decode(caCertPEM)
	caCert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	kblk, _ := pem.Decode(caKeyPEM)
	caKey, err := x509.ParsePKCS8PrivateKey(kblk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost", cn},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, pub, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// captureHandler is a minimal slog.Handler that records attrs of each record.
type captureHandler struct {
	mu      sync.Mutex
	records []map[string]any
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *captureHandler) WithGroup(string) slog.Handler            { return h }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	m := map[string]any{"msg": r.Message}
	r.Attrs(func(a slog.Attr) bool {
		m[a.Key] = a.Value.Any()
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, m)
	h.mu.Unlock()
	return nil
}

func (h *captureHandler) find(msg string) map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r["msg"] == msg {
			return r
		}
	}
	return nil
}

func TestNonAgentClientCNRejected(t *testing.T) {
	reg := agentregistry.NewInMemory()
	addr := startServer(t, reg)

	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		t.Skip("run `just certs` first: ", err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(caPEM)
	// A leaf the CA vouches for, but not a hearth-agent identity.
	cliTLS := &tls.Config{
		Certificates: []tls.Certificate{caSignedLeaf(t, "hearth-workspace")},
		RootCAs:      roots,
		ServerName:   "hearth-gateway",
		MinVersion:   tls.VersionTLS13,
	}
	conn, err := grpc.NewClient(addr.String(), grpc.WithTransportCredentials(credentials.NewTLS(cliTLS)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := hv1.NewGatewayControlClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = c.RegisterAgent(ctx, &hv1.RegisterRequest{HostId: "h1"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied for non-agent CN, got %v", err)
	}
	if len(reg.List()) != 0 {
		t.Fatalf("registry must not be touched: %+v", reg.List())
	}
}

func TestGeneratedRequestIDOnRegister(t *testing.T) {
	ch := &captureHandler{}
	reg := agentregistry.NewInMemory()
	addr := startServerLogged(t, reg, slog.New(ch))

	cliTLS, err := tlsutil.ClientConfig(caPath, agentCert, agentKey, "hearth-gateway")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(addr.String(), grpc.WithTransportCredentials(credentials.NewTLS(cliTLS)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := hv1.NewGatewayControlClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// No x-request-id metadata attached: the interceptor must generate one.
	if _, err := c.RegisterAgent(ctx, &hv1.RegisterRequest{HostId: "h1", AdvertiseAddr: "a"}); err != nil {
		t.Fatal(err)
	}
	rec := ch.find("agent registered")
	if rec == nil {
		t.Fatal("no 'agent registered' log line captured")
	}
	if id, _ := rec["request_id"].(string); id == "" {
		t.Fatalf("expected a generated request_id in the log line, got %+v", rec)
	}
}

func TestRegisterAndHeartbeatOverMTLS(t *testing.T) {
	reg := agentregistry.NewInMemory()
	addr := startServer(t, reg)

	cliTLS, err := tlsutil.ClientConfig(caPath, agentCert, agentKey, "hearth-gateway")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(addr.String(), grpc.WithTransportCredentials(credentials.NewTLS(cliTLS)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := hv1.NewGatewayControlClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := c.RegisterAgent(ctx, &hv1.RegisterRequest{
		HostId:        "h1",
		AdvertiseAddr: "https://localhost:9091",
		WorkspaceAddr: "https://localhost:9092",
		Capacity:      &hv1.Capacity{CpuMillis: 4000, MemoryBytes: 1 << 30},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AgentHeartbeat(ctx, &hv1.HeartbeatRequest{HostId: "h1"}); err != nil {
		t.Fatal(err)
	}
	got := reg.List()
	if len(got) != 1 || got[0].ID != "h1" {
		t.Fatalf("registry not updated: %+v", got)
	}
	if got[0].Capacity.CPUMillis != 4000 {
		t.Fatalf("capacity not propagated: %+v", got[0].Capacity)
	}
	if ws, ok := reg.WorkspaceAddr("h1"); !ok || ws != "https://localhost:9092" {
		t.Fatalf("workspace addr not propagated: %q %v", ws, ok)
	}
}

func TestNoClientCertRejected(t *testing.T) {
	reg := agentregistry.NewInMemory()
	addr := startServer(t, reg)

	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		t.Skip("run `just certs` first: ", err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(caPEM)
	// Valid server trust, but no client identity presented.
	cliTLS := &tls.Config{RootCAs: roots, ServerName: "hearth-gateway", MinVersion: tls.VersionTLS13}

	conn, err := grpc.NewClient(addr.String(), grpc.WithTransportCredentials(credentials.NewTLS(cliTLS)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := hv1.NewGatewayControlClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := c.RegisterAgent(ctx, &hv1.RegisterRequest{HostId: "h1"}); err == nil {
		t.Fatal("expected RegisterAgent to fail without a client certificate")
	}
	if len(reg.List()) != 0 {
		t.Fatalf("registry must not be touched by an unauthenticated call: %+v", reg.List())
	}
}

func TestWrongCAClientCertRejected(t *testing.T) {
	reg := agentregistry.NewInMemory()
	addr := startServer(t, reg)

	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		t.Skip("run `just certs` first: ", err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(caPEM)

	// A client identity signed by a foreign CA the gateway does not trust.
	foreign := selfSignedCert(t)
	cliTLS := &tls.Config{
		Certificates: []tls.Certificate{foreign},
		RootCAs:      roots,
		ServerName:   "hearth-gateway",
		MinVersion:   tls.VersionTLS13,
	}

	conn, err := grpc.NewClient(addr.String(), grpc.WithTransportCredentials(credentials.NewTLS(cliTLS)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := hv1.NewGatewayControlClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := c.RegisterAgent(ctx, &hv1.RegisterRequest{HostId: "h1"}); err == nil {
		t.Fatal("expected RegisterAgent to fail with a client cert from an untrusted CA")
	}
	if len(reg.List()) != 0 {
		t.Fatalf("registry must not be touched by an unauthenticated call: %+v", reg.List())
	}
}

func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "hearth-agent"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{"hearth-agent", "localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}
