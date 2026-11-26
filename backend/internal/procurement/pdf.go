package procurement

// Branded, localized PDFs (portrait/landscape via ?orientation=): purchase order (PRC-04), purchase request with
// its approval trail (PRC-02/05) and the procurement & expenditure report (PRC-08).

import (
	"strings"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/pdf"

	"github.com/gofiber/fiber/v2"
)

func rwf(n int64) string { return format.RWF(float64(n)) }

func isoDate(s *string) string {
	if s == nil || *s == "" {
		return "—"
	}
	return dates.FormatISO(*s)
}

func orderTone(s string) pdf.Tone {
	switch s {
	case POReceived, POClosed:
		return pdf.Green
	case POCancelled:
		return pdf.Red
	case POOrdered, POPartiallyReceived:
		return pdf.Amber
	}
	return pdf.Navy
}

func requestTone(s string) pdf.Tone {
	switch s {
	case ReqApproved, ReqReceived, ReqClosed, ReqOrdered:
		return pdf.Green
	case ReqRejected:
		return pdf.Red
	case ReqSubmitted, ReqPartiallyReceived:
		return pdf.Amber
	}
	return pdf.Navy
}

func (h *handler) pdfOrder(c *fiber.Ctx) error {
	ctx, u, lang := httpx.Ctx(c), auth.MustUser(c), httpx.Lang(c)
	o, err := h.loadOrder(ctx, h.d.Pool, lang, c.Params("id"), u)
	if err != nil {
		return err
	}
	T := func(k string, a ...any) string { return i18n.T(lang, k, a...) }
	sup := o.Supplier
	meta := []pdf.Meta{
		{Label: T("procurement.pdf.supplier"), Value: sup.Code + " · " + sup.Name},
		{Label: T("procurement.pdf.tin"), Value: orDash(sup.TIN)},
		{Label: T("procurement.pdf.contact"), Value: strings.Trim(strings.Join([]string{deref(sup.ContactName), deref(sup.Phone), deref(sup.Email)}, " · "), " ·")},
		{Label: T("procurement.pdf.deliver_to"), Value: o.DeliveryPlant.Code + " · " + o.DeliveryPlant.Name},
		{Label: T("procurement.pdf.order_date"), Value: dates.FormatISO(o.OrderDate)},
		{Label: T("procurement.pdf.expected_date"), Value: isoDate(o.ExpectedDate)},
		{Label: T("procurement.pdf.payment_term"), Value: orDash(strp(o.PaymentTermName))},
		{Label: T("procurement.pdf.quotation"), Value: orDash(o.QuotationRef)},
	}
	if len(o.Requests) > 0 {
		nums := []string{}
		for _, r := range o.Requests {
			nums = append(nums, r.Code)
		}
		meta = append(meta, pdf.Meta{Label: T("procurement.pdf.requests"), Value: strings.Join(nums, ", ")})
	}
	if len(o.Invoices) > 0 {
		nums := []string{}
		for _, inv := range o.Invoices {
			nums = append(nums, inv.InvoiceNumber)
		}
		meta = append(meta, pdf.Meta{Label: T("procurement.pdf.invoices"), Value: strings.Join(nums, ", ")})
	}
	rep, err := pdf.New(c, h.d, pdf.Options{Title: T("procurement.pdf.order_title"), Subtitle: sup.Name, DocumentNo: o.Number, Meta: meta})
	if err != nil {
		return err
	}
	rep.Badge(T("procurement.orderStatus."+o.Status), orderTone(o.Status))
	rep.KpiRow([]pdf.Kpi{{Label: T("procurement.pdf.total"), Value: rwf(o.Total)}, {Label: T("procurement.pdf.received_value"), Value: rwf(o.ReceivedValue)},
		{Label: T("procurement.pdf.invoiced"), Value: rwf(o.Invoiced)}, {Label: T("procurement.pdf.lines"), Value: format.Int(float64(len(o.Lines)))}})
	rows := make([][]any, 0, len(o.Lines))
	for _, l := range o.Lines {
		item := l.Description
		if l.ItemCode != nil {
			item = *l.ItemCode + " · " + item
		}
		req := ""
		if l.Request != nil {
			req = l.Request.Code
		}
		rows = append(rows, []any{l.LineNo, item, req, format.Qty(l.Quantity) + " " + l.Unit, format.Int(float64(l.UnitPrice)), format.Int(float64(l.Total)),
			format.Qty(l.ReceivedQty)})
	}
	rep.Section(T("procurement.pdf.lines_title")).Table([]pdf.Column{{Header: "#", Width: 0.4}, {Header: T("procurement.col.item"), Width: 4}, {Header: T("procurement.col.request"), Width: 1.6},
		{Header: T("procurement.col.quantity"), Width: 1.5, Align: pdf.Right}, {Header: T("procurement.col.unit_price"), Width: 1.4, Align: pdf.Right},
		{Header: T("procurement.col.total"), Width: 1.6, Align: pdf.Right}, {Header: T("procurement.col.received"), Width: 1.2, Align: pdf.Right}},
		rows, []any{"", pdf.Cell{Text: T("procurement.col.total"), Bold: true}, "", "", "", pdf.Cell{Text: format.Int(float64(o.Total)), Bold: true}, ""})
	if len(o.Receipts) > 0 {
		rr := [][]any{}
		for _, g := range o.Receipts {
			for _, l := range g.Lines {
				v := T("procurement.variance." + l.Variance)
				if l.Variance != VarNone {
					v += " " + format.Qty(l.VarianceQty) + " — " + deref(l.Reason)
				}
				rr = append(rr, []any{g.Number, dates.FormatISO(g.Date), g.Plant.Code, l.Description, format.Qty(l.Quantity) + " " + l.Unit, orDash(l.LotRef), v})
			}
		}
		rep.Section(T("procurement.pdf.receipts_title")).Table([]pdf.Column{{Header: T("procurement.col.receipt"), Width: 1.6}, {Header: T("procurement.col.date"), Width: 1.2},
			{Header: T("procurement.col.location"), Width: 1}, {Header: T("procurement.col.item"), Width: 3}, {Header: T("procurement.col.quantity"), Width: 1.4, Align: pdf.Right},
			{Header: T("procurement.col.lot"), Width: 1.4}, {Header: T("procurement.col.variance"), Width: 2.4}}, rr, nil)
	}
	if o.Notes != nil {
		rep.Note(T("procurement.pdf.notes") + ": " + *o.Notes)
	}
	rep.Note(T("procurement.pdf.order_terms", "number", o.Number))
	sigs := []pdf.Signatory{{Role: T("procurement.pdf.prepared_by"), Name: o.CreatedBy.Name, At: &o.CreatedAt, Verb: T("procurement.pdf.verb_prepared")}}
	if o.OrderedBy != nil {
		sigs = append(sigs, pdf.Signatory{Role: T("procurement.pdf.issued_by"), Name: o.OrderedBy.Name, At: o.OrderedAt, Verb: T("procurement.pdf.verb_issued")})
	} else {
		sigs = append(sigs, pdf.Signatory{Role: T("procurement.pdf.issued_by")})
	}
	sigs = append(sigs, pdf.Signatory{Role: T("procurement.pdf.supplier_ack")})
	rep.Signatures(sigs)
	return pdf.Send(c, rep, o.Number+"_"+safeFile(sup.Name)+".pdf", c.Query("download") == "1")
}

func (h *handler) pdfRequest(c *fiber.Ctx) error {
	ctx, u, lang := httpx.Ctx(c), auth.MustUser(c), httpx.Lang(c)
	d, err := h.loadRequest(ctx, h.d.Pool, lang, c.Params("id"), u)
	if err != nil {
		return err
	}
	T := func(k string, a ...any) string { return i18n.T(lang, k, a...) }
	meta := []pdf.Meta{{Label: T("procurement.pdf.location"), Value: d.Plant.Code + " · " + d.Plant.Name},
		{Label: T("procurement.pdf.department"), Value: orDash(strp(d.DepartmentName))},
		{Label: T("procurement.pdf.required_date"), Value: dates.FormatISO(d.RequiredDate)},
		{Label: T("procurement.pdf.requested_by"), Value: d.RequestedBy.Name},
		{Label: T("procurement.pdf.created"), Value: dates.FormatDateTime(d.CreatedAt)}}
	if d.PreferredSupplier != nil {
		meta = append(meta, pdf.Meta{Label: T("procurement.pdf.preferred_supplier"), Value: d.PreferredSupplier.Name})
	}
	rep, err := pdf.New(c, h.d, pdf.Options{Title: T("procurement.pdf.request_title"), Subtitle: d.Plant.Name, DocumentNo: d.Number, Meta: meta})
	if err != nil {
		return err
	}
	rep.Badge(T("procurement.status."+d.Status), requestTone(d.Status))
	rep.Section(T("procurement.pdf.justification")).Note(d.Justification)
	rows := [][]any{}
	for _, l := range d.Lines {
		item := l.Description
		if l.ItemCode != nil {
			item = *l.ItemCode + " · " + item
		}
		rows = append(rows, []any{l.LineNo, item, l.CategoryName, format.Qty(l.Quantity) + " " + l.Unit, format.Int(float64(l.EstimatedUnitCost)), format.Int(float64(l.EstimatedTotal)), format.Qty(l.OrderedQty), format.Qty(l.ReceivedQty)})
	}
	rep.Section(T("procurement.pdf.lines_title")).Table([]pdf.Column{{Header: "#", Width: 0.4}, {Header: T("procurement.col.item"), Width: 3.6}, {Header: T("procurement.col.category"), Width: 1.6},
		{Header: T("procurement.col.quantity"), Width: 1.4, Align: pdf.Right}, {Header: T("procurement.col.est_unit_cost"), Width: 1.4, Align: pdf.Right},
		{Header: T("procurement.col.estimated"), Width: 1.5, Align: pdf.Right}, {Header: T("procurement.col.ordered"), Width: 1.1, Align: pdf.Right}, {Header: T("procurement.col.received"), Width: 1.1, Align: pdf.Right}},
		rows, []any{"", pdf.Cell{Text: T("procurement.col.total"), Bold: true}, "", "", "", pdf.Cell{Text: format.Int(float64(d.EstimatedTotal)), Bold: true}, "", ""})
	ar := [][]any{}
	for _, s := range d.Steps {
		who, when, comment := "—", "—", ""
		if s.Decision != nil {
			who, when, comment = s.Decision.By.Name, dates.FormatDateTime(s.Decision.At), deref(s.Decision.Comment)
		}
		ar = append(ar, []any{s.Level, s.Label, pdf.B(T("procurement.stepState."+s.State), map[string]pdf.Tone{"APPROVED": pdf.Green, "REJECTED": pdf.Red, "PENDING": pdf.Amber}[s.State]), who, when, comment})
	}
	rep.Section(T("procurement.pdf.approvals_title")).Table([]pdf.Column{{Header: "#", Width: 0.4}, {Header: T("procurement.col.level"), Width: 3}, {Header: T("procurement.col.status"), Width: 1.4},
		{Header: T("procurement.col.by"), Width: 2}, {Header: T("procurement.col.date"), Width: 1.6}, {Header: T("procurement.col.comment"), Width: 3}}, ar, nil, T("procurement.pdf.no_approval"))
	if d.RejectionReason != nil {
		rep.Note(T("procurement.pdf.rejected_reason") + ": " + *d.RejectionReason)
	}
	sigs := []pdf.Signatory{{Role: T("procurement.pdf.requested_by"), Name: d.RequestedBy.Name, At: d.SubmittedAt, Verb: T("procurement.pdf.verb_submitted")}}
	for _, s := range d.Steps {
		if s.Decision != nil && s.Decision.Decision == "APPROVED" {
			at := s.Decision.At
			sigs = append(sigs, pdf.Signatory{Role: s.Label, Name: s.Decision.By.Name, At: &at})
		}
	}
	rep.Signatures(sigs)
	return pdf.Send(c, rep, d.Number+"_"+d.Plant.Code+".pdf", c.Query("download") == "1")
}

func (h *handler) pdfSpend(c *fiber.Ctx) error {
	ctx, lang := httpx.Ctx(c), httpx.Lang(c)
	f, r, err := parseSpend(c)
	if err != nil {
		return err
	}
	rep, err := buildSpend(ctx, h.d.Pool, lang, f)
	if err != nil {
		return err
	}
	T := func(k string, a ...any) string { return i18n.T(lang, k, a...) }
	meta := []pdf.Meta{{Label: T("procurement.pdf.period"), Value: dates.FormatRange(r.From, r.To)}, {Label: T("procurement.pdf.grouped_by"), Value: T("procurement.report.group." + f.groupBy)}}
	if f.plantID != "" {
		if p, _, err := loadPlant(ctx, h.d.Pool, f.plantID); err == nil && p != nil {
			meta = append(meta, pdf.Meta{Label: T("procurement.pdf.location"), Value: p.Code + " · " + p.Name})
		}
	}
	if f.supplierID != "" {
		var name string
		if err := h.d.Pool.QueryRow(ctx, `SELECT name FROM supplier WHERE id = $1`, f.supplierID).Scan(&name); err == nil {
			meta = append(meta, pdf.Meta{Label: T("procurement.pdf.supplier"), Value: name})
		}
	}
	doc, err := pdf.New(c, h.d, pdf.Options{Title: T("procurement.pdf.spend_title"), Subtitle: dates.FormatRange(r.From, r.To), Meta: meta, Landscape: true})
	if err != nil {
		return err
	}
	t := rep.Totals
	doc.KpiRow([]pdf.Kpi{{Label: T("procurement.pdf.ordered"), Value: rwf(t.Ordered), Hint: T("procurement.pdf.orders_n", "n", t.Orders)},
		{Label: T("procurement.pdf.received_value"), Value: rwf(t.Received)}, {Label: T("procurement.pdf.outstanding"), Value: rwf(t.Outstanding)},
		{Label: T("procurement.pdf.invoiced"), Value: rwf(t.Invoiced), Hint: T("procurement.pdf.suppliers_n", "n", t.Suppliers)}})
	cols := func(first string) []pdf.Column {
		return []pdf.Column{{Header: first, Width: 3.5}, {Header: T("procurement.col.orders"), Width: 0.9, Align: pdf.Right}, {Header: T("procurement.col.lines"), Width: 0.9, Align: pdf.Right},
			{Header: T("procurement.col.ordered_value"), Width: 1.6, Align: pdf.Right}, {Header: T("procurement.col.received_value"), Width: 1.6, Align: pdf.Right},
			{Header: T("procurement.col.outstanding_value"), Width: 1.6, Align: pdf.Right}, {Header: T("procurement.col.invoiced"), Width: 1.6, Align: pdf.Right}}
	}
	table := func(rows []SpendRow) [][]any {
		out := make([][]any, 0, len(rows))
		for _, x := range rows {
			out = append(out, []any{x.Label, format.Int(float64(x.Orders)), format.Int(float64(x.Lines)), format.Int(float64(x.Ordered)), format.Int(float64(x.Received)),
				format.Int(float64(x.Outstanding)), format.Int(float64(x.Invoiced))})
		}
		return out
	}
	totals := []any{pdf.Cell{Text: T("procurement.col.total"), Bold: true}, format.Int(float64(t.Orders)), format.Int(float64(t.Lines)), format.Int(float64(t.Ordered)),
		format.Int(float64(t.Received)), format.Int(float64(t.Outstanding)), format.Int(float64(t.Invoiced))}
	g := T("procurement.report.group." + f.groupBy)
	doc.Section(T("procurement.pdf.by", "group", g)).Table(cols(g), table(rep.Rows), totals, T("procurement.pdf.no_data"))
	if f.groupBy != "category" {
		doc.Section(T("procurement.pdf.by", "group", T("procurement.report.group.category"))).Table(cols(T("procurement.report.group.category")), table(rep.ByCategory), totals, T("procurement.pdf.no_data"))
	}
	if f.groupBy != "supplier" {
		doc.Section(T("procurement.pdf.by", "group", T("procurement.report.group.supplier"))).Table(cols(T("procurement.report.group.supplier")), table(rep.BySupplier), totals, T("procurement.pdf.no_data"))
	}
	doc.Note(T("procurement.pdf.spend_note"))
	return pdf.Send(c, doc, "Procurement-Spend_"+r.FromISO+"_"+r.ToISO+".pdf", c.Query("download") == "1")
}

func orDash(s *string) string {
	if s == nil || strings.TrimSpace(*s) == "" {
		return "—"
	}
	return *s
}

func safeFile(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == ' ' || r == '/' || r == '\\' {
			return '-'
		}
		return r
	}, s)
	if len([]rune(s)) > 40 {
		s = string([]rune(s)[:40])
	}
	return s
}
