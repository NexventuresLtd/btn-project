package httpx

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"btn-ops/internal/i18n"
)

func parse(t *testing.T, js string) *V {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(js))
	dec.UseNumber()
	m := map[string]any{}
	if err := dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return NewV(m, i18n.EN)
}

func TestStringRulesMatchZodMessages(t *testing.T) {
	v := parse(t, `{"email":"nope","name":" A ","n":12,"nul":null}`)
	v.Str("email").Trim().Lower().Email("v.email")
	v.Str("name").Trim().Min(2, "v.name").Max(120)
	v.Str("missing")
	v.Str("n")
	v.Str("nul")
	v.NullStr("nul")
	f := v.Fields()
	want := map[string]string{
		"email":   "Enter a valid email",
		"name":    "Enter the full name",
		"missing": "Invalid input: expected string, received undefined",
		"n":       "Invalid input: expected string, received number",
	}
	for k, w := range want {
		if f[k] != w {
			t.Errorf("%s: %q != %q", k, f[k], w)
		}
	}
	if f["nul"] != "Invalid input: expected string, received null" {
		t.Errorf("nul: %q", f["nul"])
	}
	err := v.Err().(*Error)
	if err.Status != 400 || err.Message != "Please correct the highlighted fields" {
		t.Errorf("envelope %+v", err)
	}
}

func TestFirstMessagePerFieldWins(t *testing.T) {
	v := parse(t, `{"pw":"abc"}`)
	v.Str("pw").Min(10, "short").Regex(regexp.MustCompile(`[A-Z]`), "upper")
	if v.Fields()["pw"] != "short" {
		t.Fatalf("%v", v.Fields())
	}
}

func TestNumbersAndCoercion(t *testing.T) {
	v := parse(t, `{"a":"1750","b":1.5,"c":"abc","d":-1,"e":3,"s":"7"}`)
	if v.Num("a").Coerce().Whole("v.whole_francs").Min(0).I() != 1750 {
		t.Error("coerce string")
	}
	v.Num("b").Whole("Whole francs only")
	v.Num("c").Coerce()
	v.Num("d").Min(0)
	v.Num("s") // strict: string is not a number
	if got := v.Num("e").Max(5).I(); got != 3 {
		t.Error(got)
	}
	f := v.Fields()
	if f["b"] != "Whole francs only" || f["c"] != "Invalid input: expected number, received NaN" || f["d"] != "Too small: expected number to be >=0" || f["s"] != "Invalid input: expected number, received string" {
		t.Errorf("%v", f)
	}
	if _, bad := f["a"]; bad {
		t.Error("a should be valid")
	}
}

func TestNestedAndArrayPaths(t *testing.T) {
	v := parse(t, `{"company":{"name":""},"lines":[{"qty":1},{"qty":"x"}]}`)
	v.Obj("company", false).Str("name").Min(1)
	for _, l := range v.Arr("lines", true) {
		l.Num("qty")
	}
	v.Arr("none", true)
	f := v.Fields()
	if f["company.name"] == "" || f["lines.1.qty"] == "" || f["none"] == "" {
		t.Errorf("%v", f)
	}
	if _, ok := f["lines.0.qty"]; ok {
		t.Error("lines.0.qty valid")
	}
}

func TestOptionalDefaultsAndPtrs(t *testing.T) {
	v := parse(t, `{"phone":null,"role":"OPERATOR"}`)
	if v.NullStr("phone").Ptr() != nil || v.OptStr("absent").Present() {
		t.Error("nil expected")
	}
	if !v.OptBool("isActive").Default(true).Get() {
		t.Error("default true")
	}
	if v.Str("role").OneOf([]string{"OPERATOR", "SUPERVISOR"}).Get() != "OPERATOR" || v.Failed() {
		t.Error("oneof")
	}
	if v.Str("role").OneOf([]string{"X"}); !strings.HasPrefix(v.Fields()["role"], `Invalid option: expected one of "X"`) {
		t.Errorf("%v", v.Fields())
	}
}

func TestTranslatedMessages(t *testing.T) {
	v := NewV(map[string]any{"email": "x"}, i18n.RW)
	v.Str("email").Email("v.email")
	if v.Fields()["email"] != "Andika imeri nyayo" {
		t.Errorf("%v", v.Fields())
	}
}

func TestDateRule(t *testing.T) {
	v := parse(t, `{"d":"2026-02-30","e":"2026-11-14"}`)
	v.Str("d").Date("bad")
	v.Str("e").Date("bad")
	if v.Fields()["d"] != "bad" || v.Fields()["e"] != "" {
		t.Errorf("%v", v.Fields())
	}
}

func TestEtagMatch(t *testing.T) {
	if !etagMatch(`W/"abc"`, `W/"abc"`) || !etagMatch(`"abc", W/"x"`, `W/"abc"`) || etagMatch(`"zzz"`, `W/"abc"`) || etagMatch("", `W/"abc"`) || !etagMatch("*", `W/"abc"`) {
		t.Fatal("etag matching")
	}
}

func TestRedactQuery(t *testing.T) {
	if got := RedactQuery("a=1&code=123456&token=zz"); strings.Contains(got, "123456") || strings.Contains(got, "zz") || !strings.Contains(got, "a=1") {
		t.Fatal(got)
	}
}
