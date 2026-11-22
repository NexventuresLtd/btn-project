package format

import "testing"

func TestFormats(t *testing.T) {
	if Int(2916000) != "2,916,000" || Int(999.6) != "1,000" || Int(-1234567) != "-1,234,567" || Int(0) != "0" {
		t.Fatal("int")
	}
	if RWF(1500) != "1,500 RWF" {
		t.Fatal("rwf")
	}
	if Qty(1234.5) != "1,234.5" || Qty(10) != "10" || Qty(0.126) != "0.13" {
		t.Fatal(Qty(1234.5), Qty(10), Qty(0.126))
	}
	r := 0.1234
	if Percent(&r) != "12.3%" || Percent(nil) != "—" {
		t.Fatal("percent")
	}
	if Ratio(1, 0) != nil || *Ratio(1, 4) != 0.25 {
		t.Fatal("ratio")
	}
}
