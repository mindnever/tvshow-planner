package web

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"tvshow-planner/internal/auth"
	"tvshow-planner/internal/db"
	"tvshow-planner/internal/jellyfin"
)

// quickConnectCookie carries a pending Quick Connect login between requests.
const quickConnectCookie = "quickconnect"

type loginView struct {
	Next     string
	Username string
	// A failed attempt: the message goes next to the button that was used.
	LoginError string
	BadLogin   bool // the username/password fields are at fault
	QuickError string
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	v := loginView{Next: auth.SafeNext(r.URL.Query().Get("next"))}
	if r.URL.Query().Get("quickconnect") == "expired" {
		v.QuickError = "That Quick Connect code expired or was rejected. Start again to get a new one."
	}
	s.render(w, r, http.StatusOK, "login", pageData{D: v})
}

// login signs in with a Jellyfin username and password.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	v := loginView{Next: auth.SafeNext(r.FormValue("next")), Username: strings.TrimSpace(r.FormValue("username"))}
	// A stable device ID per account makes Jellyfin reuse one device entry
	// instead of adding a new one on every login.
	sum := sha256.Sum256([]byte("tvshow-planner|" + strings.ToLower(v.Username)))
	deviceID := hex.EncodeToString(sum[:16])

	c, name, err := jellyfin.Authenticate(r.Context(), s.hc, s.jellyfinURL, v.Username, r.FormValue("password"), deviceID)
	if errors.Is(err, jellyfin.ErrBadCredentials) {
		v.LoginError, v.BadLogin = "Jellyfin rejected that username or password.", true
		s.render(w, r, http.StatusUnauthorized, "login", pageData{D: v})
		return
	}
	if err != nil {
		slog.Error("jellyfin login failed", "err", err)
		v.LoginError = "Couldn't reach the Jellyfin server. Try again in a moment."
		s.render(w, r, http.StatusBadGateway, "login", pageData{D: v})
		return
	}
	if err := s.completeLogin(w, r, c, name); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, v.Next)
}

// completeLogin records the Jellyfin account as a user and starts a session.
func (s *Server) completeLogin(w http.ResponseWriter, r *http.Request, c *jellyfin.Client, name string) error {
	u, err := s.store.UpsertUser(r.Context(), db.Credentials{
		Name: name, JellyfinUserID: c.UserID, TokenEnc: s.box.Seal(c.Token), DeviceID: c.DeviceID,
	})
	if err != nil {
		return err
	}
	return s.auth.StartSession(r.Context(), w, u.ID)
}

// startQuickConnect asks Jellyfin for a code and shows it to the user.
func (s *Server) startQuickConnect(w http.ResponseWriter, r *http.Request) {
	v := loginView{Next: auth.SafeNext(r.FormValue("next"))}
	deviceID := auth.RandomToken()
	qc, err := jellyfin.StartQuickConnect(r.Context(), s.hc, s.jellyfinURL, deviceID)
	if err != nil {
		v.QuickError = "Couldn't reach the Jellyfin server. Try again in a moment."
		status := http.StatusBadGateway
		if errors.Is(err, jellyfin.ErrQuickConnectUnavailable) {
			v.QuickError, status = "Quick Connect is turned off on this Jellyfin server. Log in with your password instead.", http.StatusBadRequest
		} else {
			slog.Error("quick connect failed to start", "err", err)
		}
		s.render(w, r, status, "login", pageData{D: v})
		return
	}
	state := url.Values{"secret": {qc.Secret}, "code": {qc.Code}, "device": {deviceID}, "next": {v.Next}}
	s.auth.SetCookie(w, quickConnectCookie, base64.RawURLEncoding.EncodeToString([]byte(state.Encode())), 600)
	redirect(w, r, "/login/quickconnect")
}

// quickConnect shows the pending code and, each time it is requested, asks
// Jellyfin whether the user has approved it yet. The page polls it with htmx;
// without JavaScript, reloading the page does the same.
func (s *Server) quickConnect(w http.ResponseWriter, r *http.Request) {
	htmx := r.Header.Get("HX-Request") != ""
	leave := func(to string) {
		s.auth.SetCookie(w, quickConnectCookie, "", -1)
		if htmx {
			w.Header().Set("HX-Redirect", to)
			return
		}
		redirect(w, r, to)
	}

	var state url.Values
	if c, err := r.Cookie(quickConnectCookie); err == nil {
		if raw, err := base64.RawURLEncoding.DecodeString(c.Value); err == nil {
			state, _ = url.ParseQuery(string(raw))
		}
	}
	secret, device := state.Get("secret"), state.Get("device")
	if secret == "" || device == "" {
		leave("/login")
		return
	}

	approved, err := jellyfin.QuickConnectApproved(r.Context(), s.hc, s.jellyfinURL, device, secret)
	if err != nil {
		// Jellyfin forgets a code after a few minutes.
		slog.Warn("quick connect check failed", "err", err)
		leave("/login?quickconnect=expired")
		return
	}
	if !approved {
		if htmx {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		s.render(w, r, http.StatusOK, "quickconnect", pageData{D: state.Get("code")})
		return
	}
	c, name, err := jellyfin.AuthenticateQuickConnect(r.Context(), s.hc, s.jellyfinURL, device, secret)
	if err == nil {
		err = s.completeLogin(w, r, c, name)
	}
	if err != nil {
		slog.Error("quick connect login failed", "err", err)
		leave("/login?quickconnect=expired")
		return
	}
	leave(auth.SafeNext(state.Get("next")))
}
