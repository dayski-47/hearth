// Package reqid generates and carries a request correlation id across the
// gateway's HTTP and gRPC entry points.
package reqid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

type ctxKey int

const key ctxKey = 0

// New returns a fresh random hex-16 request id.
func New() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// WithID returns a copy of ctx carrying the given request id.
func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, key, id)
}

// FromContext returns the request id carried by ctx, or "" if none.
func FromContext(ctx context.Context) string {
	if v, ok := ctx.Value(key).(string); ok {
		return v
	}
	return ""
}
