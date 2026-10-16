package mailer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func invoiceData() map[string]string {
	return map[string]string{
		"app": "Inno Nomads", "tenant": "Tower LLC", "number": "INV-2026-10-007",
		"issued_on": "2026.10.16", "due_on": "2026.11.15", "currency": "MNT",
		"subtotal": "1,100,000", "total": "1,100,000", "line_count": "2",
		"total_words": "Нэг сая нэг зуун мянган төгрөг",
		"issuer_name": "InnoNomads LLC", "issuer_tagline": "Вэб үйлчилгээ · Улаанбаатар, Монгол Улс", "contact_person": "Мөнх-Эрдэнэ",
		"line_1_label": "Domain tower.example.com", "line_1_amount": "100,000", "line_1_kind": "domain", "line_1_period": "2026.10.26",
		"line_2_label": "Subscription: Pro", "line_2_amount": "1,000,000", "line_2_kind": "subscription", "line_2_period": "2026.10.21",
		"pay_bank_name": "Khan Bank", "pay_account_name": "Inno Nomads LLC", "pay_account_number": "5012345678",
	}
}

// Every fixed Mongolian label of the owner's template, in both bodies.
var invoiceLabels = []string{
	"НЭХЭМЖЛЭХ", "ХУДАЛДАН АВАГЧ", "НИЙЛҮҮЛЭГЧ", "Хариуцагч", "НЭХЭМЖИЛСЭН ОГНОО", "ТӨЛӨХ ЭЦСИЙН ХУГАЦАА", "ВАЛЮТ",
	"БАРАА, ҮЙЛЧИЛГЭЭ", "ТОО", "Дэд дүн", "НИЙТ ТӨЛӨХ ДҮН", "ТӨЛБӨРИЙН МЭДЭЭЛЭЛ",
	"Дансны дугаар", "Данс эзэмшигч", "Банк", "Гүйлгээний утга",
}

func TestInvoiceTemplateRendersTextAndHTML(t *testing.T) {
	subject, text, html, err := renderMail(TemplateInvoice, invoiceData())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if subject != "Нэхэмжлэх INV-2026-10-007" {
		t.Fatalf("subject = %q", subject)
	}
	for _, want := range invoiceLabels {
		if !strings.Contains(text, want) {
			t.Errorf("text body missing label %q:\n%s", want, text)
		}
		if want == "НЭГЖ ҮНЭ" || want == "ДҮН" {
			continue
		}
		if !strings.Contains(html, want) {
			t.Errorf("html body missing label %q", want)
		}
	}
	for _, want := range []string{"НЭГЖ ҮНЭ", "ДҮН", "MNT ₮"} {
		if !strings.Contains(html, want) {
			t.Errorf("html body missing %q", want)
		}
	}
	values := []string{
		"Tower LLC", "InnoNomads LLC", "Вэб үйлчилгээ · Улаанбаатар, Монгол Улс", "Мөнх-Эрдэнэ", "INV-2026-10-007",
		"2026.10.16", "2026.11.15", "Domain tower.example.com", "Subscription: Pro", "100,000", "1,000,000", "₮1,100,000",
		"Нэг сая нэг зуун мянган төгрөг", "Khan Bank", "Inno Nomads LLC", "5012345678",
	}
	for _, want := range values {
		if !strings.Contains(text, want) {
			t.Errorf("text body missing %q:\n%s", want, text)
		}
		if !strings.Contains(html, want) {
			t.Errorf("html body missing %q", want)
		}
	}
	// The HTML is a table layout with inline CSS and no external assets.
	for _, want := range []string{"<table", "style=", `<meta charset="utf-8">`, "№ INV-2026-10-007", "font-style:italic"} {
		if !strings.Contains(html, want) {
			t.Errorf("html body missing %q", want)
		}
	}
	for _, bad := range []string{"<script", "<link", "<img", "<style", "src=", "http://", "https://", "{{"} {
		if strings.Contains(html, bad) {
			t.Errorf("html body must not contain %q", bad)
		}
	}
	if strings.Contains(text, "{{") {
		t.Fatal("unsubstituted placeholder left in the invoice")
	}
	if strings.Contains(text, "НӨАТ") || strings.Contains(html, "НӨАТ") {
		t.Error("a tax row appeared although no tax_rate was given")
	}
	// Quantity 1 and unit price equal to the amount on every line.
	if !strings.Contains(text, "ТОО 1 × 100,000 = 100,000 ₮") {
		t.Errorf("line quantity row missing:\n%s", text)
	}
	if strings.Count(html, ">100,000<") != 2 {
		t.Errorf("unit price and amount of line 1 must both show 100,000: %d", strings.Count(html, ">100,000<"))
	}
}

func TestInvoiceTemplateTaxRow(t *testing.T) {
	d := invoiceData()
	d["tax_rate"], d["tax_amount"], d["total"] = "10%", "110,000", "1,210,000"
	_, text, html, err := renderMail(TemplateInvoice, d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(text, "НӨАТ 10%: ₮110,000") || !strings.Contains(html, "НӨАТ 10%") || !strings.Contains(html, "₮110,000") || !strings.Contains(html, "₮1,210,000") {
		t.Fatalf("tax row missing:\n%s\n%s", text, html)
	}

	d2 := invoiceData()
	d2["tax_rate"] = "10%"
	if _, _, _, err := renderMail(TemplateInvoice, d2); err == nil {
		t.Fatal("tax_rate without tax_amount must be refused")
	}
}

func TestInvoiceLineSubtitles(t *testing.T) {
	d := invoiceData()
	d["line_count"] = "5"
	for i, kind := range []string{"subscription", "domain", "deployment", "other", "mystery"} {
		k := strconv.Itoa(i + 1)
		d["line_"+k+"_label"], d["line_"+k+"_amount"] = "L"+k, "1"
		d["line_"+k+"_kind"], d["line_"+k+"_period"] = kind, "2026.09.20"
	}
	_, text, html, err := renderMail(TemplateInvoice, d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"Захиалгын сунгалт · төлөх хугацаа 2026.09.20",
		"Домэйн сунгалт · төлөх хугацаа 2026.09.20",
		"Хостинг, байршуулалт · төлөх хугацаа 2026.09.20",
		"Бусад · төлөх хугацаа 2026.09.20",
	} {
		if !strings.Contains(text, want) || !strings.Contains(html, want) {
			t.Errorf("missing sub-line %q", want)
		}
	}
	// An unknown kind reads as "other".
	if strings.Count(text, "Бусад · төлөх хугацаа") != 2 {
		t.Errorf("unknown kind must read as other:\n%s", text)
	}
}

func TestInvoiceLineWithoutPeriodHasNoPeriodPart(t *testing.T) {
	d := invoiceData()
	d["line_1_period"] = ""
	delete(d, "line_2_period")
	d["line_2_kind"] = ""
	_, text, html, err := renderMail(TemplateInvoice, d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, body := range []string{text, html} {
		if strings.Contains(body, "төлөх хугацаа") {
			t.Errorf("a period part appeared for lines without a period:\n%s", body)
		}
		if !strings.Contains(body, "Домэйн сунгалт") || !strings.Contains(body, "Бусад") {
			t.Errorf("kind sub-lines missing:\n%s", body)
		}
	}
}

func TestInvoiceOptionalIssuerAndWordsAreOmitted(t *testing.T) {
	d := invoiceData()
	delete(d, "issuer_tagline")
	delete(d, "contact_person")
	delete(d, "total_words")
	_, text, html, err := renderMail(TemplateInvoice, d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, body := range []string{text, html} {
		for _, bad := range []string{"Хариуцагч:", "Вэб үйлчилгээ", "төгрөг"} {
			if strings.Contains(body, bad) {
				t.Errorf("%q rendered although its value is unset", bad)
			}
		}
	}
	if !strings.Contains(html, "InnoNomads LLC") {
		t.Fatal("issuer name missing")
	}
	d["issuer_name"] = "  "
	if _, _, _, err := renderMail(TemplateInvoice, d); err == nil {
		t.Fatal("the issuer name is required")
	}
}

// Every value is HTML-escaped where it enters the HTML body.
func TestInvoiceHTMLEscapesValues(t *testing.T) {
	d := invoiceData()
	d["tenant"] = `<script>alert(1)</script>`
	d["line_1_label"] = `"><img src=x onerror=alert(1)>`
	d["line_2_label"] = `Fish & Chips <b>`
	d["tax_rate"], d["tax_amount"] = `<i>5</i>`, `<u>1</u>`
	d["issuer_name"] = `<svg onload=alert(2)>`
	d["issuer_tagline"] = `<a href="javascript:x">tag</a>`
	d["contact_person"] = `<marquee>x</marquee>`
	d["total_words"] = `<em>w</em>`
	_, _, html, err := renderMail(TemplateInvoice, d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, bad := range []string{"<script", "<img", "<b>", "<i>", "<u>", "<svg", "<a href", "<marquee", "<em>", `onerror=alert(1)>`} {
		if strings.Contains(html, bad) {
			t.Errorf("unescaped %q reached the HTML body:\n%s", bad, html)
		}
	}
	for _, want := range []string{"&lt;script&gt;", "&amp; Chips", "&lt;b&gt;", "&lt;svg onload=alert(2)&gt;", "&lt;marquee&gt;"} {
		if !strings.Contains(html, want) {
			t.Errorf("expected escaped %q in:\n%s", want, html)
		}
	}
}

func TestInvoiceValuesAreSanitisedAndCapped(t *testing.T) {
	d := invoiceData()
	d["number"] = "INV-1\r\nBcc: evil@example.com"
	d["line_1_label"] = "Hosting\nДэд дүн: 0\x00\x1b\u202e"
	d["line_2_label"] = strings.Repeat("é", 1000)
	d["issuer_name"] = "Co\r\nBcc: x"
	subject, text, html, err := renderMail(TemplateInvoice, d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.ContainsAny(subject, "\r\n") {
		t.Fatalf("subject carries a line break: %q", subject)
	}
	for _, bad := range []string{"\x00", "\x1b", "\u202e", "\r"} {
		if strings.Contains(text, bad) || strings.Contains(html, bad) {
			t.Errorf("control character %q survived", bad)
		}
	}
	// "Дэд дүн:" appears once as the real row; the label's cannot add a line.
	n := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "Дэд дүн:") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("a line label injected a subtotal line:\n%s", text)
	}
	if strings.Contains(text, strings.Repeat("é", maxValueRunes+1)) || !strings.Contains(text, strings.Repeat("é", maxValueRunes)) {
		t.Fatal("a long label must be capped at maxValueRunes")
	}
}

func TestInvoiceSubstitutionDoesNotRecurse(t *testing.T) {
	d := invoiceData()
	d["line_1_label"] = "{{total}} {{tenant}}"
	_, text, _, err := renderMail(TemplateInvoice, d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(text, "{{total}} {{tenant}}") {
		t.Fatalf("a label's placeholder was expanded:\n%s", text)
	}
}

func TestInvoiceLineKeysAreValidated(t *testing.T) {
	for name, mutate := range map[string]func(map[string]string){
		"count zero":      func(d map[string]string) { d["line_count"] = "0" },
		"count too large": func(d map[string]string) { d["line_count"] = "51" },
		"count not int":   func(d map[string]string) { d["line_count"] = "two" },
		"missing label":   func(d map[string]string) { delete(d, "line_2_label") },
		"blank amount":    func(d map[string]string) { d["line_1_amount"] = "  " },
		"missing total":   func(d map[string]string) { delete(d, "total") },
		"line beyond cnt": func(d map[string]string) { d["line_count"] = "3" },
	} {
		d := invoiceData()
		mutate(d)
		if _, _, _, err := renderMail(TemplateInvoice, d); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}

	// The cap itself is accepted.
	d := invoiceData()
	d["line_count"] = "50"
	for i := 1; i <= 50; i++ {
		k := strconv.Itoa(i)
		d["line_"+k+"_label"], d["line_"+k+"_amount"] = "L"+k, "1"
	}
	if _, _, html, err := renderMail(TemplateInvoice, d); err != nil || !strings.Contains(html, "L50") {
		t.Fatalf("50 lines must render: %v", err)
	}
}

// The invoice is tenantcore's own mail: the /svc relay must not be able to
// send it, but the mail log filter still knows the name.
func TestInvoiceIsInternalOnly(t *testing.T) {
	if _, ok := Known("invoice"); !ok {
		t.Fatal("invoice must be a known template")
	}
	if _, ok := KnownForService("invoice"); ok {
		t.Fatal("invoice must not be sendable through /svc")
	}
	if _, ok := KnownForService("staff_invite"); !ok {
		t.Fatal("staff_invite must stay sendable through /svc")
	}
	names := strings.Join(Names(), ",")
	svcNames := strings.Join(ServiceNames(), ",")
	if !strings.Contains(names, "invoice") || strings.Contains(svcNames, "invoice") {
		t.Fatalf("Names=%s ServiceNames=%s", names, svcNames)
	}
}

// Brevo gets both bodies, as UTF-8 JSON; plain-text templates stay text only.
func TestInvoiceSendPostsHTMLAndText(t *testing.T) {
	var got payload
	var raw map[string]any
	var rawBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&b)
		rawBody = string(b)
		_ = json.Unmarshal(b, &got)
		_ = json.Unmarshal(b, &raw)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	m := New(Config{APIKey: "xkeysib-test", FromAddress: "no-reply@example.com", Endpoint: srv.URL})
	if err := m.Send("client@example.com", TemplateInvoice, invoiceData()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.Contains(got.HTMLContent, "<table") || !strings.Contains(got.TextContent, "INV-2026-10-007") {
		t.Fatalf("bodies: html=%q text=%q", got.HTMLContent, got.TextContent)
	}
	if got.Subject != "Нэхэмжлэх INV-2026-10-007" || !strings.Contains(got.HTMLContent, "НЭХЭМЖЛЭХ") {
		t.Fatalf("Mongolian text did not survive the JSON round trip: subject=%q", got.Subject)
	}
	if !strings.Contains(rawBody, "Нэхэмжлэх") {
		t.Fatal("the request body must carry the Mongolian text as plain UTF-8 JSON")
	}

	raw = nil
	if err := m.Send("me@example.com", TemplatePasswordChanged, map[string]string{"app": "A", "name": "B"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, has := raw["htmlContent"]; has {
		t.Fatal("a plain-text template must not send htmlContent")
	}
}

func TestSMTPMessageTypeForHTML(t *testing.T) {
	msg := string(buildMessageType("text/html", "N", "a@example.com", "b@example.com", "S", "<p>x</p>"))
	if !strings.Contains(msg, "Content-Type: text/html; charset=UTF-8\r\n") {
		t.Fatalf("content type: %s", msg)
	}
	if !strings.Contains(string(buildMessage("N", "a@example.com", "b@example.com", "S", "x")), "Content-Type: text/plain") {
		t.Fatal("buildMessage must stay text/plain")
	}
}

// A value made only of invisible characters is empty once sanitised, so the
// required-key check must see it as missing instead of rendering a blank.
func TestRequiredKeysAreCheckedAfterSanitising(t *testing.T) {
	for _, key := range []string{"tenant", "number", "line_1_label", "issuer_name"} {
		d := invoiceData()
		d[key] = "\u200b \u200d\u2060"
		if _, _, _, err := renderMail(TemplateInvoice, d); err == nil {
			t.Errorf("%s of only invisible characters rendered", key)
		}
	}
}

func TestSMTPAlternativeMessage(t *testing.T) {
	long := "<p>" + strings.Repeat("x", 5000) + " café</p>"
	msg := string(buildMessageAlternative("N", "a@example.com", "b@example.com", "S", "plain body\nline", long))
	head, body, ok := strings.Cut(msg, "\r\n\r\n")
	if !ok || !strings.Contains(head, "Content-Type: multipart/alternative; boundary=") {
		t.Fatalf("not multipart/alternative:\n%.400s", msg)
	}
	for _, line := range strings.Split(msg, "\r\n") {
		if len(line) > 998 {
			t.Fatalf("line of %d bytes", len(line))
		}
	}
	text := strings.Index(body, "Content-Type: text/plain")
	html := strings.Index(body, "Content-Type: text/html")
	if text < 0 || html < 0 || text > html {
		t.Fatalf("parts missing or out of order (text %d, html %d)", text, html)
	}
	if !strings.Contains(body, "Content-Transfer-Encoding: quoted-printable") || !strings.Contains(body, "plain body") {
		t.Fatalf("bad encoding or text part:\n%.600s", body)
	}
}

// The dev SMTP path: a Mongolian subject goes out as an RFC 2047 encoded word
// and the Mongolian bodies as quoted-printable UTF-8, all lines short.
func TestSMTPInvoiceIsUTF8Safe(t *testing.T) {
	subject, text, html, err := renderMail(TemplateInvoice, invoiceData())
	if err != nil {
		t.Fatal(err)
	}
	msg := string(buildMessageAlternative("N", "a@example.com", "b@example.com", subject, text, html))
	if !strings.Contains(msg, "Subject: =?UTF-8?q?") {
		t.Fatalf("subject not RFC 2047 encoded:\n%.300s", msg)
	}
	for _, line := range strings.Split(msg, "\r\n") {
		if len(line) > 998 {
			t.Fatalf("line of %d bytes", len(line))
		}
		for _, r := range line {
			if r > 127 {
				t.Fatalf("raw non-ASCII byte on the wire: %q", line)
			}
		}
	}
	// ASCII subjects are untouched.
	if !strings.Contains(string(buildMessage("N", "a@example.com", "b@example.com", "Plain subject", "x")), "Subject: Plain subject\r\n") {
		t.Fatal("an ASCII subject must pass through unchanged")
	}
}

func payData() map[string]string {
	d := invoiceData()
	d["pay_bank_name"], d["pay_account_name"], d["pay_account_number"] = "Khan Bank", "Inno Nomads LLC", "5012 3456-78"
	d["pay_swift"], d["pay_instructions"] = "AGMOMNUB", "Use the invoice number as the transfer description"
	return d
}

func TestInvoicePaymentBlock(t *testing.T) {
	_, text, html, err := renderMail(TemplateInvoice, payData())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"ТӨЛБӨРИЙН МЭДЭЭЛЭЛ", "Дансны дугаар: 5012 3456-78", "Данс эзэмшигч: Inno Nomads LLC", "Банк: Khan Bank",
		"Гүйлгээний утга: INV-2026-10-007", "SWIFT: AGMOMNUB", "Тайлбар: Use the invoice number as the transfer description",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text body missing %q:\n%s", want, text)
		}
	}
	for _, want := range []string{"ТӨЛБӨРИЙН МЭДЭЭЛЭЛ", "Khan Bank", "Inno Nomads LLC", "5012 3456-78", "AGMOMNUB", "Use the invoice number as the transfer description", "border-left:4px solid #1f3556"} {
		if !strings.Contains(html, want) {
			t.Errorf("html body missing %q", want)
		}
	}
	// The transfer description is always the invoice number, in the monospace cell.
	if !strings.Contains(html, "Гүйлгээний утга</div><div style=\"font-family:'Courier New',Courier,monospace;font-size:15px;padding-top:4px;word-break:break-word;\">INV-2026-10-007</div>") {
		t.Errorf("transfer description is not the invoice number:\n%s", html)
	}
}

func TestInvoicePaymentOptionalFieldsAreOmitted(t *testing.T) {
	d := payData()
	delete(d, "pay_swift")
	d["pay_instructions"] = "  "
	_, text, html, err := renderMail(TemplateInvoice, d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, body := range []string{text, html} {
		if strings.Contains(body, "SWIFT") || strings.Contains(body, "Тайлбар") {
			t.Errorf("an empty optional row was rendered:\n%s", body)
		}
		if !strings.Contains(body, "Khan Bank") {
			t.Errorf("required rows missing:\n%s", body)
		}
	}
}

func TestInvoicePaymentRequiresBankDetails(t *testing.T) {
	for _, key := range []string{"pay_bank_name", "pay_account_name", "pay_account_number"} {
		d := payData()
		delete(d, key)
		if _, _, _, err := renderMail(TemplateInvoice, d); err == nil {
			t.Errorf("%s missing: expected an error", key)
		}
		d[key] = "\u200b"
		if _, _, _, err := renderMail(TemplateInvoice, d); err == nil {
			t.Errorf("%s of only invisible characters rendered", key)
		}
	}
}

func TestInvoicePaymentEscapesAndSanitises(t *testing.T) {
	d := payData()
	d["pay_bank_name"] = `<script>alert(1)</script>`
	d["pay_account_name"] = `"><img src=x onerror=alert(1)>`
	d["pay_instructions"] = "Pay now\r\nДэд дүн: 0 {{total}}\x00\u202e <b>x</b>"
	_, text, html, err := renderMail(TemplateInvoice, d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, bad := range []string{"<script", "<img", "<b>", "onerror=alert(1)>"} {
		if strings.Contains(html, bad) {
			t.Errorf("unescaped %q reached the HTML body:\n%s", bad, html)
		}
	}
	if !strings.Contains(html, "&lt;script&gt;") || !strings.Contains(html, "&lt;b&gt;x&lt;/b&gt;") {
		t.Errorf("expected escaped values in:\n%s", html)
	}
	for _, bad := range []string{"\x00", "\u202e", "\r"} {
		if strings.Contains(text, bad) || strings.Contains(html, bad) {
			t.Errorf("control character %q survived", bad)
		}
	}
	n := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "Дэд дүн:") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the payment note injected a subtotal line:\n%s", text)
	}
	if !strings.Contains(text, "{{total}}") {
		t.Fatalf("a placeholder in the note was expanded:\n%s", text)
	}
}

func TestInvoicePaymentNoteCap(t *testing.T) {
	d := payData()
	d["pay_instructions"] = strings.Repeat("n", 700)
	_, text, _, err := renderMail(TemplateInvoice, d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(text, strings.Repeat("n", maxPayInstructionsRunes)) || strings.Contains(text, strings.Repeat("n", maxPayInstructionsRunes+1)) {
		t.Fatal("the payment note must be capped at maxPayInstructionsRunes")
	}
	d["pay_bank_name"] = strings.Repeat("b", 700)
	_, text, _, _ = renderMail(TemplateInvoice, d)
	if strings.Contains(text, strings.Repeat("b", maxValueRunes+1)) {
		t.Fatal("other payment values keep the standard cap")
	}
}
