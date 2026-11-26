package expenses

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
	case StatusApproved, StatusPaid:
		return pdf.Green
	case StatusRejected, StatusCancelled:
		return pdf.Red
	}
	return pdf.Amber
}

func fi(v int64) string { return format.Int(float64(v)) }

// reportPDF is the expense report for the list filters (SRS FIN-01/FIN-07): KPI row, every expense, totals by category,
// department and payment method.
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
		rs, err := h.d.Pool.Query(gctx, `SELECT `+rowCols+rowFrom+` WHERE `+cond+fmt.Sprintf(` ORDER BY e.date, e.created_at LIMIT $%d`, len(a)), a...)
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
	meta := []pdf.Meta{{Label: t("expenses.pdf.period"), Value: rangeLabel}}
	if f.status != "" {
		meta = append(meta, pdf.Meta{Label: t("expenses.pdf.status"), Value: t("expenses.status." + f.status)})
	}
	rep, err := pdf.New(c, h.d, pdf.Options{Title: t("expenses.pdf.report_title"), Subtitle: rangeLabel, Landscape: true, Meta: meta})
	if err != nil {
		return err
	}
	rep.KpiRow([]pdf.Kpi{
		{Label: t("expenses.pdf.k_approved"), Value: format.RWF(float64(sum.Approved)), Hint: t("expenses.pdf.n_expenses", "n", sum.ApprovedCount)},
		{Label: t("expenses.pdf.k_paid"), Value: format.RWF(float64(sum.Paid))},
		{Label: t("expenses.pdf.k_unpaid"), Value: format.RWF(float64(sum.Unpaid))},
		{Label: t("expenses.pdf.k_pending"), Value: format.RWF(float64(sum.Pending)), Hint: t("expenses.pdf.n_expenses", "n", sum.PendingCount)},
		{Label: t("expenses.pdf.k_procurement"), Value: format.RWF(float64(sum.Procurement))},
	})
	var data [][]any
	var total int64
	for i := range rows {
		r := &rows[i]
		site := "—"
		if r.PCode != nil {
			site = *r.PCode
		}
		data = append(data, []any{dates.FormatDate(r.Date), r.Number, nameOr(lk.cat, r.Category), nameOr(lk.dept, r.Department), site,
			deref(r.SupplierName), r.Description, t("expenses.method." + r.PaymentMethod), fi(r.Amount), pdf.B(t("expenses.status."+r.Status), statusTone(r.Status))})
		if r.Status == StatusApproved || r.Status == StatusPaid {
			total += r.Amount
		}
	}
	rep.Section(t("expenses.pdf.s_expenses")).Table([]pdf.Column{
		{Header: t("expenses.pdf.c_date"), Width: 1.1}, {Header: t("expenses.pdf.c_number"), Width: 1.5}, {Header: t("expenses.pdf.c_category"), Width: 1.5},
		{Header: t("expenses.pdf.c_department"), Width: 1.4}, {Header: t("expenses.pdf.c_site"), Width: 0.8}, {Header: t("expenses.pdf.c_supplier"), Width: 1.8},
		{Header: t("expenses.pdf.c_description"), Width: 2.8}, {Header: t("expenses.pdf.c_method"), Width: 1.2}, {Header: t("expenses.pdf.c_amount"), Width: 1.2, Align: pdf.Right},
		{Header: t("expenses.pdf.c_status"), Width: 1.3},
	}, data, []any{"", "", "", "", "", "", t("expenses.pdf.total_approved"), "", fi(total), ""}, t("expenses.pdf.none"))
	group := func(title string, list []groupRow) {
		var gr [][]any
		var tot int64
		var n int
		for _, x := range list {
			gr = append(gr, []any{x.Name, format.Int(float64(x.Count)), fi(x.Amount), pct(x.Amount, sum.Approved)})
			tot += x.Amount
			n += x.Count
		}
		rep.Section(title).Table([]pdf.Column{{Header: t("expenses.pdf.c_group"), Width: 2.5}, {Header: t("expenses.pdf.c_count"), Width: 0.8, Align: pdf.Right},
			{Header: t("expenses.pdf.c_amount"), Width: 1.3, Align: pdf.Right}, {Header: t("expenses.pdf.c_share"), Width: 0.8, Align: pdf.Right}},
			gr, []any{t("expenses.pdf.total"), format.Int(float64(n)), fi(tot), ""}, t("expenses.pdf.none"))
	}
	group(t("expenses.pdf.s_category"), sum.ByCategory)
	group(t("expenses.pdf.s_department"), sum.ByDepartment)
	group(t("expenses.pdf.s_method"), sum.ByMethod)
	if sum.MissingReceipts > 0 {
		rep.Note(t("expenses.pdf.missing_receipts", "n", sum.MissingReceipts))
	}
	rep.Signatures([]pdf.Signatory{{Role: t("expenses.pdf.sig_prepared"), Name: u.Name}, {Role: t("expenses.pdf.sig_reviewed")}})
	return pdf.Send(c, rep, "Expense-Report_"+dates.ToISO(f.from)+"_"+dates.ToISO(f.to)+".pdf", c.Query("download") == "1")
}

func pct(a, b int64) string {
	if b == 0 {
		return "—"
	}
	return format.Percent(format.Ratio(float64(a), float64(b)), 1)
}

// voucherPDF is one expense: details, budget position, approval trail, receipts and signatures.
func (h *handler) voucherPDF(c *fiber.Ctx) error {
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
	rep, err := pdf.New(c, h.d, pdf.Options{Title: t("expenses.pdf.voucher_title"), Subtitle: e.CategoryName + " · " + e.DepartmentName, DocumentNo: e.Number,
		Meta: []pdf.Meta{{Label: t("expenses.pdf.c_date"), Value: dates.FormatDate(r.Date)}}})
	if err != nil {
		return err
	}
	rep.Badge(t("expenses.status."+e.Status), statusTone(e.Status))
	kpis := []pdf.Kpi{{Label: t("expenses.pdf.c_amount"), Value: format.RWF(float64(e.Amount))}}
	if e.Budget.Remaining != nil {
		kpis = append(kpis, pdf.Kpi{Label: t("expenses.pdf.k_remaining"), Value: format.RWF(float64(*e.Budget.Remaining)), Hint: deref(e.Budget.BudgetNumber)})
	}
	if e.Budget.OverrunAmount > 0 {
		kpis = append(kpis, pdf.Kpi{Label: t("expenses.pdf.k_overrun"), Value: format.RWF(float64(e.Budget.OverrunAmount)), Hint: t("expenses.overrun." + e.Budget.OverrunStatus)})
	}
	rep.KpiRow(kpis)
	dash := func(p *string) string {
		if p == nil || *p == "" {
			return "—"
		}
		return *p
	}
	site := "—"
	if e.Plant != nil {
		site = e.Plant.Code + " · " + e.Plant.Name
	}
	det := [][]any{
		{t("expenses.pdf.c_number"), pdf.Cell{Text: e.Number, Bold: true}}, {t("expenses.pdf.c_date"), dates.FormatDate(r.Date)},
		{t("expenses.pdf.c_category"), e.CategoryName}, {t("expenses.pdf.c_department"), e.DepartmentName}, {t("expenses.pdf.c_site"), site},
		{t("expenses.pdf.c_supplier"), dash(e.SupplierName)}, {t("expenses.pdf.c_description"), e.Description},
		{t("expenses.pdf.c_method"), t("expenses.method." + e.PaymentMethod)}, {t("expenses.pdf.c_reference"), dash(e.Reference)},
	}
	if e.Source != nil {
		det = append(det, []any{t("expenses.pdf.c_source"), t("expenses.source."+e.Source.Type) + " " + dash(e.Source.Number)})
	}
	if e.PaidAt != nil {
		det = append(det, []any{t("expenses.pdf.c_paid"), dates.FormatDateTime(*e.PaidAt) + " · " + dash(e.PaymentReference)})
	}
	rep.Section(t("expenses.pdf.s_details")).Table([]pdf.Column{{Header: t("expenses.pdf.c_field"), Width: 1.4}, {Header: t("expenses.pdf.c_details"), Width: 4}}, det, nil)
	var trail [][]any
	trail = append(trail, []any{t("expenses.pdf.step_recorded"), e.CreatedBy.Name, dates.FormatDateTime(e.CreatedAt), ""})
	for _, a := range e.Approvals {
		step := a.Label
		if a.Kind == "overrun" {
			step = t("expenses.pdf.step_overrun") + " · " + a.Label
		}
		trail = append(trail, []any{step + " — " + t("expenses.decision."+a.Decision), a.UserName + " (" + a.RoleLabel + ")", dates.FormatDateTime(a.At), deref(a.Comment)})
	}
	for _, l := range e.Levels {
		if !l.Done && e.Status == StatusSubmitted {
			trail = append(trail, []any{l.Label, t("expenses.pdf.awaiting"), "", ""})
		}
	}
	rep.Section(t("expenses.pdf.s_trail")).Table([]pdf.Column{{Header: t("expenses.pdf.c_step"), Width: 2.4}, {Header: t("expenses.pdf.c_by"), Width: 2.2},
		{Header: t("expenses.pdf.c_when"), Width: 1.4}, {Header: t("expenses.pdf.c_comment"), Width: 2.2}}, trail, nil)
	var att [][]any
	for _, a := range e.Attachments {
		att = append(att, []any{a.FileName, a.ContentType, format.Int(float64(a.Size/1024)) + " KB", a.UploadedBy, dates.FormatDateTime(a.UploadedAt)})
	}
	rep.Section(t("expenses.pdf.s_receipts")).Table([]pdf.Column{{Header: t("expenses.pdf.c_file"), Width: 2.5}, {Header: t("expenses.pdf.c_type"), Width: 1.2},
		{Header: t("expenses.pdf.c_size"), Width: 0.8, Align: pdf.Right}, {Header: t("expenses.pdf.c_by"), Width: 1.5}, {Header: t("expenses.pdf.c_when"), Width: 1.3}},
		att, nil, t("expenses.pdf.no_receipts"))
	approved := pdf.Signatory{Role: t("expenses.pdf.sig_approved")}
	if e.DecidedBy != nil && (e.Status == StatusApproved || e.Status == StatusPaid) {
		approved.Name, approved.At = e.DecidedBy.Name, e.DecidedAt
	}
	paid := pdf.Signatory{Role: t("expenses.pdf.sig_paid")}
	if e.PaidBy != nil {
		paid.Name, paid.At = e.PaidBy.Name, e.PaidAt
	}
	rep.Signatures([]pdf.Signatory{{Role: t("expenses.pdf.sig_requested"), Name: e.CreatedBy.Name, At: &e.CreatedAt}, approved, paid})
	return pdf.Send(c, rep, e.Number+"_Expense_"+e.Date+".pdf", c.Query("download") == "1")
}
