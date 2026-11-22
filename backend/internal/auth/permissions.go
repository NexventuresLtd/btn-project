package auth

// Role mirrors the Postgres "Role" enum.
type Role string

const (
	RoleSuperAdmin        Role = "SUPER_ADMIN"
	RoleSystemAdmin       Role = "SYSTEM_ADMIN"
	RoleManagingDirector  Role = "MANAGING_DIRECTOR"
	RoleGeneralManager    Role = "GENERAL_MANAGER"
	RoleOperationsAdvisor Role = "OPERATIONS_ADVISOR"
	RoleProductionManager Role = "PRODUCTION_MANAGER"
	RoleSalesManager      Role = "SALES_MANAGER"
	RoleFinanceManager    Role = "FINANCE_MANAGER"
	RoleSupervisor        Role = "SUPERVISOR"
	RoleLeadOperator      Role = "LEAD_OPERATOR"
	RoleOperator          Role = "OPERATOR"
)

// AllRoles in display order (Super Administrator first).
var AllRoles = []Role{RoleSuperAdmin, RoleSystemAdmin, RoleManagingDirector, RoleGeneralManager, RoleOperationsAdvisor,
	RoleProductionManager, RoleSalesManager, RoleFinanceManager, RoleSupervisor, RoleLeadOperator, RoleOperator}

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	for _, x := range AllRoles {
		if x == r {
			return true
		}
	}
	return false
}

// Permission is a dotted permission name, e.g. "production.view".
type Permission string

var all = []Role{"*"} // sentinel: every role

type permRule struct {
	P     Permission
	Roles []Role
}

func rr(r ...Role) []Role { return r }

const (
	MD   = RoleManagingDirector
	GM   = RoleGeneralManager
	OA   = RoleOperationsAdvisor
	PM   = RoleProductionManager
	SM   = RoleSalesManager
	FM   = RoleFinanceManager
	SUP  = RoleSupervisor
	LEAD = RoleLeadOperator
	OP   = RoleOperator
	SYS  = RoleSystemAdmin
)

// Matrix is the role → permission matrix ported from legacy lib/auth/permissions.ts (spec §4).
// SUPER_ADMIN is implicitly allowed everything (see Can). Order is preserved for /api/auth/me.
var Matrix = []permRule{
	{"dashboard.view", all},

	{"production.view", rr(MD, GM, OA, PM, SM, FM, SUP, LEAD, OP)},
	{"production.record", rr(PM, SUP, LEAD)},
	{"production.approve", rr(PM, GM)},

	{"stock.view", rr(MD, GM, OA, PM, SM, FM, SUP, LEAD)},
	{"stock.adjust", rr(PM, GM)},

	{"materials.view", rr(MD, GM, OA, PM, FM, SUP, LEAD)},
	{"materials.record", rr(PM, FM, SUP)},
	{"materials.adjust", rr(PM, GM)},

	{"sales.view", rr(MD, GM, OA, PM, SM, FM)},
	{"sales.create", rr(SM, GM)},
	{"sales.confirm", rr(SM, GM)},
	{"sales.cancel", rr(GM, MD)},
	{"deliveries.create", rr(SM, PM, SUP)},
	{"payments.record", rr(FM)},
	{"customers.manage", rr(SM, GM, SYS)},

	{"pettycash.view", rr(MD, GM, OA, FM)},
	{"pettycash.record", rr(FM)},
	{"pettycash.approve", rr(GM, MD)},

	{"attendance.view", rr(MD, GM, OA, PM, FM, SUP, LEAD)},
	{"attendance.record", rr(PM, SUP, LEAD, FM)},

	{"payroll.view", rr(MD, GM, OA, PM, FM, SUP)},
	{"payroll.prepare", rr(SUP, FM, PM)},
	{"payroll.check", rr(PM, SUP)},
	{"payroll.verify", rr(FM)},
	{"payroll.authorize", rr(GM, MD)},
	{"payroll.pay", rr(FM)},
	{"workers.manage", rr(FM, PM, SUP)},

	{"reports.view", rr(MD, GM, OA, PM, SM, FM, SUP)},

	{"users.manage", rr(SYS, GM, MD)},
	{"masterdata.manage", rr(SYS, GM, PM)},
	{"audit.view", rr(SYS, MD, GM, OA)},
	{"settings.manage", rr(SYS, MD)},

	// Super Administrator only (the empty role list means nobody else holds them).
	{"branding.manage", rr()},
	{"i18n.manage", rr()},
}

var permIndex = func() map[Permission][]Role {
	m := make(map[Permission][]Role, len(Matrix))
	for _, r := range Matrix {
		m[r.P] = r.Roles
	}
	return m
}()

// Known reports whether the permission exists in the matrix.
func Known(p Permission) bool { _, ok := permIndex[p]; return ok }

// Can reports whether role holds permission. SUPER_ADMIN holds every permission, including future ones.
func Can(role Role, p Permission) bool {
	if role == RoleSuperAdmin {
		return true
	}
	roles, ok := permIndex[p]
	if !ok {
		return false
	}
	if len(roles) == 1 && roles[0] == "*" {
		return true
	}
	for _, r := range roles {
		if r == role {
			return true
		}
	}
	return false
}

// PermissionsFor lists the permissions a role holds (matrix order).
func PermissionsFor(role Role) []string {
	out := make([]string, 0, len(Matrix))
	for _, r := range Matrix {
		if Can(role, r.P) {
			out = append(out, string(r.P))
		}
	}
	return out
}

// IsPlantScoped roles may only see their own plant. SUPER_ADMIN is never plant scoped.
func IsPlantScoped(role Role) bool {
	return role == RoleSupervisor || role == RoleLeadOperator || role == RoleOperator
}

// RoleLabels are the English labels (localized via i18n keys "role.<ROLE>").
var RoleLabels = map[Role]string{
	RoleSuperAdmin:        "Super Administrator",
	RoleSystemAdmin:       "System Administrator",
	RoleManagingDirector:  "Managing Director",
	RoleGeneralManager:    "General Manager",
	RoleOperationsAdvisor: "Operations Advisor",
	RoleProductionManager: "Production Manager",
	RoleSalesManager:      "Marketing & Sales Manager",
	RoleFinanceManager:    "Administration & Finance Manager",
	RoleSupervisor:        "Supervisor",
	RoleLeadOperator:      "Lead Operator",
	RoleOperator:          "Operator",
}
