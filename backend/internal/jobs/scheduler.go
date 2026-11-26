package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultLockKey is the pg advisory lock id held by the leader ("BTNJOBS" in ASCII).
const DefaultLockKey int64 = 0x42544e4a4f4253

// HeartbeatJob is the job_state row the leader refreshes (health: "is a scheduler alive?").
const HeartbeatJob = "__scheduler__"

// Options configure a Scheduler.
type Options struct {
	Instance      string        // shown in job_run.instance (default hostname:pid)
	Tick          time.Duration // how often due jobs / queued runs are checked (default 5 s)
	LockKey       int64         // advisory lock id (default DefaultLockKey; tests use their own)
	ShutdownGrace time.Duration // how long Run waits for running jobs after ctx is cancelled (default 30 s)
	Now           func() time.Time
}

// Scheduler runs registered jobs on the instance that holds the advisory lock.
type Scheduler struct {
	pool *pgxpool.Pool
	opt  Options

	mu        sync.Mutex
	lockConn  *pgx.Conn
	running   map[string]bool
	state     map[string]time.Time // job -> last handled slot (loaded on leadership)
	lastBeat  time.Time
	wg        sync.WaitGroup
	runCtx    context.Context
	runCancel context.CancelFunc
	wake      chan struct{}
}

// NewScheduler builds a scheduler; call Run (blocking) or Tick (tests).
func NewScheduler(pool *pgxpool.Pool, opt Options) *Scheduler {
	if opt.Instance == "" {
		h, _ := os.Hostname()
		opt.Instance = fmt.Sprintf("%s:%d", h, os.Getpid())
	}
	if opt.Tick <= 0 {
		opt.Tick = 5 * time.Second
	}
	if opt.LockKey == 0 {
		opt.LockKey = DefaultLockKey
	}
	if opt.ShutdownGrace <= 0 {
		opt.ShutdownGrace = 30 * time.Second
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	s := &Scheduler{pool: pool, opt: opt, running: map[string]bool{}, wake: make(chan struct{}, 1)}
	s.runCtx, s.runCancel = context.WithCancel(context.Background())
	wakeMu.Lock()
	wakers[s] = struct{}{}
	wakeMu.Unlock()
	return s
}

func (s *Scheduler) now() time.Time { return s.opt.Now().UTC().Truncate(time.Millisecond) }

func (s *Scheduler) wakeUp() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Instance is this scheduler's instance name.
func (s *Scheduler) Instance() string { return s.opt.Instance }

// IsLeader reports whether this scheduler currently holds the advisory lock.
func (s *Scheduler) IsLeader() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lockConn != nil
}

// Run ticks until ctx is cancelled, then waits up to ShutdownGrace for running jobs (cancelling them
// afterwards) and releases the lock.
func (s *Scheduler) Run(ctx context.Context) {
	slog.Info("job scheduler started", "instance", s.opt.Instance, "jobs", len(All()))
	t := time.NewTicker(s.opt.Tick)
	defer t.Stop()
	s.Tick(ctx)
	for {
		select {
		case <-ctx.Done():
			s.Shutdown()
			return
		case <-t.C:
			s.Tick(ctx)
		case <-s.wake:
			s.Tick(ctx)
		}
	}
}

// Shutdown waits for running jobs (up to ShutdownGrace), cancels stragglers and releases the lock.
func (s *Scheduler) Shutdown() {
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(s.opt.ShutdownGrace):
		slog.Warn("job scheduler: cancelling jobs still running after grace period")
		s.runCancel()
		<-done
	}
	s.runCancel()
	s.release()
	wakeMu.Lock()
	delete(wakers, s)
	wakeMu.Unlock()
	slog.Info("job scheduler stopped", "instance", s.opt.Instance)
}

// Wait blocks until every job started by this scheduler has finished (tests).
func (s *Scheduler) Wait() { s.wg.Wait() }

func (s *Scheduler) release() {
	s.mu.Lock()
	c := s.lockConn
	s.lockConn = nil
	s.mu.Unlock()
	if c != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = c.Exec(ctx, `SELECT pg_advisory_unlock($1)`, s.opt.LockKey)
		_ = c.Close(ctx)
	}
}

// ensureLeader keeps (or tries to take) the advisory lock on a dedicated connection.
func (s *Scheduler) ensureLeader(ctx context.Context) bool {
	s.mu.Lock()
	c := s.lockConn
	s.mu.Unlock()
	if c != nil {
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := c.Ping(pctx)
		cancel()
		if err == nil {
			return true
		}
		slog.Warn("job scheduler lost its lock connection", "err", err)
		s.mu.Lock()
		s.lockConn = nil
		s.mu.Unlock()
		_ = c.Close(context.Background())
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(cctx, s.pool.Config().ConnConfig.Copy())
	if err != nil {
		slog.Warn("job scheduler: cannot open lock connection", "err", err)
		return false
	}
	var ok bool
	if err := conn.QueryRow(cctx, `SELECT pg_try_advisory_lock($1)`, s.opt.LockKey).Scan(&ok); err != nil || !ok {
		_ = conn.Close(context.Background())
		return false
	}
	s.mu.Lock()
	s.lockConn = conn
	s.state = nil // reload from the database
	s.mu.Unlock()
	slog.Info("job scheduler acquired leadership", "instance", s.opt.Instance)
	s.recoverOrphans(ctx)
	return true
}

// recoverOrphans marks runs left 'running' by a previous leader as failed (only the leader runs jobs).
func (s *Scheduler) recoverOrphans(ctx context.Context) {
	s.mu.Lock()
	busy := make([]string, 0, len(s.running)) // never nil: NULL would make "job = ANY(NULL)" filter everything
	for n := range s.running {
		busy = append(busy, n)
	}
	s.mu.Unlock()
	now := s.now()
	_, err := s.pool.Exec(ctx, `UPDATE job_run SET status='failed', finished_at=$1, error='interrupted: the instance running it stopped before it finished',
		duration_ms = CASE WHEN started_at IS NULL THEN NULL ELSE (EXTRACT(EPOCH FROM ($1 - started_at)) * 1000)::int END
		WHERE status='running' AND NOT (job = ANY($2))`, now, busy)
	if err != nil {
		slog.Warn("job scheduler: orphan recovery", "err", err)
	}
}

// Tick is one scheduler iteration: keep leadership, heartbeat, start due jobs and queued runs.
func (s *Scheduler) Tick(ctx context.Context) {
	if ctx.Err() != nil || !s.ensureLeader(ctx) {
		return
	}
	now := s.now()
	if now.Sub(s.lastBeat) >= 30*time.Second {
		s.lastBeat = now
		_, _ = s.pool.Exec(ctx, `INSERT INTO job_state (job, last_slot, instance, updated_at) VALUES ($1, $2, $3, $2)
			ON CONFLICT (job) DO UPDATE SET last_slot = EXCLUDED.last_slot, instance = EXCLUDED.instance, updated_at = EXCLUDED.updated_at`,
			HeartbeatJob, now, s.opt.Instance)
	}
	if err := s.loadState(ctx); err != nil {
		slog.Warn("job scheduler: load state", "err", err)
		return
	}
	for _, j := range All() {
		if j.sched == nil {
			continue
		}
		s.scheduleDue(ctx, j, now)
	}
	s.claimQueued(ctx, now)
}

func (s *Scheduler) loadState(ctx context.Context) error {
	s.mu.Lock()
	loaded := s.state != nil
	s.mu.Unlock()
	if loaded {
		return nil
	}
	rows, err := s.pool.Query(ctx, `SELECT job, last_slot FROM job_state WHERE job <> $1 AND last_slot IS NOT NULL`, HeartbeatJob)
	if err != nil {
		return err
	}
	defer rows.Close()
	st := map[string]time.Time{}
	for rows.Next() {
		var name string
		var t time.Time
		if err := rows.Scan(&name, &t); err != nil {
			return err
		}
		st[name] = t.UTC()
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.state = st
	s.mu.Unlock()
	return nil
}

func (s *Scheduler) saveSlot(ctx context.Context, name string, slot time.Time) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO job_state (job, last_slot, instance, updated_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (job) DO UPDATE SET last_slot = EXCLUDED.last_slot, instance = EXCLUDED.instance, updated_at = EXCLUDED.updated_at`,
		name, slot.UTC(), s.opt.Instance, s.now())
	if err == nil {
		s.mu.Lock()
		s.state[name] = slot.UTC()
		s.mu.Unlock()
	}
	return err
}

// scheduleDue applies the schedule and the catch-up policy: whatever number of slots were missed, the
// job runs once (trigger "catchup" when the run is late or more than one slot was missed).
func (s *Scheduler) scheduleDue(ctx context.Context, j Job, now time.Time) {
	s.mu.Lock()
	last, seen := s.state[j.Name]
	s.mu.Unlock()
	if !seen {
		// first time this job is seen: start counting from now (no immediate run)
		if err := s.saveSlot(ctx, j.Name, now); err != nil {
			slog.Warn("job scheduler: init state", "job", j.Name, "err", err)
		}
		return
	}
	slot, missed := j.sched.LatestAtOrBefore(last, now)
	if missed == 0 {
		return
	}
	trigger := "schedule"
	if missed > 1 || now.Sub(slot) > 2*s.opt.Tick+time.Minute {
		trigger = "catchup"
	}
	if err := s.saveSlot(ctx, j.Name, slot); err != nil {
		slog.Warn("job scheduler: save slot", "job", j.Name, "err", err)
		return
	}
	if !s.markRunning(j.Name) {
		slog.Warn("job scheduler: previous run still in progress, slot skipped", "job", j.Name, "slot", slot)
		return
	}
	var id string
	err := s.pool.QueryRow(ctx, `INSERT INTO job_run (job, trigger, status, attempt, scheduled_for, started_at, instance)
		VALUES ($1, $2, 'running', 1, $3, $4, $5) RETURNING id`, j.Name, trigger, slot.UTC(), now, s.opt.Instance).Scan(&id)
	if err != nil {
		s.unmark(j.Name)
		slog.Warn("job scheduler: insert run", "job", j.Name, "err", err)
		return
	}
	s.start(j, RunInfo{ID: id, Job: j.Name, Trigger: trigger, Attempt: 1, Instance: s.opt.Instance})
}

func (s *Scheduler) markRunning(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[name] {
		return false
	}
	s.running[name] = true
	return true
}

func (s *Scheduler) unmark(name string) {
	s.mu.Lock()
	delete(s.running, name)
	s.mu.Unlock()
}

// claimQueued starts queued manual/retry runs whose time has come (one per job at a time).
func (s *Scheduler) claimQueued(ctx context.Context, now time.Time) {
	for i := 0; i < 20; i++ {
		s.mu.Lock()
		busy := make([]string, 0, len(s.running))
		for n := range s.running {
			busy = append(busy, n)
		}
		s.mu.Unlock()
		var ri RunInfo
		err := s.pool.QueryRow(ctx, `UPDATE job_run SET status='running', started_at=$1, instance=$2
			WHERE id = (SELECT id FROM job_run WHERE status='queued' AND scheduled_for <= $1 AND NOT (job = ANY($3))
			            ORDER BY scheduled_for, created_at LIMIT 1 FOR UPDATE SKIP LOCKED)
			RETURNING id, job, trigger, attempt`, now, s.opt.Instance, busy).Scan(&ri.ID, &ri.Job, &ri.Trigger, &ri.Attempt)
		if errors.Is(err, pgx.ErrNoRows) {
			return
		}
		if err != nil {
			slog.Warn("job scheduler: claim queued", "err", err)
			return
		}
		ri.Instance = s.opt.Instance
		j, ok := Lookup(ri.Job)
		if !ok {
			_, _ = s.pool.Exec(ctx, `UPDATE job_run SET status='failed', finished_at=$2, duration_ms=0, error='unknown job (not registered on this instance)' WHERE id=$1`, ri.ID, now)
			continue
		}
		if !s.markRunning(j.Name) {
			_, _ = s.pool.Exec(ctx, `UPDATE job_run SET status='queued', started_at=NULL, instance=NULL WHERE id=$1`, ri.ID)
			continue
		}
		s.start(j, ri)
	}
}

func (s *Scheduler) start(j Job, ri RunInfo) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.unmark(j.Name)
		s.execute(j, ri)
	}()
}

// execute runs one attempt with the job timeout, records the outcome, then queues a retry or calls the
// failure hook.
func (s *Scheduler) execute(j Job, ri RunInfo) {
	ctx, cancel := context.WithTimeout(s.runCtx, j.EffectiveTimeout())
	defer cancel()
	start := time.Now()
	err := safeRun(ctx, j.Run)
	ri.Duration = time.Since(start)
	ri.Status = "success"
	var errText any
	if err != nil {
		ri.Status = "failed"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			ri.Status = "timeout"
			err = fmt.Errorf("timed out after %s: %w", j.EffectiveTimeout(), err)
		}
		errText = truncate(err.Error(), 4000)
	}
	wctx, wcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer wcancel()
	if _, uerr := s.pool.Exec(wctx, `UPDATE job_run SET status=$2, finished_at=$3, duration_ms=$4, error=$5 WHERE id=$1`,
		ri.ID, ri.Status, s.now(), int(ri.Duration.Milliseconds()), errText); uerr != nil {
		slog.Error("job scheduler: record run", "job", j.Name, "err", uerr)
	}
	if err == nil {
		slog.Info("job finished", "job", j.Name, "trigger", ri.Trigger, "ms", ri.Duration.Milliseconds())
		return
	}
	slog.Error("job failed", "job", j.Name, "trigger", ri.Trigger, "attempt", ri.Attempt, "status", ri.Status, "err", err)
	if ri.Attempt <= j.Retries && s.runCtx.Err() == nil {
		at := s.now().Add(j.RetryDelay(ri.Attempt))
		if _, qerr := s.pool.Exec(wctx, `INSERT INTO job_run (job, trigger, status, attempt, scheduled_for) VALUES ($1, 'retry', 'queued', $2, $3)`,
			j.Name, ri.Attempt+1, at); qerr != nil {
			slog.Error("job scheduler: queue retry", "job", j.Name, "err", qerr)
		}
		return
	}
	if h := getFailureHook(); h != nil {
		func() {
			defer func() {
				if p := recover(); p != nil {
					slog.Error("job failure hook panicked", "panic", p)
				}
			}()
			h(wctx, j, ri, err)
		}()
	}
}

// PanicError is returned by a job that panicked.
type PanicError struct {
	Value any
	Stack string
}

func (p *PanicError) Error() string { return fmt.Sprintf("panic: %v", p.Value) }

func safeRun(ctx context.Context, fn func(context.Context) error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = &PanicError{Value: p, Stack: TrimStack(string(debug.Stack()))}
		}
	}()
	return fn(ctx)
}

// TrimStack keeps the first goroutine's frames that belong to this module (and the panic site), at most
// 40 lines, so stored stacks stay readable.
func TrimStack(st string) string {
	lines := strings.Split(st, "\n")
	out := make([]string, 0, 40)
	for i := 0; i < len(lines) && len(out) < 40; i++ {
		l := lines[i]
		if strings.Contains(l, "runtime/debug.Stack") || strings.Contains(l, "runtime/debug/stack.go") {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
