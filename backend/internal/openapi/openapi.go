// Package openapi serves the API version alias (/api/v1 → /api) and an OpenAPI 3.1 document generated from
// Fiber's live route table plus a light metadata registry (SRS §9.2).
//
//	GET /api/v1/openapi.json   the document (signed-in users)
//	GET /api/v1/docs           Redoc rendering of it
//
// Every registered route appears automatically: path, method, path parameters, tag (first path segment),
// required permissions (read from auth.RequirePermission handlers, incl. group middleware), CSRF header for
// mutations, the common error envelope and, for report endpoints registered with internal/export, the
// ?format= parameter and the /export sibling route. Add a summary/description/query params with Describe:
//
//	openapi.Describe("GET", "/stock/movements", openapi.Op{Summary: "Finished-goods movement ledger", Query: openapi.ListParams})
package openapi

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"

	"btn-ops/internal/auth"
	"btn-ops/internal/export"

	"github.com/gofiber/fiber/v2"
)

// Version is the current API major version served under /api/v1 (and its alias /api).
const Version = "v1"

// Prefix is the canonical, versioned base path.
const Prefix = "/api/" + Version

// VersionAlias rewrites /api/v1/... to /api/... before routing, so every route is served under both
// prefixes by the same handlers. Mount it on the app before the /api group.
func VersionAlias() fiber.Handler {
	return func(c *fiber.Ctx) error {
		p := c.Path()
		if p == Prefix || strings.HasPrefix(p, Prefix+"/") {
			c.Locals("apiVersion", Version)
			c.Set("API-Version", Version)
			c.Path("/api" + strings.TrimPrefix(p, Prefix))
		}
		return c.Next()
	}
}

// Param is a query (or path) parameter.
type Param struct {
	Name        string
	Description string
	Type        string // string (default) | integer | number | boolean
	Format      string // date, date-time, …
	Enum        []string
	Required    bool
}

// Op is the optional metadata of one operation.
type Op struct {
	Summary     string
	Description string
	Query       []Param
	RequestBody string // short description of the JSON body (mutations)
	Response    string // short description of the 2xx response
	Public      bool   // no session required
}

var (
	metaMu sync.RWMutex
	meta   = map[string]Op{}
)

// Describe attaches metadata to METHOD path (path relative to /api, Fiber syntax: "/sales/:id").
func Describe(method, path string, op Op) {
	metaMu.Lock()
	defer metaMu.Unlock()
	meta[strings.ToUpper(method)+" "+normalize(path)] = op
}

func normalize(p string) string { return "/" + strings.Trim(p, "/") }

// Common parameter sets.
var (
	RangeParams = []Param{{Name: "from", Type: "string", Format: "date", Description: "Start date YYYY-MM-DD (default: first day of this month)"},
		{Name: "to", Type: "string", Format: "date", Description: "End date YYYY-MM-DD (default: today, Africa/Kigali)"}}
	PlantParam = Param{Name: "plantId", Description: "Site id; plant-scoped roles are pinned to their own site"}
	PageParams = []Param{{Name: "page", Type: "integer", Description: "Page number (≥ 1)"}, {Name: "pageSize", Type: "integer", Description: "Rows per page (1–200)"}}
	ListParams = append(append(append([]Param{}, RangeParams...), PlantParam), PageParams...)
)

var formatParam = Param{Name: "format", Enum: []string{"csv", "xlsx", "json"},
	Description: "Export the report: csv (UTF-8 BOM), xlsx (typed cells, DD/MM/YYYY) or json rows. Pagination is ignored (max 100 000 rows); filters apply."}

var paramRe = regexp.MustCompile(`:([A-Za-z0-9_]+)\??`)

var braceRe = regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)

// oasPath converts "/api/sales/:id" to "/api/v1/sales/{id}".
func oasPath(p string) string {
	rel := strings.TrimPrefix(p, "/api")
	rel = paramRe.ReplaceAllString(rel, "{$1}")
	rel = strings.ReplaceAll(rel, "*", "{wildcard}")
	if len(rel) > 1 {
		rel = strings.TrimRight(rel, "/") // StrictRouting is off: "/users/" and "/users" are the same route
	}
	return Prefix + rel
}

var methods = map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}

type useRoute struct {
	prefix string
	perms  []string
	super  bool
}

// Build generates the OpenAPI 3.1 document for app.
func Build(app *fiber.App) map[string]any {
	all := app.GetRoutes(false)
	plain := app.GetRoutes(true)
	key := func(r fiber.Route) string {
		var b strings.Builder
		b.WriteString(r.Method + " " + r.Path)
		for _, h := range r.Handlers {
			b.WriteString(fmt.Sprintf("|%x", reflect.ValueOf(h).Pointer()))
		}
		return b.String()
	}
	isPlain := map[string]int{}
	for _, r := range plain {
		isPlain[key(r)]++
	}
	var uses []useRoute
	for _, r := range all {
		k := key(r)
		if isPlain[k] > 0 {
			isPlain[k]--
			continue
		}
		if r.Method != "GET" { // use routes are registered once per method; GET is enough
			continue
		}
		for _, h := range r.Handlers {
			if perms, ok := auth.HandlerPermissions(h); ok {
				uses = append(uses, useRoute{prefix: r.Path, perms: perms})
			}
		}
	}

	paths := map[string]map[string]any{}
	tags := map[string]bool{}
	add := func(method, routePath string, params []string, handlers []fiber.Handler, extra *export.Report) {
		rel := strings.TrimPrefix(routePath, "/api")
		tag := strings.Split(strings.Trim(rel, "/"), "/")[0]
		if tag == "" {
			tag = "root"
		}
		tags[tag] = true
		metaMu.RLock()
		m := meta[method+" "+normalize(rel)]
		metaMu.RUnlock()
		var perms []string
		for _, h := range handlers {
			if p, ok := auth.HandlerPermissions(h); ok {
				perms = append(perms, p...)
			}
		}
		for _, u := range uses {
			if routePath == u.prefix || strings.HasPrefix(routePath, strings.TrimRight(u.prefix, "/")+"/") {
				perms = append(perms, u.perms...)
			}
		}
		public := m.Public || isPublic(rel)
		op := map[string]any{
			"operationId": operationID(method, rel),
			"tags":        []string{tag},
			"summary":     firstNonEmpty(m.Summary, defaultSummary(method, rel)),
			"responses":   responses(method, m, public),
		}
		desc := m.Description
		if len(perms) > 0 {
			op["x-permissions"] = perms
			desc = strings.TrimSpace(desc + "\n\nRequires any of the permissions: `" + strings.Join(perms, "`, `") + "` (Super Administrator holds all).")
		}
		var ps []map[string]any
		for _, p := range params {
			ps = append(ps, map[string]any{"name": p, "in": "path", "required": true, "schema": map[string]any{"type": "string"}})
		}
		q := m.Query
		if extra != nil {
			q = append(append([]Param{}, q...), formatParam)
			desc = strings.TrimSpace(desc + "\n\nExportable report: add `?format=csv|xlsx|json`, or call `" + oasPath(routePath) + "/export`.")
		}
		for _, p := range q {
			s := map[string]any{"type": firstNonEmpty(p.Type, "string")}
			if p.Format != "" {
				s["format"] = p.Format
			}
			if len(p.Enum) > 0 {
				s["enum"] = p.Enum
			}
			ps = append(ps, map[string]any{"name": p.Name, "in": "query", "required": p.Required, "description": p.Description, "schema": s})
		}
		if method != "GET" {
			ps = append(ps, map[string]any{"$ref": "#/components/parameters/CsrfHeader"})
			op["requestBody"] = map[string]any{"required": false, "description": firstNonEmpty(m.RequestBody, "JSON object"),
				"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object"}}}}
		}
		ps = append(ps, map[string]any{"$ref": "#/components/parameters/Lang"})
		op["parameters"] = ps
		if desc != "" {
			op["description"] = desc
		}
		if public {
			op["security"] = []any{}
		}
		p := oasPath(routePath)
		if paths[p] == nil {
			paths[p] = map[string]any{}
		}
		paths[p][strings.ToLower(method)] = op
	}

	seen := map[string]bool{}
	for _, r := range plain {
		if !methods[r.Method] || !strings.HasPrefix(r.Path, "/api/") || seen[r.Method+" "+r.Path] {
			continue
		}
		seen[r.Method+" "+r.Path] = true
		var rep *export.Report
		if r.Method == "GET" {
			rep = reportFor(r.Path)
		}
		add(r.Method, r.Path, r.Params, r.Handlers, rep)
		if rep != nil {
			add("GET", strings.TrimRight(r.Path, "/")+"/export", r.Params, r.Handlers, rep)
		}
	}

	// reports registered on a concrete path that Fiber serves through a param route (e.g. /reports/data/:key)
	for _, rep := range export.Reports() {
		full := "/api" + rep.Path
		if paths[oasPath(full)+"/export"] != nil {
			continue
		}
		var params []string
		for _, m := range paramRe.FindAllStringSubmatch(rep.Path, -1) {
			params = append(params, m[1])
		}
		r := rep
		add("GET", full+"/export", params, nil, &r)
	}

	tagList := []map[string]any{}
	names := make([]string, 0, len(tags))
	for t := range tags {
		names = append(names, t)
	}
	sort.Strings(names)
	for _, t := range names {
		tagList = append(tagList, map[string]any{"name": t})
	}
	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":   "BTN Operations API",
			"version": "1.0.0",
			"description": "REST API of the BTN Operations System (NXV-BTN-SRS-001 §9.2). Canonical base path `/api/v1`; `/api` is a " +
				"permanent alias. Authentication: the httpOnly session cookie set by `/api/v1/auth/*` (browser) — mutations must also send " +
				"`X-Requested-With: btn-ops` from an allowed origin (CSRF). Errors always use the envelope " +
				"`{\"error\":{\"code\",\"message\",\"fields\"?}}` with messages in the request language (`?lang=`, user preference, `Accept-Language`). " +
				"Dates are `YYYY-MM-DD` business dates (Africa/Kigali), timestamps are UTC ISO-8601, money is whole RWF. " +
				"Lists return `{items,total,page,pageSize}`. Rate limits answer 429 with `Retry-After`.",
		},
		"servers":    []any{map[string]any{"url": "/", "description": "This server"}},
		"tags":       tagList,
		"paths":      paths,
		"components": components(),
		"security":   []any{map[string]any{"session": []any{}}},
	}
}

func reportFor(path string) *export.Report {
	rel := normalize(strings.TrimPrefix(path, "/api"))
	for _, r := range export.Reports() {
		if strings.EqualFold(r.Path, rel) {
			r := r
			return &r
		}
	}
	return nil
}

func isPublic(rel string) bool {
	return rel == "/health" || strings.HasPrefix(rel, "/public/") || (strings.HasPrefix(rel, "/auth/") && rel != "/auth/me" && rel != "/auth/language" && rel != "/auth/password/change")
}

func responses(method string, m Op, public bool) map[string]any {
	okCode := "200"
	if method == "POST" {
		okCode = "2XX"
	}
	r := map[string]any{
		okCode: map[string]any{"description": firstNonEmpty(m.Response, "Success"), "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{}}}},
		"400":  map[string]any{"$ref": "#/components/responses/BadRequest"},
		"404":  map[string]any{"$ref": "#/components/responses/NotFound"},
		"429":  map[string]any{"$ref": "#/components/responses/TooMany"},
		"500":  map[string]any{"$ref": "#/components/responses/Internal"},
	}
	if !public {
		r["401"] = map[string]any{"$ref": "#/components/responses/Unauthorized"}
		r["403"] = map[string]any{"$ref": "#/components/responses/Forbidden"}
	}
	if method != "GET" {
		r["409"] = map[string]any{"$ref": "#/components/responses/Conflict"}
	}
	return r
}

func components() map[string]any {
	errResp := func(desc string) map[string]any {
		return map[string]any{"description": desc, "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/Error"}}}}
	}
	return map[string]any{
		"securitySchemes": map[string]any{
			"session": map[string]any{"type": "apiKey", "in": "cookie", "name": auth.SessionCookie, "description": "Session cookie set at sign-in (password + OTP or passkey)."},
		},
		"parameters": map[string]any{
			"CsrfHeader": map[string]any{"name": "X-Requested-With", "in": "header", "required": true, "schema": map[string]any{"type": "string", "const": "btn-ops"},
				"description": "Required on every non-GET request (CSRF guard)."},
			"Lang": map[string]any{"name": "lang", "in": "query", "required": false, "schema": map[string]any{"type": "string", "enum": []string{"en", "am", "rw", "fr"}},
				"description": "Response language for messages, PDFs and exports."},
		},
		"schemas": map[string]any{
			"Error": map[string]any{
				"type": "object", "required": []string{"error"},
				"properties": map[string]any{"error": map[string]any{
					"type": "object", "required": []string{"code", "message"},
					"properties": map[string]any{
						"code":    map[string]any{"type": "string", "examples": []string{"bad_request", "unauthorized", "forbidden", "not_found", "conflict", "too_many_requests", "internal"}},
						"message": map[string]any{"type": "string", "description": "Human-readable, in the request language"},
						"fields":  map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "Per-field validation messages (dotted paths, e.g. lines.0.qty)"},
					},
				}},
			},
			"Page": map[string]any{"type": "object", "properties": map[string]any{
				"items": map[string]any{"type": "array", "items": map[string]any{}}, "total": map[string]any{"type": "integer"},
				"page": map[string]any{"type": "integer"}, "pageSize": map[string]any{"type": "integer"},
			}},
		},
		"responses": map[string]any{
			"BadRequest":   errResp("Validation failed (`fields` holds per-field messages) or malformed JSON"),
			"Unauthorized": errResp("Not signed in, session expired or device revoked"),
			"Forbidden":    errResp("Missing permission, other site, CSRF header/origin missing, or password change required"),
			"NotFound":     errResp("Record or endpoint not found"),
			"Conflict":     errResp("Duplicate or state conflict (e.g. already approved, in use)"),
			"TooMany":      errResp("Rate limited; see Retry-After"),
			"Internal":     errResp("Unexpected error (details are logged with the request id, never returned)"),
		},
	}
}

func operationID(method, rel string) string {
	rel = paramRe.ReplaceAllString(rel, "by_$1")
	parts := strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '-' || r == '.' || r == '*' })
	return strings.ToLower(method) + "_" + strings.Join(parts, "_")
}

func defaultSummary(method, rel string) string {
	words := strings.FieldsFunc(paramRe.ReplaceAllString(rel, ""), func(r rune) bool { return r == '/' || r == '-' })
	verb := map[string]string{"GET": "Get", "POST": "Create / run", "PUT": "Replace", "PATCH": "Update", "DELETE": "Delete"}[method]
	return verb + " " + strings.Join(words, " ")
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// Register mounts GET /openapi.json and /docs on r (the /api group; reachable as /api/v1/...).
func Register(r fiber.Router, app *fiber.App) {
	var once sync.Once
	var doc []byte
	r.Get("/openapi.json", func(c *fiber.Ctx) error {
		once.Do(func() { doc, _ = json.Marshal(Build(app)) })
		c.Set(fiber.HeaderContentType, "application/json; charset=utf-8")
		c.Set(fiber.HeaderCacheControl, "private, max-age=300")
		return c.Send(doc)
	})
	r.Get("/docs", func(c *fiber.Ctx) error {
		c.Set(fiber.HeaderContentType, "text/html; charset=utf-8")
		c.Set("Content-Security-Policy", "default-src 'self'; script-src 'self' https://cdn.jsdelivr.net 'unsafe-inline'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src https://fonts.gstatic.com data:; img-src 'self' data: https:; worker-src blob:; connect-src 'self'")
		return c.SendString(docsHTML)
	})
	Describe("GET", "/openapi.json", Op{Summary: "This OpenAPI 3.1 document"})
	Describe("GET", "/docs", Op{Summary: "Interactive API reference (Redoc)", Response: "HTML page"})
}

// Redoc 2.1.5 from jsDelivr (pinned version + SRI hash would be added when vendoring; the CSP limits scripts to jsDelivr).
const docsHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>BTN Operations API</title>
<style>body{margin:0;background:#fff}</style></head>
<body><redoc spec-url="openapi.json" hide-download-button="false" path-in-middle-panel="true"></redoc>
<script src="https://cdn.jsdelivr.net/npm/redoc@2.1.5/bundles/redoc.standalone.js"></script>
</body></html>`
