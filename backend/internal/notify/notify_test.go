package notify_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/i18n"
	"btn-ops/internal/mail"
	"btn-ops/internal/notify"
	"btn-ops/internal/testutil"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMain(m *testing.M) { testutil.Main(m) }

func init() {
	notify.RegisterEvent(notify.Event{Key: "test.plain", Category: notify.CatStock})
	notify.RegisterEvent(notify.Event{Key: "test.mailed", Category: notify.CatStock, DefaultEmail: true})
	notify.RegisterEvent(notify.Event{Key: "test.critical", Category: notify.CatSystem, Critical: true})
}

func plantID(t *testing.T, pool *pgxpool.Pool, code string) string {
	var id string
	if err := pool.QueryRow(context.Background(), `SELECT id FROM "Plant" WHERE code=$1`, code).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func recipients(t *testing.T, pool *pgxpool.Pool, event string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT user_id FROM notification WHERE event=$1`, event)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func ids(us ...*testutil.TestUser) []string {
	var out []string
	for _, u := range us {
		out = append(out, u.ID)
	}
	sort.Strings(out)
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAudienceResolutionWithSiteScope(t *testing.T) {
	pool := testutil.Reset(t)
	ctx := context.Background()
	btn1 := plantID(t, pool, "BTN-1")
	pm := testutil.CreateUser(t, pool, auth.RoleProductionManager)                                           // all sites; no stock.record (SRS §8 PM R A)
	sup1 := testutil.CreateUser(t, pool, auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-1"})             // own site
	sup2 := testutil.CreateUser(t, pool, auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-2"})             // other site
	_ = testutil.CreateUser(t, pool, auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-1", Inactive: true}) // inactive
	stoHQ := testutil.CreateUser(t, pool, auth.RoleStockOfficer)                                             // no plant: all locations
	sto2 := testutil.CreateUser(t, pool, auth.RoleStockOfficer, testutil.UserOpts{Plant: "BTN-2"})           // pinned to BTN-2
	super := testutil.CreateUser(t, pool, auth.RoleSuperAdmin)
	fm := testutil.CreateUser(t, pool, auth.RoleFinanceManager) // no stock.record

	send := func(ev string, to notify.Audience) notify.Result {
		t.Helper()
		r, err := notify.SendResult(ctx, pool, notify.Notification{Event: ev, TitleKey: "notify.category.stock", To: to})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	send("test.plain", notify.ToPermissionAtSite("stock.record", btn1))
	if got, want := recipients(t, pool, "test.plain"), ids(sup1, stoHQ, super); !eq(got, want) {
		t.Fatalf("permission+site: got %v want %v", got, want)
	}
	_ = sup2
	_ = sto2
	_ = fm

	// permission without site: every holder; Except drops the actor
	send("test.mailed", notify.ToPermission("stock.record").Without(pm.ID))
	if got, want := recipients(t, pool, "test.mailed"), ids(sup1, sup2, stoHQ, sto2, super); !eq(got, want) {
		t.Fatalf("permission: got %v want %v", got, want)
	}

	// role at site + explicit users (explicit users are not site-filtered); inactive users never receive
	r := send("test.critical", notify.Audience{Role: auth.RoleSupervisor, PlantID: btn1, UserIDs: []string{fm.ID, sto2.ID}})
	if got, want := recipients(t, pool, "test.critical"), ids(sup1, fm, sto2); !eq(got, want) {
		t.Fatalf("role+site+users: got %v want %v", got, want)
	}
	if r.InApp != 3 || r.Emails != 0 {
		t.Fatalf("result %+v", r)
	}
	// empty audience is a no-op; unknown event is an error
	if r := send("test.plain", notify.Audience{}); r.Recipients != 0 {
		t.Fatal("empty audience")
	}
	if err := notify.Send(ctx, pool, notify.Notification{Event: "test.unknown", TitleKey: "x", To: notify.ToUsers(pm.ID)}); err == nil {
		t.Fatal("unregistered event must fail")
	}
	if err := notify.Send(ctx, pool, notify.Notification{Event: "test.plain", TitleKey: "x", Link: "https://evil", To: notify.ToUsers(pm.ID)}); err == nil {
		t.Fatal("external link must fail")
	}
}

func TestPreferenceResolution(t *testing.T) {
	pool := testutil.Reset(t)
	ctx := context.Background()
	a := testutil.CreateUser(t, pool, auth.RoleGeneralManager, testutil.UserOpts{Language: "rw"})
	b := testutil.CreateUser(t, pool, auth.RoleGeneralManager)
	c := testutil.CreateUser(t, pool, auth.RoleGeneralManager)
	// a: defaults. b: in-app off, email on. c: everything off.
	for ev, v := range map[string][2]bool{"test.plain": {false, true}, "test.mailed": {false, true}, "test.critical": {false, true}} {
		if err := notify.SetPreference(ctx, pool, b.ID, ev, v[0], v[1]); err != nil {
			t.Fatal(err)
		}
		if err := notify.SetPreference(ctx, pool, c.ID, ev, false, false); err != nil {
			t.Fatal(err)
		}
	}
	to := notify.ToUsers(a.ID, b.ID, c.ID)
	n := func(ev string) notify.Result {
		r, err := notify.SendResult(ctx, pool, notify.Notification{Event: ev, TitleKey: "notify.job_failed.title", BodyKey: "notify.job_failed.body",
			Params: map[string]any{"job": "x", "attempts": 1, "status": "failed", "error": "e"}, To: to})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	// plain (no default email): a in-app; b email only; c nothing
	if r := n("test.plain"); r.InApp != 1 || r.Emails != 1 || r.Recipients != 2 {
		t.Fatalf("plain %+v", r)
	}
	if got := recipients(t, pool, "test.plain"); !eq(got, ids(a)) {
		t.Fatalf("plain in-app %v", got)
	}
	// mailed (default email on): a in-app + email; b email
	if r := n("test.mailed"); r.InApp != 1 || r.Emails != 2 {
		t.Fatalf("mailed %+v", r)
	}
	// critical: in-app for everyone (even c who switched it off); email for b only (a: default off, c: off)
	if r := n("test.critical"); r.InApp != 3 || r.Emails != 1 {
		t.Fatalf("critical %+v", r)
	}
	// severity critical on a non-critical event also forces in-app
	if r, _ := notify.SendResult(ctx, pool, notify.Notification{Event: "test.plain", Severity: notify.Critical, TitleKey: "x", To: notify.ToUsers(c.ID)}); r.InApp != 1 {
		t.Fatalf("critical severity %+v", r)
	}
	// preferences API view: critical event keeps in-app on
	prefs, err := notify.Preferences(ctx, pool, c.ID, auth.RoleGeneralManager, i18n.EN)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range prefs {
		if p.Event == "test.critical" && (!p.InApp || !p.Critical) {
			t.Fatalf("critical pref %+v", p)
		}
		if p.Event == "test.plain" && (p.InApp || p.Email) {
			t.Fatalf("plain pref %+v", p)
		}
	}
	// email for a is in Kinyarwanda
	var subj string
	if err := pool.QueryRow(ctx, `SELECT subject FROM email_outbox WHERE user_id=$1 AND event='test.mailed'`, a.ID).Scan(&subj); err != nil {
		t.Fatal(err)
	}
	if subj != i18n.T(i18n.RW, "notify.job_failed.title", "job", "x") {
		t.Fatalf("subject %q", subj)
	}
}

func TestDedupAndReadState(t *testing.T) {
	pool := testutil.Reset(t)
	ctx := context.Background()
	u := testutil.CreateUser(t, pool, auth.RoleGeneralManager)
	n := notify.Notification{Event: "test.mailed", TitleKey: "x", To: notify.ToUsers(u.ID), DedupKey: "material:1"}
	r1, _ := notify.SendResult(ctx, pool, n)
	r2, _ := notify.SendResult(ctx, pool, n)
	if r1.InApp != 1 || r2.InApp != 0 || r2.Emails != 0 {
		t.Fatalf("dedup %+v %+v", r1, r2)
	}
	if c, _ := notify.UnreadCount(ctx, pool, u.ID); c != 1 {
		t.Fatalf("unread %d", c)
	}
	if n, err := notify.MarkAllRead(ctx, pool, u.ID); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if r3, _ := notify.SendResult(ctx, pool, n); r3.InApp != 1 { // once read, the same key notifies again
		t.Fatalf("after read %+v", r3)
	}
	if c, _ := notify.UnreadCount(ctx, pool, u.ID); c != 1 {
		t.Fatalf("unread after resend %d (cache must be invalidated)", c)
	}
}

type flaky struct {
	mu    sync.Mutex
	fail  bool
	sent  []mail.Message
	tries int
}

func (f *flaky) SendMessage(_ context.Context, m mail.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tries++
	if f.fail {
		return errors.New("421 try later")
	}
	f.sent = append(f.sent, m)
	return nil
}

func TestEmailQueueRetryAndGiveUp(t *testing.T) {
	pool := testutil.Reset(t)
	ctx := context.Background()
	u := testutil.CreateUser(t, pool, auth.RoleGeneralManager, testutil.UserOpts{Language: "fr", Name: "Aline"})
	if _, err := notify.SendResult(ctx, pool, notify.Notification{Event: "test.mailed", Severity: notify.Warning, TitleKey: "notify.job_failed.title",
		Params: map[string]any{"job": "j"}, Link: "/stock", To: notify.ToUsers(u.ID)}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	clock := func() time.Time { return now }
	f := &flaky{fail: true}
	st, err := notify.DeliverPending(ctx, pool, f, "https://ops.example", 50, clock)
	if err != nil || st.Retrying != 1 || st.Sent != 0 {
		t.Fatalf("first attempt %+v %v", st, err)
	}
	var attempts int
	var next time.Time
	var status string
	_ = pool.QueryRow(ctx, `SELECT attempts, next_attempt_at, status FROM email_outbox`).Scan(&attempts, &next, &status)
	if attempts != 1 || status != "pending" || next.Sub(now) < 59*time.Second || next.Sub(now) > 61*time.Second {
		t.Fatalf("after 1st failure attempts=%d status=%s next=+%s", attempts, status, next.Sub(now))
	}
	// not due yet: nothing is claimed
	if st, _ := notify.DeliverPending(ctx, pool, f, "", 50, clock); st.Retrying+st.Sent+st.Failed != 0 {
		t.Fatalf("claimed before back-off: %+v", st)
	}
	// keep failing until MaxAttempts -> failed
	for i := 2; i <= notify.MaxAttempts; i++ {
		now = now.Add(notify.RetryDelay(i-1) + time.Second)
		if _, err := notify.DeliverPending(ctx, pool, f, "", 50, clock); err != nil {
			t.Fatal(err)
		}
	}
	_ = pool.QueryRow(ctx, `SELECT attempts, status FROM email_outbox`).Scan(&attempts, &status)
	if attempts != notify.MaxAttempts || status != "failed" {
		t.Fatalf("attempts=%d status=%s", attempts, status)
	}

	// a second email succeeds on retry and is rendered with the recipient's language and links
	if err := notify.QueueEmails(ctx, pool, notify.Email{To: "ops@example.com", Lang: i18n.EN, Event: "test.mailed", Severity: notify.Critical,
		Subject: "S", Body: "B", Link: "/admin/monitoring"}); err != nil {
		t.Fatal(err)
	}
	_, _ = notify.DeliverPending(ctx, pool, f, "https://ops.example", 50, clock)
	f.fail = false
	now = now.Add(2 * time.Minute)
	st, _ = notify.DeliverPending(ctx, pool, f, "https://ops.example/", 50, clock)
	if st.Sent != 1 || len(f.sent) != 1 {
		t.Fatalf("retry success %+v", st)
	}
	m := f.sent[0]
	if !m.Critical || m.ActionURL != "https://ops.example/admin/monitoring" || m.FooterURL != "" || m.FooterNote != i18n.T(i18n.EN, "notify.mail.ops_footer") {
		t.Fatalf("ops message %+v", m)
	}
	_ = pool.QueryRow(ctx, `SELECT status FROM email_outbox WHERE to_email='ops@example.com'`).Scan(&status)
	if status != "sent" {
		t.Fatal(status)
	}
}

func TestUserEmailRendering(t *testing.T) {
	pool := testutil.Reset(t)
	ctx := context.Background()
	u := testutil.CreateUser(t, pool, auth.RoleGeneralManager, testutil.UserOpts{Language: "fr", Name: "Aline"})
	_ = notify.Send(ctx, pool, notify.Notification{Event: "test.mailed", TitleKey: "notify.job_failed.title", BodyKey: "notify.health_alert.body",
		Params: map[string]any{"job": "j", "issue": "Disque"}, Link: "/stock", To: notify.ToUsers(u.ID)})
	f := &flaky{}
	if st, err := notify.DeliverPending(ctx, pool, f, "https://ops.example", 50, nil); err != nil || st.Sent != 1 {
		t.Fatal(st, err)
	}
	m := f.sent[0]
	if m.Lang != i18n.FR || m.Name != "Aline" || m.Subject != i18n.T(i18n.FR, "notify.job_failed.title", "job", "j") ||
		m.ActionURL != "https://ops.example/stock" || m.FooterURL != "https://ops.example/notifications/preferences" {
		t.Fatalf("message %+v", m)
	}
	// the real mailer renders it with the branded layout
	mm := mail.New(testutil.Config(t), nil)
	subject, text, html := mm.BuildMessage(ctx, m)
	if subject != m.Subject || !strings.Contains(text, "https://ops.example/stock") || !strings.Contains(html, "cid:btnlogo") || !strings.Contains(html, "Aline") {
		t.Fatalf("render: %s\n%s", text, html)
	}
}
