package app_test

import (
	"btn-ops/internal/config"
	"strconv"
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/testutil"
)

func TestAuthEndpointsAreRateLimited(t *testing.T) {
	e := apptest.New(t, apptest.Options{KeepRateLimits: true})
	var last int
	var retry string
	var code string
	for i := 0; i < 14; i++ {
		r := e.Do("POST", "/api/auth/login", map[string]any{"email": "nobody@x.test", "password": "x"}, nil)
		last, retry = r.StatusCode, r.Header.Get("Retry-After")
		if last == 429 {
			code, _, _ = e.ErrorOf(r)
			break
		}
	}
	if last != 429 || code != "too_many_requests" {
		t.Fatalf("expected 429 envelope, got %d %q", last, code)
	}
	if n, err := strconv.Atoi(retry); err != nil || n < 1 {
		t.Fatalf("Retry-After header: %q", retry)
	}
	// ordinary API traffic is not affected by the auth bucket
	u := e.User(auth.RoleGeneralManager)
	if r := e.Do("GET", "/api/lookups", nil, u); r.StatusCode != 200 {
		t.Fatalf("lookups: %d", r.StatusCode)
	}
}

func TestGlobalLimiter(t *testing.T) {
	e := apptest.New(t, apptest.Options{Config: func(c *config.Config) { c.GlobalRateLimit = 10 }})
	u := e.User(auth.RoleGeneralManager)
	n429 := 0
	for i := 0; i < 20; i++ {
		if r := e.Do("GET", "/api/lookups?lang=rw", nil, u); r.StatusCode == 429 {
			n429++
			if _, msg, _ := e.ErrorOf(r); msg == "" {
				t.Fatal("empty message")
			}
		}
	}
	if n429 == 0 {
		t.Fatal("global limiter never triggered")
	}
}

// NFR-P1: users sharing one public IP (a site behind NAT) are limited per user on signed-in calls, with the
// DEFAULT limits: 60 users each calling /api/auth/me and a list several times from one IP never get a 429,
// while one user hammering /api/auth/me still does, and anonymous sign-in attempts stay limited per IP.
func TestSharedIPUsersAreLimitedPerUser(t *testing.T) {
	def, _ := config.FromEnv()
	e := apptest.New(t, apptest.Options{KeepRateLimits: true, Config: func(c *config.Config) {
		c.GlobalRateLimit, c.IPFloodLimit = def.GlobalRateLimit, def.IPFloodLimit
	}})
	if def.GlobalRateLimit != 600 || def.AuthRateLimit != 30 {
		t.Skipf("non-default limits in the environment: %d/%d", def.GlobalRateLimit, def.AuthRateLimit)
	}
	users := make([]*testutil.TestUser, 60)
	for i := range users {
		users[i] = e.User(auth.RoleSupervisor)
	}
	for round := 0; round < 4; round++ {
		for _, u := range users {
			for _, path := range []string{"/api/auth/me", "/api/lookups"} {
				if r := e.Do("GET", path, nil, u); r.StatusCode != 200 {
					t.Fatalf("round %d %s for %s: %d (shared IP must not be throttled per IP)", round, path, u.Email, r.StatusCode)
				}
			}
		}
	}
	// One user over the per-user session budget is throttled.
	hit := false
	for i := 0; i < 200 && !hit; i++ {
		hit = e.Do("GET", "/api/auth/me", nil, users[0]).StatusCode == 429
	}
	if !hit {
		t.Fatal("per-user session limit never triggered")
	}
	if r := e.Do("GET", "/api/auth/me", nil, users[1]); r.StatusCode != 200 {
		t.Fatalf("another user on the same IP throttled: %d", r.StatusCode)
	}
}
