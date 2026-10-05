package mailer

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
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
	var b strings.Builder
	b.WriteString("From: " + encodeHeader(fromName) + " <" + fromAddr + ">\r\n")
	b.WriteString("To: " + encodeHeader(to) + "\r\n")
	b.WriteString("Subject: " + encodeHeader(subject) + "\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	// Reset mail is not a newsletter; keep it out of Promotions and out of
	// automatic replies.
	b.WriteString("Auto-Submitted: auto-generated\r\n")
	b.WriteString("\r\n")
	b.WriteString(normalizeNewlines(body))
	return []byte(b.String())
}

// normalizeNewlines converts bare LF to CRLF as SMTP requires, without
// doubling the CR in text that already uses CRLF.
func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}
