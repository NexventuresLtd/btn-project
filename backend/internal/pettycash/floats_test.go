package pettycash

import "testing"

func TestReconcileDifference(t *testing.T) {
	if d, s := ReconcileDifference(50_000, 50_000); d != 0 || s != RecBalanced {
		t.Errorf("balanced: %d %s", d, s)
	}
	if d, s := ReconcileDifference(48_500, 50_000); d != -1_500 || s != RecPendingReview {
		t.Errorf("shortage: %d %s", d, s)
	}
	if d, s := ReconcileDifference(51_000, 50_000); d != 1_000 || s != RecPendingReview {
		t.Errorf("surplus: %d %s", d, s)
	}
	if d, _ := ReconcileDifference(0, -200); d != 200 {
		t.Errorf("negative book: %d", d)
	}
}

func TestTopUpSelfApprovalBlocked(t *testing.T) {
	cust := "sup"
	if ok, r := TopUpApproval("sup", "fm", &cust, true); ok || r != "pettycash.err.custodian_topup" {
		t.Errorf("custodian approves own float top-up: %v %s", ok, r)
	}
	if ok, r := TopUpApproval("sup", "sup", &cust, true); ok || r != "pettycash.err.self_review" {
		t.Errorf("creator approves: %v %s", ok, r)
	}
	if ok, _ := TopUpApproval("gm", "sup", &cust, true); !ok {
		t.Error("gm approves custodian's request")
	}
	if ok, _ := TopUpApproval("sup", "fm", &cust, false); !ok {
		t.Error("expense entries: custodian rule only applies to top-ups")
	}
	if ok, _ := TopUpApproval("gm", "fm", nil, true); !ok {
		t.Error("float without custodian")
	}
}

func TestFloatThresholds(t *testing.T) {
	if !FloatLow(9_999, 10_000) || FloatLow(10_000, 10_000) || FloatLow(-5, 0) {
		t.Error("FloatLow")
	}
	if TopUpSuggestion(100_000, 35_000) != 65_000 || TopUpSuggestion(100_000, 120_000) != 0 || TopUpSuggestion(0, 5) != 0 {
		t.Error("TopUpSuggestion")
	}
	if !ReconcileDue("", 30, "2026-11-20") || ReconcileDue("2026-11-01", 30, "2026-11-20") || !ReconcileDue("2026-10-21", 30, "2026-11-20") {
		t.Error("ReconcileDue")
	}
}
