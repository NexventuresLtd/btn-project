package procurement

// Supplier directory (PRC-03): contacts, categories, TIN, payment terms, status and purchase history.

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/refdata"
	"btn-ops/internal/seq"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// Supplier statuses.
const (
	SupActive   = "ACTIVE"
	SupOnHold   = "ON_HOLD"
	SupBlocked  = "BLOCKED"
	SupInactive = "INACTIVE"
)

// SupplierStatuses in display order.
var SupplierStatuses = []string{SupActive, SupOnHold, SupBlocked, SupInactive}

var reTIN = regexp.MustCompile(`^[0-9A-Z][0-9A-Z\-]{4,19}$`)
var rePhone = regexp.MustCompile(`^\+?[0-9 ()\-]{6,20}$`)

// Contact is an additional supplier contact person.
type Contact struct {
	Name  string `json:"name"`
	Role  string `json:"role"`
	Phone string `json:"phone"`
	Email string `json:"email"`
}

// Supplier is a directory entry.
type Supplier struct {
	ID              string         `json:"id"`
	Code            string         `json:"code"`
	Name            string         `json:"name"`
	TIN             *string        `json:"tin"`
	ContactName     *string        `json:"contactName"`
	Phone           *string        `json:"phone"`
	Email           *string        `json:"email"`
	Address         *string        `json:"address"`
	Contacts        []Contact      `json:"contacts"`
	Categories      []string       `json:"categories"`
	CategoryNames   []string       `json:"categoryNames"`
	PaymentTerm     *string        `json:"paymentTerm"`
	PaymentTermName string         `json:"paymentTermName"`
	Status          string         `json:"status"`
	Notes           *string        `json:"notes"`
	CreatedAt       time.Time      `json:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`
	Stats           *SupplierStats `json:"stats,omitempty"`
}

// SupplierStats summarises the purchase history with a supplier.
type SupplierStats struct {
	Orders        int     `json:"orders"`
	OpenOrders    int     `json:"openOrders"`
	OrderedValue  int64   `json:"orderedValue"`
	ReceivedValue int64   `json:"receivedValue"`
	Invoiced      int64   `json:"invoiced"`
	LastOrderDate *string `json:"lastOrderDate"`
	Exceptions    int     `json:"exceptions"`
}

const supplierCols = `s.id, s.code, s.name, s.tin, s.contact_name, s.phone, s.email, s.address, s.contacts, s.categories, s.payment_term, s.status, s.notes, s.created_at, s.updated_at`

func scanSupplier(row pgx.Row, extra ...any) (Supplier, error) {
	var s Supplier
	var contacts []byte
	dst := append([]any{&s.ID, &s.Code, &s.Name, &s.TIN, &s.ContactName, &s.Phone, &s.Email, &s.Address, &contacts, &s.Categories, &s.PaymentTerm, &s.Status, &s.Notes, &s.CreatedAt, &s.UpdatedAt}, extra...)
	if err := row.Scan(dst...); err != nil {
		return s, err
	}
	s.Contacts = []Contact{}
	_ = json.Unmarshal(contacts, &s.Contacts)
	if s.Categories == nil {
		s.Categories = []string{}
	}
	return s, nil
}

func (s *Supplier) resolveNames(names map[string]string) {
	s.CategoryNames = make([]string, 0, len(s.Categories))
	for _, c := range s.Categories {
		c := c
		s.CategoryNames = append(s.CategoryNames, nameOf(names, "", &c))
	}
	s.PaymentTermName = nameOf(names, refdata.KindPaymentTerm, s.PaymentTerm)
}

// statsSQL aggregates orders per supplier (sent orders only).
const statsSQL = `SELECT o.supplier_id, count(*) AS orders,
	count(*) FILTER (WHERE o.status IN ('ORDERED','PARTIALLY_RECEIVED')) AS open_orders,
	COALESCE(SUM(o.total), 0) AS ordered,
	COALESCE(SUM((SELECT SUM(round(pl.received_qty * pl.unit_price)) FROM purchase_order_line pl WHERE pl.po_id = o.id)), 0)::bigint AS received,
	COALESCE(SUM((SELECT SUM(i.amount) FROM purchase_invoice i WHERE i.po_id = o.id)), 0)::bigint AS invoiced,
	to_char(max(o.order_date), 'YYYY-MM-DD') AS last_order,
	COALESCE(SUM((SELECT count(*) FROM goods_receipt g WHERE g.po_id = o.id AND g.has_exception)), 0)::int AS exceptions
	FROM purchase_order o WHERE o.status NOT IN ('DRAFT','CANCELLED') GROUP BY o.supplier_id`

func (h *handler) listSuppliers(c *fiber.Ctx) error {
	ctx, lang := httpx.Ctx(c), httpx.Lang(c)
	v := httpx.Query(c)
	qtext := v.OptStr("q").Trim().Max(100).Get()
	status := v.OptStr("status").Trim().Upper().Get()
	category := v.OptStr("category").Trim().Max(80).Get()
	if status != "" && !inList(SupplierStatuses, status) {
		v.Fail("status", "procurement.v.status")
	}
	if err := v.Err(); err != nil {
		return err
	}
	args := []any{}
	arg := func(x any) string { args = append(args, x); return "$" + strconv.Itoa(len(args)) }
	where := []string{"TRUE"}
	if qtext != "" {
		p := arg("%" + strings.ToLower(qtext) + "%")
		where = append(where, "(lower(s.name) LIKE "+p+" OR lower(s.code) LIKE "+p+" OR lower(COALESCE(s.tin,'')) LIKE "+p+" OR lower(COALESCE(s.contact_name,'')) LIKE "+p+" OR lower(COALESCE(s.phone,'')) LIKE "+p+")")
	}
	if status != "" {
		where = append(where, "s.status = "+arg(status))
	}
	if category != "" {
		where = append(where, arg(category)+" = ANY(s.categories)")
	}
	w := strings.Join(where, " AND ")
	var total int
	if err := h.d.Pool.QueryRow(ctx, `SELECT count(*) FROM supplier s WHERE `+w, args...).Scan(&total); err != nil {
		return err
	}
	pg := httpx.Paginate(c, 25)
	rows, err := h.d.Pool.Query(ctx, `SELECT `+supplierCols+`, COALESCE(st.orders,0), COALESCE(st.open_orders,0), COALESCE(st.ordered,0), COALESCE(st.received,0),
		COALESCE(st.invoiced,0), st.last_order, COALESCE(st.exceptions,0)
		FROM supplier s LEFT JOIN (`+statsSQL+`) st ON st.supplier_id = s.id WHERE `+w+`
		ORDER BY array_position(ARRAY['ACTIVE','ON_HOLD','BLOCKED','INACTIVE'], s.status), lower(s.name) LIMIT `+arg(pg.Limit)+` OFFSET `+arg(pg.Offset), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	names, err := refNames(ctx, h.d.Pool, lang, append(categoryKinds, refdata.KindPaymentTerm)...)
	if err != nil {
		return err
	}
	items := []Supplier{}
	for rows.Next() {
		st := &SupplierStats{}
		s, err := scanSupplier(rows, &st.Orders, &st.OpenOrders, &st.OrderedValue, &st.ReceivedValue, &st.Invoiced, &st.LastOrderDate, &st.Exceptions)
		if err != nil {
			return err
		}
		s.Stats = st
		s.resolveNames(names)
		items = append(items, s)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return c.JSON(httpx.NewPage(items, total, pg.Page, pg.PageSize))
}

// SupplierOption is a picker entry.
type SupplierOption struct {
	ID          string  `json:"id"`
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	Status      string  `json:"status"`
	PaymentTerm *string `json:"paymentTerm"`
}

func (h *handler) supplierOptions(c *fiber.Ctx) error {
	rows, err := h.d.Pool.Query(httpx.Ctx(c), `SELECT id, code, name, status, payment_term FROM supplier WHERE status <> 'INACTIVE' ORDER BY lower(name)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []SupplierOption{}
	for rows.Next() {
		var o SupplierOption
		if err := rows.Scan(&o.ID, &o.Code, &o.Name, &o.Status, &o.PaymentTerm); err != nil {
			return err
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return httpx.JSONCached(c, fiber.Map{"items": out}, "private, no-cache")
}

// SupplierDetail adds the spend per category and the latest orders.
type SupplierDetail struct {
	Supplier
	ByCategory []SpendRow `json:"byCategory"`
}

func (h *handler) getSupplier(c *fiber.Ctx) error {
	ctx, lang := httpx.Ctx(c), httpx.Lang(c)
	id := c.Params("id")
	st := &SupplierStats{}
	s, err := scanSupplier(h.d.Pool.QueryRow(ctx, `SELECT `+supplierCols+`, COALESCE(st.orders,0), COALESCE(st.open_orders,0), COALESCE(st.ordered,0), COALESCE(st.received,0),
		COALESCE(st.invoiced,0), st.last_order, COALESCE(st.exceptions,0) FROM supplier s LEFT JOIN (`+statsSQL+`) st ON st.supplier_id = s.id WHERE s.id = $1`, id),
		&st.Orders, &st.OpenOrders, &st.OrderedValue, &st.ReceivedValue, &st.Invoiced, &st.LastOrderDate, &st.Exceptions)
	if db.IsNoRows(err) {
		return httpx.NotFound("procurement.entity.supplier")
	}
	if err != nil {
		return err
	}
	s.Stats = st
	names, err := refNames(ctx, h.d.Pool, lang, append(categoryKinds, refdata.KindPaymentTerm)...)
	if err != nil {
		return err
	}
	s.resolveNames(names)
	rep, err := spendRows(ctx, h.d.Pool, lang, spendFilter{supplierID: id, groupBy: "category"}, names)
	if err != nil {
		return err
	}
	return c.JSON(SupplierDetail{Supplier: s, ByCategory: rep})
}

func (h *handler) supplierOrders(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	id := c.Params("id")
	var exists bool
	if err := h.d.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM supplier WHERE id=$1)`, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return httpx.NotFound("procurement.entity.supplier")
	}
	pg := httpx.Paginate(c, 25)
	items, total, err := listOrderRows(ctx, h.d.Pool, auth.MustUser(c), orderFilter{supplierID: id}, pg)
	if err != nil {
		return err
	}
	return c.JSON(httpx.NewPage(items, total, pg.Page, pg.PageSize))
}

// ───────────────────────────── writes ─────────────────────────────

type supplierInput struct {
	Name        string
	TIN         *string
	ContactName *string
	Phone       *string
	Email       *string
	Address     *string
	Contacts    []Contact
	Categories  []string
	PaymentTerm *string
	Status      string
	Notes       *string
}

// parseSupplier validates a supplier body; partial = PATCH (absent fields keep their value → has[field]=false).
func parseSupplier(v *httpx.V, lang i18n.Lang, partial bool) (supplierInput, map[string]bool) {
	var in supplierInput
	has := map[string]bool{}
	at := func(n int) string { return i18n.T(lang, "procurement.v.at_most", "n", n) }
	if !partial || v.Has("name") {
		has["name"] = true
		in.Name = v.Str("name").Trim().Collapse().Min(2, "procurement.v.supplier_name").Max(150, at(150)).Get()
	}
	opt := func(key string, max int) *string {
		if partial && !v.Has(key) {
			return nil
		}
		has[key] = true
		return v.NullStr(key).Trim().Max(max, at(max)).NilIfEmpty()
	}
	in.TIN = opt("tin", 20)
	if in.TIN != nil {
		t := strings.ToUpper(strings.ReplaceAll(*in.TIN, " ", ""))
		in.TIN = &t
		if !reTIN.MatchString(t) {
			v.Fail("tin", "procurement.v.tin")
		}
	}
	in.ContactName = opt("contactName", 120)
	in.Phone = opt("phone", 20)
	if in.Phone != nil && !rePhone.MatchString(*in.Phone) {
		v.Fail("phone", "procurement.v.phone")
	}
	if !partial || v.Has("email") {
		has["email"] = true
		e := v.NullStr("email").Trim().Lower().Max(120, at(120))
		if e.Get() != "" {
			e.Email("v.email")
		}
		in.Email = e.NilIfEmpty()
	}
	in.Address = opt("address", 300)
	in.Notes = opt("notes", 1000)
	in.PaymentTerm = opt("paymentTerm", 40)
	if !partial || v.Has("status") {
		has["status"] = true
		in.Status = v.OptStr("status").Trim().Upper().Default(SupActive).OneOf(SupplierStatuses, "procurement.v.status").Get()
	}
	if !partial || v.Has("contacts") {
		has["contacts"] = true
		cs := v.Arr("contacts", false)
		if len(cs) > 10 {
			v.Fail("contacts", i18n.T(lang, "procurement.v.contacts_max", "n", 10))
		}
		in.Contacts = []Contact{}
		for _, cv := range cs {
			ct := Contact{Name: cv.Str("name").Trim().Min(2, "procurement.v.contact_name").Max(120, at(120)).Get(),
				Role: cv.OptStr("role").Trim().Max(60, at(60)).Get(), Phone: cv.OptStr("phone").Trim().Max(20, at(20)).Get()}
			if ct.Phone != "" && !rePhone.MatchString(ct.Phone) {
				cv.Fail("phone", "procurement.v.phone")
			}
			if e := cv.OptStr("email").Trim().Lower().Max(120, at(120)); e.Get() != "" {
				ct.Email = e.Email("v.email").Get()
			}
			in.Contacts = append(in.Contacts, ct)
		}
	}
	if !partial || v.Has("categories") {
		has["categories"] = true
		in.Categories = v.StrArr("categories", false)
		if len(in.Categories) > 30 {
			v.Fail("categories", i18n.T(lang, "procurement.v.categories_max", "n", 30))
		}
		if in.Categories == nil {
			in.Categories = []string{}
		}
	}
	return in, has
}

func checkSupplierRefs(ctx context.Context, q db.DBTX, lang i18n.Lang, in *supplierInput, excludeID string, has map[string]bool) error {
	fields := map[string]string{}
	seen := map[string]bool{}
	cats := []string{}
	for i, cat := range in.Categories {
		cat = strings.TrimSpace(cat)
		ok, err := validCategory(ctx, q, cat)
		if err != nil {
			return err
		}
		if !ok {
			fields["categories."+strconv.Itoa(i)] = i18n.T(lang, "procurement.v.category")
		} else if !seen[cat] {
			seen[cat] = true
			cats = append(cats, cat)
		}
	}
	in.Categories = cats
	if in.PaymentTerm != nil {
		ok, err := refdata.Valid(ctx, q, refdata.KindPaymentTerm, *in.PaymentTerm)
		if err != nil {
			return err
		}
		if !ok {
			fields["paymentTerm"] = i18n.T(lang, "procurement.v.payment_term")
		}
	}
	if has["name"] {
		var dup bool
		if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM supplier WHERE lower(name) = lower($1) AND id <> $2)`, in.Name, excludeID).Scan(&dup); err != nil {
			return err
		}
		if dup {
			fields["name"] = i18n.T(lang, "procurement.err.supplier_exists")
		}
	}
	if in.TIN != nil {
		var name string
		err := q.QueryRow(ctx, `SELECT name FROM supplier WHERE tin = $1 AND id <> $2`, *in.TIN, excludeID).Scan(&name)
		if err == nil {
			fields["tin"] = i18n.T(lang, "procurement.err.tin_exists", "name", name)
		} else if !db.IsNoRows(err) {
			return err
		}
	}
	if len(fields) > 0 {
		return httpx.BadRequest("error.validation", fields)
	}
	return nil
}

func (h *handler) createSupplier(c *fiber.Ctx) error {
	ctx, u, lang := httpx.Ctx(c), auth.MustUser(c), httpx.Lang(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in, has := parseSupplier(v, lang, false)
	if err := v.Err(); err != nil {
		return err
	}
	uid := u.ID
	id, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (string, error) {
		if err := checkSupplierRefs(ctx, tx, lang, &in, "", has); err != nil {
			return "", err
		}
		code, err := seq.NextNumber(ctx, tx, "SUP", 4)
		if err != nil {
			return "", err
		}
		contacts, _ := json.Marshal(in.Contacts)
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO supplier (code, name, tin, contact_name, phone, email, address, contacts, categories, payment_term, status, notes, created_by_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING id`, code, in.Name, in.TIN, in.ContactName, in.Phone, in.Email, in.Address, contacts,
			in.Categories, in.PaymentTerm, in.Status, in.Notes, uid).Scan(&id); err != nil {
			return "", err
		}
		return id, audit.Record(c, tx, audit.Input{Action: "procurement.supplier_create", Entity: "Supplier", EntityID: id, Summary: "Added supplier " + code + " " + in.Name, After: in})
	})
	if err != nil {
		return err
	}
	invalidate()
	c.Status(201)
	return h.respondSupplier(c, id)
}

func (h *handler) respondSupplier(c *fiber.Ctx, id string) error {
	ctx, lang := httpx.Ctx(c), httpx.Lang(c)
	s, err := scanSupplier(h.d.Pool.QueryRow(ctx, `SELECT `+supplierCols+` FROM supplier s WHERE s.id = $1`, id))
	if err != nil {
		return err
	}
	names, err := refNames(ctx, h.d.Pool, lang, append(categoryKinds, refdata.KindPaymentTerm)...)
	if err != nil {
		return err
	}
	s.resolveNames(names)
	return c.JSON(s)
}

func (h *handler) updateSupplier(c *fiber.Ctx) error {
	ctx, lang := httpx.Ctx(c), httpx.Lang(c)
	id := c.Params("id")
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in, has := parseSupplier(v, lang, true)
	if err := v.Err(); err != nil {
		return err
	}
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		before, err := scanSupplier(tx.QueryRow(ctx, `SELECT `+supplierCols+` FROM supplier s WHERE s.id = $1 FOR UPDATE`, id))
		if db.IsNoRows(err) {
			return httpx.NotFound("procurement.entity.supplier")
		}
		if err != nil {
			return err
		}
		if err := checkSupplierRefs(ctx, tx, lang, &in, id, has); err != nil {
			return err
		}
		sets := []string{}
		args := []any{id}
		set := func(col string, val any) {
			args = append(args, val)
			sets = append(sets, col+" = $"+strconv.Itoa(len(args)))
		}
		if has["name"] {
			set("name", in.Name)
		}
		for key, col := range map[string]string{"tin": "tin", "contactName": "contact_name", "phone": "phone", "email": "email", "address": "address", "notes": "notes", "paymentTerm": "payment_term"} {
			if has[key] {
				set(col, map[string]*string{"tin": in.TIN, "contactName": in.ContactName, "phone": in.Phone, "email": in.Email, "address": in.Address, "notes": in.Notes, "paymentTerm": in.PaymentTerm}[key])
			}
		}
		if has["status"] {
			set("status", in.Status)
		}
		if has["contacts"] {
			b, _ := json.Marshal(in.Contacts)
			set("contacts", b)
		}
		if has["categories"] {
			set("categories", in.Categories)
		}
		if len(sets) == 0 {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE supplier SET `+strings.Join(sets, ", ")+`, updated_at = now() WHERE id = $1`, args...); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "procurement.supplier_update", Entity: "Supplier", EntityID: id, Summary: "Updated supplier " + before.Code + " " + before.Name, Before: before, After: in})
	})
	if err != nil {
		return err
	}
	invalidate()
	return h.respondSupplier(c, id)
}
