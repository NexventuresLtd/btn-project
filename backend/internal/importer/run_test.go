package importer_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"btn-ops/internal/auth"
	. "btn-ops/internal/importer"
	"btn-ops/internal/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

func samplesDir(t *testing.T) string {
	t.Helper()
	d := filepath.Join("..", "..", "..", "data", "samples")
	if _, err := os.Stat(d); err != nil {
		t.Skip("data/samples not present")
	}
	return d
}

func run(t *testing.T, o Options) (string, int) {
	t.Helper()
	var out bytes.Buffer
	code := Run(context.Background(), testutil.Pool(t), o, &out)
	return out.String(), code
}

func count(t *testing.T, sql string) int {
	t.Helper()
	var n int
	if err := testutil.Pool(t).QueryRow(context.Background(), sql).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestImportEndToEnd(t *testing.T) {
	dir := samplesDir(t)
	p := testutil.Reset(t)
	testutil.CreateUser(t, p, auth.Role("SYSTEM_ADMIN"))
	today := "2026-11-21"

	// dry run writes nothing, prints masked data only
	out, code := run(t, Options{DryRun: true, Dir: dir, Today: today})
	if code != 0 || !strings.Contains(out, "Dry run complete") || !strings.Contains(out, "match") {
		t.Fatalf("dry run: %d\n%s", code, out)
	}
	if strings.Contains(out, "1199") { // masked national ids
		t.Fatal("unmasked ID in report")
	}
	if n := count(t, `SELECT count(*) FROM "StockMovement"`); n != 0 {
		t.Fatalf("dry run wrote %d stock movements", n)
	}

	// real import: one transaction per file, reconciliation all matching
	out, code = run(t, Options{Dir: dir, Today: today})
	if code != 0 || strings.Contains(out, "FAILED") || strings.Contains(out, "MISMATCH") {
		t.Fatalf("import: %d\n%s", code, out)
	}
	stock := count(t, `SELECT count(*) FROM "StockMovement" WHERE "sourceType"='Import'`)
	if stock == 0 || count(t, `SELECT count(*) FROM "Sale" WHERE number LIKE 'IMP-SO-%'`) == 0 || count(t, `SELECT count(*) FROM "PettyCashEntry" WHERE status='APPROVED'`) != 55 ||
		count(t, `SELECT count(*) FROM "PayrollPeriod" WHERE status='PREPARED'`) != 3 || count(t, `SELECT count(*) FROM "AuditLog" WHERE action='import.excel'`) != 7 {
		t.Fatalf("unexpected counts\n%s", out)
	}
	// both legs of every transfer exist
	if n := count(t, `SELECT count(*) FROM (SELECT "transferGroup" FROM "MaterialMovement" WHERE "transferGroup" IS NOT NULL GROUP BY 1 HAVING count(*) <> 2) x`); n != 0 {
		t.Fatalf("%d transfer groups without two legs", n)
	}

	// idempotent: a second import refuses, writes nothing more
	out, code = run(t, Options{Dir: dir, Today: today})
	if code != 1 || !strings.Contains(out, "REFUSING") {
		t.Fatalf("second import must refuse: %d\n%s", code, out)
	}
	if n := count(t, `SELECT count(*) FROM "StockMovement"`); n != stock {
		t.Fatalf("stock movements changed %d -> %d", stock, n)
	}

	// --replace removes only imported rows and imports again with identical totals
	if _, err := p.Exec(context.Background(), `INSERT INTO "Customer" (id, name) VALUES ('keep-me','NOT IMPORTED CO')`); err != nil {
		t.Fatal(err)
	}
	out, code = run(t, Options{Dir: dir, Today: today, Replace: true})
	if code != 0 || strings.Contains(out, "FAILED") {
		t.Fatalf("replace: %d\n%s", code, out)
	}
	if n := count(t, `SELECT count(*) FROM "StockMovement"`); n != stock {
		t.Fatalf("after replace %d stock movements, want %d", n, stock)
	}
	if count(t, `SELECT count(*) FROM "Customer" WHERE id='keep-me'`) != 1 {
		t.Fatal("replace removed a non-imported customer")
	}
}

func TestImportRefusesUnknownFolder(t *testing.T) {
	testutil.Reset(t)
	out, code := run(t, Options{DryRun: true, Dir: filepath.Join(t.TempDir(), "nope")})
	if code != 1 || !strings.Contains(out, "IMPORT FAILED") {
		t.Fatalf("%d %s", code, out)
	}
}
