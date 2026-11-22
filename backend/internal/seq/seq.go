// Package seq issues gap-tolerant, race-free document numbers from the "Sequence" table.
package seq

import (
	"context"
	"fmt"

	"btn-ops/internal/db"
)

// NextNumber returns the next number for prefix, e.g. NextNumber(ctx, tx, "SO-2026") → "SO-2026-00001".
// The upsert takes a row lock, so concurrent transactions get distinct values; call it inside the same
// transaction that creates the document. pad defaults to 5.
func NextNumber(ctx context.Context, tx db.DBTX, prefix string, pad ...int) (string, error) {
	n := 5
	if len(pad) > 0 {
		n = pad[0]
	}
	var v int
	err := tx.QueryRow(ctx, `INSERT INTO "Sequence" (key, value) VALUES ($1, 1)
		ON CONFLICT (key) DO UPDATE SET value = "Sequence".value + 1 RETURNING value`, prefix).Scan(&v)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%0*d", prefix, n, v), nil
}
