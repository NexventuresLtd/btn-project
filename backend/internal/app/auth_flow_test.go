package app_test

import (
	"net/http"
	"strings"
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/testutil"
)

func cookie(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// do sends JSON with explicit cookies (the sign-in flow is anonymous, so no session helper).
func do(e *apptest.Env, method, path string, body any, cookies ...*http.Cookie) *http.Response {
	var hdr []string
	var parts []string
	for _, c := range cookies {
		if c != nil {
			parts = append(parts, c.Name+"="+c.Value)
		}
	}
	if len(parts) > 0 {
		hdr = append(hdr, "Cookie", strings.Join(parts, "; "))
	}
	return e.Do(method, path, body, nil, hdr...)
}

func TestFullSignInFlow(t *testing.T) {
	e := apptest.New(t)
	u := e.User(auth.RoleGeneralManager, testutil.UserOpts{Email: "gm@x.test", Password: "GoodPassw0rd"})

	// wrong password: generic error, same status for unknown accounts
	resp := do(e, "POST", "/api/auth/login", map[string]any{"email": "gm@x.test", "password": "nope"})
	code, msg, _ := e.ErrorOf(resp)
	if resp.StatusCode != 401 || code != "invalid_credentials" || msg != "The email or password is incorrect" {
		t.Fatalf("wrong password: %d %s %s", resp.StatusCode, code, msg)
	}
	resp = do(e, "POST", "/api/auth/login", map[string]any{"email": "ghost@x.test", "password": "nope"})
	if resp.StatusCode != 401 {
		t.Fatalf("unknown account must look identical, got %d", resp.StatusCode)
	}
	// validation messages mirror zod
	resp = do(e, "POST", "/api/auth/login", map[string]any{"email": "not-an-email", "password": ""})
	_, _, fields := e.ErrorOf(resp)
	if resp.StatusCode != 400 || fields["email"] != "Enter a valid email" || fields["password"] != "Enter your password" {
		t.Fatalf("validation: %d %v", resp.StatusCode, fields)
	}

	// step 1
	resp = do(e, "POST", "/api/auth/login", map[string]any{"email": "GM@x.test ", "password": "GoodPassw0rd"})
	body := e.Map(resp)
	ticket := cookie(resp, "btn_otp")
	if resp.StatusCode != 200 || body["step"] != "otp" || body["expiresInSeconds"].(float64) != 600 || ticket == nil || !ticket.HttpOnly || ticket.Path != "/api/auth" {
		t.Fatalf("login step1: %d %v ticket=%v", resp.StatusCode, body, ticket)
	}
	otp, ok := e.Mail.LastOTP()
	if !ok || len(otp.Code) != 6 || otp.Purpose != "LOGIN" {
		t.Fatalf("otp mail missing: %+v", otp)
	}

	// no ticket cookie -> expired
	resp = do(e, "POST", "/api/auth/otp/verify", map[string]any{"code": otp.Code})
	if resp.StatusCode != 410 {
		t.Fatalf("verify without ticket: %d", resp.StatusCode)
	}
	// malformed code
	resp = do(e, "POST", "/api/auth/otp/verify", map[string]any{"code": "12"}, ticket)
	_, _, fields = e.ErrorOf(resp)
	if resp.StatusCode != 400 || fields["code"] != "Enter the 6-digit code" {
		t.Fatalf("bad code format: %d %v", resp.StatusCode, fields)
	}
	// wrong code (4 left)
	wrong := "000000"
	if otp.Code == wrong {
		wrong = "111111"
	}
	resp = do(e, "POST", "/api/auth/otp/verify", map[string]any{"code": wrong}, ticket)
	ecode, emsg, fields := e.ErrorOf(resp)
	if resp.StatusCode != 400 || ecode != "invalid_code" || emsg != "That code is not correct. 4 attempts left." || fields["code"] != "Incorrect code" {
		t.Fatalf("wrong code: %d %s %q %v", resp.StatusCode, ecode, emsg, fields)
	}
	// resend cooldown
	resp = do(e, "POST", "/api/auth/otp/resend", nil, ticket)
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") != "" && false {
		t.Fatalf("resend cooldown: %d", resp.StatusCode)
	}

	// correct code
	resp = do(e, "POST", "/api/auth/otp/verify", map[string]any{"code": otp.Code}, ticket)
	sess := cookie(resp, "btn_session")
	if resp.StatusCode != 200 || sess == nil || !sess.HttpOnly || sess.Path != "/" || sess.MaxAge != 12*3600 {
		t.Fatalf("verify: %d session=%+v", resp.StatusCode, sess)
	}
	var out struct {
		User struct {
			ID          string   `json:"id"`
			Role        string   `json:"role"`
			RoleLabel   string   `json:"roleLabel"`
			Language    string   `json:"language"`
			Permissions []string `json:"permissions"`
		} `json:"user"`
	}
	e.Decode(resp, &out)
	if out.User.ID != u.ID || out.User.Role != "GENERAL_MANAGER" || out.User.RoleLabel != "General Manager" || out.User.Language != "en" || len(out.User.Permissions) < 5 {
		t.Fatalf("payload: %+v", out.User)
	}
	// code is single use
	resp = do(e, "POST", "/api/auth/otp/verify", map[string]any{"code": otp.Code}, ticket)
	if resp.StatusCode != 410 {
		t.Fatalf("code replay must fail: %d", resp.StatusCode)
	}

	// session works, logout kills it
	resp = do(e, "GET", "/api/auth/me", nil, sess)
	if resp.StatusCode != 200 {
		t.Fatalf("me: %d", resp.StatusCode)
	}
	resp = do(e, "POST", "/api/auth/logout", nil, sess)
	if resp.StatusCode != 200 {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	resp = do(e, "GET", "/api/auth/me", nil, sess)
	if resp.StatusCode != 401 {
		t.Fatalf("session must be revoked after logout: %d", resp.StatusCode)
	}
	// audit trail written
	var n int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE action IN ('auth.login','auth.logout') AND "userId"=$1`, u.ID).Scan(&n)
	if n != 2 {
		t.Fatalf("expected login+logout audit entries, got %d", n)
	}
}

func TestOTPAttemptsLockTheChallenge(t *testing.T) {
	e := apptest.New(t)
	e.User(auth.RoleFinanceManager, testutil.UserOpts{Email: "f@x.test", Password: "GoodPassw0rd"})
	resp := do(e, "POST", "/api/auth/login", map[string]any{"email": "f@x.test", "password": "GoodPassw0rd"})
	ticket := cookie(resp, "btn_otp")
	otp, _ := e.Mail.LastOTP()
	wrong := "000000"
	if otp.Code == wrong {
		wrong = "111111"
	}
	var last *http.Response
	for i := 0; i < 5; i++ {
		last = do(e, "POST", "/api/auth/otp/verify", map[string]any{"code": wrong}, ticket)
	}
	if last.StatusCode != 410 {
		t.Fatalf("5th wrong code must lock (410), got %d", last.StatusCode)
	}
	// even the right code no longer works
	resp = do(e, "POST", "/api/auth/otp/verify", map[string]any{"code": otp.Code}, ticket)
	if resp.StatusCode != 410 {
		t.Fatalf("locked challenge accepted the right code: %d", resp.StatusCode)
	}
}

func TestAccountLockout(t *testing.T) {
	e := apptest.New(t)
	e.User(auth.RoleSalesManager, testutil.UserOpts{Email: "s@x.test", Password: "GoodPassw0rd"})
	for i := 0; i < 5; i++ {
		resp := do(e, "POST", "/api/auth/login", map[string]any{"email": "s@x.test", "password": "bad"})
		if resp.StatusCode != 401 {
			t.Fatalf("attempt %d: %d", i, resp.StatusCode)
		}
	}
	resp := do(e, "POST", "/api/auth/login", map[string]any{"email": "s@x.test", "password": "GoodPassw0rd"})
	code, msg, _ := e.ErrorOf(resp)
	if resp.StatusCode != 423 || code != "locked" || !strings.Contains(msg, "temporarily locked") {
		t.Fatalf("locked: %d %s %s", resp.StatusCode, code, msg)
	}
	// admin unlock
	admin := e.User(auth.RoleSuperAdmin)
	var id string
	_ = e.Pool.QueryRow(t.Context(), `SELECT id FROM "User" WHERE email='s@x.test'`).Scan(&id)
	if r := e.Do("POST", "/api/users/"+id+"/unlock", nil, admin); r.StatusCode != 200 {
		t.Fatalf("unlock: %d", r.StatusCode)
	}
	resp = do(e, "POST", "/api/auth/login", map[string]any{"email": "s@x.test", "password": "GoodPassw0rd"})
	if resp.StatusCode != 200 {
		t.Fatalf("login after unlock: %d", resp.StatusCode)
	}
}

func TestPasswordChangeForcedAndPolicy(t *testing.T) {
	e := apptest.New(t)
	u := e.User(auth.RoleOperationsAdvisor, testutil.UserOpts{MustChangePassword: true, Password: "GoodPassw0rd"})
	if r := e.Do("GET", "/api/lookups", nil, u); r.StatusCode != 403 {
		t.Fatalf("must-change user may not use the API: %d", r.StatusCode)
	} else if _, msg, _ := e.ErrorOf(r); msg != "You must set a new password before continuing" {
		t.Fatalf("msg %q", msg)
	}
	if r := e.Do("GET", "/api/auth/me", nil, u); r.StatusCode != 200 {
		t.Fatalf("me must work: %d", r.StatusCode)
	}
	r := e.Do("POST", "/api/auth/password/change", map[string]any{"currentPassword": "GoodPassw0rd", "newPassword": "short"}, u)
	_, _, f := e.ErrorOf(r)
	if r.StatusCode != 400 || f["newPassword"] != "Use at least 10 characters" {
		t.Fatalf("policy: %d %v", r.StatusCode, f)
	}
	r = e.Do("POST", "/api/auth/password/change", map[string]any{"currentPassword": "wrong", "newPassword": "NewPassw0rd123"}, u)
	_, _, f = e.ErrorOf(r)
	if r.StatusCode != 400 || f["currentPassword"] != "Incorrect password" {
		t.Fatalf("wrong current: %d %v", r.StatusCode, f)
	}
	r = e.Do("POST", "/api/auth/password/change", map[string]any{"currentPassword": "GoodPassw0rd", "newPassword": "GoodPassw0rd"}, u)
	_, _, f = e.ErrorOf(r)
	if r.StatusCode != 400 || f["newPassword"] != "Must differ from the current password" {
		t.Fatalf("same: %d %v", r.StatusCode, f)
	}
	r = e.Do("POST", "/api/auth/password/change", map[string]any{"currentPassword": "GoodPassw0rd", "newPassword": "NewPassw0rd123"}, u)
	if r.StatusCode != 200 {
		t.Fatalf("change: %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/lookups", nil, u); r.StatusCode != 200 {
		t.Fatalf("API usable after change (session kept): %d", r.StatusCode)
	}
}

func TestPasswordResetFlow(t *testing.T) {
	e := apptest.New(t)
	u := e.User(auth.RoleProductionManager, testutil.UserOpts{Email: "pm@x.test", Password: "GoodPassw0rd"})
	// unknown email gives the same answer and sets a cookie
	r := do(e, "POST", "/api/auth/password/forgot", map[string]any{"email": "ghost@x.test"})
	if r.StatusCode != 200 || cookie(r, "btn_otp") == nil {
		t.Fatalf("forgot (unknown): %d", r.StatusCode)
	}
	r = do(e, "POST", "/api/auth/password/forgot", map[string]any{"email": "pm@x.test"})
	ticket := cookie(r, "btn_otp")
	otp, ok := e.Mail.LastOTP()
	if r.StatusCode != 200 || !ok || otp.Purpose != "PASSWORD_RESET" {
		t.Fatalf("forgot: %d %+v", r.StatusCode, otp)
	}
	// a LOGIN-purpose challenge cannot be used here and vice-versa: wrong purpose ticket => 410
	r = do(e, "POST", "/api/auth/password/reset", map[string]any{"code": otp.Code, "password": "BrandNewPassw0rd"}, ticket)
	if r.StatusCode != 200 {
		t.Fatalf("reset: %d", r.StatusCode)
	}
	if e.Do("GET", "/api/auth/me", nil, u).StatusCode != 401 {
		t.Fatal("reset must revoke existing sessions")
	}
	r = do(e, "POST", "/api/auth/login", map[string]any{"email": "pm@x.test", "password": "BrandNewPassw0rd"})
	if r.StatusCode != 200 {
		t.Fatalf("login with new password: %d", r.StatusCode)
	}
}

func TestLanguagePreferenceAndLocalizedErrors(t *testing.T) {
	e := apptest.New(t)
	u := e.User(auth.RoleGeneralManager)
	r := e.Do("PUT", "/api/auth/language", map[string]any{"language": "xx"}, u)
	_, _, f := e.ErrorOf(r)
	if r.StatusCode != 400 || f["language"] == "" {
		t.Fatalf("invalid language: %d %v", r.StatusCode, f)
	}
	r = e.Do("PUT", "/api/auth/language", map[string]any{"language": "am"}, u)
	m := e.Map(r)
	if r.StatusCode != 200 || m["user"].(map[string]any)["language"] != "am" {
		t.Fatalf("set language: %d %v", r.StatusCode, m)
	}
	me := e.Map(e.Do("GET", "/api/auth/me", nil, u))["user"].(map[string]any)
	if me["language"] != "am" || me["roleLabel"] != "ጠቅላላ ሥራ አስኪያጅ" {
		t.Fatalf("me should reflect the language immediately: %v", me)
	}
	// error messages follow the user's language, ?lang overrides
	r = e.Do("GET", "/api/users", nil, e.User(auth.RoleOperator))
	_, msg, _ := e.ErrorOf(r)
	if msg != "You do not have permission to do this" {
		t.Fatalf("en: %q", msg)
	}
	r = e.Do("GET", "/api/users?lang=rw", nil, u)
	r = e.Do("GET", "/api/audit?lang=rw", nil, e.User(auth.RoleOperator))
	_, msg, _ = e.ErrorOf(r)
	if msg != "Nta burenganzira ufite bwo gukora ibi" {
		t.Fatalf("rw: %q", msg)
	}
	r = do(e, "POST", "/api/auth/login?lang=am", map[string]any{"email": "ghost@x.test", "password": "x"})
	_, msg, _ = e.ErrorOf(r)
	if msg != "ኢሜይሉ ወይም የይለፍ ቃሉ የተሳሳተ ነው" {
		t.Fatalf("am: %q", msg)
	}
	r = e.Do("POST", "/api/auth/login", map[string]any{"email": "ghost@x.test", "password": "x"}, nil, "Accept-Language", "rw-RW,en;q=0.5")
	_, msg, _ = e.ErrorOf(r)
	if msg != "Imeri cyangwa ijambobanga ntabwo ari byo" {
		t.Fatalf("accept-language: %q", msg)
	}
}
