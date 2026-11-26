package payroll

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"btn-ops/internal/attendance"
	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/workforce"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// ───────────────────────────── Period reads ─────────────────────────────

var statuses = []string{"DRAFT", "PREPARED", "CHECKED", "VERIFIED", "AUTHORIZED", "PAID"}

func validStatus(s string) bool {
	for _, x := range statuses {
		if x == s {
			return true
		}
	}
	return false
}

// periodFilter builds the WHERE for period lists (alias p). Only placeholders are interpolated.
func periodFilter(plantID, status string, year int, withStatus bool) (string, []any) {
	where := []string{"TRUE"}
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args))))
	}
	if plantID != "" {
		add(`p."plantId" = ?`, plantID)
	}
	if withStatus && status != "" {
		add(`p.status = ?::"PayrollStatus"`, status)
	}
	if year != 0 {
		add(`p."startDate" >= ?`, time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC))
		add(`p."startDate" < ?`, time.Date(year+1, 1, 1, 0, 0, 0, 0, time.UTC))
	}
	return strings.Join(where, " AND "), args
}

const lineAggSQL = `count(l.id), count(l.id) FILTER (WHERE l."daysWorked" > 0), COALESCE(sum(l."daysWorked"), 0)::float8,
	COALESCE(sum(l."grossAmount"), 0), COALESCE(sum(l."foodDeduction"), 0), COALESCE(sum(l."otherDeduction"), 0), COALESCE(sum(l."netAmount"), 0)`

type aggRow struct {
	Lines, Workers          int
	Days                    float64
	Gross, Food, Other, Net int
}

func (a *aggRow) dest() []any {
	return []any{&a.Lines, &a.Workers, &a.Days, &a.Gross, &a.Food, &a.Other, &a.Net}
}

func (a aggRow) totals() Totals {
	return Totals{Lines: a.Lines, Workers: a.Workers, Days: a.Days, Gross: a.Gross, Food: a.Food, Other: a.Other, Deductions: a.Food + a.Other, Net: a.Net}
}

type periodRow struct {
	ID            string   `json:"id"`
	Number        string   `json:"number"`
	Status        string   `json:"status"`
	StartDate     string   `json:"startDate"`
	EndDate       string   `json:"endDate"`
	Label         string   `json:"label"`
	UpdatedAt     string   `json:"updatedAt"`
	RejectComment *string  `json:"rejectComment"`
	Plant         plantRef `json:"plant"`
	Totals        Totals   `json:"totals"`
}

func (h *handler) listPeriods(c *fiber.Ctx) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	qv := httpx.Query(c)
	plantQ := qv.OptStr("plantId").Get()
	status := qv.OptStr("status").Get()
	if status != "" && !validStatus(status) {
		qv.Fail("status", "payroll.v.invalid_input")
	}
	year := 0
	if ys, _ := qv.Raw("year").(string); ys != "" {
		f, err := strconv.ParseFloat(strings.TrimSpace(ys), 64)
		if err != nil || f != math.Trunc(f) || f < 2000 || f > 2100 {
			qv.Fail("year", "payroll.v.invalid_input")
		} else {
			year = int(f)
		}
	}
	if err := qv.Err(); err != nil {
		return err
	}
	pg := httpx.Paginate(c, 25)
	plantID := auth.PlantFilter(u, plantQ)
	lg := lang(c)
	key := fmt.Sprintf("payroll:periods:%s:%s:%s:%s:%d:%d:%d", lg, scopeKey(u), plantID, status, year, pg.Page, pg.PageSize)
	body, err := jsonCache.GetOrLoad(ctx, key, func(ctx context.Context) ([]byte, error) {
		where, args := periodFilter(plantID, status, year, true)
		whereNS, argsNS := periodFilter(plantID, status, year, false)
		b := &pgx.Batch{}
		b.Queue(`SELECT count(*) FROM "PayrollPeriod" p WHERE `+where, args...)
		b.Queue(`SELECT p.id, p.number, p.status::text, p."startDate", p."endDate", p."updatedAt", p."rejectComment", pl.id, pl.code, pl.name, `+lineAggSQL+`
			FROM "PayrollPeriod" p JOIN "Plant" pl ON pl.id = p."plantId"
			LEFT JOIN "PayrollLine" l ON l."periodId" = p.id
			WHERE `+where+` GROUP BY p.id, pl.id ORDER BY p."startDate" DESC, pl.code ASC, p.id LIMIT `+strconv.Itoa(pg.Limit)+` OFFSET `+strconv.Itoa(pg.Offset), args...)
		b.Queue(`SELECT p.status::text, count(*) FROM "PayrollPeriod" p WHERE `+whereNS+` GROUP BY p.status`, argsNS...)
		b.Queue(`SELECT `+lineAggSQL+` FROM "PayrollLine" l JOIN "PayrollPeriod" p ON p.id = l."periodId" WHERE `+where, args...)
		br := h.d.Pool.SendBatch(ctx, b)
		defer br.Close()
		var total int
		if err := br.QueryRow().Scan(&total); err != nil {
			return nil, err
		}
		rows, err := br.Query()
		if err != nil {
			return nil, err
		}
		items := []periodRow{}
		for rows.Next() {
			var r periodRow
			var sd, ed, ua time.Time
			var a aggRow
			if err := rows.Scan(append([]any{&r.ID, &r.Number, &r.Status, &sd, &ed, &ua, &r.RejectComment, &r.Plant.ID, &r.Plant.Code, &r.Plant.Name}, a.dest()...)...); err != nil {
				rows.Close()
				return nil, err
			}
			r.StartDate, r.EndDate, r.UpdatedAt = dates.ToISO(sd), dates.ToISO(ed), isoTime(ua)
			r.Label = FormatPeriodRange(lg, sd, ed, false)
			r.Totals = a.totals()
			items = append(items, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		srows, err := br.Query()
		if err != nil {
			return nil, err
		}
		counts := map[string]int{}
		for srows.Next() {
			var s string
			var n int
			if err := srows.Scan(&s, &n); err != nil {
				srows.Close()
				return nil, err
			}
			counts[s] = n
		}
		srows.Close()
		if err := srows.Err(); err != nil {
			return nil, err
		}
		var all aggRow
		if err := br.QueryRow().Scan(all.dest()...); err != nil {
			return nil, err
		}
		return json.Marshal(fiber.Map{"items": items, "total": total, "statusCounts": counts, "totals": all.totals(), "page": pg.Page, "pageSize": pg.PageSize})
	}, cacheTag)
	if err != nil {
		return err
	}
	return httpx.BytesCached(c, body, "application/json; charset=utf-8", "private, no-cache")
}

// ── period detail ───────────────────────────────────────────────────────────

type periodHead struct {
	ID, Number, Status, PlantID                             string
	Start, End                                              time.Time
	PreparedBy, CheckedBy, VerifiedBy, AuthorizedBy, PaidBy *string
	PreparedAt, CheckedAt, VerifiedAt, AuthorizedAt, PaidAt *time.Time
	RejectComment                                           *string
	CreatedAt, UpdatedAt                                    time.Time
	Plant                                                   plantRef
}

const headCols = `p.id, p.number, p.status::text, p."plantId", p."startDate", p."endDate",
	p."preparedById", p."preparedAt", p."checkedById", p."checkedAt", p."verifiedById", p."verifiedAt", p."authorizedById", p."authorizedAt", p."paidById", p."paidAt",
	p."rejectComment", p."createdAt", p."updatedAt", pl.id, pl.code, pl.name`

func loadHead(ctx context.Context, q db.DBTX, id string, lock bool) (*periodHead, error) {
	var p periodHead
	sql := `SELECT ` + headCols + ` FROM "PayrollPeriod" p JOIN "Plant" pl ON pl.id = p."plantId" WHERE p.id = $1`
	if lock {
		sql += ` FOR UPDATE OF p`
	}
	err := q.QueryRow(ctx, sql, id).Scan(&p.ID, &p.Number, &p.Status, &p.PlantID, &p.Start, &p.End,
		&p.PreparedBy, &p.PreparedAt, &p.CheckedBy, &p.CheckedAt, &p.VerifiedBy, &p.VerifiedAt, &p.AuthorizedBy, &p.AuthorizedAt, &p.PaidBy, &p.PaidAt,
		&p.RejectComment, &p.CreatedAt, &p.UpdatedAt, &p.Plant.ID, &p.Plant.Code, &p.Plant.Name)
	if db.IsNoRows(err) {
		return nil, httpx.NotFound("entity.wage_sheet")
	}
	return &p, err
}

func (p *periodHead) state(worked int) ApprovalState {
	s := func(x *string) string {
		if x == nil {
			return ""
		}
		return *x
	}
	return ApprovalState{Status: p.Status, PreparedBy: s(p.PreparedBy), CheckedBy: s(p.CheckedBy), VerifiedBy: s(p.VerifiedBy), AuthorizedBy: s(p.AuthorizedBy), WorkedLines: worked}
}

func periodTotals(ctx context.Context, q db.DBTX, id string) (Totals, error) {
	var a aggRow
	err := q.QueryRow(ctx, `SELECT `+lineAggSQL+` FROM "PayrollLine" l WHERE l."periodId" = $1`, id).Scan(a.dest()...)
	return a.totals(), err
}

type lineWorker struct {
	ID               string  `json:"id"`
	FirstName        string  `json:"firstName"`
	LastName         string  `json:"lastName"`
	NationalID       *string `json:"nationalId"`
	MomoNumber       *string `json:"momoNumber"`
	MomoName         *string `json:"momoName"`
	JobTitle         string  `json:"jobTitle"`
	IsActive         bool    `json:"isActive"`
	PlantID          string  `json:"plantId"`
	FullName         string  `json:"fullName"`
	MomoNameMismatch bool    `json:"momoNameMismatch"`
	RateBasis        string  `json:"rateBasis"`
	Status           string  `json:"employmentStatus"`
}

type lineOut struct {
	ID             string     `json:"id"`
	WorkerID       string     `json:"workerId"`
	DailyWage      int        `json:"dailyWage"`
	DaysWorked     float64    `json:"daysWorked"`
	GrossAmount    int        `json:"grossAmount"`
	FoodDeduction  int        `json:"foodDeduction"`
	OtherDeduction int        `json:"otherDeduction"`
	NetAmount      int        `json:"netAmount"`
	PaymentRef     *string    `json:"paymentRef"`
	Note           *string    `json:"note"`
	Worker         lineWorker `json:"worker"`
	// WRK-03 calculation inputs and result (§7.4)
	FullDays         int             `json:"fullDays"`
	HalfDays         int             `json:"halfDays"`
	HalfDayRate      int             `json:"halfDayRate"`
	OvertimeHours    float64         `json:"overtimeHours"`
	OvertimeRate     int             `json:"overtimeRate"`
	BasicAmount      int             `json:"basicAmount"`
	OvertimeAmount   int             `json:"overtimeAmount"`
	PieceUnits       int             `json:"pieceUnits"`
	PieceAmount      int             `json:"pieceAmount"`
	AdjustmentAmount int             `json:"adjustmentAmount"`
	CalculatedGross  *int            `json:"calculatedGross"`
	CalculatedDays   *float64        `json:"calculatedDays"`
	Calculation      json.RawMessage `json:"calculation"`
	IsOverride       bool            `json:"isOverride"`
	OverrideReason   *string         `json:"overrideReason"`
	PaymentMethod    *string         `json:"paymentMethod"`
}

type signoff struct {
	UserID string  `json:"userId"`
	Name   string  `json:"name"`
	Role   *string `json:"role"`
	At     *string `json:"at"`
	at     *time.Time
}

type actionState struct {
	Visible bool   `json:"visible"`
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

type trailOut struct {
	Prepared   *signoff `json:"prepared"`
	Checked    *signoff `json:"checked"`
	Verified   *signoff `json:"verified"`
	Authorized *signoff `json:"authorized"`
	Paid       *signoff `json:"paid"`
}

type periodDetail struct {
	ID            string    `json:"id"`
	Number        string    `json:"number"`
	Status        string    `json:"status"`
	Plant         plantRef  `json:"plant"`
	StartDate     string    `json:"startDate"`
	EndDate       string    `json:"endDate"`
	Label         string    `json:"label"`
	PeriodDays    int       `json:"periodDays"`
	RejectComment *string   `json:"rejectComment"`
	CreatedAt     string    `json:"createdAt"`
	UpdatedAt     string    `json:"updatedAt"`
	Trail         trailOut  `json:"trail"`
	Lines         []lineOut `json:"lines"`
	Totals        Totals    `json:"totals"`
	MismatchCount int       `json:"mismatchCount"`
	PettyCash     struct {
		Entries int `json:"entries"`
		Amount  int `json:"amount"`
	} `json:"pettyCash"`
	Actions      map[string]actionState `json:"actions"`
	CalculatedAt *string                `json:"calculatedAt"`
	Overrides    int                    `json:"overrides"`
	Warnings     int                    `json:"warnings"`
	Payments     struct {
		Count  int `json:"count"`
		Amount int `json:"amount"`
	} `json:"payments"`

	start, end time.Time
}

func loadLines(ctx context.Context, q db.DBTX, periodID string) ([]lineOut, error) {
	rows, err := q.Query(ctx, `SELECT l.id, l."workerId", l."dailyWage", l."daysWorked"::float8, l."grossAmount", l."foodDeduction", l."otherDeduction", l."netAmount", l."paymentRef", l.note,
		w.id, w."firstName", w."lastName", w."nationalId", w."momoNumber", w."momoName", w."jobTitle", w."isActive", w."plantId", w."rateBasis", w."employmentStatus",
		l."fullDays", l."halfDays", l."halfDayRate", l."overtimeHours"::float8, l."overtimeRate", l."basicAmount", l."overtimeAmount", l."pieceUnits", l."pieceAmount",
		l."adjustmentAmount", l."calculatedGross", l."calculatedDays"::float8, l.calculation, l."isOverride", l."overrideReason", l."paymentMethod"
		FROM "PayrollLine" l JOIN "Worker" w ON w.id = l."workerId" WHERE l."periodId" = $1 ORDER BY w."firstName", w."lastName", w.id`, periodID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []lineOut{}
	for rows.Next() {
		var l lineOut
		w := &l.Worker
		if err := rows.Scan(&l.ID, &l.WorkerID, &l.DailyWage, &l.DaysWorked, &l.GrossAmount, &l.FoodDeduction, &l.OtherDeduction, &l.NetAmount, &l.PaymentRef, &l.Note,
			&w.ID, &w.FirstName, &w.LastName, &w.NationalID, &w.MomoNumber, &w.MomoName, &w.JobTitle, &w.IsActive, &w.PlantID, &w.RateBasis, &w.Status,
			&l.FullDays, &l.HalfDays, &l.HalfDayRate, &l.OvertimeHours, &l.OvertimeRate, &l.BasicAmount, &l.OvertimeAmount, &l.PieceUnits, &l.PieceAmount,
			&l.AdjustmentAmount, &l.CalculatedGross, &l.CalculatedDays, &l.Calculation, &l.IsOverride, &l.OverrideReason, &l.PaymentMethod); err != nil {
			return nil, err
		}
		w.FullName = fullName(w.FirstName, w.LastName)
		w.MomoNameMismatch = MomoNameMismatch(w.FirstName, w.LastName, w.MomoName)
		out = append(out, l)
	}
	return out, rows.Err()
}

// detail assembles the full wage sheet view for user (3 reads: head, lines, signers + petty cash).
func (h *handler) detail(ctx context.Context, lg i18n.Lang, u *auth.User, id string) (*periodDetail, error) {
	p, err := loadHead(ctx, h.d.Pool, id, false)
	if err != nil {
		return nil, err
	}
	if err := auth.AssertPlantAccess(u, p.PlantID); err != nil {
		return nil, err
	}
	return h.buildDetail(ctx, lg, u, p)
}

func (h *handler) buildDetail(ctx context.Context, lg i18n.Lang, u *auth.User, p *periodHead) (*periodDetail, error) {
	lines, err := loadLines(ctx, h.d.Pool, p.ID)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, x := range []*string{p.PreparedBy, p.CheckedBy, p.VerifiedBy, p.AuthorizedBy, p.PaidBy} {
		if x != nil {
			ids = append(ids, *x)
		}
	}
	type usr struct{ name, role string }
	users := map[string]usr{}
	if len(ids) > 0 {
		rows, err := h.d.Pool.Query(ctx, `SELECT id, name, role::text FROM "User" WHERE id = ANY($1)`, ids)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			var x usr
			if err := rows.Scan(&id, &x.name, &x.role); err != nil {
				rows.Close()
				return nil, err
			}
			users[id] = x
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	d := &periodDetail{ID: p.ID, Number: p.Number, Status: p.Status, Plant: p.Plant, StartDate: dates.ToISO(p.Start), EndDate: dates.ToISO(p.End),
		Label: FormatPeriodRange(lg, p.Start, p.End, false), PeriodDays: PeriodLengthDays(p.Start, p.End), RejectComment: p.RejectComment,
		CreatedAt: isoTime(p.CreatedAt), UpdatedAt: isoTime(p.UpdatedAt), Lines: lines, start: p.Start, end: p.End}
	who := func(uid *string, at *time.Time) *signoff {
		if uid == nil {
			return nil
		}
		s := &signoff{UserID: *uid, Name: t2(lg, "payroll.unknown_user"), At: isoTimePtr(at), at: at}
		if x, ok := users[*uid]; ok {
			s.Name = x.name
			r := x.role
			s.Role = &r
		}
		return s
	}
	d.Trail = trailOut{Prepared: who(p.PreparedBy, p.PreparedAt), Checked: who(p.CheckedBy, p.CheckedAt), Verified: who(p.VerifiedBy, p.VerifiedAt),
		Authorized: who(p.AuthorizedBy, p.AuthorizedAt), Paid: who(p.PaidBy, p.PaidAt)}
	tl := make([]TotalsLine, len(lines))
	for i, l := range lines {
		tl[i] = TotalsLine{Days: l.DaysWorked, Gross: l.GrossAmount, Food: l.FoodDeduction, Other: l.OtherDeduction, Net: l.NetAmount}
		if l.Worker.MomoNameMismatch && l.NetAmount > 0 {
			d.MismatchCount++
		}
	}
	d.Totals = SumLines(tl)
	if err := h.d.Pool.QueryRow(ctx, `SELECT count(*), COALESCE(sum(amount), 0) FROM "PettyCashEntry" WHERE "sourceType" = 'PayrollPeriod' AND "sourceId" = $1`, p.ID).
		Scan(&d.PettyCash.Entries, &d.PettyCash.Amount); err != nil {
		return nil, err
	}
	var calcAt *time.Time
	if err := h.d.Pool.QueryRow(ctx, `SELECT p."calculatedAt", (SELECT count(*) FROM wage_payment w WHERE w.period_id = p.id), (SELECT COALESCE(sum(amount), 0) FROM wage_payment w WHERE w.period_id = p.id)
		FROM "PayrollPeriod" p WHERE p.id = $1`, p.ID).Scan(&calcAt, &d.Payments.Count, &d.Payments.Amount); err != nil {
		return nil, err
	}
	d.CalculatedAt = isoTimePtr(calcAt)
	for _, l := range lines {
		if l.IsOverride {
			d.Overrides++
		}
		if len(l.Calculation) > 0 {
			var cw struct {
				Warnings []string `json:"warnings"`
			}
			if json.Unmarshal(l.Calculation, &cw) == nil && len(cw.Warnings) > 0 {
				d.Warnings++
			}
		}
	}
	d.Actions = actionsFor(lg, u, p, d.Totals.Workers)
	return d, nil
}

func t2(lg i18n.Lang, key string, args ...any) string { return i18n.T(lg, key, args...) }

// actionsFor mirrors the checks the endpoints enforce, for the current user.
func actionsFor(lg i18n.Lang, u *auth.User, p *periodHead, worked int) map[string]actionState {
	scoped := auth.ScopedPlantID(u)
	inPlant := scoped == "" || scoped == p.PlantID
	st := p.state(worked)
	out := map[string]actionState{}
	for _, step := range Steps {
		if !inPlant || !u.Can(auth.Permission(StepPermission[step])) || p.Status != StepFrom[step] {
			out[string(step)] = actionState{}
			continue
		}
		r := CheckTransition(st, step, u.ID)
		if r.OK {
			out[string(step)] = actionState{Visible: true, Allowed: true}
		} else {
			out[string(step)] = actionState{Visible: true, Reason: r.Msg(lg)}
		}
	}
	canReturn := false
	if inPlant && isReturnable(p.Status) {
		for _, perm := range ReturnPermissions {
			if u.Can(auth.Permission(perm)) {
				canReturn = true
			}
		}
	}
	out["edit"] = actionState{Visible: inPlant && p.Status == "DRAFT" && u.Can("payroll.prepare"), Allowed: true}
	out["calculate"] = out["edit"]
	out["return"] = actionState{Visible: canReturn, Allowed: canReturn}
	return out
}

func (h *handler) getPeriod(c *fiber.Ctx) error {
	d, err := h.detail(reqCtx(c), lang(c), auth.MustUser(c), c.Params("id"))
	if err != nil {
		return err
	}
	return c.JSON(d)
}

// ───────────────────────────── Period writes ─────────────────────────────

func (h *handler) createPeriod(c *fiber.Ctx) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	lg := lang(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	var plantID string
	if s, ok := strVal(v, "plantId", "v.required"); ok {
		switch {
		case s == "":
			v.Fail("plantId", "v.required")
		case utf8.RuneCountInString(s) > 40:
			v.Fail("plantId", "payroll.v.max_40")
		default:
			plantID = s
		}
	}
	isoField := func(key string) (time.Time, string, bool) {
		s, ok := strVal(v, key, "v.required")
		if !ok {
			return time.Time{}, "", false
		}
		d, err := dates.ParseISO(s)
		if err != nil {
			v.Fail(key, "payroll.v.date")
			return time.Time{}, "", false
		}
		return d, s, true
	}
	start, _, ok1 := isoField("startDate")
	end, _, ok2 := isoField("endDate")
	if ok1 && ok2 && !v.Failed() {
		if end.Before(start) {
			v.Fail("endDate", "payroll.v.end_before_start")
		} else if PeriodLengthDays(start, end) > 31 {
			v.Fail("endDate", "payroll.v.max_31_days")
		}
	}
	if err := v.Err(); err != nil {
		return err
	}
	if err := auth.AssertPlantAccess(u, plantID); err != nil {
		return err
	}
	plant, err := activePlant(c, h.d.Pool, plantID)
	if err != nil {
		return err
	}
	id, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (string, error) {
		// serialise creation per plant so overlapping sheets cannot slip in concurrently
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "payroll:"+plant.ID); err != nil {
			return "", err
		}
		var onum string
		var os, oe time.Time
		err := tx.QueryRow(ctx, `SELECT number, "startDate", "endDate" FROM "PayrollPeriod" WHERE "plantId" = $1 AND "startDate" <= $2 AND "endDate" >= $3 LIMIT 1`,
			plant.ID, end, start).Scan(&onum, &os, &oe)
		if err == nil {
			if os.Equal(start) && oe.Equal(end) {
				return "", keyed(409, "conflict", "payroll.err.period_exists", "plant", plant.Code, "range", FormatPeriodRange(lg, start, end, false), "number", onum)
			}
			return "", keyed(409, "conflict", "payroll.err.period_overlap", "number", onum, "range", FormatPeriodRange(lg, os, oe, false), "plant", plant.Code)
		}
		if !db.IsNoRows(err) {
			return "", err
		}
		number := PeriodNumber(plant.Code, start)
		var pid string
		if err := tx.QueryRow(ctx, `INSERT INTO "PayrollPeriod" (number, "plantId", "startDate", "endDate", "updatedAt") VALUES ($1,$2,$3,$4,$5) RETURNING id`,
			number, plant.ID, start, end, db.Now()).Scan(&pid); err != nil {
			return "", err
		}
		// WRK-03: the system calculates the sheet from attendance and rate rules right away
		lines, err := recalc(ctx, tx, &periodHead{ID: pid, Number: number, Status: "DRAFT", PlantID: plant.ID, Start: start, End: end})
		if err != nil {
			return "", err
		}
		n := len(lines)
		return pid, audit.Record(c, tx, audit.Input{Action: "payroll.period.create", Entity: "PayrollPeriod", EntityID: pid,
			Summary: fmt.Sprintf("Created wage sheet %s for %s %s with %d workers", number, plant.Code, FormatPeriodRange(i18n.EN, start, end, false), n),
			After:   map[string]any{"plantId": plant.ID, "startDate": dates.ToISO(start), "endDate": dates.ToISO(end), "number": number, "workers": n}})
	})
	if err != nil {
		return err
	}
	invalidate()
	d, err := h.detail(ctx, lg, u, id)
	if err != nil {
		return err
	}
	return c.Status(201).JSON(d)
}

func lineWorkerID(l *httpx.V) (string, bool) {
	s, ok := strVal(l, "workerId", "v.required")
	if !ok {
		return "", false
	}
	if s == "" {
		l.Fail("workerId", "v.required")
		return "", false
	}
	if utf8.RuneCountInString(s) > 40 {
		l.Fail("workerId", "payroll.v.max_40")
		return "", false
	}
	return s, true
}

// ── workflow ────────────────────────────────────────────────────────────────

var stampCols = map[Step][2]string{
	StepPrepare:   {"preparedById", "preparedAt"},
	StepCheck:     {"checkedById", "checkedAt"},
	StepVerify:    {"verifiedById", "verifiedAt"},
	StepAuthorize: {"authorizedById", "authorizedAt"},
	StepPay:       {"paidById", "paidAt"},
}

// guardStep loads the sheet (and its totals), checks plant scope, order and separation of duties.
func (h *handler) guardStep(c *fiber.Ctx, u *auth.User, id string, step Step) (*periodHead, Totals, error) {
	ctx := reqCtx(c)
	p, err := loadHead(ctx, h.d.Pool, id, false)
	if err != nil {
		return nil, Totals{}, err
	}
	if err := auth.AssertPlantAccess(u, p.PlantID); err != nil {
		return nil, Totals{}, err
	}
	tot, err := periodTotals(ctx, h.d.Pool, p.ID)
	if err != nil {
		return nil, tot, err
	}
	if err := transitionError(c, CheckTransition(p.state(tot.Workers), step, u.ID)); err != nil {
		return p, tot, err
	}
	if step == StepPrepare {
		var neg int
		if err := h.d.Pool.QueryRow(ctx, `SELECT count(*) FROM "PayrollLine" WHERE "periodId" = $1 AND "netAmount" < 0`, p.ID).Scan(&neg); err != nil {
			return p, tot, err
		}
		if neg > 0 {
			return p, tot, keyed(400, "bad_request", "payroll.err.negative_lines", "n", neg)
		}
	}
	if step == StepPrepare || step == StepVerify || step == StepAuthorize {
		var pending int // WRK-01: unapproved registrations are never paid
		if err := h.d.Pool.QueryRow(ctx, `SELECT count(*) FROM "PayrollLine" l JOIN "Worker" w ON w.id = l."workerId"
			WHERE l."periodId" = $1 AND w."registrationStatus" <> 'APPROVED'`, p.ID).Scan(&pending); err != nil {
			return p, tot, err
		}
		if pending > 0 {
			return p, tot, keyed(400, "bad_request", "payroll.err.pending_workers", "n", pending)
		}
	}
	return p, tot, nil
}

func transitionError(c *fiber.Ctx, r Result) error {
	if r.OK {
		return nil
	}
	lg := lang(c)
	e := &httpx.Error{Message: r.Msg(i18n.EN), Key: r.Key, Args: r.Args(lg)}
	switch r.Kind {
	case KindSoD:
		e.Status, e.Code = 403, "forbidden"
	case KindEmpty:
		e.Status, e.Code = 400, "bad_request"
	default:
		e.Status, e.Code = 409, "conflict"
	}
	return e
}

// stamp moves the sheet to the next status; the status guard makes concurrent steps conflict.
func stamp(ctx context.Context, tx pgx.Tx, p *periodHead, step Step, userID string, clearReject bool) (time.Time, error) {
	now := db.Now()
	cols := stampCols[step]
	extra := ""
	if clearReject {
		extra = `, "rejectComment" = NULL`
	}
	tag, err := tx.Exec(ctx, `UPDATE "PayrollPeriod" SET status = $2::"PayrollStatus", "`+cols[0]+`" = $3, "`+cols[1]+`" = $4, "updatedAt" = $4`+extra+
		` WHERE id = $1 AND status = $5::"PayrollStatus"`, p.ID, StepTo[step], userID, now, StepFrom[step])
	if err != nil {
		return now, err
	}
	if tag.RowsAffected() == 0 {
		return now, keyed(409, "conflict", "payroll.err.changed_meanwhile")
	}
	return now, nil
}

var stepAudit = map[Step]string{StepPrepare: "Prepared", StepCheck: "Checked", StepVerify: "Verified", StepAuthorize: "Authorized"}

func (h *handler) advance(c *fiber.Ctx, step Step) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	p, totals, err := h.guardStep(c, u, c.Params("id"), step)
	if err != nil {
		return err
	}
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		if _, err := stamp(ctx, tx, p, step, u.ID, step == StepPrepare); err != nil {
			return err
		}
		if err := notifyStep(ctx, tx, p, step, u, totals); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "payroll.period." + string(step), Entity: "PayrollPeriod", EntityID: p.ID,
			Summary: fmt.Sprintf("%s wage sheet %s: %d workers, net %s", stepAudit[step], p.Number, totals.Workers, format.RWF(float64(totals.Net))),
			Before:  map[string]any{"status": p.Status}, After: map[string]any{"status": StepTo[step], "totals": totals}})
	})
	if err != nil {
		return err
	}
	invalidate()
	d, err := h.detail(ctx, lang(c), u, p.ID)
	if err != nil {
		return err
	}
	return c.JSON(d)
}

func (h *handler) returnToDraft(c *fiber.Ctx) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	comment := ""
	if s, ok := strVal(v, "comment", "payroll.v.explain"); ok {
		switch {
		case utf8.RuneCountInString(s) < 3:
			v.Fail("comment", "payroll.v.explain")
		case utf8.RuneCountInString(s) > 500:
			v.Fail("comment", "payroll.v.max_500")
		default:
			comment = s
		}
	}
	if err := v.Err(); err != nil {
		return err
	}
	p, err := loadHead(ctx, h.d.Pool, c.Params("id"), false)
	if err != nil {
		return err
	}
	if err := auth.AssertPlantAccess(u, p.PlantID); err != nil {
		return err
	}
	if err := transitionError(c, CheckReturn(p.Status)); err != nil {
		return err
	}
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE "PayrollPeriod" SET status = 'DRAFT', "preparedById" = NULL, "preparedAt" = NULL, "checkedById" = NULL, "checkedAt" = NULL,
			"verifiedById" = NULL, "verifiedAt" = NULL, "authorizedById" = NULL, "authorizedAt" = NULL, "rejectComment" = $2, "updatedAt" = $3
			WHERE id = $1 AND status IN ('PREPARED','CHECKED','VERIFIED')`, p.ID, comment, db.Now())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return keyed(409, "conflict", "payroll.err.changed_meanwhile")
		}
		return audit.Record(c, tx, audit.Input{Action: "payroll.period.return", Entity: "PayrollPeriod", EntityID: p.ID,
			Summary: fmt.Sprintf("Returned wage sheet %s to draft: %s", p.Number, comment),
			Before:  map[string]any{"status": p.Status, "preparedById": p.PreparedBy, "checkedById": p.CheckedBy, "verifiedById": p.VerifiedBy},
			After:   map[string]any{"status": "DRAFT", "rejectComment": comment}})
	})
	if err != nil {
		return err
	}
	invalidate()
	d, err := h.detail(ctx, lang(c), u, p.ID)
	if err != nil {
		return err
	}
	return c.JSON(d)
}

// ── pay ─────────────────────────────────────────────────────────────────────

func (h *handler) pay(c *fiber.Ctx) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	lg := lang(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	type payRef struct{ lineID, ref string }
	var refsIn []payRef
	arr := v.Arr("paymentRefs", false)
	if len(arr) > 500 {
		v.Fail("paymentRefs", "payroll.v.max_lines")
	}
	methods := map[string]string{}
	for _, r := range arr {
		id, _ := lineWorkerIDKey(r, "lineId")
		ref := r.Str("ref").Trim().Max(60, "payroll.v.max_60").Get()
		refsIn = append(refsIn, payRef{id, ref})
		if m := r.OptStr("method").Trim().Upper().OneOf(append([]string{""}, workforce.PaymentMethods...), "payroll.v.method").Get(); m != "" {
			methods[id] = m
		}
	}
	method := v.OptStr("method").Trim().Upper().Default("MOMO").OneOf(workforce.PaymentMethods, "payroll.v.method").Get()
	paidOnS := v.OptStr("paidOn").Trim().Date("payroll.v.date").Get()
	if paidOnS != "" && paidOnS > dates.TodayISO() {
		v.Fail("paidOn", "payroll.v.paid_future")
	}
	createPetty := v.OptBool("createPettyCash").Default(false).Get()
	if err := v.Err(); err != nil {
		return err
	}
	if createPetty && !u.Can("pettycash.record") {
		return keyed(403, "forbidden", "payroll.err.no_petty_perm")
	}
	p, _, err := h.guardStep(c, u, c.Params("id"), StepPay)
	if err != nil {
		return err
	}
	lines, err := loadLines(ctx, h.d.Pool, p.ID)
	if err != nil {
		return err
	}
	lineBy := map[string]bool{}
	for _, l := range lines {
		lineBy[l.ID] = true
	}
	fields := map[string]string{}
	refs := map[string]string{}
	for i, r := range refsIn {
		if !lineBy[r.lineID] {
			fields[fmt.Sprintf("paymentRefs.%d.lineId", i)] = t(c, "payroll.err.ref_line")
		} else if r.ref != "" {
			refs[r.lineID] = r.ref
		}
	}
	if len(fields) > 0 {
		return badKey(fields, "payroll.err.refs_mismatch")
	}
	tl := make([]TotalsLine, len(lines))
	var payable []lineOut
	for i, l := range lines {
		tl[i] = TotalsLine{Days: l.DaysWorked, Gross: l.GrossAmount, Food: l.FoodDeduction, Other: l.OtherDeduction, Net: l.NetAmount}
		if l.NetAmount > 0 {
			payable = append(payable, l)
		}
	}
	totals := SumLines(tl)
	paidOn := dates.MustParseISO(dates.TodayISO())
	if paidOnS != "" {
		paidOn = dates.MustParseISO(paidOnS)
	}
	rangeShort := FormatPeriodRange(i18n.EN, p.Start, p.End, true)

	type result struct {
		created int
		warning string
	}
	pettyNo := map[string]string{}
	res, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (result, error) {
		var r result
		if _, err := stamp(ctx, tx, p, StepPay, u.ID, false); err != nil {
			return r, err
		}
		if len(refs) > 0 {
			ids, vals := make([]string, 0, len(refs)), make([]string, 0, len(refs))
			for id, ref := range refs {
				ids, vals = append(ids, id), append(vals, ref)
			}
			if _, err := tx.Exec(ctx, `UPDATE "PayrollLine" AS l SET "paymentRef" = x.ref FROM unnest($1::text[], $2::text[]) AS x(id, ref)
				WHERE l.id = x.id AND l."periodId" = $3`, ids, vals, p.ID); err != nil {
				return r, err
			}
		}
		if createPetty && len(payable) > 0 {
			n := len(payable)
			// Reserve n consecutive petty cash numbers in one statement: same "Sequence" row lock and
			// "PC-<year>-NNNNN" format as seq.NextNumber, without n round trips.
			prefix := fmt.Sprintf("PC-%d", paidOn.Year())
			var last int
			if err := tx.QueryRow(ctx, `INSERT INTO "Sequence" (key, value) VALUES ($1, $2::int) ON CONFLICT (key) DO UPDATE SET value = "Sequence".value + $2::int RETURNING value`,
				prefix, n).Scan(&last); err != nil {
				return r, err
			}
			nums, descs, payees, momos, amts, invs := make([]string, n), make([]string, n), make([]string, n), make([]*string, n), make([]int32, n), make([]*string, n)
			for i, l := range payable {
				name := fullName(l.Worker.FirstName, l.Worker.LastName)
				nums[i] = fmt.Sprintf("%s-%05d", prefix, last-n+1+i)
				descs[i] = fmt.Sprintf("WAGES %s %s — %s", p.Plant.Code, rangeShort, name)
				payees[i] = name
				if l.Worker.MomoName != nil && *l.Worker.MomoName != "" {
					payees[i] = *l.Worker.MomoName
				}
				momos[i] = l.Worker.MomoNumber
				amts[i] = int32(l.NetAmount)
				if ref, ok := refs[l.ID]; ok {
					rr := ref
					invs[i] = &rr
				}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO "PettyCashEntry" (number, date, type, category, description, "plantId", "payeeName", "momoNumber", amount, "invoiceRef",
					status, "sourceType", "sourceId", "createdById", "reviewedById", "reviewedAt", "reviewComment", "updatedAt")
				SELECT x.num, $1::date, 'EXPENSE'::"PettyCashType", 'WAGES'::"PettyCashCategory", x.descr, $2::text, x.payee, x.momo, x.amt, x.inv,
					'APPROVED'::"ApprovalStatus", 'PayrollPeriod', $3::text, $4::text, $5::text, $6::timestamp, $7::text, $8::timestamp
				FROM unnest($9::text[], $10::text[], $11::text[], $12::text[], $13::int[], $14::text[]) AS x(num, descr, payee, momo, amt, inv)`,
				paidOn, p.PlantID, p.ID, u.ID, p.AuthorizedBy, p.AuthorizedAt, "Authorized via payroll "+p.Number, db.Now(),
				nums, descs, payees, momos, amts, invs); err != nil {
				return r, err
			}
			r.created = n
			for i, l := range payable {
				pettyNo[l.ID] = nums[i]
			}
			var balance int
			if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(effect), 0) FROM petty_cash_ledger WHERE status = 'APPROVED'`).Scan(&balance); err != nil {
				return r, err
			}
			if balance < 0 {
				r.warning = t(c, "payroll.warn.petty_overdraw", "balance", format.RWF(float64(balance)))
			}
		}
		// WRK-04: one payment record per worker (date, method, amount, reference, paid by)
		if len(payable) > 0 {
			n := len(payable)
			wids, lids, meths, refsArr, pcs := make([]string, n), make([]string, n), make([]string, n), make([]*string, n), make([]*string, n)
			amts := make([]int32, n)
			for i, l := range payable {
				wids[i], lids[i], amts[i] = l.WorkerID, l.ID, int32(l.NetAmount)
				meths[i] = method
				if m, ok := methods[l.ID]; ok {
					meths[i] = m
				}
				if ref, ok := refs[l.ID]; ok {
					rr := ref
					refsArr[i] = &rr
				}
				if pc, ok := pettyNo[l.ID]; ok {
					pp := pc
					pcs[i] = &pp
				}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO wage_payment (worker_id, plant_id, period_id, line_id, date, method, amount, reference, paid_by, petty_cash_entry)
				SELECT x.w, $1, $2, x.l, $3::date, x.m, x.a, x.r, $4, x.pc FROM unnest($5::text[], $6::text[], $7::text[], $8::int[], $9::text[], $10::text[]) AS x(w, l, m, a, r, pc)`,
				p.PlantID, p.ID, paidOn, u.ID, wids, lids, meths, amts, refsArr, pcs); err != nil {
				return r, err
			}
			if _, err := tx.Exec(ctx, `UPDATE "PayrollLine" AS l SET "paymentMethod" = x.m FROM unnest($1::text[], $2::text[]) AS x(id, m) WHERE l.id = x.id`, lids, meths); err != nil {
				return r, err
			}
			// every wage paid is an expense (docs/FINANCE_RECORDS_DESIGN.md §2): cash vouchers already own theirs (petty cash
			// mirror); the rest is one PAYROLL expense per payment method, linked to the period and to each worker paid
			if r.created == 0 {
				if err := recordWageExpenses(ctx, tx, p, payable, method, methods, paidOn, rangeShort, u.ID); err != nil {
					return r, err
				}
			} else if err := linkVoucherWorkers(ctx, tx, p.ID); err != nil {
				return r, err
			}
		}
		// WRK-05: a paid period is locked — its attendance days too; later corrections become adjustments (§7.4)
		if _, err := attendance.LockDays(ctx, tx, p.PlantID, p.Start, p.End, u.ID, "Paid on wage sheet "+p.Number); err != nil {
			return r, err
		}
		if err := notifyPaid(ctx, tx, p, u, len(payable), totals.Net); err != nil {
			return r, err
		}
		summary := fmt.Sprintf("Wage payments recorded for %s: %d workers, net %s", p.Number, len(payable), format.RWF(float64(totals.Net)))
		if r.created > 0 {
			summary += fmt.Sprintf("; %d petty cash entries recorded", r.created)
		}
		return r, audit.Record(c, tx, audit.Input{Action: "payroll.period.pay", Entity: "PayrollPeriod", EntityID: p.ID, Summary: summary,
			Before: map[string]any{"status": p.Status}, After: map[string]any{"status": "PAID", "totals": totals, "paymentRefs": refs, "pettyCashEntries": r.created, "method": method, "methods": methods, "paidOn": dates.ToISO(paidOn)}})
	})
	if err != nil {
		return err
	}
	invalidate()
	d, err := h.detail(ctx, lg, u, p.ID)
	if err != nil {
		return err
	}
	pcAmount := 0
	if res.created > 0 {
		pcAmount = totals.Net
	}
	out := fiber.Map{"period": d, "pettyCash": fiber.Map{"created": res.created, "amount": pcAmount}}
	if res.warning != "" {
		out["warning"] = res.warning
	}
	return c.JSON(out)
}

func lineWorkerIDKey(l *httpx.V, key string) (string, bool) {
	s, ok := strVal(l, key, "v.required")
	if !ok {
		return "", false
	}
	if s == "" {
		l.Fail(key, "v.required")
		return "", false
	}
	if utf8.RuneCountInString(s) > 40 {
		l.Fail(key, "payroll.v.max_40")
		return "", false
	}
	return s, true
}
