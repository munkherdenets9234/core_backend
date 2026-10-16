package mailer

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"
)

// Gmail's submission endpoint, used for local development only. Production
// sends through Brevo's HTTPS API (see mailer.go) because hosts such as Render
// block the SMTP ports. Port 587 with STARTTLS is the one Google documents.
const (
	DefaultSMTPHost = "smtp.gmail.com"
	DefaultSMTPPort = 587
)

// deliverSMTP sends one message over STARTTLS SMTP.
func (m *Mailer) deliverSMTP(to string, p payload) error {
	msg := buildMessage(p.Sender.Name, p.Sender.Email, to, p.Subject, p.TextContent)
	if p.HTMLContent != "" {
		msg = buildMessageAlternative(p.Sender.Name, p.Sender.Email, to, p.Subject, p.TextContent, p.HTMLContent)
	}
	addr := net.JoinHostPort(m.cfg.Host, fmt.Sprint(m.cfg.Port))

	// Dial with a timeout rather than smtp.SendMail, which uses no deadline
	// at all: a hung SMTP connection would otherwise pin the request that
	// triggered it until something upstream gave up.
	conn, err := net.DialTimeout("tcp", addr, m.cfg.Timeout)
	if err != nil {
		return fmt.Errorf("mailer: dial %s: %w", addr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(m.cfg.Timeout))

	c, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("mailer: smtp: %w", err)
	}
	defer func() { _ = c.Quit() }()

	// ServerName must be the host, not the host:port — a mismatch here is a
	// certificate error that reads like the server is untrustworthy.
	if err := c.StartTLS(&tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
		return fmt.Errorf("mailer: starttls: %w", err)
	}

	auth := smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)
	if err := c.Auth(auth); err != nil {
		return fmt.Errorf("mailer: auth failed (is GMAIL_PASSWORD a Google App Password, with 2FA on?): %w", err)
	}

	if err := c.Mail(m.cfg.FromAddress); err != nil {
		return fmt.Errorf("mailer: from: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("mailer: rcpt: %w", err)
	}

	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mailer: data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		_ = w.Close()
		return fmt.Errorf("mailer: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mailer: close: %w", err)
	}
	return nil
}

// buildMessage assembles RFC 5322 headers and a plain-text body.
//
// Plain text only, on purpose. An HTML reset mail buys nothing a link does
// not, and multipart assembly is a class of bug (boundary handling, encoding)
// with no upside for a six-line message.
func buildMessage(fromName, fromAddr, to, subject, body string) []byte {
	return buildMessageType("text/plain", fromName, fromAddr, to, subject, body)
}

// buildMessageType is buildMessage with the content type chosen by the
// caller: text/html for the development SMTP path of an HTML-bodied template.
func buildMessageType(contentType, fromName, fromAddr, to, subject, body string) []byte {
	var b strings.Builder
	writeHeaders(&b, fromName, fromAddr, to, subject)
	b.WriteString("Content-Type: " + contentType + "; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("\r\n")
	b.WriteString(normalizeNewlines(body))
	return []byte(b.String())
}

// buildMessageAlternative assembles a multipart/alternative message with a
// text part and an HTML part (the last, preferred one), both
// quoted-printable. QP encoding keeps every line far below RFC 5322's 998
// characters whatever the HTML looks like, and is safe for non-ASCII text.
func buildMessageAlternative(fromName, fromAddr, to, subject, text, htmlBody string) []byte {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, part := range []struct{ ctype, content string }{
		{"text/plain", text}, {"text/html", htmlBody},
	} {
		h := textproto.MIMEHeader{}
		h.Set("Content-Type", part.ctype+"; charset=UTF-8")
		h.Set("Content-Transfer-Encoding", "quoted-printable")
		pw, err := mw.CreatePart(h)
		if err != nil {
			break // writing to a bytes.Buffer cannot fail
		}
		qw := quotedprintable.NewWriter(pw)
		_, _ = qw.Write([]byte(normalizeNewlines(part.content)))
		_ = qw.Close()
	}
	_ = mw.Close()

	var b strings.Builder
	writeHeaders(&b, fromName, fromAddr, to, subject)
	b.WriteString("Content-Type: multipart/alternative; boundary=\"" + mw.Boundary() + "\"\r\n")
	b.WriteString("\r\n")
	b.WriteString(body.String())
	return []byte(b.String())
}

// writeHeaders writes the headers every message shares. Values pass through
// encodeHeader, so none can start a new header line.
func writeHeaders(b *strings.Builder, fromName, fromAddr, to, subject string) {
	b.WriteString("From: " + encodeHeader(fromName) + " <" + fromAddr + ">\r\n")
	b.WriteString("To: " + encodeHeader(to) + "\r\n")
	// A non-ASCII subject (the invoice one is Mongolian) goes out as an
	// RFC 2047 encoded word; pure ASCII passes through unchanged.
	b.WriteString("Subject: " + mime.QEncoding.Encode("UTF-8", encodeHeader(subject)) + "\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	// Reset mail is not a newsletter; keep it out of Promotions and out of
	// automatic replies.
	b.WriteString("Auto-Submitted: auto-generated\r\n")
}

// normalizeNewlines converts bare LF to CRLF as SMTP requires, without
// doubling the CR in text that already uses CRLF.
func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}
