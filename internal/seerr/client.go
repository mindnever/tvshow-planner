// Package seerr is a small client for the Jellyseerr / Seerr HTTP API. It is
// used for what Jellyfin can't tell us: shows that aren't in the library yet,
// whether they have been requested, and requesting them.
package seerr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Availability of a show, as Jellyseerr reports it.
const (
	Unknown    = 1 // known to Jellyseerr's catalogue only: nobody asked for it
	Pending    = 2 // requested, waiting for approval
	Processing = 3 // approved, being downloaded
	Partial    = 4 // some of it is in the library
	Available  = 5
	Deleted    = 6
)

var (
	// ErrAlreadyRequested: someone asked for the show before.
	ErrAlreadyRequested = errors.New("this show has already been requested")
	// ErrNotAllowed: Jellyseerr refused the request for this user (no
	// request permission, quota used up, or the show is blocked).
	ErrNotAllowed = errors.New("Jellyseerr doesn't allow you to request this show")
)

// Client talks to Jellyseerr with its admin API key.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
	// PosterBase is where poster paths are resolved; empty means TMDB.
	PosterBase string

	mu      sync.Mutex
	users   map[string]int // normalised Jellyfin user ID -> Jellyseerr user ID
	usersAt time.Time
}

// Show is a TV show from Jellyseerr's catalogue (TMDB).
type Show struct {
	TMDBID     int64
	Title      string
	Year       int
	PosterPath string // e.g. "/abc.jpg", or ""
	Status     int    // one of the availability constants; 0 if Jellyseerr has no record
	JellyfinID string // the series in Jellyfin, once Jellyseerr has found it there
}

// Requestable reports whether asking for the show makes sense.
func (s Show) Requestable() bool { return Requestable(s.Status) }

// Requestable reports whether a show with this availability can be requested.
func Requestable(status int) bool {
	return status == 0 || status == Unknown || status == Deleted
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// do sends a request; asUser > 0 acts on behalf of that Jellyseerr user
// instead of the admin. The caller closes the body.
func (c *Client) do(ctx context.Context, method, path string, body any, asUser int) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+"/api/v1"+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", c.APIKey)
	req.Header.Set("Accept", "application/json")
	if asUser > 0 {
		req.Header.Set("X-API-User", strconv.Itoa(asUser))
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.httpClient().Do(req)
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	resp, err := c.do(ctx, http.MethodGet, path, nil, 0)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return errors.New("jellyseerr rejected the API key")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jellyseerr: GET %s: %s", path, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
}

// tv is the part of a search result or of TV details that we use.
type tv struct {
	ID           int64  `json:"id"`
	MediaType    string `json:"mediaType"`
	Name         string `json:"name"`
	FirstAirDate string `json:"firstAirDate"`
	PosterPath   string `json:"posterPath"`
	MediaInfo    *struct {
		Status          int    `json:"status"`
		JellyfinMediaID string `json:"jellyfinMediaId"`
	} `json:"mediaInfo"`
}

var posterRe = regexp.MustCompile(`^/[A-Za-z0-9_-]+\.(jpg|jpeg|png|webp)$`)

func (t tv) show() Show {
	s := Show{TMDBID: t.ID, Title: strings.TrimSpace(t.Name)}
	if len(t.FirstAirDate) >= 4 {
		s.Year, _ = strconv.Atoi(t.FirstAirDate[:4])
	}
	if posterRe.MatchString(t.PosterPath) {
		s.PosterPath = t.PosterPath
	}
	if t.MediaInfo != nil {
		s.Status, s.JellyfinID = t.MediaInfo.Status, t.MediaInfo.JellyfinMediaID
	}
	return s
}

// Search looks up TV shows by title in Jellyseerr's catalogue.
func (c *Client) Search(ctx context.Context, query string) ([]Show, error) {
	var res struct{ Results []tv }
	// Jellyseerr rejects "+" for spaces in the query.
	q := strings.ReplaceAll(url.QueryEscape(query), "+", "%20")
	if err := c.getJSON(ctx, "/search?page=1&query="+q, &res); err != nil {
		return nil, err
	}
	var out []Show
	for _, t := range res.Results {
		if t.MediaType == "tv" && t.ID > 0 && strings.TrimSpace(t.Name) != "" {
			out = append(out, t.show())
		}
	}
	return out, nil
}

// Show returns one TV show by its TMDB ID.
func (c *Client) Show(ctx context.Context, tmdbID int64) (Show, error) {
	var t tv
	if err := c.getJSON(ctx, "/tv/"+strconv.FormatInt(tmdbID, 10), &t); err != nil {
		return Show{}, err
	}
	if t.ID == 0 || strings.TrimSpace(t.Name) == "" {
		return Show{}, errors.New("jellyseerr: unexpected TV details response")
	}
	return t.show(), nil
}

// Request asks Jellyseerr for every season of a show on behalf of one of its
// users, so that user's approval rules and quota apply.
func (c *Client) Request(ctx context.Context, tmdbID int64, asUser int) error {
	resp, err := c.do(ctx, http.MethodPost, "/request",
		map[string]any{"mediaType": "tv", "mediaId": tmdbID, "seasons": "all"}, asUser)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK:
		return nil
	case http.StatusAccepted, http.StatusConflict: // nothing left to request, or a duplicate
		return ErrAlreadyRequested
	case http.StatusForbidden:
		return ErrNotAllowed
	}
	return fmt.Errorf("jellyseerr: POST /request: %s", resp.Status)
}

func normID(id string) string { return strings.ToLower(strings.ReplaceAll(id, "-", "")) }

// UserID finds the Jellyseerr account that belongs to a Jellyfin user.
func (c *Client) UserID(ctx context.Context, jellyfinUserID string) (id int, ok bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := normID(jellyfinUserID)
	// A miss refreshes the list, since the account may have just been created.
	if id, ok := c.users[key]; ok && time.Since(c.usersAt) < 5*time.Minute {
		return id, true, nil
	}
	users := map[string]int{}
	const page = 100
	for skip := 0; skip < 5000; skip += page {
		var res struct {
			Results []struct {
				ID             int    `json:"id"`
				JellyfinUserID string `json:"jellyfinUserId"`
			}
		}
		if err := c.getJSON(ctx, fmt.Sprintf("/user?take=%d&skip=%d", page, skip), &res); err != nil {
			return 0, false, err
		}
		for _, u := range res.Results {
			if u.JellyfinUserID != "" {
				users[normID(u.JellyfinUserID)] = u.ID
			}
		}
		if len(res.Results) < page {
			break
		}
	}
	c.users, c.usersAt = users, time.Now()
	id, ok = users[key]
	return id, ok, nil
}

// Poster fetches a show's poster from TMDB's image host. The caller must
// close the response body.
func (c *Client) Poster(ctx context.Context, path string) (*http.Response, error) {
	if !posterRe.MatchString(path) {
		return nil, errors.New("bad poster path")
	}
	base := c.PosterBase
	if base == "" {
		base = "https://image.tmdb.org/t/p/w300"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("poster: %s", resp.Status)
	}
	return resp, nil
}
