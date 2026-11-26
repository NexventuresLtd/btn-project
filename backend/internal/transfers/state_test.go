package transfers

import "testing"

func TestTransferStateMachine(t *testing.T) {
	legal := []struct{ from, act, to string }{
		{Requested, ActApprove, Approved}, {Requested, ActReject, Rejected}, {Requested, ActCancel, Cancelled},
		{Approved, ActDispatch, InTransit}, {Approved, ActCancel, Cancelled},
		{InTransit, ActReceive, Received}, {InTransit, ActReverse, Cancelled},
	}
	for _, c := range legal {
		if to, ok := Next(c.from, c.act); !ok || to != c.to {
			t.Errorf("%s --%s--> want %s got %s/%v", c.from, c.act, c.to, to, ok)
		}
	}
	illegal := []struct{ from, act string }{
		{Requested, ActDispatch}, {Requested, ActReceive}, {Requested, ActReverse},
		{Approved, ActApprove}, {Approved, ActReject}, {Approved, ActReceive}, {Approved, ActReverse},
		{InTransit, ActCancel}, {InTransit, ActApprove}, {InTransit, ActDispatch}, {InTransit, ActReject},
		{Received, ActCancel}, {Received, ActReverse}, {Received, ActReceive},
		{Rejected, ActApprove}, {Rejected, ActCancel}, {Cancelled, ActApprove}, {Cancelled, ActDispatch}, {"BOGUS", ActApprove},
	}
	for _, c := range illegal {
		if to, ok := Next(c.from, c.act); ok {
			t.Errorf("%s --%s--> must be illegal, got %s", c.from, c.act, to)
		}
	}
	for _, s := range []string{Received, Rejected, Cancelled} {
		if !Final(s) {
			t.Errorf("%s should be final", s)
		}
	}
	for _, s := range []string{Requested, Approved, InTransit} {
		if Final(s) {
			t.Errorf("%s is not final", s)
		}
	}
}

func TestShortfall(t *testing.T) {
	cases := []struct{ sent, got, want float64 }{{100, 100, 0}, {100, 97, 3}, {10.5, 10.25, 0.25}, {5, 6, 0}, {0, 0, 0}, {0.3, 0.1, 0.2}}
	for _, c := range cases {
		if got := Shortfall(c.sent, c.got); got != c.want {
			t.Errorf("Shortfall(%v,%v)=%v want %v", c.sent, c.got, got, c.want)
		}
	}
}

func TestReplaceN(t *testing.T) {
	if got := replaceN(`(a = $N OR b = $N)`, 3); got != `(a = $3 OR b = $3)` {
		t.Fatal(got)
	}
}
