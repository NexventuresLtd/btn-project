package production

// PRD-07 closing a site-day: after close, entries of that site and date are locked for supervisors; later changes go
// through correction requests. Managers close manually (production.close) or the production.day_close job closes
// every open day at the configured Kigali hour; days that still hold drafts are not closed automatically — the
// managers and the site's recorders get a "day not closed" notification instead.
//
//	GET  /days?plantId=&from=&to=          production.view   (≤ 62 days)
//	POST /days/close {plantId, date}       production.close
//	POST /days/reopen {plantId, date, reason} production.close

import (
	"context"
	"fmt"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/notify"
	"btn-ops/internal/stock"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

type dayRow struct {
	PlantID    string  `json:"plantId"`
	Plant      string  `json:"plant"`
	Date       string  `json:"date"`
	Status     string  `json:"status"`
	AutoClosed bool    `json:"autoClosed"`
	ClosedAt   *string `json:"closedAt"`
	ClosedBy   *string `json:"closedBy"`
	ReopenNote *string `json:"reopenReason"`
	Entries    int     `json:"entries"`
	Drafts     int     `json:"drafts"`
	Submitted  int     `json:"submitted"`
	Approved   int     `json:"approved"`
	Rejected   int     `json:"rejected"`
	Produced   int     `json:"produced"`
	Pending    int     `json:"pendingCorrections"`
}

func (h *handler) listDays(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	q := httpx.Query(c)
	from, to, plant := stock.QueryDate(q, "from"), stock.QueryDate(q, "to"), stock.QueryID(q, "plantId")
	if err := q.Err(); err != nil {
		return err
	}
	if from == "" {
		from = dates.ToISO(dates.MustParseISO(dates.TodayISO()).AddDate(0, 0, -13))
	}
	rg := dates.ResolveRange(from, to)
	if rg.To.Sub(rg.From) > 62*24*time.Hour {
		return httpx.BadRequest("error.validation", map[string]string{"from": i18n.T(httpx.Lang(c), "production.v.range_62")})
	}
	plantID := auth.PlantFilter(u, plant)
	rows, err := h.d.Pool.Query(ctx, `WITH days AS (SELECT p.id AS plant, p.code, g::date AS d FROM "Plant" p
			CROSS JOIN generate_series($1::date, $2::date, interval '1 day') g WHERE p.kind = 'SITE' AND p."isActive" AND ($3 = '' OR p.id = $3)),
		counts AS (SELECT r."plantId" AS plant, r."date" AS d, count(*)::int AS n,
				count(*) FILTER (WHERE r.status = 'DRAFT')::int AS dr, count(*) FILTER (WHERE r.status = 'SUBMITTED')::int AS sb,
				count(*) FILTER (WHERE r.status = 'APPROVED')::int AS ap, count(*) FILTER (WHERE r.status = 'REJECTED')::int AS rj,
				COALESCE(SUM((SELECT SUM(l."producedQty") FROM "ProductionLine" l WHERE l."reportId" = r.id)) FILTER (WHERE r.status IN ('SUBMITTED','APPROVED')), 0)::int AS prod
			FROM "ProductionReport" r WHERE r."date" BETWEEN $1 AND $2 AND ($3 = '' OR r."plantId" = $3) GROUP BY 1, 2),
		pend AS (SELECT c."plantId" AS plant, r."date" AS d, count(*)::int AS n FROM production_correction c JOIN "ProductionReport" r ON r.id = c."reportId"
			WHERE c.status = 'PENDING' AND r."date" BETWEEN $1 AND $2 GROUP BY 1, 2)
		SELECT days.plant, days.code, days.d, COALESCE(pd.status, 'OPEN'), COALESCE(pd."autoClosed", false), pd."closedAt", cu.name, pd."reopenReason",
			COALESCE(counts.n, 0), COALESCE(counts.dr, 0), COALESCE(counts.sb, 0), COALESCE(counts.ap, 0), COALESCE(counts.rj, 0), COALESCE(counts.prod, 0), COALESCE(pend.n, 0)
		FROM days LEFT JOIN production_day pd ON pd."plantId" = days.plant AND pd.date = days.d
		LEFT JOIN "User" cu ON cu.id = pd."closedById"
		LEFT JOIN counts ON counts.plant = days.plant AND counts.d = days.d
		LEFT JOIN pend ON pend.plant = days.plant AND pend.d = days.d
		ORDER BY days.d DESC, days.code`, rg.From, rg.To, plantID)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []dayRow{}
	for rows.Next() {
		var r dayRow
		var d time.Time
		var closedAt *time.Time
		if err := rows.Scan(&r.PlantID, &r.Plant, &d, &r.Status, &r.AutoClosed, &closedAt, &r.ClosedBy, &r.ReopenNote, &r.Entries, &r.Drafts, &r.Submitted,
			&r.Approved, &r.Rejected, &r.Produced, &r.Pending); err != nil {
			return err
		}
		r.Date, r.ClosedAt = dates.ToISO(d), isoTimePtr(closedAt)
		if r.Status != "CLOSED" {
			r.ClosedAt, r.ClosedBy = nil, nil
		}
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	set := loadSettings(ctx, h.d.Pool)
	return httpx.JSONCached(c, fiber.Map{"items": items, "from": rg.FromISO, "to": rg.ToISO, "settings": set}, cacheControl)
}

func (h *handler) dayInput(c *fiber.Ctx, needReason bool) (plantID string, date time.Time, reason string, err error) {
	v, err := httpx.Body(c)
	if err != nil {
		return
	}
	lang := httpx.Lang(c)
	plantID = stock.IDField(v, "plantId")
	iso := stock.PastOrToday(v, "date")
	if needReason {
		if _, ok := v.Raw("reason").(string); !ok {
			v.Fail("reason", "production.v.reopen_reason")
		} else {
			reason = v.Str("reason").Trim().Min(5, "production.v.reopen_reason").Max(500, atMost(lang, 500)).Get()
		}
	}
	if err = v.Err(); err != nil {
		return
	}
	if err = auth.AssertPlantAccess(auth.MustUser(c), plantID); err != nil {
		return
	}
	var ok bool
	if e := h.d.Pool.QueryRow(httpx.Ctx(c), `SELECT kind = 'SITE' FROM "Plant" WHERE id = $1`, plantID).Scan(&ok); e != nil || !ok {
		err = httpx.BadRequest("production.err.choose_plant", map[string]string{"plantId": i18n.T(lang, "production.err.choose_plant")})
		return
	}
	date = dates.MustParseISO(iso)
	return
}

// closeDay marks a site-day closed (idempotent: false when it already was). who="" = the scheduler.
func closeDay(ctx context.Context, tx pgx.Tx, plantID string, date time.Time, who string, auto bool) (bool, error) {
	tag, err := tx.Exec(ctx, `INSERT INTO production_day (id, "plantId", date, status, "autoClosed", "closedById", "closedAt", "updatedAt")
		VALUES ($1, $2, $3, 'CLOSED', $4, $5, now(), now())
		ON CONFLICT ("plantId", date) DO UPDATE SET status = 'CLOSED', "autoClosed" = EXCLUDED."autoClosed", "closedById" = EXCLUDED."closedById",
			"closedAt" = now(), "updatedAt" = now() WHERE production_day.status <> 'CLOSED'`, dayID(plantID, date), plantID, date, auto, nilIf(who))
	return tag.RowsAffected() > 0, err
}

func (h *handler) closeDayH(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	plantID, date, _, err := h.dayInput(c, false)
	if err != nil {
		return err
	}
	var drafts int
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		changed, err := closeDay(ctx, tx, plantID, date, u.ID, false)
		if err != nil {
			return err
		}
		if !changed {
			return httpx.Conflict("production.err.day_already_closed")
		}
		if err := tx.QueryRow(ctx, `SELECT count(*)::int FROM "ProductionReport" WHERE "plantId" = $1 AND "date" = $2 AND status = 'DRAFT'`, plantID, date).Scan(&drafts); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "production.day_close", Entity: "ProductionDay", EntityID: dayID(plantID, date),
			Summary: fmt.Sprintf("Closed production day %s (%d draft(s) left)", dates.FormatDate(date), drafts), After: map[string]any{"status": "CLOSED", "drafts": drafts}})
	})
	if err != nil {
		return err
	}
	InvalidateSummary()
	return c.JSON(fiber.Map{"plantId": plantID, "date": dates.ToISO(date), "status": "CLOSED", "drafts": drafts})
}

func (h *handler) reopenDayH(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	plantID, date, reason, err := h.dayInput(c, true)
	if err != nil {
		return err
	}
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE production_day SET status = 'OPEN', "reopenedById" = $3, "reopenedAt" = now(), "reopenReason" = $4, "updatedAt" = now()
			WHERE "plantId" = $1 AND date = $2 AND status = 'CLOSED'`, plantID, date, u.ID, reason)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.Conflict("production.err.day_not_closed")
		}
		return audit.Record(c, tx, audit.Input{Action: "production.day_reopen", Entity: "ProductionDay", EntityID: dayID(plantID, date),
			Summary: "Reopened production day " + dates.FormatDate(date) + ": " + reason, Before: map[string]string{"status": "CLOSED"}, After: map[string]string{"status": "OPEN", "reason": reason}})
	})
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"plantId": plantID, "date": dates.ToISO(date), "status": "OPEN"})
}

// AutoClose closes open site-days that are due (job production.day_close). A day is due when it is before today,
// or today once the Kigali hour reached settings.AutoCloseHour. Only days with entries in the last 30 days are
// considered; days holding drafts are left open and reported (one notification per site-day until read).
func AutoClose(ctx context.Context, d *deps.Deps, now time.Time) (closed, reminded int, err error) {
	set := loadSettings(ctx, d.Pool)
	if !set.AutoCloseEnabled {
		return 0, 0, nil
	}
	k := now.In(dates.Kigali())
	today := dates.MustParseISO(dates.TodayISO(now))
	last := today.AddDate(0, 0, -1)
	if k.Hour() >= set.AutoCloseHour {
		last = today
	}
	type due struct {
		plant, code string
		date        time.Time
		drafts      int
	}
	var list []due
	rows, err := d.Pool.Query(ctx, `SELECT r."plantId", p.code, r."date", count(*) FILTER (WHERE r.status = 'DRAFT')::int
		FROM "ProductionReport" r JOIN "Plant" p ON p.id = r."plantId"
		LEFT JOIN production_day pd ON pd."plantId" = r."plantId" AND pd.date = r."date"
		WHERE r."date" BETWEEN $1 AND $2 AND COALESCE(pd.status, 'OPEN') = 'OPEN' AND pd."reopenedAt" IS NULL
		GROUP BY 1, 2, 3 ORDER BY 3, 2`, today.AddDate(0, 0, -30), last)
	if err != nil {
		return 0, 0, err
	}
	for rows.Next() {
		var x due
		if err := rows.Scan(&x.plant, &x.code, &x.date, &x.drafts); err != nil {
			rows.Close()
			return 0, 0, err
		}
		list = append(list, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	for _, x := range list {
		err := db.Tx(ctx, d.Pool, func(tx pgx.Tx) error {
			if x.drafts > 0 {
				reminded++
				return notify.Send(ctx, tx, notify.Notification{Event: EventDayNotClosed, Severity: notify.Warning,
					TitleKey: "production.notify.day_open_title", BodyKey: "production.notify.day_open_body",
					Params: map[string]any{"plant": x.code, "date": dates.FormatDate(x.date), "drafts": x.drafts}, Link: "/production/days",
					To: notify.Audience{Permission: "production.close", PlantID: x.plant}, DedupKey: dayID(x.plant, x.date)})
			}
			changed, err := closeDay(ctx, tx, x.plant, x.date, "", true)
			if err != nil || !changed {
				return err
			}
			closed++
			return audit.Log(ctx, tx, audit.Actor{}, audit.Input{Action: "production.day_close", Entity: "ProductionDay", EntityID: dayID(x.plant, x.date),
				Summary: "Production day " + dates.FormatDate(x.date) + " at " + x.code + " closed automatically", After: map[string]any{"status": "CLOSED", "auto": true}})
		})
		if err != nil {
			return closed, reminded, err
		}
		if x.drafts > 0 {
			// recorders of the site are reminded as well (separate audience: the record permission at the site)
			_ = db.Tx(ctx, d.Pool, func(tx pgx.Tx) error {
				return notify.Send(ctx, tx, notify.Notification{Event: EventDayNotClosed, Severity: notify.Warning,
					TitleKey: "production.notify.day_open_title", BodyKey: "production.notify.day_open_body",
					Params: map[string]any{"plant": x.code, "date": dates.FormatDate(x.date), "drafts": x.drafts}, Link: "/production",
					To: notify.ToPermissionAtSite("production.record", x.plant), DedupKey: dayID(x.plant, x.date)})
			})
		}
	}
	if closed > 0 {
		InvalidateSummary()
	}
	return closed, reminded, nil
}
