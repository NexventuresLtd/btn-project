package reports

import (
	"testing"
	"time"

	"btn-ops/internal/dates"
)

func kgl(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, dates.Kigali())
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func iso(t time.Time) string { return dates.ToISO(t) }

func TestDailySlotAndPeriod(t *testing.T) {
	s := Schedule{Key: "daily_production", Enabled: true, Frequency: FreqDaily, SendTime: "06:30"}
	if got := LatestSlot(s, kgl("2026-11-23 06:29")); !got.Equal(kgl("2026-11-22 06:30")) {
		t.Fatalf("before time: %v", got)
	}
	slot := LatestSlot(s, kgl("2026-11-23 06:30"))
	if !slot.Equal(kgl("2026-11-23 06:30")) {
		t.Fatalf("at time: %v", slot)
	}
	if f, to := PeriodFor(s, slot); iso(f) != "2026-11-22" || iso(to) != "2026-11-22" {
		t.Fatalf("morning send covers yesterday: %s %s", iso(f), iso(to))
	}
	ev := Schedule{Frequency: FreqDaily, SendTime: "18:00"}
	if f, _ := PeriodFor(ev, kgl("2026-11-23 18:00")); iso(f) != "2026-11-23" {
		t.Fatalf("evening send covers today: %s", iso(f))
	}
	if n := NextSlot(s, kgl("2026-11-23 06:30")); !n.Equal(kgl("2026-11-24 06:30")) {
		t.Fatalf("next %v", n)
	}
	// Kigali, not UTC: 06:30 Kigali = 04:30 UTC
	if slot.Hour() != 4 || slot.Minute() != 30 {
		t.Fatalf("utc %v", slot)
	}
}

func TestWeeklyAndMonthly(t *testing.T) {
	w := Schedule{Frequency: FreqWeekly, SendTime: "07:00", DayOfWeek: 1, Enabled: true} // Monday
	// 2026-11-25 is a Wednesday → latest Monday 2026-11-23
	slot := LatestSlot(w, kgl("2026-11-25 10:00"))
	if !slot.Equal(kgl("2026-11-23 07:00")) {
		t.Fatalf("weekly %v", slot)
	}
	if f, to := PeriodFor(w, slot); iso(f) != "2026-11-16" || iso(to) != "2026-11-22" {
		t.Fatalf("weekly period %s %s", iso(f), iso(to))
	}
	if n := NextSlot(w, kgl("2026-11-25 10:00")); !n.Equal(kgl("2026-11-30 07:00")) {
		t.Fatalf("weekly next %v", n)
	}
	m := Schedule{Frequency: FreqMonthly, SendTime: "07:40", DayOfMonth: 2, Enabled: true}
	slot = LatestSlot(m, kgl("2026-11-01 12:00"))
	if !slot.Equal(kgl("2026-10-02 07:40")) {
		t.Fatalf("monthly %v", slot)
	}
	if f, to := PeriodFor(m, slot); iso(f) != "2026-09-01" || iso(to) != "2026-09-30" {
		t.Fatalf("monthly period %s %s", iso(f), iso(to))
	}
	if n := NextSlot(m, kgl("2026-11-01 12:00")); !n.Equal(kgl("2026-11-02 07:40")) {
		t.Fatalf("monthly next %v", n)
	}
	// January sends cover December of the previous year
	if f, to := PeriodFor(m, kgl("2027-01-02 07:40")); iso(f) != "2026-12-01" || iso(to) != "2026-12-31" {
		t.Fatalf("year boundary %s %s", iso(f), iso(to))
	}
}

func TestDueSlotRules(t *testing.T) {
	s := Schedule{Enabled: true, Frequency: FreqDaily, SendTime: "06:30"}
	now := kgl("2026-11-23 06:31")
	slot, ok := DueSlot(s, now)
	if !ok || !slot.Equal(kgl("2026-11-23 06:30")) {
		t.Fatalf("due %v %v", slot, ok)
	}
	s.LastSlotAt = &slot
	if _, ok := DueSlot(s, now); ok {
		t.Fatal("same slot twice")
	}
	s.LastSlotAt = nil
	s.Enabled = false
	if _, ok := DueSlot(s, now); ok {
		t.Fatal("disabled")
	}
	// a weekly slot older than the grace window is not sent late
	w := Schedule{Enabled: true, Frequency: FreqWeekly, SendTime: "07:00", DayOfWeek: 1}
	if _, ok := DueSlot(w, kgl("2026-11-25 10:00")); ok {
		t.Fatal("stale weekly slot")
	}
	if _, ok := DueSlot(w, kgl("2026-11-24 10:00")); !ok {
		t.Fatal("within grace")
	}
}

func TestWageSlots(t *testing.T) {
	s := Schedule{Enabled: true, Frequency: FreqWagePeriod, SendTime: "07:30"}
	d := dates.MustParseISO
	periods := []WagePeriod{{d("2026-11-01"), d("2026-11-15")}, {d("2026-11-03"), d("2026-11-15")}, {d("2026-11-16"), d("2026-11-30")}}
	got := WageSlots(s, periods, kgl("2026-11-16 08:00"))
	if len(got) != 1 || !got[0].Slot.Equal(kgl("2026-11-16 07:30")) || iso(got[0].From) != "2026-11-01" || iso(got[0].To) != "2026-11-15" {
		t.Fatalf("wage slots %+v", got)
	}
	last := got[0].Slot
	s.LastSlotAt = &last
	if len(WageSlots(s, periods, kgl("2026-11-16 09:00"))) != 0 {
		t.Fatal("sent twice")
	}
	if len(WageSlots(s, periods, kgl("2026-11-16 07:00"))) != 0 {
		t.Fatal("before send time")
	}
}

func TestResolveRecipients(t *testing.T) {
	def, _ := DefByKey("daily_production")
	plant := "p2"
	s := Schedule{Key: def.Key, Language: "", RecipientEmails: []string{"ops@example.com", "PM@btn.test", "not-an-email"}}
	users := []Candidate{
		{UserID: "u1", Email: "pm@btn.test", Name: "PM", Role: "PRODUCTION_MANAGER", Lang: "fr"},
		{UserID: "u2", Email: "sup@btn.test", Name: "Sup", Role: "SUPERVISOR", PlantID: "p1", Lang: "rw"},
		{UserID: "u3", Email: "fm@btn.test", Name: "FM", Role: "FINANCE_MANAGER", Lang: "am"}, // finance has production.view
		{UserID: "u4", Email: "sys@btn.test", Name: "Sys", Role: "SALES_MANAGER", Lang: "en"}, // no production.view (SRS §8 Sales –)
	}
	got := ResolveRecipients(def, s, users)
	by := map[string]Recipient{}
	for _, r := range got {
		by[r.Email] = r
	}
	if len(got) != 6 {
		t.Fatalf("recipients %+v", got)
	}
	if r := by["pm@btn.test"]; r.Status != "pending" || r.Lang != "fr" || r.PlantID != "" {
		t.Fatalf("pm %+v", r)
	}
	if _, dup := by["PM@btn.test"]; dup {
		t.Fatal("raw e-mail of a user is de-duplicated")
	}
	if r := by["sup@btn.test"]; r.Status != "pending" || r.PlantID != "p1" || r.Lang != "rw" {
		t.Fatalf("supervisor gets own site %+v", r)
	}
	if r := by["sys@btn.test"]; r.Status != "skipped" || r.Reason != "no_permission" {
		t.Fatalf("sysadmin %+v", r)
	}
	if r := by["not-an-email"]; r.Status != "skipped" || r.Reason != "invalid_email" {
		t.Fatalf("invalid %+v", r)
	}
	if r := by["ops@example.com"]; r.Status != "pending" || r.Lang != "en" || r.UserID != "" {
		t.Fatalf("raw %+v", r)
	}
	// fixed site + fixed language
	s.PlantID, s.Language = &plant, "am"
	got = ResolveRecipients(def, s, users)
	for _, r := range got {
		if r.Email == "sup@btn.test" && (r.Status != "skipped" || r.Reason != "other_site") {
			t.Fatalf("other site %+v", r)
		}
		if r.Status == "pending" && (r.Lang != "am" || r.PlantID != "p2") {
			t.Fatalf("fixed %+v", r)
		}
	}
}
