package pettycash

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/pdf"

	"github.com/gofiber/fiber/v2"
	"golang.org/x/sync/errgroup"
)

func n0(v int64) string {
	if v == 0 {
		return ""
	}
	return format.Int(float64(v))
}

func fi(v int64) string { return format.Int(float64(v)) }

func months(lang i18n.Lang) []string {
	out := make([]string, 12)
	for i := range out {
		out[i] = i18n.T(lang, "pettycash.month."+strconv.Itoa(i+1))
	}
	return out
}

func categoryLabel(lang i18n.Lang, c string) string {
	if c == "" {
		return ""
	}
	if k := "pettycash.category." + c; i18n.Has(k) {
		return i18n.T(lang, k)
	}
	return c
}

func plural(lang i18n.Lang, key string, n int) string {
	if n == 1 {
		return i18n.T(lang, key+"_one", "n", n)
	}
	return i18n.T(lang, key+"_other", "n", n)
}

func statusLabel(lang i18n.Lang, e *Entry) string {
	switch e.Status {
	case StatusApproved:
		if e.AutoApproved {
			return i18n.T(lang, "pettycash.pdf.st_auto")
		}
		return i18n.T(lang, "pettycash.pdf.st_approved")
	case StatusSubmitted:
		return i18n.T(lang, "pettycash.pdf.st_pending")
	case StatusRejected:
		return i18n.T(lang, "pettycash.pdf.st_rejected")
	}
	return e.Status
}

func (h *handler) statementPDF(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	rg := dates.ResolveRange(c.Query("from"), c.Query("to"))
	sc, err := scopeOf(ctx, h.d.Pool, u)
	if err != nil {
		return err
	}
	floatID := c.Query("floatId")
	if floatID != "" && !sc.has(floatID) {
		return httpx.Forbidden("pettycash.err.not_your_float")
	}
	plantID := ""
	if sc.all {
		plantID = auth.PlantFilter(u, c.Query("plantId"))
	}
	f := filter{from: rg.From, to: rg.To, plantID: plantID, floatID: floatID, floats: sc.restrict(), statuses: []string{StatusApproved, StatusSubmitted}}
	var floatLabel string
	if floatID != "" {
		if fr, err := loadFloat(ctx, h.d.Pool, floatID); err == nil && fr != nil {
			floatLabel = fr.Code + " · " + fr.Name
		}
	}

	var (
		rows                 []entryRow
		rejected             int
		opening, closed      int64
		plantCode, plantName string
		plantFound           bool
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { rows, err = listEntries(gctx, h.d.Pool, f, false, -1, 0); return })
	g.Go(func() (err error) {
		rf := f
		rf.statuses = []string{StatusRejected}
		cond, args := rf.where()
		return h.d.Pool.QueryRow(gctx, `SELECT count(*)::int FROM "PettyCashEntry" e WHERE `+cond, args...).Scan(&rejected)
	})
	g.Go(func() (err error) {
		opening, closed, err = ledgerTotals(gctx, h.d.Pool, rg.From, rg.To, floatID, sc.restrict())
		return
	})
	if plantID != "" {
		g.Go(func() error {
			err := h.d.Pool.QueryRow(gctx, `SELECT code, name FROM "Plant" WHERE id = $1`, plantID).Scan(&plantCode, &plantName)
			plantFound = err == nil
			if err != nil && !isNoRows(err) {
				return err
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	if plantID != "" && !plantFound {
		return httpx.NotFound("entity.plant")
	}
	var approved, pending []Entry
	for i := range rows {
		e := toEntry(&rows[i], lang, rows[i].Bal)
		if e.Status == StatusApproved {
			approved = append(approved, e)
		} else {
			pending = append(pending, e)
		}
	}

	t := func(k string, a ...any) string { return i18n.T(lang, k, a...) }
	rangeLabel := formatRangeLabel(rg.FromISO, rg.ToISO, months(lang))
	subtitle, plantMeta := t("pettycash.pdf.subtitle_all"), t("pettycash.pdf.all")
	if plantID != "" {
		subtitle = t("pettycash.pdf.subtitle_plant", "code", plantCode, "name", plantName)
		plantMeta = plantCode
	}
	meta := []pdf.Meta{{Label: t("pettycash.pdf.period"), Value: rangeLabel}, {Label: t("pettycash.pdf.plant"), Value: plantMeta}}
	if floatLabel != "" {
		meta = append(meta, pdf.Meta{Label: t("pettycash.pdf.float"), Value: floatLabel})
		subtitle = floatLabel
	}
	rep, err := pdf.New(c, h.d, pdf.Options{Title: t("pettycash.pdf.statement_title", "range", rangeLabel), Subtitle: subtitle, Landscape: true, Meta: meta})
	if err != nil {
		return err
	}

	var totDeposits, totOut, totCharges int64
	var nDep, nExp int
	type catAgg struct {
		count                    int
		amount, charges, grandTt int64
	}
	cats := map[string]*catAgg{}
	for i := range approved {
		e := &approved[i]
		if e.Type == TypeDeposit {
			totDeposits += e.Amount
			nDep++
			continue
		}
		totOut += e.Amount
		totCharges += e.MomoCharge
		nExp++
		a := cats[e.Category]
		if a == nil {
			a = &catAgg{}
			cats[e.Category] = a
		}
		a.count++
		a.amount += e.Amount
		a.charges += e.MomoCharge
		a.grandTt += e.Amount + e.MomoCharge
	}
	rep.KpiRow([]pdf.Kpi{
		{Label: t("pettycash.pdf.k_opening"), Value: format.RWF(float64(opening)), Hint: dates.FormatDate(rg.From)},
		{Label: t("pettycash.pdf.k_deposits"), Value: format.RWF(float64(totDeposits)), Hint: plural(lang, "pettycash.pdf.topups", nDep)},
		{Label: t("pettycash.pdf.k_out"), Value: format.RWF(float64(totOut)), Hint: plural(lang, "pettycash.pdf.payments", nExp)},
		{Label: t("pettycash.pdf.k_charges"), Value: format.RWF(float64(totCharges))},
		{Label: t("pettycash.pdf.k_closing"), Value: format.RWF(float64(closed)), Hint: dates.FormatDate(rg.To)},
	})

	btn := t("pettycash.pdf.btn")
	data := [][]any{{dates.FormatDate(rg.From), "", pdf.Cell{Text: t("pettycash.pdf.opening_row"), Bold: true}, "", "", "", "", "", fi(opening), "", ""}}
	for i := range approved {
		e := &approved[i]
		desc := e.Description
		if e.Plant != nil {
			desc += " (" + e.Plant.Code + ")"
		}
		payee := deref(e.PayeeName)
		if e.PayeeName == nil && e.Type == TypeDeposit {
			payee = btn
		}
		var out, dep string
		if e.Type == TypeExpense {
			out = n0(e.Amount)
		} else {
			dep = n0(e.Amount)
		}
		bal := int64(0)
		if e.BalanceAfter != nil {
			bal = *e.BalanceAfter
		}
		data = append(data, []any{dates.FormatDate(mustDate(e.Date)), e.Number, desc, deref(e.MomoNumber), payee, out, dep, n0(e.MomoCharge), fi(bal), deref(e.InvoiceRef), statusLabel(lang, e)})
	}
	data = append(data, []any{dates.FormatDate(rg.To), "", pdf.Cell{Text: t("pettycash.pdf.closing_row"), Bold: true}, "", "", "", "", "", fi(closed), "", ""})
	rep.Section(t("pettycash.pdf.s_ledger")).Table([]pdf.Column{
		{Header: t("pettycash.pdf.c_date"), Width: 1.2}, {Header: t("pettycash.pdf.c_no"), Width: 1.6}, {Header: t("pettycash.pdf.c_desc"), Width: 2.5},
		{Header: t("pettycash.pdf.c_momo_no"), Width: 1.2}, {Header: t("pettycash.pdf.c_name"), Width: 2.0}, {Header: t("pettycash.pdf.c_out"), Width: 1.15, Align: pdf.Right},
		{Header: t("pettycash.pdf.c_deposit"), Width: 1.15, Align: pdf.Right}, {Header: t("pettycash.pdf.c_momo_chg"), Width: 0.9, Align: pdf.Right},
		{Header: t("pettycash.pdf.c_balance"), Width: 1.25, Align: pdf.Right}, {Header: t("pettycash.pdf.c_invoice"), Width: 1.0}, {Header: t("pettycash.pdf.c_status"), Width: 1.4},
	}, data, []any{"", "", t("pettycash.pdf.total"), "", "", fi(totOut), fi(totDeposits), fi(totCharges), fi(closed), "", ""})
	if plantID != "" {
		rep.Note(t("pettycash.pdf.note_plant", "code", plantCode))
	} else {
		rep.Note(t("pettycash.pdf.note_balance"))
	}

	// spending by category (from the approved expenses already loaded)
	type catRowT struct {
		cat string
		a   *catAgg
	}
	var cl []catRowT
	for k, a := range cats {
		cl = append(cl, catRowT{k, a})
	}
	sort.Slice(cl, func(i, j int) bool {
		if cl[i].a.grandTt != cl[j].a.grandTt {
			return cl[i].a.grandTt > cl[j].a.grandTt
		}
		return cl[i].cat < cl[j].cat
	})
	grand := totOut + totCharges
	var crow [][]any
	for _, x := range cl {
		share := "—"
		if grand != 0 {
			share = strconv.FormatFloat(float64(x.a.grandTt)/float64(grand)*100, 'f', 1, 64) + "%"
		}
		crow = append(crow, []any{categoryLabel(lang, x.cat), strconv.Itoa(x.a.count), fi(x.a.amount), fi(x.a.charges), fi(x.a.grandTt), share})
	}
	share := ""
	if grand != 0 {
		share = "100.0%"
	}
	rep.Section(t("pettycash.pdf.s_category")).Table([]pdf.Column{
		{Header: t("pettycash.pdf.c_category"), Width: 2.4}, {Header: t("pettycash.pdf.c_payments"), Width: 0.9, Align: pdf.Right}, {Header: t("pettycash.pdf.c_out"), Width: 1.3, Align: pdf.Right},
		{Header: t("pettycash.pdf.c_charges"), Width: 1.2, Align: pdf.Right}, {Header: t("pettycash.pdf.c_total"), Width: 1.3, Align: pdf.Right}, {Header: t("pettycash.pdf.c_share"), Width: 0.9, Align: pdf.Right},
	}, crow, []any{t("pettycash.pdf.total"), strconv.Itoa(nExp), fi(totOut), fi(totCharges), fi(grand), share}, t("pettycash.pdf.no_expenses"))

	if len(pending) > 0 {
		var prow [][]any
		var pAmt, pChg int64
		for i := range pending {
			e := &pending[i]
			desc := e.Description
			if e.Plant != nil {
				desc += " (" + e.Plant.Code + ")"
			}
			typ := t("pettycash.pdf.type_expense")
			if e.Type == TypeDeposit {
				typ = t("pettycash.pdf.type_deposit")
			}
			pAmt += e.Amount
			pChg += e.MomoCharge
			prow = append(prow, []any{dates.FormatDate(mustDate(e.Date)), e.Number, typ, desc, deref(e.PayeeName), fi(e.Amount), n0(e.MomoCharge), e.CreatedBy.Name})
		}
		rep.Section(t("pettycash.pdf.s_pending", "n", len(pending))).Table([]pdf.Column{
			{Header: t("pettycash.pdf.c_date"), Width: 1.2}, {Header: t("pettycash.pdf.c_no"), Width: 1.6}, {Header: t("pettycash.pdf.c_type"), Width: 0.9}, {Header: t("pettycash.pdf.c_desc"), Width: 3.2},
			{Header: t("pettycash.pdf.c_name"), Width: 1.8}, {Header: t("pettycash.pdf.c_amount"), Width: 1.2, Align: pdf.Right}, {Header: t("pettycash.pdf.c_momo_chg"), Width: 0.9, Align: pdf.Right},
			{Header: t("pettycash.pdf.c_recorded_by"), Width: 1.6},
		}, prow, []any{"", "", "", t("pettycash.pdf.total_pending"), "", fi(pAmt), fi(pChg), ""})
	}
	if rejected > 0 {
		rep.Note(plural(lang, "pettycash.pdf.rejected_note", rejected))
	}
	prepared := ""
	if u.Role == auth.RoleFinanceManager {
		prepared = u.Name
	}
	rep.Signatures([]pdf.Signatory{{Role: t("pettycash.pdf.sig_prepared"), Name: prepared}, {Role: t("pettycash.pdf.sig_approved_gm")}})

	name := "Petty-Cash-Statement_" + rg.FromISO + "_" + rg.ToISO + ".pdf"
	if plantID != "" {
		name = plantCode + "_" + name
	}
	if floatLabel != "" {
		name = strings.SplitN(floatLabel, " ", 2)[0] + "_" + name
	}
	return pdf.Send(c, rep, name, c.Query("download") == "1")
}

func mustDate(iso string) time.Time { t, _ := dates.ParseISO(iso); return t }

type trailStep struct{ step, who, when, note string }

func (h *handler) voucherPDF(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	row, _, err := h.loadVisible(ctx, u, h.d.Pool, c.Params("id"))
	if err != nil {
		return err
	}
	e := toEntry(row, lang, nil)
	t := func(k string, a ...any) string { return i18n.T(lang, k, a...) }
	person := func(name, role string) string {
		if role == "" {
			return name
		}
		return name + " (" + t("role."+role) + ")"
	}
	// approval history from the audit trail (oldest first)
	hist, err := h.history(ctx, row.ID)
	if err != nil {
		return err
	}
	stepName := map[string]string{"pettycash.create": "recorded", "pettycash.update": "edited", "pettycash.resubmit": "resubmitted", "pettycash.approve": "approved", "pettycash.reject": "rejected"}
	comment := func(s string) string {
		if s == AutoApprovedComment {
			return t("pettycash.pdf.auto_comment")
		}
		return s
	}
	var trail []trailStep
	for _, l := range hist {
		who := t("pettycash.pdf.system")
		if l.userName != nil {
			who = person(*l.userName, deref(l.userRole))
		}
		when := dates.FormatDateTime(l.at)
		note := ""
		if l.action != "pettycash.create" {
			note = comment(deref(l.comment))
		}
		trail = append(trail, trailStep{t("pettycash.pdf.step_" + stepName[l.action]), who, when, note})
		if l.action == "pettycash.create" && deref(l.status) == StatusApproved {
			trail = append(trail, trailStep{t("pettycash.pdf.step_approved"), t("pettycash.pdf.system"), when, comment(AutoApprovedComment)})
		}
	}
	createdBy := person(e.CreatedBy.Name, e.CreatedBy.Role)
	if len(hist) == 0 || trail[0].step != t("pettycash.pdf.step_recorded") {
		note := ""
		if e.ReadOnly {
			note = t("pettycash.pdf.from_payroll")
		}
		trail = append([]trailStep{{t("pettycash.pdf.step_recorded"), createdBy, dates.FormatDateTime(row.CreatedAt), note}}, trail...)
	}
	if len(hist) == 0 {
		switch {
		case e.Status == StatusApproved && e.AutoApproved:
			trail = append(trail, trailStep{t("pettycash.pdf.step_approved"), t("pettycash.pdf.system"), dates.FormatDateTime(row.CreatedAt), comment(orStr(e.ReviewComment, AutoApprovedComment))})
		case e.ReviewedBy != nil && e.ReviewedAt != nil:
			st := t("pettycash.pdf.step_approved")
			if e.Status == StatusRejected {
				st = t("pettycash.pdf.step_rejected")
			}
			trail = append(trail, trailStep{st, person(e.ReviewedBy.Name, e.ReviewedBy.Role), dates.FormatDateTime(*row.ReviewedAt), comment(deref(e.ReviewComment))})
		}
	}
	if e.Status == StatusSubmitted {
		trail = append(trail, trailStep{t("pettycash.pdf.step_approval"), t("pettycash.pdf.pending_who"), "", ""})
	}

	isDep := e.Type == TypeDeposit
	title, subtitle := t("pettycash.pdf.v_title_pay"), categoryLabel(lang, e.Category)
	if e.Plant != nil {
		subtitle += " · " + e.Plant.Code
	}
	if isDep {
		title, subtitle = t("pettycash.pdf.v_title_receipt"), t("pettycash.pdf.v_sub_receipt")
	}
	rep, err := pdf.New(c, h.d, pdf.Options{Title: title, Subtitle: subtitle, DocumentNo: e.Number,
		Meta: []pdf.Meta{{Label: t("pettycash.pdf.c_date"), Value: dates.FormatDate(row.Date)}}})
	if err != nil {
		return err
	}
	tone := pdf.Amber
	switch e.Status {
	case StatusApproved:
		tone = pdf.Green
	case StatusRejected:
		tone = pdf.Red
	}
	rep.Badge(statusLabel(lang, &e), tone)
	if isDep {
		rep.KpiRow([]pdf.Kpi{{Label: t("pettycash.pdf.k_received"), Value: format.RWF(float64(e.Amount))}})
	} else {
		rep.KpiRow([]pdf.Kpi{{Label: t("pettycash.pdf.k_paid"), Value: format.RWF(float64(e.Amount))}, {Label: t("pettycash.pdf.k_momo"), Value: format.RWF(float64(e.MomoCharge))},
			{Label: t("pettycash.pdf.k_total_from"), Value: format.RWF(float64(e.Amount + e.MomoCharge))}})
	}
	dash := "—"
	orDash := func(p *string) string {
		if p == nil || *p == "" {
			return dash
		}
		return *p
	}
	who, whoLabel := orDash(e.PayeeName), t("pettycash.pdf.paid_to")
	if isDep {
		whoLabel = t("pettycash.pdf.received_from")
		if e.PayeeName == nil {
			who = t("pettycash.pdf.btn")
		}
	}
	plantTxt := t("pettycash.pdf.no_plant")
	if e.Plant != nil {
		plantTxt = e.Plant.Code + " · " + e.Plant.Name
	}
	amt := fi(e.Amount)
	if !isDep && e.MomoCharge != 0 {
		amt += "  +  " + t("pettycash.pdf.momo_charge_text", "charge", fi(e.MomoCharge)) + "  =  " + fi(e.Amount+e.MomoCharge)
	}
	b := func(s string) pdf.Cell { return pdf.Cell{Text: s, Bold: true} }
	rep.Section(t("pettycash.pdf.s_details")).Table([]pdf.Column{{Header: t("pettycash.pdf.c_field"), Width: 1.3}, {Header: t("pettycash.pdf.c_details"), Width: 4}}, [][]any{
		{t("pettycash.pdf.f_voucher"), b(e.Number)}, {t("pettycash.pdf.c_date"), dates.FormatDate(row.Date)}, {whoLabel, b(who)},
		{t("pettycash.pdf.c_momo_no"), orDash(e.MomoNumber)}, {t("pettycash.pdf.c_desc"), e.Description}, {t("pettycash.pdf.c_category"), categoryLabel(lang, e.Category)},
		{t("pettycash.pdf.c_plant"), plantTxt}, {t("pettycash.pdf.f_invoice"), orDash(e.InvoiceRef)}, {t("pettycash.pdf.f_amount"), b(amt)},
	}, nil)
	var trows [][]any
	for _, s := range trail {
		trows = append(trows, []any{s.step, s.who, s.when, s.note})
	}
	rep.Section(t("pettycash.pdf.s_trail")).Table([]pdf.Column{{Header: t("pettycash.pdf.c_step"), Width: 1.1}, {Header: t("pettycash.pdf.c_by"), Width: 3.2},
		{Header: t("pettycash.pdf.c_when"), Width: 1.6}, {Header: t("pettycash.pdf.c_comment"), Width: 2.6}}, trows, nil)
	if e.Status != StatusApproved {
		if e.Status == StatusRejected {
			rep.Note(t("pettycash.pdf.note_rejected"))
		} else {
			rep.Note(t("pettycash.pdf.note_not_approved"))
		}
	}
	approvedBy := pdf.Signatory{Role: t("pettycash.pdf.sig_approved")}
	if e.AutoApproved && e.Status == StatusApproved {
		approvedBy.Name = t("pettycash.pdf.auto_comment")
	} else if e.Status == StatusApproved && e.ReviewedBy != nil {
		approvedBy.Name = e.ReviewedBy.Name
		approvedBy.At = row.ReviewedAt
	}
	received := pdf.Signatory{Role: t("pettycash.pdf.sig_received")}
	if !isDep {
		received.Name = deref(e.PayeeName)
	}
	rep.Signatures([]pdf.Signatory{{Role: t("pettycash.pdf.sig_requested"), Name: e.CreatedBy.Name}, approvedBy, received})
	return pdf.Send(c, rep, e.Number+"_Petty-Cash-Voucher_"+e.Date+".pdf", c.Query("download") == "1")
}

func orStr(p *string, def string) string {
	if p != nil {
		return *p
	}
	return def
}

type histRow struct {
	action             string
	at                 time.Time
	comment, status    *string
	userName, userRole *string
}

func (h *handler) history(ctx context.Context, id string) ([]histRow, error) {
	rows, err := h.d.Pool.Query(ctx, `SELECT l.action, l."createdAt", l."after"->>'reviewComment', l."after"->>'status', u.name, u.role::text
		FROM "AuditLog" l LEFT JOIN "User" u ON u.id = l."userId"
		WHERE l.entity = 'PettyCashEntry' AND l."entityId" = $1
		  AND l.action IN ('pettycash.create','pettycash.update','pettycash.resubmit','pettycash.approve','pettycash.reject')
		ORDER BY l."createdAt", l.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []histRow
	for rows.Next() {
		var r histRow
		if err := rows.Scan(&r.action, &r.at, &r.comment, &r.status, &r.userName, &r.userRole); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
