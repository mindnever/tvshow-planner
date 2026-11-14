-- Who is present is no longer stored on the server; each browser keeps its
-- own selection in a cookie.
-- +goose Up
ALTER TABLE people DROP COLUMN present;

-- +goose Down
ALTER TABLE people ADD COLUMN present INTEGER NOT NULL DEFAULT 0;
