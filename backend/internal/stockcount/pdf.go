package stockcount

import (
	"context"
	"strconv"

	"btn-ops/internal/dates"
	"btn-ops/internal/export"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/pdf"
	"btn-ops/internal/stock"
	"btn-ops/internal/syncsvc"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

func meta(lang i18n.Lang, c Count) []pdf.Meta {
	scope := i18n.T(lang, "stockcount.scope."+c.Scope)
	if c.ItemKind != nil {
		scope += " · " + stock.KindLabel(lang, *c.ItemKind)
	}
	if c.Category != nil {
		scope += " · " + *c.Category
	}
	m := []pdf.Meta{{Label: i18n.T(lang, "stockcount.pdf.location"), Value: c.Plant["code"] + " · " + c.Plant["name"]},
		{Label: i18n.T(lang, "stockcount.pdf.count_date"), Value: dates.FormatDate(dates.MustParseISO(c.CountDate))},
		{Label: i18n.T(lang, "stockcount.pdf.scope"), Value: scope},
		{Label: i18n.T(lang, "stockcount.pdf.status"), Value: i18n.T(lang, "stockcount.status."+c.Status)}}
	if c.Blind {
		m = append(m, pdf.Meta{Label: i18n.T(lang, "stockcount.pdf.mode"), Value: i18n.T(lang, "stockcount.pdf.blind")})
	}
	return m
}

// pdfSheet: printable count sheet with an empty "counted" column (system quantity omitted for blind counts).
func (h *handler) pdfSheet(c *fiber.Ctx) error {
	lang := httpx.Lang(c)
	ct, err := h.loadVisible(c, c.Params("id"))
	if err != nil {
		return err
	}
	showSystem := !ct.Blind && !ct.Hidden
	doc, err := pdf.New(c, h.d, pdf.Options{Title: i18n.T(lang, "stockcount.pdf.sheet_title"), DocumentNo: ct.Number, Meta: meta(lang, ct)})
	if err != nil {
		return err
	}
	cols := []pdf.Column{{Header: "#", Width: 0.4}, {Header: i18n.T(lang, "stockcount.pdf.col_code"), Width: 1}, {Header: i18n.T(lang, "stockcount.pdf.col_item"), Width: 3},
		{Header: i18n.T(lang, "stockcount.pdf.col_unit"), Width: 0.8}}
	if showSystem {
		cols = append(cols, pdf.Column{Header: i18n.T(lang, "stockcount.pdf.col_system"), Width: 1.2, Align: pdf.Right})
	}
	cols = append(cols, pdf.Column{Header: i18n.T(lang, "stockcount.pdf.col_counted"), Width: 1.4, Align: pdf.Right}, pdf.Column{Header: i18n.T(lang, "stockcount.pdf.col_note"), Width: 2})
	rows := make([][]any, 0, len(ct.Lines))
	for _, l := range ct.Lines {
		r := []any{strconv.Itoa(l.LineNo), anyStr(l.Item["code"]), anyStr(l.Item["name"]), anyStr(l.Item["unit"])}
		if showSystem {
			r = append(r, stock.FmtQty(*l.SystemQty))
		}
		counted := "________"
		if l.CountedQty != nil {
			counted = stock.FmtQty(*l.CountedQty)
		}
		rows = append(rows, append(r, counted, ""))
	}
	doc.Section(i18n.T(lang, "stockcount.pdf.sheet_section")).Table(cols, rows, nil, "")
	doc.Note(i18n.T(lang, "stockcount.pdf.sheet_note"))
	doc.Signatures([]pdf.Signatory{{Role: i18n.T(lang, "stockcount.pdf.sig_counted")}, {Role: i18n.T(lang, "stockcount.pdf.sig_witness")}})
	return pdf.Send(c, doc, ct.Number+"_Count-Sheet.pdf", c.Query("download") == "1")
}

// pdfVariance: counted vs system, variance and value per line, totals and approvals.
func (h *handler) pdfVariance(c *fiber.Ctx) error {
	lang := httpx.Lang(c)
	ct, err := h.loadVisible(c, c.Params("id"))
	if err != nil {
		return err
	}
	if ct.Hidden {
		return httpx.NewKey(403, "forbidden", "stockcount.err.blind_hidden")
	}
	doc, err := pdf.New(c, h.d, pdf.Options{Title: i18n.T(lang, "stockcount.pdf.variance_title"), DocumentNo: ct.Number, Meta: meta(lang, ct), Landscape: true})
	if err != nil {
		return err
	}
	s := ct.Summary
	doc.KpiRow([]pdf.Kpi{{Label: i18n.T(lang, "stockcount.pdf.kpi_counted"), Value: format.Int(float64(s.Counted)) + " / " + format.Int(float64(s.Lines))},
		{Label: i18n.T(lang, "stockcount.pdf.kpi_variance_lines"), Value: format.Int(float64(s.WithVariance))},
		{Label: i18n.T(lang, "stockcount.pdf.kpi_gain"), Value: format.RWF(float64(s.GainValue))},
		{Label: i18n.T(lang, "stockcount.pdf.kpi_loss"), Value: format.RWF(float64(s.LossValue))},
		{Label: i18n.T(lang, "stockcount.pdf.kpi_accuracy"), Value: format.Percent(&s.AccuracyRatio)}})
	cols := []pdf.Column{{Header: i18n.T(lang, "stockcount.pdf.col_code"), Width: 1}, {Header: i18n.T(lang, "stockcount.pdf.col_item"), Width: 3},
		{Header: i18n.T(lang, "stockcount.pdf.col_unit"), Width: 0.7}, {Header: i18n.T(lang, "stockcount.pdf.col_system"), Width: 1.1, Align: pdf.Right},
		{Header: i18n.T(lang, "stockcount.pdf.col_counted"), Width: 1.1, Align: pdf.Right}, {Header: i18n.T(lang, "stockcount.pdf.col_variance"), Width: 1.1, Align: pdf.Right},
		{Header: i18n.T(lang, "stockcount.pdf.col_value"), Width: 1.4, Align: pdf.Right}, {Header: i18n.T(lang, "stockcount.pdf.col_note"), Width: 2}}
	rows := make([][]any, 0, len(ct.Lines))
	for _, l := range ct.Lines {
		counted, variance, value := "—", any("—"), "—"
		if l.CountedQty != nil {
			counted = stock.FmtQty(*l.CountedQty)
			v := *l.Variance
			switch {
			case v > 0:
				variance = pdf.B("+"+stock.FmtQty(v), pdf.Green)
			case v < 0:
				variance = pdf.B(stock.FmtQty(v), pdf.Red)
			default:
				variance = "0"
			}
			value = format.RWF(float64(*l.Value))
		}
		rows = append(rows, []any{anyStr(l.Item["code"]), anyStr(l.Item["name"]), anyStr(l.Item["unit"]), stock.FmtQty(*l.SystemQty), counted, variance, value, deref(l.Note)})
	}
	doc.Section(i18n.T(lang, "stockcount.pdf.variance_section")).Table(cols, rows,
		[]any{i18n.T(lang, "stockcount.pdf.total"), nil, nil, nil, nil, nil, format.RWF(float64(s.AbsValue)), nil}, "")
	if len(ct.Approvals) > 0 {
		var ar [][]any
		for _, a := range ct.Approvals {
			ar = append(ar, []any{strconv.Itoa(a.Level), a.Label, a.Name, dates.FormatDateTime(a.At)})
		}
		doc.Section(i18n.T(lang, "stockcount.pdf.approvals")).Table([]pdf.Column{{Header: i18n.T(lang, "stockcount.pdf.col_level"), Width: 0.7},
			{Header: i18n.T(lang, "stockcount.pdf.col_label"), Width: 3}, {Header: i18n.T(lang, "stockcount.pdf.col_by"), Width: 2}, {Header: i18n.T(lang, "stockcount.pdf.col_at"), Width: 1.6}}, ar, nil, "")
	}
	if ct.Status != Approved {
		doc.Note(i18n.T(lang, "stockcount.pdf.variance_pending"))
	}
	doc.Signatures([]pdf.Signatory{{Role: i18n.T(lang, "stockcount.pdf.sig_counted"), Name: deref(ct.SubmittedBy)}, {Role: i18n.T(lang, "stockcount.pdf.sig_approved"), Name: deref(ct.ApprovedBy)}})
	return pdf.Send(c, doc, ct.Number+"_Variance-Report.pdf", c.Query("download") == "1")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func init() {
	col := func(key, header string, k export.Kind) export.Column {
		return export.Column{Key: key, Header: header, Kind: k}
	}
	export.Register(export.Report{Path: "/stock-counts", Name: "stock-counts", Title: "stockcount.export.list",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("number", "stockcount.col.number", export.Text), col("location", "stockcount.col.location", export.Text),
				col("countDate", "stockcount.col.count_date", export.Date), col("scope", "stockcount.col.scope", export.Text), col("blind", "stockcount.col.blind", export.Bool),
				col("status", "stockcount.col.status", export.Text), col("lines", "stockcount.col.lines", export.Int), col("counted", "stockcount.col.counted", export.Int),
				col("value", "stockcount.col.variance_value", export.Money), col("createdBy", "stockcount.col.created_by", export.Text))
			for _, it := range j.Arr("items") {
				b.Row(it.V("number"), it.S("plant.code"), it.V("countDate"), it.V("scope"), it.V("blind"), it.V("status"), it.V("lineCount"), it.V("countedLines"),
					it.V("varianceValue"), it.V("createdBy"))
			}
		}})
	export.Register(export.Report{Path: "/stock-counts/:id", Name: "stock-count", Title: "stockcount.export.variance",
		Build: func(b *export.Builder, j export.J) {
			ct := j.Get("count")
			b.Filter("stockcount.col.number", ct.S("number"))
			b.Columns(col("line", "stockcount.col.line", export.Int), col("kind", "stockcount.col.kind", export.Text), col("code", "stockcount.col.code", export.Text),
				col("item", "stockcount.col.item", export.Text), col("unit", "stockcount.col.unit", export.Text), col("system", "stockcount.col.system", export.Qty),
				col("counted", "stockcount.col.counted_qty", export.Qty), col("variance", "stockcount.col.variance", export.Qty),
				col("value", "stockcount.col.variance_value", export.Money), col("note", "stockcount.col.note", export.Text))
			for _, l := range ct.Arr("lines") {
				b.Row(l.V("lineNo"), l.V("itemKind"), l.S("item.code"), l.S("item.name"), l.S("item.unit"), l.V("systemQty"), l.V("countedQty"), l.V("variance"),
					l.V("varianceValue"), l.V("note"))
			}
		}})
}

// ── offline (record type "stock_count") ─────────────────────────────────────
//
//	{ "countId", "lines":[{lineId, countedQty|null, note?}], "submit"?: bool }
//
// Counted quantities are captured on the device against a count pulled while online (entity "stock_counts":
// OPEN counts in scope with lines; system quantities omitted for blind counts). A count that is no longer OPEN
// on the server (submitted / approved / cancelled meanwhile) is a conflict.

func registerSync() {
	syncsvc.RegisterHandler("stock_count", syncsvc.HandlerFunc(applyCount), syncsvc.Requires("stockcount.record"))
	syncsvc.RegisterPull("stock_counts", pullCounts, syncsvc.PullRequires("stockcount.record", "stockcount.approve"))
}

func applyCount(ctx context.Context, tx pgx.Tx, rec syncsvc.Record, a syncsvc.Actor) syncsvc.Result {
	v, err := rec.Body(a.Lang)
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonInvalidRecord, i18n.T(a.Lang, "stockcount.v.invalid"))
	}
	id := stock.IDField(v, "countId")
	lines := parseLines(v)
	doSubmit := v.OptBool("submit").Get()
	if err := v.Err(); err != nil {
		return syncsvc.FromError(a.Lang, err)
	}
	ct, err := load(ctx, tx, id, true)
	if err != nil {
		return syncsvc.FromError(a.Lang, err)
	}
	if r := a.AssertPlant(ct.PlantID); r != nil {
		return *r
	}
	var ver int64
	_ = tx.QueryRow(ctx, `SELECT server_version FROM stock_count WHERE id=$1`, id).Scan(&ver)
	if ct.Status != Open {
		if rec.Force {
			return syncsvc.Reject(syncsvc.ReasonConflict, i18n.T(a.Lang, "stockcount.sync.not_open", "number", ct.Number))
		}
		return syncsvc.Conflict(ctx, tx, rec, a, syncsvc.ConflictInput{Entity: "StockCount", EntityID: id, PlantID: ct.PlantID, ServerVersion: ver,
			ServerData: map[string]any{"number": ct.Number, "status": ct.Status}, Reason: i18n.T(a.Lang, "stockcount.sync.not_open", "number", ct.Number)})
	}
	sa := stock.SyncActor(a)
	if _, err := enterCounts(ctx, tx, a.Lang, sa, a.ScopedPlantID(), id, lines); err != nil {
		return syncsvc.FromError(a.Lang, err)
	}
	if doSubmit {
		if !a.Can("stockcount.record") {
			return syncsvc.Reject(syncsvc.ReasonPermissionRevoked, i18n.T(a.Lang, "error.forbidden"))
		}
		if _, err := submit(ctx, tx, a.Lang, sa, a.ScopedPlantID(), id); err != nil {
			return syncsvc.FromError(a.Lang, err)
		}
	}
	_ = tx.QueryRow(ctx, `SELECT server_version FROM stock_count WHERE id=$1`, id).Scan(&ver)
	r := syncsvc.Accept(id, ver)
	r.PlantID, r.Entity, r.EntityID = ct.PlantID, "StockCount", id
	return r
}

func pullCounts(ctx context.Context, a syncsvc.Actor, since syncsvc.Cursor) ([]syncsvc.Change, error) {
	return syncsvc.Rows(ctx, a, since, "stock_counts", `SELECT c.id, jsonb_build_object('id', c.id, 'number', c.number, 'plantId', c.plant_id,
			'scope', c.scope, 'itemKind', c.item_kind, 'category', c.category, 'blind', c.blind, 'countDate', c.count_date, 'status', c.status,
			'lines', COALESCE((SELECT jsonb_agg(jsonb_build_object('id', l.id, 'lineNo', l.line_no, 'itemKind', l.item_kind, 'itemId', l.item_id,
				'systemQty', CASE WHEN c.blind THEN NULL ELSE l.system_qty END, 'countedQty', l.counted_qty, 'note', l.note) ORDER BY l.line_no)
				FROM stock_count_line l WHERE l.count_id = c.id), '[]'::jsonb)),
			c.server_version, c.server_xid
		FROM stock_count c WHERE ($1 = '' OR c.plant_id = $1) AND (c.status IN ('OPEN','SUBMITTED') OR c.updated_at > now() - interval '60 days')`, a.ScopedPlantID())
}
