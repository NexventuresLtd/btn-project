package reports

// Scheduled reports (SRS RPT-02, §10 table). Definitions, slot/period evaluation and recipient resolution are
// pure functions (unit-tested); schedule_run.go generates and e-mails them from the F1 job runner.

import (
	"net/mail"
	"net/url"
	"sort"
	"strings"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/i18n"
)

// Frequencies.
const (
	FreqDaily      = "daily"
	FreqWeekly     = "weekly"
	FreqMonthly    = "monthly"
	FreqWagePeriod = "wage_period"
)

// Formats of attachments.
var Formats = []string{"pdf", "xlsx", "csv"}

// Period a run covers (business dates, UTC midnight) and its site ("" = all).
type Period struct {
	From, To time.Time
	PlantID  string
}

func (p Period) q(extra ...string) string {
	v := url.Values{}
	v.Set("from", dates.ToISO(p.From))
	v.Set("to", dates.ToISO(p.To))
	if p.PlantID != "" {
		v.Set("plantId", p.PlantID)
	}
	for i := 0; i+1 < len(extra); i += 2 {
		v.Set(extra[i], extra[i+1])
	}
	return v.Encode()
}

func (p Period) asAt() string {
	v := url.Values{}
	v.Set("date", dates.ToISO(p.To))
	if p.PlantID != "" {
		v.Set("plantId", p.PlantID)
	}
	return v.Encode()
}

// Part is one attachment pair of a scheduled report: a PDF endpoint and an exportable JSON endpoint
// (attached as Excel/CSV through the export registry). Paths exclude /api.
type Part struct {
	Key    string
	PDF    func(Period) string // "" = no PDF for this part
	Export func(Period) string // "" = no tabular export
}

// Def is one row of the §10 scheduled-report table.
type Def struct {
	Key         string
	Perms       []auth.Permission // a user recipient must hold one of these
	Frequencies []string
	Parts       []Part
}

func hubPart(key string) Part {
	return Part{Key: key,
		PDF:    func(p Period) string { return "/reports/pdf/" + key + "?" + p.q() },
		Export: func(p Period) string { return "/reports/data/" + key + "?" + p.q() }}
}

// Defs is the §10 scheduled report list (order = admin screen order).
var Defs = []Def{
	{Key: "daily_production", Perms: []auth.Permission{"production.view"}, Frequencies: []string{FreqDaily, FreqWeekly},
		Parts: []Part{{Key: "production-summary",
			PDF:    func(p Period) string { return "/production/pdf/summary?" + p.q() },
			Export: func(p Period) string { return "/production/summary?" + p.q() }}}},
	{Key: "daily_stock", Perms: []auth.Permission{"stock.view"}, Frequencies: []string{FreqDaily, FreqWeekly},
		Parts: []Part{
			{Key: "stock-position", PDF: func(p Period) string { return "/stock/pdf/position?" + p.asAt() },
				Export: func(p Period) string { return "/stock/balances?" + p.asAt() }},
			{Key: "materials-position", PDF: func(p Period) string { return "/materials/pdf/position?" + p.asAt() },
				Export: func(p Period) string { return "/materials/balances?" + p.asAt() }}}},
	{Key: "daily_headcount", Perms: []auth.Permission{"attendance.view"}, Frequencies: []string{FreqDaily, FreqWeekly},
		Parts: []Part{hubPart("headcount")}},
	{Key: "orders_sales", Perms: []auth.Permission{"sales.view"}, Frequencies: []string{FreqWeekly, FreqMonthly},
		Parts: []Part{hubPart("orders-summary"), {Key: "sales-report", PDF: func(p Period) string { return "/sales/pdf/report?" + p.q() },
			Export: func(p Period) string { return "/sales?" + p.q() }}}},
	{Key: "stock_movement_variance", Perms: []auth.Permission{"stock.view"}, Frequencies: []string{FreqWeekly, FreqMonthly},
		Parts: []Part{hubPart("stock-variance")}},
	{Key: "procurement_expense", Perms: []auth.Permission{"procurement.view", "expenses.view"}, Frequencies: []string{FreqMonthly, FreqWeekly},
		Parts: []Part{hubPart("procurement-expense")}},
	{Key: "wage_workforce", Perms: []auth.Permission{"payroll.view"}, Frequencies: []string{FreqWagePeriod, FreqWeekly, FreqMonthly},
		Parts: []Part{{Key: "payroll-summary", PDF: func(p Period) string { return "/payroll/pdf/summary?" + p.q() },
			Export: func(p Period) string { return "/payroll/summary?" + p.q() }}, hubPart("headcount")}},
	{Key: "budget_monitoring", Perms: []auth.Permission{"budgets.view"}, Frequencies: []string{FreqMonthly},
		Parts: []Part{hubPart("budget-monitoring")}},
	{Key: "site_performance", Perms: []auth.Permission{"reports.view"}, Frequencies: []string{FreqMonthly},
		Parts: []Part{hubPart("site-performance"), {Key: "management-summary", PDF: func(p Period) string { return "/reports/pdf/management?" + p.q() },
			Export: func(p Period) string { return "/dashboard?" + p.q() }}}},
}

// DefByKey finds a definition.
func DefByKey(key string) (Def, bool) {
	for _, d := range Defs {
		if d.Key == key {
			return d, true
		}
	}
	return Def{}, false
}

func init() {
	for _, d := range Defs {
		for _, p := range d.Perms {
			if !auth.Known(p) {
				panic("reports: unknown permission " + string(p))
			}
		}
	}
}

// Schedule is the stored configuration of one scheduled report.
type Schedule struct {
	Key              string     `json:"key"`
	Enabled          bool       `json:"enabled"`
	Frequency        string     `json:"frequency"`
	SendTime         string     `json:"sendTime"`   // HH:MM Kigali
	DayOfWeek        int        `json:"dayOfWeek"`  // 1 = Monday … 7 = Sunday
	DayOfMonth       int        `json:"dayOfMonth"` // 1..28
	PlantID          *string    `json:"plantId"`
	Language         string     `json:"language"` // "" = each recipient's language
	Formats          []string   `json:"formats"`
	RecipientUserIDs []string   `json:"recipientUserIds"`
	RecipientRoles   []string   `json:"recipientRoles"`
	RecipientEmails  []string   `json:"recipientEmails"`
	LastSlotAt       *time.Time `json:"lastSlotAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

func (s Schedule) plant() string {
	if s.PlantID == nil {
		return ""
	}
	return *s.PlantID
}

func hm(s string) (int, int) {
	if len(s) != 5 || s[2] != ':' {
		return 6, 30
	}
	h := int(s[0]-'0')*10 + int(s[1]-'0')
	m := int(s[3]-'0')*10 + int(s[4]-'0')
	return h, m
}

func at(day time.Time, h, m int) time.Time {
	k := day.In(dates.Kigali())
	return time.Date(k.Year(), k.Month(), k.Day(), h, m, 0, 0, dates.Kigali()).UTC()
}

func bizDate(t time.Time) time.Time { return dates.MustParseISO(dates.TodayISO(t)) }

// LatestSlot is the most recent send time at or before now for daily/weekly/monthly schedules (zero for wage_period).
func LatestSlot(s Schedule, now time.Time) time.Time {
	h, m := hm(s.SendTime)
	k := now.In(dates.Kigali())
	day := time.Date(k.Year(), k.Month(), k.Day(), 0, 0, 0, 0, dates.Kigali())
	switch s.Frequency {
	case FreqDaily:
		slot := at(day, h, m)
		if slot.After(now) {
			slot = at(day.AddDate(0, 0, -1), h, m)
		}
		return slot
	case FreqWeekly:
		for i := 0; i <= 7; i++ {
			d := day.AddDate(0, 0, -i)
			wd := int(d.Weekday())
			if wd == 0 {
				wd = 7
			}
			if wd == s.DayOfWeek {
				if slot := at(d, h, m); !slot.After(now) {
					return slot
				}
			}
		}
	case FreqMonthly:
		dom := min(max(s.DayOfMonth, 1), 28)
		for i := 0; i <= 1; i++ {
			d := time.Date(k.Year(), k.Month()-time.Month(i), dom, 0, 0, 0, 0, dates.Kigali())
			if slot := at(d, h, m); !slot.After(now) {
				return slot
			}
		}
	}
	return time.Time{}
}

// NextSlot is the next send time strictly after now (zero for wage_period, which follows payroll periods).
func NextSlot(s Schedule, now time.Time) time.Time {
	switch s.Frequency {
	case FreqDaily:
		return LatestSlot(s, now.Add(24*time.Hour))
	case FreqWeekly:
		return LatestSlot(s, now.Add(7*24*time.Hour))
	case FreqMonthly:
		h, m := hm(s.SendTime)
		k := now.In(dates.Kigali())
		dom := min(max(s.DayOfMonth, 1), 28)
		for i := 0; i <= 1; i++ {
			if slot := at(time.Date(k.Year(), k.Month()+time.Month(i), dom, 0, 0, 0, 0, dates.Kigali()), h, m); slot.After(now) {
				return slot
			}
		}
	}
	return time.Time{}
}

// PeriodFor is the business period a slot reports on:
// daily — the previous day when sent before noon, else the same day; weekly — the 7 days before the send date;
// monthly — the previous calendar month.
func PeriodFor(s Schedule, slot time.Time) (from, to time.Time) {
	d := bizDate(slot)
	switch s.Frequency {
	case FreqDaily:
		if h, _ := hm(s.SendTime); h < 12 {
			d = d.AddDate(0, 0, -1)
		}
		return d, d
	case FreqWeekly:
		return d.AddDate(0, 0, -7), d.AddDate(0, 0, -1)
	case FreqMonthly:
		first := dates.StartOfMonth(d)
		pf := first.AddDate(0, -1, 0)
		return pf, first.AddDate(0, 0, -1)
	}
	return d, d
}

// GraceWindow: a missed slot (server down) is still sent if it is at most this old; older ones are skipped.
const GraceWindow = 36 * time.Hour

// DueSlot reports whether a daily/weekly/monthly schedule has a slot to send at now.
func DueSlot(s Schedule, now time.Time) (time.Time, bool) {
	if !s.Enabled || s.Frequency == FreqWagePeriod {
		return time.Time{}, false
	}
	slot := LatestSlot(s, now)
	if slot.IsZero() || now.Sub(slot) > GraceWindow {
		return time.Time{}, false
	}
	if s.LastSlotAt != nil && !slot.After(*s.LastSlotAt) {
		return time.Time{}, false
	}
	return slot, true
}

// WagePeriod is a payroll period's dates.
type WagePeriod struct{ Start, End time.Time }

// WageSlots returns the (slot, from, to) to send for a wage_period schedule: the day after each payroll period
// ended, at the send time, within the grace window and after the last sent slot. Periods ending on the same
// day are merged.
func WageSlots(s Schedule, periods []WagePeriod, now time.Time) []struct{ Slot, From, To time.Time } {
	if !s.Enabled || s.Frequency != FreqWagePeriod {
		return nil
	}
	h, m := hm(s.SendTime)
	byEnd := map[time.Time]time.Time{}
	for _, p := range periods {
		if st, ok := byEnd[p.End]; !ok || p.Start.Before(st) {
			byEnd[p.End] = p.Start
		}
	}
	var out []struct{ Slot, From, To time.Time }
	for end, start := range byEnd {
		slot := at(end.AddDate(0, 0, 1), h, m)
		if slot.After(now) || now.Sub(slot) > 7*24*time.Hour {
			continue
		}
		if s.LastSlotAt != nil && !slot.After(*s.LastSlotAt) {
			continue
		}
		out = append(out, struct{ Slot, From, To time.Time }{slot, start, end})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slot.Before(out[j].Slot) })
	return out
}

// ── recipients ──────────────────────────────────────────────────────────────

// Candidate is a user considered as a recipient.
type Candidate struct {
	UserID, Email, Name, Role, PlantID, Lang string
}

// Recipient is a resolved recipient: render identity group + delivery status.
type Recipient struct {
	Email   string `json:"email"`
	Name    string `json:"name"`
	UserID  string `json:"userId,omitempty"`
	Role    string `json:"role,omitempty"`
	Lang    string `json:"lang"`
	PlantID string `json:"plantId,omitempty"` // site the report is filtered to for this recipient
	Status  string `json:"status"`            // pending | sent | failed | skipped
	Reason  string `json:"reason,omitempty"`
}

// ValidEmail checks an address.
func ValidEmail(s string) bool {
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s && strings.Contains(s, "@")
}

// ResolveRecipients applies the RPT-02 rules: users chosen by id or by role (active ones, loaded by the caller),
// plus raw e-mail addresses; de-duplicated by address; a user must hold the report's permission; site-pinned
// users receive their own site only (and are skipped when the schedule is fixed to another site); language =
// the schedule's, else the user's, else English.
func ResolveRecipients(def Def, s Schedule, users []Candidate) []Recipient {
	seen := map[string]bool{}
	out := []Recipient{}
	lang := func(l string) string {
		if s.Language != "" {
			return s.Language
		}
		if i18n.Valid(l) {
			return l
		}
		return string(i18n.Default)
	}
	sort.SliceStable(users, func(i, j int) bool { return strings.ToLower(users[i].Email) < strings.ToLower(users[j].Email) })
	for _, u := range users {
		key := strings.ToLower(strings.TrimSpace(u.Email))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		r := Recipient{Email: u.Email, Name: u.Name, UserID: u.UserID, Role: u.Role, Lang: lang(u.Lang), PlantID: s.plant(), Status: "pending"}
		role := auth.Role(u.Role)
		allowed := false
		for _, p := range def.Perms {
			if auth.Can(role, p) {
				allowed = true
				break
			}
		}
		switch {
		case !allowed:
			r.Status, r.Reason = "skipped", "no_permission"
		case auth.UserPlantScoped(role, u.PlantID):
			if s.plant() != "" && s.plant() != u.PlantID {
				r.Status, r.Reason = "skipped", "other_site"
			}
			r.PlantID = u.PlantID
		}
		out = append(out, r)
	}
	for _, e := range s.RecipientEmails {
		e = strings.TrimSpace(e)
		key := strings.ToLower(e)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		r := Recipient{Email: e, Lang: lang(""), PlantID: s.plant(), Status: "pending"}
		if !ValidEmail(e) {
			r.Status, r.Reason = "skipped", "invalid_email"
		}
		out = append(out, r)
	}
	return out
}
