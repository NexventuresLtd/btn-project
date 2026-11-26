package jobs

import (
	"errors"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// RegisterAdmin mounts the admin API on r (= /api/ops/jobs), permission admin.monitor:
//
//	GET  /                 jobs with schedule, next run, last runs, scheduler heartbeat
//	GET  /:name/runs       run history (?page=&pageSize=)
//	POST /:name/run        queue a manual run (202)
func RegisterAdmin(r fiber.Router, d *deps.Deps) {
	h := &adminHandler{d}
	perm := auth.RequirePermission("admin.monitor")
	r.Get("/", perm, h.list)
	r.Get("/:name/runs", perm, h.runs)
	r.Post("/:name/run", perm, h.runNow)
}

type adminHandler struct{ d *deps.Deps }

type jobView struct {
	Name           string     `json:"name"`
	Schedule       string     `json:"schedule"`
	Description    string     `json:"description"`
	TimeoutSeconds int        `json:"timeoutSeconds"`
	Retries        int        `json:"retries"`
	NextRunAt      *time.Time `json:"nextRunAt"`
	LastSlotAt     *time.Time `json:"lastSlotAt"`
	LastSuccessAt  *time.Time `json:"lastSuccessAt"`
	Overdue        bool       `json:"overdue"`
	LastRun        *Run       `json:"lastRun"`
	RecentRuns     []Run      `json:"recentRuns"`
}

// SchedulerAlive is true when the leader heartbeat is recent.
func SchedulerAlive(st Stats, now time.Time) bool {
	return st.HeartbeatAt != nil && now.Sub(*st.HeartbeatAt) < 2*time.Minute
}

func (h *adminHandler) list(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	lang := httpx.Lang(c)
	st, err := LoadStats(ctx, h.d.Pool)
	if err != nil {
		return err
	}
	recent, err := RecentRuns(ctx, h.d.Pool, 5)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	items := []jobView{}
	for _, j := range All() {
		v := jobView{Name: j.Name, Schedule: j.Schedule, TimeoutSeconds: int(j.EffectiveTimeout().Seconds()), Retries: j.Retries, RecentRuns: recent[j.Name]}
		if j.DescriptionKey != "" {
			v.Description = i18n.T(lang, j.DescriptionKey)
		}
		if v.RecentRuns == nil {
			v.RecentRuns = []Run{}
		} else {
			v.LastRun = &v.RecentRuns[0]
		}
		slot, ok := st.LastSlots[j.Name]
		if ok {
			s := slot
			v.LastSlotAt = &s
		}
		if t, ok := st.LastSuccess[j.Name]; ok {
			v.LastSuccessAt = &t
		}
		v.NextRunAt = NextRun(j, slot, now)
		v.Overdue = Overdue(j, slot, now, 5*time.Minute)
		items = append(items, v)
	}
	httpx.NoStore(c)
	return c.JSON(fiber.Map{
		"items": items,
		"scheduler": fiber.Map{
			"alive": SchedulerAlive(st, now), "heartbeatAt": st.HeartbeatAt, "instance": st.LeaderInstance,
			"queued": st.Queued, "failures24h": st.Failures24h, "runs24h": st.Runs24h, "timezone": "Africa/Kigali",
		},
	})
}

func (h *adminHandler) runs(c *fiber.Ctx) error {
	name := c.Params("name")
	if _, ok := Lookup(name); !ok {
		return httpx.NotFound("entity.job")
	}
	pg := httpx.Paginate(c, 25)
	items, total, err := Runs(httpx.Ctx(c), h.d.Pool, name, pg.Limit, pg.Offset)
	if err != nil {
		return err
	}
	httpx.NoStore(c)
	return c.JSON(httpx.NewPage(items, total, pg.Page, pg.PageSize))
}

func (h *adminHandler) runNow(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	name := c.Params("name")
	if _, ok := Lookup(name); !ok {
		return httpx.NotFound("entity.job")
	}
	run, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (Run, error) {
		// serialize concurrent "run now" clicks for the same job
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('jobs.manual:' || $1))`, name); err != nil {
			return Run{}, err
		}
		busy, err := HasQueuedManual(ctx, tx, name)
		if err != nil {
			return Run{}, err
		}
		if busy {
			return Run{}, httpx.Conflict("jobs.already_queued")
		}
		r, err := Enqueue(ctx, tx, name, u.ID)
		if err != nil {
			return Run{}, err
		}
		return r, audit.Record(c, tx, audit.Input{Action: "jobs.run", Entity: "Job", EntityID: name, Summary: "Manual run queued: " + name, After: map[string]any{"runId": r.ID}})
	})
	if errors.Is(err, ErrUnknownJob) {
		return httpx.NotFound("entity.job")
	}
	if err != nil {
		return err
	}
	Wake()
	return c.Status(202).JSON(run)
}
