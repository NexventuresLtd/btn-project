// Package importer is the Go port of the legacy Excel importer (scripts/import-excel.ts): it loads
// BTN's spreadsheets (block reports, petty cash, wages) into the ledgers, with date repair,
// reconciliation tables and an anomaly report that masks personal data.
package importer

// Anomaly is one finding of the report.
type Anomaly struct {
	File, Sheet string
	Row         int // 0 = none
	Severity    string
	Kind        string
	Message     string
	Action      string // what the importer did about it
}

// AddFn records an anomaly (the file name is filled in by the caller's closure).
type AddFn func(a Anomaly)

type StockMovePlan struct {
	PlantCode, ProductCode, Date, Type string
	Quantity                           int
	Note                               string
}

type MaterialMovePlan struct {
	PlantCode, MaterialCode, Date, Type string
	Quantity                            float64
	CounterpartPlantCode                string
	TransferGroup                       string
	Note                                string
	File                                string // file whose transaction writes this movement
}

// PlantQty is a quantity sent to a plant.
type PlantQty struct {
	Plant string
	Qty   float64
}

// RawMaterialRow is one MATERIAL sheet row after date repair, before transfers are matched.
type RawMaterialRow struct {
	File      string
	Row       int
	PlantCode string
	Material  string
	Date      string
	Opening   float64
	Entered   float64
	GoesTo    []PlantQty // destination plants in sheet column order
	Used      float64
	Closing   float64
}

type ClosingCheck struct {
	PlantCode, ItemCode, LastDate string
	SheetClosing                  float64
}

type SaleLinePlan struct {
	Row         int
	ProductCode string
	Quantity    int
	UnitPrice   int
	Amount      int
	DN          string
}

type DeliveryPlan struct {
	DNNumber    string
	LineIndexes []int
}

type PaymentPlan struct {
	Method    string
	Amount    int
	Reference string
}

type SalePlan struct {
	Number, PlantCode, CustomerName, Date, InvoiceType string
	EbmInvoiceNo, EbmReceiptNo                         string // "" = null
	Notes                                              string
	Lines                                              []SaleLinePlan
	Deliveries                                         []DeliveryPlan
	Payment                                            *PaymentPlan
}

type PettyCashPlan struct {
	Row                                       int
	Number, Date, Type, Category, Description string
	PlantCode, PayeeName, MomoNumber          string // "" = null
	Amount, MomoCharge                        int
	InvoiceRef                                string // "" = null
	SheetBalance                              *float64
}

type WorkerPlan struct {
	NationalID, FirstName, LastName string
	MomoNumber, MomoName            string // "" = null
	JobTitle, PlantCode             string
	DailyWage                       int
}

type PayrollLinePlan struct {
	Row                                   int
	NationalID                            string
	DailyWage                             int
	DaysWorked                            float64
	GrossAmount, FoodDeduction, NetAmount int
}

type Totals struct{ Gross, Food, Net float64 }

type PayrollPlan struct {
	Number, PlantCode, StartDate, EndDate string
	Lines                                 []PayrollLinePlan
	SheetTotals                           *Totals
}

type FilePlan struct {
	File, Kind, PlantCode string
	Stock                 []StockMovePlan
	Material              []MaterialMovePlan
	Sales                 []SalePlan
	PettyCash             []PettyCashPlan
	Workers               []WorkerPlan
	Payroll               []PayrollPlan
	StockClosings         []ClosingCheck
	MaterialClosings      []ClosingCheck
	RowsRead              []KV // ordered (insertion) map
}

// KV is an ordered key/count pair (the report prints rowsRead in insertion order).
type KV struct {
	K string
	V int
}

func (p *FilePlan) SetRows(k string, v int) {
	for i := range p.RowsRead {
		if p.RowsRead[i].K == k {
			p.RowsRead[i].V = v
			return
		}
	}
	p.RowsRead = append(p.RowsRead, KV{k, v})
}
