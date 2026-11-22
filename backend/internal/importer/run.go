package importer

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"btn-ops/internal/dates"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xuri/excelize/v2"
)

// Options are the command-line options of `import`.
type Options struct {
	DryRun  bool
	Replace bool
	Dir     string
	Month   string // "YYYY-MM" override
	Report  string // also save the printed report here
	Today   string // override (tests); default today in Kigali
}

var (
	plantRe  = regexp.MustCompile(`(?i)BTN\s*-?\s*([123])\b`)
	blockRe  = regexp.MustCompile(`(?i)BLOCK\s+REPORT`)
	pettyRe  = regexp.MustCompile(`(?i)PETTY\s*CASH`)
	wagesRe  = regexp.MustCompile(`(?i)WAGES?`)
	upToRe   = regexp.MustCompile(`(?i)UP\s*TO\s*(\d{1,2})\b`)
	toRe     = regexp.MustCompile(`(?i)\bTO\s*(\d{1,2})\b`)
	xlsxRe   = regexp.MustCompile(`(?i)\.xlsx$`)
	monthOpt = regexp.MustCompile(`^\d{4}-\d{2}$`)
)

func plantOf(name string) string {
	if m := plantRe.FindStringSubmatch(name); m != nil {
		return "BTN-" + m[1]
	}
	return ""
}

func kindOf(name string) string {
	switch {
	case blockRe.MatchString(name):
		return "block"
	case pettyRe.MatchString(name):
		return "pettycash"
	case wagesRe.MatchString(name):
		return "wages"
	}
	return ""
}

func short(file string) string {
	s := xlsxRe.ReplaceAllString(file, "")
	if r := []rune(s); len(r) > 34 {
		s = string(r[:34])
	}
	return s
}

// DefaultDir finds data/samples walking up from the working directory.
func DefaultDir() string {
	dir, _ := os.Getwd()
	for i := 0; i < 6 && dir != ""; i++ {
		p := filepath.Join(dir, "data", "samples")
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return filepath.Join("data", "samples")
}

type pettyResult struct {
	file         string
	diffs        []BalanceCheck
	closingSheet *float64
	computed     int
}

type wageResult struct {
	file, plant string
	momo        []MomoNameCase
	totals      Totals
	sheetTotals *Totals
	lines       int
}

type importer struct {
	opts      Options
	rep       *Report
	anomalies []Anomaly
	today     string
}

// Run executes the import and returns the process exit code (0 ok, 1 failed/refused).
func Run(ctx context.Context, pool *pgxpool.Pool, opts Options, out io.Writer) (code int) {
	im := &importer{opts: opts, rep: NewReport(out), today: opts.Today}
	if im.today == "" {
		im.today = dates.TodayISO()
	}
	if opts.Dir == "" {
		opts.Dir = DefaultDir()
	}
	if abs, err := filepath.Abs(opts.Dir); err == nil {
		opts.Dir = abs
	}
	im.opts = opts
	defer func() {
		if opts.Report != "" {
			if err := os.WriteFile(opts.Report, []byte(im.rep.String()+"\n"), 0o600); err != nil {
				fmt.Fprintln(out, "cannot save report:", err)
				code = 1
			} else {
				fmt.Fprintf(out, "\nReport saved to %s\n", opts.Report)
			}
		}
	}()
	if err := im.run(ctx, pool, &code); err != nil {
		im.rep.Line("", "IMPORT FAILED: "+err.Error())
		return 1
	}
	return code
}

func (im *importer) expectedMonthFor(file, title string, fallbackYear int) (ExpectedMonth, bool) {
	if im.opts.Month != "" {
		p := strings.SplitN(im.opts.Month, "-", 2)
		y, _ := strconv.Atoi(p[0])
		m, _ := strconv.Atoi(p[1])
		return ExpectedMonth{y, m}, true
	}
	month := MonthFromText(file)
	if month == 0 {
		month = MonthFromText(title)
	}
	if month == 0 {
		return ExpectedMonth{}, false
	}
	year := YearFromText(file)
	if year == 0 {
		year = YearFromText(title)
	}
	if year == 0 {
		year = fallbackYear
	}
	return ExpectedMonth{year, month}, true
}

func sheetTitle(f *excelize.File, ws *Sheet) string {
	var parts []string
	for r := 1; r <= 3 && r <= ws.RowCount(); r++ {
		cells := ws.NonEmpty(r)
		var cs []int
		for c := range cells {
			cs = append(cs, c)
		}
		sort.Ints(cs)
		for _, c := range cs {
			parts = append(parts, Text(cells[c]))
		}
	}
	return strings.Join(parts, " ")
}

func (im *importer) run(ctx context.Context, pool *pgxpool.Pool, code *int) error {
	opts, rep := im.opts, im.rep
	mode := "IMPORT"
	if opts.DryRun {
		mode = "DRY RUN (nothing will be written)"
	} else if opts.Replace {
		mode = "REPLACE"
	}
	rep.H1("BTN legacy Excel import — " + mode)
	rep.Line("Folder: "+opts.Dir, "Today (Kigali): "+im.today)
	entries, err := os.ReadDir(opts.Dir)
	if err != nil {
		return fmt.Errorf("Folder not found: %s", opts.Dir)
	}
	var files []string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && xlsxRe.MatchString(n) && !strings.HasPrefix(n, "~$") {
			files = append(files, n)
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		return fmt.Errorf("No .xlsx files in %s", opts.Dir)
	}
	fallbackYear, _ := strconv.Atoi(im.today[:4])
	for _, f := range files {
		if y := YearFromText(f); y != 0 {
			fallbackYear = y
			break
		}
	}

	var plans []*FilePlan
	var rawMaterial []RawMaterialRow
	var stockIssues []StockIssue
	var pettyResults []pettyResult
	var wageResults []wageResult

	rep.H2("Files")
	var fileRows [][]string
	for _, file := range files {
		kind, plantCode := kindOf(file), plantOf(file)
		add := func(a Anomaly) { a.File = file; im.anomalies = append(im.anomalies, a) }
		if kind == "" {
			add(Anomaly{Sheet: "-", Severity: "warning", Kind: "file", Message: "file name not recognised (expected BLOCK REPORT / PETTY CASH / WAGES)", Action: "file skipped"})
			fileRows = append(fileRows, []string{file, "?", "-", "-", "skipped"})
			continue
		}
		f, err := excelize.OpenFile(filepath.Join(opts.Dir, file))
		if err != nil {
			add(Anomaly{Sheet: "-", Severity: "error", Kind: "file", Message: "cannot read the workbook: " + err.Error(), Action: "file skipped"})
			fileRows = append(fileRows, []string{file, kind, orDash(plantCode), "?", "skipped"})
			continue
		}
		names := f.GetSheetList()
		sheets := make([]*Sheet, 0, len(names))
		for _, n := range names {
			s, err := OpenSheet(f, n)
			if err != nil {
				f.Close()
				return fmt.Errorf("%s / %s: %w", file, n, err)
			}
			sheets = append(sheets, s)
		}
		title := ""
		if len(sheets) > 0 {
			title = sheetTitle(f, sheets[0])
		}
		exp, ok := im.expectedMonthFor(file, title, fallbackYear)
		if !ok {
			add(Anomaly{Sheet: "-", Severity: "error", Kind: "file", Message: "cannot tell which month the file covers", Action: "file skipped (use --month=YYYY-MM)"})
			fileRows = append(fileRows, []string{file, kind, orDash(plantCode), "?", "skipped"})
			f.Close()
			continue
		}
		monthLabel := fmt.Sprintf("%d-%02d", exp.Year, exp.Month)
		if (kind == "block" || kind == "wages") && plantCode == "" {
			add(Anomaly{Sheet: "-", Severity: "error", Kind: "file", Message: "plant (BTN 1/2/3) not found in the file name", Action: "file skipped"})
			fileRows = append(fileRows, []string{file, kind, "?", monthLabel, "skipped"})
			f.Close()
			continue
		}
		plan := &FilePlan{File: file, Kind: kind, PlantCode: plantCode}
		sheetByName := func(name string) *Sheet {
			for _, s := range sheets {
				if strings.ToUpper(strings.TrimSpace(s.Name)) == name {
					return s
				}
			}
			return nil
		}
		switch kind {
		case "block":
			ctx := SheetCtx{File: file, PlantCode: plantCode, Exp: exp, Today: im.today, Add: add}
			stock, material, sales := sheetByName("STOCK"), sheetByName("MATERIAL"), sheetByName("SALES")
			for _, p := range []struct {
				n string
				s *Sheet
			}{{"STOCK", stock}, {"MATERIAL", material}, {"SALES", sales}} {
				if p.s == nil {
					add(Anomaly{Sheet: p.n, Severity: "error", Kind: "layout", Message: fmt.Sprintf("sheet %s missing", p.n), Action: "nothing imported from it"})
				}
			}
			if stock != nil {
				rows, ignored := ParseStockSheet(stock, ctx)
				plan.Stock, plan.StockClosings = PlanStock(rows, file, plantCode, stock.Name)
				plan.SetRows("STOCK", len(rows))
				plan.SetRows("STOCK ignored", ignored)
				for _, r := range rows {
					if r.Issue() != 0 {
						stockIssues = append(stockIssues, StockIssue{plantCode, r.ProductCode, r.Date, int(r.Issue())})
					}
				}
			}
			if material != nil {
				rows, ignored := ParseMaterialSheet(material, ctx)
				rawMaterial = append(rawMaterial, rows...)
				plan.SetRows("MATERIAL", len(rows))
				plan.SetRows("MATERIAL ignored", ignored)
			}
			if sales != nil {
				rows, ignored := ParseSalesSheet(sales, ctx)
				plan.Sales = GroupSales(rows, ctx, sales.Name, fmt.Sprintf("IMP-SO-%s-%d%02d", strings.ReplaceAll(plantCode, "-", ""), exp.Year, exp.Month))
				plan.SetRows("SALES", len(rows))
				plan.SetRows("SALES ignored", ignored)
			}
		case "pettycash":
			ws := sheets[0]
			res := ParsePettyCash(ws, PettyCtx{File: file, Exp: exp, Add: add})
			plan.PettyCash = res.Entries
			plan.SetRows(ws.Name, len(res.Entries))
			plan.SetRows(ws.Name+" ignored", res.Ignored)
			pettyResults = append(pettyResults, pettyResult{file, res.BalanceDiffs, res.ClosingSheet, res.ClosingComputed})
		default:
			endDay := 15
			if m := upToRe.FindStringSubmatch(file); m != nil {
				endDay, _ = strconv.Atoi(m[1])
			} else if m := toRe.FindStringSubmatch(file); m != nil {
				endDay, _ = strconv.Atoi(m[1])
			}
			start := fmt.Sprintf("%d-%02d-01", exp.Year, exp.Month)
			end := fmt.Sprintf("%d-%02d-%02d", exp.Year, exp.Month, endDay)
			ws := sheets[0]
			res := ParseWages(ws, WagesCtx{File: file, PlantCode: plantCode, StartDate: start, EndDate: end, Add: add})
			plan.Workers = res.Workers
			if len(res.Period.Lines) > 0 {
				plan.Payroll = []PayrollPlan{res.Period}
			}
			plan.SetRows(ws.Name, len(res.Period.Lines))
			plan.SetRows(ws.Name+" ignored", res.Ignored)
			if res.PreparedBy != "" {
				add(Anomaly{Sheet: ws.Name, Severity: "info", Kind: "signature", Message: fmt.Sprintf("sheet says \"%s\" but carries no signatures", res.PreparedBy), Action: "period imported as PREPARED with no preparer; the approval chain starts in the system"})
			}
			wageResults = append(wageResults, wageResult{file, plantCode, res.MomoCases, res.Totals, res.Period.SheetTotals, len(res.Period.Lines)})
		}
		f.Close()
		plans = append(plans, plan)
		var parts []string
		for _, kv := range plan.RowsRead {
			parts = append(parts, fmt.Sprintf("%s: %d", kv.K, kv.V))
		}
		fileRows = append(fileRows, []string{file, kind, orDash(plantCode), monthLabel, strings.Join(parts, ", ")})
	}
	rep.Table([]string{"File", "Kind", "Plant", "Month", "Rows used / ignored"}, fileRows)

	// Raw materials need every plant's sheet at once (transfers cross files).
	mat := PlanMaterial(rawMaterial)
	for _, m := range mat.Moves {
		for _, p := range plans {
			if p.File == m.File {
				p.Material = append(p.Material, m)
				break
			}
		}
	}
	for _, c := range mat.Closings {
		for _, p := range plans {
			if p.Kind == "block" && p.PlantCode == c.PlantCode {
				p.MaterialClosings = append(p.MaterialClosings, c)
				break
			}
		}
	}

	// ── database (read-only until the write phase) ──
	refs, err := LoadRefs(ctx, pool)
	if err != nil {
		return err
	}
	existing, err := ExistingImport(ctx, pool)
	if err != nil {
		return err
	}
	existingDNs, err := ExistingDNs(ctx, pool, opts.Replace || opts.DryRun)
	if err != nil {
		return err
	}
	var allSales []SalePlan
	var saleFile []string // file of each sale in allSales
	for _, p := range plans {
		for _, s := range p.Sales {
			allSales = append(allSales, s)
			saleFile = append(saleFile, p.File)
		}
	}
	fileOfSale := func(number string) string {
		for i, s := range allSales {
			if s.Number == number {
				return saleFile[i]
			}
		}
		return ""
	}
	for _, ch := range AlignSaleProducts(allSales, stockIssues) {
		im.anomalies = append(im.anomalies, Anomaly{File: fileOfSale(ch.Sale), Sheet: "SALES", Row: ch.Row, Severity: "info", Kind: "product",
			Message: fmt.Sprintf("invoice item %s — the STOCK sheet issued the same quantity as %s that day", ch.From, ch.To), Action: "sale line recorded as " + ch.To})
	}
	renamed := AssignDeliveries(allSales, existingDNs)
	for _, r := range renamed {
		msg := "no DN number"
		if r.Reason != "DN empty" {
			msg = fmt.Sprintf("DN \"%s\" already used by another sale", r.DN)
		}
		im.anomalies = append(im.anomalies, Anomaly{File: fileOfSale(r.Sale), Sheet: "SALES", Row: r.Row, Severity: "warning", Kind: "dn", Message: msg, Action: "delivery numbered " + r.DNNumber})
	}
	// allSales holds copies: hand the aligned/renumbered sales back to their plans.
	i := 0
	for _, p := range plans {
		for j := range p.Sales {
			p.Sales[j] = allSales[i]
			i++
		}
	}

	// Payroll periods that exist but were not imported by us block that file.
	ms, err := Manifests(ctx, pool)
	if err != nil {
		return err
	}
	manifestIDs := map[string]bool{}
	for _, id := range flat(ms, func(m Manifest) []string { return m.PayrollPeriods }) {
		manifestIDs[id] = true
	}
	for _, p := range plans {
		if len(p.Payroll) == 0 {
			continue
		}
		for _, period := range p.Payroll {
			id, clash, err := ClashingPeriod(ctx, pool, period, refs.Plants[period.PlantCode])
			if err != nil {
				return err
			}
			if clash && !manifestIDs[id] {
				im.anomalies = append(im.anomalies, Anomaly{File: p.File, Sheet: "-", Severity: "error", Kind: "conflict",
					Message: fmt.Sprintf("payroll period %s (%s→%s) already exists and was not created by an import", period.Number, period.StartDate, period.EndDate), Action: "file not imported"})
				p.Payroll, p.Workers = nil, nil
			}
		}
	}

	im.printFindings(plans, mat.Transfers, len(renamed), pettyResults, wageResults, stockIssues)

	// ── reconciliation (planned) ──
	var sm, mm []PlannedBalance
	for _, p := range plans {
		for _, m := range p.Stock {
			sm = append(sm, PlannedBalance{m.PlantCode, m.ProductCode, m.Date, float64(m.Quantity)})
		}
	}
	for _, m := range mat.Moves {
		mm = append(mm, PlannedBalance{m.PlantCode, m.MaterialCode, m.Date, m.Quantity})
	}
	plannedStock, plannedMat := PlannedBalances(sm), PlannedBalances(mm)
	var stockClosings, matClosings []ClosingCheck
	for _, p := range plans {
		stockClosings = append(stockClosings, p.StockClosings...)
		matClosings = append(matClosings, p.MaterialClosings...)
	}

	if opts.DryRun {
		im.printReconciliation("Finished goods — closing stock reconciliation (planned ledger vs sheet)", stockClosings, func(c ClosingCheck) (float64, *float64) {
			return plannedStock(c.PlantCode, c.ItemCode, c.LastDate), nil
		})
		im.printReconciliation("Raw materials — closing stock reconciliation (planned ledger vs sheet)", matClosings, func(c ClosingCheck) (float64, *float64) {
			return plannedMat(c.PlantCode, c.ItemCode, c.LastDate), nil
		})
		im.printSummary(plans, nil)
		if existing.Any() {
			rep.Line("", fmt.Sprintf("NOTE: imported data already exists (%s). A real run needs --replace.", existing))
		}
		rep.Line("", "Dry run complete — nothing was written.")
		return nil
	}

	if existing.Any() && !opts.Replace {
		rep.Line("", fmt.Sprintf("REFUSING: previously imported data exists (%s).", existing),
			"Re-run with --replace to delete ONLY the imported records and import again, or --dry-run to preview.")
		*code = 1
		return nil
	}
	if opts.Replace && existing.Any() {
		rep.H2("Removing previously imported records")
		removed, err := DeleteImported(ctx, pool, refs.AdminID)
		if err != nil {
			return err
		}
		var rows [][]string
		for _, kv := range removed {
			rows = append(rows, []string{kv.K, strconv.Itoa(kv.V)})
		}
		rep.Table([]string{"Record", "Deleted"}, rows, "l", "r")
	}

	rep.H2("Writing (one transaction per file)")
	var results [][]string
	var written []*Manifest
	for _, p := range plans {
		m, err := WriteFilePlan(ctx, pool, p, refs)
		if err != nil {
			msg := err.Error()
			if len(msg) > 160 {
				msg = msg[len(msg)-160:]
			}
			results = append(results, []string{p.File, "FAILED — rolled back", msg})
			*code = 1
			continue
		}
		written = append(written, m)
		var parts []string
		for _, kv := range m.Ordered() {
			parts = append(parts, fmt.Sprintf("%s %d", kv.K, kv.V))
		}
		w := strings.Join(parts, ", ")
		if w == "" {
			w = "nothing to write"
		}
		results = append(results, []string{p.File, "OK", w})
	}
	rep.Table([]string{"File", "Result", "Written"}, results)

	stockBal, err := DBBalances(ctx, pool, "stock", refs, stockClosings)
	if err != nil {
		return err
	}
	matBal, err := DBBalances(ctx, pool, "material", refs, matClosings)
	if err != nil {
		return err
	}
	idx := func(list []ClosingCheck, bal []Balance) func(ClosingCheck) (float64, *float64) {
		return func(c ClosingCheck) (float64, *float64) {
			for i := range list {
				if list[i] == c {
					all := bal[i].All
					return bal[i].Imported, &all
				}
			}
			return 0, nil
		}
	}
	im.printReconciliation("Finished goods — closing stock reconciliation (database vs sheet)", stockClosings, idx(stockClosings, stockBal))
	im.printReconciliation("Raw materials — closing stock reconciliation (database vs sheet)", matClosings, idx(matClosings, matBal))
	im.printSummary(plans, written)
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// Reconciliation is one closing-stock comparison.
func (im *importer) printReconciliation(title string, closings []ClosingCheck, balanceOf func(ClosingCheck) (float64, *float64)) {
	im.rep.H2(title)
	sorted := append([]ClosingCheck(nil), closings...)
	sort.SliceStable(sorted, func(a, b int) bool {
		return sorted[a].PlantCode+sorted[a].ItemCode < sorted[b].PlantCode+sorted[b].ItemCode
	})
	ok := 0
	var rows [][]string
	for _, c := range sorted {
		ledger, system := balanceOf(c)
		match := math.Abs(ledger-c.SheetClosing) < 0.0005
		if match {
			ok++
		}
		note := ""
		if system != nil && math.Abs(*system-ledger) > 0.0005 {
			note = fmt.Sprintf(" (system incl. non-imported: %s)", FmtN(*system))
		}
		res := "match"
		if !match {
			res = fmt.Sprintf("MISMATCH (%s)", FmtN(ledger-c.SheetClosing))
		}
		rows = append(rows, []string{c.PlantCode, c.ItemCode, c.LastDate, FmtN(c.SheetClosing), FmtN(ledger), res + note})
	}
	im.rep.Table([]string{"Plant", "Item", "Last date", "Sheet closing", "Ledger", "Result"}, rows, "l", "l", "l", "r", "r", "l")
	im.rep.Line(fmt.Sprintf("  %d/%d match", ok, len(closings)))
}

func (im *importer) printSummary(plans []*FilePlan, written []*Manifest) {
	title := "Summary — would be written"
	if written != nil {
		title = "Summary — written"
	}
	im.rep.H2(title)
	var rows [][]string
	for _, p := range plans {
		lines := 0
		for _, x := range p.Payroll {
			lines += len(x.Lines)
		}
		rows = append(rows, []string{short(p.File), strconv.Itoa(len(p.Stock)), strconv.Itoa(len(p.Material)), strconv.Itoa(len(p.Sales)), strconv.Itoa(len(p.PettyCash)), strconv.Itoa(len(p.Workers)), strconv.Itoa(lines)})
	}
	im.rep.Table([]string{"File", "Stock mv", "Material mv", "Sales", "Petty cash", "Workers", "Payroll lines"}, rows, "l", "r", "r", "r", "r", "r", "r")
	if written != nil {
		im.rep.Line(fmt.Sprintf("  %d/%d files committed.", len(written), len(plans)))
	}
}

func (im *importer) printFindings(plans []*FilePlan, transfers []TransferMatch, renamedDN int, petty []pettyResult, wages []wageResult, issues []StockIssue) {
	rep := im.rep
	rep.H2(fmt.Sprintf("Anomalies (%d)", len(im.anomalies)))
	count := func(s string) int {
		n := 0
		for _, a := range im.anomalies {
			if a.Severity == s {
				n++
			}
		}
		return n
	}
	rep.Line(fmt.Sprintf("  errors %d, warnings %d, info %d", count("error"), count("warning"), count("info")))
	order := map[string]int{"error": 0, "warning": 1, "info": 2}
	sorted := append([]Anomaly(nil), im.anomalies...)
	sort.SliceStable(sorted, func(a, b int) bool {
		x, y := sorted[a], sorted[b]
		if order[x.Severity] != order[y.Severity] {
			return order[x.Severity] < order[y.Severity]
		}
		if x.File != y.File {
			return x.File < y.File
		}
		return x.Row < y.Row
	})
	var arows [][]string
	for _, a := range sorted {
		row := ""
		if a.Row != 0 {
			row = strconv.Itoa(a.Row)
		}
		arows = append(arows, []string{strings.ToUpper(a.Severity), short(a.File), a.Sheet, row, a.Kind, a.Message, a.Action})
	}
	rep.Table([]string{"Sev", "File", "Sheet", "Row", "Kind", "Finding", "Action"}, arows)

	rep.H2("Cement / material transfers between plants (double-counting check)")
	rep.Line(
		"  Rule: each \"GOES TO BTN n\" is written as TRANSFER_OUT + TRANSFER_IN (shared transferGroup). The receiving plant also typed",
		"  the same cement as \"Entered stock\" that day, so incoming transfers are netted out of it; only the excess is a RECEIPT.",
	)
	var trows [][]string
	for _, t := range transfers {
		trows = append(trows, []string{t.Date, t.MaterialCode, t.From, t.To, FmtN(t.Quantity), FmtN(t.ReceiverEntered), FmtN(t.IncomingThatDay), t.Outcome, FmtN(t.ReceiptWritten)})
	}
	rep.Table([]string{"Date", "Material", "From", "To", "Qty", "Receiver entered", "All incoming", "Outcome", "Receipt written"}, trows, "l", "l", "l", "l", "r", "r", "r", "l", "r")
	tally := func(o string) int {
		n := 0
		for _, t := range transfers {
			if t.Outcome == o {
				n++
			}
		}
		return n
	}
	rep.Line(fmt.Sprintf("  exact same-day match %d, entered = transfer + purchase %d, not on receiver's sheet %d", tally("exact"), tally("partial"), tally("unmatched")))

	// Sales
	var sales []SalePlan
	for _, p := range plans {
		sales = append(sales, p.Sales...)
	}
	rep.H2("Sales")
	nl, nd, np, nm, nb, total := 0, 0, 0, 0, 0, 0
	customers := map[string]bool{}
	for _, s := range sales {
		nl += len(s.Lines)
		nd += len(s.Deliveries)
		customers[s.CustomerName] = true
		if s.Payment != nil {
			np++
			switch s.Payment.Method {
			case "MOMO":
				nm++
			case "BANK_BK":
				nb++
			}
		}
		for _, l := range s.Lines {
			total += l.Amount
		}
	}
	rep.Line(fmt.Sprintf("  %d sales, %d lines, %d deliveries (%d renumbered), %d paid (%d MoMo, %d BK), %d customers, total %s RWF",
		len(sales), nl, nd, renamedDN, np, nm, nb, len(customers), FmtN(float64(total))))
	var invOrder []string
	invoiceUse := map[string][]string{}
	for _, s := range sales {
		if s.EbmInvoiceNo != "" {
			if _, ok := invoiceUse[s.EbmInvoiceNo]; !ok {
				invOrder = append(invOrder, s.EbmInvoiceNo)
			}
			invoiceUse[s.EbmInvoiceNo] = append(invoiceUse[s.EbmInvoiceNo], fmt.Sprintf("%s %s %s", s.PlantCode, s.Date, s.CustomerName))
		}
	}
	rep.Line("", "  EBM invoice numbers used on more than one sale (check against the EBM system):")
	var rrows [][]string
	for _, inv := range invOrder {
		if v := invoiceUse[inv]; len(v) > 1 {
			rrows = append(rrows, []string{inv, fmt.Sprintf("%d× — %s", len(v), strings.Join(v, "; "))})
		}
	}
	rep.Table([]string{"Invoice", "Sales"}, rrows)
	rep.Line("", "  SALES sheet quantities vs STOCK sheet \"Issue/Sold\" (same plant, day and product):")
	var drows [][]string
	for _, d := range CompareSalesWithIssues(sales, issues) {
		drows = append(drows, []string{d.PlantCode, d.Date, d.ProductCode, FmtN(float64(d.Sold)), FmtN(float64(d.Issued))})
	}
	rep.Table([]string{"Plant", "Date", "Product", "Sold (SALES)", "Issued (STOCK)"}, drows, "l", "l", "l", "r", "r")
	rep.Line("  Deliveries are imported WITHOUT stock movements; the STOCK sheet's Issue/Sold column is the stock truth.")

	// Petty cash
	rep.H2("Petty cash")
	type agg struct{ n, sum int }
	var catOrder []string
	cats := map[string]agg{}
	plantLess := 0
	for _, p := range plans {
		for _, e := range p.PettyCash {
			key := e.Type + " " + e.Category
			if _, ok := cats[key]; !ok {
				catOrder = append(catOrder, key)
			}
			c := cats[key]
			cats[key] = agg{c.n + 1, c.sum + e.Amount}
			if e.Type == "EXPENSE" && e.PlantCode == "" {
				plantLess++
			}
		}
	}
	var crows [][]string
	for _, k := range catOrder {
		crows = append(crows, []string{k, strconv.Itoa(cats[k].n), FmtN(float64(cats[k].sum))})
	}
	rep.Table([]string{"Type / category", "Entries", "Amount RWF"}, crows, "l", "r", "r")
	for _, p := range petty {
		cs := "—"
		if p.closingSheet != nil {
			cs = FmtN(*p.closingSheet)
		}
		rep.Line(fmt.Sprintf("  %s: computed closing balance %s RWF; sheet's last BALANCE %s RWF.", p.file, FmtN(float64(p.computed)), cs))
		if len(p.diffs) > 0 {
			rep.Line("  Running balance differences:")
			var brows [][]string
			for _, d := range p.diffs {
				brows = append(brows, []string{strconv.Itoa(d.Row), FmtN(float64(d.Computed)), FmtN(d.Sheet)})
			}
			rep.Table([]string{"Row", "Computed", "Sheet"}, brows, "r", "r", "r")
		} else {
			rep.Line("  Running balance matches the BALANCE column on every row.")
		}
	}
	rep.Line(fmt.Sprintf("  %d expense(s) name no single plant (company-wide or several plants) — imported without a plant.", plantLess))

	// Wages
	rep.H2("Wages")
	var wrows, mrows [][]string
	for _, w := range wages {
		sheet, check := "—", "MISMATCH"
		if w.sheetTotals != nil {
			st := w.sheetTotals
			sheet = fmt.Sprintf("%s / %s / %s", FmtN(st.Gross), FmtN(st.Food), FmtN(st.Net))
			if jsRound(st.Gross) == w.totals.Gross && jsRound(st.Food) == w.totals.Food && jsRound(st.Net) == w.totals.Net {
				check = "match"
			}
		}
		wrows = append(wrows, []string{w.plant, strconv.Itoa(w.lines), FmtN(w.totals.Gross), FmtN(w.totals.Food), FmtN(w.totals.Net), sheet, check})
		for _, c := range w.momo {
			mrows = append(mrows, []string{w.plant, strconv.Itoa(c.Row), c.Worker, c.MomoName, c.Kind})
		}
	}
	rep.Table([]string{"Plant", "Lines", "Gross", "Food", "Net", "Sheet totals", "Check"}, wrows, "l", "r", "r", "r", "r", "r", "l")
	rep.Line("", "  MoMo account name differs from the worker's name (masked; \"different\" = possibly paid to someone else's account):")
	rep.Table([]string{"Plant", "Row", "Worker", "MoMo name", "Kind"}, mrows, "l", "r", "l", "l", "l")
	var workers []WorkerPlan
	for _, p := range plans {
		workers = append(workers, p.Workers...)
	}
	var ids, names []string
	for i, w := range workers {
		if i >= 2 {
			break
		}
		ids = append(ids, MaskNumber(w.NationalID))
		names = append(names, MaskName(w.FirstName+" "+w.LastName))
	}
	rep.Line(fmt.Sprintf("  %d workers (IDs %s…; names e.g. %s).", len(workers), strings.Join(ids, ", "), strings.Join(names, ", ")))
}
