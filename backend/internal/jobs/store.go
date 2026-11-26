package jobs

import (
	"context"
	"fmt"
	"time"

	"btn-ops/internal/db"

	"github.com/jackc/pgx/v5"
)

// Run is one job_run row (JSON shape of the admin API).
type Run struct {
	ID           string     `json:"id"`
	Job          string     `json:"job"`
	Trigger      string     `json:"trigger"`
	Status       string     `json:"status"`
	Attempt      int        `json:"attempt"`
	ScheduledFor time.Time  `json:"scheduledFor"`
	StartedAt    *time.Time `json:"startedAt"`
	FinishedAt   *time.Time `json:"finishedAt"`
	DurationMs   *int       `json:"durationMs"`
	Error        *string    `json:"error"`
	TriggeredBy  *UserRef   `json:"triggeredBy"`
	Instance     *string    `json:"instance"`
	CreatedAt    time.Time  `json:"createdAt"`
}

// UserRef is a compact user reference.
type UserRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

const runCols = `r.id, r.job, r.trigger, r.status, r.attempt, r.scheduled_for, r.started_at, r.finished_at, r.duration_ms, r.error,
	r.triggered_by, u.name, r.instance, r.created_at`

func scanRun(row pgx.Row) (Run, error) {
	var r Run
	var uid, uname *string
	err := row.Scan(&r.ID, &r.Job, &r.Trigger, &r.Status, &r.Attempt, &r.ScheduledFor, &r.StartedAt, &r.FinishedAt, &r.DurationMs, &r.Error,
		&uid, &uname, &r.Instance, &r.CreatedAt)
	if uid != nil {
		r.TriggeredBy = &UserRef{ID: *uid}
		if uname != nil {
			r.TriggeredBy.Name = *uname
		}
	}
	return r, err
}

// ErrUnknownJob is returned by Enqueue for a name that is not registered.
var ErrUnknownJob = fmt.Errorf("unknown job")

// Enqueue queues a manual run of a registered job inside the caller's tx. The leader instance picks it up
// within one tick; call jobs.Wake() after commit to start it immediately on this instance. userID may be "".
func Enqueue(ctx context.Context, q db.DBTX, name, userID string) (Run, error) {
	if _, ok := Lookup(name); !ok {
		return Run{}, ErrUnknownJob
	}
	var by any
	if userID != "" {
		by = userID
	}
	var id string
	if err := q.QueryRow(ctx, `INSERT INTO job_run (job, trigger, status, attempt, scheduled_for, triggered_by)
		VALUES ($1, 'manual', 'queued', 1, $2, $3) RETURNING id`, name, db.Now(), by).Scan(&id); err != nil {
		return Run{}, err
	}
	return scanRun(q.QueryRow(ctx, `SELECT `+runCols+` FROM job_run r LEFT JOIN "User" u ON u.id = r.triggered_by WHERE r.id=$1`, id))
}

// HasQueuedManual reports whether a manual run of name is already waiting (to avoid double queuing).
func HasQueuedManual(ctx context.Context, q db.DBTX, name string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_run WHERE job=$1 AND status IN ('queued','running') AND trigger='manual')`, name).Scan(&ok)
	return ok, err
}

// Runs lists runs of one job (newest first).
func Runs(ctx context.Context, q db.DBTX, name string, limit, offset int) ([]Run, int, error) {
	var total int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM job_run WHERE job=$1`, name).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := q.Query(ctx, `SELECT `+runCols+` FROM job_run r LEFT JOIN "User" u ON u.id = r.triggered_by
		WHERE r.job=$1 ORDER BY r.created_at DESC, r.id LIMIT $2 OFFSET $3`, name, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// RecentRuns returns the last n runs of every job (map job -> runs, newest first).
func RecentRuns(ctx context.Context, q db.DBTX, n int) (map[string][]Run, error) {
	rows, err := q.Query(ctx, `SELECT `+runCols+` FROM (
		SELECT *, row_number() OVER (PARTITION BY job ORDER BY created_at DESC) AS rn FROM job_run) r
		LEFT JOIN "User" u ON u.id = r.triggered_by WHERE r.rn <= $1 ORDER BY r.job, r.created_at DESC`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out[r.Job] = append(out[r.Job], r)
	}
	return out, rows.Err()
}

// Stats summarises job health for the monitoring page.
type Stats struct {
	Failures24h    int                  `json:"failures24h"`
	Runs24h        int                  `json:"runs24h"`
	Queued         int                  `json:"queued"`
	HeartbeatAt    *time.Time           `json:"heartbeatAt"`
	LeaderInstance *string              `json:"leaderInstance"`
	LastSlots      map[string]time.Time `json:"-"`
	LastSuccess    map[string]time.Time `json:"-"`
}

// LoadStats reads the counters, the scheduler heartbeat and the per-job last slot / last success.
func LoadStats(ctx context.Context, q db.DBTX) (Stats, error) {
	st := Stats{LastSlots: map[string]time.Time{}, LastSuccess: map[string]time.Time{}}
	since := db.Now().Add(-24 * time.Hour)
	if err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status IN ('failed','timeout')), count(*) FILTER (WHERE status <> 'queued'),
		(SELECT count(*) FROM job_run WHERE status='queued') FROM job_run WHERE created_at >= $1`, since).Scan(&st.Failures24h, &st.Runs24h, &st.Queued); err != nil {
		return st, err
	}
	rows, err := q.Query(ctx, `SELECT job, last_slot, instance, updated_at FROM job_state`)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var job string
		var slot *time.Time
		var inst *string
		var upd time.Time
		if err := rows.Scan(&job, &slot, &inst, &upd); err != nil {
			rows.Close()
			return st, err
		}
		if job == HeartbeatJob {
			u := upd
			st.HeartbeatAt, st.LeaderInstance = &u, inst
			continue
		}
		if slot != nil {
			st.LastSlots[job] = slot.UTC()
		}
	}
	rows.Close()
	rows, err = q.Query(ctx, `SELECT job, max(finished_at) FROM job_run WHERE status='success' GROUP BY job`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var job string
		var t *time.Time
		if err := rows.Scan(&job, &t); err != nil {
			return st, err
		}
		if t != nil {
			st.LastSuccess[job] = t.UTC()
		}
	}
	return st, rows.Err()
}

// NextRun is the next scheduled time of j after its last handled slot (or after now); nil for manual jobs.
func NextRun(j Job, lastSlot time.Time, now time.Time) *time.Time {
	if j.sched == nil {
		return nil
	}
	from := now
	if !lastSlot.IsZero() && lastSlot.Before(now) {
		// a slot already due but not yet handled is the "next" run (it will be caught up)
		if n := j.sched.Next(lastSlot); !n.IsZero() && n.Before(now) {
			t := n.UTC()
			return &t
		}
	}
	n := j.sched.Next(from)
	if n.IsZero() {
		return nil
	}
	t := n.UTC()
	return &t
}

// Overdue reports whether a scheduled slot is more than grace past due without being handled (the
// scheduler is not running or is stuck).
func Overdue(j Job, lastSlot, now time.Time, grace time.Duration) bool {
	if j.sched == nil || lastSlot.IsZero() {
		return false
	}
	n := j.sched.Next(lastSlot)
	return !n.IsZero() && now.Sub(n) > grace
}
