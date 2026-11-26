package jobs

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"btn-ops/internal/testutil"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMain(m *testing.M) { testutil.Main(m) }

func noop(context.Context) error { return nil }

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newClock(t time.Time) *clock { return &clock{t: t} }

// each test uses its own advisory lock id: test processes share the database
func lockKey() int64 { return rand.Int64N(1<<40) + 1_000_000 }

func sched(t *testing.T, pool *pgxpool.Pool, name string, key int64, c *clock) *Scheduler {
	s := NewScheduler(pool, Options{Instance: name, LockKey: key, Tick: time.Second, Now: c.Now, ShutdownGrace: 5 * time.Second})
	t.Cleanup(s.Shutdown)
	return s
}

func register(t *testing.T, j Job) {
	Register(j)
	t.Cleanup(func() { Unregister(j.Name) })
}

type runRow struct {
	Trigger, Status string
	Attempt         int
	Instance        *string
	Error           *string
}

func runsOf(t *testing.T, pool *pgxpool.Pool, job string) []runRow {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT trigger, status, attempt, instance, error FROM job_run WHERE job=$1 ORDER BY created_at, attempt`, job)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []runRow
	for rows.Next() {
		var r runRow
		if err := rows.Scan(&r.Trigger, &r.Status, &r.Attempt, &r.Instance, &r.Error); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func setSlot(t *testing.T, pool *pgxpool.Pool, job string, at time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `INSERT INTO job_state (job, last_slot) VALUES ($1,$2)
		ON CONFLICT (job) DO UPDATE SET last_slot = EXCLUDED.last_slot`, job, at.UTC()); err != nil {
		t.Fatal(err)
	}
}

func TestSingleRunnerAdvisoryLock(t *testing.T) {
	pool := testutil.Reset(t)
	ctx := context.Background()
	var runs atomic.Int32
	register(t, Job{Name: "t.lock", Schedule: "* * * * *", Run: func(context.Context) error { runs.Add(1); return nil }})
	c := newClock(time.Now().UTC().Truncate(time.Minute).Add(30 * time.Second))
	key := lockKey()
	a := sched(t, pool, "inst-a", key, c)
	b := sched(t, pool, "inst-b", key, c)
	a.Tick(ctx)
	b.Tick(ctx)
	if a.IsLeader() == b.IsLeader() {
		t.Fatalf("exactly one leader expected: a=%v b=%v", a.IsLeader(), b.IsLeader())
	}
	leader, follower := a, b
	if b.IsLeader() {
		leader, follower = b, a
	}
	c.Add(time.Minute) // next minute slot is due
	for i := 0; i < 3; i++ {
		a.Tick(ctx)
		b.Tick(ctx)
	}
	a.Wait()
	b.Wait()
	if n := runs.Load(); n != 1 {
		t.Fatalf("job ran %d times, want exactly 1", n)
	}
	rs := runsOf(t, pool, "t.lock")
	if len(rs) != 1 || rs[0].Status != "success" || rs[0].Trigger != "schedule" || *rs[0].Instance != leader.Instance() {
		t.Fatalf("runs %+v", rs)
	}
	// leader stops -> the other instance takes over
	leader.Shutdown()
	follower.Tick(ctx)
	if !follower.IsLeader() {
		t.Fatal("follower did not take over after the leader released the lock")
	}
	c.Add(time.Minute)
	follower.Tick(ctx)
	follower.Wait()
	if n := runs.Load(); n != 2 {
		t.Fatalf("after failover ran %d times, want 2", n)
	}
}

func TestFirstSightDoesNotRunAndCatchUpRunsOnce(t *testing.T) {
	pool := testutil.Reset(t)
	ctx := context.Background()
	var runs atomic.Int32
	register(t, Job{Name: "t.catchup", Schedule: "0 * * * *", Run: func(context.Context) error { runs.Add(1); return nil }})
	c := newClock(time.Date(2026, 11, 23, 9, 30, 0, 0, time.UTC))
	s := sched(t, pool, "inst", lockKey(), c)
	s.Tick(ctx)
	s.Wait()
	if runs.Load() != 0 {
		t.Fatal("a newly seen job must not run immediately")
	}
	// the server was "down" for 5 hours: 5 hourly slots were missed
	setSlot(t, pool, "t.catchup", c.Now().Add(-5*time.Hour))
	s2 := sched(t, pool, "inst2", lockKey(), c) // fresh scheduler reloads state from the DB
	s2.Tick(ctx)
	s2.Tick(ctx)
	s2.Wait()
	if runs.Load() != 1 {
		t.Fatalf("catch-up ran %d times, want once", runs.Load())
	}
	rs := runsOf(t, pool, "t.catchup")
	if len(rs) != 1 || rs[0].Trigger != "catchup" {
		t.Fatalf("runs %+v", rs)
	}
	var slot time.Time
	_ = pool.QueryRow(ctx, `SELECT last_slot FROM job_state WHERE job='t.catchup'`).Scan(&slot)
	if want := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC); !slot.Equal(want) { // 11:00 Kigali
		t.Fatalf("last slot %s want %s", slot, want)
	}
	// on time: the next slot fires with trigger "schedule"
	c.Add(30*time.Minute + time.Second)
	s2.Tick(ctx)
	s2.Wait()
	rs = runsOf(t, pool, "t.catchup")
	if len(rs) != 2 || rs[1].Trigger != "schedule" {
		t.Fatalf("runs %+v", rs)
	}
}

func TestRetryWithBackoffThenSuccess(t *testing.T) {
	pool := testutil.Reset(t)
	ctx := context.Background()
	var calls atomic.Int32
	var hooked atomic.Int32
	SetFailureHook(func(context.Context, Job, RunInfo, error) { hooked.Add(1) })
	t.Cleanup(func() { SetFailureHook(nil) })
	register(t, Job{Name: "t.retry", Retries: 2, Backoff: time.Minute, Run: func(context.Context) error {
		if calls.Add(1) == 1 {
			return errors.New("smtp down")
		}
		return nil
	}})
	c := newClock(time.Now().UTC().Add(time.Minute))
	s := sched(t, pool, "inst", lockKey(), c)
	if _, err := Enqueue(ctx, pool, "t.retry", ""); err != nil {
		t.Fatal(err)
	}
	s.Tick(ctx)
	s.Wait()
	s.Tick(ctx) // retry not due yet (1 min back-off)
	s.Wait()
	if calls.Load() != 1 {
		t.Fatalf("calls %d before back-off elapsed", calls.Load())
	}
	c.Add(61 * time.Second)
	s.Tick(ctx)
	s.Wait()
	rs := runsOf(t, pool, "t.retry")
	if len(rs) != 2 || rs[0].Status != "failed" || rs[0].Trigger != "manual" || *rs[0].Error != "smtp down" ||
		rs[1].Status != "success" || rs[1].Trigger != "retry" || rs[1].Attempt != 2 {
		t.Fatalf("runs %+v", rs)
	}
	if hooked.Load() != 0 {
		t.Fatal("failure hook must not fire when a retry succeeded")
	}
}

func TestTimeoutAndFinalFailureHook(t *testing.T) {
	pool := testutil.Reset(t)
	ctx := context.Background()
	type hookCall struct {
		run RunInfo
		err error
	}
	got := make(chan hookCall, 2)
	SetFailureHook(func(_ context.Context, _ Job, r RunInfo, err error) { got <- hookCall{r, err} })
	t.Cleanup(func() { SetFailureHook(nil) })
	register(t, Job{Name: "t.slow", Timeout: 50 * time.Millisecond, Run: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}})
	register(t, Job{Name: "t.panic", Run: func(context.Context) error { panic("boom") }})
	c := newClock(time.Now().UTC().Add(time.Minute))
	s := sched(t, pool, "inst", lockKey(), c)
	_, _ = Enqueue(ctx, pool, "t.slow", "")
	_, _ = Enqueue(ctx, pool, "t.panic", "")
	s.Tick(ctx)
	s.Wait()
	if rs := runsOf(t, pool, "t.slow"); len(rs) != 1 || rs[0].Status != "timeout" {
		t.Fatalf("slow runs %+v", rs)
	}
	if rs := runsOf(t, pool, "t.panic"); len(rs) != 1 || rs[0].Status != "failed" || *rs[0].Error != "panic: boom" {
		t.Fatalf("panic runs %+v", rs)
	}
	seen := map[string]hookCall{}
	for i := 0; i < 2; i++ {
		select {
		case h := <-got:
			seen[h.run.Job] = h
		case <-time.After(5 * time.Second):
			t.Fatal("failure hook not called")
		}
	}
	if seen["t.slow"].run.Status != "timeout" {
		t.Fatalf("slow hook %+v", seen["t.slow"])
	}
	var pe *PanicError
	if !errors.As(seen["t.panic"].err, &pe) || pe.Stack == "" {
		t.Fatalf("panic hook err %v", seen["t.panic"].err)
	}
}

func TestNoOverlapAndOrphanRecovery(t *testing.T) {
	pool := testutil.Reset(t)
	ctx := context.Background()
	release := make(chan struct{})
	var calls atomic.Int32
	register(t, Job{Name: "t.block", Run: func(ctx context.Context) error {
		calls.Add(1)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}})
	// a run left "running" by a crashed instance
	if _, err := pool.Exec(ctx, `INSERT INTO job_run (job, trigger, status, started_at) VALUES ('t.block','schedule','running', now())`); err != nil {
		t.Fatal(err)
	}
	c := newClock(time.Now().UTC().Add(time.Minute))
	s := sched(t, pool, "inst", lockKey(), c)
	_, _ = Enqueue(ctx, pool, "t.block", "")
	_, _ = Enqueue(ctx, pool, "t.block", "")
	s.Tick(ctx)
	s.Tick(ctx)
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("overlapping runs: %d", calls.Load())
	}
	close(release)
	s.Wait()
	s.Tick(ctx)
	s.Wait()
	if calls.Load() != 2 {
		t.Fatalf("second queued run not started: %d", calls.Load())
	}
	rs := runsOf(t, pool, "t.block")
	if len(rs) != 3 || rs[0].Status != "failed" || rs[0].Error == nil || rs[1].Status != "success" || rs[2].Status != "success" {
		t.Fatalf("runs %+v", rs)
	}
}

func TestEnqueueUnknown(t *testing.T) {
	pool := testutil.Reset(t)
	if _, err := Enqueue(context.Background(), pool, "nope.job", ""); !errors.Is(err, ErrUnknownJob) {
		t.Fatalf("err %v", err)
	}
}
