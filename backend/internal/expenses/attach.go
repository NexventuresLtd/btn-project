package expenses

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// Receipt attachment limits (FIN-01).
const (
	MaxAttachmentBytes = 10 << 20 // 10 MB
	MaxAttachments     = 10       // per expense
)

// pdfActive matches PDF features that run code or open other resources; such files are refused.
var pdfActive = regexp.MustCompile(`/(JavaScript|JS|Launch|EmbeddedFile|RichMedia|XFA|OpenAction\s*<<[^>]*/S\s*/JavaScript)\b`)

// SniffReceipt identifies a receipt file from its bytes (the client's Content-Type and file name are ignored).
// Accepted: JPEG, PNG, WebP images and PDF documents without active content. ok=false → refused; reason is an i18n key.
func SniffReceipt(b []byte) (mime string, ok bool, reason string) {
	switch {
	case len(b) == 0:
		return "", false, "expenses.err.file_empty"
	case len(b) > MaxAttachmentBytes:
		return "", false, "expenses.err.file_too_large"
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "image/jpeg", true, ""
	case len(b) >= 8 && bytes.Equal(b[:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return "image/png", true, ""
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp", true, ""
	case len(b) >= 5 && string(b[:5]) == "%PDF-":
		if pdfActive.Match(b) {
			return "", false, "expenses.err.pdf_active"
		}
		return "application/pdf", true, ""
	}
	return "", false, "expenses.err.file_type"
}

var unsafeName = regexp.MustCompile(`[^\p{L}\p{N}._\- ()]+`)

// cleanFileName keeps a display name safe for headers and file systems.
func cleanFileName(name, mime string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.TrimSpace(unsafeName.ReplaceAllString(name, "_"))
	if name == "" || name == "." || name == "_" {
		name = "receipt"
	}
	for utf8.RuneCountInString(name) > 120 {
		r := []rune(name)
		name = string(r[:120])
	}
	ext := map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "application/pdf": ".pdf"}[mime]
	if !strings.EqualFold(filepath.Ext(name), ext) && !(ext == ".jpg" && strings.EqualFold(filepath.Ext(name), ".jpeg")) {
		name = strings.TrimSuffix(name, filepath.Ext(name)) + ext
	}
	return name
}

func (h *handler) upload(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	id := c.Params("id")
	fh, err := c.FormFile("file")
	if err != nil {
		return httpx.BadRequest("error.validation", map[string]string{"file": i18n.T(lang, "expenses.err.file_required")})
	}
	if fh.Size > MaxAttachmentBytes {
		return httpx.NewKey(413, "payload_too_large", "expenses.err.file_too_large")
	}
	f, err := fh.Open()
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxAttachmentBytes+1))
	f.Close()
	if err != nil {
		return err
	}
	mime, ok, reason := SniffReceipt(data)
	if !ok {
		if reason == "expenses.err.file_too_large" {
			return httpx.NewKey(413, "payload_too_large", reason)
		}
		return httpx.BadRequest("error.validation", map[string]string{"file": i18n.T(lang, reason)})
	}
	name := cleanFileName(fh.Filename, mime)
	sum := sha256.Sum256(data)
	att, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (*Attachment, error) {
		r, err := getRow(ctx, tx, id, true)
		if err != nil {
			return nil, err
		}
		if r == nil {
			return nil, httpx.NotFound("expenses.entity")
		}
		if !canSee(u, r) || (r.CreatedByID != u.ID && !u.Can("expenses.approve")) {
			return nil, httpx.Forbidden("expenses.err.not_yours")
		}
		if r.Status == StatusPaid || r.Status == StatusCancelled {
			return nil, httpx.NewKey(409, "conflict", "expenses.err.attach_closed", "status", i18n.T(lang, "expenses.status."+r.Status))
		}
		if r.Attachments >= MaxAttachments {
			return nil, httpx.NewKey(409, "conflict", "expenses.err.attach_limit", "n", MaxAttachments)
		}
		var a Attachment
		if err := tx.QueryRow(ctx, `INSERT INTO expense_attachment (expense_id, file_name, content_type, size_bytes, sha256, data, uploaded_by_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id, created_at`, id, name, mime, len(data), hex.EncodeToString(sum[:]), data, u.ID).Scan(&a.ID, &a.UploadedAt); err != nil {
			return nil, err
		}
		a.FileName, a.ContentType, a.Size, a.SHA256, a.UploadedBy = name, mime, len(data), hex.EncodeToString(sum[:]), u.Name
		if _, err := tx.Exec(ctx, `UPDATE expense SET updated_at = now() WHERE id = $1`, id); err != nil {
			return nil, err
		}
		return &a, audit.Record(c, tx, audit.Input{Action: "expenses.attach", Entity: "Expense", EntityID: id,
			Summary: fmt.Sprintf("%s receipt attached: %s (%s, %d bytes)", r.Number, name, mime, len(data)),
			After:   map[string]any{"attachmentId": a.ID, "fileName": name, "contentType": mime, "size": len(data), "sha256": a.SHA256}})
	})
	if err != nil {
		return err
	}
	Invalidate()
	return c.Status(201).JSON(att)
}

func (h *handler) download(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	r, err := h.load(c, c.Params("id"))
	if err != nil {
		return err
	}
	var name, mime string
	var data []byte
	err = h.d.Pool.QueryRow(ctx, `SELECT file_name, content_type, data FROM expense_attachment WHERE id = $1 AND expense_id = $2`, c.Params("aid"), r.ID).Scan(&name, &mime, &data)
	if db.IsNoRows(err) {
		return httpx.NotFound("expenses.attachment")
	}
	if err != nil {
		return err
	}
	disp := "inline"
	if c.Query("download") == "1" {
		disp = "attachment"
	}
	ascii := strings.Map(func(r rune) rune {
		if r < 32 || r > 126 || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	c.Set(fiber.HeaderContentType, mime)
	c.Set(fiber.HeaderContentDisposition, fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`, disp, ascii, urlEscape(name)))
	c.Set("X-Content-Type-Options", "nosniff")
	c.Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'")
	c.Set(fiber.HeaderCacheControl, "private, no-store")
	return c.Send(data)
}

func urlEscape(s string) string {
	var b strings.Builder
	for _, by := range []byte(s) {
		if (by >= 'A' && by <= 'Z') || (by >= 'a' && by <= 'z') || (by >= '0' && by <= '9') || by == '.' || by == '-' || by == '_' {
			b.WriteByte(by)
		} else {
			fmt.Fprintf(&b, "%%%02X", by)
		}
	}
	return b.String()
}

func (h *handler) deleteAttachment(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	id, aid := c.Params("id"), c.Params("aid")
	err := db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		r, err := getRow(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if r == nil {
			return httpx.NotFound("expenses.entity")
		}
		if !canSee(u, r) || (r.CreatedByID != u.ID && !u.Can("expenses.approve")) {
			return httpx.Forbidden("expenses.err.not_yours")
		}
		if r.Status != StatusSubmitted && r.Status != StatusRejected {
			return httpx.NewKey(409, "conflict", "expenses.err.attach_closed", "status", i18n.T(lang, "expenses.status."+r.Status))
		}
		var name string
		if err := tx.QueryRow(ctx, `DELETE FROM expense_attachment WHERE id = $1 AND expense_id = $2 RETURNING file_name`, aid, id).Scan(&name); err != nil {
			if db.IsNoRows(err) {
				return httpx.NotFound("expenses.attachment")
			}
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "expenses.detach", Entity: "Expense", EntityID: id,
			Summary: r.Number + " receipt removed: " + name, Before: map[string]any{"attachmentId": aid, "fileName": name}})
	})
	if err != nil {
		return err
	}
	Invalidate()
	return c.JSON(fiber.Map{"ok": true})
}
