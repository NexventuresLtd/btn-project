package syncsvc

import (
	"context"

	"btn-ops/internal/auth"
	"btn-ops/internal/notify"

	"github.com/jackc/pgx/v5"
)

// Notification events (F1 framework): conflicts and offline negative stock go to sync.resolve holders who can
// see the record's site (OFF-18, SRS §5.5).
const (
	EventConflict      = "sync.conflict"
	EventNegativeStock = "sync.negative_stock"
)

func init() {
	notify.RegisterEvent(notify.Event{Key: EventConflict, Category: notify.CatSync, DefaultEmail: false,
		LabelKey: "sync.event.conflict", DescriptionKey: "sync.event.conflict.help", Permissions: []auth.Permission{"sync.resolve"}})
	notify.RegisterEvent(notify.Event{Key: EventNegativeStock, Category: notify.CatSync, DefaultEmail: true,
		LabelKey: "sync.event.negative_stock", DescriptionKey: "sync.event.negative_stock.help", Permissions: []auth.Permission{"sync.resolve"}})
	OnReview = NotifyReview
}

// NotifyReview is the default OnReview hook: an in-app notification (and email per preferences) to every
// sync.resolve holder of the record's site, except the user who captured it.
func NotifyReview(ctx context.Context, tx pgx.Tx, it ReviewItem) error {
	n := notify.Notification{Event: EventConflict, Severity: notify.Warning, TitleKey: "sync.notify.conflict_title", BodyKey: "sync.notify.conflict_body",
		Params: map[string]any{"type": it.Type, "reason": it.Reason}, Link: "/sync/conflicts?id=" + it.ID,
		To: notify.ToPermissionAtSite("sync.resolve", it.PlantID).Without(it.UserID), DedupKey: it.ID}
	if it.Kind == "negative_stock" {
		n.Event, n.TitleKey, n.BodyKey = EventNegativeStock, "sync.notify.negative_title", "sync.notify.negative_body"
	}
	return notify.Send(ctx, tx, n)
}
