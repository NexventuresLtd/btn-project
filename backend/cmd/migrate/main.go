// Command migrate runs the embedded goose migrations: up | down | status | version | reset.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"btn-ops/internal/config"
	"btn-ops/internal/db"
	"btn-ops/migrations"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func main() {
	cmd := "up"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	config.LoadDotEnvUp()
	url := os.Getenv("DATABASE_URL")
	if os.Getenv("MIGRATE_TARGET") == "test" { // make test-db
		cfg, _ := config.FromEnv()
		url = cfg.TestDatabaseURL
		if err := db.EnsureDatabase(context.Background(), url); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if url == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL is required")
		os.Exit(1)
	}
	conn, err := sql.Open("pgx", url)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer conn.Close()
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		panic(err)
	}
	if err := goose.RunWithOptionsContext(context.Background(), cmd, conn, ".", nil, goose.WithAllowMissing()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
