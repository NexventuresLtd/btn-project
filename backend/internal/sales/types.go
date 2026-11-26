package sales

import (
	"btn-ops/internal/dates"
	"crypto/rand"
	"encoding/json"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// jsTime marshals like a JavaScript Date (Prisma JSON): 2026-11-14T10:00:00.123Z.
type jsTime struct{ time.Time }

func (t jsTime) MarshalJSON() ([]byte, error) {
	return []byte(`"` + t.UTC().Format("2006-01-02T15:04:05.000Z") + `"`), nil
}

func jt(t time.Time) jsTime { return jsTime{t} }
func jtp(t *time.Time) *jsTime {
	if t == nil {
		return nil
	}
	return &jsTime{*t}
}

type ref struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

type person struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type productRef struct {
	ID           string  `json:"id"`
	Code         string  `json:"code"`
	Name         string  `json:"name"`
	Unit         string  `json:"unit"`
	Dimensions   *string `json:"dimensions"`
	DefaultPrice int64   `json:"defaultPrice"`
}

type customer struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	TIN         *string `json:"tin"`
	Phone       *string `json:"phone"`
	Email       *string `json:"email"`
	Address     *string `json:"address"`
	IsActive    bool    `json:"isActive"`
	CreatedAt   jsTime  `json:"createdAt"`
	ClientType  *string `json:"clientType"`
	Status      string  `json:"status"`
	Notes       *string `json:"notes"`
	PaymentTerm *string `json:"paymentTerm"`
}

type saleLineOut struct {
	ID              string     `json:"id"`
	ProductID       string     `json:"productId"`
	Product         productRef `json:"product"`
	Quantity        int64      `json:"quantity"`
	UnitPrice       int64      `json:"unitPrice"`
	Amount          int64      `json:"amount"`
	DefaultPrice    int64      `json:"defaultPrice"`
	MinPrice        int64      `json:"minPrice"`
	Discount        *float64   `json:"discount"`
	NeedsGmApproval bool       `json:"needsGmApproval"`
	Delivered       int64      `json:"delivered"`
	Remaining       int64      `json:"remaining"`
	Gross           int64      `json:"gross"`
	DiscountPercent *float64   `json:"discountPercent"`
	DiscountAmount  int64      `json:"discountAmount"`
	Fulfilment      *string    `json:"fulfilment"`
}

type chargeLineOut struct {
	ID          string  `json:"id"`
	Description *string `json:"description"`
	Amount      int64   `json:"amount"`
}

type deliveryLineOut struct {
	ID         string     `json:"id"`
	SaleLineID string     `json:"saleLineId"`
	ProductID  string     `json:"productId"`
	Product    productRef `json:"product"`
	Quantity   int64      `json:"quantity"`
}

type deliveryOut struct {
	ID          string            `json:"id"`
	DNNumber    string            `json:"dnNumber"`
	Date        string            `json:"date"`
	Plant       ref               `json:"plant"`
	Vehicle     *string           `json:"vehicle"`
	Driver      *string           `json:"driver"`
	Status      string            `json:"status"`
	DeliveredAt *jsTime           `json:"deliveredAt"`
	ReceivedBy  *string           `json:"receivedBy"`
	CreatedBy   *person           `json:"createdBy"`
	CreatedAt   jsTime            `json:"createdAt"`
	TotalQty    int64             `json:"totalQty"`
	Lines       []deliveryLineOut `json:"lines"`
	Destination *string           `json:"destination"`
	Transport   string            `json:"transportMode"`
	Note        *string           `json:"deliveryNote"`
	HasPhoto    bool              `json:"hasPhoto"`
	Offline     bool              `json:"capturedOffline"`
}

type paymentOut struct {
	ID          string  `json:"id"`
	Date        string  `json:"date"`
	Amount      int64   `json:"amount"`
	Method      string  `json:"method"`
	MethodLabel string  `json:"methodLabel"`
	Reference   *string `json:"reference"`
	ReceivedBy  *person `json:"receivedBy"`
	CreatedAt   jsTime  `json:"createdAt"`
	Kind        string  `json:"kind"`
	// the income record this payment is (docs/FINANCE_RECORDS_DESIGN.md §3: recording a payment on an order is recording an income)
	IncomeID     string `json:"incomeId"`
	IncomeNumber string `json:"incomeNumber"`
	IncomeStatus string `json:"incomeStatus"`
}

type returnOut struct {
	ID        string     `json:"id"`
	Date      string     `json:"date"`
	Plant     ref        `json:"plant"`
	ProductID string     `json:"productId"`
	Product   productRef `json:"product"`
	Quantity  int64      `json:"quantity"`
	Note      *string    `json:"note"`
	CreatedBy *person    `json:"createdBy"`
	CreatedAt jsTime     `json:"createdAt"`
}

type returnableOut struct {
	ProductID  string      `json:"productId"`
	Product    *productRef `json:"product"`
	Delivered  int64       `json:"delivered"`
	Returned   int64       `json:"returned"`
	Returnable int64       `json:"returnable"`
}

type saleTotals struct {
	Quantity  int64 `json:"quantity"`
	Amount    int64 `json:"amount"`
	Paid      int64 `json:"paid"`
	Balance   int64 `json:"balance"`
	Delivered int64 `json:"delivered"`
	Remaining int64 `json:"remaining"`
	Returned  int64 `json:"returned"`
	Gross     int64 `json:"gross"`
	Discount  int64 `json:"discount"`
	Charge    int64 `json:"charge"`
	Subtotal  int64 `json:"subtotal"` // net product lines
}

type saleDetail struct {
	ID                 string          `json:"id"`
	Number             string          `json:"number"`
	Status             string          `json:"status"`
	Date               string          `json:"date"`
	InvoiceType        string          `json:"invoiceType"`
	EbmInvoiceNo       *string         `json:"ebmInvoiceNo"`
	EbmReceiptNo       *string         `json:"ebmReceiptNo"`
	Notes              *string         `json:"notes"`
	Plant              ref             `json:"plant"`
	Customer           customer        `json:"customer"`
	CreatedBy          *person         `json:"createdBy"`
	CreatedAt          jsTime          `json:"createdAt"`
	ConfirmedBy        *person         `json:"confirmedBy"`
	ConfirmedAt        *jsTime         `json:"confirmedAt"`
	CancelledBy        *person         `json:"cancelledBy"`
	CancelledAt        *jsTime         `json:"cancelledAt"`
	CancelReason       *string         `json:"cancelReason"`
	Lines              []saleLineOut   `json:"lines"`
	Deliveries         []deliveryOut   `json:"deliveries"`
	Payments           []paymentOut    `json:"payments"`
	Returns            []returnOut     `json:"returns"`
	Returnable         []returnableOut `json:"returnable"`
	Totals             saleTotals      `json:"totals"`
	PaymentStatus      string          `json:"paymentStatus"`
	DeliveryStatus     string          `json:"deliveryStatus"`
	NeedsGmApproval    bool            `json:"needsGmApproval"`
	MaxDiscountPercent float64         `json:"maxDiscountPercent"`

	// lifecycle (ORD-04), terms (ORD-03/06), delivery (DEL-01/02), versions (ORD-10)
	LegacyStatus     string         `json:"legacyStatus"`
	NextStatuses     []string       `json:"nextStatuses"`
	PaymentTerm      *string        `json:"paymentTerm"`
	PaymentTermName  *string        `json:"paymentTermName"`
	DepositAmount    int64          `json:"depositAmount"`
	DepositStatus    string         `json:"depositStatus"`
	DueDate          *string        `json:"dueDate"`
	Overdue          bool           `json:"overdue"`
	TransportMode    string         `json:"transportMode"`
	DeliveryAddress  *string        `json:"deliveryAddress"`
	DeliverySite     *string        `json:"deliverySite"`
	RequestedDate    *string        `json:"requestedDate"`
	DeliveryZone     *string        `json:"deliveryZone"`
	DeliveryZoneName *string        `json:"deliveryZoneName"`
	DistanceKm       *float64       `json:"distanceKm"`
	DeliveryCharge   int64          `json:"deliveryCharge"`
	ChargeLine       *chargeLineOut `json:"chargeLine"`
	ValidUntil       *string        `json:"validUntil"`
	QuotedBy         *person        `json:"quotedBy"`
	QuotedAt         *jsTime        `json:"quotedAt"`
	ReadyAt          *jsTime        `json:"readyAt"`
	ClosedBy         *person        `json:"closedBy"`
	ClosedAt         *jsTime        `json:"closedAt"`
	Version          int            `json:"version"`
	CapturedOffline  bool           `json:"capturedOffline"`
	Locked           bool           `json:"locked"`
	PendingApproval  *approvalOut   `json:"pendingApproval"`
	Approvals        []*approvalOut `json:"approvals"`
	Amendments       []amendmentOut `json:"amendments"`
	Versions         []versionOut   `json:"versions"`
	History          []historyOut   `json:"history"`
	Communications   []commOut      `json:"communications"`
	ServerVersion    int64          `json:"serverVersion"`
}

type historyOut struct {
	From *string `json:"from"`
	To   string  `json:"to"`
	By   *person `json:"by"`
	At   jsTime  `json:"at"`
	Note *string `json:"note"`
}

type stockWarning struct {
	ProductID   string `json:"productId"`
	ProductCode string `json:"productCode"`
	ProductName string `json:"productName"`
	Required    int64  `json:"required"`
	Available   int64  `json:"available"`
	Message     string `json:"message"`
}

// newLineID makes a cuid-like id that sorts in creation order (timestamp prefix + counter),
// so ORDER BY id keeps line order like Prisma cuids did.
func newLineID(seq int) string {
	n := big.NewInt(36)
	var b strings.Builder
	b.WriteByte('c')
	ts := strconv.FormatInt(time.Now().UnixMilli(), 36)
	for len(ts) < 9 {
		ts = "0" + ts
	}
	b.WriteString(ts)
	c := strconv.FormatInt(int64(seq), 36)
	for len(c) < 3 {
		c = "0" + c
	}
	b.WriteString(c)
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	for i := 0; i < 12; i++ {
		r, _ := rand.Int(rand.Reader, n)
		b.WriteByte(alphabet[r.Int64()])
	}
	return b.String()
}

func jsonBytes(v any) ([]byte, error) { return json.Marshal(v) }

func todayISO() string { return dates.TodayISO() }
