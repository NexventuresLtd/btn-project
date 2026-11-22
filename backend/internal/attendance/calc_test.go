package attendance

import "testing"

func TestDayFraction(t *testing.T) {
	cases := map[string]float64{StatusPresent: 1, StatusHalfDay: 0.5, StatusAbsent: 0, StatusLeave: 0, "": 0, "NOPE": 0}
	for status, want := range cases {
		if got := DayFraction(status); got != want {
			t.Errorf("DayFraction(%q) = %v, want %v", status, got, want)
		}
	}
}

func TestValidStatus(t *testing.T) {
	for _, s := range []string{StatusPresent, StatusHalfDay, StatusAbsent, StatusLeave} {
		if !ValidStatus(s) {
			t.Errorf("ValidStatus(%q) = false", s)
		}
	}
	for _, s := range []string{"", "present", "WORKING", "half"} {
		if ValidStatus(s) {
			t.Errorf("ValidStatus(%q) = true", s)
		}
	}
}

func TestCountsAddAndPayable(t *testing.T) {
	var c Counts
	marks := []string{StatusPresent, StatusPresent, StatusHalfDay, StatusAbsent, StatusLeave, ""}
	for _, m := range marks {
		c.Add(m)
	}
	if c.Present != 2 || c.Half != 1 || c.Absent != 1 || c.Leave != 1 || c.Unmarked != 1 || c.Total != 6 {
		t.Fatalf("counts = %+v", c)
	}
	// 2*1 + 1*0.5 = 2.5 payable (absent, leave and unmarked contribute nothing)
	if c.Payable != 2.5 {
		t.Errorf("payable = %v, want 2.5", c.Payable)
	}
}

func TestPayableFrom(t *testing.T) {
	cases := []struct {
		present, half int
		want          float64
	}{{0, 0, 0}, {3, 0, 3}, {0, 3, 1.5}, {10, 1, 10.5}, {5, 5, 7.5}}
	for _, tc := range cases {
		if got := PayableFrom(tc.present, tc.half); got != tc.want {
			t.Errorf("PayableFrom(%d,%d) = %v, want %v", tc.present, tc.half, got, tc.want)
		}
	}
}
