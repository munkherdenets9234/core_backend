package mailer

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode"
)

// Template names one message this platform can send.
//
// A closed set, and that is the point. The endpoint in front of Send is
// reachable by any product holding a service key, so the alternative — a
// caller supplying its own subject and body — would make a leaked key into an
// open relay sending from our own address. Adding a message means adding it
// here, in a review, rather than at a call site in another repository.
type Template string

const (
	// TemplatePasswordReset carries a one-time link to set a new password.
	// digitalservice owns the token and the link; tenantcore only delivers.
	TemplatePasswordReset Template = "password_reset"

	// TemplatePasswordResetCode carries a short numeric code rather than a
	// link. Used by tenantcore's own console reset: a code can be read off a
	// phone and typed into the tab already open, where a link forces the
	// reset to finish in whichever browser opened the mail.
	TemplatePasswordResetCode Template = "password_reset_code"

	// TemplatePasswordChanged is the after-the-fact notice. It exists
	// because a password change the owner did not make is the one thing they
	// need to hear about immediately, and a reset flow with no notification
	// gives a thief a silent takeover.
	TemplatePasswordChanged Template = "password_changed"

	// TemplateSubscriptionExpiring tells the platform operator that a
	// tenant's subscription is about to lapse. Sent by the expiry notifier,
	// not by a product service, so it carries the tenant and plan names
	// rather than anything about a recipient's account.
	TemplateSubscriptionExpiring Template = "subscription_expiring"

	// TemplateStaffInvite invites a person to a tenant's product. The invite
	// link is minted by the product; fixed fields only, no free-text message,
	// so there is no body for a caller to write. Values still land in the
	// subject and body, so they are untrusted text: buildPayload strips CR/LF
	// from the subject and sender name (encodeHeader), and JSON encoding keeps
	// any value from becoming a mail header.
	TemplateStaffInvite Template = "staff_invite"

	// TemplateLeadNotification tells a tenant a visitor left an enquiry. The
	// lead's fields are inserted into fixed positions, never as the body.
	TemplateLeadNotification Template = "lead_notification"
)

type templateDef struct {
	subject string
	body    string
	// required lists the data keys the body needs. Checked before sending so
	// a missing key is an error naming the key, rather than a mail that goes
	// out with a blank space where the reset link should be.
	required []string
}

// The bodies are plain text with {{key}} placeholders. Deliberately not
// text/template: the inputs are a flat map of strings from another service,
// and a template language buys conditionals nobody needs while adding a way
// for caller-supplied data to be interpreted rather than inserted.
var templates = map[Template]templateDef{
	TemplatePasswordReset: {
		subject:  "Reset your {{app}} password",
		required: []string{"app", "name", "reset_url", "expires_in"},
		body: `Hello {{name}},

Someone asked to reset the password for your {{app}} account.

Open this link to choose a new one:

{{reset_url}}

The link expires in {{expires_in}}. If you did not ask for this, you can
ignore this message — your password will not change until the link is used.
`,
	},
	TemplatePasswordResetCode: {
		subject:  "{{code}} is your {{app}} password reset code",
		required: []string{"app", "name", "code", "expires_in"},
		// The code is in the subject as well as the body: it is the one thing
		// the reader wants, and it saves opening the mail at all on a phone.
		body: `Hello {{name}},

Your password reset code for {{app}} is:

    {{code}}

It expires in {{expires_in}} and can be used once.

If you did not ask to reset your password, ignore this message — your
password has not changed. Nobody can use this code without it.
`,
	},
	TemplateSubscriptionExpiring: {
		subject:  "{{tenant}}: subscription ends in {{days_left}} days",
		required: []string{"app", "tenant", "plan", "ends_on", "days_left"},
		body: `The {{plan}} subscription for {{tenant}} ends on {{ends_on}}, in
{{days_left}} days.

Once it lapses, that tenant's writes start returning 402 until it is
renewed. Reads keep working.

Renew it from the {{app}}, on the tenant's Subscription page.
`,
	},
	TemplateStaffInvite: {
		subject:  "{{inviter}} invited you to {{app}}",
		required: []string{"app", "name", "inviter", "invite_url", "expires_in"},
		body: `Hello {{name}},

{{inviter}} invited you to join {{app}}.

Open this link to accept and set your password:

{{invite_url}}

The invitation expires in {{expires_in}}. If you were not expecting it, you
can ignore this message.
`,
	},
	TemplateLeadNotification: {
		subject:  "New enquiry for {{tenant}}: {{lead_name}}",
		required: []string{"app", "tenant", "lead_name", "lead_contact", "listing", "lead_url"},
		body: `A new enquiry arrived for {{tenant}} through {{app}}.

From:     {{lead_name}}
Contact:  {{lead_contact}}
Listing:  {{listing}}

Open it here:

{{lead_url}}
`,
	},
	TemplatePasswordChanged: {
		subject:  "Your {{app}} password was changed",
		required: []string{"app", "name"},
		body: `Hello {{name}},

The password for your {{app}} account was just changed.

If this was you, nothing further is needed. If it was not, contact your
administrator immediately — someone else may have access to the account.
`,
	},
}

// Known reports whether name is a template this platform sends. Used by the
// endpoint so an unknown name is a 400 naming the valid options, rather than
// a 500 from deep inside the mailer.
func Known(name string) (Template, bool) {
	t := Template(name)
	_, ok := templates[t]
	return t, ok
}

// Names lists every template, sorted, for error messages and the contract.
func Names() []string {
	out := make([]string, 0, len(templates))
	for t := range templates {
		out = append(out, string(t))
	}
	sort.Strings(out)
	return out
}

func render(t Template, data map[string]string) (subject, body string, err error) {
	def, ok := templates[t]
	if !ok {
		return "", "", fmt.Errorf("mailer: unknown template %q (known: %s)", t, strings.Join(Names(), ", "))
	}

	var missing []string
	for _, k := range def.required {
		if strings.TrimSpace(data[k]) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return "", "", fmt.Errorf("mailer: template %q is missing data: %s", t, strings.Join(missing, ", "))
	}

	clean, err := sanitize(data)
	if err != nil {
		return "", "", fmt.Errorf("mailer: template %q: %w", t, err)
	}
	return substitute(def.subject, clean), substitute(def.body, clean), nil
}

const (
	// maxValueRunes caps a plain text value. Every value is untrusted (a
	// lead's name is typed by an anonymous visitor), and none of the fixed
	// fields needs more than a line.
	maxValueRunes = 200
	// maxURLLen bounds a link. Links are refused rather than truncated past
	// it, because a cut link is a broken link that still looks valid.
	maxURLLen = 2048
)

// httpsOnly lists the link values that must be absolute https URLs. Both are
// minted by a product service and land in mail a person is asked to click;
// anything else (http, javascript:, a relative path) is a caller bug or an
// attempt to point the reader somewhere else.
var httpsOnly = map[string]bool{"invite_url": true, "lead_url": true}

// sanitize returns a copy of data safe to insert into a fixed template.
//
// Plain values have line breaks turned into spaces and every other control
// or format character removed, so a value cannot add lines to the body (a visitor's lead_name
// faking a second "Listing:" line), and are capped at maxValueRunes. Link
// values (keys ending in _url) are never altered: one carrying a control
// character or longer than maxURLLen is refused, and httpsOnly keys must be
// absolute https URLs with a host.
func sanitize(data map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(data))
	for k, v := range data {
		if strings.HasSuffix(k, "_url") {
			if len(v) > maxURLLen {
				return nil, fmt.Errorf("%s is longer than %d bytes", k, maxURLLen)
			}
			if strings.IndexFunc(v, isUnsafeRune) >= 0 {
				return nil, fmt.Errorf("%s contains a control character", k)
			}
			if httpsOnly[k] {
				u, err := url.Parse(v)
				if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
					return nil, fmt.Errorf("%s must be an absolute https URL", k)
				}
			}
			out[k] = v
			continue
		}
		v = strings.Map(func(r rune) rune {
			switch {
			case r == '\r' || r == '\n' || r == '\u0085' || r == '\u2028' || r == '\u2029':
				return ' ' // a line break becomes a space, so words stay apart
			case isUnsafeRune(r):
				return -1
			}
			return r
		}, v)
		if r := []rune(v); len(r) > maxValueRunes {
			v = string(r[:maxValueRunes])
		}
		out[k] = v
	}
	return out, nil
}

// isUnsafeRune reports a rune that could break a line or hide text: C0/C1
// controls (CR, LF, NUL, ESC, NEL), the Unicode line and paragraph
// separators, and bidi/format characters.
func isUnsafeRune(r rune) bool {
	return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || unicode.Is(unicode.Cf, r)
}

// substitute replaces {{key}} with data[key].
//
// One pass over the known keys, so a value that itself contains "{{name}}"
// is inserted literally and not expanded — otherwise caller-supplied data
// could reach placeholders the template never intended to expose.
func substitute(s string, data map[string]string) string {
	if len(data) == 0 {
		return s
	}
	pairs := make([]string, 0, len(data)*2)
	for k, v := range data {
		pairs = append(pairs, "{{"+k+"}}", v)
	}
	return strings.NewReplacer(pairs...).Replace(s)
}
