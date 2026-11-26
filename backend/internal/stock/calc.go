package stock

// Pure finished-goods calculations (port of the legacy calc.ts).
//
// Daily Block Report closing = Opening + Production - Damage 1 - Issue/Sold + Returned - Sample - Damage 2 (+ signed Adjustment).

// Movement kinds in ledger order.
const (
	Opening          = "OPENING"
	Production       = "PRODUCTION"
	DamageProduction = "DAMAGE_PRODUCTION"
	DamageHandling   = "DAMAGE_HANDLING"
	Sample           = "SAMPLE"
	SaleIssue        = "SALE_ISSUE"
	Return           = "RETURN"
	Adjustment       = "ADJUSTMENT"
	Receipt          = "RECEIPT"      // P2: goods received (procurement / manual)
	Issue            = "ISSUE"        // P2: issued for other use than sale
	TransferOut      = "TRANSFER_OUT" // P2: left for another location (in transit)
	TransferIn       = "TRANSFER_IN"  // P2: received from another location
)

// MovementTypes lists the valid kinds.
var MovementTypes = []string{Opening, Production, DamageProduction, DamageHandling, Sample, SaleIssue, Return, Adjustment, Receipt, Issue, TransferOut, TransferIn}

// LedgerEntry is a (summed) ledger line; Quantity is signed as stored (+ in, - out).
type LedgerEntry struct {
	Date      string
	ProductID string
	Type      string
	Quantity  int
}

// Figures are the Excel sheet columns (outflows shown positive).
type Figures struct {
	Opening    int `json:"opening"`
	Production int `json:"production"`
	Damage1    int `json:"damage1"`
	Sold       int `json:"sold"`
	Returned   int `json:"returned"`
	Sample     int `json:"sample"`
	Damage2    int `json:"damage2"`
	Adjustment int `json:"adjustment"`
	Received   int `json:"received"`    // receipts (P2)
	Issued     int `json:"issued"`      // non-sale issues, shown positive (P2)
	TransferIn int `json:"transferIn"`  // P2
	TransferOt int `json:"transferOut"` // P2, shown positive
	Closing    int `json:"closing"`
}

// DailyRow is one product on one day.
type DailyRow struct {
	ProductID string
	Figures
}

// DailyDay is one business day.
type DailyDay struct {
	Date   string
	Rows   []DailyRow
	Totals Figures
}

// ClosingFromColumns is the Excel closing formula extended with the signed adjustment.
func ClosingFromColumns(r Figures) int {
	return r.Opening + r.Production - r.Damage1 - r.Sold + r.Returned - r.Sample - r.Damage2 + r.Adjustment +
		r.Received - r.Issued + r.TransferIn - r.TransferOt
}

func apply(f *Figures, typ string, qty int) {
	switch typ {
	case Opening:
		f.Opening += qty
	case Production:
		f.Production += qty
	case DamageProduction:
		f.Damage1 -= qty
	case DamageHandling:
		f.Damage2 -= qty
	case Sample:
		f.Sample -= qty
	case SaleIssue:
		f.Sold -= qty
	case Return:
		f.Returned += qty
	case Adjustment:
		f.Adjustment += qty
	case Receipt:
		f.Received += qty
	case Issue:
		f.Issued -= qty
	case TransferIn:
		f.TransferIn += qty
	case TransferOut:
		f.TransferOt -= qty
	}
}

// IsZeroRow is true when no figure is set.
func IsZeroRow(r Figures) bool {
	return r.Opening == 0 && r.Production == 0 && r.Damage1 == 0 && r.Sold == 0 && r.Returned == 0 && r.Sample == 0 && r.Damage2 == 0 && r.Adjustment == 0 && r.Closing == 0 &&
		r.Received == 0 && r.Issued == 0 && r.TransferIn == 0 && r.TransferOt == 0
}

// SumRows adds figures column-wise.
func SumRows(rows []Figures) Figures {
	var t Figures
	for _, r := range rows {
		t.Opening += r.Opening
		t.Production += r.Production
		t.Damage1 += r.Damage1
		t.Sold += r.Sold
		t.Returned += r.Returned
		t.Sample += r.Sample
		t.Damage2 += r.Damage2
		t.Adjustment += r.Adjustment
		t.Received += r.Received
		t.Issued += r.Issued
		t.TransferIn += r.TransferIn
		t.TransferOt += r.TransferOt
		t.Closing += r.Closing
	}
	return t
}

// BuildDailyReport builds the Daily Block Report.
//
//	dates          every business day in the range, ascending
//	productIDs     products in display order
//	openingBefore  balance per product from all movements dated before the first day
//	entries        movements dated within the range
//	omitEmpty      drop products whose figures are zero on every day of the range
func BuildDailyReport(dates, productIDs []string, openingBefore map[string]int, entries []LedgerEntry, omitEmpty bool) []DailyDay {
	byDay := map[string]map[string][]LedgerEntry{}
	for _, e := range entries {
		d := byDay[e.Date]
		if d == nil {
			d = map[string][]LedgerEntry{}
			byDay[e.Date] = d
		}
		d[e.ProductID] = append(d[e.ProductID], e)
	}
	running := make(map[string]int, len(productIDs))
	for _, id := range productIDs {
		running[id] = openingBefore[id]
	}
	type rawDay struct {
		date string
		rows []DailyRow
	}
	raw := make([]rawDay, 0, len(dates))
	active := map[string]bool{}
	for _, date := range dates {
		day := byDay[date]
		rows := make([]DailyRow, 0, len(productIDs))
		for _, pid := range productIDs {
			var f Figures
			f.Opening = running[pid]
			for _, e := range day[pid] {
				apply(&f, e.Type, e.Quantity)
			}
			f.Closing = ClosingFromColumns(f)
			running[pid] = f.Closing
			if !IsZeroRow(f) {
				active[pid] = true
			}
			rows = append(rows, DailyRow{ProductID: pid, Figures: f})
		}
		raw = append(raw, rawDay{date, rows})
	}
	out := make([]DailyDay, 0, len(raw))
	for _, d := range raw {
		rows := d.rows
		if omitEmpty {
			kept := make([]DailyRow, 0, len(rows))
			for _, r := range rows {
				if active[r.ProductID] {
					kept = append(kept, r)
				}
			}
			rows = kept
		}
		figs := make([]Figures, len(rows))
		for i, r := range rows {
			figs[i] = r.Figures
		}
		out = append(out, DailyDay{Date: d.date, Rows: rows, Totals: SumRows(figs)})
	}
	return out
}
