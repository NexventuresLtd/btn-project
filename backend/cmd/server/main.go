// Command server runs the BTN Operations API.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"btn-ops/internal/app"
	"btn-ops/internal/auth"
	"btn-ops/internal/config"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/mail"
	"btn-ops/internal/settings"
)

func main() {
	check := flag.Bool("check", false, "validate configuration and database connectivity, then exit")
	health := flag.Bool("healthcheck", false, "probe the running API's /api/health on localhost:$PORT and exit 0/1 (for container health checks)")
	flag.Parse()
	if *health {
		os.Exit(probeHealth())
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	slog.SetDefault(httpx.NewLogger(cfg.LogLevel, cfg.IsProd()))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		slog.Error("database", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	set := settings.New(pool)
	if err := set.RefreshOverrides(ctx); err != nil {
		slog.Error("loading i18n overrides (did you run `make migrate-up`?)", "err", err)
		os.Exit(1)
	}
	mailer := mail.New(cfg, set)
	d := &deps.Deps{Cfg: cfg, Pool: pool, Settings: set, Mail: mailer, Auth: auth.New(pool, cfg, mailer)}
	if *check {
		fmt.Println("ok")
		return
	}
	a := app.New(d)

	errc := make(chan error, 1)
	go func() { errc <- a.Listen(fmt.Sprintf(":%d", cfg.Port)) }()
	slog.Info("BTN Ops API listening", "port", cfg.Port, "env", cfg.AppEnv)
	select {
	case err := <-errc:
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	case <-ctx.Done():
		slog.Info("shutting down")
		if err := a.ShutdownWithTimeout(10 * time.Second); err != nil {
			slog.Error("shutdown", "err", err)
		}
	}
}

// probeHealth asks the already-running server for /api/health. Used as the container health check,
// because the distroless image has no shell or curl.
func probeHealth() int {
	port := os.Getenv("PORT")
	if port == "" {
		port = "4000"
	}
	client := &http.Client{Timeout: 4 * time.Second}
	res, err := client.Get("http://127.0.0.1:" + port + "/api/health")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "health status", res.StatusCode)
		return 1
	}
	return 0
}
