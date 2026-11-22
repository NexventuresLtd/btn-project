package importer

import (
	"fmt"
	"regexp"
	"strings"
)

// PettyCtx is what the petty-cash parser needs.
type PettyCtx struct {
	File string
	Exp  ExpectedMonth
	Add  AddFn
}

var nonDigit = regexp.MustCompile(`\D`)

// NormalizeMsisdn: "0791234567" | 791234567 -> "0791234567" (Rwandan MoMo numbers lose their leading 0 in
// Excel). "" = none.
func NormalizeMsisdn(v any) string {
	digits := nonDigit.ReplaceAllString(Text(v), "")
	if digits == "" {
		return ""
	}
	if len(digits) == 9 && strings.HasPrefix(digits, "7") {
		return "0" + digits
	}
	if len(digits) == 12 && strings.HasPrefix(digits, "2507") {
		return "0" + digits[3:]
	}
	return digits
}

// BalanceCheck is a row whose computed running balance differs from the BALANCE column.
type BalanceCheck struct {
	Row      int
	Computed int
	Sheet    float64
}

// PettyResult is the outcome of ParsePettyCash.
type PettyResult struct {
	Entries         []PettyCashPlan
	BalanceDiffs    []BalanceCheck
	Ignored         int
	ClosingSheet    *float64
	ClosingComputed int
}

type pettyCand struct {
	PettyCashPlan
	parsed ParsedDate
}

func ParsePettyCash(ws *Sheet, ctx PettyCtx) PettyResult {
	sheet := ws.Name
	header := FindHeader(ws, []string{"DATE", "DESCRIPTION", "CREDIT", "BALANCE"})
	if header == nil {
		ctx.Add(Anomaly{Sheet: sheet, Severity: "error", Kind: "layout", Message: "header row (DATE / DESCRIPTION / CREDIT / BALANCE) not found", Action: "file skipped"})
		return PettyResult{}
	}
	col := func(h string) int { return header.Col(h) }
	cDate, cDesc, cMomo, cName := col("DATE"), col("DESCRIPTION"), col("NBR OF MOMO"), col("NAME")
	cCredit, cBalance, cDeposit, cCharges, cInvoice := col("CREDIT"), col("BALANCE"), col("NEW DEPOSIT"), col("MOMO CHARGES"), col("INVOICE")

	var cands []pettyCand
	res := PettyResult{}
	for r := header.Row + 1; r <= ws.RowCount(); r++ {
		val := func(x int) any {
			if x == 0 {
				return nil
			}
			return ws.Cell(r, x)
		}
		rawDate := val(cDate)
		desc := Text(val(cDesc))
		credit := Num(val(cCredit))
		depositCell := val(cDeposit)
		deposit := Num(depositCell)
		charges := Num(val(cCharges))
		balanceCell := val(cBalance)
		var sheetBalance *float64
		if balanceCell != nil && balanceCell != "" {
			b := Num(balanceCell)
			sheetBalance = &b
		}
		hasDate := rawDate != nil && Text(rawDate) != ""

		if desc == "" && credit == 0 && deposit == 0 {
			// Opening balance brought forward: a dated row with only a balance, before any entry.
			if hasDate && sheetBalance != nil && *sheetBalance != 0 && len(cands) == 0 {
				cands = append(cands, pettyCand{PettyCashPlan{
					Row: r, Type: "DEPOSIT", Category: "FLOAT_DEPOSIT", Description: "Opening balance brought forward",
					Amount: int(jsRound(*sheetBalance)), SheetBalance: sheetBalance,
				}, ParseLegacyDate(rawDate, ctx.Exp)})
				ctx.Add(Anomaly{Sheet: sheet, Row: r, Severity: "info", Kind: "opening", Message: fmt.Sprintf("balance brought forward %s RWF on a row with no description", FmtNum(*sheetBalance)), Action: "imported as an opening FLOAT_DEPOSIT so the running balance continues"})
			} else {
				res.Ignored++
			}
			continue
		}
		invoiceRef := Text(val(cInvoice))
		if IsNonNumericText(depositCell) {
			act := "kept as the invoice reference"
			if invoiceRef != "" {
				act = "ignored"
			}
			ctx.Add(Anomaly{Sheet: sheet, Row: r, Severity: "warning", Kind: "value", Message: fmt.Sprintf("text \"%s\" in the NEW DEPOSIT column", Text(depositCell)), Action: act})
			if invoiceRef == "" {
				invoiceRef = Text(depositCell)
			}
		}
		parsed := ParseLegacyDate(rawDate, ctx.Exp)
		base := PettyCashPlan{
			Row: r, PayeeName: Text(val(cName)), MomoNumber: NormalizeMsisdn(val(cMomo)), InvoiceRef: invoiceRef,
			SheetBalance: sheetBalance, MomoCharge: int(jsRound(charges)),
		}

		if deposit > 0 || (IsDepositDescription(desc) && credit == 0) {
			if credit != 0 {
				ctx.Add(Anomaly{Sheet: sheet, Row: r, Severity: "warning", Kind: "value", Message: "row has both a deposit and a credit", Action: "imported as two entries"})
			}
			e := base
			e.Type, e.Category, e.Description, e.Amount, e.MomoCharge = "DEPOSIT", "FLOAT_DEPOSIT", desc, int(jsRound(deposit)), 0
			if desc == "" {
				e.Description = "Float deposit"
			}
			cands = append(cands, pettyCand{e, parsed})
			if deposit == 0 {
				ctx.Add(Anomaly{Sheet: sheet, Row: r, Severity: "warning", Kind: "value", Message: fmt.Sprintf("\"%s\" row without an amount", desc), Action: "imported with amount 0"})
			}
		}
		if credit != 0 {
			inferred := InferPettyCashCategory(desc)
			if inferred.Category == "OTHER" {
				ctx.Add(Anomaly{Sheet: sheet, Row: r, Severity: "info", Kind: "category", Message: fmt.Sprintf("no category keyword in \"%s\"", desc), Action: "category OTHER"})
			}
			e := base
			e.Type, e.Category, e.Description, e.PlantCode, e.Amount = "EXPENSE", inferred.Category, desc, inferred.PlantCode, int(jsRound(credit))
			if desc == "" {
				e.Description = "(no description)"
			}
			cands = append(cands, pettyCand{e, parsed})
		}
	}

	pds := make([]ParsedDate, len(cands))
	for i, c := range cands {
		pds[i] = c.parsed
	}
	resolved := ResolveLedgerDates(pds, ctx.Exp)
	sameRow := func(row int) int {
		n := 0
		for _, x := range cands {
			if x.Row == row {
				n++
			}
		}
		return n
	}
	for i, cand := range cands {
		rs := resolved[i]
		if rs.Flagged {
			sev, act := "error", "row skipped"
			if rs.ISO != "" {
				sev, act = "warning", "kept and flagged, dated "+rs.ISO
			}
			msg := rs.Note + " — " + cand.Description
			if cand.PayeeName != "" {
				msg += " (" + MaskName(cand.PayeeName) + ")"
			}
			ctx.Add(Anomaly{Sheet: sheet, Row: cand.Row, Severity: sev, Kind: "date", Message: msg, Action: act})
		}
		if rs.ISO == "" {
			continue
		}
		e := cand.PettyCashPlan
		e.Date = rs.ISO
		suffix := ""
		if sameRow(cand.Row) > 1 {
			suffix = "E"
			if cand.Type == "DEPOSIT" {
				suffix = "D"
			}
		}
		e.Number = fmt.Sprintf("IMP-PC-%d%02d-R%03d%s", ctx.Exp.Year, ctx.Exp.Month, cand.Row, suffix)
		res.Entries = append(res.Entries, e)
	}

	// Running balance in sheet order vs the BALANCE column.
	bal := 0
	seen := map[int]bool{}
	for i, e := range res.Entries {
		if e.Type == "DEPOSIT" {
			bal += e.Amount
		} else {
			bal -= e.Amount + e.MomoCharge
		}
		// compare once per sheet row, after all of that row's entries
		lastOfRow := true
		for _, x := range res.Entries[i+1:] {
			if x.Row == e.Row {
				lastOfRow = false
				break
			}
		}
		if !lastOfRow || seen[e.Row] {
			continue
		}
		seen[e.Row] = true
		if e.SheetBalance != nil {
			sb := *e.SheetBalance
			res.ClosingSheet = &sb
			if int(jsRound(sb)) != bal {
				res.BalanceDiffs = append(res.BalanceDiffs, BalanceCheck{e.Row, bal, sb})
			}
		}
	}
	res.ClosingComputed = bal
	return res
}
