package production

// PRD-10 recipes: material per unit of product, versioned by effective date (the version in force on an entry's
// date is the latest with effectiveFrom ≤ date). Managed with catalog.manage (or masterdata.manage).
//
//	GET    /recipes                         production.view — every product with its current version and history
//	POST   /recipes {productId, effectiveFrom, note, items:[{materialId, qtyPerUnit}]}   catalog.manage | masterdata.manage (upsert of that version)
//	DELETE /recipes/:id                     catalog.manage | masterdata.manage

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/stock"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

type recipeItemOut struct {
	MaterialID string     `json:"materialId"`
	Material   productOut `json:"material"`
	QtyPerUnit float64    `json:"qtyPerUnit"`
}

type recipeOut struct {
	ID            string          `json:"id"`
	ProductID     string          `json:"productId"`
	EffectiveFrom string          `json:"effectiveFrom"`
	Note          *string         `json:"note"`
	CreatedBy     *string         `json:"createdBy"`
	CreatedAt     string          `json:"createdAt"`
	Current       bool            `json:"current"`
	Items         []recipeItemOut `json:"items"`
	RemovedAt     *string         `json:"removedAt,omitempty"` // removed versions (SRS §6.3: kept, no longer used)
	RemovedBy     *string         `json:"removedBy,omitempty"`
	RemovedReason *string         `json:"removedReason,omitempty"`
}

type productRecipes struct {
	Product  productOut  `json:"product"`
	Current  *recipeOut  `json:"current"`
	Upcoming []recipeOut `json:"upcoming"`
	History  []recipeOut `json:"history"`
	Removed  []recipeOut `json:"removed"`
}

// loadRecipes returns all recipe versions (two queries), grouped by product id, newest first.
func loadRecipes(ctx context.Context, q db.DBTX) (map[string][]recipeOut, error) {
	rows, err := q.Query(ctx, `SELECT r.id, r."productId", r."effectiveFrom", r.note, u.name, r."createdAt", r.removed_at, ru.name, r.removed_reason FROM production_recipe r
		LEFT JOIN "User" u ON u.id = r."createdById" LEFT JOIN "User" ru ON ru.id = r.removed_by_id
		ORDER BY r."productId", r."effectiveFrom" DESC, r.removed_at DESC NULLS FIRST`)
	if err != nil {
		return nil, err
	}
	byID := map[string]*recipeOut{}
	order := []string{}
	for rows.Next() {
		var r recipeOut
		var eff, at time.Time
		var rmAt *time.Time
		if err := rows.Scan(&r.ID, &r.ProductID, &eff, &r.Note, &r.CreatedBy, &at, &rmAt, &r.RemovedBy, &r.RemovedReason); err != nil {
			rows.Close()
			return nil, err
		}
		r.EffectiveFrom, r.CreatedAt, r.Items = dates.ToISO(eff), isoTime(at), []recipeItemOut{}
		if rmAt != nil {
			s := isoTime(*rmAt)
			r.RemovedAt = &s
		}
		byID[r.ID] = &r
		order = append(order, r.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, `SELECT i."recipeId", i."materialId", m.code, m.name, m.unit, i."qtyPerUnit"::float8 FROM production_recipe_item i
		JOIN "Material" m ON m.id = i."materialId" ORDER BY m."sortOrder", m.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var rid string
		var it recipeItemOut
		if err := rows.Scan(&rid, &it.MaterialID, &it.Material.Code, &it.Material.Name, &it.Material.Unit, &it.QtyPerUnit); err != nil {
			return nil, err
		}
		it.Material.ID = it.MaterialID
		if r := byID[rid]; r != nil {
			r.Items = append(r.Items, it)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := map[string][]recipeOut{}
	for _, id := range order {
		r := byID[id]
		out[r.ProductID] = append(out[r.ProductID], *r)
	}
	return out, nil
}

func (h *handler) listRecipes(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	all, err := loadRecipes(ctx, h.d.Pool)
	if err != nil {
		return err
	}
	rows, err := h.d.Pool.Query(ctx, `SELECT id, code, name, unit FROM "Product" WHERE "isActive" OR id IN (SELECT "productId" FROM production_recipe) ORDER BY "sortOrder", name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	today := dates.TodayISO()
	items := []productRecipes{}
	for rows.Next() {
		var p productOut
		if err := rows.Scan(&p.ID, &p.Code, &p.Name, &p.Unit); err != nil {
			return err
		}
		pr := productRecipes{Product: p, Upcoming: []recipeOut{}, History: []recipeOut{}, Removed: []recipeOut{}}
		for _, r := range all[p.ID] {
			switch {
			case r.RemovedAt != nil:
				pr.Removed = append(pr.Removed, r)
			case r.EffectiveFrom > today:
				pr.Upcoming = append(pr.Upcoming, r)
			case pr.Current == nil:
				r.Current = true
				rc := r
				pr.Current = &rc
			default:
				pr.History = append(pr.History, r)
			}
		}
		items = append(items, pr)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return httpx.JSONCached(c, fiber.Map{"items": items}, cacheControl)
}

func (h *handler) saveRecipe(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	lang := httpx.Lang(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	productID := stock.IDField(v, "productId")
	eff := v.Str("effectiveFrom").Trim().Date("production.v.effective_from").Get()
	note := v.NullStr("note").Trim().Max(300, atMost(lang, 300)).NilIfEmpty()
	type item struct {
		mat string
		qty float64
	}
	var items []item
	ivs := v.Arr("items", true)
	if len(ivs) == 0 && !v.Failed() {
		v.Fail("items", "production.v.recipe_items")
	}
	if len(ivs) > 30 {
		v.Fail("items", "production.v.max_usages")
	}
	seen := map[string]bool{}
	for i, iv := range ivs {
		it := item{mat: stock.IDField(iv, "materialId")}
		if n, ok := stock.Number(iv, "qtyPerUnit", stock.NumRule{TypeMsg: "production.v.enter_number", Min: stock.F64(0.000001), MinMsg: "production.v.positive",
			Max: stock.F64(100000), MaxMsg: "production.v.too_large"}); ok {
			it.qty = n
		}
		if seen[it.mat] {
			v.Fail(fmt.Sprintf("items.%d.materialId", i), "production.v.material_twice")
		}
		seen[it.mat] = true
		items = append(items, it)
	}
	if err := v.Err(); err != nil {
		return err
	}
	var pOK bool
	var mOK int
	mats := make([]string, len(items))
	qtys := make([]string, len(items))
	for i, it := range items {
		mats[i], qtys[i] = it.mat, strconv.FormatFloat(it.qty, 'f', 6, 64)
	}
	if err := h.d.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM "Product" WHERE id = $1), (SELECT count(*)::int FROM "Material" WHERE id = ANY($2))`, productID, mats).Scan(&pOK, &mOK); err != nil {
		return err
	}
	if !pOK {
		return httpx.BadRequest("error.validation", map[string]string{"productId": i18n.T(lang, "production.err.unknown_product")})
	}
	if mOK != len(mats) {
		return httpx.BadRequest("error.validation", map[string]string{"items": i18n.T(lang, "production.err.unknown_material")})
	}
	id, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (string, error) {
		var id string
		var before any
		if err := tx.QueryRow(ctx, `SELECT id FROM production_recipe WHERE "productId" = $1 AND "effectiveFrom" = $2 AND removed_at IS NULL FOR UPDATE`, productID, eff).Scan(&id); err == nil {
			all, err := loadRecipes(ctx, tx)
			if err != nil {
				return "", err
			}
			for _, r := range all[productID] {
				if r.ID == id {
					before = r
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE production_recipe SET note = $2 WHERE id = $1`, id, note); err != nil {
				return "", err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM production_recipe_item WHERE "recipeId" = $1`, id); err != nil {
				return "", err
			}
		} else if db.IsNoRows(err) {
			if err := tx.QueryRow(ctx, `INSERT INTO production_recipe ("productId","effectiveFrom",note,"createdById") VALUES ($1,$2,$3,$4) RETURNING id`,
				productID, eff, note, c.Locals("userId")).Scan(&id); err != nil {
				return "", err
			}
		} else {
			return "", err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO production_recipe_item ("recipeId","materialId","qtyPerUnit") SELECT $1, u.m, u.q::numeric FROM unnest($2::text[], $3::text[]) AS u(m, q)`,
			id, mats, qtys); err != nil {
			return "", err
		}
		after := map[string]any{"productId": productID, "effectiveFrom": eff, "note": note, "items": map[string]string{}}
		for i := range mats {
			after["items"].(map[string]string)[mats[i]] = qtys[i]
		}
		return id, audit.Record(c, tx, audit.Input{Action: "production.recipe_save", Entity: "ProductionRecipe", EntityID: id,
			Summary: fmt.Sprintf("Saved recipe effective %s (%d material(s))", dates.FormatDate(dates.MustParseISO(eff)), len(mats)), Before: before, After: after})
	})
	if err != nil {
		return err
	}
	all, err := loadRecipes(ctx, h.d.Pool)
	if err != nil {
		return err
	}
	for _, r := range all[productID] {
		if r.ID == id {
			return c.Status(201).JSON(r)
		}
	}
	return httpx.NotFound("production.entity_recipe")
}

// deleteRecipe removes a recipe version (SRS §6.3: kept with its items, who, when and why; expected usage no
// longer uses it). DELETE /production/recipes/:id {reason}.
func (h *handler) deleteRecipe(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	reason, err := httpx.RemovalReason(c)
	if err != nil {
		return err
	}
	return db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		var product string
		var eff time.Time
		err := tx.QueryRow(ctx, `UPDATE production_recipe SET removed_at = now(), removed_by_id = $2, removed_reason = $3 WHERE id = $1 AND removed_at IS NULL
			RETURNING "productId", "effectiveFrom"`, c.Params("id"), auth.MustUser(c).ID, reason).Scan(&product, &eff)
		if db.IsNoRows(err) {
			return httpx.NotFound("production.entity_recipe")
		}
		if err != nil {
			return err
		}
		if err := audit.Record(c, tx, audit.Input{Action: "production.recipe_remove", Entity: "ProductionRecipe", EntityID: c.Params("id"),
			Summary: "Removed recipe version effective " + dates.FormatDate(eff) + ": " + reason, Before: map[string]string{"productId": product, "effectiveFrom": dates.ToISO(eff)},
			After: map[string]any{"removed": true, "reason": reason}}); err != nil {
			return err
		}
		return c.SendStatus(204)
	})
}
