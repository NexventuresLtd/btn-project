package sales

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"btn-ops/internal/dates"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
)

// Validation helpers reproducing the zod schemas of the Node module (schemas.ts), with i18n messages.

type in struct {
	v     *httpx.V
	lang  i18n.Lang
	today string
}

func newIn(v *httpx.V, lang i18n.Lang) in { return in{v: v, lang: lang, today: dates.TodayISO()} }

func (x in) t(key string, args ...any) string { return i18n.T(x.lang, key, args...) }

func (x in) atMost(n int) string { return x.t("sales.v.at_most", "n", n) }

// id: z.string().trim().min(1, "Required").max(40)
func (x in) id(key string) string {
	return x.v.Str(key).Trim().Min(1, "v.required").Max(40, x.atMost(40)).Get()
}

// optID: id().optional() (absent -> "").
func (x in) optID(key string) string {
	if !x.v.Has(key) {
		return ""
	}
	return x.id(key)
}

// pastDate: YYYY-MM-DD, not in the future (Kigali calendar).
func (x in) pastDate(key string) string {
	if s, ok := x.v.Raw(key).(string); !ok {
		_ = s
		x.v.Fail(key, "v.required")
		return ""
	}
	s := x.v.Str(key).Trim().Date("sales.v.valid_date")
	if s.Present() && s.Get() > x.today {
		x.v.Fail(key, "sales.v.date_future")
		return ""
	}
	return s.Get()
}

// optText: optional nullable trimmed text, "" -> nil.
func (x in) optText(key string, max int) *string {
	return x.v.NullStr(key).Trim().Max(max, x.atMost(max)).NilIfEmpty()
}

// reqText: required trimmed text with a custom "required" message.
func (x in) reqText(key string, max int, msg string) string {
	if _, ok := x.v.Raw(key).(string); !ok {
		x.v.Fail(key, msg)
		return ""
	}
	return x.v.Str(key).Trim().Min(1, msg).Max(max, x.atMost(max)).Get()
}

func numeric(raw any) bool {
	switch t := raw.(type) {
	case json.Number, float64:
		return true
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return true
		}
		_, err := strconv.ParseFloat(s, 64)
		return err == nil
	}
	return false
}

// qty: z.coerce.number().int("Whole units only").positive().max(10_000_000)
func (x in) qty(v *httpx.V, key string) int64 {
	if !numeric(v.Raw(key)) {
		v.Fail(key, "sales.v.enter_qty")
		return 0
	}
	return v.Num(key).Coerce().Whole("sales.v.whole_units").Gt(0, "sales.v.gt_zero").Max(10_000_000, "sales.v.too_large").I()
}

// money: z.coerce.number().int("Whole francs only") with caller-specific bounds.
func (x in) money(v *httpx.V, key string) *httpx.N {
	if !numeric(v.Raw(key)) {
		v.Fail(key, "sales.v.enter_amount")
		return v.Num(key)
	}
	return v.Num(key).Coerce().Whole("v.whole_francs")
}

type lineIn struct {
	SaleLineID      string // amendments: keep an existing line ("" = new line)
	ProductID       string
	Quantity        int64
	UnitPrice       int64
	DiscountPercent *float64
	DiscountAmount  int64
}

// calc is the line money (gross, discount, net).
func (l lineIn) calc() lineCalc {
	c, _ := lineAmounts(l.Quantity, l.UnitPrice, l.DiscountPercent, l.DiscountAmount)
	return c
}

// terms are the commercial and delivery terms of an order (ORD-03, DEL-01). Set* = the key was supplied.
type terms struct {
	PaymentTerm                     *string
	TransportMode                   string
	DeliveryAddress, DeliverySite   *string
	RequestedDate, ValidUntil       *string
	DeliveryZone                    *string
	DistanceKm                      *float64
	SetPaymentTerm, SetTransport    bool
	SetAddress, SetSite, SetReqDate bool
	SetZone, SetKm, SetValid        bool
}

func (t terms) any() bool {
	return t.SetPaymentTerm || t.SetTransport || t.SetAddress || t.SetSite || t.SetReqDate || t.SetZone || t.SetKm || t.SetValid
}

type saleIn struct {
	PlantID, CustomerID, Date         string
	EbmInvoiceNo, EbmReceiptNo, Notes *string
	// set* tell PATCH whether the key was supplied (null/"" clears the column)
	SetInvoice, SetReceipt, SetNotes bool
	Lines                            []lineIn
	HasLines                         bool
	Confirm                          bool
	Quote                            bool
	Terms                            terms
}

// commercialChange: the PATCH touches something ORD-10 locks once the order is confirmed.
func (s saleIn) commercialChange() bool {
	return s.PlantID != "" || s.CustomerID != "" || s.Date != "" || s.HasLines || s.Terms.any()
}

// parseTerms validates the delivery requirements and payment terms.
func (x in) parseTerms(partial bool) terms {
	v := x.v
	var t terms
	t.SetPaymentTerm = v.Has("paymentTerm")
	t.PaymentTerm = x.optCode("paymentTerm")
	t.SetTransport = v.Has("transportMode")
	if t.SetTransport || !partial {
		t.TransportMode = v.OptStr("transportMode").Default(TransportClient).OneOf([]string{TransportClient, TransportBTN}, "sales.v.transport").Get()
	}
	t.DeliveryAddress, t.SetAddress = x.optText("deliveryAddress", 300), v.Has("deliveryAddress")
	t.DeliverySite, t.SetSite = x.optText("deliverySite", 160), v.Has("deliverySite")
	t.SetReqDate = v.Has("requestedDate")
	t.RequestedDate = x.optISODate("requestedDate")
	t.SetValid = v.Has("validUntil")
	t.ValidUntil = x.optISODate("validUntil")
	t.SetZone = v.Has("deliveryZone")
	t.DeliveryZone = x.optCode("deliveryZone")
	t.SetKm = v.Has("distanceKm")
	if t.SetKm && v.Raw("distanceKm") != nil && v.Raw("distanceKm") != "" {
		if !numeric(v.Raw("distanceKm")) {
			v.Fail("distanceKm", "sales.v.enter_distance")
		} else {
			f := v.Num("distanceKm").Coerce().Min(0, "sales.v.not_negative").Max(5000, "sales.v.too_large").F()
			f = float64(int64(f*10+0.5)) / 10
			t.DistanceKm = &f
		}
	}
	return t
}

// optCode: optional refdata code (upper-cased), null/"" → nil.
func (x in) optCode(key string) *string {
	s := x.v.NullStr(key).Trim().Upper().Max(40, x.atMost(40)).NilIfEmpty()
	return s
}

// optISODate: optional YYYY-MM-DD (future allowed), null/"" → nil.
func (x in) optISODate(key string) *string {
	raw := x.v.Raw(key)
	if raw == nil {
		return nil
	}
	if s, ok := raw.(string); ok && strings.TrimSpace(s) == "" {
		return nil
	}
	s := x.v.Str(key).Trim().Date("sales.v.valid_date").Get()
	if s == "" {
		return nil
	}
	return &s
}

// parseLine validates one order line (product, qty, unit price, optional percent OR amount discount).
func (x in) parseLine(l *httpx.V, withLineID bool) lineIn {
	li := lineIn{ProductID: x.id2(l, "productId")}
	if withLineID && l.Has("saleLineId") && l.Raw("saleLineId") != nil && l.Raw("saleLineId") != "" {
		li.SaleLineID = x.id2(l, "saleLineId")
	}
	li.Quantity = x.qty(l, "quantity")
	li.UnitPrice = x.money(l, "unitPrice").Min(0, "sales.v.not_negative").Max(100_000_000, "sales.v.too_large").I()
	hasPct := l.Has("discountPercent") && l.Raw("discountPercent") != nil && l.Raw("discountPercent") != ""
	hasAmt := l.Has("discountAmount") && l.Raw("discountAmount") != nil && l.Raw("discountAmount") != ""
	if hasPct && hasAmt {
		l.Fail("discountAmount", "sales.v.one_discount")
	}
	if hasPct {
		if !numeric(l.Raw("discountPercent")) {
			l.Fail("discountPercent", "sales.v.discount_pct")
		} else {
			p := l.Num("discountPercent").Coerce().Min(0, "sales.v.discount_pct").Max(100, "sales.v.discount_pct").F()
			p = float64(int64(p*1000+0.5)) / 1000
			if p > 0 {
				li.DiscountPercent = &p
			}
		}
	} else if hasAmt {
		li.DiscountAmount = x.money(l, "discountAmount").Min(0, "sales.v.not_negative").Max(1_000_000_000, "sales.v.too_large").I()
		if li.DiscountAmount > li.Quantity*li.UnitPrice && li.Quantity > 0 {
			l.Fail("discountAmount", "sales.v.discount_too_big")
		}
	}
	return li
}

// parseLines validates the lines array (1..50).
func (x in) parseLines(withLineID bool) []lineIn {
	v := x.v
	arr := v.Arr("lines", true)
	if arr != nil && len(arr) == 0 {
		v.Fail("lines", "sales.v.add_product")
	} else if len(arr) > 50 {
		v.Fail("lines", "sales.v.max_lines")
	}
	out := make([]lineIn, 0, len(arr))
	for _, l := range arr {
		out = append(out, x.parseLine(l, withLineID))
	}
	return out
}

// parseSale validates createSaleSchema (partial=false) or updateSaleSchema (partial=true).
func (x in) parseSale(partial bool) saleIn {
	v := x.v
	var s saleIn
	if !partial || v.Has("plantId") {
		s.PlantID = x.id("plantId")
	}
	if !partial || v.Has("customerId") {
		s.CustomerID = x.id("customerId")
	}
	if !partial || v.Has("date") {
		s.Date = x.pastDate("date")
	}
	s.EbmInvoiceNo, s.SetInvoice = x.optText("ebmInvoiceNo", 60), v.Has("ebmInvoiceNo")
	s.EbmReceiptNo, s.SetReceipt = x.optText("ebmReceiptNo", 60), v.Has("ebmReceiptNo")
	s.Notes, s.SetNotes = x.optText("notes", 1000), v.Has("notes")
	s.Terms = x.parseTerms(partial)
	if !partial || v.Has("lines") {
		s.HasLines = true
		s.Lines = x.parseLines(false)
	}
	if !partial {
		s.Confirm = v.OptBool("confirm").Default(false).Get()
		s.Quote = v.OptBool("quote").Default(false).Get()
	}
	if s.Date != "" && s.Terms.RequestedDate != nil && *s.Terms.RequestedDate < s.Date {
		v.Fail("requestedDate", "sales.v.requested_before_order")
	}
	if s.Date != "" && s.Terms.ValidUntil != nil && *s.Terms.ValidUntil < s.Date {
		v.Fail("validUntil", "sales.v.valid_before_order")
	}
	return s
}

func (x in) id2(v *httpx.V, key string) string {
	return v.Str(key).Trim().Min(1, "v.required").Max(40, x.atMost(40)).Get()
}

type deliveryIn struct {
	DNNumber, Date, PlantID      string
	Vehicle, Driver, Destination *string
	Lines                        []deliveryReq
}

func (x in) parseDelivery() deliveryIn {
	v := x.v
	var d deliveryIn
	d.DNNumber = strings.ToUpper(x.reqText("dnNumber", 40, x.t("sales.v.dn_required")))
	d.Date = x.pastDate("date")
	d.PlantID = x.optID("plantId")
	d.Vehicle = x.optText("vehicle", 40)
	d.Driver = x.optText("driver", 80)
	d.Destination = x.optText("destination", 300)
	arr := v.Arr("lines", true)
	if arr != nil && len(arr) == 0 {
		v.Fail("lines", "sales.v.add_line")
	} else if len(arr) > 50 {
		v.Fail("lines", "sales.v.max_lines")
	}
	for _, l := range arr {
		d.Lines = append(d.Lines, deliveryReq{SaleLineID: x.id2(l, "saleLineId"), Quantity: x.qty(l, "quantity")})
	}
	return d
}

type returnIn struct {
	Date, PlantID, Reason string
	Lines                 []productQty
}

func (x in) parseReturn() returnIn {
	v := x.v
	var r returnIn
	r.Date = x.pastDate("date")
	r.PlantID = x.id("plantId")
	r.Reason = x.reqText("reason", 500, x.t("sales.v.give_return_reason"))
	arr := v.Arr("lines", true)
	if arr != nil && len(arr) == 0 {
		v.Fail("lines", "sales.v.add_product")
	} else if len(arr) > 50 {
		v.Fail("lines", "sales.v.max_lines")
	}
	for _, l := range arr {
		r.Lines = append(r.Lines, productQty{ProductID: x.id2(l, "productId"), Quantity: x.qty(l, "quantity")})
	}
	return r
}

type paymentIn struct {
	Date      string
	Amount    int64
	Method    string
	Kind      string // "" = classify automatically
	Reference *string
}

func (x in) parsePayment() paymentIn {
	var p paymentIn
	p.Date = x.pastDate("date")
	p.Amount = x.money(x.v, "amount").Gt(0, "sales.v.gt_zero").Max(10_000_000_000, "sales.v.too_large").I()
	method, ok := x.v.Raw("method").(string)
	valid := false
	for _, m := range paymentMethods {
		if ok && m == method {
			valid = true
		}
	}
	if !valid {
		x.v.Fail("method", "sales.v.pick_method")
	}
	p.Method = method
	p.Reference = x.optText("reference", 80)
	p.Kind = x.v.OptStr("kind").OneOf([]string{"", PayKindDeposit, PayKindPart, PayKindBalance}, "sales.v.pay_kind").Get()
	return p
}

// deliveredIn is the "mark delivered" body.
type deliveredIn struct {
	ReceivedBy  string
	DeliveredAt *time.Time
	Note        *string
}

func (x in) parseDelivered() deliveredIn {
	var d deliveredIn
	d.ReceivedBy = x.reqText("receivedBy", 120, x.t("sales.v.received_by"))
	d.Note = x.optText("note", 500)
	if raw, ok := x.v.Raw("deliveredAt").(string); ok {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			x.v.Fail("deliveredAt", "sales.v.valid_datetime")
		} else {
			u := t.UTC().Truncate(time.Millisecond)
			d.DeliveredAt = &u
		}
	} else if x.v.Has("deliveredAt") && x.v.Raw("deliveredAt") != nil {
		x.v.Fail("deliveredAt", "sales.v.valid_datetime")
	}
	return d
}

// optDate validates an optional query date ("Invalid date").
func optDate(q *httpx.V, key string) string {
	s := q.OptStr(key)
	if s.Present() && s.Get() != "" && !dates.IsValidISO(s.Get()) {
		q.Fail(key, "sales.v.invalid_date")
		return ""
	}
	return s.Get()
}
