package procurement

// Pure procurement rules (no I/O): status machines (PRC-05), approval routing by value and requester (PRC-02),
// receipt maths incl. the over/under-receipt rule (PRC-06, SRS §7.3) and low-stock proposal quantities (PRC-07).
// Unit-tested in workflow_test.go.

import (
	"math"
	"sort"

	"btn-ops/internal/approvals"
	"btn-ops/internal/auth"
)

// Request statuses (PRC-05).
const (
	ReqDraft             = "DRAFT"
	ReqSubmitted         = "SUBMITTED"
	ReqApproved          = "APPROVED"
	ReqRejected          = "REJECTED"
	ReqOrdered           = "ORDERED"
	ReqPartiallyReceived = "PARTIALLY_RECEIVED"
	ReqReceived          = "RECEIVED"
	ReqClosed            = "CLOSED"
)

// RequestStatuses in lifecycle order.
var RequestStatuses = []string{ReqDraft, ReqSubmitted, ReqApproved, ReqRejected, ReqOrdered, ReqPartiallyReceived, ReqReceived, ReqClosed}

// Purchase order statuses (PRC-05).
const (
	PODraft             = "DRAFT"
	POOrdered           = "ORDERED"
	POPartiallyReceived = "PARTIALLY_RECEIVED"
	POReceived          = "RECEIVED"
	POClosed            = "CLOSED"
	POCancelled         = "CANCELLED"
)

// OrderStatuses in lifecycle order.
var OrderStatuses = []string{PODraft, POOrdered, POPartiallyReceived, POReceived, POClosed, POCancelled}

var requestFlow = map[string][]string{
	ReqDraft:             {ReqSubmitted, ReqApproved, ReqClosed}, // APPROVED directly when no approval level applies
	ReqSubmitted:         {ReqApproved, ReqRejected, ReqDraft},   // DRAFT = recalled by the requester before any decision
	ReqApproved:          {ReqOrdered, ReqClosed},
	ReqRejected:          {ReqDraft},                                                  // revise and resubmit
	ReqOrdered:           {ReqPartiallyReceived, ReqReceived, ReqApproved, ReqClosed}, // APPROVED = every PO cancelled
	ReqPartiallyReceived: {ReqReceived, ReqClosed},
	ReqReceived:          {ReqClosed},
}

var orderFlow = map[string][]string{
	PODraft:             {POOrdered, POCancelled},
	POOrdered:           {POPartiallyReceived, POReceived, POCancelled, POClosed},
	POPartiallyReceived: {POReceived, POClosed},
	POReceived:          {POClosed},
}

func allowed(flow map[string][]string, from, to string) bool {
	if from == to {
		return false
	}
	for _, x := range flow[from] {
		if x == to {
			return true
		}
	}
	return false
}

// CanRequestMove reports whether a request may move from → to.
func CanRequestMove(from, to string) bool { return allowed(requestFlow, from, to) }

// CanOrderMove reports whether a purchase order may move from → to.
func CanOrderMove(from, to string) bool { return allowed(orderFlow, from, to) }

// ───────────────────────────── approval routing (PRC-02) ─────────────────────────────

// RequiredLevel is one approval step frozen on a request at submit time.
type RequiredLevel struct {
	Level      int     `json:"level"`
	Permission string  `json:"permission"`
	Role       *string `json:"role"`
	Label      string  `json:"label"`
	MinAmount  int64   `json:"minAmount"`
	// Escalated is true when this level replaced one the requester would have approved themself.
	Escalated bool `json:"escalated,omitempty"`
}

func (l RequiredLevel) asLevel() approvals.Level {
	return approvals.Level{Level: l.Level, Permission: l.Permission, Role: l.Role, Label: l.Label, MinAmount: l.MinAmount}
}

// Satisfies reports whether a user with role may decide this level.
func (l RequiredLevel) Satisfies(role auth.Role) bool { return l.asLevel().Satisfies(role) }

func roleIs(r *string, role auth.Role) bool { return r != nil && *r != "" && auth.Role(*r) == role }

// RouteLevels decides the approval chain of a request (PRC-02) from the configured thresholds of the procurement
// domain, the request value and the requester:
//   - value: every active level whose [min, max) band contains the amount (approvals.Match), lowest first;
//   - requester: a level reserved to the requester's own role (e.g. a Production Manager's own request at the
//     "Production manager approval" level) cannot be approved by them, so it is covered by a higher level of the chain
//     when one already applies, otherwise escalated to the next configured higher level (any amount) whose role differs.
//     If no such level exists it stays (another holder of that role must approve).
//
// Empty result = no approval needed (the request is approved on submit).
func RouteLevels(all []approvals.Threshold, amount int64, requester auth.Role) []RequiredLevel {
	matched := approvals.Match(all, amount)
	out := []RequiredLevel{}
	have := map[int]bool{}
	add := func(l RequiredLevel) {
		if !have[l.Level] {
			have[l.Level] = true
			out = append(out, l)
		}
	}
	for i, l := range matched {
		rl := RequiredLevel{Level: l.Level, Permission: l.Permission, Role: l.Role, Label: l.Label, MinAmount: l.MinAmount}
		if !roleIs(l.Role, requester) {
			add(rl)
			continue
		}
		covered := false
		for _, h := range matched[i+1:] {
			if !roleIs(h.Role, requester) {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		if esc, ok := nextHigher(all, l.Level, requester); ok {
			esc.Escalated = true
			add(esc)
			continue
		}
		add(rl)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Level < out[j].Level })
	return out
}

// nextHigher is the lowest active threshold above level whose role is not the requester's (lowest min amount wins).
func nextHigher(all []approvals.Threshold, level int, requester auth.Role) (RequiredLevel, bool) {
	var best *approvals.Threshold
	for i := range all {
		t := all[i]
		if !t.IsActive || t.Level <= level || roleIs(t.Role, requester) {
			continue
		}
		if best == nil || t.Level < best.Level || (t.Level == best.Level && t.MinAmount < best.MinAmount) {
			best = &all[i]
		}
	}
	if best == nil {
		return RequiredLevel{}, false
	}
	return RequiredLevel{Level: best.Level, Permission: best.Permission, Role: best.Role, Label: best.Label, MinAmount: best.MinAmount}, true
}

// Decision already taken on a request in the current cycle.
type Decision struct {
	Level  int
	UserID string
}

// Approver refusal reasons (i18n keys procurement.err.<reason>).
const (
	DenySelf          = "self_approval"
	DenyNotLevel      = "not_your_level"
	DenyAlreadyActed  = "already_decided"
	DenyNotSubmitted  = "not_submitted"
	DenyNoLevelOpen   = "no_level_open"
	DenyNotPermission = "not_approver"
)

// NextLevel returns the lowest required level without a decision (ok=false when all are decided).
func NextLevel(levels []RequiredLevel, decided []Decision) (RequiredLevel, bool) {
	done := map[int]bool{}
	for _, d := range decided {
		done[d.Level] = true
	}
	for _, l := range levels {
		if !done[l.Level] {
			return l, true
		}
	}
	return RequiredLevel{}, false
}

// CheckApprover says whether userID (role) may decide the current level of a submitted request, or why not:
// never the requester (separation of duties), only a holder of the level's permission/role, one level per person.
func CheckApprover(status, requesterID string, levels []RequiredLevel, decided []Decision, userID string, role auth.Role) (RequiredLevel, string) {
	if status != ReqSubmitted {
		return RequiredLevel{}, DenyNotSubmitted
	}
	if userID == requesterID {
		return RequiredLevel{}, DenySelf
	}
	lvl, ok := NextLevel(levels, decided)
	if !ok {
		return RequiredLevel{}, DenyNoLevelOpen
	}
	if !lvl.Satisfies(role) {
		return lvl, DenyNotLevel
	}
	for _, d := range decided {
		if d.UserID == userID {
			return lvl, DenyAlreadyActed
		}
	}
	return lvl, ""
}

// ───────────────────────────── receipt maths (PRC-06, §7.3) ─────────────────────────────

// Variance of a receipt line against the ordered quantity.
const (
	VarNone  = "NONE"
	VarOver  = "OVER"
	VarUnder = "UNDER"
)

// milli converts a quantity to thousandths (quantities have at most 3 decimals).
func milli(f float64) int64 { return int64(math.Round(f * 1000)) }

func unmilli(m int64) float64 { return float64(m) / 1000 }

// LineState is a PO line before a receipt.
type LineState struct {
	Ordered  float64
	Received float64
	Closed   bool
}

// Outstanding is what is still expected (0 when closed or fully received).
func (s LineState) Outstanding() float64 {
	if s.Closed {
		return 0
	}
	if d := milli(s.Ordered) - milli(s.Received); d > 0 {
		return unmilli(d)
	}
	return 0
}

// ReceiptOutcome is the effect of receiving Qty on a line.
type ReceiptOutcome struct {
	Received    float64 // cumulative after this receipt
	Variance    string  // NONE | OVER | UNDER
	VarianceQty float64 // >= 0: quantity above (OVER) or below (UNDER) the ordered quantity
	Closed      bool    // the line expects nothing more
	NeedsReason bool    // §7.3: over/under receipt needs a reason and raises an exception
}

// EvaluateReceipt applies the §7.3 rule: receiving more than the outstanding quantity is an over-receipt; marking a
// line final (no more deliveries) below the ordered quantity is an under-receipt. Both need a reason.
func EvaluateReceipt(s LineState, qty float64, final bool) ReceiptOutcome {
	ord, rec := milli(s.Ordered), milli(s.Received)+milli(qty)
	out := ReceiptOutcome{Received: unmilli(rec), Variance: VarNone}
	switch {
	case rec > ord:
		out.Variance, out.VarianceQty, out.Closed, out.NeedsReason = VarOver, unmilli(rec-ord), true, true
	case rec == ord:
		out.Closed = true
	case final:
		out.Variance, out.VarianceQty, out.Closed, out.NeedsReason = VarUnder, unmilli(ord-rec), true, true
	}
	return out
}

// OrderStatusFor derives a sent PO's status from its lines: RECEIVED when every line is complete or closed,
// PARTIALLY_RECEIVED when anything arrived, else ORDERED.
func OrderStatusFor(lines []LineState) string {
	any, all := false, len(lines) > 0
	for _, l := range lines {
		if milli(l.Received) > 0 {
			any = true
		}
		if !l.Closed && milli(l.Received) < milli(l.Ordered) {
			all = false
		}
	}
	switch {
	case all:
		return POReceived
	case any:
		return POPartiallyReceived
	}
	return POOrdered
}

// RequestLineProgress summarises the PO lines (sent, not cancelled) that fulfil one request line.
type RequestLineProgress struct {
	OnOrder      bool // at least one sent PO line
	Received     bool // anything received
	Done         bool // every such PO line complete or closed
	FullyOrdered bool // sent PO quantities cover the requested quantity
}

// RequestStatusFor derives an ordered request's status from its lines ("" = no PO sent yet → keep the status).
func RequestStatusFor(lines []RequestLineProgress) string {
	on, rec, done := false, false, len(lines) > 0
	for _, l := range lines {
		on = on || l.OnOrder
		rec = rec || l.Received
		if !l.OnOrder || !l.Done || !l.FullyOrdered {
			done = false // a line partly left unordered keeps the request open (close it explicitly)
		}
	}
	switch {
	case !on:
		return ""
	case done:
		return ReqReceived
	case rec:
		return ReqPartiallyReceived
	}
	return ReqOrdered
}

// ───────────────────────────── low-stock proposals (PRC-07) ─────────────────────────────

// DefaultReorderFactor: a proposal tops the stock up to this multiple of the minimum (reorder) level.
const DefaultReorderFactor = 2.0

// SuggestQty is the quantity to request for an item below its reorder level: enough to reach factor × minLevel,
// counting what is already on order or requested, rounded up to whole units. 0 = nothing to propose.
func SuggestQty(balance, minLevel, onOrder, factor float64) float64 {
	if minLevel <= 0 || balance >= minLevel {
		return 0
	}
	if factor < 1 {
		factor = 1
	}
	need := minLevel*factor - balance - onOrder
	if need <= 0 {
		return 0
	}
	return math.Ceil(math.Round(need*1000) / 1000)
}
