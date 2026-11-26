package workforce

// Wage payment records (SRS WRK-04): one row per worker and payment — date, method (MoMo / cash / bank), amount,
// reference, paid by — written when a wage sheet is paid (internal/payroll), linked to petty cash when the pay
// step posted it. Personal data (NFR-D2): access is wages.view, and every read is logged by privacy.AccessLogger
// (routes under /workforce/workers/:id…) or by privacy.LogAccess here.

import (
	"context"
	"strconv"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
)

type paymentOut struct {
	ID             string   `json:"id"`
	Date           string   `json:"date"`
	Method         string   `json:"method"`
	Amount         int      `json:"amount"`
	Reference      *string  `json:"reference"`
	PaidBy         *string  `json:"paidBy"`
	PettyCashEntry *string  `json:"pettyCashEntry"`
	Note           *string  `json:"note"`
	Worker         plantRef `json:"worker"` // id, code (= national id or ""), name
	Plant          plantRef `json:"plant"`
	PeriodID       *string  `json:"periodId"`
	PeriodNumber   *string  `json:"periodNumber"`
	CreatedAt      string   `json:"createdAt"`
}

type paymentTotals struct {
	Count    int            `json:"count"`
	Amount   int            `json:"amount"`
	ByMethod map[string]int `json:"byMethod"`
}

const paymentSelect = `SELECT x.id, x.date, x.method, x.amount, x.reference, u.name, x.petty_cash_entry, x.note, w.id, COALESCE(w."nationalId", ''), w."firstName", w."lastName",
	p.id, p.code, p.name, x.period_id, pp.number, x.created_at
	FROM wage_payment x JOIN "Worker" w ON w.id = x.worker_id JOIN "Plant" p ON p.id = x.plant_id
	LEFT JOIN "PayrollPeriod" pp ON pp.id = x.period_id LEFT JOIN "User" u ON u.id = x.paid_by`

func scanPayments(ctx context.Context, q db.DBTX, sql string, args ...any) ([]paymentOut, paymentTotals, error) {
	rows, err := q.Query(ctx, sql, args...)
	tot := paymentTotals{ByMethod: map[string]int{}}
	if err != nil {
		return nil, tot, err
	}
	defer rows.Close()
	out := []paymentOut{}
	for rows.Next() {
		var o paymentOut
		var d, ca time.Time
		var f, l string
		if err := rows.Scan(&o.ID, &d, &o.Method, &o.Amount, &o.Reference, &o.PaidBy, &o.PettyCashEntry, &o.Note, &o.Worker.ID, &o.Worker.Code, &f, &l,
			&o.Plant.ID, &o.Plant.Code, &o.Plant.Name, &o.PeriodID, &o.PeriodNumber, &ca); err != nil {
			return nil, tot, err
		}
		o.Date, o.CreatedAt, o.Worker.Name = dates.ToISO(d), isoTime(ca), fullName(f, l)
		out = append(out, o)
	}
	return out, tot, rows.Err()
}

// listPayments: GET /payments?plantId&from&to&method&workerId&page (default: the current month).
func (h *handler) listPayments(c *fiber.Ctx) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	qv := httpx.Query(c)
	plantQ := qv.OptStr("plantId").Get()
	method := qv.OptStr("method").Trim().Upper().OneOf(append([]string{""}, PaymentMethods...), "workforce.v.method").Get()
	workerID := qv.OptStr("workerId").Trim().Max(40, "workforce.v.max_80").Get()
	from, to := qv.OptStr("from").Get(), qv.OptStr("to").Get()
	if err := qv.Err(); err != nil {
		return err
	}
	rg := dates.ResolveRange(from, to)
	plantID := auth.PlantFilter(u, plantQ)
	where, args := `x.date >= $1 AND x.date <= $2`, []any{rg.From, rg.To}
	add := func(cond string, v any) {
		args = append(args, v)
		where += " AND " + cond + strconv.Itoa(len(args))
	}
	if plantID != "" {
		add(`x.plant_id = $`, plantID)
	}
	if method != "" {
		add(`x.method = $`, method)
	}
	if workerID != "" {
		add(`x.worker_id = $`, workerID)
	}
	pg := httpx.Paginate(c, 50)
	tot := paymentTotals{ByMethod: map[string]int{}}
	mrows, err := h.d.Pool.Query(ctx, `SELECT x.method, count(*), COALESCE(sum(x.amount), 0) FROM wage_payment x WHERE `+where+` GROUP BY x.method`, args...)
	if err != nil {
		return err
	}
	for mrows.Next() {
		var m string
		var n, a int
		if err := mrows.Scan(&m, &n, &a); err != nil {
			mrows.Close()
			return err
		}
		tot.Count += n
		tot.Amount += a
		tot.ByMethod[m] = a
	}
	mrows.Close()
	items, _, err := scanPayments(ctx, h.d.Pool, paymentSelect+` WHERE `+where+` ORDER BY x.date DESC, p.code, w."firstName", w."lastName" LIMIT `+
		strconv.Itoa(pg.Limit)+` OFFSET `+strconv.Itoa(pg.Offset), args...)
	if err != nil {
		return err
	}
	if err := logAccess(ctx, h.d.Pool, c, workerID, "list"); err != nil {
		return err
	}
	httpx.NoStore(c)
	return c.JSON(fiber.Map{"items": items, "total": tot.Count, "page": pg.Page, "pageSize": pg.PageSize, "totals": tot, "from": rg.FromISO, "to": rg.ToISO})
}

type wageLineOut struct {
	PeriodID      string  `json:"periodId"`
	Number        string  `json:"number"`
	Status        string  `json:"status"`
	Plant         string  `json:"plant"`
	StartDate     string  `json:"startDate"`
	EndDate       string  `json:"endDate"`
	Days          float64 `json:"days"`
	OvertimeHours float64 `json:"overtimeHours"`
	Gross         int     `json:"gross"`
	Deductions    int     `json:"deductions"`
	Net           int     `json:"net"`
	Adjustment    int     `json:"adjustment"`
	IsOverride    bool    `json:"isOverride"`
}

type adjustmentOut struct {
	ID            string  `json:"id"`
	Date          string  `json:"date"`
	Amount        int     `json:"amount"`
	Reason        string  `json:"reason"`
	SourcePeriod  *string `json:"sourcePeriod"`
	AppliedPeriod *string `json:"appliedPeriod"`
	CreatedAt     string  `json:"createdAt"`
}

type workerHistory struct {
	Worker struct {
		ID               string   `json:"id"`
		FullName         string   `json:"fullName"`
		NationalID       *string  `json:"nationalId"`
		Phone            *string  `json:"phone"`
		MomoNumber       *string  `json:"momoNumber"`
		JobTitle         string   `json:"jobTitle"`
		RateBasis        string   `json:"rateBasis"`
		EmploymentStatus string   `json:"employmentStatus"`
		DailyWage        int      `json:"dailyWage"`
		Plant            plantRef `json:"plant"`
	} `json:"worker"`
	Payments    []paymentOut    `json:"items"`
	Totals      paymentTotals   `json:"totals"`
	Lines       []wageLineOut   `json:"lines"`
	Adjustments []adjustmentOut `json:"adjustments"`
	Rates       Rates           `json:"currentRates"`
}

func (h *handler) loadHistory(c *fiber.Ctx) (*workerHistory, error) {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	id := c.Params("id")
	var hist workerHistory
	w := &hist.Worker
	var f, l string
	err := h.d.Pool.QueryRow(ctx, `SELECT w.id, w."firstName", w."lastName", w."nationalId", w.phone, w."momoNumber", w."jobTitle", w."rateBasis", w."employmentStatus",
		w."dailyWage", p.id, p.code, p.name FROM "Worker" w JOIN "Plant" p ON p.id = w."plantId" WHERE w.id = $1`, id).
		Scan(&w.ID, &f, &l, &w.NationalID, &w.Phone, &w.MomoNumber, &w.JobTitle, &w.RateBasis, &w.EmploymentStatus, &w.DailyWage, &w.Plant.ID, &w.Plant.Code, &w.Plant.Name)
	if db.IsNoRows(err) {
		return nil, httpx.NotFound("entity.worker")
	}
	if err != nil {
		return nil, err
	}
	w.FullName = fullName(f, l)
	if err := auth.AssertPlantAccess(u, w.Plant.ID); err != nil {
		return nil, err
	}
	items, _, err := scanPayments(ctx, h.d.Pool, paymentSelect+` WHERE x.worker_id = $1 ORDER BY x.date DESC, x.created_at DESC LIMIT 1000`, id)
	if err != nil {
		return nil, err
	}
	hist.Payments = items
	hist.Totals = paymentTotals{ByMethod: map[string]int{}}
	for _, p := range items {
		hist.Totals.Count++
		hist.Totals.Amount += p.Amount
		hist.Totals.ByMethod[p.Method] += p.Amount
	}
	rows, err := h.d.Pool.Query(ctx, `SELECT p.id, p.number, p.status::text, pl.code, p."startDate", p."endDate", l."daysWorked"::float8, l."overtimeHours"::float8,
		l."grossAmount", l."foodDeduction" + l."otherDeduction", l."netAmount", l."adjustmentAmount", l."isOverride"
		FROM "PayrollLine" l JOIN "PayrollPeriod" p ON p.id = l."periodId" JOIN "Plant" pl ON pl.id = p."plantId"
		WHERE l."workerId" = $1 ORDER BY p."startDate" DESC LIMIT 200`, id)
	if err != nil {
		return nil, err
	}
	hist.Lines = []wageLineOut{}
	for rows.Next() {
		var x wageLineOut
		var sd, ed time.Time
		if err := rows.Scan(&x.PeriodID, &x.Number, &x.Status, &x.Plant, &sd, &ed, &x.Days, &x.OvertimeHours, &x.Gross, &x.Deductions, &x.Net, &x.Adjustment, &x.IsOverride); err != nil {
			rows.Close()
			return nil, err
		}
		x.StartDate, x.EndDate = dates.ToISO(sd), dates.ToISO(ed)
		hist.Lines = append(hist.Lines, x)
	}
	rows.Close()
	arows, err := h.d.Pool.Query(ctx, `SELECT a.id, a.date, a.amount, a.reason, sp.number, ap.number, a.created_at FROM wage_adjustment a
		LEFT JOIN "PayrollPeriod" sp ON sp.id = a.source_period_id LEFT JOIN "PayrollPeriod" ap ON ap.id = a.applied_period_id
		WHERE a.worker_id = $1 ORDER BY a.created_at DESC LIMIT 200`, id)
	if err != nil {
		return nil, err
	}
	hist.Adjustments = []adjustmentOut{}
	for arows.Next() {
		var a adjustmentOut
		var d, ca time.Time
		if err := arows.Scan(&a.ID, &d, &a.Amount, &a.Reason, &a.SourcePeriod, &a.AppliedPeriod, &ca); err != nil {
			arows.Close()
			return nil, err
		}
		a.Date, a.CreatedAt = dates.ToISO(d), isoTime(ca)
		hist.Adjustments = append(hist.Adjustments, a)
	}
	arows.Close()
	today := dates.MustParseISO(dates.TodayISO())
	rules, err := LoadRules(ctx, h.d.Pool, today, today)
	if err != nil {
		return nil, err
	}
	hist.Rates, _ = RatesOn(rules, Worker{ID: w.ID, PlantID: w.Plant.ID, JobTitle: w.JobTitle, RateBasis: w.RateBasis, DailyWage: w.DailyWage}, w.Plant.ID, today)
	return &hist, nil
}

// workerPayments: GET /workers/:id/payments — payment history, wage lines and adjustments of one worker.
func (h *handler) workerPayments(c *fiber.Ctx) error {
	hist, err := h.loadHistory(c)
	if err != nil {
		return err
	}
	httpx.NoStore(c)
	return c.JSON(hist)
}

// logAccess writes an NFR-D2 personal-data access row (same table and shape as privacy.LogAccess, which this
// package cannot import: privacy → payroll → workforce).
func logAccess(ctx context.Context, q db.DBTX, c *fiber.Ctx, subjectID, action string) error {
	uid, dev := "", ""
	if u := auth.FromCtx(c); u != nil {
		uid, dev = u.ID, u.DeviceID
	}
	c.Locals("privacy.logged", true)
	_, err := q.Exec(ctx, `INSERT INTO privacy_access_log (user_id, subject_type, subject_id, action, route, ip, device_id, rows, created_at)
		VALUES (NULLIF($1, ''), 'worker', NULLIF($2, ''), $3, $4, NULLIF($5, ''), NULLIF($6, ''), 0, $7)`, uid, subjectID, action, c.Path(), c.IP(), dev, db.Now())
	return err
}
