// Package wsresolve authorizes an HTTP request against a workspace and finds
// the address of the hearth-workspace service on the host that owns it. The
// terminal bridge and the file REST handlers share it so the checks — session,
// ownership, state, a reachable host — are written exactly once.
package wsresolve

import (
	"context"
	"net/http"
	"slices"

	"github.com/dayski-47/hearth/gateway/internal/auth"
	"github.com/dayski-47/hearth/gateway/internal/store"
	"github.com/dayski-47/hearth/gateway/internal/store/gen"
	"github.com/go-chi/chi/v5"
)

type Store interface {
	GetWorkspaceForOwner(context.Context, gen.GetWorkspaceForOwnerParams) (gen.Workspace, error)
}

type Registry interface {
	WorkspaceAddr(id string) (string, bool)
}

type Resolver struct {
	Store Store
	Reg   Registry
}

// Resolve returns the workspace and the URL of its host's workspace service.
// When ok is false the HTTP response has already been written.
func (rv Resolver) Resolve(w http.ResponseWriter, r *http.Request, allowedStates ...string) (gen.Workspace, string, bool) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return gen.Workspace{}, "", false
	}
	id, err := store.ParseUUID(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "bad workspace id", http.StatusBadRequest)
		return gen.Workspace{}, "", false
	}
	ws, err := rv.Store.GetWorkspaceForOwner(r.Context(), gen.GetWorkspaceForOwnerParams{ID: id, OwnerID: u.ID})
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return gen.Workspace{}, "", false
	}
	if len(allowedStates) > 0 && !slices.Contains(allowedStates, ws.State) {
		http.Error(w, "workspace state does not allow this", http.StatusConflict)
		return gen.Workspace{}, "", false
	}
	if ws.AgentID == nil {
		http.Error(w, "workspace has no host", http.StatusConflict)
		return gen.Workspace{}, "", false
	}
	addr, ok := rv.Reg.WorkspaceAddr(*ws.AgentID)
	if !ok {
		http.Error(w, "workspace host unavailable", http.StatusServiceUnavailable)
		return gen.Workspace{}, "", false
	}
	return ws, addr, true
}
