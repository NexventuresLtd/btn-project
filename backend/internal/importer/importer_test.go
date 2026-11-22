package importer_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	. "btn-ops/internal/importer"
)

var nov = ExpectedMonth{Year: 2026, Month: 11}

func cell(iso string) time.Time { t, _ := time.Parse("2006-01-02", iso); return t }

func TestParseLegacyDate(t *testing.T) {
	t.Run("swaps day and month of real date cells", func(t *testing.T) {
		a := ParseLegacyDate(cell("2026-01-11"), nov)
		if !a.OK || a.ISO != "2026-11-01" || !a.Swapped {
			t.Fatalf("%+v", a)
		}
		b := ParseLegacyDate(cell("2026-12-11"), nov)
		if !b.OK || b.ISO != "2026-11-12" || !b.Swapped {
			t.Fatalf("%+v", b)
		}
	})
	t.Run("parses dd/mm/yyyy text", func(t *testing.T) {
		if p := ParseLegacyDate("13/11/2026", nov); !p.OK || p.ISO != "2026-11-13" {
			t.Fatalf("%+v", p)
		}
		if p := ParseLegacyDate(" 30/11/2026 ", nov); !p.OK || p.ISO != "2026-11-30" {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("flags dates outside the report month with literal meaning and day hint", func(t *testing.T) {
		if p := ParseLegacyDate(cell("2026-03-10"), nov); p.OK || p.ISO != "2026-10-03" || p.DayHint != 3 {
			t.Fatalf("%+v", p)
		}
		if p := ParseLegacyDate("25/10/2026", nov); p.OK || p.ISO != "2026-10-25" || p.DayHint != 25 {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("flags typos and junk without guessing", func(t *testing.T) {
		if p := ParseLegacyDate("3/111/2026", nov); p.OK || p.ISO != "" || p.DayHint != 3 || !strings.Contains(p.Reason, "typo") {
			t.Fatalf("%+v", p)
		}
		if p := ParseLegacyDate("31/11/2026", nov); p.OK || p.ISO != "" {
			t.Fatalf("%+v", p)
		}
		if p := ParseLegacyDate(nil, nov); p.OK || p.Reason != "missing date" {
			t.Fatalf("%+v", p)
		}
		if p := ParseLegacyDate("hello", nov); p.OK || p.DayHint != 0 {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("takes a date cell literally when the swap is impossible", func(t *testing.T) {
		if p := ParseLegacyDate(cell("2026-11-20"), nov); !p.OK || p.ISO != "2026-11-20" || p.Swapped {
			t.Fatalf("%+v", p)
		}
	})
}

func TestResolveLedgerDates(t *testing.T) {
	p := func(v any) ParsedDate { return ParseLegacyDate(v, nov) }
	isos := func(r []ResolvedDate) (iso []string, flagged []bool) {
		for _, x := range r {
			iso, flagged = append(iso, x.ISO), append(flagged, x.Flagged)
		}
		return
	}
	t.Run("keeps the day and sets the report month when that fits the sheet order", func(t *testing.T) {
		iso, fl := isos(ResolveLedgerDates([]ParsedDate{p("3/111/2026"), p(cell("2026-03-10")), p(cell("2026-04-11"))}, nov))
		if !reflect.DeepEqual(iso, []string{"2026-11-03", "2026-11-03", "2026-11-04"}) || !reflect.DeepEqual(fl, []bool{true, true, false}) {
			t.Fatal(iso, fl)
		}
	})
	t.Run("falls back to the preceding row when the corrected date would go backwards", func(t *testing.T) {
		iso, _ := isos(ResolveLedgerDates([]ParsedDate{p(cell("2026-04-11")), p(cell("2026-03-10")), p(cell("2026-05-10")), p(cell("2026-11-11"))}, nov))
		if !reflect.DeepEqual(iso, []string{"2026-11-04", "2026-11-04", "2026-11-05", "2026-11-11"}) {
			t.Fatal(iso)
		}
	})
	t.Run("uses the following row at the top and empty when nothing is usable", func(t *testing.T) {
		if r := ResolveLedgerDates([]ParsedDate{p("junk"), p("14/11/2026")}, nov); r[0].ISO != "2026-11-14" {
			t.Fatal(r)
		}
		if r := ResolveLedgerDates([]ParsedDate{p("junk")}, nov); r[0].ISO != "" || !r[0].Flagged {
			t.Fatal(r)
		}
	})
}

func TestMonthYearFromFileNames(t *testing.T) {
	for in, want := range map[string]int{"BTN PETTY CASH NOVEMEBER.xlsx": 11, "WAGES BTN 2 FROM 1 ST NOVEMBER UP TO 15.xlsx": 11, "BLOCK REPORT BTN 1": 0} {
		if got := MonthFromText(in); got != want {
			t.Errorf("%s: %d want %d", in, got, want)
		}
	}
	if YearFromText("BLOCK REPORT BTN 3 ON 14 NOVEMBER 2026.xlsx") != 2026 {
		t.Error("year")
	}
}

func TestInferPettyCashCategory(t *testing.T) {
	for _, c := range []struct{ desc, cat, plant string }{
		{"WAGES BTN 1", "WAGES", "BTN-1"}, {"WAGES BTN 3", "WAGES", "BTN-3"}, {"AMAN MONTLY SALARY", "SALARY", ""},
		{"TRANSPORT FROM BTN 3 TO BTN 2 1NE BTN 1", "TRANSPORT", ""}, {"TRANSPORT TO BRING STEEL FROM BTN 3 TO BTN 2", "TRANSPORT", ""},
		{"FUEL OF GM CAR", "FUEL", ""}, {"BTN 1 INTERNET", "INTERNET_COMMS", "BTN-1"}, {"MONTHLY LUNCH BTN 1,BTN 2,BTN 3", "MEALS", ""},
		{"BTN 1 TWO NIGHT SHIFT", "NIGHT_SHIFT", "BTN-1"}, {"REMAINING MONEY TO PAY STEEL", "MATERIALS", ""}, {"STATIONERY", "OTHER", ""},
	} {
		got := InferPettyCashCategory(c.desc)
		if got.Category != c.cat || got.PlantCode != c.plant {
			t.Errorf("%s -> %+v, want %s/%s", c.desc, got, c.cat, c.plant)
		}
	}
	if got := PlantsMentioned("btn-2 and BTN2 and btn 2"); !reflect.DeepEqual(got, []string{"BTN-2"}) {
		t.Error(got)
	}
	if !IsDepositDescription("RECEIVED") || IsDepositDescription("WAGES BTN 1") {
		t.Error("deposit")
	}
}

func sale(number, date string, lines ...SaleLinePlan) SalePlan {
	for i := range lines {
		lines[i].UnitPrice, lines[i].Amount = 1, lines[i].Quantity
	}
	return SalePlan{Number: number, PlantCode: "BTN-2", CustomerName: "X", Date: date, InvoiceType: "EBM", Lines: lines}
}

func TestSalesHelpers(t *testing.T) {
	if dn, rep := NormalizeDn(float64(841842843)); dn != "841,842,843" || !rep {
		t.Error(dn, rep)
	}
	if dn, rep := NormalizeDn(float64(839)); dn != "839" || rep {
		t.Error(dn, rep)
	}
	if dn, _ := NormalizeDn("11, 13,15"); dn != "11,13,15" {
		t.Error(dn)
	}
	if PaymentMethodFor("MOMO CODE") != "MOMO" || PaymentMethodFor("BK") != "BANK_BK" || PaymentMethodFor("") != "" {
		t.Error("payment")
	}
	if NormalizeCustomer("  zhongtai   ltd ") != "ZHONGTAI LTD" {
		t.Error("customer")
	}

	t.Run("renumbers duplicate or empty DNs and groups lines sharing a DN", func(t *testing.T) {
		all := []SalePlan{
			sale("S1", "2026-11-12", SaleLinePlan{ProductCode: "HB15", Quantity: 1, DN: "295,296", Row: 15}, SaleLinePlan{ProductCode: "HB20", Quantity: 1, DN: "295,296", Row: 16}),
			sale("S2", "2026-11-14", SaleLinePlan{ProductCode: "HB15", Quantity: 1, DN: "295,296", Row: 17}),
			sale("S3", "2026-11-14", SaleLinePlan{ProductCode: "HB15", Quantity: 1, DN: "", Row: 18}),
		}
		renamed := AssignDeliveries(all, map[string]bool{})
		if !reflect.DeepEqual(all[0].Deliveries, []DeliveryPlan{{"295,296", []int{0, 1}}}) {
			t.Error(all[0].Deliveries)
		}
		if all[1].Deliveries[0].DNNumber != "IMP-DN-BTN2-R017" || all[2].Deliveries[0].DNNumber != "IMP-DN-BTN2-R018" || len(renamed) != 2 {
			t.Error(all[1].Deliveries, all[2].Deliveries, renamed)
		}
	})

	t.Run("moves a generic invoice item to the variant the STOCK sheet issued", func(t *testing.T) {
		issues := []StockIssue{{"BTN-2", "HB15-2H", "2026-11-02", 350}}
		s := []SalePlan{sale("S1", "2026-11-02", SaleLinePlan{ProductCode: "HB15", Quantity: 350, DN: "1", Row: 3})}
		if ch := AlignSaleProducts(s, issues); len(ch) != 1 || s[0].Lines[0].ProductCode != "HB15-2H" {
			t.Error(ch, s)
		}
		u := []SalePlan{sale("S2", "2026-11-02", SaleLinePlan{ProductCode: "HB15", Quantity: 300, DN: "2", Row: 4})}
		if ch := AlignSaleProducts(u, issues); len(ch) != 0 {
			t.Error(ch)
		}
	})
}

func TestRelabelByPosition(t *testing.T) {
	rows := []LabelRow{
		{2, "2026-11-01", "HB20"}, {3, "2026-11-01", "HB15-2H"}, {4, "2026-11-01", "HB15-3H"},
		{5, "2026-11-02", "HB20"}, {6, "2026-11-02", "HB15"}, {7, "2026-11-02", "HB15"},
	}
	RelabelByPosition(rows, nil, "STOCK")
	var got []string
	for _, r := range rows {
		got = append(got, r.ProductCode)
	}
	if !reflect.DeepEqual(got, []string{"HB20", "HB15-2H", "HB15-3H", "HB20", "HB15-2H", "HB15-3H"}) {
		t.Fatal(got)
	}
}

func TestPlanMaterialTransfers(t *testing.T) {
	row := func(plant, date string, mod func(*RawMaterialRow)) RawMaterialRow {
		r := RawMaterialRow{File: plant + ".xlsx", Row: 1, PlantCode: plant, Material: "CEMENT", Date: date}
		mod(&r)
		return r
	}
	t.Run("nets same-day transfers out of the receiver's Entered stock", func(t *testing.T) {
		p := PlanMaterial([]RawMaterialRow{
			row("BTN-3", "2026-11-04", func(r *RawMaterialRow) { r.Opening, r.GoesTo, r.Closing = 81, []PlantQty{{"BTN-1", 20}}, 61 }),
			row("BTN-1", "2026-11-04", func(r *RawMaterialRow) { r.Entered, r.Closing = 20, 20 }),
			row("BTN-1", "2026-11-13", func(r *RawMaterialRow) { r.Entered, r.Closing = 120, 140 }),
			row("BTN-3", "2026-11-13", func(r *RawMaterialRow) { r.GoesTo, r.Closing = []PlantQty{{"BTN-1", 20}}, 41 }),
		})
		if len(p.Transfers) != 2 || p.Transfers[0].Outcome != "exact" || p.Transfers[1].Outcome != "partial" {
			t.Fatal(p.Transfers)
		}
		var receipts []MaterialMovePlan
		var pair []MaterialMovePlan
		for _, m := range p.Moves {
			if m.Type == "RECEIPT" {
				receipts = append(receipts, m)
			}
			if m.TransferGroup != "" && m.Date == "2026-11-04" {
				pair = append(pair, m)
			}
		}
		if len(receipts) != 1 || receipts[0].PlantCode != "BTN-1" || receipts[0].Date != "2026-11-13" || receipts[0].Quantity != 100 {
			t.Fatal(receipts)
		}
		if len(pair) != 2 || pair[0].Type != "TRANSFER_OUT" || pair[0].Quantity != -20 || pair[1].Type != "TRANSFER_IN" || pair[1].Quantity != 20 ||
			pair[0].CounterpartPlantCode != "BTN-1" || pair[1].CounterpartPlantCode != "BTN-3" || pair[0].TransferGroup != pair[1].TransferGroup {
			t.Fatal(pair)
		}
		if pair[0].File != "BTN-3.xlsx" || pair[1].File != "BTN-3.xlsx" { // both legs are written by the sender's file
			t.Fatal("legs must belong to the sender's file")
		}
	})
	t.Run("reports a transfer the receiver never entered", func(t *testing.T) {
		p := PlanMaterial([]RawMaterialRow{
			row("BTN-1", "2026-11-06", func(r *RawMaterialRow) { r.Opening, r.GoesTo = 20, []PlantQty{{"BTN-2", 18}} }),
			row("BTN-2", "2026-11-06", func(r *RawMaterialRow) {}),
		})
		if p.Transfers[0].Outcome != "unmatched" || p.Transfers[0].ReceiptWritten != 0 {
			t.Fatal(p.Transfers)
		}
	})
}

func TestMasking(t *testing.T) {
	for in, want := range map[string]string{"1199880012345678": "119***********78", "0791234567": "079*****67"} {
		if got := MaskNumber(in); got != want {
			t.Errorf("%s -> %s", in, got)
		}
	}
	if got := MaskDigitsInText("paid 0791234567 on 4 Nov, 2,474,350 RWF"); got != "paid 079*****67 on 4 Nov, 2,474,350 RWF" {
		t.Error(got)
	}
	if MaskName("musoni ntwari paterne") != "MUS. N. P." || MaskName("") != "—" || MaskNumber("") != "—" || MaskNumber("123") != "***" {
		t.Error("name")
	}
	if NormalizeMsisdn(float64(791234567)) != "0791234567" || NormalizeMsisdn("250791234567") != "0791234567" {
		t.Error("msisdn")
	}
}

func TestCompareMomoName(t *testing.T) {
	for _, c := range []struct{ momo, worker, want string }{
		{"paterne", "MUSONI NTWARI PATERNE", "match"},
		{"PARTICK", "MBARUSHIMANA PATRICK", "variant"},
		{"JEA DE DIEU", "MAZIMPAKA J DE DIEU", "variant"},
		{"VIRIGINIE", "ZIRIKAGUSENGA J DAMOUR", "different"},
	} {
		if got := CompareMomoName(c.momo, c.worker); got != c.want {
			t.Errorf("%s vs %s: %s want %s", c.momo, c.worker, got, c.want)
		}
	}
}

func TestFmtN(t *testing.T) {
	for in, want := range map[float64]string{0: "0", 1234567: "1,234,567", -1500: "-1,500", 12.5: "12.5", 1234.5678: "1,234.568", 999: "999"} {
		if got := FmtN(in); got != want {
			t.Errorf("%v -> %s want %s", in, got, want)
		}
	}
}

func TestJobTitleAndNationalID(t *testing.T) {
	if JobTitleOf("") != "Helper" || JobTitleOf("HELPERS") != "Helper" || JobTitleOf("mason assistant") != "Mason Assistant" {
		t.Error("job")
	}
	if NormalizeNationalID(float64(1199880012345678)) != "1199880012345678" || NormalizeNationalID("1 1998 8001") != "119988001" {
		t.Error("nid")
	}
}
