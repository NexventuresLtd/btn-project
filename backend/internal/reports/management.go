package reports

import (
	"fmt"
	"strings"

	"btn-ops/internal/dashboard"
	"btn-ops/internal/dates"
	"btn-ops/internal/deps"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/pdf"

	"github.com/gofiber/fiber/v2"
)

func pdfSend(c *fiber.Ctx, rep *pdf.Report, filename string) error {
	return pdf.Send(c, rep, filename, c.Query("download") == "1")
}

func pct(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", *v)
}

func kpiValue(unit string, v *float64) string {
	if v == nil {
		return "—"
	}
	switch unit {
	case "percent":
		return pct(v)
	case "rwf":
		return format.RWF(*v)
	}
	if *v == float64(int64(*v)) {
		return format.Int(*v)
	}
	return format.Qty(*v)
}

func kpiChange(k dashboard.Kpi) string {
	if k.Change == nil {
		return "—"
	}
	sign := ""
	if *k.Change > 0 {
		sign = "+"
	} else if *k.Change < 0 {
		sign = "-"
	}
	a := *k.Change
	if a < 0 {
		a = -a
	}
	if k.ChangeKind == dashboard.PP {
		return fmt.Sprintf("%s%.1f pp", sign, a)
	}
	return fmt.Sprintf("%s%.1f%%", sign, a*100)
}

// ascii replaces the U+2212 minus so signs never disappear in any font.
func ascii(s string) string { return strings.ReplaceAll(s, "−", "-") }

func qtyOrDash(v *float64) string {
	if v == nil {
		return "—"
	}
	return format.Qty(*v)
}

func buildManagementPDF(c *fiber.Ctx, dp *deps.Deps, d *dashboard.Dashboard, pending []dashboard.PendingRow) (*pdf.Report, error) {
	lang := httpx.Lang(c)
	t := func(k string, args ...any) string { return i18n.T(lang, k, args...) }
	label := dashboard.RangeLabel(lang, dates.MustParseISO(d.Range.From), dates.MustParseISO(d.Range.To))
	prevLabel := dashboard.RangeLabel(lang, dates.MustParseISO(d.PreviousRange.From), dates.MustParseISO(d.PreviousRange.To))
	plantLabel, plantMeta := t("reports.mgmt.all_plants"), t("reports.mgmt.all_plants")
	if d.Plant != nil {
		plantLabel, plantMeta = d.Plant.Code+" — "+d.Plant.Name, d.Plant.Code
	}
	kpi := func(key string) *dashboard.Kpi {
		for i := range d.Kpis {
			if d.Kpis[i].Key == key {
				return &d.Kpis[i]
			}
		}
		return nil
	}
	rep, err := pdf.New(c, dp, pdf.Options{
		Title:    t("reports.mgmt.title", "range", label),
		Subtitle: t("reports.mgmt.subtitle", "plant", plantLabel, "previous", prevLabel),
		Meta: []pdf.Meta{{Label: t("reports.mgmt.meta.period"), Value: label}, {Label: t("reports.mgmt.meta.plant"), Value: plantMeta},
			{Label: t("reports.mgmt.meta.previous"), Value: prevLabel}},
		Lang: lang,
	})
	if err != nil {
		return nil, err
	}

	var tiles []pdf.Kpi
	add := func(key string) {
		if k := kpi(key); k != nil {
			tiles = append(tiles, pdf.Kpi{Label: k.Label, Value: kpiValue(k.Unit, k.Value),
				Hint: t("reports.mgmt.previous_hint", "value", kpiValue(k.Unit, k.Previous), "change", kpiChange(*k))})
		}
	}
	add("produced")
	add("quality")
	add("revenue")
	switch {
	case kpi("receivables") != nil:
		add("receivables")
	case kpi("pettyCashBalance") != nil:
		add("pettyCashBalance")
	default:
		add("availability")
	}
	if len(tiles) > 4 {
		tiles = tiles[:4]
	}
	rep.KpiRow(tiles)

	// KPI table
	rows := [][]any{}
	for _, k := range d.Kpis {
		if k.Key == "good" {
			continue
		}
		rows = append(rows, []any{k.Label, ascii(k.Definition), kpiValue(k.Unit, k.Value), kpiValue(k.Unit, k.Previous), kpiChange(k)})
	}
	rep.Section(t("reports.mgmt.sec.kpi")).Table([]pdf.Column{
		{Header: t("reports.mgmt.col.kpi"), Width: 2.1}, {Header: t("reports.mgmt.col.definition"), Width: 4.4},
		{Header: t("reports.mgmt.col.value"), Width: 1.6, Align: pdf.Right}, {Header: t("reports.mgmt.col.previous"), Width: 1.6, Align: pdf.Right},
		{Header: t("reports.mgmt.col.change"), Width: 1.1, Align: pdf.Right},
	}, rows, nil, t("reports.mgmt.empty.kpi"))
	rep.Note(t("reports.mgmt.note.change"))

	metricCols := []pdf.Column{{Header: t("reports.mgmt.col.measure"), Width: 3}, {Header: t("reports.mgmt.col.value"), Width: 2, Align: pdf.Right}}

	if p := d.Production; p != nil {
		rows := [][]any{}
		for _, r := range p.ByPlant {
			rows = append(rows, []any{r.Code, format.Int(float64(r.Reports)), format.Int(float64(r.Target)), format.Int(float64(r.Produced)),
				format.Int(float64(r.Good)), format.Int(float64(r.Damage)), pct(r.Achievement), pct(r.Quality), pct(r.Availability), qtyOrDash(r.CementPer1000)})
		}
		val := func(k string) *float64 {
			if x := kpi(k); x != nil {
				return x.Value
			}
			return nil
		}
		tot := p.Totals
		totals := []any{t("reports.mgmt.total"), format.Int(float64(tot.Reports)), format.Int(float64(tot.Target)), format.Int(float64(tot.Produced)),
			format.Int(float64(tot.Good)), format.Int(float64(tot.DamageProd + tot.DamageHand)), pct(val("achievement")), pct(val("quality")),
			pct(val("availability")), qtyOrDash(val("cementPer1000"))}
		rep.Section(t("reports.mgmt.sec.production")).Table([]pdf.Column{
			{Header: t("reports.mgmt.col.plant"), Width: 1}, {Header: t("reports.mgmt.col.shifts"), Width: 0.7, Align: pdf.Right},
			{Header: t("reports.mgmt.col.target"), Width: 1.1, Align: pdf.Right}, {Header: t("reports.mgmt.col.produced"), Width: 1.1, Align: pdf.Right},
			{Header: t("reports.mgmt.col.good"), Width: 1.1, Align: pdf.Right}, {Header: t("reports.mgmt.col.damage"), Width: 0.9, Align: pdf.Right},
			{Header: t("reports.mgmt.col.achievement"), Width: 1, Align: pdf.Right}, {Header: t("reports.mgmt.col.quality"), Width: 0.9, Align: pdf.Right},
			{Header: t("reports.mgmt.col.availability"), Width: 1, Align: pdf.Right}, {Header: t("reports.mgmt.col.cement"), Width: 1, Align: pdf.Right},
		}, rows, totals)
		rep.Note(t("reports.mgmt.note.production", "downtime", format.Int(float64(tot.DowntimeMins)), "planned", format.Int(float64(tot.PlannedMins)),
			"basis", p.AvailabilityBasis, "cement", format.Qty(tot.CementBags)))
	}

	if s := d.Sales; s != nil {
		rep.Section(t("reports.mgmt.sec.sales")).Table(metricCols, [][]any{
			{t("reports.mgmt.m.sales_count"), format.Int(float64(s.SalesCount))},
			{t("reports.mgmt.m.revenue"), format.RWF(float64(s.Revenue))},
			{t("reports.mgmt.m.blocks_sold"), format.Int(float64(s.BlocksSold))},
			{t("reports.mgmt.m.collected"), format.RWF(float64(s.Collected))},
			{t("reports.mgmt.m.receivables"), format.RWF(float64(s.Receivables))},
		}, nil)
		cust := [][]any{}
		for _, r := range s.TopCustomers {
			cust = append(cust, []any{r.Name, format.Int(float64(r.Blocks)), format.RWF(float64(r.Revenue))})
		}
		rep.Spacer(6).Table([]pdf.Column{{Header: t("reports.mgmt.col.top_customers"), Width: 2.4}, {Header: t("reports.mgmt.col.blocks"), Width: 1, Align: pdf.Right},
			{Header: t("reports.mgmt.col.revenue"), Width: 1.5, Align: pdf.Right}}, cust, nil, t("reports.mgmt.empty.sales"))
	}

	if p := d.PettyCash; p != nil {
		rep.Section(t("reports.mgmt.sec.cash")).Table(metricCols, [][]any{
			{t("reports.mgmt.m.opening"), format.RWF(float64(p.OpeningBalance))},
			{t("reports.mgmt.m.deposits"), format.RWF(float64(p.Deposits))},
			{t("reports.mgmt.m.expenses"), format.RWF(float64(p.Expenses))},
			{t("reports.mgmt.m.closing"), format.RWF(float64(p.Balance))},
		}, nil)
		cats := [][]any{}
		for _, r := range p.ByCategory {
			name := r.Category
			if k := "dashboard.category." + r.Category; i18n.Has(k) {
				name = t(k)
			}
			cats = append(cats, []any{name, format.Int(float64(r.Count)), format.RWF(float64(r.Amount + r.Charges))})
		}
		rep.Spacer(6).Table([]pdf.Column{{Header: t("reports.mgmt.col.by_category"), Width: 2.4}, {Header: t("reports.mgmt.col.entries"), Width: 0.8, Align: pdf.Right},
			{Header: t("reports.mgmt.col.amount"), Width: 1.5, Align: pdf.Right}}, cats, nil, t("reports.mgmt.empty.expenses"))
		rep.Note(ascii(p.BalanceBasis))
	}

	if p := d.Payroll; p != nil {
		rows := [][]any{
			{t("reports.mgmt.m.sheets"), format.Int(float64(p.Periods))},
			{t("reports.mgmt.m.workers"), format.Int(float64(p.Workers))},
			{t("reports.mgmt.m.gross"), format.RWF(float64(p.Gross))},
			{t("reports.mgmt.m.net"), format.RWF(float64(p.LabourCost))},
		}
		if d.Cost != nil {
			v := "—"
			if d.Cost.Value != nil {
				v = format.RWF(*d.Cost.Value)
			}
			rows = append(rows, []any{t("reports.mgmt.m.cost_per_block"), v})
		}
		rep.Section(t("reports.mgmt.sec.labour")).Table(metricCols, rows, nil)
		if cs := d.Cost; cs != nil {
			rep.Note(t("reports.mgmt.note.cost", "labour", format.RWF(float64(cs.Labour)), "petty", format.RWF(float64(cs.PettyCash)),
				"materials", format.RWF(float64(cs.Materials)), "total", format.RWF(float64(cs.Total)), "good", format.Int(float64(cs.GoodBlocks)), "basis", ascii(cs.Basis)))
		}
	}

	if len(pending) > 0 {
		rows := [][]any{}
		for _, r := range pending {
			amt := ""
			if r.Amount != nil {
				amt = format.RWF(float64(*r.Amount))
			}
			rows = append(rows, []any{r.Area, r.Stage, format.Int(float64(r.Count)), amt})
		}
		rep.Section(t("reports.mgmt.sec.pending")).Table([]pdf.Column{{Header: t("reports.mgmt.col.area"), Width: 1.2}, {Header: t("reports.mgmt.col.waiting"), Width: 3},
			{Header: t("reports.mgmt.col.records"), Width: 0.9, Align: pdf.Right}, {Header: t("reports.mgmt.col.amount"), Width: 1.4, Align: pdf.Right}}, rows, nil)
	}

	if m := d.Materials; m != nil {
		rows := [][]any{}
		for _, a := range m.Alerts {
			status := pdf.B(t("reports.mgmt.status.low"), pdf.Amber)
			if a.Severity == "out" {
				status = pdf.B(t("reports.mgmt.status.out"), pdf.Red)
			}
			rows = append(rows, []any{a.PlantCode, a.Name, format.Qty(a.Balance) + " " + a.Unit, format.Qty(a.MinLevel) + " " + a.Unit, status})
		}
		rep.Section(t("reports.mgmt.sec.lowstock")).Table([]pdf.Column{{Header: t("reports.mgmt.col.plant"), Width: 1}, {Header: t("reports.mgmt.col.material"), Width: 2.2},
			{Header: t("reports.mgmt.col.balance"), Width: 1.3, Align: pdf.Right}, {Header: t("reports.mgmt.col.reorder"), Width: 1.3, Align: pdf.Right},
			{Header: t("reports.mgmt.col.status"), Width: 1, Align: pdf.Right}}, rows, nil, t("reports.mgmt.empty.lowstock"))
	}

	rep.Spacer(6).Badge(t("reports.mgmt.badge"), pdf.Navy)
	rep.Note(t("reports.mgmt.note.approved"))
	rep.Signatures([]pdf.Signatory{{Role: t("reports.mgmt.sig.prepared")}, {Role: t("reports.mgmt.sig.reviewed")}})
	return rep, nil
}
