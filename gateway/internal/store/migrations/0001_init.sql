-- +goose Up
CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    username      text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    id           text PRIMARY KEY,
    user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    user_agent   text NOT NULL DEFAULT '',
    ip           inet
);
CREATE INDEX sessions_user_id_idx ON sessions(user_id);

CREATE TABLE agents (
    id                text PRIMARY KEY,
    advertise_addr    text NOT NULL,
    status            text NOT NULL DEFAULT 'ready',
    capacity          jsonb NOT NULL DEFAULT '{}'::jsonb,
    last_heartbeat_at timestamptz NOT NULL DEFAULT now(),
    registered_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE workspaces (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id        uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name            text NOT NULL,
    image           text NOT NULL,
    state           text NOT NULL DEFAULT 'creating',
    agent_id        text REFERENCES agents(id) ON DELETE SET NULL,
    container_id    text,
    limits          jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    last_started_at timestamptz
);
CREATE INDEX workspaces_owner_id_idx ON workspaces(owner_id);
CREATE INDEX workspaces_state_idx ON workspaces(state);

CREATE TABLE workspace_events (
    id           bigserial PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    at           timestamptz NOT NULL DEFAULT now(),
    kind         text NOT NULL,
    detail       jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX workspace_events_workspace_id_idx ON workspace_events(workspace_id);

-- +goose Down
DROP TABLE workspace_events;
DROP TABLE workspaces;
DROP TABLE agents;
DROP TABLE sessions;
DROP TABLE users;
