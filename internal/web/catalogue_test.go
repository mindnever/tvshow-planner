package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const slowHorsesID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa3"

// fakeSeerr implements the Jellyseerr endpoints the client uses. Its
// catalogue has Slow Horses (100), two shows called Dark (200 is being
// downloaded, 201) and Quiz Night (300). Only alice has an account (ID 7).
type fakeSeerr struct {
	*httptest.Server
	mu       sync.Mutex
	down     bool
	status   map[int64]int
	jellyfin map[int64]string // jellyfinMediaId per show
	requests []string         // "<tmdb id> as <X-API-User>"
}

func (f *fakeSeerr) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func newFakeSeerr(t *testing.T) *fakeSeerr {
	type m = map[string]any
	f := &fakeSeerr{status: map[int64]int{200: 3}, jellyfin: map[int64]string{}}
	catalogue := []m{
		{"id": 100, "mediaType": "tv", "name": "Slow Horses", "firstAirDate": "2022-04-01", "posterPath": "/slow.jpg"},
		{"id": 200, "mediaType": "tv", "name": "Dark", "firstAirDate": "2017-12-01", "posterPath": "/dark.jpg"},
		{"id": 201, "mediaType": "tv", "name": "Dark", "firstAirDate": "2020-01-01"},
		{"id": 300, "mediaType": "tv", "name": "Quiz Night", "firstAirDate": "2019-05-05", "posterPath": "/quiz.jpg"},
		{"id": 900, "mediaType": "movie", "title": "Slow Horses: The Movie"},
	}
	// withInfo adds the mediaInfo Jellyseerr has for a show, if any. Callers hold f.mu.
	withInfo := func(c m) m {
		out := m{}
		for k, v := range c {
			out[k] = v
		}
		id := int64(c["id"].(int))
		if st, ok := f.status[id]; ok {
			out["mediaInfo"] = m{"status": st, "jellyfinMediaId": f.jellyfin[id]}
		}
		return out
	}
	api := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			defer f.mu.Unlock()
			switch {
			case f.down:
				w.WriteHeader(http.StatusBadGateway)
			case r.Header.Get("X-Api-Key") != "key":
				w.WriteHeader(http.StatusForbidden)
			default:
				h(w, r)
			}
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/search", api(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "+") { // the real server rejects "+" for spaces
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		q := strings.ToLower(r.URL.Query().Get("query"))
		out := []m{}
		for _, c := range catalogue {
			name, _ := c["name"].(string)
			title, _ := c["title"].(string)
			if strings.Contains(strings.ToLower(name+title), q) {
				out = append(out, withInfo(c))
			}
		}
		json.NewEncoder(w).Encode(m{"results": out})
	}))
	mux.HandleFunc("GET /api/v1/tv/{id}", api(func(w http.ResponseWriter, r *http.Request) {
		for _, c := range catalogue {
			if strconv.Itoa(c["id"].(int)) == r.PathValue("id") && c["mediaType"] == "tv" {
				json.NewEncoder(w).Encode(withInfo(c))
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	mux.HandleFunc("GET /api/v1/user", api(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(m{"results": []m{{"id": 1}, {"id": 7, "jellyfinUserId": "uid-alice"}}})
	}))
	mux.HandleFunc("POST /api/v1/request", api(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			MediaType string
			MediaID   int64 `json:"mediaId"`
			Seasons   string
		}
		json.NewDecoder(r.Body).Decode(&in)
		if in.MediaType != "tv" || in.Seasons != "all" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if f.status[in.MediaID] >= 2 {
			w.WriteHeader(http.StatusConflict)
			return
		}
		f.status[in.MediaID] = 2
		f.requests = append(f.requests, strconv.FormatInt(in.MediaID, 10)+" as "+r.Header.Get("X-API-User"))
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("{}"))
	}))
	mux.HandleFunc("GET /poster/{file}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("JPG"))
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func mustNotContain(t *testing.T, body string, unwanted ...string) {
	t.Helper()
	for _, u := range unwanted {
		if strings.Contains(body, u) {
			t.Errorf("unexpected %q in:\n%s", u, body)
		}
	}
}

func (a *testApp) get(c *http.Client, path string) string {
	a.t.Helper()
	code, body := a.do(c, "GET", path, nil)
	if code != http.StatusOK {
		a.t.Fatalf("GET %s = %d\n%s", path, code, body)
	}
	return body
}

func TestCatalogueAddRequestAndLink(t *testing.T) {
	app := startTestApp(t, true)
	alice := app.client("alice")
	app.post(alice, "/groups", url.Values{"name": {"Family"}})
	app.post(alice, "/g/1/people", url.Values{"name": {"Ana"}})

	// Searching lists catalogue shows under the Jellyfin ones; browsing doesn't.
	lib := app.get(alice, "/g/1/library?q=slow+horses")
	mustContain(t, lib, "Not in your library yet", "Slow Horses", `name="tmdb_id" value="100"`, `src="/g/1/poster/slow.jpg"`, `name="request"`)
	mustNotContain(t, lib, "The Movie")
	mustNotContain(t, app.get(alice, "/g/1/library"), "Not in your library yet")
	// A show that is already on its way can be added but not requested again.
	lib = app.get(alice, "/g/1/library?q=dark")
	mustContain(t, lib, "Downloading", `value="200"`, `value="201"`)
	if n := strings.Count(lib, `name="request"`); n != 1 {
		t.Errorf("request buttons for Dark = %d, want 1", n)
	}
	if code, body := app.do(alice, "GET", "/g/1/poster/slow.jpg", nil); code != http.StatusOK || body != "JPG" {
		t.Errorf("poster = %d %q", code, body)
	}
	if code, _ := app.do(alice, "GET", "/g/1/poster/..%2Fsecret", nil); code != http.StatusNotFound {
		t.Errorf("bad poster path = %d", code)
	}

	// Add and request: the request goes out as alice's Jellyseerr user.
	shows := app.post(alice, "/g/1/shows", url.Values{"tmdb_id": {"100"}, "request": {"1"}})
	mustContain(t, shows, "Slow Horses", `<span class="badge wait">Requested</span>`, `src="/g/1/poster/slow.jpg"`)
	mustNotContain(t, shows, "/shows/1/request", "Find match")
	app.js.set(func() {
		if len(app.js.requests) != 1 || app.js.requests[0] != "100 as 7" {
			t.Errorf("requests = %v", app.js.requests)
		}
	})
	// It is no longer offered in the search.
	mustNotContain(t, app.get(alice, "/g/1/library?q=slow"), "Not in your library yet")

	// On the planner it is listed, marked as not available.
	app.post(alice, "/g/1/shows/1/watchers", url.Values{"person": {"1"}})
	app.post(alice, "/g/1/presence/1", nil)
	mustContain(t, app.get(alice, "/g/1"), "Slow Horses", "Not available yet · Requested")

	// Once Jellyseerr has found it in Jellyfin, a sync links it and progress shows up.
	app.js.set(func() { app.js.status[100], app.js.jellyfin[100] = 5, slowHorsesID })
	app.jf.set(func() {
		app.jf.extra = append(app.jf.extra, map[string]any{"Id": slowHorsesID, "Name": "Slow Horses", "ProductionYear": 2022,
			"UserData": map[string]any{"UnplayedItemCount": 2}})
	})
	planner := app.post(alice, "/g/1/sync", nil)
	mustContain(t, planner, "Slow Horses", "On Home Media")
	mustNotContain(t, planner, "Not available yet")
	mustNotContain(t, app.get(alice, "/g/1/shows"), "Requested", "Not requested")

	// Adding without requesting, then requesting from the card.
	shows = app.post(alice, "/g/1/shows", url.Values{"tmdb_id": {"300"}})
	mustContain(t, shows, "Quiz Night", "Not requested", `action="/g/1/shows/2/request"`)
	shows = app.post(alice, "/g/1/shows/2/request", nil)
	mustContain(t, shows, `<span class="badge wait">Requested</span>`)
	mustNotContain(t, shows, "/shows/2/request")

	// bob has no Jellyseerr account: the show is added, the request refused.
	bob := app.client("bob")
	app.post(bob, "/groups", url.Values{"name": {"Work"}})
	code, body := app.do(bob, "POST", "/g/2/shows", url.Values{"tmdb_id": {"201"}, "request": {"1"}})
	if code == http.StatusOK {
		t.Errorf("request without an account = %d", code)
	}
	mustContain(t, body, "have a Jellyseerr account yet", "Dark")
	app.js.set(func() {
		if len(app.js.requests) != 2 {
			t.Errorf("requests = %v", app.js.requests)
		}
	})
}

func TestTypedTitlesAreMatched(t *testing.T) {
	app := startTestApp(t, true)
	alice := app.client("alice")
	app.post(alice, "/groups", url.Values{"name": {"Family"}})
	app.post(alice, "/g/1/people", url.Values{"name": {"Ana"}})

	// Jellyseerr is down while the first title is looked up: nothing is
	// decided, so it is tried again later.
	app.js.set(func() { app.js.down = true })
	app.post(alice, "/g/1/shows", url.Values{"title": {"quiz night"}}) // show 1: one catalogue match
	app.post(alice, "/g/1/sync", nil)
	mustContain(t, app.get(alice, "/g/1/shows"), "quiz night", "· not in Jellyfin")
	app.js.set(func() { app.js.down = false })

	app.post(alice, "/g/1/shows", url.Values{"title": {"Dark"}})  // show 2: two catalogue matches
	app.post(alice, "/g/1/shows", url.Values{"title": {"andor"}}) // show 3: in the Jellyfin library
	app.post(alice, "/g/1/shows/2/watchers", url.Values{"person": {"1"}})
	app.post(alice, "/g/1/sync", nil)

	shows := app.get(alice, "/g/1/shows")
	mustContain(t, shows, "Quiz Night", `src="/g/1/poster/quiz.jpg"`, "Not requested", "Andor")
	mustNotContain(t, shows, "quiz night", "andor")
	// Unlinked shows in the group don't hide catalogue results.
	mustContain(t, app.get(alice, "/g/1/library?q=slow"), "Not in your library yet", "Slow Horses")
	// Only the ambiguous title is left unmatched.
	if n := strings.Count(shows, "· not in Jellyfin"); n != 1 {
		t.Errorf("unmatched shows = %d, want 1\n%s", n, shows)
	}
	if n := strings.Count(shows, "Find match"); n != 1 {
		t.Errorf("Find match buttons = %d, want 1", n)
	}

	// The user picks which Dark it is; the show keeps its watchers.
	options := app.get(alice, "/g/1/shows/2/match")
	mustContain(t, options, "2017", "2020", `action="/g/1/shows/2/match"`)
	shows = app.post(alice, "/g/1/shows/2/match", url.Values{"tmdb_id": {"200"}})
	mustContain(t, shows, "Dark", "2017", "Downloading", `name="person" value="1" checked`)
	mustNotContain(t, shows, "Find match", "· not in Jellyfin")

	// The other Dark can't be matched to a show that already has an entry.
	if code, _ := app.do(alice, "POST", "/g/1/shows/2/match", url.Values{"tmdb_id": {"201"}}); code != http.StatusBadRequest {
		t.Errorf("second match = %d", code)
	}
}

func TestWithoutJellyseerr(t *testing.T) {
	app := newTestApp(t)
	alice := app.client("alice")
	app.post(alice, "/groups", url.Values{"name": {"Family"}})
	app.post(alice, "/g/1/shows", url.Values{"title": {"Quiz Night"}})
	app.post(alice, "/g/1/shows", url.Values{"title": {"Andor"}})
	app.post(alice, "/g/1/sync", nil)

	shows := app.get(alice, "/g/1/shows")
	mustContain(t, shows, "Search for a show")
	mustNotContain(t, shows, "Find match", "Jellyseerr")
	// A typed title is still linked to the Jellyfin series with the same name.
	if n := strings.Count(shows, "· not in Jellyfin"); n != 1 {
		t.Errorf("unlinked shows = %d, want 1 (Quiz Night)\n%s", n, shows)
	}
	// Whatever is typed in the search can be added as it is.
	lib := app.get(alice, "/g/1/library?q=slow")
	mustContain(t, lib, "Nothing found.", `name="title" value="slow"`)
	mustNotContain(t, lib, "Not in your library yet")
	mustContain(t, app.get(alice, "/g/1/library?q=sever"), "Not what you", `name="title" value="sever"`)
	mustNotContain(t, app.get(alice, "/g/1/library"), `name="title"`)
	for _, req := range [][2]string{{"GET", "/g/1/poster/slow.jpg"}, {"GET", "/g/1/shows/1/match"}, {"POST", "/g/1/shows/1/request"}} {
		if code, _ := app.do(alice, req[0], req[1], url.Values{}); code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", req[0], req[1], code)
		}
	}
	if code, _ := app.do(alice, "POST", "/g/1/shows", url.Values{"tmdb_id": {"100"}}); code != http.StatusBadRequest {
		t.Errorf("catalogue add without Jellyseerr = %d", code)
	}
}

func TestWatchedElsewhere(t *testing.T) {
	app := startTestApp(t, true)
	alice := app.client("alice")
	app.post(alice, "/groups", url.Values{"name": {"Family"}})
	app.post(alice, "/g/1/people", url.Values{"name": {"Ana"}})
	app.post(alice, "/g/1/shows", url.Values{"tmdb_id": {"300"}})           // show 1: Quiz Night, from the catalogue
	app.post(alice, "/g/1/shows", url.Values{"jellyfin_id": {severanceID}}) // show 2: in Jellyfin
	mustContain(t, app.get(alice, "/g/1/shows"), "Not requested", "/g/1/shows/1/request", `<option value="Netflix">`)

	// Saying where it is watched: no status, nothing to request, and the
	// name is suggested from then on.
	shows := app.post(alice, "/g/1/shows/1/where", url.Values{"where": {"  My   Disc Shelf "}})
	mustContain(t, shows, "On My Disc Shelf", `<option value="My Disc Shelf">`, `name="where" value="My Disc Shelf"`)
	mustNotContain(t, shows, "Not requested", "/g/1/shows/1/request")

	// On the planner it is an ordinary show, not one that is still to come.
	app.post(alice, "/g/1/shows/1/watchers", url.Values{"person": {"1"}})
	app.post(alice, "/g/1/presence/1", nil)
	planner := app.get(alice, "/g/1")
	mustContain(t, planner, "Quiz Night", "On My Disc Shelf")
	mustNotContain(t, planner, "Not available yet", " finished", "On Home Media")

	// It is left alone even when it turns up in Jellyfin.
	app.js.set(func() { app.js.status[300], app.js.jellyfin[300] = 5, slowHorsesID })
	app.jf.set(func() {
		app.jf.extra = append(app.jf.extra, map[string]any{"Id": slowHorsesID, "Name": "Quiz Night", "ProductionYear": 2019,
			"UserData": map[string]any{"UnplayedItemCount": 4}})
	})
	app.post(alice, "/g/1/sync", nil)
	mustContain(t, app.get(alice, "/g/1/shows"), "On My Disc Shelf")

	// Clearing it hands the show back: the next sync links it to Jellyfin.
	mustNotContain(t, app.post(alice, "/g/1/shows/1/where", url.Values{"where": {""}}), "On My Disc Shelf")
	mustContain(t, app.post(alice, "/g/1/sync", nil), "Quiz Night", "On Home Media")

	// A show in Jellyfin is watched there; other people's groups are off limits.
	if code, _ := app.do(alice, "POST", "/g/1/shows/2/where", url.Values{"where": {"Netflix"}}); code != http.StatusBadRequest {
		t.Errorf("where on a Jellyfin show = %d", code)
	}
	bob := app.client("bob")
	if code, _ := app.do(bob, "POST", "/g/1/shows/1/where", url.Values{"where": {"Netflix"}}); code != http.StatusForbidden {
		t.Errorf("where in someone else's group = %d", code)
	}
}
