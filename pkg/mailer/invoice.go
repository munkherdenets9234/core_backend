package mailer

import (
	"fmt"
	"html"
	"strconv"
	"strings"
)

// MaxInvoiceLines is the most lines the invoice template renders.
const MaxInvoiceLines = 50

// invoiceKeys are the data keys the invoice template reads, besides the
// numbered line keys (N from 1 to line_count).
//
//	tenant, number, issued_on, due_on, currency, subtotal, total, line_count,
//	issuer_name (the issuing company)
//	issuer_tagline, contact_person, total_words        (optional)
//	tax_rate, tax_amount                               (optional pair: no tax_rate, no tax row)
//	line_N_label, line_N_amount                        (required per line)
//	line_N_kind, line_N_period                         (optional per line)
//	line_N_discount, line_N_net                        (optional pair: a discount sub-line; the ДҮН column shows line_N_net)
//	line_N_discount_pct, line_N_discount_reason        (optional, only with line_N_discount)
//	discount_total                                     (optional: the Хөнгөлөлт totals row; subtotal stays gross)
//	pay_bank_name, pay_account_name, pay_account_number (required)
//	pay_swift, pay_instructions                        (optional)
//
// Dates (issued_on, due_on, line_N_period) and amounts arrive already
// formatted; nothing here computes money. The line kind is one of
// subscription, domain, deployment or other (anything else reads as other).
//
// Every value passes through sanitize first (control and format characters
// removed, capped at maxValueRunes) and is HTML-escaped where it enters the
// HTML body. The transfer description is always the invoice number.

// Mongolian labels of the invoice, as in the owner's template.
const (
	lblInvoice    = "НЭХЭМЖЛЭХ"
	lblBuyer      = "ХУДАЛДАН АВАГЧ"
	lblSeller     = "НИЙЛҮҮЛЭГЧ"
	lblContact    = "Хариуцагч"
	lblIssued     = "НЭХЭМЖИЛСЭН ОГНОО"
	lblDue        = "ТӨЛӨХ ЭЦСИЙН ХУГАЦАА"
	lblCurrency   = "ВАЛЮТ"
	lblItems      = "БАРАА, ҮЙЛЧИЛГЭЭ"
	lblQty        = "ТОО"
	lblUnitPrice  = "НЭГЖ ҮНЭ"
	lblAmount     = "ДҮН"
	lblSubtotal   = "Дэд дүн"
	lblDiscount   = "Хөнгөлөлт"
	lblTax        = "НӨАТ"
	lblGrandTotal = "НИЙТ ТӨЛӨХ ДҮН"
	lblPayTitle   = "ТӨЛБӨРИЙН МЭДЭЭЛЭЛ"
	lblAccountNo  = "Дансны дугаар"
	lblAccountNm  = "Данс эзэмшигч"
	lblBank       = "Банк"
	lblReference  = "Гүйлгээний утга"
	lblSwift      = "SWIFT"
	lblNote       = "Тайлбар"
	lblDuePeriod  = "төлөх хугацаа"
	tugrik        = "₮"
)

// lineKindText is the muted sub-line of each line kind.
func lineKindText(kind string) string {
	switch strings.TrimSpace(kind) {
	case "subscription":
		return "Захиалгын сунгалт"
	case "domain":
		return "Домэйн сунгалт"
	case "deployment":
		return "Хостинг, байршуулалт"
	}
	return "Бусад"
}

// lineSub is the sub-line text: the kind, then " · төлөх хугацаа <period>"
// unless the line has no period.
func lineSub(clean map[string]string, k string) string {
	sub := lineKindText(clean["line_"+k+"_kind"])
	if p := strings.TrimSpace(clean["line_"+k+"_period"]); p != "" {
		sub += " · " + lblDuePeriod + " " + p
	}
	return sub
}

// lineDiscountText is the discount sub-line of line k, or "" when it has none:
// "Хөнгөлөлт −₮X", then " (N%)" and " · reason" when given. The values are
// already sanitised.
func lineDiscountText(clean map[string]string, k string) string {
	d := strings.TrimSpace(clean["line_"+k+"_discount"])
	if d == "" {
		return ""
	}
	s := lblDiscount + " −" + tugrik + d
	if p := strings.TrimSpace(clean["line_"+k+"_discount_pct"]); p != "" {
		s += " (" + p + ")"
	}
	if r := strings.TrimSpace(clean["line_"+k+"_discount_reason"]); r != "" {
		s += " · " + r
	}
	return s
}

// lineNet is the amount shown in the ДҮН column: the net when the line has a
// discount, else the line amount.
func lineNet(clean map[string]string, k string) string {
	if strings.TrimSpace(clean["line_"+k+"_discount"]) != "" {
		return strings.TrimSpace(clean["line_"+k+"_net"])
	}
	return strings.TrimSpace(clean["line_"+k+"_amount"])
}

// deriveInvoice checks the line keys and adds the computed text pieces
// to the sanitised data.
func deriveInvoice(clean map[string]string) error {
	n, err := invoiceLineCount(clean)
	if err != nil {
		return err
	}
	var lines strings.Builder
	for i := 1; i <= n; i++ {
		label, amount, err := invoiceLine(clean, i)
		if err != nil {
			return err
		}
		k := strconv.Itoa(i)
		net := lineNet(clean, k)
		if net == "" {
			return fmt.Errorf("line %d has a discount but no net amount", i)
		}
		disc := ""
		if v := lineDiscountText(clean, k); v != "" {
			disc = "     " + v + "\n"
		}
		fmt.Fprintf(&lines, "  %d. %s\n     %s\n%s     %s 1 × %s = %s %s\n", i, label, lineSub(clean, k), disc, lblQty, amount, net, tugrik)
	}
	clean["discount_text"] = ""
	if d := strings.TrimSpace(clean["discount_total"]); d != "" {
		clean["discount_text"] = fmt.Sprintf("%s: −%s%s\n", lblDiscount, tugrik, d)
	}
	clean["lines_text"] = strings.TrimRight(lines.String(), "\n")
	clean["tax_text"] = ""
	if rate := strings.TrimSpace(clean["tax_rate"]); rate != "" {
		if strings.TrimSpace(clean["tax_amount"]) == "" {
			return fmt.Errorf("tax_amount is required with tax_rate")
		}
		clean["tax_text"] = fmt.Sprintf("%s %s: %s%s\n", lblTax, rate, tugrik, clean["tax_amount"])
	}
	clean["tagline_text"] = ""
	if v := strings.TrimSpace(clean["issuer_tagline"]); v != "" {
		clean["tagline_text"] = v + "\n"
	}
	clean["contact_text"] = ""
	if v := strings.TrimSpace(clean["contact_person"]); v != "" {
		clean["contact_text"] = lblContact + ": " + v + "\n"
	}
	clean["words_text"] = ""
	if v := strings.TrimSpace(clean["total_words"]); v != "" {
		clean["words_text"] = v + "\n"
	}
	clean["pay_text"] = payText(clean)
	return nil
}

// payText is the plain text payment block. Optional rows are left out when
// empty. Values have already been sanitised. The transfer description is the
// invoice number.
func payText(clean map[string]string) string {
	var b strings.Builder
	b.WriteString(lblPayTitle + "\n")
	fmt.Fprintf(&b, "  %s: %s\n", lblAccountNo, clean["pay_account_number"])
	fmt.Fprintf(&b, "  %s: %s\n", lblAccountNm, clean["pay_account_name"])
	fmt.Fprintf(&b, "  %s: %s\n", lblBank, clean["pay_bank_name"])
	fmt.Fprintf(&b, "  %s: %s\n", lblReference, clean["number"])
	if v := strings.TrimSpace(clean["pay_swift"]); v != "" {
		fmt.Fprintf(&b, "  %s: %s\n", lblSwift, v)
	}
	if v := strings.TrimSpace(clean["pay_instructions"]); v != "" {
		fmt.Fprintf(&b, "  %s: %s\n", lblNote, v)
	}
	return strings.TrimRight(b.String(), "\n")
}

func invoiceLineCount(clean map[string]string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(clean["line_count"]))
	if err != nil || n < 1 || n > MaxInvoiceLines {
		return 0, fmt.Errorf("line_count must be a number from 1 to %d", MaxInvoiceLines)
	}
	return n, nil
}

func invoiceLine(clean map[string]string, i int) (label, amount string, err error) {
	k := strconv.Itoa(i)
	label, amount = strings.TrimSpace(clean["line_"+k+"_label"]), strings.TrimSpace(clean["line_"+k+"_amount"])
	if label == "" || amount == "" {
		return "", "", fmt.Errorf("line %d is missing its label or amount", i)
	}
	return label, amount, nil
}

// Colours and fonts of the HTML body (email safe: inline CSS, no assets).
const (
	cNavy  = "#1f3556"
	cInk   = "#1a1d23"
	cMuted = "#6b7280"
	cRule  = "#d5dae1"
	fSerif = "Georgia,'Times New Roman',Times,serif"
	fSans  = "Arial,Helvetica,sans-serif"
	fMono  = "'Courier New',Courier,monospace"
)

// cAccent colours the discount sub-line.
const cAccent = "#2f6f4e"

// invoiceHTML renders the HTML body in the layout of the owner's invoice
// template, as nested tables with inline CSS. Every value is escaped with
// html.EscapeString, so a tenant, company or fee label such as <script>
// arrives as inert text. clean has already passed deriveInvoice.
func invoiceHTML(clean map[string]string) string {
	e := func(k string) string { return html.EscapeString(clean[k]) }
	cur := e("currency")
	lbl := func(s string) string {
		return `<span style="font-family:` + fSans + `;font-size:11px;font-weight:bold;letter-spacing:1.5px;color:` + cInk + `;">` + s + `</span>`
	}
	rule := func(color string, px int) string {
		return fmt.Sprintf(`<table role="presentation" width="100%%" cellpadding="0" cellspacing="0" border="0"><tr><td style="height:0;font-size:0;line-height:0;border-top:%dpx solid %s;">&nbsp;</td></tr></table>`, px, color)
	}

	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html lang="mn"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">`)
	fmt.Fprintf(&b, `<title>%s %s</title></head>`, lblInvoice, e("number"))
	fmt.Fprintf(&b, `<body style="margin:0;padding:0;background:#ffffff;color:%s;font-family:%s;">`, cInk, fSans)
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr><td align="center" style="padding:24px 12px;">`)
	fmt.Fprintf(&b, `<table role="presentation" width="680" cellpadding="0" cellspacing="0" border="0" style="width:100%%;max-width:680px;font-family:%s;color:%s;">`, fSans, cInk)

	// Header: issuer and tagline left, title and number right, navy rule.
	b.WriteString(`<tr><td style="padding-bottom:14px;"><table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr>`)
	fmt.Fprintf(&b, `<td valign="top"><div style="font-family:%s;font-size:28px;font-weight:bold;color:%s;line-height:1.2;">%s</div>`, fSerif, cInk, e("issuer_name"))
	if v := strings.TrimSpace(clean["issuer_tagline"]); v != "" {
		fmt.Fprintf(&b, `<div style="font-size:13px;color:%s;padding-top:6px;">%s</div>`, cMuted, html.EscapeString(v))
	}
	fmt.Fprintf(&b, `</td><td valign="top" align="right"><div style="font-family:%s;font-size:26px;letter-spacing:4px;color:%s;">%s</div>`, fSerif, cNavy, lblInvoice)
	fmt.Fprintf(&b, `<div style="font-family:%s;font-size:13px;color:%s;padding-top:6px;">№ %s</div></td>`, fMono, cMuted, e("number"))
	b.WriteString(`</tr></table></td></tr>`)
	b.WriteString(`<tr><td style="padding-bottom:22px;">` + rule(cNavy, 3) + `</td></tr>`)

	// Buyer and seller blocks.
	b.WriteString(`<tr><td style="padding-bottom:22px;"><table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr>`)
	fmt.Fprintf(&b, `<td valign="top" width="50%%">%s<div style="font-size:16px;font-weight:bold;padding-top:8px;">%s</div></td>`, lbl(lblBuyer), e("tenant"))
	fmt.Fprintf(&b, `<td valign="top" width="50%%">%s<div style="font-size:16px;font-weight:bold;padding-top:8px;">%s</div>`, lbl(lblSeller), e("issuer_name"))
	if v := strings.TrimSpace(clean["contact_person"]); v != "" {
		fmt.Fprintf(&b, `<div style="font-size:13px;color:%s;padding-top:4px;">%s: %s</div>`, cMuted, lblContact, html.EscapeString(v))
	}
	b.WriteString(`</td></tr></table></td></tr>`)

	// Meta row: issue date, due date, currency.
	b.WriteString(`<tr><td style="padding-bottom:26px;"><table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr>`)
	for _, m := range []struct{ label, value string }{
		{lblIssued, e("issued_on")}, {lblDue, e("due_on")}, {lblCurrency, cur + " " + tugrik},
	} {
		fmt.Fprintf(&b, `<td valign="top" width="33%%">%s<div style="font-family:%s;font-size:15px;padding-top:8px;">%s</div></td>`, lbl(m.label), fMono, m.value)
	}
	b.WriteString(`</tr></table></td></tr>`)

	// Lines table.
	b.WriteString(`<tr><td><table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0">`)
	hd := func(label, align string, w int) string {
		width := ""
		if w > 0 {
			width = fmt.Sprintf(` width="%d"`, w)
		}
		return fmt.Sprintf(`<td align="%s"%s style="padding:0 0 8px 0;border-bottom:1px solid %s;">%s</td>`, align, width, cNavy, lbl(label))
	}
	b.WriteString(`<tr>` + hd(lblItems, "left", 0) + hd(lblQty, "right", 50) + hd(lblUnitPrice, "right", 100) + hd(lblAmount, "right", 110) + `</tr>`)
	n, _ := invoiceLineCount(clean)
	for i := 1; i <= n; i++ {
		k := strconv.Itoa(i)
		num := fmt.Sprintf(`font-family:%s;font-size:15px;`, fMono)
		discHTML := ""
		if v := lineDiscountText(clean, k); v != "" {
			discHTML = fmt.Sprintf(`<div style="font-size:12px;color:%s;padding-top:3px;">%s</div>`, cAccent, html.EscapeString(v))
		}
		fmt.Fprintf(&b, `<tr><td valign="top" style="padding:14px 8px 14px 0;border-bottom:1px solid %s;"><div style="font-size:15px;font-weight:bold;">%s</div><div style="font-size:12px;color:%s;padding-top:4px;">%s</div>%s</td>`,
			cRule, e("line_"+k+"_label"), cMuted, html.EscapeString(lineSub(clean, k)), discHTML)
		fmt.Fprintf(&b, `<td valign="top" align="right" style="padding:14px 0;border-bottom:1px solid %s;%s">1</td>`, cRule, num)
		fmt.Fprintf(&b, `<td valign="top" align="right" style="padding:14px 0;border-bottom:1px solid %s;%s">%s</td>`, cRule, num, e("line_"+k+"_amount"))
		// The ДҮН column is the net amount; without a discount it is the amount.
		netCell := e("line_" + k + "_amount")
		if strings.TrimSpace(clean["line_"+k+"_discount"]) != "" {
			netCell = html.EscapeString(lineNet(clean, k))
		}
		fmt.Fprintf(&b, `<td valign="top" align="right" style="padding:14px 0;border-bottom:1px solid %s;%s">%s</td></tr>`, cRule, num, netCell)
	}
	b.WriteString(`</table></td></tr>`)

	// Totals: right half.
	b.WriteString(`<tr><td style="padding-top:18px;padding-bottom:22px;"><table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr><td width="50%">&nbsp;</td><td width="50%" valign="top"><table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0">`)
	sub := func(label, value string) {
		fmt.Fprintf(&b, `<tr><td style="padding:4px 0;font-size:14px;color:%s;">%s</td><td align="right" style="padding:4px 0;font-family:%s;font-size:14px;">%s</td></tr>`, cMuted, label, fMono, value)
	}
	sub(lblSubtotal, tugrik+e("subtotal"))
	if d := strings.TrimSpace(clean["discount_total"]); d != "" {
		sub(lblDiscount, "−"+tugrik+html.EscapeString(d))
	}
	if rate := strings.TrimSpace(clean["tax_rate"]); rate != "" {
		sub(lblTax+" "+html.EscapeString(rate), tugrik+e("tax_amount"))
	}
	b.WriteString(`<tr><td colspan="2" style="padding:8px 0 10px 0;">` + rule(cNavy, 2) + `</td></tr>`)
	fmt.Fprintf(&b, `<tr><td valign="middle" style="font-size:11px;font-weight:bold;letter-spacing:1.5px;color:%s;">%s</td><td align="right" style="font-family:%s;font-size:26px;font-weight:bold;">%s%s</td></tr>`, cNavy, lblGrandTotal, fMono, tugrik, e("total"))
	if v := strings.TrimSpace(clean["total_words"]); v != "" {
		fmt.Fprintf(&b, `<tr><td colspan="2" align="right" style="padding-top:8px;font-family:%s;font-size:15px;font-style:italic;color:%s;">%s</td></tr>`, fSerif, cMuted, html.EscapeString(v))
	}
	b.WriteString(`</table></td></tr></table></td></tr>`)

	// Payment box: navy left border, light bordered box.
	fmt.Fprintf(&b, `<tr><td><table role="presentation" width="100%%" cellpadding="0" cellspacing="0" border="0" style="border:1px solid %s;border-left:4px solid %s;"><tr><td style="padding:16px 20px;">`, cRule, cNavy)
	b.WriteString(lbl(lblPayTitle))
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="padding-top:8px;">`)
	cell := func(label, value string, mono bool) string {
		face := fSans
		if mono {
			face = fMono
		}
		return fmt.Sprintf(`<td valign="top" width="50%%" style="padding:6px 8px 8px 0;"><div style="font-size:12px;color:%s;">%s</div><div style="font-family:%s;font-size:15px;padding-top:4px;word-break:break-word;">%s</div></td>`, cMuted, label, face, value)
	}
	payRow := func(a, c string) { b.WriteString(`<tr>` + a + c + `</tr>`) }
	payRow(cell(lblAccountNo, e("pay_account_number"), true), cell(lblAccountNm, `<strong>`+e("pay_account_name")+`</strong>`, false))
	payRow(cell(lblBank, e("pay_bank_name"), true), cell(lblReference, e("number"), true))
	swift, note := strings.TrimSpace(clean["pay_swift"]), strings.TrimSpace(clean["pay_instructions"])
	if swift != "" {
		payRow(cell(lblSwift, html.EscapeString(swift), true), `<td width="50%">&nbsp;</td>`)
	}
	if note != "" {
		fmt.Fprintf(&b, `<tr><td colspan="2" style="padding:6px 0 4px 0;font-size:12px;color:%s;">%s: %s</td></tr>`, cMuted, lblNote, html.EscapeString(note))
	}
	b.WriteString(`</table></td></tr></table></td></tr>`)

	b.WriteString(`</table></td></tr></table></body></html>`)
	return b.String()
}
