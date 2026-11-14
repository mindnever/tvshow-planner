package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"tvshow-planner/internal/auth"
	"tvshow-planner/internal/config"
	"tvshow-planner/internal/db"
	"tvshow-planner/internal/secretbox"
)

const (
	severanceID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"
	andorID     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa2"
)

// fakeJellyfin implements just the endpoints the client uses.
func fakeJellyfin(t *testing.T) *httptest.Server {
	type m = map[string]any
	series := []m{
		{"Id": severanceID, "Name": "Severance", "ProductionYear": 2022, "ImageTags": m{"Primary": "x"},
			"UserData": m{"UnplayedItemCount": 3, "LastPlayedDate": "2026-11-01T20:00:00.0000000Z"}},
		{"Id": andorID, "Name": "Andor", "ProductionYear": 2022,
			"UserData": m{"UnplayedItemCount": 0}},
	}
	authed := func(r *http.Request) bool { return strings.Contains(r.Header.Get("Authorization"), `Token="tok-123"`) }
	mux := http.NewServeMux()
	mux.HandleFunc("POST /Users/AuthenticateByName", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Username, Pw string }
		json.NewDecoder(r.Body).Decode(&in)
		if in.Username != "family" || in.Pw != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(m{"AccessToken": "tok-123", "User": m{"Id": "user-1", "Name": "family"}})
	})
	mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) || r.URL.Query().Get("userId") != "user-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		out := []m{}
		ids := r.URL.Query().Get("Ids")
		for _, s := range series {
			if ids == "" || strings.Contains(ids, s["Id"].(string)) {
				out = append(out, s)
			}
		}
		json.NewEncoder(w).Encode(m{"Items": out})
	})
	mux.HandleFunc("GET /Shows/NextUp", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) || r.URL.Query().Get("seriesId") != severanceID {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(m{"Items": []m{{"Name": "Trojan's Horse", "ParentIndexNumber": 2, "IndexNumber": 5}}})
	})
	mux.HandleFunc("GET /Items/{id}/Images/Primary", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("PNG"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

type testApp struct {
	t     *testing.T
	srv   *httptest.Server
	store *db.Store
	auth  *auth.Auth
}

func newTestApp(t *testing.T) *testApp {
	store, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	a, err := auth.New(context.Background(), config.Config{DevAuthUser: "alice", BaseURL: "http://app.test"}, store)
	if err != nil {
		t.Fatal(err)
	}
	box, _ := secretbox.New("test-secret-key-0123456789")
	srv := httptest.NewServer(New(store, a, box, "http://app.test").Handler())
	t.Cleanup(srv.Close)
	return &testApp{t: t, srv: srv, store: store, auth: a}
}

// client returns an HTTP client logged in as the named user.
func (a *testApp) client(name string) *http.Client {
	u, err := a.store.UpsertUser(context.Background(), "test", name, name, "")
	if err != nil {
		a.t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	if err := a.auth.StartSession(context.Background(), rec, u.ID); err != nil {
		a.t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	base, _ := url.Parse(a.srv.URL)
	jar.SetCookies(base, rec.Result().Cookies())
	return &http.Client{Jar: jar}
}

func (a *testApp) do(c *http.Client, method, path string, form url.Values, hdr ...string) (int, string) {
	a.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, a.srv.URL+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (a *testApp) post(c *http.Client, path string, form url.Values) string {
	a.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	code, body := a.do(c, "POST", path, form)
	if code != http.StatusOK {
		a.t.Fatalf("POST %s = %d\n%s", path, code, body)
	}
	return body
}

func mustContain(t *testing.T, body string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("missing %q in:\n%s", w, body)
		}
	}
}

func TestEndToEnd(t *testing.T) {
	app := newTestApp(t)
	jf := fakeJellyfin(t)
	alice := app.client("alice")

	app.post(alice, "/groups", url.Values{"name": {"Family"}})
	for _, n := range []string{"Ana", "Bo", "Cy"} { // person IDs 1, 2, 3
		app.post(alice, "/g/1/people", url.Values{"name": {n}})
	}

	// Linking: wrong password is reported, right one stores an encrypted token.
	link := url.Values{"server_url": {jf.URL}, "username": {"family"}, "password": {"nope"}}
	if code, body := app.do(alice, "POST", "/g/1/settings/jellyfin", link); code != http.StatusBadRequest ||
		!strings.Contains(body, "rejected that username or password") {
		t.Fatalf("bad password: %d\n%s", code, body)
	}
	link.Set("password", "secret")
	mustContain(t, app.post(alice, "/g/1/settings/jellyfin", link), "Linked to <strong>family</strong>")
	stored, err := app.store.JellyfinLink(context.Background(), 1)
	if err != nil || strings.Contains(string(stored.TokenEnc), "tok-123") {
		t.Fatalf("token not stored encrypted: %v %q", err, stored.TokenEnc)
	}

	_, lib := app.do(alice, "GET", "/g/1/library", nil)
	mustContain(t, lib, "Severance", "Andor")

	// Shows 1 and 2 from Jellyfin, show 3 by title.
	app.post(alice, "/g/1/shows", url.Values{"jellyfin_id": {severanceID}})
	app.post(alice, "/g/1/shows", url.Values{"jellyfin_id": {andorID}})
	app.post(alice, "/g/1/shows", url.Values{"title": {"Quiz Night"}})
	if _, lib := app.do(alice, "GET", "/g/1/library", nil); !strings.Contains(lib, "Nothing to add") {
		t.Errorf("library should be exhausted:\n%s", lib)
	}
	app.post(alice, "/g/1/shows/1/watchers", url.Values{"person": {"1", "2"}})
	app.post(alice, "/g/1/shows/2/watchers", url.Values{"person": {"1"}})
	app.post(alice, "/g/1/shows/3/watchers", url.Values{"person": {"1", "2", "3"}})

	// Ana and Bo are here.
	app.do(alice, "POST", "/g/1/presence/1", url.Values{}, "HX-Request", "true")
	code, frag := app.do(alice, "POST", "/g/1/presence/2", url.Values{}, "HX-Request", "true")
	if code != http.StatusOK || strings.Contains(frag, "<html") {
		t.Fatalf("presence fragment: %d\n%s", code, frag)
	}
	mustContain(t, frag,
		"Everyone's here", "Next: S02E05 · Trojan&#39;s Horse", // Severance: exact
		"Bo doesn't follow it", "All caught up", // Andor: extra person, finished
		"Missing Cy", // Quiz Night: partial
		`src="/g/1/image/`+severanceID+`"`)
	order := []int{strings.Index(frag, "Severance"), strings.Index(frag, "Andor"), strings.Index(frag, "Quiz Night")}
	if !(order[0] >= 0 && order[0] < order[1] && order[1] < order[2]) {
		t.Errorf("wrong ranking order %v:\n%s", order, frag)
	}

	if code, body := app.do(alice, "GET", "/g/1/image/"+severanceID, nil); code != 200 || body != "PNG" {
		t.Errorf("image proxy: %d %q", code, body)
	}
	if code, _ := app.do(alice, "GET", "/g/1/image/..%2f..%2fSystem", nil); code != 404 {
		t.Errorf("image proxy accepted a non-ID: %d", code)
	}

	// Jellyfin going away keeps cached data and shows a notice.
	jf.Close()
	mustContain(t, app.post(alice, "/g/1/sync", nil), "Couldn't refresh from Jellyfin", "Next: S02E05")
}

func TestAccessControlAndInvites(t *testing.T) {
	app := newTestApp(t)
	alice, bob := app.client("alice"), app.client("bob")
	app.post(alice, "/groups", url.Values{"name": {"Family"}})

	for _, path := range []string{"/g/1", "/g/1/people", "/g/1/shows", "/g/1/settings", "/g/999"} {
		if code, _ := app.do(bob, "GET", path, nil); code != http.StatusForbidden {
			t.Errorf("bob GET %s = %d, want 403", path, code)
		}
	}
	if code, _ := app.do(bob, "POST", "/g/1/people", url.Values{"name": {"Mallory"}}); code != http.StatusForbidden {
		t.Errorf("bob could add a person: %d", code)
	}

	body := app.post(alice, "/g/1/settings/invite", nil)
	m := regexp.MustCompile(`http://app\.test(/invite/[\w-]+)`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no invite link in:\n%s", body)
	}
	mustContain(t, app.post(bob, m[1], nil), `aria-current="page">Tonight`, "No people yet") // lands on the planner
	if code, _ := app.do(bob, "GET", "/invite/not-a-token", nil); code != http.StatusNotFound {
		t.Errorf("bogus invite = %d", code)
	}

	// Managers share the group but only the owner can delete it or remove managers.
	if code, _ := app.do(bob, "POST", "/g/1/settings/delete", url.Values{}); code != http.StatusForbidden {
		t.Errorf("manager could delete group: %d", code)
	}
	app.post(alice, "/g/1/settings/managers/2/remove", nil)
	if code, _ := app.do(bob, "GET", "/g/1", nil); code != http.StatusForbidden {
		t.Errorf("removed manager still has access: %d", code)
	}
}

func TestAnonymousAndCrossSite(t *testing.T) {
	app := newTestApp(t)
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Get(app.srv.URL + "/g/1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || loc != "/login?next=%2Fg%2F1" {
		t.Errorf("anonymous: %d -> %q", resp.StatusCode, loc)
	}

	alice := app.client("alice")
	if code, _ := app.do(alice, "POST", "/groups", url.Values{"name": {"X"}}, "Sec-Fetch-Site", "cross-site"); code != http.StatusForbidden {
		t.Errorf("cross-site POST = %d, want 403", code)
	}
}
