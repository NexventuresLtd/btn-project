// Package monitor is error tracking, health and alerting (SRS ADM-04, NFR-M2):
//
//   - Capture (app middleware): panics and 5xx responses are stored in error_event, deduplicated by a
//     fingerprint (kind + route + normalized message + top application frames) with an occurrence count,
//     plus one error_occurrence row each (24 h error rate, timeline).
//   - Alerts: new / recurring critical errors, final job failures and failed health checks notify users
//     holding admin.monitor (in-app, critical) and OPS_ALERT_EMAIL, rate limited per key and globally.
//   - Health summary for admins (DB, pool, SMTP, jobs, error rate, email outbox, disk, version, uptime).
//   - Built-in jobs: retention cleanup, health self-check, daily health digest.
package monitor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/jobs"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Started is the process start time (uptime).
var Started = time.Now()

// ── request counters (this instance, 24 hourly buckets) ─────────────────────────────────────────────

type bucket struct {
	hour          int64
	requests, err int64
}

var (
	cntMu   sync.Mutex
	buckets [24]bucket
)

func count(status int) {
	h := time.Now().Unix() / 3600
	cntMu.Lock()
	b := &buckets[h%24]
	if b.hour != h {
		*b = bucket{hour: h}
	}
	b.requests++
	if status >= 500 {
		b.err++
	}
	cntMu.Unlock()
}

// RequestStats returns requests and 5xx responses served by this instance in the last 24 hours.
func RequestStats() (requests, serverErrors int64) {
	h := time.Now().Unix() / 3600
	cntMu.Lock()
	defer cntMu.Unlock()
	for _, b := range buckets {
		if b.hour > h-24 {
			requests += b.requests
			serverErrors += b.err
		}
	}
	return
}

// ── middleware ──────────────────────────────────────────────────────────────────────────────────────

// Capture returns the error-capturing middleware. Mount it right after the recover middleware: it renders
// errors itself (with the app's ErrorHandler) so it knows the final status, records panics and 5xx
// (except 501 "not implemented" stubs) and counts requests.
func Capture(d *deps.Deps) fiber.Handler {
	return func(c *fiber.Ctx) (err error) {
		defer func() {
			if p := recover(); p != nil {
				// handlers may panic with an *httpx.Error (auth.MustUser): render it like a returned error
				perr, ok := p.(error)
				if !ok {
					perr = fmt.Errorf("%v", p)
				}
				err = nil
				if herr := c.App().ErrorHandler(c, perr); herr != nil {
					err = herr
				}
				status := c.Response().StatusCode()
				count(status)
				if status >= 500 {
					stack := jobs.TrimStack(string(debug.Stack()))
					slog.Error("panic recovered", "panic", p, "path", c.Path(), "rid", c.GetRespHeader(fiber.HeaderXRequestID), "stack", stack)
					capture(d, c, "panic", status, fmt.Sprint(p), stack)
				}
			}
		}()
		nerr := c.Next()
		if nerr != nil {
			if herr := c.App().ErrorHandler(c, nerr); herr != nil {
				return herr
			}
		}
		status := c.Response().StatusCode()
		count(status)
		if status >= 500 && status != fiber.StatusNotImplemented {
			msg := fmt.Sprintf("HTTP %d", status)
			if nerr != nil {
				msg = nerr.Error()
				var he *httpx.Error
				if errors.As(nerr, &he) && he.Key != "" && he.Message == "" {
					msg = he.Key
				}
			}
			capture(d, c, "http", status, msg, "")
		}
		return nil
	}
}

func capture(d *deps.Deps, c *fiber.Ctx, kind string, status int, msg, stack string) {
	route := c.Path()
	if r := c.Route(); r != nil && r.Path != "" && r.Path != "/" && !strings.HasSuffix(r.Path, "*") {
		route = r.Path
	}
	ev := Event{
		Kind: kind, Method: c.Method(), Route: route, Path: c.Path(), Status: status, Message: msg, Stack: stack,
		RequestID: c.GetRespHeader(fiber.HeaderXRequestID),
	}
	if u := auth.FromCtx(c); u != nil {
		ev.UserID = u.ID
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if _, err := RecordAndAlert(ctx, d, ev); err != nil {
		slog.Error("monitor: could not record error", "err", err)
	}
}

// ── store ───────────────────────────────────────────────────────────────────────────────────────────

// Event is one captured error.
type Event struct {
	Kind      string // panic | http | job
	Method    string
	Route     string // route pattern ("/api/sales/:id") or job name
	Path      string
	Status    int
	Message   string
	Stack     string
	RequestID string
	UserID    string
}

var (
	reUUID   = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	reCUID   = regexp.MustCompile(`\bc[0-9a-z]{24}\b`)
	reHex    = regexp.MustCompile(`\b0x[0-9a-fA-F]+\b`)
	reQuoted = regexp.MustCompile(`"[^"]*"|'[^']*'`)
	reNum    = regexp.MustCompile(`\d+(\.\d+)?`)
	reFrame  = regexp.MustCompile(`^(btn-ops/[^\s(]+)\(`)
)

// Normalize strips volatile parts (ids, numbers, quoted values) from an error message so the same bug
// always produces the same fingerprint.
func Normalize(msg string) string {
	s := reUUID.ReplaceAllString(msg, "<uuid>")
	s = reCUID.ReplaceAllString(s, "<id>")
	s = reHex.ReplaceAllString(s, "<hex>")
	s = reQuoted.ReplaceAllString(s, "<s>")
	s = reNum.ReplaceAllString(s, "<n>")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// stackSignature is the first few application frames (function names only, no line numbers or args).
func stackSignature(stack string) string {
	var out []string
	for _, l := range strings.Split(stack, "\n") {
		l = strings.TrimSpace(l)
		if m := reFrame.FindStringSubmatch(l); m != nil {
			if strings.Contains(m[1], "internal/monitor.") || strings.Contains(m[1], "internal/jobs.safeRun") {
				continue
			}
			out = append(out, m[1])
			if len(out) == 5 {
				break
			}
		}
	}
	return strings.Join(out, ">")
}

// Fingerprint identifies "the same error" across occurrences.
// A panic with application frames is identified by where it happened (its message often embeds data).
func Fingerprint(e Event) string {
	sig := stackSignature(e.Stack)
	msg := Normalize(e.Message)
	if e.Kind == "panic" && sig != "" {
		msg = ""
	}
	h := sha256.Sum256([]byte(strings.Join([]string{e.Kind, e.Method, e.Route, fmt.Sprint(e.Status / 100), msg, sig}, "|")))
	return hex.EncodeToString(h[:16])
}

// Recorded is what Record stored.
type Recorded struct {
	ID       string
	New      bool // first occurrence of this fingerprint
	Reopened bool // it had been resolved
	Muted    bool
	Count    int
}

// Record upserts the error by fingerprint and adds an occurrence.
func Record(ctx context.Context, pool *pgxpool.Pool, e Event) (Recorded, error) {
	var r Recorded
	if e.Message == "" {
		e.Message = "unknown error"
	}
	fp := Fingerprint(e)
	now := time.Now().UTC().Truncate(time.Millisecond)
	var wasResolved *bool
	_ = pool.QueryRow(ctx, `SELECT resolved_at IS NOT NULL FROM error_event WHERE fingerprint=$1`, fp).Scan(&wasResolved)
	var stack any
	if e.Stack != "" {
		stack = truncate(e.Stack, 8000)
	}
	var status any
	if e.Status != 0 {
		status = e.Status
	}
	var mutedUntil *time.Time
	err := pool.QueryRow(ctx, `INSERT INTO error_event (fingerprint, kind, method, route, status, message, stack, last_path, last_request_id, last_user_id, first_seen_at, last_seen_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)
		ON CONFLICT (fingerprint) DO UPDATE SET count = error_event.count + 1, last_seen_at = EXCLUDED.last_seen_at,
			message = EXCLUDED.message, stack = COALESCE(EXCLUDED.stack, error_event.stack), status = EXCLUDED.status,
			last_path = EXCLUDED.last_path, last_request_id = EXCLUDED.last_request_id, last_user_id = EXCLUDED.last_user_id,
			resolved_at = NULL, resolved_by = NULL
		RETURNING id, count, muted_until`,
		fp, e.Kind, nilIfEmpty(e.Method), nilIfEmpty(e.Route), status, truncate(e.Message, 2000), stack,
		nilIfEmpty(truncate(e.Path, 500)), nilIfEmpty(e.RequestID), nilIfEmpty(e.UserID), now).Scan(&r.ID, &r.Count, &mutedUntil)
	if err != nil {
		return r, err
	}
	r.New = wasResolved == nil
	r.Reopened = wasResolved != nil && *wasResolved
	r.Muted = mutedUntil != nil && mutedUntil.After(now)
	_, err = pool.Exec(ctx, `INSERT INTO error_occurrence (event_id, request_id, path, user_id, at) VALUES ($1,$2,$3,$4,$5)`,
		r.ID, nilIfEmpty(e.RequestID), nilIfEmpty(truncate(e.Path, 500)), nilIfEmpty(e.UserID), now)
	return r, err
}

// RecordAndAlert records e and raises a (rate-limited) critical alert unless the error is muted.
func RecordAndAlert(ctx context.Context, d *deps.Deps, e Event) (Recorded, error) {
	r, err := Record(ctx, d.Pool, e)
	if err != nil || r.Muted {
		return r, err
	}
	msg := truncate(e.Message, 200)
	where := e.Route
	if e.Method != "" {
		where = e.Method + " " + e.Route
	}
	_, aerr := Alert(ctx, d, AlertSpec{
		Event: EvCriticalError, Key: "error:" + Fingerprint(e), Window: time.Hour,
		TitleKey: "notify.critical_error.title", BodyKey: "notify.critical_error.body",
		Params: map[string]any{"where": where, "message": msg, "count": r.Count, "status": e.Status},
		Link:   "/admin/monitoring?error=" + r.ID,
	})
	return r, aerr
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
