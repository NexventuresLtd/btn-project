// Command loadtest: DEVELOPMENT/STAGING ONLY performance harness for NFR-P1 and NFR-P2
// (refuses to run when APP_ENV=production; it creates sessions directly in the database like `devtools session`).
//
//	go run ./cmd/loadtest run     -base http://127.0.0.1:4800 -users 50 -duration 3m   NFR-P1: 95% of reads < 2 s
//	go run ./cmd/loadtest reports -base http://127.0.0.1:4800                          NFR-P2: dashboards + reports < 5 s
//
// DATABASE_URL must point at the same database as the API under test. `run` signs in -users distinct
// active users (round-robin over every active account), and each virtual user browses a realistic page mix:
// every "page" fires the same concurrent reads the web app does (lists with filters + pagination, detail
// pages, lookups, dashboards, report JSON), then thinks for -think before the next page. Endpoints a user
// may not read (403) are dropped from that user's mix after the first probe.
// The global per-IP rate limit must be raised on the API (RATE_LIMIT_GLOBAL_PER_MIN) because every
// virtual user shares one IP here, unlike production (also RATE_LIMIT_AUTH_PER_MIN for /api/auth/me).
// Every role account is signed in first (ORDER BY puts bulk "perf-" accounts last), so each role's mix runs.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/config"
	"btn-ops/internal/db"
)

func main() {
	if len(os.Args) < 2 || (os.Args[1] != "run" && os.Args[1] != "reports") {
		fmt.Fprintln(os.Stderr, "usage: loadtest run|reports [flags]")
		os.Exit(2)
	}
	mode := os.Args[1]
	fs := flag.NewFlagSet(mode, flag.ExitOnError)
	base := fs.String("base", "http://127.0.0.1:4800", "API origin under test")
	users := fs.Int("users", 50, "concurrent virtual users (run)")
	duration := fs.Duration("duration", 3*time.Minute, "test duration (run)")
	think := fs.Duration("think", 750*time.Millisecond, "mean think time between page views (run)")
	budget := fs.Duration("budget", 0, "latency budget (default 2s for run, 5s for reports)")
	as := fs.String("as", "super@btn.test", "account used for the reports catalog (reports)")
	months := fs.Int("months", 12, "report period in months (reports)")
	_ = fs.Parse(os.Args[2:])

	cfg, err := config.Load()
	if err != nil {
		fail(err)
	}
	if cfg.IsProd() {
		fail(fmt.Errorf("loadtest must never run in production"))
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DatabaseURL, 4)
	if err != nil {
		fail(err)
	}
	defer pool.Close()
	svc := &auth.Service{Pool: pool}
	session := func(email string) string {
		var id string
		if err := pool.QueryRow(ctx, `SELECT id FROM "User" WHERE email=$1`, email).Scan(&id); err != nil {
			fail(fmt.Errorf("user %s: %w", email, err))
		}
		tok, err := svc.CreateSessionToken(ctx, id, "127.0.0.1", "loadtest")
		if err != nil {
			fail(err)
		}
		return tok
	}

	cl := &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 256, MaxConnsPerHost: 256}}
	ok := true
	switch mode {
	case "run":
		if *budget == 0 {
			*budget = 2 * time.Second
		}
		rows, err := pool.Query(ctx, `SELECT email FROM "User" WHERE "isActive" AND NOT "mustChangePassword" ORDER BY (email LIKE 'perf-%'), email`)
		if err != nil {
			fail(err)
		}
		var emails []string
		for rows.Next() {
			var e string
			_ = rows.Scan(&e)
			emails = append(emails, e)
		}
		rows.Close()
		if len(emails) == 0 {
			fail(fmt.Errorf("no active users"))
		}
		toks := make([]string, *users)
		for i := range toks {
			toks[i] = session(emails[i%len(emails)])
		}
		ok = runLoad(cl, *base, toks, *duration, *think, *budget)
	case "reports":
		if *budget == 0 {
			*budget = 5 * time.Second
		}
		ok = runReports(cl, *base, session, *as, *months, *budget)
	}
	if !ok {
		os.Exit(1)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "loadtest:", err)
	os.Exit(1)
}

// ---------- HTTP ----------

type client struct {
	cl   *http.Client
	base string
	tok  string
}

func (c *client) get(path string) (int, []byte, time.Duration, error) {
	req, err := http.NewRequest("GET", c.base+"/api"+path, nil)
	if err != nil {
		return 0, nil, 0, err
	}
	req.Header.Set("X-Requested-With", "btn-ops")
	req.AddCookie(&http.Cookie{Name: "btn_session", Value: c.tok})
	t0 := time.Now()
	resp, err := c.cl.Do(req)
	if err != nil {
		return 0, nil, time.Since(t0), err
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, body, time.Since(t0), err
}

func (c *client) getJSON(path string, out any) error {
	code, body, _, err := c.get(path)
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("GET %s: %d %s", path, code, trunc(body))
	}
	return json.Unmarshal(body, out)
}

func trunc(b []byte) string {
	if len(b) > 200 {
		b = b[:200]
	}
	return string(b)
}

// ---------- stats ----------

type stat struct {
	mu   sync.Mutex
	durs []time.Duration
	errs int
	code map[int]int
}

type stats struct {
	mu sync.Mutex
	m  map[string]*stat
}

func (s *stats) add(name string, d time.Duration, code int, err error) {
	s.mu.Lock()
	st := s.m[name]
	if st == nil {
		st = &stat{code: map[int]int{}}
		s.m[name] = st
	}
	s.mu.Unlock()
	st.mu.Lock()
	st.durs = append(st.durs, d)
	st.code[code]++
	if err != nil || code >= 500 || code == 0 || code == 429 {
		st.errs++
	}
	st.mu.Unlock()
}

func pct(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(float64(len(sorted)-1) * p)
	return sorted[i]
}

func ms(d time.Duration) string { return fmt.Sprintf("%.0f", float64(d)/float64(time.Millisecond)) }

// ---------- NFR-P1 load ----------

type ids struct {
	sites                                                                     []string
	report, sale, customer, period, transfer, request, expense, entry, worker string
}

// page = what one screen of the web app requests concurrently on open.
type page struct {
	name string
	reqs []string
}

func pages(x ids, site string) []page {
	today := time.Now().In(kigali()).Format("2006-01-02")
	from30 := time.Now().In(kigali()).AddDate(0, 0, -29).Format("2006-01-02")
	from90 := time.Now().In(kigali()).AddDate(0, 0, -89).Format("2006-01-02")
	r30 := "from=" + from30 + "&to=" + today
	r90 := "from=" + from90 + "&to=" + today
	ps := []page{
		{"dashboard", []string{"/auth/me", "/dashboard/boards", "/dashboard/boards/{board}", "/notifications?page=1&pageSize=10"}},
		{"production list", []string{"/lookups", "/production/reports?" + r30 + "&plantId=" + site + "&page=1&pageSize=25", "/production/days?" + r30 + "&plantId=" + site}},
		{"production list (status)", []string{"/production/reports?" + r90 + "&status=APPROVED&page=2&pageSize=25"}},
		{"production detail", []string{"/production/reports/" + x.report}},
		{"production summary", []string{"/production/summary?" + r30, "/production/summary/periods?" + r90}},
		{"attendance daily", []string{"/attendance?plantId=" + site + "&date=" + today, "/attendance/days?" + r30 + "&plantId=" + site}},
		{"attendance summary", []string{"/attendance/summary?" + r30 + "&plantId=" + site}},
		{"stock position", []string{"/stock/balances?date=" + today, "/stock/low-stock"}},
		{"stock movements", []string{"/stock/movements?" + r30 + "&plantId=" + site + "&page=1&pageSize=25"}},
		{"stock daily", []string{"/stock/daily?plantId=" + site + "&" + r30}},
		{"stock history", []string{"/stock/history?" + r30 + "&plantId=" + site + "&page=1&pageSize=25"}},
		{"materials", []string{"/materials/balances", "/materials/movements?" + r30 + "&plantId=" + site + "&page=1&pageSize=25"}},
		{"transfers", []string{"/transfers?page=1&pageSize=25", "/transfers/" + x.transfer}},
		{"sales list", []string{"/sales?" + r30 + "&page=1&pageSize=25", "/sales/meta"}},
		{"sales detail", []string{"/sales/" + x.sale, "/sales/" + x.sale + "/availability"}},
		{"customers", []string{"/sales/customers?page=1&pageSize=25", "/sales/customers/" + x.customer + "/profile"}},
		{"receivables", []string{"/sales/receivables"}},
		{"petty cash", []string{"/pettycash/entries?" + r30 + "&page=1&pageSize=25", "/pettycash/summary?" + r30}},
		{"expenses", []string{"/expenses?" + r30 + "&page=1&pageSize=25", "/expenses/summary?" + r30}},
		{"procurement", []string{"/procurement/requests?page=1&pageSize=25", "/procurement/requests/" + x.request, "/procurement/summary"}},
		{"payroll", []string{"/payroll/periods?page=1&pageSize=25", "/payroll/periods/" + x.period}},
		{"workers", []string{"/payroll/workers?plantId=" + site + "&page=1&pageSize=25"}},
		{"admin users", []string{"/users?page=1&pageSize=25"}},
		{"audit trail", []string{"/audit?page=1&pageSize=50", "/audit?entity=Sale&page=3&pageSize=50"}},
		{"sync issues", []string{"/sync/conflicts?page=1&pageSize=25", "/sync/monitor"}},
		{"reports json", []string{"/production/reports?" + r90 + "&page=1&pageSize=100", "/stock/movements?" + r90 + "&page=1&pageSize=100"}},
	}
	return ps
}

func kigali() *time.Location {
	if l, err := time.LoadLocation("Africa/Kigali"); err == nil {
		return l
	}
	return time.FixedZone("CAT", 2*3600)
}

func discover(c *client) ids {
	var x ids
	var lk struct {
		Plants []struct{ ID, Kind string } `json:"plants"`
	}
	_ = c.getJSON("/lookups", &lk)
	for _, p := range lk.Plants {
		if p.Kind == "SITE" || p.Kind == "" {
			x.sites = append(x.sites, p.ID)
		}
	}
	first := func(path string) string {
		var pg struct {
			Items []struct{ ID string } `json:"items"`
		}
		if err := c.getJSON(path, &pg); err != nil || len(pg.Items) == 0 {
			return "none"
		}
		return pg.Items[0].ID
	}
	x.report = first("/production/reports?page=1&pageSize=1")
	x.sale = first("/sales?page=1&pageSize=1")
	x.customer = first("/sales/customers?page=1&pageSize=1")
	x.period = first("/payroll/periods?page=1&pageSize=1")
	x.transfer = first("/transfers?page=1&pageSize=1")
	x.request = first("/procurement/requests?page=1&pageSize=1")
	return x
}

type vu struct {
	c     *client
	site  string
	board string
	pages []page
}

func runLoad(cl *http.Client, base string, toks []string, dur, think, budget time.Duration) bool {
	admin := &client{cl, base, toks[0]}
	x := discover(admin)
	if len(x.sites) == 0 {
		fail(fmt.Errorf("no sites visible"))
	}
	st := &stats{m: map[string]*stat{}}

	// setup: per user, learn the default board + scope, and drop pages the role cannot read.
	vus := make([]*vu, len(toks))
	var wg sync.WaitGroup
	for i, tok := range toks {
		wg.Add(1)
		go func(i int, tok string) {
			defer wg.Done()
			c := &client{cl, base, tok}
			v := &vu{c: c, site: x.sites[i%len(x.sites)]}
			var me struct {
				User struct {
					Plant *struct{ ID string } `json:"plant"`
				} `json:"user"`
			}
			_ = c.getJSON("/auth/me", &me)
			if me.User.Plant != nil {
				v.site = me.User.Plant.ID
			}
			var b struct{ Default string }
			_ = c.getJSON("/dashboard/boards", &b)
			v.board = b.Default
			if v.board == "" {
				v.board = "executive"
			}
			for _, p := range pages(x, v.site) {
				allowed := true
				for _, r := range p.reqs {
					code, _, _, _ := c.get(strings.ReplaceAll(r, "{board}", v.board))
					if code == 403 || code == 404 {
						allowed = false
						break
					}
				}
				if allowed {
					v.pages = append(v.pages, p)
				}
			}
			vus[i] = v
		}(i, tok)
	}
	wg.Wait()

	fmt.Printf("NFR-P1 load: %d virtual users, %s, mean think %s, base %s\n", len(vus), dur, think, base)
	deadline := time.Now().Add(dur)
	var total atomic.Int64
	start := time.Now()
	for _, v := range vus {
		wg.Add(1)
		go func(v *vu) {
			defer wg.Done()
			r := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
			time.Sleep(time.Duration(r.Int64N(int64(2 * time.Second)))) // ramp-up
			for time.Now().Before(deadline) {
				if len(v.pages) == 0 {
					return
				}
				p := v.pages[r.IntN(len(v.pages))]
				var pw sync.WaitGroup
				for _, path := range p.reqs {
					pw.Add(1)
					go func(path string) {
						defer pw.Done()
						name := endpointName(path)
						code, _, d, err := v.c.get(strings.ReplaceAll(path, "{board}", v.board))
						st.add(name, d, code, err)
						total.Add(1)
					}(path)
				}
				pw.Wait()
				time.Sleep(time.Duration(float64(think) * (0.5 + r.Float64())))
			}
		}(v)
	}
	wg.Wait()
	elapsed := time.Since(start)
	return printStats(st, total.Load(), elapsed, budget)
}

// endpointName collapses ids and query strings so endpoints aggregate ("GET /sales/:id").
func endpointName(path string) string {
	p, q, _ := strings.Cut(path, "?")
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if len(s) >= 20 || s == "{board}" || s == "none" {
			segs[i] = ":id"
		}
	}
	name := strings.Join(segs, "/")
	if q != "" {
		vals, _ := url.ParseQuery(q)
		var keys []string
		for k := range vals {
			if k != "page" && k != "pageSize" && k != "from" && k != "to" && k != "plantId" && k != "date" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		if len(keys) > 0 {
			name += "?" + strings.Join(keys, ",")
		}
	}
	return name
}

func printStats(st *stats, total int64, elapsed, budget time.Duration) bool {
	var all []time.Duration
	var errs int
	type row struct {
		name          string
		n, errs       int
		p50, p95, p99 time.Duration
		codes         string
	}
	var rows []row
	for name, s := range st.m {
		d := slices.Clone(s.durs)
		slices.Sort(d)
		all = append(all, d...)
		errs += s.errs
		var cs []string
		for c, n := range s.code {
			cs = append(cs, fmt.Sprintf("%d×%d", c, n))
		}
		sort.Strings(cs)
		rows = append(rows, row{name, len(d), s.errs, pct(d, .5), pct(d, .95), pct(d, .99), strings.Join(cs, " ")})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].p95 > rows[j].p95 })
	slices.Sort(all)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "endpoint\tn\tp50 ms\tp95 ms\tp99 ms\terrors\tstatus\t")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%d\t%s\t\n", r.name, r.n, ms(r.p50), ms(r.p95), ms(r.p99), r.errs, r.codes)
	}
	fmt.Fprintf(tw, "OVERALL\t%d\t%s\t%s\t%s\t%d\t%.1f req/s\t\n", len(all), ms(pct(all, .5)), ms(pct(all, .95)), ms(pct(all, .99)), errs, float64(total)/elapsed.Seconds())
	tw.Flush()
	under := 0
	for _, d := range all {
		if d < budget {
			under++
		}
	}
	share := 100 * float64(under) / float64(max(1, len(all)))
	pass := share >= 95 && errs == 0
	fmt.Printf("%.2f%% of %d reads under %s (requirement: >= 95%%), %d server errors -> %s\n", share, len(all), budget, errs, verdict(pass))
	return pass
}

func verdict(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}

// ---------- NFR-P2 dashboards + reports ----------

var maxDays = regexp.MustCompile(`(?i)at most (\d+) days`)

type catalog struct {
	Groups []struct {
		Reports []struct {
			Key, Path, ExportPath string
			Params                []string
			Formats               []string
		}
	}
}

func runReports(cl *http.Client, base string, session func(string) string, as string, months int, budget time.Duration) bool {
	now := time.Now().In(kigali())
	to := now.Format("2006-01-02")
	from := now.AddDate(0, -months, 1).Format("2006-01-02")
	type res struct {
		name string
		code int
		d    time.Duration
		size int
	}
	var out []res
	measure := func(c *client, name, path string) {
		get := func() (int, []byte, time.Duration, error) {
			code, body, d, err := c.get(path)
			for tries := 0; code == 429 && tries < 10; tries++ { // per-user PDF/export limits: wait, then time the real render
				time.Sleep(5 * time.Second)
				code, body, d, err = c.get(path)
			}
			return code, body, d, err
		}
		code, body, d, err := get()
		// Some reports cap their period by design ("Choose a range of at most 93 days"): time the longest allowed one.
		if m := maxDays.FindSubmatch(body); code == 400 && m != nil && strings.Contains(path, "from="+from) {
			n, _ := strconv.Atoi(string(m[1]))
			capped := now.AddDate(0, 0, 1-n).Format("2006-01-02")
			path = strings.Replace(path, "from="+from, "from="+capped, 1)
			name += fmt.Sprintf(" [range capped at %d days by design]", n)
			code, body, d, err = get()
		}
		if err != nil {
			code = 0
		}
		out = append(out, res{name, code, d, len(body)})
	}

	// every role board, as a user of that role (cold: first request of a fresh session)
	roleUsers := []struct{ board, email string }{
		{"executive", "md@btn.test"}, {"production", "pm@btn.test"}, {"finance", "finance@btn.test"},
		{"procurement", "procurement@btn.test"}, {"supervisor", "sup1@btn.test"}, {"admin", "sysadmin@btn.test"},
	}
	for _, ru := range roleUsers {
		c := &client{cl, base, session(ru.email)}
		measure(c, "board "+ru.board+" ("+ru.email+", 12 months)", "/dashboard/boards/"+ru.board+"?from="+from+"&to="+to)
		measure(c, "board "+ru.board+" ("+ru.email+", default range)", "/dashboard/boards/"+ru.board)
	}
	c := &client{cl, base, session(as)}
	measure(c, "dashboard (classic, 12 months)", "/dashboard?from="+from+"&to="+to)

	var cat catalog
	if err := c.getJSON("/reports/catalog", &cat); err != nil {
		fail(err)
	}
	x := discover(c)
	for _, g := range cat.Groups {
		for _, r := range g.Reports {
			q := url.Values{}
			for _, p := range r.Params {
				switch p {
				case "range":
					q.Set("from", from)
					q.Set("to", to)
				case "date":
					q.Set("date", to)
				case "plantRequired":
					if len(x.sites) > 0 {
						q.Set("plantId", x.sites[0])
					}
				case "month":
					q.Set("month", now.Format("2006-01"))
				case "period":
					q.Set("periodId", x.period)
				}
			}
			join := func(path string, extra url.Values) string {
				vals := url.Values{}
				for k, v := range q {
					vals[k] = v
				}
				for k, v := range extra {
					vals[k] = v
				}
				sep := "?"
				if strings.Contains(path, "?") {
					sep = "&"
				}
				return path + sep + vals.Encode()
			}
			if r.Path != "" && slices.Contains(r.Formats, "pdf") {
				measure(c, r.Key+" PDF", join(r.Path, url.Values{"download": {"1"}}))
			}
			if r.ExportPath != "" {
				if slices.Contains(r.Formats, "json") {
					measure(c, r.Key+" JSON", join(r.ExportPath, nil))
				}
				if slices.Contains(r.Formats, "xlsx") {
					b, qs, _ := strings.Cut(r.ExportPath, "?")
					p := strings.TrimSuffix(b, "/") + "/export"
					if qs != "" {
						p += "?" + qs
					}
					measure(c, r.Key+" XLSX", join(p, url.Values{"format": {"xlsx"}}))
				}
			}
		}
	}

	fmt.Printf("NFR-P2: dashboards and standard reports over %d months (%s .. %s), budget %s\n", months, from, to, budget)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "report\tstatus\tms\tbytes\t")
	pass := true
	var worst time.Duration
	for _, r := range out {
		flag := ""
		if r.code != 200 || r.d >= budget {
			flag = "  <-- FAIL"
			pass = false
		}
		worst = max(worst, r.d)
		fmt.Fprintf(tw, "%s\t%d\t%s\t%d\t%s\n", r.name, r.code, ms(r.d), r.size, flag)
	}
	tw.Flush()
	fmt.Printf("%d dashboards/reports, slowest %s ms, budget %s -> %s\n", len(out), ms(worst), budget, verdict(pass))
	return pass
}
