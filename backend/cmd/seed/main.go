// Command seed bootstraps plants, catalogue, settings, branding defaults and the super administrator (idempotent).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"btn-ops/internal/config"
	"btn-ops/internal/db"
	"btn-ops/internal/seed"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DatabaseURL, 4)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := seed.Run(ctx, pool, cfg); err != nil {
		slog.Error("seed failed", "err", err)
		os.Exit(1)
	}
	fmt.Println("Seed complete.")
}
