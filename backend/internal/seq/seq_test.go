package seq_test

import (
	"sync"
	"testing"

	"btn-ops/internal/db"
	"btn-ops/internal/seq"
	"btn-ops/internal/testutil"

	"github.com/jackc/pgx/v5"
)

func TestNextNumberFormatAndConcurrency(t *testing.T) {
	pool := testutil.Reset(t)
	ctx := t.Context()
	first, err := seq.NextNumber(ctx, pool, "SO-2026")
	if err != nil || first != "SO-2026-00001" {
		t.Fatalf("%q %v", first, err)
	}
	if n, _ := seq.NextNumber(ctx, pool, "PR-2026", 3); n != "PR-2026-001" {
		t.Fatalf("pad: %s", n)
	}
	const workers = 20
	got := make(chan string, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := db.Tx(ctx, pool, func(tx pgx.Tx) error {
				n, err := seq.NextNumber(ctx, tx, "SO-2026")
				got <- n
				return err
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	close(got)
	seen := map[string]bool{}
	for n := range got {
		if seen[n] {
			t.Fatalf("duplicate number %s", n)
		}
		seen[n] = true
	}
	if len(seen) != workers || !seen["SO-2026-00021"] {
		t.Fatalf("expected 20 distinct numbers up to 00021, got %d", len(seen))
	}
}

func TestRolledBackTransactionDoesNotConsumeNumber(t *testing.T) {
	pool := testutil.Reset(t)
	ctx := t.Context()
	_ = db.Tx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := seq.NextNumber(ctx, tx, "X-1"); err != nil {
			return err
		}
		return pgx.ErrTxClosed // force rollback
	})
	if n, _ := seq.NextNumber(ctx, pool, "X-1"); n != "X-1-00001" {
		t.Fatalf("rollback should release the number, got %s", n)
	}
}
