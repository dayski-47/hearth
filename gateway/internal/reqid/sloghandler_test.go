package reqid

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func newTestLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(Handler(slog.NewJSONHandler(buf, nil)))
}

func decode(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("decode %q: %v", buf.String(), err)
	}
	return m
}

func TestHandlerAddsRequestIDFromContext(t *testing.T) {
	var buf bytes.Buffer
	newTestLogger(&buf).InfoContext(WithID(context.Background(), "abc123"), "hello")
	if got := decode(t, &buf)["request_id"]; got != "abc123" {
		t.Fatalf("request_id = %v, want abc123", got)
	}
}

func TestHandlerOmitsRequestIDWhenAbsent(t *testing.T) {
	var buf bytes.Buffer
	newTestLogger(&buf).InfoContext(context.Background(), "hello")
	if _, ok := decode(t, &buf)["request_id"]; ok {
		t.Fatalf("unexpected request_id in %q", buf.String())
	}
}

func TestHandlerDoesNotDoubleAnExplicitRequestID(t *testing.T) {
	var buf bytes.Buffer
	newTestLogger(&buf).InfoContext(WithID(context.Background(), "from-ctx"), "hello", "request_id", "explicit")
	if got := decode(t, &buf)["request_id"]; got != "explicit" {
		t.Fatalf("request_id = %v, want the explicit attr to win", got)
	}
	if n := strings.Count(buf.String(), "request_id"); n != 1 {
		t.Fatalf("request_id appears %d times in %q", n, buf.String())
	}
}

// The id must survive logger.With and logger.WithGroup.
func TestHandlerSurvivesWithAttrsAndGroup(t *testing.T) {
	ctx := WithID(context.Background(), "kept")

	var buf bytes.Buffer
	newTestLogger(&buf).With("host_id", "local").InfoContext(ctx, "hello")
	m := decode(t, &buf)
	if m["request_id"] != "kept" || m["host_id"] != "local" {
		t.Fatalf("WithAttrs lost the id: %v", m)
	}

	buf.Reset()
	newTestLogger(&buf).WithGroup("g").InfoContext(ctx, "hello", "k", "v")
	m = decode(t, &buf)
	g, _ := m["g"].(map[string]any)
	if g["request_id"] != "kept" {
		t.Fatalf("WithGroup lost the id: %v", m)
	}
}

func TestHandlerEnabledDelegates(t *testing.T) {
	inner := slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelWarn})
	h := Handler(inner)
	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("Info should be disabled by the inner handler's level")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Fatal("Error should be enabled")
	}
}
