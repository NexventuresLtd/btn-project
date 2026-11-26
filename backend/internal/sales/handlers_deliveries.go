package sales

import (
	"fmt"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/notify"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

func (h *handler) deliveries(c *fiber.Ctx) error {
	r := newRq(c)
	q := httpx.Query(c)
	from, to := optDate(q, "from"), optDate(q, "to")
	f := deliveryFilters{PlantID: q.OptStr("plantId").Get(), CustomerID: q.OptStr("customerId").Trim().Max(40).Get(), SaleID: q.OptStr("saleId").Trim().Max(40).Get()}
	f.Status = queryEnum(q, "status", "DISPATCHED", "DELIVERED", "CANCELLED")
	f.Transport = queryEnum(q, "transportMode", TransportClient, TransportBTN)
	f.Q = q.OptStr("q").Trim().Max(80, r.t("sales.v.at_most", "n", 80)).Get()
	if err := q.Err(); err != nil {
		return err
	}
	rng := dates.ResolveRange(from, to)
	f.From, f.To = rng.FromISO, rng.ToISO
	f.PlantID = auth.PlantFilter(r.u, f.PlantID)
	pg := httpx.Paginate(c, 25)
	out, err := h.s.listDeliveries(r.ctx, f, pg)
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

// delivered: POST /deliveries/:id/delivered — JSON {receivedBy, deliveredAt?, note?} or multipart with an optional
// "photo" file (DEL-05). Completes the order when everything is shipped and received.
func (h *handler) delivered(c *fiber.Ctx) error {
	r := newRq(c)
	id := c.Params("id")
	var v *httpx.V
	var ph *photo
	if form, err := c.MultipartForm(); err == nil && form != nil {
		m := map[string]any{}
		for k, vals := range form.Value {
			if len(vals) > 0 && k != "photo" {
				m[k] = vals[0]
			}
		}
		v = httpx.NewV(m, r.lang)
		if fh, err := c.FormFile("photo"); err == nil && fh != nil {
			if fh.Size > maxPhotoBytes {
				return httpx.BadRequest("error.validation", map[string]string{"photo": r.t("sales.v.photo_too_big")})
			}
			f, err := fh.Open()
			if err != nil {
				return err
			}
			buf := make([]byte, 0, fh.Size)
			tmp := make([]byte, 64<<10)
			for {
				n, rerr := f.Read(tmp)
				buf = append(buf, tmp[:n]...)
				if len(buf) > maxPhotoBytes {
					f.Close()
					return httpx.BadRequest("error.validation", map[string]string{"photo": r.t("sales.v.photo_too_big")})
				}
				if rerr != nil {
					break
				}
			}
			f.Close()
			p, key := sniffImage(buf)
			if key != "" {
				return httpx.BadRequest("error.validation", map[string]string{"photo": r.t(key)})
			}
			ph = p
		}
	} else {
		var berr error
		if v, berr = httpx.Body(c); berr != nil {
			return berr
		}
	}
	in := newIn(v, r.lang).parseDelivered()
	if err := v.Err(); err != nil {
		return err
	}
	err := db.Tx(r.ctx, h.d.Pool, func(tx pgx.Tx) error {
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
		if at.After(dates.Now().Add(5 * time.Minute)) {
			return r.fieldErr("deliveredAt", "sales.v.future")
		}
		if dates.ToISO(at.Add(2*time.Hour)) < dates.ToISO(d.Date) {
			return r.fieldErr("deliveredAt", "sales.v.before_dispatch")
		}
		if _, err := tx.Exec(r.ctx, `UPDATE "Delivery" SET status='DELIVERED', "receivedBy"=$2, "deliveredAt"=$3, "deliveryNote"=$4, "confirmedById"=$5, "updatedAt"=now() WHERE id=$1`,
			id, in.ReceivedBy, at, in.Note, r.u.ID); err != nil {
			return err
		}
		if ph != nil {
			if _, err := tx.Exec(r.ctx, `INSERT INTO delivery_photo (delivery_id, mime, size_bytes, width, height, sha256, content, thumb, uploaded_by)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (delivery_id) DO UPDATE SET mime=EXCLUDED.mime, size_bytes=EXCLUDED.size_bytes, width=EXCLUDED.width,
				height=EXCLUDED.height, sha256=EXCLUDED.sha256, content=EXCLUDED.content, thumb=EXCLUDED.thumb, uploaded_by=EXCLUDED.uploaded_by, created_at=now()`,
				id, ph.Mime, ph.Size, ph.Width, ph.Height, ph.SHA, ph.Content, ph.Thumb, r.u.ID); err != nil {
				return err
			}
		}
		after := map[string]any{"status": "DELIVERED", "receivedBy": in.ReceivedBy, "deliveredAt": at, "note": in.Note}
		if ph != nil {
			after["photo"] = map[string]any{"mime": ph.Mime, "bytes": ph.Size, "sha256": ph.SHA, "width": ph.Width, "height": ph.Height}
		}
		if err := audit.Record(c, tx, audit.Input{Action: "sales.delivery.delivered", Entity: "Delivery", EntityID: id,
			Summary: fmt.Sprintf("DN %s (%s) received by %s", d.DN, d.SaleNumber, in.ReceivedBy),
			Before:  map[string]any{"status": d.Status}, After: after}); err != nil {
			return err
		}
		o := r.op(tx)
		hd, err := loadHead(r.ctx, tx, d.SaleID, false)
		if err != nil {
			return err
		}
		if err := o.afterShipChange(hd); err != nil {
			return err
		}
		return notify.Send(r.ctx, tx, notify.Notification{Event: EvDelivered, Severity: notify.Info, TitleKey: "sales.notify.delivered_title",
			BodyKey: "sales.notify.delivered_body", Params: map[string]any{"number": d.SaleNumber, "dn": d.DN, "receiver": in.ReceivedBy},
			Link: "/sales/" + d.SaleID, To: notify.Audience{UserIDs: orderPeople(hd), Permission: "payments.record"}.Without(r.u.ID)})
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
		hd, err := loadHead(r.ctx, tx, d.SaleID, false)
		if err != nil {
			return err
		}
		if hd.Status == StClosed {
			return r.conflict("sales.err.closed_locked")
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
		if _, err := tx.Exec(r.ctx, `UPDATE "Delivery" SET "updatedAt" = now() WHERE id = $1`, id); err != nil {
			return err
		}
		if err := audit.Record(c, tx, audit.Input{Action: "sales.delivery.cancel", Entity: "Delivery", EntityID: id,
			Summary: fmt.Sprintf("Cancelled DN %s (%s): %s", d.DN, d.SaleNumber, reason),
			Before:  map[string]any{"status": d.Status}, After: map[string]any{"status": "CANCELLED", "reason": reason}}); err != nil {
			return err
		}
		return r.op(tx).afterShipChange(hd)
	})
	if err != nil {
		return err
	}
	invalidate()
	return c.JSON(fiber.Map{"ok": true})
}
