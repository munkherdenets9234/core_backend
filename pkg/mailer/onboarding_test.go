package mailer

import (
	"strconv"
	"strings"
	"testing"
)

func onboardingData() map[string]string {
	return map[string]string{
		"tenant": "Tower LLC", "domain_count": "2", "issuer_name": "InnoNomads LLC",
		"site_url": "https://tower.example.com", "admin_url": "https://admin.tower.example.com",
		"contact_person": "Мөнх-Эрдэнэ", "tech_contact_email": "tech@tower.example.com", "billing_contact_email": "billing@tower.example.com",

		"domain_1_name": "tower.example.com", "domain_1_primary": "1",
		"domain_1_registrant": "Tower LLC", "domain_1_owner": "client",
		"domain_1_registrar": "Namecheap", "domain_1_registrar_email": "reg@tower.example.com",
		"domain_1_dns": "Cloudflare", "domain_1_dns_email": "dns@tower.example.com",
		"domain_1_expires": "2027.03.01", "domain_1_autorenew": "1",
		"domain_1_deploy": "Vercel", "domain_1_env": "production",
		"domain_1_subs":      "admin.tower.example.com · admin · active\napi.tower.example.com · api · active",
		"domain_1_subs_more": "3",

		"domain_2_name": "tower.mn", "domain_2_registrant": "InnoNomads LLC", "domain_2_owner": "us",
		"domain_2_registrar": "Datacom", "domain_2_dns": "Datacom",
	}
}

// Every fixed section label that must appear in both bodies for the full data.
var onboardingLabels = []string{
	"ТӨСЛИЙН МЭДЭЭЛЭЛ", "Вэб сайт", "Админ хаяг", "ДОМЭЙН БА ЭЗЭМШИЛ", "Эзэмшигч", "Бүртгэгч", "DNS үйлчилгээ",
	"Дуусах огноо", "Автоматаар сунгах", "Хостинг", "Дэд домэйн", "ХОЛБОО БАРИХ", "Техникийн холбоо", "Төлбөрийн холбоо",
}

func TestOnboardingRendersTextAndHTML(t *testing.T) {
	subject, text, html, err := renderMail(TemplateOnboarding, onboardingData())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if subject != "Тавтай морилно уу — Tower LLC" {
		t.Fatalf("subject = %q", subject)
	}
	if !strings.HasPrefix(html, "<!DOCTYPE html>") {
		t.Fatalf("html must start with a doctype: %.40s", html)
	}
	values := append([]string{"Tower LLC", "tower.example.com", "tower.mn", "Namecheap", "Datacom", "Cloudflare", "Vercel",
		"2027.03.01", "tech@tower.example.com", "billing@tower.example.com", "https://tower.example.com", "InnoNomads LLC",
		"api.tower.example.com", "Бид", "Үйлчлүүлэгч"}, onboardingLabels...)
	for _, want := range values {
		if !strings.Contains(text, want) {
			t.Errorf("text body missing %q:\n%s", want, text)
		}
		if !strings.Contains(html, want) {
			t.Errorf("html body missing %q", want)
		}
	}
	if strings.Contains(text, "{{") || strings.Contains(html, "{{") {
		t.Fatal("unsubstituted placeholder left in the onboarding mail")
	}
}

func TestOnboardingHTMLEscapesValues(t *testing.T) {
	d := onboardingData()
	d["tenant"] = `<script>alert(1)</script>`
	d["domain_1_registrant"] = `<img src=x>`
	d["domain_1_subs"] = `<b>x</b> · admin · active`
	d["contact_person"] = `<marquee>x</marquee>`
	d["note"] = `<svg onload=alert(2)>`
	_, _, html, err := renderMail(TemplateOnboarding, d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, bad := range []string{"<script", "<img src=x>", "<b>", "<marquee", "<svg"} {
		if strings.Contains(html, bad) {
			t.Errorf("unescaped %q reached the HTML body", bad)
		}
	}
	for _, want := range []string{"&lt;script&gt;", "&lt;img src=x&gt;", "&lt;b&gt;x&lt;/b&gt;", "&lt;svg onload=alert(2)&gt;"} {
		if !strings.Contains(html, want) {
			t.Errorf("expected escaped %q in the HTML body", want)
		}
	}
}

func TestOnboardingSubstitutionDoesNotRecurse(t *testing.T) {
	d := onboardingData()
	d["note"] = "{{tenant}}"
	d["domain_1_registrant"] = "{{note_text}}"
	_, text, html, err := renderMail(TemplateOnboarding, d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for name, body := range map[string]string{"text": text, "html": html} {
		if !strings.Contains(body, "{{tenant}}") {
			t.Errorf("%s: a note placeholder was expanded", name)
		}
	}
	if !strings.Contains(text, "{{note_text}}") || !strings.Contains(html, "{{note_text}}") {
		t.Error("a registrant placeholder was expanded")
	}
}

func TestOnboardingOptionalRowsOmitted(t *testing.T) {
	d := map[string]string{
		"tenant": "Tower LLC", "domain_count": "1", "issuer_name": "InnoNomads LLC", "domain_1_name": "tower.example.com",
	}
	_, text, html, err := renderMail(TemplateOnboarding, d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for name, body := range map[string]string{"text": text, "html": html} {
		if !strings.Contains(body, "tower.example.com") {
			t.Errorf("%s: domain name missing", name)
		}
		for _, bad := range []string{"Регистр", "Эзэмшигч", "Бүртгэгч", "DNS үйлчилгээ", "Дуусах огноо", "Автоматаар сунгах", "Хостинг",
			"Дэд домэйн", "ХОЛБОО БАРИХ", "Техникийн холбоо", "Төлбөрийн холбоо", "ТАЙЛБАР", "Вэб сайт", "Админ хаяг", "Хариуцагч"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s: %q rendered although no value was given", name, bad)
			}
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasSuffix(strings.TrimRight(line, " "), ":") {
			t.Errorf("text has an empty row: %q", line)
		}
	}
	if strings.Contains(html, ": <") || strings.Contains(html, ":</") {
		t.Errorf("html has an empty row:\n%s", html)
	}
}

func TestOnboardingNoteCapped(t *testing.T) {
	d := onboardingData()
	d["note"] = strings.Repeat("н", 600)
	_, text, html, err := renderMail(TemplateOnboarding, d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for name, body := range map[string]string{"text": text, "html": html} {
		if !strings.Contains(body, strings.Repeat("н", 500)) || strings.Contains(body, strings.Repeat("н", 501)) {
			t.Errorf("%s: the note must be capped at 500 runes", name)
		}
	}
	if !strings.Contains(text, "ТАЙЛБАР") || !strings.Contains(html, "ТАЙЛБАР") {
		t.Error("note section missing")
	}
}

func TestOnboardingRequiredAndCount(t *testing.T) {
	d := onboardingData()
	delete(d, "tenant")
	if _, _, _, err := renderMail(TemplateOnboarding, d); err == nil || !strings.Contains(err.Error(), "tenant") {
		t.Fatalf("missing tenant must name the key: %v", err)
	}
	for name, mutate := range map[string]func(map[string]string){
		"count zero":       func(d map[string]string) { d["domain_count"] = "0" },
		"count not int":    func(d map[string]string) { d["domain_count"] = "two" },
		"count too large":  func(d map[string]string) { d["domain_count"] = strconv.Itoa(MaxOnboardingDomains + 1) },
		"missing name":     func(d map[string]string) { delete(d, "domain_2_name") },
		"count beyond set": func(d map[string]string) { d["domain_count"] = "3" },
		"no issuer":        func(d map[string]string) { delete(d, "issuer_name") },
	} {
		d := onboardingData()
		mutate(d)
		if _, _, _, err := renderMail(TemplateOnboarding, d); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}

	// The cap itself is accepted.
	d = onboardingData()
	d["domain_count"] = strconv.Itoa(MaxOnboardingDomains)
	for i := 1; i <= MaxOnboardingDomains; i++ {
		d["domain_"+strconv.Itoa(i)+"_name"] = "d" + strconv.Itoa(i) + ".example.com"
	}
	if _, _, html, err := renderMail(TemplateOnboarding, d); err != nil || !strings.Contains(html, "d50.example.com") {
		t.Fatalf("%d domains must render: %v", MaxOnboardingDomains, err)
	}
}

func TestOnboardingSubdomainsAreCapped(t *testing.T) {
	d := onboardingData()
	var lines []string
	for i := 1; i <= 12; i++ {
		lines = append(lines, "s"+strconv.Itoa(i)+".tower.example.com · web · active")
	}
	d["domain_1_subs"] = strings.Join(lines, "\n")
	d["domain_1_subs_more"] = "5"
	_, text, html, err := renderMail(TemplateOnboarding, d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for name, body := range map[string]string{"text": text, "html": html} {
		if !strings.Contains(body, "s10.tower.example.com") || strings.Contains(body, "s11.tower.example.com") {
			t.Errorf("%s: only %d subdomains may be listed", name, maxOnboardingSubdomains)
		}
		if !strings.Contains(body, "+7") {
			t.Errorf("%s: the omitted count (service count plus lines over the cap) is missing", name)
		}
	}
}

func TestOnboardingIsInternal(t *testing.T) {
	if _, ok := Known("onboarding"); !ok {
		t.Fatal("onboarding must be a known template")
	}
	if _, ok := KnownForService("onboarding"); ok {
		t.Fatal("onboarding must not be sendable through /svc")
	}
	if strings.Contains(strings.Join(ServiceNames(), ","), "onboarding") {
		t.Fatal("onboarding must not be listed for services")
	}
}

func TestOnboardingUnsafeURLRefused(t *testing.T) {
	for key, val := range map[string]string{"site_url": "http://x.example", "admin_url": "javascript:alert(1)"} {
		d := onboardingData()
		d[key] = val
		if _, _, _, err := renderMail(TemplateOnboarding, d); err == nil {
			t.Errorf("%s = %q must be refused", key, val)
		}
	}
}

func TestOnboardingSubjectHasNoLineBreak(t *testing.T) {
	d := onboardingData()
	d["tenant"] = "Tower\r\nBcc: evil@example.com"
	subject, _, _, err := renderMail(TemplateOnboarding, d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.ContainsAny(subject, "\r\n") {
		t.Fatalf("subject carries a line break: %q", subject)
	}
}

// Preview equals send, and both bodies carry one intro: the same sentence in
// the text and (escaped) in the HTML.
func TestOnboardingIntroIsIdenticalInTextAndHTML(t *testing.T) {
	d := onboardingData()
	d["issuer_name"] = `Inno & <Nomads>`
	_, text, html, err := renderMail(TemplateOnboarding, d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	intro := onbIntroBefore + d["issuer_name"] + onbIntroAfter
	if !strings.Contains(text, intro) {
		t.Errorf("text intro differs:\n%s", text)
	}
	if !strings.Contains(html, onbIntroBefore+"<strong>"+"Inno &amp; &lt;Nomads&gt;"+"</strong>"+onbIntroAfter) {
		t.Errorf("html intro differs")
	}
}

// Only domain_N_subs keeps its line breaks; any other key ending in _subs is
// a plain value and is flattened.
func TestSanitizeSubsExceptionIsNarrow(t *testing.T) {
	out, err := sanitize(map[string]string{"foo_subs": "a\nb", "domain_1_subs": "a\nb"})
	if err != nil {
		t.Fatal(err)
	}
	if out["foo_subs"] != "a b" {
		t.Errorf("foo_subs = %q, want newlines flattened", out["foo_subs"])
	}
	if out["domain_1_subs"] != "a\nb" {
		t.Errorf("domain_1_subs = %q, want newline kept", out["domain_1_subs"])
	}
}
