package users

import (
	"strconv"
	"strings"
)

func trim(s string) string   { return strings.TrimSpace(s) }
func itoa(n int) string      { return strconv.Itoa(n) }
func join(s []string) string { return strings.Join(s, ", ") }

// escapeLike escapes % _ \ for LIKE/ILIKE patterns.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
