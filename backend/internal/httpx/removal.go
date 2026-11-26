package httpx

import (
	"strings"
	"unicode/utf8"

	"btn-ops/internal/i18n"

	"github.com/gofiber/fiber/v2"
)

// RemovalReason reads the mandatory reason of a soft delete / void (SRS §6.3: records are never hard-deleted;
// deletion is a status change retained for audit). The reason comes from the JSON body {"reason": "…"} or,
// for clients that cannot send a DELETE body, from ?reason=. 3–500 characters, otherwise a 400 on "reason".
func RemovalReason(c *fiber.Ctx) (string, error) {
	m, err := DecodeMap(c)
	if err != nil {
		return "", err
	}
	reason := ""
	if s, ok := m["reason"].(string); ok {
		reason = s
	}
	if strings.TrimSpace(reason) == "" {
		reason = c.Query("reason")
	}
	reason = strings.TrimSpace(reason)
	if n := utf8.RuneCountInString(reason); n < 3 || n > 500 {
		return "", BadRequest("error.validation", map[string]string{"reason": i18n.T(Lang(c), "records.reason_required")})
	}
	return reason, nil
}
