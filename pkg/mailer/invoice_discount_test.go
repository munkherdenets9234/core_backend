package mailer

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func sum(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }

// Invoices without a discount render byte for byte as they did before
// discounts existed. The hashes were taken from that output.
func TestInvoiceWithoutDiscountRendersExactlyAsBefore(t *testing.T) {
	cases := map[string]struct {
		mod        func(map[string]string)
		text, html string
	}{
		"plain": {func(map[string]string) {},
			"06f352a2e33994c298f6c80e99f9cfafb41d6d8ac999c6fe4213ece8c7061997",
			"0277f370f4387d9fb696dd85edb863cea4768969e12f93039150adbef9a5df9d"},
		"tax": {func(d map[string]string) { d["tax_rate"], d["tax_amount"], d["total"] = "10%", "110,000", "1,210,000" },
			"1f5e1589fa66d728c7f88d71e65c6cd5e436a71c886954a81af53342ac00e5e2",
			"9e518e975455a767e1b020ad0dc705a46a4b4d26d092491c459e75e3bcbbf85c"},
	}
	for name, c := range cases {
		d := invoiceData()
		c.mod(d)
		_, text, html, err := renderMail(TemplateInvoice, d)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if sum(text) != c.text || sum(html) != c.html {
			t.Errorf("%s: output changed for an invoice without a discount\n%s", name, text)
		}
	}
}

// discountedData is invoiceData with a 10% discount on line 1 (100,000 gross)
// and a plain 50,000 discount on line 2, tax 10% on the discounted base.
func discountedData() map[string]string {
	d := invoiceData()
	d["line_1_discount"], d["line_1_net"], d["line_1_discount_pct"], d["line_1_discount_reason"] = "10,000", "90,000", "10%", "Loyal client"
	d["line_2_discount"], d["line_2_net"] = "50,000", "950,000"
	d["discount_total"] = "60,000"
	d["tax_rate"], d["tax_amount"], d["total"] = "10%", "104,000", "1,144,000"
	return d
}

func TestInvoiceDiscountSubLinesAndTotals(t *testing.T) {
	_, text, html, err := renderMail(TemplateInvoice, discountedData())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"Хөнгөлөлт −₮10,000 (10%) · Loyal client",
		"Хөнгөлөлт −₮50,000\n",
		"ТОО 1 × 100,000 = 90,000 ₮", // gross unit price, net amount
		"ТОО 1 × 1,000,000 = 950,000 ₮",
		"Дэд дүн: ₮1,100,000\nХөнгөлөлт: −₮60,000\nНӨАТ 10%: ₮104,000\nНИЙТ ТӨЛӨХ ДҮН: ₮1,144,000",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Хөнгөлөлт −₮50,000 (") || strings.Contains(text, "Хөнгөлөлт −₮50,000 ·") {
		t.Errorf("line 2 has no percent or reason:\n%s", text)
	}
	for _, want := range []string{
		"Хөнгөлөлт −₮10,000 (10%) · Loyal client",
		"Хөнгөлөлт −₮50,000<",
		">90,000</td></tr>", ">950,000</td></tr>", // ДҮН column is the net
		">100,000</td>", ">1,000,000</td>", // НЭГЖ ҮНЭ column stays gross
		"Хөнгөлөлт</td>", "−₮60,000", "₮1,100,000", "₮1,144,000",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("html lacks %q", want)
		}
	}
}

func TestInvoiceDiscountRowsAreOmittedWithoutDiscount(t *testing.T) {
	d := invoiceData()
	d["tax_rate"], d["tax_amount"], d["total"] = "10%", "110,000", "1,210,000"
	_, text, html, err := renderMail(TemplateInvoice, d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "Хөнгөлөлт") || strings.Contains(html, "Хөнгөлөлт") {
		t.Error("a discount label appears on an invoice without a discount")
	}
}

func TestInvoiceDiscountTotalsRowOnly(t *testing.T) {
	// A line discount without the totals row key is a caller bug, but the
	// totals row follows its own key, not the lines.
	d := invoiceData()
	d["discount_total"] = "5"
	_, text, _, err := renderMail(TemplateInvoice, d)
	if err != nil || !strings.Contains(text, "Хөнгөлөлт: −₮5\n") {
		t.Fatalf("totals row: %v\n%s", err, text)
	}
}

func TestInvoiceDiscountNeedsNet(t *testing.T) {
	d := invoiceData()
	d["line_1_discount"] = "10"
	if _, _, _, err := renderMail(TemplateInvoice, d); err == nil {
		t.Fatal("a line discount without its net amount must be refused")
	}
}

func TestInvoiceDiscountValuesAreEscapedAndSanitised(t *testing.T) {
	d := discountedData()
	d["line_1_discount_reason"] = "<script>alert(1)</script>\r\nFAKE: line\u202e"
	_, text, html, err := renderMail(TemplateInvoice, d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "<script>") || !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("reason not escaped in html")
	}
	if strings.Contains(text, "\nFAKE") || strings.Contains(text, "\u202e") {
		t.Errorf("reason can add a line or a bidi override to the text:\n%s", text)
	}
}
