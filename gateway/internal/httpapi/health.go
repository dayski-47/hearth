package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
)

// Pinger is satisfied by *store.Store.
type Pinger interface {
	Ping(context.Context) error
}

func HealthHandler(p Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, code := "ok", http.StatusOK
		if err := p.Ping(r.Context()); err != nil {
			status, code = "unavailable", http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
	}
}
