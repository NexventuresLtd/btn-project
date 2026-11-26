package sales

// Order lifecycle (SRS ORD-04, Figure 3). Pure: no database access, so every rule is unit-tested.
//
//	DRAFT → QUOTED → CONFIRMED → IN_PRODUCTION / FROM_STOCK → READY → DISPATCHED (partial keeps it open) → DELIVERED → CLOSED
//	CANCELLED is reachable from DRAFT, QUOTED or CONFIRMED, always with a reason and an approval (Figure 3).
//
// "Sale".orderStatus holds this state; the legacy "Sale".status (DRAFT | CONFIRMED | CANCELLED, read by the dashboard,
// receivables, privacy and the importer) is kept in step by a trigger (legacyStatus mirrors it).

const (
	StDraft        = "DRAFT"
	StQuoted       = "QUOTED"
	StConfirmed    = "CONFIRMED"
	StInProduction = "IN_PRODUCTION"
	StFromStock    = "FROM_STOCK"
	StReady        = "READY"
	StDispatched   = "DISPATCHED"
	StDelivered    = "DELIVERED"
	StClosed       = "CLOSED"
	StCancelled    = "CANCELLED"
)

// OrderStatuses in lifecycle order.
var OrderStatuses = []string{StDraft, StQuoted, StConfirmed, StInProduction, StFromStock, StReady, StDispatched, StDelivered, StClosed, StCancelled}

// transitions lists every allowed move. Corrections (a cancelled delivery note) may step back from
// DISPATCHED/DELIVERED; everything else only moves forward.
var transitions = map[string][]string{
	StDraft:        {StQuoted, StConfirmed, StCancelled},
	StQuoted:       {StConfirmed, StCancelled},
	StConfirmed:    {StInProduction, StFromStock, StCancelled},
	StInProduction: {StFromStock, StReady},
	StFromStock:    {StInProduction, StReady},
	StReady:        {StDispatched, StInProduction, StFromStock},
	StDispatched:   {StDelivered, StReady},
	StDelivered:    {StClosed, StDispatched},
	StClosed:       {},
	StCancelled:    {},
}

func validStatus(s string) bool {
	_, ok := transitions[s]
	return ok
}

// canTransition reports whether from → to is allowed (staying in DISPATCHED for another partial dispatch is not a transition).
func canTransition(from, to string) bool {
	for _, t := range transitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

// nextStatuses lists the states reachable from s (for the UI).
func nextStatuses(s string) []string { return append([]string{}, transitions[s]...) }

func inSet(s string, set ...string) bool {
	for _, x := range set {
		if x == s {
			return true
		}
	}
	return false
}

// committed: the order is a commercial commitment (receivable, stock reservation, locked terms).
func committed(s string) bool {
	return inSet(s, StConfirmed, StInProduction, StFromStock, StReady, StDispatched, StDelivered, StClosed)
}

// editable: commercial terms may still change freely (ORD-10 locks them once confirmed).
func editable(s string) bool { return s == StDraft || s == StQuoted }

// dispatchable: a delivery note may be issued (Ready for dispatch, or a partial dispatch keeping the order open).
func dispatchable(s string) bool { return s == StReady || s == StDispatched }

// payable: payments (deposit, part-payment, balance) may be recorded.
func payable(s string) bool {
	return inSet(s, StConfirmed, StInProduction, StFromStock, StReady, StDispatched, StDelivered)
}

// cancellable: Figure 3 allows cancellation from Draft, Quoted or Confirmed.
func cancellable(s string) bool { return s == StDraft || s == StQuoted || s == StConfirmed }

// cancelNeedsApproval: Figure 3 — "Cancelled is reachable from Draft, Quoted or Confirmed (with reason and approval)".
// Every cancellation, a draft included, goes through a CANCEL approval (value thresholds + a sales.cancel level,
// decided by someone other than the requester).
func cancelNeedsApproval(s string) bool { return cancellable(s) }

// amendable: commercial terms of a confirmed order change only through an approved amendment, until it is closed.
func amendable(s string) bool {
	return inSet(s, StConfirmed, StInProduction, StFromStock, StReady, StDispatched, StDelivered)
}

// fulfilmentEditable: lines may be marked "from stock" / "in production" (ORD-05).
func fulfilmentEditable(s string) bool {
	return inSet(s, StConfirmed, StInProduction, StFromStock, StReady)
}

// returnable: goods may come back once something was dispatched.
func returnableState(s string) bool { return inSet(s, StDispatched, StDelivered, StClosed) }

// legacyStatus mirrors the database trigger btn_sale_status_sync.
func legacyStatus(s string) string {
	switch s {
	case StDraft, StQuoted:
		return "DRAFT"
	case StCancelled:
		return "CANCELLED"
	}
	return "CONFIRMED"
}

// fulfilmentStatus is the state a confirmed order moves to after its lines were marked:
// any line in production → IN_PRODUCTION, otherwise FROM_STOCK.
func fulfilmentStatus(anyInProduction bool) string {
	if anyInProduction {
		return StInProduction
	}
	return StFromStock
}

// shipState is the order state implied by its delivery notes once it has reached READY or later:
// no active note → READY; everything shipped and every note confirmed delivered → DELIVERED; otherwise DISPATCHED.
func shipState(ordered, shipped int64, activeNotes, openNotes int) string {
	if activeNotes == 0 || shipped <= 0 {
		return StReady
	}
	if shipped >= ordered && openNotes == 0 {
		return StDelivered
	}
	return StDispatched
}

// shouldClose: a delivered order whose balance is settled closes automatically.
func shouldClose(status string, amount, paid int64) bool {
	return status == StDelivered && paid >= amount
}
