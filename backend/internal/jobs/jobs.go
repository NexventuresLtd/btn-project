// Package jobs is the in-process background job runtime (replaces Celery/Redis from SRS §3.2).
//
//	jobs.Register(jobs.Job{
//	    Name:     "reports.daily_email",          // unique, [a-z0-9._-]
//	    Schedule: "30 6 * * *",                   // cron "m h dom mon dow", evaluated in Africa/Kigali; "" = manual only
//	    Run:      func(ctx context.Context) error { ... },
//	    Timeout:  2 * time.Minute,                // default 10 min; ctx is cancelled when it expires
//	    Retries:  2, Backoff: time.Minute,        // re-run up to 2 more times after 1 min, then 2 min (doubling, max 1 h)
//	    DescriptionKey: "reports.job.daily_email" // backend i18n key shown on Administration → Jobs
//	})
//
// Exactly one instance runs jobs: the scheduler holds a PostgreSQL session advisory lock (other instances
// stand by and take over when the leader stops). Every execution is recorded in job_run. A missed schedule
// (server down) is caught up ONCE when the scheduler next runs. Manual runs (Administration → Jobs → Run now,
// or jobs.Enqueue) are queued in job_run and picked up by the leader. When the last attempt fails the
// failure hook (set by internal/monitor) alerts users holding admin.monitor and OPS_ALERT_EMAIL.
package jobs

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"sync"
	"time"
)

// Job describes a background job. Register it once at start-up (module Register functions are fine:
// registering the same Name again replaces the previous definition).
type Job struct {
	Name           string
	Schedule       string // cron in Africa/Kigali; "" = only manual / enqueued runs
	Run            func(ctx context.Context) error
	Timeout        time.Duration // default DefaultTimeout
	Retries        int           // additional attempts after a failure (0 = none)
	Backoff        time.Duration // delay before the first retry (default 1 min); doubles per attempt, capped at 1 h
	DescriptionKey string        // optional backend i18n key describing the job (admin UI)

	sched *Schedule
}

// DefaultTimeout applies when Job.Timeout is zero.
const DefaultTimeout = 10 * time.Minute

// Cron returns the parsed schedule (nil for manual-only jobs).
func (j Job) Cron() *Schedule { return j.sched }

// EffectiveTimeout is Timeout or DefaultTimeout.
func (j Job) EffectiveTimeout() time.Duration {
	if j.Timeout > 0 {
		return j.Timeout
	}
	return DefaultTimeout
}

// RetryDelay is the back-off before attempt n+1 (n = the attempt that just failed, 1-based).
func (j Job) RetryDelay(n int) time.Duration {
	b := j.Backoff
	if b <= 0 {
		b = time.Minute
	}
	for i := 1; i < n; i++ {
		b *= 2
		if b >= time.Hour {
			return time.Hour
		}
	}
	return b
}

var (
	regMu    sync.RWMutex
	registry = map[string]Job{}
	reName   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,79}$`)
)

// Register adds (or replaces) a job. It panics on an invalid name, schedule or a nil Run, so mistakes
// surface at start-up.
func Register(j Job) {
	if !reName.MatchString(j.Name) {
		panic(fmt.Sprintf("jobs: invalid job name %q", j.Name))
	}
	if j.Run == nil {
		panic("jobs: " + j.Name + ": Run is nil")
	}
	if j.Retries < 0 || j.Retries > 10 {
		panic("jobs: " + j.Name + ": Retries must be 0..10")
	}
	if j.Schedule != "" {
		s, err := ParseCron(j.Schedule)
		if err != nil {
			panic("jobs: " + j.Name + ": " + err.Error())
		}
		j.sched = s
	}
	regMu.Lock()
	registry[j.Name] = j
	regMu.Unlock()
}

// Unregister removes a job (tests).
func Unregister(name string) {
	regMu.Lock()
	delete(registry, name)
	regMu.Unlock()
}

// Lookup returns a registered job.
func Lookup(name string) (Job, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	j, ok := registry[name]
	return j, ok
}

// All returns the registered jobs sorted by name.
func All() []Job {
	regMu.RLock()
	out := make([]Job, 0, len(registry))
	for _, j := range registry {
		out = append(out, j)
	}
	regMu.RUnlock()
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// RunInfo describes one execution (passed to the failure hook).
type RunInfo struct {
	ID       string
	Job      string
	Trigger  string // schedule | catchup | manual | retry
	Attempt  int
	Status   string // success | failed | timeout
	Duration time.Duration
	Instance string
}

// FailureHook is called after the LAST attempt of a run failed (no retries left).
type FailureHook func(ctx context.Context, j Job, run RunInfo, err error)

var hookMu sync.RWMutex
var failureHook FailureHook

// SetFailureHook installs the final-failure hook (internal/monitor does this).
func SetFailureHook(h FailureHook) {
	hookMu.Lock()
	failureHook = h
	hookMu.Unlock()
}

func getFailureHook() FailureHook {
	hookMu.RLock()
	defer hookMu.RUnlock()
	return failureHook
}

// wake pokes the local scheduler(s) (if this process runs one) so queued manual runs start without waiting
// for the next tick.
var wakeMu sync.RWMutex
var wakers = map[*Scheduler]struct{}{}

// Wake nudges every scheduler in this process.
func Wake() {
	wakeMu.RLock()
	defer wakeMu.RUnlock()
	for s := range wakers {
		s.wakeUp()
	}
}
