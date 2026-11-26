package syncsvc_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/syncsvc"
	"btn-ops/internal/testutil"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestMain(m *testing.M) { testutil.Main(m) }

// ── test-only record type "test_note" (real types belong to the phase-2 owners) ──

var (
	applyMu    sync.Mutex
	applyOrder []int64
	reviewed   []syncsvc.ReviewItem
)

type notePayload struct {
	PlantID  string `json:"plantId"`
	Text     string `json:"text"`
	Fail     string `json:"fail"`     // "reject" | "panic" | "sqlerror" | "conflict" | "negative"
	ServerVs int64  `json:"serverVs"` // for conflict
}

func ensureNoteTable(t *testing.T, e *apptest.Env) {
	t.Helper()
	if _, err := e.Pool.Exec(t.Context(), `CREATE TABLE IF NOT EXISTS t_sync_note (id TEXT PRIMARY KEY, plant_id TEXT, body TEXT NOT NULL, created_by TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Pool.Exec(t.Context(), `SELECT btn_enable_sync_version('t_sync_note'::regclass, 'test_notes')`); err != nil {
		t.Fatal(err)
	}
}

func registerNote() {
	syncsvc.RegisterHandler("test_note", syncsvc.HandlerFunc(func(ctx context.Context, tx pgx.Tx, rec syncsvc.Record, a syncsvc.Actor) syncsvc.Result {
		var p notePayload
		if err := rec.Decode(&p); err != nil {
			return syncsvc.Reject(syncsvc.ReasonValidation, err.Error())
		}
		applyMu.Lock()
		applyOrder = append(applyOrder, rec.Seq)
		applyMu.Unlock()
		if r := a.AssertPlant(p.PlantID); r != nil {
			return *r
		}
		v, err := rec.Body(a.Lang)
		if err != nil {
			return syncsvc.FromError(a.Lang, err)
		}
		v.Str("text").Trim().Min(1, "v.required")
		if err := v.Err(); err != nil {
			return syncsvc.FromError(a.Lang, err)
		}
		// Write first, so rollback isolation is observable.
		var ver int64
		if err := tx.QueryRow(ctx, `INSERT INTO t_sync_note (id, plant_id, body, created_by) VALUES ($1,$2,$3,$4)
			ON CONFLICT (id) DO UPDATE SET body=EXCLUDED.body RETURNING server_version`,
			rec.UUID+fmt.Sprint(rec.Revision), p.PlantID, p.Text, a.UserID).Scan(&ver); err != nil {
			return syncsvc.FromError(a.Lang, err)
		}
		switch p.Fail {
		case "reject":
			return syncsvc.Reject(syncsvc.ReasonValidation, "rejected on purpose")
		case "panic":
			panic("boom")
		case "sqlerror":
			if _, err := tx.Exec(ctx, `INSERT INTO t_sync_note (id, body) VALUES ($1,'x')`, rec.UUID+fmt.Sprint(rec.Revision)); err != nil {
				return syncsvc.FromError(a.Lang, errors.New("duplicate key"))
			}
		case "conflict":
			if !rec.Force {
				return syncsvc.Conflict(ctx, tx, rec, a, syncsvc.ConflictInput{Entity: "Note", EntityID: "n1", ServerData: map[string]any{"text": "server"}, ServerVersion: p.ServerVs, Reason: "changed on server"})
			}
		case "negative":
			r := syncsvc.Accept(rec.UUID, ver, syncsvc.FlagNegativeStock)
			r.Entity, r.EntityID, r.Reason = "Note", rec.UUID, "balance would be -5"
			return r
		}
		return syncsvc.Accept(rec.UUID, ver)
	}), syncsvc.Requires("production.record"), syncsvc.SchemaVersions(1, 2))
	syncsvc.OnReview = func(ctx context.Context, tx pgx.Tx, it syncsvc.ReviewItem) error {
		applyMu.Lock()
		reviewed = append(reviewed, it)
		applyMu.Unlock()
		return nil
	}
	syncsvc.RegisterPull("test_notes", func(ctx context.Context, a syncsvc.Actor, since syncsvc.Cursor) ([]syncsvc.Change, error) {
		return syncsvc.Rows(ctx, a, since, "test_notes", `SELECT n.id, to_jsonb(n) - 'server_xid', n.server_version, n.server_xid FROM t_sync_note n
			WHERE ($1 = '' OR n.plant_id = $1)`, a.ScopedPlantID())
	})
}

type fixture struct {
	e     *apptest.Env
	sup   *testutil.TestUser
	dc    *apptest.DeviceClient
	plant string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	e := apptest.New(t)
	ensureNoteTable(t, e)
	registerNote()
	applyMu.Lock()
	applyOrder, reviewed = nil, nil
	applyMu.Unlock()
	sup := e.User(auth.RoleSupervisor) // BTN-1, holds production.record
	dc := e.SignInDevice(sup, "")
	return &fixture{e: e, sup: sup, dc: dc, plant: sup.PlantID}
}

func note(seq int64, plant, text string, extra ...string) map[string]any {
	p := map[string]any{"plantId": plant, "text": text}
	if len(extra) > 0 {
		p["fail"] = extra[0]
	}
	return apptest.SyncRecord("test_note", seq, p)
}

func count(t *testing.T, e *apptest.Env, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.Pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPushIdempotencyAndDuplicates(t *testing.T) {
	f := setup(t)
	r := note(0, f.plant, "hello")
	res := f.dc.Push(r)
	if res.Status != 200 || res.Results[0].Status != "accepted" || res.Results[0].ServerID != r["uuid"] || res.Results[0].ServerVersion == 0 {
		t.Fatalf("first push: %+v", res)
	}
	again := f.dc.Push(r) // same uuid + revision
	g := again.Results[0]
	if g.Status != "duplicate" || g.OriginalStatus != "accepted" || g.ServerID != res.Results[0].ServerID || g.ServerVersion != res.Results[0].ServerVersion {
		t.Fatalf("duplicate: %+v", g)
	}
	if n := count(t, f.e, `SELECT count(*) FROM sync_ledger WHERE record_uuid=$1`, r["uuid"]); n != 1 {
		t.Fatalf("ledger rows %d", n)
	}
	if n := count(t, f.e, `SELECT count(*) FROM t_sync_note`); n != 1 {
		t.Fatalf("note rows %d", n)
	}
	if n := count(t, f.e, `SELECT duplicate_count FROM sync_ledger WHERE record_uuid=$1`, r["uuid"]); n != 1 {
		t.Fatalf("duplicate count %d", n)
	}
	// Same uuid twice inside one batch.
	r2 := note(0, f.plant, "twice")
	r2b := map[string]any{}
	for k, v := range r2 {
		r2b[k] = v
	}
	r2b["seq"] = int64(0)
	res = f.dc.Push(r2, r2b)
	if res.Results[0].Status != "accepted" || res.Results[1].Status != "duplicate" {
		t.Fatalf("in-batch duplicate: %+v", res.Results)
	}
	// Higher revision after acceptance → duplicate already_accepted; a rejected record can be resubmitted.
	r["revision"], r["seq"] = 2, int64(0)
	res = f.dc.Push(r)
	if res.Results[0].Status != "duplicate" || res.Results[0].ReasonCode != syncsvc.ReasonAlreadyAccepted {
		t.Fatalf("revision after accept: %+v", res.Results[0])
	}
	bad := note(0, f.plant, "")
	res = f.dc.Push(bad)
	if res.Results[0].Status != "rejected" || res.Results[0].ReasonCode != syncsvc.ReasonValidation || res.Results[0].Fields["text"] == "" {
		t.Fatalf("validation: %+v", res.Results[0])
	}
	bad["revision"], bad["seq"] = 2, int64(0)
	bad["payload"] = map[string]any{"plantId": f.plant, "text": "corrected"}
	res = f.dc.Push(bad)
	if res.Results[0].Status != "accepted" || res.Results[0].Revision != 2 {
		t.Fatalf("resubmission: %+v", res.Results[0])
	}
	// Records endpoint shows the latest revision state.
	resp := f.dc.Do("GET", "/api/sync/records?uuids="+bad["uuid"].(string), nil)
	m := f.e.Map(resp)
	items := m["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["status"] != "accepted" || items[0].(map[string]any)["revision"].(float64) != 2 {
		t.Fatalf("records: %v", m)
	}
}

func TestPushOrderingGapsAndLate(t *testing.T) {
	f := setup(t)
	res := f.dc.Push(note(3, f.plant, "c"), note(1, f.plant, "a"), note(2, f.plant, "b"))
	if res.Status != 200 {
		t.Fatalf("push %d", res.Status)
	}
	applyMu.Lock()
	order := append([]int64{}, applyOrder...)
	applyMu.Unlock()
	if fmt.Sprint(order) != "[1 2 3]" {
		t.Fatalf("applied out of order: %v", order)
	}
	for i, r := range res.Results { // results in request order
		if r.Status != "accepted" || len(r.Flags) != 0 {
			t.Fatalf("result %d: %+v", i, r)
		}
	}
	res = f.dc.Push(note(6, f.plant, "gap"))
	if res.Results[0].Status != "accepted" || fmt.Sprint(res.Results[0].Flags) != "[SEQ_GAP]" {
		t.Fatalf("gap: %+v", res.Results[0])
	}
	res = f.dc.Push(note(4, f.plant, "late"))
	if res.Results[0].Status != "accepted" || fmt.Sprint(res.Results[0].Flags) != "[SEQ_LATE]" {
		t.Fatalf("late: %+v", res.Results[0])
	}
}

func TestPushSavepointIsolation(t *testing.T) {
	f := setup(t)
	res := f.dc.Push(note(0, f.plant, "ok1"), note(0, f.plant, "x", "reject"), note(0, f.plant, "x", "panic"),
		note(0, f.plant, "x", "sqlerror"), note(0, f.plant, "ok2"), apptest.SyncRecord("no_such_type", 0, map[string]any{}))
	want := []string{"accepted", "rejected", "rejected", "rejected", "accepted", "rejected"}
	for i, r := range res.Results {
		if r.Status != want[i] {
			t.Fatalf("result %d: %+v", i, r)
		}
	}
	if res.Results[2].ReasonCode != syncsvc.ReasonHandlerError || res.Results[5].ReasonCode != syncsvc.ReasonUnknownType {
		t.Fatalf("reason codes: %+v", res.Results)
	}
	if n := count(t, f.e, `SELECT count(*) FROM t_sync_note`); n != 2 {
		t.Fatalf("only the accepted records may persist, got %d rows", n)
	}
	if n := count(t, f.e, `SELECT count(*) FROM sync_ledger WHERE status='rejected'`); n != 4 {
		t.Fatalf("rejected ledger rows %d", n)
	}
	// Invalid records: bad uuid → no ledger row; bad captured time → rejected invalid_record.
	r1 := note(0, f.plant, "x")
	r1["uuid"] = "not-a-uuid"
	r2 := note(0, f.plant, "x")
	r2["capturedAt"] = "yesterday"
	res = f.dc.Push(r1, r2)
	if res.Results[0].ReasonCode != syncsvc.ReasonInvalidRecord || res.Results[1].ReasonCode != syncsvc.ReasonInvalidRecord {
		t.Fatalf("invalid: %+v", res.Results)
	}
}

func TestClockSkewFlags(t *testing.T) {
	f := setup(t)
	future := note(0, f.plant, "future")
	future["capturedAt"] = time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339)
	normal := note(0, f.plant, "now")
	res := f.dc.Push(future, normal)
	if fmt.Sprint(res.Results[0].Flags) != "[CLOCK_SKEW]" || len(res.Results[1].Flags) != 0 {
		t.Fatalf("capture skew: %+v", res.Results)
	}
	// Device clock 30 min fast: every record of the batch is flagged and the device skew is recorded.
	resp := f.dc.Do("POST", "/api/sync/push", map[string]any{"deviceId": f.dc.DeviceID, "clientTime": time.Now().UTC().Add(30 * time.Minute).Format(time.RFC3339),
		"pending": 3, "records": []any{note(10, f.plant, "a"), note(11, f.plant, "b")}})
	var out apptest.PushResponse
	f.e.Decode(resp, &out)
	if out.ClockSkewSeconds < 1700 || !has(out.Results[0].Flags, "CLOCK_SKEW") || !has(out.Results[1].Flags, "CLOCK_SKEW") {
		t.Fatalf("device skew: %+v", out)
	}
	var skew, pending int
	_ = f.e.Pool.QueryRow(t.Context(), `SELECT clock_skew_seconds, pending_count FROM device WHERE device_id=$1`, f.dc.DeviceID).Scan(&skew, &pending)
	if skew < 1700 || pending != 3 {
		t.Fatalf("device row skew=%d pending=%d", skew, pending)
	}
	// Threshold is the setting: raise it to 60 min and the same clock is fine.
	admin := f.e.User(auth.RoleSystemAdmin)
	if r := f.e.Do("PUT", "/api/sync/settings", map[string]any{"maxClockSkewMinutes": 60, "offlineUnlockDays": 14}, admin); r.StatusCode != 200 {
		t.Fatalf("settings: %d %s", r.StatusCode, f.e.Text(r))
	}
	resp = f.dc.Do("POST", "/api/sync/push", map[string]any{"deviceId": f.dc.DeviceID, "clientTime": time.Now().UTC().Add(30 * time.Minute).Format(time.RFC3339),
		"records": []any{note(12, f.plant, "c")}})
	f.e.Decode(resp, &out)
	if has(out.Results[0].Flags, "CLOCK_SKEW") {
		t.Fatalf("skew within the raised threshold: %+v", out.Results[0])
	}
}

func has(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func TestRevalidationCaptureRoleAndCurrentRules(t *testing.T) {
	f := setup(t)
	if r := f.dc.Push(note(0, f.plant, "ok")); r.Results[0].Status != "accepted" {
		t.Fatalf("baseline: %+v", r.Results[0])
	}
	// Site scope: another site's record is rejected.
	var other string
	_ = f.e.Pool.QueryRow(t.Context(), `SELECT id FROM "Plant" WHERE code='BTN-2'`).Scan(&other)
	if r := f.dc.Push(note(0, other, "x")); r.Results[0].ReasonCode != syncsvc.ReasonSiteScope {
		t.Fatalf("site scope: %+v", r.Results[0])
	}
	// Permission removed after capture: current rules reject.
	if _, err := f.e.Pool.Exec(t.Context(), `UPDATE "User" SET role='OPERATOR' WHERE id=$1`, f.sup.ID); err != nil {
		t.Fatal(err)
	}
	f.e.Deps.Auth.InvalidateUser(f.sup.ID)
	if r := f.dc.Push(note(0, f.plant, "x")); r.Results[0].Status != "rejected" || r.Results[0].ReasonCode != syncsvc.ReasonPermissionRevoked {
		t.Fatalf("revoked: %+v", r.Results[0])
	}
	// Role at capture time lacked it (snapshot = OPERATOR) although the current role has it.
	if _, err := f.e.Pool.Exec(t.Context(), `UPDATE "User" SET role='SUPERVISOR' WHERE id=$1`, f.sup.ID); err != nil {
		t.Fatal(err)
	}
	f.e.Deps.Auth.InvalidateUser(f.sup.ID)
	snap, _ := json.Marshal(auth.PermSnapshot{Role: auth.RoleOperator, PlantID: f.plant, Permissions: auth.PermissionsFor(auth.RoleOperator)})
	if _, err := f.e.Pool.Exec(t.Context(), `UPDATE device SET perm_snapshot=$2 WHERE device_id=$1`, f.dc.DeviceID, snap); err != nil {
		t.Fatal(err)
	}
	if r := f.dc.Push(note(0, f.plant, "x")); r.Results[0].Status != "rejected" || r.Results[0].ReasonCode != syncsvc.ReasonPermissionDenied {
		t.Fatalf("capture-time denial: %+v", r.Results[0])
	}
}

func TestConflictCreationAndResolution(t *testing.T) {
	f := setup(t)
	c1 := note(0, f.plant, "client text", "conflict")
	c2 := note(0, f.plant, "client text 2", "conflict")
	res := f.dc.Push(c1, c2)
	if res.Results[0].Status != "conflict" || res.Results[0].ConflictID == "" || res.Results[0].ReasonCode != "conflict" {
		t.Fatalf("conflict: %+v", res.Results[0])
	}
	if n := count(t, f.e, `SELECT count(*) FROM t_sync_note`); n != 2 {
		t.Fatalf("conflict savepoint keeps the handler's writes: %d", n)
	}
	// Supervisor of BTN-1 (holds sync.resolve) lists and opens it; a BTN-2 supervisor cannot see it.
	resolver := f.e.User(auth.RoleSupervisor)
	sup2 := f.e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-2"})
	list := f.e.Map(f.e.Do("GET", "/api/sync/conflicts?kind=conflict", nil, resolver))
	if list["total"].(float64) != 2 {
		t.Fatalf("list: %v", list)
	}
	if n := f.e.Map(f.e.Do("GET", "/api/sync/conflicts", nil, sup2))["total"].(float64); n != 0 {
		t.Fatalf("other site sees %v", n)
	}
	id := res.Results[0].ConflictID
	if r := f.e.Do("GET", "/api/sync/conflicts/"+id, nil, sup2); r.StatusCode != 404 {
		t.Fatalf("other site detail: %d", r.StatusCode)
	}
	detail := f.e.Map(f.e.Do("GET", "/api/sync/conflicts/"+id, nil, resolver))["conflict"].(map[string]any)
	if detail["serverData"].(map[string]any)["text"] != "server" || detail["clientPayload"].(map[string]any)["text"] != "client text" {
		t.Fatalf("both versions: %v", detail)
	}
	// Denials and validation.
	op := f.e.User(auth.RoleOperator)
	if r := f.e.Do("GET", "/api/sync/conflicts", nil, op); r.StatusCode != 403 {
		t.Fatalf("operator: %d", r.StatusCode)
	}
	if r := f.e.Do("POST", "/api/sync/conflicts/"+id+"/resolve", map[string]any{"resolution": "acknowledge", "note": "fine"}, resolver); r.StatusCode != 400 {
		t.Fatalf("acknowledge on conflict: %d", r.StatusCode)
	}
	if r := f.e.Do("POST", "/api/sync/conflicts/"+id+"/resolve", map[string]any{"resolution": "keep_client", "note": ""}, resolver); r.StatusCode != 400 {
		t.Fatalf("note required: %d", r.StatusCode)
	}
	// keep_client re-applies the handler with Force → ledger becomes accepted.
	r := f.e.Do("POST", "/api/sync/conflicts/"+id+"/resolve", map[string]any{"resolution": "keep_client", "note": "client is right"}, resolver)
	if r.StatusCode != 200 {
		t.Fatalf("resolve: %d %s", r.StatusCode, f.e.Text(r))
	}
	var st string
	_ = f.e.Pool.QueryRow(t.Context(), `SELECT status FROM sync_ledger WHERE record_uuid=$1`, c1["uuid"]).Scan(&st)
	if st != "accepted" {
		t.Fatalf("ledger after keep_client: %s", st)
	}
	if r := f.e.Do("POST", "/api/sync/conflicts/"+id+"/resolve", map[string]any{"resolution": "keep_server", "note": "again"}, resolver); r.StatusCode != 409 {
		t.Fatalf("double resolve: %d", r.StatusCode)
	}
	// keep_server leaves the ledger as conflict, resolution recorded and audited.
	id2 := res.Results[1].ConflictID
	if r := f.e.Do("POST", "/api/sync/conflicts/"+id2+"/resolve", map[string]any{"resolution": "keep_server", "note": "server wins"}, resolver); r.StatusCode != 200 {
		t.Fatalf("keep_server: %d", r.StatusCode)
	}
	if n := count(t, f.e, `SELECT count(*) FROM "AuditLog" WHERE action='sync.conflict_resolve'`); n != 2 {
		t.Fatalf("audit rows %d", n)
	}
	if n := count(t, f.e, `SELECT count(*) FROM sync_conflict WHERE status='open'`); n != 0 {
		t.Fatalf("open conflicts %d", n)
	}
}

func TestNegativeStockFlagRoutesToReview(t *testing.T) {
	f := setup(t)
	res := f.dc.Push(note(0, f.plant, "neg", "negative"))
	r := res.Results[0]
	if r.Status != "accepted" || !has(r.Flags, syncsvc.FlagNegativeStock) {
		t.Fatalf("negative: %+v", r)
	}
	var kind, plant string
	if err := f.e.Pool.QueryRow(t.Context(), `SELECT kind, plant_id FROM sync_conflict WHERE record_uuid=$1`, r.UUID).Scan(&kind, &plant); err != nil || kind != "negative_stock" || plant != f.plant {
		t.Fatalf("review row: %v %s %s", err, kind, plant)
	}
	applyMu.Lock()
	n := len(reviewed)
	applyMu.Unlock()
	if n != 1 {
		t.Fatalf("review hook calls %d", n)
	}
	pm := f.e.User(auth.RoleProductionManager)
	list := f.e.Map(f.e.Do("GET", "/api/sync/conflicts?kind=negative_stock", nil, pm))
	items := list["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("review queue: %v", list)
	}
	id := items[0].(map[string]any)["id"].(string)
	if resp := f.e.Do("POST", "/api/sync/conflicts/"+id+"/resolve", map[string]any{"resolution": "keep_client", "note": "x x x"}, pm); resp.StatusCode != 400 {
		t.Fatalf("keep_client on review: %d", resp.StatusCode)
	}
	if resp := f.e.Do("POST", "/api/sync/conflicts/"+id+"/resolve", map[string]any{"resolution": "acknowledge", "note": "counted, correct"}, pm); resp.StatusCode != 200 {
		t.Fatalf("acknowledge: %d %s", resp.StatusCode, f.e.Text(resp))
	}
}

func TestDeviceRevokeStopsSyncAndRefresh(t *testing.T) {
	f := setup(t)
	if r := f.dc.Push(note(0, f.plant, "before")); r.Results[0].Status != "accepted" {
		t.Fatal(r)
	}
	// Another device of the same user revokes this one.
	other := f.e.SignInDevice(f.sup, "")
	devs := f.e.Map(other.Do("GET", "/api/account/devices", nil))["items"].([]any)
	var rowID string
	for _, d := range devs {
		if d.(map[string]any)["deviceId"] == f.dc.DeviceID {
			rowID = d.(map[string]any)["id"].(string)
		}
	}
	if rowID == "" {
		t.Fatalf("device not listed: %v", devs)
	}
	if r := other.Do("POST", "/api/account/devices/"+rowID+"/revoke", nil); r.StatusCode != 200 {
		t.Fatalf("revoke: %d %s", r.StatusCode, f.e.Text(r))
	}
	if r := f.dc.Push(note(0, f.plant, "after")); r.Status != 403 {
		t.Fatalf("push after revoke: %d", r.Status)
	}
	resp := f.dc.Do("GET", "/api/sync/status", nil)
	code, _, _ := f.e.ErrorOf(resp)
	if resp.StatusCode != 403 || code != "device_revoked" {
		t.Fatalf("status after revoke: %d %s", resp.StatusCode, code)
	}
	resp = f.dc.Refresh()
	code, _, _ = f.e.ErrorOf(resp)
	if resp.StatusCode != 403 || code != "device_revoked" {
		t.Fatalf("refresh after revoke: %d %s", resp.StatusCode, code)
	}
	// The other device keeps working; the monitor lists the revoked device.
	if r := other.Push(note(0, f.plant, "other")); r.Results[0].Status != "accepted" {
		t.Fatalf("other device: %+v", r)
	}
	admin := f.e.User(auth.RoleSystemAdmin)
	mon := f.e.Map(f.e.Do("GET", "/api/sync/monitor", nil, admin))
	revoked := 0
	for _, d := range mon["devices"].([]any) {
		if d.(map[string]any)["status"] == "revoked" {
			revoked++
		}
	}
	if revoked != 1 || len(mon["sites"].([]any)) == 0 {
		t.Fatalf("monitor: %v", mon)
	}
}

// T-08: a device revoked while offline delivers what it captured before the revocation, then is locked out.
func TestRevokedDeviceFinalUpload(t *testing.T) {
	f := setup(t)
	before := note(0, f.plant, "captured offline before the revocation")
	before["capturedAt"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	other := f.e.SignInDevice(f.sup, "")
	var rowID string
	_ = f.e.Pool.QueryRow(t.Context(), `SELECT id FROM device WHERE device_id=$1`, f.dc.DeviceID).Scan(&rowID)
	if r := other.Do("POST", "/api/account/devices/"+rowID+"/revoke", nil); r.StatusCode != 200 {
		t.Fatalf("revoke: %d", r.StatusCode)
	}
	after := note(0, f.plant, "captured after the revocation")
	after["capturedAt"] = time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano)

	r := f.dc.Push(before, after)
	if r.Status != 200 || r.DeviceStatus != "revoked" || len(r.Results) != 2 {
		t.Fatalf("final upload: %+v", r)
	}
	if a := r.Results[0]; a.Status != "accepted" || !slices.Contains(a.Flags, syncsvc.FlagRevokedDevice) {
		t.Fatalf("captured before: %+v", a)
	}
	if b := r.Results[1]; b.Status != "rejected" || b.ReasonCode != syncsvc.ReasonDeviceRevoked {
		t.Fatalf("captured after: %+v", b)
	}
	if n := count(t, f.e, `SELECT count(*) FROM t_sync_note WHERE body='captured offline before the revocation'`); n != 1 {
		t.Fatalf("applied %d times", n)
	}
	// re-sent (lost answer): absorbed, never applied twice
	if r := f.dc.Push(before); r.Status != 200 || r.Results[0].Status != "duplicate" || r.Results[0].OriginalStatus != "accepted" {
		t.Fatalf("re-send: %+v", r)
	}
	// nothing captured before the revocation, any other call, or a pull → locked out
	if r := f.dc.Push(note(0, f.plant, "later")); r.Status != 403 {
		t.Fatalf("push of later records: %d", r.Status)
	}
	for _, path := range []string{"/api/sync/pull", "/api/sync/status", "/api/lookups"} {
		if st, code := statusCode(f.e, f.dc.Do("GET", path, nil)); st != 403 || code != "device_revoked" {
			t.Fatalf("%s after revoke: %d %s", path, st, code)
		}
	}
	// only within the offline window after the revocation
	if _, err := f.e.Pool.Exec(t.Context(), `UPDATE device SET revoked_at = now() - interval '15 days' WHERE id=$1`, rowID); err != nil {
		t.Fatal(err)
	}
	old := note(0, f.plant, "too late")
	old["capturedAt"] = time.Now().Add(-16 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	if r := f.dc.Push(old); r.Status != 403 {
		t.Fatalf("outside the window: %d", r.Status)
	}
	// a device-less or signed-out caller gets nothing from the final route
	if r := f.e.Do("POST", "/api/sync/push", map[string]any{"records": []any{}}, nil); r.StatusCode != 401 {
		t.Fatalf("anonymous push: %d", r.StatusCode)
	}
}

func statusCode(e *apptest.Env, resp *http.Response) (int, string) {
	code, _, _ := e.ErrorOf(resp)
	return resp.StatusCode, code
}

func TestPushHTTPDenialsAndLimits(t *testing.T) {
	f := setup(t)
	// Anonymous.
	if r := f.e.Do("POST", "/api/sync/push", map[string]any{"records": []any{}}, nil); r.StatusCode != 401 {
		t.Fatalf("anon: %d", r.StatusCode)
	}
	if r := f.e.Do("GET", "/api/sync/pull", nil, nil); r.StatusCode != 401 {
		t.Fatalf("anon pull: %d", r.StatusCode)
	}
	// Device mismatch.
	resp := f.dc.Do("POST", "/api/sync/push", map[string]any{"deviceId": uuid.NewString(), "records": []any{note(0, f.plant, "x")}})
	if code, _, _ := f.e.ErrorOf(resp); resp.StatusCode != 403 || code != "device_mismatch" {
		t.Fatalf("mismatch: %d %s", resp.StatusCode, code)
	}
	// Too many records.
	recs := make([]any, 201)
	for i := range recs {
		recs[i] = note(int64(i+1), f.plant, "x")
	}
	resp = f.dc.Do("POST", "/api/sync/push", map[string]any{"deviceId": f.dc.DeviceID, "records": recs})
	if code, _, _ := f.e.ErrorOf(resp); resp.StatusCode != 400 || code != "batch_too_large" {
		t.Fatalf("batch: %d %s", resp.StatusCode, code)
	}
	// Oversized record payload.
	big := note(0, f.plant, strings.Repeat("x", 70<<10))
	if r := f.dc.Push(big); r.Results[0].ReasonCode != syncsvc.ReasonPayloadTooLarge {
		t.Fatalf("big payload: %+v", r.Results[0])
	}
	// Monitor / settings denials.
	if r := f.dc.Do("GET", "/api/sync/monitor", nil); r.StatusCode != 403 {
		t.Fatalf("monitor as supervisor: %d", r.StatusCode)
	}
	if r := f.dc.Do("PUT", "/api/sync/settings", map[string]any{"maxClockSkewMinutes": 5, "offlineUnlockDays": 3}); r.StatusCode != 403 {
		t.Fatalf("settings as supervisor: %d", r.StatusCode)
	}
	// Status reflects the device.
	st := f.e.Map(f.dc.Do("GET", "/api/sync/status?pending=7", nil))
	if st["deviceStatus"] != "active" || st["deviceId"] != f.dc.DeviceID || st["pending"].(float64) != 7 || st["maxClockSkewMinutes"].(float64) != 10 {
		t.Fatalf("status: %v", st)
	}
}

func TestPushRateLimit(t *testing.T) {
	old := syncsvc.PushRule
	syncsvc.PushRule.Burst, syncsvc.PushRule.PerMinute = 3, 3
	defer func() { syncsvc.PushRule = old }()
	f := setup(t)
	codes := []int{}
	for i := 0; i < 5; i++ {
		codes = append(codes, f.dc.Push(note(0, f.plant, "x")).Status)
	}
	if fmt.Sprint(codes) != "[200 200 200 429 429]" {
		t.Fatalf("rate limit codes %v", codes)
	}
}

func TestPullDeltaScopeAndSnapshots(t *testing.T) {
	f := setup(t)
	type pullOut struct {
		Changes []struct {
			Entity  string         `json:"entity"`
			ID      string         `json:"id"`
			Op      string         `json:"op"`
			Version int64          `json:"version"`
			Data    map[string]any `json:"data"`
		} `json:"changes"`
		Cursor  string `json:"cursor"`
		HasMore bool   `json:"hasMore"`
		Reset   bool   `json:"reset"`
	}
	pull := func(dc *apptest.DeviceClient, q string) pullOut {
		t.Helper()
		resp := dc.Do("GET", "/api/sync/pull"+q, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("pull %s: %d %s", q, resp.StatusCode, f.e.Text(resp))
		}
		var o pullOut
		f.e.Decode(resp, &o)
		return o
	}
	byEntity := func(o pullOut) map[string]int {
		m := map[string]int{}
		for _, c := range o.Changes {
			m[c.Entity]++
		}
		return m
	}
	// Seed a worker on each site.
	var p2 string
	_ = f.e.Pool.QueryRow(t.Context(), `SELECT id FROM "Plant" WHERE code='BTN-2'`).Scan(&p2)
	for _, p := range []string{f.plant, p2} {
		if _, err := f.e.Pool.Exec(t.Context(), `INSERT INTO "Worker" (id, "firstName", "lastName", "plantId", "dailyWage", "nationalId") VALUES (gen_random_uuid()::text,'A','B',$1,3000,'1199'||$1)`, p); err != nil {
			t.Fatal(err)
		}
	}
	// OFF-11: only transactions writing synced rows hold a pull back, so a full pull sees the seeded workers at once
	// even while other test packages run against the same PostgreSQL.
	first := pull(f.dc, "")
	n := byEntity(first)
	if n["sites"] < 3 || n["products"] != 11 || n["materials"] < 6 || n["workers"] != 1 || n["customers"] != 0 || n["scope"] != 1 || n["balances"] < 0 || first.HasMore {
		t.Fatalf("first pull: %v", n)
	}
	for _, c := range first.Changes {
		if c.Entity == "workers" && (c.Data["nationalId"] != nil || c.Data["dailyWage"] != nil || c.Data["plantId"] != f.plant) {
			t.Fatalf("worker data minimised and scoped: %v", c.Data)
		}
		if c.Entity == "sites" && c.Data["kind"] == nil {
			t.Fatalf("site without kind: %v", c.Data)
		}
		if c.Entity == "scope" && c.Data["scopedPlantId"] != f.plant {
			t.Fatalf("scope: %v", c.Data)
		}
	}
	// Nothing changed → only snapshots.
	second := pull(f.dc, "?cursor="+first.Cursor)
	if n := byEntity(second); n["sites"]+n["products"]+n["workers"] != 0 || n["scope"] != 1 {
		t.Fatalf("second pull: %v", n)
	}
	// A product change shows up once.
	if _, err := f.e.Pool.Exec(t.Context(), `UPDATE "Product" SET "defaultPrice"="defaultPrice"+1 WHERE code=(SELECT min(code) FROM "Product")`); err != nil {
		t.Fatal(err)
	}
	third := pull(f.dc, "?cursor="+second.Cursor)
	if n := byEntity(third); n["products"] != 1 {
		t.Fatalf("delta: %v", n)
	}
	if third.Reset {
		t.Fatal("unexpected reset")
	}
	// Pagination.
	page := pull(f.dc, "?limit=5")
	if !page.HasMore || len(page.Changes) != 5 {
		t.Fatalf("page: %d %v", len(page.Changes), page.HasMore)
	}
	total := len(page.Changes)
	cur := page.Cursor
	for i := 0; i < 20 && page.HasMore; i++ {
		page = pull(f.dc, "?limit=5&cursor="+cur)
		cur = page.Cursor
		for _, c := range page.Changes {
			if c.Entity != "scope" && c.Entity != "balances" && c.Entity != "refdata" {
				total++
			}
		}
	}
	if want := n["sites"] + n["products"] + n["materials"] + n["workers"]; total != want {
		t.Fatalf("paged total %d want %d", total, want)
	}
	// Synced records of other entities (registered by a phase-2 style pull) and deletes as tombstones.
	f.dc.Push(note(0, f.plant, "n1"))
	got := pull(f.dc, "?cursor="+cur)
	if byEntity(got)["test_notes"] != 1 {
		t.Fatalf("test notes: %v", byEntity(got))
	}
	if _, err := f.e.Pool.Exec(t.Context(), `DELETE FROM t_sync_note`); err != nil {
		t.Fatal(err)
	}
	del := pull(f.dc, "?cursor="+got.Cursor)
	if len(del.Changes) == 0 || del.Changes[0].Op != "delete" || del.Changes[0].Entity != "test_notes" {
		t.Fatalf("tombstone: %+v", del.Changes)
	}
	// Role change → reset from zero; customers now included for a sales manager.
	if _, err := f.e.Pool.Exec(t.Context(), `UPDATE "User" SET role='PRODUCTION_MANAGER', "plantId"=NULL WHERE id=$1`, f.sup.ID); err != nil {
		t.Fatal(err)
	}
	f.e.Deps.Auth.InvalidateUser(f.sup.ID)
	re := pull(f.dc, "?cursor="+del.Cursor)
	if !re.Reset || byEntity(re)["workers"] != 2 || byEntity(re)["products"] != 11 {
		t.Fatalf("reset: %v %v", re.Reset, byEntity(re))
	}
	if r := f.dc.Do("GET", "/api/sync/pull?cursor=garbage", nil); r.StatusCode != 400 {
		t.Fatalf("bad cursor: %d", r.StatusCode)
	}
}

func TestPullDoesNotSkipSlowTransactions(t *testing.T) {
	f := setup(t)
	first := struct {
		Cursor string `json:"cursor"`
	}{}
	f.e.Decode(f.dc.Do("GET", "/api/sync/pull?entities=products,materials", nil), &first)
	// A transaction updates a product but commits only after a later transaction committed and was pulled.
	tx, err := f.e.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `UPDATE "Product" SET name=name||' (slow)' WHERE code=(SELECT min(code) FROM "Product")`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.Pool.Exec(t.Context(), `UPDATE "Material" SET name=name||' (fast)' WHERE code=(SELECT min(code) FROM "Material")`); err != nil {
		t.Fatal(err)
	}
	var mid struct {
		Changes []map[string]any `json:"changes"`
		Cursor  string           `json:"cursor"`
	}
	f.e.Decode(f.dc.Do("GET", "/api/sync/pull?entities=products,materials&cursor="+first.Cursor, nil), &mid)
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var after struct {
		Changes []map[string]any `json:"changes"`
	}
	f.e.Decode(f.dc.Do("GET", "/api/sync/pull?entities=products,materials&cursor="+mid.Cursor, nil), &after)
	seen := map[string]int{}
	for _, c := range append(mid.Changes, after.Changes...) {
		seen[c["entity"].(string)]++
	}
	if seen["products"] != 1 || seen["materials"] != 1 {
		t.Logf("first=%s mid=%s %v after=%v", first.Cursor, mid.Cursor, mid.Changes, after.Changes)
		var x, xm string
		var v int64
		_ = f.e.Pool.QueryRow(t.Context(), `SELECT server_xid::text, server_version, pg_snapshot_xmin(pg_current_snapshot())::text FROM "Product" ORDER BY server_version DESC LIMIT 1`).Scan(&x, &v, &xm)
		t.Logf("product xid=%s v=%d xmin=%s", x, v, xm)
		t.Fatalf("slow transaction skipped or duplicated: %v", seen)
	}
}

// OFF-11: unrelated open transactions (another table, another schema, another database) do not stall delta pull,
// while a transaction writing a synced table is still never skipped — including one whose xid was assigned before
// the pull ran and that only writes the synced row afterwards.
func TestPullNotStalledByUnrelatedTransactions(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	type out struct {
		Changes []struct {
			Entity string         `json:"entity"`
			Data   map[string]any `json:"data"`
		} `json:"changes"`
		Cursor string `json:"cursor"`
	}
	pull := func(cursor string) out {
		t.Helper()
		var o out
		f.e.Decode(f.dc.Do("GET", "/api/sync/pull?entities=products,materials&cursor="+cursor, nil), &o)
		return o
	}
	names := func(o out, entity string) []string {
		var r []string
		for _, c := range o.Changes {
			if c.Entity == entity {
				r = append(r, fmt.Sprint(c.Data["name"]))
			}
		}
		return r
	}
	base := pull("")

	// 1. An open write transaction on a NON-synced table of the same schema (xid assigned, idle in transaction).
	unrelated, err := f.e.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unrelated.Rollback(context.Background()) }()
	if _, err := unrelated.Exec(ctx, `UPDATE "User" SET name = name WHERE id = $1`, f.sup.ID); err != nil {
		t.Fatal(err)
	}
	// 2. An open transaction holding the sync lock of ANOTHER schema (another test package or tenant).
	other, err := f.e.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Rollback(context.Background()) }()
	if _, err := other.Exec(ctx, `SELECT pg_current_xact_id(), pg_advisory_xact_lock_shared(1112821331, hashtext('some_other_schema'))`); err != nil {
		t.Fatal(err)
	}
	// 3. An open write transaction in ANOTHER database of the same cluster.
	cfg := f.e.Pool.Config().ConnConfig.Copy()
	cfg.Database = "postgres"
	delete(cfg.RuntimeParams, "search_path")
	foreign, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close(context.Background())
	ftx, err := foreign.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ftx.Rollback(context.Background()) }()
	if _, err := ftx.Exec(ctx, `SELECT pg_current_xact_id()`); err != nil {
		t.Fatal(err)
	}

	// A committed change is delivered immediately despite the three open transactions.
	if _, err := f.e.Pool.Exec(ctx, `UPDATE "Material" SET name = name || ' (fast)' WHERE code = (SELECT min(code) FROM "Material")`); err != nil {
		t.Fatal(err)
	}
	mid := pull(base.Cursor)
	if got := names(mid, "materials"); len(got) != 1 || !strings.HasSuffix(got[0], "(fast)") {
		t.Fatalf("pull stalled by unrelated transactions: %v", got)
	}

	// The unrelated same-schema transaction (xid assigned BEFORE the pull above advanced the cursor) now writes a
	// synced row and commits: it must still be delivered by the next pulls, not skipped.
	if _, err := unrelated.Exec(ctx, `UPDATE "Product" SET name = name || ' (late)' WHERE code = (SELECT min(code) FROM "Product")`); err != nil {
		t.Fatal(err)
	}
	// While it is in flight it holds the window back: a further committed change is withheld, never lost.
	if _, err := f.e.Pool.Exec(ctx, `UPDATE "Material" SET name = name || ' (held)' WHERE code = (SELECT min(code) FROM "Material")`); err != nil {
		t.Fatal(err)
	}
	held := pull(mid.Cursor)
	if err := unrelated.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	after := pull(held.Cursor)
	products := append(names(held, "products"), names(after, "products")...)
	materials := append(names(held, "materials"), names(after, "materials")...)
	if len(products) != 1 || !strings.HasSuffix(products[0], "(late)") {
		t.Fatalf("late synced write skipped or duplicated: %v", products)
	}
	if len(materials) != 1 || !strings.HasSuffix(materials[0], "(held)") {
		t.Fatalf("held change lost or duplicated: %v", materials)
	}
}

// §9.2: the sync API enforces schema versions. Older-but-supported clients and record shapes keep working; an
// unsupported version gets a clear error code and nothing of the batch is stored.
func TestSchemaVersionsEnforced(t *testing.T) {
	f := setup(t)
	push := func(hdr string, recs ...map[string]any) (int, string, map[string]string, string) {
		t.Helper()
		var h []string
		if hdr != "" {
			h = []string{syncsvc.SchemaHeader, hdr}
		}
		r := f.dc.Do("POST", "/api/sync/push", map[string]any{"deviceId": f.dc.DeviceID, "records": recs}, h...)
		if r.StatusCode == 200 {
			return 200, "", nil, f.e.Text(r)
		}
		code, msg, fields := f.e.ErrorOf(r)
		return r.StatusCode, code, fields, msg
	}
	pull := func(hdr string) (int, string) {
		t.Helper()
		r := f.dc.Do("GET", "/api/sync/pull?entities=products", nil, syncsvc.SchemaHeader, hdr)
		code, _, _ := f.e.ErrorOf(r)
		return r.StatusCode, code
	}
	ledger := func() int { return count(t, f.e, `SELECT count(*) FROM sync_ledger`) }
	withVersion := func(r map[string]any, v int) map[string]any { r["schemaVersion"] = v; return r }

	// Supported range is advertised.
	var st struct {
		Schema struct{ Min, Current int } `json:"schema"`
	}
	f.e.Decode(f.dc.Do("GET", "/api/sync/status", nil), &st)
	if st.Schema.Min != syncsvc.SchemaMin || st.Schema.Current != syncsvc.SchemaCurrent || st.Schema.Current < 2 {
		t.Fatalf("status schema: %+v", st.Schema)
	}
	// Backward compatible: a legacy client (no header, record v1) and a current one (header, record v2) both sync.
	if s, code, _, body := push("", note(1, f.plant, "legacy")); s != 200 || !strings.Contains(body, `"accepted"`) {
		t.Fatalf("legacy client: %d %s %s", s, code, body)
	}
	if s, code, _, body := push(fmt.Sprint(syncsvc.SchemaCurrent), withVersion(note(2, f.plant, "current"), 2)); s != 200 || !strings.Contains(body, `"accepted"`) {
		t.Fatalf("current client: %d %s %s", s, code, body)
	}
	if s, _ := pull(""); s != 200 {
		t.Fatalf("legacy pull: %d", s)
	}
	if s, _ := pull(fmt.Sprint(syncsvc.SchemaCurrent)); s != 200 {
		t.Fatalf("current pull: %d", s)
	}
	n := ledger()
	// A record shape newer than the handler knows: 409 schema_too_new, nothing stored.
	if s, code, fields, _ := push("2", note(3, f.plant, "ok"), withVersion(note(4, f.plant, "future"), 3)); s != 409 || code != syncsvc.CodeSchemaTooNew || fields["type"] != "test_note" {
		t.Fatalf("future record: %d %s %v", s, code, fields)
	}
	// A client newer than the server: 409 on push and pull.
	if s, code, _, _ := push(fmt.Sprint(syncsvc.SchemaCurrent+1), note(3, f.plant, "x")); s != 409 || code != syncsvc.CodeSchemaTooNew {
		t.Fatalf("future client push: %d %s", s, code)
	}
	if s, code := pull(fmt.Sprint(syncsvc.SchemaCurrent + 1)); s != 409 || code != syncsvc.CodeSchemaTooNew {
		t.Fatalf("future client pull: %d %s", s, code)
	}
	if s, code := pull("abc"); s != 400 || code != "bad_schema_version" {
		t.Fatalf("garbage header: %d %s", s, code)
	}
	// A record type whose oldest supported shape is 2: a v1 record means the app must be updated.
	syncsvc.RegisterHandler("test_note_v2", syncsvc.HandlerFunc(func(ctx context.Context, tx pgx.Tx, rec syncsvc.Record, a syncsvc.Actor) syncsvc.Result {
		return syncsvc.Accept(rec.UUID, 1)
	}), syncsvc.SchemaVersions(2, 2))
	defer syncsvc.UnregisterHandler("test_note_v2")
	old := apptest.SyncRecord("test_note_v2", 3, map[string]any{"plantId": f.plant})
	if s, code, fields, msg := push("2", old); s != 426 || code != syncsvc.CodeSchemaUpgradeRequired || fields["min"] != "2" || msg == "" {
		t.Fatalf("old record shape: %d %s %v %q", s, code, fields, msg)
	}
	// The server stops serving wire version 1: legacy clients are told to upgrade, current ones carry on.
	defer func(m int) { syncsvc.SchemaMin = m }(syncsvc.SchemaMin)
	syncsvc.SchemaMin = 2
	if s, code, _, _ := push("", note(3, f.plant, "legacy")); s != 426 || code != syncsvc.CodeSchemaUpgradeRequired {
		t.Fatalf("legacy push after min raised: %d %s", s, code)
	}
	if s, code := pull("1"); s != 426 || code != syncsvc.CodeSchemaUpgradeRequired {
		t.Fatalf("legacy pull after min raised: %d %s", s, code)
	}
	if got := ledger(); got != n {
		t.Fatalf("refused batches stored records: %d → %d", n, got)
	}
	if s, code, _, _ := push("2", withVersion(note(3, f.plant, "after"), 2)); s != 200 {
		t.Fatalf("current client after min raised: %d %s", s, code)
	}
}

func TestMonitorLedgerDrillDown(t *testing.T) {
	f := setup(t)
	res := f.dc.Push(note(0, f.plant, "a"), note(0, f.plant, "", ""))
	admin := f.e.User(auth.RoleSystemAdmin)
	list := f.e.Map(f.e.Do("GET", "/api/sync/monitor/ledger?status=rejected&deviceId="+f.dc.DeviceID, nil, admin))
	items := list["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["recordUuid"] != res.Results[1].UUID {
		t.Fatalf("ledger list: %v", list)
	}
	id := fmt.Sprint(int64(items[0].(map[string]any)["id"].(float64)))
	rec := f.e.Map(f.e.Do("GET", "/api/sync/monitor/ledger/"+id, nil, admin))["record"].(map[string]any)
	if rec["payload"].(map[string]any)["plantId"] != f.plant {
		t.Fatalf("payload: %v", rec)
	}
	mon := f.e.Map(f.e.Do("GET", "/api/sync/monitor", nil, admin))
	d := mon["devices"].([]any)[0].(map[string]any)
	if d["accepted"].(float64) != 1 || d["rejected"].(float64) != 1 || d["lastSuccessfulSyncAt"] == nil {
		t.Fatalf("monitor device: %v", d)
	}
	if r := f.e.Do("GET", "/api/sync/monitor/ledger/999999", nil, admin); r.StatusCode != 404 {
		t.Fatalf("missing ledger: %d", r.StatusCode)
	}
	gm := f.e.User(auth.RoleGeneralManager)
	if r := f.e.Do("GET", "/api/sync/monitor/ledger", nil, gm); r.StatusCode != 403 {
		t.Fatalf("gm monitor: %d", r.StatusCode)
	}
}

// NFR-P3 (server side): a 200-record batch is processed well under 10 s.
func TestBatchOf200Under10s(t *testing.T) {
	f := setup(t)
	recs := make([]map[string]any, 200)
	for i := range recs {
		recs[i] = note(int64(i+1), f.plant, fmt.Sprintf("record %d", i))
	}
	start := time.Now()
	res := f.dc.Push(recs...)
	el := time.Since(start)
	if res.Status != 200 || len(res.Results) != 200 {
		t.Fatalf("push: %d", res.Status)
	}
	for _, r := range res.Results {
		if r.Status != "accepted" {
			t.Fatalf("not accepted: %+v", r)
		}
	}
	if el > 10*time.Second {
		t.Fatalf("200 records took %s", el)
	}
	t.Logf("200-record batch: %s", el)
}

func BenchmarkPush200(b *testing.B) {
	e := apptest.New(b)
	if _, err := e.Pool.Exec(b.Context(), `CREATE TABLE IF NOT EXISTS t_sync_note (id TEXT PRIMARY KEY, plant_id TEXT, body TEXT NOT NULL, created_by TEXT)`); err != nil {
		b.Fatal(err)
	}
	_, _ = e.Pool.Exec(b.Context(), `SELECT btn_enable_sync_version('t_sync_note'::regclass, 'test_notes')`)
	registerNote()
	old := syncsvc.PushRule
	syncsvc.PushRule.Burst, syncsvc.PushRule.PerMinute = 1_000_000, 1_000_000
	defer func() { syncsvc.PushRule = old }()
	sup := e.User(auth.RoleSupervisor)
	var seq int64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		recs := make([]any, 200)
		for j := range recs {
			seq++
			recs[j] = note(seq, sup.PlantID, "bench")
		}
		resp := e.Do("POST", "/api/sync/push", map[string]any{"deviceId": "7d1c3a52-1111-4b5b-9c39-6a3c2f1d0b11", "records": recs}, sup)
		if resp.StatusCode != http.StatusOK {
			b.Fatalf("push %d", resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// The default review hook notifies sync.resolve holders who can see the site (not the capturing user,
// not a supervisor of another site).
func TestDefaultReviewHookNotifiesResolvers(t *testing.T) {
	f := setup(t)
	pm := f.e.User(auth.RoleProductionManager)
	sup1 := f.e.User(auth.RoleSupervisor)
	sup2 := f.e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-2"})
	tx, err := f.e.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if err := syncsvc.NotifyReview(t.Context(), tx, syncsvc.ReviewItem{ID: "x1", Kind: "negative_stock", Type: "stock_movement", PlantID: f.plant, UserID: f.sup.ID, Reason: "balance -5"}); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	rows, _ := tx.Query(t.Context(), `SELECT user_id FROM notification WHERE event='sync.negative_stock'`)
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		got[id] = true
	}
	rows.Close()
	if !got[pm.ID] || !got[sup1.ID] || got[sup2.ID] || got[f.sup.ID] {
		t.Fatalf("recipients: %v", got)
	}
}
