package monitor_test

import (
	"context"
	"testing"
	"time"

	"btn-ops/internal/monitor"
	"btn-ops/internal/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

func TestNormalizeAndFingerprint(t *testing.T) {
	a := monitor.Event{Kind: "http", Method: "GET", Route: "/api/sales/:id", Status: 500,
		Message: `ERROR: deadlock detected on "Sale" id=cabcdefghijklmnopqrstuvwx (pid 4123) 0x7f3a`}
	b := a
	b.Message = `ERROR: deadlock detected on "Payment" id=czzzzzzzzzzzzzzzzzzzzzzzz (pid 99) 0x1`
	b.Path, b.RequestID, b.UserID = "/api/sales/other", "rid-2", "u2"
	if monitor.Fingerprint(a) != monitor.Fingerprint(b) {
		t.Fatalf("volatile parts must not change the fingerprint:\n%s\n%s", monitor.Normalize(a.Message), monitor.Normalize(b.Message))
	}
	c := a
	c.Route = "/api/stock/:id"
	if monitor.Fingerprint(a) == monitor.Fingerprint(c) {
		t.Fatal("different routes must differ")
	}
	d := a
	d.Message = "connection refused"
	if monitor.Fingerprint(a) == monitor.Fingerprint(d) {
		t.Fatal("different messages must differ")
	}
	// stack signature: same app frames with different line numbers/args -> same; other frames -> different
	p1 := monitor.Event{Kind: "panic", Route: "/api/x", Status: 500, Message: "boom",
		Stack: "goroutine 1 [running]:\nbtn-ops/internal/sales.(*handler).get(0xc000123)\n\t/src/sales.go:10 +0x1\n"}
	p2 := p1
	p2.Stack = "goroutine 9 [running]:\nbtn-ops/internal/sales.(*handler).get(0xc999)\n\t/src/sales.go:99 +0x7\n"
	p3 := p1
	p3.Stack = "goroutine 1 [running]:\nbtn-ops/internal/stock.list(0xc000123)\n\t/src/stock.go:10 +0x1\n"
	if monitor.Fingerprint(p1) != monitor.Fingerprint(p2) || monitor.Fingerprint(p1) == monitor.Fingerprint(p3) {
		t.Fatal("stack signature")
	}
	if got := monitor.Normalize(`user "x" 12 failed at 3.5s 1b4e28ba-2fa1-11d2-883f-0016d3cca427`); got != "user <s> <n> failed at <n>s <uuid>" {
		t.Fatal(got)
	}
}

func TestRecordDeduplicatesAndReopens(t *testing.T) {
	pool := testutil.Reset(t)
	ctx := context.Background()
	e := monitor.Event{Kind: "http", Method: "POST", Route: "/api/x", Status: 500, Message: "db timeout after 30s", RequestID: "r1"}
	r1, err := monitor.Record(ctx, pool, e)
	if err != nil || !r1.New || r1.Count != 1 {
		t.Fatal(r1, err)
	}
	e.Message, e.RequestID = "db timeout after 31s", "r2"
	r2, _ := monitor.Record(ctx, pool, e)
	if r2.ID != r1.ID || r2.New || r2.Count != 2 {
		t.Fatalf("dedup %+v", r2)
	}
	_, _ = pool.Exec(ctx, `UPDATE error_event SET resolved_at = now() WHERE id=$1`, r1.ID)
	r3, _ := monitor.Record(ctx, pool, e)
	if !r3.Reopened || r3.Count != 3 {
		t.Fatalf("reopen %+v", r3)
	}
	var open bool
	var occ int
	_ = pool.QueryRow(ctx, `SELECT resolved_at IS NULL, (SELECT count(*) FROM error_occurrence WHERE event_id=$1) FROM error_event WHERE id=$1`, r1.ID).Scan(&open, &occ)
	if !open || occ != 3 {
		t.Fatalf("open=%v occurrences=%d", open, occ)
	}
	_, _ = pool.Exec(ctx, `UPDATE error_event SET muted_until = now() + interval '1 hour' WHERE id=$1`, r1.ID)
	if r4, _ := monitor.Record(ctx, pool, e); !r4.Muted {
		t.Fatal("muted flag")
	}
}

func TestAlertThrottle(t *testing.T) {
	pool := testutil.Reset(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	ok := func(key string, at time.Time) bool {
		t.Helper()
		v, err := monitor.Allow(ctx, pool, key, time.Hour, at)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if !ok("a", now) || ok("a", now.Add(10*time.Minute)) || ok("a", now.Add(59*time.Minute)) {
		t.Fatal("per-key window")
	}
	var supp int
	_ = pool.QueryRow(ctx, `SELECT suppressed FROM alert_throttle WHERE key='a'`).Scan(&supp)
	if supp != 2 {
		t.Fatalf("suppressed %d", supp)
	}
	if !ok("a", now.Add(61*time.Minute)) {
		t.Fatal("window elapsed")
	}
	// global cap per clock hour across keys (a@08:00 and a@09:01 are in different hours)
	later := now.Add(3 * time.Hour)
	allowed := 0
	for i := 0; i < monitor.GlobalAlertsPerHour+5; i++ {
		if ok("k"+string(rune('a'+i)), later) {
			allowed++
		}
	}
	if allowed != monitor.GlobalAlertsPerHour {
		t.Fatalf("global cap: allowed %d", allowed)
	}
}
