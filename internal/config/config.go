// Package config reads configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

type Config struct {
	ListenAddr       string
	BaseURL          string // public URL, no trailing slash
	DatabasePath     string
	SecretKey        string
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	// DevAuthUser, when set, replaces OIDC with an auto-login as that user.
	// For local development only.
	DevAuthUser string
}

func Load() (Config, error) {
	c := Config{
		ListenAddr:       envOr("LISTEN_ADDR", ":8080"),
		BaseURL:          strings.TrimRight(envOr("PUBLIC_BASE_URL", "http://localhost:8080"), "/"),
		DatabasePath:     envOr("DATABASE_PATH", "tvshow-planner.db"),
		SecretKey:        os.Getenv("SECRET_KEY"),
		OIDCIssuer:       os.Getenv("OIDC_ISSUER"),
		OIDCClientID:     os.Getenv("OIDC_CLIENT_ID"),
		OIDCClientSecret: os.Getenv("OIDC_CLIENT_SECRET"),
		DevAuthUser:      os.Getenv("DEV_AUTH_USER"),
	}
	var missing []string
	if len(c.SecretKey) < 16 {
		missing = append(missing, "SECRET_KEY (at least 16 characters)")
	}
	if c.DevAuthUser == "" {
		if c.OIDCIssuer == "" {
			missing = append(missing, "OIDC_ISSUER")
		}
		if c.OIDCClientID == "" {
			missing = append(missing, "OIDC_CLIENT_ID")
		}
	}
	if len(missing) > 0 {
		return c, errors.New("missing configuration: " + strings.Join(missing, ", "))
	}
	if u, err := url.Parse(c.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return c, fmt.Errorf("PUBLIC_BASE_URL %q is not an http(s) URL", c.BaseURL)
	}
	return c, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
