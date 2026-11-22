package app_test

import (
	"btn-ops/internal/config"
	"strconv"
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
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
