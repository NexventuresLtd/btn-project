package production

import (
	"fmt"
	"strings"

	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/stock"
)

// reportInput is a validated create/update payload (web form, offline production_entry, correction request).
type reportInput struct {
	PlantID      string
	Date         string
	Shift        string
	Lines        []inputLine
	Usages       []inputUsage
	DowntimeMins int
	DowntimeNote *string
	Notes        *string
	Flags        []inputFlag
	FlagsSet     bool // exceptionFlags present in the payload (absent = keep the stored flags)
	Submit       bool
	MergeInto    string // offline conflict resolution: add this entry into another one (§5.5 "merge")
}

type inputLine struct {
	ProductID  string
	BatchRef   string
	Additional bool
	LineQty
	Defects     []inputDefect
	DefectsSet  bool // defects given: damage figures are derived from them
	damageGiven bool
}

type inputDefect struct {
	ReasonCode string
	Qty        int
	Note       *string
	Stage      string // resolved from reference data in prepare
}

type inputUsage struct {
	MaterialID string
	Quantity   float64
}

type inputFlag struct {
	Code string
	Note *string
}

func atMost(lang i18n.Lang, n int) string {
	return i18n.T(lang, "production.v.at_most", "max", n)
}

var qtyRule = stock.NumRule{TypeMsg: "production.v.enter_number", WholeMsg: "production.v.whole_blocks",
	Min: stock.F64(0), MinMsg: "production.v.negative", Max: stock.F64(1_000_000), MaxMsg: "production.v.too_large", Default: stock.F64(0)}

// parseReport validates the body of POST /reports, PATCH /reports/:id, the offline production_entry record and
// a correction request (port of reportSchema + batches, defects and exception flags).
func parseReport(v *httpx.V, lang i18n.Lang) (*reportInput, error) {
	in := &reportInput{}
	in.PlantID = stock.IDField(v, "plantId")
	in.Date = stock.PastOrToday(v, "date")
	if s, ok := v.Raw("shift").(string); !ok || (s != "DAY" && s != "NIGHT") {
		v.Fail("shift", "production.v.choose_shift")
	} else {
		in.Shift = s
	}

	lineVs := v.Arr("lines", false)
	if len(lineVs) > 100 {
		v.Fail("lines", "production.v.max_lines")
	}
	for _, l := range lineVs {
		il := inputLine{ProductID: stock.IDField(l, "productId")}
		if l.Has("batchRef") && l.Raw("batchRef") != nil {
			il.BatchRef = NormBatch(l.Str("batchRef").Trim().Max(40, atMost(lang, 40)).Get())
		}
		il.Additional = l.OptBool("additionalBatch").Default(false).Get()
		for _, f := range []struct {
			key string
			dst *int
		}{{"targetQty", &il.TargetQty}, {"producedQty", &il.ProducedQty}, {"damageProdQty", &il.DamageProdQty}, {"damageHandQty", &il.DamageHandQty}, {"sampleQty", &il.SampleQty}} {
			if n, ok := stock.Number(l, f.key, qtyRule); ok {
				*f.dst = int(n)
			}
		}
		il.damageGiven = l.Has("damageProdQty") || l.Has("damageHandQty")
		if l.Has("defects") && l.Raw("defects") != nil {
			il.DefectsSet = true
			dvs := l.Arr("defects", false)
			if len(dvs) > 30 {
				l.Fail("defects", "production.v.max_defects")
			}
			for _, d := range dvs {
				id := inputDefect{ReasonCode: strings.ToUpper(stock.IDField(d, "reasonCode"))}
				if n, ok := stock.Number(d, "qty", stock.NumRule{TypeMsg: "production.v.enter_number", WholeMsg: "production.v.whole_blocks",
					Min: stock.F64(0), MinMsg: "production.v.negative", Max: stock.F64(1_000_000), MaxMsg: "production.v.too_large"}); ok {
					id.Qty = int(n)
				}
				id.Note = d.NullStr("note").Trim().Max(300, atMost(lang, 300)).NilIfEmpty()
				il.Defects = append(il.Defects, id)
			}
		}
		in.Lines = append(in.Lines, il)
	}

	usageVs := v.Arr("materialUsages", false)
	if len(usageVs) > 50 {
		v.Fail("materialUsages", "production.v.max_usages")
	}
	for _, u := range usageVs {
		iu := inputUsage{MaterialID: stock.IDField(u, "materialId")}
		if n, ok := stock.Number(u, "quantity", stock.NumRule{TypeMsg: "production.v.enter_number", Min: stock.F64(0), MinMsg: "production.v.negative",
			Max: stock.F64(1_000_000), MaxMsg: "production.v.too_large", MaxDecMsg: "production.v.max_3_decimals"}); ok {
			iu.Quantity = n
		}
		in.Usages = append(in.Usages, iu)
	}

	if v.Has("exceptionFlags") && v.Raw("exceptionFlags") != nil {
		in.FlagsSet = true
		fvs := v.Arr("exceptionFlags", false)
		if len(fvs) > 20 {
			v.Fail("exceptionFlags", "production.v.max_flags")
		}
		for _, f := range fvs {
			in.Flags = append(in.Flags, inputFlag{Code: strings.ToUpper(stock.IDField(f, "code")), Note: f.NullStr("note").Trim().Max(300, atMost(lang, 300)).NilIfEmpty()})
		}
	}

	if n, ok := stock.Number(v, "downtimeMins", stock.NumRule{TypeMsg: "production.v.enter_minutes", WholeMsg: "production.v.whole_minutes",
		Min: stock.F64(0), MinMsg: "production.v.negative", Max: stock.F64(1440), MaxMsg: "production.v.max_downtime", Default: stock.F64(0)}); ok {
		in.DowntimeMins = int(n)
	}
	in.DowntimeNote = v.NullStr("downtimeNote").Trim().Max(500, atMost(lang, 500)).NilIfEmpty()
	in.Notes = v.NullStr("notes").Trim().Max(2000, atMost(lang, 2000)).NilIfEmpty()
	in.Submit = v.OptBool("submit").Default(false).Get()
	if v.Has("mergeInto") && v.Raw("mergeInto") != nil {
		in.MergeInto = stock.IDField(v, "mergeInto")
	}

	if !v.Failed() { // cross-field rules that need no reference data (all reported together, like zod superRefine)
		in.applyDefaultBatches()
		hasLine := false
		for i, l := range in.Lines {
			if !IsEmptyLine(l.LineQty) || len(l.Defects) > 0 {
				hasLine = true
			}
			seenR := map[string]bool{}
			for j, d := range l.Defects {
				if seenR[d.ReasonCode] {
					v.Fail(fmt.Sprintf("lines.%d.defects.%d.reasonCode", i, j), "production.v.reason_twice")
				}
				seenR[d.ReasonCode] = true
			}
		}
		if !hasLine {
			v.Fail("lines", "production.v.at_least_one_product")
		}
		seenM := map[string]bool{}
		for i, u := range in.Usages {
			if seenM[u.MaterialID] {
				v.Fail(fmt.Sprintf("materialUsages.%d.materialId", i), "production.v.material_twice")
			}
			seenM[u.MaterialID] = true
		}
		seenF := map[string]bool{}
		for i, f := range in.Flags {
			if seenF[f.Code] {
				v.Fail(fmt.Sprintf("exceptionFlags.%d.code", i), "production.v.flag_twice")
			}
			seenF[f.Code] = true
		}
		if in.DowntimeMins > 0 && in.DowntimeNote == nil {
			v.Fail("downtimeNote", "production.v.downtime_cause")
		}
		in.checkLines(func(field, msg string) { v.Fail(field, msg) }, lang, false)
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return in, nil
}

// applyDefaultBatches fills empty batch references with the shift default (PRD-01).
func (in *reportInput) applyDefaultBatches() {
	for i := range in.Lines {
		if in.Lines[i].BatchRef == "" {
			in.Lines[i].BatchRef = DefaultBatch(in.Shift)
		}
	}
}

// checkLines applies the per-line cross-field rules. withDefects=true runs after defect stages were resolved
// (damage derived from the reason breakdown); before that only rules independent of reasons run.
func (in *reportInput) checkLines(fail func(field, msg string), lang i18n.Lang, withDefects bool) {
	seen := map[string]bool{}
	for i, l := range in.Lines {
		if IsEmptyLine(l.LineQty) && len(l.Defects) == 0 {
			continue
		}
		k := l.ProductID + "|" + BatchKey(l.BatchRef)
		if seen[k] {
			fail(fmt.Sprintf("lines.%d.productId", i), i18n.T(lang, "production.v.product_twice"))
		}
		seen[k] = true
		if !l.DefectsSet || withDefects {
			if sum, bad := LineExceeds(l.LineQty); bad {
				fail(fmt.Sprintf("lines.%d.damageProdQty", i), i18n.T(lang, "production.v.line_exceeds", "sum", sum, "produced", l.ProducedQty))
			}
		}
	}
}

// deriveDamage sets the damage figures of lines that carry a defect breakdown (stages must be resolved).
// A line that sent both figures and a breakdown must agree.
func (in *reportInput) deriveDamage(fail func(field, msg string), lang i18n.Lang) {
	for i := range in.Lines {
		l := &in.Lines[i]
		if !l.DefectsSet {
			continue
		}
		ds := make([]DefectQty, 0, len(l.Defects))
		for _, d := range l.Defects {
			ds = append(ds, DefectQty{Stage: d.Stage, Qty: d.Qty})
		}
		p, h := DamageFromDefects(ds)
		if l.damageGiven && (l.DamageProdQty != 0 || l.DamageHandQty != 0) && (p != l.DamageProdQty || h != l.DamageHandQty) {
			fail(fmt.Sprintf("lines.%d.defects", i), i18n.T(lang, "production.v.defects_mismatch", "prod", l.DamageProdQty, "hand", l.DamageHandQty))
			continue
		}
		l.DamageProdQty, l.DamageHandQty = p, h
	}
}

// cleanLines drops all-zero lines (what is actually stored).
func (in *reportInput) cleanLines() []inputLine {
	out := make([]inputLine, 0, len(in.Lines))
	for _, l := range in.Lines {
		if !IsEmptyLine(l.LineQty) {
			out = append(out, l)
		}
	}
	return out
}

func (in *reportInput) cleanUsages() []inputUsage {
	out := make([]inputUsage, 0, len(in.Usages))
	for _, u := range in.Usages {
		if u.Quantity > 0 {
			out = append(out, u)
		}
	}
	return out
}

func usageText(q float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", q), "0"), ".")
}

// payloadOf turns a stored report back into the canonical payload (corrections "before", offline conflict data).
func payloadOf(r *reportRec) map[string]any {
	lines := make([]map[string]any, 0, len(r.Lines))
	for _, l := range r.Lines {
		ds := make([]map[string]any, 0, len(l.Defects))
		for _, d := range l.Defects {
			ds = append(ds, map[string]any{"reasonCode": d.ReasonCode, "qty": d.Qty, "note": d.Note})
		}
		lines = append(lines, map[string]any{"productId": l.ProductID, "batchRef": l.BatchRef, "additionalBatch": l.Additional,
			"targetQty": l.TargetQty, "producedQty": l.ProducedQty, "damageProdQty": l.DamageProdQty, "damageHandQty": l.DamageHandQty,
			"sampleQty": l.SampleQty, "defects": ds})
	}
	us := make([]map[string]any, 0, len(r.Usages))
	for _, u := range r.Usages {
		us = append(us, map[string]any{"materialId": u.MaterialID, "quantity": u.Quantity})
	}
	fs := make([]map[string]any, 0, len(r.Flags))
	for _, f := range r.Flags {
		fs = append(fs, map[string]any{"code": f.Code, "note": f.Note})
	}
	return map[string]any{"id": r.ID, "number": r.Number, "status": r.Status, "plantId": r.PlantID, "date": isoDate(r.Date), "shift": r.Shift,
		"lines": lines, "materialUsages": us, "downtimeMins": r.DowntimeMins, "downtimeNote": r.DowntimeNote, "notes": r.Notes, "exceptionFlags": fs}
}
