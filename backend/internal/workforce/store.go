package workforce

// Database side of the wage calculation: loads workers, attendance, rate rules, piece rates, piece work and
// pending adjustments for many workers at once (a fixed number of queries per calculation, never per worker).
// Used by internal/payroll (wage sheets) and internal/attendance (corrections → adjustments).

import (
	"context"
	"fmt"
	"time"

	"btn-ops/internal/db"
)

// LoadRules returns the active rate rules effective at some point in [from, to].
func LoadRules(ctx context.Context, q db.DBTX, from, to time.Time) ([]Rule, error) {
	rows, err := q.Query(ctx, `SELECT id, name, COALESCE(plant_id, ''), COALESCE(job_title, ''), COALESCE(worker_id, ''), daily_rate, half_day_rate,
		overtime_rate, overtime_multiplier::float8, standard_hours::float8, effective_from, effective_to
		FROM wage_rate_rule WHERE is_active AND effective_from <= $2 AND (effective_to IS NULL OR effective_to >= $1)`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		var r Rule
		if err := rows.Scan(&r.ID, &r.Name, &r.PlantID, &r.JobTitle, &r.WorkerID, &r.DailyRate, &r.HalfDayRate, &r.OvertimeRate,
			&r.OvertimeMultiplier, &r.StandardHours, &r.From, &r.To); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LoadPieceRates returns the active piece rates effective at some point in [from, to].
func LoadPieceRates(ctx context.Context, q db.DBTX, from, to time.Time) ([]PieceRate, error) {
	rows, err := q.Query(ctx, `SELECT id, product_id, COALESCE(plant_id, ''), rate, effective_from, effective_to
		FROM piece_rate WHERE is_active AND effective_from <= $2 AND (effective_to IS NULL OR effective_to >= $1)`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PieceRate{}
	for rows.Next() {
		var r PieceRate
		if err := rows.Scan(&r.ID, &r.ProductID, &r.PlantID, &r.Rate, &r.From, &r.To); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LoadWorkers returns the calculation view of the given workers.
func LoadWorkers(ctx context.Context, q db.DBTX, ids []string) (map[string]Worker, error) {
	out := map[string]Worker{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT id, "plantId", "jobTitle", "rateBasis", "dailyWage" FROM "Worker" WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var w Worker
		if err := rows.Scan(&w.ID, &w.PlantID, &w.JobTitle, &w.RateBasis, &w.DailyWage); err != nil {
			return nil, err
		}
		out[w.ID] = w
	}
	return out, rows.Err()
}

// PeriodResult is the calculation of one worker plus the adjustment rows it consumed.
type PeriodResult struct {
	Calc          Calc
	AdjustmentIDs []string
}

// CalculateWorkers computes every worker's wage over [from, to] (WRK-03). Pending adjustments (not yet applied,
// or applied to periodID itself) are included. Six queries regardless of the number of workers.
func CalculateWorkers(ctx context.Context, q db.DBTX, workerIDs []string, from, to time.Time, periodID string) (map[string]PeriodResult, error) {
	out := map[string]PeriodResult{}
	if len(workerIDs) == 0 {
		return out, nil
	}
	workers, err := LoadWorkers(ctx, q, workerIDs)
	if err != nil {
		return nil, err
	}
	rules, err := LoadRules(ctx, q, from, to)
	if err != nil {
		return nil, err
	}
	prates, err := LoadPieceRates(ctx, q, from, to)
	if err != nil {
		return nil, err
	}
	marks := map[string][]Mark{}
	rows, err := q.Query(ctx, `SELECT "workerId", date, "plantId", status, "overtimeHours"::float8, exception FROM "Attendance"
		WHERE "workerId" = ANY($1) AND date >= $2 AND date <= $3`, workerIDs, from, to)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var wid string
		var m Mark
		if err := rows.Scan(&wid, &m.Date, &m.PlantID, &m.Status, &m.OvertimeHours, &m.Exception); err != nil {
			rows.Close()
			return nil, err
		}
		marks[wid] = append(marks[wid], m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	pieces := map[string][]PieceEntry{}
	rows, err = q.Query(ctx, `SELECT worker_id, date, plant_id, product_id, quantity FROM piece_work_entry
		WHERE worker_id = ANY($1) AND date >= $2 AND date <= $3`, workerIDs, from, to)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var wid string
		var p PieceEntry
		if err := rows.Scan(&wid, &p.Date, &p.PlantID, &p.ProductID, &p.Quantity); err != nil {
			rows.Close()
			return nil, err
		}
		pieces[wid] = append(pieces[wid], p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	adjs := map[string][]Adjustment{}
	adjIDs := map[string][]string{}
	rows, err = q.Query(ctx, `SELECT a.id, a.worker_id, to_char(a.date, 'YYYY-MM-DD'), a.amount, a.reason, COALESCE(p.number, '')
		FROM wage_adjustment a LEFT JOIN "PayrollPeriod" p ON p.id = a.source_period_id
		WHERE a.worker_id = ANY($1) AND (a.applied_period_id IS NULL OR a.applied_period_id = $2)
		ORDER BY a.date, a.created_at`, workerIDs, periodID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var wid string
		var a Adjustment
		if err := rows.Scan(&a.ID, &wid, &a.Date, &a.Amount, &a.Reason, &a.SourceNo); err != nil {
			rows.Close()
			return nil, err
		}
		adjs[wid] = append(adjs[wid], a)
		adjIDs[wid] = append(adjIDs[wid], a.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	products := map[string][2]string{}
	if len(pieces) > 0 {
		prow, err := q.Query(ctx, `SELECT id, code, name FROM "Product"`)
		if err != nil {
			return nil, err
		}
		for prow.Next() {
			var id, code, name string
			if err := prow.Scan(&id, &code, &name); err != nil {
				prow.Close()
				return nil, err
			}
			products[id] = [2]string{code, name}
		}
		prow.Close()
		if err := prow.Err(); err != nil {
			return nil, err
		}
	}
	for _, id := range workerIDs {
		w, ok := workers[id]
		if !ok {
			continue
		}
		c := Calculate(Input{Worker: w, Marks: marks[id], Rules: rules, PieceRates: prates, Pieces: pieces[id], Adjustments: adjs[id], AsOf: to})
		for i := range c.Pieces {
			pr := products[c.Pieces[i].ProductID]
			c.Pieces[i].ProductCode, c.Pieces[i].ProductName = pr[0], pr[1]
		}
		out[id] = PeriodResult{Calc: c, AdjustmentIDs: adjIDs[id]}
	}
	return out, nil
}

// DayChange is an attendance mark before and after a correction.
type DayChange struct {
	Status        string
	OvertimeHours float64
}

// CorrectionDelta is the wage difference of a corrected mark for one worker, day and site (§7.4): the
// amount the next period must add (positive) or deduct (negative).
func CorrectionDelta(ctx context.Context, q db.DBTX, workerID, plantID string, date time.Time, before, after DayChange) (int, Rates, error) {
	ws, err := LoadWorkers(ctx, q, []string{workerID})
	if err != nil {
		return 0, Rates{}, err
	}
	w, ok := ws[workerID]
	if !ok {
		return 0, Rates{}, fmt.Errorf("worker %s not found", workerID)
	}
	rules, err := LoadRules(ctx, q, date, date)
	if err != nil {
		return 0, Rates{}, err
	}
	r, _ := RatesOn(rules, w, plantID, date)
	delta := DayAmount(w.RateBasis, after.Status, after.OvertimeHours, r) - DayAmount(w.RateBasis, before.Status, before.OvertimeHours, r)
	return delta, r, nil
}

// PaidPeriodFor returns the PAID wage sheet that already paid workerID for date ("" when none).
func PaidPeriodFor(ctx context.Context, q db.DBTX, workerID string, date time.Time) (id, number string, err error) {
	err = q.QueryRow(ctx, `SELECT p.id, p.number FROM "PayrollPeriod" p JOIN "PayrollLine" l ON l."periodId" = p.id
		WHERE l."workerId" = $1 AND p.status = 'PAID' AND p."startDate" <= $2 AND p."endDate" >= $2 LIMIT 1`, workerID, date).Scan(&id, &number)
	if db.IsNoRows(err) {
		return "", "", nil
	}
	return id, number, err
}

// PaidWorkers returns which of workerIDs were already paid by a PAID wage sheet covering date.
func PaidWorkers(ctx context.Context, q db.DBTX, workerIDs []string, date time.Time) (map[string]string, error) {
	out := map[string]string{}
	if len(workerIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT l."workerId", p.number FROM "PayrollPeriod" p JOIN "PayrollLine" l ON l."periodId" = p.id
		WHERE l."workerId" = ANY($1) AND p.status = 'PAID' AND p."startDate" <= $2 AND p."endDate" >= $2`, workerIDs, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var w, n string
		if err := rows.Scan(&w, &n); err != nil {
			return nil, err
		}
		out[w] = n
	}
	return out, rows.Err()
}

// AddAdjustment records an amount to carry into the worker's next wage sheet. Zero amounts are skipped.
func AddAdjustment(ctx context.Context, q db.DBTX, workerID, plantID string, date time.Time, amount int, reason, sourcePeriodID, correctionID, userID string) (string, error) {
	if amount == 0 {
		return "", nil
	}
	var id string
	err := q.QueryRow(ctx, `INSERT INTO wage_adjustment (worker_id, plant_id, date, amount, reason, source_period_id, correction_id, created_by)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''), NULLIF($8, '')) RETURNING id`,
		workerID, plantID, date, amount, reason, sourcePeriodID, correctionID, userID).Scan(&id)
	return id, err
}
