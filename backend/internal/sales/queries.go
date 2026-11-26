package sales

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"
)

var paymentMethods = []string{"MOMO", "BANK_BK", "BANK_OTHER", "CASH", "CHEQUE"}

// methodLabel is the localized name of a payment method.
func methodLabel(lang i18n.Lang, m string) string { return i18n.T(lang, "sales.pm."+m) }

func statusLabel(lang i18n.Lang, s string) string { return i18n.T(lang, "sales.status."+s) }

// qb collects positional arguments for dynamic SQL (values are never concatenated).
type qb struct{ args []any }

func (q *qb) arg(v any) string {
	q.args = append(q.args, v)
	return "$" + strconv.Itoa(len(q.args))
}

func likeArg(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

func and(conds []string) string {
	if len(conds) == 0 {
		return "TRUE"
	}
	return strings.Join(conds, " AND ")
}

// ───────────────────────────── locks & stock ─────────────────────────────

// lockSale takes a row lock on the sale so concurrent deliveries/payments see each other's effects.
func lockSale(ctx context.Context, tx db.DBTX, saleID string) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM "Sale" WHERE id=$1 FOR UPDATE`, saleID).Scan(&id)
	if db.IsNoRows(err) {
		return nil // the caller reports 404 when it loads the sale
	}
	return err
}

// lockStock serialises stock issues per plant+product across sales (released at commit).
func lockStock(ctx context.Context, tx db.DBTX, plantID string, productIDs []string) error {
	seen := map[string]bool{}
	var ids []string
	for _, p := range productIDs {
		if !seen[p] {
			seen[p] = true
			ids = append(ids, p)
		}
	}
	sortStrings(ids)
	for _, p := range ids {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "stock:"+plantID+":"+p); err != nil {
			return err
		}
	}
	return nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// issuableStock is the finished-goods quantity that can be issued at a plant on a date: the balance at that
// date (movements dated <= D) capped so that no later day goes negative. One grouped query for all products.
func issuableStock(ctx context.Context, q db.DBTX, plantID string, productIDs []string, date time.Time) (map[string]int64, error) {
	out := map[string]int64{}
	var ids []string
	for _, p := range productIDs {
		if _, ok := out[p]; !ok {
			out[p] = 0
			ids = append(ids, p)
		}
	}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `
		SELECT "productId", CASE WHEN date <= $3::date THEN NULL ELSE date END AS d, SUM(quantity)::bigint
		FROM "StockMovement" WHERE "plantId"=$1 AND "productId" = ANY($2)
		GROUP BY 1, 2 ORDER BY 1, 2 NULLS FIRST`, plantID, ids, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	balance := map[string]int64{}
	later := map[string][]int64{}
	for rows.Next() {
		var pid string
		var d *time.Time
		var sum int64
		if err := rows.Scan(&pid, &d, &sum); err != nil {
			return nil, err
		}
		if d == nil {
			balance[pid] = sum
		} else {
			later[pid] = append(later[pid], sum)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		out[id] = availableForIssue(balance[id], later[id])
	}
	return out, nil
}

type warnLine struct {
	ProductID, Code, Name string
	Quantity, Delivered   int64
}

// stockWarnings lists products whose undelivered quantity exceeds the stock currently at the plant.
func stockWarnings(ctx context.Context, q db.DBTX, lang i18n.Lang, plantID string, today time.Time, lines []warnLine) ([]stockWarning, error) {
	type need struct {
		qty        int64
		code, name string
	}
	needs := map[string]*need{}
	var order []string
	for _, l := range lines {
		rem := l.Quantity - l.Delivered
		if rem <= 0 {
			continue
		}
		n := needs[l.ProductID]
		if n == nil {
			n = &need{code: l.Code, name: l.Name}
			needs[l.ProductID] = n
			order = append(order, l.ProductID)
		}
		n.qty += rem
	}
	avail, err := issuableStock(ctx, q, plantID, order, today)
	if err != nil {
		return nil, err
	}
	out := []stockWarning{}
	for _, id := range order {
		n := needs[id]
		if avail[id] < n.qty {
			out = append(out, stockWarning{ProductID: id, ProductCode: n.code, ProductName: n.name, Required: n.qty, Available: avail[id],
				Message: i18n.T(lang, "sales.warn.short", "product", n.name, "required", format.Int(float64(n.qty)), "available", format.Int(float64(avail[id])))})
		}
	}
	return out, nil
}

// ───────────────────────────── users ─────────────────────────────

func userNames(ctx context.Context, q db.DBTX, ids ...string) (map[string]string, error) {
	seen := map[string]bool{}
	var uniq []string
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	out := map[string]string{}
	if len(uniq) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT id, name FROM "User" WHERE id = ANY($1)`, uniq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// ───────────────────────────── sale detail ─────────────────────────────

func (s *service) loadSale(ctx context.Context, q db.DBTX, lang i18n.Lang, id string, maxDiscount float64) (*saleDetail, error) {
	var sd saleDetail
	var date time.Time
	var createdBy string
	var confirmedAt, cancelledAt *time.Time
	var createdAt time.Time
	var cc customer
	var ccCreated time.Time
	var cfBy, ccBy, qBy, clBy *string
	var due, req, valid *time.Time
	var quotedAt, readyAt, closedAt *time.Time
	err := q.QueryRow(ctx, `
		SELECT s.id, s.number, s."orderStatus", s.status::text, s.date, s."invoiceType", s."ebmInvoiceNo", s."ebmReceiptNo", s.notes,
		  p.id, p.code, p.name,
		  c.id, c.name, c.tin, c.phone, c.email, c.address, c."isActive", c."createdAt", c."clientType", c.status, c.notes, c."paymentTerm",
		  s."createdById", s."createdAt", s."confirmedById", s."confirmedAt", s."cancelledById", s."cancelledAt", s."cancelReason",
		  s."paymentTerm", s."depositAmount", s."dueDate", s."transportMode", s."deliveryAddress", s."deliverySite", s."requestedDate", s."deliveryZone",
		  s."distanceKm"::float8, s."deliveryCharge", s."validUntil", s."quotedById", s."quotedAt", s."readyAt", s."closedById", s."closedAt", s.version,
		  s."capturedOffline", s.server_version
		FROM "Sale" s JOIN "Plant" p ON p.id = s."plantId" JOIN "Customer" c ON c.id = s."customerId"
		WHERE s.id = $1`, id).Scan(
		&sd.ID, &sd.Number, &sd.Status, &sd.LegacyStatus, &date, &sd.InvoiceType, &sd.EbmInvoiceNo, &sd.EbmReceiptNo, &sd.Notes,
		&sd.Plant.ID, &sd.Plant.Code, &sd.Plant.Name,
		&cc.ID, &cc.Name, &cc.TIN, &cc.Phone, &cc.Email, &cc.Address, &cc.IsActive, &ccCreated, &cc.ClientType, &cc.Status, &cc.Notes, &cc.PaymentTerm,
		&createdBy, &createdAt, &cfBy, &confirmedAt, &ccBy, &cancelledAt, &sd.CancelReason,
		&sd.PaymentTerm, &sd.DepositAmount, &due, &sd.TransportMode, &sd.DeliveryAddress, &sd.DeliverySite, &req, &sd.DeliveryZone,
		&sd.DistanceKm, &sd.DeliveryCharge, &valid, &qBy, &quotedAt, &readyAt, &clBy, &closedAt, &sd.Version,
		&sd.CapturedOffline, &sd.ServerVersion)
	if err != nil {
		if db.IsNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	cc.CreatedAt = jt(ccCreated)
	sd.Customer = cc
	sd.Date = dates.ToISO(date)
	sd.CreatedAt = jt(createdAt)
	sd.ConfirmedAt, sd.CancelledAt = jtp(confirmedAt), jtp(cancelledAt)
	sd.MaxDiscountPercent = maxDiscount
	isoPtr := func(t *time.Time) *string {
		if t == nil {
			return nil
		}
		s := dates.ToISO(*t)
		return &s
	}
	sd.DueDate, sd.RequestedDate, sd.ValidUntil = isoPtr(due), isoPtr(req), isoPtr(valid)
	sd.QuotedAt, sd.ReadyAt, sd.ClosedAt = jtp(quotedAt), jtp(readyAt), jtp(closedAt)
	sd.NextStatuses = nextStatuses(sd.Status)
	sd.Locked = !editable(sd.Status)

	// lines with delivered quantities (active delivery notes only)
	lrows, err := q.Query(ctx, `
		SELECT sl.id, sl."productId", sl.quantity, sl."unitPrice", sl.amount,
		  pr.id, pr.code, pr.name, pr.unit, pr.dimensions, pr."defaultPrice", COALESCE(dd.q, 0)::bigint,
		  sl."discountPercent"::float8, sl."discountAmount", sl.fulfilment
		FROM "SaleLine" sl JOIN "Product" pr ON pr.id = sl."productId"
		LEFT JOIN (SELECT dl."saleLineId", SUM(dl.quantity) AS q FROM "DeliveryLine" dl
		           JOIN "Delivery" d ON d.id = dl."deliveryId" AND d.status <> 'CANCELLED'
		           WHERE d."saleId" = $1 GROUP BY dl."saleLineId") dd ON dd."saleLineId" = sl.id
		WHERE sl."saleId" = $1 ORDER BY sl.id`, id)
	if err != nil {
		return nil, err
	}
	sd.Lines = []saleLineOut{}
	productInfo := map[string]productRef{}
	for lrows.Next() {
		var l saleLineOut
		if err := lrows.Scan(&l.ID, &l.ProductID, &l.Quantity, &l.UnitPrice, &l.Amount,
			&l.Product.ID, &l.Product.Code, &l.Product.Name, &l.Product.Unit, &l.Product.Dimensions, &l.Product.DefaultPrice, &l.Delivered,
			&l.DiscountPercent, &l.DiscountAmount, &l.Fulfilment); err != nil {
			lrows.Close()
			return nil, err
		}
		l.DefaultPrice = l.Product.DefaultPrice
		l.Gross = l.Amount + l.DiscountAmount
		l.MinPrice = minPriceWithoutApproval(l.DefaultPrice, maxDiscount)
		if l.Quantity > 0 {
			l.Discount = discountRatio(l.Amount/l.Quantity, l.DefaultPrice)
		}
		l.NeedsGmApproval = netNeedsGmApproval(l.Amount, l.Quantity, l.DefaultPrice, maxDiscount)
		l.Remaining = remainingToDeliver(l.Quantity, l.Delivered)
		productInfo[l.ProductID] = l.Product
		sd.Lines = append(sd.Lines, l)
	}
	lrows.Close()
	if err := lrows.Err(); err != nil {
		return nil, err
	}

	// deliveries + their lines
	type dRow struct {
		d         deliveryOut
		createdBy string
	}
	drows, err := q.Query(ctx, `
		SELECT d.id, d."dnNumber", d.date, p.id, p.code, p.name, d.vehicle, d.driver, d.status::text, d."deliveredAt", d."receivedBy", d."createdById", d."createdAt",
		  d.destination, d."transportMode", d."deliveryNote", EXISTS (SELECT 1 FROM delivery_photo ph WHERE ph.delivery_id = d.id), d."capturedOffline"
		FROM "Delivery" d JOIN "Plant" p ON p.id = d."plantId"
		WHERE d."saleId" = $1 ORDER BY d.date, d."createdAt"`, id)
	if err != nil {
		return nil, err
	}
	var dels []*dRow
	byDel := map[string]*dRow{}
	for drows.Next() {
		r := &dRow{}
		var dd time.Time
		var dAt *time.Time
		var cAt time.Time
		if err := drows.Scan(&r.d.ID, &r.d.DNNumber, &dd, &r.d.Plant.ID, &r.d.Plant.Code, &r.d.Plant.Name, &r.d.Vehicle, &r.d.Driver, &r.d.Status, &dAt, &r.d.ReceivedBy, &r.createdBy, &cAt,
			&r.d.Destination, &r.d.Transport, &r.d.Note, &r.d.HasPhoto, &r.d.Offline); err != nil {
			drows.Close()
			return nil, err
		}
		r.d.Date = dates.ToISO(dd)
		r.d.DeliveredAt = jtp(dAt)
		r.d.CreatedAt = jt(cAt)
		r.d.Lines = []deliveryLineOut{}
		dels = append(dels, r)
		byDel[r.d.ID] = r
	}
	drows.Close()
	if err := drows.Err(); err != nil {
		return nil, err
	}
	activeByProduct := []productQty{}
	if len(dels) > 0 {
		dlrows, err := q.Query(ctx, `
			SELECT dl."deliveryId", dl.id, dl."saleLineId", dl."productId", dl.quantity
			FROM "DeliveryLine" dl JOIN "Delivery" d ON d.id = dl."deliveryId"
			WHERE d."saleId" = $1 ORDER BY dl.id`, id)
		if err != nil {
			return nil, err
		}
		for dlrows.Next() {
			var did string
			var l deliveryLineOut
			if err := dlrows.Scan(&did, &l.ID, &l.SaleLineID, &l.ProductID, &l.Quantity); err != nil {
				dlrows.Close()
				return nil, err
			}
			l.Product = productInfo[l.ProductID]
			if r := byDel[did]; r != nil {
				r.d.Lines = append(r.d.Lines, l)
				r.d.TotalQty += l.Quantity
			}
		}
		dlrows.Close()
		if err := dlrows.Err(); err != nil {
			return nil, err
		}
		for _, r := range dels {
			if r.d.Status != "CANCELLED" {
				for _, l := range r.d.Lines {
					activeByProduct = append(activeByProduct, productQty{l.ProductID, l.Quantity})
				}
			}
		}
	}

	// payments
	prows, err := q.Query(ctx, `SELECT id, date, amount, method::text, reference, "receivedById", "createdAt", kind FROM "Payment" WHERE "saleId"=$1 ORDER BY date, "createdAt"`, id)
	if err != nil {
		return nil, err
	}
	type pRow struct {
		p  paymentOut
		by string
	}
	var pays []*pRow
	var paid int64
	for prows.Next() {
		r := &pRow{}
		var d, c time.Time
		if err := prows.Scan(&r.p.ID, &d, &r.p.Amount, &r.p.Method, &r.p.Reference, &r.by, &c, &r.p.Kind); err != nil {
			prows.Close()
			return nil, err
		}
		r.p.Date = dates.ToISO(d)
		r.p.CreatedAt = jt(c)
		r.p.MethodLabel = methodLabel(lang, r.p.Method)
		paid += r.p.Amount
		pays = append(pays, r)
	}
	prows.Close()
	if err := prows.Err(); err != nil {
		return nil, err
	}

	// returns (stock movements)
	rrows, err := q.Query(ctx, `
		SELECT m.id, m.date, p.id, p.code, p.name, m."productId", m.quantity, m.note, m."createdById", m."createdAt"
		FROM "StockMovement" m JOIN "Plant" p ON p.id = m."plantId"
		WHERE m."sourceType" = 'SaleReturn' AND m."sourceId" = $1 ORDER BY m.date, m."createdAt"`, id)
	if err != nil {
		return nil, err
	}
	type rRow struct {
		r  returnOut
		by string
	}
	var rets []*rRow
	var retRows []productQty
	var returnedTotal int64
	for rrows.Next() {
		r := &rRow{}
		var d, c time.Time
		var by *string
		if err := rrows.Scan(&r.r.ID, &d, &r.r.Plant.ID, &r.r.Plant.Code, &r.r.Plant.Name, &r.r.ProductID, &r.r.Quantity, &r.r.Note, &by, &c); err != nil {
			rrows.Close()
			return nil, err
		}
		if by != nil {
			r.by = *by
		}
		r.r.Date = dates.ToISO(d)
		r.r.CreatedAt = jt(c)
		r.r.Product = productInfo[r.r.ProductID]
		retRows = append(retRows, productQty{r.r.ProductID, r.r.Quantity})
		returnedTotal += r.r.Quantity
		rets = append(rets, r)
	}
	rrows.Close()
	if err := rrows.Err(); err != nil {
		return nil, err
	}
	// a returned product always belongs to a sale line, but resolve defensively
	for _, r := range rets {
		if r.r.Product.ID == "" {
			var p productRef
			if err := q.QueryRow(ctx, `SELECT id, code, name, unit, dimensions, "defaultPrice" FROM "Product" WHERE id=$1`, r.r.ProductID).
				Scan(&p.ID, &p.Code, &p.Name, &p.Unit, &p.Dimensions, &p.DefaultPrice); err == nil {
				r.r.Product = p
			}
		}
	}

	ids := []string{createdBy}
	if qBy != nil {
		ids = append(ids, *qBy)
	}
	if clBy != nil {
		ids = append(ids, *clBy)
	}
	if cfBy != nil {
		ids = append(ids, *cfBy)
	}
	if ccBy != nil {
		ids = append(ids, *ccBy)
	}
	for _, r := range dels {
		ids = append(ids, r.createdBy)
	}
	for _, r := range pays {
		ids = append(ids, r.by)
	}
	for _, r := range rets {
		ids = append(ids, r.by)
	}
	names, err := userNames(ctx, q, ids...)
	if err != nil {
		return nil, err
	}
	who := func(uid string) *person {
		if uid == "" {
			return nil
		}
		n, ok := names[uid]
		if !ok {
			n = i18n.T(lang, "sales.unknown_user")
		}
		return &person{ID: uid, Name: n}
	}
	sd.CreatedBy = who(createdBy)
	if qBy != nil {
		sd.QuotedBy = who(*qBy)
	}
	if clBy != nil {
		sd.ClosedBy = who(*clBy)
	}
	if cfBy != nil {
		sd.ConfirmedBy = who(*cfBy)
	}
	if ccBy != nil {
		sd.CancelledBy = who(*ccBy)
	}
	sd.Deliveries = make([]deliveryOut, 0, len(dels))
	for _, r := range dels {
		r.d.CreatedBy = who(r.createdBy)
		sd.Deliveries = append(sd.Deliveries, r.d)
	}
	sd.Payments = make([]paymentOut, 0, len(pays))
	for _, r := range pays {
		r.p.ReceivedBy = who(r.by)
		sd.Payments = append(sd.Payments, r.p)
	}
	sd.Returns = make([]returnOut, 0, len(rets))
	for _, r := range rets {
		r.r.CreatedBy = who(r.by)
		sd.Returns = append(sd.Returns, r.r)
	}

	deliveredBy := sumByProduct(activeByProduct)
	returnedBy := sumByProduct(retRows)
	returnable := returnableByProduct(deliveredBy, returnedBy)
	sd.Returnable = make([]returnableOut, 0, len(returnable.keys))
	for _, pid := range returnable.keys {
		ro := returnableOut{ProductID: pid, Returnable: returnable.m[pid], Delivered: deliveredBy.m[pid], Returned: returnedBy.m[pid]}
		if p, ok := productInfo[pid]; ok {
			pp := p
			ro.Product = &pp
		}
		sd.Returnable = append(sd.Returnable, ro)
	}

	var qty, amount, delivered, remaining, gross, disc int64
	for _, l := range sd.Lines {
		qty += l.Quantity
		amount += l.Amount
		gross += l.Gross
		disc += l.DiscountAmount
		delivered += l.Delivered
		remaining += l.Remaining
		if l.NeedsGmApproval {
			sd.NeedsGmApproval = true
		}
	}
	var ch chargeLineOut
	if err := q.QueryRow(ctx, `SELECT id, description, amount FROM "SaleLine" WHERE "saleId" = $1 AND kind = 'DELIVERY_CHARGE' LIMIT 1`, id).Scan(&ch.ID, &ch.Description, &ch.Amount); err == nil {
		sd.ChargeLine = &ch
	} else if !db.IsNoRows(err) {
		return nil, err
	}
	subtotal := amount
	if sd.ChargeLine != nil {
		amount += sd.ChargeLine.Amount
	}
	sd.Totals = saleTotals{Quantity: qty, Amount: amount, Paid: paid, Balance: outstandingOf(sd.LegacyStatus, amount, paid), Delivered: delivered, Remaining: remaining,
		Returned: returnedTotal, Gross: gross, Discount: disc, Subtotal: subtotal, Charge: amount - subtotal}
	var depPaid int64
	for _, p := range sd.Payments {
		depPaid += p.Amount
	}
	sd.DepositStatus = depositStatus(sd.DepositAmount, depPaid)
	sd.Overdue = sd.DueDate != nil && *sd.DueDate < dates.TodayISO() && sd.Totals.Balance > 0
	sd.PaymentStatus = paymentStatus(amount, paid)
	sd.DeliveryStatus = deliveryStatus(qty, delivered)
	return &sd, nil
}

// ───────────────────────────── sales list ─────────────────────────────

type salesFilters struct {
	From, To       string
	PlantID        string
	CustomerID     string
	Status         string
	PaymentStatus  string
	DeliveryStatus string
	Q              string
	TransportMode  string
	Pending        bool // awaiting an approval
	Overdue        bool
}

type saleRow struct {
	ID              string        `json:"id"`
	Number          string        `json:"number"`
	Date            string        `json:"date"`
	Status          string        `json:"status"`
	Plant           ref           `json:"plant"`
	Customer        idName        `json:"customer"`
	InvoiceType     string        `json:"invoiceType"`
	EbmInvoiceNo    *string       `json:"ebmInvoiceNo"`
	EbmReceiptNo    *string       `json:"ebmReceiptNo"`
	Products        []saleProduct `json:"products"`
	DNNumbers       []string      `json:"dnNumbers"`
	Quantity        int64         `json:"quantity"`
	Amount          int64         `json:"amount"`
	Paid            int64         `json:"paid"`
	Outstanding     int64         `json:"outstanding"`
	Delivered       int64         `json:"delivered"`
	PaymentStatus   string        `json:"paymentStatus"`
	DeliveryStatus  string        `json:"deliveryStatus"`
	NeedsGmApproval bool          `json:"needsGmApproval"`
	TransportMode   string        `json:"transportMode"`
	DueDate         *string       `json:"dueDate"`
	Overdue         bool          `json:"overdue"`
	PendingApproval *string       `json:"pendingApproval"` // kind of the open approval
	Version         int           `json:"version"`
}

type idName struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type saleProduct struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	Quantity  int64  `json:"quantity"`
	UnitPrice int64  `json:"unitPrice"`
}

type moneyTotals struct {
	Amount      int64 `json:"amount"`
	Quantity    int64 `json:"quantity"`
	Paid        int64 `json:"paid"`
	Outstanding int64 `json:"outstanding"`
}

type saleList struct {
	Items        []saleRow        `json:"items"`
	Total        int              `json:"total"`
	Page         int              `json:"page"`
	PageSize     int              `json:"pageSize"`
	PageTotals   moneyTotals      `json:"pageTotals"`
	RangeTotals  moneyTotals      `json:"rangeTotals"`
	StatusCounts map[string]int64 `json:"statusCounts"`
	KPIs         salesKPIs        `json:"kpis"`
	Range        struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"range"`
}

type salesKPIs struct {
	Sales       int64 `json:"sales"`
	Amount      int64 `json:"amount"`
	Quantity    int64 `json:"quantity"`
	Paid        int64 `json:"paid"`
	Outstanding int64 `json:"outstanding"`
	Drafts      int64 `json:"drafts"`
}

// salesCTE builds the per-sale aggregate CTE (amount, qty, paid, delivered + derived statuses). Aggregates are limited to the
// filtered sales so a narrow date range never scans the whole line/payment tables.
func salesCTE(f salesFilters, q *qb) string {
	conds := []string{fmt.Sprintf("sa.date BETWEEN %s::date AND %s::date", q.arg(f.From), q.arg(f.To))}
	if f.PlantID != "" {
		conds = append(conds, `sa."plantId" = `+q.arg(f.PlantID))
	}
	if f.CustomerID != "" {
		conds = append(conds, `sa."customerId" = `+q.arg(f.CustomerID))
	}
	if f.Q != "" {
		l := q.arg(likeArg(f.Q))
		conds = append(conds, fmt.Sprintf(`(sa.number ILIKE %[1]s OR c.name ILIKE %[1]s OR sa."ebmInvoiceNo" ILIKE %[1]s OR sa."ebmReceiptNo" ILIKE %[1]s)`, l))
	}
	if f.TransportMode != "" {
		conds = append(conds, `sa."transportMode" = `+q.arg(f.TransportMode))
	}
	if f.Pending {
		conds = append(conds, `EXISTS (SELECT 1 FROM sale_approval ap WHERE ap.sale_id = sa.id AND ap.status = 'PENDING')`)
	}
	return `WITH s AS (
		  SELECT sa.id, sa.number, sa.date, sa."orderStatus" AS status, sa.status::text AS legacy, sa."transportMode", sa."dueDate", sa.version,
		    (SELECT ap.kind FROM sale_approval ap WHERE ap.sale_id = sa.id AND ap.status = 'PENDING') AS pending,
		    sa."invoiceType", sa."ebmInvoiceNo", sa."ebmReceiptNo",
		    pl.id AS "plantId", pl.code AS "plantCode", pl.name AS "plantName", c.id AS "customerId", c.name AS "customerName"
		  FROM "Sale" sa JOIN "Customer" c ON c.id = sa."customerId" JOIN "Plant" pl ON pl.id = sa."plantId"
		  WHERE ` + and(conds) + `
		), l AS (
		  SELECT "saleId", SUM(amount)::bigint AS amount, SUM(quantity)::bigint AS qty FROM "SaleLine" WHERE "saleId" IN (SELECT id FROM s) GROUP BY "saleId"
		), p AS (
		  SELECT "saleId", SUM(amount)::bigint AS paid FROM "Payment" WHERE "saleId" IN (SELECT id FROM s) GROUP BY "saleId"
		), d AS (
		  SELECT sl."saleId", SUM(dl.quantity)::bigint AS delivered
		  FROM "DeliveryLine" dl JOIN "Delivery" dv ON dv.id = dl."deliveryId" AND dv.status <> 'CANCELLED'
		  JOIN "SaleLine" sl ON sl.id = dl."saleLineId" WHERE sl."saleId" IN (SELECT id FROM s) GROUP BY sl."saleId"
		), base AS (
		  SELECT s.*, COALESCE(l.amount, 0) AS amount, COALESCE(l.qty, 0) AS qty, COALESCE(p.paid, 0) AS paid, COALESCE(d.delivered, 0) AS delivered
		  FROM s LEFT JOIN l ON l."saleId" = s.id LEFT JOIN p ON p."saleId" = s.id LEFT JOIN d ON d."saleId" = s.id
		), derived AS (
		  SELECT base.*,
		    CASE WHEN paid >= amount THEN 'PAID' WHEN paid > 0 THEN 'PARTIAL' ELSE 'UNPAID' END AS "paymentStatus",
		    CASE WHEN delivered <= 0 THEN 'NOT_DELIVERED' WHEN delivered >= qty THEN 'DELIVERED' ELSE 'PARTIAL' END AS "deliveryStatus",
		    CASE WHEN legacy = 'CONFIRMED' THEN GREATEST(amount - paid, 0) ELSE 0 END AS outstanding
		  FROM base
		)`
}

func (s *service) listSales(ctx context.Context, f salesFilters, pg httpx.Pager, maxDiscount float64) (*saleList, error) {
	q := &qb{}
	cte := salesCTE(f, q)
	var derivedConds []string
	if f.PaymentStatus != "" {
		derivedConds = append(derivedConds, `"paymentStatus" = `+q.arg(f.PaymentStatus))
	}
	if f.DeliveryStatus != "" {
		derivedConds = append(derivedConds, `"deliveryStatus" = `+q.arg(f.DeliveryStatus))
	}
	if f.Overdue {
		derivedConds = append(derivedConds, `"dueDate" < `+q.arg(dates.TodayISO())+`::date AND outstanding > 0`)
	}
	derivedFilter := and(derivedConds)
	statusFilter := "TRUE"
	if f.Status != "" {
		statusFilter = "status = " + q.arg(f.Status)
	}
	filt := "(" + derivedFilter + ") AND (" + statusFilter + ")"
	money := filt + " AND status <> 'CANCELLED'"
	if f.Status == "CANCELLED" {
		money = filt
	}
	nargs := len(q.args)
	limit, offset := q.arg(pg.Limit), q.arg(pg.Offset)
	pageArgs := append([]any(nil), q.args...)
	aggArgs := append([]any(nil), q.args[:nargs]...)

	type pageRow struct {
		saleRow
		plantID, plantCode, plantName string
		customerName                  string
	}
	var rows []pageRow
	var agg [15]int64
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		rs, err := s.pool.Query(gctx, cte+`
			SELECT id, number, date, status, "invoiceType", "ebmInvoiceNo", "ebmReceiptNo", "plantId", "plantCode", "plantName", "customerId", "customerName",
			  amount, qty, paid, delivered, outstanding, "paymentStatus", "deliveryStatus", "transportMode", "dueDate", pending, version
			FROM derived WHERE `+filt+` ORDER BY date DESC, number DESC LIMIT `+limit+` OFFSET `+offset, pageArgs...)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var r pageRow
			var d time.Time
			var due *time.Time
			if err := rs.Scan(&r.ID, &r.Number, &d, &r.Status, &r.InvoiceType, &r.EbmInvoiceNo, &r.EbmReceiptNo, &r.Plant.ID, &r.Plant.Code, &r.Plant.Name,
				&r.Customer.ID, &r.Customer.Name, &r.Amount, &r.Quantity, &r.Paid, &r.Delivered, &r.Outstanding, &r.PaymentStatus, &r.DeliveryStatus,
				&r.TransportMode, &due, &r.PendingApproval, &r.Version); err != nil {
				return err
			}
			r.Date = dates.ToISO(d)
			if due != nil {
				ds := dates.ToISO(*due)
				r.DueDate = &ds
				r.Overdue = ds < dates.TodayISO() && r.Outstanding > 0
			}
			rows = append(rows, r)
		}
		return rs.Err()
	})
	g.Go(func() error {
		return s.pool.QueryRow(gctx, cte+`
			SELECT
			  COUNT(*) FILTER (WHERE `+filt+`),
			  COALESCE(SUM(amount) FILTER (WHERE `+money+`), 0), COALESCE(SUM(qty) FILTER (WHERE `+money+`), 0),
			  COALESCE(SUM(paid) FILTER (WHERE `+money+`), 0), COALESCE(SUM(outstanding) FILTER (WHERE `+filt+`), 0),
			  COUNT(*) FILTER (WHERE `+derivedFilter+`),
			  COUNT(*) FILTER (WHERE `+derivedFilter+` AND legacy = 'DRAFT'),
			  COUNT(*) FILTER (WHERE `+derivedFilter+` AND legacy = 'CONFIRMED'),
			  COUNT(*) FILTER (WHERE `+derivedFilter+` AND legacy = 'CANCELLED'),
			  COUNT(*) FILTER (WHERE legacy = 'CONFIRMED'),
			  COALESCE(SUM(amount) FILTER (WHERE legacy = 'CONFIRMED'), 0), COALESCE(SUM(qty) FILTER (WHERE legacy = 'CONFIRMED'), 0),
			  COALESCE(SUM(paid) FILTER (WHERE legacy = 'CONFIRMED'), 0), COALESCE(SUM(outstanding) FILTER (WHERE legacy = 'CONFIRMED'), 0),
			  COUNT(*) FILTER (WHERE legacy = 'DRAFT')
			FROM derived`, aggArgs...).Scan(&agg[0], &agg[1], &agg[2], &agg[3], &agg[4], &agg[5], &agg[6], &agg[7], &agg[8], &agg[9], &agg[10], &agg[11], &agg[12], &agg[13], &agg[14])
	})
	byStatus := map[string]int64{}
	g.Go(func() error {
		rs, err := s.pool.Query(gctx, cte+` SELECT status, COUNT(*) FROM derived WHERE `+derivedFilter+` GROUP BY status`, aggArgs...)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var st string
			var n int64
			if err := rs.Scan(&st, &n); err != nil {
				return err
			}
			byStatus[st] = n
		}
		return rs.Err()
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}
	items := make([]saleRow, 0, len(rows))
	ids := make([]string, 0, len(rows))
	idx := map[string]int{}
	for _, r := range rows {
		r.Products = []saleProduct{}
		r.DNNumbers = []string{}
		idx[r.ID] = len(items)
		ids = append(ids, r.ID)
		items = append(items, r.saleRow)
	}
	if len(ids) > 0 {
		lr, err := s.pool.Query(ctx, `
			SELECT sl."saleId", pr.code, pr.name, sl.quantity, sl."unitPrice", pr."defaultPrice", sl.amount
			FROM "SaleLine" sl JOIN "Product" pr ON pr.id = sl."productId"
			WHERE sl."saleId" = ANY($1) ORDER BY sl."saleId", sl.id`, ids)
		if err != nil {
			return nil, err
		}
		for lr.Next() {
			var sid string
			var p saleProduct
			var def, amt int64
			if err := lr.Scan(&sid, &p.Code, &p.Name, &p.Quantity, &p.UnitPrice, &def, &amt); err != nil {
				lr.Close()
				return nil, err
			}
			it := &items[idx[sid]]
			it.Products = append(it.Products, p)
			if netNeedsGmApproval(amt, p.Quantity, def, maxDiscount) {
				it.NeedsGmApproval = true
			}
		}
		lr.Close()
		if err := lr.Err(); err != nil {
			return nil, err
		}
		dr, err := s.pool.Query(ctx, `SELECT "saleId", "dnNumber" FROM "Delivery" WHERE "saleId" = ANY($1) AND status <> 'CANCELLED' ORDER BY "createdAt"`, ids)
		if err != nil {
			return nil, err
		}
		for dr.Next() {
			var sid, dn string
			if err := dr.Scan(&sid, &dn); err != nil {
				dr.Close()
				return nil, err
			}
			it := &items[idx[sid]]
			it.DNNumbers = append(it.DNNumbers, dn)
		}
		dr.Close()
		if err := dr.Err(); err != nil {
			return nil, err
		}
	}

	out := &saleList{Items: items, Total: int(agg[0]), Page: pg.Page, PageSize: pg.PageSize}
	for _, i := range items {
		if i.Status == StCancelled && f.Status != StCancelled {
			continue
		}
		out.PageTotals.Amount += i.Amount
		out.PageTotals.Quantity += i.Quantity
		out.PageTotals.Paid += i.Paid
		out.PageTotals.Outstanding += i.Outstanding
	}
	out.RangeTotals = moneyTotals{Amount: agg[1], Quantity: agg[2], Paid: agg[3], Outstanding: agg[4]}
	out.StatusCounts = map[string]int64{"ALL": agg[5], "OPEN_DRAFT": agg[6], "COMMITTED": agg[7]}
	for _, st := range OrderStatuses {
		out.StatusCounts[st] = byStatus[st]
	}
	out.KPIs = salesKPIs{Sales: agg[9], Amount: agg[10], Quantity: agg[11], Paid: agg[12], Outstanding: agg[13], Drafts: agg[14]}
	out.Range.From, out.Range.To = f.From, f.To
	return out, nil
}

// ───────────────────────────── deliveries list ─────────────────────────────

type deliveryRow struct {
	ID          string          `json:"id"`
	DNNumber    string          `json:"dnNumber"`
	Date        string          `json:"date"`
	Status      string          `json:"status"`
	Plant       ref             `json:"plant"`
	Sale        saleNum         `json:"sale"`
	Customer    idName          `json:"customer"`
	Vehicle     *string         `json:"vehicle"`
	Driver      *string         `json:"driver"`
	ReceivedBy  *string         `json:"receivedBy"`
	DeliveredAt *jsTime         `json:"deliveredAt"`
	TotalQty    int64           `json:"totalQty"`
	Products    json.RawMessage `json:"products"`
	Destination *string         `json:"destination"`
	Transport   string          `json:"transportMode"`
	Note        *string         `json:"deliveryNote"`
	HasPhoto    bool            `json:"hasPhoto"`
	Offline     bool            `json:"capturedOffline"`
}

type deliveryFilters struct {
	From, To, PlantID, CustomerID, SaleID, Status, Transport, Q string
}

type saleNum struct {
	ID     string `json:"id"`
	Number string `json:"number"`
}

type deliveryList struct {
	Items       []deliveryRow `json:"items"`
	Total       int64         `json:"total"`
	Page        int           `json:"page"`
	PageSize    int           `json:"pageSize"`
	RangeTotals struct {
		Quantity int64 `json:"quantity"`
	} `json:"rangeTotals"`
	StatusCounts map[string]int64 `json:"statusCounts"`
}

func (s *service) listDeliveries(ctx context.Context, f deliveryFilters, pg httpx.Pager) (*deliveryList, error) {
	status := f.Status
	q := &qb{}
	conds := []string{fmt.Sprintf("d.date BETWEEN %s::date AND %s::date", q.arg(f.From), q.arg(f.To))}
	if f.PlantID != "" {
		conds = append(conds, `d."plantId" = `+q.arg(f.PlantID))
	}
	if f.CustomerID != "" {
		conds = append(conds, `sa."customerId" = `+q.arg(f.CustomerID))
	}
	if f.SaleID != "" {
		conds = append(conds, `d."saleId" = `+q.arg(f.SaleID))
	}
	if f.Transport != "" {
		conds = append(conds, `d."transportMode" = `+q.arg(f.Transport))
	}
	if f.Q != "" {
		l := q.arg(likeArg(f.Q))
		conds = append(conds, fmt.Sprintf(`(d."dnNumber" ILIKE %[1]s OR sa.number ILIKE %[1]s OR c.name ILIKE %[1]s OR d.driver ILIKE %[1]s OR d.vehicle ILIKE %[1]s OR d.destination ILIKE %[1]s)`, l))
	}
	from_ := ` FROM "Delivery" d JOIN "Sale" sa ON sa.id = d."saleId" JOIN "Customer" c ON c.id = sa."customerId" JOIN "Plant" pl ON pl.id = d."plantId"`
	where := and(conds)
	baseArgs := append([]any(nil), q.args...)

	out := &deliveryList{Page: pg.Page, PageSize: pg.PageSize, StatusCounts: map[string]int64{}}
	pageQ := &qb{args: append([]any(nil), baseArgs...)}
	pageWhere := where
	if status != "" {
		pageWhere += " AND d.status = " + pageQ.arg(status)
	}
	limit, offset := pageQ.arg(pg.Limit), pageQ.arg(pg.Offset)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		rs, err := s.pool.Query(gctx, `
			SELECT d.id, d."dnNumber", d.date, d.status::text, pl.id, pl.code, pl.name, sa.id, sa.number, c.id, c.name,
			  d.vehicle, d.driver, d."receivedBy", d."deliveredAt", COALESCE(x.qty, 0)::bigint, COALESCE(x.products, '[]'::json),
			  d.destination, d."transportMode", d."deliveryNote", EXISTS (SELECT 1 FROM delivery_photo ph WHERE ph.delivery_id = d.id), d."capturedOffline"
			`+from_+`
			LEFT JOIN LATERAL (
			  SELECT SUM(dl.quantity) AS qty,
			    json_agg(json_build_object('code', pr.code, 'name', pr.name, 'quantity', dl.quantity) ORDER BY pr."sortOrder", pr.code, dl.id) AS products
			  FROM "DeliveryLine" dl JOIN "Product" pr ON pr.id = dl."productId" WHERE dl."deliveryId" = d.id) x ON TRUE
			WHERE `+pageWhere+` ORDER BY d.date DESC, d."createdAt" DESC LIMIT `+limit+` OFFSET `+offset, pageQ.args...)
		if err != nil {
			return err
		}
		defer rs.Close()
		out.Items = []deliveryRow{}
		for rs.Next() {
			var r deliveryRow
			var d time.Time
			var at *time.Time
			if err := rs.Scan(&r.ID, &r.DNNumber, &d, &r.Status, &r.Plant.ID, &r.Plant.Code, &r.Plant.Name, &r.Sale.ID, &r.Sale.Number,
				&r.Customer.ID, &r.Customer.Name, &r.Vehicle, &r.Driver, &r.ReceivedBy, &at, &r.TotalQty, &r.Products,
				&r.Destination, &r.Transport, &r.Note, &r.HasPhoto, &r.Offline); err != nil {
				return err
			}
			r.Date = dates.ToISO(d)
			r.DeliveredAt = jtp(at)
			out.Items = append(out.Items, r)
		}
		return rs.Err()
	})
	var qtyByStatus = map[string]int64{}
	g.Go(func() error {
		rs, err := s.pool.Query(gctx, `
			SELECT d.status::text, COUNT(*)::bigint, COALESCE(SUM((SELECT SUM(dl.quantity) FROM "DeliveryLine" dl WHERE dl."deliveryId" = d.id)), 0)::bigint`+from_+`
			WHERE `+where+` GROUP BY d.status`, baseArgs...)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var st string
			var n, qty int64
			if err := rs.Scan(&st, &n, &qty); err != nil {
				return err
			}
			out.StatusCounts[st] = n
			qtyByStatus[st] = qty
		}
		return rs.Err()
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}
	if status != "" {
		out.Total = out.StatusCounts[status]
		out.RangeTotals.Quantity = qtyByStatus[status]
	} else {
		for st, n := range out.StatusCounts {
			out.Total += n
			if st != "CANCELLED" {
				out.RangeTotals.Quantity += qtyByStatus[st]
			}
		}
	}
	return out, nil
}

// ───────────────────────────── customers ─────────────────────────────

type customerRow struct {
	customer
	Contacts     int64   `json:"contacts"`
	SalesCount   int64   `json:"salesCount"`
	TotalSales   int64   `json:"totalSales"`
	Paid         int64   `json:"paid"`
	Outstanding  int64   `json:"outstanding"`
	LastSaleDate *string `json:"lastSaleDate"`
}

type customerList struct {
	Items    []customerRow `json:"items"`
	Total    int64         `json:"total"`
	Page     int           `json:"page"`
	PageSize int           `json:"pageSize"`
	Totals   struct {
		TotalSales  int64 `json:"totalSales"`
		Paid        int64 `json:"paid"`
		Outstanding int64 `json:"outstanding"`
		WithBalance int64 `json:"withBalance"`
	} `json:"totals"`
}

// customerAgg aggregates confirmed sales per customer (lines and payments restricted to confirmed sales).
const customerAgg = `
	SELECT sa."customerId", COUNT(*)::bigint AS sales, SUM(l.amount)::bigint AS amount, SUM(COALESCE(p.paid, 0))::bigint AS paid,
	  SUM(GREATEST(l.amount - COALESCE(p.paid, 0), 0))::bigint AS outstanding, MAX(sa.date) AS "lastSaleDate"
	FROM "Sale" sa
	JOIN (SELECT "saleId", SUM(amount) AS amount FROM "SaleLine" WHERE "saleId" IN (SELECT id FROM "Sale" WHERE status = 'CONFIRMED') GROUP BY "saleId") l ON l."saleId" = sa.id
	LEFT JOIN (SELECT "saleId", SUM(amount) AS paid FROM "Payment" WHERE "saleId" IN (SELECT id FROM "Sale" WHERE status = 'CONFIRMED') GROUP BY "saleId") p ON p."saleId" = sa.id
	WHERE sa.status = 'CONFIRMED' GROUP BY sa."customerId"`

func (s *service) listCustomers(ctx context.Context, search, sort string, activeOnly bool, status, clientType string, pg httpx.Pager) (*customerList, error) {
	q := &qb{}
	var conds []string
	if search != "" {
		l := q.arg(likeArg(search))
		conds = append(conds, fmt.Sprintf(`(c.name ILIKE %[1]s OR c.tin ILIKE %[1]s OR c.phone ILIKE %[1]s OR c.email ILIKE %[1]s
			OR EXISTS (SELECT 1 FROM customer_contact cc WHERE cc.customer_id = c.id AND (cc.name ILIKE %[1]s OR cc.phone ILIKE %[1]s OR cc.email ILIKE %[1]s)))`, l))
	}
	if activeOnly {
		conds = append(conds, `c."isActive" = TRUE`)
	}
	if status != "" {
		conds = append(conds, `c.status = `+q.arg(status))
	}
	if clientType != "" {
		conds = append(conds, `c."clientType" = `+q.arg(clientType))
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + and(conds)
	}
	order := ` ORDER BY c.name ASC`
	switch sort {
	case "outstanding":
		order = ` ORDER BY COALESCE(a.outstanding, 0) DESC, c.name ASC`
	case "lastSale":
		order = ` ORDER BY a."lastSaleDate" DESC NULLS LAST, c.name ASC`
	}
	from := ` FROM "Customer" c LEFT JOIN (` + customerAgg + `) a ON a."customerId" = c.id` + where
	totalsArgs := append([]any(nil), q.args...)
	limit, offset := q.arg(pg.Limit), q.arg(pg.Offset)
	out := &customerList{Page: pg.Page, PageSize: pg.PageSize, Items: []customerRow{}}
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		rs, err := s.pool.Query(gctx, `SELECT c.id, c.name, c.tin, c.phone, c.email, c.address, c."isActive", c."createdAt", c."clientType", c.status, c.notes, c."paymentTerm",
			(SELECT COUNT(*) FROM customer_contact cc WHERE cc.customer_id = c.id),
			COALESCE(a.sales, 0), COALESCE(a.amount, 0), COALESCE(a.paid, 0), COALESCE(a.outstanding, 0), a."lastSaleDate"`+from+order+` LIMIT `+limit+` OFFSET `+offset, q.args...)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var r customerRow
			var created time.Time
			var last *time.Time
			if err := rs.Scan(&r.ID, &r.Name, &r.TIN, &r.Phone, &r.Email, &r.Address, &r.IsActive, &created, &r.ClientType, &r.Status, &r.Notes, &r.PaymentTerm, &r.Contacts,
				&r.SalesCount, &r.TotalSales, &r.Paid, &r.Outstanding, &last); err != nil {
				return err
			}
			r.CreatedAt = jt(created)
			if last != nil {
				d := dates.ToISO(*last)
				r.LastSaleDate = &d
			}
			out.Items = append(out.Items, r)
		}
		return rs.Err()
	})
	g.Go(func() error {
		return s.pool.QueryRow(gctx, `SELECT COUNT(*)::bigint, COALESCE(SUM(a.amount), 0)::bigint, COALESCE(SUM(a.paid), 0)::bigint, COALESCE(SUM(a.outstanding), 0)::bigint,
			COUNT(*) FILTER (WHERE a.outstanding > 0)::bigint`+from, totalsArgs...).Scan(&out.Total, &out.Totals.TotalSales, &out.Totals.Paid, &out.Totals.Outstanding, &out.Totals.WithBalance)
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

// ───────────────────────────── statement ─────────────────────────────

type stmtRow struct {
	Date        string  `json:"date"`
	Kind        string  `json:"kind"`
	Reference   string  `json:"reference"`
	Description string  `json:"description"`
	Debit       int64   `json:"debit"`
	Credit      int64   `json:"credit"`
	Balance     int64   `json:"balance"`
	SaleID      *string `json:"saleId,omitempty"`
}

type openSaleRow struct {
	ID          string `json:"id"`
	Number      string `json:"number"`
	Date        string `json:"date"`
	Amount      int64  `json:"amount"`
	Paid        int64  `json:"paid"`
	Outstanding int64  `json:"outstanding"`
}

type statementOut struct {
	Customer customer `json:"customer"`
	Range    struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"range"`
	Summary struct {
		SalesCount   int     `json:"salesCount"`
		TotalSales   int64   `json:"totalSales"`
		Paid         int64   `json:"paid"`
		Outstanding  int64   `json:"outstanding"`
		LastSaleDate *string `json:"lastSaleDate"`
	} `json:"summary"`
	Statement struct {
		Opening int64     `json:"opening"`
		Debit   int64     `json:"debit"`
		Credit  int64     `json:"credit"`
		Closing int64     `json:"closing"`
		Rows    []stmtRow `json:"rows"`
	} `json:"statement"`
	OpenSales []openSaleRow `json:"openSales"`
}

// buildCustomerStatement: opening balance, sales (debits), payments (credits), running balance.
func (s *service) buildCustomerStatement(ctx context.Context, lang i18n.Lang, customerID, fromISO, toISO string) (*statementOut, error) {
	var out statementOut
	var created time.Time
	err := s.pool.QueryRow(ctx, `SELECT id, name, tin, phone, email, address, "isActive", "createdAt", "clientType", status, notes, "paymentTerm" FROM "Customer" WHERE id=$1`, customerID).
		Scan(&out.Customer.ID, &out.Customer.Name, &out.Customer.TIN, &out.Customer.Phone, &out.Customer.Email, &out.Customer.Address, &out.Customer.IsActive, &created,
			&out.Customer.ClientType, &out.Customer.Status, &out.Customer.Notes, &out.Customer.PaymentTerm)
	if err != nil {
		if db.IsNoRows(err) {
			return nil, httpx.NotFound("entity.customer")
		}
		return nil, err
	}
	out.Customer.CreatedAt = jt(created)
	today := dates.TodayISO()
	if fromISO == "" {
		fromISO = today[:4] + "-01-01"
	}
	if toISO == "" {
		toISO = today
	}
	rng := dates.ResolveRange(fromISO, toISO)

	type saleLedger struct {
		id, number, plant string
		inv               *string
		date, created     time.Time
		amount, qty       int64
	}
	var sales []saleLedger
	rs, err := s.pool.Query(ctx, `
		SELECT sa.id, sa.number, sa.date, sa."createdAt", sa."ebmInvoiceNo", pl.code, COALESCE(l.amount, 0)::bigint, COALESCE(l.qty, 0)::bigint
		FROM "Sale" sa JOIN "Plant" pl ON pl.id = sa."plantId"
		LEFT JOIN (SELECT sl."saleId", SUM(sl.amount) AS amount, SUM(sl.quantity) AS qty FROM "SaleLine" sl
		           WHERE sl."saleId" IN (SELECT id FROM "Sale" WHERE "customerId" = $1 AND status = 'CONFIRMED') GROUP BY sl."saleId") l ON l."saleId" = sa.id
		WHERE sa."customerId" = $1 AND sa.status = 'CONFIRMED' AND sa.date <= $2::date
		ORDER BY sa.date, sa."createdAt"`, customerID, rng.To)
	if err != nil {
		return nil, err
	}
	for rs.Next() {
		var r saleLedger
		if err := rs.Scan(&r.id, &r.number, &r.date, &r.created, &r.inv, &r.plant, &r.amount, &r.qty); err != nil {
			rs.Close()
			return nil, err
		}
		sales = append(sales, r)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return nil, err
	}
	type payLedger struct {
		saleID, number, method string
		ref                    *string
		date, created          time.Time
		amount                 int64
	}
	var pays []payLedger
	ps, err := s.pool.Query(ctx, `
		SELECT p."saleId", sa.number, p.date, p."createdAt", p.amount, p.method::text, p.reference
		FROM "Payment" p JOIN "Sale" sa ON sa.id = p."saleId"
		WHERE sa."customerId" = $1 AND sa.status = 'CONFIRMED' AND p.date <= $2::date ORDER BY p.date, p."createdAt"`, customerID, rng.To)
	if err != nil {
		return nil, err
	}
	for ps.Next() {
		var r payLedger
		if err := ps.Scan(&r.saleID, &r.number, &r.date, &r.created, &r.amount, &r.method, &r.ref); err != nil {
			ps.Close()
			return nil, err
		}
		pays = append(pays, r)
	}
	ps.Close()
	if err := ps.Err(); err != nil {
		return nil, err
	}

	var opening int64
	var entries []statementEntry
	paidBySale := map[string]int64{}
	for _, p := range pays {
		paidBySale[p.saleID] += p.amount
	}
	var totalSales, totalPaid int64
	for _, sl := range sales {
		totalSales += sl.amount
		if sl.date.Before(rng.From) {
			opening += sl.amount
			continue
		}
		inv := ""
		if sl.inv != nil && *sl.inv != "" {
			inv = i18n.T(lang, "sales.stmt.ebm_inv", "no", *sl.inv)
		}
		entries = append(entries, statementEntry{Date: sl.date, Kind: "SALE", Reference: sl.number,
			Description: i18n.T(lang, "sales.stmt.sale", "plant", sl.plant, "inv", inv, "qty", format.Int(float64(sl.qty))),
			Debit:       sl.amount, Order: sl.created.UnixMilli(), SaleID: sl.id})
	}
	for _, p := range pays {
		totalPaid += p.amount
		if p.date.Before(rng.From) {
			opening -= p.amount
			continue
		}
		ref := ""
		if p.ref != nil && *p.ref != "" {
			ref = i18n.T(lang, "sales.stmt.ref", "ref", *p.ref)
		}
		entries = append(entries, statementEntry{Date: p.date, Kind: "PAYMENT", Reference: p.number,
			Description: i18n.T(lang, "sales.stmt.payment", "method", methodLabel(lang, p.method), "ref", ref),
			Credit:      p.amount, Order: p.created.UnixMilli(), SaleID: p.saleID})
	}
	rows, debit, credit, closing := buildStatement(opening, entries)

	out.Range.From, out.Range.To = rng.FromISO, rng.ToISO
	out.Summary.SalesCount = len(sales)
	out.Summary.TotalSales = totalSales
	out.Summary.Paid = totalPaid
	out.Summary.Outstanding = totalSales - totalPaid
	if n := len(sales); n > 0 {
		d := dates.ToISO(sales[n-1].date)
		out.Summary.LastSaleDate = &d
	}
	out.Statement.Opening, out.Statement.Debit, out.Statement.Credit, out.Statement.Closing = opening, debit, credit, closing
	out.Statement.Rows = make([]stmtRow, 0, len(rows))
	for _, r := range rows {
		sid := r.SaleID
		out.Statement.Rows = append(out.Statement.Rows, stmtRow{Date: dates.ToISO(r.Date), Kind: r.Kind, Reference: r.Reference, Description: r.Description,
			Debit: r.Debit, Credit: r.Credit, Balance: r.Balance, SaleID: &sid})
	}
	out.OpenSales = []openSaleRow{}
	for _, sl := range sales {
		paid := paidBySale[sl.id]
		if sl.amount-paid > 0 {
			out.OpenSales = append(out.OpenSales, openSaleRow{ID: sl.id, Number: sl.number, Date: dates.ToISO(sl.date), Amount: sl.amount, Paid: paid, Outstanding: sl.amount - paid})
		}
	}
	return &out, nil
}

// ───────────────────────────── receivables ─────────────────────────────

type receivableSale struct {
	ID           string `json:"id"`
	Number       string `json:"number"`
	Date         string `json:"date"`
	CustomerID   string `json:"customerId"`
	CustomerName string `json:"customerName"`
	PlantCode    string `json:"plantCode"`
	Amount       int64  `json:"amount"`
	Paid         int64  `json:"paid"`
	Outstanding  int64  `json:"outstanding"`
	Days         int    `json:"days"`
}

type receivableRow struct {
	CustomerID   string  `json:"customerId"`
	CustomerName string  `json:"customerName"`
	OpenSales    int     `json:"openSales"`
	OldestDate   *string `json:"oldestDate"`
	buckets
}

type receivablesOut struct {
	AsOf   string           `json:"asOf"`
	Rows   []receivableRow  `json:"rows"`
	Totals ageingTotals     `json:"totals"`
	Sales  []receivableSale `json:"sales"`
}

func (s *service) buildReceivables(ctx context.Context, asOfISO, plantID string) (*receivablesOut, error) {
	asOf := dates.MustParseISO(asOfISO)
	q := &qb{}
	a := q.arg(asOfISO)
	plantCond := ""
	if plantID != "" {
		plantCond = ` AND sa."plantId" = ` + q.arg(plantID)
	}
	rs, err := s.pool.Query(ctx, `
		WITH s AS (
		  SELECT sa.id, sa.number, sa.date, sa."customerId", sa."plantId" FROM "Sale" sa
		  WHERE sa.status = 'CONFIRMED' AND sa.date <= `+a+`::date`+plantCond+`
		), l AS (SELECT "saleId", SUM(amount)::bigint AS amount FROM "SaleLine" WHERE "saleId" IN (SELECT id FROM s) GROUP BY "saleId"),
		p AS (SELECT "saleId", SUM(amount)::bigint AS paid FROM "Payment" WHERE "saleId" IN (SELECT id FROM s) AND date <= `+a+`::date GROUP BY "saleId")
		SELECT s.id, s.number, s.date, s."customerId", c.name, pl.code, l.amount, COALESCE(p.paid, 0)
		FROM s JOIN l ON l."saleId" = s.id LEFT JOIN p ON p."saleId" = s.id
		JOIN "Customer" c ON c.id = s."customerId" JOIN "Plant" pl ON pl.id = s."plantId"
		WHERE l.amount > COALESCE(p.paid, 0)
		ORDER BY s.date ASC, s.number ASC`, q.args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := &receivablesOut{AsOf: asOfISO, Sales: []receivableSale{}}
	var open []openSale
	for rs.Next() {
		var r receivableSale
		var d time.Time
		if err := rs.Scan(&r.ID, &r.Number, &d, &r.CustomerID, &r.CustomerName, &r.PlantCode, &r.Amount, &r.Paid); err != nil {
			return nil, err
		}
		r.Outstanding = r.Amount - r.Paid
		r.Date = dates.ToISO(d)
		r.Days = daysBetween(d, asOf)
		out.Sales = append(out.Sales, r)
		open = append(open, openSale{r.CustomerID, r.CustomerName, d, r.Outstanding})
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	rows, totals := buildAgeing(open, asOf)
	out.Totals = totals
	out.Rows = make([]receivableRow, 0, len(rows))
	for _, r := range rows {
		rr := receivableRow{CustomerID: r.CustomerID, CustomerName: r.CustomerName, OpenSales: r.OpenSales, buckets: r.buckets}
		if r.OldestDate != nil {
			d := dates.ToISO(*r.OldestDate)
			rr.OldestDate = &d
		}
		out.Rows = append(out.Rows, rr)
	}
	return out, nil
}

// ───────────────────────────── access helpers ─────────────────────────────

// assertSaleAccess lets plant-scoped users see a sale when it belongs to their plant or one of its deliveries left from it.
func assertSaleAccess(ctx context.Context, q db.DBTX, u *auth.User, saleID, salePlantID string) error {
	scoped := auth.ScopedPlantID(u)
	if scoped == "" || scoped == salePlantID {
		return nil
	}
	var ok bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM "Delivery" WHERE "saleId"=$1 AND "plantId"=$2)`, saleID, scoped).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return httpx.Forbidden("error.plant_scope")
	}
	return nil
}

var _ = pgx.ErrNoRows
