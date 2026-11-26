// Package syncsvc is the offline sync server (SRS §5, §6.2 Sync Ledger, Appendix A): idempotent batch push with
// per-record results, per-device sequence ordering, clock-skew flags, re-validation against the role at capture
// time and the current rules, conflicts + review queue, delta pull and the admin sync monitor.
//
// Phase-2 owners plug in with two calls from their module's Register (or an init func):
//
//	syncsvc.RegisterHandler("production_entry", h, syncsvc.Requires("production.record"))
//	syncsvc.RegisterPull("production_entries", pullFn, syncsvc.PullRequires("production.view"))
//
// See backend/README.md § "Shared services → syncsvc" for a worked example.
package syncsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"

	"github.com/jackc/pgx/v5"
)

// Status of one pushed record.
type Status string

const (
	Accepted   Status = "accepted"
	Duplicate  Status = "duplicate"
	Conflicted Status = "conflict"
	Rejected   Status = "rejected"
)

// Flags persisted on ledger rows.
const (
	FlagClockSkew     = "CLOCK_SKEW"
	FlagSeqGap        = "SEQ_GAP"
	FlagSeqLate       = "SEQ_LATE"
	FlagNegativeStock = "NEGATIVE_STOCK"
	FlagRevokedDevice = "REVOKED_DEVICE" // accepted in the final upload of a device revoked while offline (T-08)
)

// Reason codes of rejected / conflict / duplicate results.
const (
	ReasonUnknownType       = "unknown_type"
	ReasonInvalidRecord     = "invalid_record"
	ReasonPayloadTooLarge   = "payload_too_large"
	ReasonValidation        = "validation"
	ReasonPermissionDenied  = "permission_denied"  // role at capture time lacked the permission
	ReasonPermissionRevoked = "permission_revoked" // current role lacks it
	ReasonSiteScope         = "site_scope"
	ReasonNotFound          = "not_found"
	ReasonConflict          = "conflict"
	ReasonStaleRevision     = "stale_revision"
	ReasonAlreadyAccepted   = "already_accepted"
	ReasonHandlerError      = "handler_error"
	ReasonDeviceRevoked     = "device_revoked" // captured after an administrator revoked the device
)

// Record is one client record as received (OFF-06).
type Record struct {
	UUID          string
	Revision      int
	DeviceID      string // client device id
	UserID        string
	Seq           int64
	Type          string
	SchemaVersion int
	CapturedAt    time.Time // UTC, as reported by the device
	ReceivedAt    time.Time
	Payload       json.RawMessage
	Flags         []string // flags already raised by the framework (e.g. CLOCK_SKEW)
	// Force is true when a conflict is being resolved with keep_client/merge: the handler must overwrite the
	// server version instead of reporting a conflict again.
	Force bool
}

// Decode unmarshals the payload into dst.
func (r Record) Decode(dst any) error { return json.Unmarshal(r.Payload, dst) }

// Body returns the payload as an httpx validator (same rules and messages as online endpoints).
// Messages follow lang (pass actor.Lang); v.Err() → FromError gives a "validation" rejection with field errors.
func (r Record) Body(lang i18n.Lang) (*httpx.V, error) {
	m := map[string]any{}
	if err := json.Unmarshal(r.Payload, &m); err != nil {
		return nil, err
	}
	return httpx.NewV(m, lang), nil
}

// HasFlag reports whether the framework already flagged the record.
func (r Record) HasFlag(f string) bool {
	for _, x := range r.Flags {
		if x == f {
			return true
		}
	}
	return false
}

// Actor is the user pushing/pulling, with both the current role and the role recorded at capture time (OFF-16).
type Actor struct {
	UserID      string
	Name        string
	Email       string
	Role        auth.Role // current
	PlantID     string    // current
	Captured    auth.PermSnapshot
	DeviceID    string // client device id
	DeviceRowID string
	IP          string
	Lang        i18n.Lang
	// DB is the pull transaction (repeatable read, read only) for pull functions; nil in handlers (use tx).
	DB db.DBTX

	finalUpload bool // the device was revoked; this is its last upload (records are flagged REVOKED_DEVICE)
}

// User returns the current identity as an *auth.User (for auth.PlantFilter / AssertPlantAccess helpers).
func (a Actor) User() *auth.User {
	return &auth.User{ID: a.UserID, Name: a.Name, Email: a.Email, Role: a.Role, PlantID: a.PlantID, DeviceID: a.DeviceID, DeviceRowID: a.DeviceRowID}
}

// Can is true when the permission is held now AND was held at capture time.
func (a Actor) Can(p auth.Permission) bool {
	return auth.Can(a.Role, p) && a.CapturedCan(p)
}

// CapturedCan reports whether the capture-time snapshot held p.
func (a Actor) CapturedCan(p auth.Permission) bool {
	if a.Captured.Role == auth.RoleSuperAdmin {
		return true
	}
	for _, x := range a.Captured.Permissions {
		if x == string(p) {
			return true
		}
	}
	return false
}

// ScopedPlantID is the plant a site-scoped actor is pinned to ("" = all), using the current assignment.
func (a Actor) ScopedPlantID() string { return auth.ScopedPlantID(a.User()) }

// AssertPlant returns a rejected Result when plantID is outside the actor's site scope now or at capture time.
func (a Actor) AssertPlant(plantID string) *Result {
	if s := a.ScopedPlantID(); s != "" && s != plantID {
		r := Reject(ReasonSiteScope, i18n.T(a.Lang, "error.plant_scope"))
		return &r
	}
	if auth.UserPlantScoped(a.Captured.Role, a.Captured.PlantID) && a.Captured.PlantID != plantID {
		r := Reject(ReasonSiteScope, i18n.T(a.Lang, "error.plant_scope"))
		return &r
	}
	return nil
}

// Audit is the audit actor for records applied from this device.
func (a Actor) Audit() audit.Actor {
	return audit.Actor{UserID: a.UserID, IP: a.IP, DeviceID: a.DeviceID}
}

// Result is a handler's verdict on one record.
type Result struct {
	Status        Status            `json:"status"`
	ReasonCode    string            `json:"reasonCode,omitempty"`
	Reason        string            `json:"reason,omitempty"`
	Fields        map[string]string `json:"fields,omitempty"`
	ServerID      string            `json:"serverId,omitempty"`
	ServerVersion int64             `json:"serverVersion,omitempty"`
	Flags         []string          `json:"flags"`
	ConflictID    string            `json:"conflictId,omitempty"`
	// OriginalStatus is set on duplicates: the outcome of the first submission.
	OriginalStatus string `json:"originalStatus,omitempty"`
	// PlantID tags the ledger row with the site the record belongs to (monitor per site). Optional.
	PlantID string `json:"-"`
	// Entity/EntityID name the server record for review items (NEGATIVE_STOCK). Optional.
	Entity   string `json:"-"`
	EntityID string `json:"-"`
}

// Accept is an accepted result; flags such as FlagNegativeStock route the record to the review queue.
func Accept(serverID string, serverVersion int64, flags ...string) Result {
	return Result{Status: Accepted, ServerID: serverID, ServerVersion: serverVersion, Flags: flags}
}

// Reject is a rejected result with a reason code from the Reason* list and a human message.
func Reject(code, reason string) Result {
	return Result{Status: Rejected, ReasonCode: code, Reason: reason}
}

// FromError maps an error from shared validation/business code to a rejected result (400 → validation,
// 403 → permission/site, 404 → not_found, 409 → conflict). Other errors are returned as handler_error.
func FromError(lang i18n.Lang, err error) Result {
	if e, ok := err.(*httpx.Error); ok {
		msg := e.Message
		if e.Key != "" {
			msg = i18n.T(lang, e.Key, e.Args...)
		}
		r := Reject(ReasonHandlerError, msg)
		r.Fields = e.Fields
		switch {
		case e.Status == 400:
			r.ReasonCode = ReasonValidation
		case e.Status == 403 && e.Key == "error.plant_scope":
			r.ReasonCode = ReasonSiteScope
		case e.Status == 403:
			r.ReasonCode = ReasonPermissionRevoked
		case e.Status == 404:
			r.ReasonCode = ReasonNotFound
		case e.Status == 409:
			r.ReasonCode = ReasonConflict
		}
		return r
	}
	return Reject(ReasonHandlerError, err.Error())
}

// Handler applies one record inside its own savepoint. Whatever it writes is rolled back unless it returns
// Accepted or Conflict. Audit with audit.Log(ctx, tx, actor.Audit(), …) inside tx.
type Handler interface {
	Apply(ctx context.Context, tx pgx.Tx, rec Record, actor Actor) Result
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, tx pgx.Tx, rec Record, actor Actor) Result

// Apply implements Handler.
func (f HandlerFunc) Apply(ctx context.Context, tx pgx.Tx, rec Record, actor Actor) Result {
	return f(ctx, tx, rec, actor)
}

type handlerEntry struct {
	h                    Handler
	perms                []auth.Permission
	schemaMin, schemaMax int // supported record schemaVersions (0 = 1..1, see SchemaVersions)
}

// HandlerOption configures a registration.
type HandlerOption func(*handlerEntry)

// Requires makes the framework reject records unless the actor held ANY of perms at capture time
// (permission_denied) and still holds it now (permission_revoked).
func Requires(perms ...auth.Permission) HandlerOption {
	for _, p := range perms {
		if !auth.Known(p) {
			panic("syncsvc: unknown permission " + string(p))
		}
	}
	return func(e *handlerEntry) { e.perms = append(e.perms, perms...) }
}

var (
	regMu     sync.RWMutex
	handlers  = map[string]handlerEntry{}
	pulls     = map[string]pullEntry{}
	snapshots = map[string]pullEntry{}
)

// RegisterHandler registers the handler of a record type (e.g. "production_entry"). Calling it again for the same
// type replaces the registration (module Register functions run once per app build, e.g. in every test).
func RegisterHandler(recordType string, h Handler, opts ...HandlerOption) {
	regMu.Lock()
	defer regMu.Unlock()
	e := handlerEntry{h: h}
	for _, o := range opts {
		o(&e)
	}
	handlers[recordType] = e
}

// UnregisterHandler removes a handler (tests).
func UnregisterHandler(recordType string) {
	regMu.Lock()
	defer regMu.Unlock()
	delete(handlers, recordType)
}

func handlerFor(t string) (handlerEntry, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	e, ok := handlers[t]
	return e, ok
}

// RecordTypes lists the registered record types (sorted).
func RecordTypes() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(handlers))
	for k := range handlers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ── review hook (OFF-18) ─────────────────────────────────────────────────────

// ReviewItem is a record routed to the review queue (negative stock, conflicts).
type ReviewItem struct {
	ID, Kind, Type, PlantID, UserID, DeviceID, RecordUUID, Entity, EntityID, Reason string
}

// OnReview, when set, is called inside the push transaction for every new conflict / review item (e.g. to
// notify sync.resolve holders of the site). Errors are logged, never fail the batch.
var OnReview func(ctx context.Context, tx pgx.Tx, item ReviewItem) error

func joinFlags(a []string, b ...string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, f := range append(append([]string{}, a...), b...) {
		f = strings.TrimSpace(strings.ToUpper(f))
		if f != "" && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

func errf(format string, a ...any) error { return fmt.Errorf("syncsvc: "+format, a...) }
