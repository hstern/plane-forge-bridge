// Command plane-forge-bridge runs the webhook bridge.
//
// It loads YAML config (path from --config or PFB_CONFIG), starts the HTTP
// server on the configured address, and serves /forge/webhook,
// /plane/webhook, and /healthz until interrupted.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hstern/plane-forge-bridge/internal/mapping"
	"github.com/hstern/plane-forge-bridge/internal/plane"
	"github.com/hstern/plane-forge-bridge/internal/server"
	pfbsync "github.com/hstern/plane-forge-bridge/internal/sync"
)

// version is the build version, injected via -ldflags="-X main.version=...".
var version = "dev"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("plane-forge-bridge", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", envOr("PFB_CONFIG", "config.yaml"), "path to YAML config file (env: PFB_CONFIG)")
	printVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *printVersion {
		_, _ = fmt.Fprintln(stdout, version)
		return nil
	}

	cfg, err := mapping.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := newLogger(cfg.LogLevel, stdout)
	logger.LogAttrs(context.Background(), slog.LevelInfo, "starting plane-forge-bridge",
		slog.String("version", version),
		slog.String("config", *configPath),
		slog.String("listen", cfg.Listen),
	)

	planeClient := plane.NewClient(cfg.Plane.BaseURL, cfg.Plane.WorkspaceSlug, cfg.Plane.APIKey,
		&http.Client{Timeout: 30 * time.Second})
	planeClient.UserAgent = "plane-forge-bridge/" + version

	translator := pfbsync.NewEngine(planeClient, cfg, logger)
	srv := server.New(cfg, logger, translator)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := srv.ListenAndServe(ctx); err != nil {
		return fmt.Errorf("server: %w", err)
	}
	logger.LogAttrs(context.Background(), slog.LevelInfo, "shutdown complete")
	return nil
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func newLogger(level string, w *os.File) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(strings.ToLower(level))); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl}))
}
