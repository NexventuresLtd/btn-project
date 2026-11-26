package transfers

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	"btn-ops/internal/approvals"
	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/notify"
	"btn-ops/internal/seq"
	"btn-ops/internal/stock"

	"github.com/jackc/pgx/v5"
)

// Notification events (preferences: Stock / Approvals).
const (
	EvRequested = "transfers.requested"
	EvApproved  = "transfers.approved"
	EvRejected  = "transfers.rejected"
	EvInTransit = "transfers.in_transit"
	EvReceived  = "transfers.received"
	EvShortfall = "transfers.shortfall"
	EvCancelled = "transfers.cancelled"
)

func init() {
	notify.RegisterEvent(notify.Event{Key: EvRequested, Category: notify.CatApprovals, DefaultEmail: true, Permissions: []auth.Permission{"transfers.approve"}})
	notify.RegisterEvent(notify.Event{Key: EvApproved, Category: notify.CatStock, DefaultEmail: true, Permissions: []auth.Permission{"transfers.view"}})
	notify.RegisterEvent(notify.Event{Key: EvRejected, Category: notify.CatStock, DefaultEmail: true, Permissions: []auth.Permission{"transfers.view"}})
	notify.RegisterEvent(notify.Event{Key: EvInTransit, Category: notify.CatStock, DefaultEmail: true, Permissions: []auth.Permission{"transfers.view"}})
	notify.RegisterEvent(notify.Event{Key: EvReceived, Category: notify.CatStock, DefaultEmail: false, Permissions: []auth.Permission{"transfers.view"}})
	notify.RegisterEvent(notify.Event{Key: EvShortfall, Category: notify.CatStock, DefaultEmail: true, Permissions: []auth.Permission{"transfers.view"}})
	notify.RegisterEvent(notify.Event{Key: EvCancelled, Category: notify.CatStock, DefaultEmail: false, Permissions: []auth.Permission{"transfers.view"}})
}

// LineInput is a requested line.
type LineInput struct {
	Kind   stock.ItemKind
	ItemID string
	Qty    float64
	LotRef string
	Note   string
}

// CreateInput is a transfer request.
type CreateInput struct {
	ID       string // optional client uuid (offline)
	FromID   string
	ToID     string
	NeededBy *time.Time
	Note     string
	Lines    []LineInput
}

// parseCreate validates the request body.
func parseCreate(v *httpx.V, lang i18n.Lang) CreateInput {
	in := CreateInput{FromID: stock.IDField(v, "fromPlantId"), ToID: stock.IDField(v, "toPlantId")}
	if s := strings.TrimSpace(v.NullStr("neededBy").Get()); s != "" {
		if t, err := dates.ParseISO(s); err != nil {
			v.Fail("neededBy", "stock.v.valid_date")
		} else {
			in.NeededBy = &t
		}
	}
	// SRS §7.2: a request states the quantity and a reason. The reason travels as "note" (older clients) or "reason".
	in.Note = strings.TrimSpace(v.NullStr("note").Trim().Max(500).Get())
	if in.Note == "" {
		in.Note = strings.TrimSpace(v.NullStr("reason").Trim().Max(500).Get())
	}
	if len([]rune(in.Note)) < 3 {
		v.Fail("note", "transfers.v.reason_required")
	}
	raw, ok := v.Raw("lines").([]any)
	switch {
	case !ok || len(raw) == 0:
		v.Fail("lines", "stock.v.at_least_one")
	case len(raw) > 100:
		v.Fail("lines", "Too big: expected array to have <=100 items")
	default:
		seen := map[string]bool{}
		for i, l := range v.Arr("lines", true) {
			kind := stock.ItemKind(l.Str("itemKind").Trim().OneOf(stock.ItemKinds, "stock.v.item_kind").Get())
			id := stock.IDField(l, "itemId")
			var q float64
			if f, ok := stock.Number(l, "qty", stock.NumRule{TypeMsg: "stock.v.enter_quantity"}); ok {
				if msg := stock.QtyProblem(lang, kind, f, true); msg != "" {
					l.Fail("qty", msg)
				}
				q = stock.Round3(f)
			}
			lot := strings.TrimSpace(l.NullStr("lotRef").Trim().Max(stock.MaxLotRef).Get())
			note := strings.TrimSpace(l.NullStr("note").Trim().Max(200).Get())
			key := string(kind) + ":" + id + ":" + lot
			if id != "" && seen[key] {
				v.Fail("lines."+strconv.Itoa(i)+".itemId", "transfers.v.item_twice")
			}
			seen[key] = true
			in.Lines = append(in.Lines, LineInput{Kind: kind, ItemID: id, Qty: q, LotRef: lot, Note: note})
		}
	}
	if !v.Failed() && in.FromID == in.ToID {
		v.Fail("toPlantId", "transfers.v.same_location")
	}
	return in
}

// requestPerm: may this user request for these locations? A site-scoped user must be at one end.
func assertEitherEnd(u *auth.User, from, to string) error {
	if s := auth.ScopedPlantID(u); s != "" && s != from && s != to {
		return httpx.Forbidden("error.plant_scope")
	}
	return nil
}

func lineKinds(lines []LineInput) []stock.ItemKind {
	var out []stock.ItemKind
	seen := map[stock.ItemKind]bool{}
	for _, l := range lines {
		if !seen[l.Kind] {
			seen[l.Kind] = true
			out = append(out, l.Kind)
		}
	}
	return out
}

func create(ctx context.Context, tx pgx.Tx, lang i18n.Lang, a stock.Actor, in CreateInput) (Transfer, error) {
	from, err := stock.RequireLocation(ctx, tx, lang, in.FromID, "fromPlantId")
	if err != nil {
		return Transfer{}, err
	}
	to, err := stock.RequireLocation(ctx, tx, lang, in.ToID, "toPlantId")
	if err != nil {
		return Transfer{}, err
	}
	items := map[stock.ItemKind]map[string]stock.Item{}
	for _, k := range lineKinds(in.Lines) {
		var ids []string
		for _, l := range in.Lines {
			if l.Kind == k {
				ids = append(ids, l.ItemID)
			}
		}
		m, err := stock.LoadItems(ctx, tx, k, ids)
		if err != nil {
			return Transfer{}, err
		}
		items[k] = m
	}
	fields := map[string]string{}
	var value int64
	for i, l := range in.Lines {
		it, ok := items[l.Kind][l.ItemID]
		switch {
		case !ok:
			fields["lines."+strconv.Itoa(i)+".itemId"] = i18n.T(lang, "stock.err.unknown_item")
		case !it.Active:
			fields["lines."+strconv.Itoa(i)+".itemId"] = i18n.T(lang, "stock.err.item_inactive", "name", it.Name)
		default:
			value += int64(math.Round(l.Qty * float64(it.UnitValue)))
		}
	}
	if len(fields) > 0 {
		return Transfer{}, httpx.BadRequest("error.validation", fields)
	}
	levels, err := stock.RequiredLevels(ctx, tx, approvals.Transfer, value, "transfers.approve", "Transfer approval")
	if err != nil {
		return Transfer{}, err
	}
	number, err := seq.NextNumber(ctx, tx, "TR-"+strconv.Itoa(time.Now().In(dates.Kigali()).Year()))
	if err != nil {
		return Transfer{}, err
	}
	id := in.ID
	if id == "" {
		id = db.NewID()
	}
	if _, err := tx.Exec(ctx, `INSERT INTO stock_transfer (id, number, from_plant_id, to_plant_id, needed_by, note, value, required_levels, requested_by_id, device_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, number, from.ID, to.ID, in.NeededBy, nz(in.Note), value, stock.MustJSON(levels), nz(a.UserID), nz(a.DeviceID)); err != nil {
		return Transfer{}, err
	}
	parts := make([]string, 0, len(in.Lines))
	for i, l := range in.Lines {
		it := items[l.Kind][l.ItemID]
		if _, err := tx.Exec(ctx, `INSERT INTO stock_transfer_line (transfer_id, line_no, item_kind, item_id, qty_requested, lot_ref, unit_value, note)
			VALUES ($1,$2,$3,$4,$5::numeric,$6,$7,$8)`, id, i+1, string(l.Kind), it.ID, stock.Fmt3(l.Qty), nz(l.LotRef), it.UnitValue, nz(l.Note)); err != nil {
			return Transfer{}, err
		}
		parts = append(parts, stock.FmtQty(l.Qty)+" "+it.Unit+" "+it.Code)
	}
	t, err := load(ctx, tx, id, false)
	if err != nil {
		return t, err
	}
	if err := audit.Log(ctx, tx, auditActor(a), audit.Input{Action: "transfers.request", Entity: "StockTransfer", EntityID: id,
		Summary: number + " requested " + from.Code + " → " + to.Code + ": " + strings.Join(parts, ", "), After: t}); err != nil {
		return t, err
	}
	return t, send(ctx, tx, t, EvRequested, notify.Warning, notify.ToPermission("transfers.approve").Without(a.UserID), nil)
}

func auditActor(a stock.Actor) audit.Actor {
	return audit.Actor{UserID: a.UserID, DeviceID: a.DeviceID, IP: a.IP}
}

func nz(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func send(ctx context.Context, tx pgx.Tx, t Transfer, ev string, sev notify.Severity, to notify.Audience, extra map[string]any) error {
	p := map[string]any{"number": t.Number, "from": t.From.Code, "to": t.To.Code, "lines": t.LineCount}
	for k, v := range extra {
		p[k] = v
	}
	key := strings.TrimPrefix(ev, "transfers.")
	return notify.Send(ctx, tx, notify.Notification{Event: ev, Severity: sev, TitleKey: "transfers.notify." + key + "_title", BodyKey: "transfers.notify." + key + "_body",
		Params: p, Link: "/transfers/" + t.ID, To: to, DedupKey: "transfer:" + t.ID}) // one unread per user and event
}

func requester(t Transfer) []string {
	if t.RequestedByID == nil {
		return nil
	}
	return []string{*t.RequestedByID}
}

// stateErr is the 409 for an illegal transition.
func stateErr(t Transfer, action string) error {
	return httpx.NewKey(409, "invalid_state", "transfers.err.state", "number", t.Number, "status", t.Status, "action", action)
}

func approve(ctx context.Context, tx pgx.Tx, lang i18n.Lang, a stock.Actor, id, note string) (Transfer, error) {
	t, err := load(ctx, tx, id, true)
	if err != nil {
		return t, err
	}
	if _, ok := Next(t.Status, ActApprove); !ok {
		return t, stateErr(t, ActApprove)
	}
	if a.Role != auth.RoleSuperAdmin && t.RequestedByID != nil && *t.RequestedByID == a.UserID {
		return t, httpx.NewKey(403, "forbidden", "transfers.err.own_request")
	}
	next := stock.NextLevel(t.RequiredLevels, t.Approvals)
	if next == nil {
		next = &approvals.Level{Level: 1, Permission: "transfers.approve"}
	}
	if !stock.CanApproveLevel(a.Role, *next, "transfers.approve") {
		return t, httpx.NewKey(403, "forbidden", "stock.err.level_required", "level", next.Label)
	}
	if a.Role != auth.RoleSuperAdmin && approvedBy(t, a.UserID) {
		// one decision per person: a later level this person could approve was already covered by that decision
		// (stock.ApproveFrom), so reaching here means the remaining level needs someone else
		return t, httpx.NewKey(403, "forbidden", "transfers.err.already_approved")
	}
	now := db.Now()
	// §7.2 "approved by the Production Manager or per threshold": every level is held by the PM role under SRS §8 and
	// BTN has one PM, so his decision covers the consecutive levels his role satisfies (no deadlock); a threshold
	// level naming another role still needs that person.
	t.Approvals = stock.ApproveFrom(t.RequiredLevels, t.Approvals, *next, a.Role, "transfers.approve", stock.Approval{UserID: a.UserID, Name: a.Name, At: now})
	final := stock.NextLevel(t.RequiredLevels, t.Approvals) == nil
	if final {
		_, err = tx.Exec(ctx, `UPDATE stock_transfer SET status='APPROVED', approvals=$2, approved_by_id=$3, approved_at=$4, updated_at=$4 WHERE id=$1`,
			id, stock.MustJSON(t.Approvals), a.UserID, now)
	} else {
		_, err = tx.Exec(ctx, `UPDATE stock_transfer SET approvals=$2, updated_at=$3 WHERE id=$1`, id, stock.MustJSON(t.Approvals), now)
	}
	if err != nil {
		return t, err
	}
	after, err := load(ctx, tx, id, false)
	if err != nil {
		return after, err
	}
	if err := audit.Log(ctx, tx, auditActor(a), audit.Input{Action: "transfers.approve", Entity: "StockTransfer", EntityID: id,
		Summary: t.Number + " approved (level " + strconv.Itoa(next.Level) + ")" + suffix(note), Before: map[string]any{"status": t.Status}, After: map[string]any{"status": after.Status, "approvals": after.Approvals}}); err != nil {
		return after, err
	}
	if !final {
		return after, send(ctx, tx, after, EvRequested, notify.Warning, notify.ToPermission("transfers.approve").Without(a.UserID), nil)
	}
	if err := send(ctx, tx, after, EvApproved, notify.Info, notify.ToUsers(requester(after)...).Without(a.UserID), nil); err != nil {
		return after, err
	}
	return after, send(ctx, tx, after, EvApproved, notify.Info, notify.ToPermissionAtSite("transfers.dispatch", after.From.ID).Without(a.UserID), nil)
}

func suffix(n string) string {
	if strings.TrimSpace(n) == "" {
		return ""
	}
	return ": " + n
}

func reject(ctx context.Context, tx pgx.Tx, a stock.Actor, id, reason string) (Transfer, error) {
	t, err := load(ctx, tx, id, true)
	if err != nil {
		return t, err
	}
	if _, ok := Next(t.Status, ActReject); !ok {
		return t, stateErr(t, ActReject)
	}
	now := db.Now()
	if _, err := tx.Exec(ctx, `UPDATE stock_transfer SET status='REJECTED', rejected_by_id=$2, rejected_at=$3, reject_reason=$4, updated_at=$3 WHERE id=$1`, id, a.UserID, now, reason); err != nil {
		return t, err
	}
	after, err := load(ctx, tx, id, false)
	if err != nil {
		return after, err
	}
	if err := audit.Log(ctx, tx, auditActor(a), audit.Input{Action: "transfers.reject", Entity: "StockTransfer", EntityID: id, Summary: t.Number + " rejected: " + reason,
		Before: map[string]any{"status": t.Status}, After: map[string]any{"status": Rejected, "reason": reason}}); err != nil {
		return after, err
	}
	return after, send(ctx, tx, after, EvRejected, notify.Warning, notify.ToUsers(requester(after)...).Without(a.UserID), map[string]any{"reason": reason})
}

func cancel(ctx context.Context, tx pgx.Tx, a stock.Actor, id, reason string) (Transfer, error) {
	t, err := load(ctx, tx, id, true)
	if err != nil {
		return t, err
	}
	if t.Status == InTransit {
		return t, httpx.NewKey(409, "invalid_state", "transfers.err.cancel_in_transit", "number", t.Number)
	}
	if _, ok := Next(t.Status, ActCancel); !ok {
		return t, stateErr(t, ActCancel)
	}
	// the requester may withdraw their own request; otherwise an approver
	own := t.RequestedByID != nil && *t.RequestedByID == a.UserID
	if !own && !auth.Can(a.Role, "transfers.approve") {
		return t, httpx.Forbidden()
	}
	now := db.Now()
	if _, err := tx.Exec(ctx, `UPDATE stock_transfer SET status='CANCELLED', cancelled_by_id=$2, cancelled_at=$3, cancel_reason=$4, updated_at=$3 WHERE id=$1`, id, a.UserID, now, reason); err != nil {
		return t, err
	}
	after, err := load(ctx, tx, id, false)
	if err != nil {
		return after, err
	}
	if err := audit.Log(ctx, tx, auditActor(a), audit.Input{Action: "transfers.cancel", Entity: "StockTransfer", EntityID: id, Summary: t.Number + " cancelled: " + reason,
		Before: map[string]any{"status": t.Status}, After: map[string]any{"status": Cancelled, "reason": reason}}); err != nil {
		return after, err
	}
	return after, send(ctx, tx, after, EvCancelled, notify.Info, notify.ToUsers(requester(after)...).Without(a.UserID), map[string]any{"reason": reason})
}

// DispatchLine sets the quantity actually sent.
type DispatchLine struct {
	LineID string
	Qty    float64
	LotRef *string
}

// DispatchInput is the source issue.
type DispatchInput struct {
	Date    time.Time
	DateISO string
	Vehicle string
	Driver  string
	Note    string
	Lines   []DispatchLine // empty = send every line as requested
}

func dispatch(ctx context.Context, tx pgx.Tx, lang i18n.Lang, a stock.Actor, scope, id string, in DispatchInput) (Transfer, error) {
	t, err := load(ctx, tx, id, true)
	if err != nil {
		return t, err
	}
	if scope != "" && scope != t.From.ID {
		return t, httpx.Forbidden("error.plant_scope")
	}
	if _, ok := Next(t.Status, ActDispatch); !ok {
		return t, stateErr(t, ActDispatch)
	}
	byID := map[string]DispatchLine{}
	for _, l := range in.Lines {
		byID[l.LineID] = l
	}
	fields := map[string]string{}
	for i, l := range in.Lines {
		found := false
		for _, tl := range t.Lines {
			if tl.ID == l.LineID {
				found = true
				if stock.Round3(l.Qty) > tl.QtyRequested {
					fields["lines."+strconv.Itoa(i)+".qty"] = i18n.T(lang, "transfers.v.more_than_requested", "qty", stock.FmtQty(tl.QtyRequested))
				}
				if msg := stock.QtyProblem(lang, tl.ItemKind, l.Qty, false); msg != "" && l.Qty != 0 {
					fields["lines."+strconv.Itoa(i)+".qty"] = msg
				}
				if l.Qty < 0 {
					fields["lines."+strconv.Itoa(i)+".qty"] = i18n.T(lang, "stock.v.negative")
				}
			}
		}
		if !found {
			fields["lines."+strconv.Itoa(i)+".lineId"] = i18n.T(lang, "transfers.v.unknown_line")
		}
	}
	if len(fields) > 0 {
		return t, httpx.BadRequest("error.validation", fields)
	}
	from, err := stock.LoadLocation(ctx, tx, t.From.ID)
	if err != nil {
		return t, err
	}
	sent := 0
	for i, tl := range t.Lines {
		qty := tl.QtyRequested
		lot := deref(tl.LotRef)
		if dl, ok := byID[tl.ID]; ok {
			qty = stock.Round3(dl.Qty)
			if dl.LotRef != nil {
				lot = strings.TrimSpace(*dl.LotRef)
			}
		}
		if qty > 0 {
			items, err := stock.LoadItems(ctx, tx, tl.ItemKind, []string{tl.ItemID})
			if err != nil {
				return t, err
			}
			m := stock.Movement{Date: in.Date, Type: stock.TypeTransferOut, Qty: qty, LotRef: lot, Note: t.Number + suffix(in.Note), Reference: t.Number,
				SourceType: stock.SourceTransfer, SourceID: t.ID, CounterpartPlantID: t.To.ID, TransferGroup: t.ID, ActorID: a.UserID, DeviceID: a.DeviceID}
			if _, _, err := stock.Withdraw(ctx, tx, lang, items[tl.ItemID], *from, m, "lines."+strconv.Itoa(i)+".qty", false); err != nil {
				return t, err
			}
			sent++
		}
		if _, err := tx.Exec(ctx, `UPDATE stock_transfer_line SET qty_dispatched=$2::numeric, lot_ref=$3 WHERE id=$1`, tl.ID, stock.Fmt3(qty), nz(lot)); err != nil {
			return t, err
		}
	}
	if sent == 0 {
		return t, httpx.BadRequest("transfers.v.nothing_sent", map[string]string{"lines": i18n.T(lang, "transfers.v.nothing_sent")})
	}
	now := db.Now()
	if _, err := tx.Exec(ctx, `UPDATE stock_transfer SET status='IN_TRANSIT', dispatched_by_id=$2, dispatched_at=$3, dispatch_date=$4, vehicle=$5, driver=$6, dispatch_note=$7, updated_at=$3 WHERE id=$1`,
		id, a.UserID, now, in.Date, nz(in.Vehicle), nz(in.Driver), nz(in.Note)); err != nil {
		return t, err
	}
	if err := checkLow(ctx, tx, t, t.From.ID); err != nil {
		return t, err
	}
	after, err := load(ctx, tx, id, false)
	if err != nil {
		return after, err
	}
	if err := audit.Log(ctx, tx, auditActor(a), audit.Input{Action: "transfers.dispatch", Entity: "StockTransfer", EntityID: id,
		Summary: t.Number + " dispatched from " + t.From.Code + " on " + in.DateISO + suffix(in.Vehicle), Before: map[string]any{"status": t.Status}, After: after}); err != nil {
		return after, err
	}
	if err := send(ctx, tx, after, EvInTransit, notify.Info, notify.ToPermissionAtSite("transfers.receive", after.To.ID).Without(a.UserID), nil); err != nil {
		return after, err
	}
	return after, send(ctx, tx, after, EvInTransit, notify.Info, notify.ToUsers(requester(after)...).Without(a.UserID), nil)
}

func checkLow(ctx context.Context, tx pgx.Tx, t Transfer, plantID string) error {
	ids := map[stock.ItemKind][]string{}
	for _, l := range t.Lines {
		ids[l.ItemKind] = append(ids[l.ItemKind], l.ItemID)
	}
	for k, list := range ids {
		if err := stock.CheckLowStock(ctx, tx, plantID, k, list); err != nil {
			return err
		}
	}
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ReceiveLine confirms a received quantity.
type ReceiveLine struct {
	LineID string
	Qty    float64
	Note   string
}

// ReceiveInput is the destination confirmation.
type ReceiveInput struct {
	Date    time.Time
	DateISO string
	Note    string
	Lines   []ReceiveLine // empty = everything arrived as dispatched
}

func receive(ctx context.Context, tx pgx.Tx, lang i18n.Lang, a stock.Actor, scope, id string, in ReceiveInput) (Transfer, error) {
	t, err := load(ctx, tx, id, true)
	if err != nil {
		return t, err
	}
	if scope != "" && scope != t.To.ID {
		return t, httpx.Forbidden("error.plant_scope")
	}
	if _, ok := Next(t.Status, ActReceive); !ok {
		return t, stateErr(t, ActReceive)
	}
	if t.DispatchDate != nil && in.DateISO < *t.DispatchDate {
		return t, httpx.BadRequest("error.validation", map[string]string{"date": i18n.T(lang, "transfers.v.before_dispatch", "date", dates.FormatDate(dates.MustParseISO(*t.DispatchDate)))})
	}
	byID := map[string]ReceiveLine{}
	fields := map[string]string{}
	for i, l := range in.Lines {
		byID[l.LineID] = l
		found := false
		for _, tl := range t.Lines {
			if tl.ID != l.LineID {
				continue
			}
			found = true
			d := 0.0
			if tl.QtyDispatched != nil {
				d = *tl.QtyDispatched
			}
			switch {
			case l.Qty < 0:
				fields["lines."+strconv.Itoa(i)+".qty"] = i18n.T(lang, "stock.v.negative")
			case stock.Round3(l.Qty) > d:
				fields["lines."+strconv.Itoa(i)+".qty"] = i18n.T(lang, "transfers.v.more_than_sent", "qty", stock.FmtQty(d))
			case l.Qty != 0 && stock.QtyProblem(lang, tl.ItemKind, l.Qty, true) != "":
				fields["lines."+strconv.Itoa(i)+".qty"] = stock.QtyProblem(lang, tl.ItemKind, l.Qty, true)
			}
		}
		if !found {
			fields["lines."+strconv.Itoa(i)+".lineId"] = i18n.T(lang, "transfers.v.unknown_line")
		}
	}
	if len(fields) > 0 {
		return t, httpx.BadRequest("error.validation", fields)
	}
	shortfall := false
	var shortParts []string
	for _, tl := range t.Lines {
		d := 0.0
		if tl.QtyDispatched != nil {
			d = *tl.QtyDispatched
		}
		got := d
		note := ""
		if rl, ok := byID[tl.ID]; ok {
			got, note = stock.Round3(rl.Qty), rl.Note
		}
		if _, err := tx.Exec(ctx, `UPDATE stock_transfer_line SET qty_received=$2::numeric, note=COALESCE($3, note) WHERE id=$1`, tl.ID, stock.Fmt3(got), nz(note)); err != nil {
			return t, err
		}
		if d <= 0 {
			continue
		}
		// The destination books what was dispatched; a shortfall is a reviewed negative adjustment (STK-04).
		if _, err := stock.Post(ctx, tx, stock.Movement{Kind: tl.ItemKind, PlantID: t.To.ID, ItemID: tl.ItemID, Date: in.Date, Type: stock.TypeTransferIn, Qty: d,
			LotRef: deref(tl.LotRef), Note: t.Number + suffix(in.Note), Reference: t.Number, SourceType: stock.SourceTransfer, SourceID: t.ID,
			CounterpartPlantID: t.From.ID, TransferGroup: t.ID, ActorID: a.UserID, DeviceID: a.DeviceID}); err != nil {
			return t, err
		}
		if s := Shortfall(d, got); s > 0 {
			shortfall = true
			reason := i18n.T(i18n.EN, "transfers.shortfall_reason", "number", t.Number, "sent", stock.FmtQty(d), "received", stock.FmtQty(got))
			if note != "" {
				reason += ": " + note
			}
			if _, err := stock.CreateAdjustment(ctx, tx, lang, a, stock.AdjustmentInput{PlantID: t.To.ID, Kind: tl.ItemKind, ItemID: tl.ItemID, Date: in.Date,
				Qty: -s, LotRef: deref(tl.LotRef), Reason: reason, SourceType: "transfer_shortfall", SourceID: t.ID}); err != nil {
				return t, err
			}
			shortParts = append(shortParts, stock.FmtQty(s)+" "+anyStr(tl.Item["unit"])+" "+anyStr(tl.Item["name"]))
		}
	}
	now := db.Now()
	if _, err := tx.Exec(ctx, `UPDATE stock_transfer SET status='RECEIVED', received_by_id=$2, received_at=$3, receive_date=$4, receive_note=$5, has_shortfall=$6, updated_at=$3 WHERE id=$1`,
		id, a.UserID, now, in.Date, nz(in.Note), shortfall); err != nil {
		return t, err
	}
	if err := checkLow(ctx, tx, t, t.To.ID); err != nil {
		return t, err
	}
	after, err := load(ctx, tx, id, false)
	if err != nil {
		return after, err
	}
	sum := t.Number + " received at " + t.To.Code + " on " + in.DateISO
	if shortfall {
		sum += "; shortfall " + strings.Join(shortParts, ", ") + " sent for review"
	}
	if err := audit.Log(ctx, tx, auditActor(a), audit.Input{Action: "transfers.receive", Entity: "StockTransfer", EntityID: id, Summary: sum,
		Before: map[string]any{"status": t.Status}, After: after}); err != nil {
		return after, err
	}
	aud := notify.ToUsers(requester(after)...).Without(a.UserID)
	if shortfall {
		extra := map[string]any{"shortfall": strings.Join(shortParts, ", ")}
		if err := send(ctx, tx, after, EvShortfall, notify.Warning, aud, extra); err != nil {
			return after, err
		}
		if err := send(ctx, tx, after, EvShortfall, notify.Warning, notify.ToPermission("transfers.approve").Without(a.UserID), extra); err != nil {
			return after, err
		}
		return after, send(ctx, tx, after, EvShortfall, notify.Warning, notify.ToPermissionAtSite("transfers.dispatch", after.From.ID).Without(a.UserID), extra)
	}
	if err := send(ctx, tx, after, EvReceived, notify.Info, aud, nil); err != nil {
		return after, err
	}
	return after, send(ctx, tx, after, EvReceived, notify.Info, notify.ToPermissionAtSite("transfers.dispatch", after.From.ID).Without(a.UserID), nil)
}

func anyStr(v any) string {
	s, _ := v.(string)
	return s
}

// reverse cancels an in-transit transfer by booking the dispatched goods back at the source.
func reverse(ctx context.Context, tx pgx.Tx, a stock.Actor, id, reason string) (Transfer, error) {
	t, err := load(ctx, tx, id, true)
	if err != nil {
		return t, err
	}
	if _, ok := Next(t.Status, ActReverse); !ok {
		return t, stateErr(t, ActReverse)
	}
	today := dates.MustParseISO(dates.TodayISO())
	for _, tl := range t.Lines {
		if tl.QtyDispatched == nil || *tl.QtyDispatched <= 0 {
			continue
		}
		if _, err := stock.Post(ctx, tx, stock.Movement{Kind: tl.ItemKind, PlantID: t.From.ID, ItemID: tl.ItemID, Date: today, Type: stock.TypeTransferIn,
			Qty: *tl.QtyDispatched, LotRef: deref(tl.LotRef), Note: t.Number + " reversed: " + reason, Reference: t.Number, SourceType: stock.SourceTransferReversal,
			SourceID: t.ID, CounterpartPlantID: t.To.ID, TransferGroup: t.ID, ActorID: a.UserID, DeviceID: a.DeviceID}); err != nil {
			return t, err
		}
	}
	now := db.Now()
	if _, err := tx.Exec(ctx, `UPDATE stock_transfer SET status='CANCELLED', reversed=true, cancelled_by_id=$2, cancelled_at=$3, cancel_reason=$4, updated_at=$3 WHERE id=$1`,
		id, a.UserID, now, reason); err != nil {
		return t, err
	}
	if err := checkLow(ctx, tx, t, t.From.ID); err != nil {
		return t, err
	}
	after, err := load(ctx, tx, id, false)
	if err != nil {
		return after, err
	}
	if err := audit.Log(ctx, tx, auditActor(a), audit.Input{Action: "transfers.reverse", Entity: "StockTransfer", EntityID: id,
		Summary: t.Number + " reversed (goods booked back at " + t.From.Code + "): " + reason, Before: map[string]any{"status": t.Status}, After: map[string]any{"status": Cancelled, "reversed": true}}); err != nil {
		return after, err
	}
	if err := send(ctx, tx, after, EvCancelled, notify.Warning, notify.ToUsers(requester(after)...).Without(a.UserID), map[string]any{"reason": reason}); err != nil {
		return after, err
	}
	return after, send(ctx, tx, after, EvCancelled, notify.Warning, notify.ToPermissionAtSite("transfers.receive", after.To.ID).Without(a.UserID), map[string]any{"reason": reason})
}

// approvedBy reports whether userID already decided an earlier level of t.
func approvedBy(t Transfer, userID string) bool {
	for _, ap := range t.Approvals {
		if ap.UserID == userID {
			return true
		}
	}
	return false
}

// actionsFor lists what the user may do now (drives the UI buttons; the API re-checks everything).
func actionsFor(u *auth.User, t Transfer) []string {
	out := []string{}
	scope := auth.ScopedPlantID(u)
	can := func(p auth.Permission) bool { return auth.Can(u.Role, p) }
	own := t.RequestedByID != nil && *t.RequestedByID == u.ID
	switch t.Status {
	case Requested:
		if can("transfers.approve") && (!own || u.Role == auth.RoleSuperAdmin) {
			if n := stock.NextLevel(t.RequiredLevels, t.Approvals); (n == nil || stock.CanApproveLevel(u.Role, *n, "transfers.approve")) &&
				(u.Role == auth.RoleSuperAdmin || !approvedBy(t, u.ID)) {
				out = append(out, ActApprove)
			}
			out = append(out, ActReject)
		}
		if own || can("transfers.approve") {
			out = append(out, ActCancel)
		}
	case Approved:
		if can("transfers.dispatch") && (scope == "" || scope == t.From.ID) {
			out = append(out, ActDispatch)
		}
		if own || can("transfers.approve") {
			out = append(out, ActCancel)
		}
	case InTransit:
		if can("transfers.receive") && (scope == "" || scope == t.To.ID) {
			out = append(out, ActReceive)
		}
		if can("transfers.approve") {
			out = append(out, ActReverse)
		}
	}
	return out
}
