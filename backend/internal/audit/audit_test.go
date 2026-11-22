package audit_test

import (
	"encoding/json"
	"errors"
	"testing"

	"btn-ops/internal/audit"
	"btn-ops/internal/db"
	"btn-ops/internal/testutil"

	"github.com/jackc/pgx/v5"
)

func TestAuditCommitsAtomicallyWithTheTransaction(t *testing.T) {
	pool := testutil.Reset(t)
	ctx := t.Context()
	who := audit.Actor{IP: "10.1.2.3"}

	// committed
	err := db.Tx(ctx, pool, func(tx pgx.Tx) error {
		return audit.Log(ctx, tx, who, audit.Input{Action: "demo.create", Entity: "Demo", EntityID: "d1", Summary: "made it",
			Before: nil, After: map[string]any{"qty": 5}})
	})
	if err != nil {
		t.Fatal(err)
	}
	// rolled back
	_ = db.Tx(ctx, pool, func(tx pgx.Tx) error {
		_ = audit.Log(ctx, tx, who, audit.Input{Action: "demo.lost", Entity: "Demo"})
		return errors.New("boom")
	})
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM "AuditLog" WHERE entity='Demo'`).Scan(&n)
	if n != 1 {
		t.Fatalf("expected only the committed entry, got %d", n)
	}
	var after []byte
	var before *string
	var ip, summary string
	if err := pool.QueryRow(ctx, `SELECT "after", "before"::text, ip, summary FROM "AuditLog" WHERE action='demo.create'`).Scan(&after, &before, &ip, &summary); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(after, &m)
	if m["qty"].(float64) != 5 || before != nil || ip != "10.1.2.3" || summary != "made it" {
		t.Fatalf("stored: %v %v %s %s", m, before, ip, summary)
	}
}
