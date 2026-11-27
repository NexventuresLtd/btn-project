package syncsvc

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/db"
)

// Core reference-data pulls (F3): sites, products, materials, customers, workers, refdata, plus the snapshot
// entities scope (the user's role/permission/site snapshot) and balances (finished goods + raw materials).
func init() {
	RegisterPull("sites", func(ctx context.Context, a Actor, since Cursor) ([]Change, error) {
		return Rows(ctx, a, since, "sites", `SELECT p.id, to_jsonb(p) - 'server_xid' - 'server_version', p.server_version, p.server_xid FROM "Plant" p`)
	})
	RegisterPull("products", func(ctx context.Context, a Actor, since Cursor) ([]Change, error) {
		return Rows(ctx, a, since, "products", `SELECT p.id, to_jsonb(p) - 'server_xid' - 'server_version', p.server_version, p.server_xid FROM "Product" p`)
	})
	RegisterPull("materials", func(ctx context.Context, a Actor, since Cursor) ([]Change, error) {
		return Rows(ctx, a, since, "materials", `SELECT m.id, to_jsonb(m) - 'server_xid' - 'server_version', m.server_version, m.server_xid FROM "Material" m`)
	})
	RegisterPull("customers", func(ctx context.Context, a Actor, since Cursor) ([]Change, error) {
		return Rows(ctx, a, since, "customers", `SELECT c.id, to_jsonb(c) - 'server_xid' - 'server_version', c.server_version, c.server_xid FROM "Customer" c`)
	}, PullRequires("sales.view", "sales.create", "deliveries.create", "customers.manage"))
	// Workers: only what capture screens need (no national id, MoMo number or wage — NFR-D2 data minimisation), plus the
	// WRK-01 registration status so a supervisor's still-unapproved registrations show as pending offline too.
	RegisterPull("workers", func(ctx context.Context, a Actor, since Cursor) ([]Change, error) {
		return Rows(ctx, a, since, "workers", `SELECT w.id, jsonb_build_object('id', w.id, 'firstName', w."firstName", 'lastName', w."lastName",
			'jobTitle', w."jobTitle", 'plantId', w."plantId", 'isActive', w."isActive", 'startDate', w."startDate",
			'registrationStatus', w."registrationStatus"),
			w.server_version, w.server_xid FROM "Worker" w WHERE ($1 = '' OR w."plantId" = $1)`, a.ScopedPlantID())
	}, PullRequires("attendance.view", "attendance.record", "workers.manage", "payroll.view"))
	RegisterPull("refdata", func(ctx context.Context, a Actor, since Cursor) ([]Change, error) {
		if !refdataVersioned(ctx, a.DB) {
			return nil, nil
		}
		return Rows(ctx, a, since, "refdata", `SELECT r.id, to_jsonb(r) - 'server_xid' - 'server_version', r.server_version, r.server_xid FROM refdata_item r`)
	})
	// Until refdata_item carries server versions (F5 migration calling btn_enable_sync_version), send it whole.
	RegisterSnapshot("refdata", func(ctx context.Context, a Actor) ([]Change, error) {
		if refdataVersioned(ctx, a.DB) || !tableExists(ctx, a.DB, "refdata_item") {
			return nil, nil
		}
		return snapshotRows(ctx, a.DB, "refdata", `SELECT r.id, to_jsonb(r) FROM refdata_item r ORDER BY r.kind, r.sort_order, r.code`)
	})
	RegisterSnapshot("scope", func(ctx context.Context, a Actor) ([]Change, error) {
		var code *string
		if a.PlantID != "" {
			_ = a.DB.QueryRow(ctx, `SELECT code FROM "Plant" WHERE id=$1`, a.PlantID).Scan(&code)
		}
		data := map[string]any{"userId": a.UserID, "role": a.Role, "permissions": auth.PermissionsFor(a.Role),
			"plantId": a.PlantID, "plantCode": code, "scopedPlantId": a.ScopedPlantID(), "deviceId": a.DeviceID, "asOf": time.Now().UTC()}
		return []Change{{Entity: "scope", ID: "me", Op: "upsert", Data: data}}, nil
	})
	RegisterSnapshot("balances", balancesSnapshot, PullRequires("stock.view", "materials.view"))
}

var refdataFlag atomic.Int32 // 0 unknown, 1 versioned, 2 not versioned (re-checked every minute)
var refdataChecked atomic.Int64

func refdataVersioned(ctx context.Context, q db.DBTX) bool {
	now := time.Now().Unix()
	if f := refdataFlag.Load(); f != 0 && now-refdataChecked.Load() < 60 {
		return f == 1
	}
	var ok bool
	_ = q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema()
		AND table_name='refdata_item' AND column_name='server_version')`).Scan(&ok)
	if ok {
		refdataFlag.Store(1)
	} else {
		refdataFlag.Store(2)
	}
	refdataChecked.Store(now)
	return ok
}

func tableExists(ctx context.Context, q db.DBTX, name string) bool {
	var ok bool
	_ = q.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&ok)
	return ok
}

func snapshotRows(ctx context.Context, q db.DBTX, entity, sql string, args ...any) ([]Change, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Change{}
	for rows.Next() {
		var ch Change
		var raw []byte
		if err := rows.Scan(&ch.ID, &raw); err != nil {
			return nil, err
		}
		ch.Entity, ch.Op, ch.Data = entity, "upsert", json.RawMessage(raw)
		out = append(out, ch)
	}
	return out, rows.Err()
}

// balancesSnapshot: current finished-goods and raw-material balances in the user's scope, labelled with server time.
func balancesSnapshot(ctx context.Context, a Actor) ([]Change, error) {
	var asOf time.Time
	if err := a.DB.QueryRow(ctx, `SELECT now()`).Scan(&asOf); err != nil {
		return nil, err
	}
	asOf = asOf.UTC()
	plant := a.ScopedPlantID()
	out := []Change{}
	if auth.Can(a.Role, "stock.view") {
		rows, err := a.DB.Query(ctx, `SELECT m."plantId", m."productId", sum(m.quantity)::bigint FROM "StockMovement" m
			WHERE ($1 = '' OR m."plantId" = $1) GROUP BY 1, 2 ORDER BY 1, 2`, plant)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var p, item string
			var qty int64
			if err := rows.Scan(&p, &item, &qty); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, Change{Entity: "balances", ID: "fg:" + p + ":" + item, Op: "upsert",
				Data: map[string]any{"kind": "finished_goods", "plantId": p, "itemId": item, "qty": qty, "asOf": asOf}})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	if auth.Can(a.Role, "materials.view") {
		rows, err := a.DB.Query(ctx, `SELECT m."plantId", m."materialId", sum(m.quantity)::text FROM "MaterialMovement" m
			WHERE ($1 = '' OR m."plantId" = $1) GROUP BY 1, 2 ORDER BY 1, 2`, plant)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var p, item, qty string
			if err := rows.Scan(&p, &item, &qty); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, Change{Entity: "balances", ID: "rm:" + p + ":" + item, Op: "upsert",
				Data: map[string]any{"kind": "raw_material", "plantId": p, "itemId": item, "qty": db.TrimDecimal(qty), "asOf": asOf}})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}
