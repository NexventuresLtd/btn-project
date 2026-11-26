package mail

import (
	"context"
	"strings"
	"testing"

	"btn-ops/internal/config"
	"btn-ops/internal/i18n"
)

// F3 (AUTH-04): the reset email carries the single-use link (localized) above the alternative code.
func TestResetEmailCarriesLocalizedLink(t *testing.T) {
	m := New(&config.Config{AppURL: "http://localhost:5173", MailFrom: "BTN <noreply@btn.test>"}, nil)
	link := "http://localhost:5173/reset-password?token=abc&x=<y>"
	for _, l := range []i18n.Lang{i18n.EN, i18n.AM, i18n.RW, i18n.FR} {
		_, text, html := m.BuildOTP(context.Background(), OTP{To: "a@b.c", Name: "A", Code: "123456", Purpose: "PASSWORD_RESET", Minutes: 10, Lang: l, Link: link, LinkMinutes: 30})
		button := i18n.T(l, "mail.reset_link_button")
		if button == "mail.reset_link_button" || !strings.Contains(html, button) || !strings.Contains(html, "token=abc&amp;x=&lt;y&gt;") {
			t.Errorf("%s: html lacks the escaped link/button", l)
		}
		if !strings.Contains(text, link) || !strings.Contains(text, "123456") || !strings.Contains(text, "30") {
			t.Errorf("%s: text lacks link, code or expiry", l)
		}
	}
	// Login codes never carry a link.
	_, _, html := m.BuildOTP(context.Background(), OTP{Code: "1", Purpose: "LOGIN", Lang: i18n.EN, Link: link})
	if strings.Contains(html, "reset-password") {
		t.Error("login email must not contain a reset link")
	}
}
