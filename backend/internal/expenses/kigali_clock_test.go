package expenses_test

import (
	"testing"
	"time"

	"btn-ops/internal/dates"
)

// The server clock at 23:30 UTC: the Kigali date is already tomorrow, and every business date must follow it.
func TestExpenseApprovalPaymentAndReceiptsAtLateUTCEvening(t *testing.T) {
	defer dates.SetClock(dates.FixedClock(dates.LateUTCEvening(time.Now())))()
	TestExpenseApprovalPaymentAndReceipts(t)
}
