package sales

import (
	"context"
	"fmt"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/masterdata"
	"btn-ops/internal/refdata"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// Client database (ORD-01), duplicate warning (ORD-02), communication log + searchable history (ORD-07).
//
//	POST  /customers                 create (409 possible_duplicate with candidates unless acknowledgeDuplicates)
//	POST  /customers/duplicates      live duplicate check {name, tin, phone, contacts, excludeId}
//	GET   /customers/:id/profile     client + contacts + figures
//	PATCH /customers/:id             edit (same duplicate rule when name/TIN/phones change)
//	GET   /customers/:id/orders      searchable order history (q, status, from, to, page)
//	GET   /customers/:id/communications, POST …  calls, e-mails, meetings, notes

var customerStatuses = []string{"ACTIVE", "INACTIVE", "BLOCKED"}

type contactIn struct {
	Name               string
	Role, Phone, Email *string
	IsPrimary          bool
}

type customerIn struct {
	Name                                 string
	ClientType, TIN, Phone, Email        *string
	Address, Notes, PaymentTerm, Blocked *string
	Status                               string
	Contacts                             []contactIn
	Has                                  map[string]bool
	HasContacts, Acknowledge             bool
}

func (x in) parseCustomer(partial bool) customerIn {
	v := x.v
	c := customerIn{Has: map[string]bool{}}
	for _, k := range []string{"name", "clientType", "tin", "phone", "email", "address", "notes", "paymentTerm", "status", "blockedReason"} {
		c.Has[k] = v.Has(k)
	}
	if !partial || v.Has("name") {
		c.Name = v.Str("name").Trim().Collapse().Min(2, "sales.v.client_name").Max(160, x.atMost(160)).Upper().Get()
	}
	c.ClientType = x.optCode("clientType")
	c.TIN = x.optText("tin", 30)
	c.Phone = x.optText("phone", 40)
	em := v.NullStr("email").Trim().Lower().Max(160, x.atMost(160))
	if em.Present() && em.Get() != "" {
		em.Email("v.email")
	}
	c.Email = em.NilIfEmpty()
	c.Address = x.optText("address", 300)
	c.Notes = x.optText("notes", 2000)
	c.PaymentTerm = x.optCode("paymentTerm")
	c.Blocked = x.optText("blockedReason", 300)
	st := v.OptStr("status")
	if !partial {
		st.Default("ACTIVE")
	}
	c.Status = st.OneOf(customerStatuses, "sales.v.client_status").Get()
	if c.Status == "BLOCKED" && c.Blocked == nil {
		v.Fail("blockedReason", "sales.v.blocked_reason")
	}
	if v.Has("contacts") {
		c.HasContacts = true
		arr := v.Arr("contacts", true)
		if len(arr) > 20 {
			v.Fail("contacts", "sales.v.max_contacts")
		}
		for _, cv := range arr {
			ci := contactIn{Name: cv.Str("name").Trim().Collapse().Min(1, "v.required").Max(120, x.atMost(120)).Get()}
			ci.Role = cv.NullStr("role").Trim().Max(80, x.atMost(80)).NilIfEmpty()
			ci.Phone = cv.NullStr("phone").Trim().Max(40, x.atMost(40)).NilIfEmpty()
			e := cv.NullStr("email").Trim().Lower().Max(160, x.atMost(160))
			if e.Present() && e.Get() != "" {
				e.Email("v.email")
			}
			ci.Email = e.NilIfEmpty()
			ci.IsPrimary = cv.OptBool("isPrimary").Default(false).Get()
			c.Contacts = append(c.Contacts, ci)
		}
	}
	c.Acknowledge = v.OptBool("acknowledgeDuplicates").Default(false).Get()
	return c
}

// dupCandidates loads every client with its phones (one query); scoring is in Go (dupes.go).
func dupCandidates(ctx context.Context, q db.DBTX) ([]dupCandidate, error) {
	rows, err := q.Query(ctx, `SELECT c.id, c.name, c.tin, c.phone, c.status,
		COALESCE(array_agg(cc.phone) FILTER (WHERE cc.phone IS NOT NULL), '{}')
		FROM "Customer" c LEFT JOIN customer_contact cc ON cc.customer_id = c.id GROUP BY c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dupCandidate
	for rows.Next() {
		var c dupCandidate
		var phones []string
		if err := rows.Scan(&c.ID, &c.Name, &c.TIN, &c.Phone, &c.Status, &phones); err != nil {
			return nil, err
		}
		if c.Phone != nil {
			phones = append(phones, *c.Phone)
		}
		c.Phones = phones
		out = append(out, c)
	}
	return out, rows.Err()
}

func probeOf(c customerIn) dupProbe {
	p := dupProbe{Name: c.Name}
	if c.TIN != nil {
		p.TIN = *c.TIN
	}
	if c.Phone != nil {
		p.Phones = append(p.Phones, *c.Phone)
	}
	for _, ct := range c.Contacts {
		if ct.Phone != nil {
			p.Phones = append(p.Phones, *ct.Phone)
		}
	}
	return p
}

// duplicateCheck: POST /customers/duplicates — live warning while typing.
func (h *handler) duplicateCheck(c *fiber.Ctx) error {
	r := newRq(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	ci := newIn(v, r.lang).parseCustomer(true)
	exclude := v.OptStr("excludeId").Trim().Max(40).Get()
	if err := v.Err(); err != nil {
		return err
	}
	all, err := dupCandidates(r.ctx, h.d.Pool)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"candidates": findDuplicates(probeOf(ci), all, exclude)})
}

func (o *op) validateCustomerRefs(ci customerIn) error {
	f := map[string]string{}
	if ci.ClientType != nil {
		ok, err := refdata.Valid(o.ctx, o.tx, refdata.KindClientType, *ci.ClientType)
		if err != nil {
			return err
		}
		if !ok {
			f["clientType"] = o.t("sales.v.client_type")
		}
	}
	if ci.PaymentTerm != nil {
		ok, err := refdata.Valid(o.ctx, o.tx, refdata.KindPaymentTerm, *ci.PaymentTerm)
		if err != nil {
			return err
		}
		if !ok {
			f["paymentTerm"] = o.t("sales.v.payment_term")
		}
	}
	if len(f) > 0 {
		return httpx.BadRequest("error.validation", f)
	}
	return nil
}

// duplicateError is the 409 body carrying the candidates (the UI shows them and lets the user proceed).
type duplicateError struct{ candidates []dupCandidate }

func (duplicateError) Error() string { return "possible duplicate" }

func (h *handler) dupResponse(c *fiber.Ctx, r rq, d duplicateError) error {
	return c.Status(409).JSON(fiber.Map{"error": fiber.Map{"code": "possible_duplicate", "message": r.t("sales.err.possible_duplicate"), "candidates": d.candidates}})
}

func (o *op) writeContacts(customerID string, cs []contactIn) error {
	if _, err := o.tx.Exec(o.ctx, `DELETE FROM customer_contact WHERE customer_id = $1`, customerID); err != nil {
		return err
	}
	for i, ct := range cs {
		if _, err := o.tx.Exec(o.ctx, `INSERT INTO customer_contact (customer_id, name, role, phone, email, is_primary, sort_order) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			customerID, ct.Name, ct.Role, ct.Phone, ct.Email, ct.IsPrimary, i); err != nil {
			return err
		}
	}
	return nil
}

func (h *handler) createCustomer(c *fiber.Ctx) error {
	r := newRq(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	ci := newIn(v, r.lang).parseCustomer(false)
	if err := v.Err(); err != nil {
		return err
	}
	id, err := db.TxVal(r.ctx, h.d.Pool, func(tx pgx.Tx) (string, error) {
		o := r.op(tx)
		if err := o.validateCustomerRefs(ci); err != nil {
			return "", err
		}
		if !ci.Acknowledge {
			all, err := dupCandidates(r.ctx, tx)
			if err != nil {
				return "", err
			}
			if d := findDuplicates(probeOf(ci), all, ""); len(d) > 0 {
				return "", duplicateError{d}
			}
		}
		var id string
		if err := tx.QueryRow(r.ctx, `INSERT INTO "Customer" (id, name, tin, phone, email, address, "clientType", status, notes, "paymentTerm", "blockedReason", "isActive")
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id`, db.NewID(), ci.Name, ci.TIN, ci.Phone, ci.Email, ci.Address, ci.ClientType, ci.Status,
			ci.Notes, ci.PaymentTerm, ci.Blocked, ci.Status == "ACTIVE").Scan(&id); err != nil {
			return "", err
		}
		if err := o.writeContacts(id, ci.Contacts); err != nil {
			return "", err
		}
		return id, o.audit(audit.Input{Action: "sales.customer.create", Entity: "Customer", EntityID: id, Summary: "Created client " + ci.Name,
			After: map[string]any{"name": ci.Name, "clientType": ci.ClientType, "tin": ci.TIN, "status": ci.Status, "contacts": len(ci.Contacts), "duplicatesAcknowledged": ci.Acknowledge}})
	})
	if d, ok := err.(duplicateError); ok {
		return h.dupResponse(c, r, d)
	}
	if err != nil {
		return err
	}
	invalidate()
	masterdata.InvalidateLookups()
	p, err := h.s.customerProfile(r.ctx, h.d.Pool, r.lang, id)
	if err != nil {
		return err
	}
	return c.Status(201).JSON(p)
}

func (h *handler) updateCustomer(c *fiber.Ctx) error {
	r := newRq(c)
	id := c.Params("id")
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	ci := newIn(v, r.lang).parseCustomer(true)
	if err := v.Err(); err != nil {
		return err
	}
	err = db.Tx(r.ctx, h.d.Pool, func(tx pgx.Tx) error {
		o := r.op(tx)
		var cur customer
		var phones []string
		if err := tx.QueryRow(r.ctx, `SELECT name, tin, phone, status FROM "Customer" WHERE id = $1 FOR UPDATE`, id).Scan(&cur.Name, &cur.TIN, &cur.Phone, &cur.Status); err != nil {
			if db.IsNoRows(err) {
				return httpx.NotFound("entity.customer")
			}
			return err
		}
		if err := o.validateCustomerRefs(ci); err != nil {
			return err
		}
		if ci.Status == "BLOCKED" && ci.Blocked == nil {
			return httpx.BadRequest("error.validation", map[string]string{"blockedReason": r.t("sales.v.blocked_reason")})
		}
		identity := ci.Has["name"] || ci.Has["tin"] || ci.Has["phone"] || ci.HasContacts
		if identity && !ci.Acknowledge {
			probe := probeOf(ci)
			if !ci.Has["name"] {
				probe.Name = cur.Name
			}
			if !ci.Has["tin"] && cur.TIN != nil {
				probe.TIN = *cur.TIN
			}
			if !ci.Has["phone"] && cur.Phone != nil {
				probe.Phones = append(probe.Phones, *cur.Phone)
			}
			_ = phones
			all, err := dupCandidates(r.ctx, tx)
			if err != nil {
				return err
			}
			if d := findDuplicates(probe, all, id); len(d) > 0 {
				return duplicateError{d}
			}
		}
		sets := []string{`"updatedAt" = now()`}
		args := []any{id}
		add := func(col string, val any) {
			args = append(args, val)
			sets = append(sets, fmt.Sprintf(`%s = $%d`, col, len(args)))
		}
		cols := map[string]struct {
			col string
			val any
		}{"name": {"name", ci.Name}, "clientType": {`"clientType"`, ci.ClientType}, "tin": {"tin", ci.TIN}, "phone": {"phone", ci.Phone}, "email": {"email", ci.Email},
			"address": {"address", ci.Address}, "notes": {"notes", ci.Notes}, "paymentTerm": {`"paymentTerm"`, ci.PaymentTerm}, "status": {"status", ci.Status},
			"blockedReason": {`"blockedReason"`, ci.Blocked}}
		for _, k := range []string{"name", "clientType", "tin", "phone", "email", "address", "notes", "paymentTerm", "status", "blockedReason"} {
			if ci.Has[k] {
				add(cols[k].col, cols[k].val)
			}
		}
		if _, err := tx.Exec(r.ctx, `UPDATE "Customer" SET `+strings.Join(sets, ", ")+` WHERE id = $1`, args...); err != nil {
			return err
		}
		if ci.HasContacts {
			if err := o.writeContacts(id, ci.Contacts); err != nil {
				return err
			}
		}
		return o.audit(audit.Input{Action: "sales.customer.update", Entity: "Customer", EntityID: id, Summary: "Edited client " + cur.Name,
			Before: map[string]any{"name": cur.Name, "tin": cur.TIN, "phone": cur.Phone, "status": cur.Status},
			After:  map[string]any{"changed": ci.Has, "contacts": len(ci.Contacts), "status": ci.Status, "duplicatesAcknowledged": ci.Acknowledge}})
	})
	if d, ok := err.(duplicateError); ok {
		return h.dupResponse(c, r, d)
	}
	if err != nil {
		return err
	}
	invalidate()
	masterdata.InvalidateLookups()
	p, err := h.s.customerProfile(r.ctx, h.d.Pool, r.lang, id)
	if err != nil {
		return err
	}
	return c.JSON(p)
}

type contactOut struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Role      *string `json:"role"`
	Phone     *string `json:"phone"`
	Email     *string `json:"email"`
	IsPrimary bool    `json:"isPrimary"`
}

type profileOut struct {
	Customer        customer     `json:"customer"`
	ClientTypeName  *string      `json:"clientTypeName"`
	PaymentTermName *string      `json:"paymentTermName"`
	BlockedReason   *string      `json:"blockedReason"`
	Contacts        []contactOut `json:"contacts"`
	Summary         struct {
		Orders      int64   `json:"orders"`
		OpenOrders  int64   `json:"openOrders"`
		TotalSales  int64   `json:"totalSales"`
		Paid        int64   `json:"paid"`
		Outstanding int64   `json:"outstanding"`
		Overdue     int64   `json:"overdue"`
		LastOrder   *string `json:"lastOrder"`
		Comms       int64   `json:"communications"`
	} `json:"summary"`
}

func (s *service) customerProfile(ctx context.Context, q db.DBTX, lang i18n.Lang, id string) (*profileOut, error) {
	var p profileOut
	var created time.Time
	c := &p.Customer
	err := q.QueryRow(ctx, `SELECT id, name, tin, phone, email, address, "isActive", "createdAt", "clientType", status, notes, "paymentTerm", "blockedReason" FROM "Customer" WHERE id = $1`, id).
		Scan(&c.ID, &c.Name, &c.TIN, &c.Phone, &c.Email, &c.Address, &c.IsActive, &created, &c.ClientType, &c.Status, &c.Notes, &c.PaymentTerm, &p.BlockedReason)
	if err != nil {
		if db.IsNoRows(err) {
			return nil, httpx.NotFound("entity.customer")
		}
		return nil, err
	}
	c.CreatedAt = jt(created)
	if c.ClientType != nil {
		n := refdata.Name(ctx, q, refdata.KindClientType, *c.ClientType, lang)
		p.ClientTypeName = &n
	}
	if c.PaymentTerm != nil {
		n := refdata.Name(ctx, q, refdata.KindPaymentTerm, *c.PaymentTerm, lang)
		p.PaymentTermName = &n
	}
	rows, err := q.Query(ctx, `SELECT id, name, role, phone, email, is_primary FROM customer_contact WHERE customer_id = $1 ORDER BY sort_order, created_at`, id)
	if err != nil {
		return nil, err
	}
	p.Contacts = []contactOut{}
	for rows.Next() {
		var ct contactOut
		if err := rows.Scan(&ct.ID, &ct.Name, &ct.Role, &ct.Phone, &ct.Email, &ct.IsPrimary); err != nil {
			rows.Close()
			return nil, err
		}
		p.Contacts = append(p.Contacts, ct)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var last *time.Time
	err = q.QueryRow(ctx, `WITH s AS (SELECT id, date, status::text AS legacy, "orderStatus", "dueDate" FROM "Sale" WHERE "customerId" = $1),
		l AS (SELECT "saleId", SUM(amount) a FROM "SaleLine" WHERE "saleId" IN (SELECT id FROM s) GROUP BY 1),
		p AS (SELECT "saleId", SUM(amount) a FROM "Payment" WHERE "saleId" IN (SELECT id FROM s) GROUP BY 1)
		SELECT COUNT(*), COUNT(*) FILTER (WHERE "orderStatus" NOT IN ('CLOSED','CANCELLED')),
		  COALESCE(SUM(l.a) FILTER (WHERE legacy = 'CONFIRMED'), 0)::bigint, COALESCE(SUM(p.a) FILTER (WHERE legacy = 'CONFIRMED'), 0)::bigint,
		  COALESCE(SUM(GREATEST(COALESCE(l.a,0) - COALESCE(p.a,0), 0)) FILTER (WHERE legacy = 'CONFIRMED'), 0)::bigint,
		  COALESCE(SUM(GREATEST(COALESCE(l.a,0) - COALESCE(p.a,0), 0)) FILTER (WHERE legacy = 'CONFIRMED' AND "dueDate" < current_date), 0)::bigint,
		  MAX(date), (SELECT COUNT(*) FROM customer_communication WHERE customer_id = $1)
		FROM s LEFT JOIN l ON l."saleId" = s.id LEFT JOIN p ON p."saleId" = s.id`, id).
		Scan(&p.Summary.Orders, &p.Summary.OpenOrders, &p.Summary.TotalSales, &p.Summary.Paid, &p.Summary.Outstanding, &p.Summary.Overdue, &last, &p.Summary.Comms)
	if err != nil {
		return nil, err
	}
	if last != nil {
		d := dates.ToISO(*last)
		p.Summary.LastOrder = &d
	}
	return &p, nil
}

func (h *handler) customerProfileH(c *fiber.Ctx) error {
	r := newRq(c)
	p, err := h.s.customerProfile(r.ctx, h.d.Pool, r.lang, c.Params("id"))
	if err != nil {
		return err
	}
	return c.JSON(p)
}

// customerOrders: GET /customers/:id/orders — the client's complete, searchable order history (ORD-07).
func (h *handler) customerOrders(c *fiber.Ctx) error {
	r := newRq(c)
	q := httpx.Query(c)
	from, to := optDate(q, "from"), optDate(q, "to")
	status := queryEnum(q, "status", OrderStatuses...)
	search := q.OptStr("q").Trim().Max(80, r.t("sales.v.at_most", "n", 80)).Get()
	if err := q.Err(); err != nil {
		return err
	}
	if from == "" {
		from = "2000-01-01"
	}
	if to == "" {
		to = "2999-12-31"
	}
	pct, err := h.approvals(r.ctx)
	if err != nil {
		return err
	}
	out, err := h.s.listSales(r.ctx, salesFilters{From: from, To: to, CustomerID: c.Params("id"), PlantID: auth.ScopedPlantID(r.u), Status: status, Q: search}, httpx.Paginate(c, 25), pct)
	if err != nil {
		return err
	}
	return c.JSON(out)
}

// ───────────────────────────── communication log ─────────────────────────────

var commKinds = []string{"CALL", "EMAIL", "MEETING", "NOTE"}

type commOut struct {
	ID          string  `json:"id"`
	Kind        string  `json:"kind"`
	Subject     string  `json:"subject"`
	Body        *string `json:"body"`
	ContactName *string `json:"contactName"`
	OccurredAt  jsTime  `json:"occurredAt"`
	SaleID      *string `json:"saleId"`
	SaleNumber  *string `json:"saleNumber"`
	By          person  `json:"by"`
	CreatedAt   jsTime  `json:"createdAt"`
}

func listComms(ctx context.Context, q db.DBTX, customerID, saleID, kind, search string, limit, offset int) ([]commOut, error) {
	b := &qb{}
	conds := []string{"m.customer_id = " + b.arg(customerID)}
	if saleID != "" {
		conds = append(conds, "m.sale_id = "+b.arg(saleID))
	}
	if kind != "" {
		conds = append(conds, "m.kind = "+b.arg(kind))
	}
	if search != "" {
		l := b.arg(likeArg(search))
		conds = append(conds, fmt.Sprintf("(m.subject ILIKE %[1]s OR m.body ILIKE %[1]s OR m.contact_name ILIKE %[1]s)", l))
	}
	rows, err := q.Query(ctx, `SELECT m.id, m.kind, m.subject, m.body, m.contact_name, m.occurred_at, m.sale_id, s.number, u.id, u.name, m.created_at
		FROM customer_communication m JOIN "User" u ON u.id = m.created_by LEFT JOIN "Sale" s ON s.id = m.sale_id
		WHERE `+and(conds)+` ORDER BY m.occurred_at DESC, m.created_at DESC LIMIT `+b.arg(limit)+` OFFSET `+b.arg(offset), b.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []commOut{}
	for rows.Next() {
		var m commOut
		var occ, cr time.Time
		if err := rows.Scan(&m.ID, &m.Kind, &m.Subject, &m.Body, &m.ContactName, &occ, &m.SaleID, &m.SaleNumber, &m.By.ID, &m.By.Name, &cr); err != nil {
			return nil, err
		}
		m.OccurredAt, m.CreatedAt = jt(occ), jt(cr)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (h *handler) customerComms(c *fiber.Ctx) error {
	r := newRq(c)
	q := httpx.Query(c)
	kind := queryEnum(q, "kind", commKinds...)
	search := q.OptStr("q").Trim().Max(80, r.t("sales.v.at_most", "n", 80)).Get()
	if err := q.Err(); err != nil {
		return err
	}
	var exists bool
	if err := h.d.Pool.QueryRow(r.ctx, `SELECT EXISTS (SELECT 1 FROM "Customer" WHERE id = $1)`, c.Params("id")).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return httpx.NotFound("entity.customer")
	}
	pg := httpx.Paginate(c, 50)
	items, err := listComms(r.ctx, h.d.Pool, c.Params("id"), "", kind, search, pg.Limit, pg.Offset)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"items": items, "page": pg.Page, "pageSize": pg.PageSize})
}

func (h *handler) addComm(c *fiber.Ctx) error {
	r := newRq(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	x := newIn(v, r.lang)
	kind := v.Str("kind").OneOf(commKinds, "sales.v.comm_kind").Get()
	subject := x.reqText("subject", 200, r.t("sales.v.comm_subject"))
	body := x.optText("body", 4000)
	contact := x.optText("contactName", 120)
	saleID := x.optID("saleId")
	occurred := time.Now().UTC().Truncate(time.Millisecond)
	if raw, ok := v.Raw("occurredAt").(string); ok && raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			v.Fail("occurredAt", "sales.v.valid_datetime")
		} else if t.After(time.Now().Add(5 * time.Minute)) {
			v.Fail("occurredAt", "sales.v.future")
		} else {
			occurred = t.UTC().Truncate(time.Millisecond)
		}
	}
	if err := v.Err(); err != nil {
		return err
	}
	cid := c.Params("id")
	var id string
	err = db.Tx(r.ctx, h.d.Pool, func(tx pgx.Tx) error {
		var name string
		if err := tx.QueryRow(r.ctx, `SELECT name FROM "Customer" WHERE id = $1`, cid).Scan(&name); err != nil {
			if db.IsNoRows(err) {
				return httpx.NotFound("entity.customer")
			}
			return err
		}
		if saleID != "" {
			var ok bool
			if err := tx.QueryRow(r.ctx, `SELECT EXISTS (SELECT 1 FROM "Sale" WHERE id = $1 AND "customerId" = $2)`, saleID, cid).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return httpx.BadRequest("error.validation", map[string]string{"saleId": r.t("sales.v.comm_sale")})
			}
		}
		if err := tx.QueryRow(r.ctx, `INSERT INTO customer_communication (customer_id, sale_id, kind, subject, body, contact_name, occurred_at, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, cid, nilIfEmpty(saleID), kind, subject, body, contact, occurred, r.u.ID).Scan(&id); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "sales.customer.communication", Entity: "Customer", EntityID: cid,
			Summary: fmt.Sprintf("Logged %s with %s: %s", strings.ToLower(kind), name, subject), After: map[string]any{"id": id, "kind": kind, "saleId": nilIfEmpty(saleID)}})
	})
	if err != nil {
		return err
	}
	items, err := listComms(r.ctx, h.d.Pool, cid, "", "", "", 500, 0)
	if err != nil {
		return err
	}
	for _, m := range items {
		if m.ID == id {
			return c.Status(201).JSON(fiber.Map{"communication": m})
		}
	}
	return c.Status(201).JSON(fiber.Map{"communication": fiber.Map{"id": id}})
}
