package sales

import (
	"fmt"
	"strconv"
	"strings"

	"btn-ops/internal/dates"
	"btn-ops/internal/format"
	"btn-ops/internal/pdf"

	"github.com/gofiber/fiber/v2"
)

// Commercial documents (ORD-08, DEL-02): quotation, order confirmation and the order / pro-forma invoice.
// All branded (logo, company block, colours from settings), localized (?lang=), portrait by default with
// ?orientation=landscape honoured by pdf.New. Each shows per-line discounts, the delivery charge line and the terms.

type docKind string

const (
	docQuotation    docKind = "quotation"
	docConfirmation docKind = "confirmation"
	docOrder        docKind = "order"
)

func (h *handler) pdfQuotation(c *fiber.Ctx) error    { return h.pdfCommercial(c, docQuotation) }
func (h *handler) pdfConfirmation(c *fiber.Ctx) error { return h.pdfCommercial(c, docConfirmation) }
func (h *handler) pdfOrder(c *fiber.Ctx) error        { return h.pdfCommercial(c, docOrder) }

func pctText(p float64) string { return strconv.FormatFloat(p, 'f', -1, 64) + " %" }

func (h *handler) pdfCommercial(c *fiber.Ctx, kind docKind) error {
	r := newRq(c)
	T := func(k string, a ...any) string { return r.t(k, a...) }
	pct, err := h.approvals(r.ctx)
	if err != nil {
		return err
	}
	sale, err := h.s.loadSale(r.ctx, h.d.Pool, r.lang, c.Params("saleId"), pct)
	if err != nil {
		return err
	}
	if sale == nil {
		return r.notFoundSale()
	}
	if err := assertSaleAccess(r.ctx, h.d.Pool, r.u, sale.ID, sale.Plant.ID); err != nil {
		return err
	}
	if err := h.s.loadSaleExtras(r.ctx, h.d.Pool, r.lang, r.u, sale); err != nil {
		return err
	}
	if kind == docConfirmation && !committed(sale.Status) {
		return r.conflict("sales.err.confirmation_needs_confirmed")
	}
	company, err := h.d.Settings.Company(r.ctx)
	if err != nil {
		return err
	}
	title := T("sales.pdf.order.title_order")
	switch {
	case kind == docQuotation:
		title = T("sales.pdf.quote.title")
	case kind == docConfirmation:
		title = T("sales.pdf.confirm.title")
	case editable(sale.Status):
		title = T("sales.pdf.order.title_proforma")
	}
	meta := []pdf.Meta{{Label: T("sales.pdf.date"), Value: dates.FormatDate(mustDate(sale.Date))}, {Label: T("sales.pdf.status"), Value: statusWord(r.lang, sale.Status)}}
	if kind != docQuotation && sale.Version > 1 {
		meta = append(meta, pdf.Meta{Label: T("sales.pdf.version"), Value: fmt.Sprint(sale.Version)})
	}
	if kind == docQuotation && sale.ValidUntil != nil {
		meta = append(meta, pdf.Meta{Label: T("sales.pdf.quote.valid_until"), Value: dates.FormatDate(mustDate(*sale.ValidUntil))})
	}
	rep, err := pdf.New(c, h.d, pdf.Options{Title: title, Subtitle: fmt.Sprintf("%s (%s)", sale.Plant.Name, sale.Plant.Code), DocumentNo: sale.Number, Meta: meta})
	if err != nil {
		return err
	}
	if sale.Status == StCancelled {
		txt := T("sales.pdf.order.cancelled_banner")
		if sale.CancelReason != nil && *sale.CancelReason != "" {
			txt = T("sales.pdf.order.cancelled_banner_reason", "reason", *sale.CancelReason)
		}
		rep.Badge(statusWord(r.lang, StCancelled), pdf.Red).Note(txt)
	} else if kind == docConfirmation {
		rep.Badge(T("sales.pdf.confirm.badge"), pdf.Green)
	} else if kind == docOrder {
		rep.Badge(T("sales.pdf.order.not_fiscal_badge"), pdf.Amber).Note(T("sales.pdf.order.not_fiscal"))
	}
	cu := sale.Customer
	term := "—"
	if sale.PaymentTermName != nil {
		term = *sale.PaymentTermName
	}
	delivery := []string{T("sales.transport." + sale.TransportMode)}
	if sale.DeliverySite != nil {
		delivery = append(delivery, *sale.DeliverySite)
	}
	if sale.DeliveryAddress != nil {
		delivery = append(delivery, *sale.DeliveryAddress)
	}
	if sale.DeliveryZoneName != nil {
		delivery = append(delivery, T("sales.pdf.zone", "v", *sale.DeliveryZoneName))
	}
	if sale.DistanceKm != nil {
		delivery = append(delivery, T("sales.pdf.distance", "v", strconv.FormatFloat(*sale.DistanceKm, 'f', -1, 64)))
	}
	if sale.RequestedDate != nil {
		delivery = append(delivery, T("sales.pdf.requested_on", "date", dates.FormatDate(mustDate(*sale.RequestedDate))))
	}
	refs := []string{sale.Number, T("sales.pdf.payment_term", "v", term)}
	if kind != docQuotation {
		refs = append(refs, T("sales.pdf.inv_no", "type", sale.InvoiceType, "no", dash(sale.EbmInvoiceNo)))
		if sale.DueDate != nil {
			refs = append(refs, T("sales.pdf.due", "date", dates.FormatDate(mustDate(*sale.DueDate))))
		}
	}
	boxTable(rep, []string{T("sales.pdf.customer"), T("sales.pdf.references"), T("sales.pdf.delivery")},
		nonEmpty(cu.Name, deref(cu.Address), telLine(r, cu.Phone), deref(cu.Email), tinLine(r, cu.TIN)), refs, delivery)

	rep.Section(T("sales.pdf.order.items"))
	var data [][]any
	hasDisc := sale.Totals.Discount > 0
	for i, l := range sale.Lines {
		desc := l.Product.Name
		if l.Product.Dimensions != nil && *l.Product.Dimensions != "" {
			desc += " (" + *l.Product.Dimensions + ")"
		}
		disc := ""
		if l.DiscountAmount > 0 {
			disc = fmtI(l.DiscountAmount)
			if l.DiscountPercent != nil {
				disc = pctText(*l.DiscountPercent) + " · " + disc
			}
		}
		row := []any{i + 1, desc, fmtI(l.Quantity), fmtI(l.UnitPrice), fmtI(l.Gross)}
		if hasDisc {
			row = append(row, disc)
		}
		row = append(row, fmtI(l.Amount))
		data = append(data, row)
	}
	if sale.ChargeLine != nil {
		label := T("sales.delivery_charge")
		if sale.ChargeLine.Description != nil && *sale.ChargeLine.Description != "" {
			label += " — " + *sale.ChargeLine.Description
		}
		row := []any{len(data) + 1, label, "", "", fmtI(sale.ChargeLine.Amount)}
		if hasDisc {
			row = append(row, "")
		}
		data = append(data, append(row, fmtI(sale.ChargeLine.Amount)))
	}
	cols := []pdf.Column{{Header: "#", Width: 0.35}, {Header: T("sales.pdf.description"), Width: 3}, {Header: T("sales.pdf.quantity"), Width: 0.9, Align: pdf.Right},
		{Header: T("sales.pdf.unit_price"), Width: 1, Align: pdf.Right}, {Header: T("sales.pdf.gross"), Width: 1.1, Align: pdf.Right}}
	totals := []any{"", T("sales.pdf.total"), fmtI(sale.Totals.Quantity), "", fmtI(sale.Totals.Gross + sale.Totals.Charge)}
	if hasDisc {
		cols = append(cols, pdf.Column{Header: T("sales.pdf.discount"), Width: 1.2, Align: pdf.Right})
		totals = append(totals, fmtI(sale.Totals.Discount))
	}
	cols = append(cols, pdf.Column{Header: T("sales.pdf.amount_rwf"), Width: 1.2, Align: pdf.Right})
	totals = append(totals, fmtI(sale.Totals.Amount))
	rep.Table(cols, data, totals)

	kpis := []pdf.Kpi{{Label: T("sales.pdf.order.total"), Value: format.RWF(float64(sale.Totals.Amount))}}
	if sale.DepositAmount > 0 {
		kpis = append(kpis, pdf.Kpi{Label: T("sales.pdf.deposit"), Value: format.RWF(float64(sale.DepositAmount))})
	}
	if kind != docQuotation {
		kpis = append(kpis, pdf.Kpi{Label: T("sales.pdf.order.paid_to_date"), Value: format.RWF(float64(sale.Totals.Paid))},
			pdf.Kpi{Label: T("sales.pdf.order.balance_due"), Value: format.RWF(float64(max(0, sale.Totals.Amount-sale.Totals.Paid)))})
	}
	rep.KpiRow(kpis)
	if kind == docOrder && len(sale.Payments) > 0 {
		rep.Section(T("sales.pdf.order.payments"))
		var prow [][]any
		for _, p := range sale.Payments {
			prow = append(prow, []any{dates.FormatDate(mustDate(p.Date)), T("sales.paykind." + p.Kind), p.MethodLabel, deref(p.Reference), fmtI(p.Amount)})
		}
		rep.Table([]pdf.Column{{Header: T("sales.pdf.date")}, {Header: T("sales.pdf.pay_kind")}, {Header: T("sales.pdf.method")}, {Header: T("sales.pdf.reference"), Width: 1.5},
			{Header: T("sales.pdf.amount_rwf"), Align: pdf.Right}}, prow, nil)
	}
	if sale.Notes != nil && *sale.Notes != "" {
		rep.Note(T("sales.pdf.order.notes", "v", *sale.Notes))
	}
	switch kind {
	case docQuotation:
		valid := "—"
		if sale.ValidUntil != nil {
			valid = dates.FormatDate(mustDate(*sale.ValidUntil))
		}
		rep.Note(T("sales.pdf.quote.terms", "company", company.LegalName, "valid", valid))
		rep.Signatures([]pdf.Signatory{{Role: T("sales.pdf.quote.sig_prepared"), Name: personName(sale.QuotedBy, sale.CreatedBy)}, {Role: T("sales.pdf.quote.sig_accepted")}})
	case docConfirmation:
		rep.Note(T("sales.pdf.confirm.terms", "company", company.LegalName, "number", sale.Number))
		confirmed := pdf.Signatory{Role: T("sales.pdf.order.sig_confirmed"), Name: personName(sale.ConfirmedBy, nil), Verb: T("sales.pdf.order.verb_confirmed")}
		if sale.ConfirmedAt != nil {
			t := sale.ConfirmedAt.Time
			confirmed.At = &t
		}
		rep.Signatures([]pdf.Signatory{confirmed, {Role: T("sales.pdf.order.sig_customer")}})
	default:
		rep.Note(T("sales.pdf.order.payment_note", "company", company.LegalName, "number", sale.Number))
		confirmed := pdf.Signatory{Role: T("sales.pdf.order.sig_confirmed"), Name: personName(sale.ConfirmedBy, nil), Verb: T("sales.pdf.order.verb_confirmed")}
		if sale.ConfirmedAt != nil {
			t := sale.ConfirmedAt.Time
			confirmed.At = &t
		}
		rep.Signatures([]pdf.Signatory{{Role: T("sales.pdf.order.sig_prepared"), Name: personName(sale.CreatedBy, nil)}, confirmed, {Role: T("sales.pdf.order.sig_customer")}})
	}
	names := map[docKind]string{docQuotation: "Quotation", docConfirmation: "Order-Confirmation", docOrder: "Sales-Order"}
	name := names[kind]
	if kind == docOrder && editable(sale.Status) {
		name = "Pro-forma"
	}
	return pdf.Send(c, rep, fmt.Sprintf("%s_%s_%s.pdf", sale.Plant.Code, name, strings.ReplaceAll(sale.Number, "/", "-")), download(c))
}

func personName(p, fallback *person) string {
	if p != nil {
		return p.Name
	}
	if fallback != nil {
		return fallback.Name
	}
	return ""
}
