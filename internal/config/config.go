// Package config reads configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"tvshow-planner/internal/jellyfin"
)

type Config struct {
	ListenAddr   string
	BaseURL      string // public URL of this app, no trailing slash
	DatabasePath string
	SecretKey    string
	JellyfinURL  string // the Jellyfin server users log in with
	// Optional: Jellyseerr, for shows that aren't in Jellyfin yet. Both are
	// set or both are empty.
	JellyseerrURL    string
	JellyseerrAPIKey string
}

func Load() (Config, error) {
	c := Config{
		ListenAddr:   envOr("LISTEN_ADDR", ":8080"),
		BaseURL:      strings.TrimRight(envOr("PUBLIC_BASE_URL", "http://localhost:8080"), "/"),
		DatabasePath: envOr("DATABASE_PATH", "tvshow-planner.db"),
		SecretKey:    os.Getenv("SECRET_KEY"),
	}
	var missing []string
	if len(c.SecretKey) < 16 {
		missing = append(missing, "SECRET_KEY (at least 16 characters)")
	}
	if os.Getenv("JELLYFIN_URL") == "" {
		missing = append(missing, "JELLYFIN_URL")
	}
	if len(missing) > 0 {
		return c, errors.New("missing configuration: " + strings.Join(missing, ", "))
	}
	var err error
	if c.JellyfinURL, err = jellyfin.NormalizeURL(os.Getenv("JELLYFIN_URL")); err != nil {
		return c, fmt.Errorf("JELLYFIN_URL: %w", err)
	}
	c.JellyseerrAPIKey = strings.TrimSpace(os.Getenv("JELLYSEERR_API_KEY"))
	if raw := os.Getenv("JELLYSEERR_URL"); raw != "" || c.JellyseerrAPIKey != "" {
		if raw == "" || c.JellyseerrAPIKey == "" {
			return c, errors.New("JELLYSEERR_URL and JELLYSEERR_API_KEY must be set together")
		}
		if c.JellyseerrURL, err = jellyfin.NormalizeURL(raw); err != nil {
			return c, fmt.Errorf("JELLYSEERR_URL: %w", err)
		}
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
