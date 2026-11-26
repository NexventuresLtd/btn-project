// Package db wraps the pgx pool and offers a transaction helper.
package db

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"

	"btn-ops/internal/dates"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DBTX is satisfied by *pgxpool.Pool, pgx.Tx and *pgxpool.Conn, so helpers (audit, seq,
// repositories) work both inside and outside a transaction.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Pool aliases pgxpool.Pool for callers that only import this package.
type Pool = pgxpool.Pool

// Connect opens a tuned pool. Sessions run in UTC because "timestamp(3)" columns hold UTC wall-clock.
func Connect(ctx context.Context, url string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	if maxConns <= 0 {
		maxConns = 20
	}
	cfg.MaxConns = maxConns
	cfg.MinConns = 2
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 10 * time.Minute
	cfg.HealthCheckPeriod = time.Minute
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	// Bounded guard (OFF-11): a connection left idle inside a transaction (a leaked or stuck request) is ended by
	// the server after 5 minutes, so a writer of synced rows can never hold every device's delta pull back for
	// longer than that. A DATABASE_URL parameter of the same name overrides it.
	if _, ok := cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"]; !ok {
		cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "300000"
	}
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeCacheStatement // prepared statements, cached per connection
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database unreachable: %w", err)
	}
	return pool, nil
}

// Tx runs fn in a transaction: commit on nil, rollback on error or panic.
func Tx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) (err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(context.Background())
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback(context.Background())
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// TxVal is Tx returning a value.
func TxVal[T any](ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) (T, error)) (T, error) {
	var out T
	err := Tx(ctx, pool, func(tx pgx.Tx) error {
		var e error
		out, e = fn(tx)
		return e
	})
	return out, err
}

// Now returns the current time in UTC. Always pass UTC times to "timestamp(3)" columns.
func Now() time.Time { return dates.Now().UTC().Truncate(time.Millisecond) } // dates.Now: fakeable server clock

const b36 = "0123456789abcdefghijklmnopqrstuvwxyz"

// NewID returns a cuid-like identifier ("c" + 24 base36 chars). Tables also default to
// gen_random_uuid()::text, so inserts may simply omit "id" and use RETURNING id.
func NewID() string {
	b := make([]byte, 25)
	b[0] = 'c'
	n := big.NewInt(36)
	for i := 1; i < len(b); i++ {
		r, _ := rand.Int(rand.Reader, n)
		b[i] = b36[r.Int64()]
	}
	return string(b)
}

// IsNoRows reports pgx.ErrNoRows.
func IsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// PgError extracts the *pgconn.PgError, if any.
func PgError(err error) *pgconn.PgError {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe
	}
	return nil
}

// TrimDecimal formats a NUMERIC text value like Prisma's Decimal JSON ("50.000" -> "50", "1.500" -> "1.5").
func TrimDecimal(s string) string {
	if s == "" {
		return "0"
	}
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			j := len(s)
			for j > i+1 && s[j-1] == '0' {
				j--
			}
			if j == i+1 {
				j = i
			}
			return s[:j]
		}
	}
	return s
}

// EnsureDatabase creates the database named in url (from template0, avoiding collation-version
// mismatches on template1) when it does not exist. Needs CREATEDB.
func EnsureDatabase(ctx context.Context, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	name := strings.TrimPrefix(u.Path, "/")
	admin := *u
	admin.Path = "/postgres"
	c, err := pgxpool.New(ctx, admin.String())
	if err != nil {
		return err
	}
	defer c.Close()
	var exists bool
	if err := c.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)`, name).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		_, err = c.Exec(ctx, `CREATE DATABASE "`+strings.ReplaceAll(name, `"`, ``)+`" TEMPLATE template0`)
	}
	return err
}
