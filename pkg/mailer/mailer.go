// Package mailer sends transactional email over SMTP.
//
// tenantcore is the only service that holds mail credentials. Products do not
// send their own: they ask tenantcore to, over /svc/notifications, the same
// way they ask it about entitlements. One account, one place to rotate a
// password, one place to rate limit — instead of the same SMTP key copied into
// four .env files.
//
// It is deliberately small and template-driven. See Send: the caller names a
// template and supplies data, never a subject and body. That is what stops a
// leaked service key from turning this into a spam relay.
package mailer

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// Brevo's SMTP relay. Port 587 with STARTTLS rather than 465 with implicit
// TLS: both work, 587 is the submission standard.
const (
	DefaultHost = "smtp-relay.brevo.com"
	DefaultPort = 587
)

type Config struct {
	Host     string
	Port     int
	Username string // the SMTP login Brevo shows, e.g. xxxx@smtp-brevo.com
	Password string // a Brevo SMTP key, not the account password
	// FromAddress is the sender recipients see. It is NOT the SMTP login and
	// must be a sender verified in Brevo, or Brevo rejects the message. It is
	// configuration, never caller input, for the same reason as the template
	// rule on Send: a caller choosing From would make mail that says anything.
	FromAddress string
	FromName    string
	Timeout  time.Duration
}

// Mailer sends mail. A nil *Mailer is a valid "mail is not configured" value
// — every method is nil-safe, so callers need no nil check of their own.
type Mailer struct {
	cfg Config
}

// New returns nil when no credentials or no sender address are configured.
//
// Same rule as every other optional dependency: the process starts, says what
// is missing at startup and on /readyz, and the routes that need it answer
// FEATURE_UNAVAILABLE. A platform that refuses to boot because a mail
// password is absent has turned "password resets are unavailable" into "no
// tenant can do anything".
func New(cfg Config) *Mailer {
	if cfg.Username == "" || cfg.Password == "" || cfg.FromAddress == "" {
		return nil
	}
	if cfg.Host == "" {
		cfg.Host = DefaultHost
	}
	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	// A pasted key can carry stray spaces; they are never part of the secret,
	// and leaving them in produces an authentication failure that reads like
	// a wrong password.
	cfg.Password = strings.ReplaceAll(cfg.Password, " ", "")
	if cfg.FromName == "" {
		cfg.FromName = "Inno Nomads"
	}
	return &Mailer{cfg: cfg}
}

// Available reports whether mail is configured. Safe on a nil receiver.
func (m *Mailer) Available() bool { return m != nil }

// From is the address recipients will see. Empty when unconfigured.
func (m *Mailer) From() string {
	if m == nil {
		return ""
	}
	return m.cfg.FromAddress
}

// Send renders a named template and delivers it.
//
// The caller names a template; it never supplies a subject or body. This is
// the whole security model of the endpoint in front of it: a service key is a
// machine credential that lives in another service's environment, and the day
// one leaks, the blast radius should be "someone can trigger a password-reset
// email to an address" rather than "someone can send arbitrary mail from our
// domain to anyone".
//
// An unknown template is an error, not a silent no-op — a product asking for
// a template that does not exist has a bug, and swallowing it would mean the
// reset mail simply never arrives with nothing to say why.
func (m *Mailer) Send(to string, tmpl Template, data map[string]string) error {
	if !m.Available() {
		return errors.New("mailer: not configured")
	}
	if strings.TrimSpace(to) == "" {
		return errors.New("mailer: no recipient")
	}

	subject, body, err := render(tmpl, data)
	if err != nil {
		return err
	}

	msg := buildMessage(m.cfg.FromName, m.cfg.FromAddress, to, subject, body)
	addr := net.JoinHostPort(m.cfg.Host, fmt.Sprint(m.cfg.Port))

	return m.deliver(addr, to, msg)
}

func (m *Mailer) deliver(addr, to string, msg []byte) error {
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
		// The overwhelmingly common cause, worth naming rather than passing
		// Google's opaque 535 straight through.
		return fmt.Errorf("mailer: auth failed (are SMTP_USER and SMTP_PASSWORD a Brevo SMTP login and key, not the account login?): %w", err)
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
	b.WriteString("To: " + to + "\r\n")
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

// encodeHeader defends the headers against injection.
//
// A CR or LF in a subject or display name ends the header and starts a new
// one, which is how a "subject" becomes an extra Bcc. Nothing that reaches
// here should contain one; stripping is correct regardless, because the
// alternative is trusting every future caller.
func encodeHeader(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	return s
}

// normalizeNewlines converts bare LF to CRLF as SMTP requires, without
// doubling the CR in text that already uses CRLF.
func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}
