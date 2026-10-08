package mailer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// Subject and sender name are single-line text: a CR or LF in either is
// stripped, whatever put it there.
func TestSingleLineFieldsCannotCarryLineBreaks(t *testing.T) {
	p := buildPayload(
		"Evil\r\nBcc: victim@example.com",
		"me@example.com",
		"you@example.com",
		"Hi\nX-Injected: 1",
		"body",
	)
	for name, v := range map[string]string{"sender name": p.Sender.Name, "subject": p.Subject} {
		if strings.ContainsAny(v, "\r\n") {
			t.Fatalf("%s kept a line break: %q", name, v)
		}
	}
}

// A pasted key can carry stray whitespace; it is not part of the secret.
func TestAPIKeyWhitespaceIsStripped(t *testing.T) {
	m := New(Config{APIKey: " xkeysib-abc def\n", FromAddress: "me@example.com"})
	if m == nil {
		t.Fatal("expected a mailer")
	}
	if m.cfg.APIKey != "xkeysib-abcdef" {
		t.Fatalf("key = %q, want the whitespace removed", m.cfg.APIKey)
	}
}

// The request must carry the key header, the verified sender and the rendered
// template, and nothing the caller could have chosen.
func TestSendPostsToBrevo(t *testing.T) {
	var got struct {
		key, ctype string
		body       payload
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.key = r.Header.Get("api-key")
		got.ctype = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"messageId":"<x@y>"}`))
	}))
	defer srv.Close()

	m := New(Config{APIKey: "xkeysib-test", FromAddress: "no-reply@example.com", FromName: "Console", Endpoint: srv.URL})
	err := m.Send("me@example.com", TemplatePasswordResetCode, map[string]string{
		"app": "Console", "name": "Bat", "code": "048213", "expires_in": "10 minutes",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.key != "xkeysib-test" || got.ctype != "application/json" {
		t.Fatalf("headers: key=%q content-type=%q", got.key, got.ctype)
	}
	if got.body.Sender.Email != "no-reply@example.com" || got.body.Sender.Name != "Console" {
		t.Fatalf("sender = %+v", got.body.Sender)
	}
	if len(got.body.To) != 1 || got.body.To[0].Email != "me@example.com" {
		t.Fatalf("to = %+v", got.body.To)
	}
	if !strings.Contains(got.body.Subject, "048213") || !strings.Contains(got.body.TextContent, "048213") {
		t.Fatalf("code missing from subject %q / body %q", got.body.Subject, got.body.TextContent)
	}
}

// A refusal from Brevo must surface as an error naming the status and the
// reason, so a rejected key or sender is visible in the log.
func TestSendReportsBrevoRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"unauthorized","message":"Key not found"}`))
	}))
	defer srv.Close()

	m := New(Config{APIKey: "k", FromAddress: "a@example.com", Endpoint: srv.URL})
	err := m.Send("me@example.com", TemplatePasswordResetCode, map[string]string{
		"app": "X", "name": "Y", "code": "1", "expires_in": "1m",
	})
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Key not found") {
		t.Fatalf("err = %v, want it to name 401 and the reason", err)
	}
}

func TestNewReturnsNilWhenUnconfigured(t *testing.T) {
	for _, c := range []Config{{}, {APIKey: "k"}, {FromAddress: "a@example.com"}, {APIKey: "  "}} {
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

func TestKnownTemplatesIncludeStaffInviteAndLeadNotification(t *testing.T) {
	for _, name := range []string{"staff_invite", "lead_notification"} {
		if _, ok := Known(name); !ok {
			t.Errorf("template %q is not registered", name)
		}
	}

	subject, body, err := render(TemplateStaffInvite, map[string]string{
		"app": "Tower", "name": "Bat", "inviter": "Dorj", "invite_url": "https://x/accept?t=1", "expires_in": "7 days",
	})
	if err != nil {
		t.Fatalf("staff_invite: %v", err)
	}
	if strings.Contains(subject+body, "{{") || !strings.Contains(body, "https://x/accept?t=1") {
		t.Fatalf("staff_invite did not render fully: %q", body)
	}

	subject, body, err = render(TemplateLeadNotification, map[string]string{
		"app": "Tower", "tenant": "Tower LLC", "lead_name": "Sara", "lead_contact": "99112233", "listing": "Unit 12A", "lead_url": "https://x/leads/1",
	})
	if err != nil {
		t.Fatalf("lead_notification: %v", err)
	}
	if strings.Contains(subject+body, "{{") || !strings.Contains(body, "Unit 12A") {
		t.Fatalf("lead_notification did not render fully: %q", body)
	}

	if _, _, err := render(TemplateLeadNotification, map[string]string{"app": "Tower"}); err == nil {
		t.Fatal("missing data must be an error")
	}
}

// A visitor-controlled value in a subject must not leave a line break in the
// single-line fields (e.g. to fake an extra Bcc).
func TestSubjectValuesCannotInjectLineBreaks(t *testing.T) {
	evil := "x\r\nBcc: evil@example.com"
	for _, c := range []struct {
		tmpl Template
		data map[string]string
	}{
		{TemplateLeadNotification, map[string]string{
			"app": "Tower", "tenant": "Tower LLC", "lead_name": evil,
			"lead_contact": "1", "listing": "A", "lead_url": "https://x",
		}},
		{TemplateStaffInvite, map[string]string{
			"app": "Tower", "name": "Bat", "inviter": evil,
			"invite_url": "https://x", "expires_in": "7 days",
		}},
	} {
		subject, body, err := render(c.tmpl, c.data)
		if err != nil {
			t.Fatalf("%s: %v", c.tmpl, err)
		}
		p := buildPayload("Tower", "from@example.com", "to@example.com", subject, body)
		if strings.ContainsAny(p.Subject, "\r\n") {
			t.Fatalf("%s: subject kept a line break: %q", c.tmpl, p.Subject)
		}
	}
}

// Development-only Gmail transport: username and app password select it, the
// pasted spaces are dropped, and the sender defaults to the login.
func TestGmailTransportConfig(t *testing.T) {
	m := New(Config{Username: "me@gmail.com", Password: "abcd efgh ijkl mnop"})
	if m == nil {
		t.Fatal("expected a mailer")
	}
	if m.cfg.Password != "abcdefghijklmnop" || m.cfg.FromAddress != "me@gmail.com" || m.cfg.Host != DefaultSMTPHost || m.cfg.Port != DefaultSMTPPort {
		t.Fatalf("cfg = %+v", m.cfg)
	}
	if m.From() != "me@gmail.com" {
		t.Fatalf("From() = %q", m.From())
	}
}

func TestTenantPromotedRenders(t *testing.T) {
	if _, ok := Known("tenant_promoted"); !ok {
		t.Fatal("tenant_promoted should be a known template")
	}

	subject, body, err := render(TemplatePromoted, map[string]string{
		"app":         "Inno Nomads Console",
		"tenant":      "E and S Discovery Mongolia",
		"slug":        "es-discovery",
		"promoted_by": "Munkh-Erdene",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// Subject must have the tenant name
	if !strings.Contains(subject, "E and S Discovery Mongolia") {
		t.Fatalf("subject %q should contain tenant name", subject)
	}

	// Body must have slug and promoted_by
	for _, want := range []string{"es-discovery", "Munkh-Erdene"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q", want)
		}
	}

	// Body must have the full fixed closing sentence
	if !strings.Contains(body, "Open Tenants in the platform admin to set its plan and subscription.") {
		t.Fatalf("body missing full closing sentence: %q", body)
	}

	// No unsubstituted placeholders
	if strings.Contains(subject, "{{") || strings.Contains(body, "{{") {
		t.Fatal("unsubstituted placeholder left in output")
	}
}

func TestTenantPromotedRequiresAllKeys(t *testing.T) {
	data := map[string]string{
		"app":         "Console",
		"tenant":      "Tenant Name",
		"slug":        "tenant-slug",
		"promoted_by": "Admin User",
	}

	// Test dropping each required key
	requiredKeys := []string{"app", "tenant", "slug", "promoted_by"}
	for _, keyToDrop := range requiredKeys {
		testData := make(map[string]string)
		for k, v := range data {
			if k != keyToDrop {
				testData[k] = v
			}
		}

		_, _, err := render(TemplatePromoted, testData)
		if err == nil {
			t.Fatalf("expected an error when %q is missing", keyToDrop)
		}
		if !strings.Contains(err.Error(), keyToDrop) {
			t.Fatalf("error should name the missing key %q, got %v", keyToDrop, err)
		}
	}
}

func TestTenantPromotedSubjectCannotCarryLineBreaks(t *testing.T) {
	subject, body, err := render(TemplatePromoted, map[string]string{
		"app":         "Console",
		"tenant":      "x\r\nBcc: a@b.c",
		"slug":        "slug",
		"promoted_by": "user",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	p := buildPayload("Console", "from@example.com", "to@example.com", subject, body)
	if strings.ContainsAny(p.Subject, "\r\n") {
		t.Fatalf("subject kept a line break: %q", p.Subject)
	}
}

func TestTenantPromotedBodyHasNoKeyOrLink(t *testing.T) {
	subject, body, err := render(TemplatePromoted, map[string]string{
		"app":         "Console",
		"tenant":      "Tenant Name",
		"slug":        "tenant-slug",
		"promoted_by": "Admin User",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// No http/https links
	if strings.Contains(subject, "http") || strings.Contains(body, "http") {
		t.Fatal("body or subject should not contain http")
	}

	// No api_key
	if strings.Contains(subject, "api_key") || strings.Contains(body, "api_key") {
		t.Fatal("body or subject should not contain api_key")
	}
}

// With an API key set, Brevo wins even if Gmail credentials are also present.
func TestBrevoWinsOverGmail(t *testing.T) {
	m := New(Config{APIKey: "k", FromAddress: "a@example.com", Username: "me@gmail.com", Password: "x"})
	if m == nil || m.cfg.APIKey == "" || m.cfg.FromAddress != "a@example.com" {
		t.Fatalf("m = %+v", m)
	}
}

// Hand-written SMTP headers must not be able to start a new header line.
func TestSMTPHeadersCannotCarryLineBreaks(t *testing.T) {
	msg := string(buildMessage("Evil\r\nBcc: v@example.com", "me@gmail.com", "you@example.com\r\nBcc: v@example.com", "Hi\nX-Injected: 1", "body"))
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

func requestNotificationData() map[string]string {
	return map[string]string{
		"app":             "Tower",
		"tenant":          "Tower LLC",
		"request_type":    "viewing request",
		"summary":         "Sara asked to view Unit 12A on Friday",
		"admin_url":       "https://admin.example.com/requests/42",
		"unsubscribe_url": "https://admin.example.com/unsubscribe?t=abc",
	}
}

func TestRequestNotificationRenders(t *testing.T) {
	if _, ok := Known("request_notification"); !ok {
		t.Fatal("request_notification is not registered")
	}
	data := requestNotificationData()
	subject, body, err := render(TemplateRequestNotification, data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(subject, data["request_type"]) || !strings.Contains(subject, data["tenant"]) {
		t.Fatalf("subject = %q", subject)
	}
	for _, k := range []string{"admin_url", "summary", "unsubscribe_url"} {
		if !strings.Contains(body, data[k]) {
			t.Errorf("body is missing %s %q:\n%s", k, data[k], body)
		}
	}
	if strings.Contains(subject+body, "{{") {
		t.Fatalf("unrendered placeholder:\n%s\n%s", subject, body)
	}
}

func TestRequestNotificationRequiresAllKeys(t *testing.T) {
	for k := range requestNotificationData() {
		data := requestNotificationData()
		delete(data, k)
		_, _, err := render(TemplateRequestNotification, data)
		if err == nil {
			t.Errorf("dropping %q must be an error", k)
			continue
		}
		if !strings.Contains(err.Error(), k) {
			t.Errorf("error should name %q, got %v", k, err)
		}
	}
}

func TestRequestNotificationSubjectCannotCarryLineBreaks(t *testing.T) {
	data := requestNotificationData()
	data["request_type"] = "x\r\nBcc: a@b.c"
	subject, body, err := render(TemplateRequestNotification, data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.ContainsAny(subject, "\r\n") {
		t.Fatalf("subject kept a line break: %q", subject)
	}
	p := buildPayload("Tower", "from@example.com", "to@example.com", subject, body)
	if strings.ContainsAny(p.Subject, "\r\n") {
		t.Fatalf("payload subject kept a line break: %q", p.Subject)
	}
}

func TestRequestNotificationRejectsNonHTTPSLinks(t *testing.T) {
	for _, k := range []string{"admin_url", "unsubscribe_url"} {
		for _, bad := range []string{"http://x.example.com/a", "javascript:alert(1)", "/relative/path"} {
			data := requestNotificationData()
			data[k] = bad
			if _, _, err := render(TemplateRequestNotification, data); err == nil {
				t.Errorf("%s=%q must be rejected", k, bad)
			}
		}
	}
}
