// Package i18n holds the server-side message catalogs (en, am, rw) and language resolution.
//
// Core catalogs live in en.json / am.json / rw.json. Each module may add its own keys in
// modules/<module>.<lang>.json (e.g. modules/sales.am.json) so agents never edit the same file.
// All catalogs must contain identical key sets (enforced by a test).
package i18n

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync/atomic"
)

type Lang string

const (
	EN Lang = "en"
	AM Lang = "am"
	RW Lang = "rw"

	Default = EN
)

// All is the list of supported languages.
var All = []Lang{EN, AM, RW}

//go:embed en.json am.json rw.json modules/*.json
var files embed.FS

var catalogs = map[Lang]map[string]string{}

// overrides are the super-admin edited strings (key -> value) per language; replaced atomically.
var overrides atomic.Pointer[map[Lang]map[string]string]

func init() {
	for _, l := range All {
		catalogs[l] = map[string]string{}
		load(string(l)+".json", l)
	}
	entries, _ := fs.ReadDir(files, "modules")
	for _, e := range entries {
		name := e.Name() // <module>.<lang>.json
		parts := strings.Split(strings.TrimSuffix(name, ".json"), ".")
		if len(parts) < 2 {
			panic("i18n: bad module catalog name " + name)
		}
		l := Lang(parts[len(parts)-1])
		if _, ok := catalogs[l]; !ok {
			panic("i18n: unknown language in " + name)
		}
		load("modules/"+name, l)
	}
}

func load(path string, l Lang) {
	b, err := files.ReadFile(path)
	if err != nil {
		panic(err)
	}
	m := map[string]string{}
	if err := json.Unmarshal(b, &m); err != nil {
		panic(fmt.Sprintf("i18n: %s: %v", path, err))
	}
	for k, v := range m {
		catalogs[l][k] = v
	}
}

// Valid reports whether s is a supported language code.
func Valid(s string) bool { _, ok := catalogs[Lang(s)]; return ok }

// Normalize maps "am", "AM", "am-ET", "rw-RW" to a supported Lang.
func Normalize(s string) (Lang, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexAny(s, "-_"); i > 0 {
		s = s[:i]
	}
	if s == "rn" || s == "kin" {
		s = "rw"
	}
	if s == "amh" {
		s = "am"
	}
	l := Lang(s)
	_, ok := catalogs[l]
	return l, ok
}

// SetOverrides installs super-admin overrides (lang -> key -> value).
func SetOverrides(m map[Lang]map[string]string) { overrides.Store(&m) }

// Lookup returns the message for (lang,key) with override > language > English fallback.
func Lookup(lang Lang, key string) (string, bool) {
	if o := overrides.Load(); o != nil {
		if v, ok := (*o)[lang][key]; ok && v != "" {
			return v, true
		}
	}
	if v, ok := catalogs[lang][key]; ok {
		return v, true
	}
	v, ok := catalogs[EN][key]
	return v, ok
}

// Has reports whether key exists in the English catalog.
func Has(key string) bool { _, ok := catalogs[EN][key]; return ok }

// T translates key; args are name,value pairs replacing {name} placeholders.
// An unknown key is returned unchanged (so literal English strings pass through).
func T(lang Lang, key string, args ...any) string {
	s, ok := Lookup(lang, key)
	if !ok {
		s = key
	}
	for i := 0; i+1 < len(args); i += 2 {
		s = strings.ReplaceAll(s, "{"+fmt.Sprint(args[i])+"}", fmt.Sprint(args[i+1]))
	}
	return s
}

// Static returns a copy of the built-in (non-override) catalog for lang.
func Static(lang Lang) map[string]string {
	out := make(map[string]string, len(catalogs[lang]))
	for k, v := range catalogs[lang] {
		out[k] = v
	}
	return out
}

// Keys returns the sorted keys of a language catalog.
func Keys(lang Lang) []string {
	out := make([]string, 0, len(catalogs[lang]))
	for k := range catalogs[lang] {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ── request language ─────────────────────────────────────────────────────────

type ctxKey struct{}

// WithLang stores the resolved language in ctx.
func WithLang(ctx context.Context, l Lang) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// FromContext returns the request language (Default when unset).
func FromContext(ctx context.Context) Lang {
	if l, ok := ctx.Value(ctxKey{}).(Lang); ok {
		return l
	}
	return Default
}

// Resolve applies the precedence ?lang -> user preference -> Accept-Language -> defaultLang.
func Resolve(query, user, acceptLanguage string, defaultLang Lang) Lang {
	if l, ok := Normalize(query); ok && query != "" {
		return l
	}
	if l, ok := Normalize(user); ok && user != "" {
		return l
	}
	for _, part := range strings.Split(acceptLanguage, ",") {
		tag, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		if l, ok := Normalize(tag); ok && tag != "" {
			return l
		}
	}
	if defaultLang == "" {
		return Default
	}
	return defaultLang
}
