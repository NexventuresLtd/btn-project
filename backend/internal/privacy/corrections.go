package privacy

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/masterdata"
	"btn-ops/internal/payroll"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// Editable personal fields per subject type (column names). Worker "phone" is offered only when the column exists.
var (
	workerFields   = []string{"firstName", "lastName", "nationalId", "momoNumber", "momoName"}
	customerFields = []string{"name", "tin", "phone", "email", "address"}
)

func columnExists(ctx context.Context, q db.DBTX, table, col string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2)`,
		table, col).Scan(&ok)
	return ok, err
}

func editableFields(ctx context.Context, q db.DBTX, typ string) ([]string, error) {
	if typ == TypeCustomer {
		return append([]string(nil), customerFields...), nil
	}
	out := append([]string(nil), workerFields...)
	has, err := columnExists(ctx, q, "Worker", "phone")
	if err != nil {
		return nil, err
	}
	if has {
		out = append(out, "phone")
	}
	return out, nil
}

// parseChanges validates the requested values with the same rules as the worker / customer endpoints.
// Values are *string (nil = clear the optional field).
func parseChanges(cv *httpx.V, raw map[string]any, typ string, allowed []string) map[string]*string {
	ok := map[string]bool{}
	for _, f := range allowed {
		ok[f] = true
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := map[string]*string{}
	for _, k := range keys {
		if !ok[k] {
			cv.Fail(k, "privacy.v.not_editable")
			continue
		}
		var val *string
		if typ == TypeWorker {
			switch k {
			case "firstName", "lastName":
				s := cv.Str(k).Trim().Min(1, "v.required").Max(80, "payroll.v.max_80")
				if s.Present() {
					n := payroll.NormaliseName(s.Get())
					val = &n
				}
			case "nationalId":
				s := cv.Str(k).Trim().Min(1, "payroll.v.national_id_required")
				if s.Present() {
					if n, good := payroll.NormaliseNationalID(s.Get()); good {
						val = &n
					} else {
						cv.Fail(k, "payroll.v.national_id_format")
					}
				}
			case "momoNumber":
				s := cv.Str(k).Trim().Min(1, "payroll.v.momo_required")
				if s.Present() {
					if n, good := payroll.NormaliseMomoNumber(s.Get()); good {
						val = &n
					} else {
						cv.Fail(k, "payroll.v.momo_format")
					}
				}
			case "momoName":
				s := cv.NullStr(k).Trim().Max(80, "payroll.v.max_80")
				if s.Present() && s.Get() != "" {
					n := payroll.NormaliseName(s.Get())
					val = &n
				}
			case "phone":
				val = cv.NullStr(k).Trim().Max(30, "privacy.v.max_30").NilIfEmpty()
			}
		} else {
			switch k {
			case "name":
				s := cv.Str(k).Trim().Min(1, "v.required").Max(160).Collapse().Upper()
				if s.Present() {
					n := s.Get()
					val = &n
				}
			case "tin", "phone", "address":
				val = cv.NullStr(k).Trim().Max(300).NilIfEmpty()
			case "email":
				s := cv.NullStr(k).Trim()
				if s.Present() && s.Get() != "" {
					s.Email("v.email")
				}
				val = s.NilIfEmpty()
			}
		}
		out[k] = val
	}
	return out
}

type fieldChange struct {
	Before *string `json:"before"`
	After  *string `json:"after"`
}

type correctionOut struct {
	ID          string                 `json:"id"`
	SubjectType string                 `json:"subjectType"`
	SubjectID   string                 `json:"subjectId"`
	User        *accessUser            `json:"user"`
	Reason      string                 `json:"reason"`
	RequestRef  *string                `json:"requestRef"`
	Changes     map[string]fieldChange `json:"changes"`
	CreatedAt   time.Time              `json:"createdAt"`
}

func eq(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// POST /subjects/:type/:id/corrections {changes: {field: value}, reason, requestRef?}
func (h *handler) correct(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	typ, id, name, err := h.subjectParams(c)
	if err != nil {
		return err
	}
	m, err := httpx.DecodeMap(c)
	if err != nil {
		return err
	}
	v := httpx.NewV(m, lang(c))
	reason := v.Str("reason").Trim().Min(5, "privacy.v.reason_min").Max(500, "privacy.v.max_500").Get()
	ref := v.NullStr("requestRef").Trim().Max(80, "privacy.v.max_80").NilIfEmpty()
	allowed, err := editableFields(ctx, h.d.Pool, typ)
	if err != nil {
		return err
	}
	cv := v.Obj("changes", true)
	raw, _ := m["changes"].(map[string]any)
	var wanted map[string]*string
	if cv != nil {
		if raw != nil && len(raw) == 0 {
			v.Fail("changes", "privacy.v.no_changes")
		}
		wanted = parseChanges(cv, raw, typ, allowed)
	}
	if err := v.Err(); err != nil {
		return err
	}

	table, nameExpr := `"Worker"`, `btrim("firstName" || ' ' || "lastName")`
	if typ == TypeCustomer {
		table, nameExpr = `"Customer"`, `name`
	}
	fields := make([]string, 0, len(wanted))
	for k := range wanted {
		fields = append(fields, k)
	}
	sort.Strings(fields)

	out, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (correctionOut, error) {
		var res correctionOut
		// lock the row and read the current values of the requested fields
		cols := make([]string, len(fields))
		for i, f := range fields {
			cols[i] = `"` + f + `"::text`
		}
		vals := make([]*string, len(fields))
		dst := make([]any, len(fields))
		for i := range vals {
			dst[i] = &vals[i]
		}
		if err := tx.QueryRow(ctx, `SELECT `+strings.Join(cols, ", ")+` FROM `+table+` WHERE id = $1 FOR UPDATE`, id).Scan(dst...); err != nil {
			return res, err
		}
		changes := map[string]fieldChange{}
		before, after := map[string]*string{}, map[string]*string{}
		var sets []string
		var args []any
		for i, f := range fields {
			if eq(vals[i], wanted[f]) {
				continue
			}
			changes[f] = fieldChange{Before: vals[i], After: wanted[f]}
			before[f], after[f] = vals[i], wanted[f]
			args = append(args, wanted[f])
			sets = append(sets, `"`+f+`" = $`+strconv.Itoa(len(args)))
		}
		if len(changes) == 0 {
			return res, httpx.BadRequest("privacy.err.no_difference", nil)
		}
		// uniqueness rules of the owning modules
		if nid, ok := after["nationalId"]; ok && typ == TypeWorker && nid != nil {
			var other string
			err := tx.QueryRow(ctx, `SELECT btrim("firstName" || ' ' || "lastName") FROM "Worker" WHERE "nationalId" = $1 AND id <> $2`, *nid, id).Scan(&other)
			if err == nil {
				e := httpx.NewKey(409, "conflict", "privacy.err.nid_taken", "who", other)
				e.Fields = map[string]string{"changes.nationalId": t(c, "privacy.err.nid_taken", "who", other)}
				return res, e
			} else if !db.IsNoRows(err) {
				return res, err
			}
		}
		if nm, ok := after["name"]; ok && typ == TypeCustomer && nm != nil {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM "Customer" WHERE name = $1 AND id <> $2)`, *nm, id).Scan(&exists); err != nil {
				return res, err
			}
			if exists {
				e := httpx.NewKey(409, "conflict", "privacy.err.name_taken")
				e.Fields = map[string]string{"changes.name": t(c, "privacy.err.name_taken")}
				return res, e
			}
		}
		if typ == TypeWorker {
			args = append(args, db.Now())
			sets = append(sets, `"updatedAt" = $`+strconv.Itoa(len(args)))
		}
		args = append(args, id)
		var newName string
		if err := tx.QueryRow(ctx, `UPDATE `+table+` SET `+strings.Join(sets, ", ")+` WHERE id = $`+strconv.Itoa(len(args))+` RETURNING `+nameExpr, args...).Scan(&newName); err != nil {
			return res, err
		}
		chJSON, err := json.Marshal(changes)
		if err != nil {
			return res, err
		}
		uid, _ := c.Locals("userId").(string)
		if err := tx.QueryRow(ctx, `INSERT INTO privacy_correction (subject_type, subject_id, user_id, reason, request_ref, changes, created_at)
			VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7) RETURNING id, created_at`, typ, id, nilIfEmpty(uid), reason, ref, chJSON, db.Now()).Scan(&res.ID, &res.CreatedAt); err != nil {
			return res, err
		}
		changed := make([]string, 0, len(changes))
		for _, f := range fields {
			if _, ok := changes[f]; ok {
				changed = append(changed, f)
			}
		}
		summary := fmt.Sprintf("Corrected personal data of %s (%s). Reason: %s", newName, strings.Join(changed, ", "), reason)
		if ref != nil {
			summary += " [request " + *ref + "]"
		}
		if name != newName {
			summary = fmt.Sprintf("Corrected personal data of %s, formerly %s (%s). Reason: %s", newName, name, strings.Join(changed, ", "), reason)
			if ref != nil {
				summary += " [request " + *ref + "]"
			}
		}
		if err := audit.Record(c, tx, audit.Input{Action: "privacy.correct", Entity: entityName(typ), EntityID: id, Summary: summary,
			Before: before, After: after}); err != nil {
			return res, err
		}
		if err := LogAccess(ctx, tx, c, typ, id, ActionCorrect); err != nil {
			return res, err
		}
		res.SubjectType, res.SubjectID, res.Reason, res.RequestRef, res.Changes = typ, id, reason, ref, changes
		res.CreatedAt = res.CreatedAt.UTC()
		return res, nil
	})
	if err != nil {
		return err
	}
	masterdata.InvalidateLookups()
	payroll.InvalidateCaches()
	if u := c.Locals("userId"); u != nil {
		out.User = h.user(ctx, fmt.Sprint(u))
	}
	return c.Status(201).JSON(out)
}

func (h *handler) user(ctx context.Context, id string) *accessUser {
	var u accessUser
	if err := h.d.Pool.QueryRow(ctx, `SELECT id, name, email FROM "User" WHERE id = $1`, id).Scan(&u.ID, &u.Name, &u.Email); err != nil {
		return nil
	}
	return &u
}

// GET /subjects/:type/:id/corrections
func (h *handler) listCorrections(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	typ, id, _, err := h.subjectParams(c)
	if err != nil {
		return err
	}
	rows, err := h.d.Pool.Query(ctx, `SELECT pc.id, pc.reason, pc.request_ref, pc.changes, pc.created_at, u.id, u.name, u.email
		FROM privacy_correction pc LEFT JOIN "User" u ON u.id = pc.user_id
		WHERE pc.subject_type = $1 AND pc.subject_id = $2 ORDER BY pc.created_at DESC, pc.id DESC LIMIT 500`, typ, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []correctionOut{}
	for rows.Next() {
		r := correctionOut{SubjectType: typ, SubjectID: id}
		var ch []byte
		var uID, uName, uEmail *string
		if err := rows.Scan(&r.ID, &r.Reason, &r.RequestRef, &ch, &r.CreatedAt, &uID, &uName, &uEmail); err != nil {
			return err
		}
		_ = json.Unmarshal(ch, &r.Changes)
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
	return c.JSON(fiber.Map{"items": items})
}
