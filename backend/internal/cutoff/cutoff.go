// Package cutoff states the data cut-off of a report and how many sites still hold unsynchronised offline
// data (SRS RPT-06). Every report surface uses it:
//
//   - PDFs: pdf.New adds two meta rows ("Data cut-off", "Unsynchronised data") through pdf.MetaHook;
//   - Excel/JSON exports: export.SendDoc adds the same lines to the filters block through export.FiltersHook;
//   - screens: GET /api/reports/cutoff?plantId= (and the dashboards embed Info) feed <DataCutoff />;
//   - scheduled e-mails: the body repeats Note().
//
// "Pending" comes from what each active device last reported to the sync server (device.pending_count, sent
// with every push and with GET /sync/status?pending=N, F3). A site has unsynchronised data when at least one
// active device of a user assigned to that site reports pending records. Devices of users without a site
// (managers working across sites) are counted separately.
package cutoff

import (
	"context"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"btn-ops/internal/cache"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/i18n"
)

// Site is a site with unsynchronised data.
type Site struct {
	ID             string     `json:"id"`
	Code           string     `json:"code"`
	Name           string     `json:"name"`
	Devices        int        `json:"devices"`
	PendingRecords int        `json:"pendingRecords"`
	OldestSyncAt   *time.Time `json:"oldestSyncAt"` // earliest last-sync among the pending devices (nil = never synced)
}

// Info is the cut-off statement of one report.
type Info struct {
	At                 time.Time `json:"at"` // server data read at this instant (UTC); records captured offline later are not included
	PendingSites       int       `json:"pendingSites"`
	Sites              []Site    `json:"sites"`
	PendingDevices     int       `json:"pendingDevices"`
	PendingRecords     int       `json:"pendingRecords"`
	UnassignedDevices  int       `json:"unassignedDevices"` // pending devices of users without a site
	PlantID            string    `json:"plantId,omitempty"`
	Note               string    `json:"note"` // localized sentence (same text as PDFs/e-mails)
	CutoffLabel        string    `json:"cutoffLabel"`
	UnsynchronisedNote string    `json:"unsynchronisedNote,omitempty"`
}

// HasPending reports whether any device still holds data that is not on the server.
func (i Info) HasPending() bool { return i.PendingDevices > 0 }

var infoCache = cache.New[Info](15*time.Second, 256)

// Invalidate drops cached results (tests; the TTL bounds staleness otherwise).
func Invalidate() { infoCache.InvalidatePrefix("") }

// Load computes the cut-off for plantID ("" = every site) at now. The device query is cached 15 s per plant.
func Load(ctx context.Context, q db.DBTX, plantID string, lang i18n.Lang) (Info, error) {
	now := time.Now().UTC().Truncate(time.Second)
	base, err := infoCache.GetOrLoad(ctx, "p:"+plantID, func(ctx context.Context) (Info, error) { return load(ctx, q, plantID) })
	if err != nil {
		return Info{}, err
	}
	base.At = now
	base.PlantID = plantID
	return Localize(base, lang), nil
}

func load(ctx context.Context, q db.DBTX, plantID string) (Info, error) {
	out := Info{Sites: []Site{}}
	rows, err := q.Query(ctx, `SELECT u."plantId", p.code, p.name, count(*)::int, coalesce(sum(d.pending_count),0)::int,
			min(coalesce(d.last_sync_at, 'epoch'::timestamp))
		FROM device d JOIN "User" u ON u.id = d.user_id LEFT JOIN "Plant" p ON p.id = u."plantId"
		WHERE d.status = 'active' AND coalesce(d.pending_count,0) > 0 AND u."isActive"
			AND ($1 = '' OR u."plantId" = $1)
		GROUP BY 1, 2, 3 ORDER BY p.code NULLS LAST`, plantID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var pid, code, name *string
		var n, recs int
		var oldest time.Time
		if err := rows.Scan(&pid, &code, &name, &n, &recs, &oldest); err != nil {
			return out, err
		}
		out.PendingDevices += n
		out.PendingRecords += recs
		if pid == nil {
			out.UnassignedDevices += n
			continue
		}
		s := Site{ID: *pid, Code: deref(code), Name: deref(name), Devices: n, PendingRecords: recs}
		if oldest.Year() > 1970 {
			o := oldest.UTC()
			s.OldestSyncAt = &o
		}
		out.Sites = append(out.Sites, s)
	}
	out.PendingSites = len(out.Sites)
	return out, rows.Err()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Localize fills the human-readable fields in lang.
func Localize(i Info, lang i18n.Lang) Info {
	i.CutoffLabel = dates.FormatDateTime(i.At)
	i.UnsynchronisedNote = Unsynchronised(i, lang)
	i.Note = Note(i, lang)
	return i
}

// SiteCodes is "BTN-1, BTN-3" (at most max codes, then "+n").
func SiteCodes(i Info, max int) string {
	codes := make([]string, 0, len(i.Sites))
	for k, s := range i.Sites {
		if k == max {
			codes = append(codes, "+"+strconv.Itoa(len(i.Sites)-max))
			break
		}
		codes = append(codes, s.Code)
	}
	return strings.Join(codes, ", ")
}

// Unsynchronised is the short "2 sites (BTN-1, BTN-3)" value; "" when nothing is pending.
func Unsynchronised(i Info, lang i18n.Lang) string {
	if !i.HasPending() {
		return ""
	}
	parts := []string{}
	if i.PendingSites > 0 {
		key := "cutoff.sites_other"
		if i.PendingSites == 1 {
			key = "cutoff.sites_one"
		}
		parts = append(parts, i18n.T(lang, key, "count", i.PendingSites, "codes", SiteCodes(i, 6)))
	}
	if i.UnassignedDevices > 0 {
		parts = append(parts, i18n.T(lang, "cutoff.unassigned", "count", i.UnassignedDevices))
	}
	return strings.Join(parts, " · ")
}

// Note is the full sentence printed on reports and e-mails.
func Note(i Info, lang i18n.Lang) string {
	s := i18n.T(lang, "cutoff.note", "at", dates.FormatDateTime(i.At))
	if i.HasPending() {
		s += " " + i18n.T(lang, "cutoff.note_pending", "sites", i.PendingSites, "devices", i.PendingDevices, "records", i.PendingRecords,
			"list", Unsynchronised(i, lang))
	} else {
		s += " " + i18n.T(lang, "cutoff.note_none")
	}
	return s
}

// ── hooks (installed by Install) ───────────────────────────────────────────────

var pool atomic.Pointer[db.DBTX]

// SetSource sets the database the PDF/export hooks read device state from.
func SetSource(q db.DBTX) { pool.Store(&q) }

// Source is the database set by SetSource (nil before start-up).
func Source() db.DBTX {
	if p := pool.Load(); p != nil {
		return *p
	}
	return nil
}
