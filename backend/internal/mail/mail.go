// Package mail sends the branded, localized transactional emails (OTP, welcome) over SMTP
// (implicit TLS on 465, otherwise mandatory STARTTLS).
package mail

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"html"
	"log/slog"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"sync"
	"time"

	"btn-ops/assets"
	"btn-ops/internal/config"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/i18n"
	"btn-ops/internal/settings"
)

// OTP describes a one-time-code email.
type OTP struct {
	To, Name, Code string
	Purpose        string // LOGIN | PASSWORD_RESET
	Minutes        int
	IP             string
	Lang           i18n.Lang
	Link           string // PASSWORD_RESET only: single-use reset link (F3, AUTH-04); "" = code only
	LinkMinutes    int
}

// Welcome describes the new-account email.
type Welcome struct {
	To, Name, TempPassword string
	Lang                   i18n.Lang
}

// Sender is what the auth and users packages depend on (fakeable in tests).
type Sender interface {
	SendOTP(ctx context.Context, m OTP) error
	SendWelcome(ctx context.Context, m Welcome) error
}

// Mailer is the SMTP implementation.
type Mailer struct {
	cfg *config.Config
	set *settings.Service
}

func New(cfg *config.Config, set *settings.Service) *Mailer { return &Mailer{cfg: cfg, set: set} }

var _ Sender = (*Mailer)(nil)

func esc(s string) string { return html.EscapeString(s) }

type logo struct {
	data []byte
	mime string
	name string
}

// headerLogo returns the logo for the (dark) email header: configured dark variant when it is a raster
// image, else the embedded default.
func (m *Mailer) headerLogo(ctx context.Context) logo {
	def := logo{}
	def.data, _ = assets.FS.ReadFile("logo-on-dark.png")
	def.mime, def.name = "image/png", "logo.png"
	if m.set == nil {
		return def
	}
	b, err := m.set.Branding(ctx)
	if err != nil || b.LogoDarkID == "" {
		return def
	}
	a, err := m.set.Asset(ctx, b.LogoDarkID)
	if err != nil || a.Mime == "image/svg+xml" { // mail clients do not render SVG
		return def
	}
	ext := map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/webp": "webp"}[a.Mime]
	return logo{data: a.Data, mime: a.Mime, name: "logo." + ext}
}

func (m *Mailer) layout(ctx context.Context, lang i18n.Lang, title, body string) string {
	primary, accent, company := "#0C234B", "#F59E0B", "BTN Manufacturing Ltd"
	if m.set != nil {
		if b, err := m.set.Branding(ctx); err == nil {
			primary, accent, company = b.PrimaryColor, b.AccentColor, b.LegalName
		}
	}
	return fmt.Sprintf(`<!doctype html><html><body style="margin:0;background:#eef2f7;font-family:Segoe UI,Noto Sans,Noto Sans Ethiopic,Arial,sans-serif;color:#0f172a">
<table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="background:#eef2f7;padding:32px 12px"><tr><td align="center">
<table role="presentation" width="560" cellpadding="0" cellspacing="0" style="max-width:560px;width:100%%;background:#ffffff;border-radius:14px;overflow:hidden;box-shadow:0 4px 18px rgba(12,35,75,.08)">
<tr><td style="background:%s;padding:24px 32px"><img src="cid:btnlogo" alt="%s" height="40" style="display:block;height:40px"></td></tr>
<tr><td style="height:4px;background:%s"></td></tr>
<tr><td style="padding:32px">
<h1 style="margin:0 0 16px;font-size:20px;color:%s">%s</h1>
%s
</td></tr>
<tr><td style="padding:18px 32px;background:#f8fafc;color:#64748b;font-size:12px;line-height:1.5">
%s<br>%s
</td></tr></table></td></tr></table></body></html>`,
		esc(primary), esc(company), esc(accent), esc(primary), esc(title), body,
		esc(i18n.T(lang, "mail.footer_company", "company", company)), esc(i18n.T(lang, "mail.footer_auto")))
}

// BuildOTP renders subject, text and HTML (exported for tests).
func (m *Mailer) BuildOTP(ctx context.Context, o OTP) (subject, text, htmlBody string) {
	login := o.Purpose == "LOGIN"
	pre := "mail.otp_reset_"
	if login {
		pre = "mail.otp_login_"
	}
	subject = i18n.T(o.Lang, pre+"subject", "code", o.Code)
	intro := i18n.T(o.Lang, pre+"intro")
	primary := "#0C234B"
	if m.set != nil {
		if b, err := m.set.Branding(ctx); err == nil {
			primary = b.PrimaryColor
		}
	}
	body := fmt.Sprintf(`
<p style="margin:0 0 8px;font-size:15px">%s</p>
<p style="margin:0 0 24px;font-size:15px;line-height:1.6">%s</p>
<div style="font-size:34px;letter-spacing:10px;font-weight:700;color:%s;background:#f1f5f9;border:1px solid #e2e8f0;border-radius:10px;padding:18px 0;text-align:center;font-family:Consolas,monospace">%s</div>
<p style="margin:24px 0 0;font-size:13px;color:#475569;line-height:1.6">%s<br>%s</p>
<p style="margin:16px 0 0;font-size:13px;color:#b45309;line-height:1.6">%s</p>`,
		esc(i18n.T(o.Lang, "mail.hello", "name", o.Name)), esc(intro), esc(primary), esc(o.Code),
		esc(i18n.T(o.Lang, "mail.code_expiry", "minutes", o.Minutes)),
		esc(i18n.T(o.Lang, "mail.requested", "when", dates.FormatDateTime(time.Now()), "ip", o.IP)),
		esc(i18n.T(o.Lang, "mail.not_you")))
	if o.Link != "" && !login {
		body = resetLinkBlock(o, primary) + body
	}
	htmlBody = m.layout(ctx, o.Lang, i18n.T(o.Lang, pre+"title"), body)
	text = fmt.Sprintf("%s\n\n%s: %s\n\n%s\n%s", intro, i18n.T(o.Lang, "mail.code_label"), o.Code,
		i18n.T(o.Lang, "mail.code_expiry", "minutes", o.Minutes), i18n.T(o.Lang, "mail.not_you"))
	if o.Link != "" && !login {
		text = fmt.Sprintf("%s\n%s\n%s\n\n%s\n\n", i18n.T(o.Lang, "mail.reset_link_intro"), o.Link,
			i18n.T(o.Lang, "mail.reset_link_expiry", "minutes", o.LinkMinutes), i18n.T(o.Lang, "mail.reset_or_code")) + text
	}
	return
}

// resetLinkBlock is the button + plain link shown above the code in a password reset email.
func resetLinkBlock(o OTP, primary string) string {
	return fmt.Sprintf(`
<p style="margin:0 0 16px;font-size:15px;line-height:1.6">%s</p>
<p style="margin:0 0 12px;text-align:center"><a href="%s" style="display:inline-block;background:%s;color:#ffffff;text-decoration:none;font-weight:600;font-size:15px;padding:12px 28px;border-radius:8px">%s</a></p>
<p style="margin:0 0 6px;font-size:12px;color:#475569;word-break:break-all">%s</p>
<p style="margin:0 0 20px;font-size:13px;color:#475569">%s</p>
<p style="margin:0 0 12px;font-size:14px;font-weight:600">%s</p>`,
		esc(i18n.T(o.Lang, "mail.reset_link_intro")), esc(o.Link), esc(primary), esc(i18n.T(o.Lang, "mail.reset_link_button")),
		esc(o.Link), esc(i18n.T(o.Lang, "mail.reset_link_expiry", "minutes", o.LinkMinutes)), esc(i18n.T(o.Lang, "mail.reset_or_code")))
}

// BuildWelcome renders the welcome email.
func (m *Mailer) BuildWelcome(ctx context.Context, w Welcome) (subject, text, htmlBody string) {
	subject = i18n.T(w.Lang, "mail.welcome_subject")
	url := m.cfg.AppURL
	body := fmt.Sprintf(`
<p style="margin:0 0 12px;font-size:15px">%s</p>
<p style="margin:0 0 16px;font-size:15px;line-height:1.6">%s</p>
<p style="margin:0 0 6px;font-size:14px">%s <a href="%s" style="color:#1d4ed8">%s</a></p>
<p style="margin:0 0 6px;font-size:14px">%s <strong>%s</strong></p>
<p style="margin:0 0 16px;font-size:14px">%s <strong style="font-family:Consolas,monospace">%s</strong></p>
<p style="margin:0;font-size:13px;color:#475569;line-height:1.6">%s</p>`,
		esc(i18n.T(w.Lang, "mail.hello", "name", w.Name)), esc(i18n.T(w.Lang, "mail.welcome_intro")),
		esc(i18n.T(w.Lang, "mail.signin_address")), esc(url), esc(url),
		esc(i18n.T(w.Lang, "mail.email_label")), esc(w.To),
		esc(i18n.T(w.Lang, "mail.temp_password_label")), esc(w.TempPassword),
		esc(i18n.T(w.Lang, "mail.welcome_note")))
	htmlBody = m.layout(ctx, w.Lang, i18n.T(w.Lang, "mail.welcome_title"), body)
	text = fmt.Sprintf("%s\n%s %s\n%s %s\n%s %s\n%s", i18n.T(w.Lang, "mail.welcome_intro"),
		i18n.T(w.Lang, "mail.signin_address"), url, i18n.T(w.Lang, "mail.email_label"), w.To,
		i18n.T(w.Lang, "mail.temp_password_label"), w.TempPassword, i18n.T(w.Lang, "mail.welcome_note"))
	return
}

func (m *Mailer) SendOTP(ctx context.Context, o OTP) error {
	if o.Lang == "" {
		o.Lang = i18n.Default
	}
	s, t, h := m.BuildOTP(ctx, o)
	return m.send(ctx, o.To, s, t, h)
}

func (m *Mailer) SendWelcome(ctx context.Context, w Welcome) error {
	if w.Lang == "" {
		w.Lang = i18n.Default
	}
	s, t, h := m.BuildWelcome(ctx, w)
	return m.send(ctx, w.To, s, t, h)
}

// Compose builds the RFC 5322 message (multipart/related → alternative + inline logo).
func (m *Mailer) Compose(ctx context.Context, to, subject, text, htmlBody string) []byte {
	lg := m.headerLogo(ctx)
	from := m.cfg.MailFrom
	if from == "" {
		from = "BTN Operations <noreply@localhost>"
	}
	var buf bytes.Buffer
	related := multipart.NewWriter(&buf)
	h := func(k, v string) { fmt.Fprintf(&buf, "%s: %s\r\n", k, v) }
	h("From", from)
	h("To", to)
	h("Subject", mime.BEncoding.Encode("utf-8", subject))
	h("Date", time.Now().UTC().Format(time.RFC1123Z))
	h("Message-ID", fmt.Sprintf("<%s@btn-ops>", db.NewID()))
	h("MIME-Version", "1.0")
	h("Content-Type", "multipart/related; boundary="+related.Boundary())
	buf.WriteString("\r\n")

	var altBuf bytes.Buffer
	alt := multipart.NewWriter(&altBuf)
	writeQP := func(ct, body string) {
		hd := textproto.MIMEHeader{}
		hd.Set("Content-Type", ct+"; charset=utf-8")
		hd.Set("Content-Transfer-Encoding", "quoted-printable")
		pw, _ := alt.CreatePart(hd)
		qw := quotedprintable.NewWriter(pw)
		qw.Write([]byte(body))
		qw.Close()
	}
	writeQP("text/plain", text)
	writeQP("text/html", htmlBody)
	alt.Close()

	hd := textproto.MIMEHeader{}
	hd.Set("Content-Type", "multipart/alternative; boundary="+alt.Boundary())
	pw, _ := related.CreatePart(hd)
	pw.Write(altBuf.Bytes())

	ih := textproto.MIMEHeader{}
	ih.Set("Content-Type", lg.mime+"; name=\""+lg.name+"\"")
	ih.Set("Content-Transfer-Encoding", "base64")
	ih.Set("Content-ID", "<btnlogo>")
	ih.Set("Content-Disposition", "inline; filename=\""+lg.name+"\"")
	iw, _ := related.CreatePart(ih)
	enc := base64.StdEncoding.EncodeToString(lg.data)
	for len(enc) > 76 {
		iw.Write([]byte(enc[:76] + "\r\n"))
		enc = enc[76:]
	}
	iw.Write([]byte(enc + "\r\n"))
	related.Close()
	return buf.Bytes()
}

func (m *Mailer) send(ctx context.Context, to, subject, text, htmlBody string) error {
	if m.cfg.SMTPHost == "" {
		slog.Warn("SMTP not configured; email not sent", "subject_len", len(subject))
		return fmt.Errorf("smtp not configured")
	}
	msg := m.Compose(ctx, to, subject, text, htmlBody)
	fromAddr := m.cfg.MailFrom
	if a, err := mail.ParseAddress(fromAddr); err == nil {
		fromAddr = a.Address
	}
	addr := net.JoinHostPort(m.cfg.SMTPHost, fmt.Sprint(m.cfg.SMTPPort))
	d := net.Dialer{Timeout: 15 * time.Second}
	tlsCfg := &tls.Config{ServerName: m.cfg.SMTPHost, MinVersion: tls.VersionTLS12}
	var conn net.Conn
	var err error
	if m.cfg.SMTPPort == 465 {
		conn, err = tls.DialWithDialer(&d, "tcp", addr, tlsCfg)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	c, err := smtp.NewClient(conn, m.cfg.SMTPHost)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if m.cfg.SMTPPort != 465 {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return fmt.Errorf("smtp server does not offer STARTTLS")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return err
		}
	}
	if m.cfg.SMTPUser != "" {
		if err := c.Auth(smtp.PlainAuth("", m.cfg.SMTPUser, m.cfg.SMTPPassword, m.cfg.SMTPHost)); err != nil {
			return err
		}
	}
	if err := c.Mail(fromAddr); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(bytes.ReplaceAll(bytes.ReplaceAll(msg, []byte("\r\n"), []byte("\n")), []byte("\n"), []byte("\r\n"))); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// Noop is a Sender that only records (tests / dev without SMTP).
type Noop struct {
	mu       sync.Mutex
	OTPs     []OTP
	Welcomes []Welcome
	Messages []Message         // generic notification/alert emails (message.go)
	Attached []AttachedMessage // messages with file attachments (attachments.go, P7 scheduled reports)
}

func (n *Noop) SendOTP(_ context.Context, m OTP) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.OTPs = append(n.OTPs, m)
	return nil
}
func (n *Noop) SendWelcome(_ context.Context, m Welcome) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.Welcomes = append(n.Welcomes, m)
	return nil
}

// LastOTP returns the most recent OTP sent.
func (n *Noop) LastOTP() (OTP, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.OTPs) == 0 {
		return OTP{}, false
	}
	return n.OTPs[len(n.OTPs)-1], true
}
