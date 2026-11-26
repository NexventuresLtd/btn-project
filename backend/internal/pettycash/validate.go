package pettycash

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"btn-ops/internal/dates"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
)

// entryInput is a validated create / edit payload.
type entryInput struct {
	Date        string
	Type        string
	Category    string
	Description string
	PlantID     *string
	PayeeName   *string
	MomoNumber  *string
	Amount      int64
	MomoCharge  int64
	InvoiceRef  *string
	FloatID     *string
	Department  *string
}

func inList(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// number coerces like z.coerce.number(): numbers as is, numeric strings parsed ("" and null are 0).
func number(raw any) (float64, bool) {
	switch t := raw.(type) {
	case nil:
		return 0, true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case float64:
		return t, !math.IsNaN(t) && !math.IsInf(t, 0)
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0, true
		}
		f, err := strconv.ParseFloat(s, 64)
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	return 0, false
}

// money validates an RWF amount field: numeric, whole francs, not too large.
func money(v *httpx.V, key, enterKey string, def *int64) (int64, bool) {
	raw, has := v.Raw(key), v.Has(key)
	if !has {
		if def != nil {
			return *def, true
		}
		v.Fail(key, enterKey)
		return 0, false
	}
	f, ok := number(raw)
	switch {
	case !ok:
		v.Fail(key, enterKey)
	case f != math.Trunc(f):
		v.Fail(key, "v.whole_francs")
	case f > maxAmount:
		v.Fail(key, "pettycash.v.amount_large")
	default:
		return int64(f), true
	}
	return 0, false
}

func optionalText(v *httpx.V, lang i18n.Lang, key string, max int) *string {
	s := v.NullStr(key).Trim()
	if s.Present() && utf8.RuneCountInString(s.Get()) > max {
		v.Fail(key, i18n.T(lang, "pettycash.v.at_most", "n", max))
	}
	return s.Collapse().NilIfEmpty()
}

// validateEntry ports entrySchema (zod): same field messages, cross-field rules run only when every field is valid.
func validateEntry(v *httpx.V, lang i18n.Lang, today string) entryInput {
	var in entryInput
	// date
	if s, ok := v.Raw("date").(string); !ok {
		v.Fail("date", "pettycash.v.enter_date")
	} else {
		s = strings.TrimSpace(s)
		switch {
		case !dates.IsValidISO(s):
			v.Fail("date", "pettycash.v.valid_date")
		case s > today:
			v.Fail("date", "pettycash.v.date_future")
		default:
			in.Date = s
		}
	}
	// type
	if s, ok := v.Raw("type").(string); !ok || !inList(entryTypes, s) {
		v.Fail("type", "pettycash.v.choose_type")
	} else {
		in.Type = s
	}
	// category (optional): a reference-data expense category code (or a legacy enum value); checked against the DB later
	if raw := v.Raw("category"); raw != nil {
		if s, ok := raw.(string); !ok || strings.TrimSpace(s) == "" || len(s) > 40 {
			v.Fail("category", "pettycash.v.choose_category")
		} else {
			in.Category = strings.TrimSpace(s)
		}
	}
	// description
	if s, ok := v.Raw("description").(string); !ok {
		v.Fail("description", "pettycash.v.enter_description")
	} else {
		s = strings.TrimSpace(s)
		switch n := utf8.RuneCountInString(s); {
		case n < 3:
			v.Fail("description", "pettycash.v.describe")
		case n > 300:
			v.Fail("description", i18n.T(lang, "pettycash.v.at_most", "n", 300))
		default:
			in.Description = strings.Join(strings.Fields(s), " ")
		}
	}
	in.PlantID = v.NullStr("plantId").Trim().Max(40).NilIfEmpty()
	in.PayeeName = optionalText(v, lang, "payeeName", 120)
	// momoNumber
	if ms := v.NullStr("momoNumber").Trim(); ms.Present() {
		raw := ms.Get()
		if utf8.RuneCountInString(raw) > 20 {
			v.Fail("momoNumber", "pettycash.v.momo_valid")
		} else if raw != "" {
			if n := normaliseMomo(raw); n == "" {
				v.Fail("momoNumber", "pettycash.v.momo_format")
			} else {
				in.MomoNumber = &n
			}
		}
	}
	// amounts
	if a, ok := money(v, "amount", "pettycash.v.enter_amount", nil); ok {
		if a <= 0 {
			v.Fail("amount", "pettycash.v.amount_positive")
		}
		in.Amount = a
	}
	zero := int64(0)
	if c, ok := money(v, "momoCharge", "pettycash.v.enter_charge", &zero); ok {
		if c < 0 {
			v.Fail("momoCharge", "pettycash.v.charge_negative")
		}
		in.MomoCharge = c
	}
	in.InvoiceRef = optionalText(v, lang, "invoiceRef", 60)
	in.FloatID = v.NullStr("floatId").Trim().Max(60).NilIfEmpty()
	in.Department = v.NullStr("department").Trim().Max(40).NilIfEmpty()

	if v.Failed() {
		return in
	}
	if in.Type == TypeExpense {
		switch {
		case in.Category == "":
			v.Fail("category", "pettycash.v.choose_category")
		case in.Category == "FLOAT_DEPOSIT":
			v.Fail("category", "pettycash.v.float_only_deposit")
		}
		if in.PayeeName == nil {
			v.Fail("payeeName", "pettycash.v.enter_payee")
		}
		if in.MomoCharge > in.Amount {
			v.Fail("momoCharge", "pettycash.v.charge_exceeds")
		}
		if in.InvoiceRef == nil { // SRS §7.3: every expenditure carries a receipt reference
			v.Fail("invoiceRef", "pettycash.v.receipt_required")
		}
	} else {
		in.Category, in.MomoCharge, in.Department = "FLOAT_DEPOSIT", 0, nil
	}
	return in
}
