package auth

import (
	"strings"
	"testing"
)

// Hash produced by the legacy Node backend (@node-rs/argon2, m=19456,t=2,p=1) for "GoodPassw0rd".
const nodeHash = "$argon2id$v=19$m=19456,t=2,p=1$fEcDMHMvyjdxiKN0Wfzn6g$Wm4c56dsvdidOaTCoruuHB9F3tqdFBG8fHzwBqqpM28"

func TestVerifiesNodeArgon2Hash(t *testing.T) {
	if !VerifyHash(nodeHash, "GoodPassw0rd") {
		t.Fatal("Node-produced hash must verify")
	}
	if VerifyHash(nodeHash, "WrongPassw0rd") {
		t.Fatal("wrong password verified")
	}
}

func TestHashRoundTripAndFormat(t *testing.T) {
	h := HashPassword("GoodPassw0rd")
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("unexpected format %s", h)
	}
	if !VerifyHash(h, "GoodPassw0rd") || VerifyHash(h, "goodpassw0rd") {
		t.Fatal("round trip failed")
	}
	if VerifyPassword(nil, "anything") {
		t.Fatal("nil hash must never verify")
	}
	for _, bad := range []string{"", "plain", "$argon2i$v=19$m=1,t=1,p=1$a$b", "$argon2id$v=19$m=19456,t=2,p=1$!!$!!"} {
		if VerifyHash(bad, "x") {
			t.Fatalf("malformed hash %q verified", bad)
		}
	}
}

func TestOTPHashBoundToChallengeAndPepper(t *testing.T) {
	a := HashOTP("pepper", "c1", "123456")
	if !SafeEqualHex(a, HashOTP("pepper", "c1", "123456")) {
		t.Fatal("same input must match")
	}
	for _, other := range []string{HashOTP("pepper", "c2", "123456"), HashOTP("pepper", "c1", "123457"), HashOTP("other", "c1", "123456")} {
		if SafeEqualHex(a, other) {
			t.Fatal("hash must bind challenge, code and pepper")
		}
	}
	if SafeEqualHex("abcd", "abcdef") || SafeEqualHex("zz", "zz") {
		t.Fatal("length mismatch / invalid hex must be false")
	}
	for i := 0; i < 300; i++ {
		c := GenerateOTPCode()
		if len(c) != 6 || !isDigits(c) {
			t.Fatalf("bad code %q", c)
		}
	}
}

func TestTempPasswordMeetsPolicy(t *testing.T) {
	for i := 0; i < 50; i++ {
		p := TempPassword()
		if len(p) < 10 || !reLower.MatchString(p) || !reUpper.MatchString(p) || !reDigit.MatchString(p) {
			t.Fatalf("temp password %q violates policy", p)
		}
	}
}

func TestPermissions(t *testing.T) {
	// Operations advisor is read-only (spec §4 note).
	for _, r := range Matrix {
		if !strings.HasSuffix(string(r.P), ".view") && Can(RoleOperationsAdvisor, r.P) {
			t.Errorf("advisor must not hold %s", r.P)
		}
	}
	// System admin cannot approve business transactions.
	for _, p := range []Permission{"production.approve", "pettycash.approve", "payroll.authorize", "sales.confirm"} {
		if Can(RoleSystemAdmin, p) {
			t.Errorf("system admin must not hold %s", p)
		}
	}
	// Super admin holds everything, including permissions that do not exist yet.
	for _, r := range Matrix {
		if !Can(RoleSuperAdmin, r.P) {
			t.Errorf("super admin lacks %s", r.P)
		}
	}
	if !Can(RoleSuperAdmin, "future.permission") {
		t.Error("super admin must hold future permissions")
	}
	if Can(RoleGeneralManager, "branding.manage") || Can(RoleSystemAdmin, "i18n.manage") {
		t.Error("branding/i18n are super-admin only")
	}
	if !IsPlantScoped(RoleSupervisor) || IsPlantScoped(RoleSuperAdmin) || IsPlantScoped(RoleGeneralManager) {
		t.Error("plant scoping wrong")
	}
	if Can("UNKNOWN", "dashboard.view") != true || Can(RoleOperator, "nope") {
		t.Error("dashboard.view is for all; unknown permission is denied")
	}
}

func TestPlantHelpers(t *testing.T) {
	sup := &User{Role: RoleSupervisor, PlantID: "p1"}
	if PlantFilter(sup, "p2") != "p1" {
		t.Error("scoped user must be pinned to own plant")
	}
	if AssertPlantAccess(sup, "p2") == nil || AssertPlantAccess(sup, "p1") != nil {
		t.Error("assert plant access wrong")
	}
	gm := &User{Role: RoleGeneralManager}
	if PlantFilter(gm, "p2") != "p2" || PlantFilter(gm, "") != "" || AssertPlantAccess(gm, "anything") != nil {
		t.Error("unscoped user filter wrong")
	}
	orphan := &User{Role: RoleOperator}
	if ScopedPlantID(orphan) != "__none__" {
		t.Error("scoped user without plant sees nothing")
	}
}

func TestMaskEmail(t *testing.T) {
	if got := maskEmail("alice@btn.test"); got != "al•••@btn.test" {
		t.Fatal(got)
	}
}
