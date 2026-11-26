package httpx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

const maxJSONBody = 1 << 20 // 1 MB, like express.json()

// Ctx is the request context handlers must pass to DB calls (carries the request timeout).
func Ctx(c *fiber.Ctx) context.Context { return c.UserContext() }

// DecodeMap parses a JSON object body (numbers kept as json.Number). An empty body yields an empty map.
func DecodeMap(c *fiber.Ctx) (map[string]any, error) {
	body := c.Body()
	if len(body) > maxJSONBody {
		return nil, NewKey(413, "payload_too_large", "error.too_large")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var raw any
	if err := dec.Decode(&raw); err != nil {
		return nil, NewKey(400, "bad_request", "error.malformed_json")
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, NewKey(400, "bad_request", "error.malformed_json")
	}
	return m, nil
}

// Body decodes the JSON body and returns a validator over it.
func Body(c *fiber.Ctx) (*V, error) {
	m, err := DecodeMap(c)
	if err != nil {
		return nil, err
	}
	return NewV(m, Lang(c)), nil
}

// Query returns a validator over the query string (all values are strings; use .Coerce()).
func Query(c *fiber.Ctx) *V {
	q := c.Queries()
	m := make(map[string]any, len(q))
	for k, v := range q {
		m[k] = v
	}
	return NewV(m, Lang(c))
}

// BindJSON decodes the body straight into a struct (for payloads that need no zod-style messages).
func BindJSON(c *fiber.Ctx, dst any) error {
	if len(c.Body()) > maxJSONBody {
		return NewKey(413, "payload_too_large", "error.too_large")
	}
	if err := json.Unmarshal(c.Body(), dst); err != nil {
		return NewKey(400, "bad_request", "error.malformed_json")
	}
	return nil
}

// OK sends v as JSON with status 200.
func OK(c *fiber.Ctx, v any) error { return c.JSON(v) }

// Created sends v as JSON with status 201.
func Created(c *fiber.Ctx, v any) error { return c.Status(201).JSON(v) }

// Page is the standard list envelope.
type Page[T any] struct {
	Items    []T `json:"items"`
	Total    int `json:"total"`
	Page     int `json:"page"`
	PageSize int `json:"pageSize"`
}

// NewPage builds a Page, turning a nil slice into [] (never null).
func NewPage[T any](items []T, total, page, pageSize int) Page[T] {
	if items == nil {
		items = []T{}
	}
	return Page[T]{Items: items, Total: total, Page: page, PageSize: pageSize}
}

// UnpagedLocal is set by internal/export while it runs a list endpoint for an export (value = row cap).
const UnpagedLocal = "httpx.unpaged"

// Pager is the parsed ?page=&pageSize=.
type Pager struct{ Page, PageSize, Offset, Limit int }

// Paginate mirrors Node pagination(): page>=1, pageSize 1..200 (default defaultSize).
func Paginate(c *fiber.Ctx, defaultSize int) Pager {
	if n, ok := c.Locals(UnpagedLocal).(int); ok && n > 0 { // export in progress: one page with every row (internal/export)
		return Pager{Page: 1, PageSize: n, Offset: 0, Limit: n}
	}
	if defaultSize <= 0 {
		defaultSize = 25
	}
	page, _ := strconv.Atoi(c.Query("page"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(c.Query("pageSize"))
	if size == 0 {
		size = defaultSize
	}
	if size < 1 {
		size = 1
	}
	if size > 200 {
		size = 200
	}
	return Pager{Page: page, PageSize: size, Offset: (page - 1) * size, Limit: size}
}

// JSONCached marshals v, adds a weak ETag + Cache-Control and answers 304 when If-None-Match matches.
// cacheControl e.g. "private, max-age=0, must-revalidate" or "public, max-age=60".
func JSONCached(c *fiber.Ctx, v any, cacheControl string) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return BytesCached(c, b, "application/json; charset=utf-8", cacheControl)
}

// BytesCached is JSONCached for pre-encoded bodies (cache the bytes, not the struct, on hot paths).
func BytesCached(c *fiber.Ctx, body []byte, contentType, cacheControl string) error {
	sum := sha256.Sum256(body)
	etag := `W/"` + hex.EncodeToString(sum[:12]) + `"`
	c.Set(fiber.HeaderETag, etag)
	c.Set(fiber.HeaderCacheControl, cacheControl)
	c.Vary("Accept-Encoding")
	if etagMatch(c.Get(fiber.HeaderIfNoneMatch), etag) {
		return c.SendStatus(fiber.StatusNotModified)
	}
	c.Set(fiber.HeaderContentType, contentType)
	return c.Send(body)
}

// ETagMatch reports whether the request's If-None-Match matches etag (for custom 304s, e.g. assets).
func ETagMatch(c *fiber.Ctx, etag string) bool {
	return etagMatch(c.Get(fiber.HeaderIfNoneMatch), etag)
}

func etagMatch(header, etag string) bool {
	if header == "" {
		return false
	}
	if header == "*" {
		return true
	}
	strip := func(s string) string {
		return strings.TrimPrefix(s, "W/")
	}
	want := strip(etag)
	for _, p := range strings.Split(header, ",") {
		if strip(strings.TrimSpace(p)) == want {
			return true
		}
	}
	return false
}

// Timing appends a Server-Timing metric ("db;dur=3.2").
func Timing(c *fiber.Ctx, name string, d time.Duration) {
	c.Append("Server-Timing", name+";dur="+strconv.FormatFloat(float64(d.Microseconds())/1000, 'f', 1, 64))
}
