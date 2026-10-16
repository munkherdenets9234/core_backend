package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eandstravel/tenantcore/internal/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func tp(t time.Time) *time.Time { return &t }

func payment(clientAmt int64, clientThrough *time.Time, regAmt int64, regThrough *time.Time) models.DomainPayment {
	return models.DomainPayment{
		ClientCharge:  models.ClientCharge{Amount: clientAmt, PaidThrough: clientThrough},
		RegistrarCost: models.RegistrarCost{Amount: regAmt, PaidThrough: regThrough, Payer: models.PayerUs},
	}
}

// setPayment stores a payment record on a domain of the fixture.
func (fx *costFixture) setPayment(t *testing.T, id primitive.ObjectID, p models.DomainPayment) {
	t.Helper()
	for _, d := range fx.domains.docs {
		if d.ID == id {
			d.Payment = p
			return
		}
	}
	t.Fatal("domain not found")
}

func (fx *costFixture) domain(t *testing.T, id primitive.ObjectID) *models.TenantDomain {
	t.Helper()
	for _, d := range fx.domains.docs {
		if d.ID == id {
			return d
		}
	}
	t.Fatal("domain not found")
	return nil
}

func TestPaymentStatusIsDerived(t *testing.T) {
	exp := tp(date(2027, 3, 1).Add(9 * time.Hour))
	cases := []struct {
		name    string
		amount  int64
		through *time.Time
		expires *time.Time
		want    string
	}{
		{"no amount", 0, nil, exp, models.PaymentNone},
		{"no amount even if a date is set", 0, tp(date(2030, 1, 1)), exp, models.PaymentNone},
		{"amount, never paid", 100, nil, exp, models.PaymentUnpaid},
		{"paid through the expiry day (date compare)", 100, tp(date(2027, 3, 1)), exp, models.PaymentPaid},
		{"paid beyond the expiry", 100, tp(date(2028, 3, 1)), exp, models.PaymentPaid},
		{"paid through the day before", 100, tp(date(2027, 2, 28)), exp, models.PaymentUnpaid},
		{"no expiry and paid", 100, tp(date(2027, 1, 1)), nil, models.PaymentPaid},
		{"no expiry and unpaid", 100, nil, nil, models.PaymentUnpaid},
	}
	for _, c := range cases {
		if got := models.PaymentStatus(c.amount, c.through, c.expires); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRenewalFlipsPaidBackToUnpaid(t *testing.T) {
	d := &models.TenantDomain{ExpiresAt: tp(date(2027, 3, 1)), Payment: payment(500, tp(date(2027, 3, 1)), 300, tp(date(2027, 3, 1)))}
	if d.ClientStatus() != models.PaymentPaid || d.RegistrarStatus() != models.PaymentPaid {
		t.Fatalf("expected paid/paid, got %s/%s", d.ClientStatus(), d.RegistrarStatus())
	}
	// The operator renews: only expires_at moves.
	d.ExpiresAt = tp(date(2028, 3, 1))
	if d.ClientStatus() != models.PaymentUnpaid || d.RegistrarStatus() != models.PaymentUnpaid {
		t.Fatalf("after renewal expected unpaid/unpaid, got %s/%s", d.ClientStatus(), d.RegistrarStatus())
	}
}

func TestDomainJSONCarriesDerivedStatusAndMargin(t *testing.T) {
	d := &models.TenantDomain{Name: "a.example", ExpiresAt: tp(date(2027, 3, 1)), Payment: payment(500, tp(date(2027, 3, 1)), 300, nil)}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Name    string `json:"name"`
		Payment struct {
			ClientCharge struct {
				Amount int64
				Status string
			} `json:"client_charge"`
			RegistrarCost struct {
				Amount        int64
				Status, Payer string
			} `json:"registrar_cost"`
			Margin *int64 `json:"margin"`
		} `json:"payment"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	p := out.Payment
	if out.Name != "a.example" || p.ClientCharge.Status != "paid" || p.RegistrarCost.Status != "unpaid" ||
		p.ClientCharge.Amount != 500 || p.RegistrarCost.Payer != "us" || p.Margin == nil || *p.Margin != 200 {
		t.Fatalf("json = %s", b)
	}
	// No margin unless both sides are set; status none for an empty part.
	b, _ = json.Marshal(&models.TenantDomain{Payment: payment(500, nil, 0, nil)})
	if !strings.Contains(string(b), `"margin":null`) || !strings.Contains(string(b), `"status":"none"`) {
		t.Fatalf("json = %s", b)
	}
}

func TestBuildDomainPaymentValidation(t *testing.T) {
	long := strings.Repeat("r", maxPaymentRefLen+1)
	cases := []struct {
		name string
		mut  func(*models.DomainPayment)
		ok   bool
	}{
		{"empty", func(*models.DomainPayment) {}, true},
		{"full", func(p *models.DomainPayment) {
			p.ClientCharge = models.ClientCharge{Amount: 5, PaidAt: tp(date(2026, 1, 1)), PaidThrough: tp(date(2027, 1, 1)), Method: "bank", Reference: "TX-1"}
			p.RegistrarCost = models.RegistrarCost{Amount: 3, PaidAt: tp(date(2026, 1, 1)), PaidThrough: tp(date(2026, 1, 1)), Payer: "client", Method: "card"}
		}, true},
		{"negative client amount", func(p *models.DomainPayment) { p.ClientCharge.Amount = -1 }, false},
		{"client amount at cap", func(p *models.DomainPayment) { p.ClientCharge.Amount = MaxFeeAmount }, true},
		{"client amount over cap", func(p *models.DomainPayment) { p.ClientCharge.Amount = MaxFeeAmount + 1 }, false},
		{"negative registrar amount", func(p *models.DomainPayment) { p.RegistrarCost.Amount = -5 }, false},
		{"registrar amount over cap", func(p *models.DomainPayment) { p.RegistrarCost.Amount = MaxFeeAmount + 1 }, false},
		{"bad client method", func(p *models.DomainPayment) { p.ClientCharge.Method = "crypto" }, false},
		{"bad registrar method", func(p *models.DomainPayment) { p.RegistrarCost.Method = "wire" }, false},
		{"bad payer", func(p *models.DomainPayment) { p.RegistrarCost.Payer = "them" }, false},
		{"long reference", func(p *models.DomainPayment) { p.ClientCharge.Reference = long }, false},
		{"control char in reference", func(p *models.DomainPayment) { p.RegistrarCost.Reference = "a\x00b" }, false},
		{"newline in reference", func(p *models.DomainPayment) { p.ClientCharge.Reference = "a\nb" }, false},
		{"client paid through before paid at", func(p *models.DomainPayment) {
			p.ClientCharge.PaidAt, p.ClientCharge.PaidThrough = tp(date(2026, 5, 2)), tp(date(2026, 5, 1))
		}, false},
		{"same day is fine", func(p *models.DomainPayment) {
			p.ClientCharge.PaidAt, p.ClientCharge.PaidThrough = tp(date(2026, 5, 1).Add(15*time.Hour)), tp(date(2026, 5, 1))
		}, true},
		{"registrar paid through before paid at", func(p *models.DomainPayment) {
			p.RegistrarCost.PaidAt, p.RegistrarCost.PaidThrough = tp(date(2026, 5, 2)), tp(date(2026, 5, 1))
		}, false},
		{"insane paid date", func(p *models.DomainPayment) { p.ClientCharge.PaidAt = tp(date(1900, 1, 1)) }, false},
		{"insane paid through", func(p *models.DomainPayment) { p.RegistrarCost.PaidThrough = tp(date(3000, 1, 1)) }, false},
	}
	for _, c := range cases {
		in := DomainInput{Name: "pay.example"}
		c.mut(&in.Payment)
		_, err := buildDomain(in)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
		}
		if err != nil && strings.Contains(apiMessage(err), "\x00") {
			t.Errorf("%s: error echoes input", c.name)
		}
	}
}

func TestDomainPaymentStoredAndReplacedOnPut(t *testing.T) {
	svc, _, a, _ := newDomainSvc()
	ctx := context.Background()
	in := DomainInput{Name: "pay.example", ExpiresAt: tp(date(2027, 3, 1))}
	in.Payment = payment(500, tp(date(2027, 3, 1)), 300, nil)
	in.Payment.ClientCharge.Method = "bank"
	d, err := svc.Create(ctx, a.Hex(), in)
	if err != nil {
		t.Fatal(err)
	}
	if d.Payment.ClientCharge.Amount != 500 || d.Payment.ClientCharge.Method != "bank" || d.ClientStatus() != models.PaymentPaid {
		t.Fatalf("created: %+v", d.Payment)
	}
	// PUT replaces the payment like any other field: omitted means cleared.
	d2, err := svc.Update(ctx, a.Hex(), d.ID.Hex(), DomainInput{Name: "pay.example", ExpiresAt: tp(date(2027, 3, 1))})
	if err != nil {
		t.Fatal(err)
	}
	if d2.Payment != (models.DomainPayment{}) || d2.ClientStatus() != models.PaymentNone {
		t.Fatalf("after replace: %+v", d2.Payment)
	}
	// Statuses sent by a client are ignored: the input type has no such field.
	var body DomainInput
	if err := json.Unmarshal([]byte(`{"name":"x.example","payment":{"client_charge":{"amount":9,"status":"paid"}}}`), &body); err != nil {
		t.Fatal(err)
	}
	got, err := buildDomain(body)
	if err != nil || got.ClientStatus() != models.PaymentUnpaid {
		t.Fatalf("status must be derived: %v %v", got, err)
	}
}

// ── Costing ──────────────────────────────────────────────────────────────

func feeView(t *testing.T, c *Costing, id string) FeeView {
	t.Helper()
	for _, f := range c.Fees {
		if f.ID.Hex() == id {
			return f
		}
	}
	t.Fatalf("fee %s not in costing", id)
	return FeeView{}
}

func TestUnpaidDomainFeeIsDueNowAndTakesTheClientChargeAmount(t *testing.T) {
	fx := newCostFixture()
	ctx := context.Background()
	far := fx.addDomain(t, fx.a, "far.example", days(300)) // far outside the lead window
	none := fx.addDomain(t, fx.a, "none.example", nil)     // no expiry at all
	fFar := fx.addFee(t, fx.a, FeeInput{Kind: "domain", Label: "far", Amount: 7, Interval: "yearly", DomainID: far.Hex()})
	fNone := fx.addFee(t, fx.a, FeeInput{Kind: "domain", Label: "none", Amount: 9, Interval: "yearly", DomainID: none.Hex()})
	fx.setPayment(t, far, payment(250000, nil, 0, nil))
	fx.setPayment(t, none, payment(120000, nil, 0, nil))

	c, err := fx.svc.Costing(ctx, fx.a.Hex())
	if err != nil {
		t.Fatal(err)
	}
	vf := feeView(t, c, fFar.ID.Hex())
	if !vf.Due || vf.Amount != 250000 || vf.PaymentStatus != "unpaid" {
		t.Errorf("far unpaid: %+v", vf)
	}
	vn := feeView(t, c, fNone.ID.Hex())
	if !vn.Due || vn.Amount != 120000 || vn.NextDueAt != nil {
		t.Errorf("no-expiry unpaid: %+v", vn)
	}
	if c.DueTotal != 370000 || c.Payables.ReceivableTotal != 370000 {
		t.Errorf("due total %d, receivable %d", c.DueTotal, c.Payables.ReceivableTotal)
	}
}

func TestPaidDomainFeeIsNotDueUntilRenewal(t *testing.T) {
	fx := newCostFixture()
	ctx := context.Background()
	// Expires in 5 days: inside the lead window, so without payment it is due.
	dom := fx.addDomain(t, fx.a, "soon.example", days(5))
	fee := fx.addFee(t, fx.a, FeeInput{Kind: "domain", Label: "soon", Amount: 7, Interval: "yearly", DomainID: dom.Hex()})
	fx.setPayment(t, dom, payment(250000, days(5), 0, nil))

	c, _ := fx.svc.Costing(ctx, fx.a.Hex())
	v := feeView(t, c, fee.ID.Hex())
	if v.Due || v.PaymentStatus != "paid" || v.Amount != 250000 || c.DueTotal != 0 || c.Payables.ReceivableTotal != 0 {
		t.Fatalf("paid: %+v total %d", v, c.DueTotal)
	}

	// Renewal: expires_at moves forward, the charge is unpaid again and due now.
	fx.domain(t, dom).ExpiresAt = days(370)
	c, _ = fx.svc.Costing(ctx, fx.a.Hex())
	v = feeView(t, c, fee.ID.Hex())
	if !v.Due || v.PaymentStatus != "unpaid" || c.DueTotal != 250000 {
		t.Fatalf("after renewal: %+v total %d", v, c.DueTotal)
	}
}

func TestDomainFeeFallsBackToItsOwnAmountWithoutAClientCharge(t *testing.T) {
	fx := newCostFixture()
	ctx := context.Background()
	near := fx.addDomain(t, fx.a, "near.example", days(10))
	far := fx.addDomain(t, fx.a, "far.example", days(200))
	fNear := fx.addFee(t, fx.a, FeeInput{Kind: "domain", Label: "near", Amount: 42000, Interval: "yearly", DomainID: near.Hex()})
	fFar := fx.addFee(t, fx.a, FeeInput{Kind: "domain", Label: "far", Amount: 5, Interval: "yearly", DomainID: far.Hex()})
	// Only a registrar cost: still no client charge.
	fx.setPayment(t, far, payment(0, nil, 900, nil))

	c, _ := fx.svc.Costing(ctx, fx.a.Hex())
	vn, vf := feeView(t, c, fNear.ID.Hex()), feeView(t, c, fFar.ID.Hex())
	if !vn.Due || vn.Amount != 42000 || vn.PaymentStatus != "none" {
		t.Errorf("near (expiry window): %+v", vn)
	}
	if vf.Due || vf.Amount != 5 {
		t.Errorf("far (not due, own amount): %+v", vf)
	}
	if len(c.DomainLines) != 0 {
		t.Errorf("no client charge, no derived line: %+v", c.DomainLines)
	}
}

func TestDerivedDomainLineAndInvoiceSnapshot(t *testing.T) {
	fx := newInvFixture()
	ctx := context.Background()
	tid := fx.a.Hex()
	dom := fx.addDomain(t, fx.a, "solo.example", days(200))
	fx.setPayment(t, dom, payment(300000, nil, 0, nil))
	id := DomainLinePrefix + dom.Hex()

	c, err := fx.svc.Costing(ctx, tid)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Fees) != 0 || len(c.DomainLines) != 1 {
		t.Fatalf("costing: %+v", c)
	}
	l := c.DomainLines[0]
	if l.ID != id || l.DomainID != dom.Hex() || l.Amount != 300000 || !l.Due || l.PaymentStatus != "unpaid" || l.DomainName != "solo.example" {
		t.Fatalf("derived line: %+v", l)
	}
	if c.DueTotal != 300000 {
		t.Fatalf("due total = %d", c.DueTotal)
	}

	inv, err := fx.inv.Create(ctx, tid, InvoiceInput{FeeIDs: []string{id}})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Lines) != 1 {
		t.Fatalf("lines: %+v", inv.Lines)
	}
	ln := inv.Lines[0]
	if ln.Kind != models.FeeKindDomain || ln.FeeID != nil || ln.DomainID == nil || *ln.DomainID != dom ||
		ln.Amount != 300000 || ln.Period != date(2027, 5, 4).Format(dateLayout) {
		t.Fatalf("snapshot: %+v", ln)
	}
	if inv.Total != 300000 || len(inv.PeriodKeys) != 1 || !strings.Contains(inv.PeriodKeys[0], "d-"+dom.Hex()) {
		t.Fatalf("invoice: total %d keys %v", inv.Total, inv.PeriodKeys)
	}

	// The same period cannot go on a second live invoice (read check) ...
	_, err = fx.inv.Create(ctx, tid, InvoiceInput{FeeIDs: []string{id}})
	if domStatus(err) != http.StatusConflict {
		t.Fatalf("duplicate: status %d (%v)", domStatus(err), err)
	}
	_, err = fx.inv.Create(ctx, tid, InvoiceInput{AllDue: true})
	if domStatus(err) != http.StatusConflict {
		t.Fatalf("duplicate via all_due: status %d (%v)", domStatus(err), err)
	}
	// ... nor when the read check is bypassed (unique index backstop).
	racy := NewInvoiceService(racyStore{fx.st}, fx.svc, fx.fees, fx.domains, fx.tenantsM, fx.mail, fx.ps, "Inno Nomads")
	racy.now = func() time.Time { return testNow }
	if _, err = racy.Create(ctx, tid, InvoiceInput{FeeIDs: []string{id}}); domStatus(err) != http.StatusConflict {
		t.Fatalf("race: status %d (%v)", domStatus(err), err)
	}
	// A different domain with the same expiry is not a duplicate.
	other := fx.addDomain(t, fx.a, "twin.example", days(200))
	fx.setPayment(t, other, payment(1000, nil, 0, nil))
	if _, err := fx.inv.Create(ctx, tid, InvoiceInput{FeeIDs: []string{DomainLinePrefix + other.Hex()}}); err != nil {
		t.Fatalf("other domain: %v", err)
	}
	// Void frees the period.
	if _, err := fx.inv.Void(ctx, tid, inv.ID.Hex()); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.inv.Create(ctx, tid, InvoiceInput{FeeIDs: []string{id}}); err != nil {
		t.Fatalf("after void: %v", err)
	}
}

func TestDerivedDomainLineIdsAreValidatedAndTenantScoped(t *testing.T) {
	fx := newInvFixture()
	ctx := context.Background()
	dom := fx.addDomain(t, fx.b, "theirs.example", days(10))
	fx.setPayment(t, dom, payment(1000, nil, 0, nil))

	// Tenant A names tenant B's domain: indistinguishable from a missing one.
	_, err := fx.inv.Create(ctx, fx.a.Hex(), InvoiceInput{FeeIDs: []string{DomainLinePrefix + dom.Hex()}})
	if domStatus(err) != http.StatusNotFound {
		t.Fatalf("cross-tenant: status %d (%v)", domStatus(err), err)
	}
	_, err = fx.inv.Create(ctx, fx.a.Hex(), InvoiceInput{FeeIDs: []string{DomainLinePrefix + "nothex"}})
	if domStatus(err) != http.StatusBadRequest {
		t.Fatalf("malformed: status %d (%v)", domStatus(err), err)
	}
	// A paid derived line has no outstanding amount only by status; a domain
	// with a stored fee has no derived line.
	own := fx.addDomain(t, fx.a, "mine.example", days(10))
	fx.setPayment(t, own, payment(1000, nil, 0, nil))
	fx.addFee(t, fx.a, FeeInput{Kind: "domain", Label: "mine", Amount: 1, Interval: "yearly", DomainID: own.Hex()})
	_, err = fx.inv.Create(ctx, fx.a.Hex(), InvoiceInput{FeeIDs: []string{DomainLinePrefix + own.Hex()}})
	if domStatus(err) != http.StatusNotFound {
		t.Fatalf("domain with a fee row must have no derived line: status %d (%v)", domStatus(err), err)
	}
}

func TestDerivedDomainLineWithNoExpiryCanBeInvoicedOnce(t *testing.T) {
	fx := newInvFixture()
	ctx := context.Background()
	tid := fx.a.Hex()
	dom := fx.addDomain(t, fx.a, "open.example", nil)
	fx.setPayment(t, dom, payment(5000, nil, 0, nil))
	id := DomainLinePrefix + dom.Hex()

	inv, err := fx.inv.Create(ctx, tid, InvoiceInput{FeeIDs: []string{id}})
	if err != nil {
		t.Fatal(err)
	}
	if inv.Lines[0].Period != noExpiryPeriod {
		t.Fatalf("period = %q", inv.Lines[0].Period)
	}
	if _, err := fx.inv.Create(ctx, tid, InvoiceInput{FeeIDs: []string{id}}); domStatus(err) != http.StatusConflict {
		t.Fatalf("duplicate: %v", err)
	}
}

func (fx *invFixture) sendAndPay(t *testing.T, tid string, invID primitive.ObjectID) {
	t.Helper()
	ctx := context.Background()
	if _, err := fx.inv.Send(ctx, tid, invID.Hex(), SendInput{}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if _, err := fx.inv.MarkPaid(ctx, tid, invID.Hex(), PaidInput{}); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
}

func TestMarkPaidSettlesTheDomainClientChargeMonotone(t *testing.T) {
	fx := newInvFixture()
	ctx := context.Background()
	tid := fx.a.Hex()
	derived := fx.addDomain(t, fx.a, "derived.example", days(200))
	linked := fx.addDomain(t, fx.a, "linked.example", days(10))
	fx.setPayment(t, derived, payment(300000, nil, 0, nil))
	fx.setPayment(t, linked, payment(80000, nil, 0, nil))
	fx.domain(t, linked).Payment.ClientCharge.Method = "bank"
	fx.domain(t, linked).Payment.ClientCharge.Reference = "REF-9"
	fx.addFee(t, fx.a, FeeInput{Kind: "domain", Label: "linked", Amount: 1, Interval: "yearly", DomainID: linked.Hex()})

	inv, err := fx.inv.Create(ctx, tid, InvoiceInput{AllDue: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Lines) != 2 || inv.Subtotal != 380000 {
		t.Fatalf("invoice: %+v", inv)
	}
	for _, l := range inv.Lines {
		if l.DomainID == nil {
			t.Fatalf("domain line without domain id: %+v", l)
		}
	}
	fx.sendAndPay(t, tid, inv.ID)

	for id, want := range map[primitive.ObjectID]time.Time{derived: *days(200), linked: *days(10)} {
		d := fx.domain(t, id)
		cc := d.Payment.ClientCharge
		if cc.PaidAt == nil || !cc.PaidAt.Equal(testNow) || cc.PaidThrough == nil || cc.PaidThrough.Format(dateLayout) != want.Format(dateLayout) {
			t.Errorf("%s: paid_at %v paid_through %v", d.Name, cc.PaidAt, cc.PaidThrough)
		}
		if d.ClientStatus() != models.PaymentPaid {
			t.Errorf("%s status %s", d.Name, d.ClientStatus())
		}
	}
	lc := fx.domain(t, linked).Payment.ClientCharge
	if lc.Method != "bank" || lc.Reference != "REF-9" {
		t.Errorf("method/reference must be untouched: %+v", lc)
	}

	// Replays and an earlier period never move paid_through back.
	later := fx.domain(t, derived).Payment.ClientCharge.PaidThrough
	fx.domains.MarkClientPaid(ctx, fx.a, derived, testNow.Add(time.Hour), *days(50), "", "")
	if got := fx.domain(t, derived).Payment.ClientCharge.PaidThrough; !got.Equal(*later) {
		t.Fatalf("paid_through moved back: %v", got)
	}
	if _, err := fx.inv.MarkPaid(ctx, tid, inv.ID.Hex(), PaidInput{}); domStatus(err) != http.StatusConflict {
		t.Fatalf("replay: %v", err)
	}
	if got := fx.domain(t, derived).Payment.ClientCharge.PaidThrough; !got.Equal(*later) {
		t.Fatalf("replay moved paid_through: %v", got)
	}

	// Renewal makes both unpaid and due again.
	fx.domain(t, derived).ExpiresAt = days(570)
	c, _ := fx.svc.Costing(ctx, tid)
	if len(c.DomainLines) != 1 || !c.DomainLines[0].Due || c.DomainLines[0].PaymentStatus != "unpaid" {
		t.Fatalf("after renewal: %+v", c.DomainLines)
	}
}

func TestMarkPaidWithNoExpiryPaysAYearAhead(t *testing.T) {
	fx := newInvFixture()
	tid := fx.a.Hex()
	dom := fx.addDomain(t, fx.a, "open.example", nil)
	fx.setPayment(t, dom, payment(5000, nil, 0, nil))
	inv, err := fx.inv.Create(context.Background(), tid, InvoiceInput{FeeIDs: []string{DomainLinePrefix + dom.Hex()}})
	if err != nil {
		t.Fatal(err)
	}
	fx.sendAndPay(t, tid, inv.ID)
	got := fx.domain(t, dom).Payment.ClientCharge.PaidThrough
	if got == nil || got.Format(dateLayout) != "2027-10-16" {
		t.Fatalf("paid_through = %v, want today + 365d", got)
	}
}

// ── Registrar side ───────────────────────────────────────────────────────

func TestRegistrarPaidIsTenantScopedAndDefaults(t *testing.T) {
	svc, st, a, b := newDomainSvc()
	svc.now = func() time.Time { return testNow }
	ctx := context.Background()
	in := DomainInput{Name: "reg.example", ExpiresAt: tp(date(2027, 3, 1))}
	in.Payment = payment(500, nil, 300, nil)
	in.Payment.RegistrarCost.Payer = ""
	in.Payment.RegistrarCost.Reference = "keep-me"
	d, err := svc.Create(ctx, a.Hex(), in)
	if err != nil {
		t.Fatal(err)
	}
	if d.RegistrarStatus() != models.PaymentUnpaid {
		t.Fatalf("status = %s", d.RegistrarStatus())
	}

	// Another tenant cannot settle it and cannot tell it exists.
	if _, err := svc.RegistrarPaid(ctx, b.Hex(), d.ID.Hex(), RegistrarPaidInput{}); domStatus(err) != http.StatusNotFound {
		t.Fatalf("cross-tenant: status %d (%v)", domStatus(err), err)
	}
	if st.docs[0].Payment.RegistrarCost.PaidThrough != nil {
		t.Fatal("cross-tenant call changed the record")
	}
	if _, err := svc.RegistrarPaid(ctx, a.Hex(), "nothex", RegistrarPaidInput{}); domStatus(err) != http.StatusBadRequest {
		t.Fatalf("bad id: %v", err)
	}
	if _, err := svc.RegistrarPaid(ctx, a.Hex(), primitive.NewObjectID().Hex(), RegistrarPaidInput{}); domStatus(err) != http.StatusNotFound {
		t.Fatalf("unknown id: %v", err)
	}

	got, err := svc.RegistrarPaid(ctx, a.Hex(), d.ID.Hex(), RegistrarPaidInput{})
	if err != nil {
		t.Fatal(err)
	}
	rc := got.Payment.RegistrarCost
	if rc.PaidAt == nil || !rc.PaidAt.Equal(testNow) || rc.PaidThrough == nil || !rc.PaidThrough.Equal(date(2027, 3, 1)) ||
		rc.Payer != "us" || rc.Reference != "keep-me" || rc.Amount != 300 {
		t.Fatalf("defaults: %+v", rc)
	}
	if got.RegistrarStatus() != models.PaymentPaid || got.ClientStatus() != models.PaymentUnpaid {
		t.Fatalf("statuses %s/%s", got.RegistrarStatus(), got.ClientStatus())
	}

	// Explicit values win; the cost stays settled monotone for defaults.
	got, err = svc.RegistrarPaid(ctx, a.Hex(), d.ID.Hex(), RegistrarPaidInput{
		PaidAt: tp(date(2026, 9, 1)), PaidThrough: tp(date(2028, 3, 1)), Payer: "client", Method: "card", Reference: "R-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	rc = got.Payment.RegistrarCost
	if rc.Payer != "client" || rc.Method != "card" || rc.Reference != "R-2" || !rc.PaidThrough.Equal(date(2028, 3, 1)) {
		t.Fatalf("explicit: %+v", rc)
	}
	got, _ = svc.RegistrarPaid(ctx, a.Hex(), d.ID.Hex(), RegistrarPaidInput{})
	if !got.Payment.RegistrarCost.PaidThrough.Equal(date(2028, 3, 1)) {
		t.Fatalf("default moved paid_through back: %v", got.Payment.RegistrarCost.PaidThrough)
	}
}

func TestRegistrarPaidValidatesAndDefaultsWithoutExpiry(t *testing.T) {
	svc, _, a, _ := newDomainSvc()
	svc.now = func() time.Time { return testNow }
	ctx := context.Background()
	d, err := svc.Create(ctx, a.Hex(), DomainInput{Name: "open.example", Payment: payment(0, nil, 100, nil)})
	if err != nil {
		t.Fatal(err)
	}
	bad := []RegistrarPaidInput{
		{Payer: "them"},
		{Method: "wire"},
		{Reference: strings.Repeat("x", maxPaymentRefLen+1)},
		{PaidAt: tp(date(2026, 5, 2)), PaidThrough: tp(date(2026, 5, 1))},
		{PaidAt: tp(date(1800, 1, 1))},
	}
	for i, in := range bad {
		if _, err := svc.RegistrarPaid(ctx, a.Hex(), d.ID.Hex(), in); domStatus(err) != http.StatusUnprocessableEntity {
			t.Errorf("case %d: status %d (%v)", i, domStatus(err), err)
		}
	}
	got, err := svc.RegistrarPaid(ctx, a.Hex(), d.ID.Hex(), RegistrarPaidInput{})
	if err != nil {
		t.Fatal(err)
	}
	if th := got.Payment.RegistrarCost.PaidThrough; th == nil || th.Format(dateLayout) != "2027-10-16" {
		t.Fatalf("paid_through = %v", th)
	}
}

func TestCostingPayablesMarginAndTotals(t *testing.T) {
	fx := newCostFixture()
	ctx := context.Background()
	// d1: client unpaid 500000, registrar unpaid 300000 (payer us) -> margin 200000.
	d1 := fx.addDomain(t, fx.a, "one.example", days(100))
	fx.setPayment(t, d1, payment(500000, nil, 300000, nil))
	// d2: client paid, registrar unpaid (payer client), margin 20000.
	d2 := fx.addDomain(t, fx.a, "two.example", days(100))
	fx.setPayment(t, d2, payment(60000, days(100), 40000, nil))
	fx.domain(t, d2).Payment.RegistrarCost.Payer = models.PayerClient
	// d3: both paid -> not receivable, not payable, margin still reported.
	d3 := fx.addDomain(t, fx.a, "three.example", days(100))
	fx.setPayment(t, d3, payment(10000, days(100), 4000, days(100)))
	// d4: only a registrar cost -> payable, no margin.
	d4 := fx.addDomain(t, fx.a, "four.example", nil)
	fx.setPayment(t, d4, payment(0, nil, 7000, nil))
	// Other tenant's domain never appears.
	other := fx.addDomain(t, fx.b, "other.example", days(10))
	fx.setPayment(t, other, payment(999, nil, 999, nil))

	c, err := fx.svc.Costing(ctx, fx.a.Hex())
	if err != nil {
		t.Fatal(err)
	}
	p := c.Payables
	if p.ReceivableTotal != 500000 {
		t.Errorf("receivable_total = %d", p.ReceivableTotal)
	}
	if p.PayableTotal != 347000 || len(p.Items) != 3 {
		t.Errorf("payable_total = %d items %+v", p.PayableTotal, p.Items)
	}
	byName := map[string]PayableItem{}
	for _, it := range p.Items {
		byName[it.DomainName] = it
	}
	one := byName["one.example"]
	if one.Amount != 300000 || one.Payer != "us" || one.ExpiresAt == nil || one.Margin == nil || *one.Margin != 200000 {
		t.Errorf("one: %+v", one)
	}
	if two := byName["two.example"]; two.Payer != "client" || two.Margin == nil || *two.Margin != 20000 {
		t.Errorf("two: %+v", two)
	}
	if four := byName["four.example"]; four.Margin != nil || four.ExpiresAt != nil {
		t.Errorf("four: %+v", four)
	}
	margins := map[string]int64{}
	for _, m := range p.Margins {
		margins[m.DomainName] = m.Margin
	}
	if len(margins) != 3 || margins["one.example"] != 200000 || margins["two.example"] != 20000 || margins["three.example"] != 6000 {
		t.Errorf("margins = %v", margins)
	}
	// Registrar costs are never invoiceable lines: only d1's client charge is a line.
	if len(c.DomainLines) != 3 {
		t.Errorf("domain lines = %d, want one per domain with a client charge (3)", len(c.DomainLines))
	}
	if c.DueTotal != 500000 {
		t.Errorf("due total = %d (registrar costs must not be in it)", c.DueTotal)
	}
}
