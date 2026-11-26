package app_test

import (
	"strings"
	"testing"
	"time"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/testutil"
)

// AUTH-05: the Super Administrator sets the lockout threshold and period; the login flow applies them at once,
// the change is audited, and no other role may change it.
func TestLockoutPolicyConfigurable(t *testing.T) {
	e := apptest.New(t)
	super := e.User(auth.RoleSuperAdmin)
	sys := e.User(auth.RoleSystemAdmin)

	var got struct {
		Security struct {
			LockoutFailures int `json:"lockoutFailures"`
			LockoutMinutes  int `json:"lockoutMinutes"`
		} `json:"security"`
	}
	e.Decode(e.Do("GET", "/api/settings", nil, super), &got)
	if got.Security.LockoutFailures != 5 || got.Security.LockoutMinutes != 15 {
		t.Fatalf("defaults: %+v", got.Security)
	}
	// Only the Super Administrator may change it (System Administrator holds settings.manage otherwise).
	if r := e.Do("PUT", "/api/settings", map[string]any{"security": map[string]any{"lockoutMinutes": 60}}, sys); r.StatusCode != 403 {
		t.Fatalf("system admin changed the lockout policy: %d", r.StatusCode)
	}
	// Out-of-range values are refused.
	if r := e.Do("PUT", "/api/settings", map[string]any{"security": map[string]any{"lockoutMinutes": 0}}, super); r.StatusCode != 400 {
		t.Fatalf("0 minutes accepted: %d", r.StatusCode)
	}
	if r := e.Do("PUT", "/api/settings", map[string]any{"security": map[string]any{"lockoutFailures": 3, "lockoutMinutes": 40}}, super); r.StatusCode != 200 {
		t.Fatalf("save: %d %s", r.StatusCode, e.Text(r))
	}
	e.Decode(e.Do("GET", "/api/settings", nil, sys), &got)
	if got.Security.LockoutFailures != 3 || got.Security.LockoutMinutes != 40 {
		t.Fatalf("saved: %+v", got.Security)
	}
	var audited int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE action='settings.update' AND "userId"=$1
		AND (after->'security'->>'lockoutMinutes')::int = 40 AND (before->'security'->>'lockoutMinutes')::int = 15`, super.ID).Scan(&audited)
	if audited != 1 {
		t.Fatalf("policy change not audited with before/after: %d", audited)
	}

	// The login flow applies the new policy: 3 failures lock the account for 40 minutes.
	e.User(auth.RoleSalesManager, testutil.UserOpts{Email: "lock@x.test", Password: "GoodPassw0rd"})
	for i := 0; i < 3; i++ {
		if r := do(e, "POST", "/api/auth/login", map[string]any{"email": "lock@x.test", "password": "bad"}); r.StatusCode != 401 {
			t.Fatalf("attempt %d: %d", i, r.StatusCode)
		}
	}
	var until time.Time
	if err := e.Pool.QueryRow(t.Context(), `SELECT "lockedUntil" FROM "User" WHERE email='lock@x.test'`).Scan(&until); err != nil {
		t.Fatalf("not locked after 3 failures: %v", err)
	}
	if d := time.Until(until); d < 39*time.Minute || d > 41*time.Minute {
		t.Fatalf("locked for %v, want 40 min", d)
	}
	r := do(e, "POST", "/api/auth/login", map[string]any{"email": "lock@x.test", "password": "GoodPassw0rd"})
	if code, msg, _ := e.ErrorOf(r); r.StatusCode != 423 || code != "locked" || !strings.Contains(msg, "40 minutes") {
		t.Fatalf("locked answer: %d %s %q", r.StatusCode, code, msg)
	}
	var lockAudit int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE action='auth.lock' AND (after->>'lockedMinutes')::int = 40`).Scan(&lockAudit)
	if lockAudit != 1 {
		t.Fatalf("lock not audited with the period: %d", lockAudit)
	}
}
