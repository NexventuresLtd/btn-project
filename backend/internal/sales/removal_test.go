package sales_test

import (
	"strings"
	"testing"

	"btn-ops/internal/auth"
)

// SRS §6.3: delivery charge rules are removed with a reason (kept for the audit of past prices), never deleted;
// replaced contact rows are archived in deleted_record, which is append-only.
func TestRemovalKeepsRecords(t *testing.T) {
	f := setup(t)
	e := f.e
	admin := e.User(auth.RoleSuperAdmin) // masterdata.manage holder whatever the role matrix
	rule := status(t, e, "POST", "/api/sales/delivery-charges", M{"name": "TEST old flat fee", "method": "FIXED", "amount": 25000}, admin, 201)["rule"].(M)
	id := rule["id"].(string)
	if r := e.Do("DELETE", "/api/sales/delivery-charges/"+id, nil, admin); r.StatusCode != 400 {
		t.Fatalf("remove without reason: %d", r.StatusCode)
	}
	status(t, e, "DELETE", "/api/sales/delivery-charges/"+id, M{"reason": "TEST replaced by zone pricing"}, admin, 200)
	if r := e.Do("DELETE", "/api/sales/delivery-charges/"+id, M{"reason": "again"}, admin); r.StatusCode != 404 {
		t.Fatalf("remove twice: %d", r.StatusCode)
	}
	if r := e.Do("PATCH", "/api/sales/delivery-charges/"+id, M{"amount": 1}, admin); r.StatusCode != 404 {
		t.Fatalf("edit a removed rule: %d", r.StatusCode)
	}
	for _, it := range status(t, e, "GET", "/api/sales/delivery-charges", nil, f.sm, 200)["items"].([]any) {
		if it.(M)["id"] == id {
			t.Fatal("removed rule still listed")
		}
	}
	if q := status(t, e, "GET", "/api/sales/delivery-charges/quote", nil, f.sm, 200); num(q["amount"]) != 0 {
		t.Fatalf("removed rule still prices orders: %v", q)
	}
	rem := status(t, e, "GET", "/api/sales/delivery-charges/removed", nil, f.sm, 200)["items"].([]any)
	if len(rem) != 1 || rem[0].(M)["id"] != id || rem[0].(M)["reason"] != "TEST replaced by zone pricing" {
		t.Fatalf("removed history: %v", rem)
	}
	if n := f.count(`SELECT count(*) FROM "AuditLog" WHERE action='sales.charge.remove' AND "entityId"=$1 AND "after"->>'reason'='TEST replaced by zone pricing'`, id); n != 1 {
		t.Fatalf("remove audit: %d", n)
	}
	if _, err := e.Pool.Exec(t.Context(), `DELETE FROM delivery_charge_rule WHERE id=$1`, id); err == nil || !strings.Contains(err.Error(), "never deleted") {
		t.Fatalf("raw delete must be refused: %v", err)
	}

	// contacts replaced on a customer edit: the old rows go to the archive
	cu := status(t, e, "POST", "/api/sales/customers", M{"name": "TEST archive customer", "clientType": "COMPANY",
		"contacts": []M{{"name": "Old Contact", "phone": "0788000001"}}}, f.sm, 201)
	cid := cu["customer"].(M)["id"].(string)
	status(t, e, "PATCH", "/api/sales/customers/"+cid, M{"contacts": []M{{"name": "New Contact", "phone": "0788000002"}}}, f.sm, 200)
	if n := f.count(`SELECT count(*) FROM deleted_record WHERE table_name='customer_contact' AND row_data->>'customer_id'=$1 AND row_data->>'name'='Old Contact'`, cid); n != 1 {
		t.Fatalf("archived contact: %d", n)
	}
	if _, err := e.Pool.Exec(t.Context(), `UPDATE deleted_record SET row_data='{}'`); err == nil {
		t.Fatal("deleted_record must be append-only (update)")
	}
	if _, err := e.Pool.Exec(t.Context(), `DELETE FROM deleted_record`); err == nil {
		t.Fatal("deleted_record must be append-only (delete)")
	}
}

// Every table of the §6.3 policy carries its trigger: archive for child rows replaced with their parent,
// refusal for top-level business records (they are removed / voided with a reason instead).
func TestNoHardDeleteTriggersInstalled(t *testing.T) {
	f := setup(t)
	check := func(trigger string, tables ...string) {
		for _, tb := range tables {
			if n := f.count(`SELECT count(*) FROM pg_trigger WHERE tgname=$1 AND tgrelid=to_regclass(format('%I', $2::text))`, trigger, tb); n != 1 {
				t.Errorf("%s on %s: %d", trigger, tb, n)
			}
		}
	}
	check("btn_archive_deleted", "SaleLine", "purchase_order_line", "procurement_request_line", "budget_line", "customer_contact",
		"ProductionLine", "ProductionMaterialUsage", "production_exception", "production_recipe_item", "PayrollLine", "expense_approval", "stock_reorder_level")
	check("btn_forbid_delete", "budget", "approval_threshold", "delivery_charge_rule", "income_attachment", "expense_attachment",
		"production_target", "production_recipe", "piece_work_entry")
}
