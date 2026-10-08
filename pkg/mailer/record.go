package mailer

import (
	"context"
	"regexp"
	"strings"
	"time"

	"go.uber.org/zap"
)

// Mail log statuses.
const (
	StatusSent   = "sent"
	StatusFailed = "failed"
)

// SourceSystem marks mail tenantcore sends on its own account: password
// reset and the expiry notice. Mail sent for a product service carries that
// service client's name instead (see ServiceSource).
const SourceSystem = "system"

// maxErrorRunes bounds the failure text kept in the log. It is a hint for the
// operator ("brevo answered 401"), not a transcript of the provider's reply.
const maxErrorRunes = 200

// recordTimeout bounds the log insert. The log is a convenience: a slow
// database must not turn into a slow password reset.
const recordTimeout = 2 * time.Second

// Source says who asked for a send. It is bookkeeping for the mail log and
// never reaches the message.
type Source struct {
	// Name is SourceSystem, or the service client that called /svc.
	Name string
	// TenantID is the tenant the call was made for, when the caller said.
	TenantID string
}

// System is the Source for mail tenantcore sends itself.
var System = Source{Name: SourceSystem}

// ServiceSource is the Source for a send made on behalf of a product service.
func ServiceSource(name, tenantID string) Source {
	return Source{Name: name, TenantID: tenantID}
}

// Entry is one send attempt as the mail log keeps it.
//
// Deliberately narrow. There is no subject, no body and no template data, so
// a one-time code that travels through Send can never end up in the log:
// there is no field it could be written to. Error is the only free text, and
// it passes through safeError first.
type Entry struct {
	At       time.Time
	Template string
	To       string
	Status   string
	Error    string
	Source   string
	TenantID string
}

// Recorder stores log entries. Defined here, implemented by a repository, so
// this package does not import the storage layer.
type Recorder interface {
	Record(ctx context.Context, e Entry) error
}

// record writes one entry. A failure to log is logged and swallowed: the mail
// has already been sent or refused, and the log must never change that.
func (m *Mailer) record(src Source, tmpl Template, to string, data map[string]string, sendErr error) {
	if m.cfg.Recorder == nil {
		return
	}
	e := Entry{
		At:       time.Now().UTC(),
		Template: string(tmpl),
		To:       to,
		Status:   StatusSent,
		Source:   src.Name,
		TenantID: src.TenantID,
	}
	if e.Source == "" {
		e.Source = SourceSystem
	}
	if sendErr != nil {
		e.Status = StatusFailed
		e.Error = safeError(sendErr, data, m.secrets())
	}

	ctx, cancel := context.WithTimeout(context.Background(), recordTimeout)
	defer cancel()
	if err := m.cfg.Recorder.Record(ctx, e); err != nil && m.cfg.Log != nil {
		// Identifiers and outcome only: never the address, never the data.
		m.cfg.Log.Warn("mail log: write failed",
			zap.String("template", e.Template), zap.String("status", e.Status), zap.Error(err))
	}
}

// secrets lists the configured credentials, so safeError can strip them from
// a provider message that happens to echo one.
func (m *Mailer) secrets() []string {
	return []string{m.cfg.APIKey, m.cfg.Password}
}

var spaceRun = regexp.MustCompile(`\s+`)

// safeError turns a send error into short single-line text for the log.
//
// The provider's reply is quoted in the error, and a reply can echo what it
// was sent. So every template data value and every configured credential is
// removed first, whatever the error says. The text is then flattened to one
// line and cut to maxErrorRunes.
func safeError(err error, data map[string]string, secrets []string) string {
	s := err.Error()
	for _, v := range data {
		if v = strings.TrimSpace(v); len(v) >= 3 {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	for _, v := range secrets {
		if len(v) >= 3 {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	s = strings.TrimSpace(spaceRun.ReplaceAllString(s, " "))
	if r := []rune(s); len(r) > maxErrorRunes {
		s = string(r[:maxErrorRunes])
	}
	return s
}
