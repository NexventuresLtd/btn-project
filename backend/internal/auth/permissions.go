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

	// SRS §8 "Production": Sup. C R U · PM R U A · Stock R · Fin. R · Top Mgt R · Admin R (Sales, Proc. none).
	{"production.view", rr(MD, GM, OA, PM, FM, SUP, LEAD, OP, STO, SYS)},
	{"production.record", rr(PM, SUP, LEAD)}, // Sup. C U; PM U (edits)
	{"production.approve", rr(PM)},
	{"production.targets", rr(PM)},
	{"production.close", rr(PM)}, // close a day; approve late corrections

	// SRS §8 "Stock": Sup. C R (site) · PM R A · Stock C R U · Sales, Fin., Proc., Top Mgt, Admin R.
	{"stock.view", rr(MD, GM, OA, PM, SM, FM, SUP, LEAD, STO, PRO, SYS)},
	{"stock.adjust", rr(PM)},       // approve adjustments / opening balances
	{"stock.record", rr(STO, SUP)}, // receipts / issues / adjustment requests at a location
	{"transfers.view", rr(MD, GM, OA, PM, SM, FM, SUP, LEAD, STO, PRO, SYS)},
	{"transfers.request", rr(SUP, STO)}, // §7.2: requested by a site or HQ officer
	{"transfers.approve", rr(PM)},
	{"transfers.dispatch", rr(STO, SUP)},
	{"transfers.receive", rr(STO, SUP)},
	{"stockcount.record", rr(STO, SUP)},
	{"stockcount.approve", rr(PM)},
	{"materials.view", rr(MD, GM, OA, PM, SM, FM, SUP, LEAD, STO, PRO, SYS)},
	{"materials.record", rr(STO, SUP)},
	{"materials.adjust", rr(PM)},

	// SRS §8 "Clients & orders": Sup. R · PM R A · Stock R · Sales C R U A · Fin. R · Top Mgt R · Admin R.
	{"sales.view", rr(MD, GM, OA, PM, SM, FM, SUP, STO, SYS)},
	{"sales.create", rr(SM)},
	{"sales.confirm", rr(SM)},
	{"sales.cancel", rr(SM, PM)},  // approve a cancellation (ORD-04; never the requester)
	{"sales.approve", rr(SM, PM)}, // value-based order approval, amendments
	{"customers.manage", rr(SM)},
	// SRS §8 "Delivery": Stock C R U · Sales C R U · everyone else R (read through sales.view).
	{"deliveries.create", rr(SM, STO)},
	// Order payments are income records (Finance C R U A, docs/FINANCE_RECORDS_DESIGN.md).
	{"payments.record", rr(FM)},

	// SRS §8 "Finance": PM R · Sales R · Fin. C R U A · Proc. R · Top Mgt R A · Admin R (Sup., Stock none).
	{"pettycash.view", rr(MD, GM, OA, FM, PM, SM, PRO, SYS)},
	{"pettycash.record", rr(FM)},
	{"pettycash.approve", rr(GM, MD)},
	{"pettycash.custodian", rr(FM)}, // hold a float, request top-ups, reconcile
	{"expenses.view", rr(FM, GM, MD, OA, PM, SM, PRO, SYS)},
	{"expenses.record", rr(FM)},
	{"expenses.approve", rr(FM, GM, MD)},
	// Income records (money in). Recorded, never collected by the system: FM records and verifies, Sales records the
	// payments customers made on their orders (ORD-06, Clients & orders C R U A), everyone with Finance R reads.
	{"income.view", rr(FM, GM, MD, OA, PM, SM, PRO, SYS)},
	{"income.record", rr(FM, SM)},
	{"income.verify", rr(FM)},
	{"budgets.view", rr(FM, GM, MD, OA, PM, SM, PRO, SYS)},
	{"budgets.manage", rr(FM)},
	{"budgets.approve", rr(GM, MD)}, // overrun approval

	// SRS §8 "Procurement": Sup. C R · PM R A · Stock R · Fin. R A · Proc. C R U · Top Mgt R A · Admin R.
	{"procurement.view", rr(PM, FM, PRO, GM, MD, OA, STO, SUP, SYS)},
	{"procurement.request", rr(SUP, PRO)},
	{"procurement.approve", rr(PM, FM, GM, MD)}, // level decided by approval thresholds
	{"procurement.manage", rr(PRO)},             // suppliers, purchase orders
	{"procurement.receive", rr(STO, PRO, SUP)},  // goods receipts create stock receipts (Stock C R U, Sup. C)

	// SRS §8 "Attendance & wages": Sup. C R U (attendance) · PM R A · Fin. R U · Top Mgt R · Admin R.
	{"attendance.view", rr(MD, GM, OA, PM, FM, SUP, LEAD, SYS)},
	{"attendance.record", rr(SUP, LEAD)},
	{"attendance.approve", rr(PM)}, // lock attendance, approve corrections
	{"payroll.view", rr(MD, GM, OA, PM, FM, SYS)},
	{"payroll.prepare", rr(SUP)}, // §7.4/WRK-03: the site supervisor compiles the sheet from the attendance he recorded
	{"payroll.check", rr(PM)},    // §7.4: the calculation is reviewed by the Production Manager (only)
	{"payroll.verify", rr(FM)},
	{"payroll.authorize", rr(GM, MD)}, // wages are PAYROLL expenses: Finance "Top Mgt R A" (documented in docs/ROLES.md)
	{"payroll.pay", rr(FM)},
	{"workers.manage", rr(FM)},                  // worker records (WRK-01, personal data): edit, approve registrations
	{"workers.register", rr(SUP)},               // register a new casual worker at the own site, pending Finance approval
	{"wages.view", rr(FM, PM, GM, MD, OA, SYS)}, // per-worker payment history

	// SRS §8 "Reports": everyone R (supervisors their own site).
	{"reports.view", rr(MD, GM, OA, PM, SM, FM, SUP, STO, PRO, SYS)},
	{"reports.schedule", rr(SYS)}, // scheduled report recipients (a setting)

	{"sync.resolve", rr(PM, SUP)}, // §5.5: supervisor resolves own-site conflicts, PM reviews negative stock

	// SRS §8 "Users, settings, audit": Top Mgt R · Admin C R U.
	{"users.view", rr(SYS, MD, GM, OA)},
	{"users.manage", rr(SYS)},
	{"masterdata.manage", rr(SYS)},
	{"audit.view", rr(SYS, MD, GM, OA)},
	{"settings.manage", rr(SYS)},
	{"admin.monitor", rr(SYS)},               // jobs, health, errors, sync monitor, devices
	{"data.export", rr(SYS, MD, GM, OA, FM)}, // bulk CSV/JSON export (ADM-05: authorised users)
	{"devices.manage", rr(SYS)},              // revoke any user's devices

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
