-- name: UpsertUser :one
INSERT INTO users (username, password_hash)
VALUES ($1, $2)
ON CONFLICT (username) DO UPDATE SET password_hash = EXCLUDED.password_hash
RETURNING *;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE username = $1;

-- name: UpsertAgent :one
INSERT INTO agents (id, advertise_addr, workspace_addr, capacity, status, last_heartbeat_at)
VALUES ($1, $2, $3, $4, 'ready', now())
ON CONFLICT (id) DO UPDATE
   SET advertise_addr = EXCLUDED.advertise_addr,
       workspace_addr = EXCLUDED.workspace_addr,
       capacity = EXCLUDED.capacity,
       status = 'ready',
       last_heartbeat_at = now()
RETURNING *;

-- name: ListAgents :many
SELECT * FROM agents ORDER BY registered_at;

-- name: TouchAgentHeartbeat :exec
UPDATE agents SET last_heartbeat_at = now(), status = 'ready' WHERE id = $1;

-- name: SetAgentStatus :exec
UPDATE agents SET status = $2 WHERE id = $1;

-- name: CreateSession :one
INSERT INTO sessions (id, user_id, expires_at, user_agent, ip)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetSessionWithUser :one
SELECT sqlc.embed(sessions), sqlc.embed(users)
FROM sessions
JOIN users ON users.id = sessions.user_id
WHERE sessions.id = $1;

-- name: SlideSession :exec
UPDATE sessions SET last_seen_at = now(), expires_at = $2 WHERE id = $1;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = $1;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at < now();

-- name: CreateWorkspace :one
INSERT INTO workspaces (owner_id, name, image, agent_id, state)
VALUES ($1, $2, $3, $4, 'creating')
RETURNING *;

-- name: GetWorkspaceForOwner :one
SELECT * FROM workspaces WHERE id = $1 AND owner_id = $2;

-- name: ListWorkspacesForOwner :many
SELECT * FROM workspaces WHERE owner_id = $1 ORDER BY created_at DESC;

-- name: SetWorkspacePlacement :exec
UPDATE workspaces
   SET container_id = $2, state = $3, updated_at = now(),
       last_started_at = CASE WHEN $3 = 'running' THEN now() ELSE last_started_at END
 WHERE id = $1;

-- name: SetWorkspaceState :exec
UPDATE workspaces
   SET state = $2, updated_at = now(),
       last_started_at = CASE WHEN $2 = 'running' THEN now() ELSE last_started_at END
 WHERE id = $1;

-- name: DeleteWorkspace :exec
DELETE FROM workspaces WHERE id = $1;

-- name: ListReconcilableWorkspaces :many
SELECT * FROM workspaces WHERE state IN ('creating', 'running', 'stopped', 'unknown');

-- name: MarkAgentWorkspacesUnknown :many
UPDATE workspaces SET state = 'unknown', updated_at = now()
 WHERE agent_id = $1 AND state IN ('creating', 'running', 'stopped')
 RETURNING id;

-- name: AppendWorkspaceEvent :exec
INSERT INTO workspace_events (workspace_id, kind, detail) VALUES ($1, $2, $3);
