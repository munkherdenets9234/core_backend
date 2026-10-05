package mailer

import (
	"strings"
	"testing"
)

// A visitor-supplied value must not add lines to the body a tenant reads:
// "Sara\nListing: fake" would otherwise render as a second Listing line.
func TestBodyValuesCannotInjectLines(t *testing.T) {
	_, body, err := render(TemplateLeadNotification, map[string]string{
		"app": "Tower", "tenant": "Tower LLC", "lead_name": "Sara\r\nListing:  FAKE\u0085 x\x00\x1b‮",
		"lead_contact": "1", "listing": "A", "lead_url": "https://x/leads/1",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	n := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "Listing:") {
			n++
		}
	}
	if n != 1 || strings.Contains(body, "\r") {
		t.Fatalf("lead_name injected a line: %q", body)
	}
	for _, bad := range []string{"\x00", "\x1b", "\u0085", " ", "‮"} {
		if strings.Contains(body, bad) {
			t.Fatalf("control character %q survived: %q", bad, body)
		}
	}
}

func TestLongValuesAreCapped(t *testing.T) {
	long := strings.Repeat("é", 1000)
	_, body, err := render(TemplateLeadNotification, map[string]string{
		"app": "Tower", "tenant": "Tower LLC", "lead_name": long,
		"lead_contact": "1", "listing": "A", "lead_url": "https://x/leads/1",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(body, strings.Repeat("é", maxValueRunes+1)) {
		t.Fatal("a value longer than the cap was inserted whole")
	}
	if !strings.Contains(body, strings.Repeat("é", maxValueRunes)) {
		t.Fatal("the cap should keep the first maxValueRunes runes")
	}
}

// Link values are never truncated (a cut link is a broken link that still
// looks valid); an over-long one is refused instead.
func TestLinkValuesAreRefusedNotTruncated(t *testing.T) {
	_, _, err := render(TemplateStaffInvite, map[string]string{
		"app": "Tower", "name": "Bat", "inviter": "Dorj",
		"invite_url": "https://x/accept?t=" + strings.Repeat("a", maxURLLen), "expires_in": "7 days",
	})
	if err == nil {
		t.Fatal("an over-long link must be refused")
	}
	long := "https://x/accept?t=" + strings.Repeat("a", 300)
	_, body, err := render(TemplateStaffInvite, map[string]string{
		"app": "Tower", "name": "Bat", "inviter": "Dorj", "invite_url": long, "expires_in": "7 days",
	})
	if err != nil || !strings.Contains(body, long) {
		t.Fatalf("a 300-char link must go out whole: err=%v", err)
	}
}

func TestInviteAndLeadLinksMustBeHTTPS(t *testing.T) {
	for _, u := range []string{
		"http://x/accept", "javascript:alert(1)", "ftp://x", "https://", "//x/a", "x/a",
		"https://x\nBcc", "https://user@x/a",
	} {
		if _, _, err := render(TemplateStaffInvite, map[string]string{
			"app": "Tower", "name": "Bat", "inviter": "Dorj", "invite_url": u, "expires_in": "7 days",
		}); err == nil {
			t.Errorf("invite_url %q accepted", u)
		}
		if _, _, err := render(TemplateLeadNotification, map[string]string{
			"app": "Tower", "tenant": "T", "lead_name": "S", "lead_contact": "1", "listing": "A", "lead_url": u,
		}); err == nil {
			t.Errorf("lead_url %q accepted", u)
		}
	}
}
