package importer

import (
	"regexp"
	"strings"
)

type InferredCategory struct {
	Category  string
	PlantCode string // "BTN-1" when the description names exactly one plant, else ""
	Rule      string
}

func normDesc(s string) string { return strings.Join(strings.Fields(strings.ToUpper(s)), " ") }

var plantMention = regexp.MustCompile(`\bBTN\s*-?\s*([123])\b`)

// PlantsMentioned lists the distinct plants mentioned as "BTN 1", "BTN-2", "BTN3" ... in order of appearance.
func PlantsMentioned(description string) []string {
	var found []string
	seen := map[string]bool{}
	for _, m := range plantMention.FindAllStringSubmatch(normDesc(description), -1) {
		c := "BTN-" + m[1]
		if !seen[c] {
			seen[c] = true
			found = append(found, c)
		}
	}
	return found
}

// Order matters: the first matching rule wins ("TRANSPORT TO BRING STEEL" is transport, not materials).
var categoryRules = []struct {
	test           *regexp.Regexp
	category, rule string
}{
	{regexp.MustCompile(`\bWAGES?\b`), "WAGES", "WAGES"},
	{regexp.MustCompile(`\bSALAR(Y|IES)\b`), "SALARY", "SALARY"},
	{regexp.MustCompile(`\bTRANSPORT\b`), "TRANSPORT", "TRANSPORT"},
	{regexp.MustCompile(`\bFUEL\b`), "FUEL", "FUEL"},
	{regexp.MustCompile(`\b(INTERNET|AIRTIME|BUNDLES?)\b`), "INTERNET_COMMS", "INTERNET"},
	{regexp.MustCompile(`\b(LUNCH|FOOD|MEALS?)\b`), "MEALS", "LUNCH"},
	{regexp.MustCompile(`\bNIGHT ?SHIFTS?\b`), "NIGHT_SHIFT", "NIGHT SHIFT"},
	{regexp.MustCompile(`\bSTEEL\b`), "MATERIALS", "STEEL"},
}

// InferPettyCashCategory infers the category of a petty-cash expense from its free-text description.
func InferPettyCashCategory(description string) InferredCategory {
	text := normDesc(description)
	plants := PlantsMentioned(text)
	plant := ""
	if len(plants) == 1 {
		plant = plants[0]
	}
	for _, r := range categoryRules {
		if r.test.MatchString(text) {
			return InferredCategory{r.category, plant, r.rule}
		}
	}
	return InferredCategory{"OTHER", plant, "no keyword"}
}

var depositDesc = regexp.MustCompile(`^(RECEIVED|DEPOSIT|NEW DEPOSIT|TOP ?UP)\b`)

// IsDepositDescription recognises a float top-up line ("RECEIVED", "NEW DEPOSIT" ...).
func IsDepositDescription(description string) bool {
	return depositDesc.MatchString(normDesc(description))
}
