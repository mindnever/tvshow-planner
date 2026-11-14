// Package db is the SQLite persistence layer.
package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// ErrNotFound is returned by single-row lookups that match nothing.
var ErrNotFound = errors.New("not found")

type Store struct{ db *sql.DB }

// Open opens (creating if needed) the database file and applies migrations.
func Open(path string) (*Store, error) {
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	sqlDB, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	// SQLite allows one writer; a single connection avoids SQLITE_BUSY entirely.
	sqlDB.SetMaxOpenConns(1)

	goose.SetBaseFS(migrations)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		return nil, err
	}
	if err := goose.Up(sqlDB, "migrations"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: sqlDB}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func now() int64 { return time.Now().Unix() }

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func (s *Store) exec(ctx context.Context, q string, args ...any) error {
	_, err := s.db.ExecContext(ctx, q, args...)
	return err
}

func queryAll[T any](ctx context.Context, s *Store, scan func(*sql.Rows, *T) error, q string, args ...any) ([]T, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		var v T
		if err := scan(rows, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---- users & sessions ----

type User struct {
	ID    int64
	Name  string
	Email string
}

func (s *Store) UpsertUser(ctx context.Context, issuer, sub, name, email string) (User, error) {
	var u User
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO users (oidc_issuer, oidc_sub, name, email) VALUES (?, ?, ?, ?)
		ON CONFLICT (oidc_issuer, oidc_sub) DO UPDATE SET name = excluded.name, email = excluded.email
		RETURNING id, name, email`, issuer, sub, name, email).Scan(&u.ID, &u.Name, &u.Email)
	return u, err
}

func (s *Store) CreateSession(ctx context.Context, id string, userID int64, expires time.Time) error {
	_ = s.exec(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now())
	return s.exec(ctx, `INSERT INTO sessions (id, user_id, expires_at) VALUES (?, ?, ?)`, id, userID, expires.Unix())
}

func (s *Store) SessionUser(ctx context.Context, id string) (User, error) {
	var u User
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.name, u.email FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.id = ? AND s.expires_at > ?`, id, now()).Scan(&u.ID, &u.Name, &u.Email)
	return u, notFound(err)
}

func (s *Store) DeleteSession(ctx context.Context, id string) error {
	return s.exec(ctx, `DELETE FROM sessions WHERE id = ?`, id)
}

// ---- groups & managers ----

type Group struct {
	ID   int64
	Name string
	Role string // role of the user the group was loaded for
}

func (g Group) IsOwner() bool { return g.Role == "owner" }

func scanGroup(r *sql.Rows, g *Group) error { return r.Scan(&g.ID, &g.Name, &g.Role) }

// CreateGroup creates a group owned by userID.
func (s *Store) CreateGroup(ctx context.Context, name string, userID int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO watch_groups (name) VALUES (?)`, name)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO group_managers (group_id, user_id, role) VALUES (?, ?, 'owner')`, id, userID); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s *Store) ListGroups(ctx context.Context, userID int64) ([]Group, error) {
	return queryAll(ctx, s, scanGroup, `
		SELECT g.id, g.name, m.role FROM watch_groups g
		JOIN group_managers m ON m.group_id = g.id
		WHERE m.user_id = ? ORDER BY g.name COLLATE NOCASE`, userID)
}

// GroupForUser returns the group only if userID manages it.
func (s *Store) GroupForUser(ctx context.Context, groupID, userID int64) (Group, error) {
	var g Group
	err := s.db.QueryRowContext(ctx, `
		SELECT g.id, g.name, m.role FROM watch_groups g
		JOIN group_managers m ON m.group_id = g.id
		WHERE g.id = ? AND m.user_id = ?`, groupID, userID).Scan(&g.ID, &g.Name, &g.Role)
	return g, notFound(err)
}

func (s *Store) RenameGroup(ctx context.Context, groupID int64, name string) error {
	return s.exec(ctx, `UPDATE watch_groups SET name = ? WHERE id = ?`, name, groupID)
}

func (s *Store) DeleteGroup(ctx context.Context, groupID int64) error {
	return s.exec(ctx, `DELETE FROM watch_groups WHERE id = ?`, groupID)
}

type Manager struct {
	UserID int64
	Name   string
	Email  string
	Role   string
}

func (s *Store) ListManagers(ctx context.Context, groupID int64) ([]Manager, error) {
	return queryAll(ctx, s, func(r *sql.Rows, m *Manager) error {
		return r.Scan(&m.UserID, &m.Name, &m.Email, &m.Role)
	}, `SELECT u.id, u.name, u.email, m.role FROM group_managers m
		JOIN users u ON u.id = m.user_id
		WHERE m.group_id = ? ORDER BY m.role DESC, u.name COLLATE NOCASE`, groupID)
}

// AddManager is a no-op if the user already manages the group.
func (s *Store) AddManager(ctx context.Context, groupID, userID int64) error {
	return s.exec(ctx, `INSERT INTO group_managers (group_id, user_id, role) VALUES (?, ?, 'manager')
		ON CONFLICT (group_id, user_id) DO NOTHING`, groupID, userID)
}

// RemoveManager never removes the owner.
func (s *Store) RemoveManager(ctx context.Context, groupID, userID int64) error {
	return s.exec(ctx, `DELETE FROM group_managers WHERE group_id = ? AND user_id = ? AND role <> 'owner'`, groupID, userID)
}

// ---- invites ----

func (s *Store) CreateInvite(ctx context.Context, token string, groupID int64, expires time.Time) error {
	_ = s.exec(ctx, `DELETE FROM group_invites WHERE expires_at <= ?`, now())
	return s.exec(ctx, `INSERT INTO group_invites (token, group_id, expires_at) VALUES (?, ?, ?)`, token, groupID, expires.Unix())
}

// Invite returns the group a valid, unexpired invite token points at.
func (s *Store) Invite(ctx context.Context, token string) (groupID int64, groupName string, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT g.id, g.name FROM group_invites i JOIN watch_groups g ON g.id = i.group_id
		WHERE i.token = ? AND i.expires_at > ?`, token, now()).Scan(&groupID, &groupName)
	return groupID, groupName, notFound(err)
}

// ---- people ----

type Person struct {
	ID      int64
	Name    string
	Present bool
}

func (s *Store) ListPeople(ctx context.Context, groupID int64) ([]Person, error) {
	return queryAll(ctx, s, func(r *sql.Rows, p *Person) error {
		return r.Scan(&p.ID, &p.Name, &p.Present)
	}, `SELECT id, name, present FROM people WHERE group_id = ? ORDER BY name COLLATE NOCASE, id`, groupID)
}

func (s *Store) CreatePerson(ctx context.Context, groupID int64, name string) error {
	return s.exec(ctx, `INSERT INTO people (group_id, name) VALUES (?, ?)`, groupID, name)
}

func (s *Store) RenamePerson(ctx context.Context, groupID, personID int64, name string) error {
	return s.exec(ctx, `UPDATE people SET name = ? WHERE id = ? AND group_id = ?`, name, personID, groupID)
}

func (s *Store) DeletePerson(ctx context.Context, groupID, personID int64) error {
	return s.exec(ctx, `DELETE FROM people WHERE id = ? AND group_id = ?`, personID, groupID)
}

func (s *Store) TogglePresent(ctx context.Context, groupID, personID int64) error {
	return s.exec(ctx, `UPDATE people SET present = 1 - present WHERE id = ? AND group_id = ?`, personID, groupID)
}

func (s *Store) ClearPresence(ctx context.Context, groupID int64) error {
	return s.exec(ctx, `UPDATE people SET present = 0 WHERE group_id = ?`, groupID)
}

// ---- shows ----

type Show struct {
	ID           int64
	JellyfinID   string // empty for manually entered shows
	Title        string
	Year         int
	HasImage     bool
	Unplayed     int
	NextUp       string
	LastPlayedAt int64
	SyncedAt     int64
	Archived     bool
}

const showCols = `id, COALESCE(jellyfin_series_id, ''), title, year, has_image, unplayed_count, next_up, last_played_at, synced_at, archived`

func scanShow(r *sql.Rows, s *Show) error {
	return r.Scan(&s.ID, &s.JellyfinID, &s.Title, &s.Year, &s.HasImage, &s.Unplayed, &s.NextUp, &s.LastPlayedAt, &s.SyncedAt, &s.Archived)
}

func (s *Store) ListShows(ctx context.Context, groupID int64) ([]Show, error) {
	return queryAll(ctx, s, scanShow,
		`SELECT `+showCols+` FROM shows WHERE group_id = ? ORDER BY archived, title COLLATE NOCASE, id`, groupID)
}

// CreateShow adds a show; jellyfinID may be empty for a manual entry. Adding
// the same Jellyfin series twice is a no-op.
func (s *Store) CreateShow(ctx context.Context, groupID int64, jellyfinID, title string) error {
	var jf any
	if jellyfinID != "" {
		jf = jellyfinID
	}
	return s.exec(ctx, `INSERT INTO shows (group_id, jellyfin_series_id, title) VALUES (?, ?, ?)
		ON CONFLICT (group_id, jellyfin_series_id) DO NOTHING`, groupID, jf, title)
}

func (s *Store) SetShowArchived(ctx context.Context, groupID, showID int64, archived bool) error {
	return s.exec(ctx, `UPDATE shows SET archived = ? WHERE id = ? AND group_id = ?`, archived, showID, groupID)
}

func (s *Store) DeleteShow(ctx context.Context, groupID, showID int64) error {
	return s.exec(ctx, `DELETE FROM shows WHERE id = ? AND group_id = ?`, showID, groupID)
}

// UpdateShowSync stores the progress and metadata fetched from Jellyfin.
func (s *Store) UpdateShowSync(ctx context.Context, sh Show) error {
	return s.exec(ctx, `UPDATE shows SET title = ?, year = ?, has_image = ?, unplayed_count = ?, next_up = ?,
		last_played_at = ?, synced_at = ? WHERE id = ?`,
		sh.Title, sh.Year, sh.HasImage, sh.Unplayed, sh.NextUp, sh.LastPlayedAt, now(), sh.ID)
}

// Watchers returns show ID -> person IDs for every show in the group.
func (s *Store) Watchers(ctx context.Context, groupID int64) (map[int64][]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT w.show_id, w.person_id FROM show_watchers w JOIN shows s ON s.id = w.show_id
		WHERE s.group_id = ? ORDER BY w.person_id`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]int64{}
	for rows.Next() {
		var sid, pid int64
		if err := rows.Scan(&sid, &pid); err != nil {
			return nil, err
		}
		out[sid] = append(out[sid], pid)
	}
	return out, rows.Err()
}

// SetWatchers replaces the subgroup watching a show. Show and people that do
// not belong to groupID are ignored.
func (s *Store) SetWatchers(ctx context.Context, groupID, showID int64, personIDs []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM show_watchers WHERE show_id IN
		(SELECT id FROM shows WHERE id = ? AND group_id = ?)`, showID, groupID); err != nil {
		return err
	}
	for _, pid := range personIDs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO show_watchers (show_id, person_id)
			SELECT s.id, p.id FROM shows s, people p
			WHERE s.id = ? AND s.group_id = ? AND p.id = ? AND p.group_id = ?
			ON CONFLICT DO NOTHING`, showID, groupID, pid, groupID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---- jellyfin link ----

type JellyfinLink struct {
	GroupID       int64
	ServerURL     string
	UserID        string
	Username      string
	TokenEnc      []byte
	DeviceID      string
	LastSyncAt    int64
	LastSyncError string
}

func (s *Store) JellyfinLink(ctx context.Context, groupID int64) (JellyfinLink, error) {
	l := JellyfinLink{GroupID: groupID}
	err := s.db.QueryRowContext(ctx, `
		SELECT server_url, jf_user_id, jf_username, access_token_enc, device_id, last_sync_at, last_sync_error
		FROM jellyfin_links WHERE group_id = ?`, groupID).
		Scan(&l.ServerURL, &l.UserID, &l.Username, &l.TokenEnc, &l.DeviceID, &l.LastSyncAt, &l.LastSyncError)
	return l, notFound(err)
}

// SaveJellyfinLink sets or replaces the group's single Jellyfin account.
func (s *Store) SaveJellyfinLink(ctx context.Context, l JellyfinLink, linkedBy int64) error {
	return s.exec(ctx, `
		INSERT INTO jellyfin_links (group_id, server_url, jf_user_id, jf_username, access_token_enc, device_id, linked_by_user_id)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (group_id) DO UPDATE SET
			server_url = excluded.server_url, jf_user_id = excluded.jf_user_id,
			jf_username = excluded.jf_username, access_token_enc = excluded.access_token_enc,
			device_id = excluded.device_id, linked_by_user_id = excluded.linked_by_user_id,
			last_sync_at = 0, last_sync_error = ''`,
		l.GroupID, l.ServerURL, l.UserID, l.Username, l.TokenEnc, l.DeviceID, linkedBy)
}

func (s *Store) DeleteJellyfinLink(ctx context.Context, groupID int64) error {
	return s.exec(ctx, `DELETE FROM jellyfin_links WHERE group_id = ?`, groupID)
}

func (s *Store) SetJellyfinSyncStatus(ctx context.Context, groupID int64, errMsg string) error {
	return s.exec(ctx, `UPDATE jellyfin_links SET last_sync_at = ?, last_sync_error = ? WHERE group_id = ?`, now(), errMsg, groupID)
}
