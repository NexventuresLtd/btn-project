// Package catalog is the canonical catalogue seeded on first install, plus the spellings found in
// the legacy Excel reports so the importer can map them (port of legacy lib/catalog.ts).
package catalog

import "strings"

type Plant struct{ Code, Name string }

type Product struct {
	Code, Name   string
	Dimensions   *string
	DefaultPrice int
	SortOrder    int
	Aliases      []string
}

type Material struct {
	Code, Name, Unit string
	MinLevel         float64
	SortOrder        int
	Aliases          []string
}

func s(v string) *string { return &v }

var Plants = []Plant{{"BTN-1", "BTN 1 Plant"}, {"BTN-2", "BTN 2 Plant"}, {"BTN-3", "BTN 3 Plant"}}

var Products = []Product{
	{"HB20", "Hollow Block 20", s("20 x 20 x 40 cm"), 1500, 10, []string{"HOLLOW BLOCK 20", "HOLLOW BLOCK 20-20-40 CM"}},
	{"HB20-2H", "Hollow Block 20 (Two Holes)", s("20 x 20 x 40 cm"), 1500, 11, []string{"HOLLOW 20 WITH TWO HOLES", "HOLLOW BLOCK 20 TWO HOLE", "HOLLOW BLOCK 20 WITH TWO HOLES", "HOLLOW BLOCK 20C WITH TWO HOLES"}},
	{"HB15", "Hollow Block 15", s("15 x 20 x 40 cm"), 1300, 20, []string{"HOLLOW BLOCK 15", "HOLLOW BLOCK 15-20-40 CM"}},
	{"HB15-2H", "Hollow Block 15 (Two Holes)", s("15 x 20 x 40 cm"), 1300, 21, []string{"HOLLOW BLOCK 15 TWO HOLES"}},
	{"HB15-3H", "Hollow Block 15 (Three Holes)", s("15 x 20 x 40 cm"), 1300, 22, []string{"HOLLOW BLOCK 15 THREE HOLES", "HOLLOW BLOCK 15 THREE"}},
	{"HB10", "Hollow Block 10", s("10 x 20 x 40 cm"), 1200, 30, []string{"HOLLOW BLOCK 10", "HOLLOW BLOCK 10-20-40 CM"}},
	{"HD16", "Hourdi Beam Block 16", s("16 cm"), 0, 40, []string{"HOURDI BEAM BLOCK 16"}},
	{"HD10", "Hourdi Beam Block 10", s("10 cm"), 0, 41, []string{"HOURDI BEAM BLOCK 10"}},
	{"SB20", "Solid Block 20", s("20 x 20 x 40 cm"), 2500, 50, []string{"SOLID BLOCK 20", "SOLID BLOCK 20-20-40 CM"}},
	{"PV", "Pavers", nil, 0, 60, []string{"PAVERS"}},
	{"PVZ", "Zigzag Pavers", nil, 0, 61, []string{"ZIGZAG PAVERS"}},
}

var Materials = []Material{
	{"CEMENT", "Cement", "BAG", 50, 10, []string{"CEMENT"}},
	{"QDUST", "Quarry Dust", "TRUCK", 1, 20, []string{"QUARRY DUST"}},
	{"BSAND", "Black Sand", "TRUCK", 1, 30, []string{"BLACK SAND"}},
	{"KSAND", "Sand Kayumbu", "TRUCK", 1, 40, []string{"SAND KAYUMBU"}},
	{"VISCO90", "Viscocrete 90 HE", "LITER", 20, 50, []string{"VISCOCRETE 90 HE"}},
	{"BPOWDER", "Black Powder", "KG", 10, 60, []string{"BLACK POWDER"}},
}

func norm(x string) string { return strings.Join(strings.Fields(strings.ToUpper(x)), " ") }

// ProductCodeFor maps a legacy Excel product name to a product code ("" when unknown).
func ProductCodeFor(legacy string) string {
	n := norm(legacy)
	for _, p := range Products {
		for _, a := range p.Aliases {
			if norm(a) == n {
				return p.Code
			}
		}
	}
	return ""
}

// MaterialCodeFor maps a legacy Excel material name to a material code ("" when unknown).
func MaterialCodeFor(legacy string) string {
	n := norm(legacy)
	for _, m := range Materials {
		for _, a := range m.Aliases {
			if norm(a) == n {
				return m.Code
			}
		}
	}
	return ""
}
