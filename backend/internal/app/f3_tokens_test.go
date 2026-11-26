package app_test

// F3: access/refresh tokens (AUTH-06), devices, reset links (AUTH-04), offline packages (AUTH-10, OFF-12).

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/testutil"
)

func errCode(e *apptest.Env, r *http.Response) (int, string) {
	code, _, _ := e.ErrorOf(r)
	return r.StatusCode, code
}

func TestAccessExpiryAndRefreshRotation(t *testing.T) {
	e := apptest.New(t)
	u := e.User(auth.RoleProductionManager)
	dc := e.SignInDevice(u, "")
	if dc.Cookies[auth.RefreshCookie] == nil || dc.Cookies[auth.SessionCookie] == nil {
		t.Fatalf("cookies: %v", dc.Cookies)
	}
	me := e.Map(dc.Do("GET", "/api/auth/me", nil))
	dev := me["device"].(map[string]any)
	if dev["deviceId"] != dc.DeviceID || dev["current"] != true || dev["platform"] != "Android" || me["accessExpiresAt"] == nil {
		t.Fatalf("me: %v", me)
	}
	exp, _ := time.Parse(time.RFC3339Nano, me["accessExpiresAt"].(string))
	if d := time.Until(exp); d < 14*time.Minute || d > 15*time.Minute+time.Second {
		t.Fatalf("access ttl %s", d)
	}
	// Expire the access token → 401 token_expired (not plain unauthorized).
	if _, err := e.Pool.Exec(t.Context(), `UPDATE auth_access_token SET expires_at=now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	e.Deps.Auth.InvalidateUser(u.ID)
	if st, code := errCode(e, dc.Do("GET", "/api/lookups", nil)); st != 401 || code != auth.CodeTokenExpired {
		t.Fatalf("expired: %d %s", st, code)
	}
	if st, code := errCode(e, dc.Do("GET", "/api/auth/me", nil)); st != 401 || code != auth.CodeTokenExpired {
		t.Fatalf("me expired: %d %s", st, code)
	}
	old := dc.Cookies[auth.RefreshCookie].Value
	r := dc.Refresh()
	body := e.Map(r)
	if r.StatusCode != 200 || body["rotated"] != true || dc.Cookies[auth.RefreshCookie].Value == old {
		t.Fatalf("refresh: %d %v", r.StatusCode, body)
	}
	if r := dc.Do("GET", "/api/lookups", nil); r.StatusCode != 200 {
		t.Fatalf("after refresh: %d", r.StatusCode)
	}
	// Multi-tab race: the old token again within the grace window → 200 without new cookies.
	newRT := dc.Cookies[auth.RefreshCookie].Value
	dc.Cookies[auth.RefreshCookie].Value = old
	r = dc.Refresh()
	if b := e.Map(r); r.StatusCode != 200 || b["rotated"] != false {
		t.Fatalf("grace: %d %v", r.StatusCode, b)
	}
	// Reuse after the grace window → the device session (whole family) is revoked.
	if _, err := e.Pool.Exec(t.Context(), `UPDATE auth_refresh_token SET used_at=now() - interval '5 minutes' WHERE used_at IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
	dc.Cookies[auth.RefreshCookie].Value = old
	if st, code := errCode(e, dc.Refresh()); st != 401 || code != auth.CodeRefreshInvalid {
		t.Fatalf("reuse: %d %s", st, code)
	}
	var reason string
	_ = e.Pool.QueryRow(t.Context(), `SELECT revoke_reason FROM auth_session WHERE user_id=$1 AND method='password_otp'`, u.ID).Scan(&reason)
	if reason != auth.ReasonRefreshReuse {
		t.Fatalf("session reason %q", reason)
	}
	// The legitimately rotated token is dead too, and so is the access token.
	dc.Cookies[auth.RefreshCookie] = &http.Cookie{Name: auth.RefreshCookie, Value: newRT, Path: "/api/auth"}
	if st, _ := errCode(e, dc.Refresh()); st != 401 {
		t.Fatalf("family revoked: %d", st)
	}
	if n := countRows(t, e, `SELECT count(*) FROM "AuditLog" WHERE action='auth.refresh_reuse' AND "userId"=$1 AND "deviceId"=$2`, u.ID, dc.DeviceID); n != 1 {
		t.Fatalf("reuse audit %d", n)
	}
	// No refresh cookie at all.
	if st, code := errCode(e, e.Do("POST", "/api/auth/refresh", nil, nil)); st != 401 || code != auth.CodeRefreshInvalid {
		t.Fatalf("no cookie: %d %s", st, code)
	}
}

func countRows(t *testing.T, e *apptest.Env, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.Pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRefreshExpiryIdleAndLogout(t *testing.T) {
	e := apptest.New(t)
	u := e.User(auth.RoleFinanceManager)
	dc := e.SignInDevice(u, "")
	// Idle > 2 h without an offline PIN → session ends.
	if _, err := e.Pool.Exec(t.Context(), `UPDATE auth_session SET last_seen_at=now() - interval '3 hours' WHERE user_id=$1`, u.ID); err != nil {
		t.Fatal(err)
	}
	if st, _ := errCode(e, dc.Refresh()); st != 401 {
		t.Fatalf("idle refresh: %d", st)
	}
	// With an offline PIN marker, the idle limit is the offline window.
	dc = e.SignInDevice(u, dc.DeviceID)
	if r := dc.Do("PUT", "/api/auth/offline-pin", nil); r.StatusCode != 200 {
		t.Fatalf("pin: %d %s", r.StatusCode, e.Text(r))
	}
	if _, err := e.Pool.Exec(t.Context(), `UPDATE auth_session SET last_seen_at=now() - interval '3 days' WHERE user_id=$1 AND revoked_at IS NULL`, u.ID); err != nil {
		t.Fatal(err)
	}
	if r := dc.Refresh(); r.StatusCode != 200 {
		t.Fatalf("offline-enabled idle refresh: %d", r.StatusCode)
	}
	// Absolute expiry.
	if _, err := e.Pool.Exec(t.Context(), `UPDATE auth_session SET expires_at=now() - interval '1 second' WHERE user_id=$1 AND revoked_at IS NULL`, u.ID); err != nil {
		t.Fatal(err)
	}
	if st, _ := errCode(e, dc.Refresh()); st != 401 {
		t.Fatalf("absolute expiry: %d", st)
	}
	// Logout revokes the session and clears both cookies.
	dc = e.SignInDevice(u, dc.DeviceID)
	if r := dc.Do("POST", "/api/auth/logout", nil); r.StatusCode != 200 {
		t.Fatalf("logout: %d", r.StatusCode)
	}
	if len(dc.Cookies) != 0 {
		t.Fatalf("cookies not cleared: %v", dc.Cookies)
	}
	if n := countRows(t, e, `SELECT count(*) FROM "AuditLog" WHERE action='auth.logout' AND "deviceId"=$1`, dc.DeviceID); n != 1 {
		t.Fatalf("logout audit with device id: %d", n)
	}
	if n := countRows(t, e, `SELECT count(*) FROM "AuditLog" WHERE action='auth.login' AND "deviceId"=$1`, dc.DeviceID); n != 3 {
		t.Fatalf("login audits with device id: %d", n)
	}
}

func TestPasswordChangeRevokesOtherDevices(t *testing.T) {
	e := apptest.New(t)
	u := e.User(auth.RoleSalesManager, testutil.UserOpts{Password: "GoodPassw0rd1"})
	a := e.SignInDevice(u, "")
	b := e.SignInDevice(u, "")
	r := a.Do("POST", "/api/auth/password/change", map[string]any{"currentPassword": "GoodPassw0rd1", "newPassword": "EvenBetterPassw0rd2"})
	if r.StatusCode != 200 {
		t.Fatalf("change: %d %s", r.StatusCode, e.Text(r))
	}
	if r := a.Do("GET", "/api/auth/me", nil); r.StatusCode != 200 {
		t.Fatalf("current device kept: %d", r.StatusCode)
	}
	if r := b.Do("GET", "/api/auth/me", nil); r.StatusCode != 401 {
		t.Fatalf("other device must be signed out: %d", r.StatusCode)
	}
	if st, _ := errCode(e, b.Refresh()); st != 401 {
		t.Fatalf("other device refresh: %d", st)
	}
	if r := a.Refresh(); r.StatusCode != 200 {
		t.Fatalf("current device refresh: %d", r.StatusCode)
	}
}

func TestDevicesOwnAndAdmin(t *testing.T) {
	e := apptest.New(t)
	u := e.User(auth.RoleSupervisor)
	other := e.User(auth.RoleOperator)
	a := e.SignInDevice(u, "")
	b := e.SignInDevice(u, "")
	o := e.SignInDevice(other, a.DeviceID) // same browser profile, another user → separate device row
	list := e.Map(a.Do("GET", "/api/account/devices", nil))["items"].([]any)
	if len(list) != 2 {
		t.Fatalf("own devices: %v", list)
	}
	var bID string
	for _, d := range list {
		m := d.(map[string]any)
		if m["deviceId"] == b.DeviceID {
			bID = m["id"].(string)
		}
		if m["deviceId"] == a.DeviceID && m["current"] != true {
			t.Fatalf("current flag: %v", m)
		}
	}
	// Rename (validation + audit).
	if r := a.Do("PATCH", "/api/account/devices/"+bID, map[string]any{"name": ""}); r.StatusCode != 400 {
		t.Fatalf("empty name: %d", r.StatusCode)
	}
	r := a.Do("PATCH", "/api/account/devices/"+bID, map[string]any{"name": "Site office tablet"})
	if r.StatusCode != 200 || e.Map(r)["device"].(map[string]any)["name"] != "Site office tablet" {
		t.Fatalf("rename: %d", r.StatusCode)
	}
	// Another user cannot touch it.
	if r := o.Do("POST", "/api/account/devices/"+bID+"/revoke", nil); r.StatusCode != 404 {
		t.Fatalf("foreign revoke: %d", r.StatusCode)
	}
	if r := o.Do("PATCH", "/api/account/devices/"+bID, map[string]any{"name": "x"}); r.StatusCode != 404 {
		t.Fatalf("foreign rename: %d", r.StatusCode)
	}
	// Admin view: devices.manage (GM) lists everyone's and revokes; supervisor is denied.
	gm := e.User(auth.RoleGeneralManager)
	page := e.Map(e.Do("GET", "/api/devices?q="+strings.ToUpper(u.Email[:6]), nil, gm))
	if page["total"].(float64) != 2 {
		t.Fatalf("admin search: %v", page)
	}
	all := e.Map(e.Do("GET", "/api/devices", nil, gm))
	if all["total"].(float64) != 3 {
		t.Fatalf("admin all: %v", all)
	}
	if r := a.Do("GET", "/api/devices", nil); r.StatusCode != 403 {
		t.Fatalf("supervisor admin list: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/devices/"+bID+"/revoke", nil, e.User(auth.RoleSystemAdmin)); r.StatusCode != 200 {
		t.Fatalf("sysadmin revoke: %d", r.StatusCode)
	}
	if st, code := errCode(e, b.Do("GET", "/api/lookups", nil)); st != 403 || code != auth.CodeDeviceRevoked {
		t.Fatalf("revoked device: %d %s", st, code)
	}
	if r := e.Do("POST", "/api/devices/"+bID+"/revoke", nil, gm); r.StatusCode != 409 {
		t.Fatalf("double revoke: %d", r.StatusCode)
	}
	if r := a.Do("GET", "/api/lookups", nil); r.StatusCode != 200 {
		t.Fatalf("other device unaffected: %d", r.StatusCode)
	}
	if n := countRows(t, e, `SELECT count(*) FROM "AuditLog" WHERE action='device.revoke' AND "entityId"=$1`, bID); n != 1 {
		t.Fatalf("revoke audit %d", n)
	}
	// Signing in again on a revoked device reactivates it with fresh offline secrets.
	var keyBefore []byte
	_ = e.Pool.QueryRow(t.Context(), `SELECT device_key FROM device WHERE id=$1`, bID).Scan(&keyBefore)
	b2 := e.SignInDevice(u, b.DeviceID)
	var status string
	var keyAfter []byte
	_ = e.Pool.QueryRow(t.Context(), `SELECT status, device_key FROM device WHERE id=$1`, bID).Scan(&status, &keyAfter)
	if status != "active" || string(keyAfter) == string(keyBefore) {
		t.Fatalf("reactivation: %s", status)
	}
	if r := b2.Do("GET", "/api/lookups", nil); r.StatusCode != 200 {
		t.Fatalf("reactivated device: %d", r.StatusCode)
	}
	// Anonymous.
	if r := e.Do("GET", "/api/account/devices", nil, nil); r.StatusCode != 401 {
		t.Fatalf("anon: %d", r.StatusCode)
	}
}

func TestPasswordResetLink(t *testing.T) {
	e := apptest.New(t)
	u := e.User(auth.RoleFinanceManager, testutil.UserOpts{Password: "GoodPassw0rd1", Language: "fr"})
	session := e.SignInDevice(u, "")
	forgot := func() string {
		t.Helper()
		r := e.Do("POST", "/api/auth/password/forgot", map[string]any{"email": u.Email}, nil)
		if r.StatusCode != 200 {
			t.Fatalf("forgot: %d", r.StatusCode)
		}
		otp, _ := e.Mail.LastOTP()
		if otp.Purpose != "PASSWORD_RESET" || !strings.Contains(otp.Link, "/reset-password?token=") || otp.LinkMinutes != 30 {
			t.Fatalf("reset mail: %+v", otp)
		}
		return otp.Link[strings.Index(otp.Link, "token=")+6:]
	}
	tok1 := forgot()
	tok2 := forgot() // a new request invalidates the first link
	if st, code := errCode(e, e.Do("POST", "/api/auth/password/reset-link/check", map[string]any{"token": tok1}, nil)); st != 410 || code != "reset_invalid" {
		t.Fatalf("old link: %d %s", st, code)
	}
	check := e.Map(e.Do("POST", "/api/auth/password/reset-link/check", map[string]any{"token": tok2}, nil))
	if check["valid"] != true || !strings.Contains(check["email"].(string), "•") {
		t.Fatalf("check: %v", check)
	}
	// Weak password is rejected without consuming the link.
	if r := e.Do("POST", "/api/auth/password/reset-link", map[string]any{"token": tok2, "password": "short"}, nil); r.StatusCode != 400 {
		t.Fatalf("weak: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/auth/password/reset-link", map[string]any{"token": tok2, "password": "BrandNewPassw0rd9"}, nil); r.StatusCode != 200 {
		t.Fatalf("reset: %d %s", r.StatusCode, e.Text(r))
	}
	// Single use, sessions revoked, new password works.
	if st, _ := errCode(e, e.Do("POST", "/api/auth/password/reset-link", map[string]any{"token": tok2, "password": "AnotherPassw0rd7"}, nil)); st != 410 {
		t.Fatalf("reuse: %d", st)
	}
	if r := session.Do("GET", "/api/auth/me", nil); r.StatusCode != 401 {
		t.Fatalf("old session after reset: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/auth/login", map[string]any{"email": u.Email, "password": "BrandNewPassw0rd9"}, nil); r.StatusCode != 200 {
		t.Fatalf("login with new password: %d", r.StatusCode)
	}
	// Expiry.
	tok3 := forgot()
	if _, err := e.Pool.Exec(t.Context(), `UPDATE password_reset_token SET expires_at=now() - interval '1 second' WHERE used_at IS NULL AND invalidated_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	if st, _ := errCode(e, e.Do("POST", "/api/auth/password/reset-link", map[string]any{"token": tok3, "password": "AnotherPassw0rd7"}, nil)); st != 410 {
		t.Fatalf("expired: %d", st)
	}
	// A password change invalidates outstanding links.
	tok4 := forgot()
	_, _ = e.Pool.Exec(t.Context(), `DELETE FROM "AuthAttempt"`) // per-hour code limit
	dc := e.SignInDevice(&testutil.TestUser{ID: u.ID, Email: u.Email, Password: "BrandNewPassw0rd9"}, "")
	if r := dc.Do("POST", "/api/auth/password/change", map[string]any{"currentPassword": "BrandNewPassw0rd9", "newPassword": "ChangedAgainPassw0rd5"}); r.StatusCode != 200 {
		t.Fatalf("change: %d %s", r.StatusCode, e.Text(r))
	}
	if st, _ := errCode(e, e.Do("POST", "/api/auth/password/reset-link/check", map[string]any{"token": tok4}, nil)); st != 410 {
		t.Fatalf("link after password change: %d", st)
	}
	// Unknown email: same answer, no mail, no token row.
	before := len(e.Mail.OTPs)
	r := e.Do("POST", "/api/auth/password/forgot", map[string]any{"email": "nobody@btn.test"}, nil)
	if r.StatusCode != 200 || len(e.Mail.OTPs) != before {
		t.Fatalf("unknown email: %d", r.StatusCode)
	}
	// The French email carries localized link texts.
	if e.Mail.OTPs[len(e.Mail.OTPs)-1].Lang != "fr" {
		t.Fatalf("mail language %s", e.Mail.OTPs[len(e.Mail.OTPs)-1].Lang)
	}
}

func TestOfflinePackageSignature(t *testing.T) {
	e := apptest.New(t)
	u := e.User(auth.RoleSupervisor)
	dc := e.SignInDevice(u, "")
	key := e.Map(e.Do("GET", "/api/auth/offline-key", nil, nil))
	pub, err := base64.RawURLEncoding.DecodeString(key["publicKey"].(string))
	if err != nil || len(pub) != ed25519.PublicKeySize || key["alg"] != "Ed25519" || key["jwk"].(map[string]any)["crv"] != "Ed25519" {
		t.Fatalf("key: %v", key)
	}
	r := dc.Do("POST", "/api/auth/offline-package", nil)
	if r.StatusCode != 200 {
		t.Fatalf("package: %d %s", r.StatusCode, e.Text(r))
	}
	var out struct {
		Package     map[string]any `json:"package"`
		PackageJSON string         `json:"packageJson"`
		Signature   string         `json:"signature"`
		KeyID       string         `json:"keyId"`
		UnlockDays  int            `json:"unlockDays"`
		DeviceKey   string         `json:"deviceKey"`
		PinSet      bool           `json:"pinSet"`
	}
	e.Decode(r, &out)
	if !auth.VerifyOfflinePackage(pub, out.PackageJSON, out.Signature) {
		t.Fatal("signature does not verify")
	}
	tampered := strings.Replace(out.PackageJSON, `"role":"SUPERVISOR"`, `"role":"SUPER_ADMIN"`, 1)
	if tampered == out.PackageJSON || auth.VerifyOfflinePackage(pub, tampered, out.Signature) {
		t.Fatal("tampered package verified")
	}
	var signed auth.OfflinePackage
	if err := json.Unmarshal([]byte(out.PackageJSON), &signed); err != nil {
		t.Fatal(err)
	}
	if signed.UserID != u.ID || signed.DeviceID != dc.DeviceID || !signed.SiteScope.Scoped || signed.SiteScope.PlantID != u.PlantID ||
		out.UnlockDays != 14 || signed.ExpiresAt.Sub(signed.IssuedAt) != 14*24*time.Hour || signed.Salt == "" || signed.KeyID != key["keyId"] {
		t.Fatalf("package content: %+v", signed)
	}
	dk, _ := base64.StdEncoding.DecodeString(out.DeviceKey)
	if len(dk) != 32 || out.PinSet {
		t.Fatalf("device key %d pin %v", len(dk), out.PinSet)
	}
	// The window is a setting; the device key is stable per device.
	admin := e.User(auth.RoleSystemAdmin)
	if r := e.Do("PUT", "/api/sync/settings", map[string]any{"maxClockSkewMinutes": 10, "offlineUnlockDays": 7}, admin); r.StatusCode != 200 {
		t.Fatalf("settings: %d", r.StatusCode)
	}
	dc.Do("PUT", "/api/auth/offline-pin", nil)
	var again struct {
		PackageJSON string `json:"packageJson"`
		UnlockDays  int    `json:"unlockDays"`
		DeviceKey   string `json:"deviceKey"`
		PinSet      bool   `json:"pinSet"`
	}
	e.Decode(dc.Do("POST", "/api/auth/offline-package", nil), &again)
	if again.UnlockDays != 7 || again.DeviceKey != out.DeviceKey || !again.PinSet {
		t.Fatalf("second package: %+v", again)
	}
	// Device mismatch and device-less sessions are refused.
	if st, code := errCode(e, dc.Do("POST", "/api/auth/offline-package", nil, auth.DeviceHeader, "11111111-2222-4333-8444-555555555555")); st != 403 || code != auth.CodeDeviceMismatch {
		t.Fatalf("mismatch: %d %s", st, code)
	}
	if st, _ := errCode(e, e.Do("POST", "/api/auth/offline-package", nil, u)); st != 400 {
		t.Fatalf("device-less: %d", st)
	}
	if st, _ := errCode(e, e.Do("POST", "/api/auth/offline-package", nil, nil)); st != 401 {
		t.Fatalf("anon: %d", st)
	}
	// Clearing the PIN marker; revoked device gets no package.
	if b := e.Map(dc.Do("DELETE", "/api/auth/offline-pin", nil)); b["pinSet"] != false {
		t.Fatalf("clear pin: %v", b)
	}
	var row string
	_ = e.Pool.QueryRow(t.Context(), `SELECT id FROM device WHERE device_id=$1`, dc.DeviceID).Scan(&row)
	if r := e.Do("POST", "/api/devices/"+row+"/revoke", nil, admin); r.StatusCode != 200 {
		t.Fatalf("revoke: %d", r.StatusCode)
	}
	if st, code := errCode(e, dc.Do("POST", "/api/auth/offline-package", nil)); st != 403 || code != auth.CodeDeviceRevoked {
		t.Fatalf("revoked package: %d %s", st, code)
	}
}
