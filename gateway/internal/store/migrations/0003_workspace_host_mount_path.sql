-- +goose Up
ALTER TABLE workspaces ADD COLUMN host_mount_path text;

-- +goose Down
ALTER TABLE workspaces DROP COLUMN host_mount_path;
