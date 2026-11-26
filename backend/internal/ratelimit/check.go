package ratelimit

import (
	"math"
	"strconv"

	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
)

// Check consumes one token of the named group for this request without acting as middleware
// (for handlers or middlewares that decide late whether a request is, e.g., an export).
// It returns the same 429 error the group middleware would.
func Check(c *fiber.Ctx, name string) error {
	Group(name) // make sure the shared limiter exists
	regMu.RLock()
	l := registry[name]
	regMu.RUnlock()
	key := IPKey(c)
	if l.rule.ByUser {
		key = Key(c)
	}
	ok, wait := l.Allow(name + "|" + key)
	if ok {
		return nil
	}
	secs := int(math.Ceil(wait.Seconds()))
	if secs < 1 {
		secs = 1
	}
	e := httpx.NewKey(429, "too_many_requests", "error.too_many")
	e.Headers = map[string]string{"Retry-After": strconv.Itoa(secs)}
	return e
}
