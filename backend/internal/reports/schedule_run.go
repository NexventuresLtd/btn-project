package reports

// Scheduled report runner (RPT-02). One F1 job, `reports.scheduled` (every minute):
//  1. dispatch — enabled schedules whose slot is due get a report_run row (unique per schedule + slot);
//  2. process — queued/retrying runs are generated and e-mailed. Each recipient group (same role + site +
//     language) is rendered by calling the app's own report endpoints in-process with a short-lived session of a
//     recipient, so every e-mailed PDF/Excel is exactly the report that user sees on screen (permissions, site
//     scope, language, RPT-06 cut-off). Raw e-mail recipients get the all-site view rendered as the first Super
//     Administrator. Failed deliveries are retried (5 min, 15 min) up to MaxAttempts; the final failure fails
//     the job run so F1 alerts admin.monitor holders.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/cutoff"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/i18n"
	"btn-ops/internal/jobs"
	"btn-ops/internal/mail"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// JobName is the F1 job that dispatches and sends scheduled reports.
const JobName = "reports.scheduled"

// MaxAttempts per run before it is marked failed.
const MaxAttempts = 3

// MaxAttachmentBytes caps one e-mail; beyond it only PDFs are attached and the e-mail says so.
const MaxAttachmentBytes = 18 << 20

var apps sync.Map // *deps.Deps → *fiber.App

// BindApp gives the runner the application it renders reports with (called from app.New).
func BindApp(d *deps.Deps, app *fiber.App) { apps.Store(d, app) }

func appFor(d *deps.Deps) *fiber.App {
	if v, ok := apps.Load(d); ok {
		return v.(*fiber.App)
	}
	return nil
}

// SenderFor returns the mail transport (tests replace it with a fake).
var SenderFor = func(d *deps.Deps) mail.AttachmentSender {
	if s, ok := d.Mail.(mail.AttachmentSender); ok {
		return s
	}
	return nil
}

func registerJob(d *deps.Deps) {
	jobs.Register(jobs.Job{Name: JobName, Schedule: "* * * * *", Timeout: 15 * time.Minute, DescriptionKey: "reports.job.scheduled",
		Run: func(ctx context.Context) error { return RunDue(ctx, d, time.Now()) }})
}

// RunDue dispatches due schedules and processes queued runs (the job body; tests call it directly).
func RunDue(ctx context.Context, d *deps.Deps, now time.Time) error {
	if err := Dispatch(ctx, d, now); err != nil {
		return err
	}
	return ProcessQueued(ctx, d, now)
}

const scheduleCols = `key, enabled, frequency, send_time, day_of_week, day_of_month, plant_id, language, formats,
	recipient_user_ids, recipient_roles, recipient_emails, last_slot_at, updated_at`

func scanSchedule(r pgx.Row) (Schedule, error) {
	var s Schedule
	err := r.Scan(&s.Key, &s.Enabled, &s.Frequency, &s.SendTime, &s.DayOfWeek, &s.DayOfMonth, &s.PlantID, &s.Language, &s.Formats,
		&s.RecipientUserIDs, &s.RecipientRoles, &s.RecipientEmails, &s.LastSlotAt, &s.UpdatedAt)
	return s, err
}

// scheduleDefaults seeds the §10 rows (also done by migration 02600; repeated here so a reset database or a
// definition added later always has its row). Disabled until an administrator confirms the recipients.
var scheduleDefaults = []struct {
	key, freq, time string
	dom             int
	roles           []string
}{
	{"daily_production", FreqDaily, "06:30", 1, []string{"PRODUCTION_MANAGER", "GENERAL_MANAGER", "MANAGING_DIRECTOR"}},
	{"daily_stock", FreqDaily, "06:35", 1, []string{"PRODUCTION_MANAGER", "STOCK_OFFICER", "GENERAL_MANAGER"}},
	{"daily_headcount", FreqDaily, "06:40", 1, []string{"PRODUCTION_MANAGER", "FINANCE_MANAGER"}},
	{"orders_sales", FreqWeekly, "07:00", 1, []string{"SALES_MANAGER", "GENERAL_MANAGER", "MANAGING_DIRECTOR", "FINANCE_MANAGER"}},
	{"stock_movement_variance", FreqWeekly, "07:10", 1, []string{"PRODUCTION_MANAGER", "STOCK_OFFICER", "GENERAL_MANAGER"}},
	{"procurement_expense", FreqMonthly, "07:20", 1, []string{"FINANCE_MANAGER", "PROCUREMENT_OFFICER", "GENERAL_MANAGER", "MANAGING_DIRECTOR"}},
	{"wage_workforce", FreqWagePeriod, "07:30", 1, []string{"FINANCE_MANAGER", "PRODUCTION_MANAGER", "GENERAL_MANAGER"}},
	{"budget_monitoring", FreqMonthly, "07:40", 2, []string{"FINANCE_MANAGER", "GENERAL_MANAGER", "MANAGING_DIRECTOR"}},
	{"site_performance", FreqMonthly, "07:50", 2, []string{"GENERAL_MANAGER", "MANAGING_DIRECTOR", "OPERATIONS_ADVISOR", "PRODUCTION_MANAGER"}},
}

// EnsureSchedules inserts missing schedule rows (idempotent).
func EnsureSchedules(ctx context.Context, q db.DBTX) error {
	for _, d := range scheduleDefaults {
		if _, err := q.Exec(ctx, `INSERT INTO report_schedule (key, frequency, send_time, day_of_month, recipient_roles) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (key) DO NOTHING`, d.key, d.freq, d.time, d.dom, d.roles); err != nil {
			return err
		}
	}
	return nil
}

// LoadSchedules returns every schedule (Defs order).
func LoadSchedules(ctx context.Context, q db.DBTX) ([]Schedule, error) {
	if err := EnsureSchedules(ctx, q); err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `SELECT `+scheduleCols+` FROM report_schedule`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byKey := map[string]Schedule{}
	for rows.Next() {
		s, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		byKey[s.Key] = s
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []Schedule{}
	for _, d := range Defs {
		if s, ok := byKey[d.Key]; ok {
			out = append(out, s)
		}
	}
	return out, nil
}

// LoadSchedule returns one schedule.
func LoadSchedule(ctx context.Context, q db.DBTX, key string) (Schedule, error) {
	if _, ok := DefByKey(key); ok {
		if err := EnsureSchedules(ctx, q); err != nil {
			return Schedule{}, err
		}
	}
	return scanSchedule(q.QueryRow(ctx, `SELECT `+scheduleCols+` FROM report_schedule WHERE key = $1`, key))
}

// Dispatch creates the report_run rows of due slots.
func Dispatch(ctx context.Context, d *deps.Deps, now time.Time) error {
	scheds, err := LoadSchedules(ctx, d.Pool)
	if err != nil {
		return err
	}
	for _, s := range scheds {
		if !s.Enabled {
			continue
		}
		type due struct{ slot, from, to time.Time }
		var list []due
		if s.Frequency == FreqWagePeriod {
			periods, err := recentWagePeriods(ctx, d, s, now)
			if err != nil {
				return err
			}
			for _, w := range WageSlots(s, periods, now) {
				list = append(list, due{w.Slot, w.From, w.To})
			}
		} else if slot, ok := DueSlot(s, now); ok {
			f, t := PeriodFor(s, slot)
			list = append(list, due{slot, f, t})
		}
		for _, x := range list {
			if err := db.Tx(ctx, d.Pool, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `INSERT INTO report_run (schedule_key, trigger, status, slot_at, period_from, period_to)
					VALUES ($1, 'schedule', 'queued', $2, $3, $4) ON CONFLICT DO NOTHING`, s.Key, x.slot, x.from, x.to); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `UPDATE report_schedule SET last_slot_at = $2 WHERE key = $1 AND (last_slot_at IS NULL OR last_slot_at < $2)`, s.Key, x.slot)
				return err
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func recentWagePeriods(ctx context.Context, d *deps.Deps, s Schedule, now time.Time) ([]WagePeriod, error) {
	rows, err := d.Pool.Query(ctx, `SELECT "startDate", "endDate" FROM "PayrollPeriod" WHERE "endDate" >= $1::date AND "endDate" < $2::date
		AND ($3 = '' OR "plantId" = $3)`, bizDate(now).AddDate(0, 0, -8), bizDate(now), s.plant())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WagePeriod
	for rows.Next() {
		var w WagePeriod
		if err := rows.Scan(&w.Start, &w.End); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// Attachment metadata stored on the run.
type AttachmentMeta struct {
	Name   string `json:"name"`
	Part   string `json:"part"`
	Format string `json:"format"`
	Bytes  int    `json:"bytes"`
	Lang   string `json:"lang"`
	Plant  string `json:"plant,omitempty"`
	Status string `json:"status"` // attached | unavailable | forbidden | too_large
}

type claimed struct {
	ID, Key, Trigger string
	From, To         time.Time
	Attempts         int
	Recipients       []Recipient
}

// ProcessQueued generates and sends every due run. A run whose last attempt failed makes the job fail.
func ProcessQueued(ctx context.Context, d *deps.Deps, now time.Time) error {
	var finalErrs []string
	for i := 0; i < 50; i++ {
		var r claimed
		var recJSON []byte
		err := d.Pool.QueryRow(ctx, `UPDATE report_run SET status = 'running', started_at = $1, attempts = attempts + 1
			WHERE id = (SELECT id FROM report_run WHERE status IN ('queued','retrying') AND next_attempt_at <= $1
				ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1)
			RETURNING id, schedule_key, trigger, period_from, period_to, attempts, recipients`, now.UTC()).
			Scan(&r.ID, &r.Key, &r.Trigger, &r.From, &r.To, &r.Attempts, &recJSON)
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			return err
		}
		_ = json.Unmarshal(recJSON, &r.Recipients)
		res := processRun(ctx, d, r, now)
		status := "success"
		var errText *string
		next := now.UTC()
		failed := 0
		sent := 0
		for _, x := range res.recipients {
			switch x.Status {
			case "failed", "pending":
				failed++
			case "sent":
				sent++
			}
		}
		switch {
		case res.err != nil || failed > 0:
			msg := "delivery failed"
			if res.err != nil {
				msg = res.err.Error()
			} else {
				for _, x := range res.recipients {
					if x.Status == "failed" && x.Reason != "" {
						msg = x.Email + ": " + x.Reason
						break
					}
				}
			}
			errText = &msg
			if r.Attempts < MaxAttempts {
				status = "retrying"
				next = now.UTC().Add(retryDelay(r.Attempts))
			} else {
				status = "failed"
				if sent > 0 {
					status = "partial"
				}
				finalErrs = append(finalErrs, r.Key+": "+msg)
			}
		case sent == 0:
			status = "skipped"
			m := "no_recipients"
			errText = &m
		}
		recB, _ := json.Marshal(res.recipients)
		attB, _ := json.Marshal(res.attachments)
		var coB []byte
		if res.cutoff != nil {
			coB, _ = json.Marshal(res.cutoff)
		}
		if _, err := d.Pool.Exec(ctx, `UPDATE report_run SET status = $2, error = $3, recipients = $4, attachments = $5, cutoff = $6,
				next_attempt_at = $7, finished_at = CASE WHEN $2 IN ('retrying') THEN NULL ELSE now() END WHERE id = $1`,
			r.ID, status, errText, recB, attB, coB, next); err != nil {
			return err
		}
	}
	if len(finalErrs) > 0 {
		return fmt.Errorf("scheduled reports failed: %s", strings.Join(finalErrs, "; "))
	}
	return nil
}

func retryDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return 5 * time.Minute
	}
	return 15 * time.Minute
}

type runResult struct {
	recipients  []Recipient
	attachments []AttachmentMeta
	cutoff      *cutoff.Info
	err         error
}

func loadCandidates(ctx context.Context, d *deps.Deps, s Schedule) ([]Candidate, error) {
	rows, err := d.Pool.Query(ctx, `SELECT id, email, name, role::text, COALESCE("plantId",''), COALESCE(language,'') FROM "User"
		WHERE "isActive" AND (id = ANY($1) OR role::text = ANY($2))`, s.RecipientUserIDs, s.RecipientRoles)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.UserID, &c.Email, &c.Name, &c.Role, &c.PlantID, &c.Lang); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type rendered struct {
	files []mail.Attachment
	meta  []AttachmentMeta
}

func processRun(ctx context.Context, d *deps.Deps, r claimed, now time.Time) (res runResult) {
	def, ok := DefByKey(r.Key)
	if !ok {
		res.err = fmt.Errorf("unknown report %q", r.Key)
		return
	}
	s, err := LoadSchedule(ctx, d.Pool, r.Key)
	if err != nil {
		res.err = err
		return
	}
	recips := r.Recipients
	if len(recips) == 0 {
		cands, err := loadCandidates(ctx, d, s)
		if err != nil {
			res.err = err
			return
		}
		recips = ResolveRecipients(def, s, cands)
	}
	res.recipients = recips
	app := appFor(d)
	sender := SenderFor(d)
	if app == nil || sender == nil {
		res.err = errors.New("report renderer or mail transport not configured")
		return
	}
	// group pending recipients by render identity
	type group struct {
		identity, lang, plant string
		idx                   []int
	}
	groups := map[string]*group{}
	order := []string{}
	var superID string
	for i, x := range recips {
		if x.Status == "sent" || x.Status == "skipped" {
			continue
		}
		identity := x.UserID
		gk := ""
		if identity == "" {
			if superID == "" {
				_ = d.Pool.QueryRow(ctx, `SELECT id FROM "User" WHERE role = 'SUPER_ADMIN' AND "isActive" AND NOT "mustChangePassword" ORDER BY "createdAt" LIMIT 1`).Scan(&superID)
			}
			if superID == "" {
				recips[i].Status, recips[i].Reason = "failed", "no_render_identity"
				continue
			}
			identity, gk = superID, "system"
		} else {
			gk = "role:" + x.Role
		}
		gk += "|" + x.Lang + "|" + x.PlantID
		g := groups[gk]
		if g == nil {
			g = &group{identity: identity, lang: x.Lang, plant: x.PlantID}
			groups[gk] = g
			order = append(order, gk)
		}
		g.idx = append(g.idx, i)
	}
	title := func(lang string) string { return i18n.T(i18n.Lang(lang), "reports.schedule."+r.Key+".title") }
	for _, gk := range order {
		g := groups[gk]
		p := Period{From: r.From, To: r.To, PlantID: g.plant}
		out, err := renderGroup(ctx, d, app, def, s, p, g.identity, g.lang)
		res.attachments = append(res.attachments, out.meta...)
		if err != nil {
			for _, i := range g.idx {
				recips[i].Status, recips[i].Reason = "failed", truncate(err.Error(), 300)
			}
			continue
		}
		if len(out.files) == 0 {
			for _, i := range g.idx {
				recips[i].Status, recips[i].Reason = "skipped", "no_access"
			}
			continue
		}
		co, err := cutoff.Load(ctx, d.Pool, g.plant, i18n.Lang(g.lang))
		if err == nil && res.cutoff == nil {
			c := co
			res.cutoff = &c
		}
		site := i18n.T(i18n.Lang(g.lang), "reports.mgmt.all_plants")
		if g.plant != "" {
			var code, name string
			if d.Pool.QueryRow(ctx, `SELECT code, name FROM "Plant" WHERE id = $1`, g.plant).Scan(&code, &name) == nil {
				site = code + " — " + name
			}
		}
		for _, i := range g.idx {
			x := recips[i]
			msg := BuildEmail(d.Cfg.AppURL, r.Key, title(x.Lang), x, r.From, r.To, site, co, out)
			sctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			err := sender.SendWithAttachments(sctx, msg, out.files)
			cancel()
			if err != nil {
				recips[i].Status, recips[i].Reason = "failed", truncate(err.Error(), 300)
				slog.Warn("scheduled report e-mail failed", "report", r.Key, "err", err)
			} else {
				recips[i].Status, recips[i].Reason = "sent", ""
			}
		}
	}
	res.recipients = recips
	return
}

// BuildEmail is the localized message of one recipient (exported for tests).
func BuildEmail(appURL, key, title string, x Recipient, from, to time.Time, site string, co cutoff.Info, out rendered) mail.Message {
	lang := i18n.Lang(x.Lang)
	t := func(k string, a ...any) string { return i18n.T(lang, k, a...) }
	period := dates.FormatRange(from, to)
	names := make([]string, 0, len(out.files))
	for _, f := range out.files {
		names = append(names, f.Name)
	}
	paras := []string{t("reports.mail.intro", "title", title, "period", period, "site", site), co.Note, t("reports.mail.attached", "files", strings.Join(names, ", "))}
	for _, m := range out.meta {
		if m.Status == "too_large" {
			paras = append(paras, t("reports.mail.too_large"))
			break
		}
	}
	return mail.Message{To: x.Email, Name: x.Name, Lang: lang, Subject: t("reports.mail.subject", "title", title, "period", period),
		Title: title, Paragraphs: paras, ActionURL: strings.TrimRight(appURL, "/") + "/reports", ActionLabel: t("reports.mail.open"),
		FooterNote: t("reports.mail.footer")}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

var contentTypes = map[string]string{"pdf": "application/pdf", "xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "csv": "text/csv"}

// renderGroup renders every part × format as identity, in-process, then revokes the session.
func renderGroup(ctx context.Context, d *deps.Deps, app *fiber.App, def Def, s Schedule, p Period, identity, lang string) (rendered, error) {
	var out rendered
	tok, err := d.Auth.CreateSessionToken(ctx, identity, "127.0.0.1", "btn-ops/scheduled-report")
	if err != nil {
		return out, err
	}
	defer func() {
		_, _ = d.Pool.Exec(context.WithoutCancel(ctx), `UPDATE auth_session SET revoked_at = now(), revoke_reason = 'scheduled_report'
			WHERE id IN (SELECT session_id FROM auth_access_token WHERE token_hash = $1) AND revoked_at IS NULL`, auth.SHA256Hex(tok))
	}()
	plantLabel := ""
	if p.PlantID != "" {
		plantLabel = p.PlantID
	}
	total := 0
	for _, part := range def.Parts {
		for _, f := range s.Formats {
			var path string
			switch f {
			case "pdf":
				if part.PDF != nil {
					path = part.PDF(p) + "&download=1"
				}
			case "xlsx", "csv":
				if part.Export != nil {
					path = part.Export(p) + "&format=" + f
				}
			}
			if path == "" {
				continue
			}
			path += "&lang=" + lang
			body, name, status, err := fetch(ctx, app, path, tok)
			meta := AttachmentMeta{Part: part.Key, Format: f, Lang: lang, Plant: plantLabel}
			if err != nil {
				return out, err
			}
			switch {
			case status == 200:
				if name == "" {
					name = def.Key + "_" + part.Key + "_" + dates.ToISO(p.From) + "_" + dates.ToISO(p.To) + "." + f
				}
				meta.Name, meta.Bytes = name, len(body)
				if f != "pdf" && total+len(body) > MaxAttachmentBytes {
					meta.Status = "too_large"
				} else {
					meta.Status = "attached"
					total += len(body)
					out.files = append(out.files, mail.Attachment{Name: name, ContentType: contentTypes[f], Data: body})
				}
			case status == 401 || status == 403:
				meta.Status = "forbidden"
			case status == 404 || status == 501:
				meta.Status = "unavailable"
			default:
				return out, fmt.Errorf("%s %s: HTTP %d %s", part.Key, f, status, truncate(string(body), 200))
			}
			out.meta = append(out.meta, meta)
		}
	}
	return out, nil
}

func fetch(ctx context.Context, app *fiber.App, path, token string) (body []byte, filename string, status int, err error) {
	for attempt := 0; attempt < 5; attempt++ {
		req := httptest.NewRequest("GET", "/api"+path, nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: token})
		req.Header.Set("Accept", "*/*")
		req.Header.Set("User-Agent", "btn-ops/scheduled-report")
		resp, e := app.Test(req, 120_000)
		if e != nil {
			return nil, "", 0, e
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 429 && attempt < 4 {
			wait, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
			wait = min(max(wait, 1), 30)
			select {
			case <-ctx.Done():
				return nil, "", 0, ctx.Err()
			case <-time.After(time.Duration(wait) * time.Second):
			}
			continue
		}
		if _, params, e := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); e == nil {
			filename = params["filename"]
		}
		return b, filename, resp.StatusCode, nil
	}
	return nil, "", 429, nil
}
