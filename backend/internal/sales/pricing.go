package sales

import (
	"math"
	"sort"
	"time"
)

// Pure pricing maths: per-line discounts (ORD-03), delivery charges (DEL-02), deposits and due dates (ORD-06).

// lineCalc is the money of one product line. Net is stored as "SaleLine".amount.
type lineCalc struct {
	Gross, Discount, Net int64
}

// roundHalfUp rounds a non-negative amount to whole francs.
func roundHalfUp(x float64) int64 { return int64(math.Floor(x + 0.5)) }

// lineAmounts: gross = qty × price; a percent discount (0–100) wins over an amount discount; the discount never
// exceeds the gross amount. ok=false when the amount discount is larger than the line.
func lineAmounts(qty, price int64, pct *float64, amount int64) (lineCalc, bool) {
	gross := qty * price
	var disc int64
	switch {
	case pct != nil:
		p := math.Max(0, math.Min(100, *pct))
		disc = roundHalfUp(float64(gross) * p / 100)
	case amount > 0:
		if amount > gross {
			return lineCalc{Gross: gross, Discount: gross, Net: 0}, false
		}
		disc = amount
	}
	if disc > gross {
		disc = gross
	}
	return lineCalc{Gross: gross, Discount: disc, Net: gross - disc}, true
}

// netNeedsGmApproval: the effective unit price (net / qty) is more than maxDiscountPercent below the default price.
// net*100 < defaultPrice*qty*(100-pct). Products without a default price are free-priced.
func netNeedsGmApproval(net, qty, defaultPrice int64, maxDiscountPercent float64) bool {
	if !(defaultPrice > 0) || qty <= 0 {
		return false
	}
	return float64(net)*100 < float64(defaultPrice)*float64(qty)*(100-maxDiscountPercent)
}

// ───────────────────────────── delivery charges (DEL-02) ─────────────────────────────

// Charge methods.
const (
	ChargeFixed    = "FIXED"
	ChargeZone     = "ZONE"
	ChargeDistance = "DISTANCE"
)

// Transport arrangements (DEL-01).
const (
	TransportClient = "CLIENT" // the client collects with its own truck
	TransportBTN    = "BTN"    // BTN delivers (delivery charge applies)
)

type chargeRule struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Method   string   `json:"method"`
	Zone     *string  `json:"zone"`
	MinKm    *float64 `json:"minKm"`
	MaxKm    *float64 `json:"maxKm"`
	Amount   int64    `json:"amount"`
	IsActive bool     `json:"isActive"`
	Sort     int      `json:"sortOrder"`
}

// pickCharge chooses the delivery charge for an order delivered by BTN. Precedence: a rule for the order's zone,
// then the distance band containing the distance ([min, max)), then the fixed charge. Inside one method the lowest
// sort order wins. Returns (0, nil) when nothing applies.
func pickCharge(rules []chargeRule, zone string, km *float64) (int64, *chargeRule) {
	act := make([]chargeRule, 0, len(rules))
	for _, r := range rules {
		if r.IsActive {
			act = append(act, r)
		}
	}
	sort.SliceStable(act, func(i, j int) bool { return act[i].Sort < act[j].Sort })
	if zone != "" {
		for i := range act {
			if act[i].Method == ChargeZone && act[i].Zone != nil && *act[i].Zone == zone {
				return act[i].Amount, &act[i]
			}
		}
	}
	if km != nil {
		for i := range act {
			r := act[i]
			if r.Method == ChargeDistance && r.MinKm != nil && *km >= *r.MinKm && (r.MaxKm == nil || *km < *r.MaxKm) {
				return r.Amount, &act[i]
			}
		}
	}
	for i := range act {
		if act[i].Method == ChargeFixed {
			return act[i].Amount, &act[i]
		}
	}
	return 0, nil
}

// ───────────────────────────── payment terms (ORD-06) ─────────────────────────────

type paymentTerm struct {
	Days           int     `json:"days"`
	DepositPercent float64 `json:"depositPercent"`
}

// depositFor: the deposit a term requires on an order total (rounded half up).
func depositFor(t paymentTerm, total int64) int64 {
	if t.DepositPercent <= 0 {
		return 0
	}
	return roundHalfUp(float64(total) * math.Min(100, t.DepositPercent) / 100)
}

// dueDateFor: when the balance is due. Deposit terms: balance due `days` after delivery (unknown until delivered);
// other terms: `days` after confirmation.
func dueDateFor(t paymentTerm, confirmed time.Time, delivered *time.Time) *time.Time {
	base := confirmed
	if t.DepositPercent > 0 {
		if delivered == nil {
			return nil
		}
		base = *delivered
	}
	d := time.Date(base.Year(), base.Month(), base.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, t.Days)
	return &d
}

// Payment kinds.
const (
	PayKindDeposit = "DEPOSIT"
	PayKindPart    = "PART"
	PayKindBalance = "BALANCE"
)

// paymentKind classifies a payment: settling the order → BALANCE; the first payment of an order that requires a
// deposit, before anything was shipped → DEPOSIT; anything else → PART.
func paymentKind(total, paidBefore, amount, depositRequired int64, firstPayment, shipped bool) string {
	if paidBefore+amount >= total {
		return PayKindBalance
	}
	if firstPayment && !shipped && depositRequired > 0 {
		return PayKindDeposit
	}
	return PayKindPart
}

// depositStatus: NONE (not required), DUE, PARTIAL or RECEIVED.
func depositStatus(required, paid int64) string {
	switch {
	case required <= 0:
		return "NONE"
	case paid >= required:
		return "RECEIVED"
	case paid > 0:
		return "PARTIAL"
	}
	return "DUE"
}
