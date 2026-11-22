package importer

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// TransferMatch documents how one "GOES TO BTN n" quantity matched the receiver's "Entered stock".
type TransferMatch struct {
	Date, MaterialCode, From, To string
	Quantity                     float64
	ReceiverEntered              float64
	IncomingThatDay              float64
	Outcome                      string // exact | partial | unmatched
	ReceiptWritten               float64
}

func k(parts ...string) string { return strings.Join(parts, "|") }

func noteFor(r RawMaterialRow) string {
	return fmt.Sprintf("Imported from %s / MATERIAL / row %d", r.File, r.Row)
}

// MaterialPlan is the raw-material ledger of all plants.
type MaterialPlan struct {
	Moves     []MaterialMovePlan
	Closings  []ClosingCheck
	Transfers []TransferMatch
}

// PlanMaterial builds the raw-material ledger for all plants at once.
//
// Every "GOES TO BTN n" quantity becomes a TRANSFER_OUT at the sender and a TRANSFER_IN at the
// receiver (shared transferGroup). Receiving plants also typed the same cement into their own
// "Entered stock" column on the same day, so on each receiver/material/day the transfers are netted
// out of the entered quantity: only the excess is a supplier RECEIPT. If the receiver entered less
// than arrived, the receipt is 0 and the receiver's ledger will exceed its sheet by the difference —
// reported, not hidden.
func PlanMaterial(rows []RawMaterialRow) MaterialPlan {
	var out MaterialPlan
	incoming := map[string]float64{}
	entered := map[string]float64{}
	var enteredKeys []string
	for _, r := range rows {
		key := k(r.PlantCode, r.Material, r.Date)
		if _, ok := entered[key]; !ok {
			enteredKeys = append(enteredKeys, key)
		}
		entered[key] = Round3(entered[key] + r.Entered)
		for _, g := range r.GoesTo {
			if g.Qty > 0 {
				ik := k(g.Plant, r.Material, r.Date)
				incoming[ik] = Round3(incoming[ik] + g.Qty)
			}
		}
	}
	// Receipt left after netting transfers, consumed by the first row of that day.
	receiptLeft := map[string]float64{}
	for _, key := range enteredKeys {
		v := entered[key] - incoming[key]
		if v < 0 {
			v = 0
		}
		receiptLeft[key] = Round3(v)
	}

	var itemKeys []string
	byItem := map[string][]RawMaterialRow{}
	for _, r := range rows {
		key := k(r.PlantCode, r.Material)
		if _, ok := byItem[key]; !ok {
			itemKeys = append(itemKeys, key)
		}
		byItem[key] = append(byItem[key], r)
	}
	for _, ik := range itemKeys {
		sorted := append([]RawMaterialRow(nil), byItem[ik]...)
		sort.SliceStable(sorted, func(i, j int) bool {
			if sorted[i].Date != sorted[j].Date {
				return sorted[i].Date < sorted[j].Date
			}
			return sorted[i].Row < sorted[j].Row
		})
		first := sorted[0]
		if first.Opening != 0 {
			out.Moves = append(out.Moves, MaterialMovePlan{PlantCode: first.PlantCode, MaterialCode: first.Material, Date: first.Date, Type: "OPENING", Quantity: first.Opening, Note: noteFor(first) + " (opening stock)", File: first.File})
		}
		for _, r := range sorted {
			dayKey := k(r.PlantCode, r.Material, r.Date)
			if receipt := receiptLeft[dayKey]; receipt > 0 {
				note := noteFor(r)
				if receipt != r.Entered {
					note += fmt.Sprintf(" (entered %s, of which %s was a transfer from another plant)", FmtNum(r.Entered), FmtNum(Round3(r.Entered-receipt)))
				}
				out.Moves = append(out.Moves, MaterialMovePlan{PlantCode: first.PlantCode, MaterialCode: first.Material, Date: r.Date, Type: "RECEIPT", Quantity: receipt, Note: note, File: r.File})
				receiptLeft[dayKey] = 0
			}
			for _, g := range r.GoesTo {
				to, q := g.Plant, g.Qty
				if q <= 0 {
					continue
				}
				group := fmt.Sprintf("IMP-TR-%s-%s-%s-%s-R%d", r.PlantCode, to, r.Material, r.Date, r.Row)
				out.Moves = append(out.Moves,
					MaterialMovePlan{PlantCode: first.PlantCode, MaterialCode: first.Material, Date: r.Date, Type: "TRANSFER_OUT", Quantity: -q, CounterpartPlantCode: to, TransferGroup: group, Note: noteFor(r) + " (GOES TO " + to + ")", File: r.File},
					MaterialMovePlan{PlantCode: to, MaterialCode: r.Material, Date: r.Date, Type: "TRANSFER_IN", Quantity: q, CounterpartPlantCode: r.PlantCode, TransferGroup: group, Note: noteFor(r) + " (from " + r.PlantCode + ")", File: r.File})
				recv := k(to, r.Material, r.Date)
				e, inc := entered[recv], incoming[recv]
				outcome := "unmatched"
				if e == inc {
					outcome = "exact"
				} else if e > inc {
					outcome = "partial"
				}
				w := e - inc
				if w < 0 {
					w = 0
				}
				out.Transfers = append(out.Transfers, TransferMatch{r.Date, r.Material, r.PlantCode, to, q, e, inc, outcome, Round3(w)})
			}
			if r.Used != 0 {
				out.Moves = append(out.Moves, MaterialMovePlan{PlantCode: first.PlantCode, MaterialCode: first.Material, Date: r.Date, Type: "CONSUMPTION", Quantity: -r.Used, Note: noteFor(r), File: r.File})
			}
		}
		last := sorted[len(sorted)-1]
		out.Closings = append(out.Closings, ClosingCheck{last.PlantCode, last.Material, last.Date, last.Closing})
	}
	return out
}

// RenamedDN is a delivery whose DN number was replaced.
type RenamedDN struct {
	Sale     string
	Row      int
	DN       string
	DNNumber string
	Reason   string
}

// AssignDeliveries gives each sale one delivery per distinct DN. A DN that is empty or already used
// (by another imported sale or an existing delivery) becomes "IMP-DN-<plant>-R<row>".
func AssignDeliveries(sales []SalePlan, existingDNs map[string]bool) []RenamedDN {
	used := map[string]bool{}
	for d := range existingDNs {
		used[d] = true
	}
	var renamed []RenamedDN
	for si := range sales {
		s := &sales[si]
		s.Deliveries = nil
		var order []string
		byDN := map[string][]int{}
		for i, l := range s.Lines {
			key := l.DN
			if key == "" {
				key = fmt.Sprintf("__row%d", l.Row)
			}
			if _, ok := byDN[key]; !ok {
				order = append(order, key)
			}
			byDN[key] = append(byDN[key], i)
		}
		for _, dn := range order {
			idx := byDN[dn]
			row := s.Lines[idx[0]].Row
			dnNumber := dn
			empty := strings.HasPrefix(dn, "__row")
			if empty || used[dn] {
				dnNumber = fmt.Sprintf("IMP-DN-%s-R%03d", strings.ReplaceAll(s.PlantCode, "-", ""), row)
				orig, reason := dn, "DN already used"
				if empty {
					orig, reason = "", "DN empty"
				}
				renamed = append(renamed, RenamedDN{s.Number, row, orig, dnNumber, reason})
			}
			used[dnNumber] = true
			s.Deliveries = append(s.Deliveries, DeliveryPlan{dnNumber, idx})
		}
	}
	return renamed
}

// StockIssue is one "Issue/Sold" quantity from a STOCK sheet.
type StockIssue struct {
	PlantCode, ProductCode, Date string
	Quantity                     int
}

// SalesDiff is a plant/day/product where SALES and STOCK disagree.
type SalesDiff struct {
	PlantCode, Date, ProductCode string
	Sold, Issued                 int
}

// CompareSalesWithIssues compares sold quantities (SALES) with "Issue/Sold" (STOCK) per plant/day/product.
func CompareSalesWithIssues(sales []SalePlan, issues []StockIssue) []SalesDiff {
	sold, issued := map[string]int{}, map[string]int{}
	var keys []string
	seen := map[string]bool{}
	add := func(key string) {
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	for _, s := range sales {
		for _, l := range s.Lines {
			key := k(s.PlantCode, s.Date, l.ProductCode)
			sold[key] += l.Quantity
			add(key)
		}
	}
	for _, i := range issues {
		key := k(i.PlantCode, i.Date, i.ProductCode)
		issued[key] += i.Quantity
		add(key)
	}
	var out []SalesDiff
	for _, key := range keys {
		if sold[key] != issued[key] {
			p := strings.SplitN(key, "|", 3)
			out = append(out, SalesDiff{p[0], p[1], p[2], sold[key], issued[key]})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].PlantCode+out[i].Date+out[i].ProductCode < out[j].PlantCode+out[j].Date+out[j].ProductCode
	})
	return out
}

// ProductChange records a sale line moved to the variant the STOCK sheet issued.
type ProductChange struct {
	Sale     string
	Row      int
	From, To string
}

// AlignSaleProducts: invoices use a generic product name ("HOLLOW BLOCK 15-20-40 CM") where the STOCK
// sheet issued a variant (HB15-2H). When, for a plant and day, the generic product shows no issue but
// exactly one variant of its family was issued in the same total quantity, the sale lines take the variant.
func AlignSaleProducts(sales []SalePlan, issues []StockIssue) []ProductChange {
	var changes []ProductChange
	issued := map[string]int{}
	for _, i := range issues {
		issued[k(i.PlantCode, i.Date, i.ProductCode)] += i.Quantity
	}
	sold := map[string]int{}
	for _, s := range sales {
		for _, l := range s.Lines {
			sold[k(s.PlantCode, s.Date, l.ProductCode)] += l.Quantity
		}
	}
	for si := range sales {
		s := &sales[si]
		for li := range s.Lines {
			l := &s.Lines[li]
			if issued[k(s.PlantCode, s.Date, l.ProductCode)] != 0 {
				continue
			}
			var codes []string
			seen := map[string]bool{}
			for _, i := range issues {
				if i.PlantCode == s.PlantCode && i.Date == s.Date && strings.HasPrefix(i.ProductCode, l.ProductCode+"-") && !seen[i.ProductCode] {
					seen[i.ProductCode] = true
					codes = append(codes, i.ProductCode)
				}
			}
			if len(codes) != 1 {
				continue
			}
			if issued[k(s.PlantCode, s.Date, codes[0])] != sold[k(s.PlantCode, s.Date, l.ProductCode)] {
				continue
			}
			changes = append(changes, ProductChange{s.Number, l.Row, l.ProductCode, codes[0]})
			l.ProductCode = codes[0]
		}
	}
	return changes
}

// PlannedBalance is a movement as the dry-run reconciliation sees it.
type PlannedBalance struct {
	PlantCode, ItemCode, Date string
	Quantity                  float64
}

// PlannedBalances returns the ledger balance per plant+item up to a date from planned movements.
func PlannedBalances(moves []PlannedBalance) func(plant, item, upTo string) float64 {
	return func(plant, item, upTo string) float64 {
		sum := 0.0
		for _, m := range moves {
			if m.PlantCode == plant && m.ItemCode == item && m.Date <= upTo {
				sum += m.Quantity
			}
		}
		return Round3(sum)
	}
}

// EditDistance is the Levenshtein distance.
func EditDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	dp := make([][]int, len(ar)+1)
	for i := range dp {
		dp[i] = make([]int, len(br)+1)
		dp[i][0] = i
	}
	for j := 1; j <= len(br); j++ {
		dp[0][j] = j
	}
	for i := 1; i <= len(ar); i++ {
		for j := 1; j <= len(br); j++ {
			c := 1
			if ar[i-1] == br[j-1] {
				c = 0
			}
			dp[i][j] = min(dp[i-1][j]+1, dp[i][j-1]+1, dp[i-1][j-1]+c)
		}
	}
	return dp[len(ar)][len(br)]
}

var (
	notAZ    = regexp.MustCompile(`[^A-Z ]`)
	onlyX    = regexp.MustCompile(`^X+$`)
	initialR = regexp.MustCompile(`^[A-Z]\.?$`)
)

func momoWords(s string) []string {
	var out []string
	for _, w := range strings.Fields(notAZ.ReplaceAllString(strings.ToUpper(s), " ")) {
		if len(w) > 1 && !onlyX.MatchString(w) {
			out = append(out, w)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// CompareMomoName: how a MoMo account name relates to the worker's name on the sheet. "match" — every
// MoMo word is one of the worker's names; "variant" — words differ only by small spelling differences
// (PARTICK / PATRICK); "different" — at least one word is unrelated.
func CompareMomoName(momoName, workerName string) string {
	momo, names := momoWords(momoName), momoWords(workerName)
	if len(momo) == 0 {
		return "match"
	}
	var initials []byte
	for _, w := range strings.Fields(strings.ToUpper(workerName)) {
		if initialR.MatchString(w) {
			initials = append(initials, w[0])
		}
	}
	prefix := func(a, b string) int {
		i := 0
		for i < len(a) && i < len(b) && a[i] == b[i] {
			i++
		}
		return i
	}
	variant := false
	for _, w := range momo {
		if contains(names, w) {
			continue
		}
		close := false
		for _, in := range initials {
			if in == w[0] {
				close = true
			}
		}
		if !close {
			for _, n := range names {
				lim := 1
				if len(n) >= 5 && len(w) >= 5 {
					lim = 2
				}
				if EditDistance(n, w) <= lim || prefix(n, w) >= 4 || (len(w) >= 3 && strings.HasPrefix(n, w)) || (len(n) >= 3 && strings.HasPrefix(w, n)) {
					close = true
					break
				}
			}
		}
		if !close {
			return "different"
		}
		variant = true
	}
	if variant {
		return "variant"
	}
	return "match"
}
