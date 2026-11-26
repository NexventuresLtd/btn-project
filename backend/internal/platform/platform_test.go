package platform_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/config"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/jobs"
	"btn-ops/internal/monitor"
	"btn-ops/internal/notify"
	"btn-ops/internal/testutil"

	"github.com/gofiber/fiber/v2"
)

func TestMain(m *testing.M) { testutil.Main(m) }

func init() {
	notify.RegisterEvent(notify.Event{Key: "ptest.event", Category: notify.CatStock, DefaultEmail: true})
	notify.RegisterEvent(notify.Event{Key: "ptest.finance_only", Category: notify.CatFinance, Permissions: []auth.Permission{"payments.record"}})
}

// env mounts test-only routes that panic / fail so error capture can be exercised.
func env(t *testing.T) *apptest.Env {
	return apptest.New(t, apptest.Options{
		Config: func(c *config.Config) { c.OpsAlertEmail = "ops@example.com" },
		Extra: func(api fiber.Router, d *deps.Deps) {
			api.Get("/ptest/panic/:id", func(c *fiber.Ctx) error { panic(fmt.Sprintf("nil map for %s", c.Params("id"))) })
			api.Get("/ptest/fail", func(c *fiber.Ctx) error { return errors.New("pq: connection reset by peer") })
			api.Get("/ptest/notimpl", func(c *fiber.Ctx) error { return httpx.New(501, "not_implemented", "x") })
			api.Get("/ptest/forbidden", func(c *fiber.Ctx) error { panic(httpx.Forbidden()) })
		},
	})
}

func status(t *testing.T, r *http.Response, want int) {
	t.Helper()
	if r.StatusCode != want {
		b := make([]byte, 400)
		n, _ := r.Body.Read(b)
		t.Fatalf("status %d want %d: %s", r.StatusCode, want, b[:n])
	}
}

func TestNotificationsAPI(t *testing.T) {
	e := env(t)
	ctx := context.Background()
	gm := e.User(auth.RoleGeneralManager)
	other := e.User(auth.RoleGeneralManager)
	for i := 0; i < 3; i++ {
		if err := notify.Send(ctx, e.Pool, notify.Notification{Event: "ptest.event", Severity: notify.Warning, TitleKey: "notify.job_failed.title",
			BodyKey: "notify.job_failed.body", Params: map[string]any{"job": fmt.Sprint("j", i), "attempts": 1, "status": "failed", "error": "x"},
			Link: "/stock", To: notify.ToUsers(gm.ID)}); err != nil {
			t.Fatal(err)
		}
	}
	_ = notify.Send(ctx, e.Pool, notify.Notification{Event: "ptest.event", TitleKey: "x", To: notify.ToUsers(other.ID)})

	// anonymous -> 401
	status(t, e.Do("GET", "/api/notifications", nil, nil), 401)
	status(t, e.Do("GET", "/api/notifications/unread-count", nil, nil), 401)

	var cnt struct{ Count int }
	r := e.Do("GET", "/api/notifications/unread-count", nil, gm)
	status(t, r, 200)
	etag := r.Header.Get("ETag")
	e.Decode(r, &cnt)
	if cnt.Count != 3 || etag == "" {
		t.Fatalf("unread %d etag %q", cnt.Count, etag)
	}
	status(t, e.Do("GET", "/api/notifications/unread-count", nil, gm, "If-None-Match", etag), 304)

	var list struct {
		Items []struct {
			ID, Title, Body, Severity, Category string
			Link                                *string
			ReadAt                              *time.Time
		}
		Total, Unread int
	}
	e.Decode(e.Do("GET", "/api/notifications?lang=fr&pageSize=2", nil, gm), &list)
	if list.Total != 3 || len(list.Items) != 2 || list.Unread != 3 || list.Items[0].Title != "Échec de la tâche : j2" || list.Items[0].Category != "stock" || *list.Items[0].Link != "/stock" {
		t.Fatalf("list %+v", list)
	}
	first := list.Items[0].ID

	// someone else's notification -> 404
	status(t, e.Do("POST", "/api/notifications/"+first+"/read", nil, other), 404)
	var rr struct{ Unread int }
	r = e.Do("POST", "/api/notifications/"+first+"/read", nil, gm)
	status(t, r, 200)
	e.Decode(r, &rr)
	if rr.Unread != 2 {
		t.Fatalf("after read unread=%d", rr.Unread)
	}
	e.Decode(e.Do("GET", "/api/notifications?unread=1", nil, gm), &list)
	if list.Total != 2 {
		t.Fatalf("unread filter total %d", list.Total)
	}
	// CSRF header is required for mutations
	req := e.Do("POST", "/api/notifications/read-all", nil, gm, "X-Requested-With", "")
	status(t, req, 403)
	r = e.Do("POST", "/api/notifications/read-all", nil, gm)
	status(t, r, 200)
	var ra struct{ Updated int }
	e.Decode(r, &ra)
	if ra.Updated != 2 {
		t.Fatalf("read-all updated %d", ra.Updated)
	}
	e.Decode(e.Do("GET", "/api/notifications/unread-count", nil, gm), &cnt)
	if cnt.Count != 0 {
		t.Fatalf("unread after read-all %d", cnt.Count)
	}
	e.Decode(e.Do("GET", "/api/notifications/unread-count", nil, other), &cnt)
	if cnt.Count != 1 {
		t.Fatal("other user's notifications must be untouched")
	}
}

func TestPreferencesAPI(t *testing.T) {
	e := env(t)
	gm := e.User(auth.RoleGeneralManager)
	fm := e.User(auth.RoleFinanceManager)
	sa := e.User(auth.RoleSystemAdmin)
	type pref struct {
		Event, Label, Category, CategoryLabel string
		Critical, InApp, Email, DefaultEmail  bool
	}
	var out struct{ Items []pref }
	find := func(items []pref, ev string) *pref {
		for i := range items {
			if items[i].Event == ev {
				return &items[i]
			}
		}
		return nil
	}
	status(t, e.Do("GET", "/api/notifications/preferences", nil, nil), 401)
	e.Decode(e.Do("GET", "/api/notifications/preferences", nil, gm), &out)
	if p := find(out.Items, "ptest.event"); p == nil || !p.InApp || !p.Email || p.CategoryLabel != "Stock & transfers" {
		t.Fatalf("gm prefs %+v", p)
	}
	if find(out.Items, "ptest.finance_only") != nil || find(out.Items, monitor.EvJobFailed) != nil {
		t.Fatal("events gated by permission must be hidden")
	}
	e.Decode(e.Do("GET", "/api/notifications/preferences", nil, fm), &out)
	if find(out.Items, "ptest.finance_only") == nil {
		t.Fatal("finance event visible to FM")
	}
	e.Decode(e.Do("GET", "/api/notifications/preferences?lang=am", nil, sa), &out)
	if p := find(out.Items, monitor.EvJobFailed); p == nil || !p.Critical || p.Label == "Background job failed" {
		t.Fatalf("SA critical event (localized) %+v", p)
	}

	// update: critical in-app cannot be switched off
	r := e.Do("PUT", "/api/notifications/preferences", map[string]any{"preferences": []map[string]any{
		{"event": monitor.EvJobFailed, "inApp": false, "email": false},
	}}, sa)
	status(t, r, 200)
	e.Decode(r, &out)
	if p := find(out.Items, monitor.EvJobFailed); !p.InApp || p.Email {
		t.Fatalf("critical after update %+v", p)
	}
	r = e.Do("PUT", "/api/notifications/preferences", map[string]any{"preferences": []map[string]any{
		{"event": "ptest.event", "inApp": false, "email": true},
	}}, gm)
	status(t, r, 200)
	e.Decode(r, &out)
	if p := find(out.Items, "ptest.event"); p.InApp || !p.Email {
		t.Fatalf("after update %+v", p)
	}
	var audits int
	_ = e.Pool.QueryRow(context.Background(), `SELECT count(*) FROM "AuditLog" WHERE action='notifications.preferences' AND "userId"=$1`, gm.ID).Scan(&audits)
	if audits != 1 {
		t.Fatalf("audit rows %d", audits)
	}
	// validation: unknown / hidden event, wrong types, missing array
	code, _, fields := e.ErrorOf(e.Do("PUT", "/api/notifications/preferences", map[string]any{"preferences": []map[string]any{{"event": "nope.x", "inApp": true, "email": true}}}, gm))
	if code != "bad_request" || fields["preferences.0.event"] == "" {
		t.Fatalf("unknown event: %s %v", code, fields)
	}
	_, _, fields = e.ErrorOf(e.Do("PUT", "/api/notifications/preferences", map[string]any{"preferences": []map[string]any{{"event": "ptest.finance_only", "inApp": true, "email": true}}}, gm))
	if fields["preferences.0.event"] == "" {
		t.Fatalf("hidden event accepted: %v", fields)
	}
	_, _, fields = e.ErrorOf(e.Do("PUT", "/api/notifications/preferences", map[string]any{"preferences": []map[string]any{{"event": "ptest.event", "inApp": "yes"}}}, gm))
	if fields["preferences.0.inApp"] == "" || fields["preferences.0.email"] == "" {
		t.Fatalf("type errors: %v", fields)
	}
	status(t, e.Do("PUT", "/api/notifications/preferences", map[string]any{}, gm), 400)
}

func TestJobsAdminAPI(t *testing.T) {
	e := env(t)
	ctx := context.Background()
	sa := e.User(auth.RoleSystemAdmin)
	super := e.User(auth.RoleSuperAdmin)
	pm := e.User(auth.RoleProductionManager)

	// permission denials
	status(t, e.Do("GET", "/api/ops/jobs", nil, nil), 401)
	status(t, e.Do("GET", "/api/ops/jobs", nil, pm), 403)
	status(t, e.Do("POST", "/api/ops/jobs/notify.email/run", nil, pm), 403)
	status(t, e.Do("GET", "/api/ops/jobs/notify.email/runs", nil, pm), 403)

	var list struct {
		Items []struct {
			Name, Schedule, Description string
			NextRunAt                   *time.Time
			RecentRuns                  []jobs.Run
		}
		Scheduler struct {
			Alive    bool
			Timezone string
		}
	}
	r := e.Do("GET", "/api/ops/jobs?lang=rw", nil, sa) // System Admin passes (not blocked by the super-admin /admin guard)
	status(t, r, 200)
	e.Decode(r, &list)
	names := map[string]bool{}
	for _, j := range list.Items {
		names[j.Name] = true
		if j.Schedule != "" && j.NextRunAt == nil {
			t.Errorf("%s: no next run", j.Name)
		}
		if j.Name == "notify.email" && j.Description == "" {
			t.Error("description missing")
		}
	}
	for _, want := range []string{"notify.email", "retention.cleanup", "monitor.selfcheck", "monitor.health_digest"} {
		if !names[want] {
			t.Errorf("built-in job %s not registered", want)
		}
	}
	if list.Scheduler.Alive || list.Scheduler.Timezone != "Africa/Kigali" {
		t.Fatalf("scheduler %+v (no scheduler runs in tests)", list.Scheduler)
	}
	status(t, e.Do("GET", "/api/ops/jobs", nil, super), 200)

	// run now: 202 queued + audit; second click -> 409; unknown -> 404
	r = e.Do("POST", "/api/ops/jobs/retention.cleanup/run", nil, sa)
	status(t, r, 202)
	var run jobs.Run
	e.Decode(r, &run)
	if run.Status != "queued" || run.Trigger != "manual" || run.TriggeredBy == nil || run.TriggeredBy.ID != sa.ID {
		t.Fatalf("run %+v", run)
	}
	status(t, e.Do("POST", "/api/ops/jobs/retention.cleanup/run", nil, sa), 409)
	status(t, e.Do("POST", "/api/ops/jobs/nope.job/run", nil, sa), 404)
	status(t, e.Do("GET", "/api/ops/jobs/nope.job/runs", nil, sa), 404)
	var audits int
	_ = e.Pool.QueryRow(ctx, `SELECT count(*) FROM "AuditLog" WHERE action='jobs.run' AND "entityId"='retention.cleanup'`).Scan(&audits)
	if audits != 1 {
		t.Fatalf("audit %d", audits)
	}

	// a scheduler (as cmd/server runs it) executes the queued run
	s := jobs.NewScheduler(e.Pool, jobs.Options{Instance: "test", LockKey: time.Now().UnixNano()})
	defer s.Shutdown()
	s.Tick(ctx)
	s.Wait()
	var page struct {
		Items []jobs.Run
		Total int
	}
	e.Decode(e.Do("GET", "/api/ops/jobs/retention.cleanup/runs", nil, sa), &page)
	if page.Total != 1 || page.Items[0].Status != "success" || page.Items[0].DurationMs == nil {
		t.Fatalf("runs %+v", page)
	}
	e.Decode(e.Do("GET", "/api/ops/jobs", nil, sa), &list)
	if !list.Scheduler.Alive {
		t.Fatal("heartbeat not visible")
	}
}

func TestErrorCaptureAndMonitoringAPI(t *testing.T) {
	e := env(t)
	ctx := context.Background()
	sa := e.User(auth.RoleSystemAdmin)
	gm := e.User(auth.RoleGeneralManager)

	// panics -> 500 envelope, recorded once with a count, request id kept
	r := e.Do("GET", "/api/ptest/panic/abc", nil, gm)
	status(t, r, 500)
	rid := r.Header.Get("X-Request-Id")
	code, _, _ := e.ErrorOf(r)
	if code != "internal" || rid == "" {
		t.Fatalf("panic response code=%s rid=%q", code, rid)
	}
	status(t, e.Do("GET", "/api/ptest/panic/xyz", nil, gm), 500)
	status(t, e.Do("GET", "/api/ptest/fail", nil, gm), 500)
	status(t, e.Do("GET", "/api/ptest/notimpl", nil, gm), 501)   // not captured
	status(t, e.Do("GET", "/api/ptest/forbidden", nil, gm), 403) // panic(httpx error) keeps its status, not captured

	var n int
	_ = e.Pool.QueryRow(ctx, `SELECT count(*) FROM error_event`).Scan(&n)
	if n != 2 {
		t.Fatalf("error events %d, want 2 (panic deduplicated + 5xx)", n)
	}
	var cnt int
	var stack, reqID, uid, route string
	_ = e.Pool.QueryRow(ctx, `SELECT count, stack, last_request_id, last_user_id, route FROM error_event WHERE kind='panic'`).Scan(&cnt, &stack, &reqID, &uid, &route)
	if cnt != 2 || stack == "" || uid != gm.ID || route != "/api/ptest/panic/:id" || reqID == "" {
		t.Fatalf("panic event count=%d uid=%s route=%s rid=%s", cnt, uid, route, reqID)
	}

	// alerting: SA (admin.monitor) got one in-app critical notification per fingerprint (rate limited),
	// GM got none; ops email queued once per fingerprint
	var saN, gmN, ops int
	_ = e.Pool.QueryRow(ctx, `SELECT count(*) FROM notification WHERE user_id=$1 AND event=$2`, sa.ID, monitor.EvCriticalError).Scan(&saN)
	_ = e.Pool.QueryRow(ctx, `SELECT count(*) FROM notification WHERE user_id=$1`, gm.ID).Scan(&gmN)
	_ = e.Pool.QueryRow(ctx, `SELECT count(*) FROM email_outbox WHERE to_email='ops@example.com' AND user_id IS NULL`).Scan(&ops)
	if saN != 2 || gmN != 0 || ops != 2 {
		t.Fatalf("alerts sa=%d gm=%d ops=%d", saN, gmN, ops)
	}

	// monitoring API: denials
	status(t, e.Do("GET", "/api/ops/monitoring/health", nil, gm), 403)
	status(t, e.Do("GET", "/api/ops/monitoring/errors", nil, gm), 403)
	status(t, e.Do("GET", "/api/ops/monitoring/errors", nil, nil), 401)

	var h monitor.Health
	r = e.Do("GET", "/api/ops/monitoring/health?fresh=1", nil, sa)
	status(t, r, 200)
	e.Decode(r, &h)
	if !h.Database.OK || h.Database.Pool.Max == 0 || h.App.Version == "" || h.Errors.Open != 2 || h.Errors.Occurrences24h != 3 ||
		h.Errors.ServerErrors24 < 3 || h.Disk.Path == "" || h.Email.Pending < 2 || h.Status == "ok" {
		t.Fatalf("health %+v", h)
	}

	var list struct {
		Items []monitor.ErrorRow
		Total int
	}
	e.Decode(e.Do("GET", "/api/ops/monitoring/errors", nil, sa), &list)
	if list.Total != 2 {
		t.Fatalf("open errors %d", list.Total)
	}
	var panicID string
	for _, it := range list.Items {
		if it.Kind == "panic" {
			panicID = it.ID
			if it.Count != 2 || it.Count24h != 2 || it.State != "open" {
				t.Fatalf("row %+v", it)
			}
		}
	}
	e.Decode(e.Do("GET", "/api/ops/monitoring/errors?kind=http&q=connection", nil, sa), &list)
	if list.Total != 1 {
		t.Fatalf("filter %d", list.Total)
	}
	status(t, e.Do("GET", "/api/ops/monitoring/errors?status=weird", nil, sa), 400)

	var det monitor.ErrorRow
	e.Decode(e.Do("GET", "/api/ops/monitoring/errors/"+panicID, nil, sa), &det)
	if det.Stack == nil || len(det.Occurrences) != 2 || det.LastUser == nil || det.LastUser.ID != gm.ID {
		t.Fatalf("detail %+v", det)
	}
	status(t, e.Do("GET", "/api/ops/monitoring/errors/nope", nil, sa), 404)

	// mute -> listed under muted; no alert while muted
	status(t, e.Do("POST", "/api/ops/monitoring/errors/"+panicID+"/mute", map[string]any{"hours": 9999}, sa), 400)
	r = e.Do("POST", "/api/ops/monitoring/errors/"+panicID+"/mute", map[string]any{"hours": 24}, sa)
	status(t, r, 200)
	e.Decode(r, &det)
	if det.State != "muted" || det.MutedUntil == nil {
		t.Fatalf("muted %+v", det)
	}
	e.Decode(e.Do("GET", "/api/ops/monitoring/errors?status=muted", nil, sa), &list)
	if list.Total != 1 {
		t.Fatal("muted list")
	}
	status(t, e.Do("POST", "/api/ops/monitoring/errors/"+panicID+"/mute", map[string]any{"hours": 0}, sa), 200)

	// resolve -> resolved list; happens again -> reopened automatically
	r = e.Do("POST", "/api/ops/monitoring/errors/"+panicID+"/resolve", nil, sa)
	status(t, r, 200)
	e.Decode(r, &det)
	if det.State != "resolved" || det.ResolvedBy == nil || det.ResolvedBy.ID != sa.ID {
		t.Fatalf("resolved %+v", det)
	}
	status(t, e.Do("POST", "/api/ops/monitoring/errors/"+panicID+"/resolve", nil, gm), 403)
	e.Decode(e.Do("GET", "/api/ops/monitoring/errors?status=resolved", nil, sa), &list)
	if list.Total != 1 {
		t.Fatal("resolved list")
	}
	status(t, e.Do("GET", "/api/ptest/panic/again", nil, gm), 500)
	e.Decode(e.Do("GET", "/api/ops/monitoring/errors/"+panicID, nil, sa), &det)
	if det.State != "open" || det.Count != 3 {
		t.Fatalf("reopened %+v", det)
	}
	status(t, e.Do("POST", "/api/ops/monitoring/errors/"+panicID+"/resolve", nil, sa), 200)
	status(t, e.Do("POST", "/api/ops/monitoring/errors/"+panicID+"/reopen", nil, sa), 200)
	var audits int
	_ = e.Pool.QueryRow(ctx, `SELECT count(*) FROM "AuditLog" WHERE entity='ErrorEvent'`).Scan(&audits)
	if audits != 5 { // mute, unmute, resolve, resolve, reopen
		t.Fatalf("audits %d", audits)
	}
}

func TestJobFailureAlertAndBuiltins(t *testing.T) {
	e := env(t)
	ctx := context.Background()
	sa := e.User(auth.RoleSystemAdmin)
	jobs.Register(jobs.Job{Name: "ptest.broken", Run: func(context.Context) error { return errors.New("upstream 503") }})
	defer jobs.Unregister("ptest.broken")
	s := jobs.NewScheduler(e.Pool, jobs.Options{Instance: "test", LockKey: time.Now().UnixNano()})
	defer s.Shutdown()
	for i := 0; i < 2; i++ {
		if _, err := jobs.Enqueue(ctx, e.Pool, "ptest.broken", ""); err != nil {
			t.Fatal(err)
		}
		s.Tick(ctx)
		s.Wait()
	}
	var n, ev, ops int
	_ = e.Pool.QueryRow(ctx, `SELECT count(*) FROM notification WHERE user_id=$1 AND event=$2`, sa.ID, monitor.EvJobFailed).Scan(&n)
	_ = e.Pool.QueryRow(ctx, `SELECT count FROM error_event WHERE kind='job' AND route='ptest.broken'`).Scan(&ev)
	_ = e.Pool.QueryRow(ctx, `SELECT count(*) FROM email_outbox WHERE to_email='ops@example.com' AND event=$1`, monitor.EvJobFailed).Scan(&ops)
	if n != 1 || ev != 2 || ops != 1 {
		t.Fatalf("job failure: notifications=%d errorCount=%d ops=%d (second failure must be rate limited)", n, ev, ops)
	}

	// email job delivers the queued alert emails through the mailer (Noop in tests)
	if _, err := jobs.Enqueue(ctx, e.Pool, "notify.email", ""); err != nil {
		t.Fatal(err)
	}
	s.Tick(ctx)
	s.Wait()
	msgs := e.Mail.SentMessages()
	if len(msgs) < 2 { // ops address + the SA (critical event, email on by default)
		t.Fatalf("sent %d", len(msgs))
	}

	// retention cleanup removes expired challenges / old attempts but keeps recent ones
	_, _ = e.Pool.Exec(ctx, `INSERT INTO "AuthAttempt" (id, key, kind, success, "createdAt") VALUES ('old','k','login',false, now() - interval '200 days'), ('new','k','login',false, now())`)
	_, _ = e.Pool.Exec(ctx, `INSERT INTO passkey_challenge (id, kind, session_data, expires_at) VALUES ('pc1','login','{}', now() - interval '2 hours')`)
	out, err := monitor.Cleanup(ctx, e.Deps, time.Now())
	if err != nil || out["AuthAttempt"] != 1 || out["passkey_challenge"] != 1 {
		t.Fatalf("cleanup %v %v", out, err)
	}

	// daily digest: something failed -> sent once per day
	sent, err := monitor.Digest(ctx, e.Deps, time.Now())
	if err != nil || !sent {
		t.Fatalf("digest %v %v", sent, err)
	}
	if sent, _ := monitor.Digest(ctx, e.Deps, time.Now()); sent {
		t.Fatal("digest must be rate limited to once a day")
	}
}

// The admin APIs live under /api/ops (not /api/admin, which branding locks to Super Admins): a System
// Administrator (admin.monitor, not Super Admin) must reach every one of them.
func TestOpsEndpointsReachableBySystemAdmin(t *testing.T) {
	e := env(t)
	sa := e.User(auth.RoleSystemAdmin)
	gm := e.User(auth.RoleGeneralManager)
	status(t, e.Do("GET", "/api/ptest/fail", nil, gm), 500) // one captured error to act on
	var id string
	_ = e.Pool.QueryRow(context.Background(), `SELECT id FROM error_event LIMIT 1`).Scan(&id)
	for _, c := range []struct {
		method, path string
		body         any
		want         int
	}{
		{"GET", "/api/ops/jobs", nil, 200},
		{"GET", "/api/ops/jobs/notify.email/runs", nil, 200},
		{"POST", "/api/ops/jobs/notify.email/run", nil, 202},
		{"GET", "/api/ops/monitoring/health", nil, 200},
		{"GET", "/api/ops/monitoring/errors", nil, 200},
		{"GET", "/api/ops/monitoring/errors/" + id, nil, 200},
		{"POST", "/api/ops/monitoring/errors/" + id + "/mute", map[string]any{"hours": 1}, 200},
		{"POST", "/api/ops/monitoring/errors/" + id + "/resolve", nil, 200},
		{"POST", "/api/ops/monitoring/errors/" + id + "/reopen", nil, 200},
	} {
		if r := e.Do(c.method, c.path, c.body, sa); r.StatusCode != c.want {
			t.Errorf("SYSTEM_ADMIN %s %s = %d, want %d", c.method, c.path, r.StatusCode, c.want)
		}
		if r := e.Do(c.method, c.path, c.body, gm); r.StatusCode != 403 {
			t.Errorf("GM %s %s = %d, want 403", c.method, c.path, r.StatusCode)
		}
	}
}
