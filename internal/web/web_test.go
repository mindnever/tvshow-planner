package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"tvshow-planner/internal/auth"
	"tvshow-planner/internal/db"
	"tvshow-planner/internal/secretbox"
)

const (
	severanceID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"
	andorID     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa2"
)

// fakeJellyfin implements just the endpoints the client uses. Any username
// logs in with the password "secret" and gets the token "tok-<username>".
type fakeJellyfin struct {
	*httptest.Server
	mu          sync.Mutex
	qcDisabled  bool
	qcApproved  bool
	qcForgotten bool
	itemUsers   map[string]bool // userId values seen on /Items
}

func (f *fakeJellyfin) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func newFakeJellyfin(t *testing.T) *fakeJellyfin {
	type m = map[string]any
	f := &fakeJellyfin{itemUsers: map[string]bool{}}
	series := []m{
		{"Id": severanceID, "Name": "Severance", "ProductionYear": 2022, "ImageTags": m{"Primary": "x"},
			"UserData": m{"UnplayedItemCount": 3, "LastPlayedDate": "2026-11-01T20:00:00.0000000Z"}},
		{"Id": andorID, "Name": "Andor", "ProductionYear": 2022,
			"UserData": m{"UnplayedItemCount": 0}},
	}
	// tokenUser returns the user a request's token belongs to.
	tokenUser := func(r *http.Request) string {
		if mt := regexp.MustCompile(`Token="tok-(\w+)"`).FindStringSubmatch(r.Header.Get("Authorization")); mt != nil {
			return mt[1]
		}
		return ""
	}
	session := func(w http.ResponseWriter, user string) {
		json.NewEncoder(w).Encode(m{"AccessToken": "tok-" + user, "User": m{"Id": "uid-" + user, "Name": user}})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /Users/AuthenticateByName", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Username, Pw string }
		json.NewDecoder(r.Body).Decode(&in)
		if in.Username == "" || in.Pw != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		session(w, in.Username)
	})
	mux.HandleFunc("POST /QuickConnect/Initiate", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.qcDisabled {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(m{"Secret": "qc-secret", "Code": "123456", "Authenticated": false})
	})
	mux.HandleFunc("GET /QuickConnect/Connect", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.qcForgotten || r.URL.Query().Get("secret") != "qc-secret" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(m{"Authenticated": f.qcApproved})
	})
	mux.HandleFunc("POST /Users/AuthenticateWithQuickConnect", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Secret string }
		json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		if in.Secret != "qc-secret" || !f.qcApproved {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		session(w, "carol")
	})
	mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		user := tokenUser(r)
		if user == "" || r.URL.Query().Get("userId") != "uid-"+user {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.set(func() { f.itemUsers[user] = true })
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
		if tokenUser(r) == "" || r.URL.Query().Get("seriesId") != severanceID {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(m{"Items": []m{{"Name": "Trojan's Horse", "ParentIndexNumber": 2, "IndexNumber": 5}}})
	})
	mux.HandleFunc("GET /Items/{id}/Images/Primary", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("PNG"))
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

type testApp struct {
	t   *testing.T
	srv *httptest.Server
	jf  *fakeJellyfin
}

func newTestApp(t *testing.T) *testApp {
	store, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	jf := newFakeJellyfin(t)
	box, _ := secretbox.New("test-secret-key-0123456789")
	srv := httptest.NewServer(New(store, auth.New(store), box, "http://app.test", jf.URL).Handler())
	t.Cleanup(srv.Close)
	return &testApp{t: t, srv: srv, jf: jf}
}

func newClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

// client returns an HTTP client logged in as the named Jellyfin user.
func (a *testApp) client(name string) *http.Client {
	a.t.Helper()
	c := newClient()
	if body := a.post(c, "/login", url.Values{"username": {name}, "password": {"secret"}}); !strings.Contains(body, `class="who">`+name+`<`) {
		a.t.Fatalf("login as %s did not land on a logged-in page:\n%s", name, body)
	}
	return c
}

func (a *testApp) doH(c *http.Client, method, path string, form url.Values, hdr ...string) (int, string, http.Header) {
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
	return resp.StatusCode, string(b), resp.Header
}

func (a *testApp) do(c *http.Client, method, path string, form url.Values, hdr ...string) (int, string) {
	a.t.Helper()
	code, body, _ := a.doH(c, method, path, form, hdr...)
	return code, body
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

func TestPasswordLogin(t *testing.T) {
	app := newTestApp(t)
	c := newClient()

	// A wrong password is reported next to the button and marks both fields.
	code, body := app.do(c, "POST", "/login", url.Values{"username": {"alice"}, "password": {"nope"}, "next": {"/g/7"}})
	if code != http.StatusUnauthorized {
		t.Fatalf("bad password = %d", code)
	}
	mustContain(t, body, "Jellyfin rejected that username or password", `id="login-error"`,
		`name="username" value="alice" autocomplete="username" autocapitalize="none" required autofocus aria-invalid="true"`,
		`name="password" type="password" autocomplete="current-password" aria-invalid="true"`,
		`name="next" value="/g/7"`)
	if code, _ := app.do(c, "GET", "/", nil); code != http.StatusOK { // still anonymous: lands on the login page
		t.Fatalf("GET / = %d", code)
	}

	// Jellyfin being down is not blamed on the fields.
	app.jf.Close()
	code, body = app.do(c, "POST", "/login", url.Values{"username": {"alice"}, "password": {"secret"}})
	if code != http.StatusBadGateway || strings.Contains(body, "aria-invalid") || strings.Contains(body, app.jf.URL) {
		t.Fatalf("jellyfin down: %d\n%s", code, body)
	}
	mustContain(t, body, "reach the Jellyfin server")
}

func TestLoginRedirectsToNext(t *testing.T) {
	app := newTestApp(t)
	app.post(app.client("alice"), "/groups", url.Values{"name": {"Family"}})
	c := newClient()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for next, want := range map[string]string{"/g/1/shows": "/g/1/shows", "//evil.example": "/", "https://evil.example": "/"} {
		_, _, h := app.doH(c, "POST", "/login", url.Values{"username": {"alice"}, "password": {"secret"}, "next": {next}})
		if got := h.Get("Location"); got != want {
			t.Errorf("next=%q redirected to %q, want %q", next, got, want)
		}
	}
}

func TestQuickConnect(t *testing.T) {
	app := newTestApp(t)
	c := newClient()

	mustContain(t, app.post(c, "/login/quickconnect", url.Values{"next": {"/"}}), "123456", `hx-get="/login/quickconnect"`)

	// Not approved yet: the poll changes nothing.
	if code, _, h := app.doH(c, "GET", "/login/quickconnect", nil, "HX-Request", "true"); code != http.StatusNoContent || h.Get("HX-Redirect") != "" {
		t.Fatalf("pending poll = %d, HX-Redirect %q", code, h.Get("HX-Redirect"))
	}
	if code, body := app.do(c, "GET", "/", nil); code != 200 || !strings.Contains(body, "Log in with Jellyfin") {
		t.Fatalf("logged in before approval:\n%s", body)
	}

	// Approved in Jellyfin: the next poll logs in and sends the browser on.
	app.jf.set(func() { app.jf.qcApproved = true })
	if _, _, h := app.doH(c, "GET", "/login/quickconnect", nil, "HX-Request", "true"); h.Get("HX-Redirect") != "/" {
		t.Fatalf("approved poll: HX-Redirect %q", h.Get("HX-Redirect"))
	}
	_, body := app.do(c, "GET", "/", nil)
	mustContain(t, body, `class="who">carol<`, "Your groups")
}

func TestQuickConnectFailures(t *testing.T) {
	app := newTestApp(t)

	// A code Jellyfin no longer knows sends the user back to start again.
	c := newClient()
	app.post(c, "/login/quickconnect", nil)
	app.jf.set(func() { app.jf.qcForgotten = true })
	if _, _, h := app.doH(c, "GET", "/login/quickconnect", nil, "HX-Request", "true"); h.Get("HX-Redirect") != "/login?quickconnect=expired" {
		t.Fatalf("expired poll: HX-Redirect %q", h.Get("HX-Redirect"))
	}
	_, body := app.do(c, "GET", "/login?quickconnect=expired", nil)
	mustContain(t, body, "code expired")
	// Without a pending request the Quick Connect page goes back to login.
	_, body = app.do(newClient(), "GET", "/login/quickconnect", nil)
	mustContain(t, body, "Log in with Jellyfin")

	app.jf.set(func() { app.jf.qcDisabled = true })
	code, body := app.do(newClient(), "POST", "/login/quickconnect", url.Values{})
	if code != http.StatusBadRequest {
		t.Fatalf("disabled quick connect = %d", code)
	}
	mustContain(t, body, "Quick Connect is turned off")
}

func TestEndToEnd(t *testing.T) {
	app := newTestApp(t)
	alice := app.client("alice")

	app.post(alice, "/groups", url.Values{"name": {"Family"}})
	for _, n := range []string{"Ana", "Bo", "Cy"} { // person IDs 1, 2, 3
		app.post(alice, "/g/1/people", url.Values{"name": {n}})
	}

	_, lib := app.do(alice, "GET", "/g/1/library", nil)
	mustContain(t, lib, "Severance", "Andor")

	// Shows 1 and 2 from Jellyfin, show 3 by title.
	app.post(alice, "/g/1/shows", url.Values{"jellyfin_id": {severanceID}})
	app.post(alice, "/g/1/shows", url.Values{"jellyfin_id": {andorID}})
	app.post(alice, "/g/1/shows", url.Values{"title": {"Quiz Night"}})
	// The Shows page lists the most recently added show first.
	_, page := app.do(alice, "GET", "/g/1/shows", nil)
	if q, a, s := strings.Index(page, "Quiz Night"), strings.Index(page, "Andor"), strings.Index(page, "Severance"); !(q >= 0 && q < a && a < s) {
		t.Errorf("shows not newest-first: Quiz Night@%d Andor@%d Severance@%d", q, a, s)
	}
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

	// Another manager sees the group through the owner's Jellyfin account.
	bob := app.client("bob")
	checkPresence := func() {
		// Who's here is per browser: neither bob nor a second alice session
		// sees alice's ticks, and their own don't change hers.
		pressed := func(c *http.Client) int {
			_, body := app.do(c, "GET", "/g/1", nil)
			return strings.Count(body, `aria-pressed="true"`)
		}
		alice2 := app.client("alice")
		if a, b, a2 := pressed(alice), pressed(bob), pressed(alice2); a != 2 || b != 0 || a2 != 0 {
			t.Errorf("present ticks: alice=%d bob=%d alice2=%d, want 2, 0, 0", a, b, a2)
		}
		app.post(bob, "/g/1/presence/3", nil)
		app.post(alice2, "/g/1/presence/clear", nil)
		if a, b := pressed(alice), pressed(bob); a != 2 || b != 1 {
			t.Errorf("after bob ticks Cy: alice=%d bob=%d, want 2, 1", a, b)
		}
		// Logging out forgets the selection in that browser.
		app.post(bob, "/logout", nil)
		app.post(bob, "/login", url.Values{"username": {"bob"}, "password": {"secret"}})
		if b := pressed(bob); b != 0 {
			t.Errorf("bob's ticks survived logout: %d", b)
		}
	}
	m := regexp.MustCompile(`http://app\.test(/invite/[\w-]+)`).FindStringSubmatch(app.post(alice, "/g/1/settings/invite", nil))
	if m == nil {
		t.Fatal("no invite link")
	}
	app.post(bob, m[1], nil)
	_, bobShows := app.do(bob, "GET", "/g/1/shows", nil)
	mustContain(t, bobShows, "Severance", "Quiz Night")
	app.post(bob, "/g/1/sync", nil)
	_, settings := app.do(bob, "GET", "/g/1/settings", nil)
	mustContain(t, settings, "<strong>alice</strong>'s Jellyfin account")
	app.jf.set(func() {
		if !app.jf.itemUsers["alice"] || app.jf.itemUsers["bob"] {
			t.Errorf("group should read Jellyfin only as its owner, got %v", app.jf.itemUsers)
		}
	})

	checkPresence()

	// Jellyfin going away keeps cached data and shows a notice.
	app.jf.Close()
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
	// The owner's settings page offers to remove bob (never the owner); bob's own page offers nothing.
	const removeBob, removeAlice = `action="/g/1/settings/managers/2/remove"`, `action="/g/1/settings/managers/1/remove"`
	_, ownerPage := app.do(alice, "GET", "/g/1/settings", nil)
	mustContain(t, ownerPage, removeBob)
	_, managerPage := app.do(bob, "GET", "/g/1/settings", nil)
	if strings.Contains(ownerPage, removeAlice) || strings.Contains(managerPage, "/remove") {
		t.Errorf("remove buttons shown where they shouldn't be")
	}
	if code, _ := app.do(bob, "POST", "/g/1/settings/managers/1/remove", url.Values{}); code != http.StatusForbidden {
		t.Errorf("manager could remove the owner: %d", code)
	}
	if body := app.post(alice, "/g/1/settings/managers/2/remove", nil); strings.Contains(body, ">bob<") {
		t.Errorf("bob still listed after removal")
	}
	if code, _ := app.do(bob, "GET", "/g/1", nil); code != http.StatusForbidden {
		t.Errorf("removed manager still has access: %d", code)
	}

	// Logging out ends the session.
	mustContain(t, app.post(alice, "/logout", nil), "Log in with Jellyfin")
	if _, body := app.do(alice, "GET", "/g/1", nil); !strings.Contains(body, "Log in with Jellyfin") {
		t.Errorf("still logged in after logout")
	}
}

// Cookies must be usable over plain HTTP, and Secure behind a TLS proxy.
func TestCookieSecureFollowsRequestScheme(t *testing.T) {
	app := newTestApp(t)
	c := newClient()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	form := url.Values{"username": {"alice"}, "password": {"secret"}}

	_, _, h := app.doH(c, "POST", "/login", form)
	if ck := h.Get("Set-Cookie"); !strings.HasPrefix(ck, "session=") || strings.Contains(ck, "Secure") {
		t.Errorf("plain HTTP login cookie = %q, want a non-Secure session cookie", ck)
	}
	_, _, h = app.doH(c, "POST", "/login", form, "X-Forwarded-Proto", "https")
	if ck := h.Get("Set-Cookie"); !strings.Contains(ck, "Secure") {
		t.Errorf("HTTPS-proxied login cookie = %q, want Secure", ck)
	}
	_, _, h = app.doH(c, "POST", "/login/quickconnect", url.Values{})
	if ck := h.Get("Set-Cookie"); !strings.HasPrefix(ck, "quickconnect=") || strings.Contains(ck, "Secure") {
		t.Errorf("plain HTTP quick connect cookie = %q", ck)
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
