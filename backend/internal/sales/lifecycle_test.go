package sales

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"

	"btn-ops/internal/approvals"
	"btn-ops/internal/auth"
)

func TestFigure3Transitions(t *testing.T) {
	legal := [][2]string{
		{StDraft, StQuoted}, {StDraft, StConfirmed}, {StQuoted, StConfirmed}, {StConfirmed, StInProduction}, {StConfirmed, StFromStock},
		{StInProduction, StReady}, {StFromStock, StReady}, {StFromStock, StInProduction}, {StReady, StDispatched}, {StDispatched, StDelivered},
		{StDelivered, StClosed}, {StDraft, StCancelled}, {StQuoted, StCancelled}, {StConfirmed, StCancelled},
		{StDispatched, StReady}, {StDelivered, StDispatched}, // corrections when a delivery note is cancelled
	}
	for _, p := range legal {
		if !canTransition(p[0], p[1]) {
			t.Errorf("%s → %s should be allowed", p[0], p[1])
		}
	}
	illegal := [][2]string{
		{StDraft, StDispatched}, {StDraft, StReady}, {StQuoted, StDraft}, {StConfirmed, StDispatched}, {StConfirmed, StReady}, {StConfirmed, StDraft},
		{StInProduction, StCancelled}, {StFromStock, StCancelled}, {StReady, StCancelled}, {StDispatched, StCancelled}, {StDelivered, StCancelled},
		{StClosed, StDelivered}, {StClosed, StCancelled}, {StCancelled, StDraft}, {StCancelled, StConfirmed}, {StReady, StDelivered},
		{StDispatched, StClosed}, {StQuoted, StQuoted}, {"BOGUS", StDraft}, {StDraft, "BOGUS"},
	}
	for _, p := range illegal {
		if canTransition(p[0], p[1]) {
			t.Errorf("%s → %s must be rejected", p[0], p[1])
		}
	}
	for _, s := range OrderStatuses {
		if !validStatus(s) {
			t.Errorf("%s missing from the transition table", s)
		}
	}
	if len(nextStatuses(StClosed)) != 0 || len(nextStatuses(StCancelled)) != 0 {
		t.Fatal("closed / cancelled are terminal")
	}
}

func TestStatePredicatesAndLegacy(t *testing.T) {
	for _, s := range []string{StDraft, StQuoted} {
		if !editable(s) || committed(s) || payable(s) || legacyStatus(s) != "DRAFT" {
			t.Errorf("%s: editable, not committed, legacy DRAFT", s)
		}
	}
	for _, s := range []string{StConfirmed, StInProduction, StFromStock, StReady, StDispatched, StDelivered, StClosed} {
		if editable(s) || !committed(s) || legacyStatus(s) != "CONFIRMED" {
			t.Errorf("%s: locked, committed, legacy CONFIRMED", s)
		}
	}
	if legacyStatus(StCancelled) != "CANCELLED" || committed(StCancelled) {
		t.Fatal("cancelled")
	}
	if payable(StClosed) || !payable(StDelivered) || !dispatchable(StReady) || !dispatchable(StDispatched) || dispatchable(StConfirmed) {
		t.Fatal("payable / dispatchable")
	}
	if !cancelNeedsApproval(StConfirmed) || !cancelNeedsApproval(StQuoted) || !cancelNeedsApproval(StDraft) || cancelNeedsApproval(StReady) {
		t.Fatal("cancel approval rule")
	}
	if amendable(StDraft) || amendable(StClosed) || !amendable(StDispatched) {
		t.Fatal("amendable")
	}
	if fulfilmentStatus(true) != StInProduction || fulfilmentStatus(false) != StFromStock {
		t.Fatal("fulfilment status")
	}
}

func TestShipStateAndClose(t *testing.T) {
	cases := []struct {
		ordered, shipped int64
		active, open     int
		want             string
	}{
		{100, 0, 0, 0, StReady},
		{100, 40, 1, 1, StDispatched},  // partial dispatch keeps the order open
		{100, 40, 1, 0, StDispatched},  // partial, received — still open
		{100, 100, 2, 1, StDispatched}, // all shipped, one note not yet received
		{100, 100, 2, 0, StDelivered},  // all shipped and received
		{100, 120, 2, 0, StDelivered},  // defensive
		{100, 0, 1, 1, StReady},        // a note with nothing on it never counts
	}
	for _, c := range cases {
		if got := shipState(c.ordered, c.shipped, c.active, c.open); got != c.want {
			t.Errorf("%+v → %s, want %s", c, got, c.want)
		}
	}
	if !shouldClose(StDelivered, 1000, 1000) || shouldClose(StDelivered, 1000, 999) || shouldClose(StDispatched, 1000, 1000) {
		t.Fatal("auto close")
	}
}

func f64(f float64) *float64 { return &f }

func TestLineDiscountMaths(t *testing.T) {
	c, ok := lineAmounts(100, 500, f64(10), 0)
	if !ok || c.Gross != 50000 || c.Discount != 5000 || c.Net != 45000 {
		t.Fatalf("10%%: %+v", c)
	}
	c, _ = lineAmounts(3, 333, f64(12.5), 0) // 999 × 12.5 % = 124.875 → 125
	if c.Discount != 125 || c.Net != 874 {
		t.Fatalf("rounding: %+v", c)
	}
	c, ok = lineAmounts(10, 1000, nil, 2500)
	if !ok || c.Discount != 2500 || c.Net != 7500 {
		t.Fatalf("amount: %+v", c)
	}
	if _, ok = lineAmounts(10, 100, nil, 1001); ok {
		t.Fatal("an amount discount above the line must fail")
	}
	c, _ = lineAmounts(10, 100, f64(150), 0)
	if c.Net != 0 || c.Discount != 1000 {
		t.Fatalf("percent capped at 100: %+v", c)
	}
	c, _ = lineAmounts(10, 100, f64(5), 300) // percent wins over amount
	if c.Discount != 50 {
		t.Fatalf("percent wins: %+v", c)
	}
	if c, _ := lineAmounts(7, 0, nil, 0); c.Net != 0 || c.Gross != 0 {
		t.Fatal("free line")
	}
	// GM rule on the effective price: default 500, 15 % limit → net ≥ 425/unit is fine
	if netNeedsGmApproval(42500, 100, 500, 15) || !netNeedsGmApproval(42499, 100, 500, 15) || netNeedsGmApproval(10, 1, 0, 15) {
		t.Fatal("GM approval on net")
	}
}

func TestDeliveryChargeRules(t *testing.T) {
	kgl, out := "KIGALI", "OUTSIDE_KIGALI"
	zero, ten, fifty := 0.0, 10.0, 50.0
	rules := []chargeRule{
		{ID: "f", Method: ChargeFixed, Amount: 15000, IsActive: true, Sort: 1},
		{ID: "f0", Method: ChargeFixed, Amount: 9000, IsActive: false},
		{ID: "z1", Method: ChargeZone, Zone: &kgl, Amount: 20000, IsActive: true},
		{ID: "d1", Method: ChargeDistance, MinKm: &zero, MaxKm: &ten, Amount: 25000, IsActive: true},
		{ID: "d2", Method: ChargeDistance, MinKm: &ten, MaxKm: &fifty, Amount: 60000, IsActive: true},
		{ID: "d3", Method: ChargeDistance, MinKm: &fifty, Amount: 120000, IsActive: true},
	}
	check := func(zone string, km *float64, want int64, id string) {
		t.Helper()
		a, r := pickCharge(rules, zone, km)
		if a != want || (r == nil) != (id == "") || (r != nil && r.ID != id) {
			t.Errorf("zone=%q km=%v → %d %v, want %d %s", zone, km, a, r, want, id)
		}
	}
	check(kgl, f64(70), 20000, "z1")  // zone wins
	check(out, f64(9.9), 25000, "d1") // no zone rule → distance band
	check("", f64(10), 60000, "d2")   // band lower bound inclusive, upper exclusive
	check("", f64(500), 120000, "d3") // open-ended band
	check(out, nil, 15000, "f")       // fixed fallback (inactive ignored)
	if a, r := pickCharge(rules[1:2], "", nil); a != 0 || r != nil {
		t.Fatal("only inactive rules → no charge")
	}
	if f := checkCharge(ChargeZone, nil, nil, nil); f["zone"] == "" {
		t.Fatal("zone rule needs a zone")
	}
	if f := checkCharge(ChargeDistance, nil, &zero, &zero); f["maxKm"] == "" {
		t.Fatal("band must be increasing")
	}
	if f := checkCharge(ChargeFixed, nil, nil, nil); len(f) != 0 {
		t.Fatal("fixed is fine")
	}
}

func TestPaymentTermsDepositAndKinds(t *testing.T) {
	dep := paymentTerm{Days: 0, DepositPercent: 50}
	if depositFor(dep, 100001) != 50001 || depositFor(paymentTerm{Days: 30}, 100000) != 0 {
		t.Fatal("deposit")
	}
	conf := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if d := dueDateFor(paymentTerm{Days: 30}, conf, nil); d == nil || d.Format("2006-01-02") != "2026-12-01" {
		t.Fatalf("net 30: %v", d)
	}
	if dueDateFor(dep, conf, nil) != nil {
		t.Fatal("deposit terms: balance due only once delivered")
	}
	del := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	if d := dueDateFor(paymentTerm{Days: 7, DepositPercent: 30}, conf, &del); d.Format("2006-01-02") != "2026-11-27" {
		t.Fatalf("deposit + 7 after delivery: %v", d)
	}
	if paymentKind(1000, 0, 500, 500, true, false) != PayKindDeposit || paymentKind(1000, 500, 200, 500, false, false) != PayKindPart ||
		paymentKind(1000, 500, 500, 500, false, true) != PayKindBalance || paymentKind(1000, 0, 300, 0, true, false) != PayKindPart ||
		paymentKind(1000, 0, 300, 500, true, true) != PayKindPart {
		t.Fatal("payment kinds")
	}
	if depositStatus(0, 0) != "NONE" || depositStatus(500, 0) != "DUE" || depositStatus(500, 100) != "PARTIAL" || depositStatus(500, 600) != "RECEIVED" {
		t.Fatal("deposit status")
	}
}

func TestDuplicateScoring(t *testing.T) {
	tin, ph := "102 345 678", "+250 788 123 456"
	all := []dupCandidate{
		{ID: "a", Name: "KAGAME BUILDERS LTD", TIN: &tin},
		{ID: "b", Name: "NYARUGENGE HARDWARE", Phones: []string{"0788123456"}},
		{ID: "c", Name: "Société Générale de Construction"},
		{ID: "d", Name: "TOTALLY DIFFERENT CO"},
		{ID: "e", Name: "Kagame Builders", Phone: &ph, Phones: []string{ph}},
	}
	got := findDuplicates(dupProbe{Name: "Kagame Builders Ltd.", TIN: "102345678", Phones: []string{"250788123456"}}, all, "e")
	if len(got) != 2 || got[0].ID != "a" || got[0].Score != 100 || got[1].ID != "b" || got[1].Score != 90 {
		t.Fatalf("tin > phone, self excluded: %+v", got)
	}
	got = findDuplicates(dupProbe{Name: "societe generale de construction sarl"}, all, "")
	if len(got) != 1 || got[0].ID != "c" || got[0].Score != 100 || got[0].Match[0] != "name" {
		t.Fatalf("accent / suffix insensitive: %+v", got)
	}
	got = findDuplicates(dupProbe{Name: "Kagame Bulders"}, all, "") // typo
	if len(got) < 2 || got[0].Score < 72 {
		t.Fatalf("near name: %+v", got)
	}
	if got := findDuplicates(dupProbe{Name: "Amahoro Quarry"}, all, ""); len(got) != 0 {
		t.Fatalf("no match: %+v", got)
	}
	if normPhone("12") != "" || normTIN("12") != "" || normPhone("(+250) 0788-123-456") != "788123456" {
		t.Fatal("normalisation")
	}
	if s := nameSimilarity("ABC LTD", "abc"); s != 1 {
		t.Fatalf("suffix ignored: %v", s)
	}
}

func TestApprovalLevels(t *testing.T) {
	gm := string(auth.RoleProductionManager) // discount / role-bound level: the Production Manager (SRS §8)
	th := []approvals.Level{{Level: 1, Permission: "sales.approve", Label: "SM"}, {Level: 2, Permission: "sales.approve", Role: &gm, Label: "GM"}}
	if l := requiredLevels(ApprConfirm, nil, false); len(l) != 0 {
		t.Fatal("no thresholds, no discount → no approval")
	}
	if l := requiredLevels(ApprConfirm, nil, true); len(l) != 1 || *l[0].Role != gm || l[0].Label != lblDiscount {
		t.Fatalf("discount level: %+v", l)
	}
	if l := requiredLevels(ApprConfirm, th, true); len(l) != 2 {
		t.Fatalf("GM threshold already covers the discount: %+v", l)
	}
	if l := requiredLevels(ApprCancel, th[:1], false); len(l) != 2 || l[1].Permission != "sales.cancel" || l[1].Level != 2 {
		t.Fatalf("cancel adds a sales.cancel level: %+v", l)
	}
	if l := requiredLevels(ApprAmendment, nil, false); len(l) != 1 || l[0].Permission != "sales.approve" {
		t.Fatalf("amendment always needs one approval: %+v", l)
	}
	steps := []approvalStep{{Level: 1, Decision: "APPROVED"}}
	if n := nextLevel(th, steps); n == nil || n.Level != 2 {
		t.Fatal("next level")
	}
	if nextLevel(th, append(steps, approvalStep{Level: 2})) != nil {
		t.Fatal("all decided")
	}
	sm := &auth.User{ID: "sm", Role: auth.RoleSalesManager}
	gmU := &auth.User{ID: "pm", Role: auth.RoleProductionManager}
	md := &auth.User{ID: "sm2", Role: auth.RoleSalesManager}
	if mayDecide(sm, th[0], "sm", nil) {
		t.Fatal("own request")
	}
	if !mayDecide(md, th[0], "sm", nil) || mayDecide(md, th[1], "sm", nil) || !mayDecide(gmU, th[1], "sm", nil) {
		t.Fatal("role-bound level")
	}
	if mayDecide(&auth.User{ID: "gm", Role: auth.RoleGeneralManager}, th[0], "sm", nil) {
		t.Fatal("top management reads orders only (SRS §8)")
	}
	if mayDecide(gmU, th[1], "sm", map[string]bool{"pm": true}) {
		t.Fatal("one person per request")
	}
	if mayDecide(&auth.User{ID: "x", Role: auth.RoleSupervisor}, th[0], "sm", nil) {
		t.Fatal("supervisor lacks sales.approve")
	}
}

func TestAmendmentChecks(t *testing.T) {
	cur := []existingLine{{ID: "l1", ProductID: "p1", Quantity: 100, Delivered: 40}, {ID: "l2", ProductID: "p2", Quantity: 50, Delivered: 0}}
	ok := []amendLine{{SaleLineID: "l1", ProductID: "p1", Quantity: 40}, {ProductID: "p3", Quantity: 10}}
	if is := checkAmendment(cur, ok); len(is) != 0 {
		t.Fatalf("keep delivered qty, drop undelivered line, add new: %+v", is)
	}
	bad := []amendLine{{SaleLineID: "l1", ProductID: "p9", Quantity: 39}, {SaleLineID: "zz", ProductID: "p2", Quantity: 1}, {SaleLineID: "l2", ProductID: "p2", Quantity: 1}, {SaleLineID: "l2", ProductID: "p2", Quantity: 1}}
	keys := map[string]bool{}
	for _, i := range checkAmendment(cur, bad) {
		keys[i.Key] = true
	}
	for _, k := range []string{"sales.issue.product_locked", "sales.issue.below_delivered", issNotInSale, issTwice} {
		if !keys[k] {
			t.Errorf("missing issue %s in %v", k, keys)
		}
	}
	if is := checkAmendment(cur, []amendLine{{SaleLineID: "l2", ProductID: "p2", Quantity: 5}}); len(is) != 1 || is[0].Key != "sales.issue.remove_delivered" {
		t.Fatalf("removing a delivered line: %+v", is)
	}
}

func TestSniffImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 800, 400))
	for x := 0; x < 800; x++ {
		img.Set(x, x%400, color.RGBA{200, 10, 10, 255})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	p, key := sniffImage(b.Bytes())
	if key != "" || p.Mime != "image/png" || p.Width != 800 || p.Height != 400 || len(p.Thumb) == 0 || len(p.SHA) != 64 {
		t.Fatalf("png: %q %+v", key, p)
	}
	th, _, err := image.DecodeConfig(bytes.NewReader(p.Thumb))
	if err != nil || th.Width != 320 || th.Height != 160 {
		t.Fatalf("thumbnail: %+v %v", th, err)
	}
	if _, key := sniffImage([]byte("<svg xmlns='http://www.w3.org/2000/svg'></svg>")); key != "sales.v.photo_type" {
		t.Fatal("svg must be refused")
	}
	if _, key := sniffImage(append([]byte("GIF89a"), make([]byte, 100)...)); key != "sales.v.photo_type" {
		t.Fatal("gif refused")
	}
	if _, key := sniffImage(nil); key != "sales.v.photo_empty" {
		t.Fatal("empty")
	}
	if _, key := sniffImage(make([]byte, maxPhotoBytes+1)); key != "sales.v.photo_too_big" {
		t.Fatal("size")
	}
	fake := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, make([]byte, 50)...) // PNG magic, broken body
	if _, key := sniffImage(fake); key != "sales.v.photo_type" {
		t.Fatal("corrupt png")
	}
}
