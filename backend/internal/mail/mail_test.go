package mail

import (
	"context"
	"mime"
	"net/mail"
	"strings"
	"testing"

	"btn-ops/internal/config"
	"btn-ops/internal/i18n"
)

func TestOTPEmailIsLocalizedAndBranded(t *testing.T) {
	m := New(&config.Config{AppURL: "http://localhost:5173", MailFrom: "BTN <noreply@btn.test>"}, nil)
	for l, want := range map[i18n.Lang]string{
		i18n.EN: "123456 is your BTN sign-in code",
		i18n.AM: "123456 የBTN የመግቢያ ኮድዎ ነው",
		i18n.RW: "123456 ni kode yawe yo kwinjira muri BTN",
	} {
		subj, text, html := m.BuildOTP(context.Background(), OTP{To: "a@b.c", Name: "Aline <b>", Code: "123456", Purpose: "LOGIN", Minutes: 10, IP: "10.0.0.1", Lang: l})
		if subj != want {
			t.Errorf("%s subject %q", l, subj)
		}
		if !strings.Contains(html, "123456") || !strings.Contains(html, "cid:btnlogo") || !strings.Contains(html, "#F59E0B") {
			t.Errorf("%s html missing parts", l)
		}
		if strings.Contains(html, "Aline <b>") || !strings.Contains(html, "Aline &lt;b&gt;") {
			t.Errorf("%s name must be HTML-escaped", l)
		}
		if !strings.Contains(text, "123456") {
			t.Errorf("%s text", l)
		}
	}
	subj, _, _ := m.BuildOTP(context.Background(), OTP{Code: "654321", Purpose: "PASSWORD_RESET", Minutes: 10, Lang: i18n.EN})
	if subj != "654321 is your BTN password reset code" {
		t.Error(subj)
	}
}

func TestComposeIsValidMIMEWithInlineLogo(t *testing.T) {
	m := New(&config.Config{AppURL: "http://x", MailFrom: "BTN <noreply@btn.test>"}, nil)
	subj, text, html := m.BuildWelcome(context.Background(), Welcome{To: "u@btn.test", Name: "ሰላም", TempPassword: "Tmp#1", Lang: i18n.AM})
	raw := m.Compose(context.Background(), "u@btn.test", subj, text, html)
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	dec := new(mime.WordDecoder)
	got, err := dec.DecodeHeader(msg.Header.Get("Subject"))
	if err != nil || got != subj {
		t.Fatalf("subject round trip: %q %v", got, err)
	}
	mt, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/related" || params["boundary"] == "" {
		t.Fatalf("content type %q %v", mt, err)
	}
	s := string(raw)
	if !strings.Contains(strings.ToLower(s), "content-id: <btnlogo>") || !strings.Contains(s, "multipart/alternative") || strings.Contains(s, "\r\r\n") {
		t.Fatal("structure")
	}
}
