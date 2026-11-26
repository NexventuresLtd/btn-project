package syncsvc

import (
	"encoding/json"
	"strconv"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
)

// Admin sync monitor (ADM-03, admin.monitor): per device and per site — last successful sync, pending (client
// reported), accepted/rejected counts, open conflicts, clock-skew flags, revoked devices; drill-down to ledger rows.

type monitorDevice struct {
	ID               string     `json:"id"`
	DeviceID         string     `json:"deviceId"`
	Name             string     `json:"name"`
	Platform         string     `json:"platform"`
	Status           string     `json:"status"`
	UserID           string     `json:"userId"`
	UserName         string     `json:"userName"`
	UserEmail        string     `json:"userEmail"`
	Role             string     `json:"role"`
	PlantID          *string    `json:"plantId"`
	PlantCode        *string    `json:"plantCode"`
	LastSeenAt       time.Time  `json:"lastSeenAt"`
	LastSyncAt       *time.Time `json:"lastSyncAt"`
	LastAcceptedAt   *time.Time `json:"lastSuccessfulSyncAt"`
	Pending          *int       `json:"pending"`
	PendingAt        *time.Time `json:"pendingAt"`
	ClockSkewSeconds *int       `json:"clockSkewSeconds"`
	Accepted         int        `json:"accepted"`
	Rejected         int        `json:"rejected"`
	Conflicts        int        `json:"conflicts"`
	OpenConflicts    int        `json:"openConflicts"`
	OpenReviews      int        `json:"openReviews"`
	SkewFlags        int        `json:"clockSkewFlags"`
	RevokedAt        *time.Time `json:"revokedAt"`
	OfflineExpiresAt *time.Time `json:"offlineExpiresAt"`
}

type monitorSite struct {
	PlantID        *string    `json:"plantId"`
	PlantCode      *string    `json:"plantCode"`
	PlantName      *string    `json:"plantName"`
	Devices        int        `json:"devices"`
	ActiveDevices  int        `json:"activeDevices"`
	RevokedDevices int        `json:"revokedDevices"`
	LastAcceptedAt *time.Time `json:"lastSuccessfulSyncAt"`
	Pending        int        `json:"pending"`
	Accepted       int        `json:"accepted"`
	Rejected       int        `json:"rejected"`
	OpenConflicts  int        `json:"openConflicts"`
	OpenReviews    int        `json:"openReviews"`
	SkewFlags      int        `json:"clockSkewFlags"`
}

// GET /sync/monitor?plantId=&status=
func (m *module) monitor(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	httpx.NoStore(c)
	w := newWhere()
	if p := c.Query("plantId"); p != "" {
		w.add(`u."plantId" = ?`, p)
	}
	if s := c.Query("status"); s == auth.DeviceActive || s == auth.DeviceRevoked {
		w.add(`d.status = ?`, s)
	}
	rows, err := m.d.Pool.Query(ctx, `WITH led AS (
			SELECT device_row_id, count(*) FILTER (WHERE status='accepted') acc, count(*) FILTER (WHERE status='rejected') rej,
				count(*) FILTER (WHERE status='conflict') con, count(*) FILTER (WHERE 'CLOCK_SKEW' = ANY(flags)) skew,
				max(processed_at) FILTER (WHERE status='accepted') last_ok
			FROM sync_ledger GROUP BY device_row_id),
		conf AS (
			SELECT c.device_id, c.user_id, count(*) FILTER (WHERE kind='conflict') oc, count(*) FILTER (WHERE kind<>'conflict') orv
			FROM sync_conflict c WHERE status='open' GROUP BY 1, 2)
		SELECT d.id, d.device_id::text, d.name, d.platform, d.status, u.id, u.name, u.email, u.role::text, p.id, p.code,
			d.last_seen_at, d.last_sync_at, l.last_ok, d.pending_count, d.pending_at, d.clock_skew_seconds,
			coalesce(l.acc,0), coalesce(l.rej,0), coalesce(l.con,0), coalesce(cf.oc,0), coalesce(cf.orv,0), coalesce(l.skew,0),
			d.revoked_at, d.offline_expires_at
		FROM device d JOIN "User" u ON u.id=d.user_id LEFT JOIN "Plant" p ON p.id=u."plantId"
		LEFT JOIN led l ON l.device_row_id=d.id LEFT JOIN conf cf ON cf.device_id=d.device_id AND cf.user_id=d.user_id
		WHERE `+w.sql()+` ORDER BY d.status, coalesce(d.last_sync_at, d.last_seen_at) DESC LIMIT 1000`, w.args...)
	if err != nil {
		return err
	}
	devices := []monitorDevice{}
	for rows.Next() {
		var x monitorDevice
		if err := rows.Scan(&x.ID, &x.DeviceID, &x.Name, &x.Platform, &x.Status, &x.UserID, &x.UserName, &x.UserEmail, &x.Role, &x.PlantID, &x.PlantCode,
			&x.LastSeenAt, &x.LastSyncAt, &x.LastAcceptedAt, &x.Pending, &x.PendingAt, &x.ClockSkewSeconds,
			&x.Accepted, &x.Rejected, &x.Conflicts, &x.OpenConflicts, &x.OpenReviews, &x.SkewFlags, &x.RevokedAt, &x.OfflineExpiresAt); err != nil {
			rows.Close()
			return err
		}
		devices = append(devices, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// Per site: ledger rows by the record's site, devices by their user's site.
	srows, err := m.d.Pool.Query(ctx, `WITH dev AS (
			SELECT u."plantId" pid, count(*) n, count(*) FILTER (WHERE d.status='active') act, count(*) FILTER (WHERE d.status='revoked') rev,
				coalesce(sum(d.pending_count) FILTER (WHERE d.status='active'),0) pend
			FROM device d JOIN "User" u ON u.id=d.user_id GROUP BY 1),
		led AS (
			SELECT plant_id pid, count(*) FILTER (WHERE status='accepted') acc, count(*) FILTER (WHERE status='rejected') rej,
				count(*) FILTER (WHERE 'CLOCK_SKEW' = ANY(flags)) skew, max(processed_at) FILTER (WHERE status='accepted') last_ok
			FROM sync_ledger GROUP BY 1),
		conf AS (
			SELECT plant_id pid, count(*) FILTER (WHERE kind='conflict') oc, count(*) FILTER (WHERE kind<>'conflict') orv
			FROM sync_conflict WHERE status='open' GROUP BY 1),
		ids AS (SELECT pid FROM dev UNION SELECT pid FROM led UNION SELECT pid FROM conf)
		SELECT ids.pid, p.code, p.name, coalesce(dev.n,0), coalesce(dev.act,0), coalesce(dev.rev,0), led.last_ok, coalesce(dev.pend,0)::int,
			coalesce(led.acc,0), coalesce(led.rej,0), coalesce(conf.oc,0), coalesce(conf.orv,0), coalesce(led.skew,0)
		FROM ids LEFT JOIN "Plant" p ON p.id=ids.pid LEFT JOIN dev ON dev.pid IS NOT DISTINCT FROM ids.pid
		LEFT JOIN led ON led.pid IS NOT DISTINCT FROM ids.pid LEFT JOIN conf ON conf.pid IS NOT DISTINCT FROM ids.pid
		ORDER BY p.code NULLS LAST`)
	if err != nil {
		return err
	}
	sites := []monitorSite{}
	for srows.Next() {
		var s monitorSite
		if err := srows.Scan(&s.PlantID, &s.PlantCode, &s.PlantName, &s.Devices, &s.ActiveDevices, &s.RevokedDevices, &s.LastAcceptedAt, &s.Pending,
			&s.Accepted, &s.Rejected, &s.OpenConflicts, &s.OpenReviews, &s.SkewFlags); err != nil {
			srows.Close()
			return err
		}
		sites = append(sites, s)
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"devices": devices, "sites": sites, "serverTime": time.Now().UTC(), "recordTypes": RecordTypes()})
}

type ledgerRow struct {
	ID            int64           `json:"id"`
	RecordUUID    string          `json:"recordUuid"`
	Revision      int             `json:"revision"`
	DeviceID      string          `json:"deviceId"`
	DeviceRowID   *string         `json:"deviceRowId"`
	DeviceName    *string         `json:"deviceName"`
	UserID        *string         `json:"userId"`
	UserName      *string         `json:"userName"`
	PlantID       *string         `json:"plantId"`
	PlantCode     *string         `json:"plantCode"`
	Seq           int64           `json:"seq"`
	Type          string          `json:"type"`
	SchemaVersion int             `json:"schemaVersion"`
	Status        string          `json:"status"`
	ReasonCode    *string         `json:"reasonCode"`
	Reason        *string         `json:"reason"`
	CapturedAt    time.Time       `json:"capturedAt"`
	ReceivedAt    time.Time       `json:"receivedAt"`
	ProcessedAt   *time.Time      `json:"processedAt"`
	ServerID      *string         `json:"serverId"`
	ServerVersion *int64          `json:"serverVersion"`
	Flags         []string        `json:"flags"`
	ConflictID    *string         `json:"conflictId"`
	Duplicates    int             `json:"duplicates"`
	Payload       json.RawMessage `json:"payload,omitempty"`
}

const ledgerCols = `l.id, l.record_uuid::text, l.revision, l.device_id::text, l.device_row_id, d.name, l.user_id, u.name, l.plant_id, p.code, l.seq, l.type,
	l.schema_version, l.status, l.reason_code, l.reason, l.captured_at, l.received_at, l.processed_at, l.server_id, l.server_version, l.flags,
	l.conflict_id, l.duplicate_count`

const ledgerFrom = ` FROM sync_ledger l LEFT JOIN device d ON d.id=l.device_row_id LEFT JOIN "User" u ON u.id=l.user_id LEFT JOIN "Plant" p ON p.id=l.plant_id`

func scanLedger(row interface{ Scan(...any) error }, withPayload bool) (*ledgerRow, error) {
	var r ledgerRow
	dst := []any{&r.ID, &r.RecordUUID, &r.Revision, &r.DeviceID, &r.DeviceRowID, &r.DeviceName, &r.UserID, &r.UserName, &r.PlantID, &r.PlantCode,
		&r.Seq, &r.Type, &r.SchemaVersion, &r.Status, &r.ReasonCode, &r.Reason, &r.CapturedAt, &r.ReceivedAt, &r.ProcessedAt, &r.ServerID,
		&r.ServerVersion, &r.Flags, &r.ConflictID, &r.Duplicates}
	var payload []byte
	if withPayload {
		dst = append(dst, &payload)
	}
	if err := row.Scan(dst...); err != nil {
		return nil, err
	}
	if withPayload {
		r.Payload = payload
	}
	return &r, nil
}

// GET /sync/monitor/ledger?deviceRowId=&deviceId=&userId=&plantId=&status=&type=&flag=&from=&to=&page=
func (m *module) ledgerList(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	httpx.NoStore(c)
	pg := httpx.Paginate(c, 50)
	w := newWhere()
	if v := c.Query("deviceRowId"); v != "" {
		w.add("l.device_row_id = ?", v)
	}
	if v := c.Query("deviceId"); v != "" && !uuidInvalid(v) {
		w.add("l.device_id = ?::uuid", v)
	}
	if v := c.Query("userId"); v != "" {
		w.add("l.user_id = ?", v)
	}
	if v := c.Query("plantId"); v != "" {
		w.add("l.plant_id = ?", v)
	}
	if v := c.Query("status"); v == "accepted" || v == "rejected" || v == "conflict" || v == "duplicate" {
		w.add("l.status = ?", v)
	}
	if v := c.Query("type"); v != "" {
		w.add("l.type = ?", v)
	}
	if v := c.Query("flag"); v != "" {
		w.add("? = ANY(l.flags)", v)
	}
	if v := c.Query("uuid"); v != "" && !uuidInvalid(v) {
		w.add("l.record_uuid = ?::uuid", v)
	}
	if v, err := time.Parse("2006-01-02", c.Query("from")); err == nil {
		w.add("l.received_at >= ?", v)
	}
	if v, err := time.Parse("2006-01-02", c.Query("to")); err == nil {
		w.add("l.received_at < ?", v.Add(24*time.Hour))
	}
	var total int
	if err := m.d.Pool.QueryRow(ctx, `SELECT count(*) FROM sync_ledger l WHERE `+w.sql(), w.args...).Scan(&total); err != nil {
		return err
	}
	lim, off := w.page(pg.Limit, pg.Offset)
	rows, err := m.d.Pool.Query(ctx, `SELECT `+ledgerCols+ledgerFrom+` WHERE `+w.sql()+` ORDER BY l.received_at DESC, l.id DESC`+lim+off, w.args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []*ledgerRow{}
	for rows.Next() {
		r, err := scanLedger(rows, false)
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

// GET /sync/monitor/ledger/:id (with payload)
func (m *module) ledgerGet(c *fiber.Ctx) error {
	httpx.NoStore(c)
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return httpx.NotFound("entity.sync_record")
	}
	r, err := scanLedger(m.d.Pool.QueryRow(httpx.Ctx(c), `SELECT `+ledgerCols+`, l.payload`+ledgerFrom+` WHERE l.id=$1`, id), true)
	if err != nil {
		if db.IsNoRows(err) {
			return httpx.NotFound("entity.sync_record")
		}
		return err
	}
	return c.JSON(fiber.Map{"record": r})
}
