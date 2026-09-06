package reqid

import (
	"context"
	"log/slog"
)

const attrKey = "request_id"

// Handler wraps inner so that every record logged with a context carrying a
// request id gains a request_id attribute. Without it the *Context logging
// calls scattered through the gateway emit no correlation id at all.
func Handler(inner slog.Handler) slog.Handler {
	return &handler{inner: inner}
}

type handler struct{ inner slog.Handler }

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *handler) Handle(ctx context.Context, rec slog.Record) error {
	if id := FromContext(ctx); id != "" && !hasRequestID(rec) {
		rec = rec.Clone()
		rec.AddAttrs(slog.String(attrKey, id))
	}
	return h.inner.Handle(ctx, rec)
}

// WithAttrs and WithGroup re-wrap so the behaviour survives logger.With.
func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &handler{inner: h.inner.WithAttrs(attrs)}
}

func (h *handler) WithGroup(name string) slog.Handler {
	return &handler{inner: h.inner.WithGroup(name)}
}

func hasRequestID(rec slog.Record) bool {
	found := false
	rec.Attrs(func(a slog.Attr) bool {
		if a.Key == attrKey {
			found = true
			return false
		}
		return true
	})
	return found
}
