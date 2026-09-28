package mailer

import (
	"fmt"
	"sort"
	"strings"
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

	return substitute(def.subject, data), substitute(def.body, data), nil
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
