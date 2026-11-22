package importer

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// WagesCtx is what the wages parser needs.
type WagesCtx struct {
	File, PlantCode, StartDate, EndDate string
	Add                                 AddFn
}

// NormalizeNationalID: IDs arrive as 16-digit numbers (safe below 2^53) or text; "" = none.
func NormalizeNationalID(v any) string {
	if n, ok := v.(float64); ok {
		if n == math.Trunc(n) && math.Abs(n) <= 9007199254740991 {
			return strconv.FormatFloat(n, 'f', 0, 64)
		}
		return ""
	}
	return strings.Join(strings.Fields(Text(v)), "")
}

var (
	placeholderX    = regexp.MustCompile(`(?i)^(X\s*)+$`)
	placeholderDash = regexp.MustCompile(`^-+$`)
	helpersRe       = regexp.MustCompile(`(?i)^helpers?$`)
	preparedByRe    = regexp.MustCompile(`(?i)^PRE\w*ED BY\b`)
	nid16           = regexp.MustCompile(`^\d{16}$`)
	hasDigit        = regexp.MustCompile(`\d`)
)

func isPlaceholder(s string) bool {
	t := strings.TrimSpace(s)
	return t == "" || placeholderX.MatchString(t) || placeholderDash.MatchString(t)
}

func isWordChar(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// JobTitleOf: "" / "helpers" -> "Helper"; otherwise title case.
func JobTitleOf(raw string) string {
	t := strings.TrimSpace(raw)
	if t == "" || helpersRe.MatchString(t) {
		return "Helper"
	}
	b := []byte(strings.ToLower(t))
	for i := range b {
		if isWordChar(b[i]) && (i == 0 || !isWordChar(b[i-1])) && b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 32
		}
	}
	return string(b)
}

// MomoNameCase is a MoMo account name that differs from the worker's name.
type MomoNameCase struct {
	Row              int
	Worker, MomoName string // masked
	Kind             string // variant | different
}

// WagesResult is the outcome of ParseWages.
type WagesResult struct {
	Workers    []WorkerPlan
	Period     PayrollPlan
	MomoCases  []MomoNameCase
	Ignored    int
	PreparedBy string
	Totals     Totals
}

func ParseWages(ws *Sheet, ctx WagesCtx) WagesResult {
	sheet := ws.Name
	header := FindHeader(ws, []string{"FIRST NAME", "LAST NAME", "ID NUMBER", "DAILY WAGE", "TOTAL DAYS"})
	compact := strings.ReplaceAll(ctx.PlantCode, "-", "")
	res := WagesResult{Period: PayrollPlan{
		Number: fmt.Sprintf("WG-%s-%s", compact, strings.ReplaceAll(ctx.StartDate, "-", "")), PlantCode: ctx.PlantCode,
		StartDate: ctx.StartDate, EndDate: ctx.EndDate,
	}}
	if header == nil {
		ctx.Add(Anomaly{Sheet: sheet, Severity: "error", Kind: "layout", Message: "header row (First Name / Last Name / ID NUMBER / Daily Wage / Total Days) not found", Action: "file skipped"})
		return res
	}
	col := header.Col
	cFirst, cLast, cID, cMomo, cMomoName, cJob := col("FIRST NAME"), col("LAST NAME"), col("ID NUMBER"), col("MOMO NUMBER"), col("MOMO NAME"), col("JOB TITLE")
	cWage, cDays, cGross, cFood := col("DAILY WAGE"), col("TOTAL DAYS"), col("TOTAL WAGES"), col("FOOD EXPENSE")
	// "wages received" (net) has a two-row header: "wages" over "recieved"
	cNet := col("RECIEVED")
	if cNet == 0 {
		cNet = col("RECEIVED")
	}
	if cNet == 0 {
		cNet = col("WAGES RECEIVED")
	}
	seen := map[string]bool{}
	for r := header.Row + 1; r <= ws.RowCount(); r++ {
		val := func(x int) any {
			if x == 0 {
				return nil
			}
			return ws.Cell(r, x)
		}
		nid := NormalizeNationalID(val(cID))
		first := Text(val(cFirst))
		days, wage := Num(val(cDays)), Num(val(cWage))

		// Totals, blank and signature rows ("Checked By :-___") carry no ID digits and no name.
		looksLikeData := (nid != "" && hasDigit.MatchString(nid)) || (days > 0 && wage > 0)
		if !looksLikeData {
			gross := Num(val(cGross))
			cells := ws.NonEmpty(r)
			var cs []int
			for c := range cells {
				cs = append(cs, c)
			}
			sort.Ints(cs)
			if res.PreparedBy == "" {
				for _, c := range cs {
					if p := Text(cells[c]); preparedByRe.MatchString(p) {
						res.PreparedBy = p
						break
					}
				}
			}
			if gross > 0 && res.Period.SheetTotals == nil {
				res.Period.SheetTotals = &Totals{gross, Num(val(cFood)), Num(val(cNet))}
			} else {
				res.Ignored++
			}
			continue
		}
		if nid == "" || !nid16.MatchString(nid) {
			ctx.Add(Anomaly{Sheet: sheet, Row: r, Severity: "error", Kind: "national-id", Message: fmt.Sprintf("national ID \"%s\" is not 16 digits (%s)", MaskNumber(nid), MaskName(first)), Action: "worker and payroll line skipped"})
			continue
		}
		if seen[nid] {
			ctx.Add(Anomaly{Sheet: sheet, Row: r, Severity: "error", Kind: "national-id", Message: fmt.Sprintf("national ID %s appears twice on the sheet", MaskNumber(nid)), Action: "second row skipped"})
			continue
		}
		seen[nid] = true
		lastRaw := Text(val(cLast))
		lastName := lastRaw
		if isPlaceholder(lastRaw) {
			lastName = ""
		}
		if lastName == "" {
			ctx.Add(Anomaly{Sheet: sheet, Row: r, Severity: "info", Kind: "name", Message: fmt.Sprintf("last name missing or placeholder (\"%s\") for %s", lastRaw, MaskName(first)), Action: "worker imported with an empty last name"})
		}
		momoName := Text(val(cMomoName))
		fullName := strings.TrimSpace(first + " " + lastName)
		if momoName != "" {
			if cmp := CompareMomoName(momoName, fullName); cmp != "match" {
				res.MomoCases = append(res.MomoCases, MomoNameCase{r, MaskName(fullName), MaskName(momoName), cmp})
			}
		}
		dailyWage := int(jsRound(wage))
		res.Workers = append(res.Workers, WorkerPlan{
			NationalID: nid, FirstName: first, LastName: lastName, MomoNumber: NormalizeMsisdn(val(cMomo)), MomoName: momoName,
			JobTitle: JobTitleOf(Text(val(cJob))), PlantCode: ctx.PlantCode, DailyWage: dailyWage,
		})

		grossSheet := int(jsRound(Num(val(cGross))))
		food := int(jsRound(Num(val(cFood))))
		netSheet := int(jsRound(Num(val(cNet))))
		gross := int(jsRound(float64(dailyWage) * days))
		net := gross - food
		if grossSheet != gross {
			ctx.Add(Anomaly{Sheet: sheet, Row: r, Severity: "warning", Kind: "arithmetic", Message: fmt.Sprintf("total wages %d ≠ %d × %s = %d", grossSheet, dailyWage, FmtNum(days), gross), Action: "line stored with daily wage × days"})
		}
		if netSheet != net {
			ctx.Add(Anomaly{Sheet: sheet, Row: r, Severity: "warning", Kind: "arithmetic", Message: fmt.Sprintf("wages received %d ≠ %d − food %d = %d", netSheet, gross, food, net), Action: "line stored with gross − food"})
		}
		if food > gross {
			ctx.Add(Anomaly{Sheet: sheet, Row: r, Severity: "warning", Kind: "value", Message: fmt.Sprintf("food deduction %d exceeds gross %d", food, gross), Action: "imported as recorded"})
		}
		if days < 0 || days > 31 || math.Mod(days*2, 1) != 0 {
			ctx.Add(Anomaly{Sheet: sheet, Row: r, Severity: "warning", Kind: "value", Message: "unusual days worked: " + FmtNum(days), Action: "imported as recorded"})
		}
		res.Period.Lines = append(res.Period.Lines, PayrollLinePlan{r, nid, dailyWage, days, gross, food, net})
	}

	for _, l := range res.Period.Lines {
		res.Totals.Gross += float64(l.GrossAmount)
		res.Totals.Food += float64(l.FoodDeduction)
		res.Totals.Net += float64(l.NetAmount)
	}
	if res.Period.SheetTotals == nil {
		ctx.Add(Anomaly{Sheet: sheet, Severity: "warning", Kind: "totals", Message: "no totals row found", Action: "totals not checked"})
	} else {
		st := res.Period.SheetTotals
		for _, k := range []struct {
			name     string
			sheet, c float64
		}{{"gross", st.Gross, res.Totals.Gross}, {"food", st.Food, res.Totals.Food}, {"net", st.Net, res.Totals.Net}} {
			if jsRound(k.sheet) != k.c {
				ctx.Add(Anomaly{Sheet: sheet, Severity: "warning", Kind: "totals", Message: fmt.Sprintf("sheet total %s %s ≠ computed %s", k.name, FmtNum(k.sheet), FmtNum(k.c)), Action: "computed lines imported"})
			}
		}
	}
	return res
}
