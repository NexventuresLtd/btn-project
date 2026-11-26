package sales_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"strings"
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/sales"
	"btn-ops/internal/testutil"
)

// ── ORD-01 / ORD-02 / ORD-07 client database ─────────────────────────────────────────────────────

func TestClientDatabaseDuplicatesAndHistory(t *testing.T) {
	f := setup(t)
	e := f.e
	body := M{"name": "  Kagame   builders ltd ", "clientType": "COMPANY", "tin": "102345678", "phone": "0788 123 456", "email": "Info@Kagame.RW",
		"address": "KK 15 Ave, Kigali", "notes": "TEST key account", "paymentTerm": "NET30",
		"contacts": []M{{"name": "Jean Uwimana", "role": "Site engineer", "phone": "0788000111", "isPrimary": true}, {"name": "Aline", "email": "aline@kagame.rw"}}}
	// permission: a supervisor cannot manage clients
	if r := e.Do("POST", "/api/sales/customers", body, f.disp1); r.StatusCode != 403 {
		t.Fatalf("supervisor create client: %d", r.StatusCode)
	}
	// validation
	r := e.Do("POST", "/api/sales/customers", M{"name": "x", "clientType": "ALIEN", "email": "nope", "status": "GONE", "contacts": []M{{"name": ""}}}, f.sm)
	_, _, fields := e.ErrorOf(r)
	for _, k := range []string{"name", "email", "status", "contacts.0.name"} {
		if r.StatusCode != 400 || fields[k] == "" {
			t.Errorf("validation %s: %d %v", k, r.StatusCode, fields)
		}
	}
	if r := e.Do("POST", "/api/sales/customers", M{"name": "OK NAME", "clientType": "ALIEN"}, f.sm); r.StatusCode != 400 {
		t.Errorf("unknown client type: %d", r.StatusCode)
	}
	p := status(t, e, "POST", "/api/sales/customers", body, f.sm, 201)
	cu := p["customer"].(M)
	cid := cu["id"].(string)
	if cu["name"] != "KAGAME BUILDERS LTD" || cu["clientType"] != "COMPANY" || cu["status"] != "ACTIVE" || cu["email"] != "info@kagame.rw" ||
		len(p["contacts"].([]any)) != 2 || p["clientTypeName"] != "Company" || p["paymentTermName"] != "30 days" {
		t.Fatalf("created: %v", p)
	}
	// ORD-02: likely duplicates (similar name, same phone, same TIN) → 409 with candidates, user may proceed
	for _, probe := range []M{{"name": "Kagame Builders"}, {"name": "Totally other", "phone": "+250788123456"}, {"name": "Another", "tin": "102-345-678"},
		{"name": "Third", "contacts": []M{{"name": "x", "phone": "250 788 000 111"}}}} {
		r := e.Do("POST", "/api/sales/customers", probe, f.sm)
		var out struct {
			Error struct {
				Code       string `json:"code"`
				Candidates []struct {
					ID    string   `json:"id"`
					Score int      `json:"score"`
					Match []string `json:"match"`
				} `json:"candidates"`
			} `json:"error"`
		}
		e.Decode(r, &out)
		if r.StatusCode != 409 || out.Error.Code != "possible_duplicate" || len(out.Error.Candidates) != 1 || out.Error.Candidates[0].ID != cid {
			t.Errorf("duplicate %v: %d %+v", probe, r.StatusCode, out)
		}
	}
	dc := status(t, e, "POST", "/api/sales/customers/duplicates", M{"name": "KAGAME BUILDER"}, f.sm, 200)
	if len(dc["candidates"].([]any)) != 1 {
		t.Errorf("live check: %v", dc)
	}
	if dc := status(t, e, "POST", "/api/sales/customers/duplicates", M{"name": "KAGAME BUILDERS", "excludeId": cid}, f.sm, 200); len(dc["candidates"].([]any)) != 0 {
		t.Errorf("exclude self: %v", dc)
	}
	ack := status(t, e, "POST", "/api/sales/customers", M{"name": "Kagame Builders Gisenyi", "acknowledgeDuplicates": true}, f.sm, 201)
	if ack["customer"].(M)["name"] != "KAGAME BUILDERS GISENYI" {
		t.Fatal("acknowledged duplicate is saved")
	}
	// edit: blocking needs a reason; blocked clients cannot order
	if r := e.Do("PATCH", "/api/sales/customers/"+cid, M{"status": "BLOCKED"}, f.sm); r.StatusCode != 400 {
		t.Errorf("block without reason: %d", r.StatusCode)
	}
	if r := e.Do("PATCH", "/api/sales/customers/"+cid, M{"status": "BLOCKED", "blockedReason": "TEST unpaid invoices", "contacts": []M{{"name": "Jean"}}}, f.sm); r.StatusCode != 409 {
		t.Errorf("edit touching identity re-checks duplicates: %d", r.StatusCode)
	}
	up := status(t, e, "PATCH", "/api/sales/customers/"+cid, M{"status": "BLOCKED", "blockedReason": "TEST unpaid invoices", "contacts": []M{{"name": "Jean"}}, "acknowledgeDuplicates": true}, f.sm, 200)
	if up["customer"].(M)["status"] != "BLOCKED" || up["customer"].(M)["isActive"] != false || len(up["contacts"].([]any)) != 1 {
		t.Errorf("blocked: %v", up)
	}
	r = e.Do("POST", "/api/sales", M{"plantId": f.plant1, "customerId": cid, "date": f.today, "lines": []M{line(f.hb20, 1, 1500)}}, f.sm)
	if _, _, fields := e.ErrorOf(r); r.StatusCode != 400 || !strings.Contains(fields["customerId"], "blocked") {
		t.Errorf("blocked client order: %d %v", r.StatusCode, fields)
	}
	status(t, e, "PATCH", "/api/sales/customers/"+cid, M{"status": "ACTIVE", "blockedReason": nil}, f.sm, 200)
	if r := e.Do("PATCH", "/api/sales/customers/nope", M{"notes": "x"}, f.sm); r.StatusCode != 404 {
		t.Errorf("patch 404: %d", r.StatusCode)
	}
	// list filters
	if ls := status(t, e, "GET", "/api/sales/customers?clientType=COMPANY&q=jean", nil, f.sm, 200); num(ls["total"]) != 1 {
		t.Errorf("filter by type + contact search: %v", ls["total"])
	}
	if ls := status(t, e, "GET", "/api/sales/customers?status=BLOCKED", nil, f.sm, 200); num(ls["total"]) != 0 {
		t.Errorf("status filter: %v", ls["total"])
	}
	// ORD-07: communication log + searchable order history
	s1 := f.create(f.sm, M{"plantId": f.plant1, "customerId": cid, "date": f.today, "lines": []M{line(f.hb20, 10, 1500)}, "notes": "TEST first"})
	f.create(f.sm, M{"plantId": f.plant1, "customerId": cid, "date": f.today, "lines": []M{line(f.hb15, 5, 1300)}})
	if r := e.Do("POST", "/api/sales/customers/"+cid+"/communications", M{"kind": "SMS", "subject": ""}, f.sm); r.StatusCode != 400 {
		t.Errorf("comm validation: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/sales/customers/"+cid+"/communications", M{"kind": "CALL", "subject": "x", "saleId": f.create(f.sm, f.saleBody(false, line(f.hb20, 1, 1500)))["id"]}, f.sm); r.StatusCode != 400 {
		t.Errorf("comm with another client's order: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/sales/customers/"+cid+"/communications", M{"kind": "CALL", "subject": "x"}, f.op); r.StatusCode != 403 {
		t.Errorf("operator comm: %d", r.StatusCode)
	}
	cm := status(t, e, "POST", "/api/sales/customers/"+cid+"/communications", M{"kind": "CALL", "subject": "TEST price follow-up", "body": "Asked for 5% off",
		"contactName": "Jean", "saleId": s1["id"], "occurredAt": "2026-01-05T09:30:00Z"}, f.sm, 201)["communication"].(M)
	if cm["kind"] != "CALL" || cm["by"].(M)["name"] != f.sm.Name || cm["saleNumber"] != s1["number"] {
		t.Errorf("comm: %v", cm)
	}
	status(t, e, "POST", "/api/sales/customers/"+cid+"/communications", M{"kind": "MEETING", "subject": "TEST site visit"}, f.sm, 201)
	cl := status(t, e, "GET", "/api/sales/customers/"+cid+"/communications?q=price", nil, f.sm, 200)
	if len(cl["items"].([]any)) != 1 {
		t.Errorf("comm search: %v", cl)
	}
	if cl := status(t, e, "GET", "/api/sales/customers/"+cid+"/communications?kind=MEETING", nil, f.sm, 200); len(cl["items"].([]any)) != 1 {
		t.Errorf("comm kind filter: %v", cl)
	}
	oh := status(t, e, "GET", "/api/sales/customers/"+cid+"/orders?q="+s1["number"].(string), nil, f.sm, 200)
	if num(oh["total"]) != 1 {
		t.Errorf("order history search: %v", oh["total"])
	}
	if oh := status(t, e, "GET", "/api/sales/customers/"+cid+"/orders", nil, f.sm, 200); num(oh["total"]) != 2 {
		t.Errorf("order history: %v", oh["total"])
	}
	prof := status(t, e, "GET", "/api/sales/customers/"+cid+"/profile", nil, f.sm, 200)
	if num(prof["summary"].(M)["orders"]) != 2 || num(prof["summary"].(M)["communications"]) != 2 {
		t.Errorf("profile summary: %v", prof["summary"])
	}
	status(t, e, "GET", "/api/sales/customers/"+cid+"/profile", nil, f.disp1, 200) // SRS §8: Stock reads clients & orders
	if r := e.Do("GET", "/api/sales/customers/"+cid+"/profile", nil, f.op); r.StatusCode != 403 {
		t.Errorf("operator profile: %d", r.StatusCode)
	}
	if f.count(`SELECT count(*) FROM "AuditLog" WHERE action IN ('sales.customer.create','sales.customer.update','sales.customer.communication')`) < 6 {
		t.Error("client audit rows")
	}
}

// ── ORD-03/04/05/06, DEL-01…05 full lifecycle ───────────────────────────────────────────────────

func pngBytes(t *testing.T) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 640, 480))
	for i := 0; i < 640; i++ {
		img.Set(i, i%480, color.RGBA{10, 120, 200, 255})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func multipartBody(t *testing.T, fields map[string]string, file []byte, fileName string) ([]byte, string) {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	if file != nil {
		fw, err := w.CreateFormFile("photo", fileName)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write(file)
	}
	_ = w.Close()
	return b.Bytes(), w.FormDataContentType()
}

func TestOrderLifecycleChargesDeliveryAndPayments(t *testing.T) {
	f := setup(t)
	e := f.e
	pm := e.User(auth.RoleProductionManager)
	// DEL-02 charge rules: masterdata.manage only
	if r := e.Do("POST", "/api/sales/delivery-charges", M{"name": "x", "method": "FIXED", "amount": 1}, f.sm); r.StatusCode != 403 {
		t.Fatalf("sm creates rule: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/sales/delivery-charges", M{"name": "Zone", "method": "ZONE", "amount": 1}, f.adm); r.StatusCode != 400 {
		t.Fatalf("zone rule without zone: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/sales/delivery-charges", M{"name": "Band", "method": "DISTANCE", "minKm": 20, "maxKm": 10, "amount": 1}, f.adm); r.StatusCode != 400 {
		t.Fatalf("decreasing band: %d", r.StatusCode)
	}
	fixed := status(t, e, "POST", "/api/sales/delivery-charges", M{"name": "Standard delivery", "method": "FIXED", "amount": 30000}, f.adm, 201)["rule"].(M)
	status(t, e, "POST", "/api/sales/delivery-charges", M{"name": "Kigali", "method": "ZONE", "zone": "KIGALI", "amount": 20000}, f.adm, 201)
	band := status(t, e, "POST", "/api/sales/delivery-charges", M{"name": "Far", "method": "DISTANCE", "minKm": 50, "amount": 90000}, f.adm, 201)["rule"].(M)
	status(t, e, "PATCH", "/api/sales/delivery-charges/"+band["id"].(string), M{"amount": 95000}, f.adm, 200)
	if q := status(t, e, "GET", "/api/sales/delivery-charges/quote?zone=OUTSIDE_KIGALI&km=60", nil, f.sm, 200); num(q["amount"]) != 95000 {
		t.Errorf("quote: %v", q)
	}
	if ls := status(t, e, "GET", "/api/sales/delivery-charges", nil, f.sm, 200); len(ls["items"].([]any)) != 3 || len(ls["zones"].([]any)) < 2 {
		t.Errorf("rules list: %v", ls)
	}
	// ORD-03: lines with % and amount discounts, delivery requirements, payment terms; DEL-01 BTN transport → charge line
	r := e.Do("POST", "/api/sales", M{"plantId": f.plant1, "customerId": f.cust, "date": f.today, "transportMode": "TRUCK", "requestedDate": "2000-01-01",
		"lines": []M{{"productId": f.hb20, "quantity": 10, "unitPrice": 100, "discountPercent": 5, "discountAmount": 3}}}, f.sm)
	if _, _, fields := e.ErrorOf(r); r.StatusCode != 400 || fields["transportMode"] == "" || fields["requestedDate"] == "" || fields["lines.0.discountAmount"] == "" {
		t.Fatalf("order validation: %d %v", r.StatusCode, fields)
	}
	body := M{"plantId": f.plant1, "customerId": f.cust, "date": f.today, "transportMode": "BTN", "deliveryZone": "KIGALI", "deliverySite": "Kicukiro site",
		"deliveryAddress": "KK 500 St", "requestedDate": f.today, "paymentTerm": "DEPOSIT_BALANCE", "validUntil": f.today,
		"lines": []M{{"productId": f.hb20, "quantity": 100, "unitPrice": 1500, "discountPercent": 5}, {"productId": f.hb15, "quantity": 50, "unitPrice": 1300, "discountAmount": 5000}}}
	s := f.create(f.sm, body)
	id := s["id"].(string)
	tot := s["totals"].(M)
	// 150000 − 7500 + 65000 − 5000 + 20000 (zone charge)
	if num(tot["amount"]) != 222500 || num(tot["discount"]) != 12500 || num(tot["charge"]) != 20000 || s["chargeLine"] == nil || s["transportMode"] != "BTN" ||
		s["status"] != "DRAFT" || s["paymentTermName"] != "Deposit + balance on delivery" || num(s["depositAmount"]) != 111250 {
		t.Fatalf("order: %v", tot)
	}
	l0 := s["lines"].([]any)[0].(M)
	if num(l0["gross"]) != 150000 || num(l0["discountAmount"]) != 7500 || num(l0["amount"]) != 142500 {
		t.Errorf("line: %v", l0)
	}
	// client's own truck → no charge
	cl := status(t, e, "PATCH", "/api/sales/"+id, M{"transportMode": "CLIENT"}, f.sm, 200)["sale"].(M)
	if cl["chargeLine"] != nil || num(cl["totals"].(M)["amount"]) != 202500 {
		t.Errorf("client truck: %v", cl["totals"])
	}
	status(t, e, "PATCH", "/api/sales/"+id, M{"transportMode": "BTN"}, f.sm, 200)
	// quotation (Draft → Quoted), PDF
	q := status(t, e, "POST", "/api/sales/"+id+"/quote", M{}, f.sm, 200)["sale"].(M)
	if q["status"] != "QUOTED" || q["quotedBy"].(M)["name"] != f.sm.Name {
		t.Fatalf("quoted: %v", q["status"])
	}
	if r := e.Do("POST", "/api/sales/"+id+"/quote", M{}, f.sm); r.StatusCode != 409 {
		t.Errorf("re-quote: %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/sales/pdf/confirmation/"+id, nil, f.sm); r.StatusCode != 409 {
		t.Errorf("confirmation PDF before confirm: %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/sales/pdf/quotation/"+id+"?lang=fr&orientation=landscape", nil, f.sm); r.StatusCode != 200 {
		t.Errorf("quotation pdf: %d", r.StatusCode)
	}
	// quoted orders stay editable
	status(t, e, "PATCH", "/api/sales/"+id, M{"notes": "TEST quoted edit"}, f.sm, 200)
	// confirm: no stock → shortfall warnings + Production Manager notified (ORD-05)
	c := status(t, e, "POST", "/api/sales/"+id+"/confirm", M{}, f.sm, 200)
	cs := c["sale"].(M)
	if cs["status"] != "CONFIRMED" || len(c["warnings"].([]any)) != 2 || cs["locked"] != true || num(cs["version"]) != 1 || len(cs["versions"].([]any)) != 1 {
		t.Fatalf("confirmed: %v %v", cs["status"], c["warnings"])
	}
	if f.count(`SELECT count(*) FROM notification WHERE event='sales.order_shortfall' AND user_id=$1`, pm.ID) != 1 {
		t.Error("shortfall must notify the production manager")
	}
	if f.count(`SELECT count(*) FROM notification WHERE event='sales.quotation_accepted'`) != 0 {
		t.Error("the quoter confirmed it himself: no self-notification")
	}
	av := status(t, e, "GET", "/api/sales/"+id+"/availability", nil, f.sm, 200)
	if num(av["shortfall"]) != 150 || len(av["lines"].([]any)) != 2 {
		t.Errorf("availability: %v", av)
	}
	// ORD-10: commercial terms locked
	if r := e.Do("PATCH", "/api/sales/"+id, M{"paymentTerm": "NET7"}, f.sm); r.StatusCode != 409 {
		t.Errorf("locked terms: %d", r.StatusCode)
	}
	// payments: deposit (auto-classified)
	pay := status(t, e, "POST", "/api/sales/"+id+"/payments", M{"date": f.today, "amount": 111250, "method": "MOMO"}, f.fm, 201)["sale"].(M)
	if pay["payments"].([]any)[0].(M)["kind"] != "DEPOSIT" || pay["depositStatus"] != "RECEIVED" {
		t.Errorf("deposit: %v %v", pay["payments"], pay["depositStatus"])
	}
	if r := e.Do("POST", "/api/sales/"+id+"/payments", M{"date": f.today, "amount": 10, "method": "CASH", "kind": "BALANCE"}, f.fm); r.StatusCode != 400 {
		t.Errorf("balance must settle: %d", r.StatusCode)
	}
	// ORD-05: mark one line in production → IN_PRODUCTION; ready; dispatch only when ready
	lines := cs["lines"].([]any)
	la, lb := lines[0].(M)["id"].(string), lines[1].(M)["id"].(string)
	if r := e.Do("POST", "/api/sales/"+id+"/fulfilment", M{"lines": []M{{"saleLineId": la, "fulfilment": "PRODUCTION"}}}, f.disp1); r.StatusCode != 403 {
		t.Errorf("supervisor fulfilment: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/sales/"+id+"/fulfilment", M{"lines": []M{{"saleLineId": "zzz", "fulfilment": "PRODUCTION"}}}, f.sm); r.StatusCode != 400 {
		t.Errorf("foreign line: %d", r.StatusCode)
	}
	fl := status(t, e, "POST", "/api/sales/"+id+"/fulfilment", M{"lines": []M{{"saleLineId": la, "fulfilment": "PRODUCTION"}}}, pm, 200)["sale"].(M)
	if fl["status"] != "IN_PRODUCTION" || fl["lines"].([]any)[1].(M)["fulfilment"] != "STOCK" {
		t.Fatalf("in production: %v", fl["status"])
	}
	f.addStock(f.plant1, f.hb20, 100, f.today)
	f.addStock(f.plant1, f.hb15, 50, f.today)
	rd := status(t, e, "POST", "/api/sales/"+id+"/ready", M{}, f.disp1, 200)["sale"].(M)
	if rd["status"] != "READY" {
		t.Fatalf("ready: %v", rd["status"])
	}
	// DEL-03/04 partial dispatch with destination; stock reduced at the dispatching location
	d1 := status(t, e, "POST", "/api/sales/"+id+"/deliveries", M{"dnNumber": "TEST-DN-1", "date": f.today, "vehicle": "RAD 1", "driver": "TEST d",
		"lines": []M{{"saleLineId": la, "quantity": 60}}}, f.disp1, 201)
	s1 := d1["sale"].(M)
	if s1["status"] != "DISPATCHED" || f.stock(f.plant1, f.hb20) != 40 || s1["deliveries"].([]any)[0].(M)["destination"] != "Kicukiro site, KK 500 St" ||
		s1["deliveries"].([]any)[0].(M)["transportMode"] != "BTN" {
		t.Fatalf("dispatch 1: %v %d %v", s1["status"], f.stock(f.plant1, f.hb20), s1["deliveries"])
	}
	d2 := status(t, e, "POST", "/api/sales/"+id+"/deliveries", M{"dnNumber": "TEST-DN-2", "date": f.today, "destination": "Gate B",
		"lines": []M{{"saleLineId": la, "quantity": 40}, {"saleLineId": lb, "quantity": 50}}}, f.disp1, 201)
	dn1, dn2 := d1["deliveryId"].(string), d2["deliveryId"].(string)
	if d2["sale"].(M)["status"] != "DISPATCHED" {
		t.Fatal("all shipped but not received: still dispatched")
	}
	// DEL-05: delivery confirmation with photo (multipart) — type sniffed, ≤ 5 MB
	mb, ct := multipartBody(t, map[string]string{"receivedBy": "TEST site foreman", "note": "2 broken"}, []byte("<html>not an image</html>"), "x.jpg")
	if r := e.Do("POST", "/api/sales/deliveries/"+dn1+"/delivered", mb, f.disp1, "Content-Type", ct); r.StatusCode != 400 {
		t.Errorf("fake image: %d", r.StatusCode)
	}
	mb, ct = multipartBody(t, map[string]string{"receivedBy": "TEST site foreman", "note": "2 broken"}, pngBytes(t), "pod.png")
	if r := e.Do("POST", "/api/sales/deliveries/"+dn1+"/delivered", mb, f.disp1, "Content-Type", ct); r.StatusCode != 200 {
		t.Fatalf("delivered with photo: %d %s", r.StatusCode, e.Text(r))
	}
	ph := e.Do("GET", "/api/sales/deliveries/"+dn1+"/photo", nil, f.sm)
	pb, _ := io.ReadAll(ph.Body)
	if ph.StatusCode != 200 || ph.Header.Get("Content-Type") != "image/png" || ph.Header.Get("X-Content-Type-Options") != "nosniff" || len(pb) < 100 {
		t.Errorf("photo: %d %s", ph.StatusCode, ph.Header.Get("Content-Type"))
	}
	if th := e.Do("GET", "/api/sales/deliveries/"+dn1+"/photo/thumb", nil, f.disp1); th.StatusCode != 200 || th.Header.Get("Content-Type") != "image/jpeg" {
		t.Errorf("thumb: %d", th.StatusCode)
	}
	if th := e.Do("GET", "/api/sales/deliveries/"+dn1+"/photo", nil, f.disp2); th.StatusCode != 403 {
		t.Errorf("photo scope: %d", th.StatusCode)
	}
	if th := e.Do("GET", "/api/sales/deliveries/"+dn2+"/photo", nil, f.sm); th.StatusCode != 404 {
		t.Errorf("no photo: %d", th.StatusCode)
	}
	dv := status(t, e, "POST", "/api/sales/deliveries/"+dn2+"/delivered", M{"receivedBy": "TEST foreman"}, f.disp1, 200)
	_ = dv
	g := status(t, e, "GET", "/api/sales/"+id, nil, f.sm, 200)["sale"].(M)
	if g["status"] != "DELIVERED" || g["deliveries"].([]any)[0].(M)["hasPhoto"] != true || g["deliveries"].([]any)[0].(M)["deliveryNote"] != "2 broken" || g["dueDate"] != f.today {
		t.Fatalf("delivered: %v due=%v", g["status"], g["dueDate"])
	}
	if f.count(`SELECT count(*) FROM notification WHERE event='sales.delivered'`) == 0 || f.count(`SELECT count(*) FROM notification WHERE event='sales.dispatched'`) == 0 {
		t.Error("dispatch / delivered notifications")
	}
	// closing needs the balance; the balance payment closes it automatically
	if r := e.Do("POST", "/api/sales/"+id+"/close", M{}, f.sm); r.StatusCode != 409 {
		t.Errorf("close unpaid: %d", r.StatusCode)
	}
	fin := status(t, e, "POST", "/api/sales/"+id+"/payments", M{"date": f.today, "amount": 111250, "method": "BANK_BK"}, f.fm, 201)["sale"].(M)
	if fin["status"] != "CLOSED" || fin["payments"].([]any)[1].(M)["kind"] != "BALANCE" || fin["closedAt"] == nil {
		t.Fatalf("closed: %v", fin["status"])
	}
	if r := e.Do("POST", "/api/sales/deliveries/"+dn2+"/cancel", M{"reason": "x"}, f.pm); r.StatusCode != 409 {
		t.Errorf("closed order DN cancel: %d", r.StatusCode)
	}
	hist := fin["history"].([]any)
	var path []string
	for _, h := range hist {
		path = append(path, h.(M)["to"].(string))
	}
	if strings.Join(path, ">") != "DRAFT>QUOTED>CONFIRMED>IN_PRODUCTION>READY>DISPATCHED>DELIVERED>CLOSED" {
		t.Errorf("history: %v", path)
	}
	// confirmation + order PDFs show the charge; deliveries report filters (DEL-06)
	for _, p := range []string{"/api/sales/pdf/confirmation/" + id, "/api/sales/pdf/order/" + id + "?lang=am", "/api/sales/pdf/delivery-note/" + dn1 + "?lang=fr"} {
		if r := e.Do("GET", p, nil, f.sm); r.StatusCode != 200 {
			t.Errorf("%s: %d", p, r.StatusCode)
		}
	}
	if dl := status(t, e, "GET", "/api/sales/deliveries?transportMode=BTN&customerId="+f.cust+"&saleId="+id, nil, f.sm, 200); num(dl["total"]) != 2 {
		t.Errorf("deliveries filter: %v", dl["total"])
	}
	if dl := status(t, e, "GET", "/api/sales/deliveries?transportMode=CLIENT", nil, f.sm, 200); num(dl["total"]) != 0 {
		t.Errorf("transport filter: %v", dl["total"])
	}
	_ = fixed
}

// ── ORD-04 value/role approvals, cancellation with approval ─────────────────────────────────────

func TestValueApprovalsAndCancellation(t *testing.T) {
	f := setup(t)
	e := f.e
	md := e.User(auth.RoleSalesManager) // a second sales manager (SRS §8: order approvers are Sales and the PM)
	top := e.User(auth.RoleManagingDirector)
	// 6 000 000 RWF: level 1 (sales.approve) — the sales manager's own authority covers it
	mid := f.create(f.sm, f.saleBody(true, line(f.hb20, 4000, 1500)))
	if mid["status"] != "CONFIRMED" || mid["approvals"].([]any)[0].(M)["steps"].([]any)[0].(M)["auto"] != true {
		t.Fatalf("level 1 auto: %v", mid["status"])
	}
	// 25 500 000 RWF: level 2 = Production Manager role
	big := status(t, e, "POST", "/api/sales", f.saleBody(true, line(f.hb20, 17000, 1500)), f.sm, 201)
	if big["pendingApproval"] != true {
		t.Fatalf("big order must wait: %v", big["sale"].(M)["status"])
	}
	bid := big["sale"].(M)["id"].(string)
	appr := big["sale"].(M)["pendingApproval"].(M)
	aid := appr["id"].(string)
	if num(appr["nextLevel"].(M)["level"]) != 2 || appr["canDecide"] != false {
		t.Errorf("pending: %v", appr)
	}
	if f.count(`SELECT count(*) FROM notification WHERE event='sales.approval_pending' AND user_id=$1`, f.pm.ID) != 1 {
		t.Error("PM notified of the pending approval")
	}
	if ls := status(t, e, "GET", "/api/sales/approvals?mine=1", nil, f.pm, 200); len(ls["items"].([]any)) != 1 {
		t.Errorf("pm queue: %v", ls)
	}
	if ls := status(t, e, "GET", "/api/sales/approvals?mine=1", nil, md, 200); len(ls["items"].([]any)) != 0 {
		t.Errorf("another sales manager cannot decide the PM level: %v", ls)
	}
	if r := e.Do("POST", "/api/sales/approvals/"+aid+"/approve", M{}, md); r.StatusCode != 403 {
		t.Errorf("sm2 approves PM level: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/sales/approvals/"+aid+"/approve", M{}, top); r.StatusCode != 403 {
		t.Errorf("top management approves orders (SRS §8 R): %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/sales/approvals/"+aid+"/approve", M{}, f.sm); r.StatusCode != 403 {
		t.Errorf("own request: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/sales/approvals/"+aid+"/approve", M{}, f.fm); r.StatusCode != 403 {
		t.Errorf("finance: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/sales/approvals/"+aid+"/reject", M{}, f.pm); r.StatusCode != 400 {
		t.Errorf("reject needs a reason: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/sales/"+bid+"/confirm", M{}, f.sm); r.StatusCode != 409 {
		t.Errorf("second pending approval: %d", r.StatusCode)
	}
	ok := status(t, e, "POST", "/api/sales/approvals/"+aid+"/approve", M{"note": "TEST ok"}, f.pm, 200)
	if ok["approval"].(M)["status"] != "APPROVED" {
		t.Fatalf("approved: %v", ok)
	}
	if s := status(t, e, "GET", "/api/sales/"+bid, nil, f.sm, 200)["sale"].(M); s["status"] != "CONFIRMED" || s["confirmedBy"].(M)["name"] != f.pm.Name {
		t.Fatalf("confirmed after approval: %v", s["status"])
	}
	if f.count(`SELECT count(*) FROM notification WHERE event='sales.approval_decided' AND user_id=$1`, f.sm.ID) != 1 {
		t.Error("requester told of the decision")
	}
	if r := e.Do("POST", "/api/sales/approvals/"+aid+"/approve", M{}, f.pm); r.StatusCode != 409 {
		t.Errorf("decided twice: %d", r.StatusCode)
	}
	// rejection keeps the order as it was
	rej := status(t, e, "POST", "/api/sales", f.saleBody(true, line(f.hb20, 17000, 1500)), f.sm, 201)["sale"].(M)
	status(t, e, "POST", "/api/sales/approvals/"+rej["pendingApproval"].(M)["id"].(string)+"/reject", M{"note": "TEST too big"}, f.pm, 200)
	if s := status(t, e, "GET", "/api/sales/"+rej["id"].(string), nil, f.sm, 200)["sale"].(M); s["status"] != "DRAFT" || s["pendingApproval"] != nil {
		t.Errorf("rejected confirm: %v", s["status"])
	}
	// cancellation (draft, quoted or confirmed) always through approval (four eyes, sales.cancel level)
	q := f.create(f.sm, M{"plantId": f.plant1, "customerId": f.cust, "date": f.today, "lines": []M{line(f.hb20, 10, 1500)}, "quote": true})
	if q["status"] != "QUOTED" {
		t.Fatalf("quote on create: %v", q["status"])
	}
	cq := status(t, e, "POST", "/api/sales/"+q["id"].(string)+"/cancel", M{"reason": "TEST client declined"}, f.sm, 200)
	if cq["pendingApproval"] != true || cq["sale"].(M)["status"] != "QUOTED" {
		t.Fatalf("quoted cancel pending: %v", cq["sale"].(M)["status"])
	}
	ca := status(t, e, "POST", "/api/sales/approvals/"+cq["sale"].(M)["pendingApproval"].(M)["id"].(string)+"/approve", M{}, md, 200)
	if ca["approval"].(M)["status"] != "APPROVED" {
		t.Fatal("another sales manager approves the cancellation")
	}
	if s := status(t, e, "GET", "/api/sales/"+q["id"].(string), nil, f.sm, 200)["sale"].(M); s["status"] != "CANCELLED" || s["cancelReason"] != "TEST client declined" {
		t.Fatalf("cancelled: %v", s["status"])
	}
	// the PM's own cancellation of a confirmed order still needs someone else
	gc := status(t, e, "POST", "/api/sales/"+mid["id"].(string)+"/cancel", M{"reason": "TEST duplicate"}, f.pm, 200)
	gaid := gc["sale"].(M)["pendingApproval"].(M)["id"].(string)
	if r := e.Do("POST", "/api/sales/approvals/"+gaid+"/approve", M{}, f.pm); r.StatusCode != 403 {
		t.Errorf("own cancellation: %d", r.StatusCode)
	}
	// level 1 (sales.approve) by a second sales manager, level 2 (sales.cancel) needs yet another person
	status(t, e, "POST", "/api/sales/approvals/"+gaid+"/approve", M{}, md, 200)
	if r := e.Do("POST", "/api/sales/approvals/"+gaid+"/approve", M{}, md); r.StatusCode != 403 {
		t.Errorf("same approver twice: %d", r.StatusCode)
	}
	md2 := e.User(auth.RoleProductionManager)
	status(t, e, "POST", "/api/sales/approvals/"+gaid+"/approve", M{}, md2, 200)
	if s := status(t, e, "GET", "/api/sales/"+mid["id"].(string), nil, f.sm, 200)["sale"].(M); s["status"] != "CANCELLED" {
		t.Fatalf("confirmed → cancelled: %v", s["status"])
	}
	// not cancellable once in fulfilment
	o := f.create(f.sm, f.saleBody(true, line(f.hb20, 10, 1500)))
	status(t, e, "POST", "/api/sales/"+o["id"].(string)+"/ready", M{}, f.sm, 200)
	if r := e.Do("POST", "/api/sales/"+o["id"].(string)+"/cancel", M{"reason": "x"}, f.pm); r.StatusCode != 409 {
		t.Errorf("cancel a ready order: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/sales/"+o["id"].(string)+"/cancel", M{"reason": "x"}, f.disp1); r.StatusCode != 403 {
		t.Errorf("stock officer cancel: %d", r.StatusCode)
	}
	for _, a := range []string{"sales.approval.request", "sales.approval.approve", "sales.approval.reject", "sales.cancel", "sales.status"} {
		if f.count(`SELECT count(*) FROM "AuditLog" WHERE action=$1`, a) == 0 {
			t.Errorf("missing audit %s", a)
		}
	}
}

// ── ORD-10 amendments with approval and versions ─────────────────────────────────────────────────

func TestAmendmentsVersionHistory(t *testing.T) {
	f := setup(t)
	e := f.e
	f.addStock(f.plant1, f.hb20, 1000, f.today)
	s := f.create(f.sm, f.saleBody(true, line(f.hb20, 100, 1500), line(f.hb15, 20, 1300)))
	id := s["id"].(string)
	la, lb := s["lines"].([]any)[0].(M)["id"].(string), s["lines"].([]any)[1].(M)["id"].(string)
	status(t, e, "POST", "/api/sales/"+id+"/ready", M{}, f.sm, 200)
	status(t, e, "POST", "/api/sales/"+id+"/deliveries", M{"dnNumber": "TEST-AM-1", "date": f.today, "lines": []M{{"saleLineId": la, "quantity": 30}}}, f.disp1, 201)
	if r := e.Do("PATCH", "/api/sales/"+id, M{"lines": []M{line(f.hb20, 1, 1)}}, f.sm); r.StatusCode != 409 {
		t.Fatalf("direct edit of a confirmed order: %d", r.StatusCode)
	}
	// invalid proposals: below delivered, removing a delivered line
	r := e.Do("POST", "/api/sales/"+id+"/amendments", M{"reason": "TEST", "lines": []M{{"saleLineId": la, "productId": f.hb20, "quantity": 20, "unitPrice": 1500}}}, f.sm)
	if _, _, fields := e.ErrorOf(r); r.StatusCode != 400 || fields["lines.0.quantity"] == "" {
		t.Fatalf("below delivered: %d %v", r.StatusCode, fields)
	}
	if r := e.Do("POST", "/api/sales/"+id+"/amendments", M{"lines": []M{{"saleLineId": la, "productId": f.hb20, "quantity": 40, "unitPrice": 1500}}}, f.sm); r.StatusCode != 400 {
		t.Errorf("reason required: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/sales/"+id+"/amendments", M{"reason": "x", "lines": []M{{"saleLineId": la, "productId": f.hb20, "quantity": 40, "unitPrice": 1500}}}, f.fm); r.StatusCode != 403 {
		t.Errorf("finance amendment: %d", r.StatusCode)
	}
	// request: HB20 down to 80 with 10 % off, HB15 line dropped, a new HB15 line at a new price, BTN delivery
	am := status(t, e, "POST", "/api/sales/"+id+"/amendments", M{"reason": "TEST client changed the order", "transportMode": "CLIENT", "paymentTerm": "NET14",
		"lines": []M{{"saleLineId": la, "productId": f.hb20, "quantity": 80, "unitPrice": 1500, "discountPercent": 10}, {"productId": f.hb15, "quantity": 10, "unitPrice": 1250}}}, f.sm, 201)
	as := am["sale"].(M)
	if as["pendingApproval"].(M)["kind"] != "AMENDMENT" || num(as["version"]) != 1 || len(as["amendments"].([]any)) != 1 {
		t.Fatalf("amendment pending: %v", as["pendingApproval"])
	}
	if f.count(`SELECT count(*) FROM notification WHERE event='sales.amendment_requested'`) == 0 {
		t.Error("amendment requested notification")
	}
	aid := as["pendingApproval"].(M)["id"].(string)
	if r := e.Do("POST", "/api/sales/approvals/"+aid+"/approve", M{}, f.sm); r.StatusCode != 403 {
		t.Errorf("own amendment: %d", r.StatusCode)
	}
	status(t, e, "POST", "/api/sales/approvals/"+aid+"/approve", M{}, f.pm, 200)
	g := status(t, e, "GET", "/api/sales/"+id, nil, f.sm, 200)["sale"].(M)
	if num(g["version"]) != 2 || num(g["totals"].(M)["amount"]) != 80*1350+10*1250 || len(g["lines"].([]any)) != 2 || g["paymentTerm"] != "NET14" ||
		len(g["versions"].([]any)) != 2 || g["amendments"].([]any)[0].(M)["status"] != "APPLIED" || g["lines"].([]any)[0].(M)["id"] != la {
		t.Fatalf("applied: v%v %v", g["version"], g["totals"])
	}
	if f.count(`SELECT count(*) FROM "SaleLine" WHERE id=$1`, lb) != 0 {
		t.Error("dropped line deleted")
	}
	if f.count(`SELECT count(*) FROM sale_version WHERE sale_id=$1 AND version=1 AND total=$2`, id, 100*1500+20*1300) != 1 {
		t.Error("version 1 snapshot kept")
	}
	if f.count(`SELECT count(*) FROM "AuditLog" WHERE action='sales.amendment.apply' AND "entityId"=$1`, id) != 1 {
		t.Error("amendment audited")
	}
	if f.count(`SELECT count(*) FROM notification WHERE event='sales.amendment_decided' AND user_id=$1`, f.sm.ID) != 1 {
		t.Error("requester told of the applied amendment")
	}
	// a rejected amendment changes nothing
	am2 := status(t, e, "POST", "/api/sales/"+id+"/amendments", M{"reason": "TEST more", "lines": []M{{"saleLineId": la, "productId": f.hb20, "quantity": 500, "unitPrice": 1500}}}, f.sm, 201)
	status(t, e, "POST", "/api/sales/approvals/"+am2["sale"].(M)["pendingApproval"].(M)["id"].(string)+"/reject", M{"note": "TEST no"}, f.pm, 200)
	if g := status(t, e, "GET", "/api/sales/"+id, nil, f.sm, 200)["sale"].(M); num(g["version"]) != 2 || g["amendments"].([]any)[0].(M)["status"] != "REJECTED" {
		t.Errorf("rejected amendment: %v", g["version"])
	}
	// drafts are edited directly, not amended
	d := f.create(f.sm, f.saleBody(false, line(f.hb20, 1, 1500)))
	if r := e.Do("POST", "/api/sales/"+d["id"].(string)+"/amendments", M{"reason": "x", "lines": []M{line(f.hb20, 2, 1500)}}, f.sm); r.StatusCode != 409 {
		t.Errorf("amend a draft: %d", r.StatusCode)
	}
}

// ── ORD-09 / DEL-06 reports, exports; overdue job ────────────────────────────────────────────────

func TestReportsExportsAndOverdue(t *testing.T) {
	f := setup(t)
	e := f.e
	f.addStock(f.plant1, f.hb20, 100, f.today)
	a := f.create(f.sm, f.saleBody(true, line(f.hb20, 10, 1500), line(f.hb15, 5, 1300)))
	f.create(f.sm, f.saleBody(false, line(f.hb20, 3, 1500)))
	status(t, e, "POST", "/api/sales/"+a["id"].(string)+"/ready", M{}, f.sm, 200)
	status(t, e, "POST", "/api/sales/"+a["id"].(string)+"/deliveries", M{"dnNumber": "TEST-R-1", "date": f.today, "lines": []M{{"saleLineId": a["lines"].([]any)[0].(M)["id"], "quantity": 4}}}, f.disp1, 201)
	for _, g := range []string{"client", "product", "status", "period"} {
		rep := status(t, e, "GET", "/api/sales/reports/orders?groupBy="+g, nil, f.sm, 200)
		if len(rep["rows"].([]any)) == 0 || num(rep["totals"].(M)["orders"]) != 2 || num(rep["totals"].(M)["amount"]) != 15000+6500+4500 {
			t.Errorf("%s report: %v", g, rep["totals"])
		}
	}
	st := status(t, e, "GET", "/api/sales/reports/orders?groupBy=status", nil, f.sm, 200)
	if st["rows"].([]any)[0].(M)["label"] != "Draft" {
		t.Errorf("status labels: %v", st["rows"])
	}
	if r := e.Do("GET", "/api/sales/reports/orders?groupBy=weather", nil, f.sm); r.StatusCode != 400 {
		t.Errorf("bad groupBy: %d", r.StatusCode)
	}
	status(t, e, "GET", "/api/sales/reports/orders", nil, f.sup, 200) // SRS §8 Reports: supervisor "R (site)"
	if r := e.Do("GET", "/api/sales/reports/orders", nil, f.op); r.StatusCode != 403 {
		t.Errorf("operator report: %d", r.StatusCode)
	}
	for _, p := range []string{"/api/sales/reports/orders?groupBy=product&format=csv", "/api/sales/deliveries/pending?format=xlsx", "/api/sales/deliveries?format=csv", "/api/v1/sales/reports/orders?format=json"} {
		r := e.Do("GET", p, nil, f.sm)
		if r.StatusCode != 200 || strings.Contains(r.Header.Get("Content-Type"), "pdf") {
			t.Errorf("export %s: %d %s", p, r.StatusCode, r.Header.Get("Content-Type"))
		}
	}
	pd := status(t, e, "GET", "/api/sales/deliveries/pending", nil, f.disp1, 200)
	it := pd["items"].([]any)
	if len(it) != 1 || num(it[0].(M)["remaining"]) != 11 || num(it[0].(M)["openNotes"]) != 1 {
		t.Errorf("pending: %v", pd)
	}
	if pd := status(t, e, "GET", "/api/sales/deliveries/pending", nil, f.disp2, 200); len(pd["items"].([]any)) != 0 {
		t.Errorf("pending scope: %v", pd)
	}
	// overdue scan: a confirmed order whose balance is past due (dedup per due date)
	if _, err := e.Pool.Exec(t.Context(), `UPDATE "Sale" SET "dueDate" = current_date - 3 WHERE id=$1`, a["id"]); err != nil {
		t.Fatal(err)
	}
	n, err := sales.RunOverdueScan(context.Background(), e.Pool, dates.TodayISO())
	if err != nil || n != 1 {
		t.Fatalf("overdue: %d %v", n, err)
	}
	_, _ = sales.RunOverdueScan(context.Background(), e.Pool, dates.TodayISO())
	if c := f.count(`SELECT count(*) FROM notification WHERE event='sales.payment_overdue' AND user_id=$1`, f.fm.ID); c != 1 {
		t.Errorf("overdue notified once to finance: %d", c)
	}
	if ls := status(t, e, "GET", "/api/sales?overdue=1", nil, f.sm, 200); num(ls["total"]) != 1 {
		t.Errorf("overdue filter: %v", ls["total"])
	}
	// legacy writers (importer) keep working: status → orderStatus via the trigger
	var os string
	if err := e.Pool.QueryRow(t.Context(), `INSERT INTO "Sale" (id, number, "plantId", "customerId", date, status, "createdById", "updatedAt")
		VALUES ('legacy-1', 'IMP-1', $1, $2, current_date, 'CONFIRMED', $3, now()) RETURNING "orderStatus"`, f.plant1, f.cust, f.sm.ID).Scan(&os); err != nil || os != "CONFIRMED" {
		t.Errorf("legacy insert: %v %s", err, os)
	}
}

// ── §5 offline: order_draft and dispatch_note ───────────────────────────────────────────────────

func TestOfflineOrderDraftAndDispatchNote(t *testing.T) {
	f := setup(t)
	e := f.e
	dsm := e.SignInDevice(f.sm, "")
	payload := M{"plantId": f.plant1, "customerId": f.cust, "date": f.today, "transportMode": "CLIENT", "deliverySite": "Nyamata",
		"lines": []M{{"productId": f.hb20, "quantity": 20, "unitPrice": 1500, "discountPercent": 2}}}
	rec := apptest.SyncRecord(sales.SyncOrderDraft, 0, payload)
	res := dsm.Push(rec)
	if res.Status != 200 || res.Results[0].Status != "accepted" || res.Results[0].ServerID != rec["uuid"] || res.Results[0].Flags[0] != "SHORTFALL" {
		t.Fatalf("order_draft: %+v", res)
	}
	oid := rec["uuid"].(string)
	g := status(t, e, "GET", "/api/sales/"+oid, nil, f.sm, 200)["sale"].(M)
	if g["status"] != "DRAFT" || g["capturedOffline"] != true || num(g["totals"].(M)["amount"]) != 29400 {
		t.Fatalf("offline order: %v", g)
	}
	// a correction of the same draft (same uuid, revision 2) updates it
	// a later offline edit of that draft is a new record naming the order
	upd := apptest.SyncRecord(sales.SyncOrderDraft, 0, M{"orderId": oid, "plantId": f.plant1, "customerId": f.cust, "date": f.today, "lines": []M{line(f.hb20, 30, 1500)}})
	if res := dsm.Push(upd); res.Results[0].Status != "accepted" || res.Results[0].ServerID != oid {
		t.Fatalf("revision 2: %+v", res.Results)
	}
	if g := status(t, e, "GET", "/api/sales/"+oid, nil, f.sm, 200)["sale"].(M); num(g["totals"].(M)["quantity"]) != 30 {
		t.Errorf("updated draft: %v", g["totals"])
	}
	// validation and permission are re-checked on sync
	bad := dsm.Push(apptest.SyncRecord(sales.SyncOrderDraft, 0, M{"plantId": f.plant1, "customerId": f.cust, "date": "2999-01-01", "lines": []M{}}))
	if bad.Results[0].Status != "rejected" || bad.Results[0].ReasonCode != "validation" || bad.Results[0].Fields["date"] == "" {
		t.Errorf("rejected: %+v", bad.Results)
	}
	dfm := e.SignInDevice(f.fm, "")
	if r := dfm.Push(apptest.SyncRecord(sales.SyncOrderDraft, 0, payload)); r.Results[0].Status != "rejected" || !strings.HasPrefix(r.Results[0].ReasonCode, "permission") {
		t.Errorf("finance cannot capture orders: %+v", r.Results)
	}
	// §5.5: the order was quoted on the server meanwhile → server status wins, offline edit kept for review
	status(t, e, "POST", "/api/sales/"+oid+"/quote", M{}, f.sm, 200)
	late := apptest.SyncRecord(sales.SyncOrderDraft, 0, M{"orderId": oid, "plantId": f.plant1, "customerId": f.cust, "date": f.today, "lines": []M{line(f.hb20, 99, 1500)}})
	if res := dsm.Push(late); res.Results[0].Status != "conflict" || res.Results[0].ConflictID == "" {
		t.Fatalf("status conflict: %+v", res.Results)
	}
	if g := status(t, e, "GET", "/api/sales/"+oid, nil, f.sm, 200)["sale"].(M); g["status"] != "QUOTED" || num(g["totals"].(M)["quantity"]) != 30 {
		t.Errorf("server version kept: %v", g["status"])
	}
	// dispatch_note on a confirmed (not ready) order → conflict; on a ready order → accepted, stock reduced (negative flagged)
	o := f.create(f.sm, f.saleBody(true, line(f.hb20, 50, 1500)))
	ol := o["lines"].([]any)[0].(M)["id"].(string)
	dsup := e.SignInDevice(f.disp1, "")
	dn := M{"saleId": o["id"], "dnNumber": "OFF-1", "date": f.today, "vehicle": "RAE 9", "destination": "Bugesera", "lines": []M{{"saleLineId": ol, "quantity": 20}}}
	if res := dsup.Push(apptest.SyncRecord(sales.SyncDispatchNote, 0, dn)); res.Results[0].Status != "conflict" {
		t.Fatalf("dispatch before ready: %+v", res.Results)
	}
	status(t, e, "POST", "/api/sales/"+o["id"].(string)+"/ready", M{}, f.sm, 200)
	f.addStock(f.plant1, f.hb20, 10, f.today)
	dn["dnNumber"] = "OFF-2"
	drec := apptest.SyncRecord(sales.SyncDispatchNote, 0, dn)
	res = dsup.Push(drec)
	if res.Results[0].Status != "accepted" || res.Results[0].ServerID != drec["uuid"] || len(res.Results[0].Flags) == 0 || res.Results[0].Flags[0] != "NEGATIVE_STOCK" {
		t.Fatalf("dispatch_note: %+v", res.Results)
	}
	if f.stock(f.plant1, f.hb20) != -10 {
		t.Errorf("stock after offline dispatch: %d", f.stock(f.plant1, f.hb20))
	}
	if g := status(t, e, "GET", "/api/sales/"+o["id"].(string), nil, f.sm, 200)["sale"].(M); g["status"] != "DISPATCHED" || g["deliveries"].([]any)[0].(M)["capturedOffline"] != true {
		t.Errorf("offline dispatch: %v", g["status"])
	}
	// BTN-2 supervisor: site scope
	dsup2 := e.SignInDevice(f.disp2, "")
	dn["dnNumber"] = "OFF-3"
	if res := dsup2.Push(apptest.SyncRecord(sales.SyncDispatchNote, 0, dn)); res.Results[0].Status != "rejected" || res.Results[0].ReasonCode != "site_scope" {
		t.Errorf("scope: %+v", res.Results)
	}
	// pulls: orders + dispatches in scope
	pr := dsup.Do("GET", "/api/sync/pull?entities=orders,dispatches", nil)
	var pull struct {
		Changes []struct {
			Entity string `json:"entity"`
			ID     string `json:"id"`
		} `json:"changes"`
	}
	e.Decode(pr, &pull)
	n := map[string]int{}
	for _, c := range pull.Changes {
		n[c.Entity]++
	}
	if n["orders"] < 2 || n["dispatches"] != 1 {
		t.Errorf("pull: %v", n)
	}
	pr2 := dsup2.Do("GET", "/api/sync/pull?entities=orders,dispatches", nil)
	var pull2 struct {
		Changes []any `json:"changes"`
	}
	e.Decode(pr2, &pull2)
	if len(pull2.Changes) != 0 {
		t.Errorf("BTN-2 pulls nothing of BTN-1: %d", len(pull2.Changes))
	}
	_ = testutil.UserOpts{}
}
