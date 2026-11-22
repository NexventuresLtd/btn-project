package stock

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"btn-ops/internal/dates"
	"btn-ops/internal/httpx"
)

// IDField is z.string({error:"Required"}).trim().min(1).max(40).
func IDField(v *httpx.V, key string) string {
	if _, ok := v.Raw(key).(string); !ok {
		v.Fail(key, "v.required")
		return ""
	}
	return v.Str(key).Trim().Min(1, "v.required").Max(40).Get()
}

// PastOrToday validates a YYYY-MM-DD business date that is not in the future (Kigali calendar).
func PastOrToday(v *httpx.V, key string, now ...time.Time) string {
	raw, ok := v.Raw(key).(string)
	if !ok {
		v.Fail(key, "stock.v.enter_date")
		return ""
	}
	s := strings.TrimSpace(raw)
	if !dates.IsValidISO(s) {
		v.Fail(key, "stock.v.valid_date")
		return ""
	}
	if s > dates.TodayISO(now...) {
		v.Fail(key, "stock.v.date_future")
		return ""
	}
	return s
}

// QueryDate reads an optional ?from= style date: "" means unset, anything else must be a valid ISO date.
func QueryDate(q *httpx.V, key string) string {
	s, _ := q.Raw(key).(string)
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !dates.IsValidISO(s) {
		q.Fail(key, "stock.v.valid_date")
		return ""
	}
	return s
}

// QueryID reads an optional id filter (trimmed, at most 40 characters).
func QueryID(q *httpx.V, key string) string {
	s, _ := q.Raw(key).(string)
	s = strings.TrimSpace(s)
	if len([]rune(s)) > 40 {
		q.Fail(key, "Too big: expected string to have <=40 characters")
		return ""
	}
	return s
}

// NumRule describes z.coerce.number(...) with custom messages.
type NumRule struct {
	TypeMsg   string   // shown when missing / not a number
	WholeMsg  string   // "" = decimals allowed
	Min, Max  *float64 // nil = unbounded
	MinMsg    string
	MaxMsg    string
	Default   *float64 // value when the key is absent
	MaxDecMsg string   // "" = no decimal limit; otherwise at most 3 decimals
}

// F64 is a helper for rule bounds.
func F64(f float64) *float64 { return &f }

func numeric(raw any) bool {
	switch t := raw.(type) {
	case json.Number:
		_, err := t.Float64()
		return err == nil
	case float64, int, int64:
		return true
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return true
		}
		f, err := strconv.ParseFloat(s, 64)
		return err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	return false
}

// Number reads a number field under rule r; ok=false means a message was recorded.
func Number(v *httpx.V, key string, r NumRule) (float64, bool) {
	raw, has := v.Raw(key), v.Has(key)
	if !has && r.Default != nil {
		return *r.Default, true
	}
	if !has || !numeric(raw) {
		v.Fail(key, r.TypeMsg)
		return 0, false
	}
	n := v.Num(key).Coerce()
	if r.WholeMsg != "" {
		n.Whole(r.WholeMsg)
	}
	if r.Min != nil {
		n.Min(*r.Min, r.MinMsg)
	}
	if r.Max != nil {
		n.Max(*r.Max, r.MaxMsg)
	}
	if !n.Present() {
		return 0, false
	}
	f := n.F()
	if r.MaxDecMsg != "" && math.Abs(math.Round(f*1000)-f*1000) >= 1e-6 {
		v.Fail(key, r.MaxDecMsg)
		return 0, false
	}
	return f, true
}
