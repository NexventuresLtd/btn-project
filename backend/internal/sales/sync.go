package sales

// Offline capture of orders and dispatch notes (SRS §5.2, §5.5, OFF-10) — record types shared with
// frontend/src/modules/sales/offline.ts.
//
//	order_draft   { plantId, customerId, date, lines:[{productId, quantity, unitPrice, discountPercent?, discountAmount?}], notes?,
//	                paymentTerm?, transportMode?, deliveryAddress?, deliverySite?, requestedDate?, deliveryZone?, distanceKm?, validUntil? }
//	              (+ "orderId" when an offline edit targets an order already on the server; otherwise uuid = order id, §6.3)
//	              → creates or updates a DRAFT. Same validation as POST /sales. If the order moved on
//	                server-side (quoted, confirmed, cancelled …) the server status wins and the offline edit goes to review
//	                (syncsvc.Conflict). Stock availability is checked at sync ("reservation confirmed on sync"): a shortfall
//	                is returned as flag SHORTFALL and notified to the Production Manager.
//	dispatch_note { saleId, dnNumber, date, plantId?, vehicle?, driver?, destination?, lines:[{saleLineId, quantity}] }
//	              → a delivery note (uuid = delivery id) on a READY / partially DISPATCHED order, stock reduced at the dispatching
//	                location. Order not dispatchable any more (or not yet) → conflict for review; the goods already left, so a
//	                stock shortfall is accepted with NEGATIVE_STOCK (review queue, OFF-18).
//
// Pulls: "orders" (open orders + last 60 days, with lines; site-scoped) and "dispatches" (last 60 days; site-scoped).

import (
	"context"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/i18n"
	"btn-ops/internal/syncsvc"

	"github.com/jackc/pgx/v5"
)

const (
	SyncOrderDraft   = "order_draft"
	SyncDispatchNote = "dispatch_note"
	FlagShortfall    = "SHORTFALL"
)

func registerSync() {
	syncsvc.RegisterHandler(SyncOrderDraft, syncsvc.HandlerFunc(applyOrderDraft), syncsvc.Requires("sales.create"))
	syncsvc.RegisterHandler(SyncDispatchNote, syncsvc.HandlerFunc(applyDispatchNote), syncsvc.Requires("deliveries.create"))
	syncsvc.RegisterPull("orders", pullOrders, syncsvc.PullRequires("sales.view", "sales.create", "deliveries.create"))
	syncsvc.RegisterPull("dispatches", pullDispatches, syncsvc.PullRequires("sales.view", "deliveries.create"))
}

func syncOp(ctx context.Context, tx pgx.Tx, a syncsvc.Actor) (*op, rq) {
	u := a.User()
	return &op{ctx: ctx, tx: tx, u: u, lang: a.Lang, aud: a.Audit()}, rq{ctx: ctx, u: u, lang: a.Lang}
}

func applyOrderDraft(ctx context.Context, tx pgx.Tx, rec syncsvc.Record, a syncsvc.Actor) syncsvc.Result {
	v, err := rec.Body(a.Lang)
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonInvalidRecord, err.Error())
	}
	s := newIn(v, a.Lang).parseSale(false)
	s.Confirm, s.Quote = false, false
	if err := v.Err(); err != nil {
		return syncsvc.FromError(a.Lang, err)
	}
	if r := a.AssertPlant(s.PlantID); r != nil {
		return *r
	}
	// the order id: the record uuid when the order is created offline; "orderId" when a later offline edit
	// (a new record) targets an order that already reached the server
	orderID := rec.UUID
	if oid := strings.TrimSpace(v.OptStr("orderId").Get()); oid != "" {
		orderID = oid
	}
	o, r := syncOp(ctx, tx, a)
	if err := r.validateSaleRefs(tx, s.PlantID, s.CustomerID, s.Lines, true); err != nil {
		return syncsvc.FromError(a.Lang, err)
	}
	cur, err := loadHead(ctx, tx, orderID, true)
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	if cur == nil {
		if orderID != rec.UUID {
			return syncsvc.Reject(syncsvc.ReasonNotFound, i18n.T(a.Lang, "sales.sync.order_unknown"))
		}
		h, err := o.createOrder(s, rec.UUID, true)
		if err != nil {
			return syncsvc.FromError(a.Lang, err)
		}
		return o.draftResult(h, rec)
	}
	if err := auth.AssertPlantAccess(o.u, cur.PlantID); err != nil {
		return syncsvc.FromError(a.Lang, err)
	}
	// §5.5: the server status wins — an offline edit of an order that moved on goes to review.
	changedByOther := cur.UpdatedAt.After(rec.CapturedAt) && cur.CreatedBy != a.UserID
	if (cur.Status != StDraft || changedByOther) && !rec.Force {
		return syncsvc.Conflict(ctx, tx, rec, a, syncsvc.ConflictInput{Entity: "Sale", EntityID: cur.ID, PlantID: cur.PlantID,
			ServerVersion: cur.UpdatedAt.UnixMilli(), Reason: i18n.T(a.Lang, "sales.sync.order_moved", "number", cur.Number, "status", statusWord(a.Lang, cur.Status)),
			ServerData: map[string]any{"id": cur.ID, "number": cur.Number, "status": cur.Status, "total": cur.Total, "version": cur.Version, "updatedAt": cur.UpdatedAt}})
	}
	if !editable(cur.Status) {
		return syncsvc.Reject(syncsvc.ReasonConflict, i18n.T(a.Lang, "sales.err.locked_use_amendment"))
	}
	if err := o.updateDraft(cur, s); err != nil {
		return syncsvc.FromError(a.Lang, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE "Sale" SET "capturedOffline" = true WHERE id = $1`, cur.ID); err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	h, err := loadHead(ctx, tx, cur.ID, false)
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	return o.draftResult(h, rec)
}

// draftResult checks availability at sync time and tags the ledger row.
func (o *op) draftResult(h *saleHead, rec syncsvc.Record) syncsvc.Result {
	lines, err := orderAvailability(o.ctx, o.tx, h.ID, h.PlantID, dates.MustParseISO(dates.TodayISO()))
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	var flags []string
	for _, l := range lines {
		if l.Shortfall > 0 {
			flags = []string{FlagShortfall}
			break
		}
	}
	if len(flags) > 0 {
		if err := o.notifyShortfall(h, lines, false); err != nil {
			return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
		}
	}
	var ver int64
	if err := o.tx.QueryRow(o.ctx, `SELECT server_version FROM "Sale" WHERE id = $1`, h.ID).Scan(&ver); err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	invalidate()
	res := syncsvc.Accept(h.ID, ver, flags...)
	res.PlantID, res.Entity, res.EntityID = h.PlantID, "Sale", h.ID
	return res
}

func applyDispatchNote(ctx context.Context, tx pgx.Tx, rec syncsvc.Record, a syncsvc.Actor) syncsvc.Result {
	v, err := rec.Body(a.Lang)
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonInvalidRecord, err.Error())
	}
	x := newIn(v, a.Lang)
	saleID := x.id("saleId")
	in := x.parseDelivery()
	if err := v.Err(); err != nil {
		return syncsvc.FromError(a.Lang, err)
	}
	o, _ := syncOp(ctx, tx, a)
	var existing string
	if err := tx.QueryRow(ctx, `SELECT "saleId" FROM "Delivery" WHERE id = $1`, rec.UUID).Scan(&existing); err == nil {
		var ver int64
		_ = tx.QueryRow(ctx, `SELECT server_version FROM "Delivery" WHERE id = $1`, rec.UUID).Scan(&ver)
		return syncsvc.Accept(rec.UUID, ver) // already applied (e.g. resubmitted after a lost acknowledgement)
	} else if !db.IsNoRows(err) {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	h, err := loadHead(ctx, tx, saleID, true)
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	if h == nil {
		return syncsvc.Reject(syncsvc.ReasonNotFound, i18n.T(a.Lang, "sales.sync.order_unknown"))
	}
	plantID := in.PlantID
	if plantID == "" {
		plantID = h.PlantID
	}
	if r := a.AssertPlant(plantID); r != nil {
		return *r
	}
	if !dispatchable(h.Status) {
		if !rec.Force {
			return syncsvc.Conflict(ctx, tx, rec, a, syncsvc.ConflictInput{Entity: "Sale", EntityID: h.ID, PlantID: plantID, ServerVersion: h.UpdatedAt.UnixMilli(),
				Reason:     i18n.T(a.Lang, "sales.sync.not_dispatchable", "number", h.Number, "status", statusWord(a.Lang, h.Status)),
				ServerData: map[string]any{"id": h.ID, "number": h.Number, "status": h.Status, "updatedAt": h.UpdatedAt}})
		}
		// a resolver kept the device's version: the goods left, so bring a confirmed order to READY first
		if !inSet(h.Status, StConfirmed, StInProduction, StFromStock) {
			return syncsvc.Reject(syncsvc.ReasonConflict, i18n.T(a.Lang, "sales.sync.not_dispatchable", "number", h.Number, "status", statusWord(a.Lang, h.Status)))
		}
		if h.Status == StConfirmed {
			if err := o.transition(h, StFromStock, "offline dispatch"); err != nil {
				return syncsvc.FromError(a.Lang, err)
			}
		}
		if err := o.transition(h, StReady, "offline dispatch"); err != nil {
			return syncsvc.FromError(a.Lang, err)
		}
	}
	res, err := o.dispatch(h, in, rec.UUID, true, true)
	if err != nil {
		return syncsvc.FromError(a.Lang, err)
	}
	var ver int64
	if err := tx.QueryRow(ctx, `SELECT server_version FROM "Delivery" WHERE id = $1`, res.ID).Scan(&ver); err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	var flags []string
	if res.Negative {
		flags = append(flags, syncsvc.FlagNegativeStock)
		if err := audit.Log(ctx, tx, a.Audit(), audit.Input{Action: "sales.delivery.negative_stock", Entity: "Delivery", EntityID: res.ID,
			Summary: "Offline dispatch " + in.DNNumber + " drove finished stock negative (review)"}); err != nil {
			return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
		}
	}
	invalidate()
	out := syncsvc.Accept(res.ID, ver, flags...)
	out.PlantID, out.Entity, out.EntityID = plantID, "Delivery", res.ID
	return out
}

func pullOrders(ctx context.Context, a syncsvc.Actor, since syncsvc.Cursor) ([]syncsvc.Change, error) {
	return syncsvc.Rows(ctx, a, since, "orders", `SELECT s.id,
		jsonb_build_object('id', s.id, 'number', s.number, 'status', s."orderStatus", 'plantId', s."plantId", 'customerId', s."customerId", 'customerName', c.name,
		  'date', to_char(s.date, 'YYYY-MM-DD'), 'transportMode', s."transportMode", 'deliveryAddress', s."deliveryAddress", 'deliverySite', s."deliverySite",
		  'requestedDate', to_char(s."requestedDate", 'YYYY-MM-DD'), 'paymentTerm', s."paymentTerm", 'version', s.version, 'notes', s.notes,
		  'total', COALESCE((SELECT SUM(amount) FROM "SaleLine" WHERE "saleId" = s.id), 0),
		  'lines', COALESCE((SELECT jsonb_agg(jsonb_build_object('id', sl.id, 'kind', sl.kind, 'productId', sl."productId", 'quantity', sl.quantity,
		      'unitPrice', sl."unitPrice", 'discountPercent', sl."discountPercent", 'discountAmount', sl."discountAmount", 'amount', sl.amount,
		      'delivered', COALESCE((SELECT SUM(dl.quantity) FROM "DeliveryLine" dl JOIN "Delivery" d ON d.id = dl."deliveryId" WHERE dl."saleLineId" = sl.id AND d.status <> 'CANCELLED'), 0))
		    ORDER BY sl.id) FROM "SaleLine" sl WHERE sl."saleId" = s.id), '[]'::jsonb)),
		s.server_version, s.server_xid
		FROM "Sale" s JOIN "Customer" c ON c.id = s."customerId"
		WHERE ($1 = '' OR s."plantId" = $1) AND (s.date >= current_date - 60 OR s."orderStatus" NOT IN ('CLOSED', 'CANCELLED'))`, a.ScopedPlantID())
}

func pullDispatches(ctx context.Context, a syncsvc.Actor, since syncsvc.Cursor) ([]syncsvc.Change, error) {
	return syncsvc.Rows(ctx, a, since, "dispatches", `SELECT d.id,
		jsonb_build_object('id', d.id, 'dnNumber', d."dnNumber", 'saleId', d."saleId", 'plantId', d."plantId", 'date', to_char(d.date, 'YYYY-MM-DD'),
		  'status', d.status, 'vehicle', d.vehicle, 'driver', d.driver, 'destination', d.destination, 'transportMode', d."transportMode",
		  'receivedBy', d."receivedBy", 'deliveredAt', d."deliveredAt",
		  'lines', COALESCE((SELECT jsonb_agg(jsonb_build_object('saleLineId', dl."saleLineId", 'productId', dl."productId", 'quantity', dl.quantity) ORDER BY dl.id)
		    FROM "DeliveryLine" dl WHERE dl."deliveryId" = d.id), '[]'::jsonb)),
		d.server_version, d.server_xid
		FROM "Delivery" d WHERE ($1 = '' OR d."plantId" = $1) AND d.date >= current_date - 60`, a.ScopedPlantID())
}

var _ = time.Now
