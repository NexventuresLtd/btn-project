package sales

import (
	"context"
	"fmt"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/format"
	"btn-ops/internal/jobs"
	"btn-ops/internal/notify"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Notification events of orders & delivery (labels: notify.event.<key>[.help] in the sales catalogs).
const (
	EvApprovalPending    = "sales.approval_pending"
	EvApprovalDecided    = "sales.approval_decided"
	EvQuotationAccepted  = "sales.quotation_accepted"
	EvConfirmed          = "sales.order_confirmed"
	EvShortfall          = "sales.order_shortfall"
	EvDispatched         = "sales.dispatched"
	EvDelivered          = "sales.delivered"
	EvAmendmentRequested = "sales.amendment_requested"
	EvAmendmentApproved  = "sales.amendment_decided"
	EvPaymentOverdue     = "sales.payment_overdue"
)

// OverdueJob is the daily scan for orders whose balance is past its due date.
const OverdueJob = "sales.overdue_payments"

func init() {
	ev := func(key, cat string, email bool, perms ...auth.Permission) {
		notify.RegisterEvent(notify.Event{Key: key, Category: cat, DefaultEmail: email, Permissions: perms})
	}
	ev(EvApprovalPending, notify.CatApprovals, true, "sales.approve", "sales.cancel")
	ev(EvApprovalDecided, notify.CatSales, false, "sales.create", "sales.confirm", "sales.cancel")
	ev(EvQuotationAccepted, notify.CatSales, true, "sales.create")
	ev(EvConfirmed, notify.CatSales, false, "deliveries.create", "sales.view")
	ev(EvShortfall, notify.CatProduction, true, "production.view")
	ev(EvDispatched, notify.CatSales, false, "sales.view")
	ev(EvDelivered, notify.CatSales, false, "sales.view", "payments.record")
	ev(EvAmendmentRequested, notify.CatApprovals, true, "sales.approve")
	ev(EvAmendmentApproved, notify.CatSales, true, "sales.create")
	ev(EvPaymentOverdue, notify.CatFinance, true, "payments.record", "sales.view")
}

// registerJobs installs the daily overdue scan (07:30 Kigali).
func registerJobs(pool *pgxpool.Pool) {
	jobs.Register(jobs.Job{Name: OverdueJob, Schedule: "30 7 * * *", Timeout: 2 * time.Minute, Retries: 2, Backoff: time.Minute,
		DescriptionKey: "sales.job.overdue", Run: func(ctx context.Context) error { _, err := RunOverdueScan(ctx, pool, dates.TodayISO()); return err }})
}

// RunOverdueScan notifies finance and the order's people about every open order whose balance is past due
// (one notification per order and due date: DedupKey). Returns the number of overdue orders.
func RunOverdueScan(ctx context.Context, pool *pgxpool.Pool, todayISO string) (int, error) {
	rows, err := pool.Query(ctx, `
		WITH s AS (SELECT id, number, "createdById", "confirmedById", "dueDate", "plantId", "customerId" FROM "Sale"
		           WHERE "dueDate" < $1::date AND "orderStatus" NOT IN ('DRAFT','QUOTED','CLOSED','CANCELLED'))
		SELECT s.id, s.number, s."createdById", s."confirmedById", s."dueDate", c.name,
		  COALESCE((SELECT SUM(amount) FROM "SaleLine" WHERE "saleId" = s.id), 0) - COALESCE((SELECT SUM(amount) FROM "Payment" WHERE "saleId" = s.id), 0)
		FROM s JOIN "Customer" c ON c.id = s."customerId"`, todayISO)
	if err != nil {
		return 0, err
	}
	type od struct {
		id, number, createdBy, customer string
		confirmedBy                     *string
		due                             time.Time
		balance                         int64
	}
	var list []od
	for rows.Next() {
		var o od
		if err := rows.Scan(&o.id, &o.number, &o.createdBy, &o.confirmedBy, &o.due, &o.customer, &o.balance); err != nil {
			rows.Close()
			return 0, err
		}
		if o.balance > 0 {
			list = append(list, o)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, o := range list {
		ids := []string{o.createdBy}
		if o.confirmedBy != nil {
			ids = append(ids, *o.confirmedBy)
		}
		if err := notify.Send(ctx, pool, notify.Notification{Event: EvPaymentOverdue, Severity: notify.Warning, TitleKey: "sales.notify.overdue_title",
			BodyKey: "sales.notify.overdue_body", Params: map[string]any{"number": o.number, "customer": o.customer, "amount": format.Int(float64(o.balance)),
				"due": dates.FormatDate(o.due)}, Link: "/sales/" + o.id,
			To: notify.Audience{UserIDs: ids, Permission: "payments.record"}, DedupKey: fmt.Sprintf("overdue:%s:%s", o.id, dates.ToISO(o.due))}); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}
