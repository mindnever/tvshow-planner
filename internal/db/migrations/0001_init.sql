-- +goose Up
-- A user is a Jellyfin account that has logged in. The access token Jellyfin
-- issued is kept (encrypted) so groups the user owns can read their library.
CREATE TABLE users (
    id               INTEGER PRIMARY KEY,
    jellyfin_user_id TEXT    NOT NULL UNIQUE,
    name             TEXT    NOT NULL,
    access_token_enc BLOB    NOT NULL,
    device_id        TEXT    NOT NULL,
    created_at       INTEGER NOT NULL DEFAULT (unixepoch())
);

-- id is the SHA-256 of the cookie token, so a leaked DB cannot be replayed.
CREATE TABLE sessions (
    id         TEXT    PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL
);

CREATE TABLE watch_groups (
    id         INTEGER PRIMARY KEY,
    name            TEXT    NOT NULL,
    created_at      INTEGER NOT NULL DEFAULT (unixepoch()),
    -- outcome of the last refresh from the owner's Jellyfin account
    last_sync_at    INTEGER NOT NULL DEFAULT 0,
    last_sync_error TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE group_managers (
    group_id INTEGER NOT NULL REFERENCES watch_groups (id) ON DELETE CASCADE,
    user_id  INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role     TEXT    NOT NULL CHECK (role IN ('owner', 'manager')),
    PRIMARY KEY (group_id, user_id)
);

CREATE TABLE group_invites (
    token      TEXT    PRIMARY KEY,
    group_id   INTEGER NOT NULL REFERENCES watch_groups (id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL
);

CREATE TABLE people (
    id       INTEGER PRIMARY KEY,
    group_id INTEGER NOT NULL REFERENCES watch_groups (id) ON DELETE CASCADE,
    name     TEXT    NOT NULL,
    present  INTEGER NOT NULL DEFAULT 0
);

-- jellyfin_series_id is NULL for manually entered shows (no progress known).
CREATE TABLE shows (
    id                 INTEGER PRIMARY KEY,
    group_id           INTEGER NOT NULL REFERENCES watch_groups (id) ON DELETE CASCADE,
    jellyfin_series_id TEXT,
    title              TEXT    NOT NULL,
    year               INTEGER NOT NULL DEFAULT 0,
    has_image          INTEGER NOT NULL DEFAULT 0,
    unplayed_count     INTEGER NOT NULL DEFAULT 0,
    next_up            TEXT    NOT NULL DEFAULT '',
    last_played_at     INTEGER NOT NULL DEFAULT 0,
    synced_at          INTEGER NOT NULL DEFAULT 0,
    archived           INTEGER NOT NULL DEFAULT 0,
    UNIQUE (group_id, jellyfin_series_id)
);

CREATE TABLE show_watchers (
    show_id   INTEGER NOT NULL REFERENCES shows (id) ON DELETE CASCADE,
    person_id INTEGER NOT NULL REFERENCES people (id) ON DELETE CASCADE,
    PRIMARY KEY (show_id, person_id)
);

-- +goose Down
DROP TABLE show_watchers;
DROP TABLE shows;
DROP TABLE people;
DROP TABLE group_invites;
DROP TABLE group_managers;
DROP TABLE watch_groups;
DROP TABLE sessions;
DROP TABLE users;
