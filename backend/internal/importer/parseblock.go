package importer

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"btn-ops/internal/catalog"
)

// SheetCtx is what the block-report sheet parsers need.
type SheetCtx struct {
	File      string
	PlantCode string
	Exp       ExpectedMonth
	Today     string
	Add       AddFn
}

func sheetNote(file, sheet string, row int) string {
	return fmt.Sprintf("Imported from %s / %s / row %d", file, sheet, row)
}

type datedRow struct {
	idx  int // index into the candidate slice
	date string
}

// datedRows is the shared row walk: date repair over the rows that carry an item description.
func datedRows(parsed []ParsedDate, rowNums []int, ctx SheetCtx, sheet string) []datedRow {
	resolved := ResolveLedgerDates(parsed, ctx.Exp)
	var out []datedRow
	for i, res := range resolved {
		if res.Flagged {
			sev, act := "error", "row skipped"
			if res.ISO != "" {
				sev, act = "warning", "row kept, dated "+res.ISO
			}
			note := res.Note
			if note == "" {
				note = "unusable date"
			}
			ctx.Add(Anomaly{Sheet: sheet, Row: rowNums[i], Severity: sev, Kind: "date", Message: note, Action: act})
		}
		if res.ISO != "" {
			out = append(out, datedRow{i, res.ISO})
		}
	}
	return out
}

// ───────────────────────────── STOCK ─────────────────────────────

type RawStockRow struct {
	Row         int
	ProductCode string
	Date        string
	Opening     float64
	Flow        [6]float64 // production, damage1, issue, returned, sample, damage2 (stockFlows order)
	Closing     float64
}

func (r RawStockRow) Issue() float64 { return r.Flow[2] }

var stockFlows = []struct {
	header, typ string
	sign        int
}{
	{"PRODUCTION", "PRODUCTION", 1},
	{"DAMAGE 1", "DAMAGE_PRODUCTION", -1},
	{"ISSUE SOLD", "SALE_ISSUE", -1},
	{"RETURNED", "RETURN", 1},
	{"SAMPLE", "SAMPLE", -1},
	{"DAMAGE 2", "DAMAGE_HANDLING", -1},
}

func ParseStockSheet(ws *Sheet, ctx SheetCtx) (rows []RawStockRow, ignored int) {
	sheet := ws.Name
	header := FindHeader(ws, []string{"DATE", "DESCRIPTION", "OPENING STOCK", "CLOSING STOCK"})
	if header == nil {
		ctx.Add(Anomaly{Sheet: sheet, Severity: "error", Kind: "layout", Message: "header row (Date / Description / Opening Stock / Closing Stock) not found", Action: "sheet skipped"})
		return nil, 0
	}
	var missing []string
	for _, f := range stockFlows {
		if header.Col(f.header) == 0 {
			missing = append(missing, f.header)
		}
	}
	if len(missing) > 0 {
		ctx.Add(Anomaly{Sheet: sheet, Severity: "warning", Kind: "layout", Message: "columns not found: " + strings.Join(missing, ", "), Action: "treated as zero"})
	}
	var cands []RawStockRow
	var parsed []ParsedDate
	var rowNums []int
	for r := header.Row + 1; r <= ws.RowCount(); r++ {
		v := func(h string) any {
			if c := header.Col(h); c != 0 {
				return ws.Cell(r, c)
			}
			return nil
		}
		desc := Text(v("DESCRIPTION"))
		var raw RawStockRow
		raw.Opening, raw.Closing = Num(v("OPENING STOCK")), Num(v("CLOSING STOCK"))
		anyFlow := false
		for i, f := range stockFlows {
			raw.Flow[i] = Num(v(f.header))
			if raw.Flow[i] != 0 {
				anyFlow = true
			}
		}
		if desc == "" {
			if anyFlow {
				ctx.Add(Anomaly{Sheet: sheet, Row: r, Severity: "warning", Kind: "row", Message: "quantities on a row without a product description", Action: "row skipped"})
			} else {
				ignored++
			}
			continue
		}
		code := catalog.ProductCodeFor(desc)
		if code == "" {
			ctx.Add(Anomaly{Sheet: sheet, Row: r, Severity: "error", Kind: "product", Message: fmt.Sprintf("unknown product \"%s\"", desc), Action: "row skipped"})
			continue
		}
		raw.Row, raw.ProductCode = r, code
		cands = append(cands, raw)
		parsed = append(parsed, ParseLegacyDate(v("DATE"), ctx.Exp))
		rowNums = append(rowNums, r)
	}
	for _, d := range datedRows(parsed, rowNums, ctx, sheet) {
		row := cands[d.idx]
		row.Date = d.date
		rows = append(rows, row)
	}
	labels := make([]LabelRow, len(rows))
	for i, r := range rows {
		labels[i] = LabelRow{r.Row, r.Date, r.ProductCode}
	}
	RelabelByPosition(labels, ctx.Add, sheet)
	for i := range rows {
		rows[i].ProductCode = labels[i].ProductCode
	}

	// Row arithmetic and day-to-day continuity, per product in sheet order.
	type lc struct {
		closing float64
		row     int
	}
	last := map[string]lc{}
	for _, r := range rows {
		f := r.Flow
		expected := r.Opening + f[0] - f[1] - f[2] + f[3] - f[4] - f[5]
		if expected != r.Closing {
			ctx.Add(Anomaly{Sheet: sheet, Row: r.Row, Severity: "warning", Kind: "arithmetic", Message: fmt.Sprintf("%s %s: closing %s ≠ opening + movements = %s", r.ProductCode, r.Date, FmtNum(r.Closing), FmtNum(expected)), Action: "movements imported as recorded; see reconciliation"})
		}
		if prev, ok := last[r.ProductCode]; ok && prev.closing != r.Opening {
			ctx.Add(Anomaly{Sheet: sheet, Row: r.Row, Severity: "warning", Kind: "continuity", Message: fmt.Sprintf("%s %s: opening %s ≠ previous closing %s (row %d)", r.ProductCode, r.Date, FmtNum(r.Opening), FmtNum(prev.closing), prev.row), Action: "the ledger carries the previous closing forward; see reconciliation"})
		}
		for i, fl := range stockFlows {
			if f[i] < 0 {
				ctx.Add(Anomaly{Sheet: sheet, Row: r.Row, Severity: "warning", Kind: "value", Message: fmt.Sprintf("negative %s (%s) for %s", fl.header, FmtNum(f[i]), r.ProductCode), Action: "imported with its sign"})
			}
		}
		if r.Date > ctx.Today {
			anyFlow := false
			for _, x := range f {
				if x != 0 {
					anyFlow = true
				}
			}
			if anyFlow {
				ctx.Add(Anomaly{Sheet: sheet, Row: r.Row, Severity: "warning", Kind: "date", Message: fmt.Sprintf("movement dated in the future (%s)", r.Date), Action: "imported as recorded"})
			}
		}
		last[r.ProductCode] = lc{r.Closing, r.Row}
	}
	return rows, ignored
}

// LabelRow is the part of a stock row RelabelByPosition works on.
type LabelRow struct {
	Row         int
	Date        string
	ProductCode string
}

// RelabelByPosition: the sheets list the same products in the same order every day. Some days use a
// generic label for a variant (BTN 2 wrote "HOLLOW BLOCK 15" for both the two-hole and three-hole
// rows from 3 to 14 Nov). When a day's block contains the same product twice and has the usual number
// of rows, the duplicated rows take the product of their position — but only within the same family
// (HB15 -> HB15-2H / HB15-3H). Every relabel is reported. add may be nil.
func RelabelByPosition(rows []LabelRow, add AddFn, sheet string) {
	var blocks [][]int
	for i, r := range rows {
		if n := len(blocks); n > 0 && rows[blocks[n-1][0]].Date == r.Date {
			blocks[n-1] = append(blocks[n-1], i)
		} else {
			blocks = append(blocks, []int{i})
		}
	}
	distinct := func(b []int) bool {
		seen := map[string]bool{}
		for _, i := range b {
			seen[rows[i].ProductCode] = true
		}
		return len(seen) == len(b)
	}
	var canonical []string
	for _, b := range blocks {
		if distinct(b) {
			for _, i := range b {
				canonical = append(canonical, rows[i].ProductCode)
			}
			break
		}
	}
	if canonical == nil {
		return
	}
	type change struct {
		key  string
		rows []int
	}
	var changes []change
	for _, b := range blocks {
		if distinct(b) || len(b) != len(canonical) {
			continue
		}
		for pos, i := range b {
			target := canonical[pos]
			cur := rows[i].ProductCode
			if cur == target {
				continue
			}
			if !(strings.HasPrefix(target, cur+"-") || strings.HasPrefix(cur, target+"-")) {
				continue
			}
			key := cur + " → " + target
			found := false
			for k := range changes {
				if changes[k].key == key {
					changes[k].rows = append(changes[k].rows, rows[i].Row)
					found = true
				}
			}
			if !found {
				changes = append(changes, change{key, []int{rows[i].Row}})
			}
			rows[i].ProductCode = target
		}
	}
	if add == nil {
		return
	}
	for _, c := range changes {
		var nums []string
		for _, n := range c.rows {
			nums = append(nums, fmt.Sprint(n))
		}
		add(Anomaly{Sheet: sheet, Row: c.rows[0], Severity: "warning", Kind: "product", Message: fmt.Sprintf("generic label read by position %s on rows %s (the day lists the same product twice)", c.key, strings.Join(nums, ", ")), Action: "relabelled; confirmed by the continuity and closing checks"})
	}
	for _, b := range blocks {
		if !distinct(b) {
			add(Anomaly{Sheet: sheet, Row: rows[b[0]].Row, Severity: "error", Kind: "product", Message: fmt.Sprintf("%s: the same product appears twice and could not be resolved by position", rows[b[0]].Date), Action: "rows imported as labelled; expect reconciliation differences"})
		}
	}
}

// PlanStock turns stock rows into ledger movements and closing checks.
func PlanStock(rows []RawStockRow, file, plantCode, sheet string) (moves []StockMovePlan, closings []ClosingCheck) {
	var order []string
	by := map[string][]RawStockRow{}
	for _, r := range rows {
		if _, ok := by[r.ProductCode]; !ok {
			order = append(order, r.ProductCode)
		}
		by[r.ProductCode] = append(by[r.ProductCode], r)
	}
	for _, code := range order {
		sorted := append([]RawStockRow(nil), by[code]...)
		sort.SliceStable(sorted, func(i, j int) bool {
			if sorted[i].Date != sorted[j].Date {
				return sorted[i].Date < sorted[j].Date
			}
			return sorted[i].Row < sorted[j].Row
		})
		first := sorted[0]
		if first.Opening != 0 {
			moves = append(moves, StockMovePlan{plantCode, code, first.Date, "OPENING", int(first.Opening), sheetNote(file, sheet, first.Row) + " (opening stock)"})
		}
		for _, r := range sorted {
			for i, f := range stockFlows {
				q := r.Flow[i]
				if q == 0 {
					continue
				}
				moves = append(moves, StockMovePlan{plantCode, code, r.Date, f.typ, int(jsRound(q)) * f.sign, sheetNote(file, sheet, r.Row)})
			}
		}
		last := sorted[len(sorted)-1]
		closings = append(closings, ClosingCheck{plantCode, code, last.Date, last.Closing})
	}
	return
}

// ───────────────────────────── MATERIAL ─────────────────────────────

var goesToRe = regexp.MustCompile(`^GOES TO BTN ?(\d)$`)

func ParseMaterialSheet(ws *Sheet, ctx SheetCtx) (rows []RawMaterialRow, ignored int) {
	sheet := ws.Name
	header := FindHeader(ws, []string{"DATE", "DESCRIPTION", "OPENING STOCK"})
	if header == nil {
		ctx.Add(Anomaly{Sheet: sheet, Severity: "error", Kind: "layout", Message: "header row not found", Action: "sheet skipped"})
		return nil, 0
	}
	colOf := func(names ...string) int {
		for _, n := range names {
			if c := header.Col(n); c != 0 {
				return c
			}
		}
		return 0
	}
	cEntered := colOf("ENTERED STOCK", "RECEIVED", "ENTERED")
	cUsed := colOf("USED", "CONSUMED")
	cClosing := colOf("CL STOCK", "CLOSING STOCK")
	type tcol struct {
		plant string
		col   int
	}
	var transferCols []tcol
	for _, h := range header.Order {
		if m := goesToRe.FindStringSubmatch(h); m != nil {
			transferCols = append(transferCols, tcol{"BTN-" + m[1], header.Cols[h]})
		}
	}
	for _, t := range transferCols {
		if t.plant == ctx.PlantCode {
			ctx.Add(Anomaly{Sheet: sheet, Severity: "warning", Kind: "layout", Message: fmt.Sprintf("column \"GOES TO %s\" on %s's own sheet", t.plant, ctx.PlantCode), Action: "column ignored"})
		}
	}
	val := func(r, c int) any {
		if c == 0 {
			return nil
		}
		return ws.Cell(r, c)
	}
	var cands []RawMaterialRow
	var parsed []ParsedDate
	var rowNums []int
	for r := header.Row + 1; r <= ws.RowCount(); r++ {
		desc := Text(val(r, header.Col("DESCRIPTION")))
		var goes []PlantQty
		anyGoes := false
		for _, t := range transferCols {
			if t.plant != ctx.PlantCode {
				q := Num(val(r, t.col))
				goes = append(goes, PlantQty{t.plant, q})
				if q != 0 {
					anyGoes = true
				}
			}
		}
		entered, used := Num(val(r, cEntered)), Num(val(r, cUsed))
		hasFlow := entered != 0 || used != 0 || anyGoes
		if desc == "" {
			if hasFlow {
				ctx.Add(Anomaly{Sheet: sheet, Row: r, Severity: "warning", Kind: "row", Message: "quantities on a row without a material description", Action: "row skipped"})
			} else {
				ignored++
			}
			continue
		}
		code := catalog.MaterialCodeFor(desc)
		if code == "" {
			ctx.Add(Anomaly{Sheet: sheet, Row: r, Severity: "error", Kind: "material", Message: fmt.Sprintf("unknown material \"%s\"", desc), Action: "row skipped"})
			continue
		}
		cands = append(cands, RawMaterialRow{
			File: ctx.File, Row: r, PlantCode: ctx.PlantCode, Material: code,
			Opening: Round3(Num(val(r, header.Col("OPENING STOCK")))), Entered: Round3(entered), GoesTo: goes,
			Used: Round3(used), Closing: Round3(Num(val(r, cClosing))),
		})
		parsed = append(parsed, ParseLegacyDate(val(r, header.Col("DATE")), ctx.Exp))
		rowNums = append(rowNums, r)
	}
	for _, d := range datedRows(parsed, rowNums, ctx, sheet) {
		row := cands[d.idx]
		row.Date = d.date
		rows = append(rows, row)
	}
	type lc struct {
		closing float64
		row     int
	}
	last := map[string]lc{}
	for _, r := range rows {
		out := 0.0
		for _, g := range r.GoesTo {
			out += g.Qty
		}
		expected := Round3(r.Opening + r.Entered - out - r.Used)
		if expected != r.Closing {
			ctx.Add(Anomaly{Sheet: sheet, Row: r.Row, Severity: "warning", Kind: "arithmetic", Message: fmt.Sprintf("%s %s: closing %s ≠ opening + movements = %s", r.Material, r.Date, FmtNum(r.Closing), FmtNum(expected)), Action: "movements imported as recorded; see reconciliation"})
		}
		if prev, ok := last[r.Material]; ok && prev.closing != r.Opening {
			ctx.Add(Anomaly{Sheet: sheet, Row: r.Row, Severity: "warning", Kind: "continuity", Message: fmt.Sprintf("%s %s: opening %s ≠ previous closing %s (row %d)", r.Material, r.Date, FmtNum(r.Opening), FmtNum(prev.closing), prev.row), Action: "the ledger carries the previous closing forward; see reconciliation"})
		}
		last[r.Material] = lc{r.Closing, r.Row}
	}
	return rows, ignored
}

// ───────────────────────────── SALES ─────────────────────────────

type RawSaleRow struct {
	Row                            int
	Date, ProductCode, InvoiceType string
	ReceiptNo, InvoiceNo           string // "" = none
	DN, DNNote                     string
	CustomerName                   string
	UnitPrice, Quantity, Amount    int
	Deposit                        string
}

var spaces = regexp.MustCompile(`\s+`)

// NormalizeCustomer: trim, collapse spaces, uppercase (same rule as the master-data API).
func NormalizeCustomer(s string) string {
	return strings.ToUpper(strings.TrimSpace(spaces.ReplaceAllString(s, " ")))
}

var commaSpaces = regexp.MustCompile(`\s*,\s*`)

// NormalizeDn: DN cells typed "841,842,843" were stored by Excel as the number 841842843 (comma read
// as thousands separator). Such numbers are split back into 3-digit groups.
func NormalizeDn(value any) (dn string, repaired bool) {
	if n, ok := value.(float64); ok && n == float64(int64(n)) {
		s := FmtNum(n)
		if len(s) > 3 && len(s)%3 == 0 {
			var parts []string
			for i := 0; i < len(s); i += 3 {
				parts = append(parts, s[i:i+3])
			}
			return strings.Join(parts, ","), true
		}
		return s, false
	}
	return commaSpaces.ReplaceAllString(Text(value), ","), false
}

var (
	momoRe = regexp.MustCompile(`\bMOMO\b|MOBILE MONEY`)
	bkRe   = regexp.MustCompile(`^BK\b|BANK OF KIGALI`)
	cashRe = regexp.MustCompile(`\bCASH\b`)
	chqRe  = regexp.MustCompile(`\bCHEQUE\b|\bCHECK\b`)
	bankRe = regexp.MustCompile(`\bBANK\b`)
)

// PaymentMethodFor maps the DEPOSIT column to a payment method ("" = none).
func PaymentMethodFor(deposit string) string {
	d := normDesc(deposit)
	switch {
	case d == "":
		return ""
	case momoRe.MatchString(d):
		return "MOMO"
	case bkRe.MatchString(d):
		return "BANK_BK"
	case cashRe.MatchString(d):
		return "CASH"
	case chqRe.MatchString(d):
		return "CHEQUE"
	case bankRe.MatchString(d):
		return "BANK_OTHER"
	}
	return ""
}

func ParseSalesSheet(ws *Sheet, ctx SheetCtx) (rows []RawSaleRow, ignored int) {
	sheet := ws.Name
	header := FindHeader(ws, []string{"DATE", "ITEMS", "CUSTOMER", "QUANTITY"})
	if header == nil {
		ctx.Add(Anomaly{Sheet: sheet, Severity: "error", Kind: "layout", Message: "header row not found", Action: "sheet skipped"})
		return nil, 0
	}
	c := func(names ...string) int {
		for _, n := range names {
			if x := header.Col(n); x != 0 {
				return x
			}
		}
		return 0
	}
	cDate, cItems, cInvType := c("DATE"), c("ITEMS"), c("INV TYPE")
	cReceipt, cInvoice, cDN := c("RECEIPT NUM", "RECEIPT NO"), c("INVOICE N", "INVOICE NO"), c("DN")
	cCustomer, cPrice, cQty := c("CUSTOMER"), c("UNIT PER PRICE", "UNIT PRICE"), c("QUANTITY")
	cAmount, cDeposit := c("AMOUNT"), c("DEPOSIT")
	var cands []RawSaleRow
	var parsed []ParsedDate
	var rowNums []int
	for r := header.Row + 1; r <= ws.RowCount(); r++ {
		val := func(x int) any {
			if x == 0 {
				return nil
			}
			return ws.Cell(r, x)
		}
		items := Text(val(cItems))
		qty := Num(val(cQty))
		customer := NormalizeCustomer(Text(val(cCustomer)))
		if items == "" && qty == 0 && customer == "" {
			ignored++
			continue
		}
		if items == "" || qty == 0 || customer == "" {
			ctx.Add(Anomaly{Sheet: sheet, Row: r, Severity: "error", Kind: "row", Message: fmt.Sprintf("incomplete sale row (item \"%s\", qty %s, customer \"%s\")", items, FmtNum(qty), customer), Action: "row skipped"})
			continue
		}
		code := catalog.ProductCodeFor(items)
		if code == "" {
			ctx.Add(Anomaly{Sheet: sheet, Row: r, Severity: "error", Kind: "product", Message: fmt.Sprintf("unknown product \"%s\"", items), Action: "row skipped"})
			continue
		}
		dn, repaired := NormalizeDn(val(cDN))
		unitPrice := int(jsRound(Num(val(cPrice))))
		amount := int(jsRound(Num(val(cAmount))))
		if float64(amount) != float64(unitPrice)*qty {
			ctx.Add(Anomaly{Sheet: sheet, Row: r, Severity: "warning", Kind: "arithmetic", Message: fmt.Sprintf("amount %d ≠ price %d × qty %s", amount, unitPrice, FmtNum(qty)), Action: "line stored with quantity × unit price"})
		}
		invType := Text(val(cInvType))
		if invType == "" {
			invType = "EBM"
		}
		q := int(jsRound(qty))
		row := RawSaleRow{
			Row: r, ProductCode: code, InvoiceType: invType, ReceiptNo: Text(val(cReceipt)), InvoiceNo: Text(val(cInvoice)),
			DN: dn, CustomerName: customer, UnitPrice: unitPrice, Quantity: q, Amount: unitPrice * q, Deposit: Text(val(cDeposit)),
		}
		if repaired {
			row.DNNote = fmt.Sprintf("DN cell held the number %s; read as \"%s\"", Text(val(cDN)), dn)
		}
		cands = append(cands, row)
		parsed = append(parsed, ParseLegacyDate(val(cDate), ctx.Exp))
		rowNums = append(rowNums, r)
	}
	for _, d := range datedRows(parsed, rowNums, ctx, sheet) {
		row := cands[d.idx]
		row.Date = d.date
		rows = append(rows, row)
	}
	for _, r := range rows {
		if r.DNNote != "" {
			ctx.Add(Anomaly{Sheet: sheet, Row: r.Row, Severity: "info", Kind: "dn", Message: r.DNNote, Action: "DN repaired"})
		}
		if r.Deposit != "" && PaymentMethodFor(r.Deposit) == "" {
			ctx.Add(Anomaly{Sheet: sheet, Row: r.Row, Severity: "warning", Kind: "payment", Message: fmt.Sprintf("DEPOSIT \"%s\" is not a recognised payment channel", r.Deposit), Action: "sale left unpaid"})
		}
	}
	return rows, ignored
}

// GroupSales: one sale per row, except rows that share date + customer + EBM invoice number (clearly
// one invoice with several lines). Rows without an invoice number stay separate.
func GroupSales(rows []RawSaleRow, ctx SheetCtx, sheet, numberPrefix string) []SalePlan {
	var keys []string
	groups := map[string][]RawSaleRow{}
	for _, r := range rows {
		key := fmt.Sprintf("row-%d", r.Row)
		if r.InvoiceNo != "" {
			key = r.Date + "|" + r.CustomerName + "|" + r.InvoiceNo
		}
		if _, ok := groups[key]; !ok {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], r)
	}
	var sales []SalePlan
	for _, k := range keys {
		list := groups[k]
		first := list[0]
		nums := make([]string, len(list))
		for i, l := range list {
			nums[i] = fmt.Sprint(l.Row)
		}
		if len(list) > 1 {
			ctx.Add(Anomaly{Sheet: sheet, Row: first.Row, Severity: "info", Kind: "grouping", Message: fmt.Sprintf("rows %s share date, customer and invoice %s", strings.Join(nums, ", "), first.InvoiceNo), Action: "imported as one sale with several lines"})
		}
		var deposits []string
		seen := map[string]bool{}
		for _, l := range list {
			if l.Deposit != "" && !seen[l.Deposit] {
				seen[l.Deposit] = true
				deposits = append(deposits, l.Deposit)
			}
		}
		method := ""
		if len(deposits) > 0 {
			method = PaymentMethodFor(deposits[0])
		}
		total := 0
		lines := make([]SaleLinePlan, len(list))
		for i, l := range list {
			total += l.Amount
			lines[i] = SaleLinePlan{Row: l.Row, ProductCode: l.ProductCode, Quantity: l.Quantity, UnitPrice: l.UnitPrice, Amount: l.Amount, DN: l.DN}
		}
		plural := ""
		if len(list) > 1 {
			plural = "s"
		}
		s := SalePlan{
			Number: fmt.Sprintf("%s-%03d", numberPrefix, first.Row), PlantCode: ctx.PlantCode, CustomerName: first.CustomerName, Date: first.Date,
			InvoiceType: first.InvoiceType, EbmInvoiceNo: first.InvoiceNo, EbmReceiptNo: first.ReceiptNo,
			Notes: fmt.Sprintf("Imported from %s / %s / row%s %s", ctx.File, sheet, plural, strings.Join(nums, ", ")),
			Lines: lines,
		}
		if method != "" {
			s.Payment = &PaymentPlan{method, total, fmt.Sprintf("IMP: DEPOSIT column \"%s\"", deposits[0])}
		}
		sales = append(sales, s)
	}
	return sales
}
