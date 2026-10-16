package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/mailer"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// MaxInvoiceLines bounds the lines of one invoice (and the line keys the mail
// template accepts).
const MaxInvoiceLines = mailer.MaxInvoiceLines

// MaxInvoicesPerTenant bounds the invoice history of one tenant.
const MaxInvoicesPerTenant = 2000

// invoiceStore is the slice of the invoice repository the service needs,
// narrow so the rules can be tested without MongoDB. Every method is scoped
// by tenant: an invoice id of another tenant comes back as
// mongo.ErrNoDocuments.
type invoiceStore interface {
	// NextSeq hands out the next number of the month from an atomic counter
	// keyed by year and month. Numbers are unique and increasing but may have
	// gaps: one taken by a Create whose insert is then rejected is skipped,
	// never reused.
	NextSeq(ctx context.Context, year, month int) (int64, error)
	Insert(ctx context.Context, inv *models.Invoice) error
	ListByTenant(ctx context.Context, tenantID primitive.ObjectID) ([]*models.Invoice, error)
	CountByTenant(ctx context.Context, tenantID primitive.ObjectID) (int64, error)
	Find(ctx context.Context, tenantID, id primitive.ObjectID) (*models.Invoice, error)
	// LineHolder returns the number of a non-void invoice of the tenant that
	// already bills the line, or "" when none does. It matches on the fee id
	// when feeID is set, else on the domain id when domainID is set (a derived
	// domain line), else on kind and period. Scoped by tenant, so the number is
	// always one of the tenant's own.
	LineHolder(ctx context.Context, tenantID primitive.ObjectID, kind string, feeID, domainID *primitive.ObjectID, period string) (string, error)
	// DeleteDraft removes the invoice only while it is a draft of the tenant,
	// in one conditional delete. mongo.ErrNoDocuments when it is missing for
	// the tenant or no longer a draft.
	DeleteDraft(ctx context.Context, tenantID, id primitive.ObjectID) error
	Transition(ctx context.Context, tenantID, id primitive.ObjectID, from []string, set bson.M) (*models.Invoice, error)
}

// billSource lists what a tenant can be invoiced for right now.
type billSource interface {
	Billables(ctx context.Context, tid primitive.ObjectID) ([]Billable, *Billable, error)
}

// feeAdvancer moves a fee's last_invoiced_through forward.
type feeAdvancer interface {
	AdvanceInvoicedThrough(ctx context.Context, tenantID, id primitive.ObjectID, through time.Time) error
}

// domainSettler records a client payment on a domain of the tenant. It is
// monotone (paid_through never moves back) and idempotent.
type domainSettler interface {
	// method and reference (domain vocabulary: cash|bank|card|other) are
	// recorded with the payment when non-empty.
	MarkClientPaid(ctx context.Context, tenantID, id primitive.ObjectID, paidAt, through time.Time, method, reference string) error
}

// paymentSettingsReader returns the platform's bank details; an empty,
// not configured value when none were saved.
type paymentSettingsReader interface {
	Get(ctx context.Context) (*models.PaymentSettings, error)
}

// InvoiceInput is what a client may say about a new invoice: which lines, an
// optional tax rate and an optional due date. Amounts, numbers, recipient and
// status are never taken from the client.
type InvoiceInput struct {
	// FeeIDs are fee ids, SubscriptionLineID and/or derived domain line ids
	// ("domain:<id>", see DomainLinePrefix).
	FeeIDs []string `json:"fee_ids"`
	// AllDue adds every due line.
	AllDue bool `json:"all_due"`
	// TaxRateBP is the tax rate in basis points, 0..10000. Omitted means none.
	TaxRateBP *int       `json:"tax_rate_bp"`
	DueAt     *time.Time `json:"due_at"`
	// Discounts are per-line discounts keyed by the same selection id as
	// FeeIDs; a key that is not a line of the invoice is refused.
	Discounts map[string]LineDiscountInput `json:"discounts"`
}

// SendInput is the optional body of the send call.
type SendInput struct {
	// Resend must be true to send an invoice that was already sent.
	Resend bool `json:"resend"`
}

// PaidInput is the optional body of the mark paid call: the transaction
// record. Every field is optional; the amount may only confirm the total.
type PaidInput struct {
	// PaidAt is RFC3339 or a YYYY-MM-DD date. Default now.
	PaidAt string `json:"paid_at"`
	// Method is bank_transfer (default), cash, card or other.
	Method    string `json:"method"`
	Reference string `json:"reference"`
	PayerName string `json:"payer_name"`
	// PayerAccountLast4 is exactly four digits, or empty.
	PayerAccountLast4 string `json:"payer_account_last4"`
	// AmountReceived, when given, must equal the invoice total (manual partial
	// payments are not supported).
	AmountReceived *int64 `json:"amount_received"`
	Note           string `json:"note"`
}

// InvoiceService makes, sends and settles the invoices of a tenant. It is
// bookkeeping: nothing here touches entitlement, routing or authentication.
type InvoiceService struct {
	repo     invoiceStore
	bills    billSource
	fees     feeAdvancer
	domains  domainSettler
	tenants  tenantFinder
	mail     sender
	settings paymentSettingsReader
	appName  string
	now      func() time.Time
}

// NewInvoiceService wires the service. A nil mail (or one that is not
// configured) makes Send answer 503; nil settings (or ones that are not
// configured) make Send answer 409.
func NewInvoiceService(repo invoiceStore, bills billSource, fees feeAdvancer, domains domainSettler, tenants tenantFinder, mail sender, settings paymentSettingsReader, appName string) *InvoiceService {
	return &InvoiceService{repo: repo, bills: bills, fees: fees, domains: domains, tenants: tenants, mail: mail, settings: settings, appName: appName, now: time.Now}
}

func parseInvoiceID(s string) (primitive.ObjectID, error) {
	id, err := primitive.ObjectIDFromHex(s)
	if err != nil {
		return primitive.NilObjectID, apierr.BadRequest("invalid invoice id").In(apierr.DomainTenant)
	}
	return id, nil
}

func invoiceNotFoundOr(err error) error {
	if errors.Is(err, mongo.ErrNoDocuments) {
		return apierr.NotFound("invoice").In(apierr.DomainTenant)
	}
	return apierr.Internal(err)
}

func (s *InvoiceService) requireTenant(ctx context.Context, id primitive.ObjectID) (*models.Tenant, error) {
	t, err := s.tenants.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apierr.NotFound("tenant").In(apierr.DomainTenant)
		}
		return nil, apierr.Internal(err)
	}
	return t, nil
}

func (s *InvoiceService) List(ctx context.Context, tenantIDStr string) ([]*models.Invoice, error) {
	tid, err := parseTenantID(tenantIDStr)
	if err != nil {
		return nil, err
	}
	if _, err := s.requireTenant(ctx, tid); err != nil {
		return nil, err
	}
	out, err := s.repo.ListByTenant(ctx, tid)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return out, nil
}

func (s *InvoiceService) Get(ctx context.Context, tenantIDStr, invIDStr string) (*models.Invoice, error) {
	tid, iid, err := parseInvoiceKey(tenantIDStr, invIDStr)
	if err != nil {
		return nil, err
	}
	inv, err := s.repo.Find(ctx, tid, iid)
	if err != nil {
		return nil, invoiceNotFoundOr(err)
	}
	return inv, nil
}

func parseInvoiceKey(tenantIDStr, invIDStr string) (primitive.ObjectID, primitive.ObjectID, error) {
	tid, err := parseTenantID(tenantIDStr)
	if err != nil {
		return tid, primitive.NilObjectID, err
	}
	iid, err := parseInvoiceID(invIDStr)
	return tid, iid, err
}

// ── Money ────────────────────────────────────────────────────────────────

// TaxAmount is the tax on subtotal at bp basis points, rounded half up to a
// whole MNT. The multiplication cannot overflow: subtotal is at most
// MaxInvoiceLines*MaxFeeAmount and bp at most 10000.
func TaxAmount(subtotal int64, bp int) int64 {
	return (subtotal*int64(bp) + 5000) / 10000
}

// InvoiceTotals computes subtotal (the gross amounts), tax and total of
// lines. Tax is computed on the discounted base (subtotal minus the line
// discounts) and total is that base plus tax. Done in one place, on the
// server, from the snapshot lines only.
func InvoiceTotals(lines []models.InvoiceLine, bp int) (subtotal, tax, total int64) {
	subtotal, _, tax, total = invoiceTotals(lines, bp)
	return subtotal, tax, total
}

// invoiceTotals is InvoiceTotals plus the discount total.
func invoiceTotals(lines []models.InvoiceLine, bp int) (subtotal, discount, tax, total int64) {
	for _, l := range lines {
		subtotal += l.Amount
		discount += l.Discount
	}
	base := subtotal - discount
	tax = TaxAmount(base, bp)
	return subtotal, discount, tax, base + tax
}

// LineDiscountInput is a client's discount for one line: exactly one of
// Amount (whole MNT) and PercentBP (0..10000), plus an optional reason. The
// stored discount is always computed here, never taken from a percent.
type LineDiscountInput struct {
	Amount    *int64 `json:"amount"`
	PercentBP *int   `json:"percent_bp"`
	Reason    string `json:"reason"`
}

// applyDiscount validates in against the line and sets its discount fields. On
// an edit (clearEmpty) an input with neither amount nor percent clears the
// discount; on create it is refused. A discount that comes to 0 clears all
// three fields.
func applyDiscount(l *models.InvoiceLine, in LineDiscountInput, clearEmpty bool) error {
	reason, ok := cleanText(in.Reason, 100, false)
	if !ok {
		return invalid("discount reason")
	}
	var discount int64
	var bp int
	switch {
	case in.Amount != nil && in.PercentBP != nil:
		return apierr.ValidationFailed("give either a discount amount or a percent, not both").In(apierr.DomainTenant)
	case in.Amount != nil:
		discount = *in.Amount
		if discount < 0 {
			return invalid("discount")
		}
	case in.PercentBP != nil:
		bp = *in.PercentBP
		if bp < 0 || bp > 10000 || l.Amount < 0 || l.Amount > math.MaxInt64/10000-1 {
			return invalid("discount")
		}
		discount = (l.Amount*int64(bp) + 5000) / 10000
	case !clearEmpty:
		return apierr.ValidationFailed("a discount needs an amount or a percent").In(apierr.DomainTenant)
	}
	if discount > l.Amount {
		return apierr.ValidationFailed("discount cannot exceed the line amount").In(apierr.DomainTenant)
	}
	if discount == 0 {
		bp, reason = 0, ""
	}
	l.Discount, l.DiscountBP, l.DiscountReason = discount, bp, reason
	return nil
}

// ── Create ───────────────────────────────────────────────────────────────

func (s *InvoiceService) Create(ctx context.Context, tenantIDStr string, in InvoiceInput) (*models.Invoice, error) {
	tid, err := parseTenantID(tenantIDStr)
	if err != nil {
		return nil, err
	}
	bp := 0
	if in.TaxRateBP != nil {
		bp = *in.TaxRateBP
		if bp < 0 || bp > 10000 {
			return nil, invalid("tax rate")
		}
	}
	if len(in.FeeIDs) > MaxInvoiceLines {
		return nil, apierr.ValidationFailed("too many invoice lines").In(apierr.DomainTenant)
	}
	now := s.now().UTC()
	if in.DueAt != nil && (in.DueAt.UTC().Year() < 2000 || in.DueAt.UTC().Year() > 2200 || in.DueAt.UTC().Before(dayStart(now))) {
		return nil, invalid("due date")
	}
	if _, err := s.requireTenant(ctx, tid); err != nil {
		return nil, err
	}
	n, err := s.repo.CountByTenant(ctx, tid)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	if n >= MaxInvoicesPerTenant {
		return nil, apierr.ValidationFailed("too many invoices for this tenant").In(apierr.DomainTenant)
	}

	bills, sub, err := s.bills.Billables(ctx, tid)
	if err != nil {
		return nil, err
	}
	picked, err := selectBillables(in, bills, sub)
	if err != nil {
		return nil, err
	}
	if len(picked) == 0 {
		return nil, apierr.ValidationFailed("no lines selected").In(apierr.DomainTenant)
	}
	if len(picked) > MaxInvoiceLines {
		return nil, apierr.ValidationFailed("too many invoice lines").In(apierr.DomainTenant)
	}

	explicit := map[string]bool{}
	for _, raw := range in.FeeIDs {
		explicit[strings.TrimSpace(raw)] = true
	}
	lines := make([]models.InvoiceLine, 0, len(picked))
	keys := make([]string, 0, len(picked))
	firstHolder := ""
	for _, b := range picked {
		var feeID *primitive.ObjectID
		if b.Fee != nil {
			id := b.Fee.ID
			feeID = &id
		}
		holder, err := s.repo.LineHolder(ctx, tid, b.Kind, feeID, b.DomainID, b.Period())
		if err != nil {
			return nil, apierr.Internal(err)
		}
		if holder != "" {
			// A line the client named is refused; one that only came in through
			// all_due is skipped, so a single invoiced line does not fail the lot.
			if explicit[b.ID] {
				return nil, errLineOnInvoice(holder)
			}
			if firstHolder == "" {
				firstHolder = holder
			}
			continue
		}
		lines = append(lines, models.InvoiceLine{
			Label: b.Label, Amount: b.Amount, FeeID: feeID, DomainID: b.DomainID, Kind: b.Kind, Period: b.Period(),
			LineKey: b.ID,
		})
		keys = append(keys, periodKey(tid, b.Kind, feeID, b.DomainID, b.Period()))
	}
	if len(lines) == 0 {
		// Every due line is already on an invoice.
		return nil, errLineOnInvoice(firstHolder)
	}

	// Checked before a number is taken, so a refused discount burns none.
	if err := applyDiscounts(lines, in.Discounts, false); err != nil {
		return nil, err
	}

	seq, err := s.repo.NextSeq(ctx, now.Year(), int(now.Month()))
	if err != nil {
		return nil, apierr.Internal(err)
	}
	sub0, disc, tax, total := invoiceTotals(lines, bp)
	inv := &models.Invoice{
		TenantID:      tid,
		Number:        InvoiceNumber(now.Year(), int(now.Month()), seq),
		IssuedAt:      now,
		Lines:         lines,
		Subtotal:      sub0,
		DiscountTotal: disc,
		TaxRateBP:     bp,
		TaxAmount:     tax,
		Total:         total,
		Currency:      models.CurrencyMNT,
		Status:        models.InvoiceDraft,
		// The read check above gives a clean message; the unique index over
		// these keys is what holds when two Creates race past it.
		PeriodKeys: keys,
	}
	if in.DueAt != nil {
		d := in.DueAt.UTC()
		inv.DueAt = &d
	}
	if err := s.repo.Insert(ctx, inv); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			if strings.Contains(err.Error(), periodKeysIndexName) {
				return nil, errLineOnInvoice("")
			}
			return nil, apierr.Conflict("invoice number is already taken").In(apierr.DomainTenant)
		}
		return nil, apierr.Internal(err)
	}
	return inv, nil
}

// applyDiscounts applies the per-line discounts, keyed by line key (the
// selection id), to lines in place. A key that matches no line is a 422, so a
// discount for an unselected line is never silently dropped. With clearEmpty
// (edit) an input without amount or percent clears that line's discount.
func applyDiscounts(lines []models.InvoiceLine, discounts map[string]LineDiscountInput, clearEmpty bool) error {
	if len(discounts) > MaxInvoiceLines {
		return apierr.ValidationFailed("too many discounts").In(apierr.DomainTenant)
	}
	index := make(map[string]int, len(lines))
	for i, l := range lines {
		index[models.InvoiceLineKey(l)] = i
	}
	for raw, in := range discounts {
		i, ok := index[strings.TrimSpace(raw)]
		if !ok {
			return apierr.ValidationFailed("discount refers to a line that is not on this invoice").In(apierr.DomainTenant)
		}
		if err := applyDiscount(&lines[i], in, clearEmpty); err != nil {
			return err
		}
	}
	return nil
}

// errLineOnInvoice is the double-invoice 409. number is the holding invoice of
// the same tenant (LineHolder is tenant-scoped); empty only when the race was
// lost to the unique index, where no number is known.
func errLineOnInvoice(number string) error {
	if number == "" {
		return apierr.Conflict("a line is already on an invoice for this period").In(apierr.DomainTenant)
	}
	return apierr.Conflict("a line is already on invoice " + number + " for this period").In(apierr.DomainTenant)
}

// periodKeysIndexName is the unique partial index over Invoice.PeriodKeys
// (see repository.indexSpecs).
const periodKeysIndexName = "invoice_period_keys_unique"

// periodKey identifies one billed line: tenant, kind, fee (a derived domain
// line has no fee and keys on its domain as "d-<id>"; the subscription is "-")
// and period (for a domain line the expires_at date billed, or noExpiryPeriod).
// Equal keys on two live invoices is a double bill.
func periodKey(tid primitive.ObjectID, kind string, feeID, domainID *primitive.ObjectID, period string) string {
	fee := "-"
	switch {
	case feeID != nil:
		fee = feeID.Hex()
	case domainID != nil:
		fee = "d-" + domainID.Hex()
	}
	return tid.Hex() + ":" + kind + ":" + fee + ":" + period
}

// InvoiceNumber formats INV-YYYY-MM-NNN: the sequence restarts every month and
// NNN grows past three digits after 999. Numbers issued before the monthly
// sequence (INV-YYYY-NNNN) stay valid.
func InvoiceNumber(year, month int, seq int64) string {
	return fmt.Sprintf("INV-%04d-%02d-%03d", year, month, seq)
}

// selectBillables resolves the request to lines, in ledger order (the
// subscription first), each at most once. A fee id that is not the tenant's
// is a 404, exactly as if it did not exist.
func selectBillables(in InvoiceInput, bills []Billable, sub *Billable) ([]Billable, error) {
	explicit := map[string]bool{}
	for _, raw := range in.FeeIDs {
		id := strings.TrimSpace(raw)
		if id != SubscriptionLineID {
			hex := strings.TrimPrefix(id, DomainLinePrefix)
			if _, err := primitive.ObjectIDFromHex(hex); err != nil {
				return nil, apierr.BadRequest("invalid fee id").In(apierr.DomainTenant)
			}
		}
		explicit[id] = true
	}

	all := make([]Billable, 0, len(bills)+1)
	if sub != nil {
		all = append(all, *sub)
	}
	all = append(all, bills...)

	known := map[string]bool{}
	var picked []Billable
	for _, b := range all {
		known[b.ID] = true
		switch {
		case explicit[b.ID]:
			if !b.Active {
				return nil, apierr.ValidationFailed("a selected line is inactive").In(apierr.DomainTenant)
			}
			if b.Period() == "" {
				return nil, apierr.ValidationFailed("a selected line has nothing to invoice").In(apierr.DomainTenant)
			}
			picked = append(picked, b)
		case in.AllDue && b.Due:
			picked = append(picked, b)
		}
	}
	for id := range explicit {
		if !known[id] {
			if id == SubscriptionLineID {
				return nil, apierr.NotFound("subscription").In(apierr.DomainTenant)
			}
			if strings.HasPrefix(id, DomainLinePrefix) {
				return nil, apierr.NotFound("domain").In(apierr.DomainTenant)
			}
			return nil, apierr.NotFound("fee").In(apierr.DomainTenant)
		}
	}
	return picked, nil
}

// ── Send ─────────────────────────────────────────────────────────────────

func (s *InvoiceService) Send(ctx context.Context, tenantIDStr, invIDStr string, in SendInput) (*models.Invoice, error) {
	tid, iid, err := parseInvoiceKey(tenantIDStr, invIDStr)
	if err != nil {
		return nil, err
	}
	inv, err := s.repo.Find(ctx, tid, iid)
	if err != nil {
		return nil, invoiceNotFoundOr(err)
	}
	switch inv.Status {
	case models.InvoiceDraft:
	case models.InvoiceSent:
		if !in.Resend {
			return nil, apierr.Conflict("invoice was already sent; set resend to send it again").In(apierr.DomainTenant)
		}
	case models.InvoiceSending:
		return nil, apierr.Conflict("invoice is being sent").In(apierr.DomainTenant)
	default:
		return nil, apierr.Conflict("invoice is paid or void").In(apierr.DomainTenant)
	}
	prev := inv.Status

	// Checked before anything is sent or changed: with mail off the invoice
	// stays exactly as it was.
	if s.mail == nil || !s.mail.Available() {
		return nil, apierr.FeatureUnavailable("email").In(apierr.DomainTenant)
	}
	t, err := s.requireTenant(ctx, tid)
	if err != nil {
		return nil, err
	}
	// The recipient is the tenant's own contact address, never the request's.
	to, ok := cleanEmail(t.ContactEmail)
	if !ok || to == "" {
		return nil, apierr.ValidationFailed("tenant has no valid contact email").In(apierr.DomainTenant)
	}
	// Without bank details the tenant would get an invoice it cannot pay.
	// Checked before the claim, so the invoice stays exactly as it was. A
	// resend keeps the snapshot taken at the first send.
	if s.settings == nil {
		return nil, errPaymentSettingsMissing()
	}
	ps, err := s.settings.Get(ctx)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	if ps == nil || !ps.Configured() {
		return nil, errPaymentSettingsMissing()
	}
	pay := inv.PaymentInstructions
	newSnapshot := pay == nil
	if newSnapshot {
		pay = ps.Snapshot()
	}
	// What is mailed; the stored snapshot (if any) is left as it was.
	mailPay := withIssuer(pay, ps)

	// Claim the invoice before mailing: one conditional update from the status
	// just read to "sending". Only one concurrent Send wins it, and while it
	// is held Void and MarkPaid are refused, so a voided invoice is never
	// mailed and a double click sends one mail.
	claimed, err := s.repo.Transition(ctx, tid, iid, []string{prev},
		bson.M{"status": models.InvoiceSending, "send_prev": prev})
	if err != nil {
		return nil, s.transitionErr(err, "invoice changed while it was being sent")
	}

	if err := s.mail.Send(to, mailer.TemplateInvoice, s.mailData(t, claimed, mailPay)); err != nil {
		s.releaseClaim(ctx, tid, iid, prev)
		// The cause is for the log; the mail log row already records it.
		return nil, apierr.Upstream(apierr.DomainTenant, err)
	}

	set := bson.M{"status": models.InvoiceSent, "sent_at": s.now().UTC(), "sent_to": to, "send_prev": ""}
	if newSnapshot {
		// Stored only once the mail went out, so a failed send leaves no
		// snapshot behind and the next send reads the then current settings.
		set["payment_instructions"] = pay
	}
	out, err := s.repo.Transition(ctx, tid, iid, []string{models.InvoiceSending}, set)
	if err != nil {
		return nil, s.transitionErr(err, "invoice changed while it was being sent")
	}
	return out, nil
}

// releaseClaim puts a claimed invoice back to the status it had, after a send
// that did not happen. Best effort: if it fails the invoice stays "sending"
// (shown to clients as its previous status) until an operator intervenes.
func (s *InvoiceService) releaseClaim(ctx context.Context, tid, iid primitive.ObjectID, prev string) {
	_, _ = s.repo.Transition(ctx, tid, iid, []string{models.InvoiceSending},
		bson.M{"status": prev, "send_prev": ""})
}

func errPaymentSettingsMissing() error {
	return apierr.Conflict("payment settings are not configured").In(apierr.DomainTenant)
}

func (s *InvoiceService) transitionErr(err error, msg string) error {
	if errors.Is(err, mongo.ErrNoDocuments) {
		return apierr.Conflict(msg).In(apierr.DomainTenant)
	}
	return apierr.Internal(err)
}

// displayDateLayout is how dates read on the invoice (YYYY.MM.DD).
const displayDateLayout = "2006.01.02"

// withIssuer returns pay with the issuer fields filled from the current
// settings when pay carries none (an invoice sent before the issuer fields
// existed). Never changes pay itself; a snapshot with a company name keeps
// all three issuer fields as stored, empty optional ones included.
func withIssuer(pay *models.PaymentInstructions, ps *models.PaymentSettings) *models.PaymentInstructions {
	out := *pay
	if out.CompanyName == "" && ps != nil {
		out.CompanyName, out.CompanyTagline, out.ContactPerson = ps.CompanyName, ps.CompanyTagline, ps.ContactPerson
	}
	return &out
}

// mailData is the fixed field set of the invoice template. Amounts and dates
// are formatted here; the mailer escapes and caps every value.
func (s *InvoiceService) mailData(t *models.Tenant, inv *models.Invoice, pay *models.PaymentInstructions) map[string]string {
	due := "Хүлээн авмагц"
	if inv.DueAt != nil {
		due = inv.DueAt.UTC().Format(displayDateLayout)
	}
	d := map[string]string{
		"app":        s.appName,
		"tenant":     t.Name,
		"number":     inv.Number,
		"issued_on":  inv.IssuedAt.UTC().Format(displayDateLayout),
		"due_on":     due,
		"currency":   inv.Currency,
		"subtotal":   FormatMNT(inv.Subtotal),
		"tax_amount": FormatMNT(inv.TaxAmount),
		"total":      FormatMNT(inv.Total),
		"line_count": strconv.Itoa(len(inv.Lines)),

		"issuer_name":    pay.CompanyName,
		"issuer_tagline": pay.CompanyTagline,
		"contact_person": pay.ContactPerson,

		"pay_bank_name":      pay.BankName,
		"pay_account_name":   pay.AccountName,
		"pay_account_number": pay.AccountNumber,
		"pay_swift":          pay.SWIFT,
		"pay_instructions":   pay.Instructions,
	}
	if words, ok := mailer.AmountWords(inv.Total); ok {
		d["total_words"] = words
	}
	if inv.TaxRateBP > 0 {
		d["tax_rate"] = FormatBP(inv.TaxRateBP)
	}
	// subtotal stays the gross sum. The discount keys are set only when there
	// is a discount, so an invoice without one renders exactly as before.
	if inv.DiscountTotal > 0 {
		d["discount_total"] = FormatMNT(inv.DiscountTotal)
	}
	for i, l := range inv.Lines {
		n := strconv.Itoa(i + 1)
		d["line_"+n+"_label"] = l.Label
		d["line_"+n+"_amount"] = FormatMNT(l.Amount)
		if l.Discount > 0 {
			d["line_"+n+"_discount"] = FormatMNT(l.Discount)
			d["line_"+n+"_net"] = FormatMNT(l.Net())
			if l.DiscountBP > 0 {
				d["line_"+n+"_discount_pct"] = FormatBP(l.DiscountBP)
			}
			if l.DiscountReason != "" {
				d["line_"+n+"_discount_reason"] = l.DiscountReason
			}
		}
		d["line_"+n+"_kind"] = l.Kind
		// A period that is not a date (a domain with no expiry) is left out.
		if p, err := time.Parse(dateLayout, l.Period); err == nil {
			d["line_"+n+"_period"] = p.Format(displayDateLayout)
		}
	}
	return d
}

// InvoicePreview is the rendered invoice mail, nothing sent.
type InvoicePreview struct {
	Subject string `json:"subject"`
	Text    string `json:"text"`
	HTML    string `json:"html"`
}

// Preview renders the invoice mail exactly as Send would, from the invoice and
// the current payment settings (the stored snapshot wins where it has the
// field), in any status. It sends nothing and changes nothing. Like Send it is
// refused with 409 while the payment settings are not configured, since the
// template needs the issuer and bank details.
func (s *InvoiceService) Preview(ctx context.Context, tenantIDStr, invIDStr string) (*InvoicePreview, error) {
	tid, iid, err := parseInvoiceKey(tenantIDStr, invIDStr)
	if err != nil {
		return nil, err
	}
	inv, err := s.repo.Find(ctx, tid, iid)
	if err != nil {
		return nil, invoiceNotFoundOr(err)
	}
	t, err := s.requireTenant(ctx, tid)
	if err != nil {
		return nil, err
	}
	if s.settings == nil {
		return nil, errPaymentSettingsMissing()
	}
	ps, err := s.settings.Get(ctx)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	if ps == nil || !ps.Configured() {
		return nil, errPaymentSettingsMissing()
	}
	pay := inv.PaymentInstructions
	if pay == nil {
		pay = ps.Snapshot()
	}
	subject, text, htmlBody, err := mailer.Render(mailer.TemplateInvoice, s.mailData(t, inv, withIssuer(pay, ps)))
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return &InvoicePreview{Subject: subject, Text: text, HTML: htmlBody}, nil
}

// FormatMNT renders whole MNT with thousands separators: 1234567 -> "1,234,567".
func FormatMNT(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// FormatBP renders basis points as a percentage: 1000 -> "10%", 825 -> "8.25%".
func FormatBP(bp int) string {
	whole, frac := bp/100, bp%100
	if frac == 0 {
		return strconv.Itoa(whole) + "%"
	}
	return strings.TrimRight(fmt.Sprintf("%d.%02d", whole, frac), "0") + "%"
}

// ── Mark paid / void ─────────────────────────────────────────────────────

// MarkPaid records the payment of a sent invoice. For deployment and other
// fee lines it also advances last_invoiced_through to the period billed;
// domain lines settle the domain's client charge (see advanceFees); the
// subscription line advances nothing (the operator renews the subscription).
func (s *InvoiceService) MarkPaid(ctx context.Context, tenantIDStr, invIDStr string, in PaidInput) (*models.Invoice, error) {
	tid, iid, err := parseInvoiceKey(tenantIDStr, invIDStr)
	if err != nil {
		return nil, err
	}
	inv, err := s.repo.Find(ctx, tid, iid)
	if err != nil {
		return nil, invoiceNotFoundOr(err)
	}
	switch inv.Status {
	case models.InvoiceSent:
	case models.InvoiceDraft, models.InvoiceSending:
		return nil, apierr.Conflict("invoice has not been sent").In(apierr.DomainTenant)
	case models.InvoicePaid:
		// A previous call may have paid the invoice and then failed to advance
		// the fees; replaying that is harmless, so heal it before refusing.
		_ = s.advanceFees(ctx, tid, inv)
		return nil, apierr.Conflict("invoice is already paid or void").In(apierr.DomainTenant)
	default:
		return nil, apierr.Conflict("invoice is already paid or void").In(apierr.DomainTenant)
	}

	// Validated before anything changes, after the status checks so a draft
	// is still told it has not been sent.
	paidAt, pay, err := s.buildPayment(in, inv)
	if err != nil {
		return nil, err
	}

	// The status change goes first and is the conditional step that only one
	// caller wins (a concurrent Void loses). The fee advance after it is
	// idempotent and monotone, so it can safely be replayed.
	out, err := s.repo.Transition(ctx, tid, iid, []string{models.InvoiceSent},
		bson.M{"status": models.InvoicePaid, "paid_at": paidAt, "payment": pay})
	if err != nil {
		return nil, s.transitionErr(err, "invoice changed while it was being marked paid")
	}
	if err := s.advanceFees(ctx, tid, out); err != nil {
		return nil, apierr.Internal(err)
	}
	return out, nil
}

// buildPayment validates the optional transaction record and returns the
// paid date and the record to store. Errors are fixed strings naming only the
// field, never the submitted value.
func (s *InvoiceService) buildPayment(in PaidInput, inv *models.Invoice) (time.Time, *models.InvoicePayment, error) {
	now := s.now().UTC()
	paidAt := now
	if raw := strings.TrimSpace(in.PaidAt); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			t, err = time.Parse(dateLayout, raw)
		}
		if err != nil {
			return time.Time{}, nil, badPaid("paid date")
		}
		paidAt = t.UTC()
		if paidAt.After(now.Add(24*time.Hour)) || dayStart(paidAt).Before(dayStart(inv.IssuedAt.UTC())) {
			return time.Time{}, nil, badPaid("paid date")
		}
	}

	method := strings.TrimSpace(in.Method)
	switch method {
	case "":
		method = models.InvoicePayBankTransfer
	case models.InvoicePayBankTransfer, models.InvoicePayCash, models.InvoicePayCard, models.InvoicePayOther:
	default:
		return time.Time{}, nil, badPaid("payment method")
	}
	ref, ok := cleanText(in.Reference, 100, false)
	if !ok {
		return time.Time{}, nil, badPaid("reference")
	}
	payer, ok := cleanText(in.PayerName, 100, false)
	if !ok {
		return time.Time{}, nil, badPaid("payer name")
	}
	last4 := strings.TrimSpace(in.PayerAccountLast4)
	if last4 != "" {
		if len(last4) != 4 || strings.Trim(last4, "0123456789") != "" {
			return time.Time{}, nil, badPaid("payer account last4")
		}
	}
	note, ok := cleanText(in.Note, 500, true)
	if !ok {
		return time.Time{}, nil, badPaid("note")
	}
	received := inv.Total
	if in.AmountReceived != nil {
		if *in.AmountReceived != inv.Total {
			return time.Time{}, nil, apierr.BadRequest("amount received must equal the invoice total").In(apierr.DomainTenant)
		}
		received = *in.AmountReceived
	}
	return paidAt, &models.InvoicePayment{
		Method: method, Reference: ref, PayerName: payer, PayerAccountLast4: last4,
		AmountReceived: received, Note: note,
	}, nil
}

func badPaid(field string) error {
	return apierr.BadRequest("invalid " + field).In(apierr.DomainTenant)
}

// clientPayMethod maps the invoice payment method to the domain vocabulary.
func clientPayMethod(m string) string {
	if m == models.InvoicePayBankTransfer {
		return models.PayMethodBank
	}
	return m
}

// advanceFees moves last_invoiced_through of the recurring fee lines of a
// paid invoice up to the period billed.
//
// Each domain line also settles its domain's client charge: paid_at is now and
// paid_through is the expires_at date the line billed (the line's period; today
// + 365 days when the domain had no expiry), so paid_through covers the billed
// expiry. MarkClientPaid is monotone and idempotent, so a replay is harmless
// and an expiry renewed since the invoice is not marked paid.
func (s *InvoiceService) advanceFees(ctx context.Context, tid primitive.ObjectID, inv *models.Invoice) error {
	for _, l := range inv.Lines {
		if l.Kind == models.FeeKindDomain && l.DomainID != nil && s.domains != nil {
			now := s.now().UTC()
			through, err := time.Parse(dateLayout, l.Period)
			if err != nil {
				through = dayStart(now).AddDate(0, 0, 365)
			}
			var method, reference string
			if inv.Payment != nil {
				method, reference = clientPayMethod(inv.Payment.Method), inv.Payment.Reference
			}
			if err := s.domains.MarkClientPaid(ctx, tid, *l.DomainID, now, through, method, reference); err != nil {
				return err
			}
			continue
		}
		if l.FeeID == nil || (l.Kind != models.FeeKindDeployment && l.Kind != models.FeeKindOther) {
			continue
		}
		through, err := time.Parse(dateLayout, l.Period)
		if err != nil {
			continue
		}
		if err := s.fees.AdvanceInvoicedThrough(ctx, tid, *l.FeeID, through); err != nil {
			return err
		}
	}
	return nil
}

// Void cancels a draft or sent invoice, freeing its fee periods to be
// invoiced again. Paid and void invoices are terminal.
func (s *InvoiceService) Void(ctx context.Context, tenantIDStr, invIDStr string) (*models.Invoice, error) {
	tid, iid, err := parseInvoiceKey(tenantIDStr, invIDStr)
	if err != nil {
		return nil, err
	}
	inv, err := s.repo.Find(ctx, tid, iid)
	if err != nil {
		return nil, invoiceNotFoundOr(err)
	}
	if inv.Status == models.InvoiceSending {
		return nil, apierr.Conflict("invoice is being sent").In(apierr.DomainTenant)
	}
	if inv.Status != models.InvoiceDraft && inv.Status != models.InvoiceSent {
		return nil, apierr.Conflict("invoice is already paid or void").In(apierr.DomainTenant)
	}
	// period_keys is nulled so the unique index frees the periods.
	out, err := s.repo.Transition(ctx, tid, iid, []string{models.InvoiceDraft, models.InvoiceSent},
		bson.M{"status": models.InvoiceVoid, "period_keys": nil})
	if err != nil {
		return nil, s.transitionErr(err, "invoice changed while it was being voided")
	}
	return out, nil
}

// ── Edit ─────────────────────────────────────────────────────────────────

// InvoiceEditInput is what a client may change on a draft invoice: the due
// date and the tax rate. Both are optional but at least one is required. DueAt
// is raw so an explicit null can be refused (a due date is set, never
// cleared). Lines, number, issue date, tenant and currency cannot be edited.
type InvoiceEditInput struct {
	DueAt     json.RawMessage `json:"due_at"`
	TaxRateBP *int            `json:"tax_rate_bp"`
	// Discounts sets or clears line discounts, keyed by line key (the line's
	// selection id, see models.InvoiceLineKey). An empty value or a zero
	// amount/percent clears the line's discount.
	Discounts map[string]LineDiscountInput `json:"discounts"`
}

// Edit changes the due date and/or tax rate of a draft invoice. A new tax rate
// recomputes tax and total on the server from the stored lines; the lines and
// period keys are never touched. The write is one conditional update on the
// draft status, so a concurrent Send, Void or MarkPaid wins cleanly and the
// edit answers 409 (a claimed, sending invoice does not match either).
func (s *InvoiceService) Edit(ctx context.Context, tenantIDStr, invIDStr string, in InvoiceEditInput) (*models.Invoice, error) {
	tid, iid, err := parseInvoiceKey(tenantIDStr, invIDStr)
	if err != nil {
		return nil, err
	}
	if len(in.DueAt) == 0 && in.TaxRateBP == nil && len(in.Discounts) == 0 {
		return nil, apierr.ValidationFailed("nothing to change").In(apierr.DomainTenant)
	}
	inv, err := s.repo.Find(ctx, tid, iid)
	if err != nil {
		return nil, invoiceNotFoundOr(err)
	}
	if inv.Status != models.InvoiceDraft {
		return nil, errInvoiceNotDraft()
	}

	set := bson.M{}
	if len(in.DueAt) > 0 {
		var due time.Time
		if string(in.DueAt) == "null" || json.Unmarshal(in.DueAt, &due) != nil {
			return nil, invalid("due date")
		}
		due = due.UTC()
		if due.Year() < 2000 || due.Year() > 2200 || due.Before(dayStart(inv.IssuedAt.UTC())) {
			return nil, invalid("due date")
		}
		set["due_at"] = due
	}
	bp := inv.TaxRateBP
	if in.TaxRateBP != nil {
		bp = *in.TaxRateBP
		if bp < 0 || bp > 10000 {
			return nil, invalid("tax rate")
		}
	}
	if in.TaxRateBP != nil || len(in.Discounts) > 0 {
		// Work on a copy: the stored lines are only replaced by the write below.
		lines := append([]models.InvoiceLine(nil), inv.Lines...)
		for i := range lines {
			lines[i].LineKey = models.InvoiceLineKey(lines[i])
		}
		if err := applyDiscounts(lines, in.Discounts, true); err != nil {
			return nil, err
		}
		// Lines and every total are written together, so a concurrent draft
		// edit can never leave totals that disagree with the lines. Period
		// keys are not part of the write.
		sub, disc, tax, total := invoiceTotals(lines, bp)
		set["lines"], set["subtotal"], set["discount_total"] = lines, sub, disc
		set["tax_rate_bp"], set["tax_amount"], set["total"] = bp, tax, total
	}

	out, err := s.repo.Transition(ctx, tid, iid, []string{models.InvoiceDraft}, set)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, errInvoiceNotDraft()
		}
		return nil, apierr.Internal(err)
	}
	return out, nil
}

// Delete removes a draft invoice and, with it, its period keys, so its lines
// can be invoiced again. The number counter is not rolled back (gaps are
// accepted). Any other status, a claimed (sending) invoice included, is a 409;
// an invoice of another tenant is a 404. The removal is one conditional delete
// on the draft status, so a concurrent Send that claims the invoice wins.
func (s *InvoiceService) Delete(ctx context.Context, tenantIDStr, invIDStr string) error {
	tid, iid, err := parseInvoiceKey(tenantIDStr, invIDStr)
	if err != nil {
		return err
	}
	inv, err := s.repo.Find(ctx, tid, iid)
	if err != nil {
		return invoiceNotFoundOr(err)
	}
	if inv.Status != models.InvoiceDraft {
		return errInvoiceNotDeletable()
	}
	if err := s.repo.DeleteDraft(ctx, tid, iid); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return errInvoiceNotDeletable()
		}
		return apierr.Internal(err)
	}
	return nil
}

func errInvoiceNotDeletable() error {
	return apierr.Conflict("only a draft invoice can be deleted").In(apierr.DomainTenant)
}

func errInvoiceNotDraft() error {
	return apierr.Conflict("only a draft invoice can be edited").In(apierr.DomainTenant)
}
