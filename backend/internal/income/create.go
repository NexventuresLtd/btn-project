package income

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/budgets"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/finlink"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/notify"
	"btn-ops/internal/refdata"
	"btn-ops/internal/seq"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// MaxLinksOnCreate is the number of links accepted together with a new record.
const MaxLinksOnCreate = 20

// input is a validated create / edit payload.
type input struct {
	Date         time.Time
	Amount       int64
	Category     string
	Department   string
	Description  string
	PlantID      *string
	CustomerID   *string
	PayerName    *string
	Method       string
	Reference    *string
	BudgetLineID string // explicit revenue line; "" = the suggestion is used
	Links        []finlink.Input
}

// validate applies the field rules (same for the online form and offline records). today = Kigali today (ISO).
// requireRef = the reference rule applies (always for income recorded here). keptDate = the stored date of an edited
// record: keeping it is always allowed (legacy records may be older than the entry window).
func validate(v *httpx.V, today string, requireRef bool, keptDate ...string) input {
	var in input
	ds := v.Str("date").Trim().Date("income.v.date").Get()
	if ds != "" && len(keptDate) > 0 && ds == keptDate[0] {
		in.Date, _ = dates.ParseISO(ds)
	} else if ds != "" {
		if ds > today {
			v.Fail("date", "income.v.date_future")
		} else if d, err := dates.ParseISO(ds); err == nil {
			if d.Before(dates.MustParseISO(today).AddDate(-2, 0, 0)) {
				v.Fail("date", "income.v.date_old")
			}
			in.Date = d
		}
	}
	amt := v.Num("amount").Coerce()
	amt.Whole("v.whole_francs").Gt(0, "income.v.amount_positive").Max(MaxAmount, "income.v.amount_large")
	in.Amount = amt.I()
	in.Category = v.Str("category").Trim().Upper().Min(1, "income.v.category").Max(40, "income.v.category").Get()
	in.Department = v.OptStr("department").Trim().Upper().Default(DefaultDepartment).Max(40, "income.v.department").Get()
	if in.Department == "" {
		in.Department = DefaultDepartment
	}
	desc := v.Str("description").Trim().Collapse().Get()
	if n := utf8.RuneCountInString(desc); v.Has("description") && (n < 3 || n > 500) {
		v.Fail("description", "income.v.description")
	}
	in.Description = desc
	in.PlantID = v.NullStr("plantId").Trim().Max(40).NilIfEmpty()
	in.CustomerID = v.NullStr("customerId").Trim().Max(60).NilIfEmpty()
	in.PayerName = v.NullStr("payerName").Trim().Collapse().Max(160, "income.v.max_160").NilIfEmpty()
	in.Method = v.Str("method").Trim().Upper().OneOf(Methods, "income.v.method").Get()
	in.Reference = v.NullStr("reference").Trim().Collapse().Max(80, "income.v.max_80").NilIfEmpty()
	if requireRef && in.Method != "" && needsReference(in.Method) && in.Reference == nil {
		v.Fail("reference", "income.v.reference")
	}
	in.BudgetLineID = deref(v.NullStr("budgetLineId").Trim().Max(60).NilIfEmpty())
	links := v.Arr("links", false)
	if len(links) > MaxLinksOnCreate {
		v.Fail("links", "income.v.links_max")
	}
	for _, l := range links {
		in.Links = append(in.Links, finlink.ParseInput(l))
	}
	return in
}

// checkRefs validates codes, site, customer and budget line against the database; returns a 400 with field errors.
func checkRefs(ctx context.Context, q db.DBTX, lang i18n.Lang, in *input) error {
	fields := map[string]string{}
	if ok, err := refdata.Valid(ctx, q, refdata.KindIncomeCategory, in.Category); err != nil {
		return err
	} else if !ok {
		fields["category"] = i18n.T(lang, "income.v.category")
	}
	if ok, err := refdata.Valid(ctx, q, refdata.KindDepartment, in.Department); err != nil {
		return err
	} else if !ok {
		fields["department"] = i18n.T(lang, "income.v.department")
	}
	if in.PlantID != nil {
		var active bool
		err := q.QueryRow(ctx, `SELECT "isActive" FROM "Plant" WHERE id = $1`, *in.PlantID).Scan(&active)
		if err != nil && !db.IsNoRows(err) {
			return err
		}
		if err != nil || !active {
			fields["plantId"] = i18n.T(lang, "income.v.plant")
		}
	}
	if in.CustomerID != nil {
		var name string
		err := q.QueryRow(ctx, `SELECT name FROM "Customer" WHERE id = $1`, *in.CustomerID).Scan(&name)
		switch {
		case db.IsNoRows(err):
			fields["customerId"] = i18n.T(lang, "income.v.customer")
		case err != nil:
			return err
		default:
			in.PayerName = &name
		}
	}
	if in.BudgetLineID != "" && !in.Date.IsZero() {
		ok, err := budgets.ValidIncomeLine(ctx, q, in.BudgetLineID, in.Date)
		if err != nil {
			return err
		}
		if !ok {
			fields["budgetLineId"] = i18n.T(lang, "income.v.budget_line")
		}
	}
	if len(fields) > 0 {
		return httpx.BadRequest("error.validation", fields)
	}
	return nil
}

// budgetLine resolves the line to store: the chosen one, else the suggestion (department + category + date).
func budgetLine(ctx context.Context, q db.DBTX, in input) (*string, bool, error) {
	if in.BudgetLineID != "" {
		id := in.BudgetLineID
		return &id, false, nil
	}
	id, err := budgets.SuggestIncomeLine(ctx, q, in.Department, in.Category, in.Date)
	if err != nil || id == "" {
		return nil, false, err
	}
	return &id, true, nil
}

// actor is who records the income.
type actor struct {
	ID       string
	Role     auth.Role
	PlantID  string
	DeviceID string
}

// insertIncome runs the shared create path inside tx (source OTHER): number, budget line, row, links, notification, audit.
func insertIncome(ctx context.Context, tx pgx.Tx, lang i18n.Lang, in input, a actor, id string, auditFn func(audit.Input) error) (*row, error) {
	line, suggested, err := budgetLine(ctx, tx, in)
	if err != nil {
		return nil, err
	}
	number, err := seq.NextNumber(ctx, tx, fmt.Sprintf("INC-%d", in.Date.Year()))
	if err != nil {
		return nil, err
	}
	if id == "" {
		id = db.NewID()
	}
	var dev *string
	if a.DeviceID != "" {
		dev = &a.DeviceID
	}
	now := db.Now()
	if _, err := tx.Exec(ctx, `INSERT INTO income (id, number, date, amount, category, description, plant_id, department, source, customer_id,
			payer_name, method, reference, status, budget_line_id, budget_line_suggested, created_by_id, device_id, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'OTHER',$9,$10,$11,$12,'RECORDED',$13,$14,$15,$16,$17,$17)`,
		id, number, in.Date, in.Amount, in.Category, in.Description, in.PlantID, in.Department, in.CustomerID,
		in.PayerName, in.Method, in.Reference, line, suggested, a.ID, dev, now); err != nil {
		return nil, err
	}
	if err := addLinks(ctx, tx, lang, a, id, in.Links); err != nil {
		return nil, err
	}
	r, err := getRow(ctx, tx, id, false)
	if err != nil {
		return nil, err
	}
	if err := notifyRecorded(ctx, tx, r, a.ID); err != nil {
		return nil, err
	}
	sum := fmt.Sprintf("%s income recorded %s (%s, %s)", r.Number, format.RWF(float64(r.Amount)), r.Category, r.Method)
	if r.PayerName != nil {
		sum += " from " + *r.PayerName
	}
	if r.LineBudgetNo != nil {
		sum += " — budget line " + *r.LineBudgetNo + " " + deref(r.LineCategory)
	}
	if err := auditFn(audit.Input{Action: "income.create", Entity: "Income", EntityID: r.ID, Summary: sum, After: snapshot(r)}); err != nil {
		return nil, err
	}
	return r, nil
}

// addLinks validates and stores the links given with a new record.
func addLinks(ctx context.Context, tx pgx.Tx, lang i18n.Lang, a actor, incomeID string, links []finlink.Input) error {
	u := &auth.User{ID: a.ID, Role: a.Role, PlantID: a.PlantID}
	for i, l := range links {
		t, err := finlink.Validate(ctx, tx, lang, u, l)
		if err != nil {
			if he, ok := err.(*httpx.Error); ok && he.Fields != nil {
				f := map[string]string{}
				for k, v := range he.Fields {
					f[fmt.Sprintf("links.%d.%s", i, k)] = v
				}
				he.Fields = f
			}
			return err
		}
		if _, err := finlink.Add(ctx, tx, finlink.IncomeLinks, incomeID, t, l.Note, a.ID, false); err != nil {
			return err
		}
	}
	return nil
}

// notifyRecorded tells the verifiers (finance) that new income awaits verification.
func notifyRecorded(ctx context.Context, q db.DBTX, r *row, actorID string) error {
	params := map[string]any{"number": r.Number, "amount": format.RWF(float64(r.Amount)), "description": r.Description}
	to := notify.ToPermission("income.verify")
	if r.PlantID != nil {
		to = notify.ToPermissionAtSite("income.verify", *r.PlantID)
	}
	return notify.Send(ctx, q, notify.Notification{Event: EventRecorded, Severity: notify.Info, TitleKey: "income.notify.recorded_title",
		BodyKey: "income.notify.recorded_body", Params: params, Link: "/income/" + r.ID, To: to.Without(actorID), DedupKey: "recorded:" + r.ID})
}

// ── handlers ─────────────────────────────────────────────────────────────────

func (h *handler) create(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in := validate(v, dates.TodayISO(), true)
	if err := v.Err(); err != nil {
		return err
	}
	if in.PlantID == nil {
		if s := auth.ScopedPlantID(u); s != "" {
			in.PlantID = &s
		}
	}
	if in.PlantID != nil {
		if err := auth.AssertPlantAccess(u, *in.PlantID); err != nil {
			return err
		}
	}
	r, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (*row, error) {
		if err := checkRefs(ctx, tx, lang, &in); err != nil {
			return nil, err
		}
		return insertIncome(ctx, tx, lang, in, actor{ID: u.ID, Role: u.Role, PlantID: u.PlantID}, "", func(ai audit.Input) error {
			return audit.Record(c, tx, ai)
		})
	})
	if err != nil {
		return err
	}
	Invalidate()
	return h.respondDetail(c, r, 201)
}

// orderPaymentFields are the fields of an order payment that may be corrected here (the rest belongs to the order).
var orderPaymentFields = map[string]bool{"description": true, "reference": true, "category": true, "budgetLineId": true}

func (h *handler) patch(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	id := c.Params("id")
	patch, err := httpx.DecodeMap(c)
	if err != nil {
		return err
	}
	r, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (*row, error) {
		if err := lockSaleOf(ctx, tx, id); err != nil {
			return nil, err
		}
		before, err := getRow(ctx, tx, id, true)
		if err != nil {
			return nil, err
		}
		if before == nil {
			return nil, httpx.NotFound("income.entity")
		}
		if !canSee(u, before) {
			return nil, httpx.Forbidden("income.err.not_yours")
		}
		if before.CreatedByID != u.ID {
			return nil, httpx.Forbidden("income.err.only_recorder")
		}
		if before.Status != StatusRecorded {
			return nil, httpx.NewKey(409, "conflict", "income.err.not_editable", "status", i18n.T(lang, "income.status."+before.Status))
		}
		in, err := mergePatch(lang, before, patch)
		if err != nil {
			return nil, err
		}
		if in.PlantID != nil {
			if err := auth.AssertPlantAccess(u, *in.PlantID); err != nil {
				return nil, err
			}
		}
		return applyUpdate(ctx, tx, lang, before, in, func(ai audit.Input) error { return audit.Record(c, tx, ai) })
	})
	if err != nil {
		return err
	}
	Invalidate()
	return h.respondDetail(c, r, 200)
}

// mergePatch overlays a (partial) body on the stored record and validates the result. Order payments accept only
// description, reference, category and budget line changes (409 for anything else that changes).
func mergePatch(lang i18n.Lang, before *row, patch map[string]any) (input, error) {
	merged := map[string]any{"date": dates.ToISO(before.Date), "amount": before.Amount, "category": before.Category,
		"department": before.Department, "description": before.Description, "plantId": ptrAny(before.PlantID),
		"customerId": ptrAny(before.CustomerID), "payerName": ptrAny(before.PayerName), "method": before.Method,
		"reference": ptrAny(before.Reference)}
	if before.LineID != nil && !before.LineSuggested {
		merged["budgetLineId"] = *before.LineID
	}
	if before.CustomerID != nil {
		delete(merged, "payerName") // the customer's name is used
	}
	if _, ok := patch["customerId"]; ok {
		merged["payerName"] = nil
	}
	for k, val := range patch {
		if k == "links" {
			continue // links are managed on their own endpoints after creation
		}
		merged[k] = val
	}
	v := httpx.NewV(merged, lang)
	_, refTouched := patch["reference"]
	_, methodTouched := patch["method"]
	in := validate(v, dates.TodayISO(), before.Source != SrcOrderPayment || refTouched || methodTouched, dates.ToISO(before.Date))
	if err := v.Err(); err != nil {
		return in, err
	}
	in.Links = nil
	if before.Source == SrcOrderPayment {
		same := in.Date.Equal(before.Date) && in.Amount == before.Amount && in.Method == before.Method && in.Department == before.Department &&
			eqPtr(in.PlantID, before.PlantID) && eqPtr(in.CustomerID, before.CustomerID)
		for k := range patch {
			if !orderPaymentFields[k] && !same {
				return in, httpx.NewKey(409, "conflict", "income.err.order_payment_locked")
			}
		}
		in.CustomerID, in.PayerName = before.CustomerID, before.PayerName
	}
	return in, nil
}

func eqPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func ptrAny(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// applyUpdate re-checks references and the budget line and stores the edit (shared by PATCH and offline resubmissions).
func applyUpdate(ctx context.Context, tx pgx.Tx, lang i18n.Lang, before *row, in input, auditFn func(audit.Input) error) (*row, error) {
	if before.Source == SrcOrderPayment {
		in.CustomerID = nil // the order's customer is kept as is (not re-validated here)
	}
	if err := checkRefs(ctx, tx, lang, &in); err != nil {
		return nil, err
	}
	if before.Source == SrcOrderPayment {
		in.CustomerID, in.PayerName = before.CustomerID, before.PayerName
	}
	line, suggested, err := budgetLine(ctx, tx, in)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE income SET date=$2, amount=$3, category=$4, description=$5, plant_id=$6, department=$7, customer_id=$8,
		payer_name=$9, method=$10, reference=$11, budget_line_id=$12, budget_line_suggested=$13, updated_at=$14 WHERE id=$1`,
		before.ID, in.Date, in.Amount, in.Category, in.Description, in.PlantID, in.Department, in.CustomerID, in.PayerName, in.Method,
		in.Reference, line, suggested, db.Now()); err != nil {
		return nil, err
	}
	if before.SaleID != nil {
		if err := touchSale(ctx, tx, *before.SaleID); err != nil {
			return nil, err
		}
	}
	after, err := getRow(ctx, tx, before.ID, false)
	if err != nil {
		return nil, err
	}
	return after, auditFn(audit.Input{Action: "income.update", Entity: "Income", EntityID: before.ID, Summary: after.Number + " edited",
		Before: snapshot(before), After: snapshot(after)})
}

// lockSaleOf locks the order of an order payment before the income row (the order the sales module writes payments
// in: order, then payment), so concurrent payment changes queue instead of deadlocking.
func lockSaleOf(ctx context.Context, tx pgx.Tx, incomeID string) error {
	_, err := tx.Exec(ctx, `SELECT 1 FROM "Sale" s WHERE s.id = (SELECT sale_id FROM income WHERE id = $1) FOR UPDATE OF s`, incomeID)
	return err
}

// touchSale bumps the order's updatedAt so order caches and pulls see the changed payment.
func touchSale(ctx context.Context, tx pgx.Tx, saleID string) error {
	_, err := tx.Exec(ctx, `UPDATE "Sale" SET "updatedAt" = $2 WHERE id = $1`, saleID, db.Now())
	return err
}
