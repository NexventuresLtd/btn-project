package payroll

import (
	"btn-ops/internal/attendance"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"

	"github.com/gofiber/fiber/v2"
)

// attendanceItem is the attendance-derived payable days for one worker on the sheet.
type attendanceItem struct {
	WorkerID    string  `json:"workerId"`
	PayableDays float64 `json:"payableDays"`
}

// periodAttendance returns, per worker on the wage sheet, the payable days derived from Attendance over
// the period's [startDate, endDate] (PRESENT=1, HALF_DAY=0.5). The frontend offers these as an explicit
// "Fill days from attendance" action on a DRAFT sheet; it never overwrites days silently.
func (h *handler) periodAttendance(c *fiber.Ctx) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	p, err := loadHead(ctx, h.d.Pool, c.Params("id"), false)
	if err != nil {
		return err
	}
	if err := auth.AssertPlantAccess(u, p.PlantID); err != nil {
		return err
	}
	days, err := attendance.PayableDays(ctx, h.d.Pool, p.PlantID, p.Start, p.End)
	if err != nil {
		return err
	}
	lines, err := loadLines(ctx, h.d.Pool, p.ID)
	if err != nil {
		return err
	}
	items := make([]attendanceItem, 0, len(lines))
	var total float64
	for _, l := range lines {
		pd := days[l.WorkerID]
		total += pd
		items = append(items, attendanceItem{WorkerID: l.WorkerID, PayableDays: pd})
	}
	return c.JSON(fiber.Map{
		"periodId":    p.ID,
		"from":        dates.ToISO(p.Start),
		"to":          dates.ToISO(p.End),
		"items":       items,
		"payableDays": total,
	})
}
