package sales

import (
	"math"
	"sort"
	"strings"
	"time"
)

// Pure calculations for sales, deliveries, payments and receivables (port of modules/sales/calc.ts).
// No database access here, so every rule can be unit-tested. The SQL in service.go mirrors
// paymentStatus()/deliveryStatus(); keep them in step.

// PaymentStatus and DeliveryStatus are the derived progress states of a sale.
const (
	PayUnpaid  = "UNPAID"
	PayPartial = "PARTIAL"
	PayPaid    = "PAID"

	DelNone    = "NOT_DELIVERED"
	DelPartial = "PARTIAL"
	DelDone    = "DELIVERED"
)

// paymentStatus: PAID once payments cover the amount (a zero-value sale is PAID); PARTIAL when something was paid.
func paymentStatus(amount, paid int64) string {
	if paid >= amount {
		return PayPaid
	}
	if paid > 0 {
		return PayPartial
	}
	return PayUnpaid
}

// deliveryStatus is based on quantities issued on non-cancelled delivery notes.
func deliveryStatus(ordered, delivered int64) string {
	if delivered <= 0 {
		return DelNone
	}
	if delivered >= ordered {
		return DelDone
	}
	return DelPartial
}

// outstandingOf is the amount still owed on a sale. Only confirmed sales are receivables.
func outstandingOf(status string, amount, paid int64) int64 {
	if status == "CONFIRMED" {
		return max(0, amount-paid)
	}
	return 0
}

// lineNeedsGmApproval: a line needs General Manager confirmation when its price is more than
// maxDiscountPercent below the product's default price. Products without a default price are free-priced.
// unitPrice < defaultPrice*(1-pct/100)  <=>  unitPrice*100 < defaultPrice*(100-pct).
func lineNeedsGmApproval(unitPrice, defaultPrice int64, maxDiscountPercent float64) bool {
	if !(defaultPrice > 0) {
		return false
	}
	return float64(unitPrice)*100 < float64(defaultPrice)*(100-maxDiscountPercent)
}

type priced struct{ UnitPrice, DefaultPrice int64 }

func saleNeedsGmApproval(lines []priced, maxDiscountPercent float64) bool {
	for _, l := range lines {
		if lineNeedsGmApproval(l.UnitPrice, l.DefaultPrice, maxDiscountPercent) {
			return true
		}
	}
	return false
}

// minPriceWithoutApproval is the lowest unit price a Sales Manager may confirm without GM approval (rounded up).
func minPriceWithoutApproval(defaultPrice int64, maxDiscountPercent float64) int64 {
	if !(defaultPrice > 0) {
		return 0
	}
	return int64(math.Ceil(float64(defaultPrice) * (100 - maxDiscountPercent) / 100))
}

// discountRatio against the default price (0.1 = 10 %); nil when there is no default price.
func discountRatio(unitPrice, defaultPrice int64) *float64 {
	if !(defaultPrice > 0) {
		return nil
	}
	r := float64(defaultPrice-unitPrice) / float64(defaultPrice)
	return &r
}

// ───────────────────────────── deliveries & returns ─────────────────────────────

type lineProgress struct {
	ID        string
	ProductID string
	Quantity  int64
	Delivered int64
}

func remainingToDeliver(quantity, delivered int64) int64 { return max(0, quantity-delivered) }

type deliveryReq struct {
	SaleLineID string
	Quantity   int64
}

// requestIssue is a problem with one requested dispatch line. Key/Args are i18n coordinates.
type requestIssue struct {
	Index int
	Field string
	Key   string
	Args  []any
}

const (
	issNotInSale  = "sales.issue.line_not_in_sale"
	issTwice      = "sales.issue.line_twice"
	issFullyDone  = "sales.issue.fully_delivered"
	issOnlyNLeft  = "sales.issue.only_left"
	issProdTwice  = "sales.issue.product_twice"
	issNotDeliv   = "sales.issue.product_not_delivered"
	issAllReturnd = "sales.issue.all_returned"
	issAtMostNRet = "sales.issue.at_most_returnable"
)

// checkDeliveryRequest validates a dispatch request against the sale's lines: each line must belong
// to the sale, appear once, and not exceed what is still undelivered.
func checkDeliveryRequest(lines []lineProgress, request []deliveryReq) []requestIssue {
	byID := make(map[string]lineProgress, len(lines))
	for _, l := range lines {
		byID[l.ID] = l
	}
	seen := map[string]bool{}
	var issues []requestIssue
	for index, r := range request {
		line, ok := byID[r.SaleLineID]
		if !ok {
			issues = append(issues, requestIssue{Index: index, Field: "saleLineId", Key: issNotInSale})
			continue
		}
		if seen[r.SaleLineID] {
			issues = append(issues, requestIssue{Index: index, Field: "saleLineId", Key: issTwice})
			continue
		}
		seen[r.SaleLineID] = true
		remaining := remainingToDeliver(line.Quantity, line.Delivered)
		if r.Quantity > remaining {
			if remaining == 0 {
				issues = append(issues, requestIssue{Index: index, Field: "quantity", Key: issFullyDone})
			} else {
				issues = append(issues, requestIssue{Index: index, Field: "quantity", Key: issOnlyNLeft, Args: []any{"n", remaining}})
			}
		}
	}
	return issues
}

// qtyMap is an insertion-ordered product -> quantity map (Node Map semantics).
type qtyMap struct {
	keys []string
	m    map[string]int64
}

func newQtyMap() *qtyMap { return &qtyMap{m: map[string]int64{}} }

func (q *qtyMap) add(k string, v int64) {
	if _, ok := q.m[k]; !ok {
		q.keys = append(q.keys, k)
	}
	q.m[k] += v
}
func (q *qtyMap) get(k string) (int64, bool) { v, ok := q.m[k]; return v, ok }

// sumByProduct sums quantities per product, summing duplicates.
func sumByProduct(rows []productQty) *qtyMap {
	m := newQtyMap()
	for _, r := range rows {
		m.add(r.ProductID, r.Quantity)
	}
	return m
}

type productQty struct {
	ProductID string
	Quantity  int64
}

// returnableByProduct = delivered - already returned, per product (never negative).
func returnableByProduct(delivered, returned *qtyMap) *qtyMap {
	out := newQtyMap()
	for _, k := range delivered.keys {
		r, _ := returned.get(k)
		out.add(k, max(0, delivered.m[k]-r))
	}
	return out
}

// availableForIssue is the stock that can be issued on a date without driving the ledger negative on
// that date or any later date. laterDailyDeltas are the net movements of each later day, in date order.
func availableForIssue(balanceAtDate int64, laterDailyDeltas []int64) int64 {
	running, lowest := balanceAtDate, balanceAtDate
	for _, d := range laterDailyDeltas {
		running += d
		if running < lowest {
			lowest = running
		}
	}
	return max(0, lowest)
}

// ───────────────────────────── ageing ─────────────────────────────

type ageBucket string

const (
	bucket0   ageBucket = "d0_30"
	bucket31  ageBucket = "d31_60"
	bucket61  ageBucket = "d61_90"
	bucket90p ageBucket = "d90p"
)

var ageBuckets = []ageBucket{bucket0, bucket31, bucket61, bucket90p}

const dayMS = 86_400_000

// daysBetween: whole days between two business dates (UTC midnights).
func daysBetween(from, to time.Time) int {
	return int(math.Round(float64(to.Sub(from).Milliseconds()) / dayMS))
}

func bucketOf(days int) ageBucket {
	switch {
	case days <= 30:
		return bucket0
	case days <= 60:
		return bucket31
	case days <= 90:
		return bucket61
	}
	return bucket90p
}

type openSale struct {
	CustomerID, CustomerName string
	Date                     time.Time
	Outstanding              int64
}

type buckets struct {
	D0_30  int64 `json:"d0_30"`
	D31_60 int64 `json:"d31_60"`
	D61_90 int64 `json:"d61_90"`
	D90p   int64 `json:"d90p"`
	Total  int64 `json:"total"`
}

func (b *buckets) add(k ageBucket, v int64) {
	switch k {
	case bucket0:
		b.D0_30 += v
	case bucket31:
		b.D31_60 += v
	case bucket61:
		b.D61_90 += v
	default:
		b.D90p += v
	}
	b.Total += v
}

func (b buckets) of(k ageBucket) int64 {
	switch k {
	case bucket0:
		return b.D0_30
	case bucket31:
		return b.D31_60
	case bucket61:
		return b.D61_90
	}
	return b.D90p
}

type ageingRow struct {
	CustomerID   string     `json:"customerId"`
	CustomerName string     `json:"customerName"`
	OpenSales    int        `json:"openSales"`
	OldestDate   *time.Time `json:"-"`
	buckets
}

type ageingTotals struct {
	buckets
	OpenSales int `json:"openSales"`
}

// buildAgeing groups open sales into per-customer ageing rows (largest balance first) plus grand totals.
func buildAgeing(sales []openSale, asOf time.Time) ([]*ageingRow, ageingTotals) {
	idx := map[string]*ageingRow{}
	var rows []*ageingRow
	var totals ageingTotals
	for _, s := range sales {
		if s.Outstanding <= 0 {
			continue
		}
		row := idx[s.CustomerID]
		if row == nil {
			row = &ageingRow{CustomerID: s.CustomerID, CustomerName: s.CustomerName}
			idx[s.CustomerID] = row
			rows = append(rows, row)
		}
		b := bucketOf(daysBetween(s.Date, asOf))
		row.add(b, s.Outstanding)
		row.OpenSales++
		if row.OldestDate == nil || s.Date.Before(*row.OldestDate) {
			d := s.Date
			row.OldestDate = &d
		}
		totals.add(b, s.Outstanding)
		totals.OpenSales++
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Total != rows[j].Total {
			return rows[i].Total > rows[j].Total
		}
		return strings.ToLower(rows[i].CustomerName) < strings.ToLower(rows[j].CustomerName)
	})
	return rows, totals
}

// ───────────────────────────── statement ─────────────────────────────

type statementEntry struct {
	Date        time.Time
	Kind        string // "SALE" | "PAYMENT"
	Reference   string
	Description string
	Debit       int64
	Credit      int64
	Order       int64 // tie-breaker inside a day (creation time)
	SaleID      string
	Balance     int64
}

// buildStatement sorts by date (sales before payments on the same day) and carries a running balance.
func buildStatement(opening int64, entries []statementEntry) (rows []statementEntry, debit, credit, closing int64) {
	rows = append([]statementEntry(nil), entries...)
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if !a.Date.Equal(b.Date) {
			return a.Date.Before(b.Date)
		}
		if a.Kind == b.Kind {
			return a.Order < b.Order
		}
		return a.Kind == "SALE"
	})
	balance := opening
	for i := range rows {
		balance += rows[i].Debit - rows[i].Credit
		debit += rows[i].Debit
		credit += rows[i].Credit
		rows[i].Balance = balance
	}
	return rows, debit, credit, balance
}
