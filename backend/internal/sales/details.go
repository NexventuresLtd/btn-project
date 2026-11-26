package sales

import (
	"context"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/i18n"
	"btn-ops/internal/refdata"
)

// loadSaleExtras adds the order's approvals, amendments, versions, status history, communications and the
// localized names of its reference codes (one query each, never per row).
func (s *service) loadSaleExtras(ctx context.Context, q db.DBTX, lang i18n.Lang, u *auth.User, sd *saleDetail) error {
	if sd.PaymentTerm != nil {
		n := refdata.Name(ctx, q, refdata.KindPaymentTerm, *sd.PaymentTerm, lang)
		sd.PaymentTermName = &n
	}
	if sd.DeliveryZone != nil {
		n := refdata.Name(ctx, q, refdata.KindDeliveryZone, *sd.DeliveryZone, lang)
		sd.DeliveryZoneName = &n
	}
	rows, err := q.Query(ctx, `SELECT id FROM sale_approval WHERE sale_id = $1 ORDER BY requested_at DESC`, sd.ID)
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
	sd.Approvals = []*approvalOut{}
	for _, id := range ids { // an order has a handful of approvals at most
		a, err := loadApproval(ctxq{ctx, q}, lang, id, false)
		if err != nil {
			return err
		}
		if a == nil {
			continue
		}
		a.withViewer(u)
		if a.Status == "PENDING" {
			sd.PendingApproval = a
		}
		sd.Approvals = append(sd.Approvals, a)
	}
	if sd.Amendments, err = loadAmendments(ctx, q, sd.ID); err != nil {
		return err
	}
	if sd.Versions, err = loadVersions(ctx, q, sd.ID); err != nil {
		return err
	}
	hr, err := q.Query(ctx, `SELECT h.from_status, h.to_status, h.changed_by, u.name, h.changed_at, h.note FROM sale_status_history h
		LEFT JOIN "User" u ON u.id = h.changed_by WHERE h.sale_id = $1 ORDER BY h.id`, sd.ID)
	if err != nil {
		return err
	}
	defer hr.Close()
	sd.History = []historyOut{}
	for hr.Next() {
		var h historyOut
		var by, name *string
		var at time.Time
		if err := hr.Scan(&h.From, &h.To, &by, &name, &at, &h.Note); err != nil {
			return err
		}
		if by != nil {
			h.By = &person{ID: *by, Name: deref(name)}
		}
		h.At = jt(at)
		sd.History = append(sd.History, h)
	}
	if err := hr.Err(); err != nil {
		return err
	}
	hr.Close()
	sd.Communications, err = listComms(ctx, q, sd.Customer.ID, sd.ID, "", "", 50, 0)
	return err
}
