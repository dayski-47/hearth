package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/dayski-47/hearth/gateway/internal/auth"
	"github.com/dayski-47/hearth/gateway/internal/store"
	"github.com/dayski-47/hearth/gateway/internal/store/gen"
	"github.com/dayski-47/hearth/gateway/internal/workspaces"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// workspaceHandlers serves the REST surface of the workspace orchestration
// service. The owner is always resolved from the session, never the request
// body, so a caller can only ever see and drive its own workspaces.
type workspaceHandlers struct {
	svc    *workspaces.Service
	logger *slog.Logger
}

// workspaceJSON is the wire shape of a workspace. host_id carries the placement
// (the agent id); container_id is absent until the agent reports one.
type workspaceJSON struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Image         string    `json:"image"`
	State         string    `json:"state"`
	HostID        string    `json:"host_id"`
	ContainerID   string    `json:"container_id,omitempty"`
	HostMountPath string    `json:"host_mount_path,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

func toWorkspaceJSON(ws gen.Workspace) workspaceJSON {
	out := workspaceJSON{
		ID:        store.UUIDString(ws.ID),
		Name:      ws.Name,
		Image:     ws.Image,
		State:     ws.State,
		CreatedAt: ws.CreatedAt.Time,
	}
	if ws.AgentID != nil {
		out.HostID = *ws.AgentID
	}
	if ws.ContainerID != nil {
		out.ContainerID = *ws.ContainerID
	}
	if ws.HostMountPath != nil {
		out.HostMountPath = *ws.HostMountPath
	}
	return out
}

func (h *workspaceHandlers) create(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	var req struct {
		Name, Image   string
		HostMountPath string `json:"host_mount_path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad request")
		return
	}
	ws, err := h.svc.Create(r.Context(), u.ID, req.Name, req.Image, req.HostMountPath)
	switch {
	case errors.Is(err, workspaces.ErrNoName):
		writeJSONError(w, http.StatusBadRequest, "name is required")
	case errors.Is(err, workspaces.ErrNoAgent):
		writeJSONError(w, http.StatusServiceUnavailable, "no worker available")
	case errors.Is(err, workspaces.ErrAgentCall):
		h.logger.ErrorContext(r.Context(), "workspace create failed on agent", "workspace_id", store.UUIDString(ws.ID))
		writeJSON(w, http.StatusBadGateway, toWorkspaceJSON(ws)) // row persists in error
	case err != nil:
		writeJSONError(w, http.StatusInternalServerError, "internal error")
	default:
		writeJSON(w, http.StatusCreated, toWorkspaceJSON(ws))
	}
}

func (h *workspaceHandlers) list(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	list, err := h.svc.List(r.Context(), u.ID)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "workspace list failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]workspaceJSON, 0, len(list))
	for _, ws := range list {
		out = append(out, toWorkspaceJSON(ws))
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspaces": out})
}

func (h *workspaceHandlers) get(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, ok := parseWorkspaceID(w, r)
	if !ok {
		return
	}
	ws, err := h.svc.Get(r.Context(), u.ID, id)
	switch {
	case errors.Is(err, workspaces.ErrNotFound):
		writeJSONError(w, http.StatusNotFound, "not found")
	case err != nil:
		h.logger.ErrorContext(r.Context(), "workspace get failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
	default:
		writeJSON(w, http.StatusOK, toWorkspaceJSON(ws))
	}
}

func (h *workspaceHandlers) start(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, h.svc.Start)
}

func (h *workspaceHandlers) stop(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, h.svc.Stop)
}

// transition runs a Start/Stop against the service and maps the result. A
// success carries a workspace whose State is running or stopped; an agent
// failure is ErrAgentCall -> 502 with the (now error-state) row as the body.
func (h *workspaceHandlers) transition(w http.ResponseWriter, r *http.Request,
	fn func(context.Context, pgtype.UUID, pgtype.UUID) (gen.Workspace, error)) {

	u, _ := auth.UserFromContext(r.Context())
	id, ok := parseWorkspaceID(w, r)
	if !ok {
		return
	}
	ws, err := fn(r.Context(), u.ID, id)
	switch {
	case errors.Is(err, workspaces.ErrNotFound):
		writeJSONError(w, http.StatusNotFound, "not found")
	case errors.Is(err, workspaces.ErrAgentCall):
		h.logger.ErrorContext(r.Context(), "workspace transition failed on agent", "workspace_id", store.UUIDString(id))
		writeJSON(w, http.StatusBadGateway, toWorkspaceJSON(ws))
	case err != nil:
		h.logger.ErrorContext(r.Context(), "workspace transition failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
	default:
		writeJSON(w, http.StatusOK, toWorkspaceJSON(ws))
	}
}

func (h *workspaceHandlers) destroy(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, ok := parseWorkspaceID(w, r)
	if !ok {
		return
	}
	err := h.svc.Destroy(r.Context(), u.ID, id)
	switch {
	case errors.Is(err, workspaces.ErrNotFound):
		writeJSONError(w, http.StatusNotFound, "not found")
	case errors.Is(err, workspaces.ErrAgentCall):
		h.logger.ErrorContext(r.Context(), "workspace destroy failed on agent", "workspace_id", store.UUIDString(id))
		writeJSONError(w, http.StatusBadGateway, "agent call failed")
	case err != nil:
		h.logger.ErrorContext(r.Context(), "workspace destroy failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// parseWorkspaceID reads the {id} path param as a UUID, writing 400 on a
// malformed value.
func parseWorkspaceID(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	id, err := store.ParseUUID(chi.URLParam(r, "id"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid workspace id")
		return pgtype.UUID{}, false
	}
	return id, true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
