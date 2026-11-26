package export

import (
	"sync/atomic"

	"github.com/gofiber/fiber/v2"
)

// FiltersHookFunc adds lines to the filters block of every export (SendDoc). P7 installs one that states the
// data cut-off and the sites with unsynchronised offline data (SRS RPT-06, internal/cutoff).
type FiltersHookFunc func(c *fiber.Ctx) []Filter

var filtersHook atomic.Pointer[FiltersHookFunc]

// SetFiltersHook installs (or with nil removes) the filters hook.
func SetFiltersHook(h FiltersHookFunc) {
	if h == nil {
		filtersHook.Store(nil)
		return
	}
	filtersHook.Store(&h)
}

func hookFilters(c *fiber.Ctx) []Filter {
	if h := filtersHook.Load(); h != nil && c != nil {
		return (*h)(c)
	}
	return nil
}
