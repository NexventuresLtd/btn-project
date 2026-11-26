package monitor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/i18n"
	"btn-ops/internal/notify"

	"github.com/jackc/pgx/v5"
)

// Events registered by the platform (all visible to admin.monitor holders only).
const (
	EvJobFailed     = "system.job_failed"
	EvCriticalError = "system.critical_error"
	EvHealthAlert   = "system.health_alert"
	EvHealthDigest  = "system.health_digest"
)

func init() {
	for _, e := range []notify.Event{
		{Key: EvJobFailed, Category: notify.CatSystem, Critical: true, DefaultEmail: true},
		{Key: EvCriticalError, Category: notify.CatSystem, Critical: true, DefaultEmail: true},
		{Key: EvHealthAlert, Category: notify.CatSystem, Critical: true, DefaultEmail: true},
		{Key: EvHealthDigest, Category: notify.CatSystem, DefaultEmail: true},
	} {
		e.Permissions = append(e.Permissions, "admin.monitor")
		notify.RegisterEvent(e)
	}
}

// GlobalAlertsPerHour caps all alerts together (on top of the per-key window).
const GlobalAlertsPerHour = 20

// AlertSpec describes one alert.
type AlertSpec struct {
	Event    string // EvJobFailed | EvCriticalError | EvHealthAlert | EvHealthDigest
	Key      string // throttle key (fingerprint, job name, check name)
	Window   time.Duration
	Severity notify.Severity // default Critical
	TitleKey string
	BodyKey  string
	Params   map[string]any
	Link     string
}

// Allow implements the shared alert rate limit: at most one alert per key per window, and at most
// GlobalAlertsPerHour alerts per clock hour across all keys. Suppressed alerts are counted.
func Allow(ctx context.Context, q db.DBTX, key string, window time.Duration, now time.Time) (bool, error) {
	now = now.UTC().Truncate(time.Millisecond)
	var ok bool
	err := q.QueryRow(ctx, `INSERT INTO alert_throttle (key, last_sent_at) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET last_sent_at = EXCLUDED.last_sent_at, suppressed = 0
		WHERE alert_throttle.last_sent_at <= $3 RETURNING true`, key, now, now.Add(-window)).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = q.Exec(ctx, `UPDATE alert_throttle SET suppressed = suppressed + 1 WHERE key=$1`, key)
		return false, err
	}
	if err != nil {
		return false, err
	}
	var n int
	hour := fmt.Sprintf("__hour__:%s", now.Format("2006010215"))
	if err := q.QueryRow(ctx, `INSERT INTO alert_throttle (key, last_sent_at, suppressed) VALUES ($1, $2, 1)
		ON CONFLICT (key) DO UPDATE SET suppressed = alert_throttle.suppressed + 1, last_sent_at = EXCLUDED.last_sent_at
		RETURNING suppressed`, hour, now).Scan(&n); err != nil {
		return false, err
	}
	return n <= GlobalAlertsPerHour, nil
}

// Alert notifies admin.monitor holders (in-app + their email preference) and queues an email to
// OPS_ALERT_EMAIL, unless rate limited. Returns whether it was sent.
func Alert(ctx context.Context, d *deps.Deps, a AlertSpec) (bool, error) {
	if a.Window <= 0 {
		a.Window = time.Hour
	}
	if a.Severity == "" {
		a.Severity = notify.Critical
	}
	sent := false
	err := db.Tx(ctx, d.Pool, func(tx pgx.Tx) error {
		ok, err := Allow(ctx, tx, a.Key, a.Window, time.Now())
		if err != nil || !ok {
			return err
		}
		sent = true
		if err := notify.Send(ctx, tx, notify.Notification{
			Event: a.Event, Severity: a.Severity, TitleKey: a.TitleKey, BodyKey: a.BodyKey, Params: a.Params, Link: a.Link,
			To: notify.ToPermission("admin.monitor"), DedupKey: a.Key,
		}); err != nil {
			return err
		}
		if to := d.Cfg.OpsAlertEmail; to != "" {
			lang := i18n.Default
			return notify.QueueEmails(ctx, tx, notify.Email{
				To: to, Lang: lang, Event: a.Event, Severity: a.Severity,
				Subject: "[BTN Ops] " + notify.Render(lang, a.TitleKey, a.Params), Body: notify.Render(lang, a.BodyKey, a.Params), Link: a.Link,
			})
		}
		return nil
	})
	return sent, err
}
