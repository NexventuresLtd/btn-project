package app_test

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/testutil"
)

func TestCSRFGuard(t *testing.T) {
	e := apptest.New(t)
	u := e.User(auth.RoleGeneralManager)
	// missing header
	req := httptest.NewRequest("PUT", "/api/auth/language", strings.NewReader(`{"language":"en"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "btn_session", Value: u.Token})
	resp, _ := e.App.Test(req, -1)
	if resp.StatusCode != 403 {
		t.Fatalf("missing X-Requested-With must be 403, got %d", resp.StatusCode)
	}
	if _, msg, _ := e.ErrorOf(resp); msg != "Missing request header" {
		t.Fatalf("msg %q", msg)
	}
	// foreign origin
	r := e.Do("PUT", "/api/auth/language", map[string]any{"language": "en"}, u, "Origin", "https://evil.example")
	if _, msg, _ := e.ErrorOf(r); r.StatusCode != 403 || msg != "Request origin not allowed" {
		t.Fatalf("foreign origin: %d %q", r.StatusCode, msg)
	}
	// configured frontend origin and same-host origin are accepted
	if r := e.Do("PUT", "/api/auth/language", map[string]any{"language": "en"}, u, "Origin", "http://localhost:5173"); r.StatusCode != 200 {
		t.Fatalf("frontend origin: %d", r.StatusCode)
	}
	if r := e.Do("PUT", "/api/auth/language", map[string]any{"language": "en"}, u, "Origin", "http://example.com"); r.StatusCode != 200 {
		t.Fatalf("same-host origin: %d", r.StatusCode)
	}
	// GET never needs the header
	req = httptest.NewRequest("GET", "/api/health", nil)
	resp, _ = e.App.Test(req, -1)
	if resp.StatusCode != 200 {
		t.Fatalf("health: %d", resp.StatusCode)
	}
}

func TestErrorEnvelopeAndNotFound(t *testing.T) {
	e := apptest.New(t)
	u := e.User(auth.RoleGeneralManager)
	r := e.Do("GET", "/api/nope", nil, u)
	code, msg, _ := e.ErrorOf(r)
	if r.StatusCode != 404 || code != "not_found" || msg != "Endpoint not found" {
		t.Fatalf("404: %d %s %s", r.StatusCode, code, msg)
	}
	r = e.Do("GET", "/api/lookups", nil, nil)
	code, msg, _ = e.ErrorOf(r)
	if r.StatusCode != 401 || code != "unauthorized" || msg != "Please sign in" {
		t.Fatalf("401: %d %s %s", r.StatusCode, code, msg)
	}
	r = e.Do("POST", "/api/auth/login", "{not json", nil)
	code, msg, _ = e.ErrorOf(r)
	if r.StatusCode != 400 || code != "bad_request" || msg != "Malformed JSON body" {
		t.Fatalf("malformed: %d %s %s", r.StatusCode, code, msg)
	}
	r = e.Do("GET", "/api/nonexistent/anything", nil, u)
	if code, _, _ := e.ErrorOf(r); r.StatusCode != 404 || code != "not_found" {
		t.Fatalf("unknown route: %d %s", r.StatusCode, code)
	}
}

func TestSuperAdminRules(t *testing.T) {
	e := apptest.New(t)
	super := e.User(auth.RoleSuperAdmin, testutil.UserOpts{Email: "root@x.test"})
	gm := e.User(auth.RoleGeneralManager)
	sysadmin := e.User(auth.RoleSystemAdmin)

	// only a super admin may create a super admin
	body := map[string]any{"email": "boss@x.test", "name": "Second Boss", "role": "SUPER_ADMIN"}
	if r := e.Do("POST", "/api/users", body, gm); r.StatusCode != 403 {
		t.Fatalf("GM creating super admin: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/users", body, sysadmin); r.StatusCode != 403 {
		t.Fatalf("sysadmin creating super admin: %d", r.StatusCode)
	}
	r := e.Do("POST", "/api/users", body, super)
	m := e.Map(r)
	if r.StatusCode != 201 || m["emailed"] != true || m["tempPassword"] != nil {
		t.Fatalf("super creates super: %d %v", r.StatusCode, m)
	}
	boss := m["user"].(map[string]any)
	if len(e.Mail.Welcomes) != 1 {
		t.Fatal("welcome email not sent")
	}

	// normal user creation by GM; plant-scoped roles need a plant
	r = e.Do("POST", "/api/users", map[string]any{"email": "sup@x.test", "name": "Sup", "role": "SUPERVISOR"}, gm)
	_, _, f := e.ErrorOf(r)
	if r.StatusCode != 400 || f["plantId"] != "Supervisors must be assigned to a plant" {
		t.Fatalf("plant rule: %d %v", r.StatusCode, f)
	}
	// non-super cannot modify, reset, unlock or revoke a super admin
	id := boss["id"].(string)
	for _, c := range []struct{ method, path string }{{"PATCH", "/api/users/" + id}, {"POST", "/api/users/" + id + "/reset-password"}, {"POST", "/api/users/" + id + "/unlock"}, {"POST", "/api/users/" + id + "/revoke-sessions"}} {
		if r := e.Do(c.method, c.path, map[string]any{"name": "Hacked"}, gm); r.StatusCode != 403 {
			t.Fatalf("%s %s by GM: %d", c.method, c.path, r.StatusCode)
		}
	}
	// nobody can promote to super admin except a super admin
	if r := e.Do("PATCH", "/api/users/"+gm.ID, map[string]any{"role": "SUPER_ADMIN"}, sysadmin); r.StatusCode != 403 {
		t.Fatalf("promotion by sysadmin: %d", r.StatusCode)
	}

	// nobody can modify a super admin except a super admin
	if r := e.Do("PATCH", "/api/users/"+id, map[string]any{"role": "GENERAL_MANAGER"}, gm); r.StatusCode != 403 {
		t.Fatalf("GM demoting a super: %d", r.StatusCode)
	}
}

// Two super admins deactivating each other at the same moment must never leave zero active super admins.
func TestNeverZeroActiveSuperAdmins(t *testing.T) {
	e := apptest.New(t)
	a := e.User(auth.RoleSuperAdmin)
	b := e.User(auth.RoleSuperAdmin)
	codes := make(chan int, 2)
	go func() { codes <- e.Do("PATCH", "/api/users/"+b.ID, map[string]any{"isActive": false}, a).StatusCode }()
	go func() { codes <- e.Do("PATCH", "/api/users/"+a.ID, map[string]any{"isActive": false}, b).StatusCode }()
	c1, c2 := <-codes, <-codes
	var n int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "User" WHERE role='SUPER_ADMIN' AND "isActive"`).Scan(&n)
	if n != 1 {
		t.Fatalf("exactly one super admin must remain active, got %d (responses %d, %d)", n, c1, c2)
	}
	if !(c1 == 200 && (c2 == 409 || c2 == 401)) && !(c2 == 200 && (c1 == 409 || c1 == 401)) {
		t.Fatalf("unexpected responses %d, %d", c1, c2)
	}
	// and the survivor cannot deactivate or demote itself
	survivor := a
	if err := e.Pool.QueryRow(t.Context(), `SELECT 1 FROM "User" WHERE id=$1 AND "isActive"`, b.ID).Scan(new(int)); err == nil {
		survivor = b
	}
	if r := e.Do("PATCH", "/api/users/"+survivor.ID, map[string]any{"isActive": false}, survivor); r.StatusCode != 400 {
		t.Fatalf("self deactivation: %d", r.StatusCode)
	}
}

func TestBrandingWritesAreSuperAdminOnly(t *testing.T) {
	e := apptest.New(t)
	super := e.User(auth.RoleSuperAdmin)
	for _, role := range []auth.Role{auth.RoleSystemAdmin, auth.RoleManagingDirector, auth.RoleGeneralManager, auth.RoleOperator} {
		u := e.User(role)
		if r := e.Do("PUT", "/api/admin/branding", map[string]any{"name": "X"}, u); r.StatusCode != 403 {
			t.Fatalf("%s PUT branding: %d", role, r.StatusCode)
		}
		if r := e.Do("PUT", "/api/admin/i18n", map[string]any{"lang": "en", "key": "a.b", "value": "x"}, u); r.StatusCode != 403 {
			t.Fatalf("%s PUT i18n: %d", role, r.StatusCode)
		}
		if r := e.Do("DELETE", "/api/admin/branding/assets/light", nil, u); r.StatusCode != 403 {
			t.Fatalf("%s DELETE asset: %d", role, r.StatusCode)
		}
	}
	if r := e.Do("PUT", "/api/admin/branding", map[string]any{"name": "X"}, nil); r.StatusCode != 401 {
		t.Fatalf("anonymous: %d", r.StatusCode)
	}

	// public branding: defaults, ETag, 304
	r := e.Do("GET", "/api/public/branding", nil, nil)
	etag := r.Header.Get("ETag")
	pub := e.Map(r)
	if r.StatusCode != 200 || etag == "" || pub["primaryColor"] != "#0C234B" || pub["baseFontSize"].(float64) != 16 || pub["logoLightUrl"] == "" || pub["defaultLanguage"] != "en" {
		t.Fatalf("public branding: %d %v", r.StatusCode, pub)
	}
	if r := e.Do("GET", "/api/public/branding", nil, nil, "If-None-Match", etag); r.StatusCode != 304 {
		t.Fatalf("expected 304, got %d", r.StatusCode)
	}

	// validation
	r = e.Do("PUT", "/api/admin/branding", map[string]any{"primaryColor": "red", "baseFontSize": 30, "fontFamily": "x;}body{display:none"}, super)
	_, _, f := e.ErrorOf(r)
	if r.StatusCode != 400 || f["primaryColor"] == "" || f["baseFontSize"] == "" || f["fontFamily"] == "" {
		t.Fatalf("validation: %d %v", r.StatusCode, f)
	}
	// write
	r = e.Do("PUT", "/api/admin/branding", map[string]any{"name": "ACME Blocks", "primaryColor": "#112233", "baseFontSize": 15,
		"loginHeadline": map[string]any{"am": "እንኳን ደህና መጡ"}}, super)
	pub = e.Map(r)
	if r.StatusCode != 200 || pub["name"] != "ACME Blocks" || pub["primaryColor"] != "#112233" || pub["loginHeadline"].(map[string]any)["am"] != "እንኳን ደህና መጡ" || pub["loginHeadline"].(map[string]any)["en"] == "" {
		t.Fatalf("update: %d %v", r.StatusCode, pub)
	}
	r = e.Do("GET", "/api/public/branding", nil, nil, "If-None-Match", etag)
	if r.StatusCode != 200 || r.Header.Get("ETag") == etag {
		t.Fatalf("ETag must change after update: %d", r.StatusCode)
	}
}

func upload(e *apptest.Env, u *testutil.TestUser, kind, filename string, data []byte) *http.Response {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("kind", kind)
	fw, _ := w.CreateFormFile("file", filename)
	fw.Write(data)
	w.Close()
	req := httptest.NewRequest("POST", "/api/admin/branding/assets", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("X-Requested-With", "btn-ops")
	req.AddCookie(&http.Cookie{Name: "btn_session", Value: u.Token})
	resp, err := e.App.Test(req, -1)
	if err != nil {
		e.T.Fatal(err)
	}
	return resp
}

var tinyPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xf8\xff\xff?\x00\x05\xfe\x02\xfe\xa7\x9a\xa0\xa0\x00\x00\x00\x00IEND\xaeB`\x82")

func TestBrandingAssetUpload(t *testing.T) {
	e := apptest.New(t)
	super := e.User(auth.RoleSuperAdmin)
	gm := e.User(auth.RoleGeneralManager)
	if r := upload(e, gm, "light", "logo.png", tinyPNG); r.StatusCode != 403 {
		t.Fatalf("non-super upload: %d", r.StatusCode)
	}
	// a text file renamed .png is rejected by magic bytes
	r := upload(e, super, "light", "evil.png", []byte("hello world, not an image"))
	_, _, f := e.ErrorOf(r)
	if r.StatusCode != 400 || f["file"] == "" {
		t.Fatalf("magic bytes: %d %v", r.StatusCode, f)
	}
	// svg with script is rejected, clean svg accepted
	bad := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	if r := upload(e, super, "light", "x.svg", bad); r.StatusCode != 400 {
		t.Fatalf("svg script: %d", r.StatusCode)
	}
	bad2 := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect onload="x()" width="1" height="1"/></svg>`)
	if r := upload(e, super, "light", "x.svg", bad2); r.StatusCode != 400 {
		t.Fatalf("svg handler: %d", r.StatusCode)
	}
	// bad kind
	if r := upload(e, super, "banner", "x.png", tinyPNG); r.StatusCode != 400 {
		t.Fatalf("bad kind: %d", r.StatusCode)
	}
	// too large
	big := append(append([]byte{}, tinyPNG...), bytes.Repeat([]byte{0}, 1<<20)...)
	if r := upload(e, super, "light", "big.png", big); r.StatusCode != 413 {
		t.Fatalf("too large: %d", r.StatusCode)
	}
	// good png
	r = upload(e, super, "light", "logo.png", tinyPNG)
	pub := e.Map(r)
	url, _ := pub["logoLightUrl"].(string)
	if r.StatusCode != 200 || !strings.HasPrefix(url, "/api/public/assets/") || strings.HasSuffix(url, "default-light") {
		t.Fatalf("upload: %d %v", r.StatusCode, pub)
	}
	// served publicly with immutable caching + ETag + 304 + sniffed type
	ar := e.Do("GET", url, nil, nil)
	etag := ar.Header.Get("ETag")
	if ar.StatusCode != 200 || ar.Header.Get("Content-Type") != "image/png" || !strings.Contains(ar.Header.Get("Cache-Control"), "immutable") || etag == "" {
		t.Fatalf("asset: %d %v", ar.StatusCode, ar.Header)
	}
	if r := e.Do("GET", url, nil, nil, "If-None-Match", etag); r.StatusCode != 304 {
		t.Fatalf("asset 304: %d", r.StatusCode)
	}
	// clean svg accepted as favicon, then deleted -> default restored
	good := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><rect width="1" height="1" fill="#f59e0b"/></svg>`)
	if r := upload(e, super, "favicon", "f.svg", good); r.StatusCode != 200 {
		t.Fatalf("clean svg: %d", r.StatusCode)
	}
	r = e.Do("DELETE", "/api/admin/branding/assets/favicon", nil, super)
	pub = e.Map(r)
	if r.StatusCode != 200 || !strings.HasSuffix(pub["faviconUrl"].(string), "default-favicon") {
		t.Fatalf("delete: %d %v", r.StatusCode, pub)
	}
	// defaults are served
	if r := e.Do("GET", "/api/public/assets/default-dark", nil, nil); r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("default asset: %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/public/assets/does-not-exist", nil, nil); r.StatusCode != 404 {
		t.Fatalf("missing asset: %d", r.StatusCode)
	}
}

func TestI18nOverrides(t *testing.T) {
	e := apptest.New(t)
	super := e.User(auth.RoleSuperAdmin)
	r := e.Do("GET", "/api/public/i18n/am", nil, nil)
	m := e.Map(r)
	if r.StatusCode != 200 || len(m["messages"].(map[string]any)) != 0 {
		t.Fatalf("empty overrides: %d %v", r.StatusCode, m)
	}
	if r := e.Do("GET", "/api/public/i18n/fr", nil, nil); r.StatusCode != 404 {
		t.Fatalf("unknown lang: %d", r.StatusCode)
	}
	r = e.Do("PUT", "/api/admin/i18n", map[string]any{"items": []map[string]any{
		{"lang": "am", "key": "login.title", "value": "እንኳን ደህና መጡ"},
		{"lang": "rw", "key": "login.title", "value": "Murakaza neza"},
		{"lang": "am", "key": "v.required", "value": "ግዴታ"},
	}}, super)
	if r.StatusCode != 200 {
		t.Fatalf("put: %d %s", r.StatusCode, e.Text(r))
	}
	r = e.Do("GET", "/api/public/i18n/am", nil, nil)
	etag := r.Header.Get("ETag")
	m = e.Map(r)["messages"].(map[string]any)
	if m["login.title"] != "እንኳን ደህና መጡ" || len(m) != 2 {
		t.Fatalf("messages: %v", m)
	}
	if r := e.Do("GET", "/api/public/i18n/am", nil, nil, "If-None-Match", etag); r.StatusCode != 304 {
		t.Fatalf("304: %d", r.StatusCode)
	}
	// overrides also apply to server-side messages
	r = e.Do("POST", "/api/auth/login?lang=am", map[string]any{"email": "", "password": "x"}, nil)
	if _, _, f := e.ErrorOf(r); f["email"] == "" {
		t.Fatalf("fields: %v", f)
	}
	// empty value deletes
	r = e.Do("PUT", "/api/admin/i18n", map[string]any{"lang": "am", "key": "login.title", "value": ""}, super)
	if r.StatusCode != 200 {
		t.Fatalf("delete via empty: %d", r.StatusCode)
	}
	if m := e.Map(e.Do("GET", "/api/public/i18n/am", nil, nil))["messages"].(map[string]any); len(m) != 1 {
		t.Fatalf("after delete: %v", m)
	}
	if r := e.Do("PUT", "/api/admin/i18n", map[string]any{"lang": "xx", "key": "a", "value": "b"}, super); r.StatusCode != 400 {
		t.Fatalf("bad lang: %d", r.StatusCode)
	}
	if r := e.Do("PUT", "/api/admin/i18n", map[string]any{"lang": "am", "key": "bad key!", "value": "b"}, super); r.StatusCode != 400 {
		t.Fatalf("bad key: %d", r.StatusCode)
	}
}

func TestMasterdataLookupsSettingsAudit(t *testing.T) {
	e := apptest.New(t)
	gm := e.User(auth.RoleGeneralManager)
	sup := e.User(auth.RoleSupervisor) // BTN-1
	op := e.User(auth.RoleOperator)

	// lookups: shape, plant scoping, ETag/304, no SUPER_ADMIN role for non-supers
	r := e.Do("GET", "/api/lookups", nil, gm)
	etag := r.Header.Get("ETag")
	lk := e.Map(r)
	if r.StatusCode != 200 || len(lk["plants"].([]any)) != 3 || len(lk["products"].([]any)) != 11 || len(lk["materials"].([]any)) != 6 || len(lk["roles"].([]any)) != 10 {
		t.Fatalf("lookups: %d %v", r.StatusCode, lk)
	}
	if r := e.Do("GET", "/api/lookups", nil, gm, "If-None-Match", etag); r.StatusCode != 304 {
		t.Fatalf("lookups 304: %d", r.StatusCode)
	}
	if pl := e.Map(e.Do("GET", "/api/lookups", nil, sup))["plants"].([]any); len(pl) != 1 || pl[0].(map[string]any)["code"] != "BTN-1" {
		t.Fatalf("scoped plants: %v", pl)
	}
	if rl := e.Map(e.Do("GET", "/api/lookups", nil, e.User(auth.RoleSuperAdmin)))["roles"].([]any); len(rl) != 11 {
		t.Fatalf("super sees 11 roles: %d", len(rl))
	}

	// masterdata permissions: everyone reads, only masterdata.manage writes
	if r := e.Do("GET", "/api/masterdata/plants", nil, op); r.StatusCode != 200 {
		t.Fatalf("read plants: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/masterdata/products", map[string]any{"code": "x", "name": "X", "defaultPrice": 10}, op); r.StatusCode != 403 {
		t.Fatalf("operator write: %d", r.StatusCode)
	}
	// create product, normalisation + validation
	r = e.Do("POST", "/api/masterdata/products", map[string]any{"code": " tst1 ", "name": " Test Block ", "defaultPrice": "1750"}, gm)
	p := e.Map(r)
	if r.StatusCode != 201 || p["code"] != "TST1" || p["name"] != "Test Block" || p["unit"] != "Pcs" || p["defaultPrice"].(float64) != 1750 || p["isActive"] != true {
		t.Fatalf("create product: %d %v", r.StatusCode, p)
	}
	r = e.Do("POST", "/api/masterdata/products", map[string]any{"code": "", "name": "", "defaultPrice": 1.5}, gm)
	_, _, f := e.ErrorOf(r)
	if r.StatusCode != 400 || f["code"] != "Required" || f["name"] != "Required" || f["defaultPrice"] != "Whole francs only" {
		t.Fatalf("validation: %d %v", r.StatusCode, f)
	}
	// duplicate -> 409 with the Prisma-compatible message
	r = e.Do("POST", "/api/masterdata/products", map[string]any{"code": "TST1", "name": "Dup", "defaultPrice": 1}, gm)
	code, msg, _ := e.ErrorOf(r)
	if r.StatusCode != 409 || code != "conflict" || msg != "A record with the same code already exists" {
		t.Fatalf("dup: %d %s %s", r.StatusCode, code, msg)
	}
	// lookups cache invalidated by the write
	if n := len(e.Map(e.Do("GET", "/api/lookups", nil, gm))["products"].([]any)); n != 12 {
		t.Fatalf("lookups stale after write: %d products", n)
	}
	// patch + material decimal as string + customer normalisation
	r = e.Do("PATCH", "/api/masterdata/products/"+p["id"].(string), map[string]any{"defaultPrice": 1800}, gm)
	if p2 := e.Map(r); r.StatusCode != 200 || p2["defaultPrice"].(float64) != 1800 || p2["name"] != "Test Block" {
		t.Fatalf("patch: %d %v", r.StatusCode, p2)
	}
	if r := e.Do("PATCH", "/api/masterdata/products/nope", map[string]any{"name": "x"}, gm); r.StatusCode != 404 {
		t.Fatalf("patch 404: %d", r.StatusCode)
	}
	mats := e.Map(e.Do("GET", "/api/masterdata/materials", nil, gm))["items"].([]any)
	if mats[0].(map[string]any)["minLevel"] != "50" {
		t.Fatalf("minLevel should be a string like Prisma Decimal: %v", mats[0])
	}
	r = e.Do("POST", "/api/masterdata/customers", map[string]any{"name": "  acme   ltd ", "email": ""}, e.User(auth.RoleSalesManager))
	if c := e.Map(r); r.StatusCode != 201 || c["name"] != "ACME LTD" || c["email"] != nil {
		t.Fatalf("customer: %d %v", r.StatusCode, c)
	}

	// settings
	r = e.Do("GET", "/api/settings", nil, op)
	s := e.Map(r)
	if r.StatusCode != 200 || s["company"].(map[string]any)["legalName"] != "BTN Manufacturing Ltd" || s["approvals"].(map[string]any)["maxDiscountPercent"].(float64) != 10 {
		t.Fatalf("settings: %d %v", r.StatusCode, s)
	}
	if r := e.Do("PUT", "/api/settings", map[string]any{"company": map[string]any{"name": "X"}}, gm); r.StatusCode != 403 {
		t.Fatalf("GM cannot manage settings: %d", r.StatusCode)
	}
	md := e.User(auth.RoleManagingDirector)
	r = e.Do("PUT", "/api/settings", map[string]any{"company": map[string]any{"tin": "123456789"}, "approvals": map[string]any{"pettyCashApprovalLimit": "50000"}}, md)
	s = e.Map(r)
	if r.StatusCode != 200 || s["company"].(map[string]any)["tin"] != "123456789" || s["approvals"].(map[string]any)["pettyCashApprovalLimit"].(float64) != 50000 || s["company"].(map[string]any)["name"] != "BTN Manufacturing" {
		t.Fatalf("settings update: %d %v", r.StatusCode, s)
	}
	if s2 := e.Map(e.Do("GET", "/api/settings", nil, op)); s2["company"].(map[string]any)["tin"] != "123456789" {
		t.Fatalf("settings cache not invalidated: %v", s2)
	}
	r = e.Do("PUT", "/api/settings", map[string]any{"approvals": map[string]any{"maxDiscountPercent": 101}}, md)
	if _, _, f := e.ErrorOf(r); r.StatusCode != 400 || f["approvals.maxDiscountPercent"] == "" {
		t.Fatalf("settings validation: %d %v", r.StatusCode, f)
	}

	// audit: permission + content + pagination envelope
	if r := e.Do("GET", "/api/audit", nil, op); r.StatusCode != 403 {
		t.Fatalf("audit perm: %d", r.StatusCode)
	}
	r = e.Do("GET", "/api/audit?entity=Product&pageSize=5", nil, md)
	var al struct {
		Items []struct {
			Action string         `json:"action"`
			After  map[string]any `json:"after"`
			User   map[string]any `json:"user"`
		} `json:"items"`
		Total, Page, PageSize int
	}
	e.Decode(r, &al)
	if r.StatusCode != 200 || al.Total < 2 || al.Page != 1 || al.PageSize != 5 || al.Items[0].User["email"] == nil {
		t.Fatalf("audit list: %+v", al)
	}
	found := false
	for _, it := range al.Items {
		if it.Action == "masterdata.products.create" && it.After["code"] == "TST1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit entry for product create missing: %+v", al.Items)
	}
}

func TestUsersCRUD(t *testing.T) {
	e := apptest.New(t)
	gm := e.User(auth.RoleGeneralManager, testutil.UserOpts{Name: "Alpha GM"})
	var plant string
	_ = e.Pool.QueryRow(t.Context(), `SELECT id FROM "Plant" WHERE code='BTN-2'`).Scan(&plant)
	r := e.Do("POST", "/api/users", map[string]any{"email": " New.User@X.test ", "name": "New User", "role": "SUPERVISOR", "plantId": plant, "phone": "+250 700"}, gm)
	m := e.Map(r)
	u := m["user"].(map[string]any)
	if r.StatusCode != 201 || u["email"] != "new.user@x.test" || u["mustChangePassword"] != true || u["plant"].(map[string]any)["code"] != "BTN-2" || u["language"] != "en" {
		t.Fatalf("create: %d %v", r.StatusCode, m)
	}
	r = e.Do("POST", "/api/users", map[string]any{"email": "new.user@x.test", "name": "Dup", "role": "OPERATOR", "plantId": plant}, gm)
	if r.StatusCode != 409 {
		t.Fatalf("duplicate email: %d", r.StatusCode)
	}
	r = e.Do("POST", "/api/users", map[string]any{"email": "bad", "name": "A", "role": "KING"}, gm)
	_, _, f := e.ErrorOf(r)
	if r.StatusCode != 400 || f["email"] != "Enter a valid email" || f["name"] != "Enter the full name" || !strings.HasPrefix(f["role"], "Invalid option") {
		t.Fatalf("validation: %d %v", r.StatusCode, f)
	}
	id := u["id"].(string)
	// moving to a non-plant role keeps working; assigning plant-scoped without plant fails
	r = e.Do("PATCH", "/api/users/"+id, map[string]any{"role": "FINANCE_MANAGER"}, gm)
	if r.StatusCode != 200 {
		t.Fatalf("role change: %d", r.StatusCode)
	}
	r = e.Do("PATCH", "/api/users/"+id, map[string]any{"role": "OPERATOR", "plantId": nil}, gm)
	if _, _, f := e.ErrorOf(r); r.StatusCode != 400 || f["plantId"] != "Operators must be assigned to a plant" {
		t.Fatalf("plant needed: %d %v", r.StatusCode, f)
	}
	// cannot deactivate yourself
	r = e.Do("PATCH", "/api/users/"+gm.ID, map[string]any{"isActive": false}, gm)
	if _, msg, _ := e.ErrorOf(r); r.StatusCode != 400 || msg != "You cannot deactivate yourself or change your own role" {
		t.Fatalf("self: %d %q", r.StatusCode, msg)
	}
	// list + search
	r = e.Do("GET", "/api/users?q=new.user&pageSize=10", nil, gm)
	var lst struct {
		Items    []map[string]any `json:"items"`
		Total    int              `json:"total"`
		Page     int              `json:"page"`
		PageSize int              `json:"pageSize"`
	}
	e.Decode(r, &lst)
	if lst.Total != 1 || len(lst.Items) != 1 || lst.PageSize != 10 {
		t.Fatalf("search: %+v", lst)
	}
	// reset password: emailed, sessions revoked
	if r := e.Do("POST", "/api/users/"+id+"/reset-password", nil, gm); e.Map(r)["emailed"] != true {
		t.Fatalf("reset-password")
	}
	// deactivate revokes sessions
	victim := e.User(auth.RoleSalesManager)
	if r := e.Do("PATCH", "/api/users/"+victim.ID, map[string]any{"isActive": false}, gm); r.StatusCode != 200 {
		t.Fatalf("deactivate: %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/auth/me", nil, victim); r.StatusCode != 401 {
		t.Fatalf("deactivated user's session must die immediately: %d", r.StatusCode)
	}
}
