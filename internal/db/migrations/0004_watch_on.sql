-- +goose Up
-- Where the group watches a show that isn't in Jellyfin, e.g. "Netflix".
-- Empty means Jellyfin.
ALTER TABLE shows ADD COLUMN watch_on TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE shows DROP COLUMN watch_on;
