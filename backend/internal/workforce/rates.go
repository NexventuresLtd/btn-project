package workforce

// Rate rules and piece rates (SRS WRK-03): administrator-managed, effective dated, audited. A rule applies to
// every worker, a site, a job title, one worker, or a combination; the most specific rule wins (calc.go).

import (
	"fmt"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

type ruleOut struct {
	ID                 string    `json:"id"`
	Name               string    `json:"name"`
	Plant              *plantRef `json:"plant"`
	JobTitle           *string   `json:"jobTitle"`
	WorkerID           *string   `json:"workerId"`
	WorkerName         *string   `json:"workerName"`
	DailyRate          *int      `json:"dailyRate"`
	HalfDayRate        *int      `json:"halfDayRate"`
	OvertimeRate       *int      `json:"overtimeRate"`
	OvertimeMultiplier float64   `json:"overtimeMultiplier"`
	StandardHours      float64   `json:"standardHours"`
	EffectiveFrom      string    `json:"effectiveFrom"`
	EffectiveTo        *string   `json:"effectiveTo"`
	IsActive           bool      `json:"isActive"`
	Note               *string   `json:"note"`
	ApprovedBy         *string   `json:"approvedBy"`
	ApprovedAt         *string   `json:"approvedAt"`
	UpdatedAt          string    `json:"updatedAt"`
}

const ruleSelect = `SELECT r.id, r.name, p.id, p.code, p.name, r.job_title, r.worker_id, w."firstName", w."lastName", r.daily_rate, r.half_day_rate, r.overtime_rate,
	r.overtime_multiplier::float8, r.standard_hours::float8, r.effective_from, r.effective_to, r.is_active, r.note, u.name, r.approved_at, r.updated_at
	FROM wage_rate_rule r LEFT JOIN "Plant" p ON p.id = r.plant_id LEFT JOIN "Worker" w ON w.id = r.worker_id LEFT JOIN "User" u ON u.id = r.approved_by`

func scanRule(row pgx.Row) (ruleOut, error) {
	var o ruleOut
	var pid, pcode, pname, wf, wl *string
	var from time.Time
	var to, aat *time.Time
	var upd time.Time
	err := row.Scan(&o.ID, &o.Name, &pid, &pcode, &pname, &o.JobTitle, &o.WorkerID, &wf, &wl, &o.DailyRate, &o.HalfDayRate, &o.OvertimeRate,
		&o.OvertimeMultiplier, &o.StandardHours, &from, &to, &o.IsActive, &o.Note, &o.ApprovedBy, &aat, &upd)
	if err != nil {
		return o, err
	}
	if pid != nil {
		o.Plant = &plantRef{ID: *pid, Code: *pcode, Name: *pname}
	}
	if wf != nil {
		n := fullName(*wf, *wl)
		o.WorkerName = &n
	}
	o.EffectiveFrom, o.EffectiveTo, o.UpdatedAt = dates.ToISO(from), isoDatePtr(to), isoTime(upd)
	if aat != nil {
		s := isoTime(*aat)
		o.ApprovedAt = &s
	}
	return o, nil
}

func (h *handler) listRules(c *fiber.Ctx) error {
	qv := httpx.Query(c)
	all := qv.OptStr("all").Get() == "1"
	if err := qv.Err(); err != nil {
		return err
	}
	where := ` WHERE r.is_active`
	if all {
		where = ""
	}
	rows, err := h.d.Pool.Query(reqCtx(c), ruleSelect+where+` ORDER BY r.is_active DESC, r.effective_from DESC, r.name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []ruleOut{}
	for rows.Next() {
		o, err := scanRule(rows)
		if err != nil {
			return err
		}
		items = append(items, o)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"items": items, "defaults": fiber.Map{"overtimeMultiplier": DefaultOvertimeMultiplier, "standardHours": DefaultStandardHours}})
}

type ruleIn struct {
	Name                              string
	PlantID, JobTitle, WorkerID, Note *string
	Daily, Half, OT                   *int
	Mult, Std                         float64
	From                              time.Time
	To                                *time.Time
	Active                            bool
}

func optMoney(v *httpx.V, key string) *int {
	n := v.NullNum(key).Coerce().Whole("v.whole_francs").Min(0, "workforce.v.money").Max(1_000_000, "workforce.v.money")
	if !n.Present() || v.Raw(key) == nil || v.Raw(key) == "" {
		return nil
	}
	x := int(n.I())
	return &x
}

func (h *handler) parseRule(c *fiber.Ctx) (ruleIn, error) {
	v, err := httpx.Body(c)
	if err != nil {
		return ruleIn{}, err
	}
	var in ruleIn
	in.Name = v.Str("name").Trim().Min(1, "v.required").Max(80, "workforce.v.max_80").Get()
	in.PlantID = v.NullStr("plantId").Trim().Max(40, "workforce.v.max_80").NilIfEmpty()
	in.JobTitle = v.NullStr("jobTitle").Trim().Max(60, "workforce.v.max_80").NilIfEmpty()
	in.WorkerID = v.NullStr("workerId").Trim().Max(40, "workforce.v.max_80").NilIfEmpty()
	in.Note = v.NullStr("note").Trim().Max(300, "workforce.v.max_300").NilIfEmpty()
	in.Daily, in.Half, in.OT = optMoney(v, "dailyRate"), optMoney(v, "halfDayRate"), optMoney(v, "overtimeRate")
	in.Mult = v.OptNum("overtimeMultiplier").Coerce().Min(0, "workforce.v.multiplier").Max(10, "workforce.v.multiplier").Default(DefaultOvertimeMultiplier).F()
	in.Std = v.OptNum("standardHours").Coerce().Gt(0, "workforce.v.hours").Max(24, "workforce.v.hours").Default(DefaultStandardHours).F()
	fromS := v.Str("effectiveFrom").Trim().Date("workforce.v.date").Get()
	toS := v.NullStr("effectiveTo").Trim().Date("workforce.v.date").Get()
	in.Active = v.OptBool("isActive").Default(true).Get()
	if err := v.Err(); err != nil {
		return in, err
	}
	in.From = dates.MustParseISO(fromS)
	if toS != "" {
		to := dates.MustParseISO(toS)
		if to.Before(in.From) {
			return in, httpx.BadRequest("error.validation", map[string]string{"effectiveTo": t(c, "workforce.v.to_before_from")})
		}
		in.To = &to
	}
	ctx := reqCtx(c)
	if in.PlantID != nil {
		if _, err := loadSite(c, h.d.Pool, *in.PlantID); err != nil {
			return in, err
		}
	}
	if in.WorkerID != nil {
		var ok bool
		if err := h.d.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM "Worker" WHERE id = $1)`, *in.WorkerID).Scan(&ok); err != nil {
			return in, err
		}
		if !ok {
			return in, httpx.BadRequest("error.validation", map[string]string{"workerId": t(c, "workforce.err.unknown_worker")})
		}
	}
	return in, nil
}

func ruleSummary(in ruleIn) string {
	m := func(p *int) string {
		if p == nil {
			return "default"
		}
		return format.RWF(float64(*p))
	}
	to := "open"
	if in.To != nil {
		to = dates.ToISO(*in.To)
	}
	return fmt.Sprintf("%s: daily %s, half %s, overtime %s (×%.2f / %.2f h), %s – %s", in.Name, m(in.Daily), m(in.Half), m(in.OT), in.Mult, in.Std, dates.ToISO(in.From), to)
}

func (h *handler) createRule(c *fiber.Ctx) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	in, err := h.parseRule(c)
	if err != nil {
		return err
	}
	out, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (ruleOut, error) {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO wage_rate_rule (name, plant_id, job_title, worker_id, daily_rate, half_day_rate, overtime_rate, overtime_multiplier, standard_hours,
			effective_from, effective_to, is_active, note, approved_by, approved_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,now()) RETURNING id`,
			in.Name, in.PlantID, in.JobTitle, in.WorkerID, in.Daily, in.Half, in.OT, in.Mult, in.Std, in.From, in.To, in.Active, in.Note, u.ID).Scan(&id); err != nil {
			return ruleOut{}, err
		}
		o, err := scanRule(tx.QueryRow(ctx, ruleSelect+` WHERE r.id = $1`, id))
		if err != nil {
			return o, err
		}
		return o, audit.Record(c, tx, audit.Input{Action: "workforce.rate_rule.create", Entity: "WageRateRule", EntityID: id, Summary: "Approved wage rate rule " + ruleSummary(in), After: o})
	})
	if err != nil {
		return err
	}
	return c.Status(201).JSON(out)
}

func (h *handler) updateRule(c *fiber.Ctx) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	id := c.Params("id")
	before, err := scanRule(h.d.Pool.QueryRow(ctx, ruleSelect+` WHERE r.id = $1`, id))
	if db.IsNoRows(err) {
		return httpx.NotFound("workforce.entity.rule")
	}
	if err != nil {
		return err
	}
	in, err := h.parseRule(c)
	if err != nil {
		return err
	}
	out, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (ruleOut, error) {
		if _, err := tx.Exec(ctx, `UPDATE wage_rate_rule SET name=$2, plant_id=$3, job_title=$4, worker_id=$5, daily_rate=$6, half_day_rate=$7, overtime_rate=$8,
			overtime_multiplier=$9, standard_hours=$10, effective_from=$11, effective_to=$12, is_active=$13, note=$14, approved_by=$15, approved_at=now(), updated_at=now() WHERE id=$1`,
			id, in.Name, in.PlantID, in.JobTitle, in.WorkerID, in.Daily, in.Half, in.OT, in.Mult, in.Std, in.From, in.To, in.Active, in.Note, u.ID); err != nil {
			return ruleOut{}, err
		}
		o, err := scanRule(tx.QueryRow(ctx, ruleSelect+` WHERE r.id = $1`, id))
		if err != nil {
			return o, err
		}
		return o, audit.Record(c, tx, audit.Input{Action: "workforce.rate_rule.update", Entity: "WageRateRule", EntityID: id, Summary: "Changed wage rate rule " + ruleSummary(in), Before: before, After: o})
	})
	if err != nil {
		return err
	}
	return c.JSON(out)
}

// previewRates: GET /rate-rules/preview?workerId&date[&plantId] → the rule and rates that apply.
func (h *handler) previewRates(c *fiber.Ctx) error {
	ctx := reqCtx(c)
	qv := httpx.Query(c)
	wid := qv.Str("workerId").Trim().Min(1, "v.required").Get()
	ds := qv.OptStr("date").Trim().Date("workforce.v.date").Get()
	plantID := qv.OptStr("plantId").Trim().Get()
	if err := qv.Err(); err != nil {
		return err
	}
	if ds == "" {
		ds = dates.TodayISO()
	}
	date := dates.MustParseISO(ds)
	ws, err := LoadWorkers(ctx, h.d.Pool, []string{wid})
	if err != nil {
		return err
	}
	w, ok := ws[wid]
	if !ok {
		return httpx.NotFound("entity.worker")
	}
	if err := auth.AssertPlantAccess(auth.MustUser(c), w.PlantID); err != nil {
		return err
	}
	if plantID == "" {
		plantID = w.PlantID
	}
	rules, err := LoadRules(ctx, h.d.Pool, date, date)
	if err != nil {
		return err
	}
	r, matched := RatesOn(rules, w, plantID, date)
	return c.JSON(fiber.Map{"workerId": wid, "date": ds, "plantId": plantID, "rateBasis": w.RateBasis, "rates": r, "matched": matched})
}

// ── piece rates ─────────────────────────────────────────────────────────────

type pieceRateOut struct {
	ID            string    `json:"id"`
	Product       plantRef  `json:"product"`
	Plant         *plantRef `json:"plant"`
	Rate          int       `json:"rate"`
	EffectiveFrom string    `json:"effectiveFrom"`
	EffectiveTo   *string   `json:"effectiveTo"`
	IsActive      bool      `json:"isActive"`
	Note          *string   `json:"note"`
	ApprovedBy    *string   `json:"approvedBy"`
	UpdatedAt     string    `json:"updatedAt"`
}

const pieceSelect = `SELECT r.id, pr.id, pr.code, pr.name, p.id, p.code, p.name, r.rate, r.effective_from, r.effective_to, r.is_active, r.note, u.name, r.updated_at
	FROM piece_rate r JOIN "Product" pr ON pr.id = r.product_id LEFT JOIN "Plant" p ON p.id = r.plant_id LEFT JOIN "User" u ON u.id = r.approved_by`

func scanPieceRate(row pgx.Row) (pieceRateOut, error) {
	var o pieceRateOut
	var pid, pcode, pname *string
	var from, upd time.Time
	var to *time.Time
	if err := row.Scan(&o.ID, &o.Product.ID, &o.Product.Code, &o.Product.Name, &pid, &pcode, &pname, &o.Rate, &from, &to, &o.IsActive, &o.Note, &o.ApprovedBy, &upd); err != nil {
		return o, err
	}
	if pid != nil {
		o.Plant = &plantRef{ID: *pid, Code: *pcode, Name: *pname}
	}
	o.EffectiveFrom, o.EffectiveTo, o.UpdatedAt = dates.ToISO(from), isoDatePtr(to), isoTime(upd)
	return o, nil
}

func (h *handler) listPieceRates(c *fiber.Ctx) error {
	where := ` WHERE r.is_active`
	if c.Query("all") == "1" {
		where = ""
	}
	rows, err := h.d.Pool.Query(reqCtx(c), pieceSelect+where+` ORDER BY pr."sortOrder", pr.code, r.effective_from DESC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []pieceRateOut{}
	for rows.Next() {
		o, err := scanPieceRate(rows)
		if err != nil {
			return err
		}
		items = append(items, o)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"items": items})
}

type pieceIn struct {
	ProductID string
	PlantID   *string
	Rate      int
	From      time.Time
	To        *time.Time
	Active    bool
	Note      *string
}

func (h *handler) parsePieceRate(c *fiber.Ctx) (pieceIn, error) {
	v, err := httpx.Body(c)
	if err != nil {
		return pieceIn{}, err
	}
	var in pieceIn
	in.ProductID = v.Str("productId").Trim().Min(1, "v.required").Max(40, "workforce.v.max_80").Get()
	in.PlantID = v.NullStr("plantId").Trim().Max(40, "workforce.v.max_80").NilIfEmpty()
	in.Rate = int(v.Num("rate").Coerce().Whole("v.whole_francs").Min(0, "workforce.v.money").Max(1_000_000, "workforce.v.money").I())
	fromS := v.Str("effectiveFrom").Trim().Date("workforce.v.date").Get()
	toS := v.NullStr("effectiveTo").Trim().Date("workforce.v.date").Get()
	in.Active = v.OptBool("isActive").Default(true).Get()
	in.Note = v.NullStr("note").Trim().Max(300, "workforce.v.max_300").NilIfEmpty()
	if err := v.Err(); err != nil {
		return in, err
	}
	in.From = dates.MustParseISO(fromS)
	if toS != "" {
		to := dates.MustParseISO(toS)
		if to.Before(in.From) {
			return in, httpx.BadRequest("error.validation", map[string]string{"effectiveTo": t(c, "workforce.v.to_before_from")})
		}
		in.To = &to
	}
	var ok bool
	if err := h.d.Pool.QueryRow(reqCtx(c), `SELECT EXISTS (SELECT 1 FROM "Product" WHERE id = $1)`, in.ProductID).Scan(&ok); err != nil {
		return in, err
	}
	if !ok {
		return in, httpx.BadRequest("error.validation", map[string]string{"productId": t(c, "workforce.err.unknown_product")})
	}
	if in.PlantID != nil {
		if _, err := loadSite(c, h.d.Pool, *in.PlantID); err != nil {
			return in, err
		}
	}
	return in, nil
}

func (h *handler) createPieceRate(c *fiber.Ctx) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	in, err := h.parsePieceRate(c)
	if err != nil {
		return err
	}
	out, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (pieceRateOut, error) {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO piece_rate (product_id, plant_id, rate, effective_from, effective_to, is_active, note, approved_by, approved_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now()) RETURNING id`, in.ProductID, in.PlantID, in.Rate, in.From, in.To, in.Active, in.Note, u.ID).Scan(&id); err != nil {
			return pieceRateOut{}, err
		}
		o, err := scanPieceRate(tx.QueryRow(ctx, pieceSelect+` WHERE r.id = $1`, id))
		if err != nil {
			return o, err
		}
		return o, audit.Record(c, tx, audit.Input{Action: "workforce.piece_rate.create", Entity: "PieceRate", EntityID: id,
			Summary: fmt.Sprintf("Approved piece rate %s %s per unit from %s", o.Product.Code, format.RWF(float64(in.Rate)), o.EffectiveFrom), After: o})
	})
	if err != nil {
		return err
	}
	return c.Status(201).JSON(out)
}

func (h *handler) updatePieceRate(c *fiber.Ctx) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	id := c.Params("id")
	before, err := scanPieceRate(h.d.Pool.QueryRow(ctx, pieceSelect+` WHERE r.id = $1`, id))
	if db.IsNoRows(err) {
		return httpx.NotFound("workforce.entity.piece_rate")
	}
	if err != nil {
		return err
	}
	in, err := h.parsePieceRate(c)
	if err != nil {
		return err
	}
	out, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (pieceRateOut, error) {
		if _, err := tx.Exec(ctx, `UPDATE piece_rate SET product_id=$2, plant_id=$3, rate=$4, effective_from=$5, effective_to=$6, is_active=$7, note=$8, approved_by=$9,
			approved_at=now(), updated_at=now() WHERE id=$1`, id, in.ProductID, in.PlantID, in.Rate, in.From, in.To, in.Active, in.Note, u.ID); err != nil {
			return pieceRateOut{}, err
		}
		o, err := scanPieceRate(tx.QueryRow(ctx, pieceSelect+` WHERE r.id = $1`, id))
		if err != nil {
			return o, err
		}
		return o, audit.Record(c, tx, audit.Input{Action: "workforce.piece_rate.update", Entity: "PieceRate", EntityID: id,
			Summary: fmt.Sprintf("Changed piece rate %s to %s per unit", o.Product.Code, format.RWF(float64(in.Rate))), Before: before, After: o})
	})
	if err != nil {
		return err
	}
	return c.JSON(out)
}
