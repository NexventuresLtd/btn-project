// Package finlink links money records (expenses, income) to any other record of the system and answers the
// "Money" view of a record: every income and expense that belongs to it (docs/FINANCE_RECORDS_DESIGN.md §2–§3).
//
// A link is an explicit row in expense_link / income_link (entity type + id, many per record; removing stamps
// removed_at, nothing is deleted) or a typed relation (expense.purchase_order_id, payroll_period_id, float_id,
// supplier_id, income.sale_id, customer_id). Money(...) merges both.
package finlink

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
)

// Entity types a money record may be linked to.
const (
	TSale             = "sale"
	TCustomer         = "customer"
	TDelivery         = "delivery"
	TPurchaseRequest  = "purchase_request"
	TPurchaseOrder    = "purchase_order"
	TGoodsReceipt     = "goods_receipt"
	TSupplierInvoice  = "supplier_invoice"
	TSupplier         = "supplier"
	TPayrollPeriod    = "payroll_period"
	TWorker           = "worker"
	TProductionReport = "production_report"
	TProductionDay    = "production_day" // id = "<plantId>|YYYY-MM-DD"
	TTransfer         = "transfer"
	TStockCount       = "stock_count"
	TSite             = "site"
	TFloat            = "petty_cash_float"
	TReference        = "reference" // free text: asset, vehicle, EBM invoice … (no id)
)

// Types in picker order.
var Types = []string{TSale, TCustomer, TDelivery, TPurchaseRequest, TPurchaseOrder, TGoodsReceipt, TSupplierInvoice, TSupplier,
	TPayrollPeriod, TWorker, TProductionReport, TProductionDay, TTransfer, TStockCount, TSite, TFloat, TReference}

// KnownType reports whether t is a link entity type.
func KnownType(t string) bool {
	for _, x := range Types {
		if x == t {
			return true
		}
	}
	return false
}

// Target is a resolved record a money record can point to.
type Target struct {
	Type     string   `json:"type"`
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Sub      string   `json:"sub,omitempty"`
	Link     string   `json:"link,omitempty"`
	PlantIDs []string `json:"-"`
}

type spec struct {
	// one row: label, sub, plant ids (text[]) for $1 = id
	one string
	// search: id, label, sub, plants for $1 = like pattern, $2 = scoped plant ('' = all)
	search string
	link   func(id string, extra string) string
}

var specs = map[string]spec{
	TSale: {
		one: `SELECT s.number, COALESCE(c.name, ''), ARRAY[s."plantId"]::text[], '' FROM "Sale" s LEFT JOIN "Customer" c ON c.id = s."customerId" WHERE s.id = $1`,
		search: `SELECT s.id, s.number, COALESCE(c.name, ''), ARRAY[s."plantId"]::text[], '' FROM "Sale" s LEFT JOIN "Customer" c ON c.id = s."customerId"
			WHERE (s.number ILIKE $1 OR c.name ILIKE $1) AND ($2 = '' OR s."plantId" = $2) ORDER BY s.date DESC, s.number DESC LIMIT 20`,
		link: func(id, _ string) string { return "/sales/" + id }},
	TCustomer: {
		one:    `SELECT name, COALESCE(phone, ''), ARRAY[]::text[], '' FROM "Customer" WHERE id = $1`,
		search: `SELECT id, name, COALESCE(phone, ''), ARRAY[]::text[], '' FROM "Customer" WHERE (name ILIKE $1 OR phone ILIKE $1 OR tin ILIKE $1) AND $2::text = $2::text ORDER BY name LIMIT 20`,
		link:   func(id, _ string) string { return "/sales/customers/" + id }},
	TDelivery: {
		one: `SELECT d."dnNumber", COALESCE(s.number, ''), ARRAY[d."plantId"]::text[], d."saleId" FROM "Delivery" d LEFT JOIN "Sale" s ON s.id = d."saleId" WHERE d.id = $1`,
		search: `SELECT d.id, d."dnNumber", COALESCE(s.number, ''), ARRAY[d."plantId"]::text[], d."saleId" FROM "Delivery" d LEFT JOIN "Sale" s ON s.id = d."saleId"
			WHERE (d."dnNumber" ILIKE $1 OR s.number ILIKE $1) AND ($2 = '' OR d."plantId" = $2) ORDER BY d.date DESC LIMIT 20`,
		link: func(_, sale string) string { return "/sales/" + sale }},
	TPurchaseRequest: {
		one: `SELECT number, left(COALESCE(justification, ''), 80), ARRAY[plant_id]::text[], '' FROM procurement_request WHERE id = $1`,
		search: `SELECT id, number, left(COALESCE(justification, ''), 80), ARRAY[plant_id]::text[], '' FROM procurement_request
			WHERE (number ILIKE $1 OR justification ILIKE $1) AND ($2 = '' OR plant_id = $2) ORDER BY created_at DESC LIMIT 20`,
		link: func(id, _ string) string { return "/procurement/requests/" + id }},
	TPurchaseOrder: {
		one: `SELECT o.number, COALESCE(s.name, ''), ARRAY[o.delivery_plant_id]::text[], '' FROM purchase_order o LEFT JOIN supplier s ON s.id = o.supplier_id WHERE o.id = $1`,
		search: `SELECT o.id, o.number, COALESCE(s.name, ''), ARRAY[o.delivery_plant_id]::text[], '' FROM purchase_order o LEFT JOIN supplier s ON s.id = o.supplier_id
			WHERE (o.number ILIKE $1 OR s.name ILIKE $1) AND ($2 = '' OR o.delivery_plant_id = $2) ORDER BY o.order_date DESC, o.number DESC LIMIT 20`,
		link: func(id, _ string) string { return "/procurement/orders/" + id }},
	TGoodsReceipt: {
		one: `SELECT g.number, COALESCE(o.number, ''), ARRAY[g.plant_id]::text[], g.po_id FROM goods_receipt g LEFT JOIN purchase_order o ON o.id = g.po_id WHERE g.id = $1`,
		search: `SELECT g.id, g.number, COALESCE(o.number, ''), ARRAY[g.plant_id]::text[], g.po_id FROM goods_receipt g LEFT JOIN purchase_order o ON o.id = g.po_id
			WHERE (g.number ILIKE $1 OR o.number ILIKE $1 OR g.delivery_note ILIKE $1) AND ($2 = '' OR g.plant_id = $2) ORDER BY g.date DESC LIMIT 20`,
		link: func(_, po string) string { return "/procurement/orders/" + po }},
	TSupplierInvoice: {
		one: `SELECT i.invoice_number, COALESCE(o.number, ''), ARRAY[o.delivery_plant_id]::text[], i.po_id FROM purchase_invoice i LEFT JOIN purchase_order o ON o.id = i.po_id WHERE i.id = $1`,
		search: `SELECT i.id, i.invoice_number, COALESCE(o.number, ''), ARRAY[o.delivery_plant_id]::text[], i.po_id FROM purchase_invoice i LEFT JOIN purchase_order o ON o.id = i.po_id
			WHERE (i.invoice_number ILIKE $1 OR o.number ILIKE $1) AND ($2 = '' OR o.delivery_plant_id = $2) ORDER BY i.invoice_date DESC LIMIT 20`,
		link: func(_, po string) string { return "/procurement/orders/" + po }},
	TSupplier: {
		one:    `SELECT name, code, ARRAY[]::text[], '' FROM supplier WHERE id = $1`,
		search: `SELECT id, name, code, ARRAY[]::text[], '' FROM supplier WHERE (name ILIKE $1 OR code ILIKE $1 OR tin ILIKE $1) AND $2::text = $2::text ORDER BY name LIMIT 20`,
		link:   func(id, _ string) string { return "/procurement/suppliers/" + id }},
	TPayrollPeriod: {
		one: `SELECT p.number, COALESCE(pl.code, '') || ' ' || to_char(p."startDate", 'DD/MM/YYYY') || ' – ' || to_char(p."endDate", 'DD/MM/YYYY'), ARRAY[p."plantId"]::text[], ''
			FROM "PayrollPeriod" p LEFT JOIN "Plant" pl ON pl.id = p."plantId" WHERE p.id = $1`,
		search: `SELECT p.id, p.number, COALESCE(pl.code, '') || ' ' || to_char(p."startDate", 'DD/MM/YYYY') || ' – ' || to_char(p."endDate", 'DD/MM/YYYY'), ARRAY[p."plantId"]::text[], ''
			FROM "PayrollPeriod" p LEFT JOIN "Plant" pl ON pl.id = p."plantId" WHERE (p.number ILIKE $1 OR pl.code ILIKE $1) AND ($2 = '' OR p."plantId" = $2)
			ORDER BY p."startDate" DESC LIMIT 20`,
		link: func(id, _ string) string { return "/payroll/" + id }},
	TWorker: {
		one: `SELECT trim(w."firstName" || ' ' || w."lastName"), COALESCE(w."jobTitle", ''), ARRAY[w."plantId"]::text[], '' FROM "Worker" w WHERE w.id = $1`,
		search: `SELECT w.id, trim(w."firstName" || ' ' || w."lastName"), COALESCE(w."jobTitle", ''), ARRAY[w."plantId"]::text[], '' FROM "Worker" w
			WHERE (w."firstName" || ' ' || w."lastName" ILIKE $1 OR w."nationalId" ILIKE $1) AND ($2 = '' OR w."plantId" = $2) ORDER BY w."firstName", w."lastName" LIMIT 20`,
		link: func(id, _ string) string { return "/workforce/workers/" + id }},
	TProductionReport: {
		one: `SELECT r.number, COALESCE(pl.code, '') || ' ' || to_char(r.date, 'DD/MM/YYYY'), ARRAY[r."plantId"]::text[], '' FROM "ProductionReport" r
			LEFT JOIN "Plant" pl ON pl.id = r."plantId" WHERE r.id = $1`,
		search: `SELECT r.id, r.number, COALESCE(pl.code, '') || ' ' || to_char(r.date, 'DD/MM/YYYY'), ARRAY[r."plantId"]::text[], '' FROM "ProductionReport" r
			LEFT JOIN "Plant" pl ON pl.id = r."plantId" WHERE (r.number ILIKE $1 OR pl.code ILIKE $1) AND ($2 = '' OR r."plantId" = $2) ORDER BY r.date DESC LIMIT 20`,
		link: func(id, _ string) string { return "/production/" + id }},
	TTransfer: {
		one: `SELECT t.number, COALESCE(f.code, '') || ' → ' || COALESCE(d.code, ''), ARRAY[t.from_plant_id, t.to_plant_id]::text[], '' FROM stock_transfer t
			LEFT JOIN "Plant" f ON f.id = t.from_plant_id LEFT JOIN "Plant" d ON d.id = t.to_plant_id WHERE t.id = $1`,
		search: `SELECT t.id, t.number, COALESCE(f.code, '') || ' → ' || COALESCE(d.code, ''), ARRAY[t.from_plant_id, t.to_plant_id]::text[], '' FROM stock_transfer t
			LEFT JOIN "Plant" f ON f.id = t.from_plant_id LEFT JOIN "Plant" d ON d.id = t.to_plant_id
			WHERE t.number ILIKE $1 AND ($2 = '' OR t.from_plant_id = $2 OR t.to_plant_id = $2) ORDER BY t.created_at DESC LIMIT 20`,
		link: func(id, _ string) string { return "/transfers/" + id }},
	TStockCount: {
		one: `SELECT c.number, COALESCE(pl.code, '') || ' ' || to_char(c.count_date, 'DD/MM/YYYY'), ARRAY[c.plant_id]::text[], '' FROM stock_count c
			LEFT JOIN "Plant" pl ON pl.id = c.plant_id WHERE c.id = $1`,
		search: `SELECT c.id, c.number, COALESCE(pl.code, '') || ' ' || to_char(c.count_date, 'DD/MM/YYYY'), ARRAY[c.plant_id]::text[], '' FROM stock_count c
			LEFT JOIN "Plant" pl ON pl.id = c.plant_id WHERE (c.number ILIKE $1 OR pl.code ILIKE $1) AND ($2 = '' OR c.plant_id = $2) ORDER BY c.count_date DESC LIMIT 20`,
		link: func(id, _ string) string { return "/stock-counts/" + id }},
	TSite: {
		one:    `SELECT code, name, ARRAY[id]::text[], '' FROM "Plant" WHERE id = $1`,
		search: `SELECT id, code, name, ARRAY[id]::text[], '' FROM "Plant" WHERE (code ILIKE $1 OR name ILIKE $1) AND ($2 = '' OR id = $2) ORDER BY code LIMIT 20`,
		link:   func(_, _ string) string { return "/admin/sites" }},
	TFloat: {
		one: `SELECT code, name, ARRAY[plant_id]::text[], '' FROM petty_cash_float WHERE id = $1`,
		search: `SELECT id, code, name, ARRAY[plant_id]::text[], '' FROM petty_cash_float WHERE (code ILIKE $1 OR name ILIKE $1) AND ($2 = '' OR plant_id = $2 OR plant_id IS NULL)
			ORDER BY code LIMIT 20`,
		link: func(id, _ string) string { return "/petty-cash?tab=floats&float=" + id }},
}

func likePattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(strings.TrimSpace(q)) + "%"
}

// Resolve loads one target (nil, nil when it does not exist). TProductionDay ids are "<plantId>|YYYY-MM-DD".
func Resolve(ctx context.Context, q db.DBTX, typ, id string) (*Target, error) {
	if typ == TProductionDay {
		plant, day, ok := strings.Cut(id, "|")
		d, err := dates.ParseISO(day)
		if !ok || err != nil {
			return nil, nil
		}
		var code string
		err = q.QueryRow(ctx, `SELECT code FROM "Plant" WHERE id = $1`, plant).Scan(&code)
		if db.IsNoRows(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return &Target{Type: typ, ID: id, Label: code + " · " + dates.FormatDate(d), Link: "/production/days?plantId=" + plant + "&date=" + day, PlantIDs: []string{plant}}, nil
	}
	sp, ok := specs[typ]
	if !ok {
		return nil, nil
	}
	t := &Target{Type: typ, ID: id}
	var plants []*string
	var extra string
	err := q.QueryRow(ctx, sp.one, id).Scan(&t.Label, &t.Sub, &plants, &extra)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, p := range plants {
		if p != nil && *p != "" {
			t.PlantIDs = append(t.PlantIDs, *p)
		}
	}
	t.Link = sp.link(id, extra)
	return t, nil
}

// CanAccess: site-pinned users only reach records of their own site (records without a site are visible).
func CanAccess(u *auth.User, t *Target) bool {
	s := auth.ScopedPlantID(u)
	if s == "" || len(t.PlantIDs) == 0 {
		return true
	}
	for _, p := range t.PlantIDs {
		if p == s {
			return true
		}
	}
	return false
}

// Search lists up to 20 records of one type matching q (number, name …) that u may link to.
func Search(ctx context.Context, q db.DBTX, u *auth.User, typ, text string) ([]Target, error) {
	out := []Target{}
	sp, ok := specs[typ]
	if !ok {
		return out, nil
	}
	rows, err := q.Query(ctx, sp.search, likePattern(text), auth.ScopedPlantID(u))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t Target
		var plants []*string
		var extra string
		if err := rows.Scan(&t.ID, &t.Label, &t.Sub, &plants, &extra); err != nil {
			return nil, err
		}
		t.Type = typ
		t.Link = sp.link(t.ID, extra)
		out = append(out, t)
	}
	return out, rows.Err()
}

// Input is a link request: an entity (type + id) or a free reference (type "reference" + text).
type Input struct {
	Type      string
	EntityID  string
	Reference string
	Note      *string
}

// ParseInput reads {entityType, entityId, reference, note} from a validator.
func ParseInput(v *httpx.V) Input {
	in := Input{}
	in.Type = v.Str("entityType").Trim().OneOf(Types, "finlink.v.type").Get()
	in.EntityID = v.OptStr("entityId").Trim().Max(120).Get()
	in.Reference = v.OptStr("reference").Trim().Collapse().Get()
	in.Note = v.NullStr("note").Trim().Collapse().Max(300, "finlink.v.note").NilIfEmpty()
	if in.Type == TReference {
		if n := utf8.RuneCountInString(in.Reference); n < 2 || n > 120 {
			v.Fail("reference", "finlink.v.reference")
		}
	} else if in.Type != "" && in.EntityID == "" {
		v.Fail("entityId", "finlink.v.entity")
	}
	return in
}

// Validate resolves the target of in for u: 400 when the record does not exist, 403 outside u's site.
func Validate(ctx context.Context, q db.DBTX, lang i18n.Lang, u *auth.User, in Input) (*Target, error) {
	if in.Type == TReference {
		return &Target{Type: TReference, Label: in.Reference}, nil
	}
	t, err := Resolve(ctx, q, in.Type, in.EntityID)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, httpx.BadRequest("error.validation", map[string]string{"entityId": i18n.T(lang, "finlink.v.not_found")})
	}
	if u != nil && !CanAccess(u, t) {
		return nil, httpx.Forbidden("finlink.err.site_scope")
	}
	return t, nil
}

// Link is one stored link of a money record.
type Link struct {
	ID         string     `json:"id"`
	EntityType string     `json:"entityType"`
	TypeLabel  string     `json:"typeLabel"`
	EntityID   *string    `json:"entityId"`
	Label      string     `json:"label"`
	Note       *string    `json:"note"`
	Auto       bool       `json:"auto"`
	Link       string     `json:"link,omitempty"`
	CreatedBy  string     `json:"createdBy"`
	CreatedAt  time.Time  `json:"createdAt"`
	RemovedAt  *time.Time `json:"removedAt,omitempty"`
}

// Table names a link table and its parent column.
type Table struct{ Name, Parent string }

var (
	ExpenseLinks = Table{"expense_link", "expense_id"}
	IncomeLinks  = Table{"income_link", "income_id"}
)

// MaxLinks per money record.
const MaxLinks = 50

// List returns the active links of one record (removed ones too when withRemoved).
func List(ctx context.Context, q db.DBTX, lang i18n.Lang, tb Table, parentID string, withRemoved bool) ([]Link, error) {
	rows, err := q.Query(ctx, fmt.Sprintf(`SELECT l.id, l.entity_type, l.entity_id, l.label, l.note, l.auto, COALESCE(u.name, ''), l.created_at, l.removed_at
		FROM %s l LEFT JOIN "User" u ON u.id = l.created_by_id WHERE l.%s = $1 AND ($2 OR l.removed_at IS NULL) ORDER BY l.created_at, l.id`, tb.Name, tb.Parent),
		parentID, withRemoved)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Link{}
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.ID, &l.EntityType, &l.EntityID, &l.Label, &l.Note, &l.Auto, &l.CreatedBy, &l.CreatedAt, &l.RemovedAt); err != nil {
			return nil, err
		}
		l.TypeLabel = i18n.T(lang, "finlink.type."+l.EntityType)
		if l.EntityID != nil {
			if sp, ok := specs[l.EntityType]; ok && l.EntityType != TDelivery && l.EntityType != TGoodsReceipt && l.EntityType != TSupplierInvoice {
				l.Link = sp.link(*l.EntityID, "")
			}
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// links whose path needs the parent record (deliveries → order, receipts/invoices → PO)
	for i := range out {
		if out[i].EntityID != nil && out[i].Link == "" && out[i].EntityType != TReference {
			if t, err := Resolve(ctx, q, out[i].EntityType, *out[i].EntityID); err == nil && t != nil {
				out[i].Link = t.Link
			}
		}
	}
	return out, nil
}

// Add stores a link (409 when the same record is already linked; 409 above MaxLinks). Returns the new link id.
func Add(ctx context.Context, q db.DBTX, tb Table, parentID string, t *Target, note *string, userID string, auto bool) (string, error) {
	var n int
	if err := q.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s WHERE %s = $1 AND removed_at IS NULL`, tb.Name, tb.Parent), parentID).Scan(&n); err != nil {
		return "", err
	}
	if n >= MaxLinks {
		return "", httpx.NewKey(409, "conflict", "finlink.err.too_many", "n", MaxLinks)
	}
	var eid *string
	if t.Type != TReference {
		id := t.ID
		eid = &id
	}
	var exists bool
	if err := q.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE %s = $1 AND entity_type = $2 AND COALESCE(entity_id, lower(label)) = COALESCE($3, lower($4))
		AND removed_at IS NULL)`, tb.Name, tb.Parent), parentID, t.Type, eid, t.Label).Scan(&exists); err != nil {
		return "", err
	}
	if exists {
		return "", httpx.NewKey(409, "conflict", "finlink.err.duplicate")
	}
	label := t.Label
	if t.Sub != "" && t.Type != TReference && t.Type != TCustomer && t.Type != TSupplier {
		label += " · " + t.Sub
	}
	var uid *string
	if userID != "" {
		uid = &userID
	}
	var id string
	err := q.QueryRow(ctx, fmt.Sprintf(`INSERT INTO %s (%s, entity_type, entity_id, label, note, auto, created_by_id) VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		tb.Name, tb.Parent), parentID, t.Type, eid, label, note, auto, uid).Scan(&id)
	return id, err
}

// Remove stamps a link as removed (kept for the audit trail). Returns the removed link (404 when unknown / already removed).
func Remove(ctx context.Context, q db.DBTX, tb Table, parentID, linkID, userID string, reason *string) (*Link, error) {
	var l Link
	err := q.QueryRow(ctx, fmt.Sprintf(`UPDATE %s SET removed_at = $4, removed_by_id = $3, removed_reason = $5 WHERE id = $1 AND %s = $2 AND removed_at IS NULL
		RETURNING id, entity_type, entity_id, label`, tb.Name, tb.Parent), linkID, parentID, userID, db.Now(), reason).Scan(&l.ID, &l.EntityType, &l.EntityID, &l.Label)
	if db.IsNoRows(err) {
		return nil, httpx.NotFound("finlink.entity")
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}
