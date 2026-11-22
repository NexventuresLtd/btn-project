package pdf

import (
	"io"
	"net/http/httptest"
	"regexp"
	"strconv"
	"testing"

	"github.com/gofiber/fiber/v2"

	"btn-ops/internal/deps"
)

// pageShape reports whether the first page of a PDF is wider than it is tall.
func pageShape(t *testing.T, b []byte) (landscape bool) {
	t.Helper()
	m := regexp.MustCompile(`/MediaBox\s*\[\s*0\s+0\s+([\d.]+)\s+([\d.]+)\s*\]`).FindSubmatch(b)
	if m == nil {
		t.Fatalf("no MediaBox in PDF")
	}
	w, _ := strconv.ParseFloat(string(m[1]), 64)
	h, _ := strconv.ParseFloat(string(m[2]), 64)
	return w > h
}

func TestOrientationQueryOverridesDefault(t *testing.T) {
	cases := []struct {
		name          string
		defaultLand   bool
		query         string
		wantLandscape bool
	}{
		{"portrait default kept", false, "", false},
		{"landscape default kept", true, "", true},
		{"portrait report forced landscape", false, "?orientation=landscape", true},
		{"landscape report forced portrait", true, "?orientation=portrait", false},
		{"case and spaces tolerated", false, "?orientation=%20LANDSCAPE%20", true},
		{"unknown value ignored", true, "?orientation=sideways", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := fiber.New()
			app.Get("/r", func(c *fiber.Ctx) error {
				rep, err := New(c, &deps.Deps{}, Options{Title: "Orientation test", Landscape: tc.defaultLand, Lang: "en"})
				if err != nil {
					return err
				}
				rep.Table([]Column{{Header: "A"}, {Header: "B"}}, [][]any{{"x", 1}}, nil)
				return Send(c, rep, "t.pdf", false)
			})
			res, err := app.Test(httptest.NewRequest("GET", "/r"+tc.query, nil), -1)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(res.Body)
			if res.StatusCode != 200 {
				t.Fatalf("status %d: %s", res.StatusCode, body)
			}
			if got := pageShape(t, body); got != tc.wantLandscape {
				t.Fatalf("landscape = %v, want %v", got, tc.wantLandscape)
			}
		})
	}
}

func TestIsAtomicNumber(t *testing.T) {
	for in, want := range map[string]bool{
		"2,474,350":         true,
		"-67,451":           true,
		"12.5%":             true,
		"0787011992":        true,
		"(1,200)":           true,
		"−15":               true,
		"":                  false,
		"BTN-1":             false,
		"IMP-PC-2026-00003": false,
		"2,474 RWF":         false,
		"Approved":          false,
		"—":                 false,
	} {
		if got := isAtomicNumber(in); got != want {
			t.Errorf("isAtomicNumber(%q) = %v, want %v", in, got, want)
		}
	}
}
