package pettycash

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
	"btn-ops/internal/deps"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/jobs"
	"btn-ops/internal/notify"
	"btn-ops/internal/refdata"
	"btn-ops/internal/seq"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// Float is the wire shape of a petty cash float with its position.
type Float struct {
	ID                 string     `json:"id"`
	Code               string     `json:"code"`
	Name               string     `json:"name"`
	Custodian          *userRef   `json:"custodian"`
	PlantID            *string    `json:"plantId"`
	Plant              *plantRef  `json:"plant"`
	Department         *string    `json:"department"`
	DepartmentName     *string    `json:"departmentName"`
	ImprestAmount      int64      `json:"imprestAmount"`
	LowThreshold       int64      `json:"lowThreshold"`
	ReconcileEveryDays int        `json:"reconcileEveryDays"`
	IsDefault          bool       `json:"isDefault"`
	IsActive           bool       `json:"isActive"`
	Balance            int64      `json:"balance"`
	PendingOut         int64      `json:"pendingOut"`    // submitted expenditure (amount + charges)
	PendingTopUps      int64      `json:"pendingTopUps"` // submitted top-up requests
	PendingCount       int        `json:"pendingCount"`
	Low                bool       `json:"low"`
	TopUpSuggestion    int64      `json:"topUpSuggestion"`
	LastReconciliation *recBrief  `json:"lastReconciliation"`
	ReconcileDue       bool       `json:"reconcileDue"`
	IsMine             bool       `json:"isMine"`
	UpdatedAt          time.Time  `json:"updatedAt"`
	LastEntryAt        *time.Time `json:"lastEntryAt"`
}

type recBrief struct {
	ID         string `json:"id"`
	Number     string `json:"number"`
	Date       string `json:"date"`
	Difference int64  `json:"difference"`
	Status     string `json:"status"`
}

const floatSQL = `SELECT f.id, f.code, f.name, f.custodian_id, cu.name, cu.role::text, f.plant_id, p.code, p.name, f.department, f.imprest_amount,
	f.low_threshold, f.reconcile_every_days, f.is_default, f.is_active, f.updated_at,
	COALESCE(b.bal, 0)::bigint, COALESCE(b.pend_out, 0)::bigint, COALESCE(b.pend_in, 0)::bigint, COALESCE(b.pend_n, 0)::int, b.last_at,
	r.id, r.number, r.date, r.difference, r.status
FROM petty_cash_float f
LEFT JOIN "User" cu ON cu.id = f.custodian_id
LEFT JOIN "Plant" p ON p.id = f.plant_id
LEFT JOIN LATERAL (
	SELECT (SELECT SUM(l.effect) FROM petty_cash_ledger l WHERE l.float_id = f.id AND l.status = 'APPROVED') AS bal,
		SUM(e.amount + e."momoCharge") FILTER (WHERE e.status = 'SUBMITTED' AND e.type = 'EXPENSE') AS pend_out,
		SUM(e.amount) FILTER (WHERE e.status = 'SUBMITTED' AND e.type = 'DEPOSIT') AS pend_in,
		count(*) FILTER (WHERE e.status = 'SUBMITTED') AS pend_n, max(e."createdAt") AS last_at
	FROM "PettyCashEntry" e WHERE e."floatId" = f.id) b ON true
LEFT JOIN LATERAL (
	SELECT r.id, r.number, r.date, r.difference, r.status FROM petty_cash_reconciliation r WHERE r.float_id = f.id
	ORDER BY r.date DESC, r.created_at DESC LIMIT 1) r ON true`

func scanFloats(rows pgx.Rows, lang i18n.Lang, me string, depts map[string]string) ([]Float, error) {
	defer rows.Close()
	out := []Float{}
	today := dates.TodayISO()
	for rows.Next() {
		var f Float
		var cid, cname, crole, pcode, pname *string
		var rid, rno, rstatus *string
		var rdate *time.Time
		var rdiff *int64
		if err := rows.Scan(&f.ID, &f.Code, &f.Name, &cid, &cname, &crole, &f.PlantID, &pcode, &pname, &f.Department, &f.ImprestAmount,
			&f.LowThreshold, &f.ReconcileEveryDays, &f.IsDefault, &f.IsActive, &f.UpdatedAt, &f.Balance, &f.PendingOut, &f.PendingTopUps,
			&f.PendingCount, &f.LastEntryAt, &rid, &rno, &rdate, &rdiff, &rstatus); err != nil {
			return nil, err
		}
		if cid != nil {
			u := userOf(lang, *cid, cname, crole)
			f.Custodian = &u
			f.IsMine = *cid == me
		}
		if f.PlantID != nil && pcode != nil {
			f.Plant = &plantRef{ID: *f.PlantID, Code: *pcode, Name: deref(pname)}
		}
		if f.Department != nil {
			n := *f.Department
			if x, ok := depts[n]; ok {
				n = x
			}
			f.DepartmentName = &n
		}
		last := ""
		if rid != nil {
			f.LastReconciliation = &recBrief{ID: *rid, Number: deref(rno), Date: dates.ToISO(*rdate), Difference: *rdiff, Status: deref(rstatus)}
			last = f.LastReconciliation.Date
		}
		f.Low = FloatLow(f.Balance, f.LowThreshold)
		f.TopUpSuggestion = TopUpSuggestion(f.ImprestAmount, f.Balance)
		f.ReconcileDue = f.IsActive && ReconcileDue(last, f.ReconcileEveryDays, today)
		out = append(out, f)
	}
	return out, rows.Err()
}

func departmentNames(ctx context.Context, q db.DBTX, lang i18n.Lang) map[string]string {
	out := map[string]string{}
	items, err := refdata.List(ctx, q, refdata.KindDepartment, true, lang)
	if err != nil {
		return out
	}
	for _, it := range items {
		out[it.Code] = it.Name
	}
	return out
}

// ListFloats returns the floats in scope with balances (one query; used by the finance dashboard too).
// floats nil = every float.
func ListFloats(ctx context.Context, q db.DBTX, lang i18n.Lang, me string, floats []string, activeOnly bool) ([]Float, error) {
	depts := departmentNames(ctx, q, lang) // before the query: q may be a single connection (pull transaction)
	rows, err := q.Query(ctx, floatSQL+` WHERE ($1::text[] IS NULL OR f.id = ANY($1::text[])) AND (NOT $2 OR f.is_active)
		ORDER BY f.is_active DESC, f.is_default DESC, f.code`, floats, activeOnly)
	if err != nil {
		return nil, err
	}
	return scanFloats(rows, lang, me, depts)
}

func (h *handler) floats(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	sc, err := scopeOf(ctx, h.d.Pool, u)
	if err != nil {
		return err
	}
	if sc.all {
		if _, err := DefaultFloatID(ctx, h.d.Pool); err != nil {
			return err
		}
	}
	list, err := ListFloats(ctx, h.d.Pool, lang, u.ID, sc.restrict(), c.Query("active") == "1")
	if err != nil {
		return err
	}
	var total, pending int64
	for _, f := range list {
		total += f.Balance
		pending += f.PendingOut
	}
	return c.JSON(fiber.Map{"items": list, "totalBalance": total, "pendingOut": pending,
		"canManage": u.Can(permRecord), "canApprove": u.Can(permApprove)})
}

func (h *handler) floatGet(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	sc, err := scopeOf(ctx, h.d.Pool, u)
	if err != nil {
		return err
	}
	id := c.Params("id")
	if !sc.has(id) {
		return httpx.Forbidden("pettycash.err.not_your_float")
	}
	list, err := ListFloats(ctx, h.d.Pool, lang, u.ID, []string{id}, false)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return httpx.NotFound("pettycash.entity.float")
	}
	recs, err := listReconciliations(ctx, h.d.Pool, lang, id, nil, 12, 0)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"float": list[0], "reconciliations": recs})
}

var floatCodeRe = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_\-]{1,19}$`)

type floatInput struct {
	Code, Name                 string
	CustodianID, PlantID, Dept *string
	Imprest, Low               int64
	Every                      int
	Active                     bool
	has                        map[string]bool
}

func validateFloat(v *httpx.V, lang i18n.Lang) floatInput {
	in := floatInput{has: map[string]bool{}}
	for _, k := range []string{"code", "name", "custodianId", "plantId", "department", "imprestAmount", "lowThreshold", "reconcileEveryDays", "isActive"} {
		in.has[k] = v.Has(k)
	}
	in.Code = v.Str("code").Trim().Upper().Regex(floatCodeRe, "pettycash.v.float_code").Get()
	in.Name = v.Str("name").Trim().Collapse().Min(3, "pettycash.v.float_name").Max(80, "pettycash.v.float_name").Get()
	in.CustodianID = v.NullStr("custodianId").Trim().Max(60).NilIfEmpty()
	in.PlantID = v.NullStr("plantId").Trim().Max(60).NilIfEmpty()
	in.Dept = v.NullStr("department").Trim().Max(40).NilIfEmpty()
	in.Imprest = v.OptNum("imprestAmount").Coerce().Default(0).Whole("v.whole_francs").Min(0).Max(maxAmount, "pettycash.v.amount_large").I()
	in.Low = v.OptNum("lowThreshold").Coerce().Default(0).Whole("v.whole_francs").Min(0).Max(maxAmount, "pettycash.v.amount_large").I()
	in.Every = int(v.OptNum("reconcileEveryDays").Coerce().Default(30).Whole().Min(1, "pettycash.v.every_days").Max(366, "pettycash.v.every_days").I())
	in.Active = v.OptBool("isActive").Default(true).Get()
	if !v.Failed() && in.Imprest > 0 && in.Low > in.Imprest {
		v.Fail("lowThreshold", "pettycash.v.low_above_imprest")
	}
	return in
}

func checkFloatRefs(ctx context.Context, q db.DBTX, lang i18n.Lang, in floatInput) error {
	fields := map[string]string{}
	if in.CustodianID != nil {
		var role string
		var active bool
		err := q.QueryRow(ctx, `SELECT role::text, "isActive" FROM "User" WHERE id = $1`, *in.CustodianID).Scan(&role, &active)
		if err != nil && !db.IsNoRows(err) {
			return err
		}
		if err != nil || !active || !auth.Can(auth.Role(role), permCustodian) {
			fields["custodianId"] = i18n.T(lang, "pettycash.v.custodian")
		}
	}
	if in.PlantID != nil {
		var active bool
		err := q.QueryRow(ctx, `SELECT "isActive" FROM "Plant" WHERE id = $1`, *in.PlantID).Scan(&active)
		if err != nil && !db.IsNoRows(err) {
			return err
		}
		if err != nil || !active {
			fields["plantId"] = i18n.T(lang, "pettycash.v.choose_plant")
		}
	}
	if in.Dept != nil {
		ok, err := refdata.Valid(ctx, q, refdata.KindDepartment, *in.Dept)
		if err != nil {
			return err
		}
		if !ok {
			fields["department"] = i18n.T(lang, "pettycash.v.department")
		}
	}
	if len(fields) > 0 {
		return httpx.BadRequest("error.validation", fields)
	}
	return nil
}

func floatSnapshot(f *floatRow) map[string]any {
	return map[string]any{"id": f.ID, "code": f.Code, "name": f.Name, "custodianId": f.CustodianID, "plantId": f.PlantID, "department": f.Department,
		"imprestAmount": f.Imprest, "lowThreshold": f.LowThresh, "reconcileEveryDays": f.EveryDays, "isActive": f.IsActive, "isDefault": f.IsDefault}
}

func (h *handler) floatCreate(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in := validateFloat(v, lang)
	if err := v.Err(); err != nil {
		return err
	}
	id, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (string, error) {
		if err := checkFloatRefs(ctx, tx, lang, in); err != nil {
			return "", err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO petty_cash_float (code, name, custodian_id, plant_id, department, imprest_amount, low_threshold,
			reconcile_every_days, is_active, created_by_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`, in.Code, in.Name, in.CustodianID,
			in.PlantID, in.Dept, in.Imprest, in.Low, in.Every, in.Active, u.ID).Scan(&id); err != nil {
			return "", err
		}
		f, err := loadFloat(ctx, tx, id)
		if err != nil {
			return "", err
		}
		return id, audit.Record(c, tx, audit.Input{Action: "pettycash.float_create", Entity: "PettyCashFloat", EntityID: id,
			Summary: fmt.Sprintf("Float %s (%s) created, imprest %s", f.Code, f.Name, format.RWF(float64(f.Imprest))), After: floatSnapshot(f)})
	})
	if err != nil {
		return err
	}
	Invalidate()
	list, err := ListFloats(ctx, h.d.Pool, lang, u.ID, []string{id}, false)
	if err != nil {
		return err
	}
	return c.Status(201).JSON(list[0])
}

func (h *handler) floatPatch(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	id := c.Params("id")
	patch, err := httpx.DecodeMap(c)
	if err != nil {
		return err
	}
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		before, err := loadFloat(ctx, tx, id)
		if err != nil {
			return err
		}
		if before == nil {
			return httpx.NotFound("pettycash.entity.float")
		}
		merged := map[string]any{"code": before.Code, "name": before.Name, "custodianId": strOrNil(before.CustodianID), "plantId": strOrNil(before.PlantID),
			"department": strOrNil(before.Department), "imprestAmount": before.Imprest, "lowThreshold": before.LowThresh,
			"reconcileEveryDays": before.EveryDays, "isActive": before.IsActive}
		for k, val := range patch {
			merged[k] = val
		}
		v := httpx.NewV(merged, lang)
		in := validateFloat(v, lang)
		if err := v.Err(); err != nil {
			return err
		}
		if before.IsDefault && !in.Active {
			return fieldErr("isActive", i18n.T(lang, "pettycash.err.default_float"))
		}
		if err := checkFloatRefs(ctx, tx, lang, in); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE petty_cash_float SET code=$2, name=$3, custodian_id=$4, plant_id=$5, department=$6, imprest_amount=$7,
			low_threshold=$8, reconcile_every_days=$9, is_active=$10, updated_at=now() WHERE id=$1`, id, in.Code, in.Name, in.CustodianID, in.PlantID,
			in.Dept, in.Imprest, in.Low, in.Every, in.Active); err != nil {
			return err
		}
		after, err := loadFloat(ctx, tx, id)
		if err != nil {
			return err
		}
		sum := "Float " + after.Code + " updated"
		if deref(before.CustodianID) != deref(after.CustodianID) {
			sum += " — custodian changed"
		}
		return audit.Record(c, tx, audit.Input{Action: "pettycash.float_update", Entity: "PettyCashFloat", EntityID: id, Summary: sum,
			Before: floatSnapshot(before), After: floatSnapshot(after)})
	})
	if err != nil {
		return err
	}
	Invalidate()
	list, err := ListFloats(ctx, h.d.Pool, lang, u.ID, []string{id}, false)
	if err != nil {
		return err
	}
	return c.JSON(list[0])
}

// ── reconciliations (periodic cash count vs book balance) ─────────────────────

// Reconciliation is the wire shape of one cash count.
type Reconciliation struct {
	ID            string          `json:"id"`
	Number        string          `json:"number"`
	FloatID       string          `json:"floatId"`
	Float         floatRef        `json:"float"`
	Date          string          `json:"date"`
	CountedAmount int64           `json:"countedAmount"`
	BookBalance   int64           `json:"bookBalance"`
	Difference    int64           `json:"difference"`
	PendingCount  int             `json:"pendingCount"`
	Denominations json.RawMessage `json:"denominations"`
	Explanation   *string         `json:"explanation"`
	Status        string          `json:"status"`
	CreatedBy     userRef         `json:"createdBy"`
	ReviewedBy    *userRef        `json:"reviewedBy"`
	ReviewedAt    *time.Time      `json:"reviewedAt"`
	ReviewComment *string         `json:"reviewComment"`
	AdjustmentID  *string         `json:"adjustmentEntryId"`
	CreatedAt     time.Time       `json:"createdAt"`
	CanReview     bool            `json:"canReview"`
}

const recSQL = `SELECT r.id, r.number, r.float_id, f.code, f.name, f.custodian_id, r.date, r.counted_amount, r.book_balance, r.difference, r.pending_count,
	r.denominations, r.explanation, r.status, r.created_by_id, cu.name, cu.role::text, r.reviewed_by_id, ru.name, ru.role::text, r.reviewed_at,
	r.review_comment, r.adjustment_entry_id, r.created_at
FROM petty_cash_reconciliation r JOIN petty_cash_float f ON f.id = r.float_id
LEFT JOIN "User" cu ON cu.id = r.created_by_id LEFT JOIN "User" ru ON ru.id = r.reviewed_by_id`

func scanRecs(rows pgx.Rows, lang i18n.Lang) ([]Reconciliation, error) {
	defer rows.Close()
	out := []Reconciliation{}
	for rows.Next() {
		var r Reconciliation
		var d time.Time
		var den []byte
		var cname, crole, rid, rname, rrole *string
		var cid string
		if err := rows.Scan(&r.ID, &r.Number, &r.FloatID, &r.Float.Code, &r.Float.Name, &r.Float.CustodianID, &d, &r.CountedAmount, &r.BookBalance,
			&r.Difference, &r.PendingCount, &den, &r.Explanation, &r.Status, &cid, &cname, &crole, &rid, &rname, &rrole, &r.ReviewedAt,
			&r.ReviewComment, &r.AdjustmentID, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Float.ID, r.Date = r.FloatID, dates.ToISO(d)
		if den == nil {
			den = []byte("null")
		}
		r.Denominations = den
		r.CreatedBy = userOf(lang, cid, cname, crole)
		if rid != nil {
			u := userOf(lang, *rid, rname, rrole)
			r.ReviewedBy = &u
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func listReconciliations(ctx context.Context, q db.DBTX, lang i18n.Lang, floatID string, floats []string, limit, offset int) ([]Reconciliation, error) {
	rows, err := q.Query(ctx, recSQL+` WHERE ($1 = '' OR r.float_id = $1) AND ($2::text[] IS NULL OR r.float_id = ANY($2::text[]))
		ORDER BY r.date DESC, r.created_at DESC LIMIT $3 OFFSET $4`, floatID, floats, limit, offset)
	if err != nil {
		return nil, err
	}
	return scanRecs(rows, lang)
}

func getRec(ctx context.Context, q db.DBTX, lang i18n.Lang, id string) (*Reconciliation, error) {
	rows, err := q.Query(ctx, recSQL+` WHERE r.id = $1`, id)
	if err != nil {
		return nil, err
	}
	list, err := scanRecs(rows, lang)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

func canReviewRec(u *auth.User, r *Reconciliation) bool {
	if r.Status != RecPendingReview || !(u.Can(permApprove) || u.Can(permRecord)) {
		return false
	}
	ok, _ := TopUpApproval(u.ID, r.CreatedBy.ID, r.Float.CustodianID, true)
	return ok
}

func (h *handler) reconciliations(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	sc, err := scopeOf(ctx, h.d.Pool, u)
	if err != nil {
		return err
	}
	floatID := c.Query("floatId")
	if floatID != "" && !sc.has(floatID) {
		return httpx.Forbidden("pettycash.err.not_your_float")
	}
	pg := httpx.Paginate(c, 25)
	status := c.Query("status")
	var total int
	if err := h.d.Pool.QueryRow(ctx, `SELECT count(*)::int FROM petty_cash_reconciliation r WHERE ($1 = '' OR r.float_id = $1)
		AND ($2::text[] IS NULL OR r.float_id = ANY($2::text[])) AND ($3 = '' OR r.status = $3)`, floatID, sc.restrict(), status).Scan(&total); err != nil {
		return err
	}
	rows, err := h.d.Pool.Query(ctx, recSQL+` WHERE ($1 = '' OR r.float_id = $1) AND ($2::text[] IS NULL OR r.float_id = ANY($2::text[]))
		AND ($3 = '' OR r.status = $3) ORDER BY r.date DESC, r.created_at DESC LIMIT $4 OFFSET $5`, floatID, sc.restrict(), status, pg.Limit, pg.Offset)
	if err != nil {
		return err
	}
	items, err := scanRecs(rows, lang)
	if err != nil {
		return err
	}
	for i := range items {
		items[i].CanReview = canReviewRec(u, &items[i])
	}
	return c.JSON(httpx.NewPage(items, total, pg.Page, pg.PageSize))
}

func (h *handler) reconciliationGet(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	r, err := h.visibleRec(ctx, u, lang, c.Params("id"))
	if err != nil {
		return err
	}
	return c.JSON(r)
}

func (h *handler) visibleRec(ctx context.Context, u *auth.User, lang i18n.Lang, id string) (*Reconciliation, error) {
	r, err := getRec(ctx, h.d.Pool, lang, id)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, httpx.NotFound("pettycash.entity.reconciliation")
	}
	sc, err := scopeOf(ctx, h.d.Pool, u)
	if err != nil {
		return nil, err
	}
	if !sc.has(r.FloatID) {
		return nil, httpx.Forbidden("pettycash.err.not_your_float")
	}
	r.CanReview = canReviewRec(u, r)
	return r, nil
}

func (h *handler) reconcile(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	floatID := c.Params("id")
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	ds := v.OptStr("date").Trim().Date("pettycash.v.valid_date").Default(dates.TodayISO()).Get()
	if ds > dates.TodayISO() {
		v.Fail("date", "pettycash.v.date_future")
	}
	counted := v.Num("countedAmount").Coerce().Whole("v.whole_francs").Min(0, "pettycash.v.counted").Max(maxAmount, "pettycash.v.amount_large").I()
	expl := v.NullStr("explanation").Trim().Collapse().Max(1000, "pettycash.v.at_most_1000").NilIfEmpty()
	var den []map[string]int64
	for i, d := range v.Arr("denominations", false) {
		val := d.Num("value").Coerce().Whole().Gt(0).I()
		n := d.Num("count").Coerce().Whole().Min(0).I()
		if i >= 30 {
			v.Fail("denominations", "pettycash.v.denominations")
			break
		}
		den = append(den, map[string]int64{"value": val, "count": n})
	}
	if err := v.Err(); err != nil {
		return err
	}
	if len(den) > 0 {
		var sum int64
		for _, d := range den {
			sum += d["value"] * d["count"]
		}
		if sum != counted {
			return fieldErr("countedAmount", i18n.T(lang, "pettycash.v.denominations_total", "total", format.RWF(float64(sum))))
		}
	}
	sc := scope{all: true}
	if !u.Can(permRecord) {
		if sc, err = custodianScope(ctx, h.d.Pool, u.ID); err != nil {
			return err
		}
	}
	if !sc.has(floatID) {
		return httpx.Forbidden("pettycash.err.not_your_float")
	}
	date, _ := dates.ParseISO(ds)
	id, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (string, error) {
		f, err := loadFloat(ctx, tx, floatID)
		if err != nil {
			return "", err
		}
		if f == nil {
			return "", httpx.NotFound("pettycash.entity.float")
		}
		if err := lockLedger(ctx, tx, f.ID); err != nil {
			return "", err
		}
		var book int64
		var pending int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(l.effect) FILTER (WHERE l.status = 'APPROVED' AND l.date <= $2), 0)::bigint,
			count(*) FILTER (WHERE l.status = 'SUBMITTED')::int FROM petty_cash_ledger l WHERE l.float_id = $1`, f.ID, date).Scan(&book, &pending); err != nil {
			return "", err
		}
		diff, status := ReconcileDifference(counted, book)
		if diff != 0 && (expl == nil || len([]rune(*expl)) < 5) {
			return "", fieldErr("explanation", i18n.T(lang, "pettycash.v.explain_difference"))
		}
		number, err := seq.NextNumber(ctx, tx, fmt.Sprintf("REC-%d", date.Year()))
		if err != nil {
			return "", err
		}
		var denJSON []byte
		if len(den) > 0 {
			denJSON, _ = json.Marshal(den)
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO petty_cash_reconciliation (number, float_id, date, counted_amount, book_balance, difference, pending_count,
			denominations, explanation, status, created_by_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`,
			number, f.ID, date, counted, book, diff, pending, denJSON, expl, status, u.ID).Scan(&id); err != nil {
			return "", err
		}
		if diff != 0 {
			to := notify.ToPermission(permApprove)
			except := []string{u.ID}
			if f.CustodianID != nil {
				except = append(except, *f.CustodianID)
			}
			sev := notify.Warning
			if err := notify.Send(ctx, tx, notify.Notification{Event: EventDifference, Severity: sev, TitleKey: "pettycash.notify.diff_title",
				BodyKey: "pettycash.notify.diff_body", Params: map[string]any{"number": number, "float": f.Code, "counted": format.RWF(float64(counted)),
					"book": format.RWF(float64(book)), "difference": format.RWF(float64(diff))},
				Link: "/petty-cash?tab=reconciliations&rec=" + id, To: to.Without(except...), DedupKey: "diff:" + id}); err != nil {
				return "", err
			}
			if err := notify.Send(ctx, tx, notify.Notification{Event: EventDifference, Severity: sev, TitleKey: "pettycash.notify.diff_title",
				BodyKey: "pettycash.notify.diff_body", Params: map[string]any{"number": number, "float": f.Code, "counted": format.RWF(float64(counted)),
					"book": format.RWF(float64(book)), "difference": format.RWF(float64(diff))},
				Link: "/petty-cash?tab=reconciliations&rec=" + id, To: notify.ToPermission(permRecord).Without(except...), DedupKey: "diff:" + id}); err != nil {
				return "", err
			}
		}
		return id, audit.Record(c, tx, audit.Input{Action: "pettycash.reconcile", Entity: "PettyCashReconciliation", EntityID: id,
			Summary: fmt.Sprintf("%s float %s counted %s vs book %s (difference %s)", number, f.Code, format.RWF(float64(counted)),
				format.RWF(float64(book)), format.RWF(float64(diff))),
			After: map[string]any{"floatId": f.ID, "date": ds, "counted": counted, "book": book, "difference": diff, "pending": pending, "explanation": expl, "status": status}})
	})
	if err != nil {
		return err
	}
	Invalidate()
	r, err := h.visibleRec(ctx, u, lang, id)
	if err != nil {
		return err
	}
	return c.Status(201).JSON(r)
}

func (h *handler) reviewReconciliation(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	id := c.Params("id")
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	comment := v.Str("comment").Trim().Collapse().Min(3, "pettycash.v.reason").Max(500, "pettycash.v.at_most_500").Get()
	post := v.OptBool("postAdjustment").Default(false).Get()
	if err := v.Err(); err != nil {
		return err
	}
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		r, err := getRec(ctx, tx, lang, id)
		if err != nil {
			return err
		}
		if r == nil {
			return httpx.NotFound("pettycash.entity.reconciliation")
		}
		if r.Status != RecPendingReview {
			return httpx.NewKey(409, "conflict", "pettycash.err.rec_not_pending")
		}
		if ok, reason := TopUpApproval(u.ID, r.CreatedBy.ID, r.Float.CustodianID, true); !ok {
			if reason == "pettycash.err.custodian_topup" {
				reason = "pettycash.err.custodian_review"
			}
			return httpx.Forbidden(reason)
		}
		if err := lockLedger(ctx, tx, r.FloatID); err != nil {
			return err
		}
		var adj *string
		if post && r.Difference != 0 {
			typ, cat, desc := TypeExpense, "OTHER", i18n.T(i18n.EN, "pettycash.rec.shortage", "number", r.Number)
			amt := -r.Difference
			var refCode *string
			if r.Difference > 0 {
				typ, cat, desc, amt = TypeDeposit, "FLOAT_DEPOSIT", i18n.T(i18n.EN, "pettycash.rec.surplus", "number", r.Number), r.Difference
			} else {
				refCode = ptr("OTHER")
			}
			number, err := seq.NextNumber(ctx, tx, fmt.Sprintf("PC-%d", time.Now().Year()))
			if err != nil {
				return err
			}
			eid := db.NewID()
			now := db.Now()
			d, _ := dates.ParseISO(r.Date)
			if _, err := tx.Exec(ctx, insertSQL, eid, number, d, typ, cat, refCode, nil, desc, r.FloatID, nil, nil, nil, amt, 0, &r.Number,
				StatusApproved, u.ID, u.ID, now, &comment, ReconciliationSource, r.ID, nil, now, nil); err != nil {
				return err
			}
			adj = &eid
		}
		if _, err := tx.Exec(ctx, `UPDATE petty_cash_reconciliation SET status='REVIEWED', reviewed_by_id=$2, reviewed_at=$3, review_comment=$4,
			adjustment_entry_id=$5, updated_at=$3 WHERE id=$1`, id, u.ID, db.Now(), comment, adj); err != nil {
			return err
		}
		sum := fmt.Sprintf("%s reviewed (difference %s)", r.Number, format.RWF(float64(r.Difference)))
		if adj != nil {
			sum += " — adjustment posted"
		}
		return audit.Record(c, tx, audit.Input{Action: "pettycash.reconcile_review", Entity: "PettyCashReconciliation", EntityID: id, Summary: sum,
			Before: map[string]any{"status": r.Status}, After: map[string]any{"status": RecReviewed, "comment": comment, "adjustmentEntryId": adj}})
	})
	if err != nil {
		return err
	}
	Invalidate()
	r, err := h.visibleRec(ctx, u, lang, id)
	if err != nil {
		return err
	}
	return c.JSON(r)
}

// ── periodic reconciliation reminder (job) ──────────────────────────────────

func registerJobs(d *deps.Deps) {
	jobs.Register(jobs.Job{Name: reconcileReminder, Schedule: reconcileReminderS, Timeout: 2 * time.Minute, Retries: 1, Backoff: time.Minute,
		DescriptionKey: "pettycash.job.reconcile_reminder", Run: func(ctx context.Context) error {
			_, err := RemindReconciliations(ctx, d.Pool)
			return err
		}})
}

// RemindReconciliations notifies the custodians (or finance, for unassigned floats) of every active float whose cash count
// is due. Returns how many floats were due.
func RemindReconciliations(ctx context.Context, q db.DBTX) (int, error) {
	list, err := ListFloats(ctx, q, i18n.EN, "", nil, true)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, f := range list {
		if !f.ReconcileDue {
			continue
		}
		n++
		to := notify.ToPermission(permRecord)
		if f.Custodian != nil {
			to = notify.ToUsers(f.Custodian.ID)
		}
		last := ""
		if f.LastReconciliation != nil {
			last = dates.FormatISO(f.LastReconciliation.Date)
		}
		if err := notify.Send(ctx, q, notify.Notification{Event: EventReconcileDue, Severity: notify.Info, TitleKey: "pettycash.notify.due_title",
			BodyKey: "pettycash.notify.due_body", Params: map[string]any{"float": f.Code, "name": f.Name, "last": strings.TrimSpace(last),
				"balance": format.RWF(float64(f.Balance))}, Link: "/petty-cash?tab=floats&float=" + f.ID, To: to, DedupKey: "due:" + f.ID}); err != nil {
			return n, err
		}
	}
	return n, nil
}
