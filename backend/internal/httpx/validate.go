package httpx

import (
	"encoding/json"
	"fmt"
	"math"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"btn-ops/internal/dates"
	"btn-ops/internal/i18n"
)

// V is a zod-like validator over a decoded JSON object (or query map). It records the FIRST
// message per field path and produces the same 400 envelope as the Node `parse()` helper:
//
//	{"error":{"code":"bad_request","message":"Please correct the highlighted fields","fields":{...}}}
//
// Messages passed to the rule methods may be i18n keys (e.g. "v.email") or literal English text.
type V struct {
	data   map[string]any
	prefix string
	sink   *sink
	lang   i18n.Lang
}

type sink struct{ fields map[string]string }

// NewV wraps a decoded object.
func NewV(data map[string]any, lang i18n.Lang) *V {
	if data == nil {
		data = map[string]any{}
	}
	return &V{data: data, sink: &sink{fields: map[string]string{}}, lang: lang}
}

func (v *V) path(key string) string {
	if v.prefix == "" {
		return key
	}
	return v.prefix + "." + key
}

// Fail records a message for a field (first message wins).
func (v *V) Fail(field, msg string) {
	p := v.path(field)
	if _, ok := v.sink.fields[p]; !ok {
		v.sink.fields[p] = i18n.T(v.lang, msg)
	}
}

// Check records msg for field when ok is false.
func (v *V) Check(ok bool, field, msg string) {
	if !ok {
		v.Fail(field, msg)
	}
}

// Failed reports whether any field failed so far.
func (v *V) Failed() bool { return len(v.sink.fields) > 0 }

// Fields returns the collected messages.
func (v *V) Fields() map[string]string { return v.sink.fields }

// Err returns nil or the standard 400 validation error.
func (v *V) Err() error {
	if len(v.sink.fields) == 0 {
		return nil
	}
	return &Error{Status: 400, Code: "bad_request", Message: i18n.T(i18n.EN, "error.validation"), Key: "error.validation", Fields: v.sink.fields}
}

// Has reports whether key was supplied (even as null).
func (v *V) Has(key string) bool { _, ok := v.data[key]; return ok }

// Raw returns the untouched value.
func (v *V) Raw(key string) any { return v.data[key] }

func typeName(x any) string {
	switch t := x.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case json.Number, float64, int, int64:
		return "number"
	case bool:
		return "boolean"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		_ = t
		return "unknown"
	}
}

func (v *V) typeErr(key, expected string, present bool, got any) {
	rec := "undefined"
	if present {
		rec = typeName(got)
	}
	v.Fail(key, fmt.Sprintf("Invalid input: expected %s, received %s", expected, rec))
}

// ── strings ──────────────────────────────────────────────────────────────────

// S is a string rule chain.
type S struct {
	v       *V
	key     string
	val     string
	present bool // supplied and a string
	null    bool
	failed  bool
}

func (v *V) str(key string, required, nullable bool) *S {
	s := &S{v: v, key: key}
	raw, has := v.data[key]
	switch {
	case !has:
		if required {
			v.typeErr(key, "string", false, nil)
			s.failed = true
		}
	case raw == nil:
		s.null = true
		if !nullable {
			v.typeErr(key, "string", true, nil)
			s.failed = true
		}
	default:
		str, ok := raw.(string)
		if !ok {
			v.typeErr(key, "string", true, raw)
			s.failed = true
		} else {
			s.val, s.present = str, true
		}
	}
	return s
}

// Str is a required string. OptStr may be absent. NullStr may be absent or null.
func (v *V) Str(key string) *S     { return v.str(key, true, false) }
func (v *V) OptStr(key string) *S  { return v.str(key, false, false) }
func (v *V) NullStr(key string) *S { return v.str(key, false, true) }

func (s *S) active() bool { return s.present && !s.failed }
func (s *S) fail(msg string, def string) *S {
	if msg == "" {
		msg = def
	}
	s.v.Fail(s.key, msg)
	s.failed = true
	return s
}
func first(m []string) string {
	if len(m) > 0 {
		return m[0]
	}
	return ""
}

func (s *S) Trim() *S {
	if s.active() {
		s.val = strings.TrimSpace(s.val)
	}
	return s
}
func (s *S) Lower() *S {
	if s.active() {
		s.val = strings.ToLower(s.val)
	}
	return s
}
func (s *S) Upper() *S {
	if s.active() {
		s.val = strings.ToUpper(s.val)
	}
	return s
}

// Collapse replaces runs of whitespace by one space (Node: s.replace(/\s+/g," ")).
func (s *S) Collapse() *S {
	if s.active() {
		s.val = strings.Join(strings.Fields(s.val), " ")
	}
	return s
}

func (s *S) Min(n int, msg ...string) *S {
	if s.active() && utf8.RuneCountInString(s.val) < n {
		s.fail(first(msg), fmt.Sprintf("Too small: expected string to have >=%d characters", n))
	}
	return s
}
func (s *S) Max(n int, msg ...string) *S {
	if s.active() && utf8.RuneCountInString(s.val) > n {
		s.fail(first(msg), fmt.Sprintf("Too big: expected string to have <=%d characters", n))
	}
	return s
}
func (s *S) Email(msg ...string) *S {
	if s.active() {
		a, err := mail.ParseAddress(s.val)
		if err != nil || a.Address != s.val || !strings.Contains(s.val[strings.Index(s.val, "@"):], ".") {
			s.fail(first(msg), "Invalid email address")
		}
	}
	return s
}
func (s *S) Regex(re *regexp.Regexp, msg ...string) *S {
	if s.active() && !re.MatchString(s.val) {
		s.fail(first(msg), "Invalid string: must match pattern "+re.String())
	}
	return s
}
func (s *S) OneOf(vals []string, msg ...string) *S {
	if s.active() {
		for _, x := range vals {
			if x == s.val {
				return s
			}
		}
		q := make([]string, len(vals))
		for i, x := range vals {
			q[i] = strconv.Quote(x)
		}
		s.fail(first(msg), "Invalid option: expected one of "+strings.Join(q, "|"))
	}
	return s
}

// Date validates YYYY-MM-DD (Kigali business date).
func (s *S) Date(msg ...string) *S {
	if s.active() && !dates.IsValidISO(s.val) {
		s.fail(first(msg), "Invalid date (expected YYYY-MM-DD)")
	}
	return s
}

// Default applies when the key was absent.
func (s *S) Default(d string) *S {
	if !s.present && !s.null && !s.failed {
		s.val, s.present = d, true
	}
	return s
}

// Present reports whether a valid string value is available (not absent / null / failed).
func (s *S) Present() bool { return s.present && !s.failed }

// Get returns the value ("" when absent/null/failed).
func (s *S) Get() string {
	if s.failed {
		return ""
	}
	return s.val
}

// Ptr returns nil when absent or null (empty string stays a non-nil pointer).
func (s *S) Ptr() *string {
	if !s.Present() {
		return nil
	}
	x := s.val
	return &x
}

// NilIfEmpty returns nil for absent/null/"" (zod: `.transform(v => v ? v : null)`).
func (s *S) NilIfEmpty() *string {
	if !s.Present() || s.val == "" {
		return nil
	}
	x := s.val
	return &x
}

// ── numbers ──────────────────────────────────────────────────────────────────

// N is a number rule chain (float64 internally; use I() for ints).
type N struct {
	v        *V
	key      string
	val      float64
	present  bool
	failed   bool
	required bool
}

func (v *V) num(key string, required, nullable, coerce bool) *N {
	n := &N{v: v, key: key, required: required}
	raw, has := v.data[key]
	if !has || (raw == nil && nullable) {
		if !has && required {
			v.typeErr(key, "number", false, nil)
			n.failed = true
		}
		return n
	}
	if coerce {
		if s, ok := raw.(string); ok {
			s = strings.TrimSpace(s)
			f, err := strconv.ParseFloat(s, 64)
			if s == "" {
				f, err = 0, nil // Number("") === 0 in JS
			}
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
				v.Fail(key, "Invalid input: expected number, received NaN")
				n.failed = true
				return n
			}
			n.val, n.present = f, true
			return n
		}
	}
	switch t := raw.(type) {
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			v.typeErr(key, "number", true, raw)
			n.failed = true
		} else {
			n.val, n.present = f, true
		}
	case float64:
		n.val, n.present = t, true
	case int:
		n.val, n.present = float64(t), true
	case int64:
		n.val, n.present = float64(t), true
	default:
		v.typeErr(key, "number", true, raw)
		n.failed = true
	}
	return n
}

// Num is a required number; OptNum may be absent; NullNum may be absent or null.
func (v *V) Num(key string) *N     { return v.num(key, true, false, false) }
func (v *V) OptNum(key string) *N  { return v.num(key, false, false, false) }
func (v *V) NullNum(key string) *N { return v.num(key, false, true, false) }

// Coerce re-reads the field accepting numeric strings (z.coerce.number()). Call right after the constructor.
func (n *N) Coerce() *N {
	raw, has := n.v.data[n.key]
	if !has {
		return n
	}
	if _, isStr := raw.(string); !isStr {
		return n
	}
	delete(n.v.sink.fields, n.v.path(n.key)) // discard the strict-type error
	return n.v.num(n.key, n.required, false, true)
}

func (n *N) active() bool { return n.present && !n.failed }
func (n *N) fail(msg, def string) *N {
	if msg == "" {
		msg = def
	}
	n.v.Fail(n.key, msg)
	n.failed = true
	return n
}

// Whole requires an integer.
func (n *N) Whole(msg ...string) *N {
	if n.active() && n.val != math.Trunc(n.val) {
		n.fail(first(msg), "Invalid input: expected int, received number")
	}
	return n
}
func (n *N) Min(min float64, msg ...string) *N {
	if n.active() && n.val < min {
		n.fail(first(msg), fmt.Sprintf("Too small: expected number to be >=%v", min))
	}
	return n
}
func (n *N) Gt(min float64, msg ...string) *N {
	if n.active() && n.val <= min {
		n.fail(first(msg), fmt.Sprintf("Too small: expected number to be >%v", min))
	}
	return n
}
func (n *N) Max(max float64, msg ...string) *N {
	if n.active() && n.val > max {
		n.fail(first(msg), fmt.Sprintf("Too big: expected number to be <=%v", max))
	}
	return n
}
func (n *N) Default(d float64) *N {
	if !n.present && !n.failed {
		if _, has := n.v.data[n.key]; !has {
			n.val, n.present = d, true
		}
	}
	return n
}
func (n *N) Present() bool { return n.present && !n.failed }
func (n *N) F() float64 {
	if n.failed {
		return 0
	}
	return n.val
}
func (n *N) I() int64 { return int64(math.Round(n.F())) }

// PtrI returns nil when absent/null.
func (n *N) PtrI() *int64 {
	if !n.Present() {
		return nil
	}
	x := n.I()
	return &x
}
func (n *N) PtrF() *float64 {
	if !n.Present() {
		return nil
	}
	x := n.val
	return &x
}

// ── booleans ─────────────────────────────────────────────────────────────────

// B is a bool rule.
type B struct {
	val, present, failed bool
}

func (v *V) boolean(key string, required bool) *B {
	b := &B{}
	raw, has := v.data[key]
	if !has {
		if required {
			v.typeErr(key, "boolean", false, nil)
			b.failed = true
		}
		return b
	}
	x, ok := raw.(bool)
	if !ok {
		v.typeErr(key, "boolean", true, raw)
		b.failed = true
		return b
	}
	b.val, b.present = x, true
	return b
}
func (v *V) Bool(key string) *B    { return v.boolean(key, true) }
func (v *V) OptBool(key string) *B { return v.boolean(key, false) }

// Default applies when absent.
func (b *B) Default(d bool) *B {
	if !b.present && !b.failed {
		b.val, b.present = d, true
	}
	return b
}
func (b *B) Present() bool { return b.present && !b.failed }
func (b *B) Get() bool     { return b.val && !b.failed }
func (b *B) Ptr() *bool {
	if !b.Present() {
		return nil
	}
	x := b.val
	return &x
}

// ── nested ───────────────────────────────────────────────────────────────────

// Obj returns a sub-validator for an object field; nil when absent (optional) — if required is
// true, absence is an error and an empty sub-validator is returned.
func (v *V) Obj(key string, required bool) *V {
	raw, has := v.data[key]
	if !has || raw == nil {
		if required {
			v.typeErr(key, "object", has, raw)
			return &V{data: map[string]any{}, prefix: v.path(key), sink: v.sink, lang: v.lang}
		}
		return nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		v.typeErr(key, "object", true, raw)
		return nil
	}
	return &V{data: m, prefix: v.path(key), sink: v.sink, lang: v.lang}
}

// Arr returns one sub-validator per element of an array-of-objects field.
// Errors use paths like "lines.0.qty". Absent: error when required, else nil.
func (v *V) Arr(key string, required bool) []*V {
	raw, has := v.data[key]
	if !has || raw == nil {
		if required {
			v.typeErr(key, "array", has, raw)
		}
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		v.typeErr(key, "array", true, raw)
		return nil
	}
	out := make([]*V, 0, len(arr))
	for i, el := range arr {
		sub := &V{prefix: v.path(key) + "." + strconv.Itoa(i), sink: v.sink, lang: v.lang}
		if m, ok := el.(map[string]any); ok {
			sub.data = m
		} else {
			sub.data = map[string]any{}
			v.sink.fields[sub.prefix] = fmt.Sprintf("Invalid input: expected object, received %s", typeName(el))
		}
		out = append(out, sub)
	}
	return out
}

// StrArr returns an array of strings.
func (v *V) StrArr(key string, required bool) []string {
	raw, has := v.data[key]
	if !has || raw == nil {
		if required {
			v.typeErr(key, "array", has, raw)
		}
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		v.typeErr(key, "array", true, raw)
		return nil
	}
	out := make([]string, 0, len(arr))
	for i, el := range arr {
		s, ok := el.(string)
		if !ok {
			v.Fail(key+"."+strconv.Itoa(i), fmt.Sprintf("Invalid input: expected string, received %s", typeName(el)))
			continue
		}
		out = append(out, s)
	}
	return out
}
