package mail

// Messages with file attachments (P7, SRS RPT-02 scheduled reports: PDF / Excel / CSV). Same branded layout as
// SendMessage; the body is multipart/mixed { multipart/related { alternative(text, html), logo }, attachments… }.

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"

	"btn-ops/internal/db"
)

// Attachment is one file of a message.
type Attachment struct {
	Name        string // file name shown to the recipient ("BTN-1_production-summary_2026-11-23.pdf")
	ContentType string // "application/pdf", …
	Data        []byte
}

// AttachedMessage is a recorded message with attachments (Noop).
type AttachedMessage struct {
	Message     Message
	Attachments []Attachment
}

// AttachmentSender sends a branded message with attachments (implemented by *Mailer and *Noop).
type AttachmentSender interface {
	SendWithAttachments(ctx context.Context, m Message, files []Attachment) error
}

var _ AttachmentSender = (*Mailer)(nil)
var _ AttachmentSender = (*Noop)(nil)

// SendWithAttachments records the message (tests / dev).
func (n *Noop) SendWithAttachments(_ context.Context, m Message, files []Attachment) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.Attached = append(n.Attached, AttachedMessage{Message: m, Attachments: append([]Attachment(nil), files...)})
	return nil
}

// SentWithAttachments returns a copy of the recorded messages with attachments.
func (n *Noop) SentWithAttachments() []AttachedMessage {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]AttachedMessage(nil), n.Attached...)
}

// ComposeWithAttachments builds the RFC 5322 message (exported for tests).
func (m *Mailer) ComposeWithAttachments(ctx context.Context, msg Message, files []Attachment) []byte {
	subject, text, htmlBody := m.BuildMessage(ctx, msg)
	lg := m.headerLogo(ctx)
	from := m.cfg.MailFrom
	if from == "" {
		from = "BTN Operations <noreply@localhost>"
	}
	var buf bytes.Buffer
	mixed := multipart.NewWriter(&buf)
	h := func(k, v string) { fmt.Fprintf(&buf, "%s: %s\r\n", k, v) }
	h("From", from)
	h("To", msg.To)
	h("Subject", mime.BEncoding.Encode("utf-8", subject))
	h("Date", time.Now().UTC().Format(time.RFC1123Z))
	h("Message-ID", fmt.Sprintf("<%s@btn-ops>", db.NewID()))
	h("MIME-Version", "1.0")
	h("Content-Type", "multipart/mixed; boundary="+mixed.Boundary())
	buf.WriteString("\r\n")

	// related (alternative + inline logo)
	var relBuf bytes.Buffer
	related := multipart.NewWriter(&relBuf)
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
	ah := textproto.MIMEHeader{}
	ah.Set("Content-Type", "multipart/alternative; boundary="+alt.Boundary())
	pw, _ := related.CreatePart(ah)
	pw.Write(altBuf.Bytes())
	ih := textproto.MIMEHeader{}
	ih.Set("Content-Type", lg.mime+"; name=\""+lg.name+"\"")
	ih.Set("Content-Transfer-Encoding", "base64")
	ih.Set("Content-ID", "<btnlogo>")
	ih.Set("Content-Disposition", "inline; filename=\""+lg.name+"\"")
	iw, _ := related.CreatePart(ih)
	writeB64(iw, lg.data)
	related.Close()

	rh := textproto.MIMEHeader{}
	rh.Set("Content-Type", "multipart/related; boundary="+related.Boundary())
	rp, _ := mixed.CreatePart(rh)
	rp.Write(relBuf.Bytes())

	for _, f := range files {
		ct := f.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		name := mime.QEncoding.Encode("utf-8", strings.NewReplacer(`"`, "", "\r", "", "\n", "").Replace(f.Name))
		fh := textproto.MIMEHeader{}
		fh.Set("Content-Type", ct+"; name=\""+name+"\"")
		fh.Set("Content-Transfer-Encoding", "base64")
		fh.Set("Content-Disposition", "attachment; filename=\""+name+"\"")
		fw, _ := mixed.CreatePart(fh)
		writeB64(fw, f.Data)
	}
	mixed.Close()
	return buf.Bytes()
}

func writeB64(w interface{ Write([]byte) (int, error) }, data []byte) {
	enc := base64.StdEncoding.EncodeToString(data)
	for len(enc) > 76 {
		w.Write([]byte(enc[:76] + "\r\n"))
		enc = enc[76:]
	}
	w.Write([]byte(enc + "\r\n"))
}

// SendWithAttachments renders and sends msg with files over SMTP (same transport rules as SendMessage).
func (m *Mailer) SendWithAttachments(ctx context.Context, msg Message, files []Attachment) error {
	if m.cfg.SMTPHost == "" {
		return ErrNotConfigured
	}
	raw := m.ComposeWithAttachments(ctx, msg, files)
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
	_ = conn.SetDeadline(time.Now().Add(90 * time.Second))
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
	if err := c.Rcpt(msg.To); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(bytes.ReplaceAll(bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n")), []byte("\n"), []byte("\r\n"))); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
