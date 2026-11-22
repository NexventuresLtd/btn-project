// Package materials is the raw-material ledger module (stores): balances, the Excel-style daily sheet,
// receipts, transfers, usage, adjustments, opening balances and two PDFs. Port of legacy modules/materials.
//
// Quantities are numeric(14,3). Everything is accumulated in integer thousandths ("milli-units")
// so summing many movements never drifts.
package materials

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

// Movement types (Postgres enum "MaterialMovementType").
const (
	TypeOpening     = "OPENING"
	TypeReceipt     = "RECEIPT"
	TypeTransferOut = "TRANSFER_OUT"
	TypeTransferIn  = "TRANSFER_IN"
	TypeConsumption = "CONSUMPTION"
	TypeAdjustment  = "ADJUSTMENT"
)

// MovementTypes lists the valid values in legacy order.
var MovementTypes = []string{TypeOpening, TypeReceipt, TypeTransferOut, TypeTransferIn, TypeConsumption, TypeAdjustment}

func toMilli(n float64) int64   { return int64(math.Round(n * 1000)) }
func fromMilli(m int64) float64 { return float64(m) / 1000 }

// round3 rounds a quantity to the 3 decimals the ledger stores.
func round3(n float64) float64 { return fromMilli(toMilli(n)) }

// hasAtMost3Decimals is true when n fits numeric(14,3) without rounding.
func hasAtMost3Decimals(n float64) bool {
	return math.Abs(n*1000-math.Round(n*1000)) < 1e-6
}

// parseMilli parses a Postgres numeric text value into milli-units.
func parseMilli(s string) int64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return toMilli(f)
}

// MaterialInfo is a catalogue material as the frontend expects it.
type MaterialInfo struct {
	ID       string  `json:"id"`
	Code     string  `json:"code"`
	Name     string  `json:"name"`
	Unit     string  `json:"unit"`
	MinLevel float64 `json:"minLevel"`
}

// MovementAgg is one aggregated bucket (date, material, type, counterpart) -> summed signed quantity.
type MovementAgg struct {
	Date            string // YYYY-MM-DD
	MaterialID      string
	Type            string
	Quantity        float64 // signed, as stored
	CounterpartCode string
}

// DailyRow is one material on one day (or one period).
type DailyRow struct {
	MaterialID            string             `json:"materialId"`
	Code                  string             `json:"code"`
	Name                  string             `json:"name"`
	Unit                  string             `json:"unit"`
	MinLevel              float64            `json:"minLevel"`
	Opening               float64            `json:"opening"`
	Received              float64            `json:"received"`
	ReceivedFromSuppliers float64            `json:"receivedFromSuppliers"`
	ReceivedFrom          map[string]float64 `json:"receivedFrom"`
	TransferredOut        float64            `json:"transferredOut"`
	TransferredTo         map[string]float64 `json:"transferredTo"`
	Used                  float64            `json:"used"`
	Adjustment            float64            `json:"adjustment"`
	Closing               float64            `json:"closing"`
	BelowMin              bool               `json:"belowMin"`
	Active                bool               `json:"active"`
}

// DailyDay is one date of the sheet.
type DailyDay struct {
	Date   string     `json:"date"`
	Active bool       `json:"active"`
	Rows   []DailyRow `json:"rows"`
}

type acc struct {
	opening, received, receivedFromSuppliers int64
	receivedFrom                             map[string]int64
	out                                      int64
	outTo                                    map[string]int64
	used, adj                                int64
	active                                   bool
}

func newAcc() *acc { return &acc{receivedFrom: map[string]int64{}, outTo: map[string]int64{}} }

func mapMilli(m map[string]int64) map[string]float64 {
	out := make(map[string]float64, len(m))
	for k, v := range m {
		out[k] = fromMilli(v)
	}
	return out
}

// isBelowMin: a material counts as below minimum only once the plant actually stocks it (has any movement).
func isBelowMin(balance, minLevel float64, tracked bool) bool {
	return tracked && minLevel > 0 && toMilli(balance) < toMilli(minLevel)
}

// buildDailyReport builds the Excel-style daily material sheet for one plant:
// Opening | Entered stock | Goes to BTN-x | Used | Adjustment | Closing.
// openingBefore is the per-material balance of all movements before the first day (a missing key
// means the plant had no movement of that material before the range).
func buildDailyReport(dates []string, materials []MaterialInfo, openingBefore map[string]float64, movements []MovementAgg) []DailyDay {
	byDay := map[string]map[string]*acc{}
	for _, m := range movements {
		day := byDay[m.Date]
		if day == nil {
			day = map[string]*acc{}
			byDay[m.Date] = day
		}
		a := day[m.MaterialID]
		if a == nil {
			a = newAcc()
			day[m.MaterialID] = a
		}
		q := toMilli(m.Quantity)
		cp := m.CounterpartCode
		if cp == "" {
			cp = "Other"
		}
		a.active = true
		switch m.Type {
		case TypeOpening:
			a.opening += q
		case TypeReceipt:
			a.received += q
			a.receivedFromSuppliers += q
		case TypeTransferIn:
			a.received += q
			a.receivedFrom[cp] += q
		case TypeTransferOut:
			a.out += -q
			a.outTo[cp] += -q
		case TypeConsumption:
			a.used += -q
		case TypeAdjustment:
			a.adj += q
		}
	}
	carried := map[string]int64{}
	tracked := map[string]bool{}
	for _, mat := range materials {
		if v, ok := openingBefore[mat.ID]; ok {
			tracked[mat.ID] = true
			carried[mat.ID] = toMilli(v)
		}
	}
	out := make([]DailyDay, 0, len(dates))
	for _, date := range dates {
		day := byDay[date]
		rows := make([]DailyRow, 0, len(materials))
		anyActive := false
		for _, mat := range materials {
			var a *acc
			if day != nil {
				a = day[mat.ID]
			}
			if a == nil {
				a = newAcc()
			}
			if a.active {
				tracked[mat.ID] = true
				anyActive = true
			}
			opening := carried[mat.ID] + a.opening
			closing := opening + a.received - a.out - a.used + a.adj
			carried[mat.ID] = closing
			rows = append(rows, DailyRow{
				MaterialID: mat.ID, Code: mat.Code, Name: mat.Name, Unit: mat.Unit, MinLevel: mat.MinLevel,
				Opening: fromMilli(opening), Received: fromMilli(a.received), ReceivedFromSuppliers: fromMilli(a.receivedFromSuppliers),
				ReceivedFrom: mapMilli(a.receivedFrom), TransferredOut: fromMilli(a.out), TransferredTo: mapMilli(a.outTo),
				Used: fromMilli(a.used), Adjustment: fromMilli(a.adj), Closing: fromMilli(closing),
				BelowMin: isBelowMin(fromMilli(closing), mat.MinLevel, tracked[mat.ID]), Active: a.active,
			})
		}
		out = append(out, DailyDay{Date: date, Active: anyActive, Rows: rows})
	}
	return out
}

// summarizePeriod collapses a run of days into one row per material (opening of the first day,
// closing of the last). OPENING movements after the first day fold into "adjustment" so
// opening + flows = closing.
func summarizePeriod(days []DailyDay) []DailyRow {
	if len(days) == 0 {
		return []DailyRow{}
	}
	first, last := days[0], days[len(days)-1]
	out := make([]DailyRow, 0, len(first.Rows))
	for i, r := range first.Rows {
		var received, sup, outQ, used, adj, openingMovements int64
		to, from := map[string]int64{}, map[string]int64{}
		for di, d := range days {
			x := d.Rows[i]
			received += toMilli(x.Received)
			sup += toMilli(x.ReceivedFromSuppliers)
			outQ += toMilli(x.TransferredOut)
			used += toMilli(x.Used)
			adj += toMilli(x.Adjustment)
			if di > 0 {
				openingMovements += toMilli(x.Opening) - toMilli(days[di-1].Rows[i].Closing)
			}
			for k, v := range x.TransferredTo {
				to[k] += toMilli(v)
			}
			for k, v := range x.ReceivedFrom {
				from[k] += toMilli(v)
			}
		}
		out = append(out, DailyRow{
			MaterialID: r.MaterialID, Code: r.Code, Name: r.Name, Unit: r.Unit, MinLevel: r.MinLevel,
			Opening: r.Opening, Received: fromMilli(received), ReceivedFromSuppliers: fromMilli(sup), ReceivedFrom: mapMilli(from),
			TransferredOut: fromMilli(outQ), TransferredTo: mapMilli(to), Used: fromMilli(used),
			Adjustment: fromMilli(adj + openingMovements), Closing: last.Rows[i].Closing, BelowMin: last.Rows[i].BelowMin,
		})
	}
	return out
}

// availableToWithdraw: how much can be withdrawn on a date without making the balance negative on that
// date or on any later date that already has movements. laterDailyNet = net movement per later date, in date order.
func availableToWithdraw(balanceAtDate float64, laterDailyNet []float64) float64 {
	running := toMilli(balanceAtDate)
	min := running
	for _, net := range laterDailyNet {
		running += toMilli(net)
		if running < min {
			min = running
		}
	}
	return fromMilli(min)
}

// withdrawalCheck is the result of checkWithdrawal.
type withdrawalCheck struct {
	OK        bool
	Available float64 // never negative
	Shortfall float64
}

func checkWithdrawal(available, quantity float64) withdrawalCheck {
	a, q := toMilli(available), toMilli(quantity)
	free := a
	if free < 0 {
		free = 0
	}
	short := q - free
	if short < 0 {
		short = 0
	}
	return withdrawalCheck{OK: q <= a, Available: fromMilli(free), Shortfall: fromMilli(short)}
}

// fmtQty formats like Intl.NumberFormat("en-US", {maximumFractionDigits: 3}): "1,234.5".
func fmtQty(n float64) string {
	s := strconv.FormatFloat(round3(n), 'f', -1, 64)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	whole, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	for i, ch := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	r := b.String()
	if frac != "" {
		r += "." + frac
	}
	if neg && r != "0" {
		r = "-" + r
	}
	return r
}

func sortedKeys(m map[string]float64) []string {
	k := make([]string, 0, len(m))
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}
