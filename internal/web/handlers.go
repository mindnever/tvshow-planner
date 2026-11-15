package web

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"tvshow-planner/internal/auth"
	"tvshow-planner/internal/db"
	"tvshow-planner/internal/jellyfin"
	"tvshow-planner/internal/planner"
	"tvshow-planner/internal/seerr"
)

const (
	inviteTTL  = 7 * 24 * time.Hour
	syncMaxAge = 10 * time.Minute
)

// ---- home, groups, invites ----

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	groups, err := s.store.ListGroups(r.Context(), auth.User(r.Context()).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "home", pageData{D: groups})
}

func (s *Server) createGroup(w http.ResponseWriter, r *http.Request) {
	name := cleanName(r.FormValue("name"))
	if name == "" {
		redirect(w, r, "/")
		return
	}
	id, err := s.store.CreateGroup(r.Context(), name, auth.User(r.Context()).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/g/"+strconv.FormatInt(id, 10)+"/people")
}

func (s *Server) showInvite(w http.ResponseWriter, r *http.Request) {
	_, name, err := s.store.Invite(r.Context(), r.PathValue("token"))
	if errors.Is(err, db.ErrNotFound) {
		s.render(w, r, http.StatusNotFound, "message", pageData{D: "This invite link is invalid or has expired."})
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "invite", pageData{D: map[string]string{"Name": name, "Token": r.PathValue("token")}})
}

func (s *Server) acceptInvite(w http.ResponseWriter, r *http.Request) {
	gid, _, err := s.store.Invite(r.Context(), r.PathValue("token"))
	if errors.Is(err, db.ErrNotFound) {
		s.render(w, r, http.StatusNotFound, "message", pageData{D: "This invite link is invalid or has expired."})
		return
	}
	if err == nil {
		err = s.store.AddManager(r.Context(), gid, auth.User(r.Context()).ID)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/g/"+strconv.FormatInt(gid, 10))
}

// ---- planner ----

type resultView struct {
	Show     db.Show
	Tier     planner.Tier
	Missing  []string
	Extra    []string
	Finished bool
}

// personView is a person with whether this browser has them ticked as present.
type personView struct {
	ID      int64
	Name    string
	Present bool
}

type plannerView struct {
	People     []personView
	AnyPresent bool
	Results    []resultView
	HasShows   bool
	SyncError  string
}

// plannerView ranks the group's shows for the people in here (person IDs
// ticked as present; IDs that aren't people of this group are ignored).
func (s *Server) plannerView(ctx context.Context, g db.Group, here map[int64]bool) (plannerView, error) {
	var v plannerView
	people, err := s.store.ListPeople(ctx, g.ID)
	if err != nil {
		return v, err
	}
	shows, err := s.store.ListShows(ctx, g.ID)
	if err != nil {
		return v, err
	}
	watchers, err := s.store.Watchers(ctx, g.ID)
	if err != nil {
		return v, err
	}
	v.SyncError = g.LastSyncError

	names := map[int64]string{}
	var present []int64
	for _, p := range people {
		names[p.ID] = p.Name
		v.People = append(v.People, personView{ID: p.ID, Name: p.Name, Present: here[p.ID]})
		if here[p.ID] {
			present = append(present, p.ID)
		}
	}
	v.AnyPresent = len(present) > 0

	byID := map[int64]db.Show{}
	var candidates []planner.Show
	for _, sh := range shows {
		if sh.Archived {
			continue
		}
		v.HasShows = true
		byID[sh.ID] = sh
		candidates = append(candidates, planner.Show{
			ID: sh.ID, Title: sh.Title, Watchers: watchers[sh.ID],
			// Watched progress is not used for now: it only reflects the
			// owner's Jellyfin account, which is often not how the group watches.
			Unavailable: awaiting(sh),
		})
	}
	toNames := func(ids []int64) []string {
		out := make([]string, len(ids))
		for i, id := range ids {
			out[i] = names[id]
		}
		return out
	}
	for _, res := range planner.Rank(present, candidates) {
		v.Results = append(v.Results, resultView{
			Show: byID[res.Show.ID], Tier: res.Tier, Finished: res.Show.Finished,
			Missing: toNames(res.Missing), Extra: toNames(res.Extra),
		})
	}
	return v, nil
}

func (s *Server) planner(w http.ResponseWriter, r *http.Request, g db.Group) {
	// Refresh progress from Jellyfin if the cache is stale. A failure is
	// recorded on the group and shown as a notice; cached data is still used.
	if time.Since(time.Unix(g.LastSyncAt, 0)) > syncMaxAge {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		g.LastSyncError = ""
		if err := s.syncGroup(ctx, g.ID); err != nil {
			g.LastSyncError = err.Error()
		}
		cancel()
	}
	s.renderPlanner(w, r, g, "base", readPresence(r)[g.ID])
}

func (s *Server) renderPlanner(w http.ResponseWriter, r *http.Request, g db.Group, tmpl string, here map[int64]bool) {
	v, err := s.plannerView(r.Context(), g, here)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.exec(w, r, http.StatusOK, "planner", tmpl, pageData{Group: &g, Tab: "planner", D: v})
}

// afterPresence answers htmx with just the planner fragment, and plain form
// posts with a redirect.
func (s *Server) afterPresence(w http.ResponseWriter, r *http.Request, g db.Group, p presence) {
	s.writePresence(w, r, p)
	if r.Header.Get("HX-Request") != "" {
		s.renderPlanner(w, r, g, "planner", p[g.ID])
		return
	}
	redirect(w, r, groupPath(g, ""))
}

func (s *Server) togglePresence(w http.ResponseWriter, r *http.Request, g db.Group) {
	p := readPresence(r)
	if pid := pathID(r, "pid"); pid > 0 {
		if p[g.ID] == nil {
			p[g.ID] = map[int64]bool{}
		}
		if p[g.ID][pid] {
			delete(p[g.ID], pid)
		} else {
			p[g.ID][pid] = true
		}
	}
	s.afterPresence(w, r, g, p)
}

func (s *Server) clearPresence(w http.ResponseWriter, r *http.Request, g db.Group) {
	p := readPresence(r)
	delete(p, g.ID)
	s.afterPresence(w, r, g, p)
}

func (s *Server) syncNow(w http.ResponseWriter, r *http.Request, g db.Group) {
	_ = s.syncGroup(r.Context(), g.ID) // outcome is shown on the planner page
	redirect(w, r, groupPath(g, ""))
}

// ---- people ----

func (s *Server) people(w http.ResponseWriter, r *http.Request, g db.Group) {
	people, err := s.store.ListPeople(r.Context(), g.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "people", pageData{Group: &g, Tab: "people", D: people})
}

func (s *Server) createPerson(w http.ResponseWriter, r *http.Request, g db.Group) {
	if name := cleanName(r.FormValue("name")); name != "" {
		if err := s.store.CreatePerson(r.Context(), g.ID, name); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	redirect(w, r, groupPath(g, "/people"))
}

func (s *Server) renamePerson(w http.ResponseWriter, r *http.Request, g db.Group) {
	if name := cleanName(r.FormValue("name")); name != "" {
		if err := s.store.RenamePerson(r.Context(), g.ID, pathID(r, "pid"), name); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	redirect(w, r, groupPath(g, "/people"))
}

func (s *Server) deletePerson(w http.ResponseWriter, r *http.Request, g db.Group) {
	if err := s.store.DeletePerson(r.Context(), g.ID, pathID(r, "pid")); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, groupPath(g, "/people"))
}

// ---- shows ----

// showRow is one show card; it carries what the card template needs.
type showRow struct {
	GroupID  int64
	Show     db.Show
	People   []db.Person
	Watchers map[int64]bool
	// Catalogue is set when Jellyseerr is configured.
	Catalogue bool
}

type showsView struct {
	Active   []showRow
	Archived []showRow
	// AddOpen expands the "Add shows" section: when the group has no active
	// shows yet, or while the user is in the middle of adding some.
	AddOpen bool
	// Catalogue is set when Jellyseerr is configured.
	Catalogue bool
	// Services are the suggestions for "where we watch".
	Services []string
}

func (s *Server) shows(w http.ResponseWriter, r *http.Request, g db.Group) {
	s.renderShows(w, r, g, "")
}

func (s *Server) renderShows(w http.ResponseWriter, r *http.Request, g db.Group, errMsg string) {
	ctx := r.Context()
	shows, err := s.store.ListShows(ctx, g.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	people, err := s.store.ListPeople(ctx, g.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	watchers, err := s.store.Watchers(ctx, g.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Newest first, so a show that was just added is at the top.
	slices.SortFunc(shows, func(a, b db.Show) int { return cmp.Compare(b.ID, a.ID) })
	v := showsView{Catalogue: s.seerr != nil, Services: watchOptions(shows)}
	for _, sh := range shows {
		row := showRow{GroupID: g.ID, Show: sh, People: people, Watchers: map[int64]bool{}, Catalogue: v.Catalogue}
		for _, pid := range watchers[sh.ID] {
			row.Watchers[pid] = true
		}
		if sh.Archived {
			v.Archived = append(v.Archived, row)
		} else {
			v.Active = append(v.Active, row)
		}
	}
	v.AddOpen = len(v.Active) == 0 || r.URL.Query().Has("add") || errMsg != ""
	status := http.StatusOK
	if errMsg != "" {
		status = http.StatusBadGateway
	}
	s.render(w, r, status, "shows", pageData{Group: &g, Tab: "shows", Error: errMsg, D: v})
}

var jellyfinIDRe = regexp.MustCompile(`^[0-9a-fA-F-]{32,36}$`)

func (s *Server) createShow(w http.ResponseWriter, r *http.Request, g db.Group) {
	ctx := r.Context()
	if jfID := r.FormValue("jellyfin_id"); jfID != "" {
		if !jellyfinIDRe.MatchString(jfID) {
			http.Error(w, "bad series id", http.StatusBadRequest)
			return
		}
		c, err := s.jellyfinClient(ctx, g.ID)
		var found []jellyfin.Series
		if err == nil {
			found, err = c.ListSeries(ctx, "", []string{jfID}, 1)
		}
		if err == nil && len(found) == 0 {
			err = errors.New("series not found")
		}
		if err != nil {
			s.renderShows(w, r, g, "Could not add the show from Jellyfin: "+err.Error())
			return
		}
		if err := s.store.CreateShow(ctx, g.ID, db.Show{JellyfinID: found[0].ID, Title: found[0].Name}); err != nil {
			s.fail(w, r, err)
			return
		}
		_ = s.syncGroup(ctx, g.ID) // fills in progress and poster
	} else if raw := r.FormValue("tmdb_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 || s.seerr == nil {
			http.Error(w, "bad show id", http.StatusBadRequest)
			return
		}
		info, err := s.seerr.Show(ctx, id)
		if err != nil {
			s.renderShows(w, r, g, "Could not add the show from Jellyseerr: "+err.Error())
			return
		}
		if err := s.store.CreateShow(ctx, g.ID, db.Show{
			Title: info.Title, TMDBID: info.TMDBID, Year: info.Year, PosterPath: info.PosterPath, Availability: info.Status,
		}); err != nil {
			s.fail(w, r, err)
			return
		}
		if r.FormValue("request") == "1" && info.Requestable() {
			if msg := s.request(ctx, id); msg != "" {
				s.renderShows(w, r, g, "The show was added, but not requested. "+msg)
				return
			}
		}
		_ = s.syncGroup(ctx, g.ID) // picks up its status, and links it if it is in Jellyfin after all
	} else if title := cleanName(r.FormValue("title")); title != "" {
		if err := s.store.CreateShow(ctx, g.ID, db.Show{Title: title}); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	redirect(w, r, groupPath(g, "/shows?add"))
}

func (s *Server) setWatchers(w http.ResponseWriter, r *http.Request, g db.Group) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	var ids []int64
	for _, v := range r.PostForm["person"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	if err := s.store.SetWatchers(r.Context(), g.ID, pathID(r, "sid"), ids); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, groupPath(g, "/shows"))
}

func (s *Server) archiveShow(w http.ResponseWriter, r *http.Request, g db.Group) {
	if err := s.store.SetShowArchived(r.Context(), g.ID, pathID(r, "sid"), r.FormValue("archived") == "1"); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, groupPath(g, "/shows"))
}

func (s *Server) deleteShow(w http.ResponseWriter, r *http.Request, g db.Group) {
	if err := s.store.DeleteShow(r.Context(), g.ID, pathID(r, "sid")); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, groupPath(g, "/shows"))
}

type libraryView struct {
	Series []jellyfin.Series
	Error  string
	// From Jellyseerr: shows matching the search that aren't in Jellyfin.
	Catalogue      []seerr.Show
	CatalogueError string
	// Query is the search text, offered as a title to add as it is.
	Query string
}

// library is an htmx fragment: Jellyfin series not yet added to the group.
func (s *Server) library(w http.ResponseWriter, r *http.Request, g db.Group) {
	ctx := r.Context()
	q := cleanName(r.URL.Query().Get("q"))
	v := libraryView{Query: q}
	shows, err := s.store.ListShows(ctx, g.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	added, addedTMDB := map[string]bool{}, map[int64]bool{}
	for _, sh := range shows {
		added[sh.JellyfinID], addedTMDB[sh.TMDBID] = true, true
	}
	c, err := s.jellyfinClient(ctx, g.ID)
	var all []jellyfin.Series
	if err == nil {
		all, err = c.ListSeries(ctx, q, nil, 60)
	}
	if err != nil {
		v.Error = "Could not load the Jellyfin library: " + err.Error()
	}
	for _, sr := range all {
		if !added[sr.ID] {
			v.Series = append(v.Series, sr)
		}
		added[sr.ID] = true // listed above, or already in the group
	}
	// The whole catalogue is only worth showing for a specific search.
	if s.seerr != nil && q != "" {
		found, err := s.seerr.Search(ctx, q)
		if err != nil {
			v.CatalogueError = "Could not search Jellyseerr: " + err.Error()
		}
		for _, sh := range found {
			inLibrary := sh.JellyfinID != "" && added[sh.JellyfinID]
			if !addedTMDB[sh.TMDBID] && !inLibrary && len(v.Catalogue) < 20 {
				v.Catalogue = append(v.Catalogue, sh)
			}
		}
	}
	s.exec(w, r, http.StatusOK, "shows", "library", pageData{Group: &g, D: v})
}

// image proxies a poster so browsers never talk to Jellyfin directly.
func (s *Server) image(w http.ResponseWriter, r *http.Request, g db.Group) {
	item := r.PathValue("item")
	if !jellyfinIDRe.MatchString(item) {
		http.NotFound(w, r)
		return
	}
	c, err := s.jellyfinClient(r.Context(), g.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	resp, err := c.Image(r.Context(), item)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	serveImage(w, r, resp)
}

// serveImage passes on an image fetched from another server, and closes it.
func serveImage(w http.ResponseWriter, r *http.Request, resp *http.Response) {
	defer resp.Body.Close()
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") || strings.HasPrefix(ct, "image/svg") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	io.Copy(w, io.LimitReader(resp.Body, 5<<20))
}

// ---- settings ----

type settingsView struct {
	Managers  []db.Manager
	OwnerName string // whose Jellyfin account the group reads
	InviteURL string
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request, g db.Group) {
	s.renderSettings(w, r, g, "")
}

func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, g db.Group, inviteURL string) {
	managers, err := s.store.ListManagers(r.Context(), g.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	v := settingsView{Managers: managers, InviteURL: inviteURL}
	for _, m := range managers {
		if m.Role == "owner" {
			v.OwnerName = m.Name
		}
	}
	s.render(w, r, http.StatusOK, "settings", pageData{Group: &g, Tab: "settings", D: v})
}

func (s *Server) renameGroup(w http.ResponseWriter, r *http.Request, g db.Group) {
	if name := cleanName(r.FormValue("name")); name != "" {
		if err := s.store.RenameGroup(r.Context(), g.ID, name); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	redirect(w, r, groupPath(g, "/settings"))
}

func (s *Server) createInvite(w http.ResponseWriter, r *http.Request, g db.Group) {
	b := make([]byte, 24)
	rand.Read(b)
	token := base64.RawURLEncoding.EncodeToString(b)
	if err := s.store.CreateInvite(r.Context(), token, g.ID, time.Now().Add(inviteTTL)); err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderSettings(w, r, g, s.baseURL+"/invite/"+token)
}

func (s *Server) ownerOnly(w http.ResponseWriter, r *http.Request, g db.Group) bool {
	if g.IsOwner() {
		return true
	}
	s.render(w, r, http.StatusForbidden, "message", pageData{Group: &g, D: "Only the group owner can do that."})
	return false
}

func (s *Server) removeManager(w http.ResponseWriter, r *http.Request, g db.Group) {
	if !s.ownerOnly(w, r, g) {
		return
	}
	if err := s.store.RemoveManager(r.Context(), g.ID, pathID(r, "uid")); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, groupPath(g, "/settings"))
}

func (s *Server) deleteGroup(w http.ResponseWriter, r *http.Request, g db.Group) {
	if !s.ownerOnly(w, r, g) {
		return
	}
	if err := s.store.DeleteGroup(r.Context(), g.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/")
}

// ---- jellyfin sync ----

// jellyfinClient talks to Jellyfin as the group's owner: their library and
// watched state are what the group sees, whoever is looking at it.
func (s *Server) jellyfinClient(ctx context.Context, groupID int64) (*jellyfin.Client, error) {
	cred, err := s.store.OwnerCredentials(ctx, groupID)
	if err != nil {
		return nil, err
	}
	token, err := s.box.Open(cred.TokenEnc)
	if err != nil {
		return nil, fmt.Errorf("the stored Jellyfin login of %s can't be read (did SECRET_KEY change?); they need to log in again", cred.Name)
	}
	return &jellyfin.Client{BaseURL: s.jellyfinURL, Token: token, UserID: cred.JellyfinUserID, DeviceID: cred.DeviceID, HTTP: s.hc}, nil
}

// syncGroup refreshes progress and metadata of the group's Jellyfin shows
// and records the outcome on the group.
func (s *Server) syncGroup(ctx context.Context, groupID int64) error {
	err := s.doSync(ctx, groupID)
	msg := ""
	if errors.Is(err, jellyfin.ErrBadCredentials) {
		err = errors.New("the group owner's Jellyfin login is no longer valid; they need to log in to TV Show Planner again")
	}
	if err != nil {
		msg = err.Error()
	}
	// Record the outcome even if the request context has expired.
	_ = s.store.SetSyncStatus(context.WithoutCancel(ctx), groupID, msg)
	return err
}

func (s *Server) doSync(ctx context.Context, groupID int64) error {
	c, err := s.jellyfinClient(ctx, groupID)
	if err != nil {
		return err
	}
	shows, err := s.store.ListShows(ctx, groupID)
	if err != nil {
		return err
	}
	if err := s.resolveUnlinked(ctx, c, groupID, shows); err != nil {
		return err
	}
	var ids []string
	for _, sh := range shows {
		if sh.JellyfinID != "" {
			ids = append(ids, sh.JellyfinID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	series, err := c.ListSeries(ctx, "", ids, 0)
	if err != nil {
		return err
	}
	byID := map[string]jellyfin.Series{}
	for _, sr := range series {
		byID[sr.ID] = sr
	}
	for _, sh := range shows {
		sr, ok := byID[sh.JellyfinID]
		if !ok {
			continue // manual show, or removed from Jellyfin: keep what we have
		}
		sh.Title, sh.Year, sh.HasImage = sr.Name, sr.Year, sr.HasImage
		sh.Unplayed, sh.LastPlayedAt, sh.NextUp = sr.Unplayed, sr.LastPlayedAt, ""
		if err := s.store.UpdateShowSync(ctx, sh); err != nil {
			return err
		}
	}
	return nil
}
