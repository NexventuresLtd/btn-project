package workforce

// Workforce reports (SRS WRK-02 daily headcount, WRK-06 site and consolidated reports): headcount, attendance and
// wage cost per site and period — on screen (JSON), as PDF and as CSV / Excel / JSON (export framework).

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/export"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/pdf"

	"github.com/gofiber/fiber/v2"
)

type attendanceAgg struct {
	Present       int     `json:"present"`
	Half          int     `json:"half"`
	Absent        int     `json:"absent"`
	Leave         int     `json:"leave"`
	PersonDays    float64 `json:"personDays"`
	OvertimeHours float64 `json:"overtimeHours"`
	Exceptions    int     `json:"exceptions"`
}

type headcountAgg struct {
	Registered int     `json:"registered"` // active workers on the register now
	Worked     int     `json:"worked"`     // distinct workers present at least once
	AvgDaily   float64 `json:"avgDaily"`
	Peak       int     `json:"peak"`
	DaysMarked int     `json:"daysMarked"`
}

type wageAgg struct {
	Sheets     int `json:"sheets"`
	Workers    int `json:"workers"`
	Basic      int `json:"basic"`
	Overtime   int `json:"overtime"`
	Piece      int `json:"piece"`
	Adjustment int `json:"adjustment"`
	Gross      int `json:"gross"`
	Deductions int `json:"deductions"`
	Net        int `json:"net"`
	Paid       int `json:"paid"`
}

type siteReport struct {
	Plant      plantRef      `json:"plant"`
	Headcount  headcountAgg  `json:"headcount"`
	Attendance attendanceAgg `json:"attendance"`
	Wages      wageAgg       `json:"wages"`
	CostPerDay int           `json:"costPerPersonDay"`
}

type periodReport struct {
	ID            string  `json:"id"`
	Number        string  `json:"number"`
	Status        string  `json:"status"`
	PlantCode     string  `json:"plantCode"`
	StartDate     string  `json:"startDate"`
	EndDate       string  `json:"endDate"`
	Workers       int     `json:"workers"`
	Days          float64 `json:"days"`
	OvertimeHours float64 `json:"overtimeHours"`
	Gross         int     `json:"gross"`
	Net           int     `json:"net"`
	Paid          int     `json:"paid"`
}

type summaryReport struct {
	From    string         `json:"from"`
	To      string         `json:"to"`
	Days    int            `json:"days"`
	Sites   []siteReport   `json:"sites"`
	Periods []periodReport `json:"periods"`
	Totals  siteReport     `json:"totals"`
}

func sitesInScope(ctx context.Context, q db.DBTX, plantID string) ([]plantRef, error) {
	rows, err := q.Query(ctx, `SELECT id, code, name FROM "Plant" WHERE "isActive" AND kind = 'SITE' AND ($1 = '' OR id = $1) ORDER BY code`, plantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []plantRef{}
	for rows.Next() {
		var p plantRef
		if err := rows.Scan(&p.ID, &p.Code, &p.Name); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (h *handler) buildSummary(ctx context.Context, plantID string, rg dates.Range) (*summaryReport, error) {
	sites, err := sitesInScope(ctx, h.d.Pool, plantID)
	if err != nil {
		return nil, err
	}
	nDays := int(rg.To.Sub(rg.From).Hours()/24) + 1
	rep := &summaryReport{From: rg.FromISO, To: rg.ToISO, Days: nDays, Sites: []siteReport{}, Periods: []periodReport{}}
	idx := map[string]int{}
	for i, p := range sites {
		rep.Sites = append(rep.Sites, siteReport{Plant: p})
		idx[p.ID] = i
	}
	q := h.d.Pool
	// 1 registered workers
	rows, err := q.Query(ctx, `SELECT "plantId", count(*) FROM "Worker" WHERE "isActive" GROUP BY "plantId"`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p string
		var n int
		if err := rows.Scan(&p, &n); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := idx[p]; ok {
			rep.Sites[i].Headcount.Registered = n
		}
	}
	rows.Close()
	// 2 attendance per site
	rows, err = q.Query(ctx, `SELECT a."plantId", count(*) FILTER (WHERE status = 'PRESENT'), count(*) FILTER (WHERE status = 'HALF_DAY'),
		count(*) FILTER (WHERE status = 'ABSENT'), count(*) FILTER (WHERE status = 'LEAVE'), COALESCE(sum("overtimeHours"), 0)::float8,
		count(*) FILTER (WHERE exception), count(DISTINCT "workerId") FILTER (WHERE status IN ('PRESENT','HALF_DAY'))
		FROM "Attendance" a WHERE a.date >= $1 AND a.date <= $2 GROUP BY a."plantId"`, rg.From, rg.To)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p string
		var a attendanceAgg
		var worked int
		if err := rows.Scan(&p, &a.Present, &a.Half, &a.Absent, &a.Leave, &a.OvertimeHours, &a.Exceptions, &worked); err != nil {
			rows.Close()
			return nil, err
		}
		a.PersonDays = float64(a.Present) + float64(a.Half)*0.5
		if i, ok := idx[p]; ok {
			rep.Sites[i].Attendance = a
			rep.Sites[i].Headcount.Worked = worked
		}
	}
	rows.Close()
	// 3 daily headcount → average and peak
	rows, err = q.Query(ctx, `SELECT a."plantId", count(*) FILTER (WHERE status IN ('PRESENT','HALF_DAY')) FROM "Attendance" a
		WHERE a.date >= $1 AND a.date <= $2 GROUP BY a."plantId", a.date`, rg.From, rg.To)
	if err != nil {
		return nil, err
	}
	sum := map[string]int{}
	for rows.Next() {
		var p string
		var n int
		if err := rows.Scan(&p, &n); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := idx[p]; ok {
			hc := &rep.Sites[i].Headcount
			hc.DaysMarked++
			sum[p] += n
			if n > hc.Peak {
				hc.Peak = n
			}
		}
	}
	rows.Close()
	for i := range rep.Sites {
		if hc := &rep.Sites[i].Headcount; hc.DaysMarked > 0 {
			hc.AvgDaily = float64(int(float64(sum[rep.Sites[i].Plant.ID])/float64(hc.DaysMarked)*10+0.5)) / 10
		}
	}
	// 4 wage sheets overlapping the range (wage cost per site and period) + payments in the range
	rows, err = q.Query(ctx, `SELECT p.id, p.number, p.status::text, p."plantId", pl.code, p."startDate", p."endDate",
		count(l.id) FILTER (WHERE l."daysWorked" > 0 OR l."grossAmount" <> 0), COALESCE(sum(l."daysWorked"), 0)::float8, COALESCE(sum(l."overtimeHours"), 0)::float8,
		COALESCE(sum(l."basicAmount"), 0), COALESCE(sum(l."overtimeAmount"), 0), COALESCE(sum(l."pieceAmount"), 0), COALESCE(sum(l."adjustmentAmount"), 0),
		COALESCE(sum(l."grossAmount"), 0), COALESCE(sum(l."foodDeduction" + l."otherDeduction"), 0), COALESCE(sum(l."netAmount"), 0),
		(SELECT COALESCE(sum(amount), 0) FROM wage_payment x WHERE x.period_id = p.id)
		FROM "PayrollPeriod" p JOIN "Plant" pl ON pl.id = p."plantId" LEFT JOIN "PayrollLine" l ON l."periodId" = p.id
		WHERE p."startDate" <= $2 AND p."endDate" >= $1 AND ($3 = '' OR p."plantId" = $3)
		GROUP BY p.id, pl.code ORDER BY p."startDate", pl.code`, rg.From, rg.To, plantID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var pr periodReport
		var plant string
		var sd, ed time.Time
		var w wageAgg
		if err := rows.Scan(&pr.ID, &pr.Number, &pr.Status, &plant, &pr.PlantCode, &sd, &ed, &pr.Workers, &pr.Days, &pr.OvertimeHours,
			&w.Basic, &w.Overtime, &w.Piece, &w.Adjustment, &w.Gross, &w.Deductions, &w.Net, &pr.Paid); err != nil {
			rows.Close()
			return nil, err
		}
		pr.StartDate, pr.EndDate, pr.Gross, pr.Net = dates.ToISO(sd), dates.ToISO(ed), w.Gross, w.Net
		rep.Periods = append(rep.Periods, pr)
		if i, ok := idx[plant]; ok {
			s := &rep.Sites[i].Wages
			s.Sheets++
			s.Workers += pr.Workers
			s.Basic += w.Basic
			s.Overtime += w.Overtime
			s.Piece += w.Piece
			s.Adjustment += w.Adjustment
			s.Gross += w.Gross
			s.Deductions += w.Deductions
			s.Net += w.Net
			s.Paid += pr.Paid
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	t := &rep.Totals
	t.Plant = plantRef{Code: "ALL"}
	for i := range rep.Sites {
		s := &rep.Sites[i]
		if s.Attendance.PersonDays > 0 {
			s.CostPerDay = int(float64(s.Wages.Gross)/s.Attendance.PersonDays + 0.5)
		}
		t.Headcount.Registered += s.Headcount.Registered
		t.Headcount.Worked += s.Headcount.Worked
		t.Headcount.AvgDaily += s.Headcount.AvgDaily
		t.Headcount.Peak += s.Headcount.Peak
		a := &t.Attendance
		a.Present += s.Attendance.Present
		a.Half += s.Attendance.Half
		a.Absent += s.Attendance.Absent
		a.Leave += s.Attendance.Leave
		a.PersonDays += s.Attendance.PersonDays
		a.OvertimeHours += s.Attendance.OvertimeHours
		a.Exceptions += s.Attendance.Exceptions
		w := &t.Wages
		w.Sheets += s.Wages.Sheets
		w.Workers += s.Wages.Workers
		w.Basic += s.Wages.Basic
		w.Overtime += s.Wages.Overtime
		w.Piece += s.Wages.Piece
		w.Adjustment += s.Wages.Adjustment
		w.Gross += s.Wages.Gross
		w.Deductions += s.Wages.Deductions
		w.Net += s.Wages.Net
		w.Paid += s.Wages.Paid
	}
	if t.Attendance.PersonDays > 0 {
		t.CostPerDay = int(float64(t.Wages.Gross)/t.Attendance.PersonDays + 0.5)
	}
	return rep, nil
}

func (h *handler) summaryParams(c *fiber.Ctx) (string, dates.Range, error) {
	u := auth.MustUser(c)
	qv := httpx.Query(c)
	plantQ := qv.OptStr("plantId").Get()
	from, to := qv.OptStr("from").Get(), qv.OptStr("to").Get()
	if err := qv.Err(); err != nil {
		return "", dates.Range{}, err
	}
	rg := dates.ResolveRange(from, to)
	if int(rg.To.Sub(rg.From).Hours()/24)+1 > 370 {
		return "", rg, httpx.BadRequest("error.validation", map[string]string{"to": t(c, "workforce.v.range_max", "n", 370)})
	}
	return auth.PlantFilter(u, plantQ), rg, nil
}

func (h *handler) reportSummary(c *fiber.Ctx) error {
	plantID, rg, err := h.summaryParams(c)
	if err != nil {
		return err
	}
	rep, err := h.buildSummary(reqCtx(c), plantID, rg)
	if err != nil {
		return err
	}
	httpx.NoStore(c)
	return c.JSON(rep)
}

// ── daily headcount (WRK-02) ──────────────────────────────────────────────────

type headcountRow struct {
	Date          string   `json:"date"`
	Plant         plantRef `json:"plant"`
	Registered    int      `json:"registered"`
	Present       int      `json:"present"`
	Half          int      `json:"half"`
	Absent        int      `json:"absent"`
	Leave         int      `json:"leave"`
	Unmarked      int      `json:"unmarked"`
	Headcount     int      `json:"headcount"`
	OvertimeHours float64  `json:"overtimeHours"`
	Exceptions    int      `json:"exceptions"`
	DayStatus     string   `json:"dayStatus"`
}

type headcountReport struct {
	From  string         `json:"from"`
	To    string         `json:"to"`
	Sites []plantRef     `json:"sites"`
	Rows  []headcountRow `json:"rows"`
	Total headcountRow   `json:"totals"`
}

func (h *handler) buildHeadcount(ctx context.Context, plantID string, rg dates.Range) (*headcountReport, error) {
	sites, err := sitesInScope(ctx, h.d.Pool, plantID)
	if err != nil {
		return nil, err
	}
	rows, err := h.d.Pool.Query(ctx, `WITH days AS (SELECT g::date AS d FROM generate_series($1::date, $2::date, interval '1 day') g)
		SELECT to_char(days.d, 'YYYY-MM-DD'), p.id, p.code, p.name,
			(SELECT count(*) FROM "Worker" w WHERE w."plantId" = p.id AND w."isActive" AND (w."startDate" IS NULL OR w."startDate" <= days.d)),
			COALESCE(m.present, 0), COALESCE(m.half, 0), COALESCE(m.absent, 0), COALESCE(m.leave, 0), COALESCE(m.ot, 0)::float8, COALESCE(m.exc, 0),
			COALESCE(m.home_marked, 0), COALESCE(ad.status, 'OPEN')
		FROM "Plant" p CROSS JOIN days
		LEFT JOIN attendance_day ad ON ad.plant_id = p.id AND ad.date = days.d
		LEFT JOIN LATERAL (SELECT count(*) FILTER (WHERE a.status = 'PRESENT') AS present, count(*) FILTER (WHERE a.status = 'HALF_DAY') AS half,
			count(*) FILTER (WHERE a.status = 'ABSENT') AS absent, count(*) FILTER (WHERE a.status = 'LEAVE') AS leave, sum(a."overtimeHours") AS ot,
			count(*) FILTER (WHERE a.exception) AS exc, count(*) FILTER (WHERE NOT a.exception) AS home_marked
			FROM "Attendance" a WHERE a."plantId" = p.id AND a.date = days.d) m ON true
		WHERE p."isActive" AND p.kind = 'SITE' AND ($3 = '' OR p.id = $3)
		ORDER BY days.d, p.code`, rg.From, rg.To, plantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rep := &headcountReport{From: rg.FromISO, To: rg.ToISO, Sites: sites, Rows: []headcountRow{}}
	for rows.Next() {
		var r headcountRow
		var homeMarked int
		if err := rows.Scan(&r.Date, &r.Plant.ID, &r.Plant.Code, &r.Plant.Name, &r.Registered, &r.Present, &r.Half, &r.Absent, &r.Leave, &r.OvertimeHours, &r.Exceptions,
			&homeMarked, &r.DayStatus); err != nil {
			return nil, err
		}
		r.Headcount = r.Present + r.Half
		if r.Unmarked = r.Registered - homeMarked; r.Unmarked < 0 {
			r.Unmarked = 0
		}
		rep.Rows = append(rep.Rows, r)
		t := &rep.Total
		t.Present += r.Present
		t.Half += r.Half
		t.Absent += r.Absent
		t.Leave += r.Leave
		t.Unmarked += r.Unmarked
		t.Headcount += r.Headcount
		t.OvertimeHours += r.OvertimeHours
		t.Exceptions += r.Exceptions
	}
	return rep, rows.Err()
}

func (h *handler) headcountParams(c *fiber.Ctx) (string, dates.Range, error) {
	plantID, rg, err := h.summaryParams(c)
	if err == nil && int(rg.To.Sub(rg.From).Hours()/24)+1 > 62 {
		err = httpx.BadRequest("error.validation", map[string]string{"to": t(c, "workforce.v.range_max", "n", 62)})
	}
	return plantID, rg, err
}

func (h *handler) reportHeadcount(c *fiber.Ctx) error {
	plantID, rg, err := h.headcountParams(c)
	if err != nil {
		return err
	}
	rep, err := h.buildHeadcount(reqCtx(c), plantID, rg)
	if err != nil {
		return err
	}
	httpx.NoStore(c)
	return c.JSON(rep)
}

// ── PDFs ──────────────────────────────────────────────────────────────────────

func siteLabel(c *fiber.Ctx, h *handler, plantID string) string {
	if plantID == "" {
		return t(c, "workforce.pdf.all_sites")
	}
	var code, name string
	_ = h.d.Pool.QueryRow(reqCtx(c), `SELECT code, name FROM "Plant" WHERE id = $1`, plantID).Scan(&code, &name)
	return code + " · " + name
}

func (h *handler) pdfSummary(c *fiber.Ctx) error {
	plantID, rg, err := h.summaryParams(c)
	if err != nil {
		return err
	}
	s, err := h.buildSummary(reqCtx(c), plantID, rg)
	if err != nil {
		return err
	}
	rangeTxt := dates.FormatRange(rg.From, rg.To)
	rep, err := pdf.New(c, h.d, pdf.Options{Landscape: true, Title: t(c, "workforce.pdf.summary_title"), Subtitle: rangeTxt,
		Meta: []pdf.Meta{{Label: t(c, "workforce.pdf.meta_sites"), Value: siteLabel(c, h, plantID)}, {Label: t(c, "workforce.pdf.meta_period"), Value: rangeTxt}}})
	if err != nil {
		return err
	}
	tt := s.Totals
	rep.KpiRow([]pdf.Kpi{
		{Label: rep.T("workforce.kpi.registered"), Value: format.Int(float64(tt.Headcount.Registered))},
		{Label: rep.T("workforce.kpi.person_days"), Value: format.Qty(tt.Attendance.PersonDays)},
		{Label: rep.T("workforce.kpi.overtime"), Value: format.Qty(tt.Attendance.OvertimeHours)},
		{Label: rep.T("workforce.kpi.gross"), Value: format.RWF(float64(tt.Wages.Gross))},
		{Label: rep.T("workforce.kpi.paid"), Value: format.RWF(float64(tt.Wages.Paid))},
	})
	cols := []pdf.Column{{Header: rep.T("workforce.col.site"), Width: 1.6},
		{Header: rep.T("workforce.col.registered"), Width: 0.9, Align: pdf.Right}, {Header: rep.T("workforce.col.worked"), Width: 0.9, Align: pdf.Right},
		{Header: rep.T("workforce.col.avg_daily"), Width: 0.9, Align: pdf.Right}, {Header: rep.T("workforce.col.peak"), Width: 0.7, Align: pdf.Right},
		{Header: rep.T("workforce.col.present"), Width: 0.8, Align: pdf.Right}, {Header: rep.T("workforce.col.half"), Width: 0.7, Align: pdf.Right},
		{Header: rep.T("workforce.col.absent"), Width: 0.7, Align: pdf.Right}, {Header: rep.T("workforce.col.leave"), Width: 0.7, Align: pdf.Right},
		{Header: rep.T("workforce.col.person_days"), Width: 0.9, Align: pdf.Right}, {Header: rep.T("workforce.col.overtime_hours"), Width: 0.9, Align: pdf.Right},
		{Header: rep.T("workforce.col.exceptions"), Width: 0.8, Align: pdf.Right}}
	row := func(s siteReport, label string) []any {
		return []any{label, s.Headcount.Registered, s.Headcount.Worked, format.Qty(s.Headcount.AvgDaily), s.Headcount.Peak, s.Attendance.Present, s.Attendance.Half,
			s.Attendance.Absent, s.Attendance.Leave, format.Qty(s.Attendance.PersonDays), format.Qty(s.Attendance.OvertimeHours), s.Attendance.Exceptions}
	}
	var rows [][]any
	for _, x := range s.Sites {
		rows = append(rows, row(x, x.Plant.Code))
	}
	rep.Section(rep.T("workforce.pdf.section_attendance")).Table(cols, rows, row(tt, rep.T("workforce.pdf.consolidated")), rep.T("workforce.pdf.empty"))
	wcols := []pdf.Column{{Header: rep.T("workforce.col.site"), Width: 1.6}, {Header: rep.T("workforce.col.sheets"), Width: 0.7, Align: pdf.Right},
		{Header: rep.T("workforce.col.basic"), Width: 1, Align: pdf.Right}, {Header: rep.T("workforce.col.overtime_pay"), Width: 1, Align: pdf.Right},
		{Header: rep.T("workforce.col.piece_pay"), Width: 1, Align: pdf.Right}, {Header: rep.T("workforce.col.adjustment"), Width: 0.9, Align: pdf.Right},
		{Header: rep.T("workforce.col.gross"), Width: 1.1, Align: pdf.Right}, {Header: rep.T("workforce.col.deductions"), Width: 1, Align: pdf.Right},
		{Header: rep.T("workforce.col.net"), Width: 1.1, Align: pdf.Right}, {Header: rep.T("workforce.col.paid"), Width: 1.1, Align: pdf.Right},
		{Header: rep.T("workforce.col.cost_per_day"), Width: 1, Align: pdf.Right}}
	m := func(n int) string { return format.Int(float64(n)) }
	wrow := func(s siteReport, label string) []any {
		w := s.Wages
		return []any{label, w.Sheets, m(w.Basic), m(w.Overtime), m(w.Piece), m(w.Adjustment), m(w.Gross), m(w.Deductions), m(w.Net), m(w.Paid), m(s.CostPerDay)}
	}
	rows = nil
	for _, x := range s.Sites {
		rows = append(rows, wrow(x, x.Plant.Code))
	}
	rep.Section(rep.T("workforce.pdf.section_wages")).Table(wcols, rows, wrow(tt, rep.T("workforce.pdf.consolidated")), rep.T("workforce.pdf.empty"))
	pcols := []pdf.Column{{Header: rep.T("workforce.col.sheet"), Width: 1.8}, {Header: rep.T("workforce.col.site"), Width: 0.8},
		{Header: rep.T("workforce.col.period"), Width: 1.9}, {Header: rep.T("workforce.col.status"), Width: 1},
		{Header: rep.T("workforce.col.workers"), Width: 0.8, Align: pdf.Right}, {Header: rep.T("workforce.col.days"), Width: 0.8, Align: pdf.Right},
		{Header: rep.T("workforce.col.overtime_hours"), Width: 0.9, Align: pdf.Right}, {Header: rep.T("workforce.col.gross"), Width: 1.1, Align: pdf.Right},
		{Header: rep.T("workforce.col.net"), Width: 1.1, Align: pdf.Right}, {Header: rep.T("workforce.col.paid"), Width: 1.1, Align: pdf.Right}}
	rows = nil
	for _, p := range s.Periods {
		rows = append(rows, []any{p.Number, p.PlantCode, dates.FormatISORange(p.StartDate, p.EndDate), rep.T("payroll.status." + p.Status), p.Workers,
			format.Qty(p.Days), format.Qty(p.OvertimeHours), m(p.Gross), m(p.Net), m(p.Paid)})
	}
	rep.Section(rep.T("workforce.pdf.section_periods")).Table(pcols, rows, nil, rep.T("workforce.pdf.empty"))
	suffix := "all-sites"
	if plantID != "" && len(s.Sites) == 1 {
		suffix = s.Sites[0].Plant.Code
	}
	return pdf.Send(c, rep, fmt.Sprintf("Workforce-Report_%s_%s_to_%s.pdf", suffix, rg.FromISO, rg.ToISO), c.Query("download") == "1")
}

func (h *handler) pdfHeadcount(c *fiber.Ctx) error {
	plantID, rg, err := h.headcountParams(c)
	if err != nil {
		return err
	}
	hc, err := h.buildHeadcount(reqCtx(c), plantID, rg)
	if err != nil {
		return err
	}
	rangeTxt := dates.FormatRange(rg.From, rg.To)
	rep, err := pdf.New(c, h.d, pdf.Options{Landscape: len(hc.Sites) > 3, Title: t(c, "workforce.pdf.headcount_title"), Subtitle: rangeTxt,
		Meta: []pdf.Meta{{Label: t(c, "workforce.pdf.meta_sites"), Value: siteLabel(c, h, plantID)}, {Label: t(c, "workforce.pdf.meta_period"), Value: rangeTxt}}})
	if err != nil {
		return err
	}
	// pivot: one row per day, one headcount column per site + total
	cols := []pdf.Column{{Header: rep.T("workforce.col.date"), Width: 1.1}}
	for _, s := range hc.Sites {
		cols = append(cols, pdf.Column{Header: s.Code, Width: 0.8, Align: pdf.Right})
	}
	cols = append(cols, pdf.Column{Header: rep.T("workforce.col.total"), Width: 0.9, Align: pdf.Right},
		pdf.Column{Header: rep.T("workforce.col.overtime_hours"), Width: 0.9, Align: pdf.Right})
	byDate := map[string]map[string]headcountRow{}
	order := []string{}
	for _, r := range hc.Rows {
		if byDate[r.Date] == nil {
			byDate[r.Date] = map[string]headcountRow{}
			order = append(order, r.Date)
		}
		byDate[r.Date][r.Plant.ID] = r
	}
	sort.Strings(order)
	var rows [][]any
	for _, d := range order {
		row := []any{export.FormatDateISO(d)}
		tot, ot := 0, 0.0
		for _, s := range hc.Sites {
			r := byDate[d][s.ID]
			cell := strconv.Itoa(r.Headcount)
			if r.DayStatus == "APPROVED" {
				cell += " ✓"
			}
			row = append(row, cell)
			tot += r.Headcount
			ot += r.OvertimeHours
		}
		rows = append(rows, append(row, tot, format.Qty(ot)))
	}
	totals := []any{rep.T("workforce.col.total")}
	for range hc.Sites {
		totals = append(totals, nil)
	}
	totals = append(totals, hc.Total.Headcount, format.Qty(hc.Total.OvertimeHours))
	rep.Note(rep.T("workforce.pdf.headcount_note"))
	rep.Table(cols, rows, totals, rep.T("workforce.pdf.empty"))
	return pdf.Send(c, rep, fmt.Sprintf("Daily-Headcount_%s_to_%s.pdf", rg.FromISO, rg.ToISO), c.Query("download") == "1")
}

// pdfWorkerPayments: one worker's payment history (WRK-04).
func (h *handler) pdfWorkerPayments(c *fiber.Ctx) error {
	hist, err := h.loadHistory(c)
	if err != nil {
		return err
	}
	w := hist.Worker
	rep, err := pdf.New(c, h.d, pdf.Options{Title: t(c, "workforce.pdf.payments_title", "name", w.FullName), Subtitle: w.Plant.Code + " · " + w.JobTitle,
		Meta: []pdf.Meta{{Label: t(c, "workforce.col.rate_basis"), Value: t(c, "workforce.basis."+w.RateBasis)}, {Label: t(c, "workforce.col.status"), Value: t(c, "workforce.status."+w.EmploymentStatus)}}})
	if err != nil {
		return err
	}
	rep.KpiRow([]pdf.Kpi{{Label: rep.T("workforce.kpi.payments"), Value: format.Int(float64(hist.Totals.Count))}, {Label: rep.T("workforce.kpi.paid"), Value: format.RWF(float64(hist.Totals.Amount))}})
	var rows [][]any
	for _, p := range hist.Payments {
		rows = append(rows, []any{export.FormatDateISO(p.Date), rep.T("workforce.method." + p.Method), format.Int(float64(p.Amount)), strPtr(p.Reference), strPtr(p.PeriodNumber), strPtr(p.PaidBy), strPtr(p.PettyCashEntry)})
	}
	rep.Section(rep.T("workforce.pdf.section_payments")).Table([]pdf.Column{{Header: rep.T("workforce.col.date"), Width: 1}, {Header: rep.T("workforce.col.method"), Width: 0.9},
		{Header: rep.T("workforce.col.amount"), Width: 1, Align: pdf.Right}, {Header: rep.T("workforce.col.reference"), Width: 1.3}, {Header: rep.T("workforce.col.sheet"), Width: 1.6},
		{Header: rep.T("workforce.col.paid_by"), Width: 1.3}, {Header: rep.T("workforce.col.petty_cash"), Width: 1.2}}, rows, []any{rep.T("workforce.col.total"), nil, format.Int(float64(hist.Totals.Amount)), nil, nil, nil, nil}, rep.T("workforce.pdf.empty"))
	if len(hist.Adjustments) > 0 {
		var arows [][]any
		for _, a := range hist.Adjustments {
			arows = append(arows, []any{export.FormatDateISO(a.Date), format.Int(float64(a.Amount)), a.Reason, strPtr(a.SourcePeriod), strPtr(a.AppliedPeriod)})
		}
		rep.Section(rep.T("workforce.pdf.section_adjustments")).Table([]pdf.Column{{Header: rep.T("workforce.col.date"), Width: 1}, {Header: rep.T("workforce.col.amount"), Width: 1, Align: pdf.Right},
			{Header: rep.T("workforce.col.reason"), Width: 3.5}, {Header: rep.T("workforce.col.source_sheet"), Width: 1.5}, {Header: rep.T("workforce.col.applied_sheet"), Width: 1.5}}, arows, nil)
	}
	if err := logAccess(reqCtx(c), h.d.Pool, c, w.ID, "pdf"); err != nil {
		return err
	}
	return pdf.Send(c, rep, fmt.Sprintf("Wage-Payments_%s.pdf", w.ID), c.Query("download") == "1")
}

func strPtr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ── exports (CSV / Excel / JSON) ───────────────────────────────────────────────

func init() {
	col := func(key, header string, k export.Kind) export.Column {
		return export.Column{Key: key, Header: header, Kind: k}
	}
	export.Register(export.Report{Path: "/workforce/reports/summary", Name: "workforce-report", Title: "workforce.pdf.summary_title",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("site", "workforce.col.site", export.Text), col("registered", "workforce.col.registered", export.Int), col("worked", "workforce.col.worked", export.Int),
				col("avgDaily", "workforce.col.avg_daily", export.Qty), col("peak", "workforce.col.peak", export.Int), col("present", "workforce.col.present", export.Int),
				col("half", "workforce.col.half", export.Int), col("absent", "workforce.col.absent", export.Int), col("leave", "workforce.col.leave", export.Int),
				col("personDays", "workforce.col.person_days", export.Qty), col("overtimeHours", "workforce.col.overtime_hours", export.Qty),
				col("exceptions", "workforce.col.exceptions", export.Int), col("sheets", "workforce.col.sheets", export.Int), col("basic", "workforce.col.basic", export.Money),
				col("overtime", "workforce.col.overtime_pay", export.Money), col("piece", "workforce.col.piece_pay", export.Money), col("adjustment", "workforce.col.adjustment", export.Money),
				col("gross", "workforce.col.gross", export.Money), col("deductions", "workforce.col.deductions", export.Money), col("net", "workforce.col.net", export.Money),
				col("paid", "workforce.col.paid", export.Money), col("costPerDay", "workforce.col.cost_per_day", export.Money))
			row := func(s export.J, label any) {
				b.Row(label, s.V("headcount.registered"), s.V("headcount.worked"), s.V("headcount.avgDaily"), s.V("headcount.peak"), s.V("attendance.present"),
					s.V("attendance.half"), s.V("attendance.absent"), s.V("attendance.leave"), s.V("attendance.personDays"), s.V("attendance.overtimeHours"),
					s.V("attendance.exceptions"), s.V("wages.sheets"), s.V("wages.basic"), s.V("wages.overtime"), s.V("wages.piece"), s.V("wages.adjustment"),
					s.V("wages.gross"), s.V("wages.deductions"), s.V("wages.net"), s.V("wages.paid"), s.V("costPerPersonDay"))
			}
			for _, s := range j.Arr("sites") {
				row(s, s.V("plant.code"))
			}
			row(j.Get("totals"), b.T("workforce.pdf.consolidated"))
		}})
	export.Register(export.Report{Path: "/workforce/reports/headcount", Name: "daily-headcount", Title: "workforce.pdf.headcount_title",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("date", "workforce.col.date", export.Date), col("site", "workforce.col.site", export.Text), col("registered", "workforce.col.registered", export.Int),
				col("present", "workforce.col.present", export.Int), col("half", "workforce.col.half", export.Int), col("absent", "workforce.col.absent", export.Int),
				col("leave", "workforce.col.leave", export.Int), col("unmarked", "workforce.col.unmarked", export.Int), col("headcount", "workforce.col.headcount", export.Int),
				col("overtimeHours", "workforce.col.overtime_hours", export.Qty), col("exceptions", "workforce.col.exceptions", export.Int), col("dayStatus", "workforce.col.day_status", export.Text))
			for _, r := range j.Arr("rows") {
				b.Row(r.V("date"), r.V("plant.code"), r.V("registered"), r.V("present"), r.V("half"), r.V("absent"), r.V("leave"), r.V("unmarked"), r.V("headcount"),
					r.V("overtimeHours"), r.V("exceptions"), b.T("workforce.day."+r.S("dayStatus")))
			}
		}})
	payCols := func(b *export.Builder) {
		b.Columns(col("date", "workforce.col.date", export.Date), col("worker", "workforce.col.worker", export.Text), col("site", "workforce.col.site", export.Text),
			col("method", "workforce.col.method", export.Text), col("amount", "workforce.col.amount", export.Money), col("reference", "workforce.col.reference", export.Text),
			col("sheet", "workforce.col.sheet", export.Text), col("paidBy", "workforce.col.paid_by", export.Text), col("pettyCash", "workforce.col.petty_cash", export.Text))
	}
	payRow := func(b *export.Builder, p export.J) {
		b.Row(p.V("date"), p.V("worker.name"), p.V("plant.code"), b.T("workforce.method."+p.S("method")), p.V("amount"), p.V("reference"), p.V("periodNumber"), p.V("paidBy"), p.V("pettyCashEntry"))
	}
	export.Register(export.Report{Path: "/workforce/payments", Name: "wage-payments", Title: "workforce.export.payments", Personal: true,
		Build: func(b *export.Builder, j export.J) {
			payCols(b)
			for _, p := range j.Arr("items") {
				payRow(b, p)
			}
			b.Row(b.T("workforce.col.total"), nil, nil, nil, j.V("totals.amount"), nil, nil, nil, nil)
		}})
	export.Register(export.Report{Path: "/workforce/workers/:id/payments", Name: "worker-payments", Title: "workforce.export.worker_payments", Personal: true,
		Build: func(b *export.Builder, j export.J) {
			b.Name("wage-payments_" + j.S("worker.fullName"))
			b.Filter("workforce.col.worker", j.S("worker.fullName"))
			payCols(b)
			for _, p := range j.Arr("items") {
				payRow(b, p)
			}
			b.Row(b.T("workforce.col.total"), nil, nil, nil, j.V("totals.amount"), nil, nil, nil, nil)
		}})
}
