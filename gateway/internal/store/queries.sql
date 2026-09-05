-- name: UpsertUser :one
INSERT INTO users (username, password_hash)
VALUES ($1, $2)
ON CONFLICT (username) DO UPDATE SET password_hash = EXCLUDED.password_hash
RETURNING *;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE username = $1;

-- name: UpsertAgent :one
INSERT INTO agents (id, advertise_addr, capacity, status, last_heartbeat_at)
VALUES ($1, $2, $3, 'ready', now())
ON CONFLICT (id) DO UPDATE
   SET advertise_addr = EXCLUDED.advertise_addr,
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
