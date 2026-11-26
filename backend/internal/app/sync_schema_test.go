package app_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/syncsvc"
)

// §9.2: every offline record type the web client registers (frontend/src/modules/*/offline.ts) is accepted by the
// server at the schemaVersion the client sends — otherwise its records would be refused with schema_too_new.
func TestClientRecordSchemasSupportedByServer(t *testing.T) {
	apptest.New(t) // registers every module's sync handlers
	files, _ := filepath.Glob("../../../frontend/src/modules/*/offline.ts")
	if len(files) == 0 {
		t.Skip("frontend sources not available")
	}
	constRe := regexp.MustCompile(`(?m)^export const (\w+)\s*=\s*"([a-z0-9_]+)"`)
	regRe := regexp.MustCompile(`type:\s*("([a-z0-9_]+)"|(\w+)),\s*\n\s*schemaVersion:\s*(\d+)`)
	checked := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		consts := map[string]string{}
		for _, m := range constRe.FindAllStringSubmatch(string(b), -1) {
			consts[m[1]] = m[2]
		}
		for _, m := range regRe.FindAllStringSubmatch(string(b), -1) {
			typ := m[2]
			if typ == "" {
				typ = consts[m[3]]
			}
			if typ == "" {
				t.Errorf("%s: cannot resolve record type %s", f, m[1])
				continue
			}
			var v int
			for _, c := range m[4] {
				v = v*10 + int(c-'0')
			}
			lo, hi, ok := syncsvc.RecordSchemaRange(typ)
			if !ok {
				t.Errorf("%s: record type %q has no server handler", f, typ)
				continue
			}
			if v < lo || v > hi {
				t.Errorf("%s: client sends %q schemaVersion %d, server accepts %d..%d", f, typ, v, lo, hi)
			}
			checked++
		}
	}
	if checked < 10 {
		t.Fatalf("only %d client record types found; parser out of date?", checked)
	}
}
