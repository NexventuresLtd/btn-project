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

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

func (h *handler) deliveries(c *fiber.Ctx) error {
	r := newRq(c)
	q := httpx.Query(c)
	from, to := optDate(q, "from"), optDate(q, "to")
	plantID := q.OptStr("plantId").Get()
	status := queryEnum(q, "status", "DISPATCHED", "DELIVERED", "CANCELLED")
	search := q.OptStr("q").Trim().Max(80, r.t("sales.v.at_most", "n", 80)).Get()
	if err := q.Err(); err != nil {
		return err
	}
	rng := dates.ResolveRange(from, to)
	pg := httpx.Paginate(c, 25)
	out, err := h.s.listDeliveries(r.ctx, rng.FromISO, rng.ToISO, auth.PlantFilter(r.u, plantID), status, search, pg)
	if err != nil {
		return err
	}
	return httpx.JSONCached(c, out, "private, no-cache")
}

type deliveryHead struct {
	ID, DN, Status, PlantID, SaleID, SaleNumber string
	Date                                        time.Time
}

// loadDeliveryForWrite loads the delivery, checks plant access and locks the sale.
func (r rq) loadDeliveryForWrite(tx pgx.Tx, id string) (deliveryHead, error) {
	var d deliveryHead
	err := tx.QueryRow(r.ctx, `SELECT d.id, d."dnNumber", d.status::text, d."plantId", d."saleId", sa.number, d.date
		FROM "Delivery" d JOIN "Sale" sa ON sa.id = d."saleId" WHERE d.id=$1`, id).Scan(&d.ID, &d.DN, &d.Status, &d.PlantID, &d.SaleID, &d.SaleNumber, &d.Date)
	if err != nil {
		if db.IsNoRows(err) {
			return d, r.notFoundDelivery()
		}
		return d, err
	}
	if err := auth.AssertPlantAccess(r.u, d.PlantID); err != nil {
		return d, err
	}
	return d, lockSale(r.ctx, tx, d.SaleID)
}

func (h *handler) delivered(c *fiber.Ctx) error {
	r := newRq(c)
	id := c.Params("id")
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in := newIn(v, r.lang).parseDelivered()
	if err := v.Err(); err != nil {
		return err
	}
	err = db.Tx(r.ctx, h.d.Pool, func(tx pgx.Tx) error {
		d, err := r.loadDeliveryForWrite(tx, id)
		if err != nil {
			return err
		}
		// re-read the status under the sale lock
		if err := tx.QueryRow(r.ctx, `SELECT status::text FROM "Delivery" WHERE id=$1`, id).Scan(&d.Status); err != nil {
			return err
		}
		if d.Status != "DISPATCHED" {
			return r.conflict("sales.err.dn_status", "dn", d.DN, "status", lowerLabel(r, d.Status))
		}
		at := db.Now()
		if in.DeliveredAt != nil {
			at = *in.DeliveredAt
		}
		if at.After(time.Now().Add(5 * time.Minute)) {
			return r.fieldErr("deliveredAt", "sales.v.future")
		}
		if dates.ToISO(at.Add(2*time.Hour)) < dates.ToISO(d.Date) {
			return r.fieldErr("deliveredAt", "sales.v.before_dispatch")
		}
		if _, err := tx.Exec(r.ctx, `UPDATE "Delivery" SET status='DELIVERED', "receivedBy"=$2, "deliveredAt"=$3 WHERE id=$1`, id, in.ReceivedBy, at); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "sales.delivery.delivered", Entity: "Delivery", EntityID: id,
			Summary: fmt.Sprintf("DN %s (%s) received by %s", d.DN, d.SaleNumber, in.ReceivedBy),
			Before:  map[string]any{"status": d.Status}, After: map[string]any{"status": "DELIVERED", "receivedBy": in.ReceivedBy, "deliveredAt": at}})
	})
	if err != nil {
		return err
	}
	invalidate()
	return c.JSON(fiber.Map{"ok": true})
}

func lowerLabel(r rq, status string) string {
	return strings.ToLower(r.t("sales.dstatus." + status))
}

func (h *handler) cancelDelivery(c *fiber.Ctx) error {
	r := newRq(c)
	id := c.Params("id")
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	reason := newIn(v, r.lang).reqText("reason", 500, r.t("sales.v.give_reason"))
	if err := v.Err(); err != nil {
		return err
	}
	err = db.Tx(r.ctx, h.d.Pool, func(tx pgx.Tx) error {
		d, err := r.loadDeliveryForWrite(tx, id)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(r.ctx, `SELECT status::text FROM "Delivery" WHERE id=$1`, id).Scan(&d.Status); err != nil {
			return err
		}
		if d.Status == "CANCELLED" {
			return r.conflict("sales.err.dn_already_cancelled", "dn", d.DN)
		}
		// Cancelling must not leave more returned than delivered on the sale.
		deliveredAfter, returned, err := deliveredAndReturned(r.ctx, tx, d.SaleID, id)
		if err != nil {
			return err
		}
		for _, pid := range returned.keys {
			got, _ := deliveredAfter.get(pid)
			if returned.m[pid] > got {
				return r.conflict("sales.err.cancel_dn_returned")
			}
		}
		if _, err := tx.Exec(r.ctx, `UPDATE "Delivery" SET status='CANCELLED' WHERE id=$1`, id); err != nil {
			return err
		}
		rows, err := tx.Query(r.ctx, `SELECT "productId", quantity FROM "DeliveryLine" WHERE "deliveryId"=$1 ORDER BY id`, id)
		if err != nil {
			return err
		}
		var moves []movement
		for rows.Next() {
			var pid string
			var q int64
			if err := rows.Scan(&pid, &q); err != nil {
				rows.Close()
				return err
			}
			moves = append(moves, movement{PlantID: d.PlantID, ProductID: pid, Date: d.Date, Type: "SALE_ISSUE", Quantity: q,
				SourceType: "Delivery", SourceID: id, Note: fmt.Sprintf("Reversal of DN %s: %s", d.DN, reason)})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if err := insertMovements(r.ctx, tx, r.u.ID, moves); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "sales.delivery.cancel", Entity: "Delivery", EntityID: id,
			Summary: fmt.Sprintf("Cancelled DN %s (%s): %s", d.DN, d.SaleNumber, reason),
			Before:  map[string]any{"status": d.Status}, After: map[string]any{"status": "CANCELLED", "reason": reason}})
	})
	if err != nil {
		return err
	}
	invalidate()
	return c.JSON(fiber.Map{"ok": true})
}

var _ = context.Background
