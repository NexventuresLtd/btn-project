package stock

import (
	"testing"

	"btn-ops/internal/approvals"
	"btn-ops/internal/auth"
	"btn-ops/internal/i18n"
)

func TestAllocateFIFO(t *testing.T) {
	lots := []LotFlow{{Lot: "", FirstDate: "2026-11-01", In: 20}, {Lot: "L1", FirstDate: "2026-11-02", In: 100, OutTagged: 10},
		{Lot: "L2", FirstDate: "2026-11-05", In: 50}}
	got := AllocateFIFO(lots, 60) // untagged consumption: 20 from untracked, 40 from L1 (90 free)
	want := []struct{ fifo, rem float64 }{{20, 0}, {40, 50}, {0, 50}}
	for i, w := range want {
		if got[i].OutFIFO != w.fifo || got[i].Remaining != w.rem {
			t.Fatalf("lot %d: %+v want %+v", i, got[i], w)
		}
	}
	// more untagged than all lots hold: the excess stays on the last lot (negative remaining = data problem)
	got = AllocateFIFO([]LotFlow{{Lot: "A", In: 5}, {Lot: "B", In: 5}}, 12.5)
	if got[0].Remaining != 0 || got[1].OutFIFO != 7.5 || got[1].Remaining != -2.5 {
		t.Fatalf("excess %+v", got)
	}
	if out := AllocateFIFO(nil, 3); len(out) != 0 {
		t.Fatal("empty")
	}
}

func TestIsLowAndQty(t *testing.T) {
	if !IsLow(9.999, 10) || IsLow(10, 10) || IsLow(0, 0) || IsLow(-5, 0) {
		t.Fatal("IsLow")
	}
	if qtyProblem(i18n.EN, ItemProduct, 1.5, true) == "" || qtyProblem(i18n.EN, ItemMaterial, 1.5, true) != "" ||
		qtyProblem(i18n.EN, ItemMaterial, 0.0001, true) == "" || qtyProblem(i18n.EN, ItemMaterial, -1, true) == "" ||
		qtyProblem(i18n.EN, ItemMaterial, -1, false) != "" || qtyProblem(i18n.EN, ItemMaterial, 0, false) == "" {
		t.Fatal("qtyProblem")
	}
	if AvailableToWithdraw(100, []float64{-70, 20}) != 30 || AvailableToWithdraw(10, nil) != 10 {
		t.Fatal("available")
	}
	if FmtQty(1234567.5) != "1,234,567.5" || FmtQty(-1000) != "-1,000" {
		t.Fatal(FmtQty(1234567.5))
	}
}

func TestApprovalLevels(t *testing.T) {
	gm := "GENERAL_MANAGER"
	req := []approvals.Level{{Level: 1, Permission: "stockcount.approve"}, {Level: 2, Permission: "stockcount.approve", Role: &gm}}
	if n := NextLevel(req, nil); n == nil || n.Level != 1 {
		t.Fatal("first")
	}
	done := []Approval{{Level: 1}}
	n := NextLevel(req, done)
	if n == nil || n.Level != 2 {
		t.Fatal("second")
	}
	if CanApproveLevel(auth.RoleProductionManager, *n, "stockcount.approve") || !CanApproveLevel(auth.RoleGeneralManager, *n, "stockcount.approve") {
		t.Fatal("role gate")
	}
	if NextLevel(req, append(done, Approval{Level: 2})) != nil {
		t.Fatal("complete")
	}
}
