package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"tvshow-planner/internal/auth"
	"tvshow-planner/internal/config"
	"tvshow-planner/internal/db"
	"tvshow-planner/internal/secretbox"
	"tvshow-planner/internal/seerr"
	"tvshow-planner/internal/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	store, err := db.Open(cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer store.Close()
	box, err := secretbox.New(cfg.SecretKey)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a := auth.New(store)
	var sc *seerr.Client // nil when Jellyseerr isn't configured
	if cfg.JellyseerrURL != "" {
		sc = &seerr.Client{BaseURL: cfg.JellyseerrURL, APIKey: cfg.JellyseerrAPIKey, HTTP: &http.Client{Timeout: 15 * time.Second}}
	}
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           web.New(store, a, box, cfg.BaseURL, cfg.JellyfinURL, sc).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()
	slog.Info("listening", "addr", cfg.ListenAddr, "base_url", cfg.BaseURL, "jellyfin", cfg.JellyfinURL, "jellyseerr", cfg.JellyseerrURL)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
