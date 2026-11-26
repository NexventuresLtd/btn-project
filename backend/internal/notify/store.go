package notify

import (
	"context"
	"encoding/json"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/cache"
	"btn-ops/internal/db"
	"btn-ops/internal/i18n"
)

// unread counts are cached briefly per user (the bell polls); Send / mark-read invalidate them.
var unreadCache = cache.New[int](20*time.Second, 8192)

func invalidateUnread(userID string) { unreadCache.Delete(userID) }

// UnreadCount returns the user's unread notification count (cached ~20 s, invalidated on change).
func UnreadCount(ctx context.Context, q db.DBTX, userID string) (int, error) {
	return unreadCache.GetOrLoad(ctx, userID, func(ctx context.Context) (int, error) {
		var n int
		err := q.QueryRow(ctx, `SELECT count(*) FROM notification WHERE user_id=$1 AND read_at IS NULL`, userID).Scan(&n)
		return n, err
	})
}

// Item is a notification as listed for its owner (text rendered in the reader's language).
type Item struct {
	ID        string         `json:"id"`
	Event     string         `json:"event"`
	Category  string         `json:"category"`
	Severity  string         `json:"severity"`
	Title     string         `json:"title"`
	Body      string         `json:"body"`
	Link      *string        `json:"link"`
	Params    map[string]any `json:"params"`
	CreatedAt time.Time      `json:"createdAt"`
	ReadAt    *time.Time     `json:"readAt"`
}

// List returns the user's notifications, newest first.
func List(ctx context.Context, q db.DBTX, userID string, lang i18n.Lang, unreadOnly bool, limit, offset int) ([]Item, int, error) {
	var total int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM notification WHERE user_id=$1 AND ($2::bool = false OR read_at IS NULL)`, userID, unreadOnly).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := q.Query(ctx, `SELECT id, event, severity, title_key, body_key, params, link, created_at, read_at FROM notification
		WHERE user_id=$1 AND ($2::bool = false OR read_at IS NULL) ORDER BY created_at DESC, id DESC LIMIT $3 OFFSET $4`, userID, unreadOnly, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		var it Item
		var tk, bk string
		var pj []byte
		if err := rows.Scan(&it.ID, &it.Event, &it.Severity, &tk, &bk, &pj, &it.Link, &it.CreatedAt, &it.ReadAt); err != nil {
			return nil, 0, err
		}
		it.Params = map[string]any{}
		_ = json.Unmarshal(pj, &it.Params)
		it.Title, it.Body = Render(lang, tk, it.Params), Render(lang, bk, it.Params)
		it.Category = CatSystem
		if ev, ok := LookupEvent(it.Event); ok {
			it.Category = ev.Category
		}
		out = append(out, it)
	}
	return out, total, rows.Err()
}

// MarkRead marks one of the user's notifications read. Returns false when it is not theirs / not found.
func MarkRead(ctx context.Context, q db.DBTX, userID, id string) (bool, error) {
	var exists bool
	if err := q.QueryRow(ctx, `WITH u AS (UPDATE notification SET read_at = COALESCE(read_at, $3) WHERE id=$1 AND user_id=$2 RETURNING 1)
		SELECT EXISTS(SELECT 1 FROM u)`, id, userID, db.Now()).Scan(&exists); err != nil {
		return false, err
	}
	invalidateUnread(userID)
	return exists, nil
}

// MarkAllRead marks every unread notification of the user read and returns how many changed.
func MarkAllRead(ctx context.Context, q db.DBTX, userID string) (int, error) {
	tag, err := q.Exec(ctx, `UPDATE notification SET read_at=$2 WHERE user_id=$1 AND read_at IS NULL`, userID, db.Now())
	invalidateUnread(userID)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// Preference is one event's effective setting for a user (JSON of the preferences API).
type Preference struct {
	Event         string `json:"event"`
	Category      string `json:"category"`
	CategoryLabel string `json:"categoryLabel"`
	Label         string `json:"label"`
	Description   string `json:"description"`
	Critical      bool   `json:"critical"` // in-app cannot be switched off
	DefaultEmail  bool   `json:"defaultEmail"`
	InApp         bool   `json:"inApp"`
	Email         bool   `json:"email"`
}

// Visible reports whether user u may see/tune event e.
func Visible(e Event, role auth.Role) bool {
	if len(e.Permissions) == 0 {
		return true
	}
	for _, p := range e.Permissions {
		if auth.Can(role, p) {
			return true
		}
	}
	return false
}

// Preferences returns the effective preferences of a user for every event visible to their role.
func Preferences(ctx context.Context, q db.DBTX, userID string, role auth.Role, lang i18n.Lang) ([]Preference, error) {
	stored := map[string][2]bool{}
	rows, err := q.Query(ctx, `SELECT event, in_app, email FROM notification_preference WHERE user_id=$1`, userID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var ev string
		var ia, em bool
		if err := rows.Scan(&ev, &ia, &em); err != nil {
			rows.Close()
			return nil, err
		}
		stored[ev] = [2]bool{ia, em}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []Preference{}
	for _, e := range Events() {
		if !Visible(e, role) {
			continue
		}
		p := Preference{Event: e.Key, Category: e.Category, CategoryLabel: i18n.T(lang, "notify.category."+e.Category),
			Label: i18n.T(lang, e.labelKey()), Description: i18n.T(lang, e.descriptionKey()), Critical: e.Critical,
			DefaultEmail: e.DefaultEmail, InApp: true, Email: e.DefaultEmail}
		if p.Description == e.descriptionKey() {
			p.Description = "" // no help text in the catalog
		}
		if s, ok := stored[e.Key]; ok {
			p.InApp, p.Email = s[0], s[1]
		}
		if e.Critical {
			p.InApp = true
		}
		out = append(out, p)
	}
	return out, nil
}

// SetPreference upserts one event preference (critical events always keep in-app on).
func SetPreference(ctx context.Context, q db.DBTX, userID, event string, inApp, email bool) error {
	if e, ok := LookupEvent(event); ok && e.Critical {
		inApp = true
	}
	_, err := q.Exec(ctx, `INSERT INTO notification_preference (user_id, event, in_app, email, updated_at) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (user_id, event) DO UPDATE SET in_app=EXCLUDED.in_app, email=EXCLUDED.email, updated_at=EXCLUDED.updated_at`,
		userID, event, inApp, email, db.Now())
	return err
}
