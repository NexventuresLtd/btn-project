package monitor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"btn-ops/internal/dates"
	"btn-ops/internal/deps"
	"btn-ops/internal/i18n"
	"btn-ops/internal/jobs"
	"btn-ops/internal/notify"
)

// Retention periods applied by the retention.cleanup job.
var Retention = struct {
	AuthAttempts, ReadNotifications, SentEmails, JobRuns, Occurrences, ResolvedErrors time.Duration
}{
	AuthAttempts:      90 * 24 * time.Hour,
	ReadNotifications: 180 * 24 * time.Hour,
	SentEmails:        30 * 24 * time.Hour,
	JobRuns:           90 * 24 * time.Hour,
	Occurrences:       30 * 24 * time.Hour,
	ResolvedErrors:    90 * 24 * time.Hour,
}

// Setup installs the job failure hook and registers the built-in jobs (idempotent).
func Setup(d *deps.Deps) {
	jobs.SetFailureHook(func(ctx context.Context, j jobs.Job, run jobs.RunInfo, err error) { JobFailed(ctx, d, j, run, err) })
	jobs.Register(notify.EmailJob(d.Pool, d.Mail, d.Cfg.AppURL))
	jobs.Register(jobs.Job{Name: "retention.cleanup", Schedule: "15 3 * * *", Timeout: 15 * time.Minute, Retries: 2, Backoff: 10 * time.Minute,
		DescriptionKey: "monitor.job.retention", Run: func(ctx context.Context) error { _, err := Cleanup(ctx, d, time.Now()); return err }})
	jobs.Register(jobs.Job{Name: "monitor.selfcheck", Schedule: "*/5 * * * *", Timeout: time.Minute,
		DescriptionKey: "monitor.job.selfcheck", Run: func(ctx context.Context) error { return SelfCheck(ctx, d) }})
	jobs.Register(jobs.Job{Name: "monitor.health_digest", Schedule: "0 7 * * *", Timeout: 2 * time.Minute, Retries: 2, Backoff: 5 * time.Minute,
		DescriptionKey: "monitor.job.digest", Run: func(ctx context.Context) error { _, err := Digest(ctx, d, time.Now()); return err }})
}

// JobFailed records a final job failure as an error event and alerts (rate limited per job, 1 h).
func JobFailed(ctx context.Context, d *deps.Deps, j jobs.Job, run jobs.RunInfo, err error) {
	stack := ""
	var pe *jobs.PanicError
	if errors.As(err, &pe) {
		stack = pe.Stack
	}
	rec, rerr := Record(ctx, d.Pool, Event{Kind: "job", Route: j.Name, Message: err.Error(), Stack: stack})
	if rerr != nil {
		slog.Error("monitor: record job failure", "job", j.Name, "err", rerr)
	}
	if rec.Muted {
		return
	}
	if _, aerr := Alert(ctx, d, AlertSpec{
		Event: EvJobFailed, Key: "job:" + j.Name, Window: time.Hour,
		TitleKey: "notify.job_failed.title", BodyKey: "notify.job_failed.body",
		Params: map[string]any{"job": j.Name, "attempts": run.Attempt, "status": run.Status, "error": truncate(err.Error(), 300)},
		Link:   "/admin/jobs?job=" + j.Name,
	}); aerr != nil {
		slog.Error("monitor: job failure alert", "job", j.Name, "err", aerr)
	}
}

// Cleanup deletes operational rows past their retention period. Returns rows deleted per table.
func Cleanup(ctx context.Context, d *deps.Deps, now time.Time) (map[string]int64, error) {
	now = now.UTC()
	out := map[string]int64{}
	steps := []struct {
		name, sql string
		arg       time.Time
	}{
		{"AuthAttempt", `DELETE FROM "AuthAttempt" WHERE "createdAt" < $1`, now.Add(-Retention.AuthAttempts)},
		{"OtpChallenge", `DELETE FROM "OtpChallenge" WHERE "expiresAt" < $1`, now.Add(-24 * time.Hour)},
		{"passkey_challenge", `DELETE FROM passkey_challenge WHERE expires_at < $1`, now.Add(-time.Hour)},
		{"notification", `DELETE FROM notification WHERE read_at IS NOT NULL AND read_at < $1`, now.Add(-Retention.ReadNotifications)},
		{"email_outbox", `DELETE FROM email_outbox WHERE status IN ('sent','failed') AND created_at < $1`, now.Add(-Retention.SentEmails)},
		{"job_run", `DELETE FROM job_run WHERE status NOT IN ('queued','running') AND created_at < $1`, now.Add(-Retention.JobRuns)},
		{"error_occurrence", `DELETE FROM error_occurrence WHERE at < $1`, now.Add(-Retention.Occurrences)},
		{"error_event", `DELETE FROM error_event WHERE resolved_at IS NOT NULL AND last_seen_at < $1`, now.Add(-Retention.ResolvedErrors)},
		{"alert_throttle", `DELETE FROM alert_throttle WHERE last_sent_at < $1`, now.Add(-7 * 24 * time.Hour)},
	}
	var errs []string
	for _, s := range steps {
		tag, err := d.Pool.Exec(ctx, s.sql, s.arg)
		if err != nil {
			errs = append(errs, s.name+": "+err.Error())
			continue
		}
		out[s.name] = tag.RowsAffected()
	}
	slog.Info("retention cleanup", "deleted", out)
	if len(errs) > 0 {
		return out, fmt.Errorf("retention cleanup: %s", strings.Join(errs, "; "))
	}
	return out, nil
}

// SelfCheck runs the health summary and alerts on critical issues (each issue code at most every 6 h).
func SelfCheck(ctx context.Context, d *deps.Deps) error {
	h := Summary(ctx, d, i18n.Default, true)
	for _, is := range h.Issues {
		if is.Severity != "critical" || is.Code == "scheduler_down" { // the scheduler is running this job
			continue
		}
		if _, err := Alert(ctx, d, AlertSpec{
			Event: EvHealthAlert, Key: "health:" + is.Code, Window: 6 * time.Hour,
			TitleKey: "notify.health_alert.title", BodyKey: "notify.health_alert.body",
			Params: map[string]any{"issue": is.Message}, Link: "/admin/monitoring",
		}); err != nil {
			return err
		}
	}
	return nil
}

// DigestStats is what the daily digest reports.
type DigestStats struct {
	Occurrences, NewErrors, OpenErrors, JobFailures, EmailFailures int
}

// Digest sends the daily health digest to admin.monitor holders (and OPS_ALERT_EMAIL) when anything went
// wrong in the last 24 hours. Returns the stats and whether it was sent.
func Digest(ctx context.Context, d *deps.Deps, now time.Time) (bool, error) {
	since := now.UTC().Add(-24 * time.Hour)
	var s DigestStats
	if err := d.Pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM error_occurrence WHERE at >= $1),
		(SELECT count(*) FROM error_event WHERE first_seen_at >= $1),
		(SELECT count(*) FROM error_event WHERE resolved_at IS NULL),
		(SELECT count(*) FROM job_run WHERE status IN ('failed','timeout') AND created_at >= $1),
		(SELECT count(*) FROM email_outbox WHERE status = 'failed' AND created_at >= $1)`, since).
		Scan(&s.Occurrences, &s.NewErrors, &s.OpenErrors, &s.JobFailures, &s.EmailFailures); err != nil {
		return false, err
	}
	if s.Occurrences == 0 && s.JobFailures == 0 && s.EmailFailures == 0 {
		return false, nil
	}
	day := dates.TodayISO(now)
	return Alert(ctx, d, AlertSpec{
		Event: EvHealthDigest, Key: "digest:" + day, Window: 20 * time.Hour, Severity: notify.Info,
		TitleKey: "notify.health_digest.title", BodyKey: "notify.health_digest.body",
		Params: map[string]any{"date": dates.FormatDate(dates.MustParseISO(day)), "errors": s.Occurrences, "new": s.NewErrors,
			"open": s.OpenErrors, "jobs": s.JobFailures, "emails": s.EmailFailures},
		Link: "/admin/monitoring",
	})
}
