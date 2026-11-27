package syncsvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// Cursor is the delta-pull position (OFF-11): rows strictly after (Xid, Version) in (server_xid, server_version)
// order. A pull only returns rows below Upper, which is held back only by in-flight transactions writing synced rows
// (see pullUpper), so a long transaction cannot be skipped and an unrelated one does not stall the pull.
// Serialized as "<xid>.<version>.<scope>[.<upper>]"; "" = from the beginning. The scope part fingerprints the user's
// role, site and entity filter: when it changes the pull restarts from zero (reset=true). The optional upper part is
// set on hasMore pages only: the next page of the series never uses a lower bound than the page before it, so a page
// series cannot end early with rows still below the bound it started with.
type Cursor struct {
	Xid     uint64
	Version int64
	Scope   string
	upper   uint64 // exclusive upper stamp bound of this pull (pullUpper)
	carry   uint64 // bound of the previous page of this series (0 = none)
	limit   int
}

// Limit is the maximum number of changes the pull wants from each entity.
func (c Cursor) Limit() int { return c.limit }

// Upper is the exclusive xid bound; use it via Rows/PullTable (do not compare yourself).
func (c Cursor) Upper() uint64 { return c.upper }

func (c Cursor) String() string {
	if c.carry > 0 {
		return fmt.Sprintf("%d.%d.%s.%d", c.Xid, c.Version, c.Scope, c.carry)
	}
	return fmt.Sprintf("%d.%d.%s", c.Xid, c.Version, c.Scope)
}

// ParseCursor parses "<xid>.<version>[.<scope>[.<upper>]]" ("" or "0" = zero cursor).
func ParseCursor(s string) (Cursor, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return Cursor{}, nil
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 || len(parts) > 4 {
		return Cursor{}, fmt.Errorf("bad cursor")
	}
	x, err1 := strconv.ParseUint(parts[0], 10, 64)
	v, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil || v < -1 {
		return Cursor{}, fmt.Errorf("bad cursor")
	}
	c := Cursor{Xid: x, Version: v}
	if len(parts) >= 3 {
		c.Scope = parts[2]
	}
	if len(parts) == 4 {
		u, err := strconv.ParseUint(parts[3], 10, 64)
		if err != nil {
			return Cursor{}, fmt.Errorf("bad cursor")
		}
		c.carry = u
	}
	return c, nil
}

func scopeFingerprint(a Actor, names []string) string {
	h := sha256.Sum256([]byte(string(a.Role) + "|" + a.ScopedPlantID() + "|" + strings.Join(names, ",")))
	return hex.EncodeToString(h[:6])
}

func (c Cursor) less(x uint64, v int64) bool { return c.Xid < x || (c.Xid == x && c.Version < v) }

// Change is one row sent to clients. Op is "upsert" or "delete".
type Change struct {
	Entity  string `json:"entity"`
	ID      string `json:"id"`
	Op      string `json:"op"`
	Version int64  `json:"version"`
	Data    any    `json:"data"`
	xid     uint64
}

// PullFunc returns changes of one entity after since (≤ since.Limit(), ascending (xid, version)). Use PullTable or
// Rows to build the query; actor.DB is the pull's read-only snapshot transaction.
type PullFunc func(ctx context.Context, actor Actor, since Cursor) ([]Change, error)

// SnapshotFunc returns the full current set of a small derived entity (sent on the last page of every pull).
type SnapshotFunc func(ctx context.Context, actor Actor) ([]Change, error)

type pullEntry struct {
	pull  PullFunc
	snap  SnapshotFunc
	perms []auth.Permission
}

// PullOption configures RegisterPull.
type PullOption func(*pullEntry)

// PullRequires limits an entity to users holding ANY of perms (others never receive it).
func PullRequires(perms ...auth.Permission) PullOption {
	for _, p := range perms {
		if !auth.Known(p) {
			panic("syncsvc: unknown permission " + string(p))
		}
	}
	return func(e *pullEntry) { e.perms = append(e.perms, perms...) }
}

// RegisterPull registers a versioned entity for delta pull (re-registering an entity replaces it).
func RegisterPull(entity string, fn PullFunc, opts ...PullOption) {
	regMu.Lock()
	defer regMu.Unlock()
	e := pullEntry{pull: fn}
	for _, o := range opts {
		o(&e)
	}
	pulls[entity] = e
}

// RegisterSnapshot registers a snapshot entity (small sets recomputed every pull, e.g. balances).
func RegisterSnapshot(entity string, fn SnapshotFunc, opts ...PullOption) {
	regMu.Lock()
	defer regMu.Unlock()
	e := pullEntry{snap: fn}
	for _, o := range opts {
		o(&e)
	}
	snapshots[entity] = e
}

// Entities lists registered pull and snapshot entities.
func Entities() (versioned, snapshot []string) {
	regMu.RLock()
	defer regMu.RUnlock()
	for k := range pulls {
		versioned = append(versioned, k)
	}
	for k := range snapshots {
		snapshot = append(snapshot, k)
	}
	sort.Strings(versioned)
	sort.Strings(snapshot)
	return
}

func allowed(a Actor, perms []auth.Permission) bool {
	if len(perms) == 0 {
		return true
	}
	for _, p := range perms {
		if auth.Can(a.Role, p) {
			return true
		}
	}
	return false
}

// Rows runs a versioned query for a pull. selectSQL must select, in order: id (text), data (jsonb),
// server_version, server_xid — FROM the synced table, optionally with its own WHERE (use $1.. for args;
// the cursor conditions are appended as further placeholders). Example:
//
//	syncsvc.Rows(ctx, actor, since, "attendance",
//	  `SELECT a.id, to_jsonb(a) - 'server_xid', a.server_version, a.server_xid FROM "Attendance" a WHERE a."plantId" = $1`, plant)
//
// An empty WHERE is fine (`... FROM "Product" p`). Tombstones of the entity are merged in automatically.
func Rows(ctx context.Context, actor Actor, since Cursor, entity, selectSQL string, args ...any) ([]Change, error) {
	q := actor.DB
	if q == nil {
		return nil, errf("Rows called outside a pull")
	}
	n := len(args)
	sql := `SELECT id, data, server_version, server_xid::text FROM (` + selectSQL + `) AS t(id, data, server_version, server_xid)
		WHERE (server_xid, server_version) > ($` + itoa(n+1) + `::text::xid8, $` + itoa(n+2) + `::bigint) AND server_xid < $` + itoa(n+3) + `::text::xid8
		ORDER BY server_xid, server_version LIMIT $` + itoa(n+4)
	all := append(append([]any{}, args...), strconv.FormatUint(since.Xid, 10), since.Version, strconv.FormatUint(since.upper, 10), since.limit+1)
	rows, err := q.Query(ctx, sql, all...)
	if err != nil {
		return nil, err
	}
	out := []Change{}
	for rows.Next() {
		var ch Change
		var raw []byte
		var xs string
		if err := rows.Scan(&ch.ID, &raw, &ch.Version, &xs); err != nil {
			rows.Close()
			return nil, err
		}
		ch.xid, _ = strconv.ParseUint(xs, 10, 64)
		ch.Entity, ch.Op, ch.Data = entity, "upsert", json.RawMessage(raw)
		out = append(out, ch)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ts, err := Tombstones(ctx, actor, since, entity)
	if err != nil {
		return nil, err
	}
	return mergeSorted(out, ts), nil
}

// Tombstones returns hard-delete markers of entity after since (scoped to the actor's site when it is set).
func Tombstones(ctx context.Context, actor Actor, since Cursor, entity string) ([]Change, error) {
	rows, err := actor.DB.Query(ctx, `SELECT row_id, server_version, server_xid::text FROM sync_tombstone
		WHERE entity=$1 AND (server_xid, server_version) > ($2::text::xid8, $3::bigint) AND server_xid < $4::text::xid8
		AND ($5 = '' OR plant_id IS NULL OR plant_id = $5) ORDER BY server_xid, server_version LIMIT $6`,
		entity, strconv.FormatUint(since.Xid, 10), since.Version, strconv.FormatUint(since.upper, 10), actor.ScopedPlantID(), since.limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Change{}
	for rows.Next() {
		var ch Change
		var xs string
		if err := rows.Scan(&ch.ID, &ch.Version, &xs); err != nil {
			return nil, err
		}
		ch.xid, _ = strconv.ParseUint(xs, 10, 64)
		ch.Entity, ch.Op = entity, "delete"
		out = append(out, ch)
	}
	return out, rows.Err()
}

func mergeSorted(a, b []Change) []Change {
	if len(b) == 0 {
		return a
	}
	out := append(append([]Change{}, a...), b...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].xid != out[j].xid {
			return out[i].xid < out[j].xid
		}
		return out[i].Version < out[j].Version
	})
	return out
}

// PullResult is the pull answer.
type PullResult struct {
	Changes []Change `json:"changes"`
	Cursor  string   `json:"cursor"`
	HasMore bool     `json:"hasMore"`
	// Reset is true when the cursor was dropped (role, site or entity filter changed): clear the local
	// reference tables of the pulled entities before applying these changes.
	Reset bool `json:"reset"`
	// Pending is true when rows of transactions still in flight may land below the bound shortly (OFF-11 guard):
	// pull again soon (the client retries after a few seconds) even when hasMore is false.
	Pending    bool      `json:"pending"`
	ServerTime time.Time `json:"serverTime"`
}

// PersonalDataAccess logs personal-data downloads (wired to privacy.LogAccess in internal/app; a variable to avoid
// an import cycle, since modules that import syncsvc are imported by privacy).
var PersonalDataAccess func(ctx context.Context, q db.DBTX, c *fiber.Ctx, subjectType, subjectID, action string) error

// GET /sync/pull?cursor=&limit=&entities=a,b
func (m *module) pull(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	httpx.NoStore(c)
	u := auth.MustUser(c)
	if _, err := clientSchema(c); err != nil { // §9.2
		return err
	}
	since, err := ParseCursor(c.Query("cursor"))
	if err != nil {
		return httpx.NewKey(400, "bad_cursor", "sync.bad_cursor")
	}
	limit := c.QueryInt("limit", 500)
	if limit < 1 || limit > 1000 {
		limit = 500
	}
	var only map[string]bool
	if s := strings.TrimSpace(c.Query("entities", c.Query("scope"))); s != "" {
		only = map[string]bool{}
		for _, e := range strings.Split(s, ",") {
			only[strings.TrimSpace(e)] = true
		}
	}
	res, err := m.Pull(ctx, c, u, since, limit, only)
	if err != nil {
		return err
	}
	// NFR-D2: worker / customer personal data downloaded to a device is logged like any other access.
	seen := map[string]bool{}
	for _, ch := range res.Changes {
		if (ch.Entity == "workers" || ch.Entity == "customers") && !seen[ch.Entity] {
			seen[ch.Entity] = true
			subject := "worker"
			if ch.Entity == "customers" {
				subject = "customer"
			}
			if PersonalDataAccess != nil {
				if err := PersonalDataAccess(ctx, m.d.Pool, c, subject, "", "list"); err != nil {
					return err
				}
			}
		}
	}
	return c.JSON(res)
}

// Pull computes one page of changes for u (exported for tests/tools).
func (m *module) Pull(ctx context.Context, c *fiber.Ctx, u *auth.User, since Cursor, limit int, only map[string]bool) (*PullResult, error) {
	var res *PullResult
	snap := auth.PermSnapshot{Role: u.Role, PlantID: u.PlantID, Permissions: auth.PermissionsFor(u.Role)}
	// Step 1 (before the snapshot): raise the watermark to the live xmax M, then list the sync-lock holders.
	var wm string
	var holders []string
	if err := m.d.Pool.QueryRow(ctx, `SELECT wm::text, holders FROM btn_sync_pull_bound()`).Scan(&wm, &holders); err != nil {
		return nil, err
	}
	err := pullTx(ctx, m.d.Pool, func(tx pgx.Tx) error {
		upper, now, err := pullUpper(ctx, tx, wm, holders)
		if err != nil {
			return err
		}
		m64, _ := strconv.ParseUint(wm, 10, 64)
		since.upper = upper
		since.limit = limit
		actor := m.actorFor(c, u, u.DeviceRowID, u.DeviceID, snap)
		actor.DB = tx
		regMu.RLock()
		entries := map[string]pullEntry{}
		for k, e := range pulls {
			entries[k] = e
		}
		snaps := map[string]pullEntry{}
		for k, e := range snapshots {
			snaps[k] = e
		}
		regMu.RUnlock()
		all := []Change{}
		names := []string{}
		for k, e := range entries {
			if (only == nil || only[k]) && allowed(actor, e.perms) {
				names = append(names, k)
			}
		}
		sort.Strings(names)
		fp := scopeFingerprint(actor, names)
		reset := false
		if (since.Xid != 0 || since.Version != 0) && since.Scope != fp {
			since.Xid, since.Version, since.carry, reset = 0, 0, 0, true
		}
		// Within a page series the bound never drops: the previous page's bound stays valid (every row below it was
		// already visible then, so it is visible now), and the larger of two valid bounds is valid.
		if since.carry > since.upper {
			since.upper = since.carry
		}
		since.carry = 0
		since.Scope = fp
		for _, name := range names {
			e := entries[name]
			ch, err := e.pull(ctx, actor, since)
			if err != nil {
				return fmt.Errorf("pull %s: %w", name, err)
			}
			for _, x := range ch {
				if since.less(x.xid, x.Version) && x.xid < since.upper { // defensive: drop rows outside the window
					all = append(all, x)
				}
			}
		}
		sort.SliceStable(all, func(i, j int) bool {
			if all[i].xid != all[j].xid {
				return all[i].xid < all[j].xid
			}
			return all[i].Version < all[j].Version
		})
		// Pending: in-flight writers of synced rows hold the window below the live watermark; their rows arrive shortly,
		// so the client should pull again soon instead of waiting for its next scheduled sync.
		out := &PullResult{Changes: []Change{}, ServerTime: now.UTC(), Reset: reset, Pending: since.upper < m64}
		next := since
		if len(all) > limit {
			out.HasMore = true
			all = all[:limit]
		}
		out.Changes = append(out.Changes, all...)
		if out.HasMore {
			last := all[len(all)-1]
			next.Xid, next.Version = last.xid, last.Version
			next.carry = since.upper // the next page keeps (at least) this bound
		} else {
			// Everything below upper has been delivered: jump the cursor to the window edge.
			if since.upper > 0 && (next.Xid < since.upper) {
				next.Xid, next.Version = since.upper-1, 1<<62
			}
			sn := make([]string, 0, len(snaps))
			for k := range snaps {
				sn = append(sn, k)
			}
			sort.Strings(sn)
			for _, name := range sn {
				e := snaps[name]
				if (only != nil && !only[name]) || !allowed(actor, e.perms) {
					continue
				}
				ch, err := e.snap(ctx, actor)
				if err != nil {
					return fmt.Errorf("snapshot %s: %w", name, err)
				}
				out.Changes = append(out.Changes, ch...)
			}
		}
		out.Cursor = next.String()
		res = out
		return nil
	})
	return res, err
}

// Lock class shared with migrations/03250_sync_watermark.sql and 03260_sync_pull_bound.sql.
const syncStampLockClass = 1112821331

// pullUpper computes the exclusive upper bound of a pull window (OFF-11).
//
// Invariant: every row stamped s < upper is visible in the pull's snapshot σ, so advancing the cursor to upper-1
// can never skip a row, and a row below the bound can never commit later.
//
// Writers stamp s = greatest(own xid X, watermark read AFTER taking the shared sync lock of their schema); X is
// assigned before the lock is taken and the lock is held until the writer ends. Before σ, btn_sync_pull_bound()
// published the watermark M (M ≤ xmax(σ)) at time tp and THEN read the lock holders H at time tl (tp < tl < σ).
// upper = min(M, every X in xip(σ) ∩ H).
// Proof, for a row of writer W stamped s and invisible in σ: if X ≥ xmax(σ) then s ≥ X ≥ M ≥ upper. Otherwise X is
// in xip(σ) (W had not ended at σ):
//   - W took the lock before tl: it still held it at tl (it ends after σ > tl), so X ∈ H and s ≥ X ≥ upper;
//   - W took the lock after tl: it read the watermark after tp, so s ≥ M ≥ upper.
// Only transactions writing synced rows of this schema hold the window back; unrelated transactions (another table,
// schema or database, or ones that just finished) do not move the bound, so it cannot dip under load.
func pullUpper(ctx context.Context, tx pgx.Tx, wm string, holders []string) (uint64, time.Time, error) {
	var now time.Time
	var upper string
	// The first statement of the REPEATABLE READ transaction takes σ.
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT min(x) FROM pg_snapshot_xip(pg_current_snapshot()) x
		WHERE x < $1::text::xid8 AND xid(x)::text = ANY($2::text[])), $1::text::xid8)::text, now()`,
		wm, holders).Scan(&upper, &now); err != nil {
		return 0, now, err
	}
	u, err := strconv.ParseUint(upper, 10, 64)
	return u, now, err
}

// pullTx runs fn in a REPEATABLE READ, READ ONLY transaction (one consistent snapshot for all entities).
func pullTx(ctx context.Context, pool *db.Pool, fn func(tx pgx.Tx) error) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
