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
		if r.P == "data.export" { // SRS §8 / ADM-05: read-only bulk export
			continue
		}
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

// TestNewRoleMatrix pins the SRS §8 grants of docs/SRS_BUILD_PLAN.md "Roles and permissions" exactly.
// srsMatrix is the SRS §8 table (C create, R read, U update, A approve; supervisors limited to their own site).
// "Attendance & wages" is split in its attendance and wages halves because the supervisor's C R U is "(att.)".
var srsMatrix = map[string]map[string]string{
	//          Sup.      Prod. Mgr  Stock    Sales    Fin.     Proc.    Top Mgt  Admin
	"production":  {"SUP": "CRU", "PM": "RUA", "STO": "R", "SM": "", "FM": "R", "PRO": "", "TOP": "R", "SYS": "R"},
	"stock":       {"SUP": "CR", "PM": "RA", "STO": "CRU", "SM": "R", "FM": "R", "PRO": "R", "TOP": "R", "SYS": "R"},
	"orders":      {"SUP": "R", "PM": "RA", "STO": "R", "SM": "CRUA", "FM": "R", "PRO": "", "TOP": "R", "SYS": "R"},
	"delivery":    {"SUP": "R", "PM": "R", "STO": "CRU", "SM": "CRU", "FM": "R", "PRO": "", "TOP": "R", "SYS": "R"},
	"attendance":  {"SUP": "CRU", "PM": "RA", "STO": "", "SM": "", "FM": "R", "PRO": "", "TOP": "R", "SYS": "R"},
	"wages":       {"SUP": "", "PM": "RA", "STO": "", "SM": "", "FM": "RU", "PRO": "", "TOP": "R", "SYS": "R"},
	"finance":     {"SUP": "", "PM": "R", "STO": "", "SM": "R", "FM": "CRUA", "PRO": "R", "TOP": "RA", "SYS": "R"},
	"procurement": {"SUP": "CR", "PM": "RA", "STO": "R", "SM": "", "FM": "RA", "PRO": "CRU", "TOP": "RA", "SYS": "R"},
	"reports":     {"SUP": "R", "PM": "R", "STO": "R", "SM": "R", "FM": "R", "PRO": "R", "TOP": "R", "SYS": "R"},
	"admin":       {"SUP": "", "PM": "", "STO": "", "SM": "", "FM": "", "PRO": "", "TOP": "R", "SYS": "CRU"},
}

// srsColumn maps a role to its §8 column. LEAD_OPERATOR / OPERATOR are not §8 roles (checked against SUP below).
var srsColumn = map[Role]string{SUP: "SUP", PM: "PM", STO: "STO", SM: "SM", FM: "FM", PRO: "PRO", MD: "TOP", GM: "TOP", OA: "TOP", SYS: "SYS"}

// grant is one way to earn a permission: a §8 module and the letters of that cell that grant it.
type grant struct{ module, letters string }

// permGrants maps every permission to the §8 cells that justify it (any one is enough).
var permGrants = map[Permission][]grant{
	"production.view": {{"production", "R"}}, "production.record": {{"production", "CU"}}, "production.approve": {{"production", "A"}},
	"production.targets": {{"production", "A"}}, "production.close": {{"production", "A"}},
	"stock.view": {{"stock", "R"}}, "stock.adjust": {{"stock", "A"}}, "stock.record": {{"stock", "CU"}},
	"transfers.view": {{"stock", "R"}}, "transfers.request": {{"stock", "C"}}, "transfers.approve": {{"stock", "A"}},
	"transfers.dispatch": {{"stock", "CU"}}, "transfers.receive": {{"stock", "CU"}},
	"stockcount.record": {{"stock", "C"}}, "stockcount.approve": {{"stock", "A"}},
	"materials.view": {{"stock", "R"}}, "materials.record": {{"stock", "C"}}, "materials.adjust": {{"stock", "A"}},
	"sales.view": {{"orders", "R"}, {"delivery", "R"}}, "sales.create": {{"orders", "C"}}, "sales.confirm": {{"orders", "U"}},
	"sales.cancel": {{"orders", "A"}}, "sales.approve": {{"orders", "A"}}, "customers.manage": {{"orders", "CU"}},
	"deliveries.create": {{"delivery", "CU"}}, "payments.record": {{"finance", "C"}},
	"pettycash.view": {{"finance", "R"}}, "pettycash.record": {{"finance", "C"}}, "pettycash.approve": {{"finance", "A"}},
	"pettycash.custodian": {{"finance", "CU"}},
	"expenses.view":       {{"finance", "R"}}, "expenses.record": {{"finance", "C"}}, "expenses.approve": {{"finance", "A"}},
	// order payments are income records: Finance C, or Clients & orders C for Sales (ORD-06)
	"income.view": {{"finance", "R"}}, "income.record": {{"finance", "C"}, {"orders", "C"}}, "income.verify": {{"finance", "A"}},
	"budgets.view": {{"finance", "R"}}, "budgets.manage": {{"finance", "CU"}}, "budgets.approve": {{"finance", "A"}},
	"procurement.view": {{"procurement", "R"}}, "procurement.request": {{"procurement", "C"}},
	"procurement.approve": {{"procurement", "A"}}, "procurement.manage": {{"procurement", "CU"}},
	"procurement.receive": {{"stock", "C"}, {"procurement", "U"}}, // a goods receipt is a stock receipt
	"attendance.view":     {{"attendance", "R"}}, "attendance.record": {{"attendance", "C"}}, "attendance.approve": {{"attendance", "A"}},
	"payroll.view": {{"wages", "R"}}, "payroll.prepare": {{"wages", "U"}}, "payroll.check": {{"wages", "A"}},
	"payroll.verify": {{"wages", "U"}}, "payroll.authorize": {{"wages", "A"}, {"finance", "A"}}, // wages are PAYROLL expenses
	"payroll.pay": {{"wages", "U"}}, "workers.manage": {{"wages", "U"}}, "wages.view": {{"wages", "R"}},
	"reports.view": {{"reports", "R"}}, "reports.schedule": {{"admin", "CU"}},
	"sync.resolve": {{"production", "U"}, {"attendance", "U"}, {"stock", "A"}},
	"users.view":   {{"admin", "R"}}, "users.manage": {{"admin", "CU"}}, "masterdata.manage": {{"admin", "CU"}},
	"audit.view": {{"admin", "R"}}, "settings.manage": {{"admin", "CU"}}, "admin.monitor": {{"admin", "R"}},
	"data.export":    {{"admin", "R"}, {"finance", "A"}}, // ADM-05 "authorised users": administration + finance (FIN-07)
	"devices.manage": {{"admin", "CU"}},
}

func srsAllows(role Role, p Permission) bool {
	col := srsColumn[role]
	for _, g := range permGrants[p] {
		if strings.ContainsAny(srsMatrix[g.module][col], g.letters) {
			return true
		}
	}
	return false
}

// TestSRSRoleMatrix encodes SRS §8 cell by cell: (1) no role holds a permission its cells do not give it, and
// (2) every letter of every cell is backed by at least one permission. SUPER_ADMIN holds everything.
func TestSRSRoleMatrix(t *testing.T) {
	for _, r := range Matrix {
		if r.P == "dashboard.view" || r.P == "branding.manage" || r.P == "i18n.manage" {
			continue
		}
		if _, ok := permGrants[r.P]; !ok {
			t.Errorf("%s has no §8 mapping in permGrants", r.P)
		}
	}
	for role := range srsColumn {
		for _, r := range Matrix {
			if _, mapped := permGrants[r.P]; !mapped {
				continue
			}
			// Operations Advisor: Top Mgt column, read-only (spec §4) — never approves.
			want := srsAllows(role, r.P) && !(role == OA && !strings.HasSuffix(string(r.P), ".view") && r.P != "data.export" && r.P != "reports.view" && r.P != "audit.view")
			if Can(role, r.P) && !want {
				t.Errorf("over-grant: %s holds %s, not given by SRS §8", role, r.P)
			}
		}
		col := srsColumn[role]
		for module, cells := range srsMatrix {
			for _, letter := range cells[col] {
				if role == OA && letter == 'A' {
					continue
				}
				backed := false
				for p, gs := range permGrants {
					for _, g := range gs {
						if g.module == module && strings.ContainsRune(g.letters, letter) && Can(role, p) {
							backed = true
						}
					}
				}
				if !backed {
					t.Errorf("SRS §8 %s/%s has %c but %s holds no permission for it", module, col, letter, role)
				}
			}
		}
	}
	// Legacy site roles never exceed the supervisor.
	for _, role := range []Role{RoleLeadOperator, RoleOperator} {
		for _, p := range PermissionsFor(role) {
			if !Can(SUP, Permission(p)) {
				t.Errorf("%s holds %s which the supervisor does not", role, p)
			}
		}
	}
	// The cells the audit (9941a63) found different, now fixed.
	for _, c := range []struct {
		r    Role
		p    Permission
		want bool
	}{
		{SYS, "production.view", true}, {SM, "production.view", false}, {PRO, "stock.view", true}, {SYS, "stock.view", true},
		{PM, "sales.approve", true}, {GM, "sales.approve", false}, {SYS, "sales.view", true}, {SYS, "customers.manage", false},
		{STO, "deliveries.create", true}, {SUP, "deliveries.create", false}, {PM, "deliveries.create", false},
		{SUP, "payroll.prepare", false}, {SUP, "payroll.check", false}, {PM, "payroll.check", true}, {SUP, "workers.manage", false},
		{SYS, "attendance.view", true}, {PM, "expenses.view", true}, {SM, "expenses.view", true}, {SM, "income.view", true},
		{PRO, "income.view", true}, {SUP, "expenses.record", false}, {STO, "expenses.record", false}, {SUP, "pettycash.custodian", false},
		{SYS, "expenses.view", true}, {SYS, "procurement.view", true}, {SYS, "reports.view", true},
		{GM, "users.manage", false}, {GM, "users.view", true}, {MD, "settings.manage", false}, {GM, "devices.manage", false},
		{PM, "masterdata.manage", false},
	} {
		if Can(c.r, c.p) != c.want {
			t.Errorf("%s / %s: got %v want %v", c.r, c.p, Can(c.r, c.p), c.want)
		}
	}
}

func TestNewRoleMatrix(t *testing.T) {
	for _, role := range []Role{RoleStockOfficer, RoleProcurementOfficer} {
		if !role.Valid() || RoleLabels[role] == "" {
			t.Errorf("%s not registered", role)
		}
	}
	// Scoping: stock officer is pinned only when assigned a plant; procurement officer never.
	if IsPlantScoped(RoleStockOfficer) || !UserPlantScoped(RoleStockOfficer, "p1") || UserPlantScoped(RoleStockOfficer, "") {
		t.Error("stock officer optional scoping wrong")
	}
	if UserPlantScoped(RoleProcurementOfficer, "p1") {
		t.Error("procurement officer is an HQ role, never plant-scoped")
	}
	so := &User{Role: RoleStockOfficer, PlantID: "p1"}
	if PlantFilter(so, "p2") != "p1" || AssertPlantAccess(so, "p2") == nil {
		t.Error("assigned stock officer must be pinned")
	}
	central := &User{Role: RoleStockOfficer}
	if PlantFilter(central, "p2") != "p2" || AssertPlantAccess(central, "p2") != nil {
		t.Error("central stock officer sees all locations")
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
