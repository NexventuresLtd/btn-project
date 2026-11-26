package apptest

// Device-bound test clients (F3): sign in through the real password + email-code flow on a client device id, keep
// the access/refresh cookies like a browser, and push sync batches. Phase-2 owners use these to test their sync
// handlers:
//
//	dc := e.SignInDevice(sup, "")                      // "" = random device id
//	res := dc.Push(apptest.SyncRecord("production_entry", 1, map[string]any{...}))
//	if res.Results[0].Status != "accepted" { t.Fatal(res.Results[0]) }

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/testutil"

	"github.com/google/uuid"
)

// DeviceClient is a signed-in browser profile with its own cookie jar and X-Device-Id.
type DeviceClient struct {
	E        *Env
	User     *testutil.TestUser
	DeviceID string
	Cookies  map[string]*http.Cookie // btn_session, btn_refresh (current values)
	seq      int64
}

// SignInDevice signs u in with password + emailed code, sending X-Device-Id (random when deviceID is "").
func (e *Env) SignInDevice(u *testutil.TestUser, deviceID string) *DeviceClient {
	e.T.Helper()
	if deviceID == "" {
		deviceID = uuid.NewString()
	}
	dc := &DeviceClient{E: e, User: u, DeviceID: deviceID, Cookies: map[string]*http.Cookie{}}
	resp := dc.Do("POST", "/api/auth/login", map[string]any{"email": u.Email, "password": u.Password})
	if resp.StatusCode != 200 {
		e.T.Fatalf("SignInDevice login: %d %s", resp.StatusCode, e.Text(resp))
	}
	resp.Body.Close()
	otp, ok := e.Mail.LastOTP()
	if !ok {
		e.T.Fatal("SignInDevice: no OTP captured")
	}
	resp = dc.Do("POST", "/api/auth/otp/verify", map[string]any{"code": otp.Code})
	if resp.StatusCode != 200 {
		e.T.Fatalf("SignInDevice verify: %d %s", resp.StatusCode, e.Text(resp))
	}
	var out struct {
		Device struct {
			DeviceID string `json:"deviceId"`
		} `json:"device"`
	}
	e.Decode(resp, &out)
	if out.Device.DeviceID != "" {
		dc.DeviceID = out.Device.DeviceID
	}
	delete(dc.Cookies, auth.OTPCookie)
	return dc
}

// Do sends a request with this client's cookies and X-Device-Id and stores any cookies the server sets.
func (dc *DeviceClient) Do(method, path string, body any, headers ...string) *http.Response {
	dc.E.T.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			dc.E.T.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Requested-With", "btn-ops")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Mobile Safari/537.36")
	if dc.DeviceID != "" {
		req.Header.Set(auth.DeviceHeader, dc.DeviceID)
	}
	for _, ck := range dc.Cookies {
		if ck.Path == "" || len(path) >= len(ck.Path) && path[:len(ck.Path)] == ck.Path {
			req.AddCookie(&http.Cookie{Name: ck.Name, Value: ck.Value})
		}
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := dc.E.App.Test(req, -1)
	if err != nil {
		dc.E.T.Fatal(err)
	}
	for _, ck := range resp.Cookies() {
		if ck.MaxAge < 0 || ck.Value == "" {
			delete(dc.Cookies, ck.Name)
		} else {
			dc.Cookies[ck.Name] = ck
		}
	}
	return resp
}

// Refresh calls POST /api/auth/refresh.
func (dc *DeviceClient) Refresh() *http.Response { return dc.Do("POST", "/api/auth/refresh", nil) }

// SyncResult is one per-record push result.
type SyncResult struct {
	UUID           string            `json:"uuid"`
	Revision       int               `json:"revision"`
	Status         string            `json:"status"`
	ReasonCode     string            `json:"reasonCode"`
	Reason         string            `json:"reason"`
	ServerID       string            `json:"serverId"`
	ServerVersion  int64             `json:"serverVersion"`
	Flags          []string          `json:"flags"`
	ConflictID     string            `json:"conflictId"`
	OriginalStatus string            `json:"originalStatus"`
	Fields         map[string]string `json:"fields"`
}

// PushResponse is the push answer.
type PushResponse struct {
	Status           int
	Results          []SyncResult `json:"results"`
	ServerTime       time.Time    `json:"serverTime"`
	ClockSkewSeconds int          `json:"clockSkewSeconds"`
	DeviceStatus     string       `json:"deviceStatus"` // "revoked" = final upload of a revoked device
}

// SyncRecord builds a record (new uuid, capturedAt now, revision 1); seq 0 = assigned by Push.
func SyncRecord(recordType string, seq int64, payload map[string]any) map[string]any {
	return map[string]any{"uuid": uuid.NewString(), "type": recordType, "schemaVersion": 1, "seq": seq, "revision": 1,
		"capturedAt": time.Now().UTC().Format(time.RFC3339Nano), "payload": payload}
}

// Push sends records (seq 0 → next client sequence number) and decodes the answer (Status = HTTP status).
func (dc *DeviceClient) Push(records ...map[string]any) PushResponse {
	dc.E.T.Helper()
	for _, r := range records {
		var s int64
		switch v := r["seq"].(type) {
		case int64:
			s = v
		case int:
			s = int64(v)
		}
		if s == 0 {
			dc.seq++
			r["seq"] = dc.seq
		} else if s > dc.seq {
			dc.seq = s
		}
	}
	resp := dc.Do("POST", "/api/sync/push", map[string]any{"deviceId": dc.DeviceID, "records": records})
	out := PushResponse{Status: resp.StatusCode}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode == 200 {
		if err := json.Unmarshal(b, &out); err != nil {
			dc.E.T.Fatalf("push decode %q: %v", string(b), err)
		}
		out.Status = 200
	}
	return out
}
