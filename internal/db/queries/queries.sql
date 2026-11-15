-- ---- users & sessions ----

-- name: UpsertUser :one
INSERT INTO users (jellyfin_user_id, name, access_token_enc, device_id)
VALUES (?, ?, ?, ?)
ON CONFLICT (jellyfin_user_id) DO UPDATE SET
    name = excluded.name, access_token_enc = excluded.access_token_enc, device_id = excluded.device_id
RETURNING id, name;

-- name: GetUserJellyfinID :one
SELECT jellyfin_user_id FROM users WHERE id = ?;

-- name: CreateSession :exec
INSERT INTO sessions (id, user_id, expires_at) VALUES (?, ?, ?);

-- name: GetSessionUser :one
SELECT u.id, u.name FROM sessions s JOIN users u ON u.id = s.user_id
WHERE s.id = sqlc.arg(id) AND s.expires_at > sqlc.arg(now);

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = ?;

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE expires_at <= sqlc.arg(now);

-- ---- groups & managers ----

-- name: CreateGroup :one
INSERT INTO watch_groups (name) VALUES (?) RETURNING id;

-- name: AddOwner :exec
INSERT INTO group_managers (group_id, user_id, role) VALUES (?, ?, 'owner');

-- name: ListGroupsForUser :many
SELECT g.id, g.name, m.role FROM watch_groups g
JOIN group_managers m ON m.group_id = g.id
WHERE m.user_id = ? ORDER BY g.name COLLATE NOCASE;

-- name: GetGroupForUser :one
SELECT g.id, g.name, m.role, g.last_sync_at, g.last_sync_error FROM watch_groups g
JOIN group_managers m ON m.group_id = g.id
WHERE g.id = sqlc.arg(group_id) AND m.user_id = sqlc.arg(user_id);

-- name: RenameGroup :exec
UPDATE watch_groups SET name = ? WHERE id = ?;

-- name: DeleteGroup :exec
DELETE FROM watch_groups WHERE id = ?;

-- name: ListManagers :many
SELECT u.id AS user_id, u.name, m.role FROM group_managers m
JOIN users u ON u.id = m.user_id
WHERE m.group_id = ? ORDER BY m.role DESC, u.name COLLATE NOCASE;

-- name: AddManager :exec
INSERT INTO group_managers (group_id, user_id, role) VALUES (?, ?, 'manager')
ON CONFLICT (group_id, user_id) DO NOTHING;

-- name: RemoveManager :exec
DELETE FROM group_managers WHERE group_id = ? AND user_id = ? AND role <> 'owner';

-- ---- invites ----

-- name: CreateInvite :exec
INSERT INTO group_invites (token, group_id, expires_at) VALUES (?, ?, ?);

-- name: GetInvite :one
SELECT g.id, g.name FROM group_invites i
JOIN watch_groups g ON g.id = i.group_id
WHERE i.token = sqlc.arg(token) AND i.expires_at > sqlc.arg(now);

-- name: DeleteExpiredInvites :exec
DELETE FROM group_invites WHERE expires_at <= sqlc.arg(now);

-- ---- people ----

-- name: ListPeople :many
SELECT * FROM people WHERE group_id = ? ORDER BY name COLLATE NOCASE, id;

-- name: CreatePerson :exec
INSERT INTO people (group_id, name) VALUES (?, ?);

-- name: RenamePerson :exec
UPDATE people SET name = ? WHERE id = ? AND group_id = ?;

-- name: DeletePerson :exec
DELETE FROM people WHERE id = ? AND group_id = ?;

-- ---- shows ----

-- name: ListShows :many
SELECT * FROM shows WHERE group_id = ? ORDER BY archived, title COLLATE NOCASE, id;

-- name: CreateShow :exec
INSERT INTO shows (group_id, jellyfin_series_id, title, tmdb_id, year, poster_path, availability)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT DO NOTHING;

-- SetShowCatalogue ties a show to its Jellyseerr (TMDB) entry.
-- name: SetShowCatalogue :exec
UPDATE shows SET tmdb_id = ?, title = ?, year = ?, poster_path = ?, availability = ?, match_tried = 1
WHERE id = ? AND group_id = ?;

-- name: SetShowAvailability :exec
UPDATE shows SET availability = ? WHERE id = ?;

-- name: SetShowMatchTried :exec
UPDATE shows SET match_tried = 1 WHERE id = ?;

-- name: LinkShowToJellyfin :exec
UPDATE shows SET jellyfin_series_id = ? WHERE id = ?;

-- name: SetShowArchived :exec
UPDATE shows SET archived = ? WHERE id = ? AND group_id = ?;

-- name: DeleteShow :exec
DELETE FROM shows WHERE id = ? AND group_id = ?;

-- name: UpdateShowSync :exec
UPDATE shows SET title = ?, year = ?, has_image = ?, unplayed_count = ?, next_up = ?,
    last_played_at = ?, synced_at = ?
WHERE id = ?;

-- name: ListWatchers :many
SELECT w.show_id, w.person_id FROM show_watchers w
JOIN shows s ON s.id = w.show_id
WHERE s.group_id = ? ORDER BY w.person_id;

-- name: DeleteShowWatchers :exec
DELETE FROM show_watchers WHERE show_id IN
    (SELECT s.id FROM shows s WHERE s.id = sqlc.arg(show_id) AND s.group_id = sqlc.arg(group_id));

-- AddShowWatcher only inserts when both the show and the person belong to the group.
-- name: AddShowWatcher :exec
INSERT INTO show_watchers (show_id, person_id)
SELECT s.id, p.id FROM shows s, people p
WHERE s.id = sqlc.arg(show_id) AND p.id = sqlc.arg(person_id)
  AND s.group_id = sqlc.arg(group_id) AND p.group_id = sqlc.arg(group_id)
ON CONFLICT DO NOTHING;

-- ---- jellyfin ----

-- The group reads Jellyfin as its owner.
-- name: GetGroupOwnerCredentials :one
SELECT u.name, u.jellyfin_user_id, u.access_token_enc, u.device_id FROM group_managers m
JOIN users u ON u.id = m.user_id
WHERE m.group_id = ? AND m.role = 'owner';

-- name: SetGroupSyncStatus :exec
UPDATE watch_groups SET last_sync_at = ?, last_sync_error = ? WHERE id = ?;
