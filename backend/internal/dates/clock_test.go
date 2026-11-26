package dates

import (
	"testing"
	"time"
)

// Business dates follow the Africa/Kigali calendar: at 23:30 UTC it is already the next day in Kigali.
func TestKigaliClockAtLateUTCEvening(t *testing.T) {
	at := time.Date(2026, 11, 26, 23, 30, 0, 0, time.UTC)
	defer SetClock(FixedClock(at))()
	if got := TodayISO(); got != "2026-11-27" {
		t.Fatalf("TodayISO at 23:30 UTC = %s, want the Kigali date 2026-11-27", got)
	}
	if got := Today(); !got.Equal(time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("Today = %v", got)
	}
	if r := ResolveRange("", ""); r.ToISO != "2026-11-27" || r.FromISO != "2026-11-01" {
		t.Fatalf("default range %s – %s", r.FromISO, r.ToISO)
	}
	if ISOOf(at) != "2026-11-27" || ToISO(DateOf(at)) != "2026-11-27" {
		t.Fatalf("DateOf/ISOOf: %s %s", ISOOf(at), ToISO(DateOf(at)))
	}
	ny := time.Date(2026, 12, 31, 22, 15, 0, 0, time.UTC) // New Year's Eve 00:15 in Kigali
	defer SetClock(FixedClock(ny))()
	if Year() != 2027 {
		t.Fatalf("numbering year at 22:15 UTC on 31/12 = %d, want 2027", Year())
	}
	if l := LateUTCEvening(time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)); !l.Equal(time.Date(2026, 11, 26, 23, 30, 0, 0, time.UTC)) {
		t.Fatalf("LateUTCEvening %v", l)
	}
}
