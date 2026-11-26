// Package ratelimit implements in-memory token buckets (sharded) and Fiber middleware that answers
// 429 with Retry-After. Named groups ("global", "auth", "pdf", "upload", …) are configured once and
// consumed by routes via ratelimit.Group("pdf").
package ratelimit

import (
	"hash/fnv"
	"math"
	"strconv"
	"sync"
	"time"

	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
)

// Rule defines a bucket: PerMinute tokens refilled evenly, Burst the bucket size.
type Rule struct {
	PerMinute int
	Burst     int
	// ByUser keys authenticated requests by user id (falls back to IP when anonymous).
	ByUser bool
}

type bucket struct {
	tokens float64
	last   time.Time
}

const shards = 32

type shard struct {
	mu sync.Mutex
	m  map[string]*bucket
}

// Limiter is a keyed token-bucket limiter.
type Limiter struct {
	rule     Rule
	shards   [shards]shard
	now      func() time.Time
	lastSwep time.Time
	swMu     sync.Mutex
}

// New creates a limiter for rule.
func New(rule Rule) *Limiter {
	if rule.Burst <= 0 {
		rule.Burst = max(1, rule.PerMinute/6)
	}
	l := &Limiter{rule: rule, now: time.Now}
	for i := range l.shards {
		l.shards[i].m = map[string]*bucket{}
	}
	return l
}

// Allow consumes a token for key. When denied it returns the wait until a token is available.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	now := l.now()
	h := fnv.New32a()
	h.Write([]byte(key))
	s := &l.shards[h.Sum32()%shards]
	perSec := float64(l.rule.PerMinute) / 60
	s.mu.Lock()
	b, ok := s.m[key]
	if !ok {
		b = &bucket{tokens: float64(l.rule.Burst), last: now}
		s.m[key] = b
	} else {
		b.tokens = math.Min(float64(l.rule.Burst), b.tokens+now.Sub(b.last).Seconds()*perSec)
		b.last = now
	}
	var allowed bool
	var wait time.Duration
	if b.tokens >= 1 {
		b.tokens--
		allowed = true
	} else {
		wait = time.Duration((1 - b.tokens) / perSec * float64(time.Second))
	}
	s.mu.Unlock()
	l.sweep(now)
	return allowed, wait
}

// sweep drops idle full buckets about once a minute.
func (l *Limiter) sweep(now time.Time) {
	l.swMu.Lock()
	if now.Sub(l.lastSwep) < time.Minute {
		l.swMu.Unlock()
		return
	}
	l.lastSwep = now
	l.swMu.Unlock()
	idle := 2 * time.Minute
	for i := range l.shards {
		s := &l.shards[i]
		s.mu.Lock()
		for k, b := range s.m {
			if now.Sub(b.last) > idle {
				delete(s.m, k)
			}
		}
		s.mu.Unlock()
	}
}

// Middleware limits by keyFn(c) (e.g. IP). Denied requests get 429, Retry-After and the Node envelope.
func Middleware(l *Limiter, name string, keyFn func(*fiber.Ctx) string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ok, wait := l.Allow(name + "|" + keyFn(c))
		if ok {
			return c.Next()
		}
		secs := int(math.Ceil(wait.Seconds()))
		if secs < 1 {
			secs = 1
		}
		e := httpx.NewKey(429, "too_many_requests", "error.too_many")
		e.Headers = map[string]string{"Retry-After": strconv.Itoa(secs)}
		return e
	}
}

// Key returns "u:<id>" for signed-in requests, else "ip:<ip>".
func Key(c *fiber.Ctx) string {
	if id, ok := c.Locals("userId").(string); ok && id != "" {
		return "u:" + id
	}
	return "ip:" + c.IP()
}

// IPKey always keys by client IP.
func IPKey(c *fiber.Ctx) string { return "ip:" + c.IP() }

// ── named groups ─────────────────────────────────────────────────────────────

var (
	regMu    sync.RWMutex
	registry = map[string]*Limiter{}
	defaults = builtinRules()
)

func builtinRules() map[string]Rule {
	return map[string]Rule{
		"global":  {PerMinute: 600, Burst: 120},
		"auth":    {PerMinute: 30, Burst: 10},                // unauthenticated sign-in steps, per IP
		"session": {PerMinute: 120, Burst: 30, ByUser: true}, // signed-in /api/auth/* calls, per user
		"pdf":     {PerMinute: 30, Burst: 8, ByUser: true},
		"upload":  {PerMinute: 12, Burst: 5, ByUser: true},
		"export":  {PerMinute: 30, Burst: 8, ByUser: true},
	}
}

// ResetDefaults restores the built-in group rules and drops all shared limiters (tests).
func ResetDefaults() {
	regMu.Lock()
	defer regMu.Unlock()
	defaults = builtinRules()
	registry = map[string]*Limiter{}
}

// Configure overrides the rule of a group (call before building routes).
func Configure(name string, r Rule) {
	regMu.Lock()
	defer regMu.Unlock()
	defaults[name] = r
	delete(registry, name)
}

// Group returns middleware for a named group, e.g. `app.Get("/pdf/x", ratelimit.Group("pdf"), h)`.
// Unknown groups get a generous per-user default. The limiter is shared by every use of the name.
func Group(name string) fiber.Handler {
	regMu.Lock()
	l, ok := registry[name]
	if !ok {
		r, found := defaults[name]
		if !found {
			r = Rule{PerMinute: 120, Burst: 30, ByUser: true}
		}
		l = New(r)
		registry[name] = l
	}
	byUser := l.rule.ByUser
	regMu.Unlock()
	key := IPKey
	if byUser {
		key = Key
	}
	return Middleware(l, name, key)
}
