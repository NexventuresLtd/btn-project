package cutoff

import (
	"strings"
	"testing"
	"time"

	"btn-ops/internal/i18n"
)

func TestNoteWithoutPending(t *testing.T) {
	i := Localize(Info{At: time.Date(2026, 11, 23, 4, 30, 0, 0, time.UTC), Sites: []Site{}}, i18n.EN)
	if i.CutoffLabel != "23/11/2026 06:30" || i.UnsynchronisedNote != "" {
		t.Fatalf("%+v", i)
	}
	if !strings.Contains(i.Note, "23/11/2026 06:30") || !strings.Contains(i.Note, "No device reports") {
		t.Fatalf("note %q", i.Note)
	}
}

func TestNoteWithPendingSites(t *testing.T) {
	i := Info{At: time.Date(2026, 11, 23, 4, 30, 0, 0, time.UTC), PendingSites: 2, PendingDevices: 3, PendingRecords: 17, UnassignedDevices: 1,
		Sites: []Site{{Code: "BTN-1"}, {Code: "BTN-3"}}}
	for _, l := range i18n.All {
		x := Localize(i, l)
		if !strings.Contains(x.UnsynchronisedNote, "BTN-1, BTN-3") || !strings.Contains(x.Note, "17") || !strings.Contains(x.Note, "23/11/2026") {
			t.Fatalf("%s: %q / %q", l, x.UnsynchronisedNote, x.Note)
		}
	}
	en := Localize(i, i18n.EN)
	if en.UnsynchronisedNote != "2 sites (BTN-1, BTN-3) · 1 device(s) of users without a site" {
		t.Fatalf("%q", en.UnsynchronisedNote)
	}
	one := Localize(Info{At: i.At, PendingSites: 1, PendingDevices: 1, Sites: []Site{{Code: "BTN-2"}}}, i18n.EN)
	if one.UnsynchronisedNote != "1 site (BTN-2)" {
		t.Fatalf("%q", one.UnsynchronisedNote)
	}
	many := Info{PendingSites: 8, PendingDevices: 8}
	for k := 0; k < 8; k++ {
		many.Sites = append(many.Sites, Site{Code: "S" + string(rune('0'+k))})
	}
	if c := SiteCodes(many, 6); c != "S0, S1, S2, S3, S4, S5, +2" {
		t.Fatalf("%q", c)
	}
}
