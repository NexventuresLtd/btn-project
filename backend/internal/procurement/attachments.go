package procurement

// Supporting documents (PRC-04): quotations, supplier invoices, delivery notes on requests, orders, receipts, invoices.

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// MaxAttachment is the largest accepted file (5 MB).
const MaxAttachment = 5 << 20

var attachmentKinds = []string{"QUOTATION", "INVOICE", "DELIVERY_NOTE", "OTHER"}
var attachmentEntities = []string{"request", "order", "invoice", "receipt"}
var attachmentMimes = map[string]bool{"application/pdf": true, "image/png": true, "image/jpeg": true, "image/webp": true}

// Attachment is file metadata (content at GET /attachments/:id).
type Attachment struct {
	ID         string    `json:"id"`
	Entity     string    `json:"entity"`
	EntityID   string    `json:"entityId"`
	Kind       string    `json:"kind"`
	Filename   string    `json:"filename"`
	Mime       string    `json:"mime"`
	Size       int       `json:"size"`
	UploadedBy UserRef   `json:"uploadedBy"`
	CreatedAt  time.Time `json:"createdAt"`
}

// loadAttachments lists files of entity ids ("" entity = any entity).
func loadAttachments(ctx context.Context, q db.DBTX, entity string, ids []string) ([]Attachment, error) {
	rows, err := q.Query(ctx, `SELECT a.id, a.entity, a.entity_id, a.kind, a.filename, a.mime, a.size, u.id, u.name, a.created_at
		FROM procurement_attachment a JOIN "User" u ON u.id = a.uploaded_by_id WHERE ($1 = '' OR a.entity = $1) AND a.entity_id = ANY($2::text[]) ORDER BY a.created_at`, entity, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Attachment{}
	for rows.Next() {
		var a Attachment
		if err := rows.Scan(&a.ID, &a.Entity, &a.EntityID, &a.Kind, &a.Filename, &a.Mime, &a.Size, &a.UploadedBy.ID, &a.UploadedBy.Name, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// entityAccess resolves an attachment target to (number, plant) and checks the user may attach to / read it.
func entityAccess(ctx context.Context, q db.DBTX, u *auth.User, entity, id string, write bool) (string, error) {
	var number, plantID, owner string
	var err error
	switch entity {
	case "request":
		err = q.QueryRow(ctx, `SELECT number, plant_id, requested_by_id FROM procurement_request WHERE id = $1`, id).Scan(&number, &plantID, &owner)
	case "order":
		err = q.QueryRow(ctx, `SELECT number, delivery_plant_id, '' FROM purchase_order WHERE id = $1`, id).Scan(&number, &plantID, &owner)
	case "receipt":
		err = q.QueryRow(ctx, `SELECT number, plant_id, received_by_id FROM goods_receipt WHERE id = $1`, id).Scan(&number, &plantID, &owner)
	case "invoice":
		err = q.QueryRow(ctx, `SELECT i.invoice_number, o.delivery_plant_id, '' FROM purchase_invoice i JOIN purchase_order o ON o.id = i.po_id WHERE i.id = $1`, id).Scan(&number, &plantID, &owner)
	}
	if db.IsNoRows(err) {
		return "", httpx.NotFound("procurement.entity.document")
	}
	if err != nil {
		return "", err
	}
	if s := auth.ScopedPlantID(u); s != "" && s != plantID {
		return "", httpx.NotFound("procurement.entity.document")
	}
	if write {
		ok := u.Can(permManage)
		switch entity {
		case "request":
			ok = ok || (owner == u.ID && u.Can(permRequest))
		case "receipt":
			ok = ok || u.Can(permReceive)
		}
		if !ok {
			return "", httpx.Forbidden()
		}
	} else if entity == "request" && !(u.Can(permView) || u.Can(permApprove) || u.Can(permManage) || owner == u.ID || u.Can(permRequest)) {
		return "", httpx.Forbidden()
	}
	return number, nil
}

func (h *handler) uploadAttachment(c *fiber.Ctx) error {
	ctx, u, lang := httpx.Ctx(c), auth.MustUser(c), httpx.Lang(c)
	entity := strings.TrimSpace(c.FormValue("entity"))
	entityID := strings.TrimSpace(c.FormValue("entityId"))
	kind := strings.ToUpper(strings.TrimSpace(c.FormValue("kind")))
	if kind == "" {
		kind = "OTHER"
	}
	fields := map[string]string{}
	if !inList(attachmentEntities, entity) {
		fields["entity"] = i18n.T(lang, "procurement.v.attachment_entity")
	}
	if entityID == "" {
		fields["entityId"] = i18n.T(lang, "v.required")
	}
	if !inList(attachmentKinds, kind) {
		fields["kind"] = i18n.T(lang, "procurement.v.attachment_kind")
	}
	fh, err := c.FormFile("file")
	if err != nil || fh == nil {
		fields["file"] = i18n.T(lang, "procurement.v.file_required")
	} else if fh.Size <= 0 || fh.Size > MaxAttachment {
		fields["file"] = i18n.T(lang, "procurement.v.file_size", "mb", MaxAttachment>>20)
	}
	if len(fields) > 0 {
		return httpx.BadRequest("error.validation", fields)
	}
	f, err := fh.Open()
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxAttachment+1))
	_ = f.Close()
	if err != nil {
		return err
	}
	mime := http.DetectContentType(data)
	if i := strings.Index(mime, ";"); i > 0 {
		mime = mime[:i]
	}
	if mime == "application/octet-stream" && strings.EqualFold(filepath.Ext(fh.Filename), ".webp") {
		mime = "image/webp"
	}
	if !attachmentMimes[mime] {
		return badField(lang, "file", "procurement.v.file_type")
	}
	name := filepath.Base(strings.ReplaceAll(fh.Filename, "\\", "/"))
	if len([]rune(name)) > 120 {
		name = string([]rune(name)[:120])
	}
	user := *u
	var out Attachment
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		number, err := entityAccess(ctx, tx, &user, entity, entityID, true)
		if err != nil {
			return err
		}
		out = Attachment{ID: db.NewID(), Entity: entity, EntityID: entityID, Kind: kind, Filename: name, Mime: mime, Size: len(data), UploadedBy: UserRef{ID: user.ID, Name: user.Name}, CreatedAt: db.Now()}
		if _, err := tx.Exec(ctx, `INSERT INTO procurement_attachment (id, entity, entity_id, kind, filename, mime, size, data, uploaded_by_id, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, out.ID, entity, entityID, kind, name, mime, len(data), data, user.ID, out.CreatedAt); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "procurement.attach", Entity: "ProcurementAttachment", EntityID: out.ID,
			Summary: "Attached " + kind + " \"" + name + "\" (" + strconv.Itoa(len(data)) + " bytes) to " + entity + " " + number})
	})
	if err != nil {
		return err
	}
	return c.Status(201).JSON(out)
}

func (h *handler) downloadAttachment(c *fiber.Ctx) error {
	ctx, u := httpx.Ctx(c), auth.MustUser(c)
	var entity, entityID, name, mime string
	var data []byte
	err := h.d.Pool.QueryRow(ctx, `SELECT entity, entity_id, filename, mime, data FROM procurement_attachment WHERE id = $1`, c.Params("id")).Scan(&entity, &entityID, &name, &mime, &data)
	if db.IsNoRows(err) {
		return httpx.NotFound("procurement.entity.document")
	}
	if err != nil {
		return err
	}
	if _, err := entityAccess(ctx, h.d.Pool, u, entity, entityID, false); err != nil {
		return err
	}
	c.Set(fiber.HeaderContentType, mime)
	disp := "inline"
	if c.Query("download") == "1" {
		disp = "attachment"
	}
	c.Set(fiber.HeaderContentDisposition, disp+`; filename="`+strings.ReplaceAll(name, `"`, "")+`"`)
	c.Set("X-Content-Type-Options", "nosniff")
	httpx.NoStore(c)
	return c.Send(data)
}
