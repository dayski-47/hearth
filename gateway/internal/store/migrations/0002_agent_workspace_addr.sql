-- +goose Up
ALTER TABLE agents ADD COLUMN workspace_addr text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE agents DROP COLUMN workspace_addr;
