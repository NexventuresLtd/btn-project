package reports

// Scheduled report administration (RPT-02, permission reports.schedule):
//
//	GET  /api/reports/schedules                 → {items:[ScheduleOut], options}
//	PUT  /api/reports/schedules/:key            → ScheduleOut (audited)
//	POST /api/reports/schedules/:key/send       {from?, to?} → 202 {run} ("send now", audited)
//	GET  /api/reports/runs?key=&status=&page=   → page of runs
//	GET  /api/reports/runs/:id                  → run (recipients + attachments)
//	GET  /api/reports/schedules/recipients?q=   → active users to pick as recipients

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/jobs"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// ScheduleOut is a schedule with its definition, last run and next send time.
type ScheduleOut struct {
	Schedule
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Frequencies []string    `json:"frequencies"`
	Parts       []string    `json:"parts"`
	Permissions []string    `json:"permissions"`
	NextRunAt   *string     `json:"nextRunAt"`
	LastRun     *RunOut     `json:"lastRun"`
	Recipients  []Recipient `json:"recipients"` // resolved now (preview)
}

// RunOut is one report_run row.
type RunOut struct {
	ID          string           `json:"id"`
	Key         string           `json:"key"`
	Title       string           `json:"title"`
	Trigger     string           `json:"trigger"`
	Status      string           `json:"status"`
	SlotAt      *time.Time       `json:"slotAt"`
	PeriodFrom  string           `json:"periodFrom"`
	PeriodTo    string           `json:"periodTo"`
	Attempts    int              `json:"attempts"`
	Recipients  []Recipient      `json:"recipients"`
	Attachments []AttachmentMeta `json:"attachments"`
	Error       *string          `json:"error"`
	RequestedBy *string          `json:"requestedBy"`
	StartedAt   *time.Time       `json:"startedAt"`
	FinishedAt  *time.Time       `json:"finishedAt"`
	CreatedAt   time.Time        `json:"createdAt"`
}

const runCols = `r.id, r.schedule_key, r.trigger, r.status, r.slot_at, r.period_from, r.period_to, r.attempts, r.recipients, r.attachments,
	r.error, u.name, r.started_at, r.finished_at, r.created_at`

func scanRun(row pgx.Row, lang i18n.Lang) (RunOut, error) {
	var r RunOut
	var pf, pt time.Time
	var rec, att []byte
	if err := row.Scan(&r.ID, &r.Key, &r.Trigger, &r.Status, &r.SlotAt, &pf, &pt, &r.Attempts, &rec, &att, &r.Error, &r.RequestedBy,
		&r.StartedAt, &r.FinishedAt, &r.CreatedAt); err != nil {
		return r, err
	}
	r.PeriodFrom, r.PeriodTo = dates.ToISO(pf), dates.ToISO(pt)
	r.Title = i18n.T(lang, "reports.schedule."+r.Key+".title")
	r.Recipients, r.Attachments = []Recipient{}, []AttachmentMeta{}
	_ = json.Unmarshal(rec, &r.Recipients)
	_ = json.Unmarshal(att, &r.Attachments)
	// a stored JSON null (a run that failed before resolving recipients or rendering) unmarshals to a nil slice
	if r.Recipients == nil {
		r.Recipients = []Recipient{}
	}
	if r.Attachments == nil {
		r.Attachments = []AttachmentMeta{}
	}
	return r, nil
}

func (h *handler) scheduleOut(ctx context.Context, s Schedule, lang i18n.Lang, last *RunOut, preview bool) (ScheduleOut, error) {
	def, _ := DefByKey(s.Key)
	o := ScheduleOut{Schedule: s, Title: i18n.T(lang, "reports.schedule."+s.Key+".title"), Description: i18n.T(lang, "reports.schedule."+s.Key+".description"),
		Frequencies: def.Frequencies, LastRun: last, Recipients: []Recipient{}}
	for _, p := range def.Parts {
		o.Parts = append(o.Parts, p.Key)
	}
	for _, p := range def.Perms {
		o.Permissions = append(o.Permissions, string(p))
	}
	if n := NextSlot(s, dates.Now()); s.Enabled && !n.IsZero() {
		v := n.UTC().Format(time.RFC3339)
		o.NextRunAt = &v
	}
	if preview {
		cands, err := loadCandidates(ctx, h.d, s)
		if err != nil {
			return o, err
		}
		o.Recipients = ResolveRecipients(def, s, cands)
	}
	return o, nil
}

func (h *handler) listSchedules(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	lang := httpx.Lang(c)
	scheds, err := LoadSchedules(ctx, h.d.Pool)
	if err != nil {
		return err
	}
	last := map[string]*RunOut{}
	rows, err := h.d.Pool.Query(ctx, `SELECT DISTINCT ON (r.schedule_key) `+runCols+` FROM report_run r LEFT JOIN "User" u ON u.id = r.requested_by
		ORDER BY r.schedule_key, r.created_at DESC`)
	if err != nil {
		return err
	}
	for rows.Next() {
		r, err := scanRun(rows, lang)
		if err != nil {
			rows.Close()
			return err
		}
		last[r.Key] = &r
	}
	rows.Close()
	items := []ScheduleOut{}
	for _, s := range scheds {
		o, err := h.scheduleOut(ctx, s, lang, last[s.Key], true)
		if err != nil {
			return err
		}
		items = append(items, o)
	}
	roles := []map[string]string{}
	for _, r := range auth.AllRoles {
		roles = append(roles, map[string]string{"value": string(r), "label": i18n.T(lang, "role."+string(r))})
	}
	httpx.NoStore(c)
	return c.JSON(fiber.Map{"items": items, "options": fiber.Map{"roles": roles, "formats": Formats, "languages": i18n.Codes(),
		"frequencies": []string{FreqDaily, FreqWeekly, FreqMonthly, FreqWagePeriod}}})
}

var reTime = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

func (h *handler) updateSchedule(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	key := c.Params("key")
	def, ok := DefByKey(key)
	if !ok {
		return httpx.NotFound("reports.schedule")
	}
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	lang := httpx.Lang(c)
	enabled := v.Bool("enabled").Get()
	freq := v.Str("frequency").OneOf(def.Frequencies, "reports.err.frequency").Get()
	sendTime := v.Str("sendTime").Regex(reTime, "reports.err.time").Get()
	dow := v.Num("dayOfWeek").Coerce().Whole().Min(1).Max(7).I()
	dom := v.Num("dayOfMonth").Coerce().Whole().Min(1).Max(28).I()
	plant := v.NullStr("plantId").Trim().Max(40).NilIfEmpty()
	language := v.OptStr("language").Default("").OneOf(append([]string{""}, i18n.Codes()...)).Get()
	formats := v.StrArr("formats", true)
	userIDs := v.StrArr("recipientUserIds", false)
	roles := v.StrArr("recipientRoles", false)
	emails := v.StrArr("recipientEmails", false)
	if len(formats) == 0 {
		v.Fail("formats", i18n.T(lang, "reports.err.formats"))
	}
	for i, f := range formats {
		v.Check(slices.Contains(Formats, f), "formats."+itoa(i), i18n.T(lang, "reports.err.formats"))
	}
	for i, r := range roles {
		v.Check(slices.Contains(auth.AllRoles, auth.Role(r)), "recipientRoles."+itoa(i), i18n.T(lang, "reports.err.role"))
	}
	for i := range emails {
		emails[i] = strings.TrimSpace(emails[i])
		v.Check(ValidEmail(emails[i]), "recipientEmails."+itoa(i), i18n.T(lang, "reports.err.email"))
	}
	v.Check(len(userIDs)+len(roles)+len(emails) <= 200, "recipientEmails", i18n.T(lang, "reports.err.too_many"))
	if enabled {
		v.Check(len(userIDs)+len(roles)+len(emails) > 0, "recipientRoles", i18n.T(lang, "reports.err.no_recipients"))
	}
	if err := v.Err(); err != nil {
		return err
	}
	formats, roles, emails, userIDs = uniq(formats), uniq(roles), uniq(emails), uniq(userIDs)
	if userIDs == nil {
		userIDs = []string{}
	}
	if roles == nil {
		roles = []string{}
	}
	if emails == nil {
		emails = []string{}
	}
	out, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (Schedule, error) {
		before, err := LoadSchedule(ctx, tx, key)
		if err != nil {
			return Schedule{}, err
		}
		if plant != nil {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM "Plant" WHERE id = $1`, *plant).Scan(&n); err != nil || n == 0 {
				return Schedule{}, httpx.BadRequest("reports.err.plant", map[string]string{"plantId": i18n.T(lang, "reports.err.plant")})
			}
		}
		if len(userIDs) > 0 {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM "User" WHERE id = ANY($1) AND "isActive"`, userIDs).Scan(&n); err != nil {
				return Schedule{}, err
			}
			if n != len(userIDs) {
				return Schedule{}, httpx.BadRequest("reports.err.user", map[string]string{"recipientUserIds": i18n.T(lang, "reports.err.user")})
			}
		}
		next := before
		next.Enabled, next.Frequency, next.SendTime, next.DayOfWeek, next.DayOfMonth = enabled, freq, sendTime, int(dow), int(dom)
		next.PlantID, next.Language, next.Formats = plant, language, formats
		next.RecipientUserIDs, next.RecipientRoles, next.RecipientEmails = userIDs, roles, emails
		// (re)enabling or re-timing starts from the next slot — never sends an old slot immediately
		var lastSlot *time.Time
		if slot := LatestSlot(next, dates.Now()); !slot.IsZero() {
			lastSlot = &slot
		} else if next.Frequency == FreqWagePeriod {
			now := dates.Now().UTC()
			lastSlot = &now
		}
		if before.Enabled && next.Frequency == before.Frequency && next.SendTime == before.SendTime && next.DayOfWeek == before.DayOfWeek &&
			next.DayOfMonth == before.DayOfMonth {
			lastSlot = before.LastSlotAt
		}
		next.LastSlotAt = lastSlot
		if _, err := tx.Exec(ctx, `UPDATE report_schedule SET enabled=$2, frequency=$3, send_time=$4, day_of_week=$5, day_of_month=$6, plant_id=$7,
				language=$8, formats=$9, recipient_user_ids=$10, recipient_roles=$11, recipient_emails=$12, last_slot_at=$13, updated_by=$14, updated_at=now()
			WHERE key=$1`, key, enabled, freq, sendTime, dow, dom, plant, language, formats, userIDs, roles, emails, lastSlot, u.ID); err != nil {
			return Schedule{}, err
		}
		if err := audit.Record(c, tx, audit.Input{Action: "reports.schedule_update", Entity: "ReportSchedule", EntityID: key,
			Summary: i18n.T(i18n.EN, "reports.schedule."+key+".title"), Before: before, After: next}); err != nil {
			return Schedule{}, err
		}
		return LoadSchedule(ctx, tx, key)
	})
	if err != nil {
		return err
	}
	o, err := h.scheduleOut(ctx, out, lang, nil, true)
	if err != nil {
		return err
	}
	return c.JSON(o)
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[strings.ToLower(s)] {
			seen[strings.ToLower(s)] = true
			out = append(out, s)
		}
	}
	return out
}

func itoa(i int) string { return strconv.Itoa(i) }

// sendNow queues a manual run (default period = the period of the latest slot) and pokes the job runner.
func (h *handler) sendNow(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	key := c.Params("key")
	if _, ok := DefByKey(key); !ok {
		return httpx.NotFound("reports.schedule")
	}
	lang := httpx.Lang(c)
	var body struct{ From, To string }
	if len(c.Body()) > 0 {
		v, err := httpx.Body(c)
		if err != nil {
			return err
		}
		body.From = v.OptStr("from").Date("dashboard.err.date_format").Get()
		body.To = v.OptStr("to").Date("dashboard.err.date_format").Get()
		if err := v.Err(); err != nil {
			return err
		}
	}
	s, err := LoadSchedule(ctx, h.d.Pool, key)
	if err != nil {
		return err
	}
	if len(s.RecipientUserIDs)+len(s.RecipientRoles)+len(s.RecipientEmails) == 0 {
		return httpx.BadRequest("reports.err.no_recipients", map[string]string{"recipientRoles": i18n.T(lang, "reports.err.no_recipients")})
	}
	from, to := defaultPeriod(s, dates.Now())
	if body.From != "" {
		from = dates.MustParseISO(body.From)
	}
	if body.To != "" {
		to = dates.MustParseISO(body.To)
	}
	if from.After(to) {
		return httpx.BadRequest("reports.err.range", map[string]string{"to": i18n.T(lang, "reports.err.range")})
	}
	run, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (RunOut, error) {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO report_run (schedule_key, trigger, status, period_from, period_to, requested_by)
			VALUES ($1, 'manual', 'queued', $2, $3, $4) RETURNING id`, key, from, to, u.ID).Scan(&id); err != nil {
			return RunOut{}, err
		}
		if err := audit.Record(c, tx, audit.Input{Action: "reports.schedule_send", Entity: "ReportRun", EntityID: id,
			Summary: i18n.T(i18n.EN, "reports.schedule."+key+".title") + " " + dates.FormatRange(from, to)}); err != nil {
			return RunOut{}, err
		}
		if waiting, err := jobs.HasQueuedManual(ctx, tx, JobName); err == nil && !waiting {
			if _, ok := jobs.Lookup(JobName); ok {
				if _, err := jobs.Enqueue(ctx, tx, JobName, u.ID); err != nil {
					return RunOut{}, err
				}
			}
		}
		return scanRun(tx.QueryRow(ctx, `SELECT `+runCols+` FROM report_run r LEFT JOIN "User" u ON u.id = r.requested_by WHERE r.id = $1`, id), lang)
	})
	if err != nil {
		return err
	}
	jobs.Wake()
	return c.Status(202).JSON(fiber.Map{"run": run})
}

func defaultPeriod(s Schedule, now time.Time) (time.Time, time.Time) {
	if s.Frequency == FreqWagePeriod {
		to := bizDate(now).AddDate(0, 0, -1)
		return to.AddDate(0, 0, -13), to
	}
	slot := LatestSlot(s, now)
	if slot.IsZero() {
		slot = now
	}
	return PeriodFor(s, slot)
}

func (h *handler) listRuns(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	pg := httpx.Paginate(c, 25)
	key, status := c.Query("key"), c.Query("status")
	var total int
	if err := h.d.Pool.QueryRow(ctx, `SELECT count(*) FROM report_run r WHERE ($1 = '' OR r.schedule_key = $1) AND ($2 = '' OR r.status = $2)`, key, status).Scan(&total); err != nil {
		return err
	}
	rows, err := h.d.Pool.Query(ctx, `SELECT `+runCols+` FROM report_run r LEFT JOIN "User" u ON u.id = r.requested_by
		WHERE ($1 = '' OR r.schedule_key = $1) AND ($2 = '' OR r.status = $2) ORDER BY r.created_at DESC LIMIT $3 OFFSET $4`, key, status, pg.Limit, pg.Offset)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []RunOut{}
	for rows.Next() {
		r, err := scanRun(rows, httpx.Lang(c))
		if err != nil {
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

func (h *handler) getRun(c *fiber.Ctx) error {
	r, err := scanRun(h.d.Pool.QueryRow(httpx.Ctx(c), `SELECT `+runCols+` FROM report_run r LEFT JOIN "User" u ON u.id = r.requested_by WHERE r.id = $1`,
		c.Params("id")), httpx.Lang(c))
	if err != nil {
		return err
	}
	httpx.NoStore(c)
	return c.JSON(fiber.Map{"run": r})
}

func (h *handler) recipientOptions(c *fiber.Ctx) error {
	q := strings.TrimSpace(c.Query("q"))
	rows, err := h.d.Pool.Query(httpx.Ctx(c), `SELECT u.id, u.name, u.email, u.role::text, p.code FROM "User" u LEFT JOIN "Plant" p ON p.id = u."plantId"
		WHERE u."isActive" AND ($1 = '' OR u.name ILIKE '%' || $1 || '%' OR u.email ILIKE '%' || $1 || '%') ORDER BY u.name LIMIT 50`, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	type opt struct {
		ID    string  `json:"id"`
		Name  string  `json:"name"`
		Email string  `json:"email"`
		Role  string  `json:"role"`
		Plant *string `json:"plant"`
	}
	out := []opt{}
	for rows.Next() {
		var o opt
		if err := rows.Scan(&o.ID, &o.Name, &o.Email, &o.Role, &o.Plant); err != nil {
			return err
		}
		out = append(out, o)
	}
	return c.JSON(fiber.Map{"items": out})
}
