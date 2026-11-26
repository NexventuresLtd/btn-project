package privacy

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// LegalBasis is quoted in every export.
const LegalBasis = "Law N° 058/2021 of 13/10/2021 relating to the protection of personal data and privacy (Rwanda), art. 22 (right of access) and art. 27 (right to rectification)"

// otherRowCap bounds rows taken from each additional table that references the subject.
const otherRowCap = 5000

// section is one named part of the dossier (ordered).
type section struct {
	Key  string
	Data json.RawMessage
}

// dossier is everything held about one subject.
type dossier struct {
	Type, ID, Name string
	Sections       []section
	Other          map[string][]json.RawMessage
}

func (d *dossier) get(key string) json.RawMessage {
	for _, s := range d.Sections {
		if s.Key == key {
			return s.Data
		}
	}
	return nil
}

// rowsOf decodes a section into generic rows (for the PDF).
func (d *dossier) rowsOf(key string) []map[string]any {
	var out []map[string]any
	_ = json.Unmarshal(d.get(key), &out)
	return out
}

func jsonAgg(ctx context.Context, q db.DBTX, inner, order string, args ...any) (json.RawMessage, error) {
	var b []byte
	err := q.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(x.j ORDER BY `+order+`), '[]'::jsonb) FROM (`+inner+`) x`, args...).Scan(&b)
	return b, err
}

const auditSQL = `SELECT jsonb_build_object('id', a.id, 'createdAt', a."createdAt", 'action', a.action, 'summary', a.summary,
		'user', u.name, 'userEmail', u.email, 'ip', a.ip, 'deviceId', a."deviceId", 'before', a."before", 'after', a."after") AS j, a."createdAt" AS k1, a.id AS k2
	FROM "AuditLog" a LEFT JOIN "User" u ON u.id = a."userId" WHERE a.entity = $2 AND a."entityId" = $1`

const accessSQL = `SELECT jsonb_build_object('id', l.id, 'createdAt', l.created_at, 'action', l.action, 'route', l.route, 'user', u.name,
		'userEmail', u.email, 'ip', l.ip, 'deviceId', l.device_id) AS j, l.created_at AS k1, l.id AS k2
	FROM privacy_access_log l LEFT JOIN "User" u ON u.id = l.user_id WHERE l.subject_type = $2 AND l.subject_id = $1`

const correctionsSQL = `SELECT jsonb_build_object('id', pc.id, 'createdAt', pc.created_at, 'user', u.name, 'reason', pc.reason,
		'requestRef', pc.request_ref, 'changes', pc.changes) AS j, pc.created_at AS k1, pc.id AS k2
	FROM privacy_correction pc LEFT JOIN "User" u ON u.id = pc.user_id WHERE pc.subject_type = $2 AND pc.subject_id = $1`

// collect builds the dossier of a subject (type already validated, subject exists).
func collect(ctx context.Context, q db.DBTX, typ, id, name string) (*dossier, error) {
	d := &dossier{Type: typ, ID: id, Name: name}
	add := func(key string, b json.RawMessage, err error) error {
		if err != nil {
			return fmt.Errorf("privacy export %s: %w", key, err)
		}
		d.Sections = append(d.Sections, section{key, b})
		return nil
	}
	prof, err := profileJSON(ctx, q, typ, id)
	if err := add("profile", prof, err); err != nil {
		return nil, err
	}
	type part struct{ key, inner, order string }
	var parts []part
	if typ == TypeWorker {
		parts = []part{
			{"wageLines", `SELECT to_jsonb(l) || jsonb_build_object('periodNumber', pp.number, 'periodStart', pp."startDate", 'periodEnd', pp."endDate",
					'periodStatus', pp.status, 'paidAt', pp."paidAt", 'site', pl.code, 'momoRef', l."paymentRef") AS j, pp."startDate" AS k1, pp.number AS k2
				FROM "PayrollLine" l JOIN "PayrollPeriod" pp ON pp.id = l."periodId" JOIN "Plant" pl ON pl.id = pp."plantId" WHERE l."workerId" = $1`, "x.k1, x.k2"},
			{"attendance", `SELECT to_jsonb(a) || jsonb_build_object('site', pl.code, 'recordedBy', u.name) AS j, a.date AS k1, a.id AS k2
				FROM "Attendance" a JOIN "Plant" pl ON pl.id = a."plantId" LEFT JOIN "User" u ON u.id = a."recordedById" WHERE a."workerId" = $1`, "x.k1, x.k2"},
			{"pettyCash", `SELECT to_jsonb(e) || jsonb_build_object('site', pl.code) AS j, e.date AS k1, e.number AS k2
				FROM "PettyCashEntry" e LEFT JOIN "Plant" pl ON pl.id = e."plantId" WHERE ` + workerPettyCashWhere, "x.k1, x.k2"},
		}
	} else {
		parts = []part{
			{"sales", `SELECT to_jsonb(s) || jsonb_build_object('site', pl.code,
					'total', (SELECT COALESCE(sum(amount), 0) FROM "SaleLine" WHERE "saleId" = s.id),
					'lines', (SELECT COALESCE(jsonb_agg(to_jsonb(sl) || jsonb_build_object('product', p.code, 'productName', p.name) ORDER BY p."sortOrder", sl.id), '[]'::jsonb)
						FROM "SaleLine" sl JOIN "Product" p ON p.id = sl."productId" WHERE sl."saleId" = s.id)) AS j, s.date AS k1, s.number AS k2
				FROM "Sale" s JOIN "Plant" pl ON pl.id = s."plantId" WHERE ` + customerSalesWhere, "x.k1, x.k2"},
			{"deliveries", `SELECT to_jsonb(dl) || jsonb_build_object('saleNumber', s.number, 'site', pl.code,
					'lines', (SELECT COALESCE(jsonb_agg(to_jsonb(x2) || jsonb_build_object('product', p.code, 'productName', p.name) ORDER BY p."sortOrder", x2.id), '[]'::jsonb)
						FROM "DeliveryLine" x2 JOIN "Product" p ON p.id = x2."productId" WHERE x2."deliveryId" = dl.id)) AS j, dl.date AS k1, dl."dnNumber" AS k2
				FROM "Delivery" dl JOIN "Sale" s ON s.id = dl."saleId" JOIN "Plant" pl ON pl.id = dl."plantId" WHERE ` + customerSalesWhere, "x.k1, x.k2"},
			{"payments", `SELECT to_jsonb(pm) || jsonb_build_object('saleNumber', s.number, 'receivedBy', u.name) AS j, pm.date AS k1, pm.id AS k2
				FROM "Payment" pm JOIN "Sale" s ON s.id = pm."saleId" LEFT JOIN "User" u ON u.id = pm."receivedById" WHERE ` + customerSalesWhere, "x.k1, x.k2"},
			{"returns", `SELECT to_jsonb(m) || jsonb_build_object('saleNumber', s.number, 'product', p.code, 'productName', p.name, 'site', pl.code) AS j, m.date AS k1, m.id AS k2
				FROM "StockMovement" m JOIN "Sale" s ON s.id = m."sourceId" JOIN "Product" p ON p.id = m."productId" JOIN "Plant" pl ON pl.id = m."plantId"
				WHERE ` + customerReturnsWhere, "x.k1, x.k2"},
		}
	}
	parts = append(parts,
		part{"corrections", correctionsSQL, "x.k1, x.k2"},
		part{"audit", auditSQL, "x.k1, x.k2"},
		part{"accessLog", accessSQL, "x.k1 DESC, x.k2"},
	)
	for _, p := range parts {
		var args []any
		args = append(args, id)
		if strings.Contains(p.inner, "$2") {
			args = append(args, typ)
			if p.key == "audit" {
				args[1] = entityName(typ)
			}
		}
		b, err := jsonAgg(ctx, q, p.inner, p.order, args...)
		if err := add(p.key, b, err); err != nil {
			return nil, err
		}
	}
	if typ == TypeCustomer {
		st, err := statementTotals(ctx, q, id)
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(st)
		_ = add("statementTotals", b, nil)
	}
	other, err := otherRecords(ctx, q, typ, id)
	if err != nil {
		return nil, err
	}
	d.Other = other
	return d, nil
}

// covered tables are exported in their own sections; any other table with a column referencing the subject
// (added by later modules: contracts, leave, contacts, orders...) is exported generically.
var covered = map[string]map[string]bool{
	TypeWorker:   {"PayrollLine": true, "Attendance": true},
	TypeCustomer: {"Sale": true},
}

var subjectCols = map[string][]string{
	TypeWorker:   {"workerId", "worker_id"},
	TypeCustomer: {"customerId", "customer_id"},
}

var safeIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func otherRecords(ctx context.Context, q db.DBTX, typ, id string) (map[string][]json.RawMessage, error) {
	rows, err := q.Query(ctx, `SELECT c.table_name, c.column_name FROM information_schema.columns c
		JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name AND t.table_type = 'BASE TABLE'
		WHERE c.table_schema = current_schema() AND c.column_name = ANY($1) ORDER BY c.table_name, c.column_name`, subjectCols[typ])
	if err != nil {
		return nil, err
	}
	type ref struct{ table, col string }
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.table, &r.col); err != nil {
			rows.Close()
			return nil, err
		}
		if !covered[typ][r.table] && safeIdent.MatchString(r.table) && safeIdent.MatchString(r.col) {
			refs = append(refs, r)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := map[string][]json.RawMessage{}
	for _, r := range refs {
		rs, err := q.Query(ctx, fmt.Sprintf(`SELECT to_jsonb(t) FROM %s t WHERE %s = $1 LIMIT %d`,
			pgx.Identifier{r.table}.Sanitize(), pgx.Identifier{r.col}.Sanitize(), otherRowCap), id)
		if err != nil {
			return nil, err
		}
		for rs.Next() {
			var b []byte
			if err := rs.Scan(&b); err != nil {
				rs.Close()
				return nil, err
			}
			out[r.table] = append(out[r.table], b)
		}
		rs.Close()
		if err := rs.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// MarshalJSON renders the dossier as an ordered document.
func (d *dossier) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteString("{")
	for i, s := range d.Sections {
		if i > 0 {
			b.WriteString(",")
		}
		k, _ := json.Marshal(s.Key)
		b.Write(k)
		b.WriteString(":")
		b.Write(s.Data)
	}
	other, err := json.Marshal(d.Other)
	if err != nil {
		return nil, err
	}
	b.WriteString(`,"otherRecords":`)
	b.Write(other)
	b.WriteString("}")
	return []byte(b.String()), nil
}

type exportDoc struct {
	Format      string            `json:"format"`
	GeneratedAt string            `json:"generatedAt"`
	GeneratedBy map[string]string `json:"generatedBy"`
	LegalBasis  string            `json:"legalBasis"`
	Subject     map[string]string `json:"subject"`
	Data        *dossier          `json:"data"`
}

var unsafeFile = regexp.MustCompile(`[^A-Za-z0-9]+`)

func fileStem(typ, name string) string {
	n := strings.Trim(unsafeFile.ReplaceAllString(name, "-"), "-")
	if len(n) > 60 {
		n = n[:60]
	}
	if n == "" {
		n = typ
	}
	return fmt.Sprintf("Personal-data_%s_%s_%s", typ, n, dates.TodayISO())
}

// recordExport writes the access-log row and the privacy.export audit row in one transaction.
func (h *handler) recordExport(c *fiber.Ctx, typ, id, name, action, format string) error {
	ctx := httpx.Ctx(c)
	return db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		if err := LogAccess(ctx, tx, c, typ, id, action); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "privacy.export", Entity: entityName(typ), EntityID: id,
			Summary: fmt.Sprintf("Subject access export (%s) of the personal data of %s", format, name),
			After:   map[string]string{"subjectType": typ, "subjectId": id, "format": format}})
	})
}

// GET /subjects/:type/:id/export
func (h *handler) exportJSON(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	typ, id, name, err := h.subjectParams(c)
	if err != nil {
		return err
	}
	d, err := collect(ctx, h.d.Pool, typ, id, name)
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(exportDoc{
		Format: "btn-ops.personal-data/1", GeneratedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		GeneratedBy: map[string]string{"id": u.ID, "name": u.Name, "email": u.Email}, LegalBasis: LegalBasis,
		Subject: map[string]string{"type": typ, "id": id, "name": name}, Data: d,
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := h.recordExport(c, typ, id, name, ActionExport, "json"); err != nil {
		return err
	}
	c.Set(fiber.HeaderContentType, "application/json; charset=utf-8")
	c.Set(fiber.HeaderContentDisposition, fmt.Sprintf(`attachment; filename="%s.json"`, fileStem(typ, name)))
	c.Set(fiber.HeaderCacheControl, "private, no-store")
	return c.Send(body)
}
