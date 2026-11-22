package pdf

import (
	"bytes"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"

	"github.com/gofiber/fiber/v2"
)

// Renders the same sample report in en / am / rw. Set PDF_DEMO_DIR to keep the files for visual review.
func TestDemoReportsRenderInAllLanguages(t *testing.T) {
	dir := os.Getenv("PDF_DEMO_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	for _, l := range i18n.All {
		for _, landscape := range []bool{false, true} {
			rep := Demo(l, 60, landscape)
			b, err := rep.Bytes()
			if err != nil {
				t.Fatalf("%s: %v", l, err)
			}
			if !bytes.HasPrefix(b, []byte("%PDF-")) || !bytes.Contains(b, []byte("%%EOF")) {
				t.Fatalf("%s: not a PDF", l)
			}
			if rep.Pages() < 2 {
				t.Fatalf("%s: 60 rows should paginate, got %d page(s)", l, rep.Pages())
			}
			name := "demo-" + string(l)
			if landscape {
				name += "-landscape"
			}
			if err := os.WriteFile(filepath.Join(dir, name+".pdf"), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestEthiopicFontIsEmbeddedForAmharic(t *testing.T) {
	am, _ := Demo(i18n.AM, 3, false).Bytes()
	en, _ := Demo(i18n.EN, 3, false).Bytes()
	// the PDF has subsetted font names with a tag prefix, e.g. "/ABCDEF+notoeth" -- check the family appears.
	if !bytes.Contains(am, []byte("notoeth")) {
		t.Fatal("Amharic PDF must embed the Ethiopic font")
	}
	if bytes.Contains(en, []byte("notoeth")) {
		t.Log("note: English PDF also lists the Ethiopic font (unused subset)")
	}
}

func TestRunSplittingMixedScripts(t *testing.T) {
	rs := runs("ገጽ 1 ከ 3 · HB20 (ብሎክ)")
	var fam []string
	for _, r := range rs {
		fam = append(fam, r.fam)
	}
	hasEth, hasLatin := false, false
	for _, f := range fam {
		hasEth = hasEth || f == famEth
		hasLatin = hasLatin || f == famLatin
	}
	if !hasEth || !hasLatin {
		t.Fatalf("expected both families, got %v", fam)
	}
	joined := ""
	for _, r := range rs {
		joined += r.text
	}
	if joined != "ገጽ 1 ከ 3 · HB20 (ብሎክ)" {
		t.Fatalf("runs lose text: %q", joined)
	}
}

func TestWrapHandlesEthiopicAndLongWords(t *testing.T) {
	rep := NewReport(Options{Title: "t", Lang: i18n.AM})
	ts := rep.ts
	lines := ts.wrap("ይህ የናሙና ሪፖርት የጋራውን የPDF መሣሪያ ያሳያል የድርጅት ራስጌ የሚደገሙ የሰንጠረዥ ራስጌዎች", 8.5, false, 120)
	if len(lines) < 3 {
		t.Fatalf("expected wrapping, got %v", lines)
	}
	for _, l := range lines {
		if w := ts.width(l, 8.5, false); w > 121 {
			t.Fatalf("line %q is %.1f wide", l, w)
		}
	}
	long := ts.wrap("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", 8.5, false, 60)
	if len(long) < 2 {
		t.Fatal("long word must be hard-split")
	}
	for _, l := range long {
		if ts.width(l, 8.5, false) > 61 {
			t.Fatalf("overflow %q", l)
		}
	}
}

func BenchmarkRenderDemo(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if _, err := Demo(i18n.AM, 40, false).Bytes(); err != nil {
			b.Fatal(err)
		}
	}
}

func TestSendHeaders(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Get("/pdf", func(c *fiber.Ctx) error {
		return Send(c, Demo(i18n.EN, 3, false), "BTN-1 Daily Report/2026-11-14.pdf", c.Query("download") == "1")
	})
	resp, err := app.Test(httptest.NewRequest("GET", "/pdf", nil), -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Content-Type") != "application/pdf" || resp.Header.Get("Cache-Control") != "private, no-store" ||
		resp.Header.Get("Content-Disposition") != `inline; filename="BTN-1_Daily_Report_2026-11-14.pdf"` {
		t.Fatalf("headers: %v", resp.Header)
	}
	b, _ := io.ReadAll(resp.Body)
	if !bytes.HasPrefix(b, []byte("%PDF-")) || resp.ContentLength != int64(len(b)) && resp.ContentLength != -1 {
		t.Fatal("body")
	}
	resp, _ = app.Test(httptest.NewRequest("GET", "/pdf?download=1", nil), -1)
	if resp.Header.Get("Content-Disposition") != `attachment; filename="BTN-1_Daily_Report_2026-11-14.pdf"` {
		t.Fatalf("download: %v", resp.Header)
	}
}
