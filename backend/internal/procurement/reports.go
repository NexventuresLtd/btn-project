package procurement

// Procurement and expenditure by supplier, category, location and period (PRC-08) — screen JSON, PDF (pdf.go) and
// CSV/Excel/JSON through the export framework; dashboard summary for P7 (GET /summary).

import (
	"context"
	"encoding/json"
	"sort"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/export"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/stock"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Report groupings.
var spendGroups = []string{"supplier", "category", "month", "location"}

type spendFilter struct {
	from, to, plantID, supplierID, groupBy string
}

// SpendRow is one group of the spend report. Values are whole RWF: ordered = qty × price on sent orders,
// received = received qty × price, outstanding = still expected (open lines only, never negative), invoiced = supplier invoices (allocated to categories by line value).
type SpendRow struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Orders      int    `json:"orders"`
	Lines       int    `json:"lines"`
	Ordered     int64  `json:"ordered"`
	Received    int64  `json:"received"`
	Invoiced    int64  `json:"invoiced"`
	Outstanding int64  `json:"outstanding"`
}

// SpendTotals are the report totals (orders counted once).
type SpendTotals struct {
	Orders      int   `json:"orders"`
	Lines       int   `json:"lines"`
	Ordered     int64 `json:"ordered"`
	Received    int64 `json:"received"`
	Invoiced    int64 `json:"invoiced"`
	Outstanding int64 `json:"outstanding"`
	Suppliers   int   `json:"suppliers"`
}

const spendBase = `WITH po AS (
	SELECT o.* FROM purchase_order o WHERE o.status NOT IN ('DRAFT','CANCELLED')
	  AND ($1 = '' OR o.order_date >= $1::date) AND ($2 = '' OR o.order_date <= $2::date)
	  AND ($3 = '' OR o.delivery_plant_id = $3) AND ($4 = '' OR o.supplier_id = $4)
), inv AS (
	SELECT po_id, SUM(amount) AS amt FROM purchase_invoice WHERE po_id IN (SELECT id FROM po) GROUP BY po_id
), ln AS (
	SELECT l.id, l.po_id, COALESCE(l.category, '') AS category, round(l.quantity * l.unit_price) AS ordered, round(l.received_qty * l.unit_price) AS received,
		CASE WHEN l.closed THEN 0 ELSE GREATEST(round((l.quantity - l.received_qty) * l.unit_price), 0) END AS outstanding
	FROM purchase_order_line l WHERE l.po_id IN (SELECT id FROM po)
)`

func spendRows(ctx context.Context, q db.DBTX, lang i18n.Lang, f spendFilter, names map[string]string) ([]SpendRow, error) {
	key, label := "s.id", "s.code || ' · ' || s.name"
	switch f.groupBy {
	case "category":
		key, label = "ln.category", "ln.category"
	case "month":
		key, label = "to_char(po.order_date, 'YYYY-MM')", "to_char(po.order_date, 'YYYY-MM')"
	case "location":
		key, label = "p.id", "p.code || ' · ' || p.name"
	}
	rows, err := q.Query(ctx, spendBase+` SELECT `+key+`, min(`+label+`), count(DISTINCT po.id), count(ln.id), COALESCE(SUM(ln.ordered),0)::bigint, COALESCE(SUM(ln.received),0)::bigint, COALESCE(SUM(ln.outstanding),0)::bigint,
		COALESCE(SUM(CASE WHEN po.total > 0 THEN COALESCE(inv.amt, 0) * ln.ordered / po.total ELSE 0 END), 0)::bigint
		FROM ln JOIN po ON po.id = ln.po_id LEFT JOIN inv ON inv.po_id = po.id JOIN supplier s ON s.id = po.supplier_id JOIN "Plant" p ON p.id = po.delivery_plant_id
		GROUP BY `+key+` ORDER BY 5 DESC`, f.from, f.to, f.plantID, f.supplierID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SpendRow{}
	for rows.Next() {
		var r SpendRow
		if err := rows.Scan(&r.Key, &r.Label, &r.Orders, &r.Lines, &r.Ordered, &r.Received, &r.Outstanding, &r.Invoiced); err != nil {
			return nil, err
		}
		switch f.groupBy {
		case "category":
			if r.Key == "" {
				r.Label = i18n.T(lang, "procurement.report.uncategorised")
			} else {
				k := r.Key
				r.Label = nameOf(names, "", &k)
			}
		case "month":
			if t, err := dates.ParseISO(r.Key + "-01"); err == nil {
				r.Label = t.Format("01/2006")
			}
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if f.groupBy == "month" {
		sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	}
	return out, nil
}

func spendTotals(ctx context.Context, q db.DBTX, f spendFilter) (SpendTotals, error) {
	var t SpendTotals
	err := q.QueryRow(ctx, spendBase+` SELECT count(DISTINCT po.id), count(ln.id), COALESCE(SUM(ln.ordered),0)::bigint, COALESCE(SUM(ln.received),0)::bigint, COALESCE(SUM(ln.outstanding),0)::bigint,
		(SELECT COALESCE(SUM(amt),0) FROM inv)::bigint, count(DISTINCT po.supplier_id) FROM ln JOIN po ON po.id = ln.po_id`, f.from, f.to, f.plantID, f.supplierID).
		Scan(&t.Orders, &t.Lines, &t.Ordered, &t.Received, &t.Outstanding, &t.Invoiced, &t.Suppliers)
	return t, err
}

// SpendReport is GET /reports/spend.
type SpendReport struct {
	From       string      `json:"from"`
	To         string      `json:"to"`
	GroupBy    string      `json:"groupBy"`
	Rows       []SpendRow  `json:"rows"`
	Totals     SpendTotals `json:"totals"`
	ByCategory []SpendRow  `json:"byCategory"`
	BySupplier []SpendRow  `json:"bySupplier"`
}

func parseSpend(c *fiber.Ctx) (spendFilter, dates.Range, error) {
	v := httpx.Query(c)
	from, to := v.OptStr("from").Trim().Get(), v.OptStr("to").Trim().Get()
	if (from != "" && !dates.IsValidISO(from)) || (to != "" && !dates.IsValidISO(to)) {
		v.Fail("from", "procurement.v.date")
	}
	g := v.OptStr("groupBy").Trim().Default("supplier").OneOf(spendGroups, "procurement.v.group_by").Get()
	sup := v.OptStr("supplierId").Trim().Max(64).Get()
	plant := v.OptStr("plantId").Trim().Max(64).Get()
	if err := v.Err(); err != nil {
		return spendFilter{}, dates.Range{}, err
	}
	r := dates.ResolveRange(from, to)
	if r.FromISO > r.ToISO {
		return spendFilter{}, r, badField(httpx.Lang(c), "to", "procurement.v.range")
	}
	return spendFilter{from: r.FromISO, to: r.ToISO, plantID: auth.PlantFilter(auth.MustUser(c), plant), supplierID: sup, groupBy: g}, r, nil
}

func buildSpend(ctx context.Context, q db.DBTX, lang i18n.Lang, f spendFilter) (*SpendReport, error) {
	names, err := refNames(ctx, q, lang, categoryKinds...)
	if err != nil {
		return nil, err
	}
	rep := &SpendReport{From: f.from, To: f.to, GroupBy: f.groupBy}
	if rep.Rows, err = spendRows(ctx, q, lang, f, names); err != nil {
		return nil, err
	}
	if rep.Totals, err = spendTotals(ctx, q, f); err != nil {
		return nil, err
	}
	cf := f
	cf.groupBy = "category"
	if rep.ByCategory, err = spendRows(ctx, q, lang, cf, names); err != nil {
		return nil, err
	}
	sf := f
	sf.groupBy = "supplier"
	if rep.BySupplier, err = spendRows(ctx, q, lang, sf, names); err != nil {
		return nil, err
	}
	return rep, nil
}

func (h *handler) spendReport(c *fiber.Ctx) error {
	f, _, err := parseSpend(c)
	if err != nil {
		return err
	}
	rep, err := buildSpend(httpx.Ctx(c), h.d.Pool, httpx.Lang(c), f)
	if err != nil {
		return err
	}
	return httpx.JSONCached(c, rep, "private, no-cache")
}

// ───────────────────────────── dashboard summary (P7) ─────────────────────────────

// CountValue is a count with its value in RWF.
type CountValue struct {
	Count int   `json:"count"`
	Value int64 `json:"value"`
}

// Summary is GET /summary.
type Summary struct {
	PendingRequests    CountValue `json:"pendingRequests"`
	AwaitingMyApproval CountValue `json:"awaitingMyApproval"`
	ApprovedNotOrdered CountValue `json:"approvedNotOrdered"`
	OpenOrders         CountValue `json:"openOrders"`
	AwaitingReceipt    struct {
		Orders int `json:"orders"`
		Lines  int `json:"lines"`
	} `json:"awaitingReceipt"`
	OverdueOrders int `json:"overdueOrders"`
	Exceptions30d int `json:"exceptions30d"`
	Suppliers     struct {
		Active int `json:"active"`
		Total  int `json:"total"`
	} `json:"suppliers"`
	SpendMonth    int64        `json:"spendMonth"`
	SpendYear     int64        `json:"spendYear"`
	LowStockItems int          `json:"lowStockItems"`
	Recent        []RequestRow `json:"recent"`
}

func (h *handler) summary(c *fiber.Ctx) error {
	ctx, u := httpx.Ctx(c), auth.MustUser(c)
	plant := auth.PlantFilter(u, c.Query("plantId"))
	key := "sum:" + u.ID + ":" + plant + ":" + dates.TodayISO()
	body, err := summaryCache.GetOrLoad(ctx, key, func(ctx context.Context) ([]byte, error) {
		s, err := buildSummary(ctx, h.d.Pool, u, plant)
		if err != nil {
			return nil, err
		}
		return json.Marshal(s)
	}, cacheTag)
	if err != nil {
		return err
	}
	return httpx.BytesCached(c, body, "application/json; charset=utf-8", "private, no-cache")
}

func buildSummary(ctx context.Context, q db.DBTX, u *auth.User, plant string) (*Summary, error) {
	s := &Summary{Recent: []RequestRow{}}
	today := dates.TodayISO()
	monthStart := today[:8] + "01"
	yearStart := today[:5] + "01-01"
	err := q.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE status = 'SUBMITTED'), COALESCE(SUM(estimated_total) FILTER (WHERE status = 'SUBMITTED'), 0),
		count(*) FILTER (WHERE status = 'APPROVED'), COALESCE(SUM(estimated_total) FILTER (WHERE status = 'APPROVED'), 0)
		FROM procurement_request WHERE ($1 = '' OR plant_id = $1)`, plant).
		Scan(&s.PendingRequests.Count, &s.PendingRequests.Value, &s.ApprovedNotOrdered.Count, &s.ApprovedNotOrdered.Value)
	if err != nil {
		return nil, err
	}
	if u.Can(permApprove) {
		perms := auth.PermissionsFor(u.Role)
		if err := q.QueryRow(ctx, `SELECT count(*), COALESCE(SUM(r.estimated_total), 0) FROM procurement_request r WHERE ($4 = '' OR r.plant_id = $4) AND `+
			sprintfAwait("$1", "$2", "$3"), u.ID, string(u.Role), perms, plant).Scan(&s.AwaitingMyApproval.Count, &s.AwaitingMyApproval.Value); err != nil {
			return nil, err
		}
	}
	if err := q.QueryRow(ctx, `SELECT count(*), COALESCE(SUM(o.total), 0),
		count(*) FILTER (WHERE o.expected_date < $2::date),
		COALESCE((SELECT count(*) FROM purchase_order_line l JOIN purchase_order x ON x.id = l.po_id WHERE x.status IN ('ORDERED','PARTIALLY_RECEIVED')
			AND ($1 = '' OR x.delivery_plant_id = $1) AND NOT l.closed AND l.received_qty < l.quantity), 0)
		FROM purchase_order o WHERE o.status IN ('ORDERED','PARTIALLY_RECEIVED') AND ($1 = '' OR o.delivery_plant_id = $1)`, plant, today).
		Scan(&s.OpenOrders.Count, &s.OpenOrders.Value, &s.OverdueOrders, &s.AwaitingReceipt.Lines); err != nil {
		return nil, err
	}
	s.AwaitingReceipt.Orders = s.OpenOrders.Count
	if err := q.QueryRow(ctx, `SELECT count(*) FROM goods_receipt WHERE has_exception AND date >= $2::date - 30 AND ($1 = '' OR plant_id = $1)`, plant, today).Scan(&s.Exceptions30d); err != nil {
		return nil, err
	}
	if err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'ACTIVE'), count(*) FROM supplier`).Scan(&s.Suppliers.Active, &s.Suppliers.Total); err != nil {
		return nil, err
	}
	if err := q.QueryRow(ctx, `SELECT COALESCE(SUM(total) FILTER (WHERE order_date >= $2::date), 0), COALESCE(SUM(total), 0) FROM purchase_order
		WHERE status NOT IN ('DRAFT','CANCELLED') AND order_date >= $3::date AND ($1 = '' OR delivery_plant_id = $1)`, plant, monthStart, yearStart).Scan(&s.SpendMonth, &s.SpendYear); err != nil {
		return nil, err
	}
	// only the count is shown: the shared, briefly cached low-stock list (NFR-P1) instead of the full proposal
	var low []stock.LowRow
	if pool, ok := q.(*pgxpool.Pool); ok {
		low, err = stock.LowStockCached(ctx, pool, plant, "")
	} else {
		low, err = stock.LowStock(ctx, q, plant, "")
	}
	if err != nil {
		return nil, err
	}
	s.LowStockItems = len(low)
	rows, err := q.Query(ctx, `SELECT r.id, r.number, r.status, p.id, p.code, p.name, p.kind, to_char(r.required_date,'YYYY-MM-DD'), r.estimated_total, r.source, u.id, u.name, r.created_at
		FROM procurement_request r JOIN "Plant" p ON p.id = r.plant_id JOIN "User" u ON u.id = r.requested_by_id
		WHERE ($1 = '' OR r.plant_id = $1) ORDER BY r.created_at DESC LIMIT 5`, plant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r RequestRow
		if err := rows.Scan(&r.ID, &r.Number, &r.Status, &r.Plant.ID, &r.Plant.Code, &r.Plant.Name, &r.Plant.Kind, &r.RequiredDate, &r.EstimatedTotal, &r.Source,
			&r.RequestedBy.ID, &r.RequestedBy.Name, &r.CreatedAt); err != nil {
			return nil, err
		}
		s.Recent = append(s.Recent, r)
	}
	return s, rows.Err()
}

// ───────────────────────────── exports (RPT-03 / PRC-08) ─────────────────────────────

func init() {
	export.Register(export.Report{Path: "/procurement/reports/spend", Name: "procurement-spend", Title: "procurement.export.spend",
		Build: func(b *export.Builder, j export.J) {
			b.Name("procurement-spend-by-" + j.S("groupBy"))
			b.Filter("procurement.export.group_by", b.T("procurement.report.group."+j.S("groupBy")))
			b.Columns(export.Column{Key: "label", Header: "procurement.report.group." + j.S("groupBy")},
				export.Column{Key: "orders", Header: "procurement.col.orders", Kind: export.Int},
				export.Column{Key: "lines", Header: "procurement.col.lines", Kind: export.Int},
				export.Column{Key: "ordered", Header: "procurement.col.ordered_value", Kind: export.Money},
				export.Column{Key: "received", Header: "procurement.col.received_value", Kind: export.Money},
				export.Column{Key: "outstanding", Header: "procurement.col.outstanding_value", Kind: export.Money},
				export.Column{Key: "invoiced", Header: "procurement.col.invoiced", Kind: export.Money})
			for _, r := range j.Arr("rows") {
				b.Row(r.S("label"), r.V("orders"), r.V("lines"), r.V("ordered"), r.V("received"), r.V("outstanding"), r.V("invoiced"))
			}
			t := j.Get("totals")
			b.Row(b.T("procurement.col.total"), t.V("orders"), t.V("lines"), t.V("ordered"), t.V("received"), t.V("outstanding"), t.V("invoiced"))
		}})
	export.Register(export.Report{Path: "/procurement/requests", Name: "procurement-requests", Title: "procurement.export.requests",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(export.Column{Key: "number", Header: "procurement.col.number"}, export.Column{Key: "createdAt", Header: "procurement.col.created", Kind: export.DateTime},
				export.Column{Key: "status", Header: "procurement.col.status"}, export.Column{Key: "plant", Header: "procurement.col.location"},
				export.Column{Key: "requiredDate", Header: "procurement.col.required_date", Kind: export.Date}, export.Column{Key: "items", Header: "procurement.col.items"},
				export.Column{Key: "lines", Header: "procurement.col.lines", Kind: export.Int}, export.Column{Key: "estimatedTotal", Header: "procurement.col.estimated", Kind: export.Money},
				export.Column{Key: "requestedBy", Header: "procurement.col.requested_by"}, export.Column{Key: "pending", Header: "procurement.col.pending_level"})
			for _, r := range j.Arr("items") {
				b.Row(r.S("number"), r.V("createdAt"), b.T("procurement.status."+r.S("status")), r.S("plant.code"), r.V("requiredDate"), r.S("items"), r.V("lineCount"),
					r.V("estimatedTotal"), r.S("requestedBy.name"), r.S("pendingLabel"))
			}
		}})
	export.Register(export.Report{Path: "/procurement/orders", Name: "purchase-orders", Title: "procurement.export.orders", Build: buildOrderExport})
	export.Register(export.Report{Path: "/procurement/suppliers/:id/orders", Name: "supplier-purchase-history", Title: "procurement.export.supplier_history", Build: buildOrderExport})
	export.Register(export.Report{Path: "/procurement/suppliers", Name: "suppliers", Title: "procurement.export.suppliers",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(export.Column{Key: "code", Header: "procurement.col.code"}, export.Column{Key: "name", Header: "procurement.col.supplier"},
				export.Column{Key: "tin", Header: "procurement.col.tin"}, export.Column{Key: "status", Header: "procurement.col.status"},
				export.Column{Key: "contact", Header: "procurement.col.contact"}, export.Column{Key: "phone", Header: "procurement.col.phone"},
				export.Column{Key: "email", Header: "procurement.col.email"}, export.Column{Key: "categories", Header: "procurement.col.categories"},
				export.Column{Key: "paymentTerm", Header: "procurement.col.payment_term"}, export.Column{Key: "orders", Header: "procurement.col.orders", Kind: export.Int},
				export.Column{Key: "ordered", Header: "procurement.col.ordered_value", Kind: export.Money}, export.Column{Key: "lastOrder", Header: "procurement.col.last_order", Kind: export.Date})
			for _, r := range j.Arr("items") {
				cats := []string{}
				for _, c := range r.Arr("categoryNames") {
					cats = append(cats, c.S(""))
				}
				b.Row(r.S("code"), r.S("name"), r.S("tin"), b.T("procurement.supplierStatus."+r.S("status")), r.S("contactName"), r.S("phone"), r.S("email"),
					joinComma(cats), r.S("paymentTermName"), r.V("stats.orders"), r.V("stats.orderedValue"), r.V("stats.lastOrderDate"))
			}
		}})
}

func buildOrderExport(b *export.Builder, j export.J) {
	b.Columns(export.Column{Key: "number", Header: "procurement.col.number"}, export.Column{Key: "orderDate", Header: "procurement.col.order_date", Kind: export.Date},
		export.Column{Key: "status", Header: "procurement.col.status"}, export.Column{Key: "supplier", Header: "procurement.col.supplier"},
		export.Column{Key: "plant", Header: "procurement.col.delivery_location"}, export.Column{Key: "expected", Header: "procurement.col.expected_date", Kind: export.Date},
		export.Column{Key: "items", Header: "procurement.col.items"}, export.Column{Key: "requests", Header: "procurement.col.requests"},
		export.Column{Key: "total", Header: "procurement.col.total", Kind: export.Money}, export.Column{Key: "received", Header: "procurement.col.received_value", Kind: export.Money},
		export.Column{Key: "invoiced", Header: "procurement.col.invoiced", Kind: export.Money})
	for _, r := range j.Arr("items") {
		b.Row(r.S("number"), r.V("orderDate"), b.T("procurement.orderStatus."+r.S("status")), r.S("supplier.name"), r.S("deliveryPlant.code"), r.V("expectedDate"),
			r.S("items"), r.S("requests"), r.V("total"), r.V("receivedValue"), r.V("invoiced"))
	}
}

func joinComma(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}
