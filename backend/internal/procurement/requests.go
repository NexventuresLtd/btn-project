package procurement

// Purchase requests (PRC-01), approvals by value and requester (PRC-02) and request status tracking (PRC-05).

import (
	"context"
	"encoding/json"
	"fmt"
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
	"btn-ops/internal/refdata"
	"btn-ops/internal/seq"
	"btn-ops/internal/stock"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// Item types of a request / order line.
const (
	ItemMaterial = "MATERIAL"
	ItemProduct  = "PRODUCT"
	ItemOther    = "OTHER"
)

// Limits.
const (
	maxLines         = 50
	maxQty           = 99_999_999
	maxUnitCost      = 1_000_000_000
	minJustification = 5
	maxJustification = 1000
)

// ───────────────────────────── input (shared with the offline handler) ─────────────────────────────

type lineInput struct {
	ItemType   string
	MaterialID string
	ProductID  string
	Desc       string
	Unit       string
	Qty        float64
	UnitCost   int64
	Category   *string
	Note       *string
}

type requestInput struct {
	PlantID      string
	Department   *string
	RequiredISO  string
	Required     time.Time
	Justify      string
	SupplierID   *string
	Lines        []lineInput
	Submit       bool
	Source       string
	EstimatedSum int64
}

func lineTotal(qty float64, cost int64) int64 { return int64(math.Round(qty * float64(cost))) }

// parseRequest validates the body of a request (online endpoints and offline records use the same rules).
// allowPast lets an offline capture keep a required date that has passed by the time it syncs.
func parseRequest(v *httpx.V, lang i18n.Lang, allowPast bool) requestInput {
	var in requestInput
	in.PlantID = v.Str("plantId").Trim().Min(1, "procurement.v.location").Max(64).Get()
	in.Department = v.NullStr("department").Trim().Max(40).NilIfEmpty()
	in.RequiredISO = v.Str("requiredDate").Trim().Date("procurement.v.required_date").Get()
	if t, err := dates.ParseISO(in.RequiredISO); err == nil {
		in.Required = t
		if !allowPast && in.RequiredISO < dates.TodayISO() {
			v.Fail("requiredDate", "procurement.v.required_past")
		}
	}
	in.Justify = v.Str("justification").Trim().Min(minJustification, i18n.T(lang, "procurement.v.justification", "n", minJustification)).
		Max(maxJustification, i18n.T(lang, "procurement.v.at_most", "n", maxJustification)).Get()
	in.SupplierID = v.NullStr("preferredSupplierId").Trim().Max(64).NilIfEmpty()
	in.Submit = v.OptBool("submit").Get()
	lines := v.Arr("lines", true)
	if v.Has("lines") && len(lines) == 0 {
		v.Fail("lines", "procurement.v.lines_required")
	}
	if len(lines) > maxLines {
		v.Fail("lines", i18n.T(lang, "procurement.v.lines_max", "n", maxLines))
	}
	for _, l := range lines {
		in.Lines = append(in.Lines, parseLine(l, lang, true))
	}
	for _, l := range in.Lines {
		in.EstimatedSum += lineTotal(l.Qty, l.UnitCost)
	}
	return in
}

// parseLine reads one item line; withCost reads estimatedUnitCost (requests) — orders read unitPrice themselves.
func parseLine(l *httpx.V, lang i18n.Lang, withCost bool) lineInput {
	var li lineInput
	li.ItemType = l.Str("itemType").Trim().Upper().OneOf([]string{ItemMaterial, ItemProduct, ItemOther}, "procurement.v.item_type").Get()
	switch li.ItemType {
	case ItemMaterial:
		li.MaterialID = l.Str("materialId").Trim().Min(1, "procurement.v.material").Max(64).Get()
	case ItemProduct:
		li.ProductID = l.Str("productId").Trim().Min(1, "procurement.v.product").Max(64).Get()
	case ItemOther:
		li.Desc = l.Str("description").Trim().Collapse().Min(2, "procurement.v.description").Max(200, i18n.T(lang, "procurement.v.at_most", "n", 200)).Get()
		li.Unit = l.Str("unit").Trim().Min(1, "procurement.v.unit").Max(20, i18n.T(lang, "procurement.v.at_most", "n", 20)).Get()
	}
	q := l.Num("quantity").Coerce().Gt(0, "procurement.v.qty_positive").Max(maxQty, "procurement.v.qty_large")
	li.Qty = stock.Round3(q.F())
	if q.Present() && !stock.HasAtMost3Decimals(q.F()) {
		l.Fail("quantity", "procurement.v.qty_decimals")
	}
	if q.Present() && li.ItemType == ItemProduct && li.Qty != math.Trunc(li.Qty) {
		l.Fail("quantity", "procurement.v.qty_whole")
	}
	if withCost {
		li.UnitCost = l.OptNum("estimatedUnitCost").Coerce().Whole("procurement.v.whole_francs").Min(0, "procurement.v.negative").Max(maxUnitCost, "procurement.v.too_large").I()
	}
	li.Category = l.NullStr("category").Trim().Max(80).NilIfEmpty()
	li.Note = l.NullStr("note").Trim().Max(300, i18n.T(lang, "procurement.v.at_most", "n", 300)).NilIfEmpty()
	return li
}

// resolveLines checks catalogue items and categories and fills description / unit / default category from the
// catalogue (one query per item kind). Field errors use prefix + ".N.".
func resolveLines(ctx context.Context, q db.DBTX, lang i18n.Lang, prefix string, lines []lineInput) error {
	var mats, prods []string
	for _, l := range lines {
		if l.MaterialID != "" {
			mats = append(mats, l.MaterialID)
		}
		if l.ProductID != "" {
			prods = append(prods, l.ProductID)
		}
	}
	mm, pm := map[string]stock.Item{}, map[string]stock.Item{}
	var err error
	if len(mats) > 0 {
		if mm, err = stock.LoadItems(ctx, q, stock.ItemMaterial, mats); err != nil {
			return err
		}
	}
	if len(prods) > 0 {
		if pm, err = stock.LoadItems(ctx, q, stock.ItemProduct, prods); err != nil {
			return err
		}
	}
	fields := map[string]string{}
	for i := range lines {
		l := &lines[i]
		p := prefix + "." + strconv.Itoa(i) + "."
		var it stock.Item
		var ok bool
		catKind := ""
		switch l.ItemType {
		case ItemMaterial:
			it, ok = mm[l.MaterialID]
			catKind = refdata.KindMaterialCategory
			if !ok {
				fields[p+"materialId"] = i18n.T(lang, "procurement.err.unknown_item")
			}
		case ItemProduct:
			it, ok = pm[l.ProductID]
			catKind = refdata.KindProductCategory
			if !ok {
				fields[p+"productId"] = i18n.T(lang, "procurement.err.unknown_item")
			}
		}
		if ok {
			if !it.Active {
				fields[p+string(map[string]string{ItemMaterial: "materialId", ItemProduct: "productId"}[l.ItemType])] = i18n.T(lang, "procurement.err.item_inactive", "name", it.Name)
			}
			l.Desc, l.Unit = it.Name, it.Unit
			if l.Category == nil && it.Category != "" {
				l.Category = strp(catKind + ":" + it.Category)
			}
		}
		if l.Category != nil {
			valid, err := validCategory(ctx, q, *l.Category)
			if err != nil {
				return err
			}
			if !valid {
				fields[p+"category"] = i18n.T(lang, "procurement.v.category")
			}
		}
	}
	if len(fields) > 0 {
		return httpx.BadRequest("error.validation", fields)
	}
	return nil
}

// resolveRequest checks references of a parsed request (location, department, supplier, items).
func resolveRequest(ctx context.Context, q db.DBTX, lang i18n.Lang, in *requestInput) (*PlantRef, error) {
	plant, err := requireActivePlant(ctx, q, lang, in.PlantID, "plantId")
	if err != nil {
		return nil, err
	}
	if in.Department != nil {
		ok, err := refdata.Valid(ctx, q, refdata.KindDepartment, *in.Department)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, badField(lang, "department", "procurement.v.department")
		}
	}
	if in.SupplierID != nil {
		var status string
		err := q.QueryRow(ctx, `SELECT status FROM supplier WHERE id = $1`, *in.SupplierID).Scan(&status)
		if db.IsNoRows(err) || (err == nil && status != SupActive) {
			return nil, badField(lang, "preferredSupplierId", "procurement.err.supplier_unavailable")
		}
		if err != nil {
			return nil, err
		}
	}
	return plant, resolveLines(ctx, q, lang, "lines", in.Lines)
}

// saveRequest inserts (id may be a client uuid) or replaces a DRAFT request and its lines. Returns the number.
func saveRequest(ctx context.Context, tx pgx.Tx, id string, existing bool, in requestInput, actorID, deviceID string, capturedAt *time.Time) (string, error) {
	var number string
	if existing {
		if err := tx.QueryRow(ctx, `UPDATE procurement_request SET plant_id=$2, department=$3, required_date=$4, justification=$5, estimated_total=$6,
			preferred_supplier_id=$7, updated_at=now() WHERE id=$1 RETURNING number`, id, in.PlantID, in.Department, in.Required, in.Justify, in.EstimatedSum, in.SupplierID).Scan(&number); err != nil {
			return "", err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM procurement_request_line WHERE request_id = $1`, id); err != nil { // draft lines are replaced as a whole
			return "", err
		}
	} else {
		var err error
		if number, err = seq.NextNumber(ctx, tx, "PR-"+strconv.Itoa(time.Now().In(dates.Kigali()).Year()), 4); err != nil {
			return "", err
		}
		src := in.Source
		if src == "" {
			src = "MANUAL"
		}
		if _, err := tx.Exec(ctx, `INSERT INTO procurement_request (id, number, status, plant_id, department, required_date, justification, estimated_total,
			preferred_supplier_id, source, requested_by_id, device_id, captured_at) VALUES ($1,$2,'DRAFT',$3,$4,$5,$6,$7,$8,$9,$10,NULLIF($11,''),$12)`,
			id, number, in.PlantID, in.Department, in.Required, in.Justify, in.EstimatedSum, in.SupplierID, src, actorID, deviceID, capturedAt); err != nil {
			return "", err
		}
	}
	rows := make([][]any, 0, len(in.Lines))
	for i, l := range in.Lines {
		rows = append(rows, []any{db.NewID(), id, i + 1, l.ItemType, nilIfEmpty(l.MaterialID), nilIfEmpty(l.ProductID), l.Desc, l.Unit, stock.Fmt3(l.Qty), l.UnitCost, l.Category, l.Note})
	}
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"procurement_request_line"},
		[]string{"id", "request_id", "line_no", "item_type", "material_id", "product_id", "description", "unit", "quantity", "estimated_unit_cost", "category", "note"},
		pgx.CopyFromRows(rows))
	return number, err
}

// ───────────────────────────── read models ─────────────────────────────

// RequestLine is one requested item.
type RequestLine struct {
	ID                string  `json:"id"`
	LineNo            int     `json:"lineNo"`
	ItemType          string  `json:"itemType"`
	MaterialID        *string `json:"materialId"`
	ProductID         *string `json:"productId"`
	ItemCode          *string `json:"itemCode"`
	Description       string  `json:"description"`
	Unit              string  `json:"unit"`
	Quantity          float64 `json:"quantity"`
	EstimatedUnitCost int64   `json:"estimatedUnitCost"`
	EstimatedTotal    int64   `json:"estimatedTotal"`
	Category          *string `json:"category"`
	CategoryName      string  `json:"categoryName"`
	Note              *string `json:"note"`
	OnOrderQty        float64 `json:"onOrderQty"` // on POs that are not cancelled (drafts included)
	OrderedQty        float64 `json:"orderedQty"` // on sent POs
	ReceivedQty       float64 `json:"receivedQty"`
	RemainingQty      float64 `json:"remainingQty"` // still to be put on a PO
}

// ApprovalDecision is one recorded decision.
type ApprovalDecision struct {
	Cycle    int       `json:"cycle"`
	Level    int       `json:"level"`
	Label    string    `json:"label"`
	Decision string    `json:"decision"`
	By       UserRef   `json:"by"`
	Role     string    `json:"role"`
	Comment  *string   `json:"comment"`
	At       time.Time `json:"at"`
}

// ApprovalStep is one level of the current chain with its state.
type ApprovalStep struct {
	RequiredLevel
	State    string            `json:"state"` // APPROVED | REJECTED | PENDING (current) | WAITING (later)
	Decision *ApprovalDecision `json:"decision"`
}

// RequestHeader is a request without its lines.
type RequestHeader struct {
	ID                string     `json:"id"`
	Number            string     `json:"number"`
	Status            string     `json:"status"`
	Plant             PlantRef   `json:"plant"`
	Department        *string    `json:"department"`
	DepartmentName    string     `json:"departmentName"`
	RequiredDate      string     `json:"requiredDate"`
	Justification     string     `json:"justification"`
	EstimatedTotal    int64      `json:"estimatedTotal"`
	PreferredSupplier *Ref       `json:"preferredSupplier"`
	Source            string     `json:"source"`
	Cycle             int        `json:"cycle"`
	CurrentLevel      *int       `json:"currentLevel"`
	RequestedBy       UserRef    `json:"requestedBy"`
	SubmittedAt       *time.Time `json:"submittedAt"`
	ApprovedAt        *time.Time `json:"approvedAt"`
	RejectedAt        *time.Time `json:"rejectedAt"`
	RejectionReason   *string    `json:"rejectionReason"`
	ClosedAt          *time.Time `json:"closedAt"`
	CloseReason       *string    `json:"closeReason"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
	levels            []RequiredLevel
	requesterRole     string
}

const requestHeaderSQL = `SELECT r.id, r.number, r.status, p.id, p.code, p.name, p.kind, r.department, to_char(r.required_date,'YYYY-MM-DD'),
	r.justification, r.estimated_total, s.id, s.code, s.name, r.source, r.cycle, r.current_level, u.id, u.name, u.role::text,
	r.submitted_at, r.approved_at, r.rejected_at, r.rejection_reason, r.closed_at, r.close_reason, r.created_at, r.updated_at, r.required_levels
	FROM procurement_request r JOIN "Plant" p ON p.id = r.plant_id JOIN "User" u ON u.id = r.requested_by_id
	LEFT JOIN supplier s ON s.id = r.preferred_supplier_id`

func scanHeader(row pgx.Row) (RequestHeader, error) {
	var h RequestHeader
	var sid, scode, sname *string
	var lv []byte
	err := row.Scan(&h.ID, &h.Number, &h.Status, &h.Plant.ID, &h.Plant.Code, &h.Plant.Name, &h.Plant.Kind, &h.Department, &h.RequiredDate,
		&h.Justification, &h.EstimatedTotal, &sid, &scode, &sname, &h.Source, &h.Cycle, &h.CurrentLevel, &h.RequestedBy.ID, &h.RequestedBy.Name, &h.requesterRole,
		&h.SubmittedAt, &h.ApprovedAt, &h.RejectedAt, &h.RejectionReason, &h.ClosedAt, &h.CloseReason, &h.CreatedAt, &h.UpdatedAt, &lv)
	if err != nil {
		return h, err
	}
	if sid != nil {
		h.PreferredSupplier = &Ref{ID: *sid, Code: deref(scode), Name: deref(sname)}
	}
	h.levels = []RequiredLevel{}
	_ = json.Unmarshal(lv, &h.levels)
	return h, nil
}

func loadRequestLines(ctx context.Context, q db.DBTX, ids []string, names map[string]string) (map[string][]RequestLine, error) {
	rows, err := q.Query(ctx, `SELECT l.request_id, l.id, l.line_no, l.item_type, l.material_id, l.product_id, COALESCE(m.code, p.code), l.description, l.unit,
		l.quantity::text, l.estimated_unit_cost, l.category, l.note,
		COALESCE(agg.on_order, 0)::text, COALESCE(agg.ordered, 0)::text, COALESCE(agg.received, 0)::text
		FROM procurement_request_line l
		LEFT JOIN "Material" m ON m.id = l.material_id LEFT JOIN "Product" p ON p.id = l.product_id
		LEFT JOIN (SELECT pl.request_line_id, SUM(pl.quantity) AS on_order, SUM(pl.quantity) FILTER (WHERE o.status <> 'DRAFT') AS ordered, SUM(pl.received_qty) AS received
			FROM purchase_order_line pl JOIN purchase_order o ON o.id = pl.po_id
			WHERE pl.request_id = ANY($1::text[]) AND o.status <> 'CANCELLED' GROUP BY pl.request_line_id) agg ON agg.request_line_id = l.id
		WHERE l.request_id = ANY($1::text[]) ORDER BY l.request_id, l.line_no`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]RequestLine{}
	for rows.Next() {
		var rid, qty, onOrd, ord, rec string
		var l RequestLine
		if err := rows.Scan(&rid, &l.ID, &l.LineNo, &l.ItemType, &l.MaterialID, &l.ProductID, &l.ItemCode, &l.Description, &l.Unit, &qty, &l.EstimatedUnitCost,
			&l.Category, &l.Note, &onOrd, &ord, &rec); err != nil {
			return nil, err
		}
		l.Quantity, l.OnOrderQty, l.OrderedQty, l.ReceivedQty = stock.ParseQty(qty), stock.ParseQty(onOrd), stock.ParseQty(ord), stock.ParseQty(rec)
		l.RemainingQty = math.Max(0, stock.Round3(l.Quantity-l.OnOrderQty))
		l.EstimatedTotal = lineTotal(l.Quantity, l.EstimatedUnitCost)
		l.CategoryName = nameOf(names, "", l.Category)
		out[rid] = append(out[rid], l)
	}
	return out, rows.Err()
}

func loadDecisions(ctx context.Context, q db.DBTX, id string) ([]ApprovalDecision, error) {
	rows, err := q.Query(ctx, `SELECT a.cycle, a.level, a.label, a.decision, u.id, u.name, a.decided_role, a.comment, a.decided_at
		FROM procurement_approval a JOIN "User" u ON u.id = a.decided_by_id WHERE a.request_id = $1 ORDER BY a.cycle, a.level`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ApprovalDecision{}
	for rows.Next() {
		var d ApprovalDecision
		if err := rows.Scan(&d.Cycle, &d.Level, &d.Label, &d.Decision, &d.By.ID, &d.By.Name, &d.Role, &d.Comment, &d.At); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func cycleDecisions(all []ApprovalDecision, cycle int) []Decision {
	out := []Decision{}
	for _, d := range all {
		if d.Cycle == cycle {
			out = append(out, Decision{Level: d.Level, UserID: d.By.ID})
		}
	}
	return out
}

// RequestDetail is GET /requests/:id.
type RequestDetail struct {
	RequestHeader
	Lines       []RequestLine      `json:"lines"`
	Steps       []ApprovalStep     `json:"approvalSteps"`
	History     []ApprovalDecision `json:"approvalHistory"`
	Orders      []OrderBrief       `json:"orders"`
	Timeline    []TimelineEntry    `json:"timeline"`
	Attachments []Attachment       `json:"attachments"`
	Can         map[string]bool    `json:"can"`
	ApproveHint string             `json:"approveHint,omitempty"` // why the viewer cannot approve (i18n'd)
}

// OrderBrief is a PO linked to a request.
type OrderBrief struct {
	ID       string `json:"id"`
	Number   string `json:"number"`
	Status   string `json:"status"`
	Supplier Ref    `json:"supplier"`
	Total    int64  `json:"total"`
}

func (h *handler) loadRequest(ctx context.Context, q db.DBTX, lang i18n.Lang, id string, u *auth.User) (*RequestDetail, error) {
	hd, err := scanHeader(q.QueryRow(ctx, requestHeaderSQL+` WHERE r.id = $1`, id))
	if db.IsNoRows(err) {
		return nil, httpx.NotFound("procurement.entity.request")
	}
	if err != nil {
		return nil, err
	}
	if s := auth.ScopedPlantID(u); s != "" && s != hd.Plant.ID {
		return nil, httpx.NotFound("procurement.entity.request")
	}
	names, err := refNames(ctx, q, lang, append(categoryKinds, refdata.KindDepartment)...)
	if err != nil {
		return nil, err
	}
	hd.DepartmentName = nameOf(names, refdata.KindDepartment, hd.Department)
	d := &RequestDetail{RequestHeader: hd}
	lines, err := loadRequestLines(ctx, q, []string{id}, names)
	if err != nil {
		return nil, err
	}
	d.Lines = lines[id]
	if d.Lines == nil {
		d.Lines = []RequestLine{}
	}
	if d.History, err = loadDecisions(ctx, q, id); err != nil {
		return nil, err
	}
	cur := map[int]ApprovalDecision{}
	for _, x := range d.History {
		if x.Cycle == hd.Cycle {
			cur[x.Level] = x
		}
	}
	d.Steps = []ApprovalStep{}
	pendingSeen := false
	for _, l := range hd.levels {
		st := ApprovalStep{RequiredLevel: l, State: "WAITING"}
		if x, ok := cur[l.Level]; ok {
			x := x
			st.State, st.Decision = x.Decision, &x
		} else if !pendingSeen && hd.Status == ReqSubmitted {
			st.State, pendingSeen = "PENDING", true
		}
		d.Steps = append(d.Steps, st)
	}
	rows, err := q.Query(ctx, `SELECT DISTINCT o.id, o.number, o.status, s.id, s.code, s.name, o.total, o.created_at FROM purchase_order o
		JOIN purchase_order_line pl ON pl.po_id = o.id JOIN supplier s ON s.id = o.supplier_id WHERE pl.request_id = $1 ORDER BY o.created_at`, id)
	if err != nil {
		return nil, err
	}
	d.Orders = []OrderBrief{}
	for rows.Next() {
		var o OrderBrief
		var at time.Time
		if err := rows.Scan(&o.ID, &o.Number, &o.Status, &o.Supplier.ID, &o.Supplier.Code, &o.Supplier.Name, &o.Total, &at); err != nil {
			rows.Close()
			return nil, err
		}
		d.Orders = append(d.Orders, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if d.Timeline, err = loadTimeline(ctx, q, "request", id); err != nil {
		return nil, err
	}
	if d.Attachments, err = loadAttachments(ctx, q, "request", []string{id}); err != nil {
		return nil, err
	}
	// what the viewer may do
	mine := hd.RequestedBy.ID == u.ID
	_, why := CheckApprover(hd.Status, hd.RequestedBy.ID, hd.levels, cycleDecisions(d.History, hd.Cycle), u.ID, u.Role)
	canDecide := why == "" && u.Can(permApprove)
	if !canDecide && hd.Status == ReqSubmitted && u.Can(permApprove) {
		d.ApproveHint = i18n.T(lang, "procurement.err."+why)
	}
	openPO := false
	for _, o := range d.Orders {
		if o.Status == PODraft || o.Status == POOrdered || o.Status == POPartiallyReceived {
			openPO = true
		}
	}
	remaining := false
	for _, l := range d.Lines {
		if l.RemainingQty > 0 {
			remaining = true
		}
	}
	d.Can = map[string]bool{
		"edit":    mine && hd.Status == ReqDraft && u.Can(permRequest),
		"submit":  mine && hd.Status == ReqDraft && u.Can(permRequest),
		"recall":  mine && hd.Status == ReqSubmitted && len(cycleDecisions(d.History, hd.Cycle)) == 0,
		"revise":  mine && hd.Status == ReqRejected,
		"approve": canDecide,
		"reject":  canDecide,
		"close": (hd.Status == ReqDraft && (mine || u.Can(permManage))) || (hd.Status == ReqApproved && (mine || u.Can(permManage))) ||
			(!openPO && u.Can(permManage) && (hd.Status == ReqOrdered || hd.Status == ReqPartiallyReceived || hd.Status == ReqReceived)),
		"order": u.Can(permManage) && remaining && (hd.Status == ReqApproved || hd.Status == ReqOrdered || hd.Status == ReqPartiallyReceived),
	}
	return d, nil
}

// ───────────────────────────── list ─────────────────────────────

// RequestRow is one row of GET /requests.
type RequestRow struct {
	ID             string     `json:"id"`
	Number         string     `json:"number"`
	Status         string     `json:"status"`
	Plant          PlantRef   `json:"plant"`
	Department     *string    `json:"department"`
	RequiredDate   string     `json:"requiredDate"`
	EstimatedTotal int64      `json:"estimatedTotal"`
	Source         string     `json:"source"`
	RequestedBy    UserRef    `json:"requestedBy"`
	Items          string     `json:"items"`
	LineCount      int        `json:"lineCount"`
	CurrentLevel   *int       `json:"currentLevel"`
	PendingLabel   string     `json:"pendingLabel"`
	AwaitingMe     bool       `json:"awaitingMe"`
	CreatedAt      time.Time  `json:"createdAt"`
	SubmittedAt    *time.Time `json:"submittedAt"`
}

// awaitingSQL: submitted requests whose current level the viewer may decide ($A = user id, $B = role, $C = held permissions).
const awaitingSQL = `(r.status = 'SUBMITTED' AND r.requested_by_id <> %[1]s AND EXISTS (SELECT 1 FROM jsonb_array_elements(r.required_levels) lv
	WHERE (lv->>'level')::int = r.current_level AND (%[2]s = 'SUPER_ADMIN' OR ((lv->>'permission') = ANY(%[3]s::text[]) AND (COALESCE(lv->>'role','') = '' OR lv->>'role' = %[2]s))))
	AND NOT EXISTS (SELECT 1 FROM procurement_approval a WHERE a.request_id = r.id AND a.cycle = r.cycle AND a.decided_by_id = %[1]s))`

func (h *handler) listRequests(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	v := httpx.Query(c)
	status := v.OptStr("status").Trim().Upper().Get()
	plantID := v.OptStr("plantId").Trim().Max(64).Get()
	from, to := v.OptStr("from").Trim().Get(), v.OptStr("to").Trim().Get()
	qtext := v.OptStr("q").Trim().Max(100).Get()
	view := v.OptStr("view").Trim().OneOf([]string{"", "all", "mine", "awaiting"}, "procurement.v.view").Get()
	source := v.OptStr("source").Trim().Upper().Get()
	if status != "" && !inList(RequestStatuses, status) {
		v.Fail("status", "procurement.v.status")
	}
	if (from != "" && !dates.IsValidISO(from)) || (to != "" && !dates.IsValidISO(to)) {
		v.Fail("from", "procurement.v.date")
	}
	if err := v.Err(); err != nil {
		return err
	}
	plant := auth.PlantFilter(u, plantID)
	args := []any{}
	arg := func(x any) string { args = append(args, x); return "$" + strconv.Itoa(len(args)) }
	where := []string{"TRUE"}
	if plant != "" {
		where = append(where, "r.plant_id = "+arg(plant))
	}
	if !u.Can(permView) && !u.Can(permApprove) && !u.Can(permManage) { // requesters without view see their own requests (and their site's)
		where = append(where, "(r.requested_by_id = "+arg(u.ID)+" OR r.plant_id = "+arg(auth.ScopedPlantID(u))+")")
	}
	if from != "" {
		where = append(where, "(r.created_at AT TIME ZONE 'UTC' AT TIME ZONE 'Africa/Kigali')::date >= "+arg(from)+"::date")
	}
	if to != "" {
		where = append(where, "(r.created_at AT TIME ZONE 'UTC' AT TIME ZONE 'Africa/Kigali')::date <= "+arg(to)+"::date")
	}
	if source != "" {
		where = append(where, "r.source = "+arg(source))
	}
	if qtext != "" {
		p := arg("%" + strings.ToLower(qtext) + "%")
		where = append(where, "(lower(r.number) LIKE "+p+" OR lower(r.justification) LIKE "+p+" OR EXISTS (SELECT 1 FROM procurement_request_line l WHERE l.request_id = r.id AND lower(l.description) LIKE "+p+"))")
	}
	awaitExpr := func() string {
		return fmt.Sprintf(awaitingSQL, arg(u.ID), arg(string(u.Role)), arg(auth.PermissionsFor(u.Role)))
	}
	switch view {
	case "mine":
		where = append(where, "r.requested_by_id = "+arg(u.ID))
	case "awaiting":
		where = append(where, awaitExpr())
	}
	base := strings.Join(where, " AND ")
	// status counts (before the status filter)
	counts := map[string]int{"ALL": 0}
	rows, err := h.d.Pool.Query(ctx, `SELECT r.status, count(*) FROM procurement_request r WHERE `+base+` GROUP BY r.status`, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			rows.Close()
			return err
		}
		counts[s] = n
		counts["ALL"] += n
	}
	rows.Close()
	if status != "" {
		base += " AND r.status = " + arg(status)
	}
	var total int
	var totalValue int64
	if err := h.d.Pool.QueryRow(ctx, `SELECT count(*), COALESCE(SUM(r.estimated_total),0) FROM procurement_request r WHERE `+base, args...).Scan(&total, &totalValue); err != nil {
		return err
	}
	pg := httpx.Paginate(c, 25)
	awaitCol := awaitExpr()
	rows, err = h.d.Pool.Query(ctx, `SELECT r.id, r.number, r.status, p.id, p.code, p.name, p.kind, r.department, to_char(r.required_date,'YYYY-MM-DD'),
		r.estimated_total, r.source, u.id, u.name, r.current_level, r.created_at, r.submitted_at,
		COALESCE((SELECT string_agg(l.description, ', ' ORDER BY l.line_no) FROM (SELECT description, line_no FROM procurement_request_line WHERE request_id = r.id ORDER BY line_no LIMIT 3) l), ''),
		(SELECT count(*) FROM procurement_request_line WHERE request_id = r.id),
		COALESCE((SELECT lv->>'label' FROM jsonb_array_elements(r.required_levels) lv WHERE (lv->>'level')::int = r.current_level LIMIT 1), ''),
		`+awaitCol+`
		FROM procurement_request r JOIN "Plant" p ON p.id = r.plant_id JOIN "User" u ON u.id = r.requested_by_id
		WHERE `+base+` ORDER BY r.created_at DESC, r.number DESC LIMIT `+arg(pg.Limit)+` OFFSET `+arg(pg.Offset), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []RequestRow{}
	for rows.Next() {
		var r RequestRow
		if err := rows.Scan(&r.ID, &r.Number, &r.Status, &r.Plant.ID, &r.Plant.Code, &r.Plant.Name, &r.Plant.Kind, &r.Department, &r.RequiredDate,
			&r.EstimatedTotal, &r.Source, &r.RequestedBy.ID, &r.RequestedBy.Name, &r.CurrentLevel, &r.CreatedAt, &r.SubmittedAt, &r.Items, &r.LineCount,
			&r.PendingLabel, &r.AwaitingMe); err != nil {
			return err
		}
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"items": items, "total": total, "page": pg.Page, "pageSize": pg.PageSize, "statusCounts": counts, "totalValue": totalValue})
}

func inList(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (h *handler) getRequest(c *fiber.Ctx) error {
	d, err := h.loadRequest(httpx.Ctx(c), h.d.Pool, httpx.Lang(c), c.Params("id"), auth.MustUser(c))
	if err != nil {
		return err
	}
	return c.JSON(d)
}

// ───────────────────────────── create / edit ─────────────────────────────

func (h *handler) createRequest(c *fiber.Ctx) error {
	ctx, u, lang := httpx.Ctx(c), auth.MustUser(c), httpx.Lang(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in := parseRequest(v, lang, false)
	if err := v.Err(); err != nil {
		return err
	}
	if err := auth.AssertPlantAccess(u, in.PlantID); err != nil {
		return err
	}
	user := *u
	id := db.NewID()
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		plant, err := resolveRequest(ctx, tx, lang, &in)
		if err != nil {
			return err
		}
		number, err := saveRequest(ctx, tx, id, false, in, user.ID, user.DeviceID, nil)
		if err != nil {
			return err
		}
		if err := logEvent(ctx, tx, "request", id, "created", "", ReqDraft, user.ID, nil); err != nil {
			return err
		}
		if err := audit.Record(c, tx, audit.Input{Action: "procurement.request_create", Entity: "ProcurementRequest", EntityID: id,
			Summary: fmt.Sprintf("Created purchase request %s at %s (%d lines, est. %d RWF)", number, plant.Code, len(in.Lines), in.EstimatedSum), After: in}); err != nil {
			return err
		}
		if in.Submit {
			return doSubmit(ctx, tx, c, id, &user, lang)
		}
		return nil
	})
	if err != nil {
		return err
	}
	invalidate()
	d, err := h.loadRequest(ctx, h.d.Pool, lang, id, u)
	if err != nil {
		return err
	}
	return c.Status(201).JSON(d)
}

// lockRequest reads status, requester, plant, cycle and levels FOR UPDATE.
type lockedRequest struct {
	ID, Number, Status, RequesterID, PlantID string
	Cycle                                    int
	Total                                    int64
	Levels                                   []RequiredLevel
}

func lockRequest(ctx context.Context, tx pgx.Tx, id string) (*lockedRequest, error) {
	var r lockedRequest
	var lv []byte
	err := tx.QueryRow(ctx, `SELECT id, number, status, requested_by_id, plant_id, cycle, estimated_total, required_levels FROM procurement_request WHERE id = $1 FOR UPDATE`, id).
		Scan(&r.ID, &r.Number, &r.Status, &r.RequesterID, &r.PlantID, &r.Cycle, &r.Total, &lv)
	if db.IsNoRows(err) {
		return nil, httpx.NotFound("procurement.entity.request")
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(lv, &r.Levels)
	return &r, nil
}

func (h *handler) updateRequest(c *fiber.Ctx) error {
	ctx, u, lang := httpx.Ctx(c), auth.MustUser(c), httpx.Lang(c)
	id := c.Params("id")
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in := parseRequest(v, lang, false)
	if err := v.Err(); err != nil {
		return err
	}
	if err := auth.AssertPlantAccess(u, in.PlantID); err != nil {
		return err
	}
	user := *u
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		r, err := lockRequest(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := auth.AssertPlantAccess(&user, r.PlantID); err != nil {
			return httpx.NotFound("procurement.entity.request")
		}
		if r.RequesterID != user.ID {
			return errKey(403, "forbidden", "procurement.err.not_requester")
		}
		if r.Status != ReqDraft {
			return conflictKey("procurement.err.only_draft_edit")
		}
		if _, err := resolveRequest(ctx, tx, lang, &in); err != nil {
			return err
		}
		if _, err := saveRequest(ctx, tx, id, true, in, user.ID, user.DeviceID, nil); err != nil {
			return err
		}
		if err := logEvent(ctx, tx, "request", id, "edited", "", "", user.ID, nil); err != nil {
			return err
		}
		if err := audit.Record(c, tx, audit.Input{Action: "procurement.request_update", Entity: "ProcurementRequest", EntityID: id,
			Summary: "Edited purchase request " + r.Number, After: in}); err != nil {
			return err
		}
		if in.Submit {
			return doSubmit(ctx, tx, c, id, &user, lang)
		}
		return nil
	})
	if err != nil {
		return err
	}
	invalidate()
	return h.respondRequest(c, id)
}

func (h *handler) respondRequest(c *fiber.Ctx, id string) error {
	d, err := h.loadRequest(httpx.Ctx(c), h.d.Pool, httpx.Lang(c), id, auth.MustUser(c))
	if err != nil {
		return err
	}
	return c.JSON(d)
}

// ───────────────────────────── workflow actions ─────────────────────────────

// submitCore freezes the approval chain and moves DRAFT → SUBMITTED (or APPROVED when no level applies).
// Shared by the online endpoint and the offline handler; the caller audits.
func submitCore(ctx context.Context, tx pgx.Tx, r *lockedRequest, requesterRole auth.Role, actorID string) (string, []RequiredLevel, error) {
	if r.Status != ReqDraft {
		return "", nil, conflictKey("procurement.err.bad_transition", "from", r.Status, "to", ReqSubmitted)
	}
	all, err := approvals.List(ctx, tx, approvals.Procurement, true)
	if err != nil {
		return "", nil, err
	}
	levels := RouteLevels(all, r.Total, requesterRole)
	raw, _ := json.Marshal(levels)
	to := ReqSubmitted
	var cur *int
	if len(levels) == 0 {
		to = ReqApproved
	} else {
		cur = &levels[0].Level
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement_request SET status=$2, cycle=cycle+1, required_levels=$3, current_level=$4, submitted_at=now(),
		approved_at = CASE WHEN $2 = 'APPROVED' THEN now() ELSE NULL END, rejected_at=NULL, rejection_reason=NULL, updated_at=now() WHERE id=$1`,
		r.ID, to, raw, cur); err != nil {
		return "", nil, err
	}
	if err := logEvent(ctx, tx, "request", r.ID, "submitted", ReqDraft, ReqSubmitted, actorID, nil); err != nil {
		return "", nil, err
	}
	params := map[string]any{"number": r.Number, "amount": r.Total}
	if to == ReqApproved {
		if err := logEvent(ctx, tx, "request", r.ID, "auto_approved", ReqSubmitted, ReqApproved, "", nil); err != nil {
			return "", nil, err
		}
		if err := send(ctx, tx, notify.Notification{Event: EvApproved, TitleKey: "procurement.notify.approved_title", BodyKey: "procurement.notify.approved_body",
			Params: params, Link: "/procurement/requests/" + r.ID, To: notify.Audience{UserIDs: []string{r.RequesterID}, Permission: permManage}.Without(actorID)}); err != nil {
			return "", nil, err
		}
		return to, levels, nil
	}
	params["level"] = levels[0].Label
	err = send(ctx, tx, notify.Notification{Event: EvSubmitted, Severity: notify.Warning, TitleKey: "procurement.notify.submitted_title",
		BodyKey: "procurement.notify.submitted_body", Params: params, Link: "/procurement/requests/" + r.ID, To: levelAudience(levels[0], r.RequesterID),
		DedupKey: r.ID + ":" + strconv.Itoa(r.Cycle+1) + ":" + strconv.Itoa(levels[0].Level)})
	return to, levels, err
}

func doSubmit(ctx context.Context, tx pgx.Tx, c *fiber.Ctx, id string, u *auth.User, lang i18n.Lang) error {
	r, err := lockRequest(ctx, tx, id)
	if err != nil {
		return err
	}
	if r.RequesterID != u.ID {
		return errKey(403, "forbidden", "procurement.err.not_requester")
	}
	to, levels, err := submitCore(ctx, tx, r, u.Role, u.ID)
	if err != nil {
		return err
	}
	return audit.Record(c, tx, audit.Input{Action: "procurement.request_submit", Entity: "ProcurementRequest", EntityID: id,
		Summary: fmt.Sprintf("Submitted purchase request %s (%d RWF, %d approval level(s)) → %s", r.Number, r.Total, len(levels), to),
		After:   map[string]any{"status": to, "levels": levels}})
}

func (h *handler) requestAction(c *fiber.Ctx, fn func(ctx context.Context, tx pgx.Tx, r *lockedRequest, u *auth.User, v *httpx.V) error) error {
	ctx, u := httpx.Ctx(c), auth.MustUser(c)
	id := c.Params("id")
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	user := *u
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		r, err := lockRequest(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := auth.AssertPlantAccess(&user, r.PlantID); err != nil {
			return httpx.NotFound("procurement.entity.request")
		}
		return fn(ctx, tx, r, &user, v)
	})
	if err != nil {
		return err
	}
	invalidate()
	return h.respondRequest(c, id)
}

func (h *handler) submitRequest(c *fiber.Ctx) error {
	return h.requestAction(c, func(ctx context.Context, tx pgx.Tx, r *lockedRequest, u *auth.User, _ *httpx.V) error {
		return doSubmit(ctx, tx, c, r.ID, u, httpx.Lang(c))
	})
}

func (h *handler) recallRequest(c *fiber.Ctx) error {
	return h.requestAction(c, func(ctx context.Context, tx pgx.Tx, r *lockedRequest, u *auth.User, _ *httpx.V) error {
		if r.RequesterID != u.ID {
			return errKey(403, "forbidden", "procurement.err.not_requester")
		}
		if r.Status != ReqSubmitted {
			return conflictKey("procurement.err.bad_transition", "from", r.Status, "to", ReqDraft)
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM procurement_approval WHERE request_id=$1 AND cycle=$2`, r.ID, r.Cycle).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return conflictKey("procurement.err.recall_decided")
		}
		if _, err := tx.Exec(ctx, `UPDATE procurement_request SET status='DRAFT', current_level=NULL, submitted_at=NULL, updated_at=now() WHERE id=$1`, r.ID); err != nil {
			return err
		}
		if err := logEvent(ctx, tx, "request", r.ID, "recalled", ReqSubmitted, ReqDraft, u.ID, nil); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "procurement.request_recall", Entity: "ProcurementRequest", EntityID: r.ID, Summary: "Recalled purchase request " + r.Number + " to draft"})
	})
}

func (h *handler) reviseRequest(c *fiber.Ctx) error {
	return h.requestAction(c, func(ctx context.Context, tx pgx.Tx, r *lockedRequest, u *auth.User, _ *httpx.V) error {
		if r.RequesterID != u.ID {
			return errKey(403, "forbidden", "procurement.err.not_requester")
		}
		if !CanRequestMove(r.Status, ReqDraft) || r.Status != ReqRejected {
			return conflictKey("procurement.err.bad_transition", "from", r.Status, "to", ReqDraft)
		}
		if _, err := tx.Exec(ctx, `UPDATE procurement_request SET status='DRAFT', current_level=NULL, updated_at=now() WHERE id=$1`, r.ID); err != nil {
			return err
		}
		if err := logEvent(ctx, tx, "request", r.ID, "revised", ReqRejected, ReqDraft, u.ID, nil); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "procurement.request_revise", Entity: "ProcurementRequest", EntityID: r.ID, Summary: "Reopened rejected purchase request " + r.Number + " for revision"})
	})
}

func approverError(lang i18n.Lang, why string) error {
	if why == DenyNotSubmitted || why == DenyNoLevelOpen {
		return conflictKey("procurement.err." + why)
	}
	return errKey(403, "forbidden", "procurement.err."+why)
}

func (h *handler) approveRequest(c *fiber.Ctx) error {
	lang := httpx.Lang(c)
	return h.requestAction(c, func(ctx context.Context, tx pgx.Tx, r *lockedRequest, u *auth.User, v *httpx.V) error {
		comment := v.NullStr("comment").Trim().Max(500, i18n.T(lang, "procurement.v.at_most", "n", 500)).NilIfEmpty()
		if err := v.Err(); err != nil {
			return err
		}
		all, err := loadDecisions(ctx, tx, r.ID)
		if err != nil {
			return err
		}
		decided := cycleDecisions(all, r.Cycle)
		lvl, why := CheckApprover(r.Status, r.RequesterID, r.Levels, decided, u.ID, u.Role)
		if why != "" {
			return approverError(lang, why)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO procurement_approval (request_id, cycle, level, label, decision, decided_by_id, decided_role, comment)
			VALUES ($1,$2,$3,$4,'APPROVED',$5,$6,$7)`, r.ID, r.Cycle, lvl.Level, lvl.Label, u.ID, string(u.Role), comment); err != nil {
			return err
		}
		next, more := NextLevel(r.Levels, append(decided, Decision{Level: lvl.Level, UserID: u.ID}))
		params := map[string]any{"number": r.Number, "amount": r.Total, "level": lvl.Label, "by": u.Name}
		if more {
			if _, err := tx.Exec(ctx, `UPDATE procurement_request SET current_level=$2, updated_at=now() WHERE id=$1`, r.ID, next.Level); err != nil {
				return err
			}
			if err := logEvent(ctx, tx, "request", r.ID, "level_approved", "", "", u.ID, strp(lvl.Label)); err != nil {
				return err
			}
			params["level"] = next.Label
			if err := send(ctx, tx, notify.Notification{Event: EvSubmitted, Severity: notify.Warning, TitleKey: "procurement.notify.submitted_title",
				BodyKey: "procurement.notify.submitted_body", Params: params, Link: "/procurement/requests/" + r.ID, To: levelAudience(next, r.RequesterID).Without(u.ID),
				DedupKey: r.ID + ":" + strconv.Itoa(r.Cycle) + ":" + strconv.Itoa(next.Level)}); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(ctx, `UPDATE procurement_request SET status='APPROVED', current_level=NULL, approved_at=now(), updated_at=now() WHERE id=$1`, r.ID); err != nil {
				return err
			}
			if err := logEvent(ctx, tx, "request", r.ID, "approved", ReqSubmitted, ReqApproved, u.ID, comment); err != nil {
				return err
			}
			if err := send(ctx, tx, notify.Notification{Event: EvApproved, TitleKey: "procurement.notify.approved_title", BodyKey: "procurement.notify.approved_body",
				Params: params, Link: "/procurement/requests/" + r.ID, To: notify.Audience{UserIDs: []string{r.RequesterID}, Permission: permManage}.Without(u.ID)}); err != nil {
				return err
			}
		}
		return audit.Record(c, tx, audit.Input{Action: "procurement.request_approve", Entity: "ProcurementRequest", EntityID: r.ID,
			Summary: fmt.Sprintf("Approved purchase request %s at level %d (%s)", r.Number, lvl.Level, lvl.Label), After: map[string]any{"level": lvl.Level, "comment": comment, "final": !more}})
	})
}

func (h *handler) rejectRequest(c *fiber.Ctx) error {
	lang := httpx.Lang(c)
	return h.requestAction(c, func(ctx context.Context, tx pgx.Tx, r *lockedRequest, u *auth.User, v *httpx.V) error {
		reason := v.Str("reason").Trim().Min(3, "procurement.v.reason").Max(500, i18n.T(lang, "procurement.v.at_most", "n", 500)).Get()
		if err := v.Err(); err != nil {
			return err
		}
		all, err := loadDecisions(ctx, tx, r.ID)
		if err != nil {
			return err
		}
		lvl, why := CheckApprover(r.Status, r.RequesterID, r.Levels, cycleDecisions(all, r.Cycle), u.ID, u.Role)
		if why != "" {
			return approverError(lang, why)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO procurement_approval (request_id, cycle, level, label, decision, decided_by_id, decided_role, comment)
			VALUES ($1,$2,$3,$4,'REJECTED',$5,$6,$7)`, r.ID, r.Cycle, lvl.Level, lvl.Label, u.ID, string(u.Role), reason); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE procurement_request SET status='REJECTED', current_level=NULL, rejected_at=now(), rejection_reason=$2, updated_at=now() WHERE id=$1`, r.ID, reason); err != nil {
			return err
		}
		if err := logEvent(ctx, tx, "request", r.ID, "rejected", ReqSubmitted, ReqRejected, u.ID, &reason); err != nil {
			return err
		}
		if err := send(ctx, tx, notify.Notification{Event: EvRejected, Severity: notify.Warning, TitleKey: "procurement.notify.rejected_title", BodyKey: "procurement.notify.rejected_body",
			Params: map[string]any{"number": r.Number, "by": u.Name, "reason": reason}, Link: "/procurement/requests/" + r.ID, To: notify.ToUsers(r.RequesterID)}); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "procurement.request_reject", Entity: "ProcurementRequest", EntityID: r.ID,
			Summary: fmt.Sprintf("Rejected purchase request %s at level %d: %s", r.Number, lvl.Level, reason), After: map[string]any{"level": lvl.Level, "reason": reason}})
	})
}

func (h *handler) closeRequest(c *fiber.Ctx) error {
	lang := httpx.Lang(c)
	return h.requestAction(c, func(ctx context.Context, tx pgx.Tx, r *lockedRequest, u *auth.User, v *httpx.V) error {
		reasonV := v.NullStr("reason").Trim().Max(500, i18n.T(lang, "procurement.v.at_most", "n", 500))
		reason := reasonV.NilIfEmpty()
		if err := v.Err(); err != nil {
			return err
		}
		mine := r.RequesterID == u.ID
		switch r.Status {
		case ReqDraft, ReqApproved:
			if !mine && !u.Can(permManage) {
				return errKey(403, "forbidden", "procurement.err.not_requester")
			}
		case ReqOrdered, ReqPartiallyReceived, ReqReceived:
			if !u.Can(permManage) {
				return errKey(403, "forbidden", "error.forbidden")
			}
			var open int
			if err := tx.QueryRow(ctx, `SELECT count(DISTINCT o.id) FROM purchase_order o JOIN purchase_order_line pl ON pl.po_id = o.id
				WHERE pl.request_id = $1 AND o.status IN ('DRAFT','ORDERED','PARTIALLY_RECEIVED')`, r.ID).Scan(&open); err != nil {
				return err
			}
			if open > 0 {
				return conflictKey("procurement.err.open_orders")
			}
		default:
			return conflictKey("procurement.err.bad_transition", "from", r.Status, "to", ReqClosed)
		}
		if r.Status != ReqReceived && (reason == nil || len([]rune(*reason)) < 3) {
			return badField(lang, "reason", "procurement.v.reason")
		}
		if _, err := tx.Exec(ctx, `UPDATE procurement_request SET status='CLOSED', current_level=NULL, closed_at=now(), close_reason=$2, updated_at=now() WHERE id=$1`, r.ID, reason); err != nil {
			return err
		}
		if err := logEvent(ctx, tx, "request", r.ID, "closed", r.Status, ReqClosed, u.ID, reason); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "procurement.request_close", Entity: "ProcurementRequest", EntityID: r.ID,
			Summary: "Closed purchase request " + r.Number + " (was " + r.Status + ")", After: map[string]any{"reason": reason}})
	})
}

// ───────────────────────────── status roll-up from purchase orders ─────────────────────────────

// recomputeRequests re-derives the status of every request touched by a PO change (ORDERED / PARTIALLY_RECEIVED /
// RECEIVED, back to APPROVED when every PO was cancelled, CLOSED when received and all its orders are closed).
func recomputeRequests(ctx context.Context, tx pgx.Tx, requestIDs []string, actorID string) error {
	if len(requestIDs) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM procurement_request WHERE id = ANY($1::text[]) ORDER BY id FOR UPDATE`, requestIDs); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT r.id, r.status, l.id,
		bool_or(o.status IN ('ORDERED','PARTIALLY_RECEIVED','RECEIVED','CLOSED')) AS on_order,
		bool_or(pl.received_qty > 0) AS received,
		COALESCE(bool_and(pl.closed OR pl.received_qty >= pl.quantity) FILTER (WHERE o.status IN ('ORDERED','PARTIALLY_RECEIVED','RECEIVED','CLOSED')), false) AS done,
		COALESCE(bool_and(o.status IN ('CLOSED','CANCELLED')), true) AS all_closed,
		COALESCE(SUM(pl.quantity) FILTER (WHERE o.status IN ('ORDERED','PARTIALLY_RECEIVED','RECEIVED','CLOSED')), 0) >= l.quantity AS fully
		FROM procurement_request r JOIN procurement_request_line l ON l.request_id = r.id
		LEFT JOIN purchase_order_line pl ON pl.request_line_id = l.id
		LEFT JOIN purchase_order o ON o.id = pl.po_id AND o.status <> 'CANCELLED'
		WHERE r.id = ANY($1::text[]) GROUP BY r.id, r.status, l.id, l.quantity ORDER BY r.id`, requestIDs)
	if err != nil {
		return err
	}
	type acc struct {
		status    string
		lines     []RequestLineProgress
		allClosed bool
	}
	byID := map[string]*acc{}
	order := []string{}
	for rows.Next() {
		var rid, st, lid string
		var on, rec, done, closed *bool
		var fully bool
		if err := rows.Scan(&rid, &st, &lid, &on, &rec, &done, &closed, &fully); err != nil {
			rows.Close()
			return err
		}
		a := byID[rid]
		if a == nil {
			a = &acc{status: st, allClosed: true}
			byID[rid] = a
			order = append(order, rid)
		}
		b := func(p *bool) bool { return p != nil && *p }
		a.lines = append(a.lines, RequestLineProgress{OnOrder: b(on), Received: b(rec), Done: b(done), FullyOrdered: fully})
		if closed != nil && !*closed {
			a.allClosed = false
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, rid := range order {
		a := byID[rid]
		if a.status == ReqClosed || a.status == ReqDraft || a.status == ReqSubmitted || a.status == ReqRejected {
			continue
		}
		to := RequestStatusFor(a.lines)
		if to == "" {
			to = ReqApproved // nothing (left) on a sent PO
		}
		if to == ReqReceived && a.allClosed {
			to = ReqClosed
		}
		if to == a.status {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE procurement_request SET status=$2, closed_at = CASE WHEN $2='CLOSED' THEN now() ELSE closed_at END, updated_at=now() WHERE id=$1`, rid, to); err != nil {
			return err
		}
		if err := logEvent(ctx, tx, "request", rid, "status", a.status, to, actorID, nil); err != nil {
			return err
		}
	}
	return nil
}

// sprintfAwait renders awaitingSQL with the given placeholders (user id, role, held permissions).
func sprintfAwait(uid, role, perms string) string { return fmt.Sprintf(awaitingSQL, uid, role, perms) }
