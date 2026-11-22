package auth

import (
	"context"
	"time"

	"btn-ops/internal/db"
)

// Attempt kinds recorded in "AuthAttempt".
const (
	KindLogin     = "login"
	KindOTPSend   = "otp-send"
	KindOTPVerify = "otp-verify"
	KindReset     = "reset"
	KindPasskey   = "passkey"
)

// Limits are the DB-backed lockout rules (same values as Node).
var Limits = struct {
	LoginFailuresPerAccount int
	AccountLock             time.Duration
	LoginFailuresPerIP      int
	LoginIPWindow           time.Duration
	OTPSendsPerHour         int
	OTPResendCooldown       time.Duration
	OTPTTL                  time.Duration
	OTPMaxAttempts          int
	ResetPerIPPerHour       int
}{5, 15 * time.Minute, 20, 15 * time.Minute, 5, time.Minute, 10 * time.Minute, 5, 10}

// CountAttempts counts attempts for key within the window (only failures when onlyFailures).
func CountAttempts(ctx context.Context, q db.DBTX, key, kind string, window time.Duration, onlyFailures bool) (int, error) {
	var n int
	sql := `SELECT count(*) FROM "AuthAttempt" WHERE key=$1 AND kind=$2 AND "createdAt" >= $3`
	if onlyFailures {
		sql += ` AND success=false`
	}
	err := q.QueryRow(ctx, sql, key, kind, time.Now().UTC().Add(-window)).Scan(&n)
	return n, err
}

// RecordAttempt appends an attempt row.
func RecordAttempt(ctx context.Context, q db.DBTX, key, kind string, success bool) error {
	_, err := q.Exec(ctx, `INSERT INTO "AuthAttempt" (id, key, kind, success) VALUES ($1,$2,$3,$4)`, db.NewID(), key, kind, success)
	return err
}

// PruneAttempts drops attempt rows older than a day.
func PruneAttempts(ctx context.Context, q db.DBTX) {
	_, _ = q.Exec(ctx, `DELETE FROM "AuthAttempt" WHERE "createdAt" < $1`, time.Now().UTC().Add(-24*time.Hour))
}
