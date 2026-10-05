// Package mailer sends transactional email through Brevo's HTTPS API.
//
// tenantcore is the only service that holds mail credentials. Products do not
// send their own: they ask tenantcore to, over /svc/notifications, the same
// way they ask it about entitlements. One key, one place to rotate it, one
// place to rate limit — instead of the same secret copied into four .env
// files.
//
// HTTPS rather than SMTP, because the hosts this runs on (Render) block the
// SMTP ports and a connection that times out on :587 tells you nothing about
// the credentials. Port 443 is never blocked.
//
// A Gmail SMTP transport exists for local development only (smtp.go); the
// caller chooses it by passing Username/Password instead of APIKey.
//
// It is deliberately small and template-driven. See Send: the caller names a
// template and supplies data, never a subject and body. That is what stops a
// leaked service key from turning this into a spam relay.
package mailer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultEndpoint is Brevo's transactional send call.
const DefaultEndpoint = "https://api.brevo.com/v3/smtp/email"

type Config struct {
	// APIKey is a Brevo API key (xkeysib-...), not an SMTP key.
	APIKey string
	// FromAddress is the sender recipients see. It must be a sender verified
	// in Brevo, or Brevo rejects the message. It is configuration, never
	// caller input, for the same reason as the template rule on Send: a
	// caller choosing From would make mail that says anything.
	FromAddress string
	FromName    string
	// Username/Password select the SMTP transport (Gmail, development only)
	// when APIKey is empty. Password is a Google App Password. FromAddress
	// defaults to Username, because Gmail rewrites a From it does not own.
	Host     string
	Port     int
	Username string
	Password string
	// Endpoint overrides DefaultEndpoint; tests point it at a local server.
	Endpoint string
	Timeout  time.Duration
}

// Mailer sends mail. A nil *Mailer is a valid "mail is not configured" value
// — every method is nil-safe, so callers need no nil check of their own.
type Mailer struct {
	cfg    Config
	client *http.Client
}

// New returns nil when neither transport is configured: an API key with a
// sender address (Brevo), or a username with a password (Gmail SMTP).
//
// Same rule as every other optional dependency: the process starts, says what
// is missing at startup and on /readyz, and the routes that need it answer
// FEATURE_UNAVAILABLE. A platform that refuses to boot because a mail key is
// absent has turned "password resets are unavailable" into "no tenant can do
// anything".
func New(cfg Config) *Mailer {
	// A pasted key can carry stray whitespace; it is never part of the secret.
	cfg.APIKey = strings.Join(strings.Fields(cfg.APIKey), "")
	cfg.FromAddress = strings.TrimSpace(cfg.FromAddress)
	if cfg.APIKey == "" {
		// Google shows app passwords as "abcd efgh ijkl mnop"; the spaces are
		// display formatting, not part of the secret.
		cfg.Password = strings.ReplaceAll(cfg.Password, " ", "")
		if cfg.Username == "" || cfg.Password == "" {
			return nil
		}
		if cfg.FromAddress == "" {
			cfg.FromAddress = cfg.Username
		}
		if cfg.Host == "" {
			cfg.Host = DefaultSMTPHost
		}
		if cfg.Port == 0 {
			cfg.Port = DefaultSMTPPort
		}
	} else if cfg.FromAddress == "" {
		return nil
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultEndpoint
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.FromName == "" {
		cfg.FromName = "Inno Nomads"
	}
	return &Mailer{cfg: cfg, client: &http.Client{Timeout: cfg.Timeout}}
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
	to = strings.TrimSpace(to)
	if to == "" {
		return errors.New("mailer: no recipient")
	}

	subject, body, err := render(tmpl, data)
	if err != nil {
		return err
	}

	p := buildPayload(m.cfg.FromName, m.cfg.FromAddress, to, subject, body)
	if m.cfg.APIKey == "" {
		return m.deliverSMTP(to, p)
	}
	return m.deliver(p)
}

// payload is the JSON Brevo's /v3/smtp/email accepts.
type payload struct {
	Sender      address   `json:"sender"`
	To          []address `json:"to"`
	Subject     string    `json:"subject"`
	TextContent string    `json:"textContent"`
}

type address struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

// buildPayload assembles the request body: plain text only, on purpose. An
// HTML reset mail buys nothing a link does not, and the content is a handful
// of lines.
//
// JSON encoding means a CR or LF in a value cannot start a new mail header
// the way it could when headers were written by hand, but subject and name
// are still single-line text, so encodeHeader keeps stripping them.
func buildPayload(fromName, fromAddr, to, subject, body string) payload {
	return payload{
		Sender:      address{Name: encodeHeader(fromName), Email: fromAddr},
		To:          []address{{Email: to}},
		Subject:     encodeHeader(subject),
		TextContent: body,
	}
}

func (m *Mailer) deliver(p payload) error {
	buf, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("mailer: encode: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, m.cfg.Endpoint, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("mailer: request: %w", err)
	}
	req.Header.Set("api-key", m.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("mailer: send: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Brevo answers 201 with a messageId. Anything else carries a JSON
	// {code, message}; surface it, bounded, because "key not found", "sender
	// not valid" and "IP not authorised" are all different fixes.
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK {
		return nil
	}
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	hint := ""
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		hint = " (is BREVO_API_KEY an API key, not an SMTP key, and is this server's IP authorised in Brevo?)"
	case http.StatusBadRequest:
		hint = " (is MAIL_FROM_EMAIL a sender verified in Brevo?)"
	}
	return fmt.Errorf("mailer: brevo answered %d%s: %s", resp.StatusCode, hint, strings.TrimSpace(string(detail)))
}

// encodeHeader keeps single-line text single-line.
//
// A CR or LF in a subject or display name has no business there. JSON keeps it
// from becoming a header, but a subject with a line break still renders
// oddly, and stripping is correct regardless: the alternative is trusting
// every future caller.
func encodeHeader(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	return s
}
