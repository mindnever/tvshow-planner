// Package auth handles OIDC login and server-side sessions.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"tvshow-planner/internal/config"
	"tvshow-planner/internal/db"
)

const (
	sessionCookie = "session"
	flowCookie    = "oidc_flow"
	sessionTTL    = 30 * 24 * time.Hour
)

type Auth struct {
	store    *db.Store
	secure   bool // set the Secure flag on cookies
	devUser  string
	issuer   string
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth    oauth2.Config
}

func New(ctx context.Context, cfg config.Config, store *db.Store) (*Auth, error) {
	a := &Auth{store: store, secure: strings.HasPrefix(cfg.BaseURL, "https://"), devUser: cfg.DevAuthUser}
	if a.devUser != "" {
		slog.Warn("DEV_AUTH_USER is set: OIDC is disabled and everyone is logged in as this user", "user", a.devUser)
		return a, nil
	}
	provider, err := oidc.NewProvider(ctx, cfg.OIDCIssuer)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery for %s: %w", cfg.OIDCIssuer, err)
	}
	a.issuer = cfg.OIDCIssuer
	a.provider = provider
	a.verifier = provider.Verifier(&oidc.Config{ClientID: cfg.OIDCClientID})
	a.oauth = oauth2.Config{
		ClientID:     cfg.OIDCClientID,
		ClientSecret: cfg.OIDCClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  cfg.BaseURL + "/auth/callback",
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}
	return a, nil
}

func randomToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// safeNext only allows redirects to local paths.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, "\\") {
		return "/"
	}
	return next
}

func (a *Auth) setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
}

// StartSession creates a session for userID and sets its cookie.
func (a *Auth) StartSession(ctx context.Context, w http.ResponseWriter, userID int64) error {
	token := randomToken()
	if err := a.store.CreateSession(ctx, hashToken(token), userID, time.Now().Add(sessionTTL)); err != nil {
		return err
	}
	a.setCookie(w, sessionCookie, token, int(sessionTTL.Seconds()))
	return nil
}

// Login starts the OIDC authorization-code flow (with PKCE).
func (a *Auth) Login(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.URL.Query().Get("next"))
	if a.devUser != "" {
		u, err := a.store.UpsertUser(r.Context(), "dev", a.devUser, a.devUser, "")
		if err == nil {
			err = a.StartSession(r.Context(), w, u.ID)
		}
		if err != nil {
			http.Error(w, "login failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	state, nonce, verifier := randomToken(), randomToken(), oauth2.GenerateVerifier()
	flow := strings.Join([]string{state, nonce, verifier, base64.RawURLEncoding.EncodeToString([]byte(next))}, ".")
	a.setCookie(w, flowCookie, flow, 600)
	http.Redirect(w, r, a.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusSeeOther)
}

// Callback completes the OIDC flow and starts a session.
func (a *Auth) Callback(w http.ResponseWriter, r *http.Request) {
	if a.devUser != "" {
		http.NotFound(w, r)
		return
	}
	next, err := a.finishLogin(w, r)
	if err != nil {
		slog.Warn("OIDC login failed", "err", err)
		http.Error(w, "Login failed. Go back to the start page and try again.", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (a *Auth) finishLogin(w http.ResponseWriter, r *http.Request) (string, error) {
	ctx := r.Context()
	c, err := r.Cookie(flowCookie)
	if err != nil {
		return "", errors.New("missing login flow cookie")
	}
	a.setCookie(w, flowCookie, "", -1)
	parts := strings.Split(c.Value, ".")
	if len(parts) != 4 {
		return "", errors.New("malformed login flow cookie")
	}
	state, nonce, verifier := parts[0], parts[1], parts[2]
	nextRaw, _ := base64.RawURLEncoding.DecodeString(parts[3])

	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		return "", fmt.Errorf("provider returned %s: %s", e, q.Get("error_description"))
	}
	if q.Get("state") != state {
		return "", errors.New("state mismatch")
	}
	tok, err := a.oauth.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		return "", fmt.Errorf("code exchange: %w", err)
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		return "", errors.New("no id_token in token response")
	}
	idTok, err := a.verifier.Verify(ctx, rawID)
	if err != nil {
		return "", fmt.Errorf("id_token: %w", err)
	}
	if idTok.Nonce != nonce {
		return "", errors.New("nonce mismatch")
	}

	type profile struct {
		Name     string `json:"name"`
		Username string `json:"preferred_username"`
		Email    string `json:"email"`
	}
	var p profile
	_ = idTok.Claims(&p)
	// Some providers (Authelia among them) only return profile claims from
	// the userinfo endpoint, so ask there when the ID token has none.
	if p.Name == "" && p.Username == "" && p.Email == "" {
		if ui, err := a.provider.UserInfo(ctx, oauth2.StaticTokenSource(tok)); err == nil && ui.Subject == idTok.Subject {
			_ = ui.Claims(&p)
		}
	}
	name := p.Name
	for _, alt := range []string{p.Username, p.Email, idTok.Subject} {
		if name == "" {
			name = alt
		}
	}
	u, err := a.store.UpsertUser(ctx, a.issuer, idTok.Subject, name, p.Email)
	if err != nil {
		return "", err
	}
	if err := a.StartSession(ctx, w, u.ID); err != nil {
		return "", err
	}
	return safeNext(string(nextRaw)), nil
}

// Logout ends the local session. It does not sign the user out of the provider.
func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = a.store.DeleteSession(r.Context(), hashToken(c.Value))
	}
	a.setCookie(w, sessionCookie, "", -1)
	http.Redirect(w, r, "/logged-out", http.StatusSeeOther)
}

type ctxKey struct{}

// User returns the logged-in user; only valid behind RequireUser.
func User(ctx context.Context) db.User {
	u, _ := ctx.Value(ctxKey{}).(db.User)
	return u
}

// RequireUser sends anonymous visitors to the login flow.
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
		target := "/login"
		if r.Method == http.MethodGet {
			target += "?next=" + url.QueryEscape(r.URL.RequestURI())
		}
		if r.Header.Get("HX-Request") != "" {
			// htmx follows HX-Redirect with a full page navigation.
			w.Header().Set("HX-Redirect", "/login")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	})
}
