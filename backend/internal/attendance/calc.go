package attendance

// Pure attendance logic: the status set and the day-fraction mapping that feeds payroll days.
// No database access, so every rule is unit-tested (calc_test.go).

// Status is one attendance mark.
const (
	StatusPresent = "PRESENT"
	StatusHalfDay = "HALF_DAY"
	StatusAbsent  = "ABSENT"
	StatusLeave   = "LEAVE"
)

// Statuses in display order.
var Statuses = []string{StatusPresent, StatusHalfDay, StatusAbsent, StatusLeave}

// ValidStatus reports whether s is a known attendance status.
func ValidStatus(s string) bool {
	for _, x := range Statuses {
		if x == s {
			return true
		}
	}
	return false
}

// DayFraction maps a status to the payable day fraction: PRESENT=1, HALF_DAY=0.5, ABSENT/LEAVE=0.
// LEAVE is tracked but unpaid, like the wage sheets. Unknown statuses count as 0.
func DayFraction(status string) float64 {
	switch status {
	case StatusPresent:
		return 1
	case StatusHalfDay:
		return 0.5
	default:
		return 0
	}
}

// Counts tallies a set of attendance marks for a worker or a site.
type Counts struct {
	Present  int     `json:"present"`
	Half     int     `json:"half"`
	Absent   int     `json:"absent"`
	Leave    int     `json:"leave"`
	Unmarked int     `json:"unmarked"`
	Total    int     `json:"total"`
	Payable  float64 `json:"payableDays"`
}

// Add records one mark (status "" = unmarked) into the tally and keeps Payable in step.
func (c *Counts) Add(status string) {
	c.Total++
	switch status {
	case StatusPresent:
		c.Present++
	case StatusHalfDay:
		c.Half++
	case StatusAbsent:
		c.Absent++
	case StatusLeave:
		c.Leave++
	default:
		c.Unmarked++
		return
	}
	c.Payable += DayFraction(status)
}

// PayableFrom computes payable days from present/half counts (half-day arithmetic, no float drift).
func PayableFrom(present, half int) float64 {
	return float64(present) + float64(half)*0.5
}
