package jobs

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
	return t
}

func TestCronNextInKigali(t *testing.T) {
	cases := []struct {
		spec, after, want string
	}{
		{"30 6 * * *", "2026-11-23 06:29", "2026-11-23 06:30"},
		{"30 6 * * *", "2026-11-23 06:30", "2026-11-24 06:30"}, // strictly after
		{"0 0 * * *", "2026-12-31 23:59", "2027-01-01 00:00"},
		{"*/15 * * * *", "2026-11-23 10:07", "2026-11-23 10:15"},
		{"5/20 * * * *", "2026-11-23 10:26", "2026-11-23 10:45"},
		{"0 9-17/4 * * MON-FRI", "2026-11-21 08:00", "2026-11-23 09:00"}, // Sat -> Mon 09:00
		{"0 13 * * mon-fri", "2026-11-23 09:00", "2026-11-23 13:00"},
		{"0 7 1 * *", "2026-11-02 00:00", "2026-12-01 07:00"},
		{"0 0 29 2 *", "2026-03-01 00:00", "2028-02-29 00:00"},   // leap day
		{"0 8 1,15 * 5", "2026-11-02 00:00", "2026-11-06 08:00"}, // dom OR dow (Friday 6 Nov)
		{"0 0 * * 7", "2026-11-23 00:00", "2026-11-29 00:00"},    // 7 = Sunday
		{"0 0 1 JAN *", "2026-11-23 00:00", "2027-01-01 00:00"},
		{"@hourly", "2026-11-23 10:00", "2026-11-23 11:00"},
		{"@daily", "2026-11-23 10:00", "2026-11-24 00:00"},
		{"@weekly", "2026-11-23 10:00", "2026-11-29 00:00"},
		{"@monthly", "2026-11-23 10:00", "2026-12-01 00:00"},
	}
	for _, c := range cases {
		s, err := ParseCron(c.spec)
		if err != nil {
			t.Fatalf("%s: %v", c.spec, err)
		}
		got := s.Next(kgl(c.after))
		if !got.Equal(kgl(c.want)) {
			t.Errorf("%s after %s = %s, want %s", c.spec, c.after, got.In(dates.Kigali()).Format("2006-01-02 15:04 Mon"), c.want)
		}
	}
}

// Schedules are wall-clock Kigali (UTC+2): "0 6 * * *" fires at 04:00 UTC, whatever the server TZ.
func TestCronEvaluatesInKigaliNotUTC(t *testing.T) {
	s, _ := ParseCron("0 6 * * *")
	got := s.Next(time.Date(2026, 11, 23, 3, 0, 0, 0, time.UTC))
	if want := time.Date(2026, 11, 23, 4, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("got %s want %s", got.UTC(), want)
	}
	// 05:00 UTC is already 07:00 in Kigali -> next is tomorrow 04:00 UTC
	got = s.Next(time.Date(2026, 11, 23, 5, 0, 0, 0, time.UTC))
	if want := time.Date(2026, 11, 24, 4, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("got %s want %s", got.UTC(), want)
	}
	// a midnight Kigali job is 22:00 UTC the day before
	m, _ := ParseCron("0 0 * * *")
	if got := m.Next(time.Date(2026, 11, 23, 21, 30, 0, 0, time.UTC)); !got.Equal(time.Date(2026, 11, 23, 22, 0, 0, 0, time.UTC)) {
		t.Fatalf("midnight: %s", got.UTC())
	}
}

func TestCronInvalid(t *testing.T) {
	for _, spec := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8", "*/0 * * * *",
		"a * * * *", "5-1 * * * *", "0 0 30 2 *", "0 0 31 4,6,9,11 *", "1,,2 * * * *"} {
		if _, err := ParseCron(spec); err == nil {
			t.Errorf("%q should be rejected", spec)
		}
	}
}

func TestLatestAtOrBeforeCatchUp(t *testing.T) {
	s, _ := ParseCron("0 * * * *")
	slot, missed := s.LatestAtOrBefore(kgl("2026-11-23 06:00"), kgl("2026-11-23 09:30"))
	if missed != 3 || !slot.Equal(kgl("2026-11-23 09:00")) {
		t.Fatalf("slot %s missed %d", slot, missed)
	}
	if _, missed := s.LatestAtOrBefore(kgl("2026-11-23 09:00"), kgl("2026-11-23 09:59")); missed != 0 {
		t.Fatalf("nothing due, missed %d", missed)
	}
}

func TestRetryDelayDoubles(t *testing.T) {
	j := Job{Backoff: time.Minute}
	if j.RetryDelay(1) != time.Minute || j.RetryDelay(2) != 2*time.Minute || j.RetryDelay(3) != 4*time.Minute || j.RetryDelay(10) != time.Hour {
		t.Fatal(j.RetryDelay(1), j.RetryDelay(2), j.RetryDelay(3), j.RetryDelay(10))
	}
	if (Job{}).RetryDelay(1) != time.Minute {
		t.Fatal("default backoff")
	}
}

func TestRegisterValidates(t *testing.T) {
	mustPanic := func(name string, j Job) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s: expected panic", name)
			}
		}()
		Register(j)
	}
	mustPanic("bad name", Job{Name: "Bad Name", Run: noop})
	mustPanic("nil run", Job{Name: "x.ok"})
	mustPanic("bad cron", Job{Name: "x.ok", Schedule: "nope", Run: noop})
	Register(Job{Name: "x.ok", Schedule: "@daily", Run: noop})
	Register(Job{Name: "x.ok", Schedule: "@hourly", Run: noop}) // replace
	j, ok := Lookup("x.ok")
	if !ok || j.Schedule != "@hourly" || j.Cron() == nil {
		t.Fatal("replace failed")
	}
	Unregister("x.ok")
	if _, ok := Lookup("x.ok"); ok {
		t.Fatal("unregister failed")
	}
}
