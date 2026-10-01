package mailer

import (
	"strings"
	"testing"
)

func TestRenderSubstitutesAndRequiresData(t *testing.T) {
	subject, body, err := render(TemplatePasswordResetCode, map[string]string{
		"app":        "Inno Nomads Console",
		"name":       "Munkh-Erdene",
		"code":       "048213",
		"expires_in": "10 minutes",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(subject, "048213") {
		t.Fatalf("code should be in the subject, got %q", subject)
	}
	for _, want := range []string{"Munkh-Erdene", "048213", "10 minutes"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q", want)
		}
	}
	if strings.Contains(body, "{{") {
		t.Fatalf("unsubstituted placeholder left in body: %q", body)
	}
}

// A missing key must be an error naming it, never a mail that goes out with a
// blank space where the code should be.
func TestRenderRejectsMissingData(t *testing.T) {
	_, _, err := render(TemplatePasswordResetCode, map[string]string{"app": "X", "name": "Y"})
	if err == nil {
		t.Fatal("expected an error for missing template data")
	}
	if !strings.Contains(err.Error(), "code") {
		t.Fatalf("error should name the missing key, got %v", err)
	}
}

func TestUnknownTemplateIsRejected(t *testing.T) {
	if _, ok := Known("send_anything_you_like"); ok {
		t.Fatal("unknown template reported as known")
	}
	if _, _, err := render(Template("nope"), nil); err == nil {
		t.Fatal("expected an error for an unknown template")
	}
}

// Caller-supplied data is inserted, never re-expanded. Otherwise a value
// containing a placeholder could reach parts of the template it was never
// meant to touch.
func TestSubstitutionDoesNotRecurse(t *testing.T) {
	_, body, err := render(TemplatePasswordResetCode, map[string]string{
		"app":        "X",
		"name":       "{{code}}",
		"code":       "111111",
		"expires_in": "10 minutes",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(body, "{{code}}") {
		t.Fatal("a value containing a placeholder must be inserted literally, not expanded")
	}
}

// A CR or LF in a header ends it and starts another — which is how a display
// name becomes an extra Bcc.
//
// The property is NOT that the text disappears: "Bcc:" sitting inside a
// display name is inert. It is that no injected text can start its own header
// LINE. Asserting the substring is absent would be testing the wrong thing and
// would fail on correct code.
func TestHeaderInjectionCannotCreateAHeaderLine(t *testing.T) {
	msg := string(buildMessage(
		"Evil\r\nBcc: victim@example.com",
		"me@gmail.com",
		"you@example.com",
		"Hi\nX-Injected: 1",
		"body",
	))

	headers, _, found := strings.Cut(msg, "\r\n\r\n")
	if !found {
		t.Fatalf("no header/body separator in:\n%s", msg)
	}
	for _, line := range strings.Split(headers, "\r\n") {
		name, _, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("malformed header line %q", line)
		}
		switch strings.TrimSpace(name) {
		case "Bcc", "X-Injected":
			t.Fatalf("injected text became its own header line: %q", line)
		}
	}
}

// Google shows app passwords as "abcd efgh ijkl mnop" and people paste them
// exactly as shown; the spaces are display formatting, not the secret.
func TestAppPasswordSpacesAreStripped(t *testing.T) {
	m := New(Config{Username: "me@gmail.com", Password: "abcd efgh ijkl mnop"})
	if m == nil {
		t.Fatal("expected a mailer")
	}
	if m.cfg.Password != "abcdefghijklmnop" {
		t.Fatalf("password = %q, want the spaces removed", m.cfg.Password)
	}
}

func TestNewReturnsNilWhenUnconfigured(t *testing.T) {
	for _, c := range []Config{{}, {Username: "me@gmail.com"}, {Password: "x"}} {
		if New(c) != nil {
			t.Fatalf("expected nil for %+v", c)
		}
	}
	var m *Mailer
	if m.Available() {
		t.Fatal("nil mailer reported available")
	}
	if err := m.Send("x@y.z", TemplatePasswordResetCode, nil); err == nil {
		t.Fatal("sending on an unconfigured mailer must error, not silently succeed")
	}
}

func TestSubscriptionExpiringRenders(t *testing.T) {
	subject, body, err := render(TemplateSubscriptionExpiring, map[string]string{
		"app":       "Inno Nomads Console",
		"tenant":    "E and S Discovery Mongolia",
		"plan":      "Travel Pro",
		"ends_on":   "2026-10-31",
		"days_left": "7",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{"E and S Discovery Mongolia", "7"} {
		if !strings.Contains(subject, want) {
			t.Fatalf("subject %q missing %q", subject, want)
		}
	}
	for _, want := range []string{"2026-10-31", "Travel Pro"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(subject, "{{") || strings.Contains(body, "{{") {
		t.Fatal("unsubstituted placeholder left in output")
	}
}

func TestSubscriptionExpiringRequiresAllData(t *testing.T) {
	_, _, err := render(TemplateSubscriptionExpiring, map[string]string{
		"app": "X", "tenant": "Y", "plan": "Z", "ends_on": "2026-10-31",
	})
	if err == nil {
		t.Fatal("expected an error when days_left is missing")
	}
	if !strings.Contains(err.Error(), "days_left") {
		t.Fatalf("error should name the missing key, got %v", err)
	}
}
