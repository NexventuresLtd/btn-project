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
	// SRS §8 roles (migration 01300).
	RoleStockOfficer       Role = "STOCK_OFFICER"       // Stock / Warehouse Officer; site-scoped when assigned a plant
	RoleProcurementOfficer Role = "PROCUREMENT_OFFICER" // Procurement Officer; HQ (never site-scoped)
)

// AllRoles in display order (Super Administrator first).
var AllRoles = []Role{RoleSuperAdmin, RoleSystemAdmin, RoleManagingDirector, RoleGeneralManager, RoleOperationsAdvisor,
	RoleProductionManager, RoleSalesManager, RoleFinanceManager, RoleProcurementOfficer, RoleStockOfficer, RoleSupervisor,
	RoleLeadOperator, RoleOperator}

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
	STO  = RoleStockOfficer
	PRO  = RoleProcurementOfficer
)

// Matrix is the role → permission matrix ported from legacy lib/auth/permissions.ts (spec §4).
// SUPER_ADMIN is implicitly allowed everything (see Can). Order is preserved for /api/auth/me.
var Matrix = []permRule{
	{"dashboard.view", all},

	{"production.view", rr(MD, GM, OA, PM, SM, FM, SUP, LEAD, OP, STO)},
	{"production.record", rr(PM, SUP, LEAD)},
	{"production.approve", rr(PM, GM)},
	{"production.targets", rr(PM, GM)},
	{"production.close", rr(PM, GM)}, // close a day; approve late corrections

	{"stock.view", rr(MD, GM, OA, PM, SM, FM, SUP, LEAD, STO)},
	{"stock.adjust", rr(PM, GM)},
	{"stock.record", rr(STO, SUP, PM)}, // receipts / issues / adjustment requests at a location

	{"transfers.view", rr(PM, STO, SUP, LEAD, GM, MD, OA, FM, SM, PRO)},
	{"transfers.request", rr(SUP, STO, PM)},
	{"transfers.approve", rr(PM, GM)},
	{"transfers.dispatch", rr(STO, SUP, PM)},
	{"transfers.receive", rr(STO, SUP, PM)},

	{"stockcount.record", rr(STO, SUP, PM)},
	{"stockcount.approve", rr(PM, GM)},

	{"materials.view", rr(MD, GM, OA, PM, FM, SUP, LEAD, STO)},
	{"materials.record", rr(PM, FM, SUP)},
	{"materials.adjust", rr(PM, GM)},

	{"sales.view", rr(MD, GM, OA, PM, SM, FM, STO)},
	{"sales.create", rr(SM, GM)},
	{"sales.confirm", rr(SM, GM)},
	{"sales.cancel", rr(GM, MD)},
	{"sales.approve", rr(SM, GM, MD)}, // value-based order approval, amendments
	{"deliveries.create", rr(SM, PM, SUP)},
	{"payments.record", rr(FM)},
	{"customers.manage", rr(SM, GM, SYS)},

	{"pettycash.view", rr(MD, GM, OA, FM)},
	{"pettycash.record", rr(FM)},
	{"pettycash.approve", rr(GM, MD)},
	{"pettycash.custodian", rr(FM, SUP, PM)}, // hold a float, request top-ups, reconcile

	{"expenses.view", rr(FM, GM, MD, OA, PRO)},
	{"expenses.record", rr(FM, PM, SUP, STO)},
	{"expenses.approve", rr(FM, GM, MD)},

	// Income records (money in). Recorded, never collected by the system: FM records and verifies, Sales records the
	// payments customers made on orders, management reads.
	{"income.view", rr(FM, GM, MD, OA)},
	{"income.record", rr(FM, SM)},
	{"income.verify", rr(FM)},

	{"budgets.view", rr(FM, GM, MD, OA, PM)},
	{"budgets.manage", rr(FM)},
	{"budgets.approve", rr(GM, MD)}, // overrun approval

	{"procurement.view", rr(PM, FM, PRO, GM, MD, OA, STO)},
	{"procurement.request", rr(SUP, PM, STO, PRO, FM)},
	{"procurement.approve", rr(PM, FM, GM, MD)}, // level decided by approval thresholds
	{"procurement.manage", rr(PRO, FM)},         // suppliers, purchase orders
	{"procurement.receive", rr(STO, PRO, SUP, PM)},

	{"attendance.view", rr(MD, GM, OA, PM, FM, SUP, LEAD)},
	{"attendance.record", rr(PM, SUP, LEAD, FM)},
	{"attendance.approve", rr(PM, GM)}, // lock attendance, approve corrections

	{"payroll.view", rr(MD, GM, OA, PM, FM, SUP)},
	{"payroll.prepare", rr(SUP, FM, PM)},
	{"payroll.check", rr(PM, SUP)},
	{"payroll.verify", rr(FM)},
	{"payroll.authorize", rr(GM, MD)},
	{"payroll.pay", rr(FM)},
	{"workers.manage", rr(FM, PM, SUP)},
	{"wages.view", rr(FM, PM, GM, MD, OA)}, // per-worker payment history

	{"reports.view", rr(MD, GM, OA, PM, SM, FM, SUP, STO, PRO)},
	{"reports.schedule", rr(SYS, GM, MD)}, // scheduled report recipients

	{"sync.resolve", rr(PM, SUP, GM)}, // resolve sync conflicts / negative-stock flags (SUP: own site)

	{"users.manage", rr(SYS, GM, MD)},
	{"masterdata.manage", rr(SYS, GM, PM)},
	{"audit.view", rr(SYS, MD, GM, OA)},
	{"settings.manage", rr(SYS, MD)},
	{"admin.monitor", rr(SYS)},               // jobs, health, errors, sync monitor, devices
	{"data.export", rr(SYS, MD, GM, OA, FM)}, // bulk CSV/JSON export (ADM-05)
	{"devices.manage", rr(SYS, GM, MD)},      // revoke any user's devices

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

// IsPlantScoped roles must have a plant and may only see their own plant. SUPER_ADMIN is never plant scoped.
func IsPlantScoped(role Role) bool {
	return role == RoleSupervisor || role == RoleLeadOperator || role == RoleOperator
}

// IsOptionallyPlantScoped roles may be assigned a plant (location); when they are, they are pinned to it
// exactly like IsPlantScoped roles. Without a plant they work across all locations (e.g. a central warehouse
// officer). Only STOCK_OFFICER today; PROCUREMENT_OFFICER is an HQ role and never scoped.
func IsOptionallyPlantScoped(role Role) bool { return role == RoleStockOfficer }

// UserPlantScoped reports whether this user is pinned to one plant (role-scoped, or optionally scoped with a plant).
func UserPlantScoped(role Role, plantID string) bool {
	return IsPlantScoped(role) || (IsOptionallyPlantScoped(role) && plantID != "")
}

// RoleLabels are the English labels (localized via i18n keys "role.<ROLE>").
var RoleLabels = map[Role]string{
	RoleSuperAdmin:         "Super Administrator",
	RoleSystemAdmin:        "System Administrator",
	RoleManagingDirector:   "Managing Director",
	RoleGeneralManager:     "General Manager",
	RoleOperationsAdvisor:  "Operations Advisor",
	RoleProductionManager:  "Production Manager",
	RoleSalesManager:       "Marketing & Sales Manager",
	RoleFinanceManager:     "Administration & Finance Manager",
	RoleSupervisor:         "Supervisor",
	RoleLeadOperator:       "Lead Operator",
	RoleOperator:           "Operator",
	RoleStockOfficer:       "Stock / Warehouse Officer",
	RoleProcurementOfficer: "Procurement Officer",
}
