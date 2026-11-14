// Package web serves the HTML UI.
package web

import (
	"bytes"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"tvshow-planner/internal/auth"
	"tvshow-planner/internal/db"
	"tvshow-planner/internal/secretbox"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

type Server struct {
	store       *db.Store
	auth        *auth.Auth
	box         *secretbox.Box
	baseURL     string
	jellyfinURL string       // the one Jellyfin server users log in with
	hc          *http.Client // for Jellyfin
	pages       map[string]*template.Template
}

func New(store *db.Store, a *auth.Auth, box *secretbox.Box, baseURL, jellyfinURL string) *Server {
	s := &Server{
		store: store, auth: a, box: box, baseURL: baseURL, jellyfinURL: jellyfinURL,
		hc:    &http.Client{Timeout: 15 * time.Second},
		pages: map[string]*template.Template{},
	}
	for _, name := range []string{"home", "planner", "shows", "people", "settings", "invite", "message", "login", "quickconnect"} {
		s.pages[name] = template.Must(template.New("").Funcs(template.FuncMap{"join": joinNames}).ParseFS(templateFS,
			"templates/base.html", "templates/partials.html", "templates/"+name+".html"))
	}
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })

	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /login/quickconnect", s.startQuickConnect)
	mux.HandleFunc("GET /login/quickconnect", s.quickConnect)
	mux.HandleFunc("POST /logout", s.logout)

	user := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.auth.RequireUser(h)) }
	group := func(pattern string, h groupHandler) { mux.Handle(pattern, s.auth.RequireUser(s.withGroup(h))) }

	user("GET /{$}", s.home)
	user("POST /groups", s.createGroup)
	user("GET /invite/{token}", s.showInvite)
	user("POST /invite/{token}", s.acceptInvite)

	group("GET /g/{gid}", s.planner)
	group("POST /g/{gid}/presence/clear", s.clearPresence)
	group("POST /g/{gid}/presence/{pid}", s.togglePresence)
	group("POST /g/{gid}/sync", s.syncNow)

	group("GET /g/{gid}/people", s.people)
	group("POST /g/{gid}/people", s.createPerson)
	group("POST /g/{gid}/people/{pid}", s.renamePerson)
	group("POST /g/{gid}/people/{pid}/delete", s.deletePerson)

	group("GET /g/{gid}/shows", s.shows)
	group("POST /g/{gid}/shows", s.createShow)
	group("POST /g/{gid}/shows/{sid}/watchers", s.setWatchers)
	group("POST /g/{gid}/shows/{sid}/archive", s.archiveShow)
	group("POST /g/{gid}/shows/{sid}/delete", s.deleteShow)
	group("GET /g/{gid}/library", s.library)
	group("GET /g/{gid}/image/{item}", s.image)

	group("GET /g/{gid}/settings", s.settings)
	group("POST /g/{gid}/settings/name", s.renameGroup)
	group("POST /g/{gid}/settings/invite", s.createInvite)
	group("POST /g/{gid}/settings/managers/{uid}/remove", s.removeManager)
	group("POST /g/{gid}/settings/delete", s.deleteGroup)

	// Rejects cross-site state-changing requests (CSRF) based on the
	// browser's Sec-Fetch-Site / Origin headers.
	return http.NewCrossOriginProtection().Handler(securityHeaders(mux))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// pageData is what every template receives.
type pageData struct {
	User  db.User
	Group *db.Group // nil outside a group
	Tab   string
	Error string
	D     any
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, page string, data pageData) {
	s.exec(w, r, status, page, "base", data)
}

// exec renders into a buffer first so a template error never produces a
// half-written page.
func (s *Server) exec(w http.ResponseWriter, r *http.Request, status int, page, tmpl string, data pageData) {
	data.User = auth.User(r.Context())
	var buf bytes.Buffer
	if err := s.pages[page].ExecuteTemplate(&buf, tmpl, data); err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	http.Error(w, "Something went wrong.", http.StatusInternalServerError)
}

type groupHandler func(w http.ResponseWriter, r *http.Request, g db.Group)

// withGroup loads the group from the path and enforces that the logged-in
// user manages it. Unknown groups and other people's groups look the same.
func (s *Server) withGroup(h groupHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g, err := s.store.GroupForUser(r.Context(), pathID(r, "gid"), auth.User(r.Context()).ID)
		if errors.Is(err, db.ErrNotFound) {
			s.render(w, r, http.StatusForbidden, "message", pageData{D: "You don't have access to this group."})
			return
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		h(w, r, g)
	})
}

func pathID(r *http.Request, name string) int64 {
	id, _ := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id
}

// cleanName trims a user-supplied name and caps its length.
func cleanName(v string) string {
	v = strings.Join(strings.Fields(v), " ")
	for utf8.RuneCountInString(v) > 100 {
		_, size := utf8.DecodeLastRuneInString(v)
		v = v[:len(v)-size]
	}
	return v
}

func groupPath(g db.Group, sub string) string {
	return "/g/" + strconv.FormatInt(g.ID, 10) + sub
}

func redirect(w http.ResponseWriter, r *http.Request, to string) {
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// joinNames renders a list of names as "Ana", "Ana and Bo", "Ana, Bo and Cy".
func joinNames(names []string) string {
	if len(names) <= 1 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
