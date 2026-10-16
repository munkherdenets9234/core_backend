package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/mailer"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// fakeInvoiceStore mimics the repository: tenant scoping, an atomic counter
// per year, a unique number index, a conditional status transition and the
// non-void line check.
type fakeInvoiceStore struct {
	mu       sync.Mutex
	docs     []*models.Invoice
	counters map[[2]int]int64
	// forceSeq, when > 0, makes NextSeq return it every time (to prove the
	// unique index is a backstop).
	forceSeq int64
}

func newFakeInvoiceStore() *fakeInvoiceStore { return &fakeInvoiceStore{counters: map[[2]int]int64{}} }

func (f *fakeInvoiceStore) NextSeq(_ context.Context, year, month int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.forceSeq > 0 {
		return f.forceSeq, nil
	}
	k := [2]int{year, month}
	f.counters[k]++
	return f.counters[k], nil
}

func (f *fakeInvoiceStore) Insert(_ context.Context, inv *models.Invoice) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.docs {
		if e.Number == inv.Number {
			return errDup
		}
		// The unique partial index over period_keys (void invoices have none).
		if e.Status != models.InvoiceVoid {
			for _, k := range e.PeriodKeys {
				for _, nk := range inv.PeriodKeys {
					if k == nk {
						return errPeriodKeysDup
					}
				}
			}
		}
	}
	inv.ID = primitive.NewObjectID()
	inv.CreatedAt, inv.UpdatedAt = testNow, testNow
	c := *inv
	f.docs = append(f.docs, &c)
	return nil
}

func (f *fakeInvoiceStore) ListByTenant(_ context.Context, tid primitive.ObjectID) ([]*models.Invoice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []*models.Invoice{}
	for _, d := range f.docs {
		if d.TenantID == tid {
			c := *d
			out = append(out, &c)
		}
	}
	return out, nil
}

func (f *fakeInvoiceStore) CountByTenant(ctx context.Context, tid primitive.ObjectID) (int64, error) {
	l, _ := f.ListByTenant(ctx, tid)
	return int64(len(l)), nil
}

func (f *fakeInvoiceStore) Find(_ context.Context, tid, id primitive.ObjectID) (*models.Invoice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.docs {
		if d.ID == id && d.TenantID == tid {
			c := *d
			return &c, nil
		}
	}
	return nil, mongo.ErrNoDocuments
}

func (f *fakeInvoiceStore) LineHolder(_ context.Context, tid primitive.ObjectID, kind string, feeID, domainID *primitive.ObjectID, period string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.docs {
		if d.TenantID != tid || d.Status == models.InvoiceVoid {
			continue
		}
		for _, l := range d.Lines {
			if l.Kind == kind && l.Period == period && (feeID == nil && domainID == nil || feeID != nil && l.FeeID != nil && *l.FeeID == *feeID || feeID == nil && domainID != nil && l.DomainID != nil && *l.DomainID == *domainID) {
				return d.Number, nil
			}
		}
	}
	return "", nil
}

func (f *fakeInvoiceStore) ListLive(_ context.Context, tid primitive.ObjectID) ([]*models.Invoice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []*models.Invoice{}
	for _, d := range f.docs {
		if d.TenantID == tid && d.Status != models.InvoiceVoid {
			c := *d
			out = append(out, &c)
		}
	}
	return out, nil
}

// DeleteDraft mimics the conditional delete: only a draft of the tenant goes.
func (f *fakeInvoiceStore) DeleteDraft(_ context.Context, tid, id primitive.ObjectID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, d := range f.docs {
		if d.ID == id && d.TenantID == tid && d.Status == models.InvoiceDraft {
			f.docs = append(f.docs[:i], f.docs[i+1:]...)
			return nil
		}
	}
	return mongo.ErrNoDocuments
}

func (f *fakeInvoiceStore) Transition(_ context.Context, tid, id primitive.ObjectID, from []string, set bson.M) (*models.Invoice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.docs {
		if d.ID != id || d.TenantID != tid {
			continue
		}
		allowed := false
		for _, s := range from {
			allowed = allowed || d.Status == s
		}
		if !allowed {
			return nil, mongo.ErrNoDocuments
		}
		if v, ok := set["status"].(string); ok {
			d.Status = v
		}
		if v, ok := set["sent_at"].(time.Time); ok {
			d.SentAt = &v
		}
		if v, ok := set["sent_to"].(string); ok {
			d.SentTo = v
		}
		if v, ok := set["paid_at"].(time.Time); ok {
			d.PaidAt = &v
		}
		if v, ok := set["payment"].(*models.InvoicePayment); ok {
			d.Payment = v
		}
		if v, ok := set["payment_instructions"].(*models.PaymentInstructions); ok {
			d.PaymentInstructions = v
		}
		if v, ok := set["send_prev"].(string); ok {
			d.SendPrev = v
		}
		if v, ok := set["due_at"].(time.Time); ok {
			d.DueAt = &v
		}
		if v, ok := set["tax_rate_bp"].(int); ok {
			d.TaxRateBP = v
		}
		if v, ok := set["tax_amount"].(int64); ok {
			d.TaxAmount = v
		}
		if v, ok := set["total"].(int64); ok {
			d.Total = v
		}
		if v, ok := set["lines"].([]models.InvoiceLine); ok {
			d.Lines = append([]models.InvoiceLine(nil), v...)
		}
		if v, ok := set["subtotal"].(int64); ok {
			d.Subtotal = v
		}
		if v, ok := set["discount_total"].(int64); ok {
			d.DiscountTotal = v
		}
		d.UpdatedAt = testNow.Add(time.Minute)
		if _, ok := set["period_keys"]; ok {
			d.PeriodKeys = nil
		}
		c := *d
		return &c, nil
	}
	return nil, mongo.ErrNoDocuments
}

type invSentMail struct {
	to   string
	tmpl mailer.Template
	data map[string]string
}

// errPeriodKeysDup is what the unique period_keys index returns.
var errPeriodKeysDup = mongo.WriteException{WriteErrors: mongo.WriteErrors{{
	Code: 11000, Message: "E11000 duplicate key error collection: x.invoices index: invoice_period_keys_unique dup key",
}}}

type invFakeMail struct {
	avail bool
	err   error
	sent  []invSentMail
	// during runs inside Send, as another request would while the mail is
	// being handed over.
	during func()
}

func (f *invFakeMail) Available() bool { return f.avail }
func (f *invFakeMail) Send(to string, tmpl mailer.Template, data map[string]string) error {
	if f.during != nil {
		f.during()
	}
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, invSentMail{to, tmpl, data})
	return nil
}

type invFixture struct {
	*costFixture
	inv  *InvoiceService
	st   *fakeInvoiceStore
	mail *invFakeMail
	ps   *fakePaySettings
}

func newInvFixture() *invFixture {
	cf := newCostFixture()
	st, mail := newFakeInvoiceStore(), &invFakeMail{avail: true}
	ps := &fakePaySettings{p: &models.PaymentSettings{
		CompanyName: "InnoNomads LLC", CompanyTagline: "Вэб үйлчилгээ · Улаанбаатар, Монгол Улс", ContactPerson: "Мөнх-Эрдэнэ",
		BankName: "Khan Bank", AccountName: "Inno Nomads LLC", AccountNumber: "5012345678", Instructions: "Use the invoice number as the transfer description",
	}}
	inv := NewInvoiceService(st, cf.svc, cf.fees, cf.domains, cf.tenantsM, mail, ps, "Inno Nomads")
	inv.now = func() time.Time { return testNow }
	cf.svc.SetInvoiceSource(st)
	return &invFixture{costFixture: cf, inv: inv, st: st, mail: mail, ps: ps}
}

// seed gives tenant A: a domain fee due in 10 days (100000), a monthly
// deployment fee never invoiced (40000, due now) and an active subscription
// (price 1000000) ending in 5 days; plus a domain fee not due for 90 days.
func (fx *invFixture) seed(t *testing.T) (domainFee, deployFee, farFee *models.TenantFee) {
	t.Helper()
	d := fx.addDomain(t, fx.a, "tower.example", days(10))
	far := fx.addDomain(t, fx.a, "far.example", days(90))
	domainFee = fx.addFee(t, fx.a, FeeInput{Kind: "domain", Label: "Domain tower.example", Amount: 100000, Interval: "yearly", DomainID: d.Hex()})
	deployFee = fx.addFee(t, fx.a, FeeInput{Kind: "deployment", Label: "Hosting", Amount: 40000, Interval: "monthly"})
	farFee = fx.addFee(t, fx.a, FeeInput{Kind: "domain", Label: "Domain far.example", Amount: 5, Interval: "yearly", DomainID: far.Hex()})
	fx.subscribe(fx.a, 1000000, models.SubscriptionActive, 5)
	return
}

func (fx *invFixture) createAllDue(t *testing.T, bp *int) *models.Invoice {
	t.Helper()
	inv, err := fx.inv.Create(context.Background(), fx.a.Hex(), InvoiceInput{AllDue: true, TaxRateBP: bp})
	if err != nil {
		t.Fatalf("create invoice: %v", err)
	}
	return inv
}

func intp(i int) *int { return &i }

func TestTaxAmountRoundsHalfUp(t *testing.T) {
	cases := []struct {
		subtotal int64
		bp       int
		want     int64
	}{
		{1000, 0, 0},
		{1000, 1000, 100},
		{5, 1000, 1},       // 0.5 -> 1
		{15, 1000, 2},      // 1.5 -> 2
		{14, 1000, 1},      // 1.4 -> 1
		{16, 1000, 2},      // 1.6 -> 2
		{1, 1, 0},          // 0.0001 -> 0
		{10000, 5, 5},      // 5.0
		{12345, 825, 1018}, // 1018.4625 -> 1018
		{100, 10000, 100},
		{0, 10000, 0},
		{MaxInvoiceLines * MaxFeeAmount, 10000, MaxInvoiceLines * MaxFeeAmount},
	}
	for _, c := range cases {
		if got := TaxAmount(c.subtotal, c.bp); got != c.want {
			t.Errorf("TaxAmount(%d, %d) = %d, want %d", c.subtotal, c.bp, got, c.want)
		}
	}
}

func TestInvoiceTotals(t *testing.T) {
	lines := []models.InvoiceLine{{Amount: 100000}, {Amount: 40000}, {Amount: 1}}
	sub, tax, total := InvoiceTotals(lines, 1000)
	if sub != 140001 || tax != 14000 || total != 154001 {
		t.Fatalf("totals = %d %d %d", sub, tax, total)
	}
	if s, x, tt := InvoiceTotals(nil, 1000); s != 0 || x != 0 || tt != 0 {
		t.Fatalf("empty = %d %d %d", s, x, tt)
	}
}

func TestInvoiceNumberFormat(t *testing.T) {
	cases := []struct {
		year, month int
		seq         int64
		want        string
	}{
		{2026, 9, 1, "INV-2026-09-001"},
		{2026, 10, 42, "INV-2026-10-042"},
		{2026, 12, 999, "INV-2026-12-999"},
		{2027, 1, 1000, "INV-2027-01-1000"},
		{2027, 11, 12345, "INV-2027-11-12345"},
	}
	for _, c := range cases {
		if got := InvoiceNumber(c.year, c.month, c.seq); got != c.want {
			t.Errorf("InvoiceNumber(%d, %d, %d) = %q, want %q", c.year, c.month, c.seq, got, c.want)
		}
	}
}

func TestFormatMoney(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 7: "7", 999: "999", 1000: "1,000", 1234567: "1,234,567", 100000000: "100,000,000", -1500: "-1,500"} {
		if got := FormatMNT(n); got != want {
			t.Errorf("FormatMNT(%d) = %q, want %q", n, got, want)
		}
	}
	for bp, want := range map[int]string{0: "0%", 1000: "10%", 825: "8.25%", 850: "8.5%", 5: "0.05%", 10000: "100%"} {
		if got := FormatBP(bp); got != want {
			t.Errorf("FormatBP(%d) = %q, want %q", bp, got, want)
		}
	}
}

func TestCreateComputesEverythingOnTheServer(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)

	inv := fx.createAllDue(t, intp(1000))
	// Due: domain 100000, deployment 40000, subscription 1000000. Far domain is not.
	if len(inv.Lines) != 3 {
		t.Fatalf("lines = %+v", inv.Lines)
	}
	if inv.Lines[0].Kind != models.FeeKindSubscription || inv.Lines[0].FeeID != nil || inv.Lines[0].Amount != 1000000 {
		t.Errorf("first line should be the derived subscription: %+v", inv.Lines[0])
	}
	if inv.Subtotal != 1140000 || inv.TaxRateBP != 1000 || inv.TaxAmount != 114000 || inv.Total != 1254000 {
		t.Errorf("amounts: %d / %d bp / %d / %d", inv.Subtotal, inv.TaxRateBP, inv.TaxAmount, inv.Total)
	}
	if inv.Status != models.InvoiceDraft || inv.Currency != "MNT" || inv.Number != "INV-2026-10-001" {
		t.Errorf("status=%q currency=%q number=%q", inv.Status, inv.Currency, inv.Number)
	}
	if inv.SentAt != nil || inv.PaidAt != nil || inv.SentTo != "" || !inv.IssuedAt.Equal(testNow) || inv.TenantID != fx.a {
		t.Errorf("fresh invoice has stray fields: %+v", inv)
	}
	for _, l := range inv.Lines[1:] {
		if l.FeeID == nil || l.Period == "" {
			t.Errorf("fee line without id/period: %+v", l)
		}
	}
}

func TestCreateWithoutTaxAndWithDueDate(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	due := testNow.Add(14 * 24 * time.Hour)
	inv, err := fx.inv.Create(context.Background(), fx.a.Hex(), InvoiceInput{AllDue: true, DueAt: &due})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if inv.TaxRateBP != 0 || inv.TaxAmount != 0 || inv.Total != inv.Subtotal {
		t.Errorf("no tax expected: %+v", inv)
	}
	if inv.DueAt == nil || !inv.DueAt.Equal(due) {
		t.Errorf("due date = %v", inv.DueAt)
	}
}

func TestCreateValidatesTaxRateAndDueDate(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	ctx := context.Background()
	for _, bp := range []int{-1, 10001, 1 << 30} {
		if _, err := fx.inv.Create(ctx, fx.a.Hex(), InvoiceInput{AllDue: true, TaxRateBP: intp(bp)}); domStatus(err) != http.StatusUnprocessableEntity {
			t.Errorf("tax %d: %v", bp, err)
		}
	}
	for _, bp := range []int{0, 1, 10000} {
		fx2 := newInvFixture()
		fx2.seed(t)
		if _, err := fx2.inv.Create(ctx, fx2.a.Hex(), InvoiceInput{AllDue: true, TaxRateBP: intp(bp)}); err != nil {
			t.Errorf("tax %d refused: %v", bp, err)
		}
	}
	yesterday := testNow.Add(-48 * time.Hour)
	if _, err := fx.inv.Create(ctx, fx.a.Hex(), InvoiceInput{AllDue: true, DueAt: &yesterday}); domStatus(err) != http.StatusUnprocessableEntity {
		t.Errorf("due date in the past: %v", err)
	}
	if len(fx.st.docs) != 0 {
		t.Fatal("a refused request created an invoice")
	}
}

func TestCreateSelection(t *testing.T) {
	fx := newInvFixture()
	_, deployFee, farFee := fx.seed(t)
	ctx := context.Background()
	tid := fx.a.Hex()

	// No lines at all.
	if _, err := fx.inv.Create(ctx, tid, InvoiceInput{}); domStatus(err) != http.StatusUnprocessableEntity {
		t.Errorf("empty request: %v", err)
	}
	// Explicit ids, including a fee that is not due yet and the derived line.
	inv, err := fx.inv.Create(ctx, tid, InvoiceInput{FeeIDs: []string{farFee.ID.Hex(), SubscriptionLineID, farFee.ID.Hex()}})
	if err != nil || len(inv.Lines) != 2 || inv.Subtotal != 1000005 {
		t.Fatalf("explicit selection: %+v %v", inv, err)
	}
	// Unknown / other tenant's ids are 404, malformed is 400.
	other := fx.addFee(t, fx.b, FeeInput{Kind: "other", Label: "B fee", Amount: 1, Interval: "monthly"})
	for name, id := range map[string]string{"unknown": primitive.NewObjectID().Hex(), "cross-tenant": other.ID.Hex()} {
		if _, err := fx.inv.Create(ctx, tid, InvoiceInput{FeeIDs: []string{id}}); domStatus(err) != http.StatusNotFound {
			t.Errorf("%s fee id: %v", name, err)
		}
	}
	if _, err := fx.inv.Create(ctx, tid, InvoiceInput{FeeIDs: []string{"zzz"}}); domStatus(err) != http.StatusBadRequest {
		t.Errorf("malformed id: %v", err)
	}
	// A tenant without a subscription cannot select the derived line.
	if _, err := fx.inv.Create(ctx, fx.b.Hex(), InvoiceInput{FeeIDs: []string{SubscriptionLineID}}); domStatus(err) != http.StatusNotFound {
		t.Errorf("subscription line without a subscription: %v", err)
	}
	// Inactive fee and a fee with nothing to invoice are refused.
	off := false
	if _, err := fx.svc.Update(ctx, tid, deployFee.ID.Hex(), FeeInput{Kind: "other", Label: "Hosting", Amount: 1, Interval: "monthly", Active: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.inv.Create(ctx, tid, InvoiceInput{FeeIDs: []string{deployFee.ID.Hex()}}); domStatus(err) != http.StatusUnprocessableEntity {
		t.Errorf("inactive fee: %v", err)
	}
	noExp := fx.addDomain(t, fx.a, "noexp.example", nil)
	nf := fx.addFee(t, fx.a, FeeInput{Kind: "domain", Label: "n", Amount: 1, Interval: "yearly", DomainID: noExp.Hex()})
	if _, err := fx.inv.Create(ctx, tid, InvoiceInput{FeeIDs: []string{nf.ID.Hex()}}); domStatus(err) != http.StatusUnprocessableEntity {
		t.Errorf("fee without a due date: %v", err)
	}
	// all_due skips the inactive fee.
	fx2 := newInvFixture()
	fx2.seed(t)
	if got := fx2.createAllDue(t, nil); len(got.Lines) != 3 {
		t.Errorf("all_due lines = %d", len(got.Lines))
	}
}

func TestDoubleInvoiceIsRejected(t *testing.T) {
	fx := newInvFixture()
	domainFee, _, _ := fx.seed(t)
	ctx := context.Background()
	tid := fx.a.Hex()

	first := fx.createAllDue(t, nil)

	// Same period again: all due, or the single fee, or just the subscription.
	for name, in := range map[string]InvoiceInput{
		"all due again":      {AllDue: true},
		"the domain fee":     {FeeIDs: []string{domainFee.ID.Hex()}},
		"the subscription":   {FeeIDs: []string{SubscriptionLineID}},
		"a mix with new one": {FeeIDs: []string{SubscriptionLineID}, AllDue: true},
	} {
		_, err := fx.inv.Create(ctx, tid, in)
		if domStatus(err) != http.StatusConflict {
			t.Errorf("%s: status %d, err %v", name, domStatus(err), err)
		}
	}
	if len(fx.st.docs) != 1 {
		t.Fatalf("a rejected invoice was stored (%d invoices)", len(fx.st.docs))
	}
	// A draft or sent invoice blocks; a void one frees the period.
	if _, err := fx.inv.Void(ctx, tid, first.ID.Hex()); err != nil {
		t.Fatalf("void: %v", err)
	}
	again, err := fx.inv.Create(ctx, tid, InvoiceInput{AllDue: true})
	if err != nil || len(again.Lines) != 3 {
		t.Fatalf("after voiding the period is free again: %v", err)
	}

	// A paid invoice keeps blocking, and a new domain expiry is a new period.
	if _, err := fx.inv.Send(ctx, tid, again.ID.Hex(), SendInput{}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.inv.MarkPaid(ctx, tid, again.ID.Hex(), PaidInput{}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.inv.Create(ctx, tid, InvoiceInput{FeeIDs: []string{domainFee.ID.Hex()}}); domStatus(err) != http.StatusConflict {
		t.Errorf("paid period must stay blocked: %v", err)
	}
	fx.domains.docs[0].ExpiresAt = days(375)
	if _, err := fx.inv.Create(ctx, tid, InvoiceInput{FeeIDs: []string{domainFee.ID.Hex()}}); err != nil {
		t.Errorf("a renewed domain (new expiry) is a new period: %v", err)
	}
}

func TestInvoiceNumbersAreSequentialPerMonthAndUnique(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	ctx := context.Background()
	tid := fx.a.Hex()
	// Distinct explicit periods so none is a double invoice: one invoice per fee
	// (3 lines available: sub, domain, deploy) with distinct selections.
	var numbers []string
	for _, ids := range [][]string{{SubscriptionLineID}, {fx.fees.docs[0].ID.Hex()}, {fx.fees.docs[1].ID.Hex()}} {
		inv, err := fx.inv.Create(ctx, tid, InvoiceInput{FeeIDs: ids})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		numbers = append(numbers, inv.Number)
	}
	if strings.Join(numbers, ",") != "INV-2026-10-001,INV-2026-10-002,INV-2026-10-003" {
		t.Fatalf("numbers = %v", numbers)
	}

	// A new month restarts the sequence.
	fx.inv.now = func() time.Time { return time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC) }
	fx.svc.now = fx.inv.now
	fx.subs[fx.a].CurrentPeriodEnd = date(2026, 11, 20)
	inv, err := fx.inv.Create(ctx, tid, InvoiceInput{FeeIDs: []string{SubscriptionLineID}})
	if err != nil || inv.Number != "INV-2026-11-001" {
		t.Fatalf("new month: %v %v", inv, err)
	}

	// Numbers are unique across tenants too (one counter per month).
	fx.addFee(t, fx.b, FeeInput{Kind: "other", Label: "B", Amount: 1, Interval: "monthly"})
	bi, err := fx.inv.Create(ctx, fx.b.Hex(), InvoiceInput{AllDue: true})
	if err != nil || bi.Number != "INV-2026-11-002" {
		t.Fatalf("tenant B number: %v %v", bi, err)
	}

	// A new year restarts it too, and the month is the UTC month at issue time
	// (23:30 on 31 December in UTC-5 is already January in UTC).
	fx.inv.now = func() time.Time { return time.Date(2026, 12, 31, 23, 30, 0, 0, time.FixedZone("UTC-5", -5*3600)) }
	fx.svc.now = fx.inv.now
	fx.subs[fx.a].CurrentPeriodEnd = date(2027, 1, 20)
	ny, err := fx.inv.Create(ctx, tid, InvoiceInput{FeeIDs: []string{SubscriptionLineID}})
	if err != nil || ny.Number != "INV-2027-01-001" {
		t.Fatalf("new year: %v %v", ny, err)
	}
}

func TestInvoiceNumberUniqueIndexIsABackstop(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	fx.st.forceSeq = 7
	ctx := context.Background()
	if _, err := fx.inv.Create(ctx, fx.a.Hex(), InvoiceInput{FeeIDs: []string{SubscriptionLineID}}); err != nil {
		t.Fatal(err)
	}
	_, err := fx.inv.Create(ctx, fx.a.Hex(), InvoiceInput{FeeIDs: []string{fx.fees.docs[0].ID.Hex()}})
	if domStatus(err) != http.StatusConflict {
		t.Fatalf("a duplicate number must be a 409, got %d (%v)", domStatus(err), err)
	}
	if len(fx.st.docs) != 1 {
		t.Fatal("the duplicate invoice was stored")
	}
}

func TestConcurrentCreatesGetDistinctNumbers(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	// Separate fees so no create is a double invoice.
	var ids []string
	for i := 0; i < 20; i++ {
		f := fx.addFee(t, fx.a, FeeInput{Kind: "other", Label: "x", Amount: 1, Interval: "monthly"})
		ids = append(ids, f.ID.Hex())
	}
	var wg sync.WaitGroup
	nums := make(chan string, len(ids))
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			inv, err := fx.inv.Create(context.Background(), fx.a.Hex(), InvoiceInput{FeeIDs: []string{id}})
			if err != nil {
				t.Errorf("create: %v", err)
				return
			}
			nums <- inv.Number
		}(id)
	}
	wg.Wait()
	close(nums)
	seen := map[string]bool{}
	for n := range nums {
		if seen[n] {
			t.Fatalf("duplicate number %s", n)
		}
		seen[n] = true
	}
	if len(seen) != len(ids) {
		t.Fatalf("got %d numbers for %d invoices", len(seen), len(ids))
	}
}

func TestSendFlowAndIdempotency(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	ctx := context.Background()
	tid := fx.a.Hex()
	inv := fx.createAllDue(t, intp(1000))

	sent, err := fx.inv.Send(ctx, tid, inv.ID.Hex(), SendInput{})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if sent.Status != models.InvoiceSent || sent.SentAt == nil || sent.SentTo != "owner@tower.example" {
		t.Fatalf("after send: %+v", sent)
	}
	if len(fx.mail.sent) != 1 {
		t.Fatalf("mails = %d", len(fx.mail.sent))
	}
	m := fx.mail.sent[0]
	if m.to != "owner@tower.example" || m.tmpl != mailer.TemplateInvoice {
		t.Fatalf("mail = %+v", m)
	}
	if m.data["tenant"] != "Tower LLC" || m.data["number"] != inv.Number || m.data["total"] != "1,254,000" ||
		m.data["tax_rate"] != "10%" || m.data["line_count"] != "3" || m.data["line_1_amount"] != "1,000,000" {
		t.Fatalf("mail data = %v", m.data)
	}
	// The data must render with the real template.
	if _, _, _, err := mailer.Render(mailer.TemplateInvoice, m.data); err != nil {
		t.Fatalf("the data the service builds does not satisfy the template: %v", err)
	}

	// Second send without resend: 409, nothing sent, status unchanged.
	if _, err := fx.inv.Send(ctx, tid, inv.ID.Hex(), SendInput{}); domStatus(err) != http.StatusConflict {
		t.Fatalf("second send: %v", err)
	}
	if len(fx.mail.sent) != 1 {
		t.Fatalf("a refused resend still sent mail (%d)", len(fx.mail.sent))
	}
	// With resend: sent again, still "sent".
	again, err := fx.inv.Send(ctx, tid, inv.ID.Hex(), SendInput{Resend: true})
	if err != nil || again.Status != models.InvoiceSent || len(fx.mail.sent) != 2 {
		t.Fatalf("resend: %+v %v (mails %d)", again, err, len(fx.mail.sent))
	}
}

func TestSendWithEmailOffIs503AndChangesNothing(t *testing.T) {
	for name, mutate := range map[string]func(*invFixture){
		"unconfigured mailer": func(fx *invFixture) { fx.mail.avail = false },
		"nil mailer":          func(fx *invFixture) { fx.inv.mail = nil },
	} {
		fx := newInvFixture()
		fx.seed(t)
		inv := fx.createAllDue(t, nil)
		mutate(fx)

		_, err := fx.inv.Send(context.Background(), fx.a.Hex(), inv.ID.Hex(), SendInput{})
		var ae *apierr.APIError
		if !errors.As(err, &ae) || ae.HTTPStatus != http.StatusServiceUnavailable || ae.Code != apierr.CodeFeatureUnavailable {
			t.Fatalf("%s: err = %v", name, err)
		}
		got, _ := fx.inv.Get(context.Background(), fx.a.Hex(), inv.ID.Hex())
		if got.Status != models.InvoiceDraft || got.SentAt != nil || got.SentTo != "" {
			t.Fatalf("%s: invoice changed: %+v", name, got)
		}
		if len(fx.mail.sent) != 0 {
			t.Fatalf("%s: mail was sent", name)
		}
	}
}

func TestSendFailureLeavesStatusUnchanged(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	inv := fx.createAllDue(t, nil)
	fx.mail.err = errors.New("brevo answered 401 for owner@tower.example")

	_, err := fx.inv.Send(context.Background(), fx.a.Hex(), inv.ID.Hex(), SendInput{})
	var ae *apierr.APIError
	if !errors.As(err, &ae) || ae.HTTPStatus != http.StatusBadGateway {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(ae.Message, "owner@tower.example") || strings.Contains(ae.Message, "brevo") {
		t.Fatalf("client message leaks the cause: %q", ae.Message)
	}
	if got, _ := fx.inv.Get(context.Background(), fx.a.Hex(), inv.ID.Hex()); got.Status != models.InvoiceDraft || got.SentAt != nil {
		t.Fatalf("invoice changed: %+v", got)
	}
}

func TestSendNeedsATenantContactEmail(t *testing.T) {
	for _, email := range []string{"", "  ", "not-an-email", "Name <a@b.example>", "a@b"} {
		fx := newInvFixture()
		fx.seed(t)
		fx.tenantsM[fx.a].ContactEmail = email
		inv := fx.createAllDue(t, nil)
		if _, err := fx.inv.Send(context.Background(), fx.a.Hex(), inv.ID.Hex(), SendInput{}); domStatus(err) != http.StatusUnprocessableEntity {
			t.Errorf("contact %q: %v", email, err)
		}
		if len(fx.mail.sent) != 0 {
			t.Errorf("contact %q: mail sent", email)
		}
	}
}

func TestTransitions(t *testing.T) {
	ctx := context.Background()
	type action func(fx *invFixture, id string) error
	send := func(fx *invFixture, id string) error {
		_, err := fx.inv.Send(ctx, fx.a.Hex(), id, SendInput{Resend: true})
		return err
	}
	paid := func(fx *invFixture, id string) error {
		_, err := fx.inv.MarkPaid(ctx, fx.a.Hex(), id, PaidInput{})
		return err
	}
	void := func(fx *invFixture, id string) error { _, err := fx.inv.Void(ctx, fx.a.Hex(), id); return err }

	// from-state setups
	toSent := func(fx *invFixture, id string) { _ = send(fx, id) }
	toPaid := func(fx *invFixture, id string) { _ = send(fx, id); _ = paid(fx, id) }
	toVoid := func(fx *invFixture, id string) { _ = void(fx, id) }

	cases := []struct {
		from  string
		setup func(*invFixture, string)
		op    string
		do    action
		want  int // 0 = allowed
		after string
	}{
		{"draft", func(*invFixture, string) {}, "send", send, 0, models.InvoiceSent},
		{"draft", func(*invFixture, string) {}, "paid", paid, http.StatusConflict, models.InvoiceDraft},
		{"draft", func(*invFixture, string) {}, "void", void, 0, models.InvoiceVoid},
		{"sent", toSent, "send(resend)", send, 0, models.InvoiceSent},
		{"sent", toSent, "paid", paid, 0, models.InvoicePaid},
		{"sent", toSent, "void", void, 0, models.InvoiceVoid},
		{"paid", toPaid, "send", send, http.StatusConflict, models.InvoicePaid},
		{"paid", toPaid, "paid", paid, http.StatusConflict, models.InvoicePaid},
		{"paid", toPaid, "void", void, http.StatusConflict, models.InvoicePaid},
		{"void", toVoid, "send", send, http.StatusConflict, models.InvoiceVoid},
		{"void", toVoid, "paid", paid, http.StatusConflict, models.InvoiceVoid},
		{"void", toVoid, "void", void, http.StatusConflict, models.InvoiceVoid},
	}
	for _, c := range cases {
		fx := newInvFixture()
		fx.seed(t)
		inv := fx.createAllDue(t, nil)
		c.setup(fx, inv.ID.Hex())
		mailsBefore := len(fx.mail.sent)
		err := c.do(fx, inv.ID.Hex())
		if domStatus(err) != c.want {
			t.Errorf("%s -> %s: status %d, err %v", c.from, c.op, domStatus(err), err)
		}
		got, _ := fx.inv.Get(ctx, fx.a.Hex(), inv.ID.Hex())
		if got.Status != c.after {
			t.Errorf("%s -> %s: ended %q, want %q", c.from, c.op, got.Status, c.after)
		}
		if c.want != 0 && len(fx.mail.sent) != mailsBefore {
			t.Errorf("%s -> %s: a refused transition sent mail", c.from, c.op)
		}
	}
}

func TestMarkPaidSetsPaidAtAndAdvancesDeploymentFees(t *testing.T) {
	fx := newInvFixture()
	domainFee, deployFee, _ := fx.seed(t)
	other := fx.addFee(t, fx.a, FeeInput{Kind: "other", Label: "Setup", Amount: 10, Interval: "one_time"})
	ctx := context.Background()
	tid := fx.a.Hex()

	inv := fx.createAllDue(t, nil)
	if _, err := fx.inv.Send(ctx, tid, inv.ID.Hex(), SendInput{}); err != nil {
		t.Fatal(err)
	}
	paid, err := fx.inv.MarkPaid(ctx, tid, inv.ID.Hex(), PaidInput{})
	if err != nil || paid.Status != models.InvoicePaid || paid.PaidAt == nil || !paid.PaidAt.Equal(testNow) {
		t.Fatalf("paid: %+v %v", paid, err)
	}

	byID := map[primitive.ObjectID]*models.TenantFee{}
	for _, f := range fx.fees.docs {
		byID[f.ID] = f
	}
	// Deployment fee: invoiced for the period that fell due on creation day.
	if lit := byID[deployFee.ID].LastInvoicedThrough; lit == nil || !lit.Equal(date(2026, 10, 16)) {
		t.Errorf("deployment last_invoiced_through = %v", lit)
	}
	if lit := byID[other.ID].LastInvoicedThrough; lit == nil || !lit.Equal(date(2026, 10, 16)) {
		t.Errorf("one-time last_invoiced_through = %v", lit)
	}
	// Domain lines advance nothing: the operator edits the domain's expiry.
	if byID[domainFee.ID].LastInvoicedThrough != nil {
		t.Errorf("a domain fee must not get last_invoiced_through: %v", byID[domainFee.ID].LastInvoicedThrough)
	}
	// Next due moves one interval on; the settled one-time fee is gone from due.
	c, _ := fx.svc.Costing(ctx, tid)
	for _, f := range c.Fees {
		switch f.ID {
		case deployFee.ID:
			if f.NextDueAt == nil || !f.NextDueAt.Equal(date(2026, 11, 16)) || f.Due {
				// 16 Nov is 31 days out: just outside the 30-day window.
				t.Errorf("deployment next due %v due=%v", f.NextDueAt, f.Due)
			}
		case other.ID:
			if f.NextDueAt != nil || f.Due {
				t.Errorf("settled one-time fee still due: %+v", f)
			}
		}
	}
}

func TestVoidDoesNotAdvanceFees(t *testing.T) {
	fx := newInvFixture()
	_, deployFee, _ := fx.seed(t)
	ctx := context.Background()
	inv := fx.createAllDue(t, nil)
	if _, err := fx.inv.Send(ctx, fx.a.Hex(), inv.ID.Hex(), SendInput{}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.inv.Void(ctx, fx.a.Hex(), inv.ID.Hex()); err != nil {
		t.Fatal(err)
	}
	for _, f := range fx.fees.docs {
		if f.ID == deployFee.ID && f.LastInvoicedThrough != nil {
			t.Fatal("voiding advanced a fee")
		}
	}
}

func TestInvoiceTenantScoping(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	ctx := context.Background()
	inv := fx.createAllDue(t, nil)
	id, b := inv.ID.Hex(), fx.b.Hex()

	if _, err := fx.inv.Get(ctx, b, id); domStatus(err) != http.StatusNotFound {
		t.Errorf("cross-tenant get: %v", err)
	}
	if _, err := fx.inv.Send(ctx, b, id, SendInput{}); domStatus(err) != http.StatusNotFound {
		t.Errorf("cross-tenant send: %v", err)
	}
	if _, err := fx.inv.MarkPaid(ctx, b, id, PaidInput{}); domStatus(err) != http.StatusNotFound {
		t.Errorf("cross-tenant paid: %v", err)
	}
	if _, err := fx.inv.Void(ctx, b, id); domStatus(err) != http.StatusNotFound {
		t.Errorf("cross-tenant void: %v", err)
	}
	if l, err := fx.inv.List(ctx, b); err != nil || len(l) != 0 {
		t.Errorf("tenant B lists %d of A's invoices (%v)", len(l), err)
	}
	if l, _ := fx.inv.List(ctx, fx.a.Hex()); len(l) != 1 {
		t.Errorf("tenant A lists %d invoices", len(l))
	}
	if got, _ := fx.inv.Get(ctx, fx.a.Hex(), id); got.Status != models.InvoiceDraft || len(fx.mail.sent) != 0 {
		t.Fatal("a cross-tenant call changed the invoice or sent mail")
	}
	// Bad ids and unknown tenant.
	if _, err := fx.inv.Get(ctx, fx.a.Hex(), "nope"); domStatus(err) != http.StatusBadRequest {
		t.Errorf("bad invoice id: %v", err)
	}
	if _, err := fx.inv.Create(ctx, primitive.NewObjectID().Hex(), InvoiceInput{AllDue: true}); domStatus(err) != http.StatusNotFound {
		t.Errorf("unknown tenant: %v", err)
	}
	if _, err := fx.inv.Get(ctx, fx.a.Hex(), primitive.NewObjectID().Hex()); domStatus(err) != http.StatusNotFound {
		t.Errorf("unknown invoice: %v", err)
	}
}

// A client that posts amounts, a number, a status or a recipient has them
// ignored: InvoiceInput has no such fields, so the server's values win.
func TestInvoiceBodyCannotSetMoneyNumberOrStatus(t *testing.T) {
	var in InvoiceInput
	body := `{"all_due":true,"subtotal":1,"tax_amount":1,"total":1,"number":"INV-1999-0001","status":"paid","sent_to":"evil@example.com","tenant_id":"x","lines":[{"label":"free","amount":0}]}`
	if err := json.Unmarshal([]byte(body), &in); err != nil {
		t.Fatal(err)
	}
	fx := newInvFixture()
	fx.seed(t)
	inv, err := fx.inv.Create(context.Background(), fx.a.Hex(), in)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Total == 1 || inv.Number != "INV-2026-10-001" || inv.Status != models.InvoiceDraft || inv.SentTo != "" || inv.TenantID != fx.a || len(inv.Lines) != 3 {
		t.Fatalf("client values leaked into the invoice: %+v", inv)
	}
}

// racyStore reports no existing line, as if a concurrent Create had not
// inserted yet when this one read: only the unique index can stop it.
type racyStore struct{ *fakeInvoiceStore }

func (racyStore) LineHolder(context.Context, primitive.ObjectID, string, *primitive.ObjectID, *primitive.ObjectID, string) (string, error) {
	return "", nil
}

func TestCreateRaceIsStoppedByPeriodKeys(t *testing.T) {
	fx := newInvFixture()
	domainFee, _, _ := fx.seed(t)
	ctx := context.Background()
	tid := fx.a.Hex()

	first, err := fx.inv.Create(ctx, tid, InvoiceInput{FeeIDs: []string{domainFee.ID.Hex()}})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.PeriodKeys) != 1 {
		t.Fatalf("period keys = %v", first.PeriodKeys)
	}

	racy := NewInvoiceService(racyStore{fx.st}, fx.svc, fx.fees, fx.domains, fx.tenantsM, fx.mail, fx.ps, "Inno Nomads")
	racy.now = func() time.Time { return testNow }
	_, err = racy.Create(ctx, tid, InvoiceInput{FeeIDs: []string{domainFee.ID.Hex()}})
	if domStatus(err) != http.StatusConflict || apiMessage(err) != "a line is already on an invoice for this period" {
		t.Fatalf("race: status %d message %q (%v)", domStatus(err), apiMessage(err), err)
	}
	if len(fx.st.docs) != 1 {
		t.Fatalf("double invoice stored: %d", len(fx.st.docs))
	}

	// Voiding frees the keys.
	if _, err := fx.inv.Void(ctx, tid, first.ID.Hex()); err != nil {
		t.Fatal(err)
	}
	if _, err := racy.Create(ctx, tid, InvoiceInput{FeeIDs: []string{domainFee.ID.Hex()}}); err != nil {
		t.Fatalf("after void: %v", err)
	}
	// A plain number clash keeps its own message.
	fx.st.forceSeq = 1
	if _, err := racy.Create(ctx, tid, InvoiceInput{FeeIDs: []string{SubscriptionLineID}}); apiMessage(err) != "invoice number is already taken" {
		t.Fatalf("number clash message = %q (%v)", apiMessage(err), err)
	}
}

func TestVoidWhileSendingCannotMailAVoidedInvoice(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	ctx := context.Background()
	tid := fx.a.Hex()
	inv := fx.createAllDue(t, nil)

	var voidErr, paidErr, sendErr error
	var seen string
	fx.mail.during = func() {
		_, voidErr = fx.inv.Void(ctx, tid, inv.ID.Hex())
		_, paidErr = fx.inv.MarkPaid(ctx, tid, inv.ID.Hex(), PaidInput{})
		_, sendErr = fx.inv.Send(ctx, tid, inv.ID.Hex(), SendInput{Resend: true})
		cur, _ := fx.inv.Get(ctx, tid, inv.ID.Hex())
		b, _ := json.Marshal(cur)
		seen = string(b)
	}
	out, err := fx.inv.Send(ctx, tid, inv.ID.Hex(), SendInput{})
	if err != nil || out.Status != models.InvoiceSent {
		t.Fatalf("send: %+v %v", out, err)
	}
	for name, e := range map[string]error{"void": voidErr, "mark paid": paidErr, "second send": sendErr} {
		if domStatus(e) != http.StatusConflict {
			t.Errorf("%s during a send: status %d (%v)", name, domStatus(e), e)
		}
	}
	if len(fx.mail.sent) != 1 {
		t.Fatalf("%d mails sent, want 1", len(fx.mail.sent))
	}
	if fx.st.docs[0].Status != models.InvoiceSent || fx.st.docs[0].SentTo != "owner@tower.example" {
		t.Fatalf("final invoice: %+v", fx.st.docs[0])
	}
	// Clients never see the internal status.
	if !strings.Contains(seen, `"status":"draft"`) || strings.Contains(seen, "sending") {
		t.Fatalf("status shown while sending: %s", seen)
	}
}

func TestSendFailureRollsBackToThePreviousStatus(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	ctx := context.Background()
	tid := fx.a.Hex()
	inv := fx.createAllDue(t, nil)

	fx.mail.err = errors.New("provider refused")
	if _, err := fx.inv.Send(ctx, tid, inv.ID.Hex(), SendInput{}); err == nil {
		t.Fatal("expected an error")
	}
	if fx.st.docs[0].Status != models.InvoiceDraft {
		t.Fatalf("status after failed first send = %q", fx.st.docs[0].Status)
	}

	fx.mail.err = nil
	if _, err := fx.inv.Send(ctx, tid, inv.ID.Hex(), SendInput{}); err != nil {
		t.Fatal(err)
	}
	fx.mail.err = errors.New("provider refused")
	if _, err := fx.inv.Send(ctx, tid, inv.ID.Hex(), SendInput{Resend: true}); err == nil {
		t.Fatal("expected an error")
	}
	if fx.st.docs[0].Status != models.InvoiceSent {
		t.Fatalf("status after failed resend = %q", fx.st.docs[0].Status)
	}
	// And the invoice can be voided or paid as before.
	if _, err := fx.inv.MarkPaid(ctx, tid, inv.ID.Hex(), PaidInput{}); err != nil {
		t.Fatalf("pay after failed resend: %v", err)
	}
}

// failAdvance fails the first n fee advances.
type failAdvance struct {
	*fakeFeeStore
	failures int
}

func (f *failAdvance) AdvanceInvoicedThrough(ctx context.Context, tid, id primitive.ObjectID, through time.Time) error {
	if f.failures > 0 {
		f.failures--
		return errors.New("db down")
	}
	return f.fakeFeeStore.AdvanceInvoicedThrough(ctx, tid, id, through)
}

func TestMarkPaidSetsStatusBeforeAdvancingFees(t *testing.T) {
	fx := newInvFixture()
	_, deployFee, _ := fx.seed(t)
	ctx := context.Background()
	tid := fx.a.Hex()
	inv := fx.createAllDue(t, nil)
	if _, err := fx.inv.Send(ctx, tid, inv.ID.Hex(), SendInput{}); err != nil {
		t.Fatal(err)
	}
	fa := &failAdvance{fakeFeeStore: fx.fees, failures: 1}
	fx.inv.fees = fa

	if _, err := fx.inv.MarkPaid(ctx, tid, inv.ID.Hex(), PaidInput{}); domStatus(err) != http.StatusInternalServerError {
		t.Fatalf("advance failure status = %d (%v)", domStatus(err), err)
	}
	// The status change is not lost to the failed advance.
	if fx.st.docs[0].Status != models.InvoicePaid || fx.st.docs[0].PaidAt == nil {
		t.Fatalf("status = %q", fx.st.docs[0].Status)
	}
	lit := func() *time.Time {
		for _, f := range fx.fees.docs {
			if f.ID == deployFee.ID {
				return f.LastInvoicedThrough
			}
		}
		return nil
	}
	if lit() != nil {
		t.Fatal("fee advanced although the advance failed")
	}
	// A retry is a 409 (already paid) but replays the advance.
	if _, err := fx.inv.MarkPaid(ctx, tid, inv.ID.Hex(), PaidInput{}); domStatus(err) != http.StatusConflict {
		t.Fatalf("retry status = %d (%v)", domStatus(err), err)
	}
	if got := lit(); got == nil || !got.Equal(date(2026, 10, 16)) {
		t.Fatalf("fee not advanced by the retry: %v", got)
	}
	// Replaying never moves it back.
	if _, err := fx.inv.MarkPaid(ctx, tid, inv.ID.Hex(), PaidInput{}); domStatus(err) != http.StatusConflict {
		t.Fatal("expected 409")
	}
	if got := lit(); !got.Equal(date(2026, 10, 16)) {
		t.Fatalf("fee moved: %v", got)
	}
}

// raceStore lets another request change the invoice between Edit's read and
// its conditional write.
type raceStore struct {
	*fakeInvoiceStore
	before func()
}

func (r *raceStore) Transition(ctx context.Context, tid, id primitive.ObjectID, from []string, set bson.M) (*models.Invoice, error) {
	if r.before != nil {
		r.before()
	}
	return r.fakeInvoiceStore.Transition(ctx, tid, id, from, set)
}

func editDue(s string) json.RawMessage { return json.RawMessage(`"` + s + `"`) }

func TestEditDraftDueDateAndTax(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	ctx := context.Background()
	inv := fx.createAllDue(t, nil)
	id := inv.ID.Hex()
	keys := append([]string(nil), inv.PeriodKeys...)

	due := testNow.AddDate(0, 0, 20).UTC()
	out, err := fx.inv.Edit(ctx, fx.a.Hex(), id, InvoiceEditInput{DueAt: editDue(due.Format(time.RFC3339))})
	if err != nil {
		t.Fatalf("edit due: %v", err)
	}
	if out.DueAt == nil || !out.DueAt.Equal(due) || out.Total != inv.Total || out.TaxRateBP != 0 {
		t.Fatalf("due-only edit: due %v total %d bp %d", out.DueAt, out.Total, out.TaxRateBP)
	}
	if !out.UpdatedAt.After(inv.UpdatedAt) {
		t.Error("updated_at was not advanced")
	}

	out, err = fx.inv.Edit(ctx, fx.a.Hex(), id, InvoiceEditInput{TaxRateBP: intp(1000)})
	if err != nil {
		t.Fatalf("edit tax: %v", err)
	}
	wantTax := TaxAmount(inv.Subtotal, 1000)
	if out.TaxRateBP != 1000 || out.TaxAmount != wantTax || out.Total != inv.Subtotal+wantTax || out.Subtotal != inv.Subtotal {
		t.Errorf("tax edit: bp %d tax %d total %d (subtotal %d)", out.TaxRateBP, out.TaxAmount, out.Total, out.Subtotal)
	}
	if out.DueAt == nil || !out.DueAt.Equal(due) {
		t.Error("a tax-only edit changed the due date")
	}

	out, err = fx.inv.Edit(ctx, fx.a.Hex(), id, InvoiceEditInput{TaxRateBP: intp(0)})
	if err != nil || out.TaxAmount != 0 || out.Total != inv.Subtotal {
		t.Errorf("tax back to 0: %v tax %d total %d", err, out.TaxAmount, out.Total)
	}

	got, _ := fx.inv.Get(ctx, fx.a.Hex(), id)
	if len(got.Lines) != len(inv.Lines) || strings.Join(got.PeriodKeys, "|") != strings.Join(keys, "|") ||
		got.Number != inv.Number || !got.IssuedAt.Equal(inv.IssuedAt) || got.Currency != inv.Currency || got.Status != models.InvoiceDraft {
		t.Error("edit touched lines, period keys, number, issue date, currency or status")
	}
	for i := range got.Lines {
		if got.Lines[i] != inv.Lines[i] {
			t.Errorf("line %d changed", i)
		}
	}
}

func TestEditValidation(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	ctx := context.Background()
	inv := fx.createAllDue(t, intp(500))
	id := inv.ID.Hex()
	before := inv.Total

	cases := []struct {
		name string
		in   InvoiceEditInput
		want int
	}{
		{"empty", InvoiceEditInput{}, http.StatusUnprocessableEntity},
		{"null due", InvoiceEditInput{DueAt: json.RawMessage(`null`)}, http.StatusUnprocessableEntity},
		{"garbage due", InvoiceEditInput{DueAt: json.RawMessage(`"tomorrow"`)}, http.StatusUnprocessableEntity},
		{"due before issue", InvoiceEditInput{DueAt: editDue(testNow.AddDate(0, 0, -1).Format(time.RFC3339))}, http.StatusUnprocessableEntity},
		{"due year 1999", InvoiceEditInput{DueAt: editDue("1999-12-31T00:00:00Z")}, http.StatusUnprocessableEntity},
		{"due year 2201", InvoiceEditInput{DueAt: editDue("2201-01-01T00:00:00Z")}, http.StatusUnprocessableEntity},
		{"tax negative", InvoiceEditInput{TaxRateBP: intp(-1)}, http.StatusUnprocessableEntity},
		{"tax too high", InvoiceEditInput{TaxRateBP: intp(10001)}, http.StatusUnprocessableEntity},
		{"valid due but bad tax", InvoiceEditInput{DueAt: editDue(testNow.AddDate(0, 0, 5).Format(time.RFC3339)), TaxRateBP: intp(20000)}, http.StatusUnprocessableEntity},
	}
	for _, c := range cases {
		if _, err := fx.inv.Edit(ctx, fx.a.Hex(), id, c.in); domStatus(err) != c.want {
			t.Errorf("%s: status %d, err %v", c.name, domStatus(err), err)
		}
	}
	got, _ := fx.inv.Get(ctx, fx.a.Hex(), id)
	if got.Total != before || got.TaxRateBP != 500 || got.DueAt != nil {
		t.Error("a rejected edit changed the invoice")
	}
	if _, err := fx.inv.Edit(ctx, fx.a.Hex(), "nope", InvoiceEditInput{TaxRateBP: intp(1)}); domStatus(err) != http.StatusBadRequest {
		t.Errorf("bad invoice id: %v", err)
	}
	// Edge values are accepted: the issue day itself and the 0 and 10000 rates.
	if _, err := fx.inv.Edit(ctx, fx.a.Hex(), id, InvoiceEditInput{DueAt: editDue(testNow.Format(time.RFC3339)), TaxRateBP: intp(10000)}); err != nil {
		t.Errorf("edge edit: %v", err)
	}
}

func TestEditRefusesNonDraft(t *testing.T) {
	ctx := context.Background()
	in := InvoiceEditInput{TaxRateBP: intp(1000)}
	setups := map[string]func(fx *invFixture, id string){
		"sent": func(fx *invFixture, id string) { _, _ = fx.inv.Send(ctx, fx.a.Hex(), id, SendInput{}) },
		"paid": func(fx *invFixture, id string) {
			_, _ = fx.inv.Send(ctx, fx.a.Hex(), id, SendInput{})
			_, _ = fx.inv.MarkPaid(ctx, fx.a.Hex(), id, PaidInput{})
		},
		"void": func(fx *invFixture, id string) { _, _ = fx.inv.Void(ctx, fx.a.Hex(), id) },
		"sending": func(fx *invFixture, id string) {
			for _, d := range fx.st.docs {
				d.Status, d.SendPrev = models.InvoiceSending, models.InvoiceDraft
			}
		},
	}
	for name, setup := range setups {
		fx := newInvFixture()
		fx.seed(t)
		inv := fx.createAllDue(t, nil)
		setup(fx, inv.ID.Hex())
		_, err := fx.inv.Edit(ctx, fx.a.Hex(), inv.ID.Hex(), in)
		if domStatus(err) != http.StatusConflict || !strings.Contains(err.Error(), "only a draft invoice can be edited") {
			t.Errorf("%s: want 409, got %v", name, err)
		}
		got, _ := fx.st.Find(ctx, fx.a, inv.ID)
		if got.TaxRateBP != 0 || got.Total != inv.Total {
			t.Errorf("%s: a refused edit changed the invoice", name)
		}
	}
}

func TestEditIsTenantScoped(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	inv := fx.createAllDue(t, nil)
	_, err := fx.inv.Edit(context.Background(), fx.b.Hex(), inv.ID.Hex(), InvoiceEditInput{TaxRateBP: intp(1000)})
	if domStatus(err) != http.StatusNotFound {
		t.Errorf("cross-tenant edit: %v", err)
	}
	if got, _ := fx.inv.Get(context.Background(), fx.a.Hex(), inv.ID.Hex()); got.TaxRateBP != 0 {
		t.Error("cross-tenant edit changed the invoice")
	}
}

// A Send, Void or MarkPaid that lands between the read and the write makes the
// conditional update match nothing: 409, and the invoice is not edited.
func TestEditLosesRaceToStatusChange(t *testing.T) {
	ctx := context.Background()
	for _, status := range []string{models.InvoiceSending, models.InvoiceVoid, models.InvoiceSent} {
		fx := newInvFixture()
		fx.seed(t)
		inv := fx.createAllDue(t, nil)
		rs := &raceStore{fakeInvoiceStore: fx.st}
		rs.before = func() {
			rs.before = nil
			for _, d := range fx.st.docs {
				d.Status = status
			}
		}
		fx.inv.repo = rs
		_, err := fx.inv.Edit(ctx, fx.a.Hex(), inv.ID.Hex(), InvoiceEditInput{TaxRateBP: intp(1000)})
		if domStatus(err) != http.StatusConflict {
			t.Errorf("%s: want 409, got %v", status, err)
		}
		for _, d := range fx.st.docs {
			if d.TaxRateBP != 0 || d.Total != inv.Total {
				t.Errorf("%s: the edit was applied despite the race", status)
			}
		}
	}
}

// The mail preview reads the edited due date and tax.
func TestPreviewAfterEdit(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	ctx := context.Background()
	inv := fx.createAllDue(t, nil)
	due := testNow.AddDate(0, 0, 30).UTC()
	if _, err := fx.inv.Edit(ctx, fx.a.Hex(), inv.ID.Hex(), InvoiceEditInput{DueAt: editDue(due.Format(time.RFC3339)), TaxRateBP: intp(1000)}); err != nil {
		t.Fatal(err)
	}
	p, err := fx.inv.Preview(ctx, fx.a.Hex(), inv.ID.Hex())
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !strings.Contains(p.Text, due.Format(displayDateLayout)) || !strings.Contains(p.Text, "10%") {
		t.Errorf("preview does not show the edit:\n%s", p.Text)
	}
}