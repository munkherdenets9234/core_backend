package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/eandstravel/tenantcore/internal/models"
	"go.mongodb.org/mongo-driver/bson"
)

func i64p(n int64) *int64 { return &n }

func lineByKey(t *testing.T, inv *models.Invoice, key string) models.InvoiceLine {
	t.Helper()
	for _, l := range inv.Lines {
		if models.InvoiceLineKey(l) == key {
			return l
		}
	}
	t.Fatalf("no line %q on %+v", key, inv.Lines)
	return models.InvoiceLine{}
}

func TestInvoiceTotalsWithDiscounts(t *testing.T) {
	lines := []models.InvoiceLine{{Amount: 100000, Discount: 10000}, {Amount: 40000}, {Amount: 5, Discount: 1}}
	sub, tax, total := InvoiceTotals(lines, 1000)
	// base 130,004; tax 13,000.4 -> 13,000
	if sub != 140005 || tax != 13000 || total != 143004 {
		t.Fatalf("totals = %d %d %d", sub, tax, total)
	}
	if _, d, _, _ := invoiceTotals(lines, 0); d != 10001 {
		t.Fatalf("discount total = %d", d)
	}
	// Lines without discount fields behave exactly as before.
	sub, tax, total = InvoiceTotals([]models.InvoiceLine{{Amount: 100000}, {Amount: 40000}, {Amount: 1}}, 1000)
	if sub != 140001 || tax != 14000 || total != 154001 {
		t.Fatalf("legacy totals = %d %d %d", sub, tax, total)
	}
}

func TestCreateWithAmountAndPercentDiscounts(t *testing.T) {
	fx := newInvFixture()
	domainFee, _, farFee := fx.seed(t)
	ctx := context.Background()

	inv, err := fx.inv.Create(ctx, fx.a.Hex(), InvoiceInput{
		FeeIDs:    []string{SubscriptionLineID, domainFee.ID.Hex(), farFee.ID.Hex()},
		TaxRateBP: intp(1000),
		Discounts: map[string]LineDiscountInput{
			SubscriptionLineID: {Amount: i64p(100000), Reason: "  Launch offer "},
			domainFee.ID.Hex(): {PercentBP: intp(1250)},
			farFee.ID.Hex():    {PercentBP: intp(1000)}, // 5 * 10% = 0.5 -> 1 (half up)
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sub := lineByKey(t, inv, SubscriptionLineID)
	if sub.Amount != 1000000 || sub.Discount != 100000 || sub.DiscountBP != 0 || sub.DiscountReason != "Launch offer" || sub.LineKey != SubscriptionLineID {
		t.Errorf("subscription line: %+v", sub)
	}
	dom := lineByKey(t, inv, domainFee.ID.Hex())
	if dom.Amount != 100000 || dom.Discount != 12500 || dom.DiscountBP != 1250 || dom.DiscountReason != "" {
		t.Errorf("domain line: %+v", dom)
	}
	far := lineByKey(t, inv, farFee.ID.Hex())
	if far.Amount != 5 || far.Discount != 1 || far.DiscountBP != 1000 {
		t.Errorf("far line: %+v", far)
	}
	// gross 1,100,005; discounts 112,501; base 987,504; tax 98,750.4 -> 98,750.
	if inv.Subtotal != 1100005 || inv.DiscountTotal != 112501 || inv.TaxAmount != 98750 || inv.Total != 1086254 {
		t.Errorf("totals: sub %d disc %d tax %d total %d", inv.Subtotal, inv.DiscountTotal, inv.TaxAmount, inv.Total)
	}
	if inv.Total != inv.Subtotal-inv.DiscountTotal+inv.TaxAmount {
		t.Error("total is not base plus tax")
	}
	// Stored as returned.
	got, _ := fx.inv.Get(ctx, fx.a.Hex(), inv.ID.Hex())
	if got.DiscountTotal != 112501 || got.Total != 1086254 {
		t.Errorf("stored invoice: %+v", got)
	}
}

func TestPercentDiscountRounding(t *testing.T) {
	cases := []struct {
		amount int64
		bp     int
		want   int64
	}{
		{5, 1000, 1}, {15, 1000, 2}, {14, 1000, 1}, {1, 1, 0}, {100, 10000, 100},
		{12345, 825, 1018}, {0, 5000, 0}, {MaxFeeAmount, 10000, MaxFeeAmount},
	}
	for _, c := range cases {
		l := models.InvoiceLine{Amount: c.amount}
		if err := applyDiscount(&l, LineDiscountInput{PercentBP: intp(c.bp), Reason: "x"}, false); err != nil || l.Discount != c.want {
			t.Errorf("%d at %d bp = %d (%v), want %d", c.amount, c.bp, l.Discount, err, c.want)
		}
		if c.want == 0 && (l.DiscountBP != 0 || l.DiscountReason != "") {
			t.Errorf("a zero discount keeps its bp or reason: %+v", l)
		}
	}
	// Overflow guard: an absurd amount is refused rather than wrapped.
	l := models.InvoiceLine{Amount: 1 << 62}
	if err := applyDiscount(&l, LineDiscountInput{PercentBP: intp(10000)}, false); domStatus(err) != http.StatusUnprocessableEntity {
		t.Errorf("overflowing amount: %v", err)
	}
}

func TestCreateRejectsBadDiscounts(t *testing.T) {
	fx := newInvFixture()
	domainFee, deployFee, _ := fx.seed(t)
	ctx := context.Background()
	sel := []string{SubscriptionLineID, domainFee.ID.Hex()}
	long := strings.Repeat("x", 101)

	cases := map[string]map[string]LineDiscountInput{
		"more than the line":    {domainFee.ID.Hex(): {Amount: i64p(100001)}},
		"amount and percent":    {domainFee.ID.Hex(): {Amount: i64p(1), PercentBP: intp(100)}},
		"neither":               {domainFee.ID.Hex(): {Reason: "just a reason"}},
		"unselected fee":        {deployFee.ID.Hex(): {Amount: i64p(1)}},
		"unknown key":           {"nope": {Amount: i64p(1)}},
		"negative amount":       {domainFee.ID.Hex(): {Amount: i64p(-1)}},
		"percent above 100":     {domainFee.ID.Hex(): {PercentBP: intp(10001)}},
		"negative percent":      {domainFee.ID.Hex(): {PercentBP: intp(-1)}},
		"reason too long":       {domainFee.ID.Hex(): {Amount: i64p(1), Reason: long}},
		"reason with a bidi":    {domainFee.ID.Hex(): {Amount: i64p(1), Reason: "a\u202eb"}},
		"reason with a newline": {domainFee.ID.Hex(): {Amount: i64p(1), Reason: "a\nb"}},
	}
	for name, d := range cases {
		_, err := fx.inv.Create(ctx, fx.a.Hex(), InvoiceInput{FeeIDs: sel, Discounts: d})
		if domStatus(err) != http.StatusUnprocessableEntity {
			t.Errorf("%s: want 422, got %v", name, err)
		}
	}
	// A line that only came in through all_due and was skipped is not selected.
	if len(fx.st.docs) != 0 {
		t.Fatal("a refused discount created an invoice")
	}
	// The boundary is accepted: the whole line, and an exact fixed message.
	if _, err := fx.inv.Create(ctx, fx.a.Hex(), InvoiceInput{FeeIDs: sel, Discounts: map[string]LineDiscountInput{domainFee.ID.Hex(): {Amount: i64p(100000)}}}); err != nil {
		t.Errorf("100%% discount refused: %v", err)
	}
	fx = newInvFixture()
	fx.seed(t)
	_, err := fx.inv.Create(ctx, fx.a.Hex(), InvoiceInput{FeeIDs: []string{SubscriptionLineID}, Discounts: map[string]LineDiscountInput{"nope": {Amount: i64p(1)}}})
	if err == nil || !strings.Contains(err.Error(), "discount refers to a line that is not on this invoice") {
		t.Errorf("message: %v", err)
	}
}

func TestEditSetsAndClearsDiscounts(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	ctx := context.Background()
	inv := fx.createAllDue(t, intp(1000))
	id := inv.ID.Hex()
	keys := append([]string(nil), inv.PeriodKeys...)
	subKey := SubscriptionLineID

	out, err := fx.inv.Edit(ctx, fx.a.Hex(), id, InvoiceEditInput{Discounts: map[string]LineDiscountInput{
		subKey: {PercentBP: intp(500), Reason: "Year two"},
	}})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	l := lineByKey(t, out, subKey)
	if l.Discount != 50000 || l.DiscountBP != 500 || l.DiscountReason != "Year two" || l.Amount != 1000000 {
		t.Errorf("line: %+v", l)
	}
	// gross 1,140,000; base 1,090,000; tax 109,000.
	if out.Subtotal != 1140000 || out.DiscountTotal != 50000 || out.TaxAmount != 109000 || out.Total != 1199000 {
		t.Errorf("totals: %d %d %d %d", out.Subtotal, out.DiscountTotal, out.TaxAmount, out.Total)
	}

	// A tax change keeps the discount and uses the discounted base.
	out, err = fx.inv.Edit(ctx, fx.a.Hex(), id, InvoiceEditInput{TaxRateBP: intp(0)})
	if err != nil || out.DiscountTotal != 50000 || out.TaxAmount != 0 || out.Total != 1090000 {
		t.Errorf("tax edit after discount: %v %+v", err, out)
	}

	// Switching to an amount resets the percent; other lines are untouched.
	out, err = fx.inv.Edit(ctx, fx.a.Hex(), id, InvoiceEditInput{Discounts: map[string]LineDiscountInput{subKey: {Amount: i64p(7)}}})
	if err != nil {
		t.Fatal(err)
	}
	if l := lineByKey(t, out, subKey); l.Discount != 7 || l.DiscountBP != 0 || l.DiscountReason != "" {
		t.Errorf("amount edit: %+v", l)
	}

	// Clearing: an empty value, then a zero amount, then a zero percent.
	for name, clear := range map[string]LineDiscountInput{"empty": {}, "zero amount": {Amount: i64p(0), Reason: "x"}, "zero percent": {PercentBP: intp(0), Reason: "x"}} {
		if _, err := fx.inv.Edit(ctx, fx.a.Hex(), id, InvoiceEditInput{Discounts: map[string]LineDiscountInput{subKey: {Amount: i64p(10)}}}); err != nil {
			t.Fatal(err)
		}
		out, err = fx.inv.Edit(ctx, fx.a.Hex(), id, InvoiceEditInput{Discounts: map[string]LineDiscountInput{subKey: clear}})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if l := lineByKey(t, out, subKey); l.Discount != 0 || l.DiscountBP != 0 || l.DiscountReason != "" {
			t.Errorf("%s: discount not cleared: %+v", name, l)
		}
		if out.DiscountTotal != 0 || out.Total != out.Subtotal {
			t.Errorf("%s: totals after clear: %+v", name, out)
		}
	}

	got, _ := fx.inv.Get(ctx, fx.a.Hex(), id)
	if strings.Join(got.PeriodKeys, "|") != strings.Join(keys, "|") || got.Number != inv.Number || got.Status != models.InvoiceDraft || len(got.Lines) != len(inv.Lines) {
		t.Error("edit touched period keys, number, status or the line list")
	}
	for i := range got.Lines {
		if got.Lines[i].Amount != inv.Lines[i].Amount || got.Lines[i].Period != inv.Lines[i].Period || got.Lines[i].FeeID != inv.Lines[i].FeeID {
			t.Errorf("line %d snapshot changed", i)
		}
	}
}

func TestEditDiscountValidation(t *testing.T) {
	fx := newInvFixture()
	domainFee, _, _ := fx.seed(t)
	ctx := context.Background()
	inv := fx.createAllDue(t, nil)
	id := inv.ID.Hex()
	dk := domainFee.ID.Hex()

	for name, d := range map[string]map[string]LineDiscountInput{
		"more than the line": {dk: {Amount: i64p(100001)}},
		"both":               {dk: {Amount: i64p(1), PercentBP: intp(1)}},
		"unknown line":       {"nope": {Amount: i64p(1)}},
		"percent too high":   {dk: {PercentBP: intp(10001)}},
		"bad reason":         {dk: {Amount: i64p(1), Reason: "a\x00b"}},
		"one good one bad":   {dk: {Amount: i64p(1)}, "nope": {Amount: i64p(1)}},
	} {
		if _, err := fx.inv.Edit(ctx, fx.a.Hex(), id, InvoiceEditInput{Discounts: d}); domStatus(err) != http.StatusUnprocessableEntity {
			t.Errorf("%s: %v", name, err)
		}
	}
	got, _ := fx.inv.Get(ctx, fx.a.Hex(), id)
	if got.DiscountTotal != 0 || got.Total != inv.Total {
		t.Error("a rejected edit changed the invoice")
	}
	if _, err := fx.inv.Edit(ctx, fx.a.Hex(), id, InvoiceEditInput{Discounts: map[string]LineDiscountInput{}}); domStatus(err) != http.StatusUnprocessableEntity {
		t.Errorf("empty discounts object alone is nothing to change: %v", err)
	}
}

func TestEditDiscountIsDraftOnlyAndTenantScoped(t *testing.T) {
	ctx := context.Background()
	in := InvoiceEditInput{Discounts: map[string]LineDiscountInput{SubscriptionLineID: {Amount: i64p(1)}}}

	fx := newInvFixture()
	fx.seed(t)
	inv := fx.createAllDue(t, nil)
	if _, err := fx.inv.Edit(ctx, fx.b.Hex(), inv.ID.Hex(), in); domStatus(err) != http.StatusNotFound {
		t.Errorf("cross-tenant: %v", err)
	}
	if got, _ := fx.inv.Get(ctx, fx.a.Hex(), inv.ID.Hex()); got.DiscountTotal != 0 {
		t.Error("cross-tenant edit changed the invoice")
	}
	if _, err := fx.inv.Send(ctx, fx.a.Hex(), inv.ID.Hex(), SendInput{}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.inv.Edit(ctx, fx.a.Hex(), inv.ID.Hex(), in); domStatus(err) != http.StatusConflict || !strings.Contains(err.Error(), "only a draft invoice can be edited") {
		t.Errorf("sent: %v", err)
	}

	// Loses the race to a status change: 409 and nothing written.
	fx2 := newInvFixture()
	fx2.seed(t)
	inv2 := fx2.createAllDue(t, nil)
	rs := &raceStore{fakeInvoiceStore: fx2.st}
	rs.before = func() {
		rs.before = nil
		for _, d := range fx2.st.docs {
			d.Status = models.InvoiceVoid
		}
	}
	fx2.inv.repo = rs
	if _, err := fx2.inv.Edit(ctx, fx2.a.Hex(), inv2.ID.Hex(), in); domStatus(err) != http.StatusConflict {
		t.Errorf("race: %v", err)
	}
	for _, d := range fx2.st.docs {
		if d.DiscountTotal != 0 || d.Lines[0].Discount != 0 {
			t.Error("the edit was applied despite the race")
		}
	}
}

// An invoice saved before discounts existed has no line keys or discount
// fields: it loads, totals stay as stored, and its lines can be discounted by
// the key they would have been created with.
func TestLegacyInvoiceWithoutDiscountFields(t *testing.T) {
	legacyJSON := `{"label":"Hosting","amount":40000,"fee_id":null,"domain_id":null,"kind":"deployment","period":"2026-10-16"}`
	var l models.InvoiceLine
	if err := json.Unmarshal([]byte(legacyJSON), &l); err != nil || l.Discount != 0 || l.DiscountBP != 0 || l.DiscountReason != "" || l.LineKey != "" || l.Net() != 40000 {
		t.Fatalf("legacy line json: %+v %v", l, err)
	}
	raw, err := bson.Marshal(bson.M{"number": "INV-2026-0001", "subtotal": int64(40000), "total": int64(40000), "lines": bson.A{bson.M{"label": "Hosting", "amount": int64(40000), "kind": "deployment", "period": "2026-10-16"}}})
	if err != nil {
		t.Fatal(err)
	}
	var inv models.Invoice
	if err := bson.Unmarshal(raw, &inv); err != nil || inv.DiscountTotal != 0 || inv.Lines[0].Discount != 0 || inv.Total != 40000 {
		t.Fatalf("legacy invoice bson: %+v %v", inv, err)
	}
	out, err := json.Marshal(inv)
	if err != nil || !strings.Contains(string(out), `"discount_total":0`) || !strings.Contains(string(out), `"discount":0`) {
		t.Fatalf("legacy invoice json: %s %v", out, err)
	}

	// Editing a legacy line by its derived key.
	fx := newInvFixture()
	_, deployFee, _ := fx.seed(t)
	ctx := context.Background()
	made := fx.createAllDue(t, nil)
	for _, d := range fx.st.docs {
		for i := range d.Lines {
			d.Lines[i].LineKey = "" // as saved before line keys existed
		}
	}
	out2, err := fx.inv.Edit(ctx, fx.a.Hex(), made.ID.Hex(), InvoiceEditInput{Discounts: map[string]LineDiscountInput{deployFee.ID.Hex(): {Amount: i64p(1000)}, SubscriptionLineID: {PercentBP: intp(100)}}})
	if err != nil {
		t.Fatalf("edit legacy: %v", err)
	}
	if l := lineByKey(t, out2, deployFee.ID.Hex()); l.Discount != 1000 || l.LineKey != deployFee.ID.Hex() {
		t.Errorf("legacy fee line: %+v", l)
	}
	if l := lineByKey(t, out2, SubscriptionLineID); l.Discount != 10000 || l.LineKey != SubscriptionLineID {
		t.Errorf("legacy subscription line: %+v", l)
	}
	if out2.DiscountTotal != 11000 || out2.Total != made.Subtotal-11000 {
		t.Errorf("totals: %+v", out2)
	}
}

func TestMarkPaidAmountReceivedIsTheNetTotal(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	ctx := context.Background()
	inv, err := fx.inv.Create(ctx, fx.a.Hex(), InvoiceInput{AllDue: true, TaxRateBP: intp(1000),
		Discounts: map[string]LineDiscountInput{SubscriptionLineID: {Amount: i64p(200000)}}})
	if err != nil {
		t.Fatal(err)
	}
	// gross 1,140,000; discount 200,000; base 940,000; tax 94,000; net total 1,034,000.
	if inv.Total != 1034000 {
		t.Fatalf("total = %d", inv.Total)
	}
	if _, err := fx.inv.Send(ctx, fx.a.Hex(), inv.ID.Hex(), SendInput{}); err != nil {
		t.Fatal(err)
	}
	for name, gross := range map[string]int64{"gross subtotal": inv.Subtotal, "gross plus tax": inv.Subtotal + inv.TaxAmount, "base without tax": 940000} {
		if _, err := fx.inv.MarkPaid(ctx, fx.a.Hex(), inv.ID.Hex(), PaidInput{AmountReceived: i64p(gross)}); domStatus(err) != http.StatusBadRequest {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	paid, err := fx.inv.MarkPaid(ctx, fx.a.Hex(), inv.ID.Hex(), PaidInput{AmountReceived: i64p(1034000)})
	if err != nil || paid.Status != models.InvoicePaid || paid.Payment == nil || paid.Payment.AmountReceived != 1034000 {
		t.Fatalf("paid at the net total: %+v %v", paid, err)
	}

	// Without amount_received the recorded amount is the net total.
	fx2 := newInvFixture()
	fx2.seed(t)
	inv2, _ := fx2.inv.Create(ctx, fx2.a.Hex(), InvoiceInput{AllDue: true, Discounts: map[string]LineDiscountInput{SubscriptionLineID: {Amount: i64p(1)}}})
	_, _ = fx2.inv.Send(ctx, fx2.a.Hex(), inv2.ID.Hex(), SendInput{})
	p2, err := fx2.inv.MarkPaid(ctx, fx2.a.Hex(), inv2.ID.Hex(), PaidInput{})
	if err != nil || p2.Payment.AmountReceived != inv2.Subtotal-1 {
		t.Errorf("default amount: %+v %v", p2, err)
	}
}

func TestDiscountDoesNotChangePeriodKeysOrDomainSettlement(t *testing.T) {
	fx := newInvFixture()
	fx.seed(t)
	ctx := context.Background()
	plain := fx.createAllDue(t, nil)
	keys := append([]string(nil), plain.PeriodKeys...)
	if _, err := fx.inv.Void(ctx, fx.a.Hex(), plain.ID.Hex()); err != nil {
		t.Fatal(err)
	}
	disc, err := fx.inv.Create(ctx, fx.a.Hex(), InvoiceInput{AllDue: true, Discounts: map[string]LineDiscountInput{SubscriptionLineID: {PercentBP: intp(10000)}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(disc.PeriodKeys, "|") != strings.Join(keys, "|") {
		t.Errorf("period keys differ with a discount:\n%v\n%v", disc.PeriodKeys, keys)
	}
	if disc.Lines[0].Amount != 1000000 || disc.Lines[0].Discount != 1000000 || disc.Total != disc.Subtotal-1000000 {
		t.Errorf("a 100%% discount: %+v", disc)
	}
	// The same period cannot be invoiced twice, discounted or not.
	if _, err := fx.inv.Create(ctx, fx.a.Hex(), InvoiceInput{FeeIDs: []string{SubscriptionLineID}}); domStatus(err) != http.StatusConflict {
		t.Errorf("double invoice: %v", err)
	}
}

func TestPreviewShowsDiscountOnlyWhenDiscounted(t *testing.T) {
	fx := newInvFixture()
	domainFee, _, _ := fx.seed(t)
	ctx := context.Background()

	plain := fx.createAllDue(t, nil)
	p, err := fx.inv.Preview(ctx, fx.a.Hex(), plain.ID.Hex())
	if err != nil || strings.Contains(p.Text, "Хөнгөлөлт") || strings.Contains(p.HTML, "Хөнгөлөлт") {
		t.Fatalf("plain preview: %v\n%s", err, p.Text)
	}

	fx2 := newInvFixture()
	d2, _, _ := fx2.seed(t)
	inv, err := fx2.inv.Create(ctx, fx2.a.Hex(), InvoiceInput{AllDue: true, TaxRateBP: intp(1000),
		Discounts: map[string]LineDiscountInput{d2.ID.Hex(): {PercentBP: intp(1000), Reason: "Loyal"}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = domainFee
	p, err = fx2.inv.Preview(ctx, fx2.a.Hex(), inv.ID.Hex())
	if err != nil {
		t.Fatal(err)
	}
	// Domain line 100,000 less 10,000: net 90,000; tax on 1,130,000 = 113,000.
	for _, want := range []string{
		"Хөнгөлөлт −₮10,000 (10%) · Loyal",
		"ТОО 1 × 100,000 = 90,000 ₮",
		"Дэд дүн: ₮1,140,000\nХөнгөлөлт: −₮10,000\nНӨАТ 10%: ₮113,000\nНИЙТ ТӨЛӨХ ДҮН: ₮1,243,000",
	} {
		if !strings.Contains(p.Text, want) {
			t.Errorf("preview lacks %q:\n%s", want, p.Text)
		}
	}
	if !strings.Contains(p.HTML, "Хөнгөлөлт −₮10,000 (10%) · Loyal") {
		t.Error("html lacks the discount sub-line")
	}
}
