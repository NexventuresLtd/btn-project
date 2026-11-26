package monitor

import (
	"strconv"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// RegisterAdmin mounts the monitoring API on r (= /api/ops/monitoring), permission admin.monitor:
//
//	GET  /health?fresh=1            health summary (fresh re-probes SMTP)
//	GET  /errors?status=open|muted|resolved|all&kind=&q=&page=
//	GET  /errors/:id                detail + recent occurrences
//	POST /errors/:id/resolve        mark resolved (re-opens automatically if it happens again)
//	POST /errors/:id/reopen
//	POST /errors/:id/mute           {hours: 1..720} — no alerts while muted; {hours: 0} unmutes
func RegisterAdmin(r fiber.Router, d *deps.Deps) {
	h := &handler{d}
	perm := auth.RequirePermission("admin.monitor")
	r.Get("/health", perm, h.health)
	r.Get("/errors", perm, h.listErrors)
	r.Get("/errors/:id", perm, h.getError)
	r.Post("/errors/:id/resolve", perm, h.resolve)
	r.Post("/errors/:id/reopen", perm, h.reopen)
	r.Post("/errors/:id/mute", perm, h.mute)
}

type handler struct{ d *deps.Deps }

func (h *handler) health(c *fiber.Ctx) error {
	httpx.NoStore(c)
	return c.JSON(Summary(httpx.Ctx(c), h.d, httpx.Lang(c), c.Query("fresh") == "1"))
}

// ErrorRow is an error_event as listed.
type ErrorRow struct {
	ID            string     `json:"id"`
	Kind          string     `json:"kind"`
	Method        *string    `json:"method"`
	Route         *string    `json:"route"`
	Status        *int       `json:"status"`
	Message       string     `json:"message"`
	Count         int        `json:"count"`
	FirstSeenAt   time.Time  `json:"firstSeenAt"`
	LastSeenAt    time.Time  `json:"lastSeenAt"`
	ResolvedAt    *time.Time `json:"resolvedAt"`
	MutedUntil    *time.Time `json:"mutedUntil"`
	State         string     `json:"state"` // open | muted | resolved
	Count24h      int        `json:"count24h"`
	LastPath      *string    `json:"lastPath,omitempty"`
	LastRequestID *string    `json:"lastRequestId,omitempty"`
	Stack         *string    `json:"stack,omitempty"`
	LastUser      *userRef   `json:"lastUser,omitempty"`
	ResolvedBy    *userRef   `json:"resolvedBy,omitempty"`
	Occurrences   []occ      `json:"occurrences,omitempty"`
}

type userRef struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type occ struct {
	At        time.Time `json:"at"`
	RequestID *string   `json:"requestId"`
	Path      *string   `json:"path"`
	UserID    *string   `json:"userId"`
}

const stateSQL = `CASE WHEN e.resolved_at IS NOT NULL THEN 'resolved' WHEN e.muted_until > now() AT TIME ZONE 'UTC' THEN 'muted' ELSE 'open' END`

func (h *handler) listErrors(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	pg := httpx.Paginate(c, 25)
	where := []string{"true"}
	args := []any{}
	arg := func(v any) string { args = append(args, v); return "$" + strconv.Itoa(len(args)) }
	switch c.Query("status", "open") {
	case "open":
		where = append(where, "e.resolved_at IS NULL AND (e.muted_until IS NULL OR e.muted_until <= "+arg(db.Now())+")")
	case "muted":
		where = append(where, "e.resolved_at IS NULL AND e.muted_until > "+arg(db.Now()))
	case "resolved":
		where = append(where, "e.resolved_at IS NOT NULL")
	case "all":
	default:
		return httpx.BadRequest("v.invalid", map[string]string{"status": "Invalid option"})
	}
	if k := c.Query("kind"); k != "" {
		if k != "panic" && k != "http" && k != "job" {
			return httpx.BadRequest("v.invalid", map[string]string{"kind": "Invalid option"})
		}
		where = append(where, "e.kind = "+arg(k))
	}
	if q := c.Query("q"); q != "" {
		p := arg("%" + q + "%")
		where = append(where, "(e.message ILIKE "+p+" OR e.route ILIKE "+p+")")
	}
	w := strings.Join(where, " AND ")
	var total int
	if err := h.d.Pool.QueryRow(ctx, `SELECT count(*) FROM error_event e WHERE `+w, args...).Scan(&total); err != nil {
		return err
	}
	since := arg(db.Now().Add(-24 * time.Hour))
	lim, off := arg(pg.Limit), arg(pg.Offset)
	rows, err := h.d.Pool.Query(ctx, `SELECT e.id, e.kind, e.method, e.route, e.status, e.message, e.count, e.first_seen_at, e.last_seen_at,
		e.resolved_at, e.muted_until, `+stateSQL+`, (SELECT count(*) FROM error_occurrence o WHERE o.event_id = e.id AND o.at >= `+since+`)
		FROM error_event e WHERE `+w+` ORDER BY e.last_seen_at DESC, e.id LIMIT `+lim+` OFFSET `+off, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []ErrorRow{}
	for rows.Next() {
		var r ErrorRow
		if err := rows.Scan(&r.ID, &r.Kind, &r.Method, &r.Route, &r.Status, &r.Message, &r.Count, &r.FirstSeenAt, &r.LastSeenAt,
			&r.ResolvedAt, &r.MutedUntil, &r.State, &r.Count24h); err != nil {
			return err
		}
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	httpx.NoStore(c)
	return c.JSON(httpx.NewPage(items, total, pg.Page, pg.PageSize))
}

func (h *handler) loadError(c *fiber.Ctx, q db.DBTX, id string) (ErrorRow, error) {
	ctx := httpx.Ctx(c)
	var r ErrorRow
	var luID, luName, luEmail, rbID, rbName, rbEmail *string
	err := q.QueryRow(ctx, `SELECT e.id, e.kind, e.method, e.route, e.status, e.message, e.count, e.first_seen_at, e.last_seen_at,
		e.resolved_at, e.muted_until, `+stateSQL+`, (SELECT count(*) FROM error_occurrence o WHERE o.event_id = e.id AND o.at >= $2),
		e.last_path, e.last_request_id, e.stack, lu.id, lu.name, lu.email, rb.id, rb.name, rb.email
		FROM error_event e LEFT JOIN "User" lu ON lu.id = e.last_user_id LEFT JOIN "User" rb ON rb.id = e.resolved_by
		WHERE e.id = $1`, id, db.Now().Add(-24*time.Hour)).Scan(&r.ID, &r.Kind, &r.Method, &r.Route, &r.Status, &r.Message, &r.Count,
		&r.FirstSeenAt, &r.LastSeenAt, &r.ResolvedAt, &r.MutedUntil, &r.State, &r.Count24h, &r.LastPath, &r.LastRequestID, &r.Stack,
		&luID, &luName, &luEmail, &rbID, &rbName, &rbEmail)
	if db.IsNoRows(err) {
		return r, httpx.NotFound("entity.error_event")
	}
	if err != nil {
		return r, err
	}
	if luID != nil {
		r.LastUser = &userRef{*luID, deref(luName), deref(luEmail)}
	}
	if rbID != nil {
		r.ResolvedBy = &userRef{*rbID, deref(rbName), deref(rbEmail)}
	}
	return r, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (h *handler) getError(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	r, err := h.loadError(c, h.d.Pool, c.Params("id"))
	if err != nil {
		return err
	}
	rows, err := h.d.Pool.Query(ctx, `SELECT at, request_id, path, user_id FROM error_occurrence WHERE event_id=$1 ORDER BY at DESC LIMIT 20`, r.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	r.Occurrences = []occ{}
	for rows.Next() {
		var o occ
		if err := rows.Scan(&o.At, &o.RequestID, &o.Path, &o.UserID); err != nil {
			return err
		}
		r.Occurrences = append(r.Occurrences, o)
	}
	httpx.NoStore(c)
	return c.JSON(r)
}

func (h *handler) change(c *fiber.Ctx, action, summary, sql string, extra ...any) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	id := c.Params("id")
	out, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (ErrorRow, error) {
		before, err := h.loadError(c, tx, id)
		if err != nil {
			return before, err
		}
		args := append([]any{id, u.ID, db.Now()}, extra...)
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return before, err
		}
		after, err := h.loadError(c, tx, id)
		if err != nil {
			return after, err
		}
		return after, audit.Record(c, tx, audit.Input{Action: action, Entity: "ErrorEvent", EntityID: id, Summary: summary + ": " + truncate(before.Message, 120),
			Before: map[string]any{"state": before.State, "mutedUntil": before.MutedUntil}, After: map[string]any{"state": after.State, "mutedUntil": after.MutedUntil}})
	})
	if err != nil {
		return err
	}
	return c.JSON(out)
}

func (h *handler) resolve(c *fiber.Ctx) error {
	return h.change(c, "monitor.resolve", "Error resolved", `UPDATE error_event SET resolved_at=$3, resolved_by=$2 WHERE id=$1`)
}

func (h *handler) reopen(c *fiber.Ctx) error {
	return h.change(c, "monitor.reopen", "Error reopened", `UPDATE error_event SET resolved_at=NULL, resolved_by=NULL WHERE id=$1 AND $2::text IS NOT NULL AND $3::timestamp IS NOT NULL`)
}

func (h *handler) mute(c *fiber.Ctx) error {
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	hours := v.Num("hours").Coerce().Whole().Min(0).Max(720).I()
	if err := v.Err(); err != nil {
		return err
	}
	if hours == 0 {
		return h.change(c, "monitor.unmute", "Error unmuted", `UPDATE error_event SET muted_until=NULL WHERE id=$1 AND $2::text IS NOT NULL AND $3::timestamp IS NOT NULL`)
	}
	return h.change(c, "monitor.mute", "Error muted", `UPDATE error_event SET muted_until=$3::timestamp + make_interval(hours => $4::int) WHERE id=$1 AND $2::text IS NOT NULL`, int(hours))
}
