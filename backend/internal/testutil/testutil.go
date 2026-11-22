// Package testutil gives tests an isolated, fully migrated PostgreSQL schema inside the test database
// (btn_ops_go_test). Every test process gets its own schema, so packages can run in parallel.
package testutil

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/config"
	"btn-ops/internal/db"
	"btn-ops/internal/i18n"
	"btn-ops/internal/seed"
	"btn-ops/migrations"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

var (
	once   sync.Once
	pool   *pgxpool.Pool
	cfg    *config.Config
	setErr error
)

// Main wraps m.Run so the per-process schema is dropped afterwards. Optional:
//
//	func TestMain(m *testing.M) { testutil.Main(m) }
func Main(m *testing.M) {
	code := m.Run()
	Close()
	os.Exit(code)
}

// Close drops this process's schema (no-op when unused).
func Close() {
	if pool != nil {
		_, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schemaName()+` CASCADE`)
		pool.Close()
	}
}

func schemaName() string { return fmt.Sprintf("t_%d", os.Getpid()) }

// Config returns the test configuration (env + safe test defaults).
func Config(t testing.TB) *config.Config {
	t.Helper()
	Pool(t)
	return cfg
}

// Pool returns the pool bound to this process's schema (created and migrated on first use).
func Pool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	once.Do(func() { setErr = setup() })
	if setErr != nil {
		t.Fatalf("test database setup: %v (is PostgreSQL up and DATABASE_URL set in backend/.env?)", setErr)
	}
	return pool
}

func setup() error {
	config.LoadDotEnvUp()
	c, err := config.FromEnv()
	if err != nil && c == nil {
		return err
	}
	if c.TestDatabaseURL == "" {
		return fmt.Errorf("TEST_DATABASE_URL / DATABASE_URL not set")
	}
	cfg = c
	cfg.AppEnv = "test"
	cfg.OTPPepper = strings.Repeat("t", 48)
	cfg.AuthDevLogOTP = false
	cfg.WebAuthnRPID = "localhost"
	cfg.WebAuthnOrigins = []string{"http://localhost:5173"}
	cfg.FrontendOrigins = []string{"http://localhost:5173"}
	cfg.AppURL = "http://localhost:5173"
	cfg.GlobalRateLimit = 1_000_000
	ctx := context.Background()
	if err := db.EnsureDatabase(ctx, cfg.TestDatabaseURL); err != nil {
		return err
	}
	admin, err := pgxpool.New(ctx, cfg.TestDatabaseURL)
	if err != nil {
		return err
	}
	defer admin.Close()
	// purge schemas left behind by crashed runs (names embed nothing time-based, so use pg stats: drop all t_* not ours older than 2h)
	rows, _ := admin.Query(ctx, `SELECT nspname FROM pg_namespace WHERE nspname LIKE 't\_%' AND nspname <> $1`, schemaName())
	var stale []string
	for rows != nil && rows.Next() {
		var n string
		_ = rows.Scan(&n)
		stale = append(stale, n)
	}
	if rows != nil {
		rows.Close()
	}
	for _, n := range stale {
		if schemaIdle(ctx, admin, n) {
			_, _ = admin.Exec(ctx, `DROP SCHEMA `+n+` CASCADE`)
		}
	}
	sn := schemaName()
	if _, err := admin.Exec(ctx, `DROP SCHEMA IF EXISTS `+sn+` CASCADE`); err != nil {
		return err
	}
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+sn); err != nil {
		return err
	}
	u, _ := url.Parse(cfg.TestDatabaseURL)
	q := u.Query()
	q.Set("search_path", sn)
	u.RawQuery = q.Encode()

	sqlDB, err := sql.Open("pgx", u.String())
	if err != nil {
		return err
	}
	goose.SetBaseFS(migrations.FS)
	_ = goose.SetDialect("postgres")
	goose.SetLogger(goose.NopLogger())
	if err := goose.UpContext(ctx, sqlDB, "."); err != nil {
		sqlDB.Close()
		return err
	}
	sqlDB.Close()
	pool, err = db.Connect(ctx, u.String(), 10)
	return err
}

// schemaIdle: a leftover schema is stale when no live backend of that pid exists (best effort: older than 2h).
func schemaIdle(ctx context.Context, p *pgxpool.Pool, name string) bool {
	var pid int
	if _, err := fmt.Sscanf(name, "t_%d", &pid); err != nil {
		return false
	}
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err == nil {
		return false // that process is still running
	}
	return true
}

// Reset truncates every table in the test schema and re-seeds plants/catalogue/settings.
// Call at the start of each test that touches the database.
func Reset(t testing.TB) *pgxpool.Pool {
	t.Helper()
	p := Pool(t)
	ctx := context.Background()
	rows, err := p.Query(ctx, `SELECT quote_ident(tablename) FROM pg_tables WHERE schemaname = current_schema() AND tablename <> 'goose_db_version'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	if _, err := p.Exec(ctx, `TRUNCATE `+strings.Join(tables, ", ")+` RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := seed.Catalog(ctx, p); err != nil {
		t.Fatal(err)
	}
	i18n.SetOverrides(nil)
	return p
}

// TestUser is a user created by CreateUser.
type TestUser struct {
	ID, Email, Name, Password, Token string
	Role                             auth.Role
	PlantID                          string
}

// UserOpts customizes CreateUser.
type UserOpts struct {
	Email    string
	Name     string
	Plant    string // plant code; defaults to BTN-1 for plant-scoped roles
	Language string
	Inactive bool
	// MustChangePassword leaves the user in the first-login state.
	MustChangePassword bool
	Password           string // default "DevPass1234!"
}

var userSeq int
var userMu sync.Mutex

// CreateUser inserts a user for role and a live session; Token is the raw `btn_session` cookie value.
func CreateUser(t testing.TB, p *pgxpool.Pool, role auth.Role, o ...UserOpts) *TestUser {
	t.Helper()
	var opt UserOpts
	if len(o) > 0 {
		opt = o[0]
	}
	userMu.Lock()
	userSeq++
	n := userSeq
	userMu.Unlock()
	if opt.Email == "" {
		opt.Email = fmt.Sprintf("%s-%d@btn.test", strings.ToLower(strings.ReplaceAll(string(role), "_", "")), n)
	}
	if opt.Name == "" {
		opt.Name = "Test " + string(role)
	}
	if opt.Password == "" {
		opt.Password = "DevPass1234!"
	}
	if opt.Language == "" {
		opt.Language = "en"
	}
	ctx := context.Background()
	var plantID *string
	if auth.IsPlantScoped(role) || opt.Plant != "" {
		code := opt.Plant
		if code == "" {
			code = "BTN-1"
		}
		var id string
		if err := p.QueryRow(ctx, `SELECT id FROM "Plant" WHERE code=$1`, code).Scan(&id); err != nil {
			t.Fatalf("plant %s: %v", code, err)
		}
		plantID = &id
	}
	u := &TestUser{ID: db.NewID(), Email: opt.Email, Name: opt.Name, Role: role, Password: opt.Password}
	if plantID != nil {
		u.PlantID = *plantID
	}
	_, err := p.Exec(ctx, `INSERT INTO "User" (id, email, name, role, "plantId", "passwordHash", "mustChangePassword", "isActive", language)
		VALUES ($1,$2,$3,$4::"Role",$5,$6,$7,$8,$9)`, u.ID, u.Email, u.Name, string(role), plantID, auth.HashPassword(opt.Password),
		opt.MustChangePassword, !opt.Inactive, opt.Language)
	if err != nil {
		t.Fatal(err)
	}
	svc := &auth.Service{Pool: p}
	u.Token, err = svc.CreateSessionToken(ctx, u.ID, "test", "go-test")
	if err != nil {
		t.Fatal(err)
	}
	_ = time.Now
	return u
}
