package app_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/testutil"

	"github.com/fxamacker/cbor/v2"
)

// virtualAuthenticator is a minimal software FIDO2 authenticator (ES256, "none" attestation).
type virtualAuthenticator struct {
	key        *ecdsa.PrivateKey
	credID     []byte
	userHandle []byte
	counter    uint32
	origin     string
	uv         bool // set user-verified flag
}

func newVA() *virtualAuthenticator {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	id := make([]byte, 32)
	rand.Read(id)
	return &virtualAuthenticator{key: k, credID: id, origin: "http://localhost:5173", uv: true}
}

var b64 = base64.RawURLEncoding

func (v *virtualAuthenticator) flags(extra byte) byte {
	f := byte(0x01) | extra // UP
	if v.uv {
		f |= 0x04
	}
	return f
}

func (v *virtualAuthenticator) clientData(typ, challenge string) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": v.origin, "crossOrigin": false})
	return b
}

func (v *virtualAuthenticator) create(options map[string]any) map[string]any {
	pk := options
	challenge := pk["challenge"].(string)
	v.userHandle, _ = b64.DecodeString(pk["user"].(map[string]any)["id"].(string))
	rpHash := sha256.Sum256([]byte("localhost"))
	x, y := v.key.PublicKey.X.FillBytes(make([]byte, 32)), v.key.PublicKey.Y.FillBytes(make([]byte, 32))
	cose, _ := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	authData := append([]byte{}, rpHash[:]...)
	authData = append(authData, v.flags(0x40)) // AT
	authData = binary.BigEndian.AppendUint32(authData, v.counter)
	authData = append(authData, make([]byte, 16)...) // AAGUID
	authData = binary.BigEndian.AppendUint16(authData, uint16(len(v.credID)))
	authData = append(authData, v.credID...)
	authData = append(authData, cose...)
	att, _ := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": authData})
	return map[string]any{"id": b64.EncodeToString(v.credID), "rawId": b64.EncodeToString(v.credID), "type": "public-key",
		"response":               map[string]any{"clientDataJSON": b64.EncodeToString(v.clientData("webauthn.create", challenge)), "attestationObject": b64.EncodeToString(att), "transports": []string{"internal"}},
		"clientExtensionResults": map[string]any{}}
}

func (v *virtualAuthenticator) get(options map[string]any) map[string]any {
	challenge := options["challenge"].(string)
	rpHash := sha256.Sum256([]byte("localhost"))
	authData := append([]byte{}, rpHash[:]...)
	authData = append(authData, v.flags(0))
	authData = binary.BigEndian.AppendUint32(authData, v.counter)
	cd := v.clientData("webauthn.get", challenge)
	cdHash := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, authData...), cdHash[:]...))
	sig, _ := ecdsa.SignASN1(rand.Reader, v.key, digest[:])
	return map[string]any{"id": b64.EncodeToString(v.credID), "rawId": b64.EncodeToString(v.credID), "type": "public-key",
		"response":               map[string]any{"clientDataJSON": b64.EncodeToString(cd), "authenticatorData": b64.EncodeToString(authData), "signature": b64.EncodeToString(sig), "userHandle": b64.EncodeToString(v.userHandle)},
		"clientExtensionResults": map[string]any{}}
}

func register(t *testing.T, e *apptest.Env, u *testutil.TestUser, va *virtualAuthenticator, name string) map[string]any {
	t.Helper()
	r := e.Do("POST", "/api/account/passkeys/register/begin", map[string]any{}, u)
	if r.StatusCode != 200 {
		t.Fatalf("register begin: %d %s", r.StatusCode, e.Text(r))
	}
	begin := e.Map(r)
	opts := begin["options"].(map[string]any)
	r = e.Do("POST", "/api/account/passkeys/register/finish", map[string]any{"challengeId": begin["challengeId"], "credential": va.create(opts), "name": name}, u)
	if r.StatusCode != 201 {
		t.Fatalf("register finish: %d %s", r.StatusCode, e.Text(r))
	}
	return e.Map(r)["passkey"].(map[string]any)
}

func loginBegin(t *testing.T, e *apptest.Env) (challengeID string, opts map[string]any) {
	t.Helper()
	r := e.Do("POST", "/api/auth/passkey/begin", map[string]any{}, nil)
	if r.StatusCode != 200 {
		t.Fatalf("login begin: %d", r.StatusCode)
	}
	m := e.Map(r)
	return m["challengeId"].(string), m["options"].(map[string]any)
}

func TestPasskeyRegistrationAndDiscoverableLogin(t *testing.T) {
	e := apptest.New(t)
	u := e.User(auth.RoleFinanceManager, testutil.UserOpts{Email: "pk@x.test"})
	va := newVA()

	// registration requires a session, and a finished first-login password change
	if r := e.Do("POST", "/api/account/passkeys/register/begin", map[string]any{}, nil); r.StatusCode != 401 {
		t.Fatalf("anon register: %d", r.StatusCode)
	}
	fresh := e.User(auth.RoleOperationsAdvisor, testutil.UserOpts{MustChangePassword: true})
	if r := e.Do("POST", "/api/account/passkeys/register/begin", map[string]any{}, fresh); r.StatusCode != 403 {
		t.Fatalf("must-change user may not register: %d", r.StatusCode)
	}

	// options: discoverable credential + user verification required, no attestation
	r := e.Do("POST", "/api/account/passkeys/register/begin", map[string]any{}, u)
	opts := e.Map(r)["options"].(map[string]any)
	sel := opts["authenticatorSelection"].(map[string]any)
	if sel["userVerification"] != "required" || sel["residentKey"] != "required" || opts["rp"].(map[string]any)["id"] != "localhost" || opts["attestation"] != "none" {
		t.Fatalf("registration options: %v", opts)
	}

	pk := register(t, e, u, va, "Laptop")
	if pk["name"] != "Laptop" || pk["id"] == "" {
		t.Fatalf("passkey: %v", pk)
	}
	if r := e.Do("GET", "/api/auth/me", nil, u); e.Map(r)["user"].(map[string]any)["passkeyCount"].(float64) != 1 {
		t.Fatal("passkeyCount")
	}

	// rename + list
	id := pk["id"].(string)
	if r := e.Do("PATCH", "/api/account/passkeys/"+id, map[string]any{"name": "Office laptop"}, u); r.StatusCode != 200 {
		t.Fatalf("rename: %d", r.StatusCode)
	}
	if r := e.Do("PATCH", "/api/account/passkeys/"+id, map[string]any{"name": ""}, u); r.StatusCode != 400 {
		t.Fatalf("empty name: %d", r.StatusCode)
	}
	other := e.User(auth.RoleGeneralManager)
	if r := e.Do("PATCH", "/api/account/passkeys/"+id, map[string]any{"name": "steal"}, other); r.StatusCode != 404 {
		t.Fatalf("other user's passkey: %d", r.StatusCode)
	}
	list := e.Map(e.Do("GET", "/api/account/passkeys", nil, u))["items"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["name"] != "Office laptop" {
		t.Fatalf("list: %v", list)
	}

	// discoverable sign-in: no email, user verification required
	va.counter = 1
	cid, lopts := loginBegin(t, e)
	if lopts["userVerification"] != "required" {
		t.Fatalf("login options: %v", lopts)
	}
	if ac, ok := lopts["allowCredentials"].([]any); ok && len(ac) > 0 {
		t.Fatal("discoverable login must not list credentials")
	}
	body := map[string]any{"challengeId": cid, "credential": va.get(lopts)}
	r = e.Do("POST", "/api/auth/passkey/finish", body, nil)
	sess := cookie(r, "btn_session")
	if r.StatusCode != 200 || sess == nil || !sess.HttpOnly {
		t.Fatalf("login finish: %d %s", r.StatusCode, e.Text(r))
	}
	if r := do(e, "GET", "/api/lookups", nil, sess); r.StatusCode != 200 {
		t.Fatalf("session from passkey: %d", r.StatusCode)
	}
	// challenge is single use
	if r := e.Do("POST", "/api/auth/passkey/finish", body, nil); r.StatusCode != 400 {
		t.Fatalf("replay: %d", r.StatusCode)
	}
	// audit + last_used
	var n int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE action IN ('auth.passkey_login','passkey.register','passkey.rename') AND "userId"=$1`, u.ID).Scan(&n)
	if n != 3 {
		t.Fatalf("audit entries: %d", n)
	}
	var sc int64
	var used bool
	_ = e.Pool.QueryRow(t.Context(), `SELECT sign_count, last_used_at IS NOT NULL FROM passkey WHERE id=$1`, id).Scan(&sc, &used)
	if sc != 1 || !used {
		t.Fatalf("counter/last_used: %d %v", sc, used)
	}

	// counter regression (cloned authenticator) is rejected
	cid, lopts = loginBegin(t, e)
	if r := e.Do("POST", "/api/auth/passkey/finish", map[string]any{"challengeId": cid, "credential": va.get(lopts)}, nil); r.StatusCode != 401 {
		t.Fatalf("clone warning must reject: %d", r.StatusCode)
	}
	va.counter = 2
	cid, lopts = loginBegin(t, e)
	if r := e.Do("POST", "/api/auth/passkey/finish", map[string]any{"challengeId": cid, "credential": va.get(lopts)}, nil); r.StatusCode != 200 {
		t.Fatalf("valid counter after regression: %d", r.StatusCode)
	}

	// user verification is mandatory
	va.counter, va.uv = 3, false
	cid, lopts = loginBegin(t, e)
	if r := e.Do("POST", "/api/auth/passkey/finish", map[string]any{"challengeId": cid, "credential": va.get(lopts)}, nil); r.StatusCode != 401 {
		t.Fatalf("UV missing must fail: %d", r.StatusCode)
	}
	va.uv = true
	// wrong origin
	va.counter, va.origin = 4, "https://evil.example"
	cid, lopts = loginBegin(t, e)
	if r := e.Do("POST", "/api/auth/passkey/finish", map[string]any{"challengeId": cid, "credential": va.get(lopts)}, nil); r.StatusCode != 401 {
		t.Fatalf("wrong origin must fail: %d", r.StatusCode)
	}
	va.origin = "http://localhost:5173"
	// expired challenge
	cid, lopts = loginBegin(t, e)
	_, _ = e.Pool.Exec(t.Context(), `UPDATE passkey_challenge SET expires_at = now() - interval '1 minute'`)
	if r := e.Do("POST", "/api/auth/passkey/finish", map[string]any{"challengeId": cid, "credential": va.get(lopts)}, nil); r.StatusCode != 400 {
		t.Fatalf("expired challenge: %d", r.StatusCode)
	}
	// deactivated user cannot sign in
	_, _ = e.Pool.Exec(t.Context(), `UPDATE "User" SET "isActive"=false WHERE id=$1`, u.ID)
	cid, lopts = loginBegin(t, e)
	va.counter = 9
	if r := e.Do("POST", "/api/auth/passkey/finish", map[string]any{"challengeId": cid, "credential": va.get(lopts)}, nil); r.StatusCode != 401 {
		t.Fatalf("inactive user: %d", r.StatusCode)
	}
	_, _ = e.Pool.Exec(t.Context(), `UPDATE "User" SET "isActive"=true WHERE id=$1`, u.ID)

	// delete -> credential no longer accepted
	if r := e.Do("DELETE", "/api/account/passkeys/"+id, nil, u); r.StatusCode != 200 {
		t.Fatalf("delete: %d", r.StatusCode)
	}
	va.counter = 10
	cid, lopts = loginBegin(t, e)
	if r := e.Do("POST", "/api/auth/passkey/finish", map[string]any{"challengeId": cid, "credential": va.get(lopts)}, nil); r.StatusCode != 401 {
		t.Fatalf("deleted passkey: %d", r.StatusCode)
	}
}

func TestPasskeyFailuresAreRateLimited(t *testing.T) {
	e := apptest.New(t)
	for i := 0; i < 20; i++ {
		cid, _ := loginBegin(t, e)
		e.Do("POST", "/api/auth/passkey/finish", map[string]any{"challengeId": cid, "credential": map[string]any{"id": "x"}}, nil)
	}
	if r := e.Do("POST", "/api/auth/passkey/begin", map[string]any{}, nil); r.StatusCode != 429 {
		t.Fatalf("expected 429 after repeated failures, got %d", r.StatusCode)
	}
}
