package pdf

import (
	"sync/atomic"

	"btn-ops/internal/i18n"

	"github.com/gofiber/fiber/v2"
)

// MetaHookFunc adds rows to the meta box of every request-built report (pdf.New). P7 installs one that
// states the data cut-off and the sites with unsynchronised offline data (SRS RPT-06, internal/cutoff).
type MetaHookFunc func(c *fiber.Ctx, lang i18n.Lang) []Meta

var metaHook atomic.Pointer[MetaHookFunc]

// SetMetaHook installs (or with nil removes) the meta hook.
func SetMetaHook(h MetaHookFunc) {
	if h == nil {
		metaHook.Store(nil)
		return
	}
	metaHook.Store(&h)
}

func hookMeta(c *fiber.Ctx, lang i18n.Lang) []Meta {
	if h := metaHook.Load(); h != nil && c != nil {
		return (*h)(c, lang)
	}
	return nil
}
