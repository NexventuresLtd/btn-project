package reports

import (
	"btn-ops/internal/auth"
	"btn-ops/internal/cutoff"
	"btn-ops/internal/dashboard"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/ratelimit"

	"github.com/gofiber/fiber/v2"
)

// Register mounts /api/reports.
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d}
	cutoff.Install(d.Pool)       // RPT-06: PDF meta rows + export filter lines state the data cut-off and unsynchronised sites
	r.Get("/catalog", h.catalog) // any signed-in user; the list itself is filtered by permission
	r.Get("/pdf/management", auth.RequirePermission("reports.view"), ratelimit.Group("pdf"), h.management)
	sched := auth.RequirePermission("reports.schedule")
	r.Get("/schedules", sched, h.listSchedules) // RPT-02 admin screen
	r.Get("/schedules/recipients", sched, h.recipientOptions)
	r.Put("/schedules/:key", sched, h.updateSchedule)
	r.Post("/schedules/:key/send", sched, h.sendNow)
	r.Get("/runs", sched, h.listRuns)
	r.Get("/runs/:id", sched, h.getRun)
	registerJob(d)
	r.Get("/cutoff", h.cutoff)                                  // RPT-06: data cut-off + unsynchronised sites (screens)
	r.Get("/data/:key", h.hubData)                              // hub reports (JSON; ?format= export) — permission per report
	r.Get("/pdf/export/*", ratelimit.Group("pdf"), h.exportPDF) // RPT-03: PDF of any registered export report
	r.Get("/pdf/:key", ratelimit.Group("pdf"), h.hubPDF)        // hub reports (PDF)
}

type handler struct{ d *deps.Deps }

// catalog: the PDF reports this user may open, grouped. Paths exclude the /api prefix.
func (h *handler) catalog(c *fiber.Ctx) error {
	u := auth.MustUser(c)
	return httpx.JSONCached(c, fiber.Map{"groups": CatalogFor(u.Role, httpx.Lang(c))}, "private, no-cache")
}

func (h *handler) management(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	in, err := dashboard.ParseQuery(c)
	if err != nil {
		return err
	}
	lang := httpx.Lang(c)
	uc := *u
	d, err := dashboard.Build(ctx, h.d.Pool, &uc, lang, in)
	if err != nil {
		return err
	}
	plantID := ""
	if d.Plant != nil {
		plantID = d.Plant.ID
	}
	pending, err := dashboard.Pending(ctx, h.d.Pool, &uc, lang, plantID)
	if err != nil {
		return err
	}
	rep, err := buildManagementPDF(c, h.d, d, pending)
	if err != nil {
		return err
	}
	code := "All-Plants"
	if d.Plant != nil {
		code = d.Plant.Code
	}
	return pdfSend(c, rep, code+"_Management-Summary_"+d.Range.From+"_"+d.Range.To+".pdf")
}

// GET /reports/cutoff?plantId= — the RPT-06 statement for screens (any signed-in user; site scope applies).
func (h *handler) cutoff(c *fiber.Ctx) error {
	u := auth.MustUser(c)
	info, err := cutoff.Load(httpx.Ctx(c), h.d.Pool, auth.PlantFilter(u, c.Query("plantId")), httpx.Lang(c))
	if err != nil {
		return err
	}
	httpx.NoStore(c)
	return c.JSON(info)
}
