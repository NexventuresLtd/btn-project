package sales

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"btn-ops/internal/approvals"
	"btn-ops/internal/audit"
	"btn-ops/internal/db"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// Amendments (ORD-10): the commercial terms of a confirmed order are locked. A change is requested as an amendment
// (the full proposed lines + terms), approved through the sales_order approval levels, then applied in one transaction:
// lines updated in place (delivered quantities protected), charge/deposit/due date recomputed, version + 1 and the new
// version snapshot stored. Every step is audited.

type ctxq struct {
	ctx context.Context
	q   db.DBTX
}

// amendProposal is what an amendment would set (stored as JSON).
type amendProposal struct {
	Lines           []amendLine `json:"lines"`
	PaymentTerm     *string     `json:"paymentTerm"`
	TransportMode   string      `json:"transportMode"`
	DeliveryAddress *string     `json:"deliveryAddress"`
	DeliverySite    *string     `json:"deliverySite"`
	RequestedDate   *string     `json:"requestedDate"`
	DeliveryZone    *string     `json:"deliveryZone"`
	DistanceKm      *float64    `json:"distanceKm"`
}

type amendLine struct {
	SaleLineID      string   `json:"saleLineId,omitempty"`
	ProductID       string   `json:"productId"`
	Quantity        int64    `json:"quantity"`
	UnitPrice       int64    `json:"unitPrice"`
	DiscountPercent *float64 `json:"discountPercent"`
	DiscountAmount  int64    `json:"discountAmount"`
}

func (l amendLine) in() lineIn {
	return lineIn{SaleLineID: l.SaleLineID, ProductID: l.ProductID, Quantity: l.Quantity, UnitPrice: l.UnitPrice, DiscountPercent: l.DiscountPercent, DiscountAmount: l.DiscountAmount}
}

// amendIssue is one problem of a proposal against the current order (pure check).
type amendIssue struct {
	Field, Key string
	Args       []any
}

type existingLine struct {
	ID, ProductID       string
	Quantity, Delivered int64
}

// checkAmendment: kept lines must exist and keep their product; quantities never drop below what was delivered;
// removed lines must have nothing delivered.
func checkAmendment(current []existingLine, lines []amendLine) []amendIssue {
	byID := map[string]existingLine{}
	for _, l := range current {
		byID[l.ID] = l
	}
	kept := map[string]bool{}
	var out []amendIssue
	for i, l := range lines {
		if l.SaleLineID == "" {
			continue
		}
		cur, ok := byID[l.SaleLineID]
		if !ok {
			out = append(out, amendIssue{fmt.Sprintf("lines.%d.saleLineId", i), issNotInSale, nil})
			continue
		}
		if kept[l.SaleLineID] {
			out = append(out, amendIssue{fmt.Sprintf("lines.%d.saleLineId", i), issTwice, nil})
			continue
		}
		kept[l.SaleLineID] = true
		if cur.ProductID != l.ProductID && cur.Delivered > 0 {
			out = append(out, amendIssue{fmt.Sprintf("lines.%d.productId", i), "sales.issue.product_locked", nil})
		}
		if l.Quantity < cur.Delivered {
			out = append(out, amendIssue{fmt.Sprintf("lines.%d.quantity", i), "sales.issue.below_delivered", []any{"n", cur.Delivered}})
		}
	}
	for _, c := range current {
		if !kept[c.ID] && c.Delivered > 0 {
			out = append(out, amendIssue{"lines", "sales.issue.remove_delivered", []any{"n", c.Delivered}})
		}
	}
	return out
}

func currentLines(ctx context.Context, q db.DBTX, saleID string) ([]existingLine, error) {
	rows, err := q.Query(ctx, `SELECT sl.id, sl."productId", sl.quantity, COALESCE((SELECT SUM(dl.quantity) FROM "DeliveryLine" dl JOIN "Delivery" d ON d.id = dl."deliveryId"
		WHERE dl."saleLineId" = sl.id AND d.status <> 'CANCELLED'), 0)::bigint FROM "SaleLine" sl WHERE sl."saleId" = $1 AND sl.kind = 'PRODUCT' ORDER BY sl.id`, saleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []existingLine
	for rows.Next() {
		var l existingLine
		if err := rows.Scan(&l.ID, &l.ProductID, &l.Quantity, &l.Delivered); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// proposalTotal: net product lines + the charge the proposal would get.
func (o *op) proposalTotal(p amendProposal) (int64, error) {
	var total int64
	for _, l := range p.Lines {
		total += l.in().calc().Net
	}
	if p.TransportMode == TransportBTN {
		rules, err := loadChargeRules(o.ctx, o.tx, true)
		if err != nil {
			return 0, err
		}
		z := ""
		if p.DeliveryZone != nil {
			z = *p.DeliveryZone
		}
		a, _ := pickCharge(rules, z, p.DistanceKm)
		total += a
	}
	return total, nil
}

func (o *op) issuesErr(issues []amendIssue) error {
	if len(issues) == 0 {
		return nil
	}
	f := map[string]string{}
	for _, i := range issues {
		setFirst(f, i.Field, o.t(i.Key, i.Args...))
	}
	return httpx.BadRequest("sales.err.fix_lines", f)
}

// requestAmendment stores a PENDING amendment and opens its approval.
func (o *op) requestAmendment(h *saleHead, reason string, p amendProposal) (string, error) {
	if !amendable(h.Status) {
		return "", o.conflict("sales.err.not_amendable", "status", statusWord(o.lang, h.Status))
	}
	cur, err := currentLines(o.ctx, o.tx, h.ID)
	if err != nil {
		return "", err
	}
	if err := o.issuesErr(checkAmendment(cur, p.Lines)); err != nil {
		return "", err
	}
	newTotal, err := o.proposalTotal(p)
	if err != nil {
		return "", err
	}
	var paid int64
	if err := o.tx.QueryRow(o.ctx, `SELECT COALESCE(SUM(amount), 0)::bigint FROM "Payment" WHERE "saleId" = $1`, h.ID).Scan(&paid); err != nil {
		return "", err
	}
	if newTotal < paid {
		return "", httpx.BadRequest("error.validation", map[string]string{"lines": o.t("sales.v.below_paid", "amount", format.Int(float64(paid)))})
	}
	before, _, err := commercialSnapshot(o.ctx, o.tx, h.ID)
	if err != nil {
		return "", err
	}
	prop, _ := json.Marshal(p)
	var id string
	if err := o.tx.QueryRow(o.ctx, `INSERT INTO sale_amendment (sale_id, from_version, reason, proposed, before, old_total, new_total, requested_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, h.ID, h.Version, reason, prop, before, h.Total, newTotal, o.u.ID).Scan(&id); err != nil {
		return "", err
	}
	if err := o.audit(audit.Input{Action: "sales.amendment.request", Entity: "Sale", EntityID: h.ID,
		Summary: fmt.Sprintf("Amendment requested on %s v%d: %s → %s RWF (%s)", h.Number, h.Version, format.Int(float64(h.Total)), format.Int(float64(newTotal)), reason),
		Before:  before, After: p}); err != nil {
		return "", err
	}
	th, err := approvals.LevelsWith(o.ctx, o.tx, approvals.SalesOrder, max(newTotal, h.Total))
	if err != nil {
		return "", err
	}
	if _, _, err := o.openApproval(h, ApprAmendment, newTotal, requiredLevels(ApprAmendment, th, false), reason, id); err != nil {
		return "", err
	}
	_, err = o.tx.Exec(o.ctx, `UPDATE sale_approval SET amendment_id = $2 WHERE sale_id = $1 AND status = 'PENDING'`, h.ID, id)
	return id, err
}

// applyAmendment writes an approved amendment (re-validated against the order as it is now).
func (o *op) applyAmendment(h *saleHead, amendmentID string) error {
	var raw []byte
	var status string
	var fromVersion int
	if err := o.tx.QueryRow(o.ctx, `SELECT proposed, status, from_version FROM sale_amendment WHERE id = $1 FOR UPDATE`, amendmentID).Scan(&raw, &status, &fromVersion); err != nil {
		return err
	}
	if status != "PENDING" {
		return o.conflict("sales.err.approval_closed")
	}
	if fromVersion != h.Version {
		return o.conflict("sales.err.amend_stale")
	}
	if !amendable(h.Status) {
		return o.conflict("sales.err.not_amendable", "status", statusWord(o.lang, h.Status))
	}
	var p amendProposal
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	cur, err := currentLines(o.ctx, o.tx, h.ID)
	if err != nil {
		return err
	}
	if err := o.issuesErr(checkAmendment(cur, p.Lines)); err != nil {
		return err
	}
	before, _, err := commercialSnapshot(o.ctx, o.tx, h.ID)
	if err != nil {
		return err
	}
	kept := map[string]bool{}
	var fresh []lineIn
	for _, l := range p.Lines {
		if l.SaleLineID == "" {
			fresh = append(fresh, l.in())
			continue
		}
		kept[l.SaleLineID] = true
		c := l.in().calc()
		if _, err := o.tx.Exec(o.ctx, `UPDATE "SaleLine" SET "productId" = $2, quantity = $3, "unitPrice" = $4, amount = $5, "discountAmount" = $6, "discountPercent" = $7
			WHERE id = $1`, l.SaleLineID, l.ProductID, l.Quantity, l.UnitPrice, c.Net, c.Discount, l.DiscountPercent); err != nil {
			return err
		}
	}
	for _, c := range cur {
		if !kept[c.ID] {
			if _, err := o.tx.Exec(o.ctx, `DELETE FROM "SaleLine" WHERE id = $1`, c.ID); err != nil {
				return err
			}
		}
	}
	if len(fresh) > 0 {
		if err := insertSaleLines(o.ctx, o.tx, h.ID, fresh); err != nil {
			return err
		}
	}
	newVersion := h.Version + 1
	if _, err := o.tx.Exec(o.ctx, `UPDATE "Sale" SET "paymentTerm" = $2, "transportMode" = $3, "deliveryAddress" = $4, "deliverySite" = $5, "requestedDate" = $6::date,
		"deliveryZone" = $7, "distanceKm" = $8, version = $9, "updatedAt" = $10 WHERE id = $1`,
		h.ID, p.PaymentTerm, p.TransportMode, p.DeliveryAddress, p.DeliverySite, p.RequestedDate, p.DeliveryZone, p.DistanceKm, newVersion, db.Now()); err != nil {
		return err
	}
	if err := o.applyCharge(h.ID); err != nil {
		return err
	}
	if err := o.applyTerms(h.ID); err != nil {
		return err
	}
	if err := o.saveVersion(h.ID, newVersion, amendmentID); err != nil {
		return err
	}
	if _, err := o.tx.Exec(o.ctx, `UPDATE sale_amendment SET status = 'APPLIED', to_version = $2, decided_at = $3 WHERE id = $1`, amendmentID, newVersion, db.Now()); err != nil {
		return err
	}
	after, total, err := commercialSnapshot(o.ctx, o.tx, h.ID)
	if err != nil {
		return err
	}
	h.Version, h.Total = newVersion, total
	if err := o.audit(audit.Input{Action: "sales.amendment.apply", Entity: "Sale", EntityID: h.ID,
		Summary: fmt.Sprintf("Applied amendment on %s: version %d → %d (%s RWF)", h.Number, newVersion-1, newVersion, format.Int(float64(total))),
		Before:  before, After: after}); err != nil {
		return err
	}
	// a quantity reduction may complete the order
	return o.afterShipChange(h)
}

// ───────────────────────────── HTTP ─────────────────────────────

type amendmentOut struct {
	ID          string          `json:"id"`
	Status      string          `json:"status"`
	FromVersion int             `json:"fromVersion"`
	ToVersion   *int            `json:"toVersion"`
	Reason      string          `json:"reason"`
	Proposed    json.RawMessage `json:"proposed"`
	Before      json.RawMessage `json:"before"`
	OldTotal    int64           `json:"oldTotal"`
	NewTotal    int64           `json:"newTotal"`
	RequestedBy person          `json:"requestedBy"`
	RequestedAt jsTime          `json:"requestedAt"`
	DecidedAt   *jsTime         `json:"decidedAt"`
}

type versionOut struct {
	Version     int             `json:"version"`
	Total       int64           `json:"total"`
	Snapshot    json.RawMessage `json:"snapshot"`
	AmendmentID *string         `json:"amendmentId"`
	CreatedBy   person          `json:"createdBy"`
	CreatedAt   jsTime          `json:"createdAt"`
}

func loadAmendments(ctx context.Context, q db.DBTX, saleID string) ([]amendmentOut, error) {
	rows, err := q.Query(ctx, `SELECT a.id, a.status, a.from_version, a.to_version, a.reason, a.proposed, a.before, a.old_total, a.new_total, u.id, u.name, a.requested_at, a.decided_at
		FROM sale_amendment a JOIN "User" u ON u.id = a.requested_by WHERE a.sale_id = $1 ORDER BY a.requested_at DESC`, saleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []amendmentOut{}
	for rows.Next() {
		var a amendmentOut
		var at time.Time
		var dec *time.Time
		if err := rows.Scan(&a.ID, &a.Status, &a.FromVersion, &a.ToVersion, &a.Reason, &a.Proposed, &a.Before, &a.OldTotal, &a.NewTotal, &a.RequestedBy.ID, &a.RequestedBy.Name, &at, &dec); err != nil {
			return nil, err
		}
		a.RequestedAt, a.DecidedAt = jt(at), jtp(dec)
		out = append(out, a)
	}
	return out, rows.Err()
}

func loadVersions(ctx context.Context, q db.DBTX, saleID string) ([]versionOut, error) {
	rows, err := q.Query(ctx, `SELECT v.version, v.total, v.snapshot, v.amendment_id, u.id, u.name, v.created_at
		FROM sale_version v JOIN "User" u ON u.id = v.created_by WHERE v.sale_id = $1 ORDER BY v.version DESC`, saleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []versionOut{}
	for rows.Next() {
		var v versionOut
		var at time.Time
		if err := rows.Scan(&v.Version, &v.Total, &v.Snapshot, &v.AmendmentID, &v.CreatedBy.ID, &v.CreatedBy.Name, &at); err != nil {
			return nil, err
		}
		v.CreatedAt = jt(at)
		out = append(out, v)
	}
	return out, rows.Err()
}

// parseAmendment validates POST /:id/amendments.
func (x in) parseAmendment() (string, amendProposal) {
	reason := x.reqText("reason", 500, x.t("sales.v.give_reason"))
	t := x.parseTerms(false)
	var p amendProposal
	for _, l := range x.parseLines(true) {
		p.Lines = append(p.Lines, amendLine{SaleLineID: l.SaleLineID, ProductID: l.ProductID, Quantity: l.Quantity, UnitPrice: l.UnitPrice,
			DiscountPercent: l.DiscountPercent, DiscountAmount: l.DiscountAmount})
	}
	p.PaymentTerm, p.TransportMode, p.DeliveryAddress, p.DeliverySite = t.PaymentTerm, t.TransportMode, t.DeliveryAddress, t.DeliverySite
	p.RequestedDate, p.DeliveryZone, p.DistanceKm = t.RequestedDate, t.DeliveryZone, t.DistanceKm
	return reason, p
}

func (h *handler) requestAmendment(c *fiber.Ctx) error {
	r := newRq(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	reason, p := newIn(v, r.lang).parseAmendment()
	if err := v.Err(); err != nil {
		return err
	}
	id, err := db.TxVal(r.ctx, h.d.Pool, func(tx pgx.Tx) (string, error) {
		o := r.op(tx)
		hd, err := o.mustHead(c.Params("id"))
		if err != nil {
			return "", err
		}
		lines := make([]lineIn, 0, len(p.Lines))
		for _, l := range p.Lines {
			lines = append(lines, l.in())
		}
		if err := r.validateSaleRefs(tx, "", "", lines, true); err != nil {
			return "", err
		}
		if err := o.validateTerms(terms{PaymentTerm: p.PaymentTerm, DeliveryZone: p.DeliveryZone}); err != nil {
			return "", err
		}
		return o.requestAmendment(hd, reason, p)
	})
	if err != nil {
		return err
	}
	invalidate()
	return h.respondSale(c, r, c.Params("id"), 201, fiber.Map{"amendmentId": id})
}
