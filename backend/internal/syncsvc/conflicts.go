package syncsvc

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// ConflictInput describes the server side of a conflict (SRS §5.5).
type ConflictInput struct {
	Entity        string // e.g. "ProductionReport"
	EntityID      string
	ServerData    any // current server version (stored as JSON)
	ServerVersion int64
	Reason        string // human explanation, e.g. "The report was approved on the server after this edit"
	PlantID       string
}

// Conflict stores both versions in sync_conflict and returns a Conflict result for the record. Call it from a
// handler when the server version changed underneath the client's edit. The ledger row is linked automatically.
func Conflict(ctx context.Context, tx pgx.Tx, rec Record, actor Actor, in ConflictInput) Result {
	var serverJSON []byte
	if in.ServerData != nil {
		if b, err := json.Marshal(in.ServerData); err == nil {
			serverJSON = b
		}
	}
	var server any
	if serverJSON != nil {
		server = serverJSON
	}
	plant := in.PlantID
	if plant == "" {
		plant = payloadPlant(rec.Payload)
	}
	if plant == "" {
		plant = actor.PlantID
	}
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO sync_conflict (kind, record_uuid, revision, type, entity, entity_id, plant_id, user_id, device_id, client_payload, server_data, server_version, reason)
		VALUES ('conflict',$1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11,$12) RETURNING id`,
		rec.UUID, rec.Revision, rec.Type, nilIf(in.Entity), nilIf(in.EntityID), nilIf(plant), actor.UserID, rec.DeviceID, payloadOrEmpty(rec.Payload),
		server, nilIf64(in.ServerVersion), nilIf(in.Reason)).Scan(&id)
	if err != nil {
		return Reject(ReasonHandlerError, err.Error())
	}
	if err := auditConflictOpen(ctx, tx, actor, id, "conflict", rec, serverJSON, in.ServerVersion, in.Reason); err != nil {
		return Reject(ReasonHandlerError, err.Error())
	}
	reason := in.Reason
	if reason == "" {
		reason = i18n.T(actor.Lang, "sync.conflict_default")
	}
	return Result{Status: Conflicted, ReasonCode: ReasonConflict, Reason: reason, ServerVersion: in.ServerVersion, ConflictID: id,
		Flags: []string{}, PlantID: plant, Entity: in.Entity, EntityID: in.EntityID}
}

// auditConflictOpen writes both versions of a new conflict / review item into the immutable audit trail (SRS §5.5
// "both versions of any conflict are kept in the audit trail"): Before = the server version, After = the device
// version. sync_conflict itself is a working table (its status changes on resolution); the audit row is the record.
func auditConflictOpen(ctx context.Context, tx pgx.Tx, actor Actor, id, kind string, rec Record, server []byte, serverVersion int64, reason string) error {
	var sv any
	if server != nil {
		sv = json.RawMessage(server)
	}
	before := map[string]any{"serverData": sv, "serverVersion": nilIf64(serverVersion)}
	after := map[string]any{"clientPayload": json.RawMessage(payloadOrEmpty(rec.Payload)), "recordUuid": rec.UUID, "revision": rec.Revision,
		"type": rec.Type, "deviceId": rec.DeviceID, "capturedAt": rec.CapturedAt, "reason": nilIf(reason)}
	return audit.Log(ctx, tx, actor.Audit(), audit.Input{Action: "sync.conflict_open", Entity: "SyncConflict", EntityID: id,
		Summary: kind + " " + rec.Type + " held for review", Before: before, After: after})
}

// AuditReviewOpen is auditConflictOpen for review items created by the push pipeline (negative stock).
func AuditReviewOpen(ctx context.Context, tx pgx.Tx, actor Actor, id, kind string, rec Record, reason string) error {
	return auditConflictOpen(ctx, tx, actor, id, kind, rec, nil, 0, reason)
}

// ConflictRow is the JSON shape of a conflict / review item.
type ConflictRow struct {
	ID             string          `json:"id"`
	Kind           string          `json:"kind"`
	LedgerID       *int64          `json:"ledgerId"`
	RecordUUID     string          `json:"recordUuid"`
	Revision       int             `json:"revision"`
	Type           string          `json:"type"`
	Entity         *string         `json:"entity"`
	EntityID       *string         `json:"entityId"`
	PlantID        *string         `json:"plantId"`
	PlantCode      *string         `json:"plantCode"`
	UserID         *string         `json:"userId"`
	UserName       *string         `json:"userName"`
	DeviceID       *string         `json:"deviceId"`
	ClientPayload  json.RawMessage `json:"clientPayload"`
	ServerData     json.RawMessage `json:"serverData"`
	ServerVersion  *int64          `json:"serverVersion"`
	Reason         *string         `json:"reason"`
	Status         string          `json:"status"`
	Resolution     *string         `json:"resolution"`
	ResolutionNote *string         `json:"resolutionNote"`
	MergedPayload  json.RawMessage `json:"mergedPayload"`
	ResolvedBy     *string         `json:"resolvedBy"`
	ResolvedByName *string         `json:"resolvedByName"`
	ResolvedAt     *time.Time      `json:"resolvedAt"`
	CreatedAt      time.Time       `json:"createdAt"`
	CapturedAt     *time.Time      `json:"capturedAt"`
}

const conflictCols = `c.id, c.kind, c.ledger_id, c.record_uuid::text, c.revision, c.type, c.entity, c.entity_id, c.plant_id, p.code, c.user_id, u.name,
	c.device_id::text, c.client_payload, c.server_data, c.server_version, c.reason, c.status, c.resolution, c.resolution_note, c.merged_payload,
	c.resolved_by, r.name, c.resolved_at, c.created_at, l.captured_at`

const conflictFrom = ` FROM sync_conflict c LEFT JOIN "Plant" p ON p.id=c.plant_id LEFT JOIN "User" u ON u.id=c.user_id
	LEFT JOIN "User" r ON r.id=c.resolved_by LEFT JOIN sync_ledger l ON l.id=c.ledger_id`

func scanConflict(row pgx.Row) (*ConflictRow, error) {
	var c ConflictRow
	var server, merged []byte
	if err := row.Scan(&c.ID, &c.Kind, &c.LedgerID, &c.RecordUUID, &c.Revision, &c.Type, &c.Entity, &c.EntityID, &c.PlantID, &c.PlantCode, &c.UserID, &c.UserName,
		&c.DeviceID, &c.ClientPayload, &server, &c.ServerVersion, &c.Reason, &c.Status, &c.Resolution, &c.ResolutionNote, &merged,
		&c.ResolvedBy, &c.ResolvedByName, &c.ResolvedAt, &c.CreatedAt, &c.CapturedAt); err != nil {
		return nil, err
	}
	if server != nil {
		c.ServerData = server
	}
	if merged != nil {
		c.MergedPayload = merged
	}
	return &c, nil
}

// GET /sync/conflicts?status=open|resolved|all&kind=&plantId=&page=
func (m *module) listConflicts(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	httpx.NoStore(c)
	u := auth.MustUser(c)
	pg := httpx.Paginate(c, 25)
	w := newWhere()
	switch st := c.Query("status", "open"); st {
	case "open", "resolved":
		w.add("c.status = ?", st)
	}
	if k := c.Query("kind"); k == "conflict" || k == "negative_stock" || k == "review" {
		w.add("c.kind = ?", k)
	}
	if p := auth.PlantFilter(u, c.Query("plantId")); p != "" {
		w.add("c.plant_id = ?", p)
	}
	if t := c.Query("type"); t != "" {
		w.add("c.type = ?", t)
	}
	var total int
	if err := m.d.Pool.QueryRow(ctx, `SELECT count(*) FROM sync_conflict c WHERE `+w.sql(), w.args...).Scan(&total); err != nil {
		return err
	}
	lim, off := w.page(pg.Limit, pg.Offset)
	rows, err := m.d.Pool.Query(ctx, `SELECT `+conflictCols+conflictFrom+` WHERE `+w.sql()+` ORDER BY c.created_at DESC `+lim+off, w.args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []*ConflictRow{}
	for rows.Next() {
		r, err := scanConflict(rows)
		if err != nil {
			return err
		}
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return c.JSON(httpx.NewPage(items, total, pg.Page, pg.PageSize))
}

func (m *module) conflictFor(ctx context.Context, q db.DBTX, u *auth.User, id string, lock bool) (*ConflictRow, error) {
	sql := `SELECT ` + conflictCols + conflictFrom + ` WHERE c.id=$1`
	if lock {
		sql += ` FOR UPDATE OF c`
	}
	r, err := scanConflict(q.QueryRow(ctx, sql, id))
	if err != nil {
		if db.IsNoRows(err) {
			return nil, httpx.NotFound("entity.sync_conflict")
		}
		return nil, err
	}
	if s := auth.ScopedPlantID(u); s != "" && (r.PlantID == nil || *r.PlantID != s) {
		return nil, httpx.NotFound("entity.sync_conflict")
	}
	return r, nil
}

// GET /sync/conflicts/:id
func (m *module) getConflict(c *fiber.Ctx) error {
	httpx.NoStore(c)
	r, err := m.conflictFor(httpx.Ctx(c), m.d.Pool, auth.MustUser(c), c.Params("id"), false)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"conflict": r})
}

// POST /sync/conflicts/:id/resolve {resolution, note, mergedPayload?}
func (m *module) resolveConflict(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	resolution := v.Str("resolution").Trim().OneOf([]string{"keep_server", "keep_client", "merge", "acknowledge"}, "sync.resolution_invalid").Get()
	note := v.Str("note").Trim().Min(3, "sync.note_required").Max(1000).Get()
	var merged json.RawMessage
	if raw := v.Raw("mergedPayload"); raw != nil {
		if b, err := json.Marshal(raw); err == nil && len(b) > 0 && b[0] == '{' {
			merged = b
		} else {
			v.Fail("mergedPayload", i18n.T(lang, "sync.payload_invalid"))
		}
	}
	if resolution == "merge" && merged == nil {
		v.Fail("mergedPayload", i18n.T(lang, "sync.merged_required"))
	}
	if err := v.Err(); err != nil {
		return err
	}
	out, err := db.TxVal(ctx, m.d.Pool, func(tx pgx.Tx) (*ConflictRow, error) {
		r, err := m.conflictFor(ctx, tx, u, c.Params("id"), true)
		if err != nil {
			return nil, err
		}
		if r.Status != "open" {
			return nil, httpx.Conflict(i18n.T(lang, "sync.already_resolved"))
		}
		if (r.Kind == "conflict") == (resolution == "acknowledge") {
			return nil, httpx.BadRequest("error.validation", map[string]string{"resolution": i18n.T(lang, "sync.resolution_invalid")})
		}
		var applied *Result
		if resolution == "keep_client" || resolution == "merge" {
			payload := r.ClientPayload
			if resolution == "merge" {
				payload = merged
			}
			res, err := m.reapply(ctx, tx, r, u, c, payload)
			if err != nil {
				return nil, err
			}
			if res.Status != Accepted {
				msg := res.Reason
				if msg == "" {
					msg = res.ReasonCode
				}
				return nil, httpx.Conflict(i18n.T(lang, "sync.reapply_failed", "reason", msg))
			}
			applied = &res
		}
		if _, err := tx.Exec(ctx, `UPDATE sync_conflict SET status='resolved', resolution=$2, resolution_note=$3, merged_payload=$4, resolved_by=$5, resolved_at=now() WHERE id=$1`,
			r.ID, resolution, note, mergedOrNil(merged), u.ID); err != nil {
			return nil, err
		}
		if applied != nil && r.LedgerID != nil {
			if _, err := tx.Exec(ctx, `UPDATE sync_ledger SET status='accepted', reason_code='resolved_'||$2, server_id=$3, server_version=$4, processed_at=now(),
				flags=(SELECT array(SELECT DISTINCT unnest(flags || $5::text[]) ORDER BY 1)) WHERE id=$1`,
				*r.LedgerID, resolution, nilIf(applied.ServerID), nilIf64(applied.ServerVersion), joinFlags(applied.Flags)); err != nil {
				return nil, err
			}
		}
		// both versions go into the immutable audit row (§5.5), with what was kept
		after := map[string]any{"status": "resolved", "resolution": resolution, "note": note, "recordUuid": r.RecordUUID}
		if merged != nil {
			after["mergedPayload"] = merged
		}
		if applied != nil {
			after["appliedServerId"] = nilIf(applied.ServerID)
			after["appliedServerVersion"] = nilIf64(applied.ServerVersion)
		}
		before := map[string]any{"status": "open", "kind": r.Kind, "clientPayload": r.ClientPayload, "serverData": r.ServerData, "serverVersion": r.ServerVersion}
		if err := audit.Record(c, tx, audit.Input{Action: "sync.conflict_resolve", Entity: "SyncConflict", EntityID: r.ID,
			Summary: r.Kind + " " + r.Type + " resolved: " + resolution, Before: before, After: after}); err != nil {
			return nil, err
		}
		return m.conflictFor(ctx, tx, u, r.ID, false)
	})
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"conflict": out})
}

func mergedOrNil(b json.RawMessage) any {
	if b == nil {
		return nil
	}
	return []byte(b)
}

// reapply runs the record's handler again with Force=true, as the resolving user (current rules) on the
// original device/user's behalf, inside a savepoint.
func (m *module) reapply(ctx context.Context, tx pgx.Tx, r *ConflictRow, u *auth.User, c *fiber.Ctx, payload json.RawMessage) (Result, error) {
	rec := Record{UUID: r.RecordUUID, Revision: r.Revision, Type: r.Type, SchemaVersion: 1, Payload: payload, Force: true, ReceivedAt: time.Now().UTC()}
	if r.DeviceID != nil {
		rec.DeviceID = *r.DeviceID
	}
	if r.CapturedAt != nil {
		rec.CapturedAt = *r.CapturedAt
	}
	if r.UserID != nil {
		rec.UserID = *r.UserID
	}
	if r.LedgerID != nil {
		_ = tx.QueryRow(ctx, `SELECT seq, schema_version FROM sync_ledger WHERE id=$1`, *r.LedgerID).Scan(&rec.Seq, &rec.SchemaVersion)
	}
	snap := auth.PermSnapshot{Role: u.Role, PlantID: u.PlantID, Permissions: auth.PermissionsFor(u.Role)}
	actor := m.actorFor(c, u, u.DeviceRowID, u.DeviceID, snap)
	sp, err := tx.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	res := m.apply(ctx, sp, rec, actor)
	if res.Status != Accepted {
		_ = sp.Rollback(ctx)
		return res, nil
	}
	return res, sp.Commit(ctx)
}

// GET /sync/records?uuids=a,b,c → latest ledger state per uuid (client badges after a resolution).
func (m *module) recordStates(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	httpx.NoStore(c)
	u := auth.MustUser(c)
	ids := []string{}
	for _, s := range strings.Split(c.Query("uuids"), ",") {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" && !uuidInvalid(s) {
			ids = append(ids, s)
		}
	}
	if len(ids) > MaxBatch {
		return httpx.NewKey(400, "batch_too_large", "sync.batch_too_large", "max", MaxBatch)
	}
	type state struct {
		UUID          string   `json:"uuid"`
		Revision      int      `json:"revision"`
		Status        string   `json:"status"`
		ReasonCode    *string  `json:"reasonCode"`
		Reason        *string  `json:"reason"`
		ServerID      *string  `json:"serverId"`
		ServerVersion *int64   `json:"serverVersion"`
		Flags         []string `json:"flags"`
		ConflictID    *string  `json:"conflictId"`
		ConflictState *string  `json:"conflictStatus"`
		Resolution    *string  `json:"resolution"`
	}
	items := []state{}
	if len(ids) > 0 {
		rows, err := m.d.Pool.Query(ctx, `SELECT DISTINCT ON (l.record_uuid) l.record_uuid::text, l.revision, l.status, l.reason_code, l.reason, l.server_id,
			l.server_version, l.flags, l.conflict_id, c.status, c.resolution
			FROM sync_ledger l LEFT JOIN sync_conflict c ON c.id=l.conflict_id
			WHERE l.record_uuid = ANY($1::uuid[]) AND l.user_id=$2 ORDER BY l.record_uuid, l.revision DESC`, ids, u.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s state
			if err := rows.Scan(&s.UUID, &s.Revision, &s.Status, &s.ReasonCode, &s.Reason, &s.ServerID, &s.ServerVersion, &s.Flags, &s.ConflictID, &s.ConflictState, &s.Resolution); err != nil {
				return err
			}
			items = append(items, s)
		}
		if err := rows.Err(); err != nil {
			return err
		}
	}
	return c.JSON(fiber.Map{"items": items})
}

// where builds "$n" placeholder conditions.
type where struct {
	conds []string
	args  []any
}

func newWhere() *where { return &where{conds: []string{"true"}} }

func (w *where) add(cond string, v any) {
	w.args = append(w.args, v)
	w.conds = append(w.conds, strings.ReplaceAll(cond, "?", "$"+itoa(len(w.args))))
}

func (w *where) sql() string { return strings.Join(w.conds, " AND ") }

// page appends LIMIT/OFFSET args and returns their SQL fragments.
func (w *where) page(limit, offset int) (string, string) {
	w.args = append(w.args, limit, offset)
	return " LIMIT $" + itoa(len(w.args)-1), " OFFSET $" + itoa(len(w.args))
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}
