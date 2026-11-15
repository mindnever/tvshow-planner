-- +goose Up
-- The colour scheme a user picked: 'light', 'dark', or '' to follow the device.
ALTER TABLE users ADD COLUMN theme TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE users DROP COLUMN theme;
