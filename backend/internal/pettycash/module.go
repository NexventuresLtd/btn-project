package pettycash

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/cache"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/ratelimit"
	"btn-ops/internal/seq"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"
)

// Register mounts /api/pettycash (router is already behind session + RequireAuth).
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d}
	view, record, approve := auth.RequirePermission("pettycash.view"), auth.RequirePermission("pettycash.record"), auth.RequirePermission("pettycash.approve")
	r.Get("/entries", view, h.list)
	r.Get("/entries/:id", view, h.get)
	r.Get("/summary", view, h.summary)
	r.Post("/entries", record, h.create)
	r.Patch("/entries/:id", record, h.patch)
	r.Post("/entries/:id/approve", approve, h.approve)
	r.Post("/entries/:id/reject", approve, h.reject)
	r.Get("/pdf/statement", view, ratelimit.Group("pdf"), h.statementPDF)
	r.Get("/pdf/voucher/:id", view, ratelimit.Group("pdf"), h.voucherPDF)
}

type handler struct{ d *deps.Deps }

// summaries are cached briefly (payroll writes ledger rows without going through this package, so the TTL
// bounds staleness; every write here invalidates explicitly).
var sumCache = cache.New[[]byte](10*time.Second, 256)

// Invalidate drops cached petty-cash aggregates. Other modules that insert/update PettyCashEntry rows
// (payroll) should call it after commit.
func Invalidate() { sumCache.InvalidateTag("pettycash") }

// ── JSON shapes ──────────────────────────────────────────────────────────────

type userRef struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	RoleLabel string `json:"roleLabel"`
}

type plantRef struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

// Entry is the wire shape of a petty cash entry (same as the Node serialize()).
type Entry struct {
	ID            string    `json:"id"`
	Number        string    `json:"number"`
	Date          string    `json:"date"`
	Type          string    `json:"type"`
	Category      string    `json:"category"`
	Description   string    `json:"description"`
	PlantID       *string   `json:"plantId"`
	Plant         *plantRef `json:"plant"`
	PayeeName     *string   `json:"payeeName"`
	MomoNumber    *string   `json:"momoNumber"`
	Amount        int64     `json:"amount"`
	MomoCharge    int64     `json:"momoCharge"`
	Total         int64     `json:"total"`
	InvoiceRef    *string   `json:"invoiceRef"`
	Status        string    `json:"status"`
	SourceType    *string   `json:"sourceType"`
	SourceID      *string   `json:"sourceId"`
	ReadOnly      bool      `json:"readOnly"`
	AutoApproved  bool      `json:"autoApproved"`
	CreatedBy     userRef   `json:"createdBy"`
	ReviewedBy    *userRef  `json:"reviewedBy"`
	ReviewedAt    *string   `json:"reviewedAt"`
	ReviewComment *string   `json:"reviewComment"`
	CreatedAt     string    `json:"createdAt"`
	UpdatedAt     string    `json:"updatedAt"`
	BalanceAfter  *int64    `json:"balanceAfter"`
}

func iso(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func isoPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := iso(*t)
	return &s
}

func userOf(lang i18n.Lang, id string, name, role *string) userRef {
	if name == nil {
		return userRef{ID: id, Name: i18n.T(lang, "pettycash.user.unknown")}
	}
	rl := ""
	if role != nil {
		rl = i18n.T(lang, "role."+*role)
	}
	rr := ""
	if role != nil {
		rr = *role
	}
	return userRef{ID: id, Name: *name, Role: rr, RoleLabel: rl}
}

func toEntry(r *entryRow, lang i18n.Lang, bal *int64) Entry {
	e := Entry{ID: r.ID, Number: r.Number, Date: dates.ToISO(r.Date), Type: r.Type, Category: r.Category, Description: r.Description,
		PlantID: r.PlantID, PayeeName: r.PayeeName, MomoNumber: r.MomoNo, Amount: r.Amount, MomoCharge: r.MomoCharge,
		InvoiceRef: r.InvoiceRef, Status: r.Status, SourceType: r.SourceType, SourceID: r.SourceID,
		ReviewedAt: isoPtr(r.ReviewedAt), ReviewComment: r.ReviewComment, CreatedAt: iso(r.CreatedAt), UpdatedAt: iso(r.UpdatedAt)}
	if r.PID != nil {
		e.Plant = &plantRef{ID: *r.PID, Code: deref(r.PCode), Name: deref(r.PName)}
	}
	e.Total = r.Amount
	if r.Type == TypeExpense {
		e.Total += r.MomoCharge
	}
	e.AutoApproved = r.Status == StatusApproved && r.ReviewedByID == nil
	e.ReadOnly = r.SourceType != nil && *r.SourceType == PayrollSource
	e.CreatedBy = userOf(lang, r.CreatedByID, r.CUName, r.CURole)
	if r.ReviewedByID != nil {
		u := userOf(lang, *r.ReviewedByID, r.RUName, r.RURole)
		e.ReviewedBy = &u
	}
	if r.Status == StatusApproved {
		e.BalanceAfter = bal
	}
	return e
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// snapshot is the audit image of a row (same fields as the Node snapshot()).
func snapshot(r *entryRow) map[string]any {
	plant := any(nil)
	if r.PCode != nil {
		plant = *r.PCode
	}
	return map[string]any{"id": r.ID, "number": r.Number, "date": dates.ToISO(r.Date), "type": r.Type, "category": r.Category,
		"description": r.Description, "plantId": r.PlantID, "plant": plant, "payeeName": r.PayeeName, "momoNumber": r.MomoNo,
		"amount": r.Amount, "momoCharge": r.MomoCharge, "invoiceRef": r.InvoiceRef, "status": r.Status, "sourceType": r.SourceType,
		"sourceId": r.SourceID, "createdById": r.CreatedByID, "reviewedById": r.ReviewedByID, "reviewedAt": isoPtr(r.ReviewedAt),
		"reviewComment": r.ReviewComment, "createdAt": iso(r.CreatedAt), "updatedAt": iso(r.UpdatedAt)}
}

// ── errors ───────────────────────────────────────────────────────────────────

func statusWord(lang i18n.Lang, s string) string {
	return i18n.T(lang, "pettycash.status."+strings.ToLower(s))
}

func fieldErr(field, msg string) error {
	return httpx.BadRequest("error.validation", map[string]string{field: msg})
}

func insufficient(lang i18n.Lang, balance, needed int64) error {
	e := httpx.NewKey(409, "insufficient_balance", "pettycash.err.insufficient", "balance", format.RWF(float64(balance)), "needed", format.RWF(float64(needed)))
	e.Fields = map[string]string{"amount": i18n.T(lang, "pettycash.err.exceeds_balance", "balance", format.RWF(float64(balance)))}
	return e
}

// assertSufficient returns 409 when amount + MoMo charge exceeds the approved balance (call under the ledger lock).
func assertSufficient(ctx context.Context, tx pgx.Tx, lang i18n.Lang, amount, charge int64) error {
	bal, err := currentBalance(ctx, tx)
	if err != nil {
		return err
	}
	if ok, needed, _ := checkSufficientBalance(bal, amount, charge); !ok {
		return insufficient(lang, bal, needed)
	}
	return nil
}

func assertPlant(ctx context.Context, tx pgx.Tx, lang i18n.Lang, plantID *string) error {
	if plantID == nil {
		return nil
	}
	var active bool
	err := tx.QueryRow(ctx, `SELECT "isActive" FROM "Plant" WHERE id = $1`, *plantID).Scan(&active)
	if (err != nil && db.IsNoRows(err)) || (err == nil && !active) {
		return fieldErr("plantId", i18n.T(lang, "pettycash.v.choose_plant"))
	}
	return err
}

// ── reads ────────────────────────────────────────────────────────────────────

func (h *handler) list(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	rg := dates.ResolveRange(c.Query("from"), c.Query("to"))
	m := map[string]any{}
	for k, val := range c.Queries() {
		if val != "" {
			m[k] = val
		}
	}
	v := httpx.NewV(m, lang)
	typ := v.OptStr("type").OneOf(entryTypes).Get()
	cat := v.OptStr("category").OneOf(categories).Get()
	status := v.OptStr("status").OneOf(statuses).Get()
	q := v.OptStr("q").Trim().Max(100).Get()
	order := v.OptStr("order").Default("asc").OneOf([]string{"asc", "desc"}).Get()
	if err := v.Err(); err != nil {
		return err
	}
	plantID := auth.PlantFilter(u, c.Query("plantId"))
	pg := httpx.Paginate(c, 50)
	base := filter{from: rg.From, to: rg.To, plantID: plantID, typ: typ, category: cat, q: q}
	page := base
	if status != "" {
		page.statuses = []string{status}
	}

	var (
		rows           []entryRow
		counts         map[string]int
		opening, close int64
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) {
		rows, err = listEntries(gctx, h.d.Pool, page, order == "desc", pg.Limit, pg.Offset)
		return
	})
	g.Go(func() (err error) { counts, err = statusCounts(gctx, h.d.Pool, base); return })
	g.Go(func() (err error) { opening, close, err = ledgerTotals(gctx, h.d.Pool, rg.From, rg.To); return })
	if err := g.Wait(); err != nil {
		return err
	}
	all, total := 0, 0
	for s, n := range counts {
		all += n
		if status == "" || s == status {
			total += n
		}
	}
	items := make([]Entry, 0, len(rows))
	for i := range rows {
		items = append(items, toEntry(&rows[i], lang, rows[i].Bal))
	}
	sc := map[string]int{"ALL": all}
	for s, n := range counts {
		sc[s] = n
	}
	return c.JSON(fiber.Map{"items": items, "total": total, "page": pg.Page, "pageSize": pg.PageSize, "from": rg.FromISO, "to": rg.ToISO,
		"openingBalance": opening, "closingBalance": close, "statusCounts": sc})
}

func (h *handler) get(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	row, err := getRow(ctx, h.d.Pool, c.Params("id"))
	if err != nil {
		return err
	}
	if row == nil {
		return httpx.NotFound("entity.petty_entry")
	}
	if !canSee(u, row.PlantID) {
		return httpx.Forbidden("error.plant_scope")
	}
	var bal *int64
	if row.Status == StatusApproved {
		if bal, err = balanceAfter(ctx, h.d.Pool, row.ID, row.Date); err != nil {
			return err
		}
	}
	return c.JSON(toEntry(row, lang, bal))
}

func (h *handler) summary(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	rg := dates.ResolveRange(c.Query("from"), c.Query("to"))
	plantID := auth.PlantFilter(u, c.Query("plantId"))
	key := fmt.Sprintf("pc:sum:%s:%s:%s:%s", plantID, rg.FromISO, rg.ToISO, lang)
	body, err := sumCache.GetOrLoad(ctx, key, func(ctx context.Context) ([]byte, error) {
		ap, err := h.d.Settings.Approvals(ctx)
		if err != nil {
			return nil, err
		}
		s, err := computeSummary(ctx, h.d.Pool, lang, rg.From, rg.To, plantID)
		if err != nil {
			return nil, err
		}
		s.ApprovalLimit = ap.PettyCashApprovalLimit
		return json.Marshal(s)
	}, "pettycash")
	if err != nil {
		return err
	}
	return httpx.BytesCached(c, body, "application/json; charset=utf-8", "private, no-cache")
}

// ── mutations ────────────────────────────────────────────────────────────────

const insertSQL = `INSERT INTO "PettyCashEntry" (number, date, type, category, description, "plantId", "payeeName", "momoNumber", amount, "momoCharge",
	"invoiceRef", status, "createdById", "reviewedAt", "reviewComment", "createdAt", "updatedAt")
	VALUES ($1,$2,$3::"PettyCashType",$4::"PettyCashCategory",$5,$6,$7,$8,$9,$10,$11,$12::"ApprovalStatus",$13,$14,$15,$16,$16) RETURNING id`

func (h *handler) create(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	body, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in := validateEntry(body, lang, dates.TodayISO())
	if err := body.Err(); err != nil {
		return err
	}
	if in.PlantID != nil {
		if err := auth.AssertPlantAccess(u, *in.PlantID); err != nil {
			return err
		}
	}
	ap, err := h.d.Settings.Approvals(ctx)
	if err != nil {
		return err
	}
	dec := approvalDecision(in.Type, in.Amount, ap.PettyCashApprovalLimit)
	date, _ := dates.ParseISO(in.Date)
	now := db.Now()

	created, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (*entryRow, error) {
		if err := lockLedger(ctx, tx); err != nil {
			return nil, err
		}
		if err := assertPlant(ctx, tx, lang, in.PlantID); err != nil {
			return nil, err
		}
		if in.Type == TypeExpense {
			if err := assertSufficient(ctx, tx, lang, in.Amount, in.MomoCharge); err != nil {
				return nil, err
			}
		}
		number, err := seq.NextNumber(ctx, tx, "PC-"+in.Date[:4])
		if err != nil {
			return nil, err
		}
		var reviewedAt *time.Time
		var comment *string
		if dec.AutoApproved {
			reviewedAt, comment = &now, ptr(AutoApprovedComment)
		}
		var id string
		if err := tx.QueryRow(ctx, insertSQL, number, date, in.Type, in.Category, in.Description, in.PlantID, in.PayeeName, in.MomoNumber,
			in.Amount, in.MomoCharge, in.InvoiceRef, dec.Status, u.ID, reviewedAt, comment, now).Scan(&id); err != nil {
			return nil, err
		}
		row, err := getRow(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		kind, how := "expense", "submitted for approval"
		if row.Type == TypeDeposit {
			kind = "deposit"
		}
		if dec.AutoApproved {
			how = "auto-approved"
		}
		return row, audit.Record(c, tx, audit.Input{Action: "pettycash.create", Entity: "PettyCashEntry", EntityID: row.ID,
			Summary: fmt.Sprintf("%s %s %s — %s", row.Number, kind, format.RWF(float64(row.Amount)), how), After: snapshot(row)})
	})
	if err != nil {
		return err
	}
	Invalidate()
	out := struct {
		Entry
		Decision string `json:"decision"`
	}{toEntry(created, lang, nil), i18n.T(lang, dec.ReasonKey)}
	return c.Status(201).JSON(out)
}

func ptr[T any](v T) *T { return &v }

func (h *handler) patch(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	id := c.Params("id")
	patch, err := httpx.DecodeMap(c)
	if err != nil {
		return err
	}
	date := dates.TodayISO()
	updated, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (*entryRow, error) {
		if err := lockLedger(ctx, tx); err != nil {
			return nil, err
		}
		before, err := getRow(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if before == nil {
			return nil, httpx.NotFound("entity.petty_entry")
		}
		if before.SourceType != nil && *before.SourceType == PayrollSource {
			return nil, httpx.NewKey(409, "conflict", "pettycash.err.payroll_readonly")
		}
		if !canSee(u, before.PlantID) {
			return nil, httpx.Forbidden("error.plant_scope")
		}
		if before.CreatedByID != u.ID {
			return nil, httpx.Forbidden("pettycash.err.only_creator")
		}
		if before.Status != StatusSubmitted && before.Status != StatusRejected {
			return nil, httpx.NewKey(409, "conflict", "pettycash.err.not_editable", "status", statusWord(lang, before.Status))
		}
		if t, has := patch["type"]; has && t != before.Type {
			return nil, fieldErr("type", i18n.T(lang, "pettycash.err.type_locked"))
		}
		merged := map[string]any{"date": dates.ToISO(before.Date), "type": before.Type, "category": before.Category, "description": before.Description,
			"plantId": strOrNil(before.PlantID), "payeeName": strOrNil(before.PayeeName), "momoNumber": strOrNil(before.MomoNo),
			"amount": before.Amount, "momoCharge": before.MomoCharge, "invoiceRef": strOrNil(before.InvoiceRef)}
		for k, val := range patch {
			merged[k] = val
		}
		v := httpx.NewV(merged, lang)
		in := validateEntry(v, lang, date)
		if err := v.Err(); err != nil {
			return nil, err
		}
		if in.PlantID != nil {
			if err := auth.AssertPlantAccess(u, *in.PlantID); err != nil {
				return nil, err
			}
		}
		if err := assertPlant(ctx, tx, lang, in.PlantID); err != nil {
			return nil, err
		}
		if in.Type == TypeExpense {
			if err := assertSufficient(ctx, tx, lang, in.Amount, in.MomoCharge); err != nil {
				return nil, err
			}
		}
		d, _ := dates.ParseISO(in.Date)
		// any edit goes back to the approver, even below the limit
		if _, err := tx.Exec(ctx, `UPDATE "PettyCashEntry" SET date=$2, type=$3::"PettyCashType", category=$4::"PettyCashCategory", description=$5, "plantId"=$6,
			"payeeName"=$7, "momoNumber"=$8, amount=$9, "momoCharge"=$10, "invoiceRef"=$11, status='SUBMITTED', "reviewedById"=NULL, "reviewedAt"=NULL,
			"reviewComment"=NULL, "updatedAt"=$12 WHERE id=$1`, id, d, in.Type, in.Category, in.Description, in.PlantID, in.PayeeName, in.MomoNumber,
			in.Amount, in.MomoCharge, in.InvoiceRef, db.Now()); err != nil {
			return nil, err
		}
		row, err := getRow(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		action, text := "pettycash.update", "edited"
		if before.Status == StatusRejected {
			action, text = "pettycash.resubmit", "corrected and resubmitted"
		}
		return row, audit.Record(c, tx, audit.Input{Action: action, Entity: "PettyCashEntry", EntityID: id,
			Summary: row.Number + " " + text, Before: snapshot(before), After: snapshot(row)})
	})
	if err != nil {
		return err
	}
	Invalidate()
	return c.JSON(toEntry(updated, lang, nil))
}

func strOrNil(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func (h *handler) approve(c *fiber.Ctx) error {
	body, err := httpx.Body(c)
	if err != nil {
		return err
	}
	comment := body.NullStr("comment").Trim().Max(500).NilIfEmpty()
	if err := body.Err(); err != nil {
		return err
	}
	return h.review(c, StatusApproved, comment)
}

func (h *handler) reject(c *fiber.Ctx) error {
	body, err := httpx.Body(c)
	if err != nil {
		return err
	}
	var comment *string
	if s, ok := body.Raw("comment").(string); !ok {
		body.Fail("comment", "pettycash.v.reason")
	} else if s = strings.TrimSpace(s); len([]rune(s)) < 3 {
		body.Fail("comment", "pettycash.v.reason")
	} else if len([]rune(s)) > 500 {
		body.Fail("comment", i18n.T(httpx.Lang(c), "pettycash.v.at_most", "n", 500))
	} else {
		comment = &s
	}
	if err := body.Err(); err != nil {
		return err
	}
	return h.review(c, StatusRejected, comment)
}

func (h *handler) review(c *fiber.Ctx, decision string, comment *string) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	id := c.Params("id")
	row, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (*entryRow, error) {
		if err := lockLedger(ctx, tx); err != nil {
			return nil, err
		}
		before, err := getRow(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if before == nil {
			return nil, httpx.NotFound("entity.petty_entry")
		}
		if !canSee(u, before.PlantID) {
			return nil, httpx.Forbidden("error.plant_scope")
		}
		if before.Status != StatusSubmitted {
			return nil, httpx.NewKey(409, "conflict", "pettycash.err.already", "status", statusWord(lang, before.Status))
		}
		if before.CreatedByID == u.ID {
			return nil, httpx.Forbidden("pettycash.err.self_review")
		}
		if decision == StatusApproved && before.Type == TypeExpense {
			if err := assertSufficient(ctx, tx, lang, before.Amount, before.MomoCharge); err != nil {
				return nil, err
			}
		}
		now := db.Now()
		if _, err := tx.Exec(ctx, `UPDATE "PettyCashEntry" SET status=$2::"ApprovalStatus", "reviewedById"=$3, "reviewedAt"=$4, "reviewComment"=$5, "updatedAt"=$4 WHERE id=$1`,
			id, decision, u.ID, now, comment); err != nil {
			return nil, err
		}
		after, err := getRow(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		action, verb := "pettycash.approve", "approved"
		if decision == StatusRejected {
			action, verb = "pettycash.reject", "rejected"
		}
		sum := fmt.Sprintf("%s %s (%s)", after.Number, verb, format.RWF(float64(after.Amount)))
		if comment != nil {
			sum += ": " + *comment
		}
		return after, audit.Record(c, tx, audit.Input{Action: action, Entity: "PettyCashEntry", EntityID: id, Summary: sum,
			Before: map[string]any{"status": before.Status}, After: map[string]any{"status": after.Status, "reviewComment": comment}})
	})
	if err != nil {
		return err
	}
	Invalidate()
	return c.JSON(toEntry(row, lang, nil))
}
