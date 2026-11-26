package dates

import (
	"testing"
	"time"
)

func TestParseFormatNoDrift(t *testing.T) {
	d, err := ParseISO("2026-11-14")
	if err != nil || ToISO(d) != "2026-11-14" || FormatDate(d) != "14/11/2026" {
		t.Fatal(d, err)
	}
	for _, bad := range []string{"2026-02-30", "2026-13-01", "26-1-1", "2026-11-14T00:00:00Z", ""} {
		if IsValidISO(bad) {
			t.Errorf("%q must be invalid", bad)
		}
	}
	if FormatDate(MustParseISO("2026-09-05")) != "05/09/2026" || FormatISO("2026-01-02") != "02/01/2026" || FormatISO("bad") != "bad" {
		t.Error("September follows en-GB")
	}
}

func TestTodayIsKigali(t *testing.T) {
	// 23:30 UTC on the 14th is already the 15th in Kigali (UTC+2)
	if got := TodayISO(time.Date(2026, 11, 14, 23, 30, 0, 0, time.UTC)); got != "2026-11-15" {
		t.Fatal(got)
	}
	if got := FormatDateTime(time.Date(2026, 11, 14, 14, 5, 0, 0, time.UTC)); got != "14/11/2026 16:05" {
		t.Fatal(got)
	}
}

func TestRangesAndMonths(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	r := ResolveRange("", "", now)
	if r.FromISO != "2026-11-01" || r.ToISO != "2026-11-20" {
		t.Fatal(r)
	}
	r = ResolveRange("2026-11-10", "2026-11-05", now) // swapped
	if r.FromISO != "2026-11-05" || r.ToISO != "2026-11-10" {
		t.Fatal(r)
	}
	r = ResolveRange("garbage", "2026-11-05", now)
	if r.FromISO != "2026-11-01" || r.ToISO != "2026-11-05" {
		t.Fatal(r)
	}
	if ToISO(EndOfMonth(MustParseISO("2026-02-10"))) != "2026-02-28" || ToISO(StartOfMonth(MustParseISO("2026-02-10"))) != "2026-02-01" {
		t.Fatal("month bounds")
	}
	days := EachDay(MustParseISO("2026-11-28"), MustParseISO("2026-12-02"))
	if len(days) != 5 || ToISO(days[4]) != "2026-12-02" {
		t.Fatal(days)
	}
	if ToISO(AddDays(MustParseISO("2026-12-31"), 1)) != "2027-01-01" {
		t.Fatal("add days")
	}
}

func TestParseDisplay(t *testing.T) {
	for in, want := range map[string]string{"14/11/2026": "2026-11-14", "1/2/2026": "2026-02-01", "05-09-2026": "2026-09-05", " 31.12.2026 ": "2026-12-31"} {
		d, err := ParseDisplay(in)
		if err != nil || ToISO(d) != want {
			t.Errorf("%q => %v %v", in, d, err)
		}
	}
	for _, bad := range []string{"2026-11-14", "31/02/2026", "14/13/2026", ""} {
		if _, err := ParseDisplay(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}
