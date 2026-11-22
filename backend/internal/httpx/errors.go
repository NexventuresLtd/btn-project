// Package httpx contains the Fiber plumbing shared by every module: error envelope,
// JSON helpers, request validation, pagination, middleware and caching helpers.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"btn-ops/internal/db"
	"btn-ops/internal/i18n"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// Error is thrown (returned) from handlers and rendered as the Node-compatible envelope
// {"error":{"code","message","fields"?}}.
type Error struct {
	Status  int
	Code    string
	Message string // literal English message (used when Key is empty / unknown)
	Key     string // optional i18n key; the message is looked up in the request language
	Args    []any
	Fields  map[string]string
	Headers map[string]string
}

func (e *Error) Error() string { return e.Message }

// WithKey attaches an i18n key + args (the literal Message stays as English fallback).
func (e *Error) WithKey(key string, args ...any) *Error { e.Key, e.Args = key, args; return e }

// New builds an arbitrary error.
func New(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// NewKey builds an error whose message is the catalog entry for key.
func NewKey(status int, code, key string, args ...any) *Error {
	return &Error{Status: status, Code: code, Message: i18n.T(i18n.EN, key, args...), Key: key, Args: args}
}

// BadRequest: message is a literal or an i18n key. fields may be nil.
func BadRequest(message string, fields map[string]string) *Error {
	return &Error{Status: 400, Code: "bad_request", Message: tmsg(message), Key: keyOf(message), Fields: fields}
}
func Unauthorized(message ...string) *Error {
	if len(message) > 0 {
		return &Error{Status: 401, Code: "unauthorized", Message: tmsg(message[0]), Key: keyOf(message[0])}
	}
	return NewKey(401, "unauthorized", "error.unauthorized")
}
func Forbidden(message ...string) *Error {
	if len(message) > 0 {
		return &Error{Status: 403, Code: "forbidden", Message: tmsg(message[0]), Key: keyOf(message[0])}
	}
	return NewKey(403, "forbidden", "error.forbidden")
}

// NotFound: what is a literal ("User") or an entity key ("entity.user").
func NotFound(what ...string) *Error {
	w := "entity.record"
	if len(what) > 0 {
		w = what[0]
	}
	return &Error{Status: 404, Code: "not_found", Message: i18n.T(i18n.EN, "error.not_found", "what", i18n.T(i18n.EN, w)), Key: "error.not_found", Args: []any{"what", w}}
}
func Conflict(message string) *Error {
	return &Error{Status: 409, Code: "conflict", Message: tmsg(message), Key: keyOf(message)}
}
func TooMany(message string) *Error {
	return &Error{Status: 429, Code: "too_many_requests", Message: tmsg(message), Key: keyOf(message)}
}

// tmsg resolves an i18n key to English, or passes a literal through.
func tmsg(m string) string { return i18n.T(i18n.EN, m) }
func keyOf(m string) string {
	if i18n.Has(m) {
		return m
	}
	return ""
}

// Lang returns the language resolved for this request (set by the language middleware).
func Lang(c *fiber.Ctx) i18n.Lang {
	if l, ok := c.Locals("lang").(i18n.Lang); ok {
		return l
	}
	return i18n.Default
}

// ErrorHandler is the fiber.Config.ErrorHandler. It renders the envelope and logs 5xx.
func ErrorHandler(c *fiber.Ctx, err error) error {
	lang := Lang(c)
	var e *Error
	var fe *fiber.Error
	var se *json.SyntaxError
	var ute *json.UnmarshalTypeError
	switch {
	case errors.As(err, &e):
	case errors.As(err, &fe):
		switch fe.Code {
		case 404:
			e = NotFound("entity.endpoint")
		case 405:
			e = New(405, "method_not_allowed", "Method not allowed")
		case 413:
			e = NewKey(413, "payload_too_large", "error.too_large")
		case 408:
			e = New(408, "timeout", "Request timed out")
		default:
			e = New(fe.Code, "error", fe.Message)
		}
	case errors.As(err, &se), errors.As(err, &ute):
		e = NewKey(400, "bad_request", "error.malformed_json")
	case db.IsNoRows(err) || errors.Is(err, pgx.ErrNoRows):
		e = NotFound()
	default:
		if pe := db.PgError(err); pe != nil {
			switch pe.Code {
			case "23505":
				e = NewKey(409, "conflict", "error.conflict_exists", "field", constraintTarget(pe.Detail))
			case "23503":
				e = NewKey(409, "conflict", "error.in_use")
			case "23514", "22P02", "22003", "22007", "22008":
				e = New(400, "bad_request", "Invalid input")
			}
		}
	}
	if e == nil {
		slog.Error("unhandled error", "err", err, "path", c.Path(), "method", c.Method(), "request_id", c.GetRespHeader("X-Request-Id"))
		e = NewKey(500, "internal", "error.internal")
	}
	msg := e.Message
	if e.Key != "" && lang != i18n.EN {
		args := make([]any, len(e.Args))
		copy(args, e.Args)
		for i := 0; i+1 < len(args); i += 2 { // translate entity-name arguments
			if s, ok := args[i+1].(string); ok && strings.HasPrefix(s, "entity.") {
				args[i+1] = i18n.T(lang, s)
			}
		}
		msg = i18n.T(lang, e.Key, args...)
	}
	for k, v := range e.Headers {
		c.Set(k, v)
	}
	body := map[string]any{"code": e.Code, "message": msg}
	if len(e.Fields) > 0 {
		body["fields"] = e.Fields
	}
	return c.Status(e.Status).JSON(fiber.Map{"error": body})
}

// constraintTarget extracts "email" from `Key (email)=(x) already exists.`
func constraintTarget(detail string) string {
	if i := strings.Index(detail, "("); i >= 0 {
		if j := strings.Index(detail[i:], ")"); j > 0 {
			return strings.ReplaceAll(detail[i+1:i+j], `"`, "")
		}
	}
	return "value"
}

// Errorf is a convenience for 500s that should not leak details.
func Errorf(format string, a ...any) error { return fmt.Errorf(format, a...) }
