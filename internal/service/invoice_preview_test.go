package service

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/eandstravel/tenantcore/internal/models"
)

func TestPreviewRendersTheMongolianTemplateInAnyStatus(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	ctx := context.Background()
	tid := fx.a.Hex()

	draft := fx.createAllDue(t, intp(1000))
	p, err := fx.inv.Preview(ctx, tid, draft.ID.Hex())
	if err != nil {
		t.Fatalf("preview draft: %v", err)
	}
	if p.Subject != "Нэхэмжлэх INV-2026-10-001" {
		t.Fatalf("subject = %q", p.Subject)
	}
	for _, want := range []string{
		"InnoNomads LLC", "Вэб үйлчилгээ · Улаанбаатар, Монгол Улс", "Хариуцагч: Мөнх-Эрдэнэ", "Tower LLC",
		"ХУДАЛДАН АВАГЧ", "НИЙЛҮҮЛЭГЧ", "ТӨЛӨХ ЭЦСИЙН ХУГАЦАА", "2026.10.16", "НӨАТ 10%", "₮1,254,000", "₮1,140,000",
		"Захиалгын сунгалт · төлөх хугацаа 2026.10.21",
		"Домэйн сунгалт · төлөх хугацаа 2026.10.26",
		"Нэг сая хоёр зуун тавин дөрвөн мянган төгрөг",
		"Гүйлгээний утга: INV-2026-10-001", "Khan Bank", "5012345678",
	} {
		if !strings.Contains(p.Text, want) {
			t.Errorf("text missing %q:\n%s", want, p.Text)
		}
		if !strings.Contains(p.HTML, want) && !strings.Contains(want, ":") && !strings.Contains(want, "·") {
			t.Errorf("html missing %q", want)
		}
	}
	for _, want := range []string{"Захиалгын сунгалт · төлөх хугацаа 2026.10.21", "Домэйн сунгалт · төлөх хугацаа 2026.10.26"} {
		if !strings.Contains(p.HTML, want) {
			t.Errorf("html missing %q", want)
		}
	}

	// Sent, paid and void invoices preview too; nothing is mailed and no
	// status changes.
	voided, err := fx.inv.Void(ctx, tid, draft.ID.Hex())
	if err != nil {
		t.Fatalf("void: %v", err)
	}
	sent := fx.createAllDue(t, nil)
	if _, err := fx.inv.Send(ctx, tid, sent.ID.Hex(), SendInput{}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if _, err := fx.inv.MarkPaid(ctx, tid, sent.ID.Hex(), PaidInput{}); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	mails := len(fx.mail.sent)
	for name, id := range map[string]string{"paid": sent.ID.Hex(), "void": voided.ID.Hex()} {
		before, _ := fx.inv.Get(ctx, tid, id)
		if _, err := fx.inv.Preview(ctx, tid, id); err != nil {
			t.Errorf("preview %s: %v", name, err)
		}
		after, _ := fx.inv.Get(ctx, tid, id)
		if before.Status != after.Status || before.UpdatedAt != after.UpdatedAt || (before.PaymentInstructions == nil) != (after.PaymentInstructions == nil) {
			t.Errorf("preview %s changed the invoice", name)
		}
	}
	if len(fx.mail.sent) != mails {
		t.Fatal("a preview sent mail")
	}
}

func TestPreviewIsTenantScoped(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	ctx := context.Background()
	inv := fx.createAllDue(t, nil)

	// Another tenant, or a tenant that does not exist, gets a 404.
	if _, err := fx.inv.Preview(ctx, fx.b.Hex(), inv.ID.Hex()); domStatus(err) != http.StatusNotFound {
		t.Fatalf("cross tenant: status %d (%v)", domStatus(err), err)
	}
	if _, err := fx.inv.Preview(ctx, "000000000000000000000001", inv.ID.Hex()); domStatus(err) != http.StatusNotFound {
		t.Fatalf("unknown tenant: status %d (%v)", domStatus(err), err)
	}
	if _, err := fx.inv.Preview(ctx, fx.a.Hex(), "000000000000000000000001"); domStatus(err) != http.StatusNotFound {
		t.Fatalf("unknown invoice: status %d (%v)", domStatus(err), err)
	}
	for _, bad := range [][2]string{{"nope", inv.ID.Hex()}, {fx.a.Hex(), "nope"}} {
		if _, err := fx.inv.Preview(ctx, bad[0], bad[1]); domStatus(err) != http.StatusBadRequest {
			t.Errorf("bad id %v: status %d (%v)", bad, domStatus(err), err)
		}
	}
}

func TestPreviewNeedsConfiguredSettings(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	inv := fx.createAllDue(t, nil)
	for name, set := range map[string]*models.PaymentSettings{
		"nothing saved": nil,
		"no company":    {BankName: "B", AccountName: "N", AccountNumber: "12345"},
	} {
		fx.ps.p = set
		if _, err := fx.inv.Preview(context.Background(), fx.a.Hex(), inv.ID.Hex()); domStatus(err) != http.StatusConflict {
			t.Errorf("%s: status %d (%v)", name, domStatus(err), err)
		}
	}
}

// An invoice sent before the issuer fields existed falls back to the current
// settings for them; one with an issuer snapshot keeps it.
func TestIssuerFallbackAndSnapshot(t *testing.T) {
	fx := newInvFixture()
	sent := fx.sentInvoice(t)
	ctx := context.Background()
	tid := fx.a.Hex()
	if snap := sent.PaymentInstructions; snap.CompanyName != "InnoNomads LLC" || snap.ContactPerson != "Мөнх-Эрдэнэ" {
		t.Fatalf("issuer fields missing from the snapshot: %+v", snap)
	}

	// Settings change: the snapshot still wins in preview and resend.
	fx.ps.p.CompanyName, fx.ps.p.CompanyTagline, fx.ps.p.ContactPerson = "New Co", "", ""
	p, err := fx.inv.Preview(ctx, tid, sent.ID.Hex())
	if err != nil || !strings.Contains(p.Text, "InnoNomads LLC") || strings.Contains(p.Text, "New Co") || !strings.Contains(p.Text, "Мөнх-Эрдэнэ") {
		t.Fatalf("snapshot issuer not used: %v\n%s", err, p.Text)
	}

	// An old snapshot has bank details only: the issuer comes from the settings.
	for _, d := range fx.st.docs {
		if d.ID == sent.ID {
			d.PaymentInstructions = &models.PaymentInstructions{BankName: "Old Bank", AccountName: "Old LLC", AccountNumber: "111111"}
		}
	}
	fx.ps.p.ContactPerson = "Нараа"
	p, err = fx.inv.Preview(ctx, tid, sent.ID.Hex())
	if err != nil || !strings.Contains(p.Text, "New Co") || !strings.Contains(p.Text, "Хариуцагч: Нараа") || !strings.Contains(p.Text, "Old Bank") || strings.Contains(p.Text, "Khan Bank") {
		t.Fatalf("fallback not applied: %v\n%s", err, p.Text)
	}
	again, err := fx.inv.Send(ctx, tid, sent.ID.Hex(), SendInput{Resend: true})
	if err != nil {
		t.Fatalf("resend: %v", err)
	}
	if m := fx.mail.sent[len(fx.mail.sent)-1].data; m["issuer_name"] != "New Co" || m["contact_person"] != "Нараа" || m["pay_bank_name"] != "Old Bank" {
		t.Fatalf("resend data: %v", m)
	}
	if again.PaymentInstructions.CompanyName != "" || again.PaymentInstructions.BankName != "Old Bank" {
		t.Fatalf("a resend must not rewrite the stored snapshot: %+v", again.PaymentInstructions)
	}
}

func TestPreviewEscapesInjectedValues(t *testing.T) {
	fx := newInvFixture()
	ctx := context.Background()
	fx.tenantsM[fx.a].Name = `<script>alert(1)</script> & Co`
	fx.ps.p.CompanyName = `<svg onload=alert(2)>`
	fx.ps.p.ContactPerson = `"><img src=x onerror=alert(3)>`
	fx.addFee(t, fx.a, FeeInput{Kind: "other", Label: `<b onmouseover=alert(4)>Hosting</b>`, Amount: 5000, Interval: "monthly"})
	inv := fx.createAllDue(t, nil)
	p, err := fx.inv.Preview(ctx, fx.a.Hex(), inv.ID.Hex())
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	for _, bad := range []string{"<script", "<svg", "<img", "<b ", "<b>"} {
		if strings.Contains(p.HTML, bad) {
			t.Errorf("unescaped %q in the preview HTML", bad)
		}
	}
	for _, want := range []string{"&lt;script&gt;alert(1)&lt;/script&gt; &amp; Co", "&lt;svg onload=alert(2)&gt;", "&lt;b onmouseover=alert(4)&gt;Hosting&lt;/b&gt;"} {
		if !strings.Contains(p.HTML, want) {
			t.Errorf("preview HTML missing %q", want)
		}
	}
}
