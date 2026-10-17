package mailer

import (
	"fmt"
	"html"
	"strconv"
	"strings"
)

// MaxOnboardingDomains is the most domain records the onboarding template
// renders.
const MaxOnboardingDomains = 50

// maxOnboardingSubdomains is the most subdomains listed under one domain; the
// rest are summarised as a count.
const maxOnboardingSubdomains = 10

// onboardingKeys are the data keys the onboarding template reads, besides the
// numbered domain keys (N from 1 to domain_count).
//
//	tenant, domain_count, issuer_name (the operating company)     (required)
//	site_url, admin_url                                          (optional, https only)
//	contact_person, tech_contact_email, billing_contact_email    (optional)
//	note                                                         (optional, up to 500 runes)
//	domain_N_name                                                (required per domain)
//	domain_N_primary ("1"), domain_N_registrant, domain_N_owner (us or client)
//	domain_N_registrar, domain_N_registrar_email, domain_N_dns, domain_N_dns_email
//	domain_N_expires (display date), domain_N_autorenew ("1")
//	domain_N_deploy, domain_N_env
//	domain_N_subs (one subdomain per line, "name · purpose · status"), domain_N_subs_more (omitted count)
//	                                                             (all optional per domain)
//
// Dates arrive already formatted. Every value passes through sanitize first
// and is HTML-escaped where it enters the HTML body. Optional rows are left
// out when their value is empty. The mail carries no credentials: only account
// identifiers and emails already stored with the domain records.

// Mongolian labels of the onboarding mail.
const (
	lblWelcome    = "Тавтай морилно уу"
	lblProject    = "ТӨСЛИЙН МЭДЭЭЛЭЛ"
	lblSite       = "Вэб сайт"
	lblAdmin      = "Админ хаяг"
	lblDomains    = "ДОМЭЙН БА ЭЗЭМШИЛ"
	lblRegistrant = "Регистрлэгч"
	lblOwner      = "Эзэмшигч"
	lblRegistrar  = "Бүртгэгч"
	lblDNS        = "DNS үйлчилгээ"
	lblExpires    = "Дуусах огноо"
	lblAutoRenew  = "Автоматаар сунгах"
	lblHosting    = "Хостинг"
	lblSubdomains = "Дэд домэйн"
	lblContacts   = "ХОЛБОО БАРИХ"
	lblTech       = "Техникийн холбоо"
	lblBilling    = "Төлбөрийн холбоо"
	lblOnbNote    = "ТАЙЛБАР"

	onbPrimary   = "үндсэн"
	onbYes       = "Тийм"
	onbOwnerUs   = "Бид"
	onbOwnerThem = "Үйлчлүүлэгч"
	onbMore      = "бусад"

	// The intro is fixed copy; deriveOnboarding builds the plain text intro
	// from the same parts (intro_text), so both bodies read alike.
	onbIntroBefore = "Таны төслийг "
	onbIntroAfter  = " байгуулж, ажиллуулахад бэлэн боллоо. Доор таны домэйнуудын эзэмшил, байршуулалтын мэдээллийг нэг дор хавсаргав."
)

// onboardingRow is one label and value of the mail. Rows with an empty value
// are never built.
type onboardingRow struct{ label, value string }

func onboardingDomainCount(clean map[string]string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(clean["domain_count"]))
	if err != nil || n < 1 || n > MaxOnboardingDomains {
		return 0, fmt.Errorf("domain_count must be a number from 1 to %d", MaxOnboardingDomains)
	}
	return n, nil
}

// onboardingOwner is the display text of an owner value, "" for anything
// other than us or client.
func onboardingOwner(owner string) string {
	switch strings.TrimSpace(owner) {
	case "us":
		return onbOwnerUs
	case "client":
		return onbOwnerThem
	}
	return ""
}

// joinNonEmpty joins the non-blank parts with sep.
func joinNonEmpty(sep string, parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}

// withEmail renders "name (email)", or whichever of the two exists.
func withEmail(name, email string) string {
	name, email = strings.TrimSpace(name), strings.TrimSpace(email)
	if name != "" && email != "" {
		return name + " (" + email + ")"
	}
	return name + email
}

// onboardingDomainRows are the rows of domain k, in display order, without the
// subdomains. Empty values are left out.
func onboardingDomainRows(clean map[string]string, k string) []onboardingRow {
	g := func(s string) string { return strings.TrimSpace(clean["domain_"+k+"_"+s]) }
	autoRenew := ""
	if g("autorenew") == "1" {
		autoRenew = onbYes
	}
	all := []onboardingRow{
		{lblRegistrant, g("registrant")},
		{lblOwner, onboardingOwner(g("owner"))},
		{lblRegistrar, withEmail(g("registrar"), g("registrar_email"))},
		{lblDNS, withEmail(g("dns"), g("dns_email"))},
		{lblExpires, g("expires")},
		{lblAutoRenew, autoRenew},
		{lblHosting, joinNonEmpty(" · ", g("deploy"), g("env"))},
	}
	rows := all[:0]
	for _, r := range all {
		if r.value != "" {
			rows = append(rows, r)
		}
	}
	return rows
}

// onboardingSubdomains returns the subdomain lines of domain k, at most
// maxOnboardingSubdomains of them, and how many were left out (the service's
// own count plus any lines beyond the cap).
func onboardingSubdomains(clean map[string]string, k string) (lines []string, more int) {
	for _, l := range strings.Split(clean["domain_"+k+"_subs"], "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if n, err := strconv.Atoi(strings.TrimSpace(clean["domain_"+k+"_subs_more"])); err == nil && n > 0 {
		more = n
	}
	if len(lines) > maxOnboardingSubdomains {
		more += len(lines) - maxOnboardingSubdomains
		lines = lines[:maxOnboardingSubdomains]
	}
	return lines, more
}

// onboardingSiteRows are the site and admin addresses, when known.
func onboardingSiteRows(clean map[string]string) []onboardingRow {
	var rows []onboardingRow
	if v := strings.TrimSpace(clean["site_url"]); v != "" {
		rows = append(rows, onboardingRow{lblSite, v})
	}
	if v := strings.TrimSpace(clean["admin_url"]); v != "" {
		rows = append(rows, onboardingRow{lblAdmin, v})
	}
	return rows
}

// onboardingContactRows are the contacts of the mail: the platform contact
// person, then the domain tech and billing contacts.
func onboardingContactRows(clean map[string]string) []onboardingRow {
	var rows []onboardingRow
	if v := strings.TrimSpace(clean["contact_person"]); v != "" {
		rows = append(rows, onboardingRow{lblContact, v + " · " + strings.TrimSpace(clean["issuer_name"])})
	}
	if v := strings.TrimSpace(clean["tech_contact_email"]); v != "" {
		rows = append(rows, onboardingRow{lblTech, v})
	}
	if v := strings.TrimSpace(clean["billing_contact_email"]); v != "" {
		rows = append(rows, onboardingRow{lblBilling, v})
	}
	return rows
}

// textRows renders rows as indented "label: value" lines, one per row.
func textRows(rows []onboardingRow, indent string) string {
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "%s%s: %s\n", indent, r.label, r.value)
	}
	return b.String()
}

// deriveOnboarding checks the domain keys and adds the plain text pieces of
// the body to the sanitised data. A piece is empty when it has no content, so
// the body never shows a stray heading or blank row.
func deriveOnboarding(clean map[string]string) error {
	n, err := onboardingDomainCount(clean)
	if err != nil {
		return err
	}
	// One source for the intro: the HTML body builds it from the same parts.
	clean["intro_text"] = onbIntroBefore + clean["issuer_name"] + onbIntroAfter
	var domains strings.Builder
	for i := 1; i <= n; i++ {
		k := strconv.Itoa(i)
		name := strings.TrimSpace(clean["domain_"+k+"_name"])
		if name == "" {
			return fmt.Errorf("domain %d is missing its name", i)
		}
		if i > 1 {
			domains.WriteString("\n")
		}
		fmt.Fprintf(&domains, "  %d. %s", i, name)
		if strings.TrimSpace(clean["domain_"+k+"_primary"]) == "1" {
			domains.WriteString(" (" + onbPrimary + ")")
		}
		domains.WriteString("\n")
		domains.WriteString(textRows(onboardingDomainRows(clean, k), "     "))
		if subs, more := onboardingSubdomains(clean, k); len(subs) > 0 || more > 0 {
			domains.WriteString("     " + lblSubdomains + ":\n")
			for _, s := range subs {
				domains.WriteString("       - " + s + "\n")
			}
			if more > 0 {
				fmt.Fprintf(&domains, "       - +%d %s\n", more, onbMore)
			}
		}
	}
	clean["domains_text"] = strings.TrimRight(domains.String(), "\n")

	clean["site_text"] = ""
	if rows := onboardingSiteRows(clean); len(rows) > 0 {
		clean["site_text"] = "\n" + textRows(rows, "  ")
	}
	clean["contacts_text"] = ""
	if rows := onboardingContactRows(clean); len(rows) > 0 {
		clean["contacts_text"] = "\n" + lblContacts + "\n" + textRows(rows, "  ")
	}
	clean["note_text"] = ""
	if v := strings.TrimSpace(clean["note"]); v != "" {
		clean["note_text"] = "\n" + lblOnbNote + "\n  " + v + "\n"
	}
	return nil
}

// onboardingHTML renders the HTML body in the style of the invoice, as nested
// tables with inline CSS. Every value is escaped with html.EscapeString, so a
// tenant, registrant or note such as <script> arrives as inert text. clean has
// already passed deriveOnboarding.
func onboardingHTML(clean map[string]string) string {
	esc := html.EscapeString
	lbl := func(s string) string {
		return `<span style="font-family:` + fSans + `;font-size:11px;font-weight:bold;letter-spacing:1.5px;color:` + cInk + `;">` + s + `</span>`
	}
	// kv renders rows as a two column table of muted labels and values.
	kv := func(b *strings.Builder, rows []onboardingRow) {
		b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0">`)
		for _, r := range rows {
			fmt.Fprintf(b, `<tr><td valign="top" width="170" style="padding:3px 10px 3px 0;font-size:12px;color:%s;">%s</td><td valign="top" style="padding:3px 0;font-size:14px;word-break:break-word;">%s</td></tr>`,
				cMuted, r.label, esc(r.value))
		}
		b.WriteString(`</table>`)
	}
	section := func(b *strings.Builder, title string) {
		fmt.Fprintf(b, `<tr><td style="padding-top:24px;padding-bottom:8px;border-bottom:1px solid %s;">%s</td></tr>`, cNavy, lbl(title))
	}

	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html lang="mn"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">`)
	fmt.Fprintf(&b, `<title>%s %s</title></head>`, lblWelcome, esc(clean["tenant"]))
	fmt.Fprintf(&b, `<body style="margin:0;padding:0;background:#ffffff;color:%s;font-family:%s;">`, cInk, fSans)
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr><td align="center" style="padding:24px 12px;">`)
	fmt.Fprintf(&b, `<table role="presentation" width="680" cellpadding="0" cellspacing="0" border="0" style="width:100%%;max-width:680px;font-family:%s;color:%s;">`, fSans, cInk)

	// Header: the operating company, then a navy rule and the greeting.
	fmt.Fprintf(&b, `<tr><td style="padding-bottom:14px;border-bottom:3px solid %s;font-family:%s;font-size:28px;font-weight:bold;line-height:1.2;">%s</td></tr>`, cNavy, fSerif, esc(clean["issuer_name"]))
	fmt.Fprintf(&b, `<tr><td style="padding-top:22px;font-family:%s;font-size:22px;color:%s;">%s, %s</td></tr>`, fSerif, cNavy, lblWelcome, esc(clean["tenant"]))

	// Project intro and the site addresses.
	section(&b, lblProject)
	fmt.Fprintf(&b, `<tr><td style="padding-top:12px;font-size:14px;line-height:1.5;">%s<strong>%s</strong>%s</td></tr>`, onbIntroBefore, esc(clean["issuer_name"]), onbIntroAfter)
	if rows := onboardingSiteRows(clean); len(rows) > 0 {
		b.WriteString(`<tr><td style="padding-top:10px;">`)
		kv(&b, rows)
		b.WriteString(`</td></tr>`)
	}

	// Domains and ownership: one bordered block per domain.
	section(&b, lblDomains)
	n, _ := onboardingDomainCount(clean)
	for i := 1; i <= n; i++ {
		k := strconv.Itoa(i)
		fmt.Fprintf(&b, `<tr><td style="padding-top:14px;"><table role="presentation" width="100%%" cellpadding="0" cellspacing="0" border="0" style="border:1px solid %s;border-left:4px solid %s;"><tr><td style="padding:12px 16px;">`, cRule, cNavy)
		fmt.Fprintf(&b, `<div style="font-family:%s;font-size:16px;font-weight:bold;word-break:break-word;">%s`, fMono, esc(clean["domain_"+k+"_name"]))
		if strings.TrimSpace(clean["domain_"+k+"_primary"]) == "1" {
			fmt.Fprintf(&b, ` <span style="font-family:%s;font-size:11px;font-weight:normal;color:%s;">(%s)</span>`, fSans, cAccent, onbPrimary)
		}
		b.WriteString(`</div>`)
		if rows := onboardingDomainRows(clean, k); len(rows) > 0 {
			b.WriteString(`<div style="padding-top:8px;">`)
			kv(&b, rows)
			b.WriteString(`</div>`)
		}
		if subs, more := onboardingSubdomains(clean, k); len(subs) > 0 || more > 0 {
			fmt.Fprintf(&b, `<div style="padding-top:10px;font-size:12px;color:%s;">%s</div>`, cMuted, lblSubdomains)
			for _, s := range subs {
				fmt.Fprintf(&b, `<div style="padding-top:3px;font-family:%s;font-size:13px;word-break:break-word;">%s</div>`, fMono, esc(s))
			}
			if more > 0 {
				fmt.Fprintf(&b, `<div style="padding-top:3px;font-size:13px;color:%s;">+%d %s</div>`, cMuted, more, onbMore)
			}
		}
		b.WriteString(`</td></tr></table></td></tr>`)
	}

	// Contacts.
	if rows := onboardingContactRows(clean); len(rows) > 0 {
		section(&b, lblContacts)
		b.WriteString(`<tr><td style="padding-top:10px;">`)
		kv(&b, rows)
		b.WriteString(`</td></tr>`)
	}

	// The optional note of this send.
	if v := strings.TrimSpace(clean["note"]); v != "" {
		section(&b, lblOnbNote)
		fmt.Fprintf(&b, `<tr><td style="padding-top:12px;font-size:14px;line-height:1.5;word-break:break-word;">%s</td></tr>`, esc(v))
	}

	fmt.Fprintf(&b, `<tr><td style="padding-top:28px;font-family:%s;font-size:15px;color:%s;">%s</td></tr>`, fSerif, cNavy, esc(clean["issuer_name"]))
	b.WriteString(`</table></td></tr></table></body></html>`)
	return b.String()
}
