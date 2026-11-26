package income

import (
	"fmt"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/export"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/pdf"

	"github.com/gofiber/fiber/v2"
	"golang.org/x/sync/errgroup"
)

func statusTone(s string) pdf.Tone {
	switch s {
	case StatusVerified:
		return pdf.Green
	case StatusVoid:
		return pdf.Red
	}
	return pdf.Amber
}

func fi(v int64) string { return format.Int(float64(v)) }

func pct(a, b int64) string {
	if b == 0 {
		return "—"
	}
	return format.Percent(format.Ratio(float64(a), float64(b)), 1)
}

func dash(p *string) string {
	if p == nil || *p == "" {
		return "—"
	}
	return *p
}

// reportPDF is the income report for the list filters: KPI row, every record, totals by category, source, method,
// site, payer and month.
func (h *handler) reportPDF(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	f, err := h.parseFilter(c, u)
	if err != nil {
		return err
	}
	cond, args := f.where()
	var (
		rows []row
		lk   *lookups
		sum  *Summary
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		a := append(append([]any{}, args...), export.MaxRows)
		rs, err := h.d.Pool.Query(gctx, `SELECT `+rowCols+rowFrom+` WHERE `+cond+fmt.Sprintf(` ORDER BY i.date, i.created_at, i.id LIMIT $%d`, len(a)), a...)
		if err != nil {
			return err
		}
		rows, err = scanRows(rs)
		return err
	})
	g.Go(func() (err error) { lk, err = loadLookups(gctx, h.d.Pool, lang); return })
	g.Go(func() (err error) {
		uid := ""
		if f.mine == "1" {
			uid = u.ID
		}
		sum, err = ComputeSummary(gctx, h.d.Pool, lang, f.from, f.to, f.plantID, uid)
		return
	})
	if err := g.Wait(); err != nil {
		return err
	}
	t := func(k string, a ...any) string { return i18n.T(lang, k, a...) }
	rangeLabel := dates.FormatRange(f.from, f.to)
	meta := []pdf.Meta{{Label: t("income.pdf.period"), Value: rangeLabel}}
	if f.status != "" {
		meta = append(meta, pdf.Meta{Label: t("income.pdf.c_status"), Value: t("income.status." + f.status)})
	}
	if f.source != "" {
		meta = append(meta, pdf.Meta{Label: t("income.pdf.c_source"), Value: t("income.source." + f.source)})
	}
	rep, err := pdf.New(c, h.d, pdf.Options{Title: t("income.pdf.report_title"), Subtitle: rangeLabel, Landscape: true, Meta: meta})
	if err != nil {
		return err
	}
	rep.KpiRow([]pdf.Kpi{
		{Label: t("income.pdf.k_total"), Value: format.RWF(float64(sum.Total)), Hint: t("income.pdf.n_records", "n", sum.Count)},
		{Label: t("income.pdf.k_verified"), Value: format.RWF(float64(sum.Verified)), Hint: t("income.pdf.n_records", "n", sum.VerifiedCount)},
		{Label: t("income.pdf.k_unverified"), Value: format.RWF(float64(sum.Unverified)), Hint: t("income.pdf.n_records", "n", sum.UnverifiedCount)},
		{Label: t("income.pdf.k_order_payments"), Value: format.RWF(float64(sum.OrderPayments))},
		{Label: t("income.pdf.k_other"), Value: format.RWF(float64(sum.Other))},
	})
	var data [][]any
	var total int64
	for i := range rows {
		r := &rows[i]
		site := "—"
		if r.PCode != nil {
			site = *r.PCode
		}
		payer := deref(r.CustomerName)
		if payer == "" {
			payer = deref(r.PayerName)
		}
		src := t("income.source." + r.Source)
		if r.SaleNo != nil {
			src += " " + *r.SaleNo
		}
		data = append(data, []any{dates.FormatDate(r.Date), r.Number, nameOr(lk.cat, r.Category), site, src, payer, r.Description,
			t("income.method." + r.Method), deref(r.Reference), fi(r.Amount), pdf.B(t("income.status."+r.Status), statusTone(r.Status))})
		if r.Status != StatusVoid {
			total += r.Amount
		}
	}
	rep.Section(t("income.pdf.s_records")).Table([]pdf.Column{
		{Header: t("income.pdf.c_date"), Width: 1.1}, {Header: t("income.pdf.c_number"), Width: 1.5}, {Header: t("income.pdf.c_category"), Width: 1.5},
		{Header: t("income.pdf.c_site"), Width: 0.8}, {Header: t("income.pdf.c_source"), Width: 1.6}, {Header: t("income.pdf.c_payer"), Width: 1.8},
		{Header: t("income.pdf.c_description"), Width: 2.6}, {Header: t("income.pdf.c_method"), Width: 1.2}, {Header: t("income.pdf.c_reference"), Width: 1.2},
		{Header: t("income.pdf.c_amount"), Width: 1.2, Align: pdf.Right}, {Header: t("income.pdf.c_status"), Width: 1.1},
	}, data, []any{"", "", "", "", "", "", t("income.pdf.total_not_void"), "", "", fi(total), ""}, t("income.pdf.none"))
	group := func(title string, list []groupRow) {
		var gr [][]any
		var tot int64
		var n int
		for _, x := range list {
			gr = append(gr, []any{x.Name, format.Int(float64(x.Count)), fi(x.Amount), pct(x.Amount, sum.Total)})
			tot += x.Amount
			n += x.Count
		}
		rep.Section(title).Table([]pdf.Column{{Header: t("income.pdf.c_group"), Width: 2.5}, {Header: t("income.pdf.c_count"), Width: 0.8, Align: pdf.Right},
			{Header: t("income.pdf.c_amount"), Width: 1.3, Align: pdf.Right}, {Header: t("income.pdf.c_share"), Width: 0.8, Align: pdf.Right}},
			gr, []any{t("income.pdf.total"), format.Int(float64(n)), fi(tot), ""}, t("income.pdf.none"))
	}
	group(t("income.pdf.s_category"), sum.ByCategory)
	group(t("income.pdf.s_source"), sum.BySource)
	group(t("income.pdf.s_method"), sum.ByMethod)
	group(t("income.pdf.s_site"), sum.BySite)
	group(t("income.pdf.s_customer"), sum.ByCustomer)
	group(t("income.pdf.s_month"), sum.ByMonth)
	rep.Note(t("income.pdf.note_recorded"))
	rep.Signatures([]pdf.Signatory{{Role: t("income.pdf.sig_prepared"), Name: u.Name}, {Role: t("income.pdf.sig_reviewed")}})
	return pdf.Send(c, rep, "Income-Report_"+dates.ToISO(f.from)+"_"+dates.ToISO(f.to)+".pdf", c.Query("download") == "1")
}

// recordPDF is one income record ("Income record" — a record of money received, not a receipt issued by the system):
// details, history, attachments and signatures.
func (h *handler) recordPDF(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	r, err := h.load(c, c.Params("id"))
	if err != nil {
		return err
	}
	e, err := h.detail(ctx, lang, u, r)
	if err != nil {
		return err
	}
	t := func(k string, a ...any) string { return i18n.T(lang, k, a...) }
	rep, err := pdf.New(c, h.d, pdf.Options{Title: t("income.pdf.record_title"), Subtitle: e.CategoryName + " · " + t("income.source."+e.Source),
		DocumentNo: e.Number, Meta: []pdf.Meta{{Label: t("income.pdf.c_date"), Value: dates.FormatDate(r.Date)}}})
	if err != nil {
		return err
	}
	rep.Badge(t("income.status."+e.Status), statusTone(e.Status))
	rep.KpiRow([]pdf.Kpi{{Label: t("income.pdf.c_amount"), Value: format.RWF(float64(e.Amount))},
		{Label: t("income.pdf.c_method"), Value: t("income.method." + e.Method), Hint: dash(e.Reference)}})
	site := "—"
	if e.Plant != nil {
		site = e.Plant.Code + " · " + e.Plant.Name
	}
	det := [][]any{
		{t("income.pdf.c_number"), pdf.Cell{Text: e.Number, Bold: true}}, {t("income.pdf.c_date"), dates.FormatDate(r.Date)},
		{t("income.pdf.c_category"), e.CategoryName}, {t("income.pdf.c_department"), e.DepartmentName}, {t("income.pdf.c_site"), site},
		{t("income.pdf.c_source"), t("income.source." + e.Source)},
	}
	if e.Sale != nil {
		det = append(det, []any{t("income.pdf.c_order"), e.Sale.Number})
	}
	det = append(det, []any{t("income.pdf.c_payer"), dash(e.PayerName)}, []any{t("income.pdf.c_description"), e.Description},
		[]any{t("income.pdf.c_method"), t("income.method." + e.Method)}, []any{t("income.pdf.c_reference"), dash(e.Reference)})
	if e.PaymentKind != nil {
		det = append(det, []any{t("income.pdf.c_kind"), t("income.kind." + *e.PaymentKind)})
	}
	if e.Budget.LineID != nil {
		v := dash(e.Budget.LineBudgetNumber) + " · " + dash(e.Budget.LineCategoryName)
		if e.Budget.Suggested {
			v += " (" + t("income.budget.suggested") + ")"
		}
		det = append(det, []any{t("income.pdf.c_budget_line"), v})
	} else {
		det = append(det, []any{t("income.pdf.c_budget_line"), t("income.budget.none")})
	}
	if e.VoidReason != nil {
		det = append(det, []any{t("income.pdf.c_void_reason"), *e.VoidReason})
	}
	for _, l := range e.Links {
		if l.RemovedAt == nil {
			det = append(det, []any{t("income.pdf.c_linked"), l.TypeLabel + ": " + l.Label})
		}
	}
	rep.Section(t("income.pdf.s_details")).Table([]pdf.Column{{Header: t("income.pdf.c_field"), Width: 1.4}, {Header: t("income.pdf.c_details"), Width: 4}}, det, nil)
	var trail [][]any
	for _, x := range e.History {
		trail = append(trail, []any{x.Summary, x.UserName, dates.FormatDateTime(x.At)})
	}
	rep.Section(t("income.pdf.s_history")).Table([]pdf.Column{{Header: t("income.pdf.c_step"), Width: 4}, {Header: t("income.pdf.c_by"), Width: 1.6},
		{Header: t("income.pdf.c_when"), Width: 1.3}}, trail, nil, t("income.pdf.none"))
	var att [][]any
	for _, a := range e.Attachments {
		att = append(att, []any{a.FileName, a.ContentType, format.Int(float64(a.Size/1024)) + " KB", a.UploadedBy, dates.FormatDateTime(a.UploadedAt)})
	}
	rep.Section(t("income.pdf.s_attachments")).Table([]pdf.Column{{Header: t("income.pdf.c_file"), Width: 2.5}, {Header: t("income.pdf.c_type"), Width: 1.2},
		{Header: t("income.pdf.c_size"), Width: 0.8, Align: pdf.Right}, {Header: t("income.pdf.c_by"), Width: 1.5}, {Header: t("income.pdf.c_when"), Width: 1.3}},
		att, nil, t("income.pdf.no_attachments"))
	rep.Note(t("income.pdf.note_recorded"))
	verified := pdf.Signatory{Role: t("income.pdf.sig_verified")}
	if e.VerifiedBy != nil {
		verified.Name, verified.At = e.VerifiedBy.Name, e.VerifiedAt
	}
	rep.Signatures([]pdf.Signatory{{Role: t("income.pdf.sig_recorded"), Name: e.CreatedBy.Name, At: &e.CreatedAt}, verified})
	return pdf.Send(c, rep, e.Number+"_Income-record_"+e.Date+".pdf", c.Query("download") == "1")
}
