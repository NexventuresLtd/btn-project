package stock

// STK-10 batch / lot traceability. Receipts (and transfers, issues, consumption, adjustments) may carry a lot
// reference. The lot report shows, per location and item, each lot's quantity in, the quantity that left tagged
// with the lot, and the share of untagged outflows (e.g. production consumption) attributed first-in-first-out.

import (
	"context"
	"sort"

	"btn-ops/internal/db"
)

// LotFlow is the movement summary of one lot of one item at one location ("" = untracked stock).
type LotFlow struct {
	Lot       string  `json:"lot"`
	FirstDate string  `json:"firstDate"`
	In        float64 `json:"in"`
	OutTagged float64 `json:"outTagged"` // outflows recorded against the lot (positive number)
}

// LotBalance is a lot with its FIFO-attributed untagged outflow and remaining quantity.
type LotBalance struct {
	LotFlow
	OutFIFO   float64 `json:"outFifo"`
	Remaining float64 `json:"remaining"`
}

// AllocateFIFO attributes untagged outflows (positive amount) to lots oldest first. Lots must be sorted by
// FirstDate (then Lot). Any excess beyond all lots stays on the last lot (negative remaining = data issue).
func AllocateFIFO(lots []LotFlow, untaggedOut float64) []LotBalance {
	out := make([]LotBalance, len(lots))
	left := toMilli(untaggedOut)
	for i, l := range lots {
		out[i] = LotBalance{LotFlow: l}
		free := toMilli(l.In) - toMilli(l.OutTagged)
		take := int64(0)
		if left > 0 && free > 0 {
			take = free
			if take > left {
				take = left
			}
		}
		left -= take
		out[i].OutFIFO = fromMilli(take)
	}
	if left > 0 && len(out) > 0 {
		out[len(out)-1].OutFIFO = Round3(out[len(out)-1].OutFIFO + fromMilli(left))
	}
	for i := range out {
		out[i].Remaining = Round3(out[i].In - out[i].OutTagged - out[i].OutFIFO)
	}
	return out
}

// LotGroup is the lot report of one item at one location.
type LotGroup struct {
	PlantID   string       `json:"plantId"`
	PlantCode string       `json:"plantCode"`
	ItemKind  ItemKind     `json:"itemKind"`
	ItemID    string       `json:"itemId"`
	Code      string       `json:"code"`
	Name      string       `json:"name"`
	Unit      string       `json:"unit"`
	Balance   float64      `json:"balance"`
	Lots      []LotBalance `json:"lots"`
}

// LotReport builds the lot report for plants (ids) and an optional item, for one kind. Two grouped queries.
func LotReport(ctx context.Context, q db.DBTX, plantIDs []string, kind ItemKind, itemID string) ([]LotGroup, error) {
	t, col := ledger(kind)
	itemTable := `"Material"`
	if kind == ItemProduct {
		itemTable = `"Product"`
	}
	// only items that have at least one lot-tagged movement at the location
	rows, err := q.Query(ctx, `WITH f AS (
			SELECT m."plantId" AS plant, m.`+col+` AS item, COALESCE(m."lotRef", '') AS lot, MIN(m.date) FILTER (WHERE m.quantity > 0) AS first_in, MIN(m.date) AS first_any,
				COALESCE(SUM(m.quantity) FILTER (WHERE m.quantity > 0), 0) AS qin, COALESCE(-SUM(m.quantity) FILTER (WHERE m.quantity < 0), 0) AS qout
			FROM `+t+` m WHERE m."plantId" = ANY($1::text[]) AND ($2 = '' OR m.`+col+` = $2)
				AND EXISTS (SELECT 1 FROM `+t+` x WHERE x."plantId" = m."plantId" AND x.`+col+` = m.`+col+` AND x."lotRef" IS NOT NULL)
			GROUP BY 1, 2, 3)
		SELECT f.plant, pl.code, f.item, i.code, i.name, i.unit, f.lot, to_char(COALESCE(f.first_in, f.first_any),'YYYY-MM-DD'), f.qin::text, f.qout::text
		FROM f JOIN "Plant" pl ON pl.id = f.plant JOIN `+itemTable+` i ON i.id = f.item
		ORDER BY pl.code, i."sortOrder", i.name, COALESCE(f.first_in, f.first_any), f.lot`, plantIDs, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type acc struct {
		g        LotGroup
		lots     []LotFlow
		untagged float64
	}
	var order []string
	groups := map[string]*acc{}
	for rows.Next() {
		var plant, pcode, item, code, name, unit, lot, first, qin, qout string
		if err := rows.Scan(&plant, &pcode, &item, &code, &name, &unit, &lot, &first, &qin, &qout); err != nil {
			return nil, err
		}
		k := plant + "|" + item
		a := groups[k]
		if a == nil {
			a = &acc{g: LotGroup{PlantID: plant, PlantCode: pcode, ItemKind: kind, ItemID: item, Code: code, Name: name, Unit: unit}}
			groups[k] = a
			order = append(order, k)
		}
		in, out := ParseQty(qin), ParseQty(qout)
		a.g.Balance = Round3(a.g.Balance + in - out)
		if lot == "" {
			// untracked stock: its inflow is a pseudo-lot, its outflows are attributed FIFO across all lots
			if in > 0 {
				a.lots = append(a.lots, LotFlow{Lot: "", FirstDate: first, In: in})
			}
			a.untagged += out
		} else {
			a.lots = append(a.lots, LotFlow{Lot: lot, FirstDate: first, In: in, OutTagged: out})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]LotGroup, 0, len(order))
	for _, k := range order {
		a := groups[k]
		sort.SliceStable(a.lots, func(i, j int) bool {
			if a.lots[i].FirstDate != a.lots[j].FirstDate {
				return a.lots[i].FirstDate < a.lots[j].FirstDate
			}
			return a.lots[i].Lot < a.lots[j].Lot
		})
		a.g.Lots = AllocateFIFO(a.lots, a.untagged)
		out = append(out, a.g)
	}
	return out, nil
}
