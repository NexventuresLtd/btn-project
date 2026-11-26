package sales

import (
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Duplicate client detection (ORD-02). Pure scoring; the handler loads the candidates.

// legalSuffixes are dropped before comparing names ("KAGAME BUILDERS LTD" ≈ "Kagame Builders").
var legalSuffixes = map[string]bool{"ltd": true, "limited": true, "sarl": true, "sa": true, "plc": true, "inc": true, "co": true,
	"company": true, "corp": true, "corporation": true, "llc": true, "group": true, "the": true, "and": true, "et": true, "cie": true, "rwanda": true}

// normName lower-cases, strips accents and punctuation, drops legal suffixes and collapses spaces.
func normName(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	if r, _, err := transform.String(t, s); err == nil {
		s = r
	}
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	words := strings.Fields(b.String())
	out := words[:0]
	for _, w := range words {
		if !legalSuffixes[w] {
			out = append(out, w)
		}
	}
	if len(out) == 0 {
		return strings.Join(words, " ")
	}
	return strings.Join(out, " ")
}

// normPhone keeps digits and compares the last 9 (Rwandan numbers: 0788…, +250788…, 250788…).
func normPhone(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	if len(d) < 7 {
		return ""
	}
	if len(d) > 9 {
		d = d[len(d)-9:]
	}
	return d
}

func normTIN(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	if b.Len() < 5 {
		return ""
	}
	return b.String()
}

func bigrams(s string) map[string]int {
	s = strings.ReplaceAll(s, " ", "")
	rs := []rune(s)
	m := map[string]int{}
	if len(rs) == 1 {
		m[s]++
	}
	for i := 0; i+1 < len(rs); i++ {
		m[string(rs[i:i+2])]++
	}
	return m
}

// nameSimilarity: max of the Sørensen–Dice coefficient on character bigrams and token overlap (0..1).
func nameSimilarity(a, b string) float64 {
	a, b = normName(a), normName(b)
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	ba, bb := bigrams(a), bigrams(b)
	var inter, na, nb int
	for k, v := range ba {
		na += v
		if w, ok := bb[k]; ok {
			inter += min(v, w)
		}
	}
	for _, v := range bb {
		nb += v
	}
	dice := 0.0
	if na+nb > 0 {
		dice = 2 * float64(inter) / float64(na+nb)
	}
	ta, tb := strings.Fields(a), strings.Fields(b)
	set := map[string]bool{}
	for _, t := range ta {
		set[t] = true
	}
	common := 0
	for _, t := range tb {
		if set[t] {
			common++
			delete(set, t)
		}
	}
	tok := 0.0
	if n := max(len(ta), len(tb)); n > 0 {
		tok = float64(common) / float64(n)
	}
	return max(dice, tok)
}

// dupProbe is the client being created/edited; dupCandidate an existing client.
type dupProbe struct {
	Name   string
	TIN    string
	Phones []string
}

type dupCandidate struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	TIN    *string  `json:"tin"`
	Phone  *string  `json:"phone"`
	Status string   `json:"status"`
	Phones []string `json:"-"`
	Score  int      `json:"score"`
	Match  []string `json:"match"` // "tin" | "phone" | "name"
}

// Duplicate thresholds.
const (
	dupNameThreshold = 0.72 // names at least this similar are flagged
	dupMinScore      = 60
	dupMaxResults    = 5
)

// scoreDuplicate: same TIN → 100, same phone → 90, otherwise the name similarity (×100) when ≥ dupNameThreshold.
// Several reasons add a small bonus (capped at 100).
func scoreDuplicate(p dupProbe, c dupCandidate) (int, []string) {
	score := 0
	var why []string
	if t := normTIN(p.TIN); t != "" && c.TIN != nil && normTIN(*c.TIN) == t {
		score = 100
		why = append(why, "tin")
	}
	phones := map[string]bool{}
	for _, ph := range c.Phones {
		if n := normPhone(ph); n != "" {
			phones[n] = true
		}
	}
	for _, ph := range p.Phones {
		if n := normPhone(ph); n != "" && phones[n] {
			score = max(score, 90)
			why = append(why, "phone")
			break
		}
	}
	if sim := nameSimilarity(p.Name, c.Name); sim >= dupNameThreshold {
		s := int(sim*100 + 0.5)
		if len(why) > 0 {
			score = min(100, score+5)
		} else {
			score = s
		}
		why = append(why, "name")
	}
	return score, why
}

// findDuplicates ranks candidates (best first), excluding selfID, keeping those scoring ≥ dupMinScore.
func findDuplicates(p dupProbe, all []dupCandidate, selfID string) []dupCandidate {
	out := []dupCandidate{}
	for _, c := range all {
		if c.ID == selfID {
			continue
		}
		s, why := scoreDuplicate(p, c)
		if s >= dupMinScore {
			c.Score, c.Match = s, why
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > dupMaxResults {
		out = out[:dupMaxResults]
	}
	return out
}
