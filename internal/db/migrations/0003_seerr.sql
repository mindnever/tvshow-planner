-- +goose Up
-- A show can be known to Jellyseerr (by its TMDB ID) before it exists in
-- Jellyfin. availability is Jellyseerr's status for it; match_tried records
-- that a typed title was already looked up in Jellyseerr once.
ALTER TABLE shows ADD COLUMN tmdb_id INTEGER;
ALTER TABLE shows ADD COLUMN poster_path TEXT NOT NULL DEFAULT '';
ALTER TABLE shows ADD COLUMN availability INTEGER NOT NULL DEFAULT 0;
ALTER TABLE shows ADD COLUMN match_tried INTEGER NOT NULL DEFAULT 0;
CREATE UNIQUE INDEX shows_group_tmdb ON shows (group_id, tmdb_id);

-- +goose Down
DROP INDEX shows_group_tmdb;
ALTER TABLE shows DROP COLUMN match_tried;
ALTER TABLE shows DROP COLUMN availability;
ALTER TABLE shows DROP COLUMN poster_path;
ALTER TABLE shows DROP COLUMN tmdb_id;
