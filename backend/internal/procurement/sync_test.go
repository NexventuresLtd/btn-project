package procurement_test

import (
	"strings"
	"testing"
	"time"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/testutil"
)

func TestSyncProcurementRequest(t *testing.T) {
	e := setup(t)
	dc := e.SignInDevice(e.sup, "")
	payload := func(plant string) map[string]any {
		return map[string]any{"plantId": plant, "requiredDate": future(4), "justification": "Cement needed for the weekend shift", "submit": true,
			"lines": []any{map[string]any{"itemType": "MATERIAL", "materialId": e.cement, "quantity": 30, "estimatedUnitCost": 9000}}}
	}
	ok := apptest.SyncRecord("procurement_request", 0, payload(e.btn1))
	invalid := apptest.SyncRecord("procurement_request", 0, map[string]any{"plantId": e.btn1, "requiredDate": future(4), "justification": "x", "lines": []any{}})
	scope := apptest.SyncRecord("procurement_request", 0, payload(e.btn2))
	res := dc.Push(ok, invalid, scope)
	if res.Status != 200 || len(res.Results) != 3 {
		t.Fatalf("push: %+v", res)
	}
	if r := res.Results[0]; r.Status != "accepted" || r.ServerID != ok["uuid"] {
		t.Fatalf("accepted: %+v", r)
	}
	if r := res.Results[1]; r.Status != "rejected" || r.ReasonCode != "validation" || r.Fields["justification"] == "" {
		t.Fatalf("validation: %+v", r)
	}
	if r := res.Results[2]; r.Status != "rejected" || r.ReasonCode != "site_scope" {
		t.Fatalf("site scope: %+v", r)
	}
	// stored with the client uuid as id, submitted (approval online), approvers notified
	id := ok["uuid"].(string)
	r := e.call("GET", "/api/procurement/requests/"+id, nil, e.sup, 200)
	if r["status"] != "SUBMITTED" || r["source"] != "OFFLINE" || r["requestedBy"].(map[string]any)["id"] != e.sup.ID {
		t.Fatalf("synced request: %v", r)
	}
	if e.count(`SELECT count(*) FROM notification WHERE user_id = $1 AND event = 'procurement.request_submitted'`, e.pm.ID) != 1 {
		t.Fatal("approver notified after sync")
	}
	if e.count(`SELECT count(*) FROM "AuditLog" WHERE action = 'procurement.request_sync' AND "entityId" = $1 AND "deviceId" = $2`, id, dc.DeviceID) != 1 {
		t.Fatal("audit with device id")
	}
	// same record again → duplicate (idempotent)
	again := dc.Push(ok)
	if again.Results[0].Status != "duplicate" {
		t.Fatalf("duplicate: %+v", again.Results[0])
	}
	// a past required date is accepted when it is not before the capture date; before capture → rejected
	old := apptest.SyncRecord("procurement_request", 0, payload(e.btn1))
	old["capturedAt"] = time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339Nano)
	old["payload"].(map[string]any)["requiredDate"] = future(-3)
	if r := dc.Push(old).Results[0]; r.Status != "rejected" || r.Fields["requiredDate"] == "" {
		t.Fatalf("required before capture: %+v", r)
	}
	// a user without procurement.request cannot push this type
	op := e.SignInDevice(e.User(auth.RoleOperator, testutil.UserOpts{Plant: "BTN-1"}), "")
	if r := op.Push(apptest.SyncRecord("procurement_request", 0, payload(e.btn1))).Results[0]; r.Status != "rejected" || !strings.HasPrefix(r.ReasonCode, "permission") {
		t.Fatalf("permission: %+v", r)
	}
	// delta pull returns the request with its lines
	resp := dc.Do("GET", "/api/sync/pull?entities=procurement_requests", nil)
	var pull struct {
		Changes []struct {
			Entity string         `json:"entity"`
			ID     string         `json:"id"`
			Data   map[string]any `json:"data"`
		} `json:"changes"`
	}
	e.Decode(resp, &pull)
	found := false
	for _, c := range pull.Changes {
		if c.Entity == "procurement_requests" && c.ID == id {
			found = len(c.Data["lines"].([]any)) == 1 && c.Data["status"] == "SUBMITTED"
		}
	}
	if !found {
		t.Fatalf("pull: %+v", pull.Changes)
	}
	// another site's supervisor does not receive it
	other := e.SignInDevice(e.sup2, "")
	resp = other.Do("GET", "/api/sync/pull?entities=procurement_requests", nil)
	e.Decode(resp, &pull)
	for _, c := range pull.Changes {
		if c.ID == id {
			t.Fatal("pull leaked across sites")
		}
	}
}
