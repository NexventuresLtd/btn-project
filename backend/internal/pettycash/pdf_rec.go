package pettycash

import (
	"encoding/json"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/pdf"

	"github.com/gofiber/fiber/v2"
)

// reconciliationPDF is the float reconciliation report: count vs book, denominations, entries awaiting approval,
// explanation and the review (SRS FIN-02 / §7.3).
func (h *handler) reconciliationPDF(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	r, err := h.visibleRec(ctx, u, lang, c.Params("id"))
	if err != nil {
		return err
	}
	t := func(k string, a ...any) string { return i18n.T(lang, k, a...) }
	d, _ := dates.ParseISO(r.Date)
	rep, err := pdf.New(c, h.d, pdf.Options{Title: t("pettycash.pdf.rec_title"), Subtitle: r.Float.Code + " · " + r.Float.Name, DocumentNo: r.Number,
		Meta: []pdf.Meta{{Label: t("pettycash.pdf.c_date"), Value: dates.FormatDate(d)}, {Label: t("pettycash.pdf.float"), Value: r.Float.Code}}})
	if err != nil {
		return err
	}
	tone := pdf.Green
	switch {
	case r.Status == RecPendingReview:
		tone = pdf.Amber
	case r.Difference != 0:
		tone = pdf.Navy
	}
	rep.Badge(t("pettycash.rec.status."+r.Status), tone)
	rep.KpiRow([]pdf.Kpi{
		{Label: t("pettycash.pdf.k_book"), Value: format.RWF(float64(r.BookBalance))},
		{Label: t("pettycash.pdf.k_counted"), Value: format.RWF(float64(r.CountedAmount))},
		{Label: t("pettycash.pdf.k_difference"), Value: format.RWF(float64(r.Difference)), Hint: diffHint(lang, r.Difference)},
	})
	var den []struct {
		Value int64 `json:"value"`
		Count int64 `json:"count"`
	}
	_ = json.Unmarshal(r.Denominations, &den)
	if len(den) > 0 {
		var rows [][]any
		var total int64
		for _, x := range den {
			rows = append(rows, []any{format.Int(float64(x.Value)), format.Int(float64(x.Count)), format.Int(float64(x.Value * x.Count))})
			total += x.Value * x.Count
		}
		rep.Section(t("pettycash.pdf.s_denominations")).Table([]pdf.Column{{Header: t("pettycash.pdf.c_denomination"), Width: 1.5, Align: pdf.Right},
			{Header: t("pettycash.pdf.c_count"), Width: 1, Align: pdf.Right}, {Header: t("pettycash.pdf.c_total"), Width: 1.5, Align: pdf.Right}},
			rows, []any{t("pettycash.pdf.total"), "", format.Int(float64(total))})
	}
	b := func(s string) pdf.Cell { return pdf.Cell{Text: s, Bold: true} }
	orDash := func(p *string) string {
		if p == nil || *p == "" {
			return "—"
		}
		return *p
	}
	details := [][]any{
		{t("pettycash.pdf.f_rec"), b(r.Number)},
		{t("pettycash.pdf.float"), r.Float.Code + " · " + r.Float.Name},
		{t("pettycash.pdf.f_pending"), format.Int(float64(r.PendingCount))},
		{t("pettycash.pdf.f_explanation"), orDash(r.Explanation)},
		{t("pettycash.pdf.f_counted_by"), r.CreatedBy.Name + " · " + dates.FormatDateTime(r.CreatedAt)},
	}
	if r.ReviewedBy != nil && r.ReviewedAt != nil {
		details = append(details, []any{t("pettycash.pdf.f_reviewed_by"), r.ReviewedBy.Name + " · " + dates.FormatDateTime(*r.ReviewedAt)},
			[]any{t("pettycash.pdf.c_comment"), orDash(r.ReviewComment)})
	}
	if r.AdjustmentID != nil {
		details = append(details, []any{t("pettycash.pdf.f_adjustment"), t("pettycash.pdf.adjustment_posted")})
	}
	rep.Section(t("pettycash.pdf.s_details")).Table([]pdf.Column{{Header: t("pettycash.pdf.c_field"), Width: 1.4}, {Header: t("pettycash.pdf.c_details"), Width: 4}}, details, nil)
	if r.PendingCount > 0 {
		rep.Note(t("pettycash.pdf.rec_pending_note", "n", r.PendingCount))
	}
	sigs := []pdf.Signatory{{Role: t("pettycash.pdf.sig_counted"), Name: r.CreatedBy.Name, At: &r.CreatedAt}, {Role: t("pettycash.pdf.sig_reviewed")}}
	if r.ReviewedBy != nil {
		sigs[1].Name, sigs[1].At = r.ReviewedBy.Name, r.ReviewedAt
	}
	rep.Signatures(sigs)
	return pdf.Send(c, rep, r.Float.Code+"_"+r.Number+"_Reconciliation_"+r.Date+".pdf", c.Query("download") == "1")
}

func diffHint(lang i18n.Lang, d int64) string {
	switch {
	case d < 0:
		return i18n.T(lang, "pettycash.rec.shortage_hint")
	case d > 0:
		return i18n.T(lang, "pettycash.rec.surplus_hint")
	}
	return i18n.T(lang, "pettycash.rec.balanced_hint")
}
