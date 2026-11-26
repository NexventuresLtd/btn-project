package stockcount

import "testing"

func TestCountStateMachine(t *testing.T) {
	ok := []struct{ from, act, to string }{{Open, ActCount, Open}, {Open, ActSubmit, Submitted}, {Open, ActCancel, Cancelled},
		{Submitted, ActApprove, Approved}, {Submitted, ActReject, Open}, {Submitted, ActCancel, Cancelled}}
	for _, c := range ok {
		if to, legal := Next(c.from, c.act); !legal || to != c.to {
			t.Errorf("%s/%s → %s, %v", c.from, c.act, to, legal)
		}
	}
	bad := []struct{ from, act string }{{Open, ActApprove}, {Open, ActReject}, {Submitted, ActCount}, {Submitted, ActSubmit},
		{Approved, ActCancel}, {Approved, ActCount}, {Approved, ActReject}, {Cancelled, ActSubmit}}
	for _, c := range bad {
		if _, legal := Next(c.from, c.act); legal {
			t.Errorf("%s/%s must be illegal", c.from, c.act)
		}
	}
}

func TestVariance(t *testing.T) {
	if v := Variance(100, 97); v != -3 {
		t.Fatal(v)
	}
	if v := Variance(10.125, 10.5); v != 0.375 {
		t.Fatal(v)
	}
	if v := Variance(0.1+0.2, 0.3); v != 0 {
		t.Fatal(v)
	}
	if VarianceValue(-3, 7000) != 21000 || VarianceValue(0.5, 1500) != 750 {
		t.Fatal("value")
	}
	f := func(x float64) *float64 { return &x }
	s := Summarise([]LineFigures{{System: 100, Counted: f(97), UnitValue: 7000}, {System: 10, Counted: f(12), UnitValue: 1500},
		{System: 5, Counted: f(5), UnitValue: 100}, {System: 8, Counted: nil, UnitValue: 100}})
	if s.Lines != 4 || s.Counted != 3 || s.WithVariance != 2 || s.LossValue != 21000 || s.GainValue != 3000 || s.AbsValue != 24000 {
		t.Fatalf("%+v", s)
	}
	if s.AccuracyRatio < 0.333 || s.AccuracyRatio > 0.334 {
		t.Fatalf("accuracy %v", s.AccuracyRatio)
	}
}
