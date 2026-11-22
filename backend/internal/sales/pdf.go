package sales

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/pdf"

	"github.com/gofiber/fiber/v2"
)

var slugRe = regexp.MustCompile(`[^\w-]+`)
var dashRe = regexp.MustCompile(`-+`)

func slug(s string) string {
	return strings.Trim(dashRe.ReplaceAllString(slugRe.ReplaceAllString(s, "-"), "-"), "-")
}

func download(c *fiber.Ctx) bool { return c.Query("download") == "1" }

func trunc(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func fmtI(n int64) string { return format.Int(float64(n)) }

func nonEmpty(lines ...string) []string {
	var out []string
	for _, l := range lines {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func dash(s *string) string {
	if s == nil || *s == "" {
		return "—"
	}
	return *s
}

// boxTable renders "party boxes" as a table: the headers are the box titles and each row one line of every box.
func boxTable(rep *pdf.Report, titles []string, boxes ...[]string) {
	n := 0
	for _, b := range boxes {
		n = max(n, len(b))
	}
	cols := make([]pdf.Column, len(titles))
	for i, t := range titles {
		cols[i] = pdf.Column{Header: t, Width: 1}
	}
	rows := make([][]any, 0, n)
	for i := 0; i < n; i++ {
		row := make([]any, len(boxes))
		for j, b := range boxes {
			if i < len(b) {
				row[j] = b[i]
			}
		}
		rows = append(rows, row)
	}
	rep.Table(cols, rows, nil)
}

// plantLabel returns the localized plant label for the document and an ASCII-only one for the file name.
func (h *handler) plantLabel(ctx context.Context, lang i18n.Lang, plantID string) (label, file string, err error) {
	if plantID == "" {
		return i18n.T(lang, "sales.pdf.all_plants"), "All-plants", nil
	}
	var code string
	if err := h.d.Pool.QueryRow(ctx, `SELECT code FROM "Plant" WHERE id=$1`, plantID).Scan(&code); err != nil {
		if db.IsNoRows(err) {
			return i18n.T(lang, "sales.pdf.unknown_plant"), "Unknown-plant", nil
		}
		return "", "", err
	}
	return code, slug(code), nil
}

// ───────────────────────────── sales report ─────────────────────────────

func (h *handler) pdfReport(c *fiber.Ctx) error {
	r := newRq(c)
	q := httpx.Query(c)
	from, to := q.OptStr("from").Get(), q.OptStr("to").Get()
	plantID := auth.PlantFilter(r.u, q.OptStr("plantId").Get())
	rng := dates.ResolveRange(from, to)

	qq := &qb{}
	conds := []string{"sa.status = 'CONFIRMED'", fmt.Sprintf("sa.date BETWEEN %s::date AND %s::date", qq.arg(rng.FromISO), qq.arg(rng.ToISO))}
	if plantID != "" {
		conds = append(conds, `sa."plantId" = `+qq.arg(plantID))
	}
	where := and(conds)
	type lineRow struct {
		saleID, invType, receipt, invoice, customerID, customer, productID, product string
		date                                                                        time.Time
		sort                                                                        int
		unit, qty, amount                                                           int64
	}
	rows, err := h.d.Pool.Query(r.ctx, `
		SELECT sa.id, sa.date, sa."invoiceType", COALESCE(sa."ebmReceiptNo", ''), COALESCE(sa."ebmInvoiceNo", ''), sa."customerId", c.name,
		  sl."productId", pr.name, pr."sortOrder", sl."unitPrice", sl.quantity, sl.amount
		FROM "Sale" sa JOIN "Customer" c ON c.id = sa."customerId" JOIN "SaleLine" sl ON sl."saleId" = sa.id JOIN "Product" pr ON pr.id = sl."productId"
		WHERE `+where+` ORDER BY sa.date, sa.number, sl.id`, qq.args...)
	if err != nil {
		return err
	}
	var lines []lineRow
	for rows.Next() {
		var l lineRow
		if err := rows.Scan(&l.saleID, &l.date, &l.invType, &l.receipt, &l.invoice, &l.customerID, &l.customer, &l.productID, &l.product, &l.sort, &l.unit, &l.qty, &l.amount); err != nil {
			rows.Close()
			return err
		}
		lines = append(lines, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	paidBy := map[string]int64{}
	channels := map[string][]string{}
	pr, err := h.d.Pool.Query(r.ctx, `
		SELECT p."saleId", p.amount, p.method::text FROM "Payment" p JOIN "Sale" sa ON sa.id = p."saleId" WHERE `+where+` ORDER BY p.date, p."createdAt"`, qq.args...)
	if err != nil {
		return err
	}
	for pr.Next() {
		var sid, m string
		var a int64
		if err := pr.Scan(&sid, &a, &m); err != nil {
			pr.Close()
			return err
		}
		paidBy[sid] += a
		label := methodLabel(r.lang, m)
		dup := false
		for _, x := range channels[sid] {
			dup = dup || x == label
		}
		if !dup {
			channels[sid] = append(channels[sid], label)
		}
	}
	pr.Close()
	if err := pr.Err(); err != nil {
		return err
	}
	dns := map[string][]string{}
	dr, err := h.d.Pool.Query(r.ctx, `SELECT d."saleId", d."dnNumber" FROM "Delivery" d JOIN "Sale" sa ON sa.id = d."saleId" WHERE d.status <> 'CANCELLED' AND `+where+` ORDER BY d."createdAt"`, qq.args...)
	if err != nil {
		return err
	}
	for dr.Next() {
		var sid, dn string
		if err := dr.Scan(&sid, &dn); err != nil {
			dr.Close()
			return err
		}
		dns[sid] = append(dns[sid], dn)
	}
	dr.Close()
	if err := dr.Err(); err != nil {
		return err
	}

	type prodAgg struct {
		name        string
		sort        int
		qty, amount int64
	}
	type custAgg struct {
		name              string
		sales             int
		qty, amount, paid int64
	}
	var data [][]any
	byProduct := map[string]*prodAgg{}
	byCustomer := map[string]*custAgg{}
	var custOrder []string
	var totalAmount, totalQty, totalPaid int64
	saleSeen := map[string]bool{}
	nSales := 0
	// sale totals (a sale's lines are contiguous because of the ORDER BY)
	saleQty, saleAmount := map[string]int64{}, map[string]int64{}
	for _, l := range lines {
		saleQty[l.saleID] += l.qty
		saleAmount[l.saleID] += l.amount
	}
	for _, l := range lines {
		first := !saleSeen[l.saleID]
		if first {
			saleSeen[l.saleID] = true
			nSales++
			paid := paidBy[l.saleID]
			totalAmount += saleAmount[l.saleID]
			totalQty += saleQty[l.saleID]
			totalPaid += paid
			cu := byCustomer[l.customerID]
			if cu == nil {
				cu = &custAgg{name: l.customer}
				byCustomer[l.customerID] = cu
				custOrder = append(custOrder, l.customerID)
			}
			cu.sales++
			cu.qty += saleQty[l.saleID]
			cu.amount += saleAmount[l.saleID]
			cu.paid += paid
		}
		var paidCell any = ""
		channel := ""
		if first {
			paidCell, channel = fmtI(paidBy[l.saleID]), strings.Join(channels[l.saleID], ", ")
		}
		data = append(data, []any{dates.FormatDate(l.date), l.product, l.invType, l.receipt, l.invoice, strings.Join(dns[l.saleID], ", "), l.customer,
			fmtI(l.unit), fmtI(l.qty), fmtI(l.amount), paidCell, channel})
		pa := byProduct[l.productID]
		if pa == nil {
			pa = &prodAgg{name: l.product, sort: l.sort}
			byProduct[l.productID] = pa
		}
		pa.qty += l.qty
		pa.amount += l.amount
	}

	plant, plantFile, err := h.plantLabel(r.ctx, r.lang, plantID)
	if err != nil {
		return err
	}
	period := dates.FormatDate(rng.From)
	if rng.FromISO != rng.ToISO {
		period += " – " + dates.FormatDate(rng.To)
	}
	T := func(k string, a ...any) string { return r.t(k, a...) }
	rep, err := pdf.New(c, h.d, pdf.Options{Title: T("sales.pdf.report.title"), Subtitle: fmt.Sprintf("%s · %s · %s", plant, period, T("sales.pdf.report.confirmed_only")),
		Meta: []pdf.Meta{{Label: T("sales.pdf.plant"), Value: plant}, {Label: T("sales.pdf.period"), Value: period}}, Landscape: true})
	if err != nil {
		return err
	}
	outHint := ""
	if totalAmount > 0 {
		outHint = T("sales.pdf.report.pct_of_value", "pct", fmt.Sprintf("%.1f", (1-float64(totalPaid)/float64(totalAmount))*100))
	}
	rep.KpiRow([]pdf.Kpi{
		{Label: T("sales.pdf.report.kpi_value"), Value: format.RWF(float64(totalAmount)), Hint: T("sales.pdf.report.n_orders", "n", nSales)},
		{Label: T("sales.pdf.report.kpi_blocks"), Value: fmtI(totalQty), Hint: T("sales.pdf.report.pieces_ordered")},
		{Label: T("sales.pdf.report.kpi_collected"), Value: format.RWF(float64(totalPaid)), Hint: T("sales.pdf.report.paid_against")},
		{Label: T("sales.pdf.outstanding"), Value: format.RWF(float64(totalAmount - totalPaid)), Hint: outHint},
	})
	rep.Section(T("sales.pdf.report.section_sales"))
	cols := []pdf.Column{
		{Header: T("sales.pdf.date"), Width: 1}, {Header: T("sales.pdf.report.col_items"), Width: 1.8}, {Header: T("sales.pdf.report.col_inv_type"), Width: 0.65},
		{Header: T("sales.pdf.report.col_receipt"), Width: 0.85}, {Header: T("sales.pdf.report.col_invoice"), Width: 0.85}, {Header: T("sales.pdf.report.col_dn"), Width: 1.1},
		{Header: T("sales.pdf.customer"), Width: 2.9}, {Header: T("sales.pdf.unit_price"), Width: 0.9, Align: pdf.Right}, {Header: T("sales.pdf.quantity"), Width: 0.85, Align: pdf.Right},
		{Header: T("sales.pdf.amount"), Width: 1.15, Align: pdf.Right}, {Header: T("sales.pdf.paid"), Width: 1.1, Align: pdf.Right}, {Header: T("sales.pdf.report.col_deposit"), Width: 1.1},
	}
	rep.Table(cols, data, []any{T("sales.pdf.total"), "", "", "", "", "", "", "", fmtI(totalQty), fmtI(totalAmount), fmtI(totalPaid), ""}, T("sales.pdf.report.empty"))

	prods := make([]*prodAgg, 0, len(byProduct))
	for _, p := range byProduct {
		prods = append(prods, p)
	}
	sortSlice(prods, func(a, b *prodAgg) bool {
		if a.sort != b.sort {
			return a.sort < b.sort
		}
		return strings.ToLower(a.name) < strings.ToLower(b.name)
	})
	rep.Section(T("sales.pdf.report.by_product"))
	var prow [][]any
	for _, p := range prods {
		avg := "—"
		if p.qty > 0 {
			avg = format.Int(float64(p.amount) / float64(p.qty))
		}
		prow = append(prow, []any{p.name, fmtI(p.qty), avg, fmtI(p.amount)})
	}
	rep.Table([]pdf.Column{{Header: T("sales.pdf.product"), Width: 2.4}, {Header: T("sales.pdf.quantity"), Align: pdf.Right}, {Header: T("sales.pdf.report.avg_price"), Align: pdf.Right}, {Header: T("sales.pdf.amount"), Width: 1.3, Align: pdf.Right}},
		prow, []any{T("sales.pdf.total"), fmtI(totalQty), "", fmtI(totalAmount)})

	custs := make([]*custAgg, 0, len(byCustomer))
	for _, id := range custOrder {
		custs = append(custs, byCustomer[id])
	}
	sortSlice(custs, func(a, b *custAgg) bool { return a.amount > b.amount })
	rep.Section(T("sales.pdf.report.by_customer"))
	var crow [][]any
	for _, cu := range custs {
		crow = append(crow, []any{cu.name, cu.sales, fmtI(cu.qty), fmtI(cu.amount), fmtI(cu.paid), fmtI(cu.amount - cu.paid)})
	}
	rep.Table([]pdf.Column{{Header: T("sales.pdf.customer"), Width: 2.4}, {Header: T("sales.pdf.report.col_sales"), Width: 0.6, Align: pdf.Right}, {Header: T("sales.pdf.quantity"), Align: pdf.Right},
		{Header: T("sales.pdf.amount"), Width: 1.2, Align: pdf.Right}, {Header: T("sales.pdf.paid"), Width: 1.2, Align: pdf.Right}, {Header: T("sales.pdf.outstanding"), Width: 1.2, Align: pdf.Right}},
		crow, []any{T("sales.pdf.total"), nSales, fmtI(totalQty), fmtI(totalAmount), fmtI(totalPaid), fmtI(totalAmount - totalPaid)})
	rep.Note(T("sales.pdf.report.note"))
	return pdf.Send(c, rep, fmt.Sprintf("%s_Sales-Report_%s_%s.pdf", plantFile, rng.FromISO, rng.ToISO), download(c))
}

func sortSlice[T any](s []T, less func(a, b T) bool) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && less(s[j], s[j-1]); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// ───────────────────────────── delivery note ─────────────────────────────

func (h *handler) pdfDeliveryNote(c *fiber.Ctx) error {
	r := newRq(c)
	T := func(k string, a ...any) string { return r.t(k, a...) }
	var (
		dnNumber, status, plantID, plantCode, plantName, saleNo, invType, custName string
		vehicle, driver, receivedBy, ebmInv, ebmRec, addr, phone, tin              *string
		date, saleDate                                                             time.Time
		deliveredAt                                                                *time.Time
		creator                                                                    string
	)
	err := h.d.Pool.QueryRow(r.ctx, `
		SELECT d."dnNumber", d.status::text, d."plantId", pl.code, pl.name, sa.number, sa."invoiceType", sa."ebmInvoiceNo", sa."ebmReceiptNo", sa.date,
		  c.name, c.address, c.phone, c.tin, d.vehicle, d.driver, d."receivedBy", d."deliveredAt", d.date, COALESCE(u.name, '')
		FROM "Delivery" d JOIN "Plant" pl ON pl.id = d."plantId" JOIN "Sale" sa ON sa.id = d."saleId" JOIN "Customer" c ON c.id = sa."customerId"
		LEFT JOIN "User" u ON u.id = d."createdById" WHERE d.id = $1`, c.Params("deliveryId")).
		Scan(&dnNumber, &status, &plantID, &plantCode, &plantName, &saleNo, &invType, &ebmInv, &ebmRec, &saleDate, &custName, &addr, &phone, &tin, &vehicle, &driver, &receivedBy, &deliveredAt, &date, &creator)
	if err != nil {
		if db.IsNoRows(err) {
			return r.notFoundDelivery()
		}
		return err
	}
	if err := auth.AssertPlantAccess(r.u, plantID); err != nil {
		return err
	}
	rows, err := h.d.Pool.Query(r.ctx, `SELECT pr.code, pr.name, COALESCE(pr.dimensions, ''), pr.unit, dl.quantity FROM "DeliveryLine" dl JOIN "Product" pr ON pr.id = dl."productId"
		WHERE dl."deliveryId" = $1 ORDER BY pr."sortOrder", pr.code, dl.id`, c.Params("deliveryId"))
	if err != nil {
		return err
	}
	var data [][]any
	var total int64
	for rows.Next() {
		var code, name, dim, unit string
		var qty int64
		if err := rows.Scan(&code, &name, &dim, &unit, &qty); err != nil {
			rows.Close()
			return err
		}
		total += qty
		data = append(data, []any{len(data) + 1, code, name, dim, unit, fmtI(qty)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	company, err := h.d.Settings.Company(r.ctx)
	if err != nil {
		return err
	}
	rep, err := pdf.New(c, h.d, pdf.Options{Title: T("sales.pdf.dn.title"), Subtitle: fmt.Sprintf("%s (%s)", plantName, plantCode), DocumentNo: dnNumber,
		Meta: []pdf.Meta{{Label: T("sales.pdf.date"), Value: dates.FormatDate(date)}, {Label: T("sales.pdf.sales_order"), Value: saleNo},
			{Label: T("sales.pdf.status"), Value: T("sales.dstatus." + status)}}})
	if err != nil {
		return err
	}
	if status == "CANCELLED" {
		rep.Badge(T("sales.dstatus.CANCELLED"), pdf.Red).Note(T("sales.pdf.dn.cancelled_banner"))
	}
	blank := "________________"
	transport := []string{T("sales.pdf.dn.from", "plant", plantCode), T("sales.pdf.dn.vehicle", "v", orBlank(vehicle, blank)), T("sales.pdf.dn.driver", "v", orBlank(driver, blank))}
	boxTable(rep, []string{T("sales.pdf.dn.deliver_to"), T("sales.pdf.references"), T("sales.pdf.dn.transport")},
		nonEmpty(custName, deref(addr), telLine(r, phone), tinLine(r, tin)),
		[]string{saleNo, T("sales.pdf.inv_no", "type", invType, "no", dash(ebmInv)), T("sales.pdf.ebm_receipt", "no", dash(ebmRec)), T("sales.pdf.sale_date", "date", dates.FormatDate(saleDate))},
		transport)
	if status == "DELIVERED" {
		when := ""
		if deliveredAt != nil {
			when = dates.FormatDateTime(*deliveredAt)
		}
		rep.Badge(strings.TrimSpace(T("sales.pdf.dn.delivered_badge", "when", when)), pdf.Green)
	}
	rep.Section(T("sales.pdf.dn.goods"))
	rep.Table([]pdf.Column{{Header: "#", Width: 0.35}, {Header: T("sales.pdf.code"), Width: 0.9}, {Header: T("sales.pdf.description"), Width: 3}, {Header: T("sales.pdf.dimensions"), Width: 1.4},
		{Header: T("sales.pdf.unit"), Width: 0.6}, {Header: T("sales.pdf.quantity"), Width: 1, Align: pdf.Right}},
		data, []any{"", "", T("sales.pdf.dn.total_pieces"), "", "", fmtI(total)})
	if status == "DELIVERED" && receivedBy != nil && *receivedBy != "" {
		note := T("sales.pdf.dn.received_by", "name", *receivedBy)
		if deliveredAt != nil {
			note = T("sales.pdf.dn.received_by_on", "name", *receivedBy, "when", dates.FormatDateTime(*deliveredAt))
		}
		rep.Note(note)
	}
	rep.Note(T("sales.pdf.dn.terms", "company", company.LegalName))
	rep.Signatures([]pdf.Signatory{
		{Role: T("sales.pdf.dn.sig_dispatched"), Name: creator},
		{Role: T("sales.pdf.dn.sig_driver"), Name: deref(driver)},
		{Role: T("sales.pdf.dn.sig_received"), Name: deref(receivedBy)},
	})
	return pdf.Send(c, rep, fmt.Sprintf("%s_Delivery-Note_%s_%s.pdf", plantCode, slug(dnNumber), dates.ToISO(date)), download(c))
}

func orBlank(s *string, blank string) string {
	if s == nil || *s == "" {
		return blank
	}
	return *s
}

func telLine(r rq, s *string) string {
	if s == nil || *s == "" {
		return ""
	}
	return r.t("sales.pdf.tel", "v", *s)
}

func tinLine(r rq, s *string) string {
	if s == nil || *s == "" {
		return ""
	}
	return r.t("sales.pdf.tin", "v", *s)
}

// ───────────────────────────── order / pro-forma ─────────────────────────────

func (h *handler) pdfOrder(c *fiber.Ctx) error {
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
	company, err := h.d.Settings.Company(r.ctx)
	if err != nil {
		return err
	}
	cu := sale.Customer
	title := T("sales.pdf.order.title_order")
	if sale.Status == "DRAFT" {
		title = T("sales.pdf.order.title_proforma")
	}
	rep, err := pdf.New(c, h.d, pdf.Options{Title: title, Subtitle: fmt.Sprintf("%s (%s)", sale.Plant.Name, sale.Plant.Code), DocumentNo: sale.Number,
		Meta: []pdf.Meta{{Label: T("sales.pdf.date"), Value: dates.FormatDate(mustDate(sale.Date))}, {Label: T("sales.pdf.status"), Value: T("sales.status." + sale.Status)}}})
	if err != nil {
		return err
	}
	if sale.Status == "CANCELLED" {
		txt := T("sales.pdf.order.cancelled_banner")
		if sale.CancelReason != nil && *sale.CancelReason != "" {
			txt = T("sales.pdf.order.cancelled_banner_reason", "reason", *sale.CancelReason)
		}
		rep.Badge(T("sales.status.CANCELLED"), pdf.Red).Note(txt)
	} else {
		rep.Badge(T("sales.pdf.order.not_fiscal_badge"), pdf.Amber).Note(T("sales.pdf.order.not_fiscal"))
	}
	var activeDNs []string
	for _, d := range sale.Deliveries {
		if d.Status != "CANCELLED" {
			activeDNs = append(activeDNs, d.DNNumber)
		}
	}
	dns := "—"
	if len(activeDNs) > 0 {
		dns = strings.Join(activeDNs, ", ")
	}
	boxTable(rep, []string{T("sales.pdf.customer"), T("sales.pdf.references")},
		nonEmpty(cu.Name, deref(cu.Address), telLine(r, cu.Phone), deref(cu.Email), tinLine(r, cu.TIN)),
		[]string{sale.Number, T("sales.pdf.inv_no", "type", sale.InvoiceType, "no", dash(sale.EbmInvoiceNo)), T("sales.pdf.ebm_receipt", "no", dash(sale.EbmReceiptNo)), T("sales.pdf.order.delivery_notes", "dns", dns)})
	rep.Section(T("sales.pdf.order.items"))
	var data [][]any
	for i, l := range sale.Lines {
		desc := l.Product.Name
		if l.Product.Dimensions != nil && *l.Product.Dimensions != "" {
			desc += " (" + *l.Product.Dimensions + ")"
		}
		data = append(data, []any{i + 1, desc, fmtI(l.Quantity), fmtI(l.UnitPrice), fmtI(l.Amount), fmtI(l.Delivered)})
	}
	rep.Table([]pdf.Column{{Header: "#", Width: 0.35}, {Header: T("sales.pdf.description"), Width: 3}, {Header: T("sales.pdf.quantity"), Width: 1, Align: pdf.Right}, {Header: T("sales.pdf.unit_price"), Width: 1, Align: pdf.Right},
		{Header: T("sales.pdf.amount_rwf"), Width: 1.3, Align: pdf.Right}, {Header: T("sales.pdf.delivered"), Width: 0.9, Align: pdf.Right}},
		data, []any{"", T("sales.pdf.total"), fmtI(sale.Totals.Quantity), "", fmtI(sale.Totals.Amount), fmtI(sale.Totals.Delivered)})
	balance := sale.Totals.Amount - sale.Totals.Paid
	if sale.Status == "CONFIRMED" {
		balance = sale.Totals.Balance
	}
	rep.KpiRow([]pdf.Kpi{{Label: T("sales.pdf.order.total"), Value: format.RWF(float64(sale.Totals.Amount))}, {Label: T("sales.pdf.order.paid_to_date"), Value: format.RWF(float64(sale.Totals.Paid))},
		{Label: T("sales.pdf.order.balance_due"), Value: format.RWF(float64(balance))}})
	if len(sale.Payments) > 0 {
		rep.Section(T("sales.pdf.order.payments"))
		var prow [][]any
		for _, p := range sale.Payments {
			prow = append(prow, []any{dates.FormatDate(mustDate(p.Date)), p.MethodLabel, deref(p.Reference), fmtI(p.Amount)})
		}
		rep.Table([]pdf.Column{{Header: T("sales.pdf.date")}, {Header: T("sales.pdf.method")}, {Header: T("sales.pdf.reference"), Width: 1.5}, {Header: T("sales.pdf.amount_rwf"), Align: pdf.Right}}, prow, nil)
	}
	if sale.Notes != nil && *sale.Notes != "" {
		rep.Note(T("sales.pdf.order.notes", "v", *sale.Notes))
	}
	rep.Note(T("sales.pdf.order.payment_note", "company", company.LegalName, "number", sale.Number))
	prepared := pdf.Signatory{Role: T("sales.pdf.order.sig_prepared")}
	if sale.CreatedBy != nil {
		prepared.Name = sale.CreatedBy.Name
	}
	confirmed := pdf.Signatory{Role: T("sales.pdf.order.sig_confirmed"), Verb: T("sales.pdf.order.verb_confirmed")}
	if sale.ConfirmedBy != nil {
		confirmed.Name = sale.ConfirmedBy.Name
	}
	if sale.ConfirmedAt != nil {
		t := sale.ConfirmedAt.Time
		confirmed.At = &t
	}
	rep.Signatures([]pdf.Signatory{prepared, confirmed, {Role: T("sales.pdf.order.sig_customer")}})
	kind := "Sales-Order"
	if sale.Status == "DRAFT" {
		kind = "Pro-forma"
	}
	return pdf.Send(c, rep, fmt.Sprintf("%s_%s_%s.pdf", sale.Plant.Code, kind, sale.Number), download(c))
}

func mustDate(iso string) time.Time { return dates.MustParseISO(iso) }

// ───────────────────────────── customer statement ─────────────────────────────

func (h *handler) pdfStatement(c *fiber.Ctx) error {
	r := newRq(c)
	T := func(k string, a ...any) string { return r.t(k, a...) }
	q := httpx.Query(c)
	from, to := optDate(q, "from"), optDate(q, "to")
	if err := q.Err(); err != nil {
		return err
	}
	data, err := h.s.buildCustomerStatement(r.ctx, r.lang, c.Params("customerId"), from, to)
	if err != nil {
		return err
	}
	cu := data.Customer
	period := dates.FormatDate(mustDate(data.Range.From)) + " – " + dates.FormatDate(mustDate(data.Range.To))
	rep, err := pdf.New(c, h.d, pdf.Options{Title: T("sales.pdf.stmt.title"), Subtitle: period,
		Meta: []pdf.Meta{{Label: T("sales.pdf.customer"), Value: trunc(cu.Name, 28)}, {Label: T("sales.pdf.period"), Value: period}}})
	if err != nil {
		return err
	}
	last := "—"
	if data.Summary.LastSaleDate != nil {
		last = dates.FormatDate(mustDate(*data.Summary.LastSaleDate))
	}
	boxTable(rep, []string{T("sales.pdf.stmt.statement_for"), T("sales.pdf.stmt.account_summary")},
		nonEmpty(cu.Name, deref(cu.Address), telLine(r, cu.Phone), deref(cu.Email), tinLine(r, cu.TIN)),
		[]string{T("sales.pdf.stmt.outstanding", "v", format.RWF(float64(data.Summary.Outstanding))),
			T("sales.pdf.stmt.total_sales", "v", format.RWF(float64(data.Summary.TotalSales)), "n", data.Summary.SalesCount),
			T("sales.pdf.stmt.total_paid", "v", format.RWF(float64(data.Summary.Paid))), T("sales.pdf.stmt.last_sale", "v", last)})
	st := data.Statement
	rep.KpiRow([]pdf.Kpi{
		{Label: T("sales.pdf.stmt.kpi_opening"), Value: format.RWF(float64(st.Opening)), Hint: T("sales.pdf.stmt.at", "date", dates.FormatDate(mustDate(data.Range.From)))},
		{Label: T("sales.pdf.stmt.kpi_invoiced"), Value: format.RWF(float64(st.Debit)), Hint: T("sales.pdf.stmt.hint_invoiced")},
		{Label: T("sales.pdf.stmt.kpi_received"), Value: format.RWF(float64(st.Credit)), Hint: T("sales.pdf.stmt.hint_received")},
		{Label: T("sales.pdf.stmt.kpi_closing"), Value: format.RWF(float64(st.Closing)), Hint: T("sales.pdf.stmt.at", "date", dates.FormatDate(mustDate(data.Range.To)))},
	})
	rep.Section(T("sales.pdf.stmt.activity"))
	rows := [][]any{{dates.FormatDate(mustDate(data.Range.From)), "", T("sales.pdf.stmt.opening_row"), "", "", fmtI(st.Opening)}}
	for _, x := range st.Rows {
		var dr, cr any = "", ""
		if x.Debit != 0 {
			dr = fmtI(x.Debit)
		}
		if x.Credit != 0 {
			cr = fmtI(x.Credit)
		}
		rows = append(rows, []any{dates.FormatDate(mustDate(x.Date)), x.Reference, x.Description, dr, cr, fmtI(x.Balance)})
	}
	rep.Table([]pdf.Column{{Header: T("sales.pdf.date"), Width: 0.9}, {Header: T("sales.pdf.reference"), Width: 1.1}, {Header: T("sales.pdf.description"), Width: 2.8},
		{Header: T("sales.pdf.stmt.debit"), Width: 1, Align: pdf.Right}, {Header: T("sales.pdf.stmt.credit"), Width: 1, Align: pdf.Right}, {Header: T("sales.pdf.stmt.balance"), Width: 1.1, Align: pdf.Right}},
		rows, []any{"", "", T("sales.pdf.stmt.totals_period"), fmtI(st.Debit), fmtI(st.Credit), fmtI(st.Closing)})
	if len(data.OpenSales) > 0 {
		rep.Section(T("sales.pdf.stmt.unpaid"))
		var orows [][]any
		var tot int64
		for _, s := range data.OpenSales {
			tot += s.Outstanding
			orows = append(orows, []any{s.Number, dates.FormatDate(mustDate(s.Date)), fmtI(s.Amount), fmtI(s.Paid), fmtI(s.Outstanding)})
		}
		rep.Table([]pdf.Column{{Header: T("sales.pdf.sale")}, {Header: T("sales.pdf.date")}, {Header: T("sales.pdf.amount"), Align: pdf.Right}, {Header: T("sales.pdf.paid"), Align: pdf.Right}, {Header: T("sales.pdf.outstanding"), Align: pdf.Right}},
			orows, []any{T("sales.pdf.total"), "", "", "", fmtI(tot)})
	}
	rep.Note(T("sales.pdf.stmt.note"))
	return pdf.Send(c, rep, fmt.Sprintf("Statement_%s_%s_%s.pdf", slug(cu.Name), data.Range.From, data.Range.To), download(c))
}

// ───────────────────────────── receivables ageing ─────────────────────────────

func (h *handler) pdfReceivables(c *fiber.Ctx) error {
	r := newRq(c)
	T := func(k string, a ...any) string { return r.t(k, a...) }
	asOf, plantID, err := receivablesParams(c, r.u)
	if err != nil {
		return err
	}
	data, err := h.s.buildReceivables(r.ctx, asOf, plantID)
	if err != nil {
		return err
	}
	plant, plantFile, err := h.plantLabel(r.ctx, r.lang, plantID)
	if err != nil {
		return err
	}
	asOfFmt := dates.FormatDate(mustDate(asOf))
	rep, err := pdf.New(c, h.d, pdf.Options{Title: T("sales.pdf.rcv.title"), Subtitle: T("sales.pdf.rcv.subtitle", "plant", plant, "date", asOfFmt),
		Meta: []pdf.Meta{{Label: T("sales.pdf.plant"), Value: plant}, {Label: T("sales.pdf.rcv.as_of"), Value: asOfFmt}}, Landscape: true})
	if err != nil {
		return err
	}
	t := data.Totals
	share := func(v int64) string {
		if t.Total == 0 {
			return ""
		}
		return T("sales.pdf.rcv.share", "pct", fmt.Sprintf("%.0f", float64(v)/float64(t.Total)*100))
	}
	kpis := []pdf.Kpi{{Label: T("sales.pdf.rcv.total_outstanding"), Value: format.RWF(float64(t.Total)), Hint: T("sales.pdf.rcv.hint_total", "customers", len(data.Rows), "sales", t.OpenSales)}}
	for _, b := range ageBuckets {
		kpis = append(kpis, pdf.Kpi{Label: T("sales.pdf.rcv.b_" + string(b)), Value: format.RWF(float64(t.of(b))), Hint: share(t.of(b))})
	}
	rep.KpiRow(kpis)
	rep.Section(T("sales.pdf.rcv.by_customer"))
	cols := []pdf.Column{{Header: T("sales.pdf.customer"), Width: 2.6}, {Header: T("sales.pdf.rcv.open_sales"), Width: 0.8, Align: pdf.Right}, {Header: T("sales.pdf.rcv.oldest"), Width: 1}}
	for _, b := range ageBuckets {
		cols = append(cols, pdf.Column{Header: T("sales.pdf.rcv.b_" + string(b)), Width: 1.1, Align: pdf.Right})
	}
	cols = append(cols, pdf.Column{Header: T("sales.pdf.total"), Width: 1.2, Align: pdf.Right})
	var rows [][]any
	for _, x := range data.Rows {
		oldest := ""
		if x.OldestDate != nil {
			oldest = dates.FormatDate(mustDate(*x.OldestDate))
		}
		row := []any{x.CustomerName, x.OpenSales, oldest}
		for _, b := range ageBuckets {
			if v := x.of(b); v != 0 {
				row = append(row, fmtI(v))
			} else {
				row = append(row, "–")
			}
		}
		rows = append(rows, append(row, fmtI(x.Total)))
	}
	totals := []any{T("sales.pdf.total"), t.OpenSales, ""}
	for _, b := range ageBuckets {
		totals = append(totals, fmtI(t.of(b)))
	}
	rep.Table(cols, rows, append(totals, fmtI(t.Total)), T("sales.pdf.rcv.empty_customers"))
	rep.Section(T("sales.pdf.rcv.open_sales_section"))
	var srows [][]any
	for _, s := range data.Sales {
		srows = append(srows, []any{s.Number, dates.FormatDate(mustDate(s.Date)), s.PlantCode, s.CustomerName, s.Days, fmtI(s.Amount), fmtI(s.Paid), fmtI(s.Outstanding)})
	}
	rep.Table([]pdf.Column{{Header: T("sales.pdf.sale"), Width: 1.1}, {Header: T("sales.pdf.date"), Width: 0.9}, {Header: T("sales.pdf.plant"), Width: 0.6}, {Header: T("sales.pdf.customer"), Width: 2.6},
		{Header: T("sales.pdf.rcv.days"), Width: 0.6, Align: pdf.Right}, {Header: T("sales.pdf.amount"), Width: 1.1, Align: pdf.Right}, {Header: T("sales.pdf.paid"), Width: 1.1, Align: pdf.Right}, {Header: T("sales.pdf.outstanding"), Width: 1.1, Align: pdf.Right}},
		srows, nil, T("sales.pdf.rcv.empty_sales"))
	rep.Note(T("sales.pdf.rcv.note"))
	return pdf.Send(c, rep, fmt.Sprintf("%s_Receivables-Ageing_%s.pdf", plantFile, asOf), download(c))
}
