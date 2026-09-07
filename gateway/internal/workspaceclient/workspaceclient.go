// Package workspaceclient dials a hearth-workspace's WorkspaceIo gRPC service over mTLS.
package workspaceclient

import (
	"crypto/tls"
	"io"
	"strings"

	hv1 "github.com/dayski-47/hearth/gateway/internal/hearth/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Dial connects to a workspace service at its advertise address (scheme optional) and
// returns a client plus a closer for the connection.
func Dial(addr string, tlsCfg *tls.Config) (hv1.WorkspaceIoClient, io.Closer, error) {
	target := strings.TrimPrefix(strings.TrimPrefix(addr, "https://"), "http://")
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return nil, nil, err
	}
	return hv1.NewWorkspaceIoClient(conn), conn, nil
}
