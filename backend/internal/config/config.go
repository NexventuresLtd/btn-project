// Package config loads the process configuration from the environment (and an optional .env file).
package config

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is immutable after Load.
type Config struct {
	AppEnv          string // development | test | production
	DatabaseURL     string
	TestDatabaseURL string
	Port            int
	AppURL          string
	FrontendOrigins []string
	OTPPepper       string
	SMTPHost        string
	SMTPPort        int
	SMTPUser        string
	SMTPPassword    string
	MailFrom        string
	SuperAdminEmail string
	SuperAdminName  string
	SuperAdminPass  string
	WebAuthnRPID    string
	WebAuthnRPName  string
	WebAuthnOrigins []string
	AuthDevLogOTP   bool
	TrustedProxies  []string
	LogLevel        string
	RequestTimeout  time.Duration
	DBMaxConns      int32
	AssetsDir       string // optional override dir for logo files (backend/assets)
	GlobalRateLimit int    // requests per minute per IP
	AuthRateLimit   int    // requests per minute per IP on /api/auth/*
	OpsAlertEmail   string // F1: critical alerts (job failures, server errors, health) are also emailed here; "" = in-app only
	DataDir         string // F1: directory whose free disk space the health check reports (default ".")
	JobsEnabled     bool   // F1: run the background job scheduler in this process (JOBS_ENABLED, default true)
}

func (c *Config) IsProd() bool { return c.AppEnv == "production" }

// LoadDotEnv reads KEY=VALUE lines from path into the process environment (existing vars win).
func LoadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		if _, set := os.LookupEnv(k); !set {
			os.Setenv(k, v)
		}
	}
}

func get(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func list(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func atoi(k string, def int) int {
	if v, err := strconv.Atoi(get(k, "")); err == nil {
		return v
	}
	return def
}

// Load reads .env (cwd, then backend/) and validates the result.
func Load() (*Config, error) {
	LoadDotEnvUp()
	return FromEnv()
}

// LoadDotEnvUp loads the first ".env" or "backend/.env" found walking up from the working directory
// (so `go test` inside a package directory finds backend/.env).
func LoadDotEnvUp() {
	dir, err := os.Getwd()
	if err != nil {
		return
	}
	sep := string(os.PathSeparator)
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(dir + sep + "go.mod"); err == nil {
			LoadDotEnv(dir + sep + ".env")
			return
		}
		if _, err := os.Stat(dir + sep + "backend" + sep + ".env"); err == nil {
			LoadDotEnv(dir + sep + "backend" + sep + ".env")
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}

// FromEnv builds a Config from the current environment.
func FromEnv() (*Config, error) {
	env := get("APP_ENV", get("NODE_ENV", "development"))
	c := &Config{
		AppEnv:          env,
		DatabaseURL:     get("DATABASE_URL", ""),
		TestDatabaseURL: get("TEST_DATABASE_URL", ""),
		Port:            atoi("PORT", 4200),
		AppURL:          get("APP_URL", "http://localhost:5173"),
		FrontendOrigins: list(get("FRONTEND_ORIGIN", "http://localhost:5173")),
		OTPPepper:       get("OTP_PEPPER", ""),
		SMTPHost:        get("SMTP_HOST", ""),
		SMTPPort:        atoi("SMTP_PORT", 587),
		SMTPUser:        get("SMTP_USER", ""),
		SMTPPassword:    get("SMTP_PASSWORD", ""),
		MailFrom:        get("MAIL_FROM", ""),
		SuperAdminEmail: strings.ToLower(strings.TrimSpace(get("SUPER_ADMIN_EMAIL", ""))),
		SuperAdminName:  get("SUPER_ADMIN_NAME", "Super Administrator"),
		SuperAdminPass:  get("SUPER_ADMIN_PASSWORD", ""),
		WebAuthnRPID:    get("WEBAUTHN_RP_ID", "localhost"),
		WebAuthnRPName:  get("WEBAUTHN_RP_NAME", "BTN Operations System"),
		WebAuthnOrigins: list(get("WEBAUTHN_ORIGINS", "http://localhost:5173")),
		AuthDevLogOTP:   get("AUTH_DEV_LOG_OTP", "") == "true",
		TrustedProxies:  list(get("TRUSTED_PROXIES", "")),
		LogLevel:        get("LOG_LEVEL", "info"),
		RequestTimeout:  time.Duration(atoi("REQUEST_TIMEOUT_SECONDS", 30)) * time.Second,
		DBMaxConns:      int32(atoi("DB_MAX_CONNS", 20)),
		AssetsDir:       get("ASSETS_DIR", ""),
		GlobalRateLimit: atoi("RATE_LIMIT_GLOBAL_PER_MIN", 600),
		AuthRateLimit:   atoi("RATE_LIMIT_AUTH_PER_MIN", 30),
		OpsAlertEmail:   strings.TrimSpace(get("OPS_ALERT_EMAIL", "")),
		DataDir:         get("DATA_DIR", "."),
		JobsEnabled:     get("JOBS_ENABLED", "true") != "false",
	}
	if c.IsProd() {
		c.AuthDevLogOTP = false // never print codes in production
	}
	if c.TestDatabaseURL == "" && c.DatabaseURL != "" {
		c.TestDatabaseURL = deriveTestURL(c.DatabaseURL)
	}
	var errs []string
	if c.DatabaseURL == "" {
		errs = append(errs, "DATABASE_URL is required")
	}
	if len(c.OTPPepper) < 32 {
		errs = append(errs, "OTP_PEPPER must be at least 32 characters")
	}
	if len(errs) > 0 {
		return c, fmt.Errorf("invalid environment configuration: %s", strings.Join(errs, "; "))
	}
	return c, nil
}

// deriveTestURL swaps the database name for <name>_test.
func deriveTestURL(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return ""
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "_test"
	return u.String()
}
