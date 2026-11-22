package payroll

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// ───────────────────────────── Workers ─────────────────────────────

type workerOut struct {
	ID               string   `json:"id"`
	FirstName        string   `json:"firstName"`
	LastName         string   `json:"lastName"`
	NationalID       *string  `json:"nationalId"`
	MomoNumber       *string  `json:"momoNumber"`
	MomoName         *string  `json:"momoName"`
	JobTitle         string   `json:"jobTitle"`
	PlantID          string   `json:"plantId"`
	DailyWage        int      `json:"dailyWage"`
	IsActive         bool     `json:"isActive"`
	StartDate        *string  `json:"startDate"`
	CreatedAt        string   `json:"createdAt"`
	UpdatedAt        string   `json:"updatedAt"`
	Plant            plantRef `json:"plant"`
	FullName         string   `json:"fullName"`
	MomoNameMismatch bool     `json:"momoNameMismatch"`
}

const workerCols = `w.id, w."firstName", w."lastName", w."nationalId", w."momoNumber", w."momoName", w."jobTitle", w."plantId",
	w."dailyWage", w."isActive", w."startDate", w."createdAt", w."updatedAt", p.id, p.code, p.name`
const workerFrom = `FROM "Worker" w JOIN "Plant" p ON p.id = w."plantId"`

func scanWorker(row pgx.Row) (workerOut, error) {
	var w workerOut
	var sd *time.Time
	var ca, ua time.Time
	if err := row.Scan(&w.ID, &w.FirstName, &w.LastName, &w.NationalID, &w.MomoNumber, &w.MomoName, &w.JobTitle, &w.PlantID,
		&w.DailyWage, &w.IsActive, &sd, &ca, &ua, &w.Plant.ID, &w.Plant.Code, &w.Plant.Name); err != nil {
		return w, err
	}
	if sd != nil {
		s := dates.ToISO(*sd)
		w.StartDate = &s
	}
	w.CreatedAt, w.UpdatedAt = isoTime(ca), isoTime(ua)
	w.FullName = fullName(w.FirstName, w.LastName)
	w.MomoNameMismatch = MomoNameMismatch(w.FirstName, w.LastName, w.MomoName)
	return w, nil
}

func getWorker(ctx context.Context, q db.DBTX, id string) (workerOut, error) {
	return scanWorker(q.QueryRow(ctx, `SELECT `+workerCols+` `+workerFrom+` WHERE w.id = $1`, id))
}

var likeEsc = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
var digitsRe = regexp.MustCompile(`^\d+$`)
var plusWs = regexp.MustCompile(`[\s+]`)

func (h *handler) listWorkers(c *fiber.Ctx) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	qv := httpx.Query(c)
	active := qv.OptStr("active").OneOf([]string{"true", "false", "all"}, "payroll.v.active").Get()
	search := qv.OptStr("q").Trim().Max(80, "payroll.v.max_80").Get()
	plantQ := qv.OptStr("plantId").Get()
	if err := qv.Err(); err != nil {
		return err
	}
	pg := httpx.Paginate(c, 50)
	plantID := auth.PlantFilter(u, plantQ)
	key := fmt.Sprintf("payroll:workers:%s:%s:%s:%s:%s:%d:%d", lang(c), scopeKey(u), plantID, active, search, pg.Page, pg.PageSize)
	body, err := jsonCache.GetOrLoad(ctx, key, func(ctx context.Context) ([]byte, error) {
		where := []string{"TRUE"}
		var args []any
		add := func(cond string, v any) {
			args = append(args, v)
			where = append(where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args))))
		}
		if plantID != "" {
			add(`w."plantId" = ?`, plantID)
		}
		switch active {
		case "false":
			where = append(where, `NOT w."isActive"`)
		case "true":
			where = append(where, `w."isActive"`)
		}
		if search != "" {
			pat := "%" + likeEsc.Replace(search) + "%"
			args = append(args, pat)
			n := "$" + strconv.Itoa(len(args))
			conds := []string{`w."firstName" ILIKE ` + n, `w."lastName" ILIKE ` + n, `w."momoName" ILIKE ` + n, `w."jobTitle" ILIKE ` + n}
			if digits := plusWs.ReplaceAllString(search, ""); digits != "" && digitsRe.MatchString(digits) {
				momo := digits
				if strings.HasPrefix(momo, "250") {
					momo = "0" + momo[3:]
				}
				args = append(args, "%"+digits+"%", "%"+momo+"%")
				conds = append(conds, `w."nationalId" LIKE $`+strconv.Itoa(len(args)-1), `w."momoNumber" LIKE $`+strconv.Itoa(len(args)))
			}
			where = append(where, "("+strings.Join(conds, " OR ")+")")
		}
		w := strings.Join(where, " AND ")
		b := &pgx.Batch{}
		b.Queue(`SELECT count(*) FROM "Worker" w WHERE `+w, args...)
		b.Queue(`SELECT `+workerCols+` `+workerFrom+` WHERE `+w+` ORDER BY w."isActive" DESC, w."firstName", w."lastName", w.id LIMIT `+
			strconv.Itoa(pg.Limit)+` OFFSET `+strconv.Itoa(pg.Offset), args...)
		// lightweight pass over the whole filter so the page can say "3 MoMo name mismatches"
		b.Queue(`SELECT "firstName", "lastName", "momoName" FROM "Worker" w WHERE `+w+` AND w."momoName" IS NOT NULL`, args...)
		br := h.d.Pool.SendBatch(ctx, b)
		defer br.Close()
		var total int
		if err := br.QueryRow().Scan(&total); err != nil {
			return nil, err
		}
		rows, err := br.Query()
		if err != nil {
			return nil, err
		}
		items := []workerOut{}
		for rows.Next() {
			wk, err := scanWorker(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			items = append(items, wk)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		nrows, err := br.Query()
		if err != nil {
			return nil, err
		}
		mismatch := 0
		for nrows.Next() {
			var f, l string
			var m *string
			if err := nrows.Scan(&f, &l, &m); err != nil {
				nrows.Close()
				return nil, err
			}
			if MomoNameMismatch(f, l, m) {
				mismatch++
			}
		}
		nrows.Close()
		if err := nrows.Err(); err != nil {
			return nil, err
		}
		return json.Marshal(fiber.Map{"items": items, "total": total, "mismatchCount": mismatch, "page": pg.Page, "pageSize": pg.PageSize})
	}, cacheTag)
	if err != nil {
		return err
	}
	return httpx.BytesCached(c, body, "application/json; charset=utf-8", "private, no-cache")
}

// ── input parsing ───────────────────────────────────────────────────────────

type workerInput struct {
	FirstName, LastName, NationalID, MomoNumber, JobTitle, PlantID string
	MomoName                                                       *string
	DailyWage                                                      int
	StartDate                                                      *time.Time
	IsActive                                                       bool

	has map[string]bool // fields supplied (PATCH semantics)
}

// strVal returns the trimmed string at key, or records msg when it is missing / not a string.
func strVal(v *httpx.V, key, msg string) (string, bool) {
	raw, ok := v.Raw(key).(string)
	if !ok {
		v.Fail(key, msg)
		return "", false
	}
	return strings.TrimSpace(raw), true
}

func personName(v *httpx.V, key string) (string, bool) {
	s, ok := strVal(v, key, "v.required")
	if !ok {
		return "", false
	}
	if s == "" {
		v.Fail(key, "v.required")
		return "", false
	}
	if utf8.RuneCountInString(s) > 80 {
		v.Fail(key, "payroll.v.max_80")
		return "", false
	}
	return NormaliseName(s), true
}

// coerceNumber is z.coerce.number(): numbers, numeric strings (""->0), null->0, booleans.
func coerceNumber(raw any) (float64, bool) {
	switch t := raw.(type) {
	case nil:
		return 0, true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case float64:
		return t, true
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0, true
		}
		f, err := strconv.ParseFloat(s, 64)
		return f, err == nil && !math.IsInf(f, 0)
	}
	return 0, false
}

// parseWorker validates the worker fields; partial=true is the PATCH shape (absent = unchanged).
func parseWorker(v *httpx.V, partial bool) workerInput {
	in := workerInput{has: map[string]bool{}}
	want := func(key string) bool {
		if partial && !v.Has(key) {
			return false
		}
		in.has[key] = true
		return true
	}
	if want("firstName") {
		in.FirstName, _ = personName(v, "firstName")
	}
	if want("lastName") {
		in.LastName, _ = personName(v, "lastName")
	}
	if want("nationalId") {
		if s, ok := strVal(v, "nationalId", "payroll.v.national_id_required"); ok {
			if n, ok := NormaliseNationalID(s); ok {
				in.NationalID = n
			} else {
				v.Fail("nationalId", "payroll.v.national_id_format")
			}
		}
	}
	if want("momoNumber") {
		if s, ok := strVal(v, "momoNumber", "payroll.v.momo_required"); ok {
			if n, ok := NormaliseMomoNumber(s); ok {
				in.MomoNumber = n
			} else {
				v.Fail("momoNumber", "payroll.v.momo_format")
			}
		}
	}
	if want("momoName") {
		s := v.NullStr("momoName").Trim().Max(80, "payroll.v.max_80")
		if s.Present() && s.Get() != "" {
			n := NormaliseName(s.Get())
			in.MomoName = &n
		}
	}
	if want("jobTitle") {
		if !v.Has("jobTitle") {
			in.JobTitle = "Helper" // create only: PATCH skips absent fields
		} else {
			in.JobTitle = v.OptStr("jobTitle").Trim().Min(1, "v.required").Max(60, "payroll.v.max_60").Get()
		}
	}
	if want("plantId") {
		if s, ok := strVal(v, "plantId", "v.required"); ok {
			switch {
			case s == "":
				v.Fail("plantId", "v.required")
			case utf8.RuneCountInString(s) > 40:
				v.Fail("plantId", "payroll.v.max_40")
			default:
				in.PlantID = s
			}
		}
	}
	if want("dailyWage") {
		f, ok := coerceNumber(v.Raw("dailyWage"))
		switch {
		case !v.Has("dailyWage") || !ok || math.IsNaN(f):
			v.Fail("dailyWage", "payroll.v.wage_required")
		case f != math.Trunc(f):
			v.Fail("dailyWage", "v.whole_francs")
		case f <= 0:
			v.Fail("dailyWage", "payroll.v.wage_positive")
		case f > 1_000_000:
			v.Fail("dailyWage", "payroll.v.wage_max")
		default:
			in.DailyWage = int(f)
		}
	}
	if want("startDate") {
		s := v.NullStr("startDate").Trim().Date("payroll.v.date")
		if s.Present() {
			if d, err := dates.ParseISO(s.Get()); err == nil {
				in.StartDate = &d
			}
		}
	}
	if want("isActive") {
		b := v.OptBool("isActive")
		if !partial {
			b = b.Default(true)
		}
		in.IsActive = b.Get()
	}
	return in
}

type plantInfo struct {
	ID, Code, Name string
	Active         bool
}

func loadPlant(ctx context.Context, q db.DBTX, id string) (*plantInfo, error) {
	var p plantInfo
	err := q.QueryRow(ctx, `SELECT id, code, name, "isActive" FROM "Plant" WHERE id = $1`, id).Scan(&p.ID, &p.Code, &p.Name, &p.Active)
	if db.IsNoRows(err) {
		return nil, nil
	}
	return &p, err
}

// activePlant resolves a plant that exists and is active, or returns the field error.
func activePlant(c *fiber.Ctx, q db.DBTX, id string) (*plantInfo, error) {
	p, err := loadPlant(reqCtx(c), q, id)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, invalidFields(map[string]string{"plantId": t(c, "payroll.err.unknown_plant")})
	}
	if !p.Active {
		return nil, invalidFields(map[string]string{"plantId": t(c, "payroll.err.plant_inactive")})
	}
	return p, nil
}

func (h *handler) assertUniqueNationalID(c *fiber.Ctx, u *auth.User, nid, exceptID string) error {
	var id, first, last, plantID, code string
	err := h.d.Pool.QueryRow(reqCtx(c), `SELECT w.id, w."firstName", w."lastName", w."plantId", p.code FROM "Worker" w JOIN "Plant" p ON p.id = w."plantId"
		WHERE w."nationalId" = $1`, nid).Scan(&id, &first, &last, &plantID, &code)
	if db.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if id == exceptID {
		return nil
	}
	who := fmt.Sprintf("%s (%s)", fullName(first, last), code)
	if s := auth.ScopedPlantID(u); s != "" && s != plantID { // plant-scoped users are not shown who is at another plant
		who = t(c, "payroll.err.who_other_plant", "plant", code)
	}
	e := keyed(409, "conflict", "payroll.err.nid_taken", "id", nid, "who", who)
	e.Fields = map[string]string{"nationalId": t(c, "payroll.err.nid_taken_field", "who", who)}
	return e
}

func (h *handler) createWorker(c *fiber.Ctx) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in := parseWorker(v, false)
	if err := v.Err(); err != nil {
		return err
	}
	if err := auth.AssertPlantAccess(u, in.PlantID); err != nil {
		return err
	}
	plant, err := activePlant(c, h.d.Pool, in.PlantID)
	if err != nil {
		return err
	}
	if err := h.assertUniqueNationalID(c, u, in.NationalID, ""); err != nil {
		return err
	}
	out, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (workerOut, error) {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO "Worker" ("firstName","lastName","nationalId","momoNumber","momoName","jobTitle","plantId","dailyWage","isActive","startDate","updatedAt")
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`,
			in.FirstName, in.LastName, in.NationalID, in.MomoNumber, in.MomoName, in.JobTitle, in.PlantID, in.DailyWage, in.IsActive, in.StartDate, db.Now()).Scan(&id); err != nil {
			return workerOut{}, err
		}
		w, err := getWorker(ctx, tx, id)
		if err != nil {
			return w, err
		}
		return w, audit.Record(c, tx, audit.Input{Action: "payroll.worker.create", Entity: "Worker", EntityID: id,
			Summary: fmt.Sprintf("Registered worker %s (%s)", w.FullName, plant.Code), After: w})
	})
	if err != nil {
		return err
	}
	invalidate()
	return c.Status(201).JSON(out)
}

func (h *handler) updateWorker(c *fiber.Ctx) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	id := c.Params("id")
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in := parseWorker(v, true)
	if err := v.Err(); err != nil {
		return err
	}
	before, err := getWorker(ctx, h.d.Pool, id)
	if db.IsNoRows(err) {
		return httpx.NotFound("entity.worker")
	}
	if err != nil {
		return err
	}
	if err := auth.AssertPlantAccess(u, before.PlantID); err != nil {
		return err
	}
	if in.has["plantId"] && in.PlantID != before.PlantID {
		if err := auth.AssertPlantAccess(u, in.PlantID); err != nil {
			return err
		}
		if _, err := activePlant(c, h.d.Pool, in.PlantID); err != nil {
			return err
		}
	}
	if in.has["nationalId"] && (before.NationalID == nil || in.NationalID != *before.NationalID) {
		if err := h.assertUniqueNationalID(c, u, in.NationalID, id); err != nil {
			return err
		}
	}
	var sets []string
	var args []any
	set := func(col string, val any) {
		args = append(args, val)
		sets = append(sets, `"`+col+`" = $`+strconv.Itoa(len(args)))
	}
	for _, f := range []struct {
		key string
		val any
	}{{"firstName", in.FirstName}, {"lastName", in.LastName}, {"nationalId", in.NationalID}, {"momoNumber", in.MomoNumber}, {"momoName", in.MomoName},
		{"jobTitle", in.JobTitle}, {"plantId", in.PlantID}, {"dailyWage", in.DailyWage}, {"startDate", in.StartDate}, {"isActive", in.IsActive}} {
		if in.has[f.key] {
			set(f.key, f.val)
		}
	}
	set("updatedAt", db.Now())
	args = append(args, id)
	out, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (workerOut, error) {
		if _, err := tx.Exec(ctx, `UPDATE "Worker" SET `+strings.Join(sets, ", ")+` WHERE id = $`+strconv.Itoa(len(args)), args...); err != nil {
			return workerOut{}, err
		}
		w, err := getWorker(ctx, tx, id)
		if err != nil {
			return w, err
		}
		return w, audit.Record(c, tx, audit.Input{Action: "payroll.worker.update", Entity: "Worker", EntityID: id,
			Summary: "Updated worker " + w.FullName, Before: before, After: w})
	})
	if err != nil {
		return err
	}
	invalidate()
	return c.JSON(out)
}
