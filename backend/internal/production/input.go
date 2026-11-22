package production

import (
	"fmt"
	"strings"

	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/stock"
)

// reportInput is a validated create/update payload.
type reportInput struct {
	PlantID      string
	Date         string
	Shift        string
	Lines        []inputLine
	Usages       []inputUsage
	DowntimeMins int
	DowntimeNote *string
	Notes        *string
	Submit       bool
}

type inputLine struct {
	ProductID string
	LineQty
}

type inputUsage struct {
	MaterialID string
	Quantity   float64
}

func atMost(lang i18n.Lang, n int) string {
	return i18n.T(lang, "production.v.at_most", "max", n)
}

var qtyRule = stock.NumRule{TypeMsg: "production.v.enter_number", WholeMsg: "production.v.whole_blocks",
	Min: stock.F64(0), MinMsg: "production.v.negative", Max: stock.F64(1_000_000), MaxMsg: "production.v.too_large", Default: stock.F64(0)}

// parseReport validates the body of POST /reports and PATCH /reports/:id (port of reportSchema).
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
		for _, f := range []struct {
			key string
			dst *int
		}{{"targetQty", &il.TargetQty}, {"producedQty", &il.ProducedQty}, {"damageProdQty", &il.DamageProdQty}, {"damageHandQty", &il.DamageHandQty}, {"sampleQty", &il.SampleQty}} {
			if n, ok := stock.Number(l, f.key, qtyRule); ok {
				*f.dst = int(n)
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

	if n, ok := stock.Number(v, "downtimeMins", stock.NumRule{TypeMsg: "production.v.enter_minutes", WholeMsg: "production.v.whole_minutes",
		Min: stock.F64(0), MinMsg: "production.v.negative", Max: stock.F64(1440), MaxMsg: "production.v.max_downtime", Default: stock.F64(0)}); ok {
		in.DowntimeMins = int(n)
	}
	in.DowntimeNote = v.NullStr("downtimeNote").Trim().Max(500, atMost(lang, 500)).NilIfEmpty()
	in.Notes = v.NullStr("notes").Trim().Max(2000, atMost(lang, 2000)).NilIfEmpty()
	in.Submit = v.OptBool("submit").Default(false).Get()

	if !v.Failed() { // cross-field rules (zod superRefine) only when every field is individually valid
		seenP := map[string]bool{}
		for i, l := range in.Lines {
			if seenP[l.ProductID] {
				v.Fail(fmt.Sprintf("lines.%d.productId", i), "production.v.product_twice")
			}
			seenP[l.ProductID] = true
			if sum, bad := LineExceeds(l.LineQty); bad {
				v.Fail(fmt.Sprintf("lines.%d.damageProdQty", i), i18n.T(lang, "production.v.line_exceeds", "sum", sum, "produced", l.ProducedQty))
			}
		}
		hasLine := false
		for _, l := range in.Lines {
			if !IsEmptyLine(l.LineQty) {
				hasLine = true
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
		if in.DowntimeMins > 0 && in.DowntimeNote == nil {
			v.Fail("downtimeNote", "production.v.downtime_cause")
		}
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return in, nil
}

// cleanLines drops all-zero lines and zero-quantity material usages (what is actually stored).
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
