package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"btn-ops/internal/db"
	"btn-ops/internal/i18n"
	"btn-ops/internal/jobs"
	"btn-ops/internal/mail"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Email is one queued message in email_outbox. Subject and Body are already localized plain text; the
// branded HTML layout is applied when it is sent.
type Email struct {
	To             string
	UserID         string // "" for operational addresses (OPS_ALERT_EMAIL)
	NotificationID string // "" when there is no in-app row
	Name           string // greeting name ("" = none)
	Lang           i18n.Lang
	Event          string
	Severity       Severity
	Subject, Body  string
	Link           string // in-app path; becomes an "Open" button with APP_URL prefixed
}

// MaxAttempts per email before it is marked failed.
const MaxAttempts = 6

// retryDelays after attempt n (1-based): 1 min, 5 min, 15 min, 1 h, 3 h.
var retryDelays = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 3 * time.Hour}

// RetryDelay is the back-off after the given failed attempt.
func RetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > len(retryDelays) {
		return retryDelays[len(retryDelays)-1]
	}
	return retryDelays[attempt-1]
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// QueueEmails inserts emails into the outbox inside the caller's tx (sent later by the notify.email job).
func QueueEmails(ctx context.Context, q db.DBTX, mails ...Email) error {
	for _, m := range mails {
		if strings.TrimSpace(m.To) == "" {
			continue
		}
		sev := m.Severity
		if sev == "" {
			sev = Info
		}
		lang := m.Lang
		if lang == "" {
			lang = i18n.Default
		}
		if _, err := q.Exec(ctx, `INSERT INTO email_outbox (notification_id, user_id, to_email, lang, event, severity, recipient_name, subject, body, link, max_attempts)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			nilIfEmpty(m.NotificationID), nilIfEmpty(m.UserID), strings.TrimSpace(m.To), string(lang), m.Event, string(sev), m.Name,
			truncate(m.Subject, 300), truncate(m.Body, 8000), nilIfEmpty(m.Link), MaxAttempts); err != nil {
			return err
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// DeliveryStats is the outcome of one DeliverPending call.
type DeliveryStats struct {
	Sent, Retrying, Failed int
	LastErr                error
}

// DeliverPending claims up to batch due emails (lease of 10 min, SKIP LOCKED) and sends them. A failed
// send is retried with RetryDelay back-off until MaxAttempts, then marked failed.
func DeliverPending(ctx context.Context, pool *pgxpool.Pool, sender mail.MessageSender, appURL string, batch int, now func() time.Time) (DeliveryStats, error) {
	var st DeliveryStats
	if now == nil {
		now = db.Now
	}
	if batch <= 0 {
		batch = 50
	}
	t := now().UTC()
	rows, err := pool.Query(ctx, `UPDATE email_outbox SET next_attempt_at = $1::timestamp + interval '10 minutes', attempts = attempts + 1
		WHERE id IN (SELECT id FROM email_outbox WHERE status='pending' AND next_attempt_at <= $1 ORDER BY next_attempt_at LIMIT $2 FOR UPDATE SKIP LOCKED)
		RETURNING id, to_email, lang, severity, recipient_name, subject, body, COALESCE(link, ''), user_id IS NULL, attempts, max_attempts`, t, batch)
	if err != nil {
		return st, err
	}
	type item struct {
		id, to, lang, sev, name, subject, body, link string
		ops                                          bool
		attempts, max                                int
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.to, &it.lang, &it.sev, &it.name, &it.subject, &it.body, &it.link, &it.ops, &it.attempts, &it.max); err != nil {
			rows.Close()
			return st, err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}
	base := strings.TrimRight(appURL, "/")
	for _, it := range items {
		if ctx.Err() != nil {
			break // lease expires; picked up next run
		}
		lang := langOf(it.lang)
		msg := mail.Message{
			To: it.to, Name: it.name, Lang: lang, Subject: it.subject, Title: it.subject, Paragraphs: []string{it.body},
			Critical: it.sev == string(Critical),
		}
		if it.link != "" && base != "" {
			msg.ActionURL, msg.ActionLabel = base+it.link, i18n.T(lang, "notify.mail.open")
		}
		if it.ops {
			msg.FooterNote = i18n.T(lang, "notify.mail.ops_footer")
		} else {
			msg.FooterNote = i18n.T(lang, "notify.mail.footer_reason")
			if base != "" {
				msg.FooterURL, msg.FooterLabel = base+"/notifications/preferences", i18n.T(lang, "notify.mail.manage")
			}
		}
		sctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		serr := sender.SendMessage(sctx, msg)
		cancel()
		wctx, wcancel := context.WithTimeout(context.Background(), 5*time.Second)
		if serr == nil {
			_, err = pool.Exec(wctx, `UPDATE email_outbox SET status='sent', sent_at=$2, last_error=NULL WHERE id=$1`, it.id, now().UTC())
			st.Sent++
		} else {
			st.LastErr = serr
			if it.attempts >= it.max {
				_, err = pool.Exec(wctx, `UPDATE email_outbox SET status='failed', last_error=$2 WHERE id=$1`, it.id, truncate(serr.Error(), 1000))
				st.Failed++
			} else {
				_, err = pool.Exec(wctx, `UPDATE email_outbox SET next_attempt_at=$2, last_error=$3 WHERE id=$1`,
					it.id, now().UTC().Add(RetryDelay(it.attempts)), truncate(serr.Error(), 1000))
				st.Retrying++
			}
		}
		wcancel()
		if err != nil {
			return st, err
		}
	}
	return st, nil
}

// EmailJob is the built-in job that drains the outbox every minute. A run fails (and so shows up on
// Administration → Jobs and in alerts) when emails were due but none could be sent.
func EmailJob(pool *pgxpool.Pool, sender mail.Sender, appURL string) jobs.Job {
	return jobs.Job{
		Name: "notify.email", Schedule: "* * * * *", Timeout: 4 * time.Minute, DescriptionKey: "notify.job.email",
		Run: func(ctx context.Context) error {
			ms, ok := sender.(mail.MessageSender)
			if !ok {
				return errors.New("the configured mailer cannot send notification emails")
			}
			for i := 0; i < 20; i++ { // up to 1000 emails per run
				st, err := DeliverPending(ctx, pool, ms, appURL, 50, nil)
				if err != nil {
					return err
				}
				if st.Sent == 0 && st.LastErr != nil {
					return fmt.Errorf("email delivery failing (%d will retry, %d gave up): %w", st.Retrying, st.Failed, st.LastErr)
				}
				if st.Sent+st.Retrying+st.Failed < 50 {
					return nil
				}
			}
			return nil
		},
	}
}

// OutboxStats counts outbox rows for the monitoring page.
type OutboxStats struct {
	Pending       int        `json:"pending"`
	Failed24h     int        `json:"failed24h"`
	Sent24h       int        `json:"sent24h"`
	OldestPending *time.Time `json:"oldestPending"`
}

// LoadOutboxStats reads email outbox counters.
func LoadOutboxStats(ctx context.Context, q db.DBTX) (OutboxStats, error) {
	var s OutboxStats
	since := db.Now().Add(-24 * time.Hour)
	err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='pending'),
		count(*) FILTER (WHERE status='failed' AND created_at >= $1),
		count(*) FILTER (WHERE status='sent' AND sent_at >= $1),
		min(created_at) FILTER (WHERE status='pending') FROM email_outbox`, since).Scan(&s.Pending, &s.Failed24h, &s.Sent24h, &s.OldestPending)
	return s, err
}
