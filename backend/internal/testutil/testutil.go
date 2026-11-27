// Package testutil gives tests an isolated, fully migrated PostgreSQL database. The migrations run ONCE per
// migration set into a template database (<test db>_tpl_<hash>); every test process then gets its own clone
// (<test db>_p<pid>, CREATE DATABASE … TEMPLATE), so packages run in parallel without each one replaying every
// migration in large transactions — that exhausted the shared lock table (SQLSTATE 53200 "out of shared memory",
// max_locks_per_transaction) when `go test ./...` ran many packages at once (NFR-M1).
package testutil

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"sort"
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
	dbName string // this process's clone
	baseU  *url.URL
)

// Main wraps m.Run so the per-process database is dropped afterwards. Optional:
//
//	func TestMain(m *testing.M) { testutil.Main(m) }
func Main(m *testing.M) {
	code := m.Run()
	Close()
	os.Exit(code)
}

// Close drops this process's database (no-op when unused).
func Close() {
	if pool == nil {
		return
	}
	pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if admin, err := pgxpool.New(ctx, adminURL(baseU.String())); err == nil {
		_, _ = admin.Exec(ctx, `DROP DATABASE IF EXISTS `+quoteIdent(dbName)+` WITH (FORCE)`)
		admin.Close()
	}
}

// Config returns the test configuration (env + safe test defaults).
func Config(t testing.TB) *config.Config {
	t.Helper()
	Pool(t)
	return cfg
}

// Pool returns the pool bound to this process's database (cloned from the migrated template on first use).
func Pool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	once.Do(func() { setErr = setupWithRetry() })
	if setErr != nil {
		t.Fatalf("test database setup: %v (is PostgreSQL up and DATABASE_URL set in backend/.env?)", setErr)
	}
	return pool
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func withDB(u *url.URL, name string) string {
	c := *u
	c.Path = "/" + name
	return c.String()
}

// migrationsHash fingerprints the embedded migration set (names + contents).
func migrationsHash() (string, error) {
	h := sha256.New()
	var names []string
	if err := fs.WalkDir(migrations.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".sql") {
			names = append(names, p)
		}
		return err
	}); err != nil {
		return "", err
	}
	sort.Strings(names)
	for _, n := range names {
		b, err := fs.ReadFile(migrations.FS, n)
		if err != nil {
			return "", err
		}
		h.Write([]byte(n))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:10], nil
}

// setupWithRetry retries while PostgreSQL is out of connection slots (SQLSTATE 53300 "too many clients"): with
// every package of `go test ./...` starting at once on a many-core machine, the slots free up within seconds.
func setupWithRetry() error {
	var err error
	for i := 0; i < 60; i++ {
		if err = setup(); err == nil || !outOfSlots(err) {
			return err
		}
		time.Sleep(time.Duration(500+100*i) * time.Millisecond)
	}
	return err
}

func outOfSlots(err error) bool {
	m := err.Error()
	return strings.Contains(m, "53300") || strings.Contains(m, "too many clients") || strings.Contains(m, "remaining connection slots")
}

// adminURL is url with a small connection cap: the maintenance pools only run a few sequential statements.
func adminURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Set("pool_max_conns", "2")
	u.RawQuery = q.Encode()
	return u.String()
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
	cfg.IPFloodLimit = 1_000_000
	ctx := context.Background()
	if err := db.EnsureDatabase(ctx, cfg.TestDatabaseURL); err != nil {
		return err
	}
	baseU, err = url.Parse(cfg.TestDatabaseURL)
	if err != nil {
		return err
	}
	base := strings.TrimPrefix(baseU.Path, "/")
	admin, err := pgxpool.New(ctx, adminURL(cfg.TestDatabaseURL))
	if err != nil {
		return err
	}
	defer admin.Close()
	hash, err := migrationsHash()
	if err != nil {
		return err
	}
	tpl := fmt.Sprintf("%s_tpl_%s", base, hash)
	if err := ensureTemplate(ctx, admin, base, tpl); err != nil {
		return fmt.Errorf("template %s: %w", tpl, err)
	}
	purgeStale(ctx, admin, base)
	dbName = fmt.Sprintf("%s_p%d", base, os.Getpid())
	if _, err := admin.Exec(ctx, `DROP DATABASE IF EXISTS `+quoteIdent(dbName)+` WITH (FORCE)`); err != nil {
		return err
	}
	if err := createFromTemplate(ctx, admin, dbName, tpl); err != nil {
		return err
	}
	// 6 connections per test process: enough for tests that hold a few transactions open while issuing requests,
	// and 24 parallel packages stay well under PostgreSQL's default max_connections (100).
	pool, err = db.Connect(ctx, withDB(baseU, dbName), 6)
	return err
}

// createFromTemplate clones tpl; PostgreSQL refuses while another session is still connected to the template
// (e.g. a concurrent clone's catalog check), so retry briefly.
func createFromTemplate(ctx context.Context, admin *pgxpool.Pool, name, tpl string) error {
	var err error
	for i := 0; i < 50; i++ {
		if _, err = admin.Exec(ctx, `CREATE DATABASE `+quoteIdent(name)+` TEMPLATE `+quoteIdent(tpl)); err == nil {
			return nil
		}
		if !strings.Contains(err.Error(), "being accessed by other users") {
			return err
		}
		time.Sleep(time.Duration(50+i*10) * time.Millisecond)
	}
	return err
}

// ensureTemplate migrates the template database once per migration set. Processes racing to build it are
// serialized by an advisory lock; a template is usable once it carries the "ready" comment.
func ensureTemplate(ctx context.Context, admin *pgxpool.Pool, base, tpl string) error {
	ready := func() (bool, error) {
		var ok bool
		err := admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_database d WHERE d.datname = $1
			AND shobj_description(d.oid, 'pg_database') = 'btn-test-template ready')`, tpl).Scan(&ok)
		return ok, err
	}
	if ok, err := ready(); err != nil || ok {
		return err
	}
	conn, err := admin.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext($1))`, "btn-test-template:"+base); err != nil {
		return err
	}
	defer func() { _, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtext($1))`, "btn-test-template:"+base) }()
	if ok, err := ready(); err != nil || ok {
		return err
	}
	// Older templates of this test database are no longer needed (best effort: skip ones still in use).
	rows, err := conn.Query(ctx, `SELECT datname FROM pg_database WHERE datname LIKE $1 AND datname <> $2`,
		strings.ReplaceAll(base, "_", `\_`)+`\_tpl\_%`, tpl)
	if err == nil {
		var old []string
		for rows.Next() {
			var n string
			_ = rows.Scan(&n)
			old = append(old, n)
		}
		rows.Close()
		for _, n := range old {
			_, _ = conn.Exec(ctx, `DROP DATABASE IF EXISTS `+quoteIdent(n))
		}
	}
	if _, err := conn.Exec(ctx, `DROP DATABASE IF EXISTS `+quoteIdent(tpl)+` WITH (FORCE)`); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, `CREATE DATABASE `+quoteIdent(tpl)+` TEMPLATE template0`); err != nil {
		return err
	}
	sqlDB, err := sql.Open("pgx", withDB(baseU, tpl))
	if err != nil {
		return err
	}
	goose.SetBaseFS(migrations.FS)
	_ = goose.SetDialect("postgres")
	goose.SetLogger(goose.NopLogger())
	err = goose.UpContext(ctx, sqlDB, ".")
	sqlDB.Close()
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, `COMMENT ON DATABASE `+quoteIdent(tpl)+` IS 'btn-test-template ready'`)
	return err
}

// purgeStale drops per-process databases (and legacy per-process schemas) left behind by crashed runs.
func purgeStale(ctx context.Context, admin *pgxpool.Pool, base string) {
	alive := func(pid int) bool {
		_, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
		return err == nil
	}
	rows, err := admin.Query(ctx, `SELECT datname FROM pg_database WHERE datname LIKE $1`, strings.ReplaceAll(base, "_", `\_`)+`\_p%`)
	if err == nil {
		var stale []string
		for rows.Next() {
			var n string
			var pid int
			_ = rows.Scan(&n)
			if _, err := fmt.Sscanf(strings.TrimPrefix(n, base+"_p"), "%d", &pid); err == nil && pid != os.Getpid() && !alive(pid) {
				stale = append(stale, n)
			}
		}
		rows.Close()
		for _, n := range stale {
			_, _ = admin.Exec(ctx, `DROP DATABASE IF EXISTS `+quoteIdent(n)+` WITH (FORCE)`)
		}
	}
	rows, err = admin.Query(ctx, `SELECT nspname FROM pg_namespace WHERE nspname LIKE 't\_%'`)
	if err == nil {
		var stale []string
		for rows.Next() {
			var n string
			var pid int
			_ = rows.Scan(&n)
			if _, err := fmt.Sscanf(n, "t_%d", &pid); err == nil && !alive(pid) {
				stale = append(stale, n)
			}
		}
		rows.Close()
		for _, n := range stale {
			_, _ = admin.Exec(ctx, `DROP SCHEMA IF EXISTS `+quoteIdent(n)+` CASCADE`)
		}
	}
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
