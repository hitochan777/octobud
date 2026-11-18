-- +goose Up
ALTER TABLE users ADD COLUMN github_cli_auth BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE users DROP COLUMN github_cli_auth;
