package attendance

import (
	"context"
	"fmt"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
)

// ── summary data ──────────────────────────────────────────────────────────────

type workerSummary struct {
	WorkerID    string   `json:"workerId"`
	Name        string   `json:"name"`
	JobTitle    string   `json:"jobTitle"`
	Plant       plantRef `json:"plant"`
	DaysPresent int      `json:"daysPresent"`
	HalfDays    int      `json:"halfDays"`
	DaysAbsent  int      `json:"daysAbsent"`
	DaysLeave   int      `json:"daysLeave"`
	PayableDays float64  `json:"payableDays"`
	Overtime    float64  `json:"overtimeHours"`
	Exceptions  int      `json:"exceptions"`
}

type summaryTotals struct {
	Workers     int     `json:"workers"`
	DaysPresent int     `json:"daysPresent"`
	HalfDays    int     `json:"halfDays"`
	DaysAbsent  int     `json:"daysAbsent"`
	DaysLeave   int     `json:"daysLeave"`
	PayableDays float64 `json:"payableDays"`
	Overtime    float64 `json:"overtimeHours"`
	Exceptions  int     `json:"exceptions"`
}

type summaryData struct {
	Plant   *plantRef       `json:"plant"`
	From    string          `json:"from"`
	To      string          `json:"to"`
	Days    int             `json:"days"`
	Workers []workerSummary `json:"workers"`
	Totals  summaryTotals   `json:"totals"`
}

// aggregate tallies every active worker (of the plant, or all plants when plantID is "") over [from,to]
// entirely in SQL — one grouped query, no N+1.
func aggregate(ctx context.Context, q db.DBTX, plantID string, from, to time.Time) ([]workerSummary, summaryTotals, error) {
	where := `w."isActive"`
	args := []any{from, to}
	if plantID != "" {
		where += ` AND w."plantId" = $3`
		args = append(args, plantID)
	}
	rows, err := q.Query(ctx, `SELECT w.id, w."firstName", w."lastName", w."jobTitle", p.id, p.code, p.name,
		count(*) FILTER (WHERE a.status = 'PRESENT')  AS present,
		count(*) FILTER (WHERE a.status = 'HALF_DAY') AS half,
		count(*) FILTER (WHERE a.status = 'ABSENT')   AS absent,
		count(*) FILTER (WHERE a.status = 'LEAVE')    AS leave,
		COALESCE(sum(a."overtimeHours"), 0)::float8  AS ot,
		count(*) FILTER (WHERE a.exception)           AS exc
		FROM "Worker" w JOIN "Plant" p ON p.id = w."plantId"
		LEFT JOIN "Attendance" a ON a."workerId" = w.id AND a.date >= $1 AND a.date <= $2
		WHERE `+where+`
		GROUP BY w.id, p.id
		ORDER BY p.code, w."firstName", w."lastName", w.id`, args...)
	if err != nil {
		return nil, summaryTotals{}, err
	}
	defer rows.Close()
	out := []workerSummary{}
	var tot summaryTotals
	for rows.Next() {
		var ws workerSummary
		var first, last string
		if err := rows.Scan(&ws.WorkerID, &first, &last, &ws.JobTitle, &ws.Plant.ID, &ws.Plant.Code, &ws.Plant.Name,
			&ws.DaysPresent, &ws.HalfDays, &ws.DaysAbsent, &ws.DaysLeave, &ws.Overtime, &ws.Exceptions); err != nil {
			return nil, summaryTotals{}, err
		}
		ws.Name = fullName(first, last)
		ws.PayableDays = PayableFrom(ws.DaysPresent, ws.HalfDays)
		out = append(out, ws)
		tot.Workers++
		tot.DaysPresent += ws.DaysPresent
		tot.HalfDays += ws.HalfDays
		tot.DaysAbsent += ws.DaysAbsent
		tot.DaysLeave += ws.DaysLeave
		tot.PayableDays += ws.PayableDays
		tot.Overtime += ws.Overtime
		tot.Exceptions += ws.Exceptions
	}
	return out, tot, rows.Err()
}

func (h *handler) summaryData(ctx context.Context, u *auth.User, plant *plantRef, plantID string, rg dates.Range) (*summaryData, error) {
	key := fmt.Sprintf("attendance:summary:%s:%s:%s:%s", scopeKey(u), plantID, rg.FromISO, rg.ToISO)
	return summaryCache.GetOrLoad(ctx, key, func(ctx context.Context) (*summaryData, error) {
		workers, totals, err := aggregate(ctx, h.d.Pool, plantID, rg.From, rg.To)
		if err != nil {
			return nil, err
		}
		return &summaryData{Plant: plant, From: rg.FromISO, To: rg.ToISO,
			Days: int(rg.To.Sub(rg.From).Hours()/24) + 1, Workers: workers, Totals: totals}, nil
	}, cacheTag)
}

func (h *handler) summary(c *fiber.Ctx) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	qv := httpx.Query(c)
	plantQ := qv.OptStr("plantId").Get()
	from, to := qv.OptStr("from").Get(), qv.OptStr("to").Get()
	if err := qv.Err(); err != nil {
		return err
	}
	rg, err := h.resolveRange(c, from, to)
	if err != nil {
		return err
	}
	plantID := auth.PlantFilter(u, plantQ)
	var plant *plantRef
	if plantID != "" {
		p, _, err := loadActivePlant(ctx, h.d.Pool, plantID)
		if err != nil {
			return err
		}
		if p == nil {
			return httpx.BadRequest("error.validation", map[string]string{"plantId": t(c, "attendance.err.unknown_plant")})
		}
		plant = p
	}
	data, err := h.summaryData(ctx, u, plant, plantID, rg)
	if err != nil {
		return err
	}
	return httpx.JSONCached(c, data, "private, no-cache")
}

// ── payroll integration ───────────────────────────────────────────────────────

// PayableDays returns, per worker of a site, the payable days derived from attendance over [from,to]
// (PRESENT=1, HALF_DAY=0.5). Workers with no marks are omitted. Aggregated in SQL (no N+1). Used by the
// payroll module to offer attendance-derived days when preparing a wage sheet.
func PayableDays(ctx context.Context, q db.DBTX, plantID string, from, to time.Time) (map[string]float64, error) {
	rows, err := q.Query(ctx, `SELECT a."workerId",
		count(*) FILTER (WHERE a.status = 'PRESENT'),
		count(*) FILTER (WHERE a.status = 'HALF_DAY')
		FROM "Attendance" a JOIN "Worker" w ON w.id = a."workerId"
		WHERE a."plantId" = $1 AND a.date >= $2 AND a.date <= $3 AND w."plantId" = $1
		GROUP BY a."workerId"`, plantID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var id string
		var present, half int
		if err := rows.Scan(&id, &present, &half); err != nil {
			return nil, err
		}
		out[id] = PayableFrom(present, half)
	}
	return out, rows.Err()
}
