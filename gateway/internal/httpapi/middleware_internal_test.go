package httpapi

import (
	"bufio"
	"net"
	"net/http"
	"testing"
)

// compile-time proof that the access-log wrapper forwards connection upgrades.
var _ http.Hijacker = (*statusWriter)(nil)
var _ http.Flusher = (*statusWriter)(nil)

type hijackableRecorder struct {
	http.ResponseWriter
	hijacked bool
}

func (h *hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijacked = true
	c1, _ := net.Pipe()
	return c1, bufio.NewReadWriter(bufio.NewReader(c1), bufio.NewWriter(c1)), nil
}

func TestStatusWriterForwardsHijack(t *testing.T) {
	rec := &hijackableRecorder{}
	sw := &statusWriter{ResponseWriter: rec, status: 200}

	hj, ok := any(sw).(http.Hijacker)
	if !ok {
		t.Fatal("*statusWriter does not satisfy http.Hijacker")
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		t.Fatalf("hijack: %v", err)
	}
	_ = conn.Close()
	if !rec.hijacked {
		t.Fatal("hijack was not forwarded to the underlying writer")
	}
}

type plainRecorder struct{ http.ResponseWriter }

func TestStatusWriterHijackErrorsWithoutSupport(t *testing.T) {
	sw := &statusWriter{ResponseWriter: plainRecorder{}, status: 200}
	if _, _, err := sw.Hijack(); err == nil {
		t.Fatal("expected error when underlying writer is not a Hijacker")
	}
}
