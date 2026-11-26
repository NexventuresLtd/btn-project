package syncsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Limits (NFR-S4, NFR-P3).
const (
	MaxBatch       = 200
	MaxBodyBytes   = 1 << 20  // 1 MB per push
	MaxRecordBytes = 64 << 10 // 64 KB per payload

	SettingMaxClockSkew     = "sync.maxClockSkewMinutes"
	DefaultMaxClockSkewMins = 10
)

// RecordResult is the per-record answer (OFF-10).
type RecordResult struct {
	UUID     string `json:"uuid"`
	Revision int    `json:"revision"`
	Result
}

type pushRecord struct {
	UUID          string          `json:"uuid"`
	Type          string          `json:"type"`
	SchemaVersion int             `json:"schemaVersion"`
	Seq           int64           `json:"seq"`
	Revision      int             `json:"revision"`
	CapturedAt    string          `json:"capturedAt"`
	Payload       json.RawMessage `json:"payload"`
}

type pushBody struct {
	DeviceID   string       `json:"deviceId"`
	ClientTime string       `json:"clientTime"`
	Pending    *int         `json:"pending"`
	Records    []pushRecord `json:"records"`
}

// PushOutcome is the whole push answer.
type PushOutcome struct {
	Results          []RecordResult `json:"results"`
	ServerTime       time.Time      `json:"serverTime"`
	ClockSkewSeconds int            `json:"clockSkewSeconds"`
	// DeviceStatus "revoked" = this was the device's final upload (T-08): the device must lock out and purge its data.
	DeviceStatus string `json:"deviceStatus,omitempty"`
}

// pushOpts: a final upload of a revoked device accepts only records captured before revokedAt.
type pushOpts struct {
	revokedAt *time.Time
}

// resolveDevice finds the device row for a push/status: bound sessions use their device (the body/header id must
// match); device-less sessions (devtools/tests) get a row created for (deviceId, user).
func (m *module) resolveDevice(c *fiber.Ctx, ctx context.Context, q db.DBTX, u *auth.User, bodyDevice string, allowRevoked ...bool) (rowID, deviceID string, snap auth.PermSnapshot, err error) {
	hdr := auth.RequestDeviceID(c)
	if bodyDevice != "" {
		if _, perr := uuid.Parse(bodyDevice); perr != nil {
			return "", "", snap, httpx.BadRequest("error.validation", map[string]string{"deviceId": i18n.T(httpx.Lang(c), "sync.device_id_invalid")})
		}
		bodyDevice = strings.ToLower(bodyDevice)
	}
	if u.DeviceRowID != "" {
		if (bodyDevice != "" && bodyDevice != u.DeviceID) || (hdr != "" && hdr != u.DeviceID) {
			return "", "", snap, httpx.NewKey(403, auth.CodeDeviceMismatch, "auth.device_mismatch")
		}
		rowID, deviceID = u.DeviceRowID, u.DeviceID
	} else {
		deviceID = bodyDevice
		if deviceID == "" {
			deviceID = hdr
		}
		if deviceID == "" {
			return "", "", snap, httpx.NewKey(400, auth.CodeDeviceMismatch, "auth.device_required")
		}
		var role string
		var plant *string
		if err := q.QueryRow(ctx, `SELECT role::text, "plantId" FROM "User" WHERE id=$1`, u.ID).Scan(&role, &plant); err != nil {
			return "", "", snap, err
		}
		s := auth.PermSnapshot{Role: auth.Role(role), Permissions: auth.PermissionsFor(auth.Role(role)), At: time.Now().UTC()}
		if plant != nil {
			s.PlantID = *plant
		}
		ua := c.Get("User-Agent")
		if len(ua) > 300 {
			ua = ua[:300]
		}
		if err := q.QueryRow(ctx, `INSERT INTO device (device_id, user_id, name, user_agent, platform, last_ip, device_key, offline_salt, perm_snapshot)
			VALUES ($1,$2,$3,$4,$5,$6,gen_random_uuid()::text::bytea,gen_random_uuid()::text::bytea,$7)
			ON CONFLICT (device_id, user_id) DO UPDATE SET last_seen_at=now() RETURNING id`, deviceID, u.ID, auth.DefaultDeviceName(ua), ua, auth.Platform(ua), c.IP(), s).Scan(&rowID); err != nil {
			return "", "", snap, err
		}
	}
	var status string
	var raw []byte
	if err := q.QueryRow(ctx, `SELECT status, perm_snapshot FROM device WHERE id=$1`, rowID).Scan(&status, &raw); err != nil {
		return "", "", snap, err
	}
	if status != auth.DeviceActive && !(len(allowRevoked) > 0 && allowRevoked[0] && status == auth.DeviceRevoked) {
		return "", "", snap, httpx.NewKey(403, auth.CodeDeviceRevoked, "auth.device_revoked")
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &snap)
	}
	if snap.Role == "" { // no snapshot recorded: the current role is the best evidence
		snap = auth.PermSnapshot{Role: u.Role, PlantID: u.PlantID, Permissions: auth.PermissionsFor(u.Role)}
	}
	return rowID, deviceID, snap, nil
}

func (m *module) actorFor(c *fiber.Ctx, u *auth.User, rowID, deviceID string, snap auth.PermSnapshot) Actor {
	return Actor{UserID: u.ID, Name: u.Name, Email: u.Email, Role: u.Role, PlantID: u.PlantID, Captured: snap,
		DeviceID: deviceID, DeviceRowID: rowID, IP: c.IP(), Lang: httpx.Lang(c)}
}

func (m *module) maxSkew(ctx context.Context) time.Duration {
	return time.Duration(m.d.Auth.SettingInt(ctx, SettingMaxClockSkew, DefaultMaxClockSkewMins)) * time.Minute
}

// POST /sync/push
func (m *module) push(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	httpx.NoStore(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	if len(c.Body()) > MaxBodyBytes {
		return httpx.NewKey(413, "payload_too_large", "sync.body_too_large", "kb", MaxBodyBytes>>10)
	}
	var body pushBody
	if err := httpx.BindJSON(c, &body); err != nil {
		return err
	}
	if len(body.Records) > MaxBatch {
		return httpx.NewKey(400, "batch_too_large", "sync.batch_too_large", "max", MaxBatch)
	}
	if _, err := clientSchema(c); err != nil { // §9.2: unsupported client versions are refused before anything is stored
		return err
	}
	if err := checkRecordSchemas(body.Records); err != nil {
		return err
	}
	out, err := m.Push(c, ctx, u, body, lang)
	if err != nil {
		return err
	}
	return c.JSON(out)
}

type prior struct {
	id                 int64
	revision           int
	status             Status
	reasonCode, reason *string
	serverID           *string
	serverVersion      *int64
	flags              []string
	conflictID         *string
}

func (p prior) result() Result {
	r := Result{Status: p.status, Flags: p.flags}
	if p.reasonCode != nil {
		r.ReasonCode = *p.reasonCode
	}
	if p.reason != nil {
		r.Reason = *p.reason
	}
	if p.serverID != nil {
		r.ServerID = *p.serverID
	}
	if p.serverVersion != nil {
		r.ServerVersion = *p.serverVersion
	}
	if p.conflictID != nil {
		r.ConflictID = *p.conflictID
	}
	if r.Flags == nil {
		r.Flags = []string{}
	}
	return r
}

type work struct {
	idx int
	rec Record
	err *Result // pre-validation failure (no ledger row possible when uuid is invalid)
}

// Push processes one batch for user u (exported for benchmarks and tools).
func (m *module) Push(c *fiber.Ctx, ctx context.Context, u *auth.User, body pushBody, lang i18n.Lang) (*PushOutcome, error) {
	return m.pushWith(c, ctx, u, body, lang, pushOpts{})
}

func (m *module) pushWith(c *fiber.Ctx, ctx context.Context, u *auth.User, body pushBody, lang i18n.Lang, opts pushOpts) (*PushOutcome, error) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	maxSkew := m.maxSkew(ctx)
	out := &PushOutcome{Results: make([]RecordResult, len(body.Records)), ServerTime: now}

	deviceSkewed := false
	if body.ClientTime != "" {
		if ct, err := time.Parse(time.RFC3339Nano, body.ClientTime); err == nil {
			skew := ct.Sub(now)
			out.ClockSkewSeconds = int(math.Round(skew.Seconds()))
			deviceSkewed = skew > maxSkew || skew < -maxSkew
		}
	}

	items := make([]work, len(body.Records))
	for i, r := range body.Records {
		w := work{idx: i}
		rev := r.Revision
		if rev == 0 {
			rev = 1
		}
		sv := r.SchemaVersion
		if sv == 0 {
			sv = 1
		}
		w.rec = Record{UUID: strings.ToLower(strings.TrimSpace(r.UUID)), Revision: rev, UserID: u.ID, Seq: r.Seq, Type: strings.TrimSpace(r.Type),
			SchemaVersion: sv, ReceivedAt: now, Payload: r.Payload}
		fail := func(code, key string, args ...any) {
			res := Reject(code, i18n.T(lang, key, args...))
			res.Flags = []string{}
			w.err = &res
		}
		ca, cerr := time.Parse(time.RFC3339Nano, r.CapturedAt)
		switch {
		case uuidInvalid(w.rec.UUID):
			fail(ReasonInvalidRecord, "sync.uuid_invalid")
		case rev < 1 || sv < 1 || r.Seq < 1:
			fail(ReasonInvalidRecord, "sync.seq_invalid")
		case w.rec.Type == "" || len(w.rec.Type) > 64:
			fail(ReasonInvalidRecord, "sync.type_required")
		case cerr != nil:
			fail(ReasonInvalidRecord, "sync.captured_invalid")
		case len(r.Payload) == 0 || r.Payload[0] != '{':
			fail(ReasonInvalidRecord, "sync.payload_invalid")
		case len(r.Payload) > MaxRecordBytes:
			fail(ReasonPayloadTooLarge, "sync.payload_too_large", "kb", MaxRecordBytes>>10)
			w.rec.Payload = json.RawMessage(fmt.Sprintf(`{"_omitted":"payload_too_large","bytes":%d}`, len(r.Payload)))
		case bytes.Contains(r.Payload, []byte(`\u0000`)): // PostgreSQL jsonb cannot store NUL
			fail(ReasonInvalidRecord, "sync.payload_invalid")
			w.rec.Payload = json.RawMessage(`{"_omitted":"nul_character"}`)
		}
		if cerr == nil {
			w.rec.CapturedAt = ca.UTC().Truncate(time.Millisecond)
			if w.err == nil && opts.revokedAt != nil && w.rec.CapturedAt.After(*opts.revokedAt) {
				fail(ReasonDeviceRevoked, "sync.captured_after_revoke")
			}
		}
		items[i] = w
	}

	var reviews []ReviewItem
	var counts = map[Status]int{}
	err := db.Tx(ctx, m.d.Pool, func(tx pgx.Tx) error {
		rowID, deviceID, snap, err := m.resolveDevice(c, ctx, tx, u, body.DeviceID, opts.revokedAt != nil)
		if err != nil {
			return err
		}
		actor := m.actorFor(c, u, rowID, deviceID, snap)
		actor.finalUpload = opts.revokedAt != nil
		// One push per device at a time: ordering and duplicate detection are then race-free.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 7))`, deviceID); err != nil {
			return err
		}
		var lastSeq int64
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(seq),0) FROM sync_ledger WHERE device_id=$1`, deviceID).Scan(&lastSeq); err != nil {
			return err
		}
		priors, err := loadPriors(ctx, tx, items)
		if err != nil {
			return err
		}
		order := make([]int, len(items))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool { return items[order[a]].rec.Seq < items[order[b]].rec.Seq })
		seenInBatch := map[string]int{} // uuid:rev -> index already handled in this batch
		for _, i := range order {
			w := &items[i]
			w.rec.DeviceID = deviceID
			res, rv := m.processOne(ctx, tx, w, actor, priors, seenInBatch, &lastSeq, now, maxSkew, deviceSkewed, &reviews)
			out.Results[i] = RecordResult{UUID: w.rec.UUID, Revision: rv, Result: res}
			counts[res.Status]++
		}
		pending := body.Pending
		if _, err := tx.Exec(ctx, `UPDATE device SET last_sync_at=$2, last_seen_at=$2, last_ip=$3,
			pending_count=coalesce($4, pending_count), pending_at=CASE WHEN $4::int IS NULL THEN pending_at ELSE $2 END,
			clock_skew_seconds=CASE WHEN $5 THEN $6::int ELSE clock_skew_seconds END WHERE id=$1`,
			rowID, now, c.IP(), pending, body.ClientTime != "", out.ClockSkewSeconds); err != nil {
			return err
		}
		if len(items) > 0 {
			if err := audit.Log(ctx, tx, actor.Audit(), audit.Input{Action: "sync.push", Entity: "Device", EntityID: rowID,
				Summary: fmt.Sprintf("Sync batch: %d records (%d accepted, %d duplicate, %d conflict, %d rejected)", len(items),
					counts[Accepted], counts[Duplicate], counts[Conflicted], counts[Rejected])}); err != nil {
				return err
			}
		}
		for _, it := range reviews {
			if OnReview != nil {
				sp, err := tx.Begin(ctx)
				if err != nil {
					return err
				}
				if herr := OnReview(ctx, sp, it); herr != nil {
					slog.Error("sync review hook failed", "err", herr, "item", it.ID)
					_ = sp.Rollback(ctx)
				} else if err := sp.Commit(ctx); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func uuidInvalid(s string) bool {
	_, err := uuid.Parse(s)
	return err != nil || len(s) != 36
}

func loadPriors(ctx context.Context, tx pgx.Tx, items []work) (map[string][]prior, error) {
	ids := []string{}
	for _, w := range items {
		if w.err == nil {
			ids = append(ids, w.rec.UUID)
		}
	}
	out := map[string][]prior{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT record_uuid::text, id, revision, status, reason_code, reason, server_id, server_version, flags, conflict_id
		FROM sync_ledger WHERE record_uuid = ANY($1::uuid[]) ORDER BY revision`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var p prior
		if err := rows.Scan(&id, &p.id, &p.revision, &p.status, &p.reasonCode, &p.reason, &p.serverID, &p.serverVersion, &p.flags, &p.conflictID); err != nil {
			return nil, err
		}
		out[id] = append(out[id], p)
	}
	return out, rows.Err()
}

// processOne decides and records one item; returns the result and the revision it refers to.
func (m *module) processOne(ctx context.Context, tx pgx.Tx, w *work, actor Actor, priors map[string][]prior, seen map[string]int,
	lastSeq *int64, now time.Time, maxSkew time.Duration, deviceSkewed bool, reviews *[]ReviewItem) (Result, int) {
	rec := w.rec
	if w.err != nil {
		if uuidInvalid(rec.UUID) {
			return *w.err, rec.Revision // nothing can be stored without a valid uuid
		}
	}
	key := fmt.Sprintf("%s:%d", rec.UUID, rec.Revision)
	// Duplicates: same (uuid, revision) already in the ledger, or repeated inside this batch.
	for _, p := range priors[rec.UUID] {
		if p.revision == rec.Revision {
			_, _ = tx.Exec(ctx, `UPDATE sync_ledger SET duplicate_count=duplicate_count+1, last_duplicate_at=$2 WHERE id=$1`, p.id, now)
			res := p.result() // original result fields, so the client can settle its badge
			res.OriginalStatus, res.Status = string(p.status), Duplicate
			return res, rec.Revision
		}
	}
	if _, dup := seen[key]; dup {
		return Result{Status: Duplicate, ReasonCode: "duplicate_in_batch", Flags: []string{}}, rec.Revision
	}
	seen[key] = w.idx
	for _, p := range priors[rec.UUID] {
		if p.status == Accepted {
			res := p.result()
			res.OriginalStatus, res.Status, res.ReasonCode = string(Accepted), Duplicate, ReasonAlreadyAccepted
			res.Reason = i18n.T(actor.Lang, "sync.already_accepted")
			return res, p.revision
		}
	}

	flags := []string{}
	if w.err == nil {
		if deviceSkewed || rec.CapturedAt.After(now.Add(maxSkew)) {
			flags = append(flags, FlagClockSkew)
		}
		if *lastSeq > 0 && rec.Seq > *lastSeq+1 {
			flags = append(flags, FlagSeqGap)
		}
		if rec.Seq <= *lastSeq {
			flags = append(flags, FlagSeqLate)
		}
		if rec.Seq > *lastSeq {
			*lastSeq = rec.Seq
		}
		if actor.finalUpload {
			flags = append(flags, FlagRevokedDevice)
		}
	}
	rec.Flags = flags

	var res Result
	switch {
	case w.err != nil:
		res = *w.err
	default:
		for _, p := range priors[rec.UUID] {
			if p.revision > rec.Revision {
				res = Reject(ReasonStaleRevision, i18n.T(actor.Lang, "sync.stale_revision"))
			}
		}
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return Reject(ReasonHandlerError, err.Error()), rec.Revision
	}
	applied := false
	if res.Status == "" {
		res = m.apply(ctx, sp, rec, actor)
		applied = res.Status == Accepted || res.Status == Conflicted
	}
	if !applied {
		_ = sp.Rollback(ctx)
		if sp, err = tx.Begin(ctx); err != nil {
			return Reject(ReasonHandlerError, err.Error()), rec.Revision
		}
	}
	res.Flags = joinFlags(flags, res.Flags...)
	plant := res.PlantID
	if plant == "" {
		plant = payloadPlant(rec.Payload)
	}
	if plant == "" {
		plant = actor.PlantID
	}
	var ledgerID int64
	err = sp.QueryRow(ctx, `INSERT INTO sync_ledger (record_uuid, revision, device_id, device_row_id, user_id, plant_id, seq, type, schema_version, payload,
		status, reason_code, reason, captured_at, received_at, processed_at, server_id, server_version, flags, conflict_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,now(),$16,$17,$18,$19) RETURNING id`,
		rec.UUID, rec.Revision, rec.DeviceID, actor.DeviceRowID, actor.UserID, nilIf(plant), rec.Seq, rec.Type, rec.SchemaVersion, payloadOrEmpty(rec.Payload),
		string(res.Status), nilIf(res.ReasonCode), nilIf(trunc(res.Reason, 1000)), capturedOr(rec.CapturedAt, now), now, nilIf(res.ServerID), nilIf64(res.ServerVersion), res.Flags, nilIf(res.ConflictID)).Scan(&ledgerID)
	if err != nil {
		_ = sp.Rollback(ctx)
		if pe := db.PgError(err); pe != nil && pe.Code == "23505" {
			return Result{Status: Duplicate, ReasonCode: "concurrent_duplicate", Flags: []string{}}, rec.Revision
		}
		slog.Error("sync ledger insert failed", "err", err, "type", rec.Type)
		return Reject(ReasonHandlerError, i18n.T(actor.Lang, "error.internal")), rec.Revision
	}
	if res.ConflictID != "" {
		if _, err := sp.Exec(ctx, `UPDATE sync_conflict SET ledger_id=$2 WHERE id=$1`, res.ConflictID, ledgerID); err != nil {
			_ = sp.Rollback(ctx)
			return Reject(ReasonHandlerError, err.Error()), rec.Revision
		}
		*reviews = append(*reviews, ReviewItem{ID: res.ConflictID, Kind: "conflict", Type: rec.Type, PlantID: plant, UserID: actor.UserID,
			DeviceID: rec.DeviceID, RecordUUID: rec.UUID, Entity: res.Entity, EntityID: res.EntityID, Reason: res.Reason})
	}
	if res.Status == Accepted && hasFlag(res.Flags, FlagNegativeStock) {
		var id string
		if err := sp.QueryRow(ctx, `INSERT INTO sync_conflict (kind, ledger_id, record_uuid, revision, type, entity, entity_id, plant_id, user_id, device_id, client_payload, reason)
			VALUES ('negative_stock',$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`, ledgerID, rec.UUID, rec.Revision, rec.Type, nilIf(res.Entity), nilIf(res.EntityID),
			nilIf(plant), actor.UserID, rec.DeviceID, payloadOrEmpty(rec.Payload), nilIf(res.Reason)).Scan(&id); err != nil {
			_ = sp.Rollback(ctx)
			return Reject(ReasonHandlerError, err.Error()), rec.Revision
		}
		*reviews = append(*reviews, ReviewItem{ID: id, Kind: "negative_stock", Type: rec.Type, PlantID: plant, UserID: actor.UserID,
			DeviceID: rec.DeviceID, RecordUUID: rec.UUID, Entity: res.Entity, EntityID: res.EntityID, Reason: res.Reason})
	}
	if err := sp.Commit(ctx); err != nil {
		return Reject(ReasonHandlerError, err.Error()), rec.Revision
	}
	return res, rec.Revision
}

// apply runs the handler with permission re-validation; panics become handler_error.
func (m *module) apply(ctx context.Context, tx pgx.Tx, rec Record, actor Actor) (res Result) {
	e, ok := handlerFor(rec.Type)
	if !ok {
		return Reject(ReasonUnknownType, i18n.T(actor.Lang, "sync.unknown_type", "type", rec.Type))
	}
	if len(e.perms) > 0 {
		cur, cap := false, false
		for _, p := range e.perms {
			cur = cur || auth.Can(actor.Role, p)
			cap = cap || actor.CapturedCan(p)
		}
		if !cap {
			return Reject(ReasonPermissionDenied, i18n.T(actor.Lang, "sync.permission_denied"))
		}
		if !cur {
			return Reject(ReasonPermissionRevoked, i18n.T(actor.Lang, "sync.permission_revoked"))
		}
	}
	defer func() {
		if p := recover(); p != nil {
			slog.Error("sync handler panic", "type", rec.Type, "panic", p, "stack", string(debug.Stack()))
			res = Reject(ReasonHandlerError, i18n.T(actor.Lang, "error.internal"))
		}
	}()
	res = e.h.Apply(ctx, tx, rec, actor)
	switch res.Status {
	case Accepted, Conflicted, Rejected:
	default:
		res = Reject(ReasonHandlerError, "handler returned no status")
	}
	if res.Status == Conflicted && res.ConflictID == "" {
		// The handler reported a conflict without storing it: keep at least the client version.
		res = Conflict(ctx, tx, rec, actor, ConflictInput{Reason: res.Reason, ServerVersion: res.ServerVersion})
	}
	return res
}

func hasFlag(flags []string, f string) bool {
	for _, x := range flags {
		if x == f {
			return true
		}
	}
	return false
}

func payloadPlant(raw json.RawMessage) string {
	var m struct {
		PlantID string `json:"plantId"`
		SiteID  string `json:"siteId"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	if m.PlantID != "" {
		return m.PlantID
	}
	return m.SiteID
}

func payloadOrEmpty(raw json.RawMessage) []byte {
	if len(raw) == 0 || !json.Valid(raw) || raw[0] != '{' {
		return []byte(`{}`)
	}
	return raw
}

func capturedOr(t, def time.Time) time.Time {
	if t.IsZero() {
		return def
	}
	return t
}

func nilIf(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nilIf64(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
