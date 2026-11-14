// Package auth manages server-side sessions. Who a user is gets decided by
// Jellyfin (see the login handlers in internal/web); this package only
// remembers it.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"tvshow-planner/internal/db"
)

const (
	sessionCookie = "session"
	sessionTTL    = 30 * 24 * time.Hour
)

type Auth struct {
	store  *db.Store
	secure bool // set the Secure flag on cookies
}

func New(store *db.Store, secureCookies bool) *Auth {
	return &Auth{store: store, secure: secureCookies}
}

// RandomToken returns 32 random bytes, URL-safe encoded.
func RandomToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// SafeNext only allows redirects to local paths.
func SafeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, "\\") {
		return "/"
	}
	return next
}

// SetCookie sets an HttpOnly cookie; maxAge < 0 deletes it.
func (a *Auth) SetCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
}

// StartSession creates a session for userID and sets its cookie.
func (a *Auth) StartSession(ctx context.Context, w http.ResponseWriter, userID int64) error {
	token := RandomToken()
	if err := a.store.CreateSession(ctx, hashToken(token), userID, time.Now().Add(sessionTTL)); err != nil {
		return err
	}
	a.SetCookie(w, sessionCookie, token, int(sessionTTL.Seconds()))
	return nil
}

// Logout ends the session in this app. The Jellyfin token is kept, because
// groups the user owns keep reading Jellyfin with it.
func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = a.store.DeleteSession(r.Context(), hashToken(c.Value))
	}
	a.SetCookie(w, sessionCookie, "", -1)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

type ctxKey struct{}

// User returns the logged-in user, or the zero User if there is none.
func User(ctx context.Context) db.User {
	u, _ := ctx.Value(ctxKey{}).(db.User)
	return u
}

// RequireUser sends anonymous visitors to the login page.
func (a *Auth) RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookie); err == nil {
			u, err := a.store.SessionUser(r.Context(), hashToken(c.Value))
			if err == nil {
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
				return
			}
			if !errors.Is(err, db.ErrNotFound) {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
		}
		if r.Header.Get("HX-Request") != "" {
			// htmx follows HX-Redirect with a full page navigation.
			w.Header().Set("HX-Redirect", "/login")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		target := "/login"
		if r.Method == http.MethodGet {
			target += "?next=" + url.QueryEscape(r.URL.RequestURI())
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	})
}
