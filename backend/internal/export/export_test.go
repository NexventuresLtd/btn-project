package export

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/xuri/excelize/v2"
)

var testCols = []Column{
	{Key: "name", Header: "Name", Kind: Text},
	{Key: "qty", Header: "Qty", Kind: Int},
	{Key: "amount", Header: "Amount", Kind: Money},
	{Key: "date", Header: "Date", Kind: Date},
	{Key: "at", Header: "At", Kind: DateTime},
	{Key: "rate", Header: "Rate", Kind: Percent},
	{Key: "ok", Header: "OK", Kind: Bool},
}

var testRows = [][]any{
	{`Block "A", 20cm`, json.Number("1200"), 1380000, "2026-11-14", "2026-11-21T14:32:14.187Z", 0.125, true},
	{"line1\nline2", "15", json.Number("-500"), nil, nil, nil, false},
	{"=HYPERLINK(\"x\")", 0, 0, "2026-01-02", "", "0.5", nil},
}

func run(t *testing.T, format Format) (*fiber.App, []byte, string) {
	t.Helper()
	app := fiber.New()
	app.Get("/x", func(c *fiber.Ctx) error { return Send(c, format, "test-report", testCols, testRows) })
	resp, err := app.Test(httptest.NewRequest("GET", "/x?from=2026-11-01&to=2026-11-23&status=PAID", nil), -1)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	return app, b, resp.Header.Get("Content-Disposition") + "|" + resp.Header.Get("Content-Type")
}

func TestCSVEscapingBOMAndFormats(t *testing.T) {
	_, b, hdr := run(t, CSV)
	if !strings.Contains(hdr, `attachment; filename="test-report.csv"`) || !strings.Contains(hdr, "text/csv") {
		t.Fatalf("headers %q", hdr)
	}
	if !bytes.HasPrefix(b, []byte("\xEF\xBB\xBF")) {
		t.Fatal("missing UTF-8 BOM")
	}
	s := string(b[3:])
	want := "Name,Qty,Amount,Date,At,Rate,OK\r\n" +
		`"Block ""A"", 20cm",1200,1380000,14/11/2026,21/11/2026 16:32,12.5,Yes` + "\r\n" +
		"\"line1\r\nline2\",15,-500,,,,No\r\n" +
		`"'=HYPERLINK(""x"")",0,0,02/01/2026,,50,` + "\r\n"
	if s != want {
		t.Fatalf("csv mismatch:\n got %q\nwant %q", s, want)
	}
}

func TestXLSXTypedCells(t *testing.T) {
	_, b, hdr := run(t, XLSX)
	if !strings.Contains(hdr, "spreadsheetml") || !strings.Contains(hdr, "test-report.xlsx") {
		t.Fatalf("headers %q", hdr)
	}
	f, err := excelize.OpenReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, _ := f.GetRows(sheetName)
	// title, generated, period filter, status filter, blank, header
	if rows[0][0] != "test-report" || !strings.HasPrefix(rows[2][0], "Period: 01/11/2026 – 23/11/2026") || rows[3][0] != "Status: PAID" {
		t.Fatalf("header block %q", rows[:4])
	}
	hr := 6
	if v, _ := f.GetCellValue(sheetName, cellName(1, hr)); v != "Name" {
		t.Fatalf("header row %d = %q", hr, v)
	}
	typ := func(cell string) excelize.CellType { ct, _ := f.GetCellType(sheetName, cell); return ct }
	isNum := func(cell string) bool {
		ct := typ(cell)
		return ct == excelize.CellTypeNumber || ct == excelize.CellTypeUnset
	} // stream writer omits t="n"
	if !isNum("B7") || !isNum("C7") || !isNum("D7") || typ("A7") == excelize.CellTypeUnset {
		t.Fatalf("qty/amount not numeric: %v %v", typ("B7"), typ("C7"))
	}
	raw, _ := f.GetCellValue(sheetName, "C7", excelize.Options{RawCellValue: true})
	if raw != "1380000" {
		t.Fatalf("money raw %q", raw)
	}
	if v, _ := f.GetCellValue(sheetName, "C7"); v != "1,380,000 RWF" {
		t.Fatalf("money formatted %q", v)
	}
	if v, _ := f.GetCellValue(sheetName, "D7"); v != "14/11/2026" {
		t.Fatalf("date formatted %q", v)
	}
	if v, _ := f.GetCellValue(sheetName, "E7"); v != "21/11/2026 16:32" {
		t.Fatalf("datetime formatted %q", v)
	}
	if v, _ := f.GetCellValue(sheetName, "D7", excelize.Options{RawCellValue: true}); v != "46340" {
		t.Fatalf("date serial %q", v)
	}
	if v, _ := f.GetCellValue(sheetName, "A9"); v != `'=HYPERLINK("x")` {
		t.Fatalf("formula not neutralised: %q", v)
	}
	if fm, _ := f.GetCellFormula(sheetName, "A9"); fm != "" {
		t.Fatalf("formula cell %q", fm)
	}
	panes, err := f.GetPanes(sheetName)
	if err != nil || !panes.Freeze || panes.YSplit != hr {
		t.Fatalf("freeze pane %+v %v", panes, err)
	}
	if dn := f.GetDefinedName(); len(dn) == 0 || !strings.Contains(dn[0].RefersTo, "$A$6:$G$9") {
		t.Fatalf("autofilter %+v", dn)
	}
}

func TestJSONRows(t *testing.T) {
	_, b, _ := run(t, JSON)
	var out struct {
		Title   string           `json:"title"`
		Columns []map[string]any `json:"columns"`
		Rows    []map[string]any `json:"rows"`
		Filters []map[string]any `json:"filters"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) != 3 || out.Rows[0]["qty"] != float64(1200) || out.Rows[0]["date"] != "2026-11-14" || out.Rows[0]["at"] != "2026-11-21T16:32:14" {
		t.Fatalf("rows %+v", out.Rows[0])
	}
	if out.Columns[2]["type"] != "money" || len(out.Filters) != 2 {
		t.Fatalf("columns/filters %+v %+v", out.Columns, out.Filters)
	}
}

func TestParseFormatAndMatch(t *testing.T) {
	for in, want := range map[string]Format{"csv": CSV, "XLSX": XLSX, "excel": XLSX, "json": JSON} {
		if f, ok := ParseFormat(in); !ok || f != want {
			t.Fatalf("%s -> %s", in, f)
		}
	}
	if _, ok := ParseFormat("pdf"); ok {
		t.Fatal("pdf is not an export format")
	}
	Register(Report{Path: "/zz-test/:id/lines", Name: "zz", Build: func(*Builder, J) {}})
	if r, p := match("/zz-test/abc/lines"); r == nil || p["id"] != "abc" {
		t.Fatalf("match %v %v", r, p)
	}
	if r, _ := match("/zz-test//lines"); r != nil {
		t.Fatal("empty param matched")
	}
	_ = time.Now
}
