package service

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eandstravel/tenantcore/internal/models"
)

const dupLinePrefix = "a line is already on invoice "

// The 409 names the invoice holding the line, and only an invoice of the same
// tenant can be named.
func TestDoubleInvoiceNamesTheHoldingInvoice(t *testing.T) {
	fx := newInvFixture()
	domainFee, _, _ := fx.seed(t)
	ctx := context.Background()

	first := fx.createAllDue(t, nil)
	_, err := fx.inv.Create(ctx, fx.a.Hex(), InvoiceInput{FeeIDs: []string{domainFee.ID.Hex()}})
	want := "a line is already on invoice " + first.Number + " for this period"
	if domStatus(err) != http.StatusConflict || apiMessage(err) != want {
		t.Fatalf("status %d message %q, want %q", domStatus(err), apiMessage(err), want)
	}

	// Tenant B has the same kind of line and period but its own invoices: it is
	// neither blocked by A's invoice nor told A's number.
	fx.subscribe(fx.b, 500, models.SubscriptionActive, 5)
	bi, err := fx.inv.Create(ctx, fx.b.Hex(), InvoiceInput{FeeIDs: []string{SubscriptionLineID}})
	if err != nil {
		t.Fatalf("tenant B blocked by tenant A's invoice: %v", err)
	}
	_, err = fx.inv.Create(ctx, fx.b.Hex(), InvoiceInput{FeeIDs: []string{SubscriptionLineID}})
	if got := apiMessage(err); !strings.Contains(got, bi.Number) || strings.Contains(got, first.Number) {
		t.Fatalf("tenant B message %q must name B's own invoice %s only", got, bi.Number)
	}
}

// all_due must not fail as a whole because one due line is already invoiced:
// the invoiced lines are skipped, and only an explicit pick is a 409.
func TestAllDueSkipsLinesAlreadyInvoiced(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	ctx := context.Background()
	tid := fx.a.Hex()

	sub, err := fx.inv.Create(ctx, tid, InvoiceInput{FeeIDs: []string{SubscriptionLineID}})
	if err != nil {
		t.Fatal(err)
	}
	rest, err := fx.inv.Create(ctx, tid, InvoiceInput{AllDue: true})
	if err != nil {
		t.Fatalf("all_due with one line already invoiced: %v", err)
	}
	if len(rest.Lines) != 2 {
		t.Fatalf("lines = %+v, want the 2 not yet invoiced", rest.Lines)
	}
	for _, l := range rest.Lines {
		if l.Kind == models.FeeKindSubscription {
			t.Fatalf("the invoiced subscription line was billed again")
		}
	}
	// Nothing left: a 409 naming a holder.
	_, err = fx.inv.Create(ctx, tid, InvoiceInput{AllDue: true})
	if domStatus(err) != http.StatusConflict || !strings.HasPrefix(apiMessage(err), dupLinePrefix+"INV-") {
		t.Fatalf("all invoiced: status %d %q", domStatus(err), apiMessage(err))
	}
	// An explicit pick of an invoiced line is still a 409 naming its invoice.
	_, err = fx.inv.Create(ctx, tid, InvoiceInput{FeeIDs: []string{SubscriptionLineID}, AllDue: true})
	if want := dupLinePrefix + sub.Number + " for this period"; apiMessage(err) != want {
		t.Fatalf("explicit pick: %q, want %q", apiMessage(err), want)
	}
}

func invoicedOnMap(c *Costing) map[string]string {
	m := map[string]string{}
	if c.Subscription != nil {
		m["subscription"] = c.Subscription.InvoicedOn
	}
	for _, f := range c.Fees {
		m[f.Label] = f.InvoicedOn
	}
	for _, d := range c.DomainLines {
		m[d.Label] = d.InvoicedOn
	}
	return m
}

// Costing says which lines already sit on a live invoice, so the admin UI can
// un-preselect them; void frees them again.
func TestCostingReportsInvoicedOn(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	// A derived domain line (client charge, no fee row).
	dd := fx.addDomain(t, fx.a, "derived.example", days(20))
	fx.domains.docs[len(fx.domains.docs)-1].Payment.ClientCharge.Amount = 77
	_ = dd
	ctx := context.Background()
	tid := fx.a.Hex()

	c, err := fx.svc.Costing(ctx, tid)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range invoicedOnMap(c) {
		if v != "" {
			t.Fatalf("nothing invoiced yet, %s on %q", k, v)
		}
	}

	inv, err := fx.inv.Create(ctx, tid, InvoiceInput{AllDue: true})
	if err != nil {
		t.Fatal(err)
	}
	c, _ = fx.svc.Costing(ctx, tid)
	m := invoicedOnMap(c)
	for _, k := range []string{"subscription", "Domain tower.example", "Hosting", "Domain: derived.example"} {
		if m[k] != inv.Number {
			t.Errorf("%s invoiced_on = %q, want %q", k, m[k], inv.Number)
		}
	}
	if m["Domain far.example"] != "" {
		t.Errorf("a line on no invoice reports %q", m["Domain far.example"])
	}

	// Another tenant never sees this tenant's numbers.
	cb, err := fx.svc.Costing(ctx, fx.b.Hex())
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range invoicedOnMap(cb) {
		if v != "" {
			t.Errorf("tenant B %s invoiced_on %q", k, v)
		}
	}

	if _, err := fx.inv.Void(ctx, tid, inv.ID.Hex()); err != nil {
		t.Fatal(err)
	}
	c, _ = fx.svc.Costing(ctx, tid)
	for k, v := range invoicedOnMap(c) {
		if v != "" {
			t.Errorf("after void %s still on %q", k, v)
		}
	}
}

// A paid subscription invoice leaves the line due (the operator renews the
// plan), so it must read as invoiced until the period moves; the renewed
// period is a new key and invoices cleanly.
func TestPaidSubscriptionStaysInvoicedUntilRenewal(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	ctx := context.Background()
	tid := fx.a.Hex()

	inv, err := fx.inv.Create(ctx, tid, InvoiceInput{FeeIDs: []string{SubscriptionLineID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fx.inv.Send(ctx, tid, inv.ID.Hex(), SendInput{}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.inv.MarkPaid(ctx, tid, inv.ID.Hex(), PaidInput{}); err != nil {
		t.Fatal(err)
	}
	c, _ := fx.svc.Costing(ctx, tid)
	if c.Subscription.InvoicedOn != inv.Number {
		t.Fatalf("paid, not renewed: invoiced_on = %q", c.Subscription.InvoicedOn)
	}
	if _, err := fx.inv.Create(ctx, tid, InvoiceInput{FeeIDs: []string{SubscriptionLineID}}); domStatus(err) != http.StatusConflict {
		t.Fatalf("same subscription period: %v", err)
	}
	fx.subs[fx.a].CurrentPeriodEnd = *days(35)
	c, _ = fx.svc.Costing(ctx, tid)
	if c.Subscription.InvoicedOn != "" {
		t.Fatalf("renewed period still reads invoiced on %q", c.Subscription.InvoicedOn)
	}
	if _, err := fx.inv.Create(ctx, tid, InvoiceInput{FeeIDs: []string{SubscriptionLineID}}); err != nil {
		t.Fatalf("renewed period must invoice: %v", err)
	}
}

// After a paid deployment fee, its next period has another key and invoices.
func TestPaidFeeMovesToNextPeriodKey(t *testing.T) {
	fx := newInvFixture()
	_, deploy, _ := fx.seed(t)
	ctx := context.Background()
	tid := fx.a.Hex()

	inv, err := fx.inv.Create(ctx, tid, InvoiceInput{FeeIDs: []string{deploy.ID.Hex()}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fx.inv.Send(ctx, tid, inv.ID.Hex(), SendInput{}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.inv.MarkPaid(ctx, tid, inv.ID.Hex(), PaidInput{}); err != nil {
		t.Fatal(err)
	}
	fx.svc.now = func() time.Time { return testNow.AddDate(0, 1, 0) }
	fx.inv.now = fx.svc.now
	next, err := fx.inv.Create(ctx, tid, InvoiceInput{FeeIDs: []string{deploy.ID.Hex()}})
	if err != nil {
		t.Fatalf("next period of a paid fee: %v", err)
	}
	if next.PeriodKeys[0] == inv.PeriodKeys[0] {
		t.Fatalf("same key for two periods: %s", next.PeriodKeys[0])
	}
}

// Edit never touches the period keys.
func TestEditKeepsPeriodKeys(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	ctx := context.Background()
	tid := fx.a.Hex()
	inv := fx.createAllDue(t, nil)
	if _, err := fx.inv.Edit(ctx, tid, inv.ID.Hex(), InvoiceEditInput{TaxRateBP: intp(500)}); err != nil {
		t.Fatal(err)
	}
	if got := fx.st.docs[0].PeriodKeys; len(got) != len(inv.PeriodKeys) || len(got) != 3 {
		t.Fatalf("keys after edit = %v, want %v", got, inv.PeriodKeys)
	}
}

func TestDeleteDraftInvoice(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	ctx := context.Background()
	tid := fx.a.Hex()

	draft := fx.createAllDue(t, nil)
	if err := fx.inv.Delete(ctx, tid, "zzz"); domStatus(err) != http.StatusBadRequest {
		t.Errorf("malformed id: %v", err)
	}
	// Cross-tenant is a 404 and deletes nothing.
	if err := fx.inv.Delete(ctx, fx.b.Hex(), draft.ID.Hex()); domStatus(err) != http.StatusNotFound {
		t.Errorf("cross-tenant: %v", err)
	}
	if len(fx.st.docs) != 1 {
		t.Fatal("cross-tenant delete removed the invoice")
	}
	if err := fx.inv.Delete(ctx, tid, draft.ID.Hex()); err != nil {
		t.Fatalf("delete draft: %v", err)
	}
	if len(fx.st.docs) != 0 {
		t.Fatal("draft still stored")
	}
	if err := fx.inv.Delete(ctx, tid, draft.ID.Hex()); domStatus(err) != http.StatusNotFound {
		t.Errorf("second delete: %v", err)
	}
	// The periods are free again; the number is not reused.
	again, err := fx.inv.Create(ctx, tid, InvoiceInput{AllDue: true})
	if err != nil || len(again.Lines) != 3 {
		t.Fatalf("recreate after delete: %v", err)
	}
	if again.Number == draft.Number {
		t.Fatalf("number %s reused", again.Number)
	}
}

func TestDeleteRefusesEverythingButDraft(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	ctx := context.Background()
	tid := fx.a.Hex()

	inv := fx.createAllDue(t, nil)
	// Claimed by a send in flight.
	var sendingErr error
	fx.mail.during = func() { sendingErr = fx.inv.Delete(ctx, tid, inv.ID.Hex()) }
	if _, err := fx.inv.Send(ctx, tid, inv.ID.Hex(), SendInput{}); err != nil {
		t.Fatal(err)
	}
	fx.mail.during = nil
	if domStatus(sendingErr) != http.StatusConflict {
		t.Errorf("sending: %v", sendingErr)
	}
	if err := fx.inv.Delete(ctx, tid, inv.ID.Hex()); domStatus(err) != http.StatusConflict {
		t.Errorf("sent: %v", err)
	}
	if _, err := fx.inv.MarkPaid(ctx, tid, inv.ID.Hex(), PaidInput{}); err != nil {
		t.Fatal(err)
	}
	if err := fx.inv.Delete(ctx, tid, inv.ID.Hex()); domStatus(err) != http.StatusConflict {
		t.Errorf("paid: %v", err)
	}
	if len(fx.st.docs) != 1 {
		t.Fatal("a non-draft invoice was deleted")
	}

	fx2 := newInvFixture()
	fx2.seed(t)
	v := fx2.createAllDue(t, nil)
	if _, err := fx2.inv.Void(ctx, fx2.a.Hex(), v.ID.Hex()); err != nil {
		t.Fatal(err)
	}
	if err := fx2.inv.Delete(ctx, fx2.a.Hex(), v.ID.Hex()); domStatus(err) != http.StatusConflict {
		t.Errorf("void: %v", err)
	}
}
