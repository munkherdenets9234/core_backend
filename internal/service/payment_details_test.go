package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/pkg/mailer"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// fakePaySettings is both the settings store (Get/Put) and the reader the
// invoice service uses. A nil p means nothing saved.
type fakePaySettings struct {
	p *models.PaymentSettings
}

func (f *fakePaySettings) Get(context.Context) (*models.PaymentSettings, error) {
	if f.p == nil {
		return &models.PaymentSettings{ID: models.PaymentSettingsID}, nil
	}
	c := *f.p
	return &c, nil
}

// fakePayStore mimics the repository: ErrNoDocuments when empty.
type fakePayStore struct{ p *models.PaymentSettings }

func (f *fakePayStore) Get(context.Context) (*models.PaymentSettings, error) {
	if f.p == nil {
		return nil, mongo.ErrNoDocuments
	}
	c := *f.p
	return &c, nil
}

func (f *fakePayStore) Put(_ context.Context, p *models.PaymentSettings) error {
	c := *p
	f.p = &c
	return nil
}

func validSettings() PaymentSettingsInput {
	return PaymentSettingsInput{
		CompanyName: "InnoNomads LLC", CompanyTagline: "Вэб үйлчилгээ · Улаанбаатар, Монгол Улс", ContactPerson: "Мөнх-Эрдэнэ",
		BankName: "Khan Bank", AccountName: "Inno Nomads LLC", AccountNumber: "MN12 0005 0012 3456 78",
		SWIFT: "agmomnub", Instructions: "Use the invoice number as the transfer description",
	}
}

func TestPaymentSettingsGetUnsetThenConfigured(t *testing.T) {
	st := &fakePayStore{}
	svc := NewPaymentSettingsService(st)
	svc.now = func() time.Time { return testNow }
	ctx := context.Background()

	got, err := svc.Get(ctx)
	if err != nil || got.Configured() {
		t.Fatalf("unset: %+v %v", got, err)
	}
	raw, _ := json.Marshal(got)
	if !strings.Contains(string(raw), `"configured":false`) {
		t.Fatalf("unset json: %s", raw)
	}

	actor := primitive.NewObjectID()
	saved, err := svc.Put(ctx, validSettings(), &actor)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if saved.SWIFT != "AGMOMNUB" || saved.UserID == nil || *saved.UserID != actor || saved.UpdatedAt == nil || !saved.UpdatedAt.Equal(testNow) {
		t.Fatalf("saved: %+v", saved)
	}
	got, _ = svc.Get(ctx)
	raw, _ = json.Marshal(got)
	if !got.Configured() || !strings.Contains(string(raw), `"configured":true`) || !strings.Contains(string(raw), `"account_number":"MN12 0005 0012 3456 78"`) {
		t.Fatalf("configured json: %s", raw)
	}

	// A PUT replaces the whole document: optional fields are cleared.
	in := validSettings()
	in.SWIFT, in.Instructions = "", ""
	if _, err := svc.Put(ctx, in, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = svc.Get(ctx)
	if got.SWIFT != "" || got.Instructions != "" || got.UserID != nil {
		t.Fatalf("not replaced: %+v", got)
	}
}

func TestPaymentSettingsValidation(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	cases := map[string]func(*PaymentSettingsInput){
		"company name empty":      func(i *PaymentSettingsInput) { i.CompanyName = " " },
		"company name too long":   func(i *PaymentSettingsInput) { i.CompanyName = long(101) },
		"company name control":    func(i *PaymentSettingsInput) { i.CompanyName = "Inno\nNomads" },
		"company name format chr": func(i *PaymentSettingsInput) { i.CompanyName = "Inno\u202eNomads" },
		"tagline too long":        func(i *PaymentSettingsInput) { i.CompanyTagline = long(151) },
		"tagline newline":         func(i *PaymentSettingsInput) { i.CompanyTagline = "a\nb" },
		"contact too long":        func(i *PaymentSettingsInput) { i.ContactPerson = long(101) },
		"contact control":         func(i *PaymentSettingsInput) { i.ContactPerson = "a\x00b" },
		"bank name empty":         func(i *PaymentSettingsInput) { i.BankName = "  " },
		"bank name too long":      func(i *PaymentSettingsInput) { i.BankName = long(101) },
		"bank name control":       func(i *PaymentSettingsInput) { i.BankName = "Khan\x00Bank" },
		"bank name zero width":    func(i *PaymentSettingsInput) { i.BankName = "​" },
		"account name empty":      func(i *PaymentSettingsInput) { i.AccountName = "" },
		"account name too long":   func(i *PaymentSettingsInput) { i.AccountName = long(101) },
		"account number empty":    func(i *PaymentSettingsInput) { i.AccountNumber = "" },
		"account number short":    func(i *PaymentSettingsInput) { i.AccountNumber = "123" },
		"account number long":     func(i *PaymentSettingsInput) { i.AccountNumber = strings.Repeat("1", 35) },
		"account number symbol":   func(i *PaymentSettingsInput) { i.AccountNumber = "5012/345678" },
		"account number dot":      func(i *PaymentSettingsInput) { i.AccountNumber = "5012.345678" },
		"account number unicode":  func(i *PaymentSettingsInput) { i.AccountNumber = "5012٣456" },
		"account number newline":  func(i *PaymentSettingsInput) { i.AccountNumber = "5012\n3456" },
		"swift 7":                 func(i *PaymentSettingsInput) { i.SWIFT = "AGMOMNU" },
		"swift 9":                 func(i *PaymentSettingsInput) { i.SWIFT = "AGMOMNUB1" },
		"swift 12":                func(i *PaymentSettingsInput) { i.SWIFT = "AGMOMNUBXXXX" },
		"swift symbol":            func(i *PaymentSettingsInput) { i.SWIFT = "AGMO-NUB" },
		"instructions too long":   func(i *PaymentSettingsInput) { i.Instructions = long(501) },
		"instructions format chr": func(i *PaymentSettingsInput) { i.Instructions = "pay‮me" },
		"instructions control":    func(i *PaymentSettingsInput) { i.Instructions = "pay\x1bme" },
	}
	for name, mutate := range cases {
		st := &fakePayStore{}
		svc := NewPaymentSettingsService(st)
		in := validSettings()
		mutate(&in)
		_, err := svc.Put(context.Background(), in, nil)
		if domStatus(err) != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d (%v)", name, domStatus(err), err)
		}
		if st.p != nil {
			t.Errorf("%s: invalid settings were stored", name)
		}
		if err != nil && strings.Contains(err.Error(), in.AccountNumber) && in.AccountNumber != "" {
			t.Errorf("%s: error echoes the submitted value: %v", name, err)
		}
	}

	// The edges are accepted.
	ok := map[string]func(*PaymentSettingsInput){
		"account number 4":    func(i *PaymentSettingsInput) { i.AccountNumber = "1234" },
		"account number 34":   func(i *PaymentSettingsInput) { i.AccountNumber = strings.Repeat("1", 34) },
		"account with hyphen": func(i *PaymentSettingsInput) { i.AccountNumber = "5012-34-5678" },
		"swift 11":            func(i *PaymentSettingsInput) { i.SWIFT = "AGMOMNUB123" },
		"swift empty":         func(i *PaymentSettingsInput) { i.SWIFT = "" },
		"tagline 150":         func(i *PaymentSettingsInput) { i.CompanyTagline = long(150) },
		"tagline empty":       func(i *PaymentSettingsInput) { i.CompanyTagline = "" },
		"contact empty":       func(i *PaymentSettingsInput) { i.ContactPerson = "" },
		"contact 100":         func(i *PaymentSettingsInput) { i.ContactPerson = long(100) },
		"company name 100":    func(i *PaymentSettingsInput) { i.CompanyName = long(100) },
		"mongolian names": func(i *PaymentSettingsInput) {
			i.CompanyName, i.ContactPerson = "Инно Номадс ХХК", "Мөнх-Эрдэнэ"
		},
		"instructions 500": func(i *PaymentSettingsInput) { i.Instructions = long(500) },
		"names 100":        func(i *PaymentSettingsInput) { i.BankName, i.AccountName = long(100), long(100) },
	}
	for name, mutate := range ok {
		svc := NewPaymentSettingsService(&fakePayStore{})
		in := validSettings()
		mutate(&in)
		if _, err := svc.Put(context.Background(), in, nil); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func (fx *invFixture) sentInvoice(t *testing.T) *models.Invoice {
	t.Helper()
	fx.seed(t)
	inv := fx.createAllDue(t, nil)
	if _, err := fx.inv.Send(context.Background(), fx.a.Hex(), inv.ID.Hex(), SendInput{}); err != nil {
		t.Fatalf("send: %v", err)
	}
	got, err := fx.inv.Get(context.Background(), fx.a.Hex(), inv.ID.Hex())
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestSendBlockedWhenPaymentSettingsNotConfigured(t *testing.T) {
	for name, set := range map[string]*models.PaymentSettings{
		"nothing saved": nil,
		"no account":    {CompanyName: "C", BankName: "Khan Bank", AccountName: "Inno Nomads LLC"},
		"no bank":       {CompanyName: "C", AccountName: "Inno Nomads LLC", AccountNumber: "5012345678"},
		"no name":       {CompanyName: "C", BankName: "Khan Bank", AccountNumber: "5012345678"},
		"no company":    {BankName: "Khan Bank", AccountName: "Inno Nomads LLC", AccountNumber: "5012345678"},
	} {
		fx := newInvFixture()
		fx.seed(t)
		fx.ps.p = set
		inv := fx.createAllDue(t, nil)
		_, err := fx.inv.Send(context.Background(), fx.a.Hex(), inv.ID.Hex(), SendInput{})
		if domStatus(err) != http.StatusConflict || !strings.Contains(err.Error(), "payment settings are not configured") {
			t.Fatalf("%s: %v", name, err)
		}
		got, _ := fx.inv.Get(context.Background(), fx.a.Hex(), inv.ID.Hex())
		if got.Status != models.InvoiceDraft || got.SentAt != nil || got.PaymentInstructions != nil || len(fx.mail.sent) != 0 {
			t.Fatalf("%s: a blocked send changed the invoice or sent mail: %+v", name, got)
		}
		// Configured later: the same invoice can then be sent (it was not claimed).
		fx.ps.p = &models.PaymentSettings{CompanyName: "C", BankName: "B", AccountName: "N", AccountNumber: "12345"}
		if _, err := fx.inv.Send(context.Background(), fx.a.Hex(), inv.ID.Hex(), SendInput{}); err != nil {
			t.Fatalf("%s: send after configuring: %v", name, err)
		}
	}
}

func TestSendSnapshotsPaymentSettingsAndKeepsThemOnResend(t *testing.T) {
	fx := newInvFixture()
	sent := fx.sentInvoice(t)
	snap := sent.PaymentInstructions
	if snap == nil || snap.CompanyName != "InnoNomads LLC" || snap.CompanyTagline == "" || snap.ContactPerson != "Мөнх-Эрдэнэ" || snap.BankName != "Khan Bank" || snap.AccountName != "Inno Nomads LLC" || snap.AccountNumber != "5012345678" ||
		snap.Instructions == "" {
		t.Fatalf("snapshot: %+v", snap)
	}
	m := fx.mail.sent[0].data
	if m["pay_bank_name"] != "Khan Bank" || m["pay_account_number"] != "5012345678" || m["pay_account_name"] != "Inno Nomads LLC" {
		t.Fatalf("mail data: %v", m)
	}
	if _, _, _, err := mailer.Render(mailer.TemplateInvoice, m); err != nil {
		t.Fatalf("the data the service builds does not satisfy the template: %v", err)
	}

	// Settings are edited, then the invoice is resent: the tenant gets the
	// original details and the stored snapshot does not move.
	fx.ps.p = &models.PaymentSettings{CompanyName: "New Co", BankName: "TDB", AccountName: "New LLC", AccountNumber: "999999"}
	again, err := fx.inv.Send(context.Background(), fx.a.Hex(), sent.ID.Hex(), SendInput{Resend: true})
	if err != nil {
		t.Fatalf("resend: %v", err)
	}
	if *again.PaymentInstructions != *snap {
		t.Fatalf("snapshot changed on resend: %+v", again.PaymentInstructions)
	}
	if m2 := fx.mail.sent[1].data; m2["issuer_name"] != "InnoNomads LLC" || m2["pay_bank_name"] != "Khan Bank" || m2["pay_account_number"] != "5012345678" {
		t.Fatalf("resend mailed the edited settings: %v", m2)
	}

	// Resend with the settings now unconfigured is blocked too, status unchanged.
	fx.ps.p = nil
	if _, err := fx.inv.Send(context.Background(), fx.a.Hex(), sent.ID.Hex(), SendInput{Resend: true}); domStatus(err) != http.StatusConflict {
		t.Fatalf("resend unconfigured: %v", err)
	}
	got, _ := fx.inv.Get(context.Background(), fx.a.Hex(), sent.ID.Hex())
	if got.Status != models.InvoiceSent {
		t.Fatalf("status %s", got.Status)
	}
}

func TestSendSnapshotIsAddedOnResendWhenAbsent(t *testing.T) {
	fx := newInvFixture()
	sent := fx.sentInvoice(t)
	// An invoice sent before payment settings existed has no snapshot.
	for _, d := range fx.st.docs {
		if d.ID == sent.ID {
			d.PaymentInstructions = nil
		}
	}
	again, err := fx.inv.Send(context.Background(), fx.a.Hex(), sent.ID.Hex(), SendInput{Resend: true})
	if err != nil || again.PaymentInstructions == nil || again.PaymentInstructions.BankName != "Khan Bank" {
		t.Fatalf("resend: %+v %v", again, err)
	}
}

func TestFailedSendStoresNoSnapshot(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	inv := fx.createAllDue(t, nil)
	fx.mail.err = context.DeadlineExceeded
	if _, err := fx.inv.Send(context.Background(), fx.a.Hex(), inv.ID.Hex(), SendInput{}); err == nil {
		t.Fatal("expected a failure")
	}
	got, _ := fx.inv.Get(context.Background(), fx.a.Hex(), inv.ID.Hex())
	if got.Status != models.InvoiceDraft || got.PaymentInstructions != nil {
		t.Fatalf("failed send left state: %+v", got)
	}
}

func TestMarkPaidEmptyBodyUsesDefaults(t *testing.T) {
	fx := newInvFixture()
	sent := fx.sentInvoice(t)
	paid, err := fx.inv.MarkPaid(context.Background(), fx.a.Hex(), sent.ID.Hex(), PaidInput{})
	if err != nil {
		t.Fatal(err)
	}
	p := paid.Payment
	if paid.PaidAt == nil || !paid.PaidAt.Equal(testNow) || p == nil || p.Method != "bank_transfer" || p.AmountReceived != paid.Total ||
		p.Reference != "" || p.PayerName != "" || p.PayerAccountLast4 != "" || p.Note != "" {
		t.Fatalf("defaults: %+v / %+v", paid.PaidAt, p)
	}
	raw, _ := json.Marshal(paid)
	if !strings.Contains(string(raw), `"payment":{"method":"bank_transfer"`) || !strings.Contains(string(raw), `"payment_instructions":{`) {
		t.Fatalf("invoice json: %s", raw)
	}
}

func TestMarkPaidStoresTheTransactionRecord(t *testing.T) {
	fx := newInvFixture()
	sent := fx.sentInvoice(t)
	total := sent.Total
	paid, err := fx.inv.MarkPaid(context.Background(), fx.a.Hex(), sent.ID.Hex(), PaidInput{
		PaidAt: "2026-10-16", Method: "cash", Reference: " TXN-77 ", PayerName: "Bat Erdene", PayerAccountLast4: "4821",
		AmountReceived: &total, Note: "Paid at the office",
	})
	if err != nil {
		t.Fatal(err)
	}
	p := paid.Payment
	if !paid.PaidAt.Equal(date(2026, 10, 16)) || p.Method != "cash" || p.Reference != "TXN-77" || p.PayerName != "Bat Erdene" ||
		p.PayerAccountLast4 != "4821" || p.AmountReceived != total || p.Note != "Paid at the office" {
		t.Fatalf("record: %v %+v", paid.PaidAt, p)
	}

	// RFC3339 is accepted too.
	fx2 := newInvFixture()
	s2 := fx2.sentInvoice(t)
	paid2, err := fx2.inv.MarkPaid(context.Background(), fx2.a.Hex(), s2.ID.Hex(), PaidInput{PaidAt: "2026-10-16T08:30:00+08:00"})
	if err != nil || !paid2.PaidAt.Equal(time.Date(2026, 10, 16, 0, 30, 0, 0, time.UTC)) {
		t.Fatalf("rfc3339: %v %v", paid2, err)
	}
}

func TestMarkPaidBodyValidation(t *testing.T) {
	wrong := int64(1)
	long := func(n int) string { return strings.Repeat("x", n) }
	cases := map[string]PaidInput{
		"future date":        {PaidAt: "2026-10-18"},
		"future rfc3339":     {PaidAt: "2026-10-17T13:00:00Z"},
		"before issue date":  {PaidAt: "2026-10-15T23:59:59Z"},
		"garbage date":       {PaidAt: "yesterday"},
		"unknown method":     {Method: "bitcoin"},
		"reference too long": {Reference: long(101)},
		"reference control":  {Reference: "a\x00b"},
		"payer too long":     {PayerName: long(101)},
		"last4 too long":     {PayerAccountLast4: "12345"},
		"last4 too short":    {PayerAccountLast4: "123"},
		"last4 letters":      {PayerAccountLast4: "12a4"},
		"last4 full account": {PayerAccountLast4: "5012345678"},
		"note too long":      {Note: long(501)},
		"amount mismatch":    {AmountReceived: &wrong},
	}
	for name, in := range cases {
		fx := newInvFixture()
		sent := fx.sentInvoice(t)
		_, err := fx.inv.MarkPaid(context.Background(), fx.a.Hex(), sent.ID.Hex(), in)
		if domStatus(err) != http.StatusBadRequest {
			t.Errorf("%s: status %d (%v)", name, domStatus(err), err)
		}
		got, _ := fx.inv.Get(context.Background(), fx.a.Hex(), sent.ID.Hex())
		if got.Status != models.InvoiceSent || got.Payment != nil || got.PaidAt != nil {
			t.Errorf("%s: a rejected body changed the invoice: %+v", name, got)
		}
	}

	// Edge cases that are accepted: one day ahead, the issue day itself.
	fx := newInvFixture()
	sent := fx.sentInvoice(t)
	if _, err := fx.inv.MarkPaid(context.Background(), fx.a.Hex(), sent.ID.Hex(), PaidInput{PaidAt: "2026-10-16", PayerAccountLast4: "0042"}); err != nil {
		t.Fatalf("issue day: %v", err)
	}
	fx = newInvFixture()
	sent = fx.sentInvoice(t)
	if _, err := fx.inv.MarkPaid(context.Background(), fx.a.Hex(), sent.ID.Hex(), PaidInput{PaidAt: "2026-10-17T11:59:00Z"}); err != nil {
		t.Fatalf("just under a day ahead: %v", err)
	}
}

func TestMarkPaidAmountMustEqualTotalMessageIsFixed(t *testing.T) {
	fx := newInvFixture()
	sent := fx.sentInvoice(t)
	wrong := sent.Total - 1
	_, err := fx.inv.MarkPaid(context.Background(), fx.a.Hex(), sent.ID.Hex(), PaidInput{AmountReceived: &wrong})
	if err == nil || !strings.Contains(err.Error(), "amount received must equal the invoice total") || strings.Contains(err.Error(), FormatMNT(wrong)) {
		t.Fatalf("message: %v", err)
	}
}

func TestMarkPaidCopiesMethodAndReferenceToDomainClientCharge(t *testing.T) {
	fx := newInvFixture()
	ctx := context.Background()
	tid := fx.a.Hex()
	dom := fx.addDomain(t, fx.a, "paid.example", days(200))
	fx.setPayment(t, dom, payment(300000, nil, 0, nil))
	inv, err := fx.inv.Create(ctx, tid, InvoiceInput{FeeIDs: []string{DomainLinePrefix + dom.Hex()}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fx.inv.Send(ctx, tid, inv.ID.Hex(), SendInput{}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.inv.MarkPaid(ctx, tid, inv.ID.Hex(), PaidInput{Method: "bank_transfer", Reference: "TXN-1"}); err != nil {
		t.Fatal(err)
	}
	cc := fx.domain(t, dom).Payment.ClientCharge
	if cc.Method != models.PayMethodBank || cc.Reference != "TXN-1" || cc.PaidThrough == nil {
		t.Fatalf("client charge: %+v", cc)
	}

	// Card and cash map to the same names; other stays other.
	for in, want := range map[string]string{"cash": "cash", "card": "card", "other": "other", "bank_transfer": "bank"} {
		if got := clientPayMethod(in); got != want {
			t.Errorf("clientPayMethod(%s) = %s", in, got)
		}
	}

	// Replay (retry after a failed settle) keeps the same values and a
	// second paid call is still refused.
	if _, err := fx.inv.MarkPaid(ctx, tid, inv.ID.Hex(), PaidInput{Reference: "OTHER"}); domStatus(err) != http.StatusConflict {
		t.Fatalf("replay: %v", err)
	}
	if got := fx.domain(t, dom).Payment.ClientCharge.Reference; got != "TXN-1" {
		t.Fatalf("replay changed the reference: %s", got)
	}
}

func TestMarkPaidWithoutReferenceKeepsDomainReference(t *testing.T) {
	fx := newInvFixture()
	ctx := context.Background()
	tid := fx.a.Hex()
	dom := fx.addDomain(t, fx.a, "keep.example", days(200))
	fx.setPayment(t, dom, payment(300000, nil, 0, nil))
	fx.domain(t, dom).Payment.ClientCharge.Reference = "OLD-REF"
	inv, err := fx.inv.Create(ctx, tid, InvoiceInput{FeeIDs: []string{DomainLinePrefix + dom.Hex()}})
	if err != nil {
		t.Fatal(err)
	}
	fx.sendAndPay(t, tid, inv.ID)
	cc := fx.domain(t, dom).Payment.ClientCharge
	if cc.Reference != "OLD-REF" || cc.Method != models.PayMethodBank {
		t.Fatalf("client charge: %+v", cc)
	}
}

func TestMarkPaidBodyIsValidatedAfterTheStatusChecks(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	inv := fx.createAllDue(t, nil)
	// A draft is told it was not sent, whatever the body says.
	_, err := fx.inv.MarkPaid(context.Background(), fx.a.Hex(), inv.ID.Hex(), PaidInput{Method: "bitcoin"})
	if domStatus(err) != http.StatusConflict {
		t.Fatalf("draft: %v", err)
	}
}
