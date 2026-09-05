package grpcserver_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"testing"
	"time"

	"github.com/dayski-47/hearth/gateway/internal/agentregistry"
	"github.com/dayski-47/hearth/gateway/internal/grpcserver"
	hv1 "github.com/dayski-47/hearth/gateway/internal/hearth/v1"
	"github.com/dayski-47/hearth/gateway/internal/tlsutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	caPath      = "../../../deploy/certs/ca.pem"
	gatewayCert = "../../../deploy/certs/gateway.pem"
	gatewayKey  = "../../../deploy/certs/gateway-key.pem"
	agentCert   = "../../../deploy/certs/agent.pem"
	agentKey    = "../../../deploy/certs/agent-key.pem"
)

type noopPersistence struct{}

func (noopPersistence) UpsertAgent(context.Context, string, string, agentregistry.Capacity) error {
	return nil
}
func (noopPersistence) TouchHeartbeat(context.Context, string) error { return nil }
func (noopPersistence) SetStatus(context.Context, string, string) error {
	return nil
}

func startServer(t *testing.T, reg *agentregistry.Registry) net.Addr {
	t.Helper()
	srvTLS, err := tlsutil.ServerConfig(caPath, gatewayCert, gatewayKey)
	if err != nil {
		t.Skip("run `just certs` first: ", err)
	}
	gs := grpcserver.New(reg, noopPersistence{}, srvTLS, nil)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	return lis.Addr()
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
