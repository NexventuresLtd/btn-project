// Package pdf is the shared report toolkit (port of legacy lib/pdf/components.tsx) built on go-pdf/fpdf with
// embedded Noto Sans + Noto Sans Ethiopic, so English, Amharic and Kinyarwanda reports render correctly.
//
//	rep, _ := pdf.New(c, d, pdf.Options{Title: "Daily Block Report", Subtitle: "BTN 1 · Day"})
//	rep.Section("Production")
//	rep.Table([]pdf.Column{{Header: "Product", Width: 3}, {Header: "Qty", Align: pdf.Right}}, rows, totals)
//	return pdf.Send(c, rep, "BTN-1_Daily-Block-Report_2026-11-14.pdf", c.Query("download") == "1")
package pdf

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"regexp"
	"strings"
	"time"

	"btn-ops/assets"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/settings"

	"github.com/go-pdf/fpdf"
	"github.com/gofiber/fiber/v2"
	"golang.org/x/image/webp"
)

// Layout constants in points (same as the Node report).
const (
	padTop    = 28.0
	padBottom = 54.0
	padX      = 32.0
	bodySize  = 8.5
)

// Align of a table column.
type Align string

const (
	Left   Align = "L"
	Right  Align = "R"
	Center Align = "C"
)

// Tone of a status badge.
type Tone string

const (
	Green Tone = "green"
	Red   Tone = "red"
	Amber Tone = "amber"
	Navy  Tone = "navy"
)

// Meta is one "label: value" row of the meta box.
type Meta struct{ Label, Value string }

// Options configure a report. Company/Logo/Colors default sensibly; New() fills them from settings.
type Options struct {
	Title       string
	Subtitle    string
	DocumentNo  string
	Meta        []Meta
	Company     settings.Company
	GeneratedBy string
	GeneratedAt time.Time
	Landscape   bool
	Lang        i18n.Lang
	LogoPNG     []byte // light-background logo (PNG or JPEG bytes); default embedded logo when empty
	Primary     string // "#0C234B"
	Accent      string // "#F59E0B"
}

// Column describes a table column; Width is a flex weight (default 1).
type Column struct {
	Header string
	Width  float64
	Align  Align
}

// Cell is a rich table cell. Plain strings / numbers may be used instead.
type Cell struct {
	Text  string
	Bold  bool
	Badge Tone // when set, renders a status badge
	Color string
}

// B builds a badge cell.
func B(label string, tone Tone) Cell { return Cell{Text: label, Badge: tone} }

// Kpi is one tile of a KpiRow.
type Kpi struct{ Label, Value, Hint string }

// Signatory is one signature slot. Verb defaults to "Approved" (localized).
type Signatory struct {
	Role string
	Name string
	At   *time.Time
	Verb string
}

// Report is a document under construction.
type Report struct {
	o        Options
	p        *fpdf.Fpdf
	ts       typesetter
	y        float64
	pageW    float64
	pageH    float64
	contentW float64
	primary  rgb
	accent   rgb
	soft     rgb
	title    string
	footer   string
	logoName string
	logoW    float64
	logoH    float64
	err      error
}

type rgb struct{ r, g, b int }

func parseHex(h string, def rgb) rgb {
	var r, g, b int
	if _, err := fmt.Sscanf(strings.TrimPrefix(h, "#"), "%02x%02x%02x", &r, &g, &b); err != nil || len(strings.TrimPrefix(h, "#")) != 6 {
		return def
	}
	return rgb{r, g, b}
}

func (c rgb) mix(o rgb, t float64) rgb { // t of o
	m := func(a, b int) int { return int(float64(a)*(1-t) + float64(b)*t + 0.5) }
	return rgb{m(c.r, o.r), m(c.g, o.g), m(c.b, o.b)}
}

var (
	colInk    = rgb{0x0F, 0x17, 0x2A}
	colMuted  = rgb{0x64, 0x74, 0x8B}
	colLine   = rgb{0xE2, 0xE8, 0xF0}
	colZebra  = rgb{0xF8, 0xFA, 0xFC}
	colGreen  = rgb{0x15, 0x80, 0x3D}
	colRed    = rgb{0xB9, 0x1C, 0x1C}
	colWhite  = rgb{255, 255, 255}
	white     = colWhite
	badgeFill = map[Tone][2]rgb{
		Green: {{0xDC, 0xFC, 0xE7}, colGreen},
		Red:   {{0xFE, 0xE2, 0xE2}, colRed},
		Amber: {{0xFE, 0xF3, 0xC7}, {0x92, 0x40, 0x0E}},
	}
)

func (r *Report) fill(c rgb) { r.p.SetFillColor(c.r, c.g, c.b) }
func (r *Report) text(c rgb) { r.p.SetTextColor(c.r, c.g, c.b) }
func (r *Report) draw(c rgb) { r.p.SetDrawColor(c.r, c.g, c.b) }

func (r *Report) T(key string, args ...any) string { return i18n.T(r.o.Lang, key, args...) }

// Lang is the report language.
func (r *Report) Lang() i18n.Lang { return r.o.Lang }

// NewReport starts a report. The first page is created immediately.
func NewReport(o Options) *Report {
	if o.Lang == "" {
		o.Lang = i18n.Default
	}
	if o.GeneratedAt.IsZero() {
		o.GeneratedAt = time.Now()
	}
	if o.Company.LegalName == "" {
		o.Company = settings.DefaultCompany
	}
	orient := "P"
	if o.Landscape {
		orient = "L"
	}
	p := fpdf.New(orient, "pt", "A4", "")
	p.SetAutoPageBreak(false, 0)
	p.SetMargins(0, 0, 0)
	p.SetCellMargin(0)
	p.SetCompression(true)
	for _, f := range fontFiles {
		p.AddUTF8FontFromBytes(f.fam, f.style, loadFontBytes(f.file))
	}
	p.SetTitle(o.Title, true)
	p.SetAuthor(o.Company.LegalName, true)
	p.SetCreator("BTN Operations System", true)
	p.SetProducer("BTN Operations System", true)
	p.SetCreationDate(o.GeneratedAt)
	w, h := p.GetPageSize()
	if o.Landscape {
		w, h = h, w
	}
	pw, ph := p.GetPageSize()
	r := &Report{o: o, p: p, ts: typesetter{p}, pageW: pw, pageH: ph, contentW: pw - 2*padX}
	_, _ = w, h
	r.primary = parseHex(o.Primary, rgb{0x0C, 0x23, 0x4B})
	r.accent = parseHex(o.Accent, rgb{0xF5, 0x9E, 0x0B})
	r.soft = r.primary.mix(white, 0.91)
	r.registerLogo()
	r.newPage()
	r.titleBlock()
	return r
}

func (r *Report) registerLogo() {
	data := r.o.LogoPNG
	if len(data) == 0 {
		data, _ = assets.FS.ReadFile("logo-on-light.png")
	}
	// fpdf reads PNG/JPEG/GIF; convert WebP to PNG.
	if len(data) > 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		if img, err := webp.Decode(bytes.NewReader(data)); err == nil {
			var buf bytes.Buffer
			if png.Encode(&buf, img) == nil {
				data = buf.Bytes()
			}
		}
	}
	typ := "PNG"
	if len(data) > 3 && data[0] == 0xFF && data[1] == 0xD8 {
		typ = "JPG"
	}
	info := r.p.RegisterImageOptionsReader("logo", fpdf.ImageOptions{ImageType: typ, ReadDpi: false}, bytes.NewReader(data))
	if info == nil || r.p.Err() {
		r.p.ClearError()
		data, _ = assets.FS.ReadFile("logo-on-light.png")
		info = r.p.RegisterImageOptionsReader("logo", fpdf.ImageOptions{ImageType: "PNG"}, bytes.NewReader(data))
	}
	r.logoName = "logo"
	// object-fit: contain inside 120 x 45 pt
	iw, ih := info.Extent()
	if iw == 0 || ih == 0 {
		iw, ih = 120, 45
	}
	s := min(120/iw, 45/ih)
	r.logoW, r.logoH = iw*s, ih*s
	_ = image.Rect
}

// newPage adds a page with the running header (logo, company block, band).
func (r *Report) newPage() {
	r.p.AddPage()
	x := padX
	r.p.ImageOptions(r.logoName, x, padTop, r.logoW, r.logoH, false, fpdf.ImageOptions{}, 0, "")
	// company block, right aligned
	right := r.pageW - padX
	y := padTop
	c := r.o.Company
	lh := 7.5 * 1.45
	r.text(r.primary)
	r.ts.draw(c.LegalName, padX, y+9.5*0.85, right-padX, 9.5, true, "R")
	y += 9.5 * 1.45
	r.text(colMuted)
	for _, line := range []string{c.Address, strings.Join(nonEmpty(c.Phone, c.Email), "  ·  "), tinLine(r, c.TIN)} {
		if line == "" {
			continue
		}
		r.ts.draw(line, padX, y+7.5*0.85, right-padX, 7.5, false, "R")
		y += lh
	}
	headerH := max(r.logoH, y-padTop)
	by := padTop + headerH + 10
	r.fill(r.primary)
	r.p.Rect(padX, by, r.contentW*5/6, 3, "F")
	r.fill(r.accent)
	r.p.Rect(padX+r.contentW*5/6, by, r.contentW/6, 3, "F")
	r.y = by + 3 + 12
}

func tinLine(r *Report, tin string) string {
	if tin == "" {
		return ""
	}
	return r.T("pdf.tin") + ": " + tin
}

func nonEmpty(s ...string) []string {
	var out []string
	for _, x := range s {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}

func (r *Report) bottom() float64 { return r.pageH - padBottom }

// ensure starts a new page when h points do not fit; returns true when it did.
func (r *Report) ensure(h float64) bool {
	if r.y+h > r.bottom() && r.y > padTop+80 {
		r.newPage()
		return true
	}
	return false
}

func (r *Report) titleBlock() {
	o := r.o
	// meta box rows
	rows := []Meta{}
	if o.DocumentNo != "" {
		rows = append(rows, Meta{r.T("pdf.document"), o.DocumentNo})
	}
	rows = append(rows, o.Meta...)
	rows = append(rows, Meta{r.T("pdf.generated"), dates.FormatDateTime(o.GeneratedAt)}, Meta{r.T("pdf.by"), o.GeneratedBy})
	maxRow := 150.0 - 16
	for _, m := range rows {
		maxRow = max(maxRow, r.ts.width(m.Label, 7.5, false)+10+r.ts.width(m.Value, 7.5, true))
	}
	boxW := min(maxRow+16, r.contentW*0.5)
	rowH := 7.5*1.3 + 2
	boxH := float64(len(rows))*rowH + 10
	titleW := r.contentW - boxW - 12
	tl := r.ts.wrap(o.Title, 15, true, titleW)
	var sl []string
	if o.Subtitle != "" {
		sl = r.ts.wrap(o.Subtitle, 9, false, titleW)
	}
	titleH := float64(len(tl))*15*1.3 + func() float64 {
		if len(sl) == 0 {
			return 0
		}
		return 3 + float64(len(sl))*9*1.3
	}()
	blockH := max(titleH, boxH)
	r.ensure(blockH)
	top := r.y
	// title (bottom-aligned with the box like align-items: flex-end)
	ty := top + blockH - titleH
	r.text(r.primary)
	for _, l := range tl {
		r.ts.draw(l, padX, ty+15*1.0, titleW, 15, true, "L")
		ty += 15 * 1.3
	}
	if len(sl) > 0 {
		ty += 3
		r.text(colMuted)
		for _, l := range sl {
			r.ts.draw(l, padX, ty+9*1.0, titleW, 9, false, "L")
			ty += 9 * 1.3
		}
	}
	// meta box
	bx := padX + r.contentW - boxW
	by := top + blockH - boxH
	r.draw(colLine)
	r.p.SetLineWidth(1)
	r.p.RoundedRect(bx, by, boxW, boxH, 4, "1234", "D")
	yy := by + 5
	for _, m := range rows {
		r.text(colMuted)
		r.ts.draw(r.ts.truncate(m.Label, 7.5, false, boxW/2), bx+8, yy+7.5*0.95, boxW, 7.5, false, "L")
		r.text(colInk)
		r.ts.draw(r.ts.truncate(m.Value, 7.5, true, boxW-16-r.ts.width(m.Label, 7.5, false)-10), bx+8, yy+7.5*0.95, boxW-16, 7.5, true, "R")
		yy += rowH
	}
	r.y = top + blockH + 12
}

// Section draws a section title that stays with the content that follows (keep-with-next).
func (r *Report) Section(title string) *Report {
	r.y += 10
	r.ensure(10*1.3 + 6 + 40)
	r.text(r.primary)
	r.ts.draw(title, padX, r.y+10*0.95, r.contentW, 10, true, "L")
	r.y += 10*1.3 + 6
	return r
}

// Spacer adds vertical space (points).
func (r *Report) Spacer(h float64) *Report { r.y += h; return r }

// Note draws small muted explanatory text.
func (r *Report) Note(text string) *Report {
	r.y += 8
	lines := r.ts.wrap(text, 7.5, false, r.contentW)
	lh := 7.5 * 1.4
	r.text(colMuted)
	for _, l := range lines {
		r.ensure(lh)
		r.ts.draw(l, padX, r.y+7.5*0.9, r.contentW, 7.5, false, "L")
		r.y += lh
	}
	return r
}

// Badge draws a standalone status badge at the current position.
func (r *Report) Badge(label string, tone Tone) *Report {
	r.ensure(14)
	r.drawBadge(padX, r.y, label, tone)
	r.y += 14
	return r
}

func (r *Report) toneColors(t Tone) (bg, fg rgb) {
	if t == Navy {
		return r.soft, r.primary
	}
	c := badgeFill[t]
	return c[0], c[1]
}

func (r *Report) drawBadge(x, y float64, label string, tone Tone) float64 {
	bg, fg := r.toneColors(tone)
	w := r.ts.width(label, 7, false) + 10
	r.fill(bg)
	r.p.RoundedRect(x, y, w, 7*1.3+4, 3, "1234", "F")
	r.text(fg)
	r.ts.draw(label, x+5, y+2+7*0.95, w, 7, false, "L")
	return w
}

// KpiRow draws equal-width KPI tiles.
func (r *Report) KpiRow(items []Kpi) *Report {
	if len(items) == 0 {
		return r
	}
	gap := 6.0
	w := (r.contentW - gap*float64(len(items)-1)) / float64(len(items))
	inner := w - 14 - 3
	h := 0.0
	type tile struct {
		label, hint []string
		value       string
	}
	tiles := make([]tile, len(items))
	for i, k := range items {
		t := tile{label: r.ts.wrap(strings.ToUpper(k.Label), 7, false, inner), value: k.Value}
		if k.Hint != "" {
			t.hint = r.ts.wrap(k.Hint, 6.5, false, inner)
		}
		tiles[i] = t
		th := 14 + float64(len(t.label))*7*1.3 + 3 + 12*1.3
		if len(t.hint) > 0 {
			th += 2 + float64(len(t.hint))*6.5*1.3
		}
		h = max(h, th)
	}
	r.ensure(h + 6)
	for i, t := range tiles {
		x := padX + float64(i)*(w+gap)
		r.draw(colLine)
		r.p.SetLineWidth(1)
		r.p.RoundedRect(x, r.y, w, h, 3, "1234", "D")
		r.fill(r.accent)
		r.p.Rect(x, r.y, 3, h, "F")
		yy := r.y + 7
		r.text(colMuted)
		for _, l := range t.label {
			r.ts.draw(l, x+3+7, yy+7*0.95, inner, 7, false, "L")
			yy += 7 * 1.3
		}
		yy += 3
		r.text(r.primary)
		r.ts.draw(r.ts.truncate(t.value, 12, true, inner), x+3+7, yy+12*0.95, inner, 12, true, "L")
		yy += 12 * 1.3
		if len(t.hint) > 0 {
			yy += 2
			r.text(colMuted)
			for _, l := range t.hint {
				r.ts.draw(l, x+3+7, yy+6.5*0.95, inner, 6.5, false, "L")
				yy += 6.5 * 1.3
			}
		}
	}
	r.y += h + 6
	return r
}

// Signatures draws a signature row (kept together on one page).
func (r *Report) Signatures(sigs []Signatory) *Report {
	if len(sigs) == 0 {
		return r
	}
	gap := 14.0
	w := (r.contentW - gap*float64(len(sigs)-1)) / float64(len(sigs))
	r.y += 26
	r.ensure(26 + 4 + 7.5*1.3*2 + 7*1.3*2 + 10)
	top := r.y
	maxH := 0.0
	for i, s := range sigs {
		x := padX + float64(i)*(w+gap)
		if s.Name != "" {
			r.text(colInk)
			r.ts.draw(r.ts.truncate(s.Name, 8, false, w), x, top+26-3-2, w, 8, false, "L")
		}
		r.draw(colInk)
		r.p.SetLineWidth(1)
		r.p.Line(x, top+26, x+w, top+26)
		yy := top + 26 + 4
		r.text(r.primary)
		for _, l := range r.ts.wrap(s.Role, 7.5, true, w) {
			r.ts.draw(l, x, yy+7.5*0.95, w, 7.5, true, "L")
			yy += 7.5 * 1.3
		}
		meta := r.T("pdf.signature_date")
		if s.At != nil {
			verb := s.Verb
			if verb == "" {
				verb = r.T("pdf.verb_approved")
			}
			meta = r.T("pdf.in_system", "verb", verb, "when", dates.FormatDateTime(*s.At))
		}
		yy += 1
		r.text(colMuted)
		for _, l := range r.ts.wrap(meta, 7, false, w) {
			r.ts.draw(l, x, yy+7*0.95, w, 7, false, "L")
			yy += 7 * 1.3
		}
		maxH = max(maxH, yy-top)
	}
	r.y = top + maxH + 4
	return r
}

// ── tables ───────────────────────────────────────────────────────────────────

func cellOf(v any) Cell {
	switch t := v.(type) {
	case nil:
		return Cell{}
	case Cell:
		return t
	case *Cell:
		return *t
	case string:
		return Cell{Text: t}
	case fmt.Stringer:
		return Cell{Text: t.String()}
	default:
		return Cell{Text: fmt.Sprint(t)}
	}
}

// Table draws a table with a navy header repeated on every page, zebra rows and an optional totals row.
// Rows and totals hold strings, numbers or Cell values. Rows never split across pages.
func (r *Report) Table(cols []Column, rows [][]any, totals []any, empty ...string) *Report {
	pad := 4.0
	total := 0.0
	for i := range cols {
		if cols[i].Width <= 0 {
			cols[i].Width = 1
		}
		if cols[i].Align == "" {
			cols[i].Align = Left
		}
		total += cols[i].Width
	}
	widths := make([]float64, len(cols))
	for i, c := range cols {
		widths[i] = r.contentW * c.Width / total
	}
	lh := bodySize * 1.3

	// cellLines returns the wrapped lines for a cell and the font size to draw them at. Number-like
	// values (amounts, quantities, phone numbers) never break across lines — a wrapped "2,474,350"
	// reads as two different figures — so they shrink to fit instead, down to minNumberSize.
	cellLines := func(c Cell, w float64, size float64, bold bool) ([]string, float64) {
		if c.Badge != "" {
			return []string{c.Text}, size
		}
		b := bold || c.Bold
		avail := w - 2*pad
		if isAtomicNumber(c.Text) {
			if tw := r.ts.width(c.Text, size, b); tw > avail && tw > 0 {
				fit := size * avail / tw
				if fit >= minNumberSize {
					return []string{c.Text}, fit
				}
				size = minNumberSize
			} else {
				return []string{c.Text}, size
			}
		}
		return r.ts.wrap(c.Text, size, b, avail), size
	}
	header := func() {
		h := 0.0
		lines := make([][]string, len(cols))
		for i, c := range cols {
			lines[i] = r.ts.wrap(c.Header, 7.5, true, widths[i]-2*pad)
			h = max(h, float64(len(lines[i]))*7.5*1.3+2*pad)
		}
		r.fill(r.primary)
		r.p.Rect(padX, r.y, r.contentW, h, "F")
		r.text(white)
		x := padX
		for i, c := range cols {
			yy := r.y + pad
			for _, l := range lines[i] {
				r.ts.draw(l, x+pad, yy+7.5*0.95, widths[i]-2*pad, 7.5, true, string(c.Align))
				yy += 7.5 * 1.3
			}
			x += widths[i]
		}
		r.y += h
	}
	drawRow := func(cells []any, i int, isTotal bool) {
		cs := make([]Cell, len(cols))
		h := 0.0
		lines := make([][]string, len(cols))
		sizes := make([]float64, len(cols))
		for ci := range cols {
			var v any
			if ci < len(cells) {
				v = cells[ci]
			}
			cs[ci] = cellOf(v)
			lines[ci], sizes[ci] = cellLines(cs[ci], widths[ci], bodySize, isTotal)
			ch := float64(len(lines[ci])) * lh
			if cs[ci].Badge != "" {
				ch = 7*1.3 + 4
			}
			h = max(h, ch+2*pad)
		}
		if r.ensure(h) {
			header()
		}
		switch {
		case isTotal:
			r.fill(r.soft)
			r.p.Rect(padX, r.y, r.contentW, h, "F")
			r.draw(r.primary)
			r.p.SetLineWidth(1)
			r.p.Line(padX, r.y, padX+r.contentW, r.y)
		case i%2 == 1:
			r.fill(colZebra)
			r.p.Rect(padX, r.y, r.contentW, h, "F")
		}
		x := padX
		for ci, c := range cs {
			if c.Badge != "" {
				bw := r.ts.width(c.Text, 7, false) + 10
				bx := x + pad
				switch cols[ci].Align {
				case Right:
					bx = x + widths[ci] - pad - bw
				case Center:
					bx = x + (widths[ci]-bw)/2
				}
				r.drawBadge(bx, r.y+pad, c.Text, c.Badge)
			} else {
				if c.Color != "" {
					r.text(parseHex(c.Color, colInk))
				} else {
					r.text(colInk)
				}
				yy := r.y + pad
				fs := sizes[ci]
				// keep shrunken numbers on the row's text baseline
				base := yy + bodySize*0.95
				for _, l := range lines[ci] {
					r.ts.draw(l, x+pad, base, widths[ci]-2*pad, fs, isTotal || c.Bold, string(cols[ci].Align))
					base += lh
				}
			}
			x += widths[ci]
		}
		if !isTotal {
			r.draw(colLine)
			r.p.SetLineWidth(0.5)
			r.p.Line(padX, r.y+h, padX+r.contentW, r.y+h)
		}
		r.y += h
	}

	r.ensure(7.5*1.3 + 2*pad + lh + 2*pad)
	header()
	if len(rows) == 0 {
		msg := r.T("pdf.no_records")
		if len(empty) > 0 {
			msg = empty[0]
		}
		h := lh + 2*pad
		r.draw(colLine)
		r.p.SetLineWidth(0.5)
		r.p.Line(padX, r.y+h, padX+r.contentW, r.y+h)
		r.text(colMuted)
		r.ts.draw(msg, padX, r.y+pad+bodySize*0.95, r.contentW, bodySize, false, "C")
		r.y += h
	}
	for i, row := range rows {
		drawRow(row, i, false)
	}
	if totals != nil {
		drawRow(totals, 0, true)
	}
	r.y += 4
	return r
}

// Pages is the current page count.
func (r *Report) Pages() int { return r.p.PageCount() }

// Bytes finishes the document (running footers with "Page X of Y") and returns the PDF.
func (r *Report) Bytes() ([]byte, error) {
	total := r.p.PageCount()
	left := r.o.Company.LegalName + " · " + r.o.Title
	if r.o.DocumentNo != "" {
		left += " · " + r.o.DocumentNo
	}
	for i := 1; i <= total; i++ {
		r.p.SetPage(i)
		fy := r.pageH - 22 - (6 + 7*1.3 + 1)
		r.draw(colLine)
		r.p.SetLineWidth(1)
		r.p.Line(padX, fy, padX+r.contentW, fy)
		pg := r.T("pdf.page_of", "page", i, "total", total)
		pw := r.ts.width(pg, 7, false)
		r.text(colMuted)
		r.ts.draw(r.ts.truncate(left, 7, false, r.contentW-pw-12), padX, fy+6+7*0.95, r.contentW, 7, false, "L")
		r.ts.draw(pg, padX, fy+6+7*0.95, r.contentW, 7, false, "R")
	}
	var buf bytes.Buffer
	if err := r.p.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var unsafeName = regexp.MustCompile(`[^\w.\-]+`)

// Send writes the PDF with the same headers as Node sendPdf. filename should include plant and dates,
// e.g. "BTN-1_Daily-Block-Report_2026-11-14.pdf". download=true forces a download (?download=1).
func Send(c *fiber.Ctx, r *Report, filename string, download bool) error {
	b, err := r.Bytes()
	if err != nil {
		return err
	}
	disp := "inline"
	if download {
		disp = "attachment"
	}
	c.Set(fiber.HeaderContentType, "application/pdf")
	c.Set(fiber.HeaderContentDisposition, fmt.Sprintf(`%s; filename="%s"`, disp, unsafeName.ReplaceAllString(filename, "_")))
	c.Set(fiber.HeaderCacheControl, "private, no-store")
	return c.Send(b)
}

// New builds a report for the current request: language from the request, company block and logo from
// settings/branding, generated-by = the signed-in user.
func New(c *fiber.Ctx, d *deps.Deps, o Options) (*Report, error) {
	ctx := httpx.Ctx(c)
	if o.Lang == "" {
		o.Lang = httpx.Lang(c)
	}
	// ?orientation=portrait|landscape lets the user override each report's default page shape.
	switch strings.ToLower(strings.TrimSpace(c.Query("orientation"))) {
	case "portrait":
		o.Landscape = false
	case "landscape":
		o.Landscape = true
	}
	if o.GeneratedBy == "" {
		if u := auth.FromCtx(c); u != nil {
			o.GeneratedBy = u.Name
		}
	}
	if err := fillBranding(ctx, d.Settings, &o); err != nil {
		return nil, err
	}
	return NewReport(o), nil
}

func fillBranding(ctx context.Context, set *settings.Service, o *Options) error {
	if set == nil {
		return nil
	}
	if o.Company.LegalName == "" {
		co, err := set.Company(ctx)
		if err != nil {
			return err
		}
		o.Company = co
	}
	b, err := set.Branding(ctx)
	if err != nil {
		return err
	}
	if o.Primary == "" {
		o.Primary = b.PrimaryColor
	}
	if o.Accent == "" {
		o.Accent = b.AccentColor
	}
	if len(o.LogoPNG) == 0 && b.LogoLightID != "" {
		if a, err := set.Asset(ctx, b.LogoLightID); err == nil && a.Mime != "image/svg+xml" { // SVG falls back to the default logo
			o.LogoPNG = a.Data
		}
	}
	return nil
}

// minNumberSize is the smallest font a number-like table cell shrinks to before it is allowed to wrap.
const minNumberSize = 5.5

// isAtomicNumber reports whether a cell holds a single number-like token that must not be split
// across lines: digits with grouping/decimal separators, signs, a percent sign, or a phone number.
func isAtomicNumber(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \t\n") {
		return false
	}
	digits := 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == ',' || r == '.' || r == '-' || r == '+' || r == '%' || r == '\u2212' || r == '(' || r == ')':
		default:
			return false
		}
	}
	return digits > 0
}
