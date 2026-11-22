package attendance

import (
	"fmt"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/pdf"

	"github.com/gofiber/fiber/v2"
)

// compact, language-neutral marks (Latin, like the wage sheets); colours cue the status at a glance.
const (
	markPresent = "P"
	markHalf    = "½"
	markAbsent  = "A"
	markLeave   = "L"
)

func markCell(status string) pdf.Cell {
	switch status {
	case StatusPresent:
		return pdf.Cell{Text: markPresent, Bold: true, Color: "#15803D"}
	case StatusHalfDay:
		return pdf.Cell{Text: markHalf, Bold: true, Color: "#92400E"}
	case StatusAbsent:
		return pdf.Cell{Text: markAbsent, Color: "#B91C1C"}
	case StatusLeave:
		return pdf.Cell{Text: markLeave, Color: "#1D4ED8"}
	}
	return pdf.Cell{Text: "·", Color: "#94A3B8"}
}

func (h *handler) pdf(c *fiber.Ctx) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	lg := lang(c)
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
	plant, err := h.resolvePlant(c, u, plantQ)
	if err != nil {
		return err
	}
	days := dates.EachDay(rg.From, rg.To)

	// active workers of the site (ordered) + every mark in the range — two queries, no N+1
	type wrow struct{ id, name, job string }
	var workers []wrow
	wr, err := h.d.Pool.Query(ctx, `SELECT id, "firstName", "lastName", "jobTitle" FROM "Worker"
		WHERE "plantId" = $1 AND "isActive" ORDER BY "firstName", "lastName", id`, plant.ID)
	if err != nil {
		return err
	}
	for wr.Next() {
		var id, first, last, job string
		if err := wr.Scan(&id, &first, &last, &job); err != nil {
			wr.Close()
			return err
		}
		workers = append(workers, wrow{id: id, name: fullName(first, last), job: job})
	}
	wr.Close()
	if err := wr.Err(); err != nil {
		return err
	}

	marks := map[string]map[string]string{} // workerId -> dateISO -> status
	ar, err := h.d.Pool.Query(ctx, `SELECT "workerId", date, status FROM "Attendance"
		WHERE "plantId" = $1 AND date >= $2 AND date <= $3`, plant.ID, rg.From, rg.To)
	if err != nil {
		return err
	}
	for ar.Next() {
		var wid, status string
		var date time.Time
		if err := ar.Scan(&wid, &date, &status); err != nil {
			ar.Close()
			return err
		}
		m := marks[wid]
		if m == nil {
			m = map[string]string{}
			marks[wid] = m
		}
		m[dates.ToISO(date)] = status
	}
	ar.Close()
	if err := ar.Err(); err != nil {
		return err
	}

	rangeTxt := formatRange(lg, rg.From, rg.To)
	rep, err := pdf.New(c, h.d, pdf.Options{
		Landscape: true,
		Title:     t2(lg, "attendance.pdf.title", "plant", plant.Code, "range", rangeTxt),
		Subtitle:  t2(lg, "attendance.pdf.subtitle", "range", rangeTxt, "days", len(days)),
		Meta: []pdf.Meta{
			{Label: t2(lg, "attendance.pdf.meta_site"), Value: plant.Code + " · " + plant.Name},
			{Label: t2(lg, "attendance.pdf.meta_from"), Value: rg.FromISO},
			{Label: t2(lg, "attendance.pdf.meta_to"), Value: rg.ToISO},
		},
	})
	if err != nil {
		return err
	}

	// columns: # · Worker · <one per date, header = day-of-month> · Payable days
	cols := []pdf.Column{
		{Header: rep.T("attendance.pdf.col_no"), Width: 0.5, Align: pdf.Right},
		{Header: rep.T("attendance.pdf.col_worker"), Width: 3.2},
	}
	for _, d := range days {
		cols = append(cols, pdf.Column{Header: fmt.Sprintf("%d", d.Day()), Width: 0.5, Align: pdf.Center})
	}
	cols = append(cols, pdf.Column{Header: rep.T("attendance.pdf.col_payable"), Width: 1.1, Align: pdf.Right})

	var grand Counts
	var rows [][]any
	for i, w := range workers {
		row := []any{i + 1, w.name}
		var cnt Counts
		for _, d := range days {
			st := marks[w.id][dates.ToISO(d)]
			cnt.Add(st)
			row = append(row, markCell(st))
		}
		grand.Present += cnt.Present
		grand.Half += cnt.Half
		grand.Absent += cnt.Absent
		grand.Leave += cnt.Leave
		grand.Payable += cnt.Payable
		row = append(row, pdf.Cell{Text: format.Qty(cnt.Payable), Bold: true})
		rows = append(rows, row)
	}

	totals := make([]any, len(cols))
	totals[1] = rep.T("attendance.pdf.total")
	totals[len(cols)-1] = pdf.Cell{Text: format.Qty(grand.Payable), Bold: true}

	rep.Note(rep.T("attendance.pdf.legend"))
	rep.Spacer(2).Table(cols, rows, totals, rep.T("attendance.pdf.empty"))
	rep.Note(rep.T("attendance.pdf.counts", "present", grand.Present, "half", grand.Half, "absent", grand.Absent, "leave", grand.Leave, "payable", format.Qty(grand.Payable)))
	rep.Signatures([]pdf.Signatory{
		{Role: rep.T("attendance.pdf.sig_recorded")},
		{Role: rep.T("attendance.pdf.sig_verified")},
	})
	return pdf.Send(c, rep, fmt.Sprintf("%s_Attendance-Sheet_%s_to_%s.pdf", plant.Code, rg.FromISO, rg.ToISO), c.Query("download") == "1")
}
