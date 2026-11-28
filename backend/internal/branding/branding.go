// Package branding serves the public branding / asset / i18n-override endpoints and the
// SUPER_ADMIN-only write endpoints.
package branding

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"btn-ops/assets"
	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/cache"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/ratelimit"
	"btn-ops/internal/settings"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

const maxAsset = 1 << 20 // 1 MB

// Public is the JSON returned by GET /api/public/branding.
type Public struct {
	Name            string             `json:"name"`
	LegalName       string             `json:"legalName"`
	PrimaryColor    string             `json:"primaryColor"`
	AccentColor     string             `json:"accentColor"`
	FontFamily      string             `json:"fontFamily"`
	BaseFontSize    int                `json:"baseFontSize"`
	LogoLightURL    string             `json:"logoLightUrl"`
	LogoDarkURL     string             `json:"logoDarkUrl"`
	FaviconURL      string             `json:"faviconUrl"`
	LoginHeadline   settings.Localized `json:"loginHeadline"`
	LoginSubtitle   settings.Localized `json:"loginSubtitle"`
	FooterText      settings.Localized `json:"footerText"`
	DefaultLanguage string             `json:"defaultLanguage"`
	Version         string             `json:"version"`
}

type handler struct {
	d     *deps.Deps
	cache *cache.Cache[[]byte]
}

var (
	reColor = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	reFont  = regexp.MustCompile(`^[\p{L}\p{N} ,.'"_-]{1,80}$`)
	reKey   = regexp.MustCompile(`^[A-Za-z0-9_.:\-]{1,200}$`)
)

// brandCache holds the public branding payload; Invalidate clears it (e.g. after a system reset).
var brandCache = cache.New[[]byte](10*time.Minute, 16)

// Invalidate drops the cached public branding.
func Invalidate() { brandCache.Clear() }

// Register mounts the public endpoints (/api/public/*, no auth) and the SUPER_ADMIN endpoints
// (/api/admin/branding*, /api/admin/i18n*) on the /api router r.
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d: d, cache: brandCache}
	g := r.Group("/public")
	g.Get("/branding", h.getBranding)
	g.Get("/assets/:id", h.getAsset)
	g.Get("/i18n/:lang", h.getI18n)

	a := r.Group("/admin", auth.RequireSuperAdmin())
	a.Put("/branding", h.putBranding)
	a.Post("/branding/assets", ratelimit.Group("upload"), h.uploadAsset)
	a.Delete("/branding/assets/:kind", h.deleteAsset)
	a.Get("/i18n", h.adminListI18n)
	a.Put("/i18n", h.adminPutI18n)
	a.Delete("/i18n/:lang/:key", h.adminDeleteI18n)
}

func assetURL(id, def string) string {
	if id == "" {
		return "/api/public/assets/" + def
	}
	return "/api/public/assets/" + id
}

func (h *handler) publicJSON(ctx context.Context) ([]byte, error) {
	return h.cache.GetOrLoad(ctx, "branding", func(ctx context.Context) ([]byte, error) {
		b, err := h.d.Settings.Branding(ctx)
		if err != nil {
			return nil, err
		}
		ov, err := h.d.Settings.Overrides(ctx)
		if err != nil {
			return nil, err
		}
		p := Public{Name: b.Name, LegalName: b.LegalName, PrimaryColor: b.PrimaryColor, AccentColor: b.AccentColor, FontFamily: b.FontFamily,
			BaseFontSize: b.BaseFontSize, LogoLightURL: assetURL(b.LogoLightID, "default-light"), LogoDarkURL: assetURL(b.LogoDarkID, "default-dark"),
			FaviconURL: assetURL(b.FaviconID, "default-favicon"), LoginHeadline: b.LoginHeadline, LoginSubtitle: b.LoginSubtitle,
			FooterText: b.FooterText, DefaultLanguage: b.DefaultLanguage, Version: h.d.Settings.BrandingVersion(ctx) + "." + ov.Version}
		return json.Marshal(p)
	})
}

func (h *handler) getBranding(c *fiber.Ctx) error {
	b, err := h.publicJSON(httpx.Ctx(c))
	if err != nil {
		return err
	}
	return httpx.BytesCached(c, b, "application/json; charset=utf-8", "public, max-age=60, must-revalidate")
}

var defaultAssets = map[string]struct{ file, mime string }{
	"default-light":   {"logo-on-light.png", "image/png"},
	"default-dark":    {"logo-on-dark.png", "image/png"},
	"default-favicon": {"favicon.png", "image/png"},
}

func (h *handler) getAsset(c *fiber.Ctx) error {
	id := c.Params("id")
	var data []byte
	var mime, sum, cc string
	if def, ok := defaultAssets[id]; ok {
		data, _ = assets.FS.ReadFile(def.file)
		s := sha256.Sum256(data)
		mime, sum, cc = def.mime, hex.EncodeToString(s[:]), "public, max-age=3600"
	} else {
		a, err := h.d.Settings.Asset(httpx.Ctx(c), id)
		if err != nil {
			if db.IsNoRows(err) {
				return httpx.NotFound("entity.asset")
			}
			return err
		}
		data, mime, sum, cc = a.Data, a.Mime, a.SHA256, "public, max-age=31536000, immutable"
	}
	etag := `"` + sum + `"`
	c.Set(fiber.HeaderETag, etag)
	c.Set(fiber.HeaderCacheControl, cc)
	c.Set("X-Content-Type-Options", "nosniff")
	if mime == "image/svg+xml" { // belt and braces for SVG
		c.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	}
	if httpx.ETagMatch(c, etag) {
		return c.SendStatus(fiber.StatusNotModified)
	}
	c.Set(fiber.HeaderContentType, mime)
	return c.Send(data)
}

type i18nPublic struct {
	Version  string            `json:"version"`
	Messages map[string]string `json:"messages"`
}

func (h *handler) getI18n(c *fiber.Ctx) error {
	l, ok := i18n.Normalize(c.Params("lang"))
	if !ok || len(c.Params("lang")) > 5 {
		return httpx.NotFound("entity.endpoint")
	}
	ov, err := h.d.Settings.Overrides(httpx.Ctx(c))
	if err != nil {
		return err
	}
	msgs := ov.ByLang[l]
	if msgs == nil {
		msgs = map[string]string{}
	}
	return httpx.JSONCached(c, i18nPublic{Version: ov.Version, Messages: msgs}, "public, max-age=60, must-revalidate")
}

// ── admin: branding ──────────────────────────────────────────────────────────

func localized(v *httpx.V, key string, max int, cur settings.Localized) settings.Localized {
	sub := v.Obj(key, false)
	if sub == nil {
		return cur
	}
	pick := func(k string, dst *string) {
		if sub.Has(k) {
			if s := sub.Str(k).Trim().Max(max); s.Present() {
				*dst = s.Get()
			}
		}
	}
	pick("en", &cur.EN)
	pick("am", &cur.AM)
	pick("rw", &cur.RW)
	pick("fr", &cur.FR)
	return cur
}

func (h *handler) putBranding(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	cur, err := h.d.Settings.Branding(ctx)
	if err != nil {
		return err
	}
	before := cur
	if v.Has("name") {
		cur.Name = v.Str("name").Trim().Min(1, "v.required").Max(80).Get()
	}
	if v.Has("legalName") {
		cur.LegalName = v.Str("legalName").Trim().Min(1, "v.required").Max(120).Get()
	}
	if v.Has("primaryColor") {
		cur.PrimaryColor = v.Str("primaryColor").Trim().Regex(reColor, "branding.invalid_color").Get()
	}
	if v.Has("accentColor") {
		cur.AccentColor = v.Str("accentColor").Trim().Regex(reColor, "branding.invalid_color").Get()
	}
	if v.Has("fontFamily") {
		cur.FontFamily = v.Str("fontFamily").Trim().Regex(reFont, "branding.invalid_font").Get()
	}
	if v.Has("baseFontSize") {
		cur.BaseFontSize = int(v.Num("baseFontSize").Whole("branding.font_size").Min(12, "branding.font_size").Max(20, "branding.font_size").I())
	}
	cur.LoginHeadline = localized(v, "loginHeadline", 120, cur.LoginHeadline)
	cur.LoginSubtitle = localized(v, "loginSubtitle", 300, cur.LoginSubtitle)
	cur.FooterText = localized(v, "footerText", 200, cur.FooterText)
	if v.Has("defaultLanguage") {
		cur.DefaultLanguage = v.Str("defaultLanguage").Trim().Lower().OneOf(i18n.Codes(), "v.language").Get()
	}
	if err := v.Err(); err != nil {
		return err
	}
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		if err := h.d.Settings.SaveBranding(ctx, tx, cur); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "branding.update", Entity: "Setting", EntityID: "branding", Before: before, After: cur})
	})
	h.d.Settings.Invalidate()
	h.cache.Clear()
	if err != nil {
		return err
	}
	return h.respondBranding(c)
}

func (h *handler) respondBranding(c *fiber.Ctx) error {
	b, err := h.publicJSON(httpx.Ctx(c))
	if err != nil {
		return err
	}
	c.Set(fiber.HeaderContentType, "application/json; charset=utf-8")
	httpx.NoStore(c)
	return c.Send(b)
}

// sniff detects the image type from magic bytes (never trusts the client's Content-Type / filename).
func sniff(b []byte) (mime string, ok bool) {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png", true
	case bytes.HasPrefix(b, []byte("\xff\xd8\xff")):
		return "image/jpeg", true
	case len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp", true
	}
	head := b
	if len(head) > 1024 {
		head = head[:1024]
	}
	h := strings.ToLower(string(head))
	if strings.Contains(h, "<svg") && (strings.HasPrefix(strings.TrimLeft(h, "\xef\xbb\xbf \t\r\n"), "<")) {
		return "image/svg+xml", true
	}
	return "", false
}

var svgBad = regexp.MustCompile(`(?is)<\s*(script|foreignobject|iframe|embed|object|audio|video|link|meta|base|animate|set)\b|\bon[a-z]+\s*=|javascript\s*:|data\s*:\s*text/html|<!(doctype|entity)|(xlink:)?href\s*=\s*["']\s*(https?:|//|data:)|@import|expression\s*\(|url\s*\(\s*["']?\s*(https?:|//)`)

// SVGSafe reports whether an SVG document is free of script and external references.
func SVGSafe(b []byte) bool { return !svgBad.Match(b) }

func (h *handler) uploadAsset(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	lang := httpx.Lang(c)
	kind := strings.TrimSpace(c.FormValue("kind"))
	if kind != "light" && kind != "dark" && kind != "favicon" {
		return httpx.BadRequest("error.validation", map[string]string{"kind": i18n.T(lang, "branding.bad_kind")})
	}
	fh, err := c.FormFile("file")
	if err != nil {
		return httpx.BadRequest("error.validation", map[string]string{"file": i18n.T(lang, "branding.file_required")})
	}
	if fh.Size > maxAsset {
		return httpx.NewKey(413, "payload_too_large", "branding.too_large")
	}
	f, err := fh.Open()
	if err != nil {
		return err
	}
	defer f.Close()
	data := make([]byte, 0, fh.Size)
	buf := bytes.NewBuffer(data)
	if _, err := buf.ReadFrom(f); err != nil {
		return err
	}
	data = buf.Bytes()
	if len(data) == 0 || len(data) > maxAsset {
		return httpx.NewKey(413, "payload_too_large", "branding.too_large")
	}
	mime, ok := sniff(data)
	if !ok {
		return httpx.BadRequest("error.validation", map[string]string{"file": i18n.T(lang, "branding.bad_image")})
	}
	if mime == "image/svg+xml" && !SVGSafe(data) {
		return httpx.BadRequest("error.validation", map[string]string{"file": i18n.T(lang, "branding.svg_unsafe")})
	}
	sum := sha256.Sum256(data)
	cur, err := h.d.Settings.Branding(ctx)
	if err != nil {
		return err
	}
	var oldID string
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		id, err := h.d.Settings.PutAsset(ctx, tx, kind, mime, data, hex.EncodeToString(sum[:]))
		if err != nil {
			return err
		}
		switch kind {
		case "light":
			oldID, cur.LogoLightID = cur.LogoLightID, id
		case "dark":
			oldID, cur.LogoDarkID = cur.LogoDarkID, id
		default:
			oldID, cur.FaviconID = cur.FaviconID, id
		}
		if err := h.d.Settings.SaveBranding(ctx, tx, cur); err != nil {
			return err
		}
		if oldID != "" {
			if err := h.d.Settings.DeleteAsset(ctx, tx, oldID); err != nil {
				return err
			}
		}
		return audit.Record(c, tx, audit.Input{Action: "branding.asset_upload", Entity: "Asset", EntityID: id, Summary: "Uploaded " + kind + " logo (" + mime + ", " + itoa(len(data)) + " bytes)"})
	})
	h.d.Settings.Invalidate()
	h.cache.Clear()
	if err != nil {
		return err
	}
	return h.respondBranding(c)
}

func (h *handler) deleteAsset(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	kind := c.Params("kind")
	cur, err := h.d.Settings.Branding(ctx)
	if err != nil {
		return err
	}
	var oldID string
	switch kind {
	case "light":
		oldID, cur.LogoLightID = cur.LogoLightID, ""
	case "dark":
		oldID, cur.LogoDarkID = cur.LogoDarkID, ""
	case "favicon":
		oldID, cur.FaviconID = cur.FaviconID, ""
	default:
		return httpx.BadRequest("error.validation", map[string]string{"kind": i18n.T(httpx.Lang(c), "branding.bad_kind")})
	}
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		if err := h.d.Settings.SaveBranding(ctx, tx, cur); err != nil {
			return err
		}
		if oldID != "" {
			if err := h.d.Settings.DeleteAsset(ctx, tx, oldID); err != nil {
				return err
			}
		}
		return audit.Record(c, tx, audit.Input{Action: "branding.asset_delete", Entity: "Asset", EntityID: oldID, Summary: "Removed " + kind + " logo (reverted to default)"})
	})
	h.d.Settings.Invalidate()
	h.cache.Clear()
	if err != nil {
		return err
	}
	return h.respondBranding(c)
}

// ── admin: i18n overrides ────────────────────────────────────────────────────

func (h *handler) adminListI18n(c *fiber.Ctx) error {
	ov, err := h.d.Settings.Overrides(httpx.Ctx(c))
	if err != nil {
		return err
	}
	items := ov.Entities
	if l := c.Query("lang"); l != "" {
		filtered := []settings.Override{}
		for _, e := range ov.Entities {
			if e.Lang == l {
				filtered = append(filtered, e)
			}
		}
		items = filtered
	}
	httpx.NoStore(c)
	return c.JSON(fiber.Map{"items": items, "version": ov.Version})
}

// PUT /api/admin/i18n  {items:[{lang,key,value}]} | {lang,key,value}. An empty value deletes the override.
func (h *handler) adminPutI18n(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	type item struct{ lang, key, value string }
	var list []item
	parseOne := func(x *httpx.V) {
		l := x.Str("lang").Trim().Lower().OneOf(i18n.Codes(), "i18n.bad_lang").Get()
		k := x.Str("key").Trim().Regex(reKey, "i18n.bad_key").Get()
		val := x.Str("value").Max(2000).Get()
		list = append(list, item{l, k, val})
	}
	if v.Has("items") {
		arr := v.Arr("items", true)
		if len(arr) > 500 {
			v.Fail("items", "i18n.too_many")
		}
		for _, x := range arr {
			parseOne(x)
		}
	} else {
		parseOne(v)
	}
	if err := v.Err(); err != nil {
		return err
	}
	uid := auth.MustUser(c).ID
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		for _, it := range list {
			if strings.TrimSpace(it.value) == "" {
				if _, err := tx.Exec(ctx, `DELETE FROM i18n_override WHERE lang=$1 AND key=$2`, it.lang, it.key); err != nil {
					return err
				}
				continue
			}
			if _, err := tx.Exec(ctx, `INSERT INTO i18n_override (lang, key, value, updated_by) VALUES ($1,$2,$3,$4)
				ON CONFLICT (lang, key) DO UPDATE SET value=EXCLUDED.value, updated_by=EXCLUDED.updated_by, updated_at=now()`, it.lang, it.key, it.value, uid); err != nil {
				return err
			}
		}
		return audit.Record(c, tx, audit.Input{Action: "i18n.update", Entity: "I18nOverride", Summary: "Updated " + itoa(len(list)) + " translation override(s)"})
	})
	if err != nil {
		return err
	}
	return h.afterI18n(c)
}

func (h *handler) adminDeleteI18n(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	l, ok := i18n.Normalize(c.Params("lang"))
	if !ok {
		return httpx.NotFound("entity.endpoint")
	}
	tag, err := h.d.Pool.Exec(ctx, `DELETE FROM i18n_override WHERE lang=$1 AND key=$2`, string(l), c.Params("key"))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return httpx.NotFound("entity.record")
	}
	_ = audit.Record(c, h.d.Pool, audit.Input{Action: "i18n.delete", Entity: "I18nOverride", EntityID: string(l) + ":" + c.Params("key")})
	return h.afterI18n(c)
}

func (h *handler) afterI18n(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	if err := h.d.Settings.RefreshOverrides(ctx); err != nil {
		return err
	}
	h.cache.Clear()
	ov, err := h.d.Settings.Overrides(ctx)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"items": ov.Entities, "version": ov.Version})
}

func itoa(n int) string { return strconv.Itoa(n) }
