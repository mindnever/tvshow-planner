package web

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// "Who's here" is kept in a cookie, not in the database: each browser has its
// own selection, so two people using the app at once don't change each
// other's, and it disappears on logout or after presenceTTL.
const (
	presenceCookie = "here"
	presenceTTL    = 12 * time.Hour
)

// presence maps a group ID to the set of person IDs ticked as present.
type presence map[int64]map[int64]bool

// readPresence parses the cookie, e.g. "1-4.7_3-12": in group 1 people 4 and
// 7 are here, in group 3 person 12. Anything malformed is ignored.
func readPresence(r *http.Request) presence {
	p := presence{}
	c, err := r.Cookie(presenceCookie)
	if err != nil {
		return p
	}
	for _, part := range strings.Split(c.Value, "_") {
		gid, ids, ok := strings.Cut(part, "-")
		g, err := strconv.ParseInt(gid, 10, 64)
		if !ok || err != nil {
			continue
		}
		for _, id := range strings.Split(ids, ".") {
			if pid, err := strconv.ParseInt(id, 10, 64); err == nil {
				if p[g] == nil {
					p[g] = map[int64]bool{}
				}
				p[g][pid] = true
			}
		}
	}
	return p
}

func (p presence) encode() string {
	var parts []string
	for g, set := range p {
		var ids []string
		for pid := range set {
			ids = append(ids, strconv.FormatInt(pid, 10))
		}
		if len(ids) > 0 {
			slices.Sort(ids)
			parts = append(parts, strconv.FormatInt(g, 10)+"-"+strings.Join(ids, "."))
		}
	}
	slices.Sort(parts)
	return strings.Join(parts, "_")
}

func (s *Server) writePresence(w http.ResponseWriter, r *http.Request, p presence) {
	if v := p.encode(); v != "" {
		s.auth.SetCookie(w, r, presenceCookie, v, int(presenceTTL.Seconds()))
	} else {
		s.auth.SetCookie(w, r, presenceCookie, "", -1)
	}
}

// logout also forgets who was ticked as present in this browser.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.auth.SetCookie(w, r, presenceCookie, "", -1)
	s.auth.Logout(w, r)
}
