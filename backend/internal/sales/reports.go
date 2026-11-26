package sales

import (
	"context"
	"fmt"
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

// Order reports (ORD-09) and delivery reports (DEL-06), each on screen (JSON), PDF and CSV/Excel/JSON (export framework).
//
//	GET /reports/orders?groupBy=client|product|status|period&period=day|week|month&from&to&plantId&status&customerId&transportMode
//	GET /deliveries/pending?plantId&customerId&transportMode&q      orders still to ship + dispatched notes awaiting confirmation
//	GET /pdf/orders-report?…  /pdf/deliveries?…  /pdf/pending-deliveries?…

type reportRow struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Code        string `json:"code,omitempty"`
	Orders      int64  `json:"orders"`
	Quantity    int64  `json:"quantity"`
	Amount      int64  `json:"amount"`
	Paid        int64  `json:"paid"`
	Outstanding int64  `json:"outstanding"`
	Delivered   int64  `json:"delivered"`
}

type reportOut struct {
	GroupBy string `json:"groupBy"`
	Period  string `json:"period"`
	Range   struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"range"`
	Rows   []reportRow `json:"rows"`
	Totals reportRow   `json:"totals"`
}

type reportQuery struct {
	GroupBy, Period, From, To, PlantID, Status, CustomerID, Transport string
}

func parseReportQuery(c *fiber.Ctx, u *auth.User) (reportQuery, error) {
	q := httpx.Query(c)
	var rq reportQuery
	rq.GroupBy = queryEnum(q, "groupBy", "client", "product", "status", "period")
	rq.Period = queryEnum(q, "period", "day", "week", "month")
	from, to := optDate(q, "from"), optDate(q, "to")
	rq.Status = queryEnum(q, "status", OrderStatuses...)
	rq.Transport = queryEnum(q, "transportMode", TransportClient, TransportBTN)
	rq.CustomerID = q.OptStr("customerId").Trim().Max(40).Get()
	pid := q.OptStr("plantId").Get()
	if err := q.Err(); err != nil {
		return rq, err
	}
	if rq.GroupBy == "" {
		rq.GroupBy = "client"
	}
	if rq.Period == "" {
		rq.Period = "month"
	}
	rng := dates.ResolveRange(from, to)
	rq.From, rq.To = rng.FromISO, rng.ToISO
	rq.PlantID = auth.PlantFilter(u, pid)
	return rq, nil
}

// buildOrdersReport groups orders (cancelled excluded unless filtered for) by client, product, status or period.
// Product rows count line quantities/amounts; other groupings count whole orders. One aggregate query.
func (s *service) buildOrdersReport(ctx context.Context, q db.DBTX, lang i18n.Lang, rq reportQuery) (*reportOut, error) {
	b := &qb{}
	conds := []string{fmt.Sprintf(`sa.date BETWEEN %s::date AND %s::date`, b.arg(rq.From), b.arg(rq.To))}
	if rq.PlantID != "" {
		conds = append(conds, `sa."plantId" = `+b.arg(rq.PlantID))
	}
	if rq.CustomerID != "" {
		conds = append(conds, `sa."customerId" = `+b.arg(rq.CustomerID))
	}
	if rq.Transport != "" {
		conds = append(conds, `sa."transportMode" = `+b.arg(rq.Transport))
	}
	if rq.Status != "" {
		conds = append(conds, `sa."orderStatus" = `+b.arg(rq.Status))
	} else {
		conds = append(conds, `sa."orderStatus" <> 'CANCELLED'`)
	}
	base := `WITH s AS (SELECT sa.id, sa.date, sa."orderStatus" AS st, sa.status::text AS legacy, sa."customerId" FROM "Sale" sa WHERE ` + and(conds) + `),
		l AS (SELECT "saleId", SUM(amount)::bigint a, SUM(quantity)::bigint q FROM "SaleLine" WHERE "saleId" IN (SELECT id FROM s) GROUP BY 1),
		p AS (SELECT "saleId", SUM(amount)::bigint a FROM "Payment" WHERE "saleId" IN (SELECT id FROM s) GROUP BY 1),
		d AS (SELECT dv."saleId", SUM(dl.quantity)::bigint q FROM "DeliveryLine" dl JOIN "Delivery" dv ON dv.id = dl."deliveryId" AND dv.status <> 'CANCELLED'
		      WHERE dv."saleId" IN (SELECT id FROM s) GROUP BY 1),
		o AS (SELECT s.*, COALESCE(l.a,0) amount, COALESCE(l.q,0) qty, COALESCE(p.a,0) paid, COALESCE(d.q,0) shipped,
		      CASE WHEN s.legacy = 'CONFIRMED' THEN GREATEST(COALESCE(l.a,0) - COALESCE(p.a,0), 0) ELSE 0 END outstanding
		      FROM s LEFT JOIN l ON l."saleId" = s.id LEFT JOIN p ON p."saleId" = s.id LEFT JOIN d ON d."saleId" = s.id)`
	var sql string
	trunc := map[string]string{"day": "day", "week": "week", "month": "month"}[rq.Period]
	switch rq.GroupBy {
	case "client":
		sql = base + ` SELECT o."customerId", c.name, '', COUNT(*), SUM(qty), SUM(amount), SUM(paid), SUM(outstanding), SUM(shipped)
			FROM o JOIN "Customer" c ON c.id = o."customerId" GROUP BY 1, 2 ORDER BY SUM(amount) DESC, c.name`
	case "status":
		sql = base + ` SELECT st, st, '', COUNT(*), SUM(qty), SUM(amount), SUM(paid), SUM(outstanding), SUM(shipped) FROM o GROUP BY st ORDER BY array_position($` +
			fmt.Sprint(len(b.args)+1) + `::text[], st)`
		b.args = append(b.args, OrderStatuses)
	case "period":
		sql = base + ` SELECT to_char(date_trunc('` + trunc + `', date), 'YYYY-MM-DD'), to_char(date_trunc('` + trunc + `', date), 'YYYY-MM-DD'), '',
			COUNT(*), SUM(qty), SUM(amount), SUM(paid), SUM(outstanding), SUM(shipped) FROM o GROUP BY 1 ORDER BY 1`
	case "product":
		sql = base + ` SELECT COALESCE(pr.id, 'DELIVERY_CHARGE'), COALESCE(pr.name, ''), COALESCE(pr.code, ''), COUNT(DISTINCT sl."saleId"), SUM(sl.quantity), SUM(sl.amount), 0, 0,
			COALESCE(SUM((SELECT SUM(dl.quantity) FROM "DeliveryLine" dl JOIN "Delivery" dv ON dv.id = dl."deliveryId" AND dv.status <> 'CANCELLED' WHERE dl."saleLineId" = sl.id)), 0)
			FROM "SaleLine" sl LEFT JOIN "Product" pr ON pr.id = sl."productId" WHERE sl."saleId" IN (SELECT id FROM o)
			GROUP BY 1, 2, 3, pr."sortOrder" ORDER BY pr."sortOrder" NULLS LAST, 3`
	}
	rows, err := q.Query(ctx, sql, b.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &reportOut{GroupBy: rq.GroupBy, Period: rq.Period, Rows: []reportRow{}}
	out.Range.From, out.Range.To = rq.From, rq.To
	for rows.Next() {
		var r reportRow
		if err := rows.Scan(&r.Key, &r.Label, &r.Code, &r.Orders, &r.Quantity, &r.Amount, &r.Paid, &r.Outstanding, &r.Delivered); err != nil {
			return nil, err
		}
		switch rq.GroupBy {
		case "status":
			r.Label = statusWord(lang, r.Key)
		case "period":
			r.Label = periodLabel(r.Key, rq.Period)
		case "product":
			if r.Key == "DELIVERY_CHARGE" {
				r.Label = i18n.T(lang, "sales.delivery_charge")
			}
		}
		out.Totals.Quantity += r.Quantity
		out.Totals.Amount += r.Amount
		out.Totals.Paid += r.Paid
		out.Totals.Outstanding += r.Outstanding
		out.Totals.Delivered += r.Delivered
		if rq.GroupBy != "product" {
			out.Totals.Orders += r.Orders
		}
		out.Rows = append(out.Rows, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if rq.GroupBy == "product" { // money totals per order, not per line
		if err := q.QueryRow(ctx, base+` SELECT COUNT(*), COALESCE(SUM(paid),0), COALESCE(SUM(outstanding),0) FROM o`, b.args...).Scan(&out.Totals.Orders, &out.Totals.Paid, &out.Totals.Outstanding); err != nil {
			return nil, err
		}
	}
	out.Totals.Label = "total"
	return out, nil
}

func periodLabel(iso, period string) string {
	t, err := dates.ParseISO(iso)
	if err != nil {
		return iso
	}
	switch period {
	case "month":
		return t.Format("01/2006")
	case "week":
		return dates.FormatRange(t, t.AddDate(0, 0, 6))
	}
	return dates.FormatDate(t)
}

func (h *handler) ordersReport(c *fiber.Ctx) error {
	r := newRq(c)
	rq, err := parseReportQuery(c, r.u)
	if err != nil {
		return err
	}
	out, err := h.s.buildOrdersReport(r.ctx, h.d.Pool, r.lang, rq)
	if err != nil {
		return err
	}
	return c.JSON(out)
}

func (h *handler) pdfOrdersReport(c *fiber.Ctx) error {
	r := newRq(c)
	rq, err := parseReportQuery(c, r.u)
	if err != nil {
		return err
	}
	out, err := h.s.buildOrdersReport(r.ctx, h.d.Pool, r.lang, rq)
	if err != nil {
		return err
	}
	T := r.t
	from, to := dates.MustParseISO(rq.From), dates.MustParseISO(rq.To)
	rep, err := pdf.New(c, h.d, pdf.Options{Title: T("sales.pdf.orders_report.title"), Subtitle: T("sales.report.by." + rq.GroupBy),
		Meta: []pdf.Meta{{Label: T("sales.pdf.period"), Value: dates.FormatRange(from, to)}}, Landscape: true})
	if err != nil {
		return err
	}
	rep.KpiRow([]pdf.Kpi{{Label: T("sales.report.orders"), Value: format.Int(float64(out.Totals.Orders))}, {Label: T("sales.pdf.amount_rwf"), Value: format.RWF(float64(out.Totals.Amount))},
		{Label: T("sales.report.paid"), Value: format.RWF(float64(out.Totals.Paid))}, {Label: T("sales.report.outstanding"), Value: format.RWF(float64(out.Totals.Outstanding))}})
	var data [][]any
	for _, x := range out.Rows {
		label := x.Label
		if x.Code != "" {
			label = x.Code + " — " + x.Label
		}
		data = append(data, []any{label, fmtI(x.Orders), fmtI(x.Quantity), fmtI(x.Delivered), fmtI(x.Amount), fmtI(x.Paid), fmtI(x.Outstanding)})
	}
	rep.Section(T("sales.report.by."+rq.GroupBy)).Table([]pdf.Column{{Header: T("sales.report.group." + rq.GroupBy), Width: 3},
		{Header: T("sales.report.orders"), Align: pdf.Right}, {Header: T("sales.pdf.quantity"), Align: pdf.Right}, {Header: T("sales.pdf.delivered"), Align: pdf.Right},
		{Header: T("sales.pdf.amount_rwf"), Width: 1.3, Align: pdf.Right}, {Header: T("sales.report.paid"), Width: 1.2, Align: pdf.Right}, {Header: T("sales.report.outstanding"), Width: 1.2, Align: pdf.Right}},
		data, []any{T("sales.pdf.total"), fmtI(out.Totals.Orders), fmtI(out.Totals.Quantity), fmtI(out.Totals.Delivered), fmtI(out.Totals.Amount), fmtI(out.Totals.Paid), fmtI(out.Totals.Outstanding)})
	return pdf.Send(c, rep, fmt.Sprintf("Orders-report_%s_%s_%s.pdf", rq.GroupBy, rq.From, rq.To), download(c))
}

// ───────────────────────────── pending deliveries (DEL-06) ─────────────────────────────

type pendingRow struct {
	SaleID        string  `json:"saleId"`
	Number        string  `json:"number"`
	Status        string  `json:"status"`
	Date          string  `json:"date"`
	RequestedDate *string `json:"requestedDate"`
	Late          bool    `json:"late"`
	Customer      idName  `json:"customer"`
	Plant         ref     `json:"plant"`
	Transport     string  `json:"transportMode"`
	Destination   *string `json:"destination"`
	Ordered       int64   `json:"ordered"`
	Shipped       int64   `json:"shipped"`
	Remaining     int64   `json:"remaining"`
	OpenNotes     int64   `json:"openNotes"` // dispatched, not yet confirmed delivered
}

func (s *service) pendingDeliveries(ctx context.Context, q db.DBTX, plantID, customerID, transport, search string) ([]pendingRow, error) {
	b := &qb{}
	conds := []string{`sa."orderStatus" = ANY(` + b.arg([]string{StConfirmed, StInProduction, StFromStock, StReady, StDispatched}) + `)`}
	if plantID != "" {
		conds = append(conds, `sa."plantId" = `+b.arg(plantID))
	}
	if customerID != "" {
		conds = append(conds, `sa."customerId" = `+b.arg(customerID))
	}
	if transport != "" {
		conds = append(conds, `sa."transportMode" = `+b.arg(transport))
	}
	if search != "" {
		l := b.arg(likeArg(search))
		conds = append(conds, fmt.Sprintf(`(sa.number ILIKE %[1]s OR c.name ILIKE %[1]s OR sa."deliverySite" ILIKE %[1]s OR sa."deliveryAddress" ILIKE %[1]s)`, l))
	}
	rows, err := q.Query(ctx, `SELECT sa.id, sa.number, sa."orderStatus", sa.date, sa."requestedDate", c.id, c.name, pl.id, pl.code, pl.name, sa."transportMode",
		NULLIF(concat_ws(', ', NULLIF(sa."deliverySite", ''), NULLIF(sa."deliveryAddress", '')), ''),
		COALESCE((SELECT SUM(quantity) FROM "SaleLine" WHERE "saleId" = sa.id AND kind = 'PRODUCT'), 0)::bigint,
		COALESCE((SELECT SUM(dl.quantity) FROM "DeliveryLine" dl JOIN "Delivery" d ON d.id = dl."deliveryId" WHERE d."saleId" = sa.id AND d.status <> 'CANCELLED'), 0)::bigint,
		(SELECT COUNT(*) FROM "Delivery" d WHERE d."saleId" = sa.id AND d.status = 'DISPATCHED')
		FROM "Sale" sa JOIN "Customer" c ON c.id = sa."customerId" JOIN "Plant" pl ON pl.id = sa."plantId"
		WHERE `+and(conds)+` ORDER BY sa."requestedDate" NULLS LAST, sa.date, sa.number LIMIT 2000`, b.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	today := dates.TodayISO()
	out := []pendingRow{}
	for rows.Next() {
		var p pendingRow
		var d time.Time
		var req *time.Time
		if err := rows.Scan(&p.SaleID, &p.Number, &p.Status, &d, &req, &p.Customer.ID, &p.Customer.Name, &p.Plant.ID, &p.Plant.Code, &p.Plant.Name, &p.Transport,
			&p.Destination, &p.Ordered, &p.Shipped, &p.OpenNotes); err != nil {
			return nil, err
		}
		p.Date = dates.ToISO(d)
		p.Remaining = max(0, p.Ordered-p.Shipped)
		if req != nil {
			s := dates.ToISO(*req)
			p.RequestedDate = &s
			p.Late = s < today && p.Remaining > 0
		}
		if p.Remaining > 0 || p.OpenNotes > 0 {
			out = append(out, p)
		}
	}
	return out, rows.Err()
}

func pendingParams(c *fiber.Ctx, u *auth.User) (plant, customer, transport, search string, err error) {
	q := httpx.Query(c)
	transport = queryEnum(q, "transportMode", TransportClient, TransportBTN)
	customer = q.OptStr("customerId").Trim().Max(40).Get()
	search = q.OptStr("q").Trim().Max(80).Get()
	plant = auth.PlantFilter(u, q.OptStr("plantId").Get())
	err = q.Err()
	return
}

func (h *handler) pendingDeliveriesH(c *fiber.Ctx) error {
	r := newRq(c)
	plant, cust, tr, search, err := pendingParams(c, r.u)
	if err != nil {
		return err
	}
	items, err := h.s.pendingDeliveries(r.ctx, h.d.Pool, plant, cust, tr, search)
	if err != nil {
		return err
	}
	var rem int64
	for _, i := range items {
		rem += i.Remaining
	}
	return c.JSON(fiber.Map{"items": items, "total": len(items), "remaining": rem})
}

func (h *handler) pdfPendingDeliveries(c *fiber.Ctx) error {
	r := newRq(c)
	plant, cust, tr, search, err := pendingParams(c, r.u)
	if err != nil {
		return err
	}
	items, err := h.s.pendingDeliveries(r.ctx, h.d.Pool, plant, cust, tr, search)
	if err != nil {
		return err
	}
	T := r.t
	rep, err := pdf.New(c, h.d, pdf.Options{Title: T("sales.pdf.pending.title"), Subtitle: T("sales.pdf.pending.subtitle"),
		Meta: []pdf.Meta{{Label: T("sales.pdf.date"), Value: dates.FormatDate(dates.MustParseISO(dates.TodayISO()))}}, Landscape: true})
	if err != nil {
		return err
	}
	var data [][]any
	var rem int64
	for _, p := range items {
		req := "—"
		if p.RequestedDate != nil {
			req = dates.FormatDate(dates.MustParseISO(*p.RequestedDate))
			if p.Late {
				req += " !"
			}
		}
		rem += p.Remaining
		data = append(data, []any{p.Number, p.Customer.Name, p.Plant.Code, statusWord(r.lang, p.Status), T("sales.transport." + p.Transport), deref(p.Destination), req,
			fmtI(p.Ordered), fmtI(p.Shipped), fmtI(p.Remaining), fmtI(p.OpenNotes)})
	}
	rep.Table([]pdf.Column{{Header: T("sales.pdf.sales_order")}, {Header: T("sales.pdf.customer"), Width: 2}, {Header: T("sales.pdf.plant"), Width: 0.7},
		{Header: T("sales.pdf.status"), Width: 1.2}, {Header: T("sales.pdf.transport"), Width: 1}, {Header: T("sales.pdf.destination"), Width: 2},
		{Header: T("sales.pdf.requested"), Width: 1}, {Header: T("sales.pdf.quantity"), Align: pdf.Right, Width: 0.8}, {Header: T("sales.pdf.shipped"), Align: pdf.Right, Width: 0.8},
		{Header: T("sales.pdf.remaining"), Align: pdf.Right, Width: 0.8}, {Header: T("sales.pdf.open_notes"), Align: pdf.Right, Width: 0.7}},
		data, []any{T("sales.pdf.total"), "", "", "", "", "", "", "", "", fmtI(rem), ""})
	return pdf.Send(c, rep, "Pending-deliveries_"+dates.TodayISO()+".pdf", download(c))
}

// pdfDeliveries: the deliveries report (DEL-06) with the list filters (client, order, date, arrangement).
func (h *handler) pdfDeliveries(c *fiber.Ctx) error {
	r := newRq(c)
	q := httpx.Query(c)
	from, to := optDate(q, "from"), optDate(q, "to")
	f := deliveryFilters{PlantID: auth.PlantFilter(r.u, q.OptStr("plantId").Get()), CustomerID: q.OptStr("customerId").Trim().Max(40).Get(), SaleID: q.OptStr("saleId").Trim().Max(40).Get()}
	f.Status = queryEnum(q, "status", "DISPATCHED", "DELIVERED", "CANCELLED")
	f.Transport = queryEnum(q, "transportMode", TransportClient, TransportBTN)
	f.Q = q.OptStr("q").Trim().Max(80).Get()
	if err := q.Err(); err != nil {
		return err
	}
	rng := dates.ResolveRange(from, to)
	f.From, f.To = rng.FromISO, rng.ToISO
	out, err := h.s.listDeliveries(r.ctx, f, httpx.Pager{Page: 1, PageSize: 5000, Limit: 5000})
	if err != nil {
		return err
	}
	T := r.t
	rep, err := pdf.New(c, h.d, pdf.Options{Title: T("sales.pdf.deliveries.title"), Subtitle: T("sales.pdf.deliveries.subtitle"),
		Meta: []pdf.Meta{{Label: T("sales.pdf.period"), Value: dates.FormatRange(rng.From, rng.To)}}, Landscape: true})
	if err != nil {
		return err
	}
	byMode := map[string]int64{}
	var data [][]any
	for _, d := range out.Items {
		if d.Status != "CANCELLED" {
			byMode[d.Transport] += d.TotalQty
		}
		at := "—"
		if d.DeliveredAt != nil {
			at = dates.FormatDateTime(d.DeliveredAt.Time)
		}
		data = append(data, []any{d.DNNumber, dates.FormatDate(dates.MustParseISO(d.Date)), d.Sale.Number, d.Customer.Name, d.Plant.Code, T("sales.transport." + d.Transport),
			deref(d.Destination), fmtI(d.TotalQty), T("sales.dstatus." + d.Status), deref(d.ReceivedBy), at})
	}
	rep.KpiRow([]pdf.Kpi{{Label: T("sales.pdf.deliveries.count"), Value: format.Int(float64(out.Total))}, {Label: T("sales.pdf.quantity"), Value: format.Int(float64(out.RangeTotals.Quantity))},
		{Label: T("sales.transport.BTN"), Value: format.Int(float64(byMode[TransportBTN]))}, {Label: T("sales.transport.CLIENT"), Value: format.Int(float64(byMode[TransportClient]))}})
	rep.Table([]pdf.Column{{Header: T("sales.pdf.dn_number")}, {Header: T("sales.pdf.date"), Width: 0.9}, {Header: T("sales.pdf.sales_order")}, {Header: T("sales.pdf.customer"), Width: 2},
		{Header: T("sales.pdf.plant"), Width: 0.6}, {Header: T("sales.pdf.transport"), Width: 1}, {Header: T("sales.pdf.destination"), Width: 1.8},
		{Header: T("sales.pdf.quantity"), Align: pdf.Right, Width: 0.8}, {Header: T("sales.pdf.status"), Width: 0.9}, {Header: T("sales.pdf.received_by"), Width: 1.3}, {Header: T("sales.pdf.delivered_at"), Width: 1.2}},
		data, []any{T("sales.pdf.total"), "", "", "", "", "", "", fmtI(out.RangeTotals.Quantity), "", "", ""})
	return pdf.Send(c, rep, fmt.Sprintf("Deliveries_%s_%s.pdf", rng.FromISO, rng.ToISO), download(c))
}
