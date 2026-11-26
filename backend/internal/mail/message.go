package mail

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"btn-ops/internal/i18n"
)

// Message is a generic branded email (notifications, monitoring alerts) rendered with the same layout,
// logo and colours as the sign-in emails.
type Message struct {
	To, Name    string // Name "" = no greeting line
	Lang        i18n.Lang
	Subject     string
	Title       string   // heading inside the email (defaults to Subject)
	Paragraphs  []string // plain text, HTML-escaped when rendered
	Critical    bool     // red accent strip above the body
	ActionURL   string   // optional button
	ActionLabel string
	FooterNote  string // optional small print (e.g. why you receive this)
	FooterURL   string // optional link under the footer note
	FooterLabel string
}

// MessageSender is implemented by *Mailer and *Noop (notify/monitor depend on it, tests fake it).
type MessageSender interface {
	SendMessage(ctx context.Context, m Message) error
}

var _ MessageSender = (*Mailer)(nil)
var _ MessageSender = (*Noop)(nil)

// BuildMessage renders subject, text and HTML (exported for tests).
func (m *Mailer) BuildMessage(ctx context.Context, msg Message) (subject, text, htmlBody string) {
	lang := msg.Lang
	if lang == "" {
		lang = i18n.Default
	}
	subject = msg.Subject
	title := msg.Title
	if title == "" {
		title = subject
	}
	primary := "#0C234B"
	if m.set != nil {
		if b, err := m.set.Branding(ctx); err == nil {
			primary = b.PrimaryColor
		}
	}
	var hb, tb strings.Builder
	if msg.Critical {
		hb.WriteString(`<div style="margin:0 0 16px;padding:10px 14px;border-radius:8px;background:#fef2f2;border:1px solid #fecaca;color:#991b1b;font-size:13px;font-weight:600">` +
			esc(i18n.T(lang, "notify.mail.critical_banner")) + `</div>`)
	}
	if msg.Name != "" {
		g := i18n.T(lang, "mail.hello", "name", msg.Name)
		fmt.Fprintf(&hb, `<p style="margin:0 0 12px;font-size:15px">%s</p>`, esc(g))
		tb.WriteString(g + "\n\n")
	}
	for _, p := range msg.Paragraphs {
		if strings.TrimSpace(p) == "" {
			continue
		}
		fmt.Fprintf(&hb, `<p style="margin:0 0 12px;font-size:15px;line-height:1.6;white-space:pre-line">%s</p>`, esc(p))
		tb.WriteString(p + "\n\n")
	}
	if msg.ActionURL != "" {
		label := msg.ActionLabel
		if label == "" {
			label = msg.ActionURL
		}
		fmt.Fprintf(&hb, `<p style="margin:20px 0 4px"><a href="%s" style="display:inline-block;background:%s;color:#ffffff;text-decoration:none;font-weight:600;font-size:14px;padding:11px 20px;border-radius:9px">%s</a></p>`,
			esc(msg.ActionURL), esc(primary), esc(label))
		tb.WriteString(label + ": " + msg.ActionURL + "\n\n")
	}
	if msg.FooterNote != "" {
		fmt.Fprintf(&hb, `<p style="margin:24px 0 0;font-size:12px;color:#64748b;line-height:1.6">%s`, esc(msg.FooterNote))
		tb.WriteString(msg.FooterNote)
		if msg.FooterURL != "" {
			fl := msg.FooterLabel
			if fl == "" {
				fl = msg.FooterURL
			}
			fmt.Fprintf(&hb, ` <a href="%s" style="color:#1d4ed8">%s</a>`, esc(msg.FooterURL), esc(fl))
			tb.WriteString(" " + msg.FooterURL)
		}
		hb.WriteString(`</p>`)
		tb.WriteString("\n")
	}
	htmlBody = m.layout(ctx, lang, title, hb.String())
	text = strings.TrimSpace(tb.String()) + "\n"
	return
}

// SendMessage renders and sends msg over SMTP.
func (m *Mailer) SendMessage(ctx context.Context, msg Message) error {
	s, t, h := m.BuildMessage(ctx, msg)
	return m.send(ctx, msg.To, s, t, h)
}

// Probe checks that the SMTP server accepts TCP connections (no login, no message). Returns
// ErrNotConfigured when SMTP_HOST is empty.
func (m *Mailer) Probe(ctx context.Context) error {
	if m.cfg.SMTPHost == "" {
		return ErrNotConfigured
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(m.cfg.SMTPHost, fmt.Sprint(m.cfg.SMTPPort)))
	if err != nil {
		return err
	}
	return conn.Close()
}

// ErrNotConfigured: SMTP_HOST is not set.
var ErrNotConfigured = fmt.Errorf("smtp not configured")

// SendMessage records the message (tests / dev).
func (n *Noop) SendMessage(_ context.Context, m Message) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.Messages = append(n.Messages, m)
	return nil
}

// SentMessages returns a copy of the recorded generic messages.
func (n *Noop) SentMessages() []Message {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]Message(nil), n.Messages...)
}
