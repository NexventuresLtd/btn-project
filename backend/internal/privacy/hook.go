package privacy

import (
	"strings"

	"btn-ops/internal/deps"
	"btn-ops/internal/export"

	"github.com/gofiber/fiber/v2"
)

// ExportSubjectType derives the subject type of a bulk export from its report name ("" = not personal data).
func ExportSubjectType(report string) string {
	r := strings.ToLower(report)
	switch {
	case strings.Contains(r, "customer"), strings.Contains(r, "statement"), strings.Contains(r, "receivable"):
		return TypeCustomer
	case strings.Contains(r, "worker"), strings.Contains(r, "payroll"), strings.Contains(r, "wage"),
		strings.Contains(r, "attendance"), strings.Contains(r, "labour"), strings.Contains(r, "labor"):
		return TypeWorker
	}
	return ""
}

// PersonalDataExport logs a bulk export of personal data (one access-log row, action "export", no subject id).
// It is the function the export framework calls through its PersonalDataHook.
func PersonalDataExport(d *deps.Deps) func(c *fiber.Ctx, report string, rows int) {
	return func(c *fiber.Ctx, report string, rows int) {
		typ := ExportSubjectType(report)
		if typ == "" {
			return
		}
		e := entryFrom(c, typ, "", ActionExport)
		n := rows
		e.rows = &n
		c.Locals(loggedKey, true)
		logAsync(d, e)
	}
}

// registerExportHook wires PersonalDataExport into internal/export when that package is present.
func registerExportHook(d *deps.Deps) { export.PersonalDataHook = PersonalDataExport(d) }
