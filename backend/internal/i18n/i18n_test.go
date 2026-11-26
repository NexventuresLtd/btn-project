package i18n

import (
	"regexp"
	"sort"
	"testing"
)

func TestCatalogsHaveIdenticalKeys(t *testing.T) {
	en := Keys(EN)
	if len(All) != 4 {
		t.Fatalf("expected en, am, rw, fr; got %v", All)
	}
	for _, l := range []Lang{AM, RW, FR} {
		got := Keys(l)
		if len(got) != len(en) {
			t.Errorf("%s has %d keys, en has %d", l, len(got), len(en))
		}
		missing, extra := diff(en, got)
		if len(missing) > 0 {
			t.Errorf("%s is missing keys: %v", l, missing)
		}
		if len(extra) > 0 {
			t.Errorf("%s has extra keys: %v", l, extra)
		}
	}
}

func diff(want, got []string) (missing, extra []string) {
	w, g := map[string]bool{}, map[string]bool{}
	for _, k := range want {
		w[k] = true
	}
	for _, k := range got {
		g[k] = true
	}
	for k := range w {
		if !g[k] {
			missing = append(missing, k)
		}
	}
	for k := range g {
		if !w[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return
}

var placeholder = regexp.MustCompile(`\{[a-zA-Z]+\}`)

// Every translation must use exactly the same {placeholders} as English, and no value may be empty.
func TestPlaceholdersMatchAndNoEmpty(t *testing.T) {
	for _, k := range Keys(EN) {
		en := Static(EN)[k]
		want := placeholder.FindAllString(en, -1)
		sort.Strings(want)
		for _, l := range []Lang{AM, RW, FR} {
			v := Static(l)[k]
			if v == "" {
				t.Errorf("%s/%s empty", l, k)
			}
			got := placeholder.FindAllString(v, -1)
			sort.Strings(got)
			if len(got) != len(want) {
				t.Errorf("%s/%s placeholders %v != %v", l, k, got, want)
				continue
			}
			for i := range got {
				if got[i] != want[i] {
					t.Errorf("%s/%s placeholders %v != %v", l, k, got, want)
				}
			}
		}
	}
}

func TestAmharicUsesEthiopicScript(t *testing.T) {
	eth := regexp.MustCompile(`[\x{1200}-\x{137F}]`)
	for k, v := range Static(AM) {
		if k == "passkey.default_name" || k == "role.SUPER_ADMIN" {
			continue
		}
		if !eth.MatchString(v) {
			t.Errorf("am/%s has no Ethiopic characters: %q", k, v)
		}
	}
}

func TestTAndFallback(t *testing.T) {
	if got := T(RW, "auth.resend_wait", "seconds", 7); got != "Tegereza amasegonda 7 mbere yo gusaba indi kode." {
		t.Fatal(got)
	}
	if T(EN, "not.a.key") != "not.a.key" {
		t.Fatal("unknown key should pass through")
	}
	SetOverrides(map[Lang]map[string]string{AM: {"v.required": "ግዴታ"}})
	defer SetOverrides(nil)
	if T(AM, "v.required") != "ግዴታ" {
		t.Fatal("override not applied")
	}
	if T(RW, "v.required") != "Birakenewe" {
		t.Fatal("override leaked across languages")
	}
}

func TestResolve(t *testing.T) {
	cases := []struct {
		q, u, a string
		want    Lang
	}{
		{"am", "rw", "en", AM},
		{"", "rw", "am", RW},
		{"", "", "de;q=0.9, rw-RW;q=0.8, en;q=0.7", RW},
		{"", "", "fr-FR,fr;q=0.9,en;q=0.8", FR},
		{"", "", "fr-RW", FR},
		{"FR", "", "", FR},
		{"", "fr", "rw", FR},
		{"xx", "", "", EN},
		{"", "", "am-ET,am;q=0.9", AM},
	}
	for _, c := range cases {
		if got := Resolve(c.q, c.u, c.a, EN); got != c.want {
			t.Errorf("%+v => %s", c, got)
		}
	}
}

// French must really be French: most values differ from English (brand names, codes and units may match).
func TestFrenchIsTranslated(t *testing.T) {
	en, fr := Static(EN), Static(FR)
	same := 0
	for k, v := range en {
		if fr[k] == v {
			same++
		}
	}
	if share := float64(same) / float64(len(en)); share > 0.12 {
		t.Errorf("%.0f%% of French strings equal English (%d of %d)", share*100, same, len(en))
	}
	if T(FR, "v.required") == T(EN, "v.required") {
		t.Error("v.required not translated")
	}
}
