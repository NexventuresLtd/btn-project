package pdf

import (
	"strings"
	"sync"
	"unicode"

	"btn-ops/assets"

	"github.com/go-pdf/fpdf"
	"golang.org/x/image/font/sfnt"
)

// Two font families are embedded: Noto Sans (Latin and symbols) and Noto Sans Ethiopic (Amharic).
// Text is split into runs so mixed strings ("ገጽ 1 ከ 3", product codes inside Amharic text) render
// correctly in any language; measuring and wrapping operate on those runs.
const (
	famLatin = "noto"
	famEth   = "notoeth"
)

var fontFiles = []struct{ fam, style, file string }{
	{famLatin, "", "fonts/NotoSans-Regular.ttf"},
	{famLatin, "B", "fonts/NotoSans-Bold.ttf"},
	{famEth, "", "fonts/NotoSansEthiopic-Regular.ttf"},
	{famEth, "B", "fonts/NotoSansEthiopic-Bold.ttf"},
}

var (
	fontBytes   = map[string][]byte{}
	fontBytesMu sync.Mutex
	coverage    = map[string]*sfnt.Font{} // by family (regular) for glyph lookups
)

func loadFontBytes(file string) []byte {
	fontBytesMu.Lock()
	defer fontBytesMu.Unlock()
	if b, ok := fontBytes[file]; ok {
		return b
	}
	b, err := assets.FS.ReadFile(file)
	if err != nil {
		panic("pdf: missing embedded font " + file)
	}
	fontBytes[file] = b
	if strings.HasSuffix(file, "Regular.ttf") {
		fam := famLatin
		if strings.Contains(file, "Ethiopic") {
			fam = famEth
		}
		if f, err := sfnt.Parse(b); err == nil {
			coverage[fam] = f
		}
	}
	return b
}

var glyphMu sync.Mutex
var glyphCache = map[string]map[rune]bool{famLatin: {}, famEth: {}}

func covers(fam string, r rune) bool {
	glyphMu.Lock()
	defer glyphMu.Unlock()
	if v, ok := glyphCache[fam][r]; ok {
		return v
	}
	f := coverage[fam]
	ok := false
	if f != nil {
		var buf sfnt.Buffer
		idx, err := f.GlyphIndex(&buf, r)
		ok = err == nil && idx != 0
	}
	glyphCache[fam][r] = ok
	return ok
}

func isEthiopic(r rune) bool {
	return (r >= 0x1200 && r <= 0x139F) || (r >= 0x2D80 && r <= 0x2DDF) || (r >= 0xAB00 && r <= 0xAB2F)
}

type run struct {
	fam  string
	text string
}

// runs splits s into font runs.
func runs(s string) []run {
	var out []run
	prev := famLatin
	for _, r := range s {
		fam := famLatin
		switch {
		case isEthiopic(r):
			fam = famEth
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if covers(famLatin, r) {
				fam = famLatin
			} else if covers(famEth, r) {
				fam = famEth
			}
		default: // spaces, punctuation, symbols: stay with the neighbour when it has the glyph
			switch {
			case covers(prev, r):
				fam = prev
			case covers(famLatin, r):
				fam = famLatin
			case covers(famEth, r):
				fam = famEth
			}
		}
		if n := len(out); n > 0 && out[n-1].fam == fam {
			out[n-1].text += string(r)
		} else {
			out = append(out, run{fam, string(r)})
		}
		prev = fam
	}
	return out
}

// typesetter measures and draws text on a fpdf document (units: points).
type typesetter struct{ p *fpdf.Fpdf }

func (t typesetter) width(s string, size float64, bold bool) float64 {
	style := ""
	if bold {
		style = "B"
	}
	w := 0.0
	for _, r := range runs(s) {
		t.p.SetFont(r.fam, style, size)
		w += t.p.GetStringWidth(r.text)
	}
	return w
}

// wrap breaks s into lines no wider than maxW (words; very long words are split by character).
func (t typesetter) wrap(s string, size float64, bold bool, maxW float64) []string {
	var lines []string
	for _, para := range strings.Split(s, "\n") {
		if strings.TrimSpace(para) == "" {
			lines = append(lines, "")
			continue
		}
		var cur string
		for _, word := range splitWords(para) {
			cand := cur + word
			if t.width(strings.TrimRight(cand, " "), size, bold) <= maxW || cur == "" {
				cur = cand
				// a single word wider than the column: hard-split by character
				for t.width(strings.TrimRight(cur, " "), size, bold) > maxW && len([]rune(cur)) > 1 {
					rs := []rune(cur)
					n := len(rs) - 1
					for n > 1 && t.width(string(rs[:n]), size, bold) > maxW {
						n--
					}
					lines = append(lines, string(rs[:n]))
					cur = string(rs[n:])
				}
				continue
			}
			lines = append(lines, strings.TrimRight(cur, " "))
			cur = word
		}
		lines = append(lines, strings.TrimRight(cur, " "))
	}
	return lines
}

// splitWords splits after spaces (and after the Ethiopic word separator ፡) keeping the separator.
func splitWords(s string) []string {
	var out []string
	var b strings.Builder
	for _, r := range s {
		b.WriteRune(r)
		if r == ' ' || r == '፡' {
			out = append(out, b.String())
			b.Reset()
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

// draw writes one line with its baseline at y; align is "L", "R" or "C" within [x, x+w].
func (t typesetter) draw(s string, x, y, w, size float64, bold bool, align string) {
	style := ""
	if bold {
		style = "B"
	}
	tw := t.width(s, size, bold)
	switch align {
	case "R":
		x = x + w - tw
	case "C":
		x = x + (w-tw)/2
	}
	for _, r := range runs(s) {
		t.p.SetFont(r.fam, style, size)
		t.p.Text(x, y, r.text)
		x += t.p.GetStringWidth(r.text)
	}
}

// truncate shortens s with an ellipsis to fit maxW.
func (t typesetter) truncate(s string, size float64, bold bool, maxW float64) string {
	if t.width(s, size, bold) <= maxW {
		return s
	}
	rs := []rune(s)
	for len(rs) > 1 && t.width(string(rs)+"…", size, bold) > maxW {
		rs = rs[:len(rs)-1]
	}
	return string(rs) + "…"
}
