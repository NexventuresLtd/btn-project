package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"btn-ops/internal/audit"
	"btn-ops/internal/db"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Refs resolves catalogue codes to ids.
type Refs struct {
	AdminID   string
	Plants    map[string]string
	Products  map[string]string
	Materials map[string]string
}

const (
	ImportSource   = "Import"
	ManifestAction = "import.excel"
)

// Manifest is what each file's transaction records in the audit log (and what --replace relies on).
type Manifest struct {
	File           string         `json:"file"`
	Customers      []string       `json:"customers"`
	Workers        []string       `json:"workers"`
	PayrollPeriods []string       `json:"payrollPeriods"`
	Counts         map[string]int `json:"counts"`
	order          []KV           // counts in insertion order, for the summary line
}

func (m *Manifest) count(k string, v int) {
	m.Counts[k] = v
	m.order = append(m.order, KV{k, v})
}

// Ordered returns the counts in insertion order.
func (m *Manifest) Ordered() []KV { return m.order }

func ref(m map[string]string, code, what string) (string, error) {
	id, ok := m[code]
	if !ok {
		return "", fmt.Errorf("%s %s is not in the database — run `make seed` first", what, code)
	}
	return id, nil
}

// LoadRefs reads plants, products, materials and the importing user (first active SYSTEM_ADMIN,
// else SUPER_ADMIN) in a few set-based queries.
func LoadRefs(ctx context.Context, q db.DBTX) (*Refs, error) {
	refs := &Refs{Plants: map[string]string{}, Products: map[string]string{}, Materials: map[string]string{}}
	for _, t := range []struct {
		table string
		dst   map[string]string
	}{{"Plant", refs.Plants}, {"Product", refs.Products}, {"Material", refs.Materials}} {
		rows, err := q.Query(ctx, `SELECT code, id FROM "`+t.table+`"`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var code, id string
			if err := rows.Scan(&code, &id); err != nil {
				rows.Close()
				return nil, err
			}
			t.dst[code] = id
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	err := q.QueryRow(ctx, `SELECT id FROM "User" WHERE "isActive" AND role IN ('SYSTEM_ADMIN','SUPER_ADMIN')
		ORDER BY (role = 'SYSTEM_ADMIN') DESC, "createdAt" ASC LIMIT 1`).Scan(&refs.AdminID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("No active SYSTEM_ADMIN user — run `make seed` first")
	}
	return refs, err
}

// ───────────────────────────── detection & replace ─────────────────────────────

// Manifests returns every manifest of earlier imports.
func Manifests(ctx context.Context, q db.DBTX) ([]Manifest, error) {
	rows, err := q.Query(ctx, `SELECT "after" FROM "AuditLog" WHERE action = $1 AND "after" IS NOT NULL`, ManifestAction)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Manifest
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var m struct {
			File           string    `json:"file"`
			Customers      *[]string `json:"customers"`
			Workers        []string  `json:"workers"`
			PayrollPeriods []string  `json:"payrollPeriods"`
		}
		if json.Unmarshal(raw, &m) != nil || m.Customers == nil {
			continue
		}
		out = append(out, Manifest{File: m.File, Customers: *m.Customers, Workers: m.Workers, PayrollPeriods: m.PayrollPeriods})
	}
	return out, rows.Err()
}

func flat(ms []Manifest, pick func(Manifest) []string) []string {
	out := []string{}
	for _, m := range ms {
		out = append(out, pick(m)...)
	}
	return out
}

// Existing counts what a previous import left behind.
type Existing struct {
	Stock, Material, Sales, Petty, Periods int
}

func (e Existing) Any() bool { return e.Stock+e.Material+e.Sales+e.Petty+e.Periods > 0 }

func (e Existing) String() string {
	return fmt.Sprintf("%d stock movements, %d material movements, %d sales, %d petty-cash entries, %d payroll periods", e.Stock, e.Material, e.Sales, e.Petty, e.Periods)
}

func ExistingImport(ctx context.Context, q db.DBTX) (Existing, error) {
	ms, err := Manifests(ctx, q)
	if err != nil {
		return Existing{}, err
	}
	var e Existing
	err = q.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM "StockMovement" WHERE "sourceType" = $1),
		(SELECT count(*) FROM "MaterialMovement" WHERE "sourceType" = $1),
		(SELECT count(*) FROM "Sale" WHERE number LIKE 'IMP-SO-%'),
		(SELECT count(*) FROM "PettyCashEntry" WHERE number LIKE 'IMP-PC-%' OR "sourceType" = $1),
		(SELECT count(*) FROM "PayrollPeriod" WHERE id = ANY($2::text[]))`,
		ImportSource, flat(ms, func(m Manifest) []string { return m.PayrollPeriods })).
		Scan(&e.Stock, &e.Material, &e.Sales, &e.Petty, &e.Periods)
	return e, err
}

func execCount(ctx context.Context, tx pgx.Tx, sql string, args ...any) (int, error) {
	tag, err := tx.Exec(ctx, sql, args...)
	return int(tag.RowsAffected()), err
}

// DeleteImported removes everything a previous import wrote — and nothing else — in FK-safe order,
// in one transaction with an audit entry. It refuses if users have since built on imported records.
func DeleteImported(ctx context.Context, pool *pgxpool.Pool, adminID string) ([]KV, error) {
	var out []KV
	err := db.Tx(ctx, pool, func(tx pgx.Tx) error {
		ms, err := Manifests(ctx, tx)
		if err != nil {
			return err
		}
		periodIDs := flat(ms, func(m Manifest) []string { return m.PayrollPeriods })
		saleIDs, err := idList(ctx, tx, `SELECT id FROM "Sale" WHERE number LIKE 'IMP-SO-%'`)
		if err != nil {
			return err
		}
		delIDs, err := idList(ctx, tx, `SELECT id FROM "Delivery" WHERE "saleId" = ANY($1::text[])`, saleIDs)
		if err != nil {
			return err
		}
		var posted, foreign, advanced, paidViaPetty int
		err = tx.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM "StockMovement" WHERE "sourceType" = 'Delivery' AND "sourceId" = ANY($1::text[])),
			(SELECT count(*) FROM "Payment" WHERE "saleId" = ANY($2::text[]) AND reference IS NOT NULL AND reference NOT LIKE 'IMP%'),
			(SELECT count(*) FROM "PayrollPeriod" WHERE id = ANY($3::text[]) AND status NOT IN ('DRAFT','PREPARED')),
			(SELECT count(*) FROM "PettyCashEntry" WHERE "sourceType" = 'PayrollPeriod' AND "sourceId" = ANY($3::text[]))`,
			delIDs, saleIDs, periodIDs).Scan(&posted, &foreign, &advanced, &paidViaPetty)
		if err != nil {
			return err
		}
		switch {
		case posted > 0:
			return fmt.Errorf("%d stock movement(s) were posted by deliveries created in the app against imported sales — resolve those first", posted)
		case foreign > 0:
			return fmt.Errorf("%d payment(s) were recorded in the app against imported sales — resolve those first", foreign)
		case advanced > 0:
			return fmt.Errorf("%d imported payroll period(s) have moved past PREPARED — they can no longer be replaced automatically", advanced)
		case paidViaPetty > 0:
			return fmt.Errorf("%d petty-cash entr(y/ies) reference imported payroll periods — resolve those first", paidViaPetty)
		}
		steps := []struct {
			name, sql string
			args      []any
		}{
			{"deliveries", `DELETE FROM "Delivery" WHERE id = ANY($1::text[])`, []any{delIDs}}, // lines cascade
			{"payments", `DELETE FROM "Payment" WHERE "saleId" = ANY($1::text[])`, []any{saleIDs}},
			{"sales", `DELETE FROM "Sale" WHERE id = ANY($1::text[])`, []any{saleIDs}}, // lines cascade
			{"stockMovements", `DELETE FROM "StockMovement" WHERE "sourceType" = $1`, []any{ImportSource}},
			{"materialMovements", `DELETE FROM "MaterialMovement" WHERE "sourceType" = $1`, []any{ImportSource}},
			{"pettyCash", `DELETE FROM "PettyCashEntry" WHERE number LIKE 'IMP-PC-%' OR "sourceType" = $1`, []any{ImportSource}},
			{"payrollPeriods", `DELETE FROM "PayrollPeriod" WHERE id = ANY($1::text[])`, []any{periodIDs}}, // lines cascade
			{"workers", `DELETE FROM "Worker" w WHERE id = ANY($1::text[]) AND NOT EXISTS (SELECT 1 FROM "PayrollLine" l WHERE l."workerId" = w.id)`, []any{flat(ms, func(m Manifest) []string { return m.Workers })}},
			{"customers", `DELETE FROM "Customer" c WHERE id = ANY($1::text[]) AND NOT EXISTS (SELECT 1 FROM "Sale" s WHERE s."customerId" = c.id)`, []any{flat(ms, func(m Manifest) []string { return m.Customers })}},
		}
		summary := map[string]int{}
		out = nil
		for _, s := range steps {
			n, err := execCount(ctx, tx, s.sql, s.args...)
			if err != nil {
				return err
			}
			summary[s.name] = n
			out = append(out, KV{s.name, n})
		}
		b, _ := json.Marshal(summary)
		return audit.Log(ctx, tx, audit.Actor{UserID: adminID}, audit.Input{
			Action: "import.replace", Entity: "Import", Summary: "Removed previously imported Excel data: " + string(b), After: summary,
		})
	})
	return out, err
}

func idList(ctx context.Context, q db.DBTX, sql string, args ...any) ([]string, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ExistingDNs returns the delivery numbers in use; with ignoreImported the ones of imported sales are
// left out (they are about to be replaced, or this is a dry run).
func ExistingDNs(ctx context.Context, q db.DBTX, ignoreImported bool) (map[string]bool, error) {
	sql := `SELECT "dnNumber" FROM "Delivery"`
	if ignoreImported {
		sql = `SELECT d."dnNumber" FROM "Delivery" d JOIN "Sale" s ON s.id = d."saleId" WHERE s.number NOT LIKE 'IMP-SO-%'`
	}
	ids, err := idList(ctx, q, sql)
	m := make(map[string]bool, len(ids))
	for _, d := range ids {
		m[d] = true
	}
	return m, err
}

// ───────────────────────────── writes ─────────────────────────────

func ns(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func send(ctx context.Context, tx pgx.Tx, b *pgx.Batch) error {
	if b.Len() == 0 {
		return nil
	}
	br := tx.SendBatch(ctx, b)
	for i := 0; i < b.Len(); i++ {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return err
		}
	}
	return br.Close()
}

// WriteFilePlan writes one file in ONE transaction (set-based batches, client-generated ids) and
// records the manifest in the audit log inside the same transaction.
func WriteFilePlan(ctx context.Context, pool *pgxpool.Pool, plan *FilePlan, refs *Refs) (*Manifest, error) {
	m := &Manifest{File: plan.File, Customers: []string{}, Workers: []string{}, PayrollPeriods: []string{}, Counts: map[string]int{}}
	err := db.Tx(ctx, pool, func(tx pgx.Tx) error {
		by := refs.AdminID
		if len(plan.Stock) > 0 {
			b := &pgx.Batch{}
			for _, s := range plan.Stock {
				plant, err := ref(refs.Plants, s.PlantCode, "Plant")
				if err != nil {
					return err
				}
				prod, err := ref(refs.Products, s.ProductCode, "Product")
				if err != nil {
					return err
				}
				b.Queue(`INSERT INTO "StockMovement" (id, "plantId", "productId", date, type, quantity, "sourceType", note, "createdById")
					VALUES ($1,$2,$3,$4::date,$5::text::"StockMovementType",$6,$7,$8,$9)`, db.NewID(), plant, prod, s.Date, s.Type, s.Quantity, ImportSource, s.Note, by)
			}
			if err := send(ctx, tx, b); err != nil {
				return err
			}
			m.count("stockMovements", len(plan.Stock))
		}

		if len(plan.Material) > 0 {
			b := &pgx.Batch{}
			for _, mv := range plan.Material {
				plant, err := ref(refs.Plants, mv.PlantCode, "Plant")
				if err != nil {
					return err
				}
				mat, err := ref(refs.Materials, mv.MaterialCode, "Material")
				if err != nil {
					return err
				}
				var counter any
				if mv.CounterpartPlantCode != "" {
					id, err := ref(refs.Plants, mv.CounterpartPlantCode, "Plant")
					if err != nil {
						return err
					}
					counter = id
				}
				b.Queue(`INSERT INTO "MaterialMovement" (id, "plantId", "materialId", date, type, quantity, "counterpartPlantId", "transferGroup", "sourceType", note, "createdById")
					VALUES ($1,$2,$3,$4::date,$5::text::"MaterialMovementType",$6,$7,$8,$9,$10,$11)`,
					db.NewID(), plant, mat, mv.Date, mv.Type, mv.Quantity, counter, ns(mv.TransferGroup), ImportSource, mv.Note, by)
			}
			if err := send(ctx, tx, b); err != nil {
				return err
			}
			m.count("materialMovements", len(plan.Material))
		}

		if len(plan.Sales) > 0 {
			if err := writeSales(ctx, tx, plan, refs, m); err != nil {
				return err
			}
		}

		if len(plan.PettyCash) > 0 {
			b := &pgx.Batch{}
			now := db.Now()
			for _, e := range plan.PettyCash {
				var plant any
				if e.PlantCode != "" {
					id, err := ref(refs.Plants, e.PlantCode, "Plant")
					if err != nil {
						return err
					}
					plant = id
				}
				b.Queue(`INSERT INTO "PettyCashEntry" (id, number, date, type, category, description, "plantId", "payeeName", "momoNumber", amount, "momoCharge", "invoiceRef",
						status, "sourceType", "createdById", "reviewedById", "reviewedAt", "reviewComment")
					VALUES ($1,$2,$3::date,$4::text::"PettyCashType",$5::text::"PettyCashCategory",$6,$7,$8,$9,$10,$11,$12,'APPROVED',$13,$14,$14,$15,'Imported from Excel')`,
					db.NewID(), e.Number, e.Date, e.Type, e.Category, e.Description, plant, ns(e.PayeeName), ns(e.MomoNumber), e.Amount, e.MomoCharge, ns(e.InvoiceRef), ImportSource, by, now)
			}
			if err := send(ctx, tx, b); err != nil {
				return err
			}
			m.count("pettyCashEntries", len(plan.PettyCash))
		}

		if len(plan.Payroll) > 0 {
			if err := writePayroll(ctx, tx, plan, refs, m); err != nil {
				return err
			}
		}

		parts := make([]string, 0, len(m.order))
		for _, kv := range m.order {
			parts = append(parts, fmt.Sprintf("%d %s", kv.V, kv.K))
		}
		return audit.Log(ctx, tx, audit.Actor{UserID: refs.AdminID}, audit.Input{
			Action: ManifestAction, Entity: "Import", Summary: fmt.Sprintf("Imported %s: %s", plan.File, strings.Join(parts, ", ")), After: m,
		})
	})
	if err != nil {
		return nil, err
	}
	return m, nil
}

func writeSales(ctx context.Context, tx pgx.Tx, plan *FilePlan, refs *Refs, m *Manifest) error {
	// customers: one lookup, then the missing ones in a batch
	var names []string
	seen := map[string]bool{}
	for _, s := range plan.Sales {
		if !seen[s.CustomerName] {
			seen[s.CustomerName] = true
			names = append(names, s.CustomerName)
		}
	}
	lower := make([]string, len(names))
	for i, n := range names {
		lower[i] = strings.ToLower(n)
	}
	customerIDs := map[string]string{}
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (lower(name)) lower(name), id FROM "Customer" WHERE lower(name) = ANY($1::text[]) ORDER BY lower(name), "createdAt"`, lower)
	if err != nil {
		return err
	}
	found := map[string]string{}
	for rows.Next() {
		var l, id string
		if err := rows.Scan(&l, &id); err != nil {
			rows.Close()
			return err
		}
		found[l] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	cb := &pgx.Batch{}
	for _, n := range names {
		if id, ok := found[strings.ToLower(n)]; ok {
			customerIDs[n] = id
			continue
		}
		id := db.NewID()
		customerIDs[n] = id
		m.Customers = append(m.Customers, id)
		cb.Queue(`INSERT INTO "Customer" (id, name) VALUES ($1,$2)`, id, n)
	}
	if err := send(ctx, tx, cb); err != nil {
		return err
	}

	b := &pgx.Batch{}
	now := db.Now()
	lines, deliveries, payments := 0, 0, 0
	for _, s := range plan.Sales {
		plant, err := ref(refs.Plants, s.PlantCode, "Plant")
		if err != nil {
			return err
		}
		saleID := db.NewID()
		b.Queue(`INSERT INTO "Sale" (id, number, "plantId", "customerId", date, "invoiceType", "ebmInvoiceNo", "ebmReceiptNo", status, notes, "createdById", "confirmedById", "confirmedAt")
			VALUES ($1,$2,$3,$4,$5::date,$6,$7,$8,'CONFIRMED',$9,$10,$10,$11)`,
			saleID, s.Number, plant, customerIDs[s.CustomerName], s.Date, s.InvoiceType, ns(s.EbmInvoiceNo), ns(s.EbmReceiptNo), s.Notes, refs.AdminID, now)
		lineIDs := make([]string, len(s.Lines))
		for i, l := range s.Lines {
			prod, err := ref(refs.Products, l.ProductCode, "Product")
			if err != nil {
				return err
			}
			lineIDs[i] = db.NewID()
			b.Queue(`INSERT INTO "SaleLine" (id, "saleId", "productId", quantity, "unitPrice", amount) VALUES ($1,$2,$3,$4,$5,$6)`, lineIDs[i], saleID, prod, l.Quantity, l.UnitPrice, l.Amount)
			lines++
		}
		// Historical deliveries: no stock movements (the STOCK sheet already carries Issue/Sold).
		for _, dl := range s.Deliveries {
			delID := db.NewID()
			b.Queue(`INSERT INTO "Delivery" (id, "dnNumber", "saleId", "plantId", date, status, "deliveredAt", "createdById")
				VALUES ($1,$2,$3,$4,$5::date,'DELIVERED',$5::date,$6)`, delID, dl.DNNumber, saleID, plant, s.Date, refs.AdminID)
			for _, i := range dl.LineIndexes {
				prod, _ := ref(refs.Products, s.Lines[i].ProductCode, "Product")
				b.Queue(`INSERT INTO "DeliveryLine" (id, "deliveryId", "saleLineId", "productId", quantity) VALUES ($1,$2,$3,$4,$5)`, db.NewID(), delID, lineIDs[i], prod, s.Lines[i].Quantity)
			}
			deliveries++
		}
		if s.Payment != nil {
			b.Queue(`INSERT INTO "Payment" (id, "saleId", date, amount, method, reference, "receivedById")
				VALUES ($1,$2,$3::date,$4,$5::text::"PaymentMethod",$6,$7)`, db.NewID(), saleID, s.Date, s.Payment.Amount, s.Payment.Method, s.Payment.Reference, refs.AdminID)
			payments++
		}
	}
	if err := send(ctx, tx, b); err != nil {
		return err
	}
	m.count("sales", len(plan.Sales))
	m.count("saleLines", lines)
	m.count("deliveries", deliveries)
	m.count("payments", payments)
	m.count("customersCreated", len(m.Customers))
	return nil
}

func writePayroll(ctx context.Context, tx pgx.Tx, plan *FilePlan, refs *Refs, m *Manifest) error {
	var nids []string
	for _, w := range plan.Workers {
		nids = append(nids, w.NationalID)
	}
	existing := map[string]string{}
	rows, err := tx.Query(ctx, `SELECT "nationalId", id FROM "Worker" WHERE "nationalId" = ANY($1::text[])`, nids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var nid, id string
		if err := rows.Scan(&nid, &id); err != nil {
			rows.Close()
			return err
		}
		existing[nid] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	workerIDs := map[string]string{}
	wb := &pgx.Batch{}
	for _, w := range plan.Workers {
		if id, ok := existing[w.NationalID]; ok {
			workerIDs[w.NationalID] = id
			continue
		}
		plant, err := ref(refs.Plants, w.PlantCode, "Plant")
		if err != nil {
			return err
		}
		id := db.NewID()
		workerIDs[w.NationalID] = id
		m.Workers = append(m.Workers, id)
		wb.Queue(`INSERT INTO "Worker" (id, "firstName", "lastName", "nationalId", "momoNumber", "momoName", "jobTitle", "plantId", "dailyWage")
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, w.FirstName, w.LastName, w.NationalID, ns(w.MomoNumber), ns(w.MomoName), w.JobTitle, plant, w.DailyWage)
	}
	if err := send(ctx, tx, wb); err != nil {
		return err
	}
	pb := &pgx.Batch{}
	total := 0
	for _, p := range plan.Payroll {
		plant, err := ref(refs.Plants, p.PlantCode, "Plant")
		if err != nil {
			return err
		}
		pid := db.NewID()
		m.PayrollPeriods = append(m.PayrollPeriods, pid)
		pb.Queue(`INSERT INTO "PayrollPeriod" (id, number, "plantId", "startDate", "endDate", status, "preparedById")
			VALUES ($1,$2,$3,$4::date,$5::date,'PREPARED',NULL)`, pid, p.Number, plant, p.StartDate, p.EndDate)
		for _, l := range p.Lines {
			pb.Queue(`INSERT INTO "PayrollLine" (id, "periodId", "workerId", "dailyWage", "daysWorked", "grossAmount", "foodDeduction", "netAmount", note)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, db.NewID(), pid, workerIDs[l.NationalID], l.DailyWage, l.DaysWorked, l.GrossAmount, l.FoodDeduction, l.NetAmount,
				fmt.Sprintf("Imported from %s / row %d", plan.File, l.Row))
		}
		total += len(p.Lines)
	}
	if err := send(ctx, tx, pb); err != nil {
		return err
	}
	m.count("workersCreated", len(m.Workers))
	m.count("workersReused", len(plan.Workers)-len(m.Workers))
	m.count("payrollPeriods", len(plan.Payroll))
	m.count("payrollLines", total)
	return nil
}

// Balance is a ledger total up to a date: rows written by the importer, and all rows.
type Balance struct{ Imported, All float64 }

// ClosingKey identifies a closing check by index for DBBalances.
// DBBalances returns the ledger balances for every closing in ONE set-based query.
func DBBalances(ctx context.Context, q db.DBTX, kind string, refs *Refs, closings []ClosingCheck) ([]Balance, error) {
	table, itemCol, items := `"StockMovement"`, `"productId"`, refs.Products
	if kind == "material" {
		table, itemCol, items = `"MaterialMovement"`, `"materialId"`, refs.Materials
	}
	plants, itemIDs, dates := make([]string, len(closings)), make([]string, len(closings)), make([]string, len(closings))
	for i, c := range closings {
		plants[i], itemIDs[i], dates[i] = refs.Plants[c.PlantCode], items[c.ItemCode], c.LastDate
	}
	rows, err := q.Query(ctx, `SELECT c.i::int,
			COALESCE(SUM(m.quantity) FILTER (WHERE m."sourceType" = $4), 0)::float8,
			COALESCE(SUM(m.quantity), 0)::float8
		FROM unnest($1::text[], $2::text[], $3::date[]) WITH ORDINALITY AS c(plant, item, upto, i)
		LEFT JOIN `+table+` m ON m."plantId" = c.plant AND m.`+itemCol+` = c.item AND m.date <= c.upto
		GROUP BY c.i`, plants, itemIDs, dates, ImportSource)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Balance, len(closings))
	for rows.Next() {
		var i int
		var imp, all float64
		if err := rows.Scan(&i, &imp, &all); err != nil {
			return nil, err
		}
		out[i-1] = Balance{Round3(imp), Round3(all)}
	}
	return out, rows.Err()
}

// ClashingPeriod reports whether a payroll period (by number, or plant+dates) exists and its id.
func ClashingPeriod(ctx context.Context, q db.DBTX, p PayrollPlan, plantID string) (string, bool, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT id FROM "PayrollPeriod" WHERE number = $1 OR ("plantId" = $2 AND "startDate" = $3::date AND "endDate" = $4::date) LIMIT 1`,
		p.Number, plantID, p.StartDate, p.EndDate).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}
