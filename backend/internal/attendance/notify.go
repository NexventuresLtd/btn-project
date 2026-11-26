package attendance

// Notifications (RPT-04/05 for the workforce module) and the attendance cut-off job.

import (
	"context"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/jobs"
	"btn-ops/internal/notify"

	"github.com/jackc/pgx/v5"
)

// Notification events.
const (
	EventNotSubmitted      = "attendance.not_submitted"
	EventApprovalPending   = "attendance.approval_pending"
	EventCorrectionPending = "attendance.correction_pending"
	EventCorrectionDecided = "attendance.correction_decided"
)

// SettingCutoffHour is the Kigali hour after which an unsubmitted site day raises a reminder (default 17).
const SettingCutoffHour = "attendance.cutoffHour"

// DefaultCutoffHour applies when the setting is absent.
const DefaultCutoffHour = 17

// JobCutoff is the job name of the cut-off reminder.
const JobCutoff = "attendance.cutoff_reminder"

func init() {
	notify.RegisterEvent(notify.Event{Key: EventNotSubmitted, Category: notify.CatWorkforce, DefaultEmail: true, Permissions: []auth.Permission{"attendance.record", "attendance.approve"}})
	notify.RegisterEvent(notify.Event{Key: EventApprovalPending, Category: notify.CatWorkforce, Permissions: []auth.Permission{"attendance.approve"}})
	notify.RegisterEvent(notify.Event{Key: EventCorrectionPending, Category: notify.CatWorkforce, DefaultEmail: true, Permissions: []auth.Permission{"attendance.approve"}})
	notify.RegisterEvent(notify.Event{Key: EventCorrectionDecided, Category: notify.CatWorkforce, Permissions: []auth.Permission{"attendance.record"}})
}

func registerJobs(d *deps.Deps) {
	jobs.Register(jobs.Job{Name: JobCutoff, Schedule: "0 * * * *", Timeout: 2 * time.Minute, Retries: 1, DescriptionKey: "attendance.job.cutoff",
		Run: func(ctx context.Context) error {
			hour := DefaultCutoffHour
			if d.Auth != nil {
				hour = d.Auth.SettingInt(ctx, SettingCutoffHour, DefaultCutoffHour)
			}
			_, err := RunCutoff(ctx, d.Pool, time.Now(), hour)
			return err
		}})
}

// RunCutoff reminds every site whose day is not submitted once the Kigali clock passes cutoffHour (once per site
// and day). Returns the sites reminded.
func RunCutoff(ctx context.Context, pool *db.Pool, now time.Time, cutoffHour int) ([]string, error) {
	local := now.In(dates.Kigali())
	if local.Hour() < cutoffHour {
		return nil, nil
	}
	today := dates.MustParseISO(local.Format("2006-01-02"))
	var sites []plantRef
	err := db.Tx(ctx, pool, func(tx pgx.Tx) error {
		var err error
		if sites, err = unsubmittedSites(ctx, tx, today); err != nil {
			return err
		}
		for _, p := range sites {
			if _, err := tx.Exec(ctx, `INSERT INTO attendance_day (plant_id, date, status, reminded_at, updated_at) VALUES ($1, $2, 'OPEN', now(), now())
				ON CONFLICT (plant_id, date) DO UPDATE SET reminded_at = now()`, p.ID, today); err != nil {
				return err
			}
			aud := notify.ToPermissionAtSite("attendance.record", p.ID)
			if err := notify.Send(ctx, tx, notify.Notification{Event: EventNotSubmitted, Severity: notify.Warning,
				TitleKey: "attendance.notify.not_submitted_title", BodyKey: "attendance.notify.not_submitted_body",
				Params: map[string]any{"plant": p.Code, "date": dates.FormatDate(today), "hour": cutoffHour},
				Link:   "/attendance?plantId=" + p.ID, DedupKey: p.ID + ":" + dates.ToISO(today), To: aud}); err != nil {
				return err
			}
			if err := notify.Send(ctx, tx, notify.Notification{Event: EventNotSubmitted, Severity: notify.Warning,
				TitleKey: "attendance.notify.not_submitted_title", BodyKey: "attendance.notify.not_submitted_body",
				Params: map[string]any{"plant": p.Code, "date": dates.FormatDate(today), "hour": cutoffHour},
				Link:   "/attendance?plantId=" + p.ID, DedupKey: p.ID + ":" + dates.ToISO(today), To: notify.ToPermissionAtSite("attendance.approve", p.ID)}); err != nil {
				return err
			}
		}
		return nil
	})
	codes := make([]string, len(sites))
	for i, p := range sites {
		codes[i] = p.Code
	}
	return codes, err
}
