package dataimport

import "testing"

func TestValueParsers(t *testing.T) {
	for in, want := range map[string]string{"1500": "1500", "1,500": "1500", "1 500": "1500", "RWF 1,500": "1500", "12,5": "12.5",
		"1.234,5": "1234.5", "1,234.5": "1234.5", "+3": "3", "-2.5": "-2.5"} {
		if got, ok := cleanNumber(in); !ok || got != want {
			t.Errorf("cleanNumber(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"abc", "1.2.3", "", "1e5", "12a"} {
		if _, ok := cleanNumber(bad); ok {
			t.Errorf("cleanNumber(%q) should fail", bad)
		}
	}
	if v, ok, p := parseMilli("45,5"); !ok || p || v != 45500 {
		t.Errorf("parseMilli 45,5 = %d %v %v", v, ok, p)
	}
	if _, ok, p := parseMilli("1.2345"); !ok || !p {
		t.Error("parseMilli should flag 4 decimals")
	}
	if formatMilli(45500) != "45.5" || formatMilli(3000) != "3" || formatMilli(1) != "0.001" {
		t.Error("formatMilli")
	}
	if n, ok, frac := parseInt("2.0"); !ok || frac || n != 2 {
		t.Error("parseInt 2.0")
	}
	if _, _, frac := parseInt("2.5"); !frac {
		t.Error("parseInt 2.5 should be fractional")
	}
	for in, want := range map[string]string{"31/01/2026": "2026-01-31", "2026-01-31": "2026-01-31", "1/2/2026": "2026-02-01", "31.01.2026": "2026-01-31",
		"2026-01-31 00:00:00": "2026-01-31"} {
		if got, ok := parseDate(in); !ok || got != want {
			t.Errorf("parseDate(%q) = %q", in, got)
		}
	}
	for _, bad := range []string{"31/02/2026", "01/13/2026", "2026/31/01", "yesterday", "01/01/1890"} {
		if _, ok := parseDate(bad); ok {
			t.Errorf("parseDate(%q) should fail", bad)
		}
	}
	if detectDelimiter("a;b;c\n1,5;2;3") != ';' || detectDelimiter("a,b\n") != ',' || detectDelimiter("\"x;y\",b,c") != ',' || detectDelimiter("a\tb\tc") != '\t' {
		t.Error("detectDelimiter")
	}
	if b, ok := parseBool("Oui"); !ok || !b {
		t.Error("parseBool oui")
	}
	if b, ok := parseBool("oya"); !ok || b {
		t.Error("parseBool oya")
	}
}

func TestParseCSVHeadersAndEncoding(t *testing.T) {
	k := kindByName("opening-stock")
	// Windows-1252 file (é = 0xE9), header aliases, extra column, BOM-less
	raw := []byte("Site Code;Product;Qty;As of date;Note;Extra\r\nBTN-1;HB20;10;01/01/2026;Caf\xe9;x\r\n")
	tb, err := parseCSV(raw, k)
	if err != nil {
		t.Fatal(err)
	}
	if !tb.latin1 || len(tb.unknown) != 1 || tb.unknown[0] != "Extra" || len(tb.records) != 1 || tb.records[0].line != 2 {
		t.Fatalf("table: %+v", tb)
	}
	if v, _ := tb.cell(tb.records[0], "note"); v != "Café" {
		t.Fatalf("note %q", v)
	}
	if v, _ := tb.cell(tb.records[0], "quantity"); v != "10" {
		t.Fatalf("qty %q", v)
	}
	if _, err := parseCSV([]byte("site,product,quantity,date\n"+string(make([]byte, 0))), k); err == nil {
		t.Fatal("no rows should fail")
	}
	big := make([]byte, MaxBytes+1)
	if _, err := parseCSV(big, k); err == nil {
		t.Fatal("too large should fail")
	}
}
