package importer

import (
	"regexp"
	"strings"
)

// Everything the importer prints goes through these helpers: the sheets hold national IDs,
// MoMo numbers and names.

// MaskNumber: "0791234567" -> "079*****67".
func MaskNumber(value string) string {
	s := strings.Join(strings.Fields(value), "")
	if s == "" {
		return "—"
	}
	if len(s) <= 4 {
		return strings.Repeat("*", len(s))
	}
	return s[:3] + strings.Repeat("*", len(s)-5) + s[len(s)-2:]
}

var digitRun = regexp.MustCompile(`\d{9,}`)

// MaskDigitsInText masks any run of 9+ digits inside free text.
func MaskDigitsInText(text string) string {
	return digitRun.ReplaceAllStringFunc(text, MaskNumber)
}

// MaskName: "MUSONI NTWARI PATERNE" -> "MUS. N. P.".
func MaskName(name string) string {
	parts := strings.Fields(strings.ToUpper(name))
	if len(parts) == 0 {
		return "—"
	}
	out := make([]string, len(parts))
	for i, p := range parts {
		r := []rune(p)
		if i == 0 {
			n := 3
			if len(r) < n {
				n = len(r)
			}
			out[i] = string(r[:n]) + "."
		} else {
			out[i] = string(r[:1]) + "."
		}
	}
	return strings.Join(out, " ")
}
