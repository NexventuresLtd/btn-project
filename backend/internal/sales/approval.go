package sales

import (
	"encoding/json"
	"fmt"
	"time"

	"btn-ops/internal/approvals"
	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/notify"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// Order approvals (ORD-04, ORD-10): value/role levels come from approvals.Levels("sales_order", amount) (admin-configurable
// thresholds), required lowest level first, one approval per level.
//
//	CONFIRM   — thresholds on the order total (+ a Production-Manager level when a line is discounted beyond the limit).
//	            Levels the requester is entitled to approve are satisfied by the request itself.
//	CANCEL    — thresholds + a "sales.cancel" level; every level by someone other than the requester (four eyes).
//	AMENDMENT — thresholds on the new total (at least one "sales.approve" level); four eyes.
const (
	ApprConfirm   = "CONFIRM"
	ApprCancel    = "CANCEL"
	ApprAmendment = "AMENDMENT"
)

// Synthetic level labels (i18n keys; threshold rows carry their own text).
const (
	lblDiscount = "sales.approval.level_discount"
	lblCancel   = "sales.approval.level_cancel"
	lblAmend    = "sales.approval.level_amend"
)

// requiredLevels builds the level list of a request (pure apart from the threshold rows passed in).
func requiredLevels(kind string, thresholds []approvals.Level, discountBeyondLimit bool) []approvals.Level {
	out := append([]approvals.Level{}, thresholds...)
	top := 0
	for _, l := range out {
		top = max(top, l.Level)
	}
	// below-limit prices are approved by the Production Manager: SRS §8 gives order approval ("A") to the
	// Production Manager and Sales only (Top Management reads orders)
	gm := string(auth.RoleProductionManager)
	switch kind {
	case ApprConfirm:
		if discountBeyondLimit {
			covered := false
			for _, l := range out {
				if l.Role != nil && *l.Role == gm {
					covered = true
				}
			}
			if !covered {
				out = append(out, approvals.Level{Level: top + 1, Permission: "sales.approve", Role: &gm, Label: lblDiscount})
			}
		}
	case ApprCancel:
		out = append(out, approvals.Level{Level: top + 1, Permission: "sales.cancel", Label: lblCancel})
	case ApprAmendment:
		if len(out) == 0 {
			out = append(out, approvals.Level{Level: 1, Permission: "sales.approve", Label: lblAmend})
		}
	}
	return out
}

type approvalStep struct {
	Level     int       `json:"level"`
	Label     string    `json:"label"`
	Decision  string    `json:"decision"`
	By        person    `json:"by"`
	At        jsTime    `json:"at"`
	Note      *string   `json:"note"`
	Auto      bool      `json:"auto"`
	decidedBy string    `json:"-"`
	at        time.Time `json:"-"`
}

type approvalOut struct {
	ID          string            `json:"id"`
	SaleID      string            `json:"saleId"`
	SaleNumber  string            `json:"saleNumber,omitempty"`
	Customer    string            `json:"customer,omitempty"`
	Kind        string            `json:"kind"`
	Status      string            `json:"status"`
	Amount      int64             `json:"amount"`
	Reason      *string           `json:"reason"`
	AmendmentID *string           `json:"amendmentId"`
	Levels      []approvals.Level `json:"levels"`
	Steps       []approvalStep    `json:"steps"`
	NextLevel   *approvals.Level  `json:"nextLevel"`
	CanDecide   bool              `json:"canDecide"`
	RequestedBy person            `json:"requestedBy"`
	RequestedAt jsTime            `json:"requestedAt"`
	DecidedAt   *jsTime           `json:"decidedAt"`
}

// nextLevel is the lowest level without a decision.
func nextLevel(levels []approvals.Level, steps []approvalStep) *approvals.Level {
	done := map[int]bool{}
	for _, s := range steps {
		done[s.Level] = true
	}
	for i := range levels {
		if !done[levels[i].Level] {
			l := levels[i]
			return &l
		}
	}
	return nil
}

// mayDecide: u may decide level l of a request by requester whose earlier levels were decided by `deciders`.
func mayDecide(u *auth.User, l approvals.Level, requester string, deciders map[string]bool) bool {
	if !l.Satisfies(u.Role) || u.ID == requester || deciders[u.ID] {
		return false
	}
	return true
}

func levelLabel(lang i18n.Lang, l string) string {
	if i18n.Has(l) {
		return i18n.T(lang, l)
	}
	return l
}

// openApproval creates a request. For CONFIRM, levels the requester satisfies are recorded as approved on request.
// Returns the approval id and whether it is already fully approved.
func (o *op) openApproval(h *saleHead, kind string, amount int64, levels []approvals.Level, reason, amendmentID string) (string, bool, error) {
	var pending bool
	if err := o.tx.QueryRow(o.ctx, `SELECT EXISTS (SELECT 1 FROM sale_approval WHERE sale_id = $1 AND status = 'PENDING')`, h.ID).Scan(&pending); err != nil {
		return "", false, err
	}
	if pending {
		return "", false, o.conflict("sales.err.approval_pending")
	}
	lv, _ := json.Marshal(levels)
	var id string
	if err := o.tx.QueryRow(o.ctx, `INSERT INTO sale_approval (sale_id, kind, amount, levels, reason, amendment_id, requested_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`, h.ID, kind, amount, lv, nilIfEmpty(reason), nilIfEmpty(amendmentID), o.u.ID).Scan(&id); err != nil {
		return "", false, err
	}
	remaining := 0
	for _, l := range levels {
		if kind == ApprConfirm && l.Satisfies(o.u.Role) {
			if _, err := o.tx.Exec(o.ctx, `INSERT INTO sale_approval_step (approval_id, level, label, decision, decided_by, auto) VALUES ($1,$2,$3,'APPROVED',$4,true)`,
				id, l.Level, l.Label, o.u.ID); err != nil {
				return "", false, err
			}
			continue
		}
		remaining++
	}
	if remaining == 0 {
		_, err := o.tx.Exec(o.ctx, `UPDATE sale_approval SET status = 'APPROVED', decided_at = $2 WHERE id = $1`, id, db.Now())
		return id, true, err
	}
	if err := o.audit(audit.Input{Action: "sales.approval.request", Entity: "Sale", EntityID: h.ID,
		Summary: fmt.Sprintf("Requested %s approval for %s (%s RWF)", kind, h.Number, format.Int(float64(amount))),
		After:   map[string]any{"approvalId": id, "kind": kind, "levels": levels, "reason": nilIfEmpty(reason)}}); err != nil {
		return "", false, err
	}
	return id, false, o.notifyApprovers(h, id, kind, amount, levels, nil)
}

// notifyApprovers tells whoever can decide the next level.
func (o *op) notifyApprovers(h *saleHead, approvalID, kind string, amount int64, levels []approvals.Level, steps []approvalStep) error {
	next := nextLevel(levels, steps)
	if next == nil {
		return nil
	}
	aud := notify.Audience{Permission: auth.Permission(next.Permission)}
	if next.Role != nil && *next.Role != "" {
		aud = notify.Audience{Role: auth.Role(*next.Role)}
	}
	ev, title, body := EvApprovalPending, "sales.notify.approval_title", "sales.notify.approval_body"
	if kind == ApprAmendment {
		ev, title, body = EvAmendmentRequested, "sales.notify.amend_req_title", "sales.notify.amend_req_body"
	}
	var requester string
	if err := o.tx.QueryRow(o.ctx, `SELECT requested_by FROM sale_approval WHERE id = $1`, approvalID).Scan(&requester); err != nil {
		return err
	}
	return notify.Send(o.ctx, o.tx, notify.Notification{Event: ev, Severity: notify.Warning, TitleKey: title, BodyKey: body,
		Params: map[string]any{"number": h.Number, "kind": i18n.T(o.lang, "sales.approval.kind."+kind), "amount": format.Int(float64(amount)), "level": next.Level},
		Link:   "/sales/" + h.ID, To: aud.Without(requester, o.u.ID), DedupKey: "appr:" + approvalID + ":" + fmt.Sprint(next.Level)})
}

// loadApproval reads one approval with its steps (names resolved).
func loadApproval(c ctxq, lang i18n.Lang, id string, lock bool) (*approvalOut, error) {
	sql := `SELECT a.id, a.sale_id, s.number, cu.name, a.kind, a.status, a.amount, a.reason, a.amendment_id, a.levels, a.requested_by, u.name, a.requested_at, a.decided_at
		FROM sale_approval a JOIN "Sale" s ON s.id = a.sale_id JOIN "Customer" cu ON cu.id = s."customerId" JOIN "User" u ON u.id = a.requested_by WHERE a.id = $1`
	if lock {
		sql += ` FOR UPDATE OF a`
	}
	var a approvalOut
	var lv []byte
	var reqAt time.Time
	var decAt *time.Time
	if err := c.q.QueryRow(c.ctx, sql, id).Scan(&a.ID, &a.SaleID, &a.SaleNumber, &a.Customer, &a.Kind, &a.Status, &a.Amount, &a.Reason, &a.AmendmentID, &lv,
		&a.RequestedBy.ID, &a.RequestedBy.Name, &reqAt, &decAt); err != nil {
		if db.IsNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	a.RequestedAt, a.DecidedAt = jt(reqAt), jtp(decAt)
	_ = json.Unmarshal(lv, &a.Levels)
	for i := range a.Levels {
		a.Levels[i].Label = levelLabel(lang, a.Levels[i].Label)
	}
	rows, err := c.q.Query(c.ctx, `SELECT st.level, st.label, st.decision, st.decided_by, u.name, st.decided_at, st.note, st.auto
		FROM sale_approval_step st JOIN "User" u ON u.id = st.decided_by WHERE st.approval_id = $1 ORDER BY st.level`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	a.Steps = []approvalStep{}
	for rows.Next() {
		var s approvalStep
		if err := rows.Scan(&s.Level, &s.Label, &s.Decision, &s.By.ID, &s.By.Name, &s.at, &s.Note, &s.Auto); err != nil {
			return nil, err
		}
		s.Label = levelLabel(lang, s.Label)
		s.decidedBy, s.At = s.By.ID, jt(s.at)
		a.Steps = append(a.Steps, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if a.Status == "PENDING" {
		a.NextLevel = nextLevel(a.Levels, a.Steps)
	}
	return &a, nil
}

func (a *approvalOut) withViewer(u *auth.User) *approvalOut {
	if a == nil {
		return nil
	}
	if a.Status == "PENDING" && a.NextLevel != nil {
		deciders := map[string]bool{}
		for _, s := range a.Steps {
			if !s.Auto {
				deciders[s.decidedBy] = true
			}
		}
		a.CanDecide = mayDecide(u, *a.NextLevel, a.RequestedBy.ID, deciders)
	}
	return a
}

// decideApproval records one level's decision and applies the effect once the last level approves.
func (o *op) decideApproval(id string, approve bool, note string) (*approvalOut, []stockWarning, error) {
	a, err := loadApproval(ctxq{o.ctx, o.tx}, o.lang, id, true)
	if err != nil {
		return nil, nil, err
	}
	if a == nil {
		return nil, nil, httpx.NotFound("entity.approval")
	}
	h, err := o.mustHead(a.SaleID)
	if err != nil {
		return nil, nil, err
	}
	if a.Status != "PENDING" || a.NextLevel == nil {
		return nil, nil, o.conflict("sales.err.approval_closed")
	}
	deciders := map[string]bool{}
	for _, s := range a.Steps {
		if !s.Auto {
			deciders[s.decidedBy] = true
		}
	}
	lvl := *a.NextLevel
	if !mayDecide(o.u, lvl, a.RequestedBy.ID, deciders) {
		if o.u.ID == a.RequestedBy.ID {
			return nil, nil, httpx.NewKey(403, "forbidden", "sales.err.own_request")
		}
		if deciders[o.u.ID] {
			return nil, nil, httpx.NewKey(403, "forbidden", "sales.err.already_decided")
		}
		return nil, nil, httpx.NewKey(403, "forbidden", "sales.err.not_approver", "level", levelLabel(o.lang, lvl.Label))
	}
	decision := "REJECTED"
	if approve {
		decision = "APPROVED"
	}
	if _, err := o.tx.Exec(o.ctx, `INSERT INTO sale_approval_step (approval_id, level, label, decision, decided_by, note) VALUES ($1,$2,$3,$4,$5,$6)`,
		id, lvl.Level, lvl.Label, decision, o.u.ID, nilIfEmpty(note)); err != nil {
		return nil, nil, err
	}
	if err := o.audit(audit.Input{Action: "sales.approval." + map[bool]string{true: "approve", false: "reject"}[approve], Entity: "Sale", EntityID: h.ID,
		Summary: fmt.Sprintf("%s level %d of %s approval for %s", decision, lvl.Level, a.Kind, h.Number),
		After:   map[string]any{"approvalId": id, "level": lvl.Level, "decision": decision, "note": nilIfEmpty(note)}}); err != nil {
		return nil, nil, err
	}
	a.Steps = append(a.Steps, approvalStep{Level: lvl.Level, Decision: decision, decidedBy: o.u.ID})
	// One approver's decision covers every further consecutive level they are entitled to (BTN has one person per
	// role: e.g. a 20 M RWF amendment or a cancellation of a ≥ 5 M order needs a sales.approve level and a Production
	// Manager / sales.cancel level, and only the PM is left once the Sales Manager is the requester). Four eyes is
	// kept: the requester never decides, and each covered level is recorded and audited.
	for approve {
		nl := nextLevel(a.Levels, a.Steps)
		if nl == nil || !nl.Satisfies(o.u.Role) {
			break
		}
		if _, err := o.tx.Exec(o.ctx, `INSERT INTO sale_approval_step (approval_id, level, label, decision, decided_by, note) VALUES ($1,$2,$3,'APPROVED',$4,$5)`,
			id, nl.Level, nl.Label, o.u.ID, nilIfEmpty(note)); err != nil {
			return nil, nil, err
		}
		if err := o.audit(audit.Input{Action: "sales.approval.approve", Entity: "Sale", EntityID: h.ID,
			Summary: fmt.Sprintf("APPROVED level %d of %s approval for %s (same approver)", nl.Level, a.Kind, h.Number),
			After:   map[string]any{"approvalId": id, "level": nl.Level, "decision": "APPROVED", "note": nilIfEmpty(note), "cascade": true}}); err != nil {
			return nil, nil, err
		}
		a.Steps = append(a.Steps, approvalStep{Level: nl.Level, Decision: "APPROVED", decidedBy: o.u.ID})
	}
	var warnings []stockWarning
	final := ""
	if !approve {
		final = "REJECTED"
	} else if nextLevel(a.Levels, a.Steps) == nil {
		final = "APPROVED"
	}
	switch final {
	case "":
		if err := o.notifyApprovers(h, id, a.Kind, a.Amount, a.Levels, a.Steps); err != nil {
			return nil, nil, err
		}
	case "REJECTED":
		if _, err := o.tx.Exec(o.ctx, `UPDATE sale_approval SET status = 'REJECTED', decided_at = $2 WHERE id = $1`, id, db.Now()); err != nil {
			return nil, nil, err
		}
		if a.Kind == ApprAmendment && a.AmendmentID != nil {
			if _, err := o.tx.Exec(o.ctx, `UPDATE sale_amendment SET status = 'REJECTED', decided_at = $2 WHERE id = $1`, *a.AmendmentID, db.Now()); err != nil {
				return nil, nil, err
			}
		}
	case "APPROVED":
		if _, err := o.tx.Exec(o.ctx, `UPDATE sale_approval SET status = 'APPROVED', decided_at = $2 WHERE id = $1`, id, db.Now()); err != nil {
			return nil, nil, err
		}
		switch a.Kind {
		case ApprConfirm:
			if !editable(h.Status) {
				return nil, nil, o.conflict("sales.err.bad_transition", "from", statusWord(o.lang, h.Status), "to", statusWord(o.lang, StConfirmed))
			}
			if warnings, err = o.confirmNow(h); err != nil {
				return nil, nil, err
			}
		case ApprCancel:
			reason := ""
			if a.Reason != nil {
				reason = *a.Reason
			}
			if err := o.cancelNow(h, reason); err != nil {
				return nil, nil, err
			}
		case ApprAmendment:
			if a.AmendmentID == nil {
				return nil, nil, o.conflict("sales.err.approval_closed")
			}
			if err := o.applyAmendment(h, *a.AmendmentID); err != nil {
				return nil, nil, err
			}
		}
	}
	if final != "" {
		ev, title, body := EvApprovalDecided, "sales.notify.decided_title", "sales.notify.decided_body"
		if a.Kind == ApprAmendment {
			ev, title, body = EvAmendmentApproved, "sales.notify.amend_done_title", "sales.notify.amend_done_body"
		}
		if err := notify.Send(o.ctx, o.tx, notify.Notification{Event: ev, Severity: notify.Info, TitleKey: title, BodyKey: body,
			Params: map[string]any{"number": h.Number, "kind": i18n.T(o.lang, "sales.approval.kind."+a.Kind), "decision": i18n.T(o.lang, "sales.approval.status."+final)},
			Link:   "/sales/" + h.ID, To: notify.ToUsers(a.RequestedBy.ID).Without(o.u.ID)}); err != nil {
			return nil, nil, err
		}
	}
	out, err := loadApproval(ctxq{o.ctx, o.tx}, o.lang, id, false)
	return out.withViewer(o.u), warnings, err
}

// withdrawApproval lets the requester take back a pending request.
func (o *op) withdrawApproval(id string) error {
	a, err := loadApproval(ctxq{o.ctx, o.tx}, o.lang, id, true)
	if err != nil {
		return err
	}
	if a == nil {
		return httpx.NotFound("entity.approval")
	}
	h, err := o.mustHead(a.SaleID)
	if err != nil {
		return err
	}
	if a.Status != "PENDING" {
		return o.conflict("sales.err.approval_closed")
	}
	if a.RequestedBy.ID != o.u.ID && !o.u.IsSuperAdmin() {
		return httpx.NewKey(403, "forbidden", "sales.err.withdraw_own")
	}
	if _, err := o.tx.Exec(o.ctx, `UPDATE sale_approval SET status = 'WITHDRAWN', decided_at = $2 WHERE id = $1`, id, db.Now()); err != nil {
		return err
	}
	if a.AmendmentID != nil {
		if _, err := o.tx.Exec(o.ctx, `UPDATE sale_amendment SET status = 'WITHDRAWN', decided_at = $2 WHERE id = $1`, *a.AmendmentID, db.Now()); err != nil {
			return err
		}
	}
	return o.audit(audit.Input{Action: "sales.approval.withdraw", Entity: "Sale", EntityID: h.ID, Summary: fmt.Sprintf("Withdrew %s approval request for %s", a.Kind, h.Number),
		After: map[string]any{"approvalId": id}})
}

// cancelNow cancels (draft, quotation or confirmed order without dispatches or payments).
func (o *op) cancelNow(h *saleHead, reason string) error {
	if !cancellable(h.Status) {
		return o.conflict("sales.err.cannot_cancel", "status", statusWord(o.lang, h.Status))
	}
	var dns []string
	if err := o.tx.QueryRow(o.ctx, `SELECT COALESCE(array_agg("dnNumber" ORDER BY "createdAt"), '{}') FROM "Delivery" WHERE "saleId"=$1 AND status <> 'CANCELLED'`, h.ID).Scan(&dns); err != nil {
		return err
	}
	if len(dns) > 0 {
		return o.conflict("sales.err.cancel_dns_first", "dns", joinComma(dns))
	}
	var pays int
	if err := o.tx.QueryRow(o.ctx, `SELECT COUNT(*) FROM "Payment" WHERE "saleId"=$1`, h.ID).Scan(&pays); err != nil {
		return err
	}
	if pays > 0 {
		return o.conflict("sales.err.cancel_has_payments")
	}
	from := h.Status
	if err := o.transition(h, StCancelled, reason); err != nil {
		return err
	}
	if _, err := o.tx.Exec(o.ctx, `UPDATE sale_approval SET status = 'WITHDRAWN', decided_at = $2 WHERE sale_id = $1 AND status = 'PENDING' AND kind <> 'CANCEL'`, h.ID, db.Now()); err != nil {
		return err
	}
	return o.audit(audit.Input{Action: "sales.cancel", Entity: "Sale", EntityID: h.ID, Summary: fmt.Sprintf("Cancelled %s: %s", h.Number, reason),
		Before: map[string]any{"status": from}, After: map[string]any{"status": StCancelled, "reason": reason}})
}

// ───────────────────────────── HTTP ─────────────────────────────

// pendingApproval returns the open request of an order, if any.
func pendingApproval(c ctxq, lang i18n.Lang, saleID string) (*approvalOut, error) {
	var id string
	err := c.q.QueryRow(c.ctx, `SELECT id FROM sale_approval WHERE sale_id = $1 AND status = 'PENDING'`, saleID).Scan(&id)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return loadApproval(c, lang, id, false)
}

// listApprovals: GET /approvals?status=PENDING|APPROVED|REJECTED|WITHDRAWN&mine=1 (mine = I can decide the next level).
func (h *handler) listApprovals(c *fiber.Ctx) error {
	r := newRq(c)
	q := httpx.Query(c)
	status := queryEnum(q, "status", "PENDING", "APPROVED", "REJECTED", "WITHDRAWN")
	mine := q.OptStr("mine").Get() == "1"
	if err := q.Err(); err != nil {
		return err
	}
	if status == "" {
		status = "PENDING"
	}
	plant := auth.ScopedPlantID(r.u)
	rows, err := h.d.Pool.Query(r.ctx, `SELECT a.id FROM sale_approval a JOIN "Sale" s ON s.id = a.sale_id
		WHERE a.status = $1 AND ($2 = '' OR s."plantId" = $2) ORDER BY a.requested_at DESC LIMIT 200`, status, plant)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	items := []*approvalOut{}
	for _, id := range ids { // ≤ 200 rows; each approval needs its steps (2 small indexed queries)
		a, err := loadApproval(ctxq{r.ctx, h.d.Pool}, r.lang, id, false)
		if err != nil {
			return err
		}
		if a == nil {
			continue
		}
		a.withViewer(r.u)
		if mine && !a.CanDecide {
			continue
		}
		items = append(items, a)
	}
	return c.JSON(fiber.Map{"items": items})
}

func (h *handler) decide(approve bool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		r := newRq(c)
		v, err := httpx.Body(c)
		if err != nil {
			return err
		}
		note := ""
		if approve {
			if p := newIn(v, r.lang).optText("note", 500); p != nil {
				note = *p
			}
		} else {
			note = newIn(v, r.lang).reqText("note", 500, r.t("sales.v.give_reason"))
		}
		if err := v.Err(); err != nil {
			return err
		}
		type res struct {
			a *approvalOut
			w []stockWarning
		}
		out, err := db.TxVal(r.ctx, h.d.Pool, func(tx pgx.Tx) (res, error) {
			a, w, err := r.op(tx).decideApproval(c.Params("id"), approve, note)
			return res{a, w}, err
		})
		if err != nil {
			return err
		}
		invalidate()
		if out.w == nil {
			out.w = []stockWarning{}
		}
		return c.JSON(fiber.Map{"approval": out.a, "warnings": out.w})
	}
}

func (h *handler) withdraw(c *fiber.Ctx) error {
	r := newRq(c)
	if err := db.Tx(r.ctx, h.d.Pool, func(tx pgx.Tx) error { return r.op(tx).withdrawApproval(c.Params("id")) }); err != nil {
		return err
	}
	invalidate()
	return c.JSON(fiber.Map{"ok": true})
}
