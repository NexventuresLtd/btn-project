package production

// PRD-08 summaries by day, week, month or a custom period, per site and consolidated, with yield, defect rate,
// planned targets (PRD-05) and the defect breakdown by reason (PRD-02). Approved entries only (confirmed figures);
// entries awaiting approval are counted separately.
//
//	GET /summary/periods?from=&to=&plantId=&groupBy=day|week|month|period   (+ ?format=csv|xlsx|json, /export)
//	GET /pdf/periods?…                                                       PDF (landscape by default)

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/pdf"
	"btn-ops/internal/stock"

	"github.com/gofiber/fiber/v2"
)

var groupBys = []string{"day", "week", "month", "period"}

type periodFig struct {
	LineTotals
	Achievement     *float64 `json:"achievement"`
	RejectionRate   *float64 `json:"rejectionRate"`
	Yield           *float64 `json:"yield"`
	DefectRate      *float64 `json:"defectRate"`
	Planned         int      `json:"planned"`
	PlanAchievement *float64 `json:"planAchievement"`
	Entries         int      `json:"entries"`
	DowntimeMins    int      `json:"downtimeMins"`
}

func (f *periodFig) finish() {
	t := f.LineTotals
	t.GoodOutput = t.Produced - t.DamageProd - t.DamageHand - t.Samples
	f.LineTotals = t
	f.Achievement = Achievement(t.Target, t.Produced)
	f.RejectionRate = RejectionRate(t.DamageProd, t.DamageHand, t.Produced)
	f.Yield = Yield(t.GoodOutput, t.Produced)
	f.DefectRate = DefectRate(t.DamageProd, t.DamageHand, t.Produced)
	f.PlanAchievement = Achievement(f.Planned, t.Produced)
}

func (f *periodFig) add(o periodFig) {
	f.LineTotals = addTotals(f.LineTotals, o.LineTotals)
	f.Entries += o.Entries
	f.DowntimeMins += o.DowntimeMins
}

type periodPlant struct {
	Plant plantOut `json:"plant"`
	periodFig
}

type periodBucket struct {
	From   string        `json:"from"`
	To     string        `json:"to"`
	Plants []periodPlant `json:"plants"`
	Totals periodFig     `json:"totals"`
}

type defectAgg struct {
	ReasonCode string   `json:"reasonCode"`
	Reason     string   `json:"reason"`
	Stage      string   `json:"stage"`
	Qty        int      `json:"qty"`
	Share      *float64 `json:"share"` // of all defects
	Rate       *float64 `json:"rate"`  // of produced
}

// PeriodSummary is the PRD-08 report.
type PeriodSummary struct {
	From           string         `json:"from"`
	To             string         `json:"to"`
	GroupBy        string         `json:"groupBy"`
	Buckets        []periodBucket `json:"buckets"`
	Plants         []periodPlant  `json:"plants"`
	Totals         periodFig      `json:"totals"`
	Defects        []defectAgg    `json:"defects"`
	PendingEntries int            `json:"pendingEntries"`
	VarianceFlags  int            `json:"varianceFlags"`
	ExceptionFlags []defectAgg    `json:"exceptionFlags"`
}

func periodSummary(ctx context.Context, q db.DBTX, from, to time.Time, plantID, groupBy string, lang i18n.Lang) (*PeriodSummary, error) {
	out := &PeriodSummary{From: dates.ToISO(from), To: dates.ToISO(to), GroupBy: groupBy, Buckets: []periodBucket{}, Plants: []periodPlant{},
		Defects: []defectAgg{}, ExceptionFlags: []defectAgg{}}
	// sites in scope
	rows, err := q.Query(ctx, `SELECT id, code, name FROM "Plant" WHERE kind = 'SITE' AND ("isActive" OR id IN (SELECT "plantId" FROM "ProductionReport" WHERE "date" BETWEEN $1 AND $2))
		AND ($3 = '' OR id = $3) ORDER BY code`, from, to, plantID)
	if err != nil {
		return nil, err
	}
	var plants []plantOut
	for rows.Next() {
		var p plantOut
		if err := rows.Scan(&p.ID, &p.Code, &p.Name); err != nil {
			rows.Close()
			return nil, err
		}
		plants = append(plants, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// figures per site and day (approved), one query
	type pd struct {
		plant string
		d     time.Time
	}
	daily := map[pd]periodFig{}
	rows, err = q.Query(ctx, `SELECT r."plantId", r."date", count(DISTINCT r.id)::int, 0,
			COALESCE(SUM(l."targetQty"), 0)::int, COALESCE(SUM(l."producedQty"), 0)::int, COALESCE(SUM(l."damageProdQty"), 0)::int,
			COALESCE(SUM(l."damageHandQty"), 0)::int, COALESCE(SUM(l."sampleQty"), 0)::int
		FROM "ProductionReport" r LEFT JOIN "ProductionLine" l ON l."reportId" = r.id
		WHERE r.status = 'APPROVED' AND r."date" BETWEEN $1 AND $2 AND ($3 = '' OR r."plantId" = $3) GROUP BY 1, 2`, from, to, plantID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k pd
		var f periodFig
		if err := rows.Scan(&k.plant, &k.d, &f.Entries, &f.DowntimeMins, &f.Target, &f.Produced, &f.DamageProd, &f.DamageHand, &f.Samples); err != nil {
			rows.Close()
			return nil, err
		}
		daily[k] = f
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// downtime is per entry, not per line: summed separately
	rows, err = q.Query(ctx, `SELECT r."plantId", r."date", SUM(r."downtimeMins")::int FROM "ProductionReport" r
		WHERE r.status = 'APPROVED' AND r."date" BETWEEN $1 AND $2 AND ($3 = '' OR r."plantId" = $3) GROUP BY 1, 2`, from, to, plantID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k pd
		var n int
		if err := rows.Scan(&k.plant, &k.d, &n); err != nil {
			rows.Close()
			return nil, err
		}
		f := daily[k]
		f.DowntimeMins = n
		daily[k] = f
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ts, err := loadTargets(ctx, q, plantID, "", from, to)
	if err != nil {
		return nil, err
	}
	targets := toTargets(ts)
	plannedFor := func(a, b time.Time) map[string]float64 {
		out := map[string]float64{}
		for k, v := range Planned(targets, a, b) {
			out[k.PlantID] += v
		}
		return out
	}
	rangeTotals := map[string]*periodFig{}
	for _, p := range plants {
		rangeTotals[p.ID] = &periodFig{}
	}
	for _, bk := range Buckets(groupBy, from, to) {
		b := periodBucket{From: dates.ToISO(bk[0]), To: dates.ToISO(bk[1]), Plants: []periodPlant{}}
		planned := plannedFor(bk[0], bk[1])
		for _, p := range plants {
			var f periodFig
			for d := bk[0]; !d.After(bk[1]); d = d.AddDate(0, 0, 1) {
				f.add(daily[pd{p.ID, d}])
			}
			f.Planned = int(math.Round(planned[p.ID]))
			rt := rangeTotals[p.ID]
			rt.add(f)
			rt.Planned += f.Planned
			f.finish()
			b.Plants = append(b.Plants, periodPlant{Plant: p, periodFig: f})
			b.Totals.add(f)
			b.Totals.Planned += f.Planned
		}
		b.Totals.finish()
		out.Buckets = append(out.Buckets, b)
	}
	for _, p := range plants {
		f := *rangeTotals[p.ID]
		f.finish()
		out.Plants = append(out.Plants, periodPlant{Plant: p, periodFig: f})
		out.Totals.add(f)
		out.Totals.Planned += f.Planned
	}
	out.Totals.finish()
	// defect breakdown by reason + unclassified remainder (migrated or legacy figures)
	reasons, err := refNames(ctx, q, "defect_reason", lang)
	if err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, `SELECT d."reasonCode", d.stage, SUM(d.quantity)::int FROM production_defect d JOIN "ProductionReport" r ON r.id = d."reportId"
		WHERE r.status = 'APPROVED' AND r."date" BETWEEN $1 AND $2 AND ($3 = '' OR r."plantId" = $3) GROUP BY 1, 2 ORDER BY 3 DESC, 1`, from, to, plantID)
	if err != nil {
		return nil, err
	}
	classified := 0
	for rows.Next() {
		var a defectAgg
		if err := rows.Scan(&a.ReasonCode, &a.Stage, &a.Qty); err != nil {
			rows.Close()
			return nil, err
		}
		a.Reason = reasons[a.ReasonCode]
		if a.Reason == "" {
			a.Reason = a.ReasonCode
		}
		classified += a.Qty
		out.Defects = append(out.Defects, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	allDef := out.Totals.DamageProd + out.Totals.DamageHand
	if rest := allDef - classified; rest > 0 {
		out.Defects = append(out.Defects, defectAgg{ReasonCode: "", Reason: i18n.T(lang, "production.unclassified"), Stage: "", Qty: rest})
	}
	for i := range out.Defects {
		out.Defects[i].Share = safe(float64(out.Defects[i].Qty), float64(allDef))
		out.Defects[i].Rate = safe(float64(out.Defects[i].Qty), float64(out.Totals.Produced))
	}
	// exceptions (PRD-09) and variance flags (PRD-10) in the period
	flagNames, err := refNames(ctx, q, "exception_flag", lang)
	if err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, `SELECT f.code, count(*)::int FROM production_exception f JOIN "ProductionReport" r ON r.id = f."reportId"
		WHERE r.status IN ('SUBMITTED','APPROVED') AND r."date" BETWEEN $1 AND $2 AND ($3 = '' OR r."plantId" = $3) GROUP BY 1 ORDER BY 2 DESC, 1`, from, to, plantID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a defectAgg
		if err := rows.Scan(&a.ReasonCode, &a.Qty); err != nil {
			rows.Close()
			return nil, err
		}
		a.Reason = flagNames[a.ReasonCode]
		if a.Reason == "" {
			a.Reason = a.ReasonCode
		}
		out.ExceptionFlags = append(out.ExceptionFlags, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status IN ('DRAFT','SUBMITTED'))::int, count(*) FILTER (WHERE "varianceFlagged" AND status IN ('SUBMITTED','APPROVED'))::int
		FROM "ProductionReport" r WHERE r."date" BETWEEN $1 AND $2 AND ($3 = '' OR r."plantId" = $3)`, from, to, plantID).Scan(&out.PendingEntries, &out.VarianceFlags); err != nil {
		return nil, err
	}
	return out, nil
}

func (h *handler) periodParams(c *fiber.Ctx) (time.Time, time.Time, string, string, error) {
	u := auth.MustUser(c)
	q := httpx.Query(c)
	from, to, plant := stock.QueryDate(q, "from"), stock.QueryDate(q, "to"), stock.QueryID(q, "plantId")
	g := strings.ToLower(strings.TrimSpace(c.Query("groupBy")))
	if g == "" {
		g = "day"
	} else {
		q.Str("groupBy").Trim().Lower().OneOf(groupBys, "production.v.group_by")
	}
	if err := q.Err(); err != nil {
		return time.Time{}, time.Time{}, "", "", err
	}
	rg := dates.ResolveRange(from, to)
	if rg.To.Sub(rg.From) > 731*24*time.Hour {
		return time.Time{}, time.Time{}, "", "", httpx.BadRequest("error.validation", map[string]string{"from": i18n.T(httpx.Lang(c), "production.v.range_2y")})
	}
	plantID := auth.PlantFilter(u, plant)
	return rg.From, rg.To, plantID, g, nil
}

func (h *handler) periods(c *fiber.Ctx) error {
	from, to, plantID, g, err := h.periodParams(c)
	if err != nil {
		return err
	}
	s, err := periodSummary(httpx.Ctx(c), h.d.Pool, from, to, plantID, g, httpx.Lang(c))
	if err != nil {
		return err
	}
	return httpx.JSONCached(c, s, cacheControl)
}

func pctOr(p *float64) string {
	if p == nil {
		return "—"
	}
	return format.Percent(p)
}

func (h *handler) pdfPeriods(c *fiber.Ctx) error {
	lang := httpx.Lang(c)
	T := func(k string, a ...any) string { return i18n.T(lang, k, a...) }
	from, to, plantID, g, err := h.periodParams(c)
	if err != nil {
		return err
	}
	s, err := periodSummary(httpx.Ctx(c), h.d.Pool, from, to, plantID, g, lang)
	if err != nil {
		return err
	}
	scope := T("production.pdf.all_plants")
	if plantID != "" && len(s.Plants) > 0 {
		scope = s.Plants[0].Plant.Code
	}
	period := dates.FormatISORange(s.From, s.To)
	doc, err := pdf.New(c, h.d, pdf.Options{Title: T("production.pdf.periods_title"), Subtitle: T("production.pdf.summary_subtitle", "plants", scope, "period", period),
		Landscape: true, Meta: []pdf.Meta{{Label: T("production.pdf.meta_plants"), Value: scope}, {Label: T("production.pdf.meta_period"), Value: period},
			{Label: T("production.pdf.meta_group"), Value: T("production.group." + g)}}})
	if err != nil {
		return err
	}
	tt := s.Totals
	doc.KpiRow([]pdf.Kpi{
		{Label: T("production.pdf.col_planned"), Value: fi(tt.Planned)},
		{Label: T("production.pdf.col_produced"), Value: fi(tt.Produced), Hint: T("production.pdf.hint_plan", "pct", pctOr(tt.PlanAchievement))},
		{Label: T("production.pdf.col_good"), Value: fi(tt.GoodOutput)},
		{Label: T("production.pdf.kpi_yield"), Value: pctOr(tt.Yield), Hint: T("production.pdf.hint_yield")},
		{Label: T("production.pdf.kpi_defect_rate"), Value: pctOr(tt.DefectRate), Hint: T("production.pdf.hint_rejection")},
		{Label: T("production.pdf.kpi_entries"), Value: fi(tt.Entries), Hint: T("production.pdf.hint_pending", "n", fi(s.PendingEntries))},
	})
	cols := []pdf.Column{
		{Header: T("production.pdf.col_period"), Width: 1.6}, {Header: T("production.pdf.meta_plant"), Width: 1},
		{Header: T("production.pdf.col_entries"), Width: 0.7, Align: pdf.Right}, {Header: T("production.pdf.col_planned"), Width: 1, Align: pdf.Right},
		{Header: T("production.pdf.col_produced"), Width: 1, Align: pdf.Right}, {Header: T("production.pdf.col_vs_plan"), Width: 0.8, Align: pdf.Right},
		{Header: T("production.pdf.col_defects"), Width: 0.9, Align: pdf.Right}, {Header: T("production.pdf.col_samples"), Width: 0.8, Align: pdf.Right},
		{Header: T("production.pdf.col_good"), Width: 1, Align: pdf.Right}, {Header: T("production.pdf.col_yield"), Width: 0.8, Align: pdf.Right},
		{Header: T("production.pdf.col_defect_rate"), Width: 0.8, Align: pdf.Right}, {Header: T("production.pdf.col_downtime"), Width: 0.9, Align: pdf.Right},
	}
	row := func(label, site string, f periodFig) []any {
		return []any{label, site, fi(f.Entries), fi(f.Planned), fi(f.Produced), pctOr(f.PlanAchievement), fi(f.DamageProd + f.DamageHand), fi(f.Samples),
			fi(f.GoodOutput), pctOr(f.Yield), pctOr(f.DefectRate), fi(f.DowntimeMins)}
	}
	var rows [][]any
	for _, b := range s.Buckets {
		label := dates.FormatISORange(b.From, b.To)
		if b.From == b.To {
			label = dates.FormatDate(dates.MustParseISO(b.From))
		}
		for _, p := range b.Plants {
			rows = append(rows, row(label, p.Plant.Code, p.periodFig))
		}
		if len(b.Plants) > 1 {
			r := row(label, T("production.pdf.consolidated"), b.Totals)
			for i := range r {
				r[i] = pdf.B(fmt.Sprint(r[i]), pdf.Navy)
			}
			rows = append(rows, r)
		}
	}
	doc.Section(T("production.pdf.section_periods")).Table(cols, rows, row(T("production.pdf.total"), "", tt), T("production.pdf.empty_summary"))
	var prow [][]any
	for _, p := range s.Plants {
		prow = append(prow, row(period, p.Plant.Code, p.periodFig))
	}
	doc.Section(T("production.pdf.section_by_plant")).Table(cols, prow, row(T("production.pdf.consolidated"), "", tt), T("production.pdf.empty_summary"))
	var drow [][]any
	for _, d := range s.Defects {
		stage := "—"
		if d.Stage != "" {
			stage = T("production.stage." + d.Stage)
		}
		drow = append(drow, []any{d.Reason, stage, fi(d.Qty), pctOr(d.Share), pctOr(d.Rate)})
	}
	doc.Section(T("production.pdf.section_defects")).Table([]pdf.Column{{Header: T("production.pdf.col_reason"), Width: 3}, {Header: T("production.pdf.col_stage"), Width: 1.5},
		{Header: T("production.pdf.col_qty"), Width: 1, Align: pdf.Right}, {Header: T("production.pdf.col_share"), Width: 1, Align: pdf.Right},
		{Header: T("production.pdf.col_defect_rate"), Width: 1, Align: pdf.Right}}, drow, nil, T("production.pdf.empty_defects"))
	if len(s.ExceptionFlags) > 0 {
		var frow [][]any
		for _, f := range s.ExceptionFlags {
			frow = append(frow, []any{f.Reason, fi(f.Qty)})
		}
		doc.Section(T("production.pdf.section_exceptions")).Table([]pdf.Column{{Header: T("production.pdf.col_flag"), Width: 3}, {Header: T("production.pdf.col_entries"), Width: 1, Align: pdf.Right}}, frow, nil)
	}
	doc.Note(T("production.pdf.periods_note", "n", fi(s.PendingEntries), "v", fi(s.VarianceFlags)))
	return pdf.Send(c, doc, fmt.Sprintf("Production-Summary_%s_%s_to_%s.pdf", strings.ToUpper(g[:1])+g[1:], s.From, s.To), c.Query("download") == "1")
}
