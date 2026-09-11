// Package idle stops a workspace whose owner has gone quiet for longer than
// the configured idle timeout. One Sweep call is one pass; the gateway runs
// it on a ticker, mirroring package reconcile's shape.
package idle

import (
	"context"
	"log/slog"
	"time"

	"github.com/dayski-47/hearth/gateway/internal/activity"
	"github.com/dayski-47/hearth/gateway/internal/store"
	"github.com/dayski-47/hearth/gateway/internal/store/gen"
	"github.com/jackc/pgx/v5/pgtype"
)

// Store is the subset of *gen.Queries the sweep calls. *gen.Queries satisfies it.
type Store interface {
	ListRunningWorkspaces(context.Context) ([]gen.Workspace, error)
}

// Stopper stops a workspace on the system's behalf, not a particular owner's.
// *workspaces.Service satisfies it via StopIdle.
type Stopper interface {
	StopIdle(context.Context, pgtype.UUID) (gen.Workspace, error)
}

// Deps are the collaborators for one Sweep pass.
type Deps struct {
	Store   Store
	Tracker *activity.Tracker
	Stop    Stopper
	Timeout time.Duration
	Logger  *slog.Logger
}

// Sweep runs one idle-stop pass at wall-clock time now. A running workspace
// the tracker has never seen is seeded to now rather than stopped (so a
// gateway restart, which empties the tracker, cannot treat "no record yet"
// as "idle since forever"); a workspace idle longer than Timeout is stopped.
// A Stop failure is logged and the pass continues; only a failure to list
// running workspaces is returned.
func Sweep(ctx context.Context, d Deps, now time.Time) error {
	rows, err := d.Store.ListRunningWorkspaces(ctx)
	if err != nil {
		return err
	}
	for _, ws := range rows {
		id := store.UUIDString(ws.ID)
		idleFor, seen := d.Tracker.IdleFor(id, now)
		if !seen {
			d.Tracker.Touch(id)
			continue
		}
		if idleFor <= d.Timeout {
			continue
		}
		if _, err := d.Stop.StopIdle(ctx, ws.ID); err != nil {
			d.Logger.WarnContext(ctx, "idle stop failed", "workspace_id", id, "error", err)
			continue
		}
		d.Logger.InfoContext(ctx, "workspace idle-stopped", "workspace_id", id, "idle_for", idleFor)
	}
	return nil
}
