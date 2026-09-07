// Package agentclient dials a hearth-agent's Agent gRPC service over mTLS.
package agentclient

import (
	"crypto/tls"
	"io"
	"strings"

	hv1 "github.com/dayski-47/hearth/gateway/internal/hearth/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Dial connects to an agent at its advertise address (scheme optional) and
// returns a client plus a closer for the connection.
func Dial(addr string, tlsCfg *tls.Config) (hv1.AgentClient, io.Closer, error) {
	target := strings.TrimPrefix(strings.TrimPrefix(addr, "https://"), "http://")
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return nil, nil, err
	}
	return hv1.NewAgentClient(conn), conn, nil
}
