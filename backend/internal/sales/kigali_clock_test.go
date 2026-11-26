package sales_test

import (
	"testing"
	"time"

	"btn-ops/internal/dates"
)

// The server clock at 23:30 UTC: due dates and every other business date follow the Kigali calendar.
func TestOrderLifecycleAtLateUTCEvening(t *testing.T) {
	defer dates.SetClock(dates.FixedClock(dates.LateUTCEvening(time.Now())))()
	TestOrderLifecycleChargesDeliveryAndPayments(t)
}
