package transfers

// Transfer state machine (SRS STK-04):
//
//	REQUESTED ──approve (last level)──▶ APPROVED ──dispatch──▶ IN_TRANSIT ──receive──▶ RECEIVED
//	    │  └─approve (more levels)─▶ REQUESTED                       │
//	    ├──reject──▶ REJECTED                                         └──reverse──▶ CANCELLED (goods booked back at source)
//	    └──cancel──▶ CANCELLED ◀──cancel── APPROVED
//
// RECEIVED, REJECTED and CANCELLED are final. After dispatch a transfer can only be undone by reversal.

// Statuses.
const (
	Requested = "REQUESTED"
	Approved  = "APPROVED"
	Rejected  = "REJECTED"
	InTransit = "IN_TRANSIT"
	Received  = "RECEIVED"
	Cancelled = "CANCELLED"
)

// Statuses lists every status in workflow order.
var Statuses = []string{Requested, Approved, InTransit, Received, Rejected, Cancelled}

// Actions.
const (
	ActApprove  = "approve"
	ActReject   = "reject"
	ActDispatch = "dispatch"
	ActReceive  = "receive"
	ActCancel   = "cancel"
	ActReverse  = "reverse"
)

var transitions = map[string]map[string]string{
	Requested: {ActApprove: Approved, ActReject: Rejected, ActCancel: Cancelled},
	Approved:  {ActDispatch: InTransit, ActCancel: Cancelled},
	InTransit: {ActReceive: Received, ActReverse: Cancelled},
}

// Next returns the status reached by action from status (ok=false: illegal transition).
func Next(status, action string) (string, bool) {
	to, ok := transitions[status][action]
	return to, ok
}

// Final reports whether no action is possible any more.
func Final(status string) bool { return len(transitions[status]) == 0 }

// Shortfall is dispatched − received (≥ 0) in milli-units precision.
func Shortfall(dispatched, received float64) float64 {
	d := int64(dispatched*1000 + 0.5)
	r := int64(received*1000 + 0.5)
	if r >= d {
		return 0
	}
	return float64(d-r) / 1000
}
