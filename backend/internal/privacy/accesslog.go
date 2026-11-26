package privacy

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
)

// loggedKey marks a request whose handler already wrote its access-log row (AccessLogger then skips it).
const loggedKey = "privacy.logged"

// entry is a fully copied access-log row (safe to hand to a goroutine after the request returns).
type entry struct {
	userID, subjectType, subjectID, action, route, ip, deviceID string
	rows                                                        *int
}

func entryFrom(c *fiber.Ctx, subjectType, subjectID, action string) entry {
	uid := ""
	if u := auth.FromCtx(c); u != nil {
		uid = u.ID
	}
	did, _ := c.Locals("deviceId").(string)
	route := c.Path()
	if len(route) > 300 {
		route = route[:300]
	}
	return entry{userID: strings.Clone(uid), subjectType: strings.Clone(subjectType), subjectID: strings.Clone(subjectID),
		action: action, route: strings.Clone(route), ip: strings.Clone(c.IP()), deviceID: strings.Clone(did)}
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func insertEntry(ctx context.Context, q db.DBTX, e entry) error {
	_, err := q.Exec(ctx, `INSERT INTO privacy_access_log (user_id, subject_type, subject_id, action, route, ip, device_id, rows, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		nilIfEmpty(e.userID), e.subjectType, nilIfEmpty(e.subjectID), e.action, e.route, nilIfEmpty(e.ip), nilIfEmpty(e.deviceID), e.rows, db.Now())
	return err
}

// LogAccess records that the signed-in user of c viewed / listed / exported / printed / corrected personal data
// of a subject (subjectID "" for list views). Pass the transaction when the access is part of a write.
// subjectType: "worker" | "customer" | "mixed"; action: "view" | "list" | "export" | "pdf" | "correct".
func LogAccess(ctx context.Context, q db.DBTX, c *fiber.Ctx, subjectType, subjectID, action string) error {
	c.Locals(loggedKey, true)
	return insertEntry(ctx, q, entryFrom(c, subjectType, subjectID, action))
}

// logAsync writes e in the background (bounded); when too many writes are in flight it writes inline.
// Errors are logged, never returned: an access-log failure must not fail the request.
var inflight = make(chan struct{}, 64)

func logAsync(d *deps.Deps, e entry) {
	write := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := insertEntry(ctx, d.Pool, e); err != nil {
			slog.Warn("privacy access log write failed", "err", err, "route", e.route, "action", e.action)
		}
	}
	select {
	case inflight <- struct{}{}:
		go func() {
			defer func() { <-inflight }()
			write()
		}()
	default:
		write()
	}
}

// AccessLogger is app-level middleware (mount right after auth.RequireAuth()). After the handler ran it
// records successful GETs (2xx, and 304 revalidations of data the client shows again) of personal-data routes:
// worker and customer lists/records of payroll, workforce, sales and master data, customer statements, wage
// sheet PDFs, their /export and ?format= variants, and the privacy module's own views.
// Matching is on c.Path() after the /api/v1 → /api rewrite (a leftover /api/v1 prefix is tolerated too).
func AccessLogger(d *deps.Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		err := c.Next()
		if err != nil || c.Method() != fiber.MethodGet {
			return err
		}
		if st := c.Response().StatusCode(); (st < 200 || st > 299) && st != fiber.StatusNotModified {
			return nil
		}
		if done, _ := c.Locals(loggedKey).(bool); done {
			return nil
		}
		typ, id, action, ok := Classify(c.Path(), c.Query("format"), c.Query("type"))
		if !ok || auth.FromCtx(c) == nil {
			return nil
		}
		logAsync(d, entryFrom(c, typ, id, action))
		return nil
	}
}

// Classify maps a request path to (subjectType, subjectID, action). ok=false: not a personal-data route.
// format is the ?format= value (csv/xlsx/json → export); typeQ is ?type= (privacy search).
func Classify(path, format, typeQ string) (subjectType, subjectID, action string, ok bool) {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	if len(segs) < 2 || !strings.EqualFold(segs[0], "api") {
		return "", "", "", false
	}
	segs = segs[1:]
	if strings.EqualFold(segs[0], "v1") {
		segs = segs[1:]
	}
	lower := make([]string, len(segs))
	for i, s := range segs {
		lower[i] = strings.ToLower(s)
	}
	exportish := func(rest []string) string {
		for _, s := range rest {
			switch s {
			case "export":
				return ActionExport
			case "pdf":
				return ActionPDF
			}
		}
		if format != "" {
			return ActionExport
		}
		return ""
	}
	// collection: /api/<module>/<collection>[/<id>[/...]]
	collection := func(typ string, rest, restRaw []string) (string, string, string, bool) {
		if len(rest) == 0 || rest[0] == "export" || rest[0] == "pdf" || rest[0] == "stats" {
			a := exportish(rest)
			if a == "" {
				a = ActionList
			}
			return typ, "", a, true
		}
		a := exportish(rest[1:])
		if a == "" {
			a = ActionView
		}
		return typ, restRaw[0], a, true
	}
	if len(lower) < 2 {
		return "", "", "", false
	}
	mod, coll := lower[0], lower[1]
	switch {
	case (mod == "payroll" || mod == "workforce") && coll == "workers":
		return collection(TypeWorker, lower[2:], segs[2:])
	case (mod == "sales" || mod == "masterdata") && coll == "customers":
		return collection(TypeCustomer, lower[2:], segs[2:])
	case mod == "sales" && coll == "pdf" && len(lower) >= 4 && lower[2] == "statement":
		return TypeCustomer, segs[3], ActionPDF, true
	case mod == "payroll" && coll == "pdf": // wage sheets / labour summary list workers, MoMo numbers and pay
		return TypeWorker, "", ActionPDF, true
	case mod == "privacy" && coll == "subjects":
		if len(lower) == 2 {
			switch typeQ {
			case TypeWorker, TypeCustomer:
				return typeQ, "", ActionList, true
			}
			return TypeMixed, "", ActionList, true
		}
		if len(lower) >= 4 && (lower[2] == TypeWorker || lower[2] == TypeCustomer) {
			a := ActionView
			if len(lower) >= 5 {
				switch lower[4] {
				case "export":
					a = ActionExport
				case "pdf":
					a = ActionPDF
				}
			}
			return lower[2], segs[3], a, true
		}
	}
	return "", "", "", false
}

// ── GET /access-log ─────────────────────────────────────────────────────────

type accessUser struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type accessRow struct {
	ID          string      `json:"id"`
	User        *accessUser `json:"user"`
	SubjectType string      `json:"subjectType"`
	SubjectID   *string     `json:"subjectId"`
	SubjectName *string     `json:"subjectName"`
	Action      string      `json:"action"`
	Route       string      `json:"route"`
	IP          *string     `json:"ip"`
	DeviceID    *string     `json:"deviceId"`
	Rows        *int        `json:"rows"`
	CreatedAt   time.Time   `json:"createdAt"`
}

func (h *handler) accessLog(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	qv := httpx.Query(c)
	st := qv.OptStr("subjectType").Trim().OneOf([]string{TypeWorker, TypeCustomer, TypeMixed}, "privacy.v.subject_type").Get()
	sid := qv.OptStr("subjectId").Trim().Max(64).Get()
	uid := qv.OptStr("userId").Trim().Max(64).Get()
	from := qv.OptStr("from").Trim().Date("privacy.v.date").Get()
	to := qv.OptStr("to").Trim().Date("privacy.v.date").Get()
	action := qv.OptStr("action").Trim().OneOf([]string{ActionView, ActionList, ActionExport, ActionPDF, ActionCorrect}, "privacy.v.action").Get()
	if err := qv.Err(); err != nil {
		return err
	}
	pg := httpx.Paginate(c, 50)
	conds := []string{"TRUE"}
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args))))
	}
	if st != "" {
		add(`l.subject_type = ?`, st)
	}
	if sid != "" {
		add(`l.subject_id = ?`, sid)
	}
	if uid != "" {
		add(`l.user_id = ?`, uid)
	}
	if action != "" {
		add(`l.action = ?`, action)
	}
	// business dates are Kigali calendar days; created_at is UTC
	if from != "" {
		f, _ := dates.ParseISO(from)
		add(`l.created_at >= ?`, kigaliMidnightUTC(f))
	}
	if to != "" {
		tt, _ := dates.ParseISO(to)
		add(`l.created_at < ?`, kigaliMidnightUTC(dates.AddDays(tt, 1)))
	}
	where := strings.Join(conds, " AND ")
	var total int
	if err := h.d.Pool.QueryRow(ctx, `SELECT count(*) FROM privacy_access_log l WHERE `+where, args...).Scan(&total); err != nil {
		return err
	}
	rows, err := h.d.Pool.Query(ctx, `SELECT l.id, u.id, u.name, u.email, l.subject_type, l.subject_id,
			CASE l.subject_type WHEN 'worker' THEN (SELECT btrim(w."firstName" || ' ' || w."lastName") FROM "Worker" w WHERE w.id = l.subject_id)
			                    WHEN 'customer' THEN (SELECT cu.name FROM "Customer" cu WHERE cu.id = l.subject_id) END,
			l.action, l.route, l.ip, l.device_id, l.rows, l.created_at
		FROM privacy_access_log l LEFT JOIN "User" u ON u.id = l.user_id
		WHERE `+where+` ORDER BY l.created_at DESC, l.id DESC LIMIT `+strconv.Itoa(pg.Limit)+` OFFSET `+strconv.Itoa(pg.Offset), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []accessRow{}
	for rows.Next() {
		var r accessRow
		var uID, uName, uEmail *string
		if err := rows.Scan(&r.ID, &uID, &uName, &uEmail, &r.SubjectType, &r.SubjectID, &r.SubjectName, &r.Action, &r.Route, &r.IP, &r.DeviceID, &r.Rows, &r.CreatedAt); err != nil {
			return err
		}
		if uID != nil {
			r.User = &accessUser{ID: *uID, Name: deref(uName), Email: deref(uEmail)}
		}
		r.CreatedAt = r.CreatedAt.UTC()
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	httpx.NoStore(c)
	return c.JSON(httpx.NewPage(items, total, pg.Page, pg.PageSize))
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// kigaliMidnightUTC is 00:00 Africa/Kigali of the calendar day d (UTC midnight), as a UTC instant.
func kigaliMidnightUTC(d time.Time) time.Time {
	d = d.UTC()
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, dates.Kigali()).UTC()
}
