package reports_test

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"strings"
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/cutoff"
	"btn-ops/internal/i18n"
	"btn-ops/internal/reports"
	"btn-ops/internal/testutil"
)

func plantID(t *testing.T, e *apptest.Env, code string) string {
	var id string
	if err := e.Pool.QueryRow(context.Background(), `SELECT id FROM "Plant" WHERE code = $1`, code).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func query(params []string, plant string) string {
	v := url.Values{}
	for _, p := range params {
		switch p {
		case "range":
			v.Set("from", "2026-11-01")
			v.Set("to", "2026-11-23")
		case "date":
			v.Set("date", "2026-11-23")
		case "plantRequired":
			v.Set("plantId", plant)
		}
	}
	return v.Encode()
}

// RPT-03: every report of the catalog answers as PDF and as CSV / Excel / JSON for a user who may see it.
func TestEveryCatalogReportServesPDFAndExports(t *testing.T) {
	e := apptest.New(t)
	su := e.User(auth.RoleSuperAdmin)
	p1 := plantID(t, e, "BTN-1")
	for _, x := range reports.Catalog() {
		q := query(x.Params, p1)
		r := e.Do("GET", "/api"+x.Path+"?"+q, nil, su)
		b, _ := io.ReadAll(r.Body)
		if r.StatusCode != 200 || !strings.HasPrefix(string(b), "%PDF") {
			t.Errorf("%s PDF %s: %d %.200s", x.Key, x.Path, r.StatusCode, b)
		}
		for _, f := range []string{"csv", "xlsx", "json"} {
			r := e.Do("GET", "/api"+x.ExportPath+"?"+q+"&format="+f, nil, su)
			b, _ := io.ReadAll(r.Body)
			if r.StatusCode != 200 || len(b) == 0 {
				t.Errorf("%s %s %s: %d %.200s", x.Key, f, x.ExportPath, r.StatusCode, b)
			}
		}
	}
}

func TestHubReportsPermissionsAndScope(t *testing.T) {
	e := apptest.New(t)
	gm := e.User(auth.RoleGeneralManager)
	for _, k := range reports.HubKeys() {
		r := e.Do("GET", "/api/reports/data/"+k+"?from=2026-11-01&to=2026-11-23", nil, gm)
		var d struct {
			Key, Title string
			Sections   []struct {
				Key     string
				Columns []map[string]any
				Rows    [][]any
			}
			Cutoff struct{ CutoffLabel, Note string }
		}
		e.Decode(r, &d)
		if r.StatusCode != 200 || d.Key != k || len(d.Sections) == 0 || d.Cutoff.CutoffLabel == "" || d.Cutoff.Note == "" {
			t.Fatalf("%s: %d %+v", k, r.StatusCode, d)
		}
	}
	if r := e.Do("GET", "/api/reports/data/nope", nil, gm); r.StatusCode != 404 {
		t.Fatalf("unknown %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/reports/data/budget-monitoring", nil, nil); r.StatusCode != 401 {
		t.Fatalf("anon %d", r.StatusCode)
	}
	op := e.User(auth.RoleOperator)
	for _, k := range []string{"budget-monitoring", "procurement-expense", "orders-summary", "site-performance"} {
		if r := e.Do("GET", "/api/reports/data/"+k, nil, op); r.StatusCode != 403 {
			t.Fatalf("operator %s %d", k, r.StatusCode)
		}
		if r := e.Do("GET", "/api/reports/pdf/"+k, nil, op); r.StatusCode != 403 {
			t.Fatalf("operator pdf %s %d", k, r.StatusCode)
		}
		if r := e.Do("GET", "/api/reports/data/"+k+"?format=xlsx", nil, op); r.StatusCode != 403 {
			t.Fatalf("operator export %s %d", k, r.StatusCode)
		}
	}
	if r := e.Do("GET", "/api/reports/data/headcount?from=bad", nil, gm); r.StatusCode != 400 {
		t.Fatalf("validation %d", r.StatusCode)
	}
	// a supervisor is pinned to their site whatever plantId says
	sup := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-2"})
	var d struct{ Plant *struct{ Code string } }
	e.Decode(e.Do("GET", "/api/reports/data/headcount?plantId="+plantID(t, e, "BTN-1"), nil, sup), &d)
	if d.Plant == nil || d.Plant.Code != "BTN-2" {
		t.Fatalf("scope %+v", d.Plant)
	}
	// generic PDF only for registered export reports, and the inner permission applies
	if r := e.Do("GET", "/api/reports/pdf/export/not/registered", nil, gm); r.StatusCode != 404 {
		t.Fatalf("unregistered %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/reports/pdf/export/audit", nil, op); r.StatusCode != 403 {
		t.Fatalf("generic pdf permission %d", r.StatusCode)
	}
	r := e.Do("GET", "/api/reports/pdf/export/audit?from=2026-11-01&to=2026-11-23", nil, e.User(auth.RoleSystemAdmin))
	if b, _ := io.ReadAll(r.Body); r.StatusCode != 200 || !strings.HasPrefix(string(b), "%PDF") {
		t.Fatalf("audit pdf %d", r.StatusCode)
	}
}

// RPT-06: cut-off + unsynchronised sites on screens (endpoint) and exports (filters block, via the hook).
func TestCutoffStatement(t *testing.T) {
	e := apptest.New(t)
	gm := e.User(auth.RoleGeneralManager)
	cutoff.Invalidate()
	var none cutoff.Info
	e.Decode(e.Do("GET", "/api/reports/cutoff", nil, gm), &none)
	if none.PendingSites != 0 || none.CutoffLabel == "" || !strings.Contains(none.Note, "No device") {
		t.Fatalf("none %+v", none)
	}
	sup := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-3"})
	exec(t, e, `INSERT INTO device (device_id, user_id, name, device_key, offline_salt, pending_count, pending_at)
		VALUES (gen_random_uuid(), $1, 'tablet', '\x00', '\x00', 4, now())`, sup.ID)
	cutoff.Invalidate()
	var info cutoff.Info
	e.Decode(e.Do("GET", "/api/reports/cutoff", nil, gm), &info)
	if info.PendingSites != 1 || info.PendingRecords != 4 || info.Sites[0].Code != "BTN-3" || !strings.Contains(info.UnsynchronisedNote, "BTN-3") {
		t.Fatalf("pending %+v", info)
	}
	// another site's filter does not count BTN-3
	var other cutoff.Info
	e.Decode(e.Do("GET", "/api/reports/cutoff?plantId="+plantID(t, e, "BTN-1"), nil, gm), &other)
	if other.PendingSites != 0 {
		t.Fatalf("filtered %+v", other)
	}
	// every export carries the statement in its filters block
	r := e.Do("GET", "/api/production/summary?from=2026-11-01&to=2026-11-23&format=json", nil, gm)
	var doc struct {
		Filters []struct{ Label, Value string }
	}
	b, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(b, &doc)
	labels := map[string]string{}
	for _, f := range doc.Filters {
		labels[f.Label] = f.Value
	}
	if labels[i18n.T(i18n.EN, "cutoff.label")] == "" || !strings.Contains(labels[i18n.T(i18n.EN, "cutoff.unsynced_label")], "BTN-3") {
		t.Fatalf("export filters %+v", doc.Filters)
	}
	// and PDFs embed the meta rows (uncompressed text is not searchable; the hook is checked directly)
	if rows := cutoff.MetaRows(cutoff.Localize(info, i18n.FR), i18n.FR); len(rows) != 2 || rows[0].Label != "Date d'arrêt des données" {
		t.Fatalf("pdf meta %+v", rows)
	}
}
