package monitor

import (
	"context"
	"errors"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"btn-ops/internal/deps"
	"btn-ops/internal/i18n"
	"btn-ops/internal/jobs"
	"btn-ops/internal/mail"
	"btn-ops/internal/notify"
)

// Health is the admin health summary.
type Health struct {
	Status    string             `json:"status"` // ok | degraded | down
	CheckedAt time.Time          `json:"checkedAt"`
	Issues    []Issue            `json:"issues"`
	App       AppInfo            `json:"app"`
	Database  DBHealth           `json:"database"`
	SMTP      SMTPCheck          `json:"smtp"`
	Jobs      JobHealth          `json:"jobs"`
	Errors    ErrHealth          `json:"errors"`
	Email     notify.OutboxStats `json:"email"`
	Disk      DiskInfo           `json:"disk"`
}

// Issue is one problem found by the health check (Message is localized).
type Issue struct {
	Code     string `json:"code"`
	Severity string `json:"severity"` // warning | critical
	Message  string `json:"message"`
}

type AppInfo struct {
	Version       string    `json:"version"`
	GoVersion     string    `json:"goVersion"`
	Env           string    `json:"env"`
	Instance      string    `json:"instance"`
	StartedAt     time.Time `json:"startedAt"`
	UptimeSeconds int64     `json:"uptimeSeconds"`
}

type PoolStats struct {
	Total             int32   `json:"total"`
	Idle              int32   `json:"idle"`
	Acquired          int32   `json:"acquired"`
	Max               int32   `json:"max"`
	AcquireCount      int64   `json:"acquireCount"`
	EmptyAcquireCount int64   `json:"emptyAcquireCount"`
	AvgAcquireMs      float64 `json:"avgAcquireMs"`
}

type DBHealth struct {
	OK        bool      `json:"ok"`
	LatencyMs float64   `json:"latencyMs"`
	Error     string    `json:"error,omitempty"`
	Pool      PoolStats `json:"pool"`
}

type SMTPCheck struct {
	Status    string    `json:"status"` // ok | unreachable | not_configured | unknown
	LatencyMs float64   `json:"latencyMs"`
	Error     string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
}

type JobHealth struct {
	SchedulerAlive bool       `json:"schedulerAlive"`
	HeartbeatAt    *time.Time `json:"heartbeatAt"`
	Leader         *string    `json:"leader"`
	Runs24h        int        `json:"runs24h"`
	Failures24h    int        `json:"failures24h"`
	Queued         int        `json:"queued"`
	Overdue        []string   `json:"overdue"`
	Failing        []string   `json:"failing"` // last run failed
	MaxLagSeconds  int64      `json:"maxLagSeconds"`
}

type ErrHealth struct {
	Open           int     `json:"open"`
	Occurrences24h int     `json:"occurrences24h"`
	New24h         int     `json:"new24h"`
	Requests24h    int64   `json:"requests24h"`     // this instance
	ServerErrors24 int64   `json:"serverErrors24h"` // this instance
	ErrorRate      float64 `json:"errorRate"`       // % of requests answered 5xx (this instance)
}

type DiskInfo struct {
	Path        string  `json:"path"`
	FreeBytes   uint64  `json:"freeBytes"`
	TotalBytes  uint64  `json:"totalBytes"`
	FreePercent float64 `json:"freePercent"`
	Error       string  `json:"error,omitempty"`
}

// Version is APP_VERSION, else the VCS revision embedded at build time, else "dev".
func Version() string {
	if v := os.Getenv("APP_VERSION"); v != "" {
		return v
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		rev, dirty := "", false
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
		if len(rev) > 12 {
			rev = rev[:12]
		}
		if rev != "" {
			if dirty {
				rev += "-dirty"
			}
			return rev
		}
		if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			return bi.Main.Version
		}
	}
	return "dev"
}

// ── SMTP probe (cached) ─────────────────────────────────────────────────────────────────────────────

type prober interface {
	Probe(ctx context.Context) error
}

var (
	smtpMu     sync.Mutex
	smtpCached SMTPCheck
)

// SMTPTTL is how long an SMTP reachability result is reused.
const SMTPTTL = 5 * time.Minute

// CheckSMTP dials the SMTP server (cached SMTPTTL unless fresh).
func CheckSMTP(ctx context.Context, m mail.Sender, fresh bool) SMTPCheck {
	smtpMu.Lock()
	defer smtpMu.Unlock()
	if !fresh && !smtpCached.CheckedAt.IsZero() && time.Since(smtpCached.CheckedAt) < SMTPTTL {
		return smtpCached
	}
	res := SMTPCheck{Status: "unknown", CheckedAt: time.Now().UTC()}
	if p, ok := m.(prober); ok {
		start := time.Now()
		err := p.Probe(ctx)
		res.LatencyMs = ms(time.Since(start))
		switch {
		case errors.Is(err, mail.ErrNotConfigured):
			res.Status = "not_configured"
		case err != nil:
			res.Status, res.Error = "unreachable", err.Error()
		default:
			res.Status = "ok"
		}
	}
	smtpCached = res
	return res
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// Summary builds the health summary. fresh forces a new SMTP probe.
func Summary(ctx context.Context, d *deps.Deps, lang i18n.Lang, fresh bool) Health {
	now := time.Now().UTC()
	h := Health{Status: "ok", CheckedAt: now, Issues: []Issue{}}
	host, _ := os.Hostname()
	h.App = AppInfo{Version: Version(), GoVersion: runtime.Version(), Env: d.Cfg.AppEnv, Instance: host, StartedAt: Started.UTC(), UptimeSeconds: int64(time.Since(Started).Seconds())}
	add := func(code, sev string, args ...any) {
		h.Issues = append(h.Issues, Issue{Code: code, Severity: sev, Message: i18n.T(lang, "monitor.issue."+code, args...)})
		if h.Status == "ok" {
			h.Status = "degraded"
		}
	}

	// database
	st := d.Pool.Stat()
	h.Database.Pool = PoolStats{Total: st.TotalConns(), Idle: st.IdleConns(), Acquired: st.AcquiredConns(), Max: st.MaxConns(),
		AcquireCount: st.AcquireCount(), EmptyAcquireCount: st.EmptyAcquireCount()}
	if st.AcquireCount() > 0 {
		h.Database.Pool.AvgAcquireMs = ms(st.AcquireDuration()) / float64(st.AcquireCount())
	}
	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	start := time.Now()
	var one int
	err := d.Pool.QueryRow(dctx, `SELECT 1`).Scan(&one)
	cancel()
	h.Database.LatencyMs = ms(time.Since(start))
	if err != nil {
		h.Database.Error = err.Error()
		h.Status = "down"
		h.Issues = append(h.Issues, Issue{Code: "db_down", Severity: "critical", Message: i18n.T(lang, "monitor.issue.db_down")})
		return h
	}
	h.Database.OK = true
	if h.Database.LatencyMs > 200 {
		add("db_slow", "warning", "ms", int(h.Database.LatencyMs))
	}
	if st.MaxConns() > 0 && st.AcquiredConns() >= st.MaxConns() {
		add("pool_exhausted", "warning")
	}

	// smtp
	h.SMTP = CheckSMTP(ctx, d.Mail, fresh)
	if h.SMTP.Status == "unreachable" {
		add("smtp_unreachable", "critical")
	}

	// jobs
	if js, err := jobs.LoadStats(ctx, d.Pool); err == nil {
		h.Jobs = JobHealth{SchedulerAlive: jobs.SchedulerAlive(js, now), HeartbeatAt: js.HeartbeatAt, Leader: js.LeaderInstance,
			Runs24h: js.Runs24h, Failures24h: js.Failures24h, Queued: js.Queued, Overdue: []string{}, Failing: []string{}}
		recent, _ := jobs.RecentRuns(ctx, d.Pool, 1)
		for _, j := range jobs.All() {
			slot := js.LastSlots[j.Name]
			if jobs.Overdue(j, slot, now, 5*time.Minute) {
				h.Jobs.Overdue = append(h.Jobs.Overdue, j.Name)
				if n := j.Cron().Next(slot); !n.IsZero() {
					if lag := int64(now.Sub(n).Seconds()); lag > h.Jobs.MaxLagSeconds {
						h.Jobs.MaxLagSeconds = lag
					}
				}
			}
			if rs := recent[j.Name]; len(rs) > 0 && (rs[0].Status == "failed" || rs[0].Status == "timeout") {
				h.Jobs.Failing = append(h.Jobs.Failing, j.Name)
			}
		}
		if !h.Jobs.SchedulerAlive {
			add("scheduler_down", "critical")
		}
		if len(h.Jobs.Overdue) > 0 {
			add("jobs_overdue", "warning", "count", len(h.Jobs.Overdue))
		}
		if len(h.Jobs.Failing) > 0 {
			add("jobs_failing", "warning", "count", len(h.Jobs.Failing))
		}
	}

	// errors
	since := now.Add(-24 * time.Hour)
	_ = d.Pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM error_event WHERE resolved_at IS NULL),
		(SELECT count(*) FROM error_occurrence WHERE at >= $1),
		(SELECT count(*) FROM error_event WHERE first_seen_at >= $1)`, since).Scan(&h.Errors.Open, &h.Errors.Occurrences24h, &h.Errors.New24h)
	h.Errors.Requests24h, h.Errors.ServerErrors24 = RequestStats()
	if h.Errors.Requests24h > 0 {
		h.Errors.ErrorRate = float64(h.Errors.ServerErrors24) * 100 / float64(h.Errors.Requests24h)
	}
	if h.Errors.Requests24h >= 50 && h.Errors.ErrorRate >= 2 {
		add("error_rate", "critical", "rate", formatPct(h.Errors.ErrorRate))
	} else if h.Errors.New24h > 0 {
		add("new_errors", "warning", "count", h.Errors.New24h)
	}

	// email outbox
	if es, err := notify.LoadOutboxStats(ctx, d.Pool); err == nil {
		h.Email = es
		if es.Failed24h > 0 {
			add("email_failed", "warning", "count", es.Failed24h)
		}
		if es.OldestPending != nil && now.Sub(*es.OldestPending) > 30*time.Minute {
			add("email_backlog", "warning", "count", es.Pending)
		}
	}

	// disk
	h.Disk = DiskUsage(d.Cfg.DataDir)
	if h.Disk.Error == "" && h.Disk.TotalBytes > 0 {
		if h.Disk.FreePercent < 5 || h.Disk.FreeBytes < 1<<30 {
			add("disk_low", "critical", "free", formatPct(h.Disk.FreePercent))
		} else if h.Disk.FreePercent < 15 {
			add("disk_low", "warning", "free", formatPct(h.Disk.FreePercent))
		}
	}
	return h
}

func formatPct(f float64) string { return strconv.FormatFloat(f, 'f', 1, 64) + "%" }
