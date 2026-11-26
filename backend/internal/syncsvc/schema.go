package syncsvc

import (
	"strconv"
	"strings"

	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
)

// Sync API versioning (SRS §9.2 backward compatibility).
//
// Two version numbers travel with every sync call:
//   - the client's sync schema (wire) version, header X-Sync-Schema on push, pull and status. Clients from before
//     the header existed send none and count as version 1.
//   - each record's schemaVersion (its payload shape), checked against the range its record type supports
//     (RegisterHandler option SchemaVersions; default 1..1).
//
// A version below the supported minimum answers 426 schema_upgrade_required: the client must update before it
// syncs again (nothing of the batch is stored, so the records stay pending on the device and are sent again by the
// updated client). A version above the server's answers 409 schema_too_new: the server is older than the client
// (e.g. during a deployment) and the client retries later. Versions in between keep working unchanged.
const SchemaHeader = "X-Sync-Schema"

// Supported sync schema (wire) versions. Raise Current when the wire shape changes in a compatible way; raise Min
// only when the oldest clients can no longer be served. Variables so tests can narrow the range.
var (
	SchemaMin     = 1
	SchemaCurrent = 2
)

// Error codes of version mismatches.
const (
	CodeSchemaUpgradeRequired = "schema_upgrade_required"
	CodeSchemaTooNew          = "schema_too_new"
)

// SchemaInfo is the supported range, returned by /sync/status and in version errors.
type SchemaInfo struct {
	Min     int `json:"min"`
	Current int `json:"current"`
}

func schemaInfo() SchemaInfo { return SchemaInfo{Min: SchemaMin, Current: SchemaCurrent} }

func upgradeRequired(details map[string]string) error {
	e := httpx.NewKey(426, CodeSchemaUpgradeRequired, "sync.schema_upgrade_required", "min", SchemaMin)
	e.Fields = details
	return e
}

func tooNew(details map[string]string) error {
	e := httpx.NewKey(409, CodeSchemaTooNew, "sync.schema_too_new", "current", SchemaCurrent)
	e.Fields = details
	return e
}

// clientSchema reads the client's wire version (absent = 1) and rejects unsupported ones.
func clientSchema(c *fiber.Ctx) (int, error) {
	raw := strings.TrimSpace(c.Get(SchemaHeader))
	v := 1
	if raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return 0, httpx.NewKey(400, "bad_schema_version", "sync.schema_invalid")
		}
		v = n
	}
	details := map[string]string{"schemaVersion": strconv.Itoa(v), "min": strconv.Itoa(SchemaMin), "current": strconv.Itoa(SchemaCurrent)}
	if v < SchemaMin {
		return v, upgradeRequired(details)
	}
	if v > SchemaCurrent {
		return v, tooNew(details)
	}
	return v, nil
}

// SchemaVersions declares the record schemaVersions a handler accepts (default 1..1). Raise max when the handler
// learns a new payload shape and keep min as long as the old shape is still handled (backward compatible).
func SchemaVersions(min, max int) HandlerOption {
	if min < 1 || max < min {
		panic("syncsvc: bad schema version range")
	}
	return func(e *handlerEntry) { e.schemaMin, e.schemaMax = min, max }
}

func (e handlerEntry) schemaRange() (int, int) {
	if e.schemaMin == 0 {
		return 1, 1
	}
	return e.schemaMin, e.schemaMax
}

// RecordSchemaRange returns the schemaVersions accepted for a record type (ok=false: type not registered).
func RecordSchemaRange(recordType string) (min, max int, ok bool) {
	e, ok := handlerFor(recordType)
	if !ok {
		return 0, 0, false
	}
	min, max = e.schemaRange()
	return min, max, true
}

// checkRecordSchemas rejects the whole batch when a record of a known type has an unsupported schemaVersion.
func checkRecordSchemas(records []pushRecord) error {
	for _, r := range records {
		e, ok := handlerFor(strings.TrimSpace(r.Type))
		if !ok {
			continue // unknown types are rejected per record (unknown_type)
		}
		sv := r.SchemaVersion
		if sv == 0 {
			sv = 1
		}
		lo, hi := e.schemaRange()
		details := map[string]string{"type": r.Type, "schemaVersion": strconv.Itoa(sv), "min": strconv.Itoa(lo), "max": strconv.Itoa(hi)}
		if sv < lo {
			return upgradeRequired(details)
		}
		if sv > hi {
			return tooNew(details)
		}
	}
	return nil
}
