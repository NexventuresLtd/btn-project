package workforce

// Piece-work entries (SRS WRK-03 piece rate): output per worker, day, site and product, recorded with attendance.
// Locked days (approved attendance, or already paid) are changed only by a manager (attendance.approve) with a
// reason; a change to a paid day becomes an adjustment for the next wage sheet (§7.4).

import (
	"fmt"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

type pieceWorker struct {
	WorkerID  string `json:"workerId"`
	Name      string `json:"name"`
	JobTitle  string `json:"jobTitle"`
	RateBasis string `json:"rateBasis"`
}

type pieceEntryOut struct {
	ID        string  `json:"id"`
	WorkerID  string  `json:"workerId"`
	ProductID string  `json:"productId"`
	Quantity  int     `json:"quantity"`
	Note      *string `json:"note"`
	Rate      int     `json:"rate"`
	Amount    int     `json:"amount"`
}

type productRef struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
	Rate *int   `json:"rate"`
}

func dayLocked(c *fiber.Ctx, q db.DBTX, plantID string, date time.Time) (bool, error) {
	var st string
	err := q.QueryRow(reqCtx(c), `SELECT status FROM attendance_day WHERE plant_id = $1 AND date = $2`, plantID, date).Scan(&st)
	if db.IsNoRows(err) {
		return false, nil
	}
	return st == "APPROVED", err
}

func (h *handler) pieceWork(c *fiber.Ctx) error {
	u := auth.MustUser(c)
	qv := httpx.Query(c)
	plantQ := qv.OptStr("plantId").Get()
	ds := qv.OptStr("date").Trim().Date("workforce.v.date").Get()
	if err := qv.Err(); err != nil {
		return err
	}
	if ds == "" {
		ds = dates.TodayISO()
	}
	plantID := auth.PlantFilter(u, plantQ)
	if plantID == "" {
		return httpx.BadRequest("error.validation", map[string]string{"plantId": t(c, "workforce.err.choose_plant")})
	}
	plant, err := loadSite(c, h.d.Pool, plantID)
	if err != nil {
		return err
	}
	return h.pieceDay(c, plant, dates.MustParseISO(ds))
}

func (h *handler) pieceDay(c *fiber.Ctx, plant *plantRef, date time.Time) error {
	ctx := reqCtx(c)
	ds := dates.ToISO(date)
	prates, err := LoadPieceRates(ctx, h.d.Pool, date, date)
	if err != nil {
		return err
	}
	rows, err := h.d.Pool.Query(ctx, `SELECT w.id, w."firstName", w."lastName", w."jobTitle", w."rateBasis" FROM "Worker" w
		WHERE (w."plantId" = $1 AND w."isActive" AND w."rateBasis" IN ('PIECE','MIXED'))
		   OR EXISTS (SELECT 1 FROM piece_work_entry e WHERE e.worker_id = w.id AND e.plant_id = $1 AND e.date = $2 AND e.removed_at IS NULL)
		ORDER BY w."firstName", w."lastName", w.id`, plant.ID, date)
	if err != nil {
		return err
	}
	workers := []pieceWorker{}
	for rows.Next() {
		var w pieceWorker
		var f, l string
		if err := rows.Scan(&w.WorkerID, &f, &l, &w.JobTitle, &w.RateBasis); err != nil {
			rows.Close()
			return err
		}
		w.Name = fullName(f, l)
		workers = append(workers, w)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	products := []productRef{}
	prow, err := h.d.Pool.Query(ctx, `SELECT id, code, name FROM "Product" WHERE "isActive" ORDER BY "sortOrder", code`)
	if err != nil {
		return err
	}
	for prow.Next() {
		var p productRef
		if err := prow.Scan(&p.ID, &p.Code, &p.Name); err != nil {
			prow.Close()
			return err
		}
		if r, ok := resolvePiece(prates, p.ID, plant.ID, date); ok {
			rr := r.Rate
			p.Rate = &rr
		}
		products = append(products, p)
	}
	prow.Close()
	entries := []pieceEntryOut{}
	erow, err := h.d.Pool.Query(ctx, `SELECT id, worker_id, product_id, quantity, note FROM piece_work_entry WHERE plant_id = $1 AND date = $2 AND removed_at IS NULL ORDER BY created_at`, plant.ID, date)
	if err != nil {
		return err
	}
	for erow.Next() {
		var e pieceEntryOut
		if err := erow.Scan(&e.ID, &e.WorkerID, &e.ProductID, &e.Quantity, &e.Note); err != nil {
			erow.Close()
			return err
		}
		if r, ok := resolvePiece(prates, e.ProductID, plant.ID, date); ok {
			e.Rate, e.Amount = r.Rate, r.Rate*e.Quantity
		}
		entries = append(entries, e)
	}
	erow.Close()
	if err := erow.Err(); err != nil {
		return err
	}
	locked, err := dayLocked(c, h.d.Pool, plant.ID, date)
	if err != nil {
		return err
	}
	httpx.NoStore(c)
	return c.JSON(fiber.Map{"plant": plant, "date": ds, "locked": locked, "workers": workers, "products": products, "entries": entries})
}

type pieceInput struct {
	WorkerID, ProductID string
	Quantity            int
	Note                *string
}

// savePieceWork: POST /piece-work {plantId, date, entries:[{workerId, productId, quantity, note}], reason?}.
// Quantity 0 removes the entry. Each listed (worker, product) is set; others are untouched.
func (h *handler) savePieceWork(c *fiber.Ctx) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	plantID := v.Str("plantId").Trim().Min(1, "v.required").Max(40, "workforce.v.max_80").Get()
	ds := v.Str("date").Trim().Date("workforce.v.date").Get()
	reason := v.NullStr("reason").Trim().Max(300, "workforce.v.max_300").NilIfEmpty()
	var in []pieceInput
	arr := v.Arr("entries", true)
	if len(arr) > 1000 {
		v.Fail("entries", "workforce.v.too_many")
	}
	for _, e := range arr {
		in = append(in, pieceInput{
			WorkerID:  e.Str("workerId").Trim().Min(1, "v.required").Max(40, "workforce.v.max_80").Get(),
			ProductID: e.Str("productId").Trim().Min(1, "v.required").Max(40, "workforce.v.max_80").Get(),
			Quantity:  int(e.Num("quantity").Coerce().Whole("workforce.v.quantity").Min(0, "workforce.v.quantity").Max(1_000_000, "workforce.v.quantity").I()),
			Note:      e.NullStr("note").Trim().Max(200, "workforce.v.max_300").NilIfEmpty(),
		})
	}
	if err := v.Err(); err != nil {
		return err
	}
	if ds > dates.TodayISO() {
		return httpx.BadRequest("error.validation", map[string]string{"date": t(c, "workforce.v.future")})
	}
	date := dates.MustParseISO(ds)
	if err := auth.AssertPlantAccess(u, plantID); err != nil {
		return err
	}
	plant, err := loadSite(c, h.d.Pool, plantID)
	if err != nil {
		return err
	}
	ids := []string{}
	seen := map[string]bool{}
	fields := map[string]string{}
	for i, e := range in {
		k := e.WorkerID + "|" + e.ProductID
		if seen[k] {
			fields[fmt.Sprintf("entries.%d.productId", i)] = t(c, "workforce.err.duplicate_entry")
		}
		seen[k] = true
		ids = append(ids, e.WorkerID)
	}
	// workers: home site = this site, or recorded here under an attendance exception (lent)
	type winfo struct {
		name, plantID string
		active, lent  bool
	}
	infos := map[string]winfo{}
	rows, err := h.d.Pool.Query(ctx, `SELECT w.id, w."firstName", w."lastName", w."plantId", w."isActive",
		EXISTS (SELECT 1 FROM "Attendance" a WHERE a."workerId" = w.id AND a."plantId" = $2 AND a.date = $3 AND a.exception)
		FROM "Worker" w WHERE w.id = ANY($1)`, ids, plant.ID, date)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, f, l string
		var wi winfo
		if err := rows.Scan(&id, &f, &l, &wi.plantID, &wi.active, &wi.lent); err != nil {
			rows.Close()
			return err
		}
		wi.name = fullName(f, l)
		infos[id] = wi
	}
	rows.Close()
	for i, e := range in {
		wi, ok := infos[e.WorkerID]
		if !ok || !wi.active || (wi.plantID != plant.ID && !wi.lent) {
			fields[fmt.Sprintf("entries.%d.workerId", i)] = t(c, "workforce.err.not_at_site", "plant", plant.Code)
		}
	}
	if len(fields) > 0 {
		return httpx.BadRequest("error.validation", fields)
	}
	locked, err := dayLocked(c, h.d.Pool, plant.ID, date)
	if err != nil {
		return err
	}
	paid, err := PaidWorkers(ctx, h.d.Pool, ids, date)
	if err != nil {
		return err
	}
	if locked || len(paid) > 0 {
		if !u.Can("attendance.approve") {
			return httpx.NewKey(409, "attendance_locked", "workforce.err.piece_locked")
		}
		if reason == nil || len([]rune(*reason)) < 3 {
			return httpx.BadRequest("error.validation", map[string]string{"reason": t(c, "workforce.err.reason")})
		}
	} else if !u.Can("attendance.record") {
		return httpx.Forbidden()
	}
	prates, err := LoadPieceRates(ctx, h.d.Pool, date, date)
	if err != nil {
		return err
	}
	ws, err := LoadWorkers(ctx, h.d.Pool, ids)
	if err != nil {
		return err
	}
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		changes := []map[string]any{}
		for _, e := range in {
			var old int
			var oid string
			var removed bool
			err := tx.QueryRow(ctx, `SELECT id, quantity, removed_at IS NOT NULL FROM piece_work_entry WHERE worker_id = $1 AND date = $2 AND plant_id = $3 AND product_id = $4 FOR UPDATE`,
				e.WorkerID, date, plant.ID, e.ProductID).Scan(&oid, &old, &removed)
			if removed {
				old = 0 // a cleared entry counts as nothing
			}
			if err != nil && !db.IsNoRows(err) {
				return err
			}
			if e.Quantity == 0 {
				// SRS §6.3: a cleared entry is marked removed (kept with its last quantity), never deleted
				if oid != "" && !removed {
					why := "cleared (quantity set to 0)"
					if reason != nil {
						why += ": " + *reason
					}
					if _, err := tx.Exec(ctx, `UPDATE piece_work_entry SET removed_at = now(), removed_by_id = $2, removed_reason = $3, updated_at = now() WHERE id = $1`,
						oid, u.ID, why); err != nil {
						return err
					}
				}
			} else if _, err := tx.Exec(ctx, `INSERT INTO piece_work_entry (worker_id, plant_id, date, product_id, quantity, note, recorded_by)
				VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (worker_id, date, plant_id, product_id) DO UPDATE SET quantity = EXCLUDED.quantity,
				note = EXCLUDED.note, recorded_by = EXCLUDED.recorded_by, updated_at = now(), removed_at = NULL, removed_by_id = NULL, removed_reason = NULL`,
				e.WorkerID, plant.ID, date, e.ProductID, e.Quantity, e.Note, u.ID); err != nil {
				return err
			}
			if old == e.Quantity {
				continue
			}
			changes = append(changes, map[string]any{"workerId": e.WorkerID, "productId": e.ProductID, "before": old, "after": e.Quantity})
			// §7.4: a paid day → the difference is carried to the next wage sheet
			if n, ok := paid[e.WorkerID]; ok {
				if w := ws[e.WorkerID]; w.RateBasis != BasisDaily {
					r, _ := resolvePiece(prates, e.ProductID, plant.ID, date)
					pid, _, err := PaidPeriodFor(ctx, tx, e.WorkerID, date)
					if err != nil {
						return err
					}
					if _, err := AddAdjustment(ctx, tx, e.WorkerID, plant.ID, date, (e.Quantity-old)*r.Rate,
						fmt.Sprintf("Piece-work correction %s %s (paid on %s): %s", plant.Code, dates.FormatDate(date), n, *reason), pid, "", u.ID); err != nil {
						return err
					}
				}
			}
		}
		summary := fmt.Sprintf("Recorded piece work for %s on %s: %d changes", plant.Code, ds, len(changes))
		if reason != nil {
			summary += " (correction of a locked day: " + *reason + ")"
		}
		return audit.Record(c, tx, audit.Input{Action: "workforce.piece_work.record", Entity: "PieceWorkEntry", EntityID: plant.ID + ":" + ds, Summary: summary,
			After: map[string]any{"plantId": plant.ID, "date": ds, "changes": changes, "reason": reason, "locked": locked}})
	})
	if err != nil {
		return err
	}
	return h.pieceDay(c, plant, date)
}
