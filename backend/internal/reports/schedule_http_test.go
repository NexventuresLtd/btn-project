package reports_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/deps"
	"btn-ops/internal/mail"
	"btn-ops/internal/reports"
	"btn-ops/internal/testutil"
)

type schedules struct {
	Items []struct {
		reports.Schedule
		Title      string
		Recipients []reports.Recipient
		LastRun    *reports.RunOut
		NextRunAt  *string
	}
	Options struct{ Formats, Languages []string }
}

func validBody(extra map[string]any) map[string]any {
	b := map[string]any{"enabled": true, "frequency": "daily", "sendTime": "06:30", "dayOfWeek": 1, "dayOfMonth": 1, "plantId": nil,
		"language": "", "formats": []string{"pdf", "xlsx"}, "recipientUserIds": []string{}, "recipientRoles": []string{"PRODUCTION_MANAGER"},
		"recipientEmails": []string{"ops@example.com"}}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

func TestScheduleAdminEndpoints(t *testing.T) {
	e := apptest.New(t)
	gm := e.User(auth.RoleSystemAdmin) // reports.schedule: System Administrator (SRS §8 settings)
	pm := e.User(auth.RoleProductionManager)
	for _, p := range []string{"/api/reports/schedules", "/api/reports/runs", "/api/reports/schedules/recipients"} {
		if r := e.Do("GET", p, nil, pm); r.StatusCode != 403 {
			t.Fatalf("pm %s %d", p, r.StatusCode)
		}
		if r := e.Do("GET", p, nil, nil); r.StatusCode != 401 {
			t.Fatalf("anon %s %d", p, r.StatusCode)
		}
	}
	if r := e.Do("PUT", "/api/reports/schedules/daily_production", validBody(nil), pm); r.StatusCode != 403 {
		t.Fatalf("pm put %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/reports/schedules/daily_production/send", nil, pm); r.StatusCode != 403 {
		t.Fatalf("pm send %d", r.StatusCode)
	}
	var list schedules
	e.Decode(e.Do("GET", "/api/reports/schedules", nil, gm), &list)
	if len(list.Items) != len(reports.Defs) || list.Items[0].Key != "daily_production" || !list.Items[0].Enabled || list.Items[0].Title == "" {
		t.Fatalf("list %+v", list.Items)
	}
	// validation
	code, _, f := e.ErrorOf(e.Do("PUT", "/api/reports/schedules/daily_production", validBody(map[string]any{"frequency": "monthly", "sendTime": "25:00",
		"formats": []string{"doc"}, "recipientEmails": []string{"bad"}, "recipientRoles": []string{"NOPE"}}), gm))
	if code == "" || f["frequency"] == "" || f["sendTime"] == "" || f["formats.0"] == "" || f["recipientEmails.0"] == "" || f["recipientRoles.0"] == "" {
		t.Fatalf("validation %s %v", code, f)
	}
	_, _, f = e.ErrorOf(e.Do("PUT", "/api/reports/schedules/daily_production", validBody(map[string]any{"recipientRoles": []string{}, "recipientEmails": []string{}}), gm))
	if f["recipientRoles"] == "" {
		t.Fatalf("enabled without recipients %v", f)
	}
	if r := e.Do("PUT", "/api/reports/schedules/nope", validBody(nil), gm); r.StatusCode != 404 {
		t.Fatalf("unknown %d", r.StatusCode)
	}
	// save → audited; enabling never fires the slot that already passed (schedules ship enabled, §10: an
	// administrator switched this one off first)
	if _, err := e.Pool.Exec(context.Background(), `UPDATE report_schedule SET enabled = false WHERE key = 'daily_production'`); err != nil {
		t.Fatal(err)
	}
	r := e.Do("PUT", "/api/reports/schedules/daily_production", validBody(nil), gm)
	var saved struct {
		reports.Schedule
		Recipients []reports.Recipient
		NextRunAt  *string
	}
	e.Decode(r, &saved)
	if r.StatusCode != 200 || !saved.Enabled || saved.LastSlotAt == nil || saved.NextRunAt == nil || len(saved.Recipients) != 2 {
		t.Fatalf("saved %d %+v", r.StatusCode, saved)
	}
	var n int
	_ = e.Pool.QueryRow(context.Background(), `SELECT count(*) FROM "AuditLog" WHERE action = 'reports.schedule_update' AND "entityId" = 'daily_production'`).Scan(&n)
	if n != 1 {
		t.Fatalf("audit %d", n)
	}
	if err := reports.Dispatch(context.Background(), e.Deps, time.Now()); err != nil {
		t.Fatal(err)
	}
	_ = e.Pool.QueryRow(context.Background(), `SELECT count(*) FROM report_run WHERE schedule_key = 'daily_production'`).Scan(&n)
	if n != 0 {
		t.Fatalf("enabling sent an old slot: %d runs", n)
	}
	var opts struct{ Items []struct{ ID, Email string } }
	e.Decode(e.Do("GET", "/api/reports/schedules/recipients?q="+pm.Email[:4], nil, gm), &opts)
	if len(opts.Items) == 0 {
		t.Fatal("recipient options")
	}
}

type fakeSender struct {
	mu   sync.Mutex
	fail int // fail the first n sends
	sent []mail.AttachedMessage
}

func (f *fakeSender) SendWithAttachments(_ context.Context, m mail.Message, files []mail.Attachment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail > 0 {
		f.fail--
		return errors.New("smtp: 451 try again later")
	}
	f.sent = append(f.sent, mail.AttachedMessage{Message: m, Attachments: files})
	return nil
}

func withSender(t *testing.T, s mail.AttachmentSender) {
	prev := reports.SenderFor
	reports.SenderFor = func(*deps.Deps) mail.AttachmentSender { return s }
	t.Cleanup(func() { reports.SenderFor = prev })
}

func TestSendNowEmailsAttachmentsPerRecipient(t *testing.T) {
	e := apptest.New(t)
	gm := e.User(auth.RoleSystemAdmin) // reports.schedule: System Administrator (SRS §8 settings)
	pm := e.User(auth.RoleProductionManager, testutil.UserOpts{Language: "fr"})
	sup := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-2"})
	sys := e.User(auth.RoleSalesManager) // no production.view (SRS §8 Sales –)
	e.User(auth.RoleSuperAdmin)          // identity for raw e-mail recipients
	fake := &fakeSender{}
	withSender(t, fake)
	body := validBody(map[string]any{"enabled": false, "recipientUserIds": []string{pm.ID, sup.ID, sys.ID}, "recipientRoles": []string{}})
	if r := e.Do("PUT", "/api/reports/schedules/daily_production", body, gm); r.StatusCode != 200 {
		t.Fatalf("put %d", r.StatusCode)
	}
	r := e.Do("POST", "/api/reports/schedules/daily_production/send", map[string]any{"from": "2026-11-20", "to": "2026-11-22"}, gm)
	var q struct{ Run reports.RunOut }
	e.Decode(r, &q)
	if r.StatusCode != 202 || q.Run.Status != "queued" || q.Run.Trigger != "manual" || q.Run.PeriodFrom != "2026-11-20" {
		t.Fatalf("send %d %+v", r.StatusCode, q.Run)
	}
	if err := reports.RunDue(context.Background(), e.Deps, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(fake.sent) != 3 { // pm, supervisor, ops@example.com — the sales manager lacks production.view
		t.Fatalf("sent %d", len(fake.sent))
	}
	byTo := map[string]mail.AttachedMessage{}
	for _, m := range fake.sent {
		byTo[m.Message.To] = m
	}
	m := byTo[pm.Email]
	if len(m.Attachments) != 2 || !strings.HasSuffix(m.Attachments[0].Name, ".pdf") || !strings.HasPrefix(string(m.Attachments[0].Data), "%PDF") ||
		!strings.HasSuffix(m.Attachments[1].Name, ".xlsx") || m.Attachments[1].ContentType != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Fatalf("pm attachments %+v", names(m.Attachments))
	}
	if !strings.Contains(m.Message.Subject, "20/11/2026") || m.Message.Lang != "fr" || !strings.Contains(strings.Join(m.Message.Paragraphs, " "), "Arrêt des données") {
		t.Fatalf("pm message %+v", m.Message)
	}
	if s := byTo[sup.Email]; !strings.HasPrefix(s.Attachments[0].Name, "BTN-2") {
		t.Fatalf("supervisor gets own site %v", names(s.Attachments))
	}
	if _, ok := byTo["ops@example.com"]; !ok {
		t.Fatal("raw e-mail recipient")
	}
	var runs struct {
		Items []reports.RunOut
		Total int
	}
	e.Decode(e.Do("GET", "/api/reports/runs?key=daily_production", nil, gm), &runs)
	if runs.Total != 1 || runs.Items[0].Status != "success" || len(runs.Items[0].Attachments) < 2 {
		t.Fatalf("runs %+v", runs)
	}
	skipped := 0
	for _, x := range runs.Items[0].Recipients {
		if x.Status == "skipped" && x.Reason == "no_permission" {
			skipped++
		}
	}
	if skipped != 1 {
		t.Fatalf("recipients %+v", runs.Items[0].Recipients)
	}
	var one struct{ Run reports.RunOut }
	if r := e.Do("GET", "/api/reports/runs/"+runs.Items[0].ID, nil, gm); r.StatusCode != 200 {
		t.Fatalf("run %d", r.StatusCode)
	} else {
		e.Decode(r, &one)
	}
	// the in-process render sessions are revoked
	var open int
	_ = e.Pool.QueryRow(context.Background(), `SELECT count(*) FROM auth_session WHERE user_agent = 'btn-ops/scheduled-report' AND revoked_at IS NULL`).Scan(&open)
	if open != 0 {
		t.Fatalf("%d render sessions still open", open)
	}
}

func TestScheduledRunRetriesThenFails(t *testing.T) {
	e := apptest.New(t)
	gm := e.User(auth.RoleSystemAdmin) // reports.schedule: System Administrator (SRS §8 settings)
	e.User(auth.RoleSuperAdmin)
	// the other schedules ship enabled (§10); keep this run the only one
	if _, err := e.Pool.Exec(context.Background(), `UPDATE report_schedule SET enabled = false`); err != nil {
		t.Fatal(err)
	}
	fake := &fakeSender{fail: 1}
	withSender(t, fake)
	body := validBody(map[string]any{"enabled": false, "recipientRoles": []string{}, "recipientEmails": []string{"ops@example.com"}, "formats": []string{"csv"}})
	if r := e.Do("PUT", "/api/reports/schedules/site_performance", body, gm); r.StatusCode != 400 {
		// site_performance is monthly only
		t.Fatalf("daily site performance %d", r.StatusCode)
	}
	body["frequency"] = "monthly"
	if r := e.Do("PUT", "/api/reports/schedules/site_performance", body, gm); r.StatusCode != 200 {
		t.Fatalf("put %d", r.StatusCode)
	}
	e.Do("POST", "/api/reports/schedules/site_performance/send", nil, gm)
	now := time.Now()
	if err := reports.RunDue(context.Background(), e.Deps, now); err != nil {
		t.Fatalf("first failure is retried, not final: %v", err)
	}
	var status string
	var attempts int
	_ = e.Pool.QueryRow(context.Background(), `SELECT status, attempts FROM report_run WHERE schedule_key = 'site_performance'`).Scan(&status, &attempts)
	if status != "retrying" || attempts != 1 {
		t.Fatalf("after failure %s %d", status, attempts)
	}
	// not due yet
	_ = reports.RunDue(context.Background(), e.Deps, now.Add(time.Minute))
	if len(fake.sent) != 0 {
		t.Fatal("retried before back-off")
	}
	if err := reports.RunDue(context.Background(), e.Deps, now.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	_ = e.Pool.QueryRow(context.Background(), `SELECT status, attempts FROM report_run WHERE schedule_key = 'site_performance'`).Scan(&status, &attempts)
	if status != "success" || attempts != 2 || len(fake.sent) != 1 || len(fake.sent[0].Attachments) != 2 { // hub CSV + management CSV
		t.Fatalf("after retry %s %d %d", status, attempts, len(fake.sent))
	}
	// permanent failure: the last attempt fails the job (F1 alerts)
	fake.fail = 10
	e.Do("POST", "/api/reports/schedules/site_performance/send", nil, gm)
	var err error
	for i := 0; i < reports.MaxAttempts; i++ {
		err = reports.RunDue(context.Background(), e.Deps, now.Add(time.Duration(i+1)*time.Hour))
	}
	if err == nil || !strings.Contains(err.Error(), "site_performance") {
		t.Fatalf("final failure %v", err)
	}
	_ = e.Pool.QueryRow(context.Background(), `SELECT status FROM report_run WHERE schedule_key = 'site_performance' ORDER BY created_at DESC LIMIT 1`).Scan(&status)
	if status != "failed" {
		t.Fatalf("status %s", status)
	}
}

func TestScheduleDispatchCreatesOneRunPerSlot(t *testing.T) {
	e := apptest.New(t)
	e.User(auth.RoleSuperAdmin)
	fake := &fakeSender{}
	withSender(t, fake)
	if err := reports.EnsureSchedules(context.Background(), e.Pool); err != nil {
		t.Fatal(err)
	}
	exec(t, e, `UPDATE report_schedule SET enabled = true, send_time = '06:30', recipient_emails = '{ops@example.com}', last_slot_at = NULL WHERE key = 'daily_stock'`)
	now := kgl(t, "2026-11-23 06:31")
	for i := 0; i < 2; i++ {
		if err := reports.Dispatch(context.Background(), e.Deps, now); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	var from string
	_ = e.Pool.QueryRow(context.Background(), `SELECT count(*), min(period_from)::text FROM report_run WHERE schedule_key = 'daily_stock'`).Scan(&n, &from)
	if n != 1 || from != "2026-11-22" {
		t.Fatalf("runs %d %s", n, from)
	}
	if err := reports.ProcessQueued(context.Background(), e.Deps, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(fake.sent) != 1 || len(fake.sent[0].Attachments) != 4 { // stock + materials position, PDF + Excel
		t.Fatalf("sent %+v", fake.sent)
	}
	// the job is registered with the F1 runner
	if _, ok := reportsJob(); !ok {
		t.Fatal("job not registered")
	}
}

func names(a []mail.Attachment) []string {
	out := []string{}
	for _, x := range a {
		out = append(out, x.Name)
	}
	return out
}

func kgl(t *testing.T, s string) time.Time {
	loc, _ := time.LoadLocation("Africa/Kigali")
	v, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func reportsJob() (any, bool) { j, ok := jobsLookup(reports.JobName); return j, ok }

// SRS §10: the scheduled reports ship enabled, with the frequencies and recipients of the §10 table (the order and
// sales summary both weekly and monthly; finance receives stock movement & variance).
func TestScheduleDefaultsFollowSRS10(t *testing.T) {
	e := apptest.New(t)
	var list schedules
	e.Decode(e.Do("GET", "/api/reports/schedules", nil, e.User(auth.RoleSystemAdmin)), &list)
	by := map[string]reports.Schedule{}
	for _, it := range list.Items {
		by[it.Key] = it.Schedule
		if !it.Enabled {
			t.Errorf("%s ships disabled", it.Key)
		}
	}
	want := map[string]struct {
		freq  string
		roles []string
	}{
		"daily_production":        {"daily", []string{"PRODUCTION_MANAGER", "MANAGING_DIRECTOR", "GENERAL_MANAGER", "OPERATIONS_ADVISOR"}},
		"daily_stock":             {"daily", []string{"PRODUCTION_MANAGER", "STOCK_OFFICER"}},
		"daily_headcount":         {"daily", []string{"PRODUCTION_MANAGER", "FINANCE_MANAGER"}},
		"orders_sales":            {"weekly", []string{"SALES_MANAGER", "MANAGING_DIRECTOR", "GENERAL_MANAGER", "OPERATIONS_ADVISOR"}},
		"orders_sales_monthly":    {"monthly", []string{"SALES_MANAGER", "MANAGING_DIRECTOR", "GENERAL_MANAGER", "OPERATIONS_ADVISOR"}},
		"stock_movement_variance": {"weekly", []string{"PRODUCTION_MANAGER", "FINANCE_MANAGER"}},
		"procurement_expense":     {"monthly", []string{"FINANCE_MANAGER", "MANAGING_DIRECTOR", "GENERAL_MANAGER", "OPERATIONS_ADVISOR"}},
		"wage_workforce":          {"wage_period", []string{"FINANCE_MANAGER", "PRODUCTION_MANAGER"}},
		"budget_monitoring":       {"monthly", []string{"FINANCE_MANAGER", "MANAGING_DIRECTOR", "GENERAL_MANAGER", "OPERATIONS_ADVISOR"}},
		"site_performance":        {"monthly", []string{"MANAGING_DIRECTOR", "GENERAL_MANAGER", "OPERATIONS_ADVISOR"}},
	}
	for k, w := range want {
		s, ok := by[k]
		if !ok {
			t.Errorf("%s missing", k)
			continue
		}
		got := append([]string{}, s.RecipientRoles...)
		exp := append([]string{}, w.roles...)
		slices.Sort(got)
		slices.Sort(exp)
		if s.Frequency != w.freq || !slices.Equal(got, exp) {
			t.Errorf("%s: %s %v, want %s %v", k, s.Frequency, s.RecipientRoles, w.freq, w.roles)
		}
	}
}
