package export

import (
	"bytes"
	"time"
	"unicode/utf8"

	"btn-ops/internal/dates"
	"btn-ops/internal/i18n"

	"github.com/xuri/excelize/v2"
)

const sheetName = "Data"

// xlsx number formats (custom): dates DD/MM/YYYY per SRS NFR-L1, money as whole RWF.
const (
	fmtDate     = "dd/mm/yyyy"
	fmtDateTime = "dd/mm/yyyy hh:mm"
	fmtInt      = "#,##0"
	fmtMoney    = `#,##0 "RWF"`
	fmtNumber   = "#,##0.00"
	fmtPercent  = "0.0%"
)

func buildXLSX(m meta, headers []string, cols []Column, rows [][]any) ([]byte, error) {
	f := excelize.NewFile()
	defer f.Close()
	if err := f.SetSheetName("Sheet1", sheetName); err != nil {
		return nil, err
	}
	f.SetDocProps(&excelize.DocProperties{Title: m.title, Creator: firstNonEmpty(m.generatedBy, "BTN Operations"), Created: m.generatedAt.UTC().Format(time.RFC3339)})
	// header block: title, generated at/by, filters, blank line; then the column headers and one row per record
	headerRow := 3 + len(m.filters) + 1
	if len(cols) > 0 {
		// Set the auto-filter before streaming: the stream writer emits it with the sheet. Adding it after
		// Flush made excelize re-read and re-encode the whole streamed sheet (~1.8 s of a 15k-row export).
		if err := f.AutoFilter(sheetName, cellName(1, headerRow)+":"+cellName(len(cols), headerRow+len(rows)), nil); err != nil {
			return nil, err
		}
	}
	sw, err := f.NewStreamWriter(sheetName)
	if err != nil {
		return nil, err
	}
	style := func(s *excelize.Style) int {
		id, _ := f.NewStyle(s)
		return id
	}
	custom := func(code string) int { c := code; return style(&excelize.Style{CustomNumFmt: &c}) }
	titleSt := style(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 14, Color: "0C234B"}})
	metaSt := style(&excelize.Style{Font: &excelize.Font{Size: 9, Color: "475569"}})
	headSt := style(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"0C234B"}},
		Alignment: &excelize.Alignment{Vertical: "center", WrapText: true},
		Border:    []excelize.Border{{Type: "bottom", Color: "F59E0B", Style: 2}},
	})
	kindSt := map[Kind]int{
		Int: custom(fmtInt), Money: custom(fmtMoney), Number: custom(fmtNumber), Qty: custom(fmtNumber),
		Percent: custom(fmtPercent), Date: custom(fmtDate), DateTime: custom(fmtDateTime),
	}

	// widths: header length vs. kind defaults vs. a sample of the data
	for i, col := range cols {
		w := col.Width
		if w == 0 {
			w = float64(utf8.RuneCountInString(headers[i])) + 2
			switch col.Kind {
			case Date:
				w = max(w, 12)
			case DateTime:
				w = max(w, 17)
			case Money:
				w = max(w, 16)
			case Int, Number, Qty, Percent:
				w = max(w, 11)
			default:
				for j := 0; j < len(rows) && j < 200; j++ {
					if i < len(rows[j]) {
						w = max(w, float64(utf8.RuneCountInString(str(rows[j][i])))+2)
					}
				}
			}
			w = min(max(w, 8), 60)
		}
		if err := sw.SetColWidth(i+1, i+1, w); err != nil {
			return nil, err
		}
	}

	row := 1
	put := func(vals []any, opts ...excelize.RowOpts) error {
		cell, _ := excelize.CoordinatesToCellName(1, row)
		row++
		return sw.SetRow(cell, vals, opts...)
	}
	if len(cols) > 0 {
		if err := sw.SetPanes(&excelize.Panes{Freeze: true, YSplit: headerRow, TopLeftCell: cellName(1, headerRow+1), ActivePane: "bottomLeft"}); err != nil {
			return nil, err
		}
	}
	if err := put([]any{excelize.Cell{Value: m.title, StyleID: titleSt}}, excelize.RowOpts{Height: 22}); err != nil {
		return nil, err
	}
	gen := i18n.T(m.lang, "export.generated", "date", m.generatedAt.In(dates.Kigali()).Format("02/01/2006 15:04"), "user", firstNonEmpty(m.generatedBy, "—"))
	if err := put([]any{excelize.Cell{Value: gen, StyleID: metaSt}}); err != nil {
		return nil, err
	}
	for _, fl := range m.filters {
		if err := put([]any{excelize.Cell{Value: fl.Label + ": " + fl.Value, StyleID: metaSt}}); err != nil {
			return nil, err
		}
	}
	row = headerRow // one blank row after the filters block
	hv := make([]any, len(headers))
	for i, h := range headers {
		hv[i] = excelize.Cell{Value: h, StyleID: headSt}
	}
	if err := put(hv, excelize.RowOpts{Height: 20}); err != nil {
		return nil, err
	}
	vals := make([]any, len(cols))
	for _, r := range rows {
		for i, col := range cols {
			var v any
			if i < len(r) {
				v = r[i]
			}
			t := typed(col.Kind, v)
			if s, ok := t.(string); ok {
				t = safeText(s)
			}
			if b, ok := t.(bool); ok {
				if b {
					t = i18n.T(m.lang, "export.yes")
				} else {
					t = i18n.T(m.lang, "export.no")
				}
			}
			if st, ok := kindSt[col.Kind]; ok && t != nil {
				if _, isStr := t.(string); !isStr {
					vals[i] = excelize.Cell{Value: t, StyleID: st}
					continue
				}
			}
			vals[i] = t
		}
		if err := put(vals); err != nil {
			return nil, err
		}
	}
	if err := sw.Flush(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func cellName(col, row int) string {
	s, _ := excelize.CoordinatesToCellName(col, row)
	return s
}
