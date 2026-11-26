package expenses

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"btn-ops/internal/approvals"
	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/notify"
	"btn-ops/internal/pettycash"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// ── separation of duties (pure, unit-tested) ─────────────────────────────────

// Decision of whether a user may approve the next pending level of kind ("expense" | "overrun").
type Decision struct {
	OK     bool
	Level  approvals.Level
	Reason string // i18n key when !OK
}

// NextApproval applies the rules: never the creator; one person approves at most one level of a kind; the approver must
// satisfy the next pending level, or a higher configured level of the same domain (a senior may sign for a junior level).
// Self-approval escalates: when the creator holds the role a level asks for (only Finance records expenses, and the
// first level is "Finance approval"), top management (GM / MD, §8 Finance "Top Mgt R A") signs that level instead, so
// the workflow completes with one Finance Manager while the creator never approves. See coversLevel for the cascade.
func NextApproval(levels []approvals.Level, decisions []approvalRow, kind, userID string, role auth.Role, creatorID string, creatorRole auth.Role) Decision {
	if userID == creatorID {
		return Decision{Reason: "expenses.err.self_approve"}
	}
	states, next := levelStates(levels, decisions, kind)
	if next == nil {
		return Decision{Reason: "expenses.err.no_level"}
	}
	for _, d := range decisions {
		if d.Kind == kind && d.UserID == userID && d.Decision == "APPROVED" {
			return Decision{Reason: "expenses.err.already_signed"}
		}
	}
	var lv approvals.Level
	for _, s := range states {
		if s.Level.Level == *next {
			lv = s.Level
		}
	}
	if coversLevel(lv, role, creatorRole) {
		return Decision{OK: true, Level: lv}
	}
	for _, s := range states {
		if s.Level.Level > lv.Level && s.Level.Satisfies(role) {
			return Decision{OK: true, Level: lv}
		}
	}
	return Decision{Reason: "expenses.err.level_role", Level: lv}
}

// coversLevel: role may sign level lv of an expense created by creatorRole — it satisfies the level, or the level's
// role is the creator's own (self-approval) and role is top management holding the level's permission.
func coversLevel(lv approvals.Level, role, creatorRole auth.Role) bool {
	if lv.Satisfies(role) {
		return true
	}
	topMgt := role == auth.RoleGeneralManager || role == auth.RoleManagingDirector
	return topMgt && creatorRole != "" && lv.Satisfies(creatorRole) && auth.Can(role, auth.Permission(lv.Permission))
}

// cascade records an approval of every further consecutive pending level the same approver covers (one approver,
// one click, for an org with one person per role); the creator never approves and each level is still recorded.
func cascade(ctx context.Context, tx pgx.Tx, id, kind string, levels []approvals.Level, dec []approvalRow, u *auth.User, creatorRole auth.Role, comment *string) ([]approvalRow, error) {
	for {
		states, next := levelStates(levels, dec, kind)
		if next == nil {
			return dec, nil
		}
		var lv approvals.Level
		for _, st := range states {
			if st.Level.Level == *next {
				lv = st.Level
			}
		}
		if !coversLevel(lv, u.Role, creatorRole) {
			return dec, nil
		}
		if err := recordDecision(ctx, tx, id, kind, lv, "APPROVED", u, comment); err != nil {
			return dec, err
		}
		dec = append(dec, approvalRow{Kind: kind, Level: lv.Level, Decision: "APPROVED", UserID: u.ID})
	}
}

func actionsFor(u *auth.User, r *row, decisions []approvalRow) *Actions {
	a := &Actions{}
	own := r.CreatedByID == u.ID
	open := r.Status == StatusSubmitted || r.Status == StatusRejected
	hasDecisions := false
	for _, d := range decisions {
		if d.Kind == "expense" && d.Decision == "APPROVED" {
			hasDecisions = true
		}
	}
	a.Link = r.Status != StatusCancelled && ((own && u.Can("expenses.record")) || u.Can("expenses.approve"))
	if managed(r.Source) { // petty cash voucher / wage sheet: decided there; receipts and links can still be added here
		a.Attach = r.Status != StatusCancelled && (own || u.Can("expenses.approve") || u.Can("expenses.record"))
		a.Reason = "expenses.reason.managed_" + strings.ToLower(r.Source)
		return a
	}
	a.Edit = own && u.Can("expenses.record") && (r.Status == StatusRejected || (r.Status == StatusSubmitted && !hasDecisions)) && r.SourceType == nil
	a.Cancel = open && ((own && u.Can("expenses.record")) || u.Can("expenses.approve"))
	a.Attach = r.Status != StatusCancelled && r.Status != StatusPaid && ((own && u.Can("expenses.record")) || u.Can("expenses.approve"))
	if r.Status == StatusSubmitted {
		if r.OverrunStatus == OverrunPending {
			if u.Can("budgets.approve") {
				d := NextApproval(decodeLevels(r.OverrunLevels), decisions, "overrun", u.ID, u.Role, r.CreatedByID, auth.Role(deref(r.CreatedByRole)))
				a.ApproveBudget, a.Reject = d.OK, d.OK
				if !d.OK {
					a.Reason = d.Reason
				}
			} else {
				a.Reason = "expenses.err.overrun_pending"
			}
		} else if u.Can("expenses.approve") {
			d := NextApproval(decodeLevels(r.Levels), decisions, "expense", u.ID, u.Role, r.CreatedByID, auth.Role(deref(r.CreatedByRole)))
			a.Approve, a.Reject = d.OK, d.OK
			if !d.OK {
				a.Reason = d.Reason
			}
		}
	}
	a.Pay = r.Status == StatusApproved && u.Can("expenses.approve")
	return a
}

// ── create / edit ────────────────────────────────────────────────────────────

func (h *handler) create(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in := validate(v, lang, dates.TodayISO())
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
	res, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (*created, error) {
		if err := checkRefs(ctx, tx, lang, &in); err != nil {
			return nil, err
		}
		return insertExpense(ctx, tx, lang, in, actor{ID: u.ID, Name: u.Name, Role: u.Role, PlantID: u.PlantID}, "", nil, func(ai audit.Input) error {
			return audit.Record(c, tx, ai)
		})
	})
	if err != nil {
		return err
	}
	Invalidate()
	e, err := h.detail(ctx, lang, u, res.row)
	if err != nil {
		return err
	}
	out := wrapDetail(e)
	out.BudgetCheck = res.check
	return c.Status(201).JSON(out)
}

func (h *handler) patch(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	id := c.Params("id")
	patch, err := httpx.DecodeMap(c)
	if err != nil {
		return err
	}
	r, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (*row, error) {
		before, err := getRow(ctx, tx, id, true)
		if err != nil {
			return nil, err
		}
		if before == nil {
			return nil, httpx.NotFound("expenses.entity")
		}
		if before.CreatedByID != u.ID {
			return nil, httpx.Forbidden("expenses.err.only_creator")
		}
		if before.SourceType != nil {
			return nil, httpx.NewKey(409, "conflict", "expenses.err.purchase_locked")
		}
		if managed(before.Source) {
			return nil, httpx.NewKey(409, "conflict", "expenses.err.managed")
		}
		dec, err := loadApprovals(ctx, tx, lang, id)
		if err != nil {
			return nil, err
		}
		signed := false
		for _, d := range dec[id] {
			if d.Decision == "APPROVED" && d.Kind == "expense" {
				signed = true
			}
		}
		if !(before.Status == StatusRejected || (before.Status == StatusSubmitted && !signed)) {
			return nil, httpx.NewKey(409, "conflict", "expenses.err.not_editable", "status", i18n.T(lang, "expenses.status."+before.Status))
		}
		merged := map[string]any{"date": dates.ToISO(before.Date), "category": before.Category, "department": before.Department,
			"plantId": ptrAny(before.PlantID), "supplierId": ptrAny(before.SupplierID), "supplierName": ptrAny(before.SupplierName),
			"paymentMethod": before.PaymentMethod, "amount": before.Amount, "description": before.Description, "reference": ptrAny(before.Reference),
			"overrunReason": ptrAny(before.OverrunReason), "source": before.Source, "unbudgeted": before.Unbudgeted,
			"unbudgetedReason": ptrAny(before.UnbudgetedReason)}
		if before.LineID != nil && !before.LineSuggested {
			merged["budgetLineId"] = *before.LineID
		}
		if _, ok := patch["budgetLineId"]; ok {
			merged["unbudgeted"] = false
		}
		if ub, ok := patch["unbudgeted"].(bool); ok && ub {
			delete(merged, "budgetLineId")
		}
		for k, val := range patch {
			merged[k] = val
		}
		v := httpx.NewV(merged, lang)
		in := validate(v, lang, dates.TodayISO())
		if err := v.Err(); err != nil {
			return nil, err
		}
		if in.PlantID != nil {
			if err := auth.AssertPlantAccess(u, *in.PlantID); err != nil {
				return nil, err
			}
		}
		in.Links = nil // links are managed on their own endpoints after creation
		return applyUpdate(ctx, tx, lang, before, in, u.ID, func(ai audit.Input) error { return audit.Record(c, tx, ai) })
	})
	if err != nil {
		return err
	}
	Invalidate()
	e, err := h.detail(ctx, lang, u, r)
	if err != nil {
		return err
	}
	return c.JSON(wrapDetail(e))
}

// applyUpdate re-validates links, re-runs the approval levels and the budget check, restarts the approval trail and
// stores the edit (shared by PATCH and offline resubmissions).
func applyUpdate(ctx context.Context, tx pgx.Tx, lang i18n.Lang, before *row, in input, actorID string, auditFn func(audit.Input) error) (*row, error) {
	id := before.ID
	if err := checkRefs(ctx, tx, lang, &in); err != nil {
		return nil, err
	}
	levels, err := approvals.LevelsWith(ctx, tx, approvals.Expense, in.Amount)
	if err != nil {
		return nil, err
	}
	chk, err := budgetCheck(ctx, tx, lang, in, id)
	if err != nil {
		return nil, err
	}
	bf, err := applyBudget(lang, chk, in, true)
	if err != nil {
		return nil, err
	}
	overStatus, overAmt := bf.overStatus, bf.overAmt
	status := StatusSubmitted
	if len(levels) == 0 && overStatus != OverrunPending {
		status = StatusApproved
	}
	budgetID, remaining := bf.budgetID, bf.remaining
	lv, _ := json.Marshal(levels)
	ol, _ := json.Marshal(bf.overLevels)
	if in.Source == "" {
		in.Source = before.Source
	}
	// an edit restarts the approval trail (earlier decisions stay in the audit log)
	if _, err := tx.Exec(ctx, `DELETE FROM expense_approval WHERE expense_id = $1`, id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE expense SET date=$2, category=$3, department=$4, plant_id=$5, supplier_id=$6, supplier_name=$7, payment_method=$8,
		amount=$9, description=$10, reference=$11, status=$12, approval_levels=$13, budget_id=$14, budget_remaining=$15, overrun_amount=$16,
		overrun_status=$17, overrun_levels=$18, overrun_reason=$19, decided_by_id=NULL, decided_at=NULL, decision_comment=NULL, updated_at=$20,
		source=$21, budget_line_id=$22, budget_line_suggested=$23, unbudgeted=$24, unbudgeted_reason=$25 WHERE id=$1`,
		id, in.Date, in.Category, in.Department, in.PlantID, in.SupplierID, in.SupplierName, in.PaymentMethod, in.Amount, in.Description,
		in.Reference, status, lv, budgetID, remaining, overAmt, overStatus, ol, in.OverrunReason, db.Now(),
		in.Source, bf.lineID, bf.suggested, bf.unbudgeted, bf.unbudgetedWhy); err != nil {
		return nil, err
	}
	after, err := getRow(ctx, tx, id, false)
	if err != nil {
		return nil, err
	}
	if err := notifyCreated(ctx, tx, after, actorID); err != nil {
		return nil, err
	}
	action, text := "expenses.update", "edited"
	if before.Status == StatusRejected {
		action, text = "expenses.resubmit", "corrected and resubmitted"
	}
	return after, auditFn(audit.Input{Action: action, Entity: "Expense", EntityID: id, Summary: after.Number + " " + text,
		Before: snapshot(before), After: snapshot(after)})
}

func ptrAny(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func reasonBody(c *fiber.Ctx, required bool) (*string, error) {
	v, err := httpx.Body(c)
	if err != nil {
		return nil, err
	}
	s := v.NullStr("comment").Trim().Collapse()
	if required {
		if got := s.Get(); len([]rune(got)) < 3 {
			v.Fail("comment", "expenses.v.reason")
		}
	}
	s.Max(500, "expenses.v.max_500")
	out := s.NilIfEmpty()
	if err := v.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (h *handler) cancel(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	comment, err := reasonBody(c, true)
	if err != nil {
		return err
	}
	return h.transition(c, func(tx pgx.Tx, r *row, dec []approvalRow) (string, error) {
		if r.CreatedByID != u.ID && !u.Can("expenses.approve") {
			return "", httpx.Forbidden("expenses.err.only_creator")
		}
		if r.Status != StatusSubmitted && r.Status != StatusRejected {
			return "", httpx.NewKey(409, "conflict", "expenses.err.not_cancellable", "status", i18n.T(lang, "expenses.status."+r.Status))
		}
		if _, err := tx.Exec(ctx, `UPDATE expense SET status='CANCELLED', decided_by_id=$2, decided_at=$3, decision_comment=$4, updated_at=$3 WHERE id=$1`,
			r.ID, u.ID, db.Now(), comment); err != nil {
			return "", err
		}
		return "expenses.cancel", nil
	})
}

// transition runs fn on a locked, visible expense and records the audit row.
func (h *handler) transition(c *fiber.Ctx, fn func(tx pgx.Tx, r *row, dec []approvalRow) (string, error)) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	id := c.Params("id")
	after, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (*row, error) {
		before, err := getRow(ctx, tx, id, true)
		if err != nil {
			return nil, err
		}
		if before == nil {
			return nil, httpx.NotFound("expenses.entity")
		}
		if !canSee(u, before) {
			return nil, httpx.Forbidden("expenses.err.not_yours")
		}
		dec, err := loadApprovals(ctx, tx, lang, id)
		if err != nil {
			return nil, err
		}
		action, err := fn(tx, before, dec[id])
		if err != nil {
			return nil, err
		}
		after, err := getRow(ctx, tx, id, false)
		if err != nil {
			return nil, err
		}
		sum := fmt.Sprintf("%s %s → %s (%s)", after.Number, before.Status, after.Status, format.RWF(float64(after.Amount)))
		if before.OverrunStatus != after.OverrunStatus {
			sum += fmt.Sprintf("; budget overrun %s → %s", before.OverrunStatus, after.OverrunStatus)
		}
		return after, audit.Record(c, tx, audit.Input{Action: action, Entity: "Expense", EntityID: id, Summary: sum,
			Before: map[string]any{"status": before.Status, "overrunStatus": before.OverrunStatus},
			After:  map[string]any{"status": after.Status, "overrunStatus": after.OverrunStatus, "decisionComment": after.DecisionComment, "paymentReference": after.PaymentRef}})
	})
	if err != nil {
		return err
	}
	Invalidate()
	pettycash.Invalidate()
	e, err := h.detail(ctx, lang, u, after)
	if err != nil {
		return err
	}
	return c.JSON(wrapDetail(e))
}

func recordDecision(ctx context.Context, tx pgx.Tx, expenseID, kind string, lv approvals.Level, decision string, u *auth.User, comment *string) error {
	_, err := tx.Exec(ctx, `INSERT INTO expense_approval (expense_id, kind, level, label, decision, user_id, role, comment) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		expenseID, kind, lv.Level, lv.Label, decision, u.ID, string(u.Role), comment)
	return err
}

func notifyDecided(ctx context.Context, q db.DBTX, r *row, verdict, actorID string, comment *string) error {
	params := map[string]any{"number": r.Number, "amount": format.RWF(float64(r.Amount)), "verdict": verdict, "comment": deref(comment)}
	sev := notify.Info
	if verdict == "rejected" {
		sev = notify.Warning
	}
	return notify.Send(ctx, q, notify.Notification{Event: EventDecided, Severity: sev, TitleKey: "expenses.notify.decided_title_" + verdict,
		BodyKey: "expenses.notify.decided_body", Params: params, Link: "/expenses/" + r.ID, To: notify.ToUsers(r.CreatedByID).Without(actorID)})
}

func (h *handler) approve(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	comment, err := reasonBody(c, false)
	if err != nil {
		return err
	}
	return h.transition(c, func(tx pgx.Tx, r *row, dec []approvalRow) (string, error) {
		if r.Status != StatusSubmitted {
			return "", httpx.NewKey(409, "conflict", "expenses.err.not_pending", "status", i18n.T(lang, "expenses.status."+r.Status))
		}
		if r.OverrunStatus == OverrunPending {
			return "", httpx.NewKey(409, "overrun_pending", "expenses.err.overrun_pending")
		}
		if r.SourceType == nil && r.Attachments == 0 {
			return "", httpx.NewKey(409, "receipt_required", "expenses.err.receipt_required")
		}
		d := NextApproval(decodeLevels(r.Levels), dec, "expense", u.ID, u.Role, r.CreatedByID, auth.Role(deref(r.CreatedByRole)))
		if !d.OK {
			if d.Reason == "expenses.err.level_role" {
				return "", httpx.Forbidden(i18n.T(lang, d.Reason, "level", d.Level.Label))
			}
			return "", httpx.Forbidden(d.Reason)
		}
		if err := recordDecision(ctx, tx, r.ID, "expense", d.Level, "APPROVED", u, comment); err != nil {
			return "", err
		}
		dec = append(dec, approvalRow{Kind: "expense", Level: d.Level.Level, Decision: "APPROVED", UserID: u.ID})
		if dec, err = cascade(ctx, tx, r.ID, "expense", decodeLevels(r.Levels), dec, u, auth.Role(deref(r.CreatedByRole)), comment); err != nil {
			return "", err
		}
		if _, next := levelStates(decodeLevels(r.Levels), dec, "expense"); next == nil {
			now := db.Now()
			if _, err := tx.Exec(ctx, `UPDATE expense SET status='APPROVED', decided_by_id=$2, decided_at=$3, decision_comment=$4, updated_at=$3 WHERE id=$1`,
				r.ID, u.ID, now, comment); err != nil {
				return "", err
			}
			r.Status = StatusApproved
			return "expenses.approve", notifyDecided(ctx, tx, r, "approved", u.ID, comment)
		}
		if _, err := tx.Exec(ctx, `UPDATE expense SET updated_at=$2 WHERE id=$1`, r.ID, db.Now()); err != nil {
			return "", err
		}
		return "expenses.approve_level", notifyNextLevel(ctx, tx, r, u.ID)
	})
}

func (h *handler) reject(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	comment, err := reasonBody(c, true)
	if err != nil {
		return err
	}
	return h.transition(c, func(tx pgx.Tx, r *row, dec []approvalRow) (string, error) {
		if r.Status != StatusSubmitted {
			return "", httpx.NewKey(409, "conflict", "expenses.err.not_pending", "status", i18n.T(lang, "expenses.status."+r.Status))
		}
		kind, levels := "expense", decodeLevels(r.Levels)
		if r.OverrunStatus == OverrunPending {
			kind, levels = "overrun", decodeLevels(r.OverrunLevels)
			if !u.Can("budgets.approve") {
				return "", httpx.NewKey(409, "overrun_pending", "expenses.err.overrun_pending")
			}
		} else if !u.Can("expenses.approve") {
			return "", httpx.Forbidden()
		}
		d := NextApproval(levels, dec, kind, u.ID, u.Role, r.CreatedByID, auth.Role(deref(r.CreatedByRole)))
		if !d.OK {
			return "", httpx.Forbidden(d.Reason)
		}
		if err := recordDecision(ctx, tx, r.ID, kind, d.Level, "REJECTED", u, comment); err != nil {
			return "", err
		}
		overrun := r.OverrunStatus
		if kind == "overrun" {
			overrun = OverrunRejected
		}
		now := db.Now()
		if _, err := tx.Exec(ctx, `UPDATE expense SET status='REJECTED', overrun_status=$5, decided_by_id=$2, decided_at=$3, decision_comment=$4, updated_at=$3 WHERE id=$1`,
			r.ID, u.ID, now, comment, overrun); err != nil {
			return "", err
		}
		return "expenses.reject", notifyDecided(ctx, tx, r, "rejected", u.ID, comment)
	})
}

func (h *handler) overrunApprove(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	comment, err := reasonBody(c, false)
	if err != nil {
		return err
	}
	return h.transition(c, func(tx pgx.Tx, r *row, dec []approvalRow) (string, error) {
		if r.Status != StatusSubmitted || r.OverrunStatus != OverrunPending {
			return "", httpx.NewKey(409, "conflict", "expenses.err.no_overrun")
		}
		levels := decodeLevels(r.OverrunLevels)
		d := NextApproval(levels, dec, "overrun", u.ID, u.Role, r.CreatedByID, auth.Role(deref(r.CreatedByRole)))
		if !d.OK {
			if d.Reason == "expenses.err.level_role" {
				return "", httpx.Forbidden(i18n.T(lang, d.Reason, "level", d.Level.Label))
			}
			return "", httpx.Forbidden(d.Reason)
		}
		if err := recordDecision(ctx, tx, r.ID, "overrun", d.Level, "APPROVED", u, comment); err != nil {
			return "", err
		}
		dec = append(dec, approvalRow{Kind: "overrun", Level: d.Level.Level, Decision: "APPROVED", UserID: u.ID})
		if dec, err = cascade(ctx, tx, r.ID, "overrun", levels, dec, u, auth.Role(deref(r.CreatedByRole)), comment); err != nil {
			return "", err
		}
		if _, next := levelStates(levels, dec, "overrun"); next != nil {
			_, err := tx.Exec(ctx, `UPDATE expense SET updated_at=$2 WHERE id=$1`, r.ID, db.Now())
			return "budgets.overrun_approve_level", err
		}
		status := StatusSubmitted
		if len(decodeLevels(r.Levels)) == 0 {
			status = StatusApproved
		}
		if _, err := tx.Exec(ctx, `UPDATE expense SET overrun_status='APPROVED', status=$2, updated_at=$3 WHERE id=$1`, r.ID, status, db.Now()); err != nil {
			return "", err
		}
		r.OverrunStatus, r.Status = OverrunApproved, status
		if status == StatusApproved {
			return "budgets.overrun_approve", notifyDecided(ctx, tx, r, "approved", u.ID, comment)
		}
		return "budgets.overrun_approve", notifyNextLevel(ctx, tx, r, u.ID)
	})
}

func (h *handler) overrunReject(c *fiber.Ctx) error {
	return h.reject(c)
}

// recordPayment records that an approved expense was paid outside the system (cash, MoMo, bank, cheque …) with its
// date, method and reference. POST /expenses/:id/record-payment (alias /pay kept one release). The system never pays.
func (h *handler) recordPayment(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	ref := v.NullStr("paymentReference").Trim().Collapse().Max(80, "expenses.v.max_80").NilIfEmpty()
	method := v.OptStr("paymentMethod").Trim().Upper().OneOf(append([]string{""}, PaymentMethods...), "expenses.v.payment_method").Get()
	paidOn := v.OptStr("paidOn").Trim().Date("expenses.v.date").Default(dates.TodayISO()).Get()
	floatID := v.NullStr("floatId").Trim().Max(60).NilIfEmpty()
	if paidOn > dates.TodayISO() {
		v.Fail("paidOn", "expenses.v.date_future")
	}
	if err := v.Err(); err != nil {
		return err
	}
	return h.transition(c, func(tx pgx.Tx, r *row, dec []approvalRow) (string, error) {
		if r.Status != StatusApproved {
			return "", httpx.NewKey(409, "conflict", "expenses.err.not_approved", "status", i18n.T(lang, "expenses.status."+r.Status))
		}
		if managed(r.Source) {
			return "", httpx.NewKey(409, "conflict", "expenses.err.managed")
		}
		if method != "" && method != r.PaymentMethod {
			if _, err := tx.Exec(ctx, `UPDATE expense SET payment_method = $2 WHERE id = $1`, r.ID, method); err != nil {
				return "", err
			}
			r.PaymentMethod = method
		}
		if r.PaymentMethod != PayCash && r.PaymentMethod != PayPettyCash && r.PaymentMethod != PayOther && ref == nil {
			return "", httpx.BadRequest("error.validation", map[string]string{"paymentReference": i18n.T(lang, "expenses.v.payment_reference")})
		}
		d, _ := dates.ParseISO(paidOn)
		if r.PaymentMethod == PayPettyCash {
			if floatID == nil {
				return "", httpx.BadRequest("error.validation", map[string]string{"floatId": i18n.T(lang, "expenses.v.float")})
			}
			entry, err := pettycash.PayExpense(ctx, tx, lang, pettycash.ExpensePayment{FloatID: *floatID, Date: d, Amount: r.Amount,
				Category: r.Category, Department: r.Department, PlantID: r.PlantID, Payee: deref(r.SupplierName), Description: r.Number + " " + r.Description,
				ExpenseID: r.ID, Reference: firstNonEmpty(deref(ref), deref(r.Reference)), ActorID: u.ID})
			if err != nil {
				return "", err
			}
			if ref == nil {
				ref = &entry.Number
			}
			if _, err := tx.Exec(ctx, `UPDATE expense SET float_id = $2, petty_cash_entry_id = $3 WHERE id = $1`, r.ID, *floatID, entry.ID); err != nil {
				return "", err
			}
		}
		now := db.Now()
		paidAt := d.Add(now.Sub(dates.MustParseISO(dates.ToISO(now))))
		if _, err := tx.Exec(ctx, `UPDATE expense SET status='PAID', paid_at=$2, paid_by_id=$3, payment_reference=$4, updated_at=$5 WHERE id=$1`,
			r.ID, paidAt, u.ID, ref, now); err != nil {
			return "", err
		}
		return "expenses.record_payment", nil
	})
}

// strings is used by validate helpers in other files
var _ = strings.TrimSpace
