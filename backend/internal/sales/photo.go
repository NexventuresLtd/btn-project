package sales

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/jpeg"
	_ "image/png" // decoders for DecodeConfig / Decode
	"net/http"

	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// Proof-of-delivery photo (DEL-05): ≤ 5 MB, the content type is sniffed from the bytes (never trusted from the
// client), only JPEG/PNG/WebP, dimensions checked before decoding (decompression bombs), a 320 px JPEG thumbnail
// is made, both are stored in delivery_photo. Served only to users who may see the delivery, with nosniff.

const (
	maxPhotoBytes  = 5 << 20
	maxPhotoPixels = 40_000_000 // e.g. 8000 × 5000
	thumbMax       = 320
)

var errPhoto = errors.New("photo")

type photo struct {
	Mime          string
	Size          int
	Width, Height int
	SHA           string
	Content       []byte
	Thumb         []byte
}

// sniffImage validates the uploaded bytes and makes the thumbnail.
func sniffImage(b []byte) (*photo, string) {
	if len(b) == 0 {
		return nil, "sales.v.photo_empty"
	}
	if len(b) > maxPhotoBytes {
		return nil, "sales.v.photo_too_big"
	}
	mime := http.DetectContentType(b)
	switch mime {
	case "image/jpeg", "image/png", "image/webp":
	default:
		return nil, "sales.v.photo_type"
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || (format != "jpeg" && format != "png" && format != "webp") {
		return nil, "sales.v.photo_type"
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxPhotoPixels {
		return nil, "sales.v.photo_dimensions"
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, "sales.v.photo_type"
	}
	w, h := cfg.Width, cfg.Height
	tw, th := w, h
	if w > thumbMax || h > thumbMax {
		if w >= h {
			tw, th = thumbMax, max(1, h*thumbMax/w)
		} else {
			tw, th = max(1, w*thumbMax/h), thumbMax
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Over, nil)
	var tb bytes.Buffer
	if err := jpeg.Encode(&tb, dst, &jpeg.Options{Quality: 78}); err != nil {
		return nil, "sales.v.photo_type"
	}
	sum := sha256.Sum256(b)
	return &photo{Mime: mime, Size: len(b), Width: w, Height: h, SHA: hex.EncodeToString(sum[:]), Content: b, Thumb: tb.Bytes()}, ""
}

// servePhoto: GET /deliveries/:id/photo and /deliveries/:id/photo/thumb.
func (h *handler) servePhoto(thumb bool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		r := newRq(c)
		var plantID, salePlant, saleID, mime, sha string
		var content []byte
		col := "content"
		if thumb {
			col = "thumb"
		}
		err := h.d.Pool.QueryRow(r.ctx, `SELECT d."plantId", s."plantId", s.id, ph.mime, ph.sha256, ph.`+col+`
			FROM delivery_photo ph JOIN "Delivery" d ON d.id = ph.delivery_id JOIN "Sale" s ON s.id = d."saleId" WHERE ph.delivery_id = $1`, c.Params("id")).
			Scan(&plantID, &salePlant, &saleID, &mime, &sha, &content)
		if err != nil {
			if db.IsNoRows(err) {
				return httpx.NotFound("entity.photo")
			}
			return err
		}
		if scoped := auth.ScopedPlantID(r.u); scoped != "" && scoped != plantID && scoped != salePlant {
			return httpx.Forbidden("error.plant_scope")
		}
		if thumb {
			mime = "image/jpeg"
			sha += "-t"
		}
		etag := `"` + sha + `"`
		c.Set("ETag", etag)
		c.Set("Cache-Control", "private, max-age=86400")
		c.Set("X-Content-Type-Options", "nosniff")
		c.Set("Content-Security-Policy", "default-src 'none'; img-src 'self'")
		c.Set("Content-Disposition", "inline")
		if c.Get("If-None-Match") == etag {
			return c.SendStatus(304)
		}
		c.Set("Content-Type", mime)
		return c.Send(content)
	}
}
