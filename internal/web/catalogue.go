package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"tvshow-planner/internal/auth"
	"tvshow-planner/internal/db"
	"tvshow-planner/internal/jellyfin"
	"tvshow-planner/internal/seerr"
)

// Shows from Jellyseerr's catalogue: known by their TMDB ID, possibly
// requested, and not in Jellyfin yet.

// awaiting reports whether a show is tracked in Jellyseerr but can't be
// watched yet because it isn't in Jellyfin.
func awaiting(sh db.Show) bool { return sh.JellyfinID == "" && sh.TMDBID != 0 }

func availabilityLabel(status int) string {
	switch status {
	case seerr.Pending:
		return "Requested"
	case seerr.Processing:
		return "Downloading"
	case seerr.Partial:
		return "Partly available"
	case seerr.Available:
		return "Available"
	}
	return "Not requested"
}

func sameTitle(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// findShow returns the group's show named by the {sid} path value.
func (s *Server) findShow(r *http.Request, g db.Group) (db.Show, []db.Show, bool) {
	shows, err := s.store.ListShows(r.Context(), g.ID)
	if err != nil {
		return db.Show{}, nil, false
	}
	id := pathID(r, "sid")
	for _, sh := range shows {
		if sh.ID == id {
			return sh, shows, true
		}
	}
	return db.Show{}, shows, false
}

// request asks Jellyseerr for a show in the name of the logged-in user. It
// returns a message for the user if that didn't work.
func (s *Server) request(ctx context.Context, tmdbID int64) string {
	jfID, err := s.store.JellyfinUserID(ctx, auth.User(ctx).ID)
	if err != nil {
		return "Your account could not be read."
	}
	uid, ok, err := s.seerr.UserID(ctx, jfID)
	if err != nil {
		slog.Error("jellyseerr user lookup failed", "err", err)
		return "Jellyseerr could not be reached. Try again in a moment."
	}
	if !ok {
		return "You don't have a Jellyseerr account yet. Log in to Jellyseerr once, then try again."
	}
	switch err := s.seerr.Request(ctx, tmdbID, uid); {
	case err == nil, errors.Is(err, seerr.ErrAlreadyRequested):
		return ""
	case errors.Is(err, seerr.ErrNotAllowed):
		return "Jellyseerr doesn't allow you to request this show (no permission, or your quota is used up)."
	default:
		slog.Error("jellyseerr request failed", "err", err)
		return "Jellyseerr could not take the request. Try again in a moment."
	}
}

// requestShow requests a show that is already in the group.
func (s *Server) requestShow(w http.ResponseWriter, r *http.Request, g db.Group) {
	ctx := r.Context()
	sh, _, ok := s.findShow(r, g)
	if !ok || s.seerr == nil || !awaiting(sh) {
		http.NotFound(w, r)
		return
	}
	if msg := s.request(ctx, sh.TMDBID); msg != "" {
		s.renderShows(w, r, g, msg)
		return
	}
	if info, err := s.seerr.Show(ctx, sh.TMDBID); err == nil {
		_ = s.store.SetShowAvailability(ctx, sh.ID, info.Status)
	}
	redirect(w, r, groupPath(g, "/shows"))
}

type matchView struct {
	ShowID int64
	Shows  []seerr.Show
	Error  string
}

// matches is an htmx fragment: Jellyseerr shows that a typed title could be.
func (s *Server) matches(w http.ResponseWriter, r *http.Request, g db.Group) {
	sh, shows, ok := s.findShow(r, g)
	if !ok || s.seerr == nil {
		http.NotFound(w, r)
		return
	}
	v := matchView{ShowID: sh.ID}
	found, err := s.seerr.Search(r.Context(), sh.Title)
	if err != nil {
		v.Error = "Could not search Jellyseerr: " + err.Error()
	}
	taken := map[int64]bool{}
	for _, other := range shows {
		taken[other.TMDBID] = true
	}
	for _, f := range found {
		if !taken[f.TMDBID] && len(v.Shows) < 8 {
			v.Shows = append(v.Shows, f)
		}
	}
	s.exec(w, r, http.StatusOK, "shows", "matches", pageData{Group: &g, D: v})
}

// matchShow ties a typed title to the Jellyseerr show the user picked. The
// show keeps its watchers.
func (s *Server) matchShow(w http.ResponseWriter, r *http.Request, g db.Group) {
	ctx := r.Context()
	sh, shows, ok := s.findShow(r, g)
	id, err := strconv.ParseInt(r.FormValue("tmdb_id"), 10, 64)
	if !ok || s.seerr == nil || err != nil || id <= 0 || sh.JellyfinID != "" || sh.TMDBID != 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	for _, other := range shows {
		if other.TMDBID == id {
			s.renderShows(w, r, g, "This group already has "+other.Title+".")
			return
		}
	}
	info, err := s.seerr.Show(ctx, id)
	if err != nil {
		s.renderShows(w, r, g, "Could not read the show from Jellyseerr: "+err.Error())
		return
	}
	sh.TMDBID, sh.Title, sh.Year, sh.PosterPath, sh.Availability = info.TMDBID, info.Title, info.Year, info.PosterPath, info.Status
	if err := s.store.SetShowCatalogue(ctx, g.ID, sh); err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.syncGroup(ctx, g.ID) // links it right away if it is in Jellyfin
	redirect(w, r, groupPath(g, "/shows"))
}

// poster proxies a TMDB poster, so browsers only ever talk to this app.
func (s *Server) poster(w http.ResponseWriter, r *http.Request, g db.Group) {
	if s.seerr == nil {
		http.NotFound(w, r)
		return
	}
	resp, err := s.seerr.Poster(r.Context(), "/"+r.PathValue("file"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	serveImage(w, r, resp)
}

// resolveUnlinked works on the shows that aren't tied to a Jellyfin series
// yet, updating shows in place:
//
//   - a typed title is linked to the one Jellyfin series with the same name,
//     or failing that, once, to the one Jellyseerr show with the same name;
//   - a Jellyseerr show gets its current status, and is linked to Jellyfin as
//     soon as Jellyseerr has found it there.
//
// Nothing is linked on a partial or ambiguous match. Jellyfin errors are
// returned; Jellyseerr errors are only logged, to be retried next time.
func (s *Server) resolveUnlinked(ctx context.Context, c *jellyfin.Client, groupID int64, shows []db.Show) error {
	inJellyfin, inCatalogue := map[string]bool{}, map[int64]bool{}
	for _, sh := range shows {
		inJellyfin[sh.JellyfinID], inCatalogue[sh.TMDBID] = true, true
	}
	link := func(sh *db.Show, jellyfinID string) {
		if !jellyfinIDRe.MatchString(jellyfinID) || inJellyfin[jellyfinID] {
			return
		}
		if err := s.store.LinkShowToJellyfin(ctx, sh.ID, jellyfinID); err != nil {
			slog.Warn("could not link show to Jellyfin", "show", sh.ID, "err", err)
			return
		}
		sh.JellyfinID, inJellyfin[jellyfinID] = jellyfinID, true
	}

	for i := range shows {
		sh := &shows[i]
		if sh.JellyfinID != "" {
			continue
		}
		if sh.TMDBID == 0 {
			found, err := c.ListSeries(ctx, sh.Title, nil, 20)
			if err != nil {
				return err
			}
			var same []jellyfin.Series
			for _, f := range found {
				if sameTitle(f.Name, sh.Title) {
					same = append(same, f)
				}
			}
			if len(same) == 1 {
				link(sh, same[0].ID)
			}
			if len(same) > 0 || s.seerr == nil || sh.MatchTried {
				continue
			}
			candidates, err := s.seerr.Search(ctx, sh.Title)
			if err != nil {
				slog.Warn("jellyseerr search failed", "title", sh.Title, "err", err)
				continue
			}
			var match []seerr.Show
			for _, f := range candidates {
				if sameTitle(f.Title, sh.Title) {
					match = append(match, f)
				}
			}
			if len(match) != 1 || inCatalogue[match[0].TMDBID] {
				_ = s.store.SetShowMatchTried(ctx, sh.ID)
				continue
			}
			m := match[0]
			sh.TMDBID, sh.Title, sh.Year, sh.PosterPath, sh.Availability = m.TMDBID, m.Title, m.Year, m.PosterPath, m.Status
			if err := s.store.SetShowCatalogue(ctx, groupID, *sh); err != nil {
				slog.Warn("could not match show to Jellyseerr", "show", sh.ID, "err", err)
				sh.TMDBID = 0
				continue
			}
			inCatalogue[m.TMDBID] = true
		}
		if s.seerr == nil {
			continue
		}
		info, err := s.seerr.Show(ctx, sh.TMDBID)
		if err != nil {
			slog.Warn("jellyseerr lookup failed", "show", sh.ID, "err", err)
			continue
		}
		if info.Status != sh.Availability {
			if err := s.store.SetShowAvailability(ctx, sh.ID, info.Status); err == nil {
				sh.Availability = info.Status
			}
		}
		if info.JellyfinID != "" {
			link(sh, info.JellyfinID)
		}
	}
	return nil
}
