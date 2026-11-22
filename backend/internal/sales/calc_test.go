package sales

import (
	"reflect"
	"testing"

	"btn-ops/internal/dates"
)

func TestPaymentStatus(t *testing.T) {
	cases := []struct {
		amount, paid int64
		want         string
	}{{1_500_000, 0, "UNPAID"}, {1_500_000, 1, "PARTIAL"}, {1_500_000, 1_500_000, "PAID"}, {0, 0, "PAID"}}
	for _, c := range cases {
		if got := paymentStatus(c.amount, c.paid); got != c.want {
			t.Errorf("paymentStatus(%d,%d)=%s want %s", c.amount, c.paid, got, c.want)
		}
	}
}

func TestDeliveryStatus(t *testing.T) {
	cases := []struct {
		ordered, delivered int64
		want               string
	}{{1000, 0, "NOT_DELIVERED"}, {1000, 400, "PARTIAL"}, {1000, 1000, "DELIVERED"}}
	for _, c := range cases {
		if got := deliveryStatus(c.ordered, c.delivered); got != c.want {
			t.Errorf("deliveryStatus(%d,%d)=%s", c.ordered, c.delivered, got)
		}
	}
}

func TestOutstandingOf(t *testing.T) {
	if outstandingOf("CONFIRMED", 1000, 400) != 600 || outstandingOf("DRAFT", 1000, 0) != 0 || outstandingOf("CANCELLED", 1000, 0) != 0 {
		t.Fatal("only confirmed sales count")
	}
	if outstandingOf("CONFIRMED", 1000, 1200) != 0 {
		t.Fatal("never negative")
	}
}

func TestDiscountRule(t *testing.T) {
	cases := []struct {
		unit, def int64
		pct       float64
		want      bool
		name      string
	}{
		{1350, 1500, 10, false, "exactly 10% off"}, {1500, 1500, 10, false, "default"}, {1550, 1500, 10, false, "above default"},
		{1349, 1500, 10, true, "just below"}, {1150, 1500, 10, true, "well below"}, {1, 0, 10, false, "no default price"},
		{1035, 1150, 10, false, "float edge 1"}, {1034, 1150, 10, true, "float edge 2"},
		{1200, 1500, 20, false, "20%"}, {1200, 1500, 15, true, "15%"}, {1499, 1500, 0, true, "0% allows none"},
	}
	for _, c := range cases {
		if got := lineNeedsGmApproval(c.unit, c.def, c.pct); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
	if !saleNeedsGmApproval([]priced{{1500, 1500}, {900, 1300}}, 10) {
		t.Error("any line below the limit flags the sale")
	}
	if saleNeedsGmApproval([]priced{{1400, 1500}, {1200, 1300}}, 10) {
		t.Error("lines within the limit")
	}
	if minPriceWithoutApproval(1500, 10) != 1350 || minPriceWithoutApproval(1155, 10) != 1040 || minPriceWithoutApproval(0, 10) != 0 {
		t.Error("minPriceWithoutApproval")
	}
	if r := discountRatio(1350, 1500); r == nil || *r < 0.0999 || *r > 0.1001 {
		t.Error("discountRatio")
	}
	if discountRatio(1350, 0) != nil {
		t.Error("discountRatio without default is nil")
	}
}

func TestRemainingAndDeliveryRequest(t *testing.T) {
	lines := []lineProgress{{"a", "HB20", 1000, 600}, {"b", "HB15", 500, 500}}
	if remainingToDeliver(1000, 600) != 400 || remainingToDeliver(500, 500) != 0 || remainingToDeliver(10, 12) != 0 {
		t.Error("remaining")
	}
	if got := checkDeliveryRequest(lines, []deliveryReq{{"a", 400}}); len(got) != 0 {
		t.Errorf("within remaining: %v", got)
	}
	issues := checkDeliveryRequest(lines, []deliveryReq{{"a", 401}, {"b", 1}, {"a", 1}, {"zzz", 1}})
	var got [][2]any
	for _, i := range issues {
		got = append(got, [2]any{i.Index, i.Field})
	}
	want := [][2]any{{0, "quantity"}, {1, "quantity"}, {2, "saleLineId"}, {3, "saleLineId"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("issues %v", got)
	}
	if issues[0].Key != issOnlyNLeft || issues[0].Args[1].(int64) != 400 || issues[1].Key != issFullyDone {
		t.Errorf("issue messages: %+v", issues)
	}
}

func TestSumAndReturnable(t *testing.T) {
	delivered := sumByProduct([]productQty{{"HB20", 300}, {"HB20", 200}, {"HB15", 100}})
	if v, _ := delivered.get("HB20"); v != 500 {
		t.Fatal("sum")
	}
	returned := newQtyMap()
	returned.add("HB20", 120)
	r := returnableByProduct(delivered, returned)
	if v, _ := r.get("HB20"); v != 380 {
		t.Fatal("returnable HB20")
	}
	if v, _ := r.get("HB15"); v != 100 {
		t.Fatal("returnable HB15")
	}
	if !reflect.DeepEqual(r.keys, []string{"HB20", "HB15"}) {
		t.Fatal("insertion order kept")
	}
}

func TestAvailableForIssue(t *testing.T) {
	if availableForIssue(500, nil) != 500 {
		t.Error("balance when nothing later")
	}
	if availableForIssue(500, []int64{-300, -150}) != 50 {
		t.Error("back-dated issue capped")
	}
	if availableForIssue(500, []int64{-400, 1000}) != 100 {
		t.Error("dip limits it")
	}
	if availableForIssue(-20, nil) != 0 {
		t.Error("never negative")
	}
}

func TestAgeing(t *testing.T) {
	asOf := dates.MustParseISO("2026-11-21")
	for _, c := range []struct {
		d    int
		want ageBucket
	}{{0, bucket0}, {30, bucket0}, {31, bucket31}, {60, bucket31}, {61, bucket61}, {90, bucket61}, {91, bucket90p}} {
		if bucketOf(c.d) != c.want {
			t.Errorf("bucketOf(%d)", c.d)
		}
	}
	if daysBetween(dates.MustParseISO("2026-10-22"), asOf) != 30 {
		t.Error("daysBetween")
	}
	rows, totals := buildAgeing([]openSale{
		{"g", "GOOD LIFE GROUP LTD", dates.MustParseISO("2026-11-20"), 100_000},
		{"g", "GOOD LIFE GROUP LTD", dates.MustParseISO("2026-08-01"), 50_000},
		{"z", "ZHONGTAI", dates.MustParseISO("2026-09-30"), 400_000},
		{"d", "DELTA ONE LTD", dates.MustParseISO("2026-09-01"), 0},
	}, asOf)
	if len(rows) != 2 || rows[0].CustomerID != "z" || rows[1].CustomerID != "g" {
		t.Fatalf("order: %+v", rows)
	}
	if rows[0].D31_60 != 400_000 || rows[0].Total != 400_000 || rows[0].OpenSales != 1 {
		t.Errorf("row z %+v", rows[0])
	}
	g := rows[1]
	if g.D0_30 != 100_000 || g.D90p != 50_000 || g.Total != 150_000 || g.OpenSales != 2 || dates.ToISO(*g.OldestDate) != "2026-08-01" {
		t.Errorf("row g %+v", g)
	}
	want := ageingTotals{buckets: buckets{D0_30: 100_000, D31_60: 400_000, D61_90: 0, D90p: 50_000, Total: 550_000}, OpenSales: 3}
	if totals != want {
		t.Errorf("totals %+v", totals)
	}
}

func TestStatement(t *testing.T) {
	rows, debit, credit, closing := buildStatement(200, []statementEntry{
		{Date: dates.MustParseISO("2026-11-05"), Kind: "PAYMENT", Reference: "SO-1", Credit: 300},
		{Date: dates.MustParseISO("2026-11-05"), Kind: "SALE", Reference: "SO-2", Debit: 1000},
		{Date: dates.MustParseISO("2026-11-01"), Kind: "SALE", Reference: "SO-1", Debit: 500},
	})
	type rb struct {
		ref string
		bal int64
	}
	var got []rb
	for _, r := range rows {
		got = append(got, rb{r.Reference, r.Balance})
	}
	if !reflect.DeepEqual(got, []rb{{"SO-1", 700}, {"SO-2", 1700}, {"SO-1", 1400}}) {
		t.Fatalf("rows %v", got)
	}
	if debit != 1500 || credit != 300 || closing != 1400 {
		t.Fatalf("totals %d %d %d", debit, credit, closing)
	}
}
