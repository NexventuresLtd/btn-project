package payroll

// Wage sheets computed by the system (SRS WRK-03, §7.4): every line comes from attendance (days, half days,
// overtime), the administrator-approved rate rules effective on each day, piece-work output and adjustments
// carried from earlier paid periods (internal/workforce). Deductions and notes are entered by people; the
// days/overtime may be overridden only with a reason (audited), and the system figure stays visible.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/workforce"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// prevLine is what people entered on a line and survives a recalculation.
type prevLine struct {
	Food, Other int
	Note        *string
	Override    bool
	Days, OT    float64
	Reason      *string
	PaymentRef  *string
}

// calcLine is a line ready to be written.
type calcLine struct {
	WorkerID            string
	Calc                workforce.Calc
	AdjIDs              []string
	Days, OT            float64
	Override            bool
	Reason              *string
	Food, Other         int
	Note, PaymentRef    *string
	Basic, Overtime     int
	Gross, Net          int
	CalculatedGross     int
	CalculatedDays      float64
	DailyRate, HalfRate int
	OvertimeRate        int
}

func buildLine(workerID string, r workforce.PeriodResult, prev prevLine) calcLine {
	c := r.Calc
	l := calcLine{WorkerID: workerID, Calc: c, AdjIDs: r.AdjustmentIDs, Food: prev.Food, Other: prev.Other, Note: prev.Note, PaymentRef: prev.PaymentRef,
		CalculatedGross: c.Gross, CalculatedDays: c.PayableDays, DailyRate: c.Last.DailyRate, HalfRate: c.Last.HalfDayRate, OvertimeRate: c.Last.OvertimeRate}
	if prev.Override {
		l.Override, l.Reason, l.Days, l.OT = true, prev.Reason, prev.Days, prev.OT
		l.Gross, l.Basic, l.Overtime = workforce.OverrideGross(c, prev.Days, prev.OT)
	} else {
		l.Days, l.OT, l.Gross, l.Basic, l.Overtime = c.PayableDays, c.OvertimeHours, c.Gross, c.Basic, c.Overtime
	}
	l.Net = l.Gross - l.Food - l.Other
	return l
}

// writeLines replaces the sheet's lines and links the adjustments they consume to the period.
func writeLines(ctx context.Context, tx pgx.Tx, periodID string, lines []calcLine) error {
	if _, err := tx.Exec(ctx, `UPDATE wage_adjustment SET applied_period_id = NULL WHERE applied_period_id = $1`, periodID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM "PayrollLine" WHERE "periodId" = $1`, periodID); err != nil {
		return err
	}
	if len(lines) == 0 {
		return nil
	}
	n := len(lines)
	wid := make([]string, n)
	ints := map[string][]int32{}
	for _, k := range []string{"wage", "gross", "food", "other", "net", "full", "half", "halfRate", "otRate", "basic", "ot", "units", "piece", "adj", "cgross"} {
		ints[k] = make([]int32, n)
	}
	days, ots, cdays := make([]float64, n), make([]float64, n), make([]float64, n)
	notes, refs, reasons, calcs := make([]*string, n), make([]*string, n), make([]*string, n), make([]string, n)
	over := make([]bool, n)
	adjIDs := []string{}
	for i, l := range lines {
		cj, err := json.Marshal(l.Calc)
		if err != nil {
			return err
		}
		wid[i], days[i], ots[i], cdays[i], notes[i], refs[i], calcs[i], over[i] = l.WorkerID, l.Days, l.OT, l.CalculatedDays, l.Note, l.PaymentRef, string(cj), l.Override
		if l.Override {
			reasons[i] = l.Reason
		}
		set := func(k string, v int) { ints[k][i] = int32(v) }
		set("wage", l.DailyRate)
		set("gross", l.Gross)
		set("food", l.Food)
		set("other", l.Other)
		set("net", l.Net)
		set("full", l.Calc.FullDays)
		set("half", l.Calc.HalfDays)
		set("halfRate", l.HalfRate)
		set("otRate", l.OvertimeRate)
		set("basic", l.Basic)
		set("ot", l.Overtime)
		set("units", l.Calc.PieceUnits)
		set("piece", l.Calc.Piece)
		set("adj", l.Calc.Adjustment)
		set("cgross", l.CalculatedGross)
		adjIDs = append(adjIDs, l.AdjIDs...)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO "PayrollLine" ("periodId","workerId","dailyWage","daysWorked","grossAmount","foodDeduction","otherDeduction","netAmount","note","paymentRef",
			"fullDays","halfDays","halfDayRate","overtimeHours","overtimeRate","basicAmount","overtimeAmount","pieceUnits","pieceAmount","adjustmentAmount",
			"calculatedGross","calculatedDays","calculation","isOverride","overrideReason")
		SELECT $1, t.w, t.wage, t.days, t.gross, t.food, t.other, t.net, t.note, t.ref, t.fd, t.hd, t.hr, t.oth, t.otr, t.basic, t.ot, t.units, t.piece, t.adj,
			t.cg, t.cd, t.calc::jsonb, t.ov, t.reason
		FROM unnest($2::text[], $3::int[], $4::float8[], $5::int[], $6::int[], $7::int[], $8::int[], $9::text[], $10::text[], $11::int[], $12::int[], $13::int[],
			$14::float8[], $15::int[], $16::int[], $17::int[], $18::int[], $19::int[], $20::int[], $21::int[], $22::float8[], $23::text[], $24::bool[], $25::text[])
			AS t(w, wage, days, gross, food, other, net, note, ref, fd, hd, hr, oth, otr, basic, ot, units, piece, adj, cg, cd, calc, ov, reason)`,
		periodID, wid, ints["wage"], days, ints["gross"], ints["food"], ints["other"], ints["net"], notes, refs, ints["full"], ints["half"], ints["halfRate"],
		ots, ints["otRate"], ints["basic"], ints["ot"], ints["units"], ints["piece"], ints["adj"], ints["cgross"], cdays, calcs, over, reasons); err != nil {
		return err
	}
	if len(adjIDs) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE wage_adjustment SET applied_period_id = $1 WHERE id = ANY($2)`, periodID, adjIDs); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `UPDATE "PayrollPeriod" SET "calculatedAt" = now(), "updatedAt" = $2 WHERE id = $1`, periodID, db.Now())
	return err
}

// loadPrev reads the people-entered parts of the current lines.
func loadPrev(ctx context.Context, q db.DBTX, periodID string) (map[string]prevLine, []string, error) {
	rows, err := q.Query(ctx, `SELECT "workerId", "foodDeduction", "otherDeduction", note, "isOverride", "daysWorked"::float8, "overtimeHours"::float8, "overrideReason", "paymentRef"
		FROM "PayrollLine" WHERE "periodId" = $1`, periodID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out := map[string]prevLine{}
	ids := []string{}
	for rows.Next() {
		var w string
		var p prevLine
		if err := rows.Scan(&w, &p.Food, &p.Other, &p.Note, &p.Override, &p.Days, &p.OT, &p.Reason, &p.PaymentRef); err != nil {
			return nil, nil, err
		}
		out[w] = p
		ids = append(ids, w)
	}
	return out, ids, rows.Err()
}

// recalc recomputes a DRAFT sheet: every worker of the site who is active or has attendance / piece work in the
// period gets a line; deductions, notes and overrides (with their reasons) are kept.
func recalc(ctx context.Context, tx pgx.Tx, p *periodHead) ([]calcLine, error) {
	prev, existing, err := loadPrev(ctx, tx, p.ID)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT w.id FROM "Worker" w WHERE w."plantId" = $1 AND (w."isActive"
			OR EXISTS (SELECT 1 FROM "Attendance" a WHERE a."workerId" = w.id AND a.date >= $2 AND a.date <= $3)
			OR EXISTS (SELECT 1 FROM piece_work_entry e WHERE e.worker_id = w.id AND e.date >= $2 AND e.date <= $3 AND e.removed_at IS NULL))
		ORDER BY w.id`, p.PlantID, p.Start, p.End)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		seen[id] = true
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range existing {
		if !seen[id] {
			ids = append(ids, id)
		}
	}
	res, err := workforce.CalculateWorkers(ctx, tx, ids, p.Start, p.End, p.ID)
	if err != nil {
		return nil, err
	}
	lines := make([]calcLine, 0, len(ids))
	for _, id := range ids {
		if r, ok := res[id]; ok {
			lines = append(lines, buildLine(id, r, prev[id]))
		}
	}
	return lines, writeLines(ctx, tx, p.ID, lines)
}

func linesTotals(lines []calcLine) Totals {
	tl := make([]TotalsLine, len(lines))
	for i, l := range lines {
		tl[i] = TotalsLine{Days: l.Days, Gross: l.Gross, Food: l.Food, Other: l.Other, Net: l.Net}
	}
	return SumLines(tl)
}

// calculate: POST /periods/:id/calculate — the system computes the draft sheet from attendance and rate rules.
func (h *handler) calculate(c *fiber.Ctx) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	p, err := loadHead(ctx, h.d.Pool, c.Params("id"), false)
	if err != nil {
		return err
	}
	if err := auth.AssertPlantAccess(u, p.PlantID); err != nil {
		return err
	}
	if p.Status != "DRAFT" {
		return keyed(409, "conflict", "payroll.err.not_draft")
	}
	before, err := periodTotals(ctx, h.d.Pool, p.ID)
	if err != nil {
		return err
	}
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE "PayrollPeriod" SET "updatedAt" = $2 WHERE id = $1 AND status = 'DRAFT'`, p.ID, db.Now())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return keyed(409, "conflict", "payroll.err.submitted_meanwhile")
		}
		lines, err := recalc(ctx, tx, p)
		if err != nil {
			return err
		}
		after := linesTotals(lines)
		return audit.Record(c, tx, audit.Input{Action: "payroll.period.calculate", Entity: "PayrollPeriod", EntityID: p.ID,
			Summary: fmt.Sprintf("Calculated wage sheet %s from attendance and rate rules: %d workers, %s days, gross %s", p.Number, after.Workers, format.Qty(after.Days), format.RWF(float64(after.Gross))),
			Before:  before, After: after})
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

type lineInput struct {
	WorkerID string
	Days     *float64
	OT       *float64
	Food     int
	Other    int
	Note     *string
	Reason   *string
}

// saveLines: PUT /periods/:id/lines — deductions and notes per line; daysWorked / overtimeHours that differ from
// the calculation are an override and need overrideReason (≥ 3 characters). Lines left out are removed.
func (h *handler) saveLines(c *fiber.Ctx) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	lg := lang(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	var in []lineInput
	arr := v.Arr("lines", true)
	if len(arr) > 500 {
		v.Fail("lines", "payroll.v.max_lines")
	}
	for _, l := range arr {
		li := lineInput{}
		li.WorkerID, _ = lineWorkerID(l)
		if l.Has("daysWorked") && l.Raw("daysWorked") != nil {
			dn := l.Num("daysWorked").Coerce().Min(0, "payroll.v.days_negative")
			if dn.Present() && !IsHalfDayStep(dn.F()) {
				l.Fail("daysWorked", "payroll.v.days_half")
			} else if dn.Present() {
				f := dn.F()
				li.Days = &f
			}
		}
		if l.Has("overtimeHours") && l.Raw("overtimeHours") != nil {
			on := l.Num("overtimeHours").Coerce().Min(0, "payroll.v.overtime").Max(16*31, "payroll.v.overtime")
			if on.Present() && math.Abs(on.F()*4-math.Round(on.F()*4)) > 1e-9 {
				l.Fail("overtimeHours", "payroll.v.overtime")
			} else if on.Present() {
				f := on.F()
				li.OT = &f
			}
		}
		li.Food = int(l.OptNum("foodDeduction").Coerce().Whole("payroll.v.food_whole").Min(0, "payroll.v.food_neg").Max(100_000_000, "payroll.v.money_max").Default(0).I())
		li.Other = int(l.OptNum("otherDeduction").Coerce().Whole("payroll.v.other_whole").Min(0, "payroll.v.other_neg").Max(100_000_000, "payroll.v.money_max").Default(0).I())
		li.Note = l.NullStr("note").Trim().Max(200, "payroll.v.max_200").NilIfEmpty()
		li.Reason = l.NullStr("overrideReason").Trim().Max(300, "payroll.v.max_300").NilIfEmpty()
		in = append(in, li)
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
	if p.Status != "DRAFT" {
		return keyed(409, "conflict", "payroll.err.not_draft")
	}
	periodDays := PeriodLengthDays(p.Start, p.End)

	var fieldOrder []string
	fields := map[string]string{}
	setField := func(k, msg string) {
		if _, ok := fields[k]; !ok {
			fieldOrder = append(fieldOrder, k)
		}
		fields[k] = msg
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(in))
	for i, l := range in {
		if seen[l.WorkerID] {
			setField(fmt.Sprintf("lines.%d.workerId", i), t(c, "payroll.err.dup_worker"))
		} else {
			ids = append(ids, l.WorkerID)
		}
		seen[l.WorkerID] = true
	}
	if len(fields) > 0 {
		return badKey(fields, "payroll.err.once_only")
	}
	type wrow struct {
		first, last, plantID string
		active               bool
	}
	workers := map[string]wrow{}
	if len(ids) > 0 {
		rows, err := h.d.Pool.Query(ctx, `SELECT id, "firstName", "lastName", "plantId", "isActive" FROM "Worker" WHERE id = ANY($1)`, ids)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var w wrow
			if err := rows.Scan(&id, &w.first, &w.last, &w.plantID, &w.active); err != nil {
				rows.Close()
				return err
			}
			workers[id] = w
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	prev, _, err := loadPrev(ctx, h.d.Pool, p.ID)
	if err != nil {
		return err
	}
	res, err := workforce.CalculateWorkers(ctx, h.d.Pool, ids, p.Start, p.End, p.ID)
	if err != nil {
		return err
	}
	var out []calcLine
	overrides := []map[string]any{}
	for i, l := range in {
		w, ok := workers[l.WorkerID]
		if !ok {
			setField(fmt.Sprintf("lines.%d.workerId", i), t(c, "payroll.err.unknown_worker"))
			continue
		}
		name := fullName(w.first, w.last)
		_, onSheet := prev[l.WorkerID]
		if !onSheet && w.plantID != p.PlantID {
			setField(fmt.Sprintf("lines.%d.workerId", i), t(c, "payroll.err.not_at_plant", "name", name, "plant", p.Plant.Code))
			continue
		}
		if !onSheet && !w.active {
			setField(fmt.Sprintf("lines.%d.workerId", i), t(c, "payroll.err.worker_inactive", "name", name))
			continue
		}
		r := res[l.WorkerID]
		cDays, cOT := r.Calc.PayableDays, r.Calc.OvertimeHours
		days, ot := cDays, cOT
		if l.Days != nil {
			days = *l.Days
		}
		if l.OT != nil {
			ot = *l.OT
		}
		override := math.Abs(days-cDays) > 1e-9 || workforce.Quarters(ot) != workforce.Quarters(cOT)
		if override && (l.Reason == nil || utf8.RuneCountInString(*l.Reason) < 3) {
			setField(fmt.Sprintf("lines.%d.overrideReason", i), t(c, "payroll.err.override_reason", "name", name, "days", format.Qty(cDays), "hours", format.Qty(cOT)))
			continue
		}
		if days > float64(periodDays) {
			setField(fmt.Sprintf("lines.%d.daysWorked", i), t(c, "payroll.err.days_exceed", "name", name, "max", periodDays))
			continue
		}
		pl := prevLine{Food: l.Food, Other: l.Other, Note: l.Note, PaymentRef: prev[l.WorkerID].PaymentRef}
		if override {
			pl.Override, pl.Days, pl.OT, pl.Reason = true, days, ot, l.Reason
			overrides = append(overrides, map[string]any{"workerId": l.WorkerID, "name": name, "calculatedDays": cDays, "days": days,
				"calculatedOvertime": cOT, "overtime": ot, "reason": *l.Reason})
		}
		line := buildLine(l.WorkerID, r, pl)
		if line.Net < 0 {
			setField(fmt.Sprintf("lines.%d.foodDeduction", i), t(c, "payroll.err.net_negative", "name", name, "deductions", format.RWF(float64(l.Food+l.Other)), "gross", format.RWF(float64(line.Gross))))
			continue
		}
		out = append(out, line)
	}
	if len(fields) > 0 {
		first := fields[fieldOrder[0]]
		if len(fields) == 1 {
			return &httpx.Error{Status: 400, Code: "bad_request", Message: first, Fields: fields}
		}
		return &httpx.Error{Status: 400, Code: "bad_request", Message: t(c, "payroll.err.and_more", "first", first, "count", len(fields)-1), Fields: fields}
	}
	before, err := periodTotals(ctx, h.d.Pool, p.ID)
	if err != nil {
		return err
	}
	after := linesTotals(out)
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE "PayrollPeriod" SET "updatedAt" = $2 WHERE id = $1 AND status = 'DRAFT'`, p.ID, db.Now())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return keyed(409, "conflict", "payroll.err.submitted_meanwhile")
		}
		if err := writeLines(ctx, tx, p.ID, out); err != nil {
			return err
		}
		auditLines := make([]map[string]any, 0, len(out))
		for _, l := range out {
			auditLines = append(auditLines, map[string]any{"workerId": l.WorkerID, "days": l.Days, "overtime": l.OT, "food": l.Food, "other": l.Other, "gross": l.Gross, "net": l.Net, "override": l.Override})
		}
		summary := fmt.Sprintf("Saved %d lines on %s: %s days, net %s", len(out), p.Number, format.Qty(after.Days), format.RWF(float64(after.Net)))
		if len(overrides) > 0 {
			names := make([]string, len(overrides))
			for i, o := range overrides {
				names[i] = fmt.Sprintf("%v (%v)", o["name"], o["reason"])
			}
			summary += fmt.Sprintf("; %d manual overrides: %s", len(overrides), strings.Join(names, "; "))
		}
		return audit.Record(c, tx, audit.Input{Action: "payroll.period.lines", Entity: "PayrollPeriod", EntityID: p.ID, Summary: summary,
			Before: before, After: map[string]any{"lines": auditLines, "overrides": overrides, "workers": after.Workers, "days": after.Days, "gross": after.Gross,
				"food": after.Food, "other": after.Other, "deductions": after.Deductions, "net": after.Net}})
	})
	if err != nil {
		return err
	}
	invalidate()
	d, err := h.detail(ctx, lg, u, p.ID)
	if err != nil {
		return err
	}
	return c.JSON(d)
}
