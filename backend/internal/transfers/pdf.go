package transfers

import (
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/export"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/pdf"
	"btn-ops/internal/stock"

	"github.com/gofiber/fiber/v2"
)

func statusLabel(lang i18n.Lang, s string) string { return i18n.T(lang, "transfers.status."+s) }

func tone(s string) pdf.Tone {
	switch s {
	case Received:
		return pdf.Green
	case Rejected, Cancelled:
		return pdf.Red
	case InTransit:
		return pdf.Navy
	}
	return pdf.Amber
}

func fmtDT(s *string) string {
	if s == nil {
		return "—"
	}
	t, err := timeParse(*s)
	if err != nil {
		return *s
	}
	return dates.FormatDateTime(t)
}

func fmtD(s *string) string {
	if s == nil || *s == "" {
		return "—"
	}
	return dates.FormatDate(dates.MustParseISO(*s))
}

func qtyOrDash(p *float64) string {
	if p == nil {
		return "—"
	}
	return stock.FmtQty(*p)
}

// pdfNote is the transfer note: lines with requested / dispatched / received, approvals and signatures.
func (h *handler) pdfNote(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	t, err := load(ctx, h.d.Pool, c.Params("id"), false)
	if err != nil {
		return err
	}
	if err := visible(u, t); err != nil {
		return err
	}
	meta := []pdf.Meta{
		{Label: i18n.T(lang, "transfers.pdf.from"), Value: t.From.Code + " · " + t.From.Name},
		{Label: i18n.T(lang, "transfers.pdf.to"), Value: t.To.Code + " · " + t.To.Name},
		{Label: i18n.T(lang, "transfers.pdf.status"), Value: statusLabel(lang, t.Status)},
		{Label: i18n.T(lang, "transfers.pdf.requested"), Value: fmtDT(&t.RequestedAt)},
	}
	if t.NeededBy != nil {
		meta = append(meta, pdf.Meta{Label: i18n.T(lang, "transfers.pdf.needed_by"), Value: fmtD(t.NeededBy)})
	}
	if t.Vehicle != nil {
		meta = append(meta, pdf.Meta{Label: i18n.T(lang, "transfers.pdf.vehicle"), Value: *t.Vehicle})
	}
	if t.Driver != nil {
		meta = append(meta, pdf.Meta{Label: i18n.T(lang, "transfers.pdf.driver"), Value: *t.Driver})
	}
	doc, err := pdf.New(c, h.d, pdf.Options{Title: i18n.T(lang, "transfers.pdf.note_title"), Subtitle: t.From.Code + " → " + t.To.Code,
		DocumentNo: t.Number, Meta: meta})
	if err != nil {
		return err
	}
	doc.KpiRow([]pdf.Kpi{{Label: i18n.T(lang, "transfers.pdf.lines"), Value: format.Int(float64(len(t.Lines)))},
		{Label: i18n.T(lang, "transfers.pdf.value"), Value: format.RWF(float64(t.Value))},
		{Label: i18n.T(lang, "transfers.pdf.status"), Value: statusLabel(lang, t.Status)}})
	cols := []pdf.Column{{Header: "#", Width: 0.4}, {Header: i18n.T(lang, "transfers.pdf.col_item"), Width: 3}, {Header: i18n.T(lang, "transfers.pdf.col_unit"), Width: 0.8},
		{Header: i18n.T(lang, "transfers.pdf.col_lot"), Width: 1.2}, {Header: i18n.T(lang, "transfers.pdf.col_requested"), Width: 1.1, Align: pdf.Right},
		{Header: i18n.T(lang, "transfers.pdf.col_dispatched"), Width: 1.1, Align: pdf.Right}, {Header: i18n.T(lang, "transfers.pdf.col_received"), Width: 1.1, Align: pdf.Right},
		{Header: i18n.T(lang, "transfers.pdf.col_shortfall"), Width: 1.1, Align: pdf.Right}}
	rows := make([][]any, 0, len(t.Lines))
	for _, l := range t.Lines {
		short := any("—")
		if l.Shortfall > 0 {
			short = pdf.B(stock.FmtQty(l.Shortfall), pdf.Red)
		}
		rows = append(rows, []any{format.Int(float64(l.LineNo)), anyStr(l.Item["code"]) + " · " + anyStr(l.Item["name"]), anyStr(l.Item["unit"]), deref(l.LotRef),
			stock.FmtQty(l.QtyRequested), qtyOrDash(l.QtyDispatched), qtyOrDash(l.QtyReceived), short})
	}
	doc.Section(i18n.T(lang, "transfers.pdf.section_lines")).Table(cols, rows, nil, "")
	if len(t.Approvals) > 0 {
		acols := []pdf.Column{{Header: i18n.T(lang, "transfers.pdf.col_level"), Width: 0.8}, {Header: i18n.T(lang, "transfers.pdf.col_label"), Width: 3},
			{Header: i18n.T(lang, "transfers.pdf.col_by"), Width: 2}, {Header: i18n.T(lang, "transfers.pdf.col_at"), Width: 1.6}}
		var ar [][]any
		for _, a := range t.Approvals {
			ar = append(ar, []any{format.Int(float64(a.Level)), a.Label, a.Name, dates.FormatDateTime(a.At)})
		}
		doc.Section(i18n.T(lang, "transfers.pdf.section_approvals")).Table(acols, ar, nil, "")
	}
	for _, n := range []*string{t.Note, t.DispatchNote, t.ReceiveNote, t.RejectReason, t.CancelReason} {
		if n != nil && *n != "" {
			doc.Note(*n)
		}
	}
	reqAt, _ := timeParse(t.RequestedAt)
	sig := []pdf.Signatory{{Role: i18n.T(lang, "transfers.pdf.sig_requested"), Name: deref(t.RequestedBy), At: &reqAt, Verb: i18n.T(lang, "transfers.pdf.verb_requested")}}
	add := func(role, name *string, at *string, verb string) {
		s := pdf.Signatory{Role: deref(role)}
		if name != nil {
			s.Name = *name
		}
		if at != nil {
			if tm, err := timeParse(*at); err == nil {
				s.At, s.Verb = &tm, verb
			}
		}
		sig = append(sig, s)
	}
	r1, r2, r3 := i18n.T(lang, "transfers.pdf.sig_approved"), i18n.T(lang, "transfers.pdf.sig_dispatched"), i18n.T(lang, "transfers.pdf.sig_received")
	add(&r1, t.ApprovedBy, t.ApprovedAt, i18n.T(lang, "transfers.pdf.verb_approved"))
	add(&r2, t.DispatchedBy, t.DispatchedAt, i18n.T(lang, "transfers.pdf.verb_dispatched"))
	add(&r3, t.ReceivedBy, t.ReceivedAt, i18n.T(lang, "transfers.pdf.verb_received"))
	doc.Signatures(sig)
	return pdf.Send(c, doc, t.Number+"_Transfer-Note.pdf", c.Query("download") == "1")
}

func (h *handler) pdfList(c *fiber.Ctx) error {
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	q := httpx.Query(c)
	plant := stock.QueryID(q, "plantId")
	status := q.OptStr("status").Trim().Upper().OneOf(append([]string{"OPEN"}, Statuses...), "transfers.v.status").Get()
	if err := q.Err(); err != nil {
		return err
	}
	items, total, err := list(httpx.Ctx(c), h.d.Pool, Filter{PlantID: auth.PlantFilter(u, plant), Status: status, ItemKinds: kindsFor(u),
		Direction: c.Query("direction")}, 2000, 0)
	if err != nil {
		return err
	}
	doc, err := pdf.New(c, h.d, pdf.Options{Title: i18n.T(lang, "transfers.pdf.list_title"), Landscape: true,
		Meta: []pdf.Meta{{Label: i18n.T(lang, "transfers.pdf.count"), Value: format.Int(float64(total))}}})
	if err != nil {
		return err
	}
	cols := []pdf.Column{{Header: i18n.T(lang, "transfers.pdf.col_number"), Width: 1.4}, {Header: i18n.T(lang, "transfers.pdf.from"), Width: 1},
		{Header: i18n.T(lang, "transfers.pdf.to"), Width: 1}, {Header: i18n.T(lang, "transfers.pdf.requested"), Width: 1.5},
		{Header: i18n.T(lang, "transfers.pdf.col_by"), Width: 1.6}, {Header: i18n.T(lang, "transfers.pdf.lines"), Width: 0.7, Align: pdf.Right},
		{Header: i18n.T(lang, "transfers.pdf.value"), Width: 1.3, Align: pdf.Right}, {Header: i18n.T(lang, "transfers.pdf.status"), Width: 1.3}}
	rows := make([][]any, 0, len(items))
	for _, t := range items {
		st := statusLabel(lang, t.Status)
		if t.HasShortfall {
			st += " *"
		}
		rows = append(rows, []any{t.Number, t.From.Code, t.To.Code, fmtDT(&t.RequestedAt), deref(t.RequestedBy), format.Int(float64(t.LineCount)),
			format.RWF(float64(t.Value)), pdf.B(st, tone(t.Status))})
	}
	doc.Section(i18n.T(lang, "transfers.pdf.list_title")).Table(cols, rows, nil, i18n.T(lang, "transfers.pdf.empty"))
	doc.Note(i18n.T(lang, "transfers.pdf.list_note"))
	return pdf.Send(c, doc, "Transfers_"+dates.TodayISO()+".pdf", c.Query("download") == "1")
}

// Exports (F2 framework): GET /api/transfers?format=csv|xlsx|json (list) and /api/transfers/:id?format=… (note lines).
func init() {
	col := func(key, header string, k export.Kind) export.Column {
		return export.Column{Key: key, Header: header, Kind: k}
	}
	export.Register(export.Report{Path: "/transfers", Name: "transfers", Title: "transfers.export.list",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("number", "transfers.col.number", export.Text), col("from", "transfers.col.from", export.Text), col("to", "transfers.col.to", export.Text),
				col("status", "transfers.col.status", export.Text), col("requestedAt", "transfers.col.requested_at", export.DateTime),
				col("requestedBy", "transfers.col.requested_by", export.Text), col("lines", "transfers.col.lines", export.Int), col("value", "transfers.col.value", export.Money),
				col("dispatchDate", "transfers.col.dispatch_date", export.Date), col("receiveDate", "transfers.col.receive_date", export.Date),
				col("shortfall", "transfers.col.shortfall", export.Bool))
			for _, it := range j.Arr("items") {
				b.Row(it.V("number"), it.S("from.code"), it.S("to.code"), it.V("status"), it.V("requestedAt"), it.V("requestedBy"), it.V("lineCount"), it.V("value"),
					it.V("dispatchDate"), it.V("receiveDate"), it.V("hasShortfall"))
			}
		}})
	export.Register(export.Report{Path: "/transfers/:id", Name: "transfer", Title: "transfers.export.note",
		Build: func(b *export.Builder, j export.J) {
			t := j.Get("transfer")
			b.Filter("transfers.col.number", t.S("number"))
			b.Columns(col("line", "transfers.col.line", export.Int), col("kind", "transfers.col.kind", export.Text), col("code", "transfers.col.code", export.Text),
				col("item", "transfers.col.item", export.Text), col("unit", "transfers.col.unit", export.Text), col("lot", "transfers.col.lot", export.Text),
				col("requested", "transfers.col.requested", export.Qty), col("dispatched", "transfers.col.dispatched", export.Qty),
				col("received", "transfers.col.received", export.Qty), col("shortfall", "transfers.col.shortfall", export.Qty))
			for _, l := range t.Arr("lines") {
				b.Row(l.V("lineNo"), l.V("itemKind"), l.S("item.code"), l.S("item.name"), l.S("item.unit"), l.V("lotRef"), l.V("qtyRequested"),
					l.V("qtyDispatched"), l.V("qtyReceived"), l.V("shortfall"))
			}
		}})
}

func timeParse(s string) (time.Time, error) { return time.Parse("2006-01-02T15:04:05.000Z", s) }
