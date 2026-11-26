package income

import (
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
	"btn-ops/internal/expenses"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// Receipt / slip attachment limits (same rules as expense receipts).
const (
	MaxAttachmentBytes = expenses.MaxAttachmentBytes // 10 MB
	MaxAttachments     = 10                          // per income record
)

var unsafeName = regexp.MustCompile(`[^\p{L}\p{N}._\- ()]+`)

// cleanFileName keeps a display name safe for headers and file systems (same rule as expense receipts).
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

// sniffReason maps the shared sniffer's reasons to this module's messages.
func sniffReason(reason string) string {
	return "income.err." + strings.TrimPrefix(reason, "expenses.err.")
}

func (h *handler) upload(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	id := c.Params("id")
	fh, err := c.FormFile("file")
	if err != nil {
		return httpx.BadRequest("error.validation", map[string]string{"file": i18n.T(lang, "income.err.file_required")})
	}
	if fh.Size > MaxAttachmentBytes {
		return httpx.NewKey(413, "payload_too_large", "income.err.file_too_large")
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
	mime, ok, reason := expenses.SniffReceipt(data)
	if !ok {
		if reason == "expenses.err.file_too_large" {
			return httpx.NewKey(413, "payload_too_large", sniffReason(reason))
		}
		return httpx.BadRequest("error.validation", map[string]string{"file": i18n.T(lang, sniffReason(reason))})
	}
	name := cleanFileName(fh.Filename, mime)
	sum := sha256.Sum256(data)
	att, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (*Attachment, error) {
		r, err := getRow(ctx, tx, id, true)
		if err != nil {
			return nil, err
		}
		if r == nil {
			return nil, httpx.NotFound("income.entity")
		}
		if !canWrite(u, r) {
			return nil, httpx.Forbidden("income.err.not_yours")
		}
		if r.Status == StatusVoid {
			return nil, httpx.NewKey(409, "conflict", "income.err.attach_closed", "status", i18n.T(lang, "income.status."+r.Status))
		}
		if r.Attachments >= MaxAttachments {
			return nil, httpx.NewKey(409, "conflict", "income.err.attach_limit", "n", MaxAttachments)
		}
		var a Attachment
		if err := tx.QueryRow(ctx, `INSERT INTO income_attachment (income_id, file_name, content_type, size_bytes, sha256, data, uploaded_by_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id, created_at`, id, name, mime, len(data), hex.EncodeToString(sum[:]), data, u.ID).Scan(&a.ID, &a.UploadedAt); err != nil {
			return nil, err
		}
		a.FileName, a.ContentType, a.Size, a.SHA256, a.UploadedBy = name, mime, len(data), hex.EncodeToString(sum[:]), u.Name
		if _, err := tx.Exec(ctx, `UPDATE income SET updated_at = $2 WHERE id = $1`, id, db.Now()); err != nil {
			return nil, err
		}
		return &a, audit.Record(c, tx, audit.Input{Action: "income.attach", Entity: "Income", EntityID: id,
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
	err = h.d.Pool.QueryRow(ctx, `SELECT file_name, content_type, data FROM income_attachment WHERE id = $1 AND income_id = $2`, c.Params("aid"), r.ID).Scan(&name, &mime, &data)
	if db.IsNoRows(err) {
		return httpx.NotFound("income.attachment")
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
	reason, reasonErr := httpx.RemovalReason(c) // SRS §6.3: the file is marked removed and kept, never deleted
	err := db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		r, err := getRow(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if r == nil {
			return httpx.NotFound("income.entity")
		}
		if !canWrite(u, r) {
			return httpx.Forbidden("income.err.not_yours")
		}
		if r.Status != StatusRecorded {
			return httpx.NewKey(409, "conflict", "income.err.attach_locked", "status", i18n.T(lang, "income.status."+r.Status))
		}
		if reasonErr != nil {
			return reasonErr
		}
		var name string
		if err := tx.QueryRow(ctx, `UPDATE income_attachment SET removed_at = now(), removed_by_id = $3, removed_reason = $4
			WHERE id = $1 AND income_id = $2 AND removed_at IS NULL RETURNING file_name`, aid, id, u.ID, reason).Scan(&name); err != nil {
			if db.IsNoRows(err) {
				return httpx.NotFound("income.attachment")
			}
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE income SET updated_at = $2 WHERE id = $1`, id, db.Now()); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "income.detach", Entity: "Income", EntityID: id,
			Summary: r.Number + " receipt removed: " + name + " (" + reason + ")", Before: map[string]any{"attachmentId": aid, "fileName": name},
			After: map[string]any{"attachmentId": aid, "removed": true, "reason": reason}})
	})
	if err != nil {
		return err
	}
	Invalidate()
	return c.JSON(fiber.Map{"ok": true})
}
