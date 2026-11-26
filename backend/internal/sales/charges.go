package sales

import (
	"fmt"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/refdata"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// Delivery charge rules (DEL-02), edited at Sales → Delivery charges (masterdata.manage):
//
//	GET    /delivery-charges            rules (+ zones)       GET /delivery-charges/quote?zone=&km=  charge for an order
//	POST   /delivery-charges            {name, method FIXED|ZONE|DISTANCE, zone?, minKm?, maxKm?, amount, isActive?, sortOrder?}
//	PATCH  /delivery-charges/:id        DELETE /delivery-charges/:id {reason} (removed, never deleted: SRS §6.3)
//	GET    /delivery-charges/removed    removed rules with who, when and why
//
// Precedence when an order is delivered by BTN: zone rule → distance band → fixed charge (pricing.go pickCharge).

func (x in) parseCharge(partial bool) (map[string]any, []string) {
	v := x.v
	vals := map[string]any{}
	var order []string
	set := func(k string, val any) { vals[k] = val; order = append(order, k) }
	if !partial || v.Has("name") {
		set("name", v.Str("name").Trim().Collapse().Min(2, "sales.v.charge_name").Max(120, x.atMost(120)).Get())
	}
	if !partial || v.Has("method") {
		set("method", v.Str("method").OneOf([]string{ChargeFixed, ChargeZone, ChargeDistance}, "sales.v.charge_method").Get())
	}
	if v.Has("zone") {
		set("zone", x.optCode("zone"))
	}
	for _, k := range []string{"minKm", "maxKm"} {
		if v.Has(k) {
			if v.Raw(k) == nil || v.Raw(k) == "" {
				set(k, nil)
			} else if !numeric(v.Raw(k)) {
				v.Fail(k, "sales.v.enter_distance")
			} else {
				set(k, v.Num(k).Coerce().Min(0, "sales.v.not_negative").Max(5000, "sales.v.too_large").F())
			}
		}
	}
	if !partial || v.Has("amount") {
		set("amount", x.money(v, "amount").Min(0, "sales.v.not_negative").Max(100_000_000, "sales.v.too_large").I())
	}
	if v.Has("isActive") {
		set("isActive", v.Bool("isActive").Get())
	}
	if v.Has("sortOrder") {
		set("sortOrder", v.Num("sortOrder").Coerce().Whole().Min(0).Max(9999).I())
	}
	return vals, order
}

// checkCharge validates a rule's combination of fields (pure).
func checkCharge(method string, zone *string, minKm, maxKm *float64) map[string]string {
	f := map[string]string{}
	switch method {
	case ChargeZone:
		if zone == nil || *zone == "" {
			f["zone"] = "sales.v.charge_zone"
		}
	case ChargeDistance:
		if minKm == nil {
			f["minKm"] = "sales.v.charge_band"
		} else if maxKm != nil && *maxKm <= *minKm {
			f["maxKm"] = "sales.v.charge_band"
		}
	}
	return f
}

func (h *handler) listCharges(c *fiber.Ctx) error {
	r := newRq(c)
	rules, err := loadChargeRules(r.ctx, h.d.Pool, false)
	if err != nil {
		return err
	}
	zones, err := refdata.List(r.ctx, h.d.Pool, refdata.KindDeliveryZone, false, r.lang)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"items": rules, "zones": zones})
}

func (h *handler) quoteCharge(c *fiber.Ctx) error {
	r := newRq(c)
	q := httpx.Query(c)
	zone := q.OptStr("zone").Trim().Upper().Max(40).Get()
	var km *float64
	if q.Has("km") && q.Raw("km") != "" {
		f := q.Num("km").Coerce().Min(0).Max(5000).F()
		km = &f
	}
	if err := q.Err(); err != nil {
		return err
	}
	rules, err := loadChargeRules(r.ctx, h.d.Pool, true)
	if err != nil {
		return err
	}
	amount, rule := pickCharge(rules, zone, km)
	return c.JSON(fiber.Map{"amount": amount, "rule": rule})
}

func (h *handler) saveCharge(partial bool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		r := newRq(c)
		v, err := httpx.Body(c)
		if err != nil {
			return err
		}
		vals, order := newIn(v, r.lang).parseCharge(partial)
		if err := v.Err(); err != nil {
			return err
		}
		id := c.Params("id")
		cols := map[string]string{"name": "name", "method": "method", "zone": "zone", "minKm": "min_km", "maxKm": "max_km", "amount": "amount", "isActive": "is_active", "sortOrder": "sort_order"}
		err = db.Tx(r.ctx, h.d.Pool, func(tx pgx.Tx) error {
			var before chargeRule
			if partial {
				if err := tx.QueryRow(r.ctx, `SELECT id, name, method, zone, min_km::float8, max_km::float8, amount, is_active, sort_order FROM delivery_charge_rule WHERE id = $1 AND removed_at IS NULL FOR UPDATE`, id).
					Scan(&before.ID, &before.Name, &before.Method, &before.Zone, &before.MinKm, &before.MaxKm, &before.Amount, &before.IsActive, &before.Sort); err != nil {
					if db.IsNoRows(err) {
						return httpx.NotFound("entity.charge_rule")
					}
					return err
				}
			}
			// effective combination after the change
			method, zone, minKm, maxKm := before.Method, before.Zone, before.MinKm, before.MaxKm
			if m, ok := vals["method"].(string); ok {
				method = m
			}
			if z, ok := vals["zone"]; ok {
				zone, _ = z.(*string)
			}
			if x, ok := vals["minKm"]; ok {
				if f, ok := x.(float64); ok {
					minKm = &f
				} else {
					minKm = nil
				}
			}
			if x, ok := vals["maxKm"]; ok {
				if f, ok := x.(float64); ok {
					maxKm = &f
				} else {
					maxKm = nil
				}
			}
			if method != ChargeZone {
				zone = nil
				vals["zone"], order = nil, appendOnce(order, "zone")
			}
			if method != ChargeDistance {
				minKm, maxKm = nil, nil
				vals["minKm"], vals["maxKm"], order = nil, nil, appendOnce(appendOnce(order, "minKm"), "maxKm")
			}
			if f := checkCharge(method, zone, minKm, maxKm); len(f) > 0 {
				for k, key := range f {
					f[k] = r.t(key)
				}
				return httpx.BadRequest("error.validation", f)
			}
			if zone != nil {
				ok, err := refdata.Valid(r.ctx, tx, refdata.KindDeliveryZone, *zone)
				if err != nil {
					return err
				}
				if !ok {
					return httpx.BadRequest("error.validation", map[string]string{"zone": r.t("sales.v.delivery_zone")})
				}
			}
			args := []any{}
			if !partial {
				names, ph := []string{}, []string{}
				for _, k := range order {
					args = append(args, vals[k])
					names = append(names, cols[k])
					ph = append(ph, fmt.Sprintf("$%d", len(args)))
				}
				if err := tx.QueryRow(r.ctx, `INSERT INTO delivery_charge_rule (`+strings.Join(names, ", ")+`) VALUES (`+strings.Join(ph, ", ")+`) RETURNING id`, args...).Scan(&id); err != nil {
					return err
				}
			} else {
				sets := []string{"updated_at = now()"}
				args = append(args, id)
				for _, k := range order {
					args = append(args, vals[k])
					sets = append(sets, fmt.Sprintf("%s = $%d", cols[k], len(args)))
				}
				if _, err := tx.Exec(r.ctx, `UPDATE delivery_charge_rule SET `+strings.Join(sets, ", ")+` WHERE id = $1`, args...); err != nil {
					return err
				}
			}
			action := map[bool]string{true: "sales.charge.update", false: "sales.charge.create"}[partial]
			var b any
			if partial {
				b = before
			}
			return audit.Record(c, tx, audit.Input{Action: action, Entity: "DeliveryChargeRule", EntityID: id, Summary: "Delivery charge rule " + method, Before: b, After: vals})
		})
		if err != nil {
			return err
		}
		rules, err := loadChargeRules(r.ctx, h.d.Pool, false)
		if err != nil {
			return err
		}
		for _, rl := range rules {
			if rl.ID == id {
				return c.Status(map[bool]int{true: 200, false: 201}[partial]).JSON(fiber.Map{"rule": rl})
			}
		}
		return httpx.NotFound("entity.charge_rule")
	}
}

func appendOnce(s []string, k string) []string {
	for _, x := range s {
		if x == k {
			return s
		}
	}
	return append(s, k)
}

func (h *handler) deleteCharge(c *fiber.Ctx) error {
	r := newRq(c)
	reason, err := httpx.RemovalReason(c)
	if err != nil {
		return err
	}
	err = db.Tx(r.ctx, h.d.Pool, func(tx pgx.Tx) error {
		var name string
		var active bool
		var amount int64
		if err := tx.QueryRow(r.ctx, `SELECT name, amount, is_active FROM delivery_charge_rule WHERE id = $1 AND removed_at IS NULL FOR UPDATE`,
			c.Params("id")).Scan(&name, &amount, &active); err != nil {
			if db.IsNoRows(err) {
				return httpx.NotFound("entity.charge_rule")
			}
			return err
		}
		if _, err := tx.Exec(r.ctx, `UPDATE delivery_charge_rule SET removed_at = now(), removed_by_id = $2, removed_reason = $3, is_active = false, updated_at = now()
			WHERE id = $1`, c.Params("id"), r.u.ID, reason); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "sales.charge.remove", Entity: "DeliveryChargeRule", EntityID: c.Params("id"),
			Summary: "Removed delivery charge rule " + name + ": " + reason,
			Before:  map[string]any{"name": name, "amount": amount, "isActive": active},
			After:   map[string]any{"removed": true, "reason": reason, "isActive": false}})
	})
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"ok": true})
}

// removedCharges lists removed rules (history: the prices past orders were charged with).
func (h *handler) removedCharges(c *fiber.Ctx) error {
	r := newRq(c)
	rows, err := h.d.Pool.Query(r.ctx, `SELECT d.id, d.name, d.method, d.zone, d.amount, d.removed_at, d.removed_reason, COALESCE(u.name, '')
		FROM delivery_charge_rule d LEFT JOIN "User" u ON u.id = d.removed_by_id WHERE d.removed_at IS NOT NULL ORDER BY d.removed_at DESC LIMIT 200`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type removed struct {
		ID        string    `json:"id"`
		Name      string    `json:"name"`
		Method    string    `json:"method"`
		Zone      *string   `json:"zone"`
		Amount    int64     `json:"amount"`
		RemovedAt time.Time `json:"removedAt"`
		Reason    *string   `json:"reason"`
		RemovedBy string    `json:"removedBy"`
	}
	out := []removed{}
	for rows.Next() {
		var x removed
		if err := rows.Scan(&x.ID, &x.Name, &x.Method, &x.Zone, &x.Amount, &x.RemovedAt, &x.Reason, &x.RemovedBy); err != nil {
			return err
		}
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"items": out})
}
