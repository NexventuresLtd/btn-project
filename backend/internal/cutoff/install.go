package cutoff

import (
	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/export"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/pdf"

	"github.com/gofiber/fiber/v2"
)

// Install points the PDF and export hooks at q (called once from reports.Register; safe to call again).
func Install(q db.DBTX) {
	SetSource(q)
	pdf.SetMetaHook(func(c *fiber.Ctx, lang i18n.Lang) []pdf.Meta {
		info, ok := forRequest(c, lang)
		if !ok {
			return nil
		}
		return MetaRows(info, lang)
	})
	export.SetFiltersHook(func(c *fiber.Ctx) []export.Filter {
		lang := httpx.Lang(c)
		info, ok := forRequest(c, lang)
		if !ok {
			return nil
		}
		out := []export.Filter{{Label: "cutoff.label", Value: info.CutoffLabel}}
		if info.HasPending() {
			out = append(out, export.Filter{Label: "cutoff.unsynced_label", Value: info.UnsynchronisedNote})
		}
		return out
	})
}

// MetaRows are the PDF meta-box rows for info.
func MetaRows(info Info, lang i18n.Lang) []pdf.Meta {
	rows := []pdf.Meta{{Label: i18n.T(lang, "cutoff.label"), Value: info.CutoffLabel}}
	if info.HasPending() {
		rows = append(rows, pdf.Meta{Label: i18n.T(lang, "cutoff.unsynced_label"), Value: info.UnsynchronisedNote})
	}
	return rows
}

// PlantOf is the plant a request's report covers: the user's pinned site, else ?plantId=, else all.
func PlantOf(c *fiber.Ctx) string {
	if u := auth.FromCtx(c); u != nil {
		return auth.PlantFilter(u, c.Query("plantId"))
	}
	return c.Query("plantId")
}

func forRequest(c *fiber.Ctx, lang i18n.Lang) (Info, bool) {
	q := Source()
	if q == nil {
		return Info{}, false
	}
	info, err := Load(httpx.Ctx(c), q, PlantOf(c), lang)
	if err != nil {
		return Info{}, false
	}
	return info, true
}
