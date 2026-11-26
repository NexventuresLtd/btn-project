package dataimport

import (
	"regexp"
	"strconv"
	"strings"

	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/i18n"
	"btn-ops/internal/masterdata"
	"btn-ops/internal/payroll"

	"github.com/jackc/pgx/v5"
)

// Column types (shown in the column help and used for template examples).
const (
	TText    = "text"
	TCode    = "code"
	TInteger = "integer"
	TDecimal = "decimal"
	TMoney   = "money"
	TDate    = "date"
	TBoolean = "boolean"
	TEnum    = "enum"
)

// column describes one CSV column of a kind.
type column struct {
	Name     string
	Required bool
	Type     string
	Example  string
	Values   []string // enum values
	Aliases  []string // extra header spellings (compared after normHeader)
}

// kindSpec is one importable data set.
type kindSpec struct {
	Name     string
	Perm     auth.Permission
	Columns  []column
	Examples [][]string // template example rows (in Columns order)
	plan     func(e *planEnv) ([]*Item, error)
	// opening kinds take a per-plant advisory lock shared with the manual opening endpoints
	lockPrefix string
}

func (k *kindSpec) headerIndex() map[string]string {
	m := map[string]string{}
	for _, c := range k.Columns {
		m[normHeader(c.Name)] = c.Name
		for _, a := range c.Aliases {
			m[normHeader(a)] = c.Name
		}
	}
	return m
}

var activeCol = column{Name: "active", Type: TBoolean, Example: "yes", Aliases: []string{"isActive", "status", "enabled"}}
var siteCol = column{Name: "site", Required: true, Type: TCode, Example: "BTN-1", Aliases: []string{"siteCode", "plant", "plantCode", "location", "locationCode"}}

var kinds = []*kindSpec{
	{
		Name: "products", Perm: "masterdata.manage", plan: planProducts,
		Columns: []column{
			{Name: "code", Required: true, Type: TCode, Example: "HB25", Aliases: []string{"productCode"}},
			{Name: "name", Required: true, Type: TText, Example: "Hollow Block 25", Aliases: []string{"productName"}},
			{Name: "dimensions", Type: TText, Example: "25 x 20 x 40 cm", Aliases: []string{"size"}},
			{Name: "unit", Type: TText, Example: "Pcs", Aliases: []string{"uom"}},
			{Name: "defaultPrice", Required: true, Type: TMoney, Example: "1800", Aliases: []string{"price", "unitPrice"}},
			{Name: "sortOrder", Type: TInteger, Example: "70", Aliases: []string{"sort", "order"}},
			activeCol,
		},
		Examples: [][]string{{"HB25", "Hollow Block 25", "25 x 20 x 40 cm", "Pcs", "1800", "70", "yes"}, {"PV8", "Pavers 8 cm", "", "Pcs", "450", "62", "yes"}},
	},
	{
		Name: "materials", Perm: "masterdata.manage", plan: planMaterials,
		Columns: []column{
			{Name: "code", Required: true, Type: TCode, Example: "GRAVEL", Aliases: []string{"materialCode"}},
			{Name: "name", Required: true, Type: TText, Example: "Gravel 3/8", Aliases: []string{"materialName"}},
			{Name: "unit", Required: true, Type: TText, Example: "TRUCK", Aliases: []string{"uom"}},
			{Name: "minLevel", Type: TDecimal, Example: "2", Aliases: []string{"minimum", "reorderLevel", "minStock"}},
			{Name: "sortOrder", Type: TInteger, Example: "70", Aliases: []string{"sort", "order"}},
			activeCol,
		},
		Examples: [][]string{{"GRAVEL", "Gravel 3/8", "TRUCK", "2", "70", "yes"}, {"PIGMENT", "Red Pigment", "KG", "12.5", "80", "yes"}},
	},
	{
		Name: "customers", Perm: "customers.manage", plan: planCustomers,
		Columns: []column{
			{Name: "name", Required: true, Type: TText, Example: "KIGALI BUILDERS LTD", Aliases: []string{"customer", "customerName", "company"}},
			{Name: "tin", Type: TText, Example: "102345678", Aliases: []string{"tinNumber", "taxId"}},
			{Name: "phone", Type: TText, Example: "0788123456", Aliases: []string{"telephone", "mobile"}},
			{Name: "email", Type: TText, Example: "orders@kigalibuilders.rw", Aliases: []string{"mail", "emailAddress"}},
			{Name: "address", Type: TText, Example: "KN 5 Rd, Kigali"},
			activeCol,
		},
		Examples: [][]string{{"KIGALI BUILDERS LTD", "102345678", "0788123456", "orders@kigalibuilders.rw", "KN 5 Rd, Kigali", "yes"},
			{"MUKAMANA JEANNE", "", "0722000111", "", "Bugesera", "yes"}},
	},
	{
		Name: "workers", Perm: "workers.manage", plan: planWorkers,
		Columns: []column{
			{Name: "nationalId", Required: true, Type: TText, Example: "1199080012345678", Aliases: []string{"nid", "idNumber", "nationalIdNumber"}},
			{Name: "firstName", Required: true, Type: TText, Example: "JEAN", Aliases: []string{"first", "givenName"}},
			{Name: "lastName", Required: true, Type: TText, Example: "MUGISHA", Aliases: []string{"last", "surname", "familyName"}},
			siteCol,
			{Name: "dailyWage", Required: true, Type: TMoney, Example: "3500", Aliases: []string{"wage", "dailyRate", "rate"}},
			{Name: "momoNumber", Required: true, Type: TText, Example: "0788123456", Aliases: []string{"momo", "momoPhone", "phone"}},
			{Name: "momoName", Type: TText, Example: "JEAN MUGISHA", Aliases: []string{"momoAccountName"}},
			{Name: "jobTitle", Type: TText, Example: "Helper", Aliases: []string{"job", "title", "position"}},
			{Name: "startDate", Type: TDate, Example: "01/12/2026", Aliases: []string{"start", "hireDate", "joined"}},
			activeCol,
		},
		Examples: [][]string{{"1199080012345678", "JEAN", "MUGISHA", "BTN-1", "3500", "0788123456", "JEAN MUGISHA", "Helper", "01/12/2026", "yes"}},
	},
	{
		Name: "sites", Perm: "masterdata.manage", plan: planSites,
		Columns: []column{
			{Name: "code", Required: true, Type: TCode, Example: "BTN-4", Aliases: []string{"siteCode", "locationCode"}},
			{Name: "name", Required: true, Type: TText, Example: "BTN 4 Plant", Aliases: []string{"siteName"}},
			{Name: "kind", Type: TEnum, Example: "SITE", Values: masterdata.LocationKinds, Aliases: []string{"type", "locationKind"}},
			{Name: "location", Type: TText, Example: "Bugesera", Aliases: []string{"address", "district"}},
			activeCol,
		},
		Examples: [][]string{{"BTN-4", "BTN 4 Plant", "SITE", "Bugesera", "yes"}, {"WH-KGL", "Kigali Warehouse", "WAREHOUSE", "Gikondo", "yes"}},
	},
	{
		Name: "opening-stock", Perm: "stock.adjust", plan: planOpeningStock, lockPrefix: "stock-opening:",
		Columns: []column{
			siteCol,
			{Name: "product", Required: true, Type: TCode, Example: "HB20", Aliases: []string{"productCode"}},
			{Name: "quantity", Required: true, Type: TInteger, Example: "1200", Aliases: []string{"qty", "openingQuantity", "balance"}},
			{Name: "date", Required: true, Type: TDate, Example: "01/11/2026", Aliases: []string{"openingDate", "asOf", "asOfDate"}},
			{Name: "note", Type: TText, Example: "Go-live count", Aliases: []string{"notes", "comment"}},
		},
		Examples: [][]string{{"BTN-1", "HB20", "1200", "01/11/2026", "Go-live count"}, {"BTN-1", "HB15", "800", "01/11/2026", ""}},
	},
	{
		Name: "opening-materials", Perm: "materials.adjust", plan: planOpeningMaterials, lockPrefix: "material-opening:",
		Columns: []column{
			siteCol,
			{Name: "material", Required: true, Type: TCode, Example: "CEMENT", Aliases: []string{"materialCode"}},
			{Name: "quantity", Required: true, Type: TDecimal, Example: "45.5", Aliases: []string{"qty", "openingQuantity", "balance"}},
			{Name: "unitCost", Type: TMoney, Example: "12500", Aliases: []string{"cost", "unitPrice", "price"}},
			{Name: "date", Required: true, Type: TDate, Example: "01/11/2026", Aliases: []string{"openingDate", "asOf", "asOfDate"}},
			{Name: "note", Type: TText, Example: "Go-live count", Aliases: []string{"notes", "comment"}},
		},
		Examples: [][]string{{"BTN-1", "CEMENT", "45.5", "12500", "01/11/2026", "Go-live count"}, {"BTN-1", "QDUST", "3", "", "01/11/2026", ""}},
	},
}

func kindByName(name string) *kindSpec {
	for _, k := range kinds {
		if k.Name == name {
			return k
		}
	}
	return nil
}

func updateApply(table, id string, ch changes) func(*pgx.Batch) {
	sql := `UPDATE ` + table + ` SET ` + strings.Join(ch.sets, ", ") + ` WHERE id=$1`
	args := append([]any{id}, ch.args...)
	return func(b *pgx.Batch) { b.Queue(sql, args...) }
}

// finish sets create/update/unchanged for a row without errors.
func finish(x *rowCtx, found bool, ch changes, create func(*pgx.Batch), update func(*pgx.Batch)) {
	if !x.ok() {
		return
	}
	switch {
	case !found:
		x.it.Action, x.it.apply = ActCreate, create
	case len(ch.cols) == 0:
		x.it.Action = ActUnchanged
	default:
		x.it.Action, x.it.Changes, x.it.apply = ActUpdate, ch.cols, update
	}
}

// ───────────────────────────── products ─────────────────────────────

func planProducts(e *planEnv) ([]*Item, error) {
	type prod struct {
		id, name, unit string
		dims           *string
		price, sort    int
		active         bool
	}
	ex := map[string]prod{}
	rows, err := e.q.Query(e.ctx, `SELECT id, code, name, dimensions, unit, "defaultPrice", "sortOrder", "isActive" FROM "Product"`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p prod
		var code string
		if err := rows.Scan(&p.id, &code, &p.name, &p.dims, &p.unit, &p.price, &p.sort, &p.active); err != nil {
			rows.Close()
			return nil, err
		}
		ex[strings.ToUpper(code)] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := e.loadUnits(); err != nil {
		return nil, err
	}
	seen := dups{}
	var items []*Item
	for _, rec := range e.t.records {
		x := e.row(rec)
		code, okc := x.code("code", true)
		name, _ := x.text("name", true, 120)
		dims, hasDims := x.text("dimensions", false, 300)
		unit, hasUnit := x.text("unit", false, 20)
		price, _ := x.money("defaultPrice", true, 0, 100_000_000)
		sort, hasSort := x.whole("sortOrder", false, -1_000_000, 1_000_000)
		active := x.boolean("active")
		x.it.Key, x.it.Label = code, name
		if okc {
			seen.check(x, "code", "code", code)
		}
		if hasUnit && len(e.units) > 0 && !e.units[strings.ToUpper(unit)] {
			x.warn("unit", "dataimport.warn.unit_unknown", "unit", unit)
		}
		p, found := ex[code]
		var ch changes
		if found {
			if name != p.name {
				ch.add("name", "name", name)
			}
			if hasDims && !strPtrEq(p.dims, dims) {
				ch.add("dimensions", "dimensions", dims)
			}
			if hasUnit && unit != p.unit {
				ch.add("unit", "unit", unit)
			}
			if int(price) != p.price {
				ch.add("defaultPrice", "defaultPrice", int(price))
			}
			if hasSort && int(sort) != p.sort {
				ch.add("sortOrder", "sortOrder", int(sort))
			}
			if active != nil && *active != p.active {
				ch.add("active", "isActive", *active)
			}
		}
		if !hasUnit {
			unit = "Pcs"
		}
		isActive := active == nil || *active
		var dimsArg any
		if hasDims {
			dimsArg = dims
		}
		finish(x, found, ch, func(b *pgx.Batch) {
			b.Queue(`INSERT INTO "Product" (id, code, name, dimensions, unit, "defaultPrice", "sortOrder", "isActive") VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
				db.NewID(), code, name, dimsArg, unit, int(price), int(sort), isActive)
		}, updateApply(`"Product"`, p.id, ch))
		items = append(items, x.it)
	}
	return items, nil
}

// ───────────────────────────── materials ─────────────────────────────

func planMaterials(e *planEnv) ([]*Item, error) {
	type mat struct {
		id, name, unit string
		min            int64
		sort           int
		active         bool
	}
	ex := map[string]mat{}
	rows, err := e.q.Query(e.ctx, `SELECT id, code, name, unit, "minLevel"::text, "sortOrder", "isActive" FROM "Material"`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var m mat
		var code, min string
		if err := rows.Scan(&m.id, &code, &m.name, &m.unit, &min, &m.sort, &m.active); err != nil {
			rows.Close()
			return nil, err
		}
		m.min = milliFromDB(min)
		ex[strings.ToUpper(code)] = m
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := e.loadUnits(); err != nil {
		return nil, err
	}
	seen := dups{}
	var items []*Item
	for _, rec := range e.t.records {
		x := e.row(rec)
		code, okc := x.code("code", true)
		name, _ := x.text("name", true, 120)
		unit, hasUnit := x.text("unit", true, 20)
		min, hasMin := x.milli("minLevel", false, 0, 1_000_000_000_000)
		sort, hasSort := x.whole("sortOrder", false, -1_000_000, 1_000_000)
		active := x.boolean("active")
		x.it.Key, x.it.Label = code, name
		if okc {
			seen.check(x, "code", "code", code)
		}
		if hasUnit && len(e.units) > 0 && !e.units[strings.ToUpper(unit)] {
			x.warn("unit", "dataimport.warn.unit_unknown", "unit", unit)
		}
		m, found := ex[code]
		var ch changes
		if found {
			if name != m.name {
				ch.add("name", "name", name)
			}
			if unit != m.unit {
				ch.add("unit", "unit", unit)
			}
			if hasMin && min != m.min {
				ch.add("minLevel", "minLevel", formatMilli(min))
			}
			if hasSort && int(sort) != m.sort {
				ch.add("sortOrder", "sortOrder", int(sort))
			}
			if active != nil && *active != m.active {
				ch.add("active", "isActive", *active)
			}
			// numeric column: cast the text placeholder
			for i, s := range ch.sets {
				if strings.HasPrefix(s, `"minLevel"=`) {
					ch.sets[i] = s + "::numeric"
				}
			}
		}
		isActive := active == nil || *active
		finish(x, found, ch, func(b *pgx.Batch) {
			b.Queue(`INSERT INTO "Material" (id, code, name, unit, "minLevel", "sortOrder", "isActive") VALUES ($1,$2,$3,$4,$5::numeric,$6,$7)`,
				db.NewID(), code, name, unit, formatMilli(min), int(sort), isActive)
		}, updateApply(`"Material"`, m.id, ch))
		items = append(items, x.it)
	}
	return items, nil
}

// ───────────────────────────── customers ─────────────────────────────

var emailRe = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

func planCustomers(e *planEnv) ([]*Item, error) {
	type cust struct {
		id, name                   string
		tin, phone, email, address *string
		active                     bool
	}
	byName := map[string]*cust{}
	byTIN := map[string][]*cust{}
	rows, err := e.q.Query(e.ctx, `SELECT id, name, tin, phone, email, address, "isActive" FROM "Customer"`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		c := &cust{}
		if err := rows.Scan(&c.id, &c.name, &c.tin, &c.phone, &c.email, &c.address, &c.active); err != nil {
			rows.Close()
			return nil, err
		}
		byName[strings.ToUpper(collapse(c.name))] = c
		if c.tin != nil && *c.tin != "" {
			k := strings.ReplaceAll(*c.tin, " ", "")
			byTIN[k] = append(byTIN[k], c)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	names, tins := dups{}, dups{}
	var items []*Item
	for _, rec := range e.t.records {
		x := e.row(rec)
		name, okn := x.text("name", true, 160)
		name = strings.ToUpper(name)
		tin, hasTIN := x.text("tin", false, 30)
		tin = strings.ReplaceAll(tin, " ", "")
		phone, hasPhone := x.text("phone", false, 40)
		email, hasEmail := x.text("email", false, 160)
		address, hasAddr := x.text("address", false, 300)
		active := x.boolean("active")
		if hasEmail {
			email = strings.ToLower(email)
			if !emailRe.MatchString(email) {
				x.fail("email", "dataimport.v.email")
			}
		}
		x.it.Key, x.it.Label = name, name
		if hasTIN {
			x.it.Key = tin
		}
		if okn {
			names.check(x, "name", "name", name)
		}
		if hasTIN {
			tins.check(x, "tin", "tin", tin)
		}
		var target *cust
		if okn {
			nameMatch := byName[name]
			target = nameMatch
			if hasTIN {
				switch m := byTIN[tin]; {
				case len(m) > 1:
					x.fail("tin", "dataimport.conflict.customer_tin_shared", "tin", tin)
				case len(m) == 1:
					if nameMatch != nil && nameMatch.id != m[0].id {
						x.fail("name", "dataimport.conflict.customer_tin_name", "tin", tin, "other", m[0].name)
					}
					target = m[0]
				}
			}
		}
		var ch changes
		found := target != nil
		if found {
			if name != target.name {
				ch.add("name", "name", name)
			}
			if hasTIN && !strPtrEq(target.tin, tin) {
				ch.add("tin", "tin", tin)
			}
			if hasPhone && !strPtrEq(target.phone, phone) {
				ch.add("phone", "phone", phone)
			}
			if hasEmail && !strPtrEq(target.email, email) {
				ch.add("email", "email", email)
			}
			if hasAddr && !strPtrEq(target.address, address) {
				ch.add("address", "address", address)
			}
			if active != nil && *active != target.active {
				ch.add("active", "isActive", *active)
			}
		}
		opt := func(set bool, s string) any {
			if set {
				return s
			}
			return nil
		}
		tinA, phoneA, emailA, addrA := opt(hasTIN, tin), opt(hasPhone, phone), opt(hasEmail, email), opt(hasAddr, address)
		isActive := active == nil || *active
		id := ""
		if target != nil {
			id = target.id
		}
		finish(x, found, ch, func(b *pgx.Batch) {
			b.Queue(`INSERT INTO "Customer" (id, name, tin, phone, email, address, "isActive") VALUES ($1,$2,$3,$4,$5,$6,$7)`,
				db.NewID(), name, tinA, phoneA, emailA, addrA, isActive)
		}, updateApply(`"Customer"`, id, ch))
		items = append(items, x.it)
	}
	return items, nil
}

// ───────────────────────────── sites (locations) ─────────────────────────────

func planSites(e *planEnv) ([]*Item, error) {
	if err := e.loadSites(); err != nil {
		return nil, err
	}
	seen := dups{}
	var items []*Item
	for _, rec := range e.t.records {
		x := e.row(rec)
		code, okc := x.code("code", true)
		name, _ := x.text("name", true, 120)
		kind, hasKind := x.enum("kind", masterdata.LocationKinds)
		loc, hasLoc := x.text("location", false, 300)
		active := x.boolean("active")
		x.it.Key, x.it.Label = code, name
		if okc {
			seen.check(x, "code", "code", code)
		}
		s, found := e.sites[code]
		if found && e.scope != "" && s.ID != e.scope {
			x.fail("code", "dataimport.scope.site")
		}
		var ch changes
		if found {
			if name != s.Name {
				ch.add("name", "name", name)
			}
			if hasKind && kind != s.Kind {
				ch.add("kind", "kind", kind)
			}
			if hasLoc && !strPtrEq(s.Location, loc) {
				ch.add("location", "location", loc)
			}
			if active != nil && *active != s.Active {
				ch.add("active", "isActive", *active)
				if !*active {
					x.warn("active", "dataimport.warn.deactivate_site")
				}
			}
		}
		if !hasKind {
			kind = masterdata.KindSite
		}
		var locA any
		if hasLoc {
			locA = loc
		}
		isActive := active == nil || *active
		finish(x, found, ch, func(b *pgx.Batch) {
			b.Queue(`INSERT INTO "Plant" (id, code, name, kind, location, "isActive") VALUES ($1,$2,$3,$4,$5,$6)`, db.NewID(), code, name, kind, locA, isActive)
		}, updateApply(`"Plant"`, s.ID, ch))
		items = append(items, x.it)
	}
	return items, nil
}

// ───────────────────────────── workers ─────────────────────────────

func planWorkers(e *planEnv) ([]*Item, error) {
	if err := e.loadSites(); err != nil {
		return nil, err
	}
	type worker struct {
		id, first, last, momo, job, plantID string
		momoName, start                     *string
		wage                                int
		active                              bool
	}
	ex := map[string]worker{}
	rows, err := e.q.Query(e.ctx, `SELECT id, "nationalId", "firstName", "lastName", "momoNumber", "momoName", "jobTitle", "plantId", "dailyWage", "isActive", "startDate"::text
		FROM "Worker" WHERE "nationalId" IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var w worker
		var nid string
		var momo *string
		if err := rows.Scan(&w.id, &nid, &w.first, &w.last, &momo, &w.momoName, &w.job, &w.plantID, &w.wage, &w.active, &w.start); err != nil {
			rows.Close()
			return nil, err
		}
		if momo != nil {
			w.momo = *momo
		}
		ex[nid] = w
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	seen := dups{}
	var items []*Item
	for _, rec := range e.t.records {
		x := e.row(rec)
		nidRaw, hasNID := x.text("nationalId", true, 40)
		nid := ""
		if hasNID {
			if n, ok := payroll.NormaliseNationalID(nidRaw); ok {
				nid = n
			} else {
				x.fail("nationalId", "dataimport.v.national_id")
			}
		}
		first, okF := x.text("firstName", true, 80)
		last, okL := x.text("lastName", true, 80)
		first, last = payroll.NormaliseName(first), payroll.NormaliseName(last)
		site, okSite := x.site("site")
		wage, _ := x.money("dailyWage", true, 1, 1_000_000)
		momoRaw, hasMomo := x.text("momoNumber", true, 20)
		momo := ""
		if hasMomo {
			if n, ok := payroll.NormaliseMomoNumber(momoRaw); ok {
				momo = n
			} else {
				x.fail("momoNumber", "dataimport.v.momo")
			}
		}
		momoName, hasMomoName := x.text("momoName", false, 80)
		momoName = payroll.NormaliseName(momoName)
		job, hasJob := x.text("jobTitle", false, 60)
		start, hasStart := x.date("startDate", false, false)
		active := x.boolean("active")
		x.it.Key, x.it.Label = nid, strings.TrimSpace(first+" "+last)
		seen.check(x, "nationalId", "nationalId", nid)
		if okF && okL && hasMomoName && payroll.MomoNameMismatch(first, last, &momoName) {
			x.warn("momoName", "dataimport.warn.momo_name")
		}
		w, found := ex[nid]
		if found && e.scope != "" && w.plantID != e.scope {
			x.fail("nationalId", "dataimport.scope.worker_other_site")
		}
		var ch changes
		if found {
			if first != w.first {
				ch.add("firstName", "firstName", first)
			}
			if last != w.last {
				ch.add("lastName", "lastName", last)
			}
			if okSite && site.ID != w.plantID {
				ch.add("site", "plantId", site.ID)
			}
			if int(wage) != w.wage {
				ch.add("dailyWage", "dailyWage", int(wage))
			}
			if momo != w.momo {
				ch.add("momoNumber", "momoNumber", momo)
			}
			if hasMomoName && !strPtrEq(w.momoName, momoName) {
				ch.add("momoName", "momoName", momoName)
			}
			if hasJob && job != w.job {
				ch.add("jobTitle", "jobTitle", job)
			}
			if hasStart && !strPtrEq(w.start, start) {
				ch.add("startDate", "startDate", start)
				ch.sets[len(ch.sets)-1] += "::date"
			}
			if active != nil && *active != w.active {
				ch.add("active", "isActive", *active)
			}
			if len(ch.cols) > 0 {
				ch.args = append(ch.args, db.Now())
				ch.sets = append(ch.sets, `"updatedAt"=$`+strconv.Itoa(len(ch.args)+1))
			}
		}
		if !hasJob {
			job = "Helper"
		}
		var momoNameA, startA any
		if hasMomoName {
			momoNameA = momoName
		}
		if hasStart {
			startA = start
		}
		isActive := active == nil || *active
		finish(x, found, ch, func(b *pgx.Batch) {
			b.Queue(`INSERT INTO "Worker" (id, "firstName", "lastName", "nationalId", "momoNumber", "momoName", "jobTitle", "plantId", "dailyWage", "isActive", "startDate", "updatedAt")
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::date,$12)`,
				db.NewID(), first, last, nid, momo, momoNameA, job, site.ID, int(wage), isActive, startA, db.Now())
		}, updateApply(`"Worker"`, w.id, ch))
		items = append(items, x.it)
	}
	return items, nil
}

// ───────────────────────────── opening balances ─────────────────────────────

type openingRow struct {
	qty   int64 // stock: units; materials: thousandths
	date  string
	cost  *int
	first string // earliest non-opening movement date
}

func loadOpenings(e *planEnv, table, itemCol string, milli bool) (map[string]*openingRow, error) {
	out := map[string]*openingRow{}
	qtyExpr := `sum(quantity)::text`
	costExpr := `NULL::int`
	if table == `"MaterialMovement"` {
		costExpr = `max("unitCost")`
	}
	rows, err := e.q.Query(e.ctx, `SELECT "plantId", `+itemCol+`, `+qtyExpr+`, min(date)::text, `+costExpr+` FROM `+table+` WHERE type='OPENING' GROUP BY 1, 2`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p, i, q, d string
		var cost *int
		if err := rows.Scan(&p, &i, &q, &d, &cost); err != nil {
			rows.Close()
			return nil, err
		}
		o := &openingRow{date: d, cost: cost}
		if milli {
			o.qty = milliFromDB(q)
		} else {
			n, _, _ := parseInt(q)
			o.qty = n
		}
		out[p+"/"+i] = o
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = e.q.Query(e.ctx, `SELECT "plantId", `+itemCol+`, min(date)::text FROM `+table+` WHERE type<>'OPENING' GROUP BY 1, 2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p, i, d string
		if err := rows.Scan(&p, &i, &d); err != nil {
			return nil, err
		}
		o := out[p+"/"+i]
		if o == nil {
			o = &openingRow{qty: -1}
			out[p+"/"+i] = o
		}
		o.first = d
	}
	return out, rows.Err()
}

// dmy prints an ISO date as DD/MM/YYYY.
func dmy(iso string) string {
	if len(iso) != 10 {
		return iso
	}
	return iso[8:10] + "/" + iso[5:7] + "/" + iso[0:4]
}

func planOpeningStock(e *planEnv) ([]*Item, error) {
	if err := e.loadSites(); err != nil {
		return nil, err
	}
	products, err := e.loadItems(`"Product"`)
	if err != nil {
		return nil, err
	}
	ex, err := loadOpenings(e, `"StockMovement"`, `"productId"`, false)
	if err != nil {
		return nil, err
	}
	seen := dups{}
	noteDefault := i18n.T(i18n.EN, "stock.opening_note")
	var items []*Item
	for _, rec := range e.t.records {
		x := e.row(rec)
		site, okS := x.site("site")
		prod, okP := x.ref("product", products, "dataimport.ref.unknown_product", "dataimport.ref.inactive_product")
		qty, _ := x.whole("quantity", true, 0, 10_000_000)
		date, _ := x.date("date", true, true)
		note, hasNote := x.text("note", false, 300)
		sc, _ := x.raw("site")
		pc, _ := x.raw("product")
		x.it.Key = strings.ToUpper(sc) + " / " + strings.ToUpper(pc)
		if okS && okP {
			x.it.Label = prod.Name
			seen.check(x, "product", "site + product", site.ID+"/"+prod.ID)
			x.it.plant = site.ID
		}
		if x.ok() {
			if o := ex[site.ID+"/"+prod.ID]; o != nil && o.qty >= 0 {
				if o.qty == qty && o.date == date {
					x.it.Action = ActUnchanged
				} else {
					x.fail("quantity", "dataimport.conflict.opening_exists", "qty", fmtInt(o.qty), "date", dmy(o.date))
				}
			} else {
				if o != nil && o.first != "" && o.first < date {
					x.warn("date", "dataimport.warn.movements_before", "date", dmy(o.first))
				}
				if !hasNote {
					note = noteDefault
				}
				x.it.Action = ActCreate
				pid, plid, q, rid, uid := prod.ID, site.ID, int(qty), e.reportID, e.userID
				x.it.apply = func(b *pgx.Batch) {
					b.Queue(`INSERT INTO "StockMovement" (id, "plantId", "productId", date, type, quantity, "sourceType", "sourceId", note, "createdById")
						VALUES ($1,$2,$3,$4::date,'OPENING',$5,'Opening',$6,$7,$8)`, db.NewID(), plid, pid, date, q, rid, note, uid)
				}
			}
		}
		items = append(items, x.it)
	}
	return items, nil
}

func planOpeningMaterials(e *planEnv) ([]*Item, error) {
	if err := e.loadSites(); err != nil {
		return nil, err
	}
	mats, err := e.loadItems(`"Material"`)
	if err != nil {
		return nil, err
	}
	ex, err := loadOpenings(e, `"MaterialMovement"`, `"materialId"`, true)
	if err != nil {
		return nil, err
	}
	seen := dups{}
	var items []*Item
	for _, rec := range e.t.records {
		x := e.row(rec)
		site, okS := x.site("site")
		mat, okM := x.ref("material", mats, "dataimport.ref.unknown_material", "dataimport.ref.inactive_material")
		qty, _ := x.milli("quantity", true, 0, 10_000_000_000)
		cost, hasCost := x.money("unitCost", false, 0, 100_000_000)
		date, _ := x.date("date", true, true)
		note, hasNote := x.text("note", false, 500)
		sc, _ := x.raw("site")
		mc, _ := x.raw("material")
		x.it.Key = strings.ToUpper(sc) + " / " + strings.ToUpper(mc)
		if okS && okM {
			x.it.Label = mat.Name
			seen.check(x, "material", "site + material", site.ID+"/"+mat.ID)
			x.it.plant = site.ID
		}
		if x.ok() {
			o := ex[site.ID+"/"+mat.ID]
			switch {
			case o != nil && o.qty >= 0:
				sameCost := !hasCost || (o.cost != nil && int64(*o.cost) == cost)
				if o.qty == qty && o.date == date && sameCost {
					x.it.Action = ActUnchanged
				} else {
					x.fail("quantity", "dataimport.conflict.opening_exists", "qty", formatMilli(o.qty), "date", dmy(o.date))
				}
			case o != nil && o.first != "" && o.first < date:
				x.fail("date", "dataimport.conflict.opening_after_first", "date", dmy(o.first))
			default:
				var costA, noteA any
				if hasCost {
					costA = int(cost)
				}
				if hasNote {
					noteA = note
				}
				x.it.Action = ActCreate
				mid, plid, q, rid, uid := mat.ID, site.ID, formatMilli(qty), e.reportID, e.userID
				x.it.apply = func(b *pgx.Batch) {
					b.Queue(`INSERT INTO "MaterialMovement" (id, "plantId", "materialId", date, type, quantity, "unitCost", "sourceType", "sourceId", note, "createdById")
						VALUES ($1,$2,$3,$4::date,'OPENING',$5::numeric,$6,'CsvImport',$7,$8,$9)`, db.NewID(), plid, mid, date, q, costA, rid, noteA, uid)
				}
			}
		}
		items = append(items, x.it)
	}
	return items, nil
}
