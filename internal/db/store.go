// Package db is the SQLite persistence layer. All SQL lives in
// queries/queries.sql; sqlcgen is generated from it (`make generate`). Store
// maps the generated rows onto the small domain types the rest of the app uses.
package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	"tvshow-planner/internal/db/sqlcgen"
)

//go:embed migrations/*.sql
var migrations embed.FS

// ErrNotFound is returned by single-row lookups that match nothing.
var ErrNotFound = errors.New("not found")

type Store struct {
	db *sql.DB
	q  *sqlcgen.Queries
}

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
	return &Store{db: sqlDB, q: sqlcgen.New(sqlDB)}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func now() int64 { return time.Now().Unix() }

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// inTx runs fn in a transaction, committing if it returns nil.
func (s *Store) inTx(ctx context.Context, fn func(q *sqlcgen.Queries) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(s.q.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- users & sessions ----

type User struct {
	ID   int64
	Name string
	// Theme is the colour scheme the user picked: "light", "dark", or ""
	// to follow their device.
	Theme string
}

// SetUserTheme stores the user's colour scheme; anything but "light" or
// "dark" means following the device.
func (s *Store) SetUserTheme(ctx context.Context, userID int64, theme string) error {
	if theme != "light" && theme != "dark" {
		theme = ""
	}
	return s.q.SetUserTheme(ctx, sqlcgen.SetUserThemeParams{Theme: theme, ID: userID})
}

// Credentials are what is needed to call Jellyfin as a user.
type Credentials struct {
	Name           string
	JellyfinUserID string
	TokenEnc       []byte
	DeviceID       string
}

// UpsertUser records a successful Jellyfin login, keeping the newest token.
func (s *Store) UpsertUser(ctx context.Context, c Credentials) (User, error) {
	u, err := s.q.UpsertUser(ctx, sqlcgen.UpsertUserParams{
		JellyfinUserID: c.JellyfinUserID, Name: c.Name, AccessTokenEnc: c.TokenEnc, DeviceID: c.DeviceID,
	})
	return User(u), err
}

// JellyfinUserID returns the Jellyfin account a user logs in with.
func (s *Store) JellyfinUserID(ctx context.Context, userID int64) (string, error) {
	id, err := s.q.GetUserJellyfinID(ctx, userID)
	return id, notFound(err)
}

func (s *Store) CreateSession(ctx context.Context, id string, userID int64, expires time.Time) error {
	_ = s.q.DeleteExpiredSessions(ctx, now())
	return s.q.CreateSession(ctx, sqlcgen.CreateSessionParams{ID: id, UserID: userID, ExpiresAt: expires.Unix()})
}

func (s *Store) SessionUser(ctx context.Context, id string) (User, error) {
	u, err := s.q.GetSessionUser(ctx, sqlcgen.GetSessionUserParams{ID: id, Now: now()})
	return User(u), notFound(err)
}

func (s *Store) DeleteSession(ctx context.Context, id string) error {
	return s.q.DeleteSession(ctx, id)
}

// ---- groups & managers ----

type Group struct {
	ID   int64
	Name string
	Role string // role of the user the group was loaded for
	// Outcome of the last refresh from Jellyfin (not set by ListGroups).
	LastSyncAt    int64
	LastSyncError string
}

func (g Group) IsOwner() bool { return g.Role == "owner" }

// CreateGroup creates a group owned by userID.
func (s *Store) CreateGroup(ctx context.Context, name string, userID int64) (id int64, err error) {
	err = s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if id, err = q.CreateGroup(ctx, name); err != nil {
			return err
		}
		return q.AddOwner(ctx, sqlcgen.AddOwnerParams{GroupID: id, UserID: userID})
	})
	return id, err
}

func (s *Store) ListGroups(ctx context.Context, userID int64) ([]Group, error) {
	rows, err := s.q.ListGroupsForUser(ctx, userID)
	out := make([]Group, len(rows))
	for i, r := range rows {
		out[i] = Group{ID: r.ID, Name: r.Name, Role: r.Role}
	}
	return out, err
}

// GroupForUser returns the group only if userID manages it.
func (s *Store) GroupForUser(ctx context.Context, groupID, userID int64) (Group, error) {
	r, err := s.q.GetGroupForUser(ctx, sqlcgen.GetGroupForUserParams{GroupID: groupID, UserID: userID})
	return Group(r), notFound(err)
}

func (s *Store) RenameGroup(ctx context.Context, groupID int64, name string) error {
	return s.q.RenameGroup(ctx, sqlcgen.RenameGroupParams{Name: name, ID: groupID})
}

func (s *Store) DeleteGroup(ctx context.Context, groupID int64) error {
	return s.q.DeleteGroup(ctx, groupID)
}

type Manager struct {
	UserID int64
	Name   string
	Role   string
}

func (s *Store) ListManagers(ctx context.Context, groupID int64) ([]Manager, error) {
	rows, err := s.q.ListManagers(ctx, groupID)
	out := make([]Manager, len(rows))
	for i, r := range rows {
		out[i] = Manager(r)
	}
	return out, err
}

// AddManager is a no-op if the user already manages the group.
func (s *Store) AddManager(ctx context.Context, groupID, userID int64) error {
	return s.q.AddManager(ctx, sqlcgen.AddManagerParams{GroupID: groupID, UserID: userID})
}

// RemoveManager never removes the owner.
func (s *Store) RemoveManager(ctx context.Context, groupID, userID int64) error {
	return s.q.RemoveManager(ctx, sqlcgen.RemoveManagerParams{GroupID: groupID, UserID: userID})
}

// ---- invites ----

func (s *Store) CreateInvite(ctx context.Context, token string, groupID int64, expires time.Time) error {
	_ = s.q.DeleteExpiredInvites(ctx, now())
	return s.q.CreateInvite(ctx, sqlcgen.CreateInviteParams{Token: token, GroupID: groupID, ExpiresAt: expires.Unix()})
}

// Invite returns the group a valid, unexpired invite token points at.
func (s *Store) Invite(ctx context.Context, token string) (groupID int64, groupName string, err error) {
	r, err := s.q.GetInvite(ctx, sqlcgen.GetInviteParams{Token: token, Now: now()})
	return r.ID, r.Name, notFound(err)
}

// ---- people ----

type Person struct {
	ID   int64
	Name string
}

func (s *Store) ListPeople(ctx context.Context, groupID int64) ([]Person, error) {
	rows, err := s.q.ListPeople(ctx, groupID)
	out := make([]Person, len(rows))
	for i, r := range rows {
		out[i] = Person{ID: r.ID, Name: r.Name}
	}
	return out, err
}

func (s *Store) CreatePerson(ctx context.Context, groupID int64, name string) error {
	return s.q.CreatePerson(ctx, sqlcgen.CreatePersonParams{GroupID: groupID, Name: name})
}

func (s *Store) RenamePerson(ctx context.Context, groupID, personID int64, name string) error {
	return s.q.RenamePerson(ctx, sqlcgen.RenamePersonParams{Name: name, ID: personID, GroupID: groupID})
}

func (s *Store) DeletePerson(ctx context.Context, groupID, personID int64) error {
	return s.q.DeletePerson(ctx, sqlcgen.DeletePersonParams{ID: personID, GroupID: groupID})
}

// ---- shows ----

type Show struct {
	ID           int64
	JellyfinID   string // empty until the show is in Jellyfin
	Title        string
	Year         int
	HasImage     bool
	Unplayed     int
	NextUp       string
	LastPlayedAt int64
	SyncedAt     int64
	Archived     bool
	// From Jellyseerr, for shows added from its catalogue.
	TMDBID       int64  // 0 for a typed title that hasn't been matched
	PosterPath   string // TMDB poster, e.g. "/abc.jpg"
	Availability int    // Jellyseerr's status, see package seerr
	MatchTried   bool   // the typed title was looked up in Jellyseerr already
	// WatchOn is where the group watches a show that isn't in Jellyfin,
	// e.g. "Netflix". Empty means Jellyfin.
	WatchOn string
}

// PosterFile is PosterPath without its leading slash, for use in a URL.
func (s Show) PosterFile() string { return strings.TrimPrefix(s.PosterPath, "/") }

func (s *Store) ListShows(ctx context.Context, groupID int64) ([]Show, error) {
	rows, err := s.q.ListShows(ctx, groupID)
	out := make([]Show, len(rows))
	for i, r := range rows {
		out[i] = Show{
			ID: r.ID, JellyfinID: r.JellyfinSeriesID.String, Title: r.Title, Year: int(r.Year),
			HasImage: r.HasImage, Unplayed: int(r.UnplayedCount), NextUp: r.NextUp,
			LastPlayedAt: r.LastPlayedAt, SyncedAt: r.SyncedAt, Archived: r.Archived,
			TMDBID: r.TmdbID.Int64, PosterPath: r.PosterPath, Availability: int(r.Availability),
			MatchTried: r.MatchTried, WatchOn: r.WatchOn,
		}
	}
	return out, err
}

// CreateShow adds a show from its JellyfinID, TMDBID or just a Title; the
// catalogue fields (Year, PosterPath, Availability) are stored too. Adding a
// Jellyfin series or a catalogue show the group already has is a no-op.
func (s *Store) CreateShow(ctx context.Context, groupID int64, sh Show) error {
	return s.q.CreateShow(ctx, sqlcgen.CreateShowParams{
		GroupID:          groupID,
		JellyfinSeriesID: sql.NullString{String: sh.JellyfinID, Valid: sh.JellyfinID != ""},
		Title:            sh.Title,
		TmdbID:           sql.NullInt64{Int64: sh.TMDBID, Valid: sh.TMDBID != 0},
		Year:             int64(sh.Year),
		PosterPath:       sh.PosterPath,
		Availability:     int64(sh.Availability),
	})
}

// SetShowCatalogue ties an existing show to its Jellyseerr entry, taking
// TMDBID, Title, Year, PosterPath and Availability from sh.
func (s *Store) SetShowCatalogue(ctx context.Context, groupID int64, sh Show) error {
	return s.q.SetShowCatalogue(ctx, sqlcgen.SetShowCatalogueParams{
		TmdbID: sql.NullInt64{Int64: sh.TMDBID, Valid: true}, Title: sh.Title, Year: int64(sh.Year),
		PosterPath: sh.PosterPath, Availability: int64(sh.Availability), ID: sh.ID, GroupID: groupID,
	})
}

// SetShowWatchOn records where the group watches the show; "" means Jellyfin.
func (s *Store) SetShowWatchOn(ctx context.Context, groupID, showID int64, where string) error {
	return s.q.SetShowWatchOn(ctx, sqlcgen.SetShowWatchOnParams{WatchOn: where, ID: showID, GroupID: groupID})
}

func (s *Store) SetShowAvailability(ctx context.Context, showID int64, availability int) error {
	return s.q.SetShowAvailability(ctx, sqlcgen.SetShowAvailabilityParams{Availability: int64(availability), ID: showID})
}

func (s *Store) SetShowMatchTried(ctx context.Context, showID int64) error {
	return s.q.SetShowMatchTried(ctx, showID)
}

// LinkShowToJellyfin records that the show now exists in Jellyfin.
func (s *Store) LinkShowToJellyfin(ctx context.Context, showID int64, jellyfinID string) error {
	return s.q.LinkShowToJellyfin(ctx, sqlcgen.LinkShowToJellyfinParams{
		JellyfinSeriesID: sql.NullString{String: jellyfinID, Valid: true}, ID: showID,
	})
}

func (s *Store) SetShowArchived(ctx context.Context, groupID, showID int64, archived bool) error {
	return s.q.SetShowArchived(ctx, sqlcgen.SetShowArchivedParams{Archived: archived, ID: showID, GroupID: groupID})
}

func (s *Store) DeleteShow(ctx context.Context, groupID, showID int64) error {
	return s.q.DeleteShow(ctx, sqlcgen.DeleteShowParams{ID: showID, GroupID: groupID})
}

// UpdateShowSync stores the progress and metadata fetched from Jellyfin.
func (s *Store) UpdateShowSync(ctx context.Context, sh Show) error {
	return s.q.UpdateShowSync(ctx, sqlcgen.UpdateShowSyncParams{
		Title: sh.Title, Year: int64(sh.Year), HasImage: sh.HasImage, UnplayedCount: int64(sh.Unplayed),
		NextUp: sh.NextUp, LastPlayedAt: sh.LastPlayedAt, SyncedAt: now(), ID: sh.ID,
	})
}

// Watchers returns show ID -> person IDs for every show in the group.
func (s *Store) Watchers(ctx context.Context, groupID int64) (map[int64][]int64, error) {
	rows, err := s.q.ListWatchers(ctx, groupID)
	out := map[int64][]int64{}
	for _, r := range rows {
		out[r.ShowID] = append(out[r.ShowID], r.PersonID)
	}
	return out, err
}

// SetWatchers replaces the subgroup watching a show. Show and people that do
// not belong to groupID are ignored.
func (s *Store) SetWatchers(ctx context.Context, groupID, showID int64, personIDs []int64) error {
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := q.DeleteShowWatchers(ctx, sqlcgen.DeleteShowWatchersParams{ShowID: showID, GroupID: groupID}); err != nil {
			return err
		}
		for _, pid := range personIDs {
			if err := q.AddShowWatcher(ctx, sqlcgen.AddShowWatcherParams{ShowID: showID, PersonID: pid, GroupID: groupID}); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---- jellyfin ----

// OwnerCredentials returns the Jellyfin credentials of the group's owner,
// which is the account the group's shows and progress are read from.
func (s *Store) OwnerCredentials(ctx context.Context, groupID int64) (Credentials, error) {
	r, err := s.q.GetGroupOwnerCredentials(ctx, groupID)
	return Credentials{Name: r.Name, JellyfinUserID: r.JellyfinUserID, TokenEnc: r.AccessTokenEnc, DeviceID: r.DeviceID}, notFound(err)
}

func (s *Store) SetSyncStatus(ctx context.Context, groupID int64, errMsg string) error {
	return s.q.SetGroupSyncStatus(ctx, sqlcgen.SetGroupSyncStatusParams{LastSyncAt: now(), LastSyncError: errMsg, ID: groupID})
}
