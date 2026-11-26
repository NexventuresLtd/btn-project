// Package dataexport is the ADM-05 bulk data export: entity tables as CSV or JSON (or a ZIP of all of them),
// streamed from the database, permission `data.export`, audited (action "data.export") and, for tables with
// personal data, recorded in the NFR-D2 access log through export.PersonalDataHook.
//
//	GET /api/v1/data/export/tables                       table list with row counts
//	GET /api/v1/data/export/tables/:table?format=csv|json[&from=&to=]
//	GET /api/v1/data/export/all?format=csv|json[&from=&to=]   ZIP with one file per table + manifest.json
package dataexport

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/deps"
	"btn-ops/internal/export"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/ratelimit"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Table is one exportable entity table.
type Table struct {
	Name     string // url + file name
	Label    string // i18n key
	Personal bool
	Optional string // only listed when this relation exists (to_regclass), e.g. a phase-2 table
	DateCol  string // column the from/to range applies to ("" = no range)
	SQL      string // SELECT … (columns in Columns order); WHERE/ORDER are appended
	Order    string
	Columns  []export.Column
}

func c(key, header string, k export.Kind) export.Column {
	return export.Column{Key: key, Header: header, Kind: k}
}

const (
	T  = export.Text
	I  = export.Int
	M  = export.Money
	Q  = export.Qty
	D  = export.Date
	DT = export.DateTime
	B  = export.Bool
)

// Tables is the catalogue (phase-2 agents: append your tables here, using Optional for new relations).
var Tables = []Table{
	{Name: "sites", Label: "dataexport.table.sites", SQL: `SELECT p.id, p.code, p.name, p.location, p.kind, p."isActive", p."createdAt" FROM "Plant" p`, Order: `p.code`,
		Columns: []export.Column{c("id", "dataexport.col.id", T), c("code", "export.col.code", T), c("name", "dataexport.col.name", T), c("location", "dataexport.col.location", T),
			c("kind", "export.col.type", T), c("isActive", "export.col.active", B), c("createdAt", "export.col.created_at", DT)}},
	{Name: "products", Label: "dataexport.table.products", SQL: `SELECT p.id, p.code, p.name, p.dimensions, p.unit, p."defaultPrice", p."sortOrder", p."isActive", p."createdAt" FROM "Product" p`, Order: `p."sortOrder", p.code`,
		Columns: []export.Column{c("id", "dataexport.col.id", T), c("code", "export.col.code", T), c("name", "dataexport.col.name", T), c("dimensions", "dataexport.col.dimensions", T),
			c("unit", "export.col.unit", T), c("defaultPrice", "dataexport.col.default_price", M), c("sortOrder", "dataexport.col.sort", I), c("isActive", "export.col.active", B), c("createdAt", "export.col.created_at", DT)}},
	{Name: "materials", Label: "dataexport.table.materials", SQL: `SELECT m.id, m.code, m.name, m.unit, m."minLevel"::float8, m."sortOrder", m."isActive", m."createdAt" FROM "Material" m`, Order: `m."sortOrder", m.code`,
		Columns: []export.Column{c("id", "dataexport.col.id", T), c("code", "export.col.code", T), c("name", "dataexport.col.name", T), c("unit", "export.col.unit", T),
			c("minLevel", "export.col.min_level", Q), c("sortOrder", "dataexport.col.sort", I), c("isActive", "export.col.active", B), c("createdAt", "export.col.created_at", DT)}},
	{Name: "customers", Label: "dataexport.table.customers", Personal: true, SQL: `SELECT x.id, x.name, x.tin, x.phone, x.email, x.address, x."isActive", x."createdAt" FROM "Customer" x`, Order: `x.name`,
		Columns: []export.Column{c("id", "dataexport.col.id", T), c("name", "export.col.customer", T), c("tin", "export.col.tin", T), c("phone", "export.col.phone", T),
			c("email", "export.col.email", T), c("address", "export.col.address", T), c("isActive", "export.col.active", B), c("createdAt", "export.col.created_at", DT)}},
	{Name: "suppliers", Label: "dataexport.table.suppliers", Optional: "supplier", SQL: `SELECT to_jsonb(s)::text FROM supplier s`, Order: `1`,
		Columns: []export.Column{c("record", "dataexport.col.record", T)}},
	// P1 production: entries (with batch lines, defects, materials, flags, notes, corrections), targets, recipes, day close
	{Name: "production-entries", Label: "production.export.entries", Optional: "production_defect", DateCol: `r."date"`, Order: `r."date", r.number`,
		SQL: `SELECT (to_jsonb(r) - 'server_xid' || jsonb_build_object('lines', (SELECT COALESCE(jsonb_agg(to_jsonb(l) || jsonb_build_object('defects', (SELECT COALESCE(jsonb_agg(to_jsonb(d)), '[]') FROM production_defect d WHERE d."lineId" = l.id))), '[]') FROM "ProductionLine" l WHERE l."reportId" = r.id),
			'materials', (SELECT COALESCE(jsonb_agg(to_jsonb(u)), '[]') FROM "ProductionMaterialUsage" u WHERE u."reportId" = r.id), 'exceptionFlags', (SELECT COALESCE(jsonb_agg(to_jsonb(f)), '[]') FROM production_exception f WHERE f."reportId" = r.id),
			'notes', (SELECT COALESCE(jsonb_agg(to_jsonb(n)), '[]') FROM production_note n WHERE n."reportId" = r.id), 'corrections', (SELECT COALESCE(jsonb_agg(to_jsonb(c)), '[]') FROM production_correction c WHERE c."reportId" = r.id)))::text FROM "ProductionReport" r`,
		Columns: []export.Column{c("record", "dataexport.col.record", T)}},
	{Name: "production-targets", Label: "production.export.targets", Optional: "production_target", DateCol: `t."periodStart"`, Order: `t."periodStart", t."plantId"`, SQL: `SELECT to_jsonb(t)::text FROM production_target t`, Columns: []export.Column{c("record", "dataexport.col.record", T)}},
	{Name: "production-recipes", Label: "production.export.recipes", Optional: "production_recipe", Order: `r."productId", r."effectiveFrom"`,
		SQL: `SELECT (to_jsonb(r) || jsonb_build_object('items', (SELECT COALESCE(jsonb_agg(to_jsonb(i)), '[]') FROM production_recipe_item i WHERE i."recipeId" = r.id)))::text FROM production_recipe r`, Columns: []export.Column{c("record", "dataexport.col.record", T)}},
	{Name: "production-days", Label: "production.export.days", Optional: "production_day", DateCol: `d.date`, Order: `d.date, d."plantId"`, SQL: `SELECT (to_jsonb(d) - 'server_xid')::text FROM production_day d`, Columns: []export.Column{c("record", "dataexport.col.record", T)}},
	// P6 procurement: requests (with lines + approvals), purchase orders (with lines + invoices), goods receipts (with lines)
	{Name: "procurement-requests", Label: "procurement.export.requests", Optional: "procurement_request", DateCol: `r.created_at::date`, Order: `r.created_at`,
		SQL: `SELECT (to_jsonb(r) - 'server_xid' || jsonb_build_object('lines', (SELECT COALESCE(jsonb_agg(to_jsonb(l) ORDER BY l.line_no), '[]') FROM procurement_request_line l WHERE l.request_id = r.id),
			'approvals', (SELECT COALESCE(jsonb_agg(to_jsonb(a) ORDER BY a.cycle, a.level), '[]') FROM procurement_approval a WHERE a.request_id = r.id)))::text FROM procurement_request r`,
		Columns: []export.Column{c("record", "dataexport.col.record", T)}},
	{Name: "purchase-orders", Label: "procurement.export.orders", Optional: "purchase_order", DateCol: `o.order_date`, Order: `o.order_date, o.number`,
		SQL: `SELECT (to_jsonb(o) || jsonb_build_object('lines', (SELECT COALESCE(jsonb_agg(to_jsonb(l) ORDER BY l.line_no), '[]') FROM purchase_order_line l WHERE l.po_id = o.id),
			'invoices', (SELECT COALESCE(jsonb_agg(to_jsonb(i)), '[]') FROM purchase_invoice i WHERE i.po_id = o.id)))::text FROM purchase_order o`,
		Columns: []export.Column{c("record", "dataexport.col.record", T)}},
	{Name: "goods-receipts", Label: "procurement.export.receipts", Optional: "goods_receipt", DateCol: `g.date`, Order: `g.date, g.number`,
		SQL:     `SELECT (to_jsonb(g) || jsonb_build_object('lines', (SELECT COALESCE(jsonb_agg(to_jsonb(l)), '[]') FROM goods_receipt_line l WHERE l.receipt_id = g.id)))::text FROM goods_receipt g`,
		Columns: []export.Column{c("record", "dataexport.col.record", T)}},
	{Name: "workers", Label: "dataexport.table.workers", Personal: true,
		SQL: `SELECT w.id, w."firstName", w."lastName", w."nationalId", w."momoNumber", w."momoName", w."jobTitle", p.code, w."dailyWage", w."startDate", w."isActive", w."createdAt"
			FROM "Worker" w LEFT JOIN "Plant" p ON p.id = w."plantId"`, Order: `p.code, w."lastName", w."firstName"`,
		Columns: []export.Column{c("id", "dataexport.col.id", T), c("firstName", "dataexport.col.first_name", T), c("lastName", "dataexport.col.last_name", T),
			c("nationalId", "export.col.national_id", T), c("momoNumber", "export.col.momo", T), c("momoName", "export.col.momo_name", T), c("jobTitle", "export.col.job_title", T),
			c("plant", "export.col.plant", T), c("dailyWage", "export.col.daily_wage", M), c("startDate", "export.col.start", D), c("isActive", "export.col.active", B), c("createdAt", "export.col.created_at", DT)}},
	{Name: "stock-movements", Label: "dataexport.table.stock_movements", DateCol: `m.date`,
		SQL: `SELECT m.id, m.date, p.code, pr.code, pr.name, m.type::text, m.quantity, m."sourceType", m."sourceId", m.note, u.name, m."createdAt"
			FROM "StockMovement" m JOIN "Plant" p ON p.id = m."plantId" JOIN "Product" pr ON pr.id = m."productId" LEFT JOIN "User" u ON u.id = m."createdById"`, Order: `m.date, m."createdAt", m.id`,
		Columns: []export.Column{c("id", "dataexport.col.id", T), c("date", "export.col.date", D), c("plant", "export.col.plant", T), c("productCode", "export.col.code", T),
			c("product", "export.col.product", T), c("type", "export.col.type", T), c("quantity", "export.col.quantity", I), c("sourceType", "export.col.source", T),
			c("sourceId", "dataexport.col.source_id", T), c("note", "export.col.note", T), c("createdBy", "export.col.created_by", T), c("createdAt", "export.col.created_at", DT)}},
	{Name: "material-movements", Label: "dataexport.table.material_movements", DateCol: `m.date`,
		SQL: `SELECT m.id, m.date, p.code, mt.code, mt.name, m.type::text, m.quantity::float8, m."unitCost", m.supplier, m.reference, cp.code, m."transferGroup", m."sourceType", m."sourceId", m.note, u.name, m."createdAt"
			FROM "MaterialMovement" m JOIN "Plant" p ON p.id = m."plantId" JOIN "Material" mt ON mt.id = m."materialId" LEFT JOIN "Plant" cp ON cp.id = m."counterpartPlantId" LEFT JOIN "User" u ON u.id = m."createdById"`,
		Order: `m.date, m."createdAt", m.id`,
		Columns: []export.Column{c("id", "dataexport.col.id", T), c("date", "export.col.date", D), c("plant", "export.col.plant", T), c("materialCode", "export.col.code", T),
			c("material", "export.col.material", T), c("type", "export.col.type", T), c("quantity", "export.col.quantity", Q), c("unitCost", "export.col.unit_cost", M),
			c("supplier", "export.col.supplier", T), c("reference", "export.col.reference", T), c("counterpart", "export.col.counterpart", T), c("transferGroup", "dataexport.col.transfer_group", T),
			c("sourceType", "export.col.source", T), c("sourceId", "dataexport.col.source_id", T), c("note", "export.col.note", T), c("createdBy", "export.col.created_by", T), c("createdAt", "export.col.created_at", DT)}},
	{Name: "sales", Label: "dataexport.table.sales", Personal: true, DateCol: `s.date`,
		SQL: `SELECT s.id, s.number, s.date, p.code, cu.name, s.status::text, s."invoiceType", s."ebmInvoiceNo", s."ebmReceiptNo",
			(SELECT coalesce(sum(l.quantity),0) FROM "SaleLine" l WHERE l."saleId" = s.id), (SELECT coalesce(sum(l.amount),0) FROM "SaleLine" l WHERE l."saleId" = s.id),
			s.notes, s."confirmedAt", s."cancelledAt", s."cancelReason", s."createdAt"
			FROM "Sale" s JOIN "Plant" p ON p.id = s."plantId" LEFT JOIN "Customer" cu ON cu.id = s."customerId"`, Order: `s.date, s.number`,
		Columns: []export.Column{c("id", "dataexport.col.id", T), c("number", "export.col.number", T), c("date", "export.col.date", D), c("plant", "export.col.plant", T),
			c("customer", "export.col.customer", T), c("status", "export.col.status", T), c("invoiceType", "export.col.invoice_type", T), c("ebmInvoiceNo", "export.col.ebm_invoice", T),
			c("ebmReceiptNo", "dataexport.col.ebm_receipt", T), c("quantity", "export.col.quantity", I), c("amount", "export.col.amount", M), c("notes", "export.col.note", T),
			c("confirmedAt", "dataexport.col.confirmed_at", DT), c("cancelledAt", "dataexport.col.cancelled_at", DT), c("cancelReason", "dataexport.col.cancel_reason", T), c("createdAt", "export.col.created_at", DT)}},
	{Name: "sale-lines", Label: "dataexport.table.sale_lines", DateCol: `s.date`,
		SQL:   `SELECT l.id, s.number, s.date, pr.code, pr.name, l.quantity, l."unitPrice", l.amount FROM "SaleLine" l JOIN "Sale" s ON s.id = l."saleId" JOIN "Product" pr ON pr.id = l."productId"`,
		Order: `s.date, s.number, pr.code`,
		Columns: []export.Column{c("id", "dataexport.col.id", T), c("sale", "export.col.sale", T), c("date", "export.col.date", D), c("productCode", "export.col.code", T),
			c("product", "export.col.product", T), c("quantity", "export.col.quantity", I), c("unitPrice", "dataexport.col.unit_price", M), c("amount", "export.col.amount", M)}},
	{Name: "payments", Label: "dataexport.table.payments", Personal: true, DateCol: `pm.date`,
		SQL: `SELECT pm.id, pm.date, s.number, cu.name, pm.amount, pm.method::text, pm.reference, u.name, pm."createdAt"
			FROM "Payment" pm JOIN "Sale" s ON s.id = pm."saleId" LEFT JOIN "Customer" cu ON cu.id = s."customerId" LEFT JOIN "User" u ON u.id = pm."receivedById"`, Order: `pm.date, pm."createdAt"`,
		Columns: []export.Column{c("id", "dataexport.col.id", T), c("date", "export.col.date", D), c("sale", "export.col.sale", T), c("customer", "export.col.customer", T),
			c("amount", "export.col.amount", M), c("method", "dataexport.col.method", T), c("reference", "export.col.reference", T), c("receivedBy", "export.col.received_by", T), c("createdAt", "export.col.created_at", DT)}},
	{Name: "deliveries", Label: "dataexport.table.deliveries", DateCol: `dl.date`,
		SQL: `SELECT dl.id, dl."dnNumber", dl.date, p.code, s.number, dl.status::text, dl.vehicle, dl.driver, dl."receivedBy", dl."deliveredAt", dl."createdAt"
			FROM "Delivery" dl JOIN "Plant" p ON p.id = dl."plantId" JOIN "Sale" s ON s.id = dl."saleId"`, Order: `dl.date, dl."dnNumber"`,
		Columns: []export.Column{c("id", "dataexport.col.id", T), c("dnNumber", "export.col.dn_number", T), c("date", "export.col.date", D), c("plant", "export.col.plant", T),
			c("sale", "export.col.sale", T), c("status", "export.col.status", T), c("vehicle", "export.col.vehicle", T), c("driver", "export.col.driver", T),
			c("receivedBy", "export.col.received_by", T), c("deliveredAt", "export.col.delivered_at", DT), c("createdAt", "export.col.created_at", DT)}},
	{Name: "petty-cash", Label: "dataexport.table.petty_cash", Personal: true, DateCol: `e.date`,
		SQL: `SELECT e.id, e.number, e.date, e.type::text, e.category::text, e.description, p.code, e."payeeName", e."momoNumber", e.amount, e."momoCharge", e."invoiceRef", e.status::text,
			cu.name, ru.name, e."reviewedAt", e."reviewComment", e."createdAt"
			FROM "PettyCashEntry" e LEFT JOIN "Plant" p ON p.id = e."plantId" LEFT JOIN "User" cu ON cu.id = e."createdById" LEFT JOIN "User" ru ON ru.id = e."reviewedById"`, Order: `e.date, e."createdAt"`,
		Columns: []export.Column{c("id", "dataexport.col.id", T), c("number", "export.col.number", T), c("date", "export.col.date", D), c("type", "export.col.type", T),
			c("category", "export.col.category", T), c("description", "export.col.description", T), c("plant", "export.col.plant", T), c("payee", "export.col.payee", T),
			c("momo", "export.col.momo", T), c("amount", "export.col.amount", M), c("momoCharge", "export.col.momo_charge", M), c("invoiceRef", "export.col.reference", T),
			c("status", "export.col.status", T), c("createdBy", "export.col.created_by", T), c("reviewedBy", "export.col.reviewed_by", T), c("reviewedAt", "export.col.reviewed_at", DT),
			c("reviewComment", "dataexport.col.review_comment", T), c("createdAt", "export.col.created_at", DT)}},
	{Name: "payroll-periods", Label: "dataexport.table.payroll_periods", DateCol: `pp."startDate"`,
		SQL: `SELECT pp.id, pp.number, p.code, pp."startDate", pp."endDate", pp.status::text, pp."preparedAt", pp."checkedAt", pp."verifiedAt", pp."authorizedAt", pp."paidAt", pp."createdAt"
			FROM "PayrollPeriod" pp JOIN "Plant" p ON p.id = pp."plantId"`, Order: `pp."startDate", p.code`,
		Columns: []export.Column{c("id", "dataexport.col.id", T), c("number", "export.col.number", T), c("plant", "export.col.plant", T), c("startDate", "export.col.start", D),
			c("endDate", "export.col.end", D), c("status", "export.col.status", T), c("preparedAt", "dataexport.col.prepared_at", DT), c("checkedAt", "dataexport.col.checked_at", DT),
			c("verifiedAt", "dataexport.col.verified_at", DT), c("authorizedAt", "dataexport.col.authorized_at", DT), c("paidAt", "dataexport.col.paid_at", DT), c("createdAt", "export.col.created_at", DT)}},
	{Name: "payroll-lines", Label: "dataexport.table.payroll_lines", Personal: true, DateCol: `pp."startDate"`,
		SQL: `SELECT l.id, pp.number, p.code, w."firstName" || ' ' || w."lastName", w."nationalId", l."dailyWage", l."daysWorked"::float8, l."grossAmount", l."foodDeduction", l."otherDeduction", l."netAmount", l."paymentRef", l.note
			FROM "PayrollLine" l JOIN "PayrollPeriod" pp ON pp.id = l."periodId" JOIN "Plant" p ON p.id = pp."plantId" JOIN "Worker" w ON w.id = l."workerId"`, Order: `pp."startDate", pp.number, w."lastName"`,
		Columns: []export.Column{c("id", "dataexport.col.id", T), c("period", "export.col.number", T), c("plant", "export.col.plant", T), c("worker", "export.col.worker", T),
			c("nationalId", "export.col.national_id", T), c("dailyWage", "export.col.daily_wage", M), c("daysWorked", "export.col.days", Q), c("gross", "export.col.gross", M),
			c("food", "export.col.food", M), c("other", "export.col.other_deductions", M), c("net", "export.col.net", M), c("paymentRef", "export.col.payment_ref", T), c("note", "export.col.note", T)}},
	{Name: "attendance", Label: "dataexport.table.attendance", Personal: true, DateCol: `a.date`,
		SQL: `SELECT a.id, a.date, p.code, w."firstName" || ' ' || w."lastName", a.status, a.note, u.name, a."createdAt"
			FROM "Attendance" a JOIN "Plant" p ON p.id = a."plantId" JOIN "Worker" w ON w.id = a."workerId" LEFT JOIN "User" u ON u.id = a."recordedById"`, Order: `a.date, p.code, w."lastName"`,
		Columns: []export.Column{c("id", "dataexport.col.id", T), c("date", "export.col.date", D), c("plant", "export.col.plant", T), c("worker", "export.col.worker", T),
			c("status", "export.col.status", T), c("note", "export.col.note", T), c("recordedBy", "export.col.created_by", T), c("createdAt", "export.col.created_at", DT)}},
	{Name: "users", Label: "dataexport.table.users", Personal: true,
		SQL: `SELECT u.id, u.name, u.email, u.phone, u.role::text, p.code, u."isActive", u.language, u."lastLoginAt", u."createdAt" FROM "User" u LEFT JOIN "Plant" p ON p.id = u."plantId"`, Order: `u.name`,
		Columns: []export.Column{c("id", "dataexport.col.id", T), c("name", "dataexport.col.name", T), c("email", "export.col.email", T), c("phone", "export.col.phone", T),
			c("role", "dataexport.col.role", T), c("plant", "export.col.plant", T), c("isActive", "export.col.active", B), c("language", "dataexport.col.language", T),
			c("lastLoginAt", "dataexport.col.last_login", DT), c("createdAt", "export.col.created_at", DT)}},
	{Name: "audit", Label: "dataexport.table.audit", DateCol: `a."createdAt"`,
		SQL: `SELECT a.id, a."createdAt", u.name, u.email, a.action, a.entity, a."entityId", a.summary, a.ip, a."deviceId", a."userAgent", a."before"::text, a."after"::text
			FROM "AuditLog" a LEFT JOIN "User" u ON u.id = a."userId"`, Order: `a."createdAt", a.id`,
		Columns: []export.Column{c("id", "dataexport.col.id", T), c("createdAt", "export.col.time", DT), c("user", "export.col.user", T), c("email", "export.col.email", T),
			c("action", "export.col.action", T), c("entity", "export.col.entity", T), c("entityId", "export.col.entity_id", T), c("summary", "export.col.summary", T),
			c("ip", "export.col.ip", T), c("deviceId", "export.col.device", T), c("userAgent", "export.col.user_agent", T), c("before", "export.col.before", T), c("after", "export.col.after", T)}},
}

func find(name string) *Table {
	for i := range Tables {
		if Tables[i].Name == name {
			return &Tables[i]
		}
	}
	return nil
}

// Register mounts the routes (behind RequireAuth).
func Register(r fiber.Router, d *deps.Deps) {
	perm := auth.RequirePermission("data.export")
	r.Get("/tables", perm, func(c *fiber.Ctx) error { return list(c, d) })
	r.Get("/tables/:table", perm, ratelimit.Group("export"), func(c *fiber.Ctx) error { return one(c, d) })
	r.Get("/all", perm, ratelimit.Group("export"), func(c *fiber.Ctx) error { return all(c, d) })
}

func available(ctx context.Context, pool *pgxpool.Pool, t *Table) bool {
	if t.Optional == "" {
		return true
	}
	var ok bool
	_ = pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+t.Optional).Scan(&ok)
	return ok
}

func list(c *fiber.Ctx, d *deps.Deps) error {
	ctx := httpx.Ctx(c)
	lang := httpx.Lang(c)
	items := []fiber.Map{}
	for i := range Tables {
		t := &Tables[i]
		if !available(ctx, d.Pool, t) {
			continue
		}
		var n int64
		if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM (`+t.SQL+`) x`).Scan(&n); err != nil {
			return err
		}
		cols := make([]string, len(t.Columns))
		for j, col := range t.Columns {
			cols[j] = i18n.T(lang, col.Header)
		}
		items = append(items, fiber.Map{"name": t.Name, "label": i18n.T(lang, t.Label), "rows": n, "personal": t.Personal, "dateFilter": t.DateCol != "", "columns": cols})
	}
	httpx.NoStore(c)
	return c.JSON(fiber.Map{"items": items, "formats": []string{"csv", "json"}})
}

type params struct {
	format   export.Format
	from, to *time.Time
}

func parse(c *fiber.Ctx) (params, error) {
	p := params{format: export.CSV}
	if f := c.Query("format"); f != "" {
		ff, ok := export.ParseFormat(f)
		if !ok || ff == export.XLSX {
			return p, httpx.BadRequest("dataexport.err.format", map[string]string{"format": i18n.T(httpx.Lang(c), "dataexport.err.format")})
		}
		p.format = ff
	}
	for k, dst := range map[string]**time.Time{"from": &p.from, "to": &p.to} {
		if v := c.Query(k); v != "" {
			t, err := dates.ParseISO(v)
			if err != nil {
				return p, httpx.BadRequest("dataexport.err.date", map[string]string{k: i18n.T(httpx.Lang(c), "dataexport.err.date")})
			}
			*dst = &t
		}
	}
	return p, nil
}

func (t *Table) query(p params) (string, []any) {
	sql := t.SQL
	var conds []string
	var args []any
	if t.DateCol != "" && p.from != nil {
		args = append(args, *p.from)
		conds = append(conds, fmt.Sprintf("%s >= $%d", t.DateCol, len(args)))
	}
	if t.DateCol != "" && p.to != nil {
		args = append(args, dates.AddDays(*p.to, 1))
		conds = append(conds, fmt.Sprintf("%s < $%d", t.DateCol, len(args)))
	}
	if len(conds) > 0 {
		sql += " WHERE " + strings.Join(conds, " AND ")
	}
	return sql + " ORDER BY " + t.Order, args
}

// stream writes one table to w; returns the row count.
func stream(ctx context.Context, pool *pgxpool.Pool, w *bufio.Writer, t *Table, p params, lang i18n.Lang) (int, error) {
	sql, args := t.query(p)
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	next := func() ([]any, bool, error) {
		if !rows.Next() {
			return nil, false, rows.Err()
		}
		v, err := rows.Values()
		return v, err == nil, err
	}
	if p.format == export.CSV {
		return export.WriteCSV(w, lang, t.Columns, next, true)
	}
	type colOut struct {
		Key    string `json:"key"`
		Header string `json:"header"`
		Type   string `json:"type"`
	}
	cols := make([]colOut, len(t.Columns))
	for i, col := range t.Columns {
		cols[i] = colOut{col.Key, i18n.T(lang, col.Header), col.Kind.String()}
	}
	hdr, _ := json.Marshal(cols)
	fmt.Fprintf(w, `{"table":%q,"exportedAt":%q,"columns":%s,"rows":[`, t.Name, time.Now().UTC().Format(time.RFC3339), hdr)
	n := 0
	for {
		r, ok, err := next()
		if err != nil {
			return n, err
		}
		if !ok {
			break
		}
		b, err := json.Marshal(export.RowObject(t.Columns, r))
		if err != nil {
			return n, err
		}
		if n > 0 {
			w.WriteByte(',')
		}
		w.Write(b)
		n++
		if n%500 == 0 {
			w.Flush()
		}
	}
	w.WriteString("]}")
	return n, nil
}

func stamp() string { return time.Now().In(dates.Kigali()).Format("2006-01-02_1504") }

func recordExport(c *fiber.Ctx, d *deps.Deps, tables []string, p params, personal bool) error {
	after := map[string]any{"tables": tables, "format": p.format}
	if p.from != nil {
		after["from"] = dates.ToISO(*p.from)
	}
	if p.to != nil {
		after["to"] = dates.ToISO(*p.to)
	}
	if err := audit.Record(c, d.Pool, audit.Input{Action: "data.export", Entity: "DataExport", Summary: "Exported " + strings.Join(tables, ", ") + " (" + string(p.format) + ")", After: after}); err != nil {
		return err
	}
	if personal && export.PersonalDataHook != nil {
		export.PersonalDataHook(c, "data-export:"+strings.Join(tables, ","), -1)
	}
	return nil
}

const streamTimeout = 10 * time.Minute

func one(c *fiber.Ctx, d *deps.Deps) error {
	t := find(c.Params("table"))
	if t == nil || !available(httpx.Ctx(c), d.Pool, t) {
		return httpx.NotFound("dataexport.entity")
	}
	p, err := parse(c)
	if err != nil {
		return err
	}
	if err := recordExport(c, d, []string{t.Name}, p, t.Personal || t.Name == "audit"); err != nil {
		return err
	}
	lang := httpx.Lang(c)
	ext := "csv"
	c.Set(fiber.HeaderContentType, "text/csv; charset=utf-8")
	if p.format == export.JSON {
		ext = "json"
		c.Set(fiber.HeaderContentType, "application/json; charset=utf-8")
	}
	c.Attachment("btn-" + t.Name + "_" + stamp() + "." + ext)
	httpx.NoStore(c)
	tbl := *t
	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		ctx, cancel := context.WithTimeout(context.Background(), streamTimeout)
		defer cancel()
		if _, err := stream(ctx, d.Pool, w, &tbl, p, lang); err != nil {
			slog.Error("data export stream", "table", tbl.Name, "err", err)
		}
		w.Flush()
	})
	return nil
}

func all(c *fiber.Ctx, d *deps.Deps) error {
	p, err := parse(c)
	if err != nil {
		return err
	}
	ctx := httpx.Ctx(c)
	var tables []Table
	names := []string{}
	for i := range Tables {
		if available(ctx, d.Pool, &Tables[i]) {
			tables = append(tables, Tables[i])
			names = append(names, Tables[i].Name)
		}
	}
	if err := recordExport(c, d, names, p, true); err != nil {
		return err
	}
	lang := httpx.Lang(c)
	by := ""
	if u := auth.FromCtx(c); u != nil {
		by = u.Name
	}
	c.Set(fiber.HeaderContentType, "application/zip")
	c.Attachment("btn-data-export_" + stamp() + ".zip")
	httpx.NoStore(c)
	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		ctx, cancel := context.WithTimeout(context.Background(), streamTimeout)
		defer cancel()
		zw := zip.NewWriter(w)
		manifest := map[string]any{"exportedAt": time.Now().UTC().Format(time.RFC3339), "exportedBy": by, "format": p.format, "tables": map[string]int{}}
		for i := range tables {
			fw, err := zw.Create(tables[i].Name + "." + string(p.format))
			if err != nil {
				slog.Error("data export zip", "err", err)
				return
			}
			bw := bufio.NewWriterSize(fw, 64<<10)
			n, err := stream(ctx, d.Pool, bw, &tables[i], p, lang)
			bw.Flush()
			if err != nil {
				slog.Error("data export stream", "table", tables[i].Name, "err", err)
			}
			manifest["tables"].(map[string]int)[tables[i].Name] = n
		}
		if fw, err := zw.Create("manifest.json"); err == nil {
			b, _ := json.MarshalIndent(manifest, "", "  ")
			fw.Write(b)
		}
		zw.Close()
		w.Flush()
	})
	return nil
}
