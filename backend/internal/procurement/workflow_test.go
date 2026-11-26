package procurement

import (
	"testing"

	"btn-ops/internal/approvals"
	"btn-ops/internal/auth"
)

func sp(s string) *string { return &s }

// defaults mirrors approvals.Defaults for the procurement domain.
func defaults() []approvals.Threshold {
	return []approvals.Threshold{
		{Level: 1, MinAmount: 0, Permission: "procurement.approve", Role: sp("PRODUCTION_MANAGER"), Label: "PM", IsActive: true},
		{Level: 2, MinAmount: 500_000, Permission: "procurement.approve", Role: sp("FINANCE_MANAGER"), Label: "FM", IsActive: true},
		{Level: 3, MinAmount: 2_000_000, Permission: "procurement.approve", Role: sp("GENERAL_MANAGER"), Label: "GM", IsActive: true},
	}
}

func levelsOf(ls []RequiredLevel) []int {
	out := []int{}
	for _, l := range ls {
		out = append(out, l.Level)
	}
	return out
}

func eqInts(a, b []int) bool {
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

func TestRouteLevelsByValue(t *testing.T) {
	cases := []struct {
		amount int64
		want   []int
	}{{0, []int{1}}, {499_999, []int{1}}, {500_000, []int{1, 2}}, {1_999_999, []int{1, 2}}, {2_000_000, []int{1, 2, 3}}, {9_000_000, []int{1, 2, 3}}}
	for _, c := range cases {
		got := RouteLevels(defaults(), c.amount, auth.RoleSupervisor)
		if !eqInts(levelsOf(got), c.want) {
			t.Errorf("amount %d: levels %v, want %v", c.amount, levelsOf(got), c.want)
		}
	}
	// inactive and max-bounded rows
	th := defaults()
	th[2].IsActive = false
	max := int64(1_000_000)
	th[1].MaxAmount = &max
	if got := levelsOf(RouteLevels(th, 3_000_000, auth.RoleSupervisor)); !eqInts(got, []int{1}) {
		t.Errorf("bounded: %v", got)
	}
	if got := RouteLevels(nil, 100, auth.RoleSupervisor); len(got) != 0 {
		t.Errorf("no thresholds → no approval, got %v", got)
	}
}

func TestRouteLevelsByRequester(t *testing.T) {
	// PM's own small request: level 1 (PM) escalates to the next higher level (FM), whatever the amount.
	got := RouteLevels(defaults(), 100_000, auth.RoleProductionManager)
	if !eqInts(levelsOf(got), []int{2}) || !got[0].Escalated || got[0].Role == nil || *got[0].Role != "FINANCE_MANAGER" {
		t.Fatalf("PM small: %+v", got)
	}
	// PM's big request: level 1 is covered by levels 2 and 3, no escalation marker.
	got = RouteLevels(defaults(), 2_500_000, auth.RoleProductionManager)
	if !eqInts(levelsOf(got), []int{2, 3}) || got[0].Escalated {
		t.Fatalf("PM big: %+v", got)
	}
	// FM's 600k request: level 2 (FM) is the last matched → escalated to GM (level 3).
	got = RouteLevels(defaults(), 600_000, auth.RoleFinanceManager)
	if !eqInts(levelsOf(got), []int{1, 3}) || !got[1].Escalated {
		t.Fatalf("FM: %+v", got)
	}
	// GM's 3M request: no higher level exists → level 3 stays (another GM / super admin must approve).
	got = RouteLevels(defaults(), 3_000_000, auth.RoleGeneralManager)
	if !eqInts(levelsOf(got), []int{1, 2, 3}) {
		t.Fatalf("GM: %+v", got)
	}
}

func TestCheckApprover(t *testing.T) {
	levels := RouteLevels(defaults(), 600_000, auth.RoleSupervisor) // PM then FM
	req := "requester"
	if _, why := CheckApprover(ReqSubmitted, req, levels, nil, req, auth.RoleProductionManager); why != DenySelf {
		t.Fatalf("self approval must be blocked, got %q", why)
	}
	if _, why := CheckApprover(ReqDraft, req, levels, nil, "pm", auth.RoleProductionManager); why != DenyNotSubmitted {
		t.Fatalf("draft: %q", why)
	}
	if _, why := CheckApprover(ReqSubmitted, req, levels, nil, "fm", auth.RoleFinanceManager); why != DenyNotLevel {
		t.Fatalf("FM cannot take level 1: %q", why)
	}
	if _, why := CheckApprover(ReqSubmitted, req, levels, nil, "sup", auth.RoleSupervisor); why != DenyNotLevel {
		t.Fatalf("supervisor lacks procurement.approve: %q", why)
	}
	l, why := CheckApprover(ReqSubmitted, req, levels, nil, "pm", auth.RoleProductionManager)
	if why != "" || l.Level != 1 {
		t.Fatalf("PM level 1: %v %q", l, why)
	}
	decided := []Decision{{Level: 1, UserID: "pm"}}
	if l, why := CheckApprover(ReqSubmitted, req, levels, decided, "fm", auth.RoleFinanceManager); why != "" || l.Level != 2 {
		t.Fatalf("FM level 2: %v %q", l, why)
	}
	// a super admin who already approved level 1 cannot also approve level 2 (one level per person)
	if _, why := CheckApprover(ReqSubmitted, req, levels, []Decision{{Level: 1, UserID: "sa"}}, "sa", auth.RoleSuperAdmin); why != DenyAlreadyActed {
		t.Fatalf("one level per person: %q", why)
	}
	if _, why := CheckApprover(ReqSubmitted, req, levels, []Decision{{1, "pm"}, {2, "fm"}}, "gm", auth.RoleGeneralManager); why != DenyNoLevelOpen {
		t.Fatalf("all decided: %q", why)
	}
}

func TestStateMachines(t *testing.T) {
	okReq := [][2]string{{ReqDraft, ReqSubmitted}, {ReqSubmitted, ReqApproved}, {ReqSubmitted, ReqRejected}, {ReqRejected, ReqDraft},
		{ReqApproved, ReqOrdered}, {ReqOrdered, ReqPartiallyReceived}, {ReqPartiallyReceived, ReqReceived}, {ReqReceived, ReqClosed},
		{ReqSubmitted, ReqDraft}, {ReqApproved, ReqClosed}, {ReqDraft, ReqApproved}}
	for _, p := range okReq {
		if !CanRequestMove(p[0], p[1]) {
			t.Errorf("request %s → %s should be allowed", p[0], p[1])
		}
	}
	badReq := [][2]string{{ReqDraft, ReqOrdered}, {ReqRejected, ReqApproved}, {ReqClosed, ReqDraft}, {ReqReceived, ReqDraft},
		{ReqSubmitted, ReqOrdered}, {ReqApproved, ReqRejected}, {ReqDraft, ReqDraft}}
	for _, p := range badReq {
		if CanRequestMove(p[0], p[1]) {
			t.Errorf("request %s → %s must be refused", p[0], p[1])
		}
	}
	okPO := [][2]string{{PODraft, POOrdered}, {PODraft, POCancelled}, {POOrdered, POPartiallyReceived}, {POOrdered, POReceived},
		{POPartiallyReceived, POReceived}, {POReceived, POClosed}, {POPartiallyReceived, POClosed}, {POOrdered, POCancelled}}
	for _, p := range okPO {
		if !CanOrderMove(p[0], p[1]) {
			t.Errorf("order %s → %s should be allowed", p[0], p[1])
		}
	}
	badPO := [][2]string{{PODraft, POReceived}, {POPartiallyReceived, POCancelled}, {POClosed, POOrdered}, {POCancelled, POOrdered}, {POReceived, POCancelled}}
	for _, p := range badPO {
		if CanOrderMove(p[0], p[1]) {
			t.Errorf("order %s → %s must be refused", p[0], p[1])
		}
	}
}

func TestEvaluateReceipt(t *testing.T) {
	// partial, then the rest: exact, no reason
	s := LineState{Ordered: 100}
	o := EvaluateReceipt(s, 40, false)
	if o.Received != 40 || o.Closed || o.Variance != VarNone || o.NeedsReason {
		t.Fatalf("partial: %+v", o)
	}
	s.Received = 40
	if out := s.Outstanding(); out != 60 {
		t.Fatalf("outstanding %v", out)
	}
	o = EvaluateReceipt(s, 60, false)
	if o.Received != 100 || !o.Closed || o.Variance != VarNone || o.NeedsReason {
		t.Fatalf("complete: %+v", o)
	}
	// over-receipt
	o = EvaluateReceipt(s, 65.5, false)
	if o.Variance != VarOver || o.VarianceQty != 5.5 || !o.NeedsReason || !o.Closed {
		t.Fatalf("over: %+v", o)
	}
	// under-receipt only when marked final
	o = EvaluateReceipt(s, 50, true)
	if o.Variance != VarUnder || o.VarianceQty != 10 || !o.NeedsReason || !o.Closed {
		t.Fatalf("under: %+v", o)
	}
	// decimals without float drift: 0.1 + 0.2 of 0.3
	o = EvaluateReceipt(LineState{Ordered: 0.3, Received: 0.1}, 0.2, false)
	if o.Variance != VarNone || !o.Closed {
		t.Fatalf("decimal drift: %+v", o)
	}
	// final with nothing more (qty 0) on a short line
	o = EvaluateReceipt(LineState{Ordered: 10, Received: 7}, 0, true)
	if o.Variance != VarUnder || o.VarianceQty != 3 {
		t.Fatalf("short close: %+v", o)
	}
	if (LineState{Ordered: 10, Received: 12}).Outstanding() != 0 || (LineState{Ordered: 10, Closed: true}).Outstanding() != 0 {
		t.Fatal("outstanding never negative / closed")
	}
}

func TestOrderAndRequestStatusFor(t *testing.T) {
	if s := OrderStatusFor([]LineState{{Ordered: 10}, {Ordered: 5}}); s != POOrdered {
		t.Fatal(s)
	}
	if s := OrderStatusFor([]LineState{{Ordered: 10, Received: 10}, {Ordered: 5, Received: 1}}); s != POPartiallyReceived {
		t.Fatal(s)
	}
	if s := OrderStatusFor([]LineState{{Ordered: 10, Received: 10}, {Ordered: 5, Received: 1, Closed: true}}); s != POReceived {
		t.Fatal(s)
	}
	if s := RequestStatusFor([]RequestLineProgress{{}, {}}); s != "" {
		t.Fatal(s)
	}
	if s := RequestStatusFor([]RequestLineProgress{{OnOrder: true}, {}}); s != ReqOrdered {
		t.Fatal(s)
	}
	if s := RequestStatusFor([]RequestLineProgress{{OnOrder: true, Received: true, Done: true}, {}}); s != ReqPartiallyReceived {
		t.Fatal(s)
	}
	if s := RequestStatusFor([]RequestLineProgress{{OnOrder: true, Received: true, Done: true, FullyOrdered: true}, {OnOrder: true, Done: true, FullyOrdered: true}}); s != ReqReceived {
		t.Fatal(s)
	}
	// received in full what was ordered, but part of the line was never ordered → still partially received
	if s := RequestStatusFor([]RequestLineProgress{{OnOrder: true, Received: true, Done: true, FullyOrdered: false}}); s != ReqPartiallyReceived {
		t.Fatal(s)
	}
}

func TestSuggestQty(t *testing.T) {
	cases := []struct{ bal, min, onOrder, factor, want float64 }{
		{10, 50, 0, 2, 90},    // top up to 100
		{50, 50, 0, 2, 0},     // at the level: not low
		{0, 0, 0, 2, 0},       // no reorder level
		{10, 50, 95, 2, 0},    // enough already on order
		{10, 50, 30, 2, 60},   // part on order
		{0.4, 1, 0, 2, 2},     // 1.6 trucks → 2 (round up)
		{-5, 20, 0, 1.5, 35},  // negative balance counts
		{10, 50, 0, 0.5, 40},  // factor below 1 → 1
		{49.999, 50, 0, 1, 1}, // 0.001 → 1
	}
	for _, c := range cases {
		if got := SuggestQty(c.bal, c.min, c.onOrder, c.factor); got != c.want {
			t.Errorf("SuggestQty(%v,%v,%v,%v)=%v want %v", c.bal, c.min, c.onOrder, c.factor, got, c.want)
		}
	}
}
