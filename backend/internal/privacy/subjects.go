package privacy

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"btn-ops/internal/db"
	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
)

var likeEsc = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
var nonDigits = regexp.MustCompile(`[\s+\-().]`)
var digitsOnly = regexp.MustCompile(`^\d+$`)

type subjectHit struct {
	Type     string  `json:"type"`
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Site     *string `json:"site"`     // worker: plant code
	Identity *string `json:"identity"` // worker: national id; customer: TIN
	Phone    *string `json:"phone"`    // worker: MoMo number; customer: phone
	Email    *string `json:"email"`
	IsActive bool    `json:"isActive"`
}

// GET /subjects?type=worker|customer&q=
func (h *handler) search(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	qv := httpx.Query(c)
	typ := qv.OptStr("type").Trim().Lower().OneOf([]string{TypeWorker, TypeCustomer}, "privacy.v.subject_type").Get()
	q := qv.OptStr("q").Trim().Max(80, "privacy.v.max_80").Get()
	if err := qv.Err(); err != nil {
		return err
	}
	pg := httpx.Paginate(c, 25)
	var pat, digits, momo any // nil = no filter / never matches
	if q != "" {
		pat = "%" + likeEsc.Replace(q) + "%"
		if d := nonDigits.ReplaceAllString(q, ""); d != "" && digitsOnly.MatchString(d) {
			digits = "%" + d + "%"
			m := d
			if strings.HasPrefix(m, "250") {
				m = "0" + m[3:]
			}
			momo = "%" + m + "%"
		}
	}
	const workers = `SELECT 'worker' AS type, w.id, btrim(w."firstName" || ' ' || w."lastName") AS name, p.code AS site,
			w."nationalId" AS identity, w."momoNumber" AS phone, NULL::text AS email, w."isActive" AS active
		FROM "Worker" w JOIN "Plant" p ON p.id = w."plantId"
		WHERE $1::text IS NULL OR w."firstName" ILIKE $1 OR w."lastName" ILIKE $1 OR (w."firstName" || ' ' || w."lastName") ILIKE $1
			OR w."momoName" ILIKE $1 OR w."nationalId" ILIKE $1 OR w."momoNumber" ILIKE $1 OR w."nationalId" LIKE $2 OR w."momoNumber" LIKE $3`
	const customers = `SELECT 'customer' AS type, cu.id, cu.name, NULL::text AS site, cu.tin AS identity, cu.phone, cu.email, cu."isActive" AS active
		FROM "Customer" cu
		WHERE $1::text IS NULL OR cu.name ILIKE $1 OR cu.tin ILIKE $1 OR cu.phone ILIKE $1 OR cu.email ILIKE $1
			OR regexp_replace(COALESCE(cu.tin, ''), '\D', '', 'g') LIKE $2 OR regexp_replace(COALESCE(cu.phone, ''), '\D', '', 'g') LIKE $2
			OR regexp_replace(COALESCE(cu.phone, ''), '\D', '', 'g') LIKE $3`
	var src string
	switch typ {
	case TypeWorker:
		src = workers
	case TypeCustomer:
		src = customers
	default:
		src = workers + " UNION ALL " + customers
	}
	args := []any{pat, digits, momo}
	var total int
	if err := h.d.Pool.QueryRow(ctx, `SELECT count(*) FROM (`+src+`) s`, args...).Scan(&total); err != nil {
		return err
	}
	rows, err := h.d.Pool.Query(ctx, `SELECT type, id, name, site, identity, phone, email, active FROM (`+src+`) s
		ORDER BY active DESC, name, type, id LIMIT `+strconv.Itoa(pg.Limit)+` OFFSET `+strconv.Itoa(pg.Offset), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []subjectHit{}
	for rows.Next() {
		var s subjectHit
		if err := rows.Scan(&s.Type, &s.ID, &s.Name, &s.Site, &s.Identity, &s.Phone, &s.Email, &s.IsActive); err != nil {
			return err
		}
		items = append(items, s)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	httpx.NoStore(c)
	return c.JSON(httpx.NewPage(items, total, pg.Page, pg.PageSize))
}

// ── GET /subjects/:type/:id ─────────────────────────────────────────────────

type category struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

type subjectDetail struct {
	Type       string          `json:"type"`
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Profile    json.RawMessage `json:"profile"`
	Editable   []string        `json:"editable"`
	Categories []category      `json:"categories"`
	Totals     *statement      `json:"totals,omitempty"` // customers: statement totals
}

type statement struct {
	Invoiced int64 `json:"invoiced"`
	Paid     int64 `json:"paid"`
	Balance  int64 `json:"balance"`
}

func (h *handler) detail(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	typ, id, name, err := h.subjectParams(c)
	if err != nil {
		return err
	}
	out := subjectDetail{Type: typ, ID: id, Name: name}
	if out.Profile, err = profileJSON(ctx, h.d.Pool, typ, id); err != nil {
		return err
	}
	if out.Editable, err = editableFields(ctx, h.d.Pool, typ); err != nil {
		return err
	}
	if out.Categories, err = counts(ctx, h.d.Pool, typ, id); err != nil {
		return err
	}
	if typ == TypeCustomer {
		st, err := statementTotals(ctx, h.d.Pool, id)
		if err != nil {
			return err
		}
		out.Totals = &st
	}
	httpx.NoStore(c)
	return c.JSON(out)
}

func profileJSON(ctx context.Context, q db.DBTX, typ, id string) (json.RawMessage, error) {
	var b []byte
	var err error
	if typ == TypeWorker {
		err = q.QueryRow(ctx, `SELECT to_jsonb(w) || jsonb_build_object('plant', jsonb_build_object('id', p.id, 'code', p.code, 'name', p.name))
			FROM "Worker" w JOIN "Plant" p ON p.id = w."plantId" WHERE w.id = $1`, id).Scan(&b)
	} else {
		err = q.QueryRow(ctx, `SELECT to_jsonb(cu) FROM "Customer" cu WHERE cu.id = $1`, id).Scan(&b)
	}
	return b, err
}

// Shared predicates ($1 = subject id).
const (
	// Petty cash entries about a worker: anything paid to their MoMo number, and the wage entries the pay step
	// posted for their wage sheets under their name (or MoMo name).
	workerPettyCashWhere = `(e."momoNumber" IS NOT NULL AND e."momoNumber" = (SELECT "momoNumber" FROM "Worker" WHERE id = $1))
		OR (e."sourceType" = 'PayrollPeriod' AND e."sourceId" IN (SELECT "periodId" FROM "PayrollLine" WHERE "workerId" = $1)
			AND upper(e."payeeName") IN (SELECT upper(btrim("firstName" || ' ' || "lastName")) FROM "Worker" WHERE id = $1
				UNION SELECT upper("momoName") FROM "Worker" WHERE id = $1 AND "momoName" IS NOT NULL))`
	customerSalesWhere   = `s."customerId" = $1`
	customerReturnsWhere = `m."sourceType" = 'SaleReturn' AND m."sourceId" IN (SELECT id FROM "Sale" WHERE "customerId" = $1)`
)

func counts(ctx context.Context, q db.DBTX, typ, id string) ([]category, error) {
	var keys []string
	var sql string
	if typ == TypeWorker {
		keys = []string{"wageLines", "attendance", "pettyCash"}
		sql = `SELECT (SELECT count(*) FROM "PayrollLine" WHERE "workerId" = $1),
			(SELECT count(*) FROM "Attendance" WHERE "workerId" = $1),
			(SELECT count(*) FROM "PettyCashEntry" e WHERE ` + workerPettyCashWhere + `)`
	} else {
		keys = []string{"sales", "deliveries", "payments", "returns"}
		sql = `SELECT (SELECT count(*) FROM "Sale" s WHERE ` + customerSalesWhere + `),
			(SELECT count(*) FROM "Delivery" d WHERE d."saleId" IN (SELECT id FROM "Sale" s WHERE ` + customerSalesWhere + `)),
			(SELECT count(*) FROM "Payment" pm WHERE pm."saleId" IN (SELECT id FROM "Sale" s WHERE ` + customerSalesWhere + `)),
			(SELECT count(*) FROM "StockMovement" m WHERE ` + customerReturnsWhere + `)`
	}
	sql += `, (SELECT count(*) FROM privacy_correction WHERE subject_type = $2 AND subject_id = $1),
		(SELECT count(*) FROM "AuditLog" WHERE entity = $3 AND "entityId" = $1),
		(SELECT count(*) FROM privacy_access_log WHERE subject_type = $2 AND subject_id = $1)`
	keys = append(keys, "corrections", "audit", "accessLog")
	vals := make([]int, len(keys))
	dst := make([]any, len(keys))
	for i := range vals {
		dst[i] = &vals[i]
	}
	if err := q.QueryRow(ctx, sql, id, typ, entityName(typ)).Scan(dst...); err != nil {
		return nil, err
	}
	out := make([]category, 0, len(keys)+1)
	for i, k := range keys {
		out = append(out, category{k, vals[i]})
	}
	other, err := otherRecords(ctx, q, typ, id)
	if err != nil {
		return nil, err
	}
	n := 0
	for _, rows := range other {
		n += len(rows)
	}
	return append(out, category{"otherRecords", n}), nil
}

func statementTotals(ctx context.Context, q db.DBTX, id string) (statement, error) {
	var st statement
	err := q.QueryRow(ctx, `SELECT
			COALESCE((SELECT sum(sl.amount) FROM "SaleLine" sl JOIN "Sale" s ON s.id = sl."saleId" WHERE `+customerSalesWhere+` AND s.status = 'CONFIRMED'), 0)::bigint,
			COALESCE((SELECT sum(pm.amount) FROM "Payment" pm JOIN "Sale" s ON s.id = pm."saleId" WHERE `+customerSalesWhere+`), 0)::bigint`, id).
		Scan(&st.Invoiced, &st.Paid)
	st.Balance = st.Invoiced - st.Paid
	return st, err
}
