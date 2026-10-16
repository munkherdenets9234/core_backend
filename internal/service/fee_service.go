package service

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// Limits and constants of the fee ledger.
const (
	// DueLeadDays is how far ahead of its due date a fee counts as due.
	DueLeadDays = 30

	MaxFeesPerTenant = 100
	maxFeeLabelLen   = 200

	// MaxFeeAmount bounds one amount (whole MNT). With at most MaxInvoiceLines
	// lines and a tax rate of at most 10000 bp, every sum and product on an
	// invoice stays far below the int64 limit.
	MaxFeeAmount int64 = 1_000_000_000_000

	// SubscriptionLineID is the id the derived subscription line carries in
	// costing and in an invoice request. It is not a fee id.
	SubscriptionLineID = "subscription"

	// DomainLinePrefix starts the id of a derived domain line ("domain:<id>").
	// Such a line stands for a domain whose client charge is set but that no
	// fee row bills; it is not a fee id.
	DomainLinePrefix = "domain:"

	// noExpiryPeriod is the period key of an unpaid domain line whose domain
	// has no expires_at, so it can still be invoiced (once per live invoice).
	noExpiryPeriod = "no-expiry"
)

const dateLayout = "2006-01-02"

// feeStore is the slice of the fee repository the services need. Every method
// is scoped by tenant: a fee id of another tenant comes back as
// mongo.ErrNoDocuments.
type feeStore interface {
	ListByTenant(ctx context.Context, tenantID primitive.ObjectID) ([]*models.TenantFee, error)
	CountByTenant(ctx context.Context, tenantID primitive.ObjectID) (int64, error)
	Find(ctx context.Context, tenantID, id primitive.ObjectID) (*models.TenantFee, error)
	Insert(ctx context.Context, f *models.TenantFee) error
	Update(ctx context.Context, f *models.TenantFee) error
	Delete(ctx context.Context, tenantID, id primitive.ObjectID) error
	AdvanceInvoicedThrough(ctx context.Context, tenantID, id primitive.ObjectID, through time.Time) error
}

// feeDomains is the read access to a tenant's domain records a fee needs: the
// link target of a domain fee and the expiry that makes it due.
type feeDomains interface {
	ListByTenant(ctx context.Context, tenantID primitive.ObjectID) ([]*models.TenantDomain, error)
	Find(ctx context.Context, tenantID, id primitive.ObjectID) (*models.TenantDomain, error)
}

// feeSubscriptions reads a tenant's subscription with its plan. A tenant with
// none yields a 404 *apierr.APIError, as SubscriptionService.Get does.
type feeSubscriptions interface {
	Get(ctx context.Context, tenantID primitive.ObjectID) (*models.Subscription, error)
}

// FeeInput is the client-editable part of a fee. Identity, tenant, currency
// and timestamps are never taken from the client.
type FeeInput struct {
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Amount   int64  `json:"amount"`
	Interval string `json:"interval"`
	DomainID string `json:"domain_id"`
	// Active defaults to true when omitted on create; on update, omitted keeps
	// the stored value.
	Active *bool `json:"active"`
	// LastInvoicedThrough, when omitted on update, keeps the stored value.
	LastInvoicedThrough *time.Time `json:"last_invoiced_through"`
}

// liveInvoices lists the tenant's non-void invoices, so Costing can say which
// due lines are already on one. Scoped by tenant.
type liveInvoices interface {
	ListLive(ctx context.Context, tenantID primitive.ObjectID) ([]*models.Invoice, error)
}

// FeeService manages the operator's fee ledger of a tenant and derives what
// is due. It is bookkeeping: nothing here touches entitlement or routing.
type FeeService struct {
	repo     feeStore
	domains  feeDomains
	subs     feeSubscriptions
	tenants  tenantFinder
	invoices liveInvoices // optional; nil leaves invoiced_on empty
	now      func() time.Time
}

// SetInvoiceSource lets Costing report which lines are on a live invoice.
func (s *FeeService) SetInvoiceSource(src liveInvoices) { s.invoices = src }

// invoicedOn maps the period key of every line on a non-void invoice of the
// tenant (paid ones included: they hold the period too) to that invoice's
// number. Keys embed the tenant id, and the source is tenant-scoped.
func (s *FeeService) invoicedOn(ctx context.Context, tid primitive.ObjectID) (map[string]string, error) {
	out := map[string]string{}
	if s.invoices == nil {
		return out, nil
	}
	list, err := s.invoices.ListLive(ctx, tid)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	for _, inv := range list {
		for _, l := range inv.Lines {
			out[periodKey(tid, l.Kind, l.FeeID, l.DomainID, l.Period)] = inv.Number
		}
	}
	return out, nil
}

// onInvoice is the number of the live invoice holding b, or "".
func onInvoice(held map[string]string, tid primitive.ObjectID, b Billable) string {
	if b.Period() == "" {
		return ""
	}
	var feeID *primitive.ObjectID
	if b.Fee != nil {
		id := b.Fee.ID
		feeID = &id
	}
	return held[periodKey(tid, b.Kind, feeID, b.DomainID, b.Period())]
}

func NewFeeService(repo feeStore, domains feeDomains, subs feeSubscriptions, tenants tenantFinder) *FeeService {
	return &FeeService{repo: repo, domains: domains, subs: subs, tenants: tenants, now: time.Now}
}

func parseFeeID(s string) (primitive.ObjectID, error) {
	id, err := primitive.ObjectIDFromHex(s)
	if err != nil {
		return primitive.NilObjectID, apierr.BadRequest("invalid fee id").In(apierr.DomainTenant)
	}
	return id, nil
}

func feeNotFoundOr(err error) error {
	if errors.Is(err, mongo.ErrNoDocuments) {
		return apierr.NotFound("fee").In(apierr.DomainTenant)
	}
	return apierr.Internal(err)
}

func (s *FeeService) requireTenant(ctx context.Context, id primitive.ObjectID) error {
	if _, err := s.tenants.FindByID(ctx, id); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return apierr.NotFound("tenant").In(apierr.DomainTenant)
		}
		return apierr.Internal(err)
	}
	return nil
}

func (s *FeeService) List(ctx context.Context, tenantIDStr string) ([]*models.TenantFee, error) {
	tid, err := parseTenantID(tenantIDStr)
	if err != nil {
		return nil, err
	}
	if err := s.requireTenant(ctx, tid); err != nil {
		return nil, err
	}
	out, err := s.repo.ListByTenant(ctx, tid)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return out, nil
}

func (s *FeeService) Create(ctx context.Context, tenantIDStr string, in FeeInput) (*models.TenantFee, error) {
	tid, err := parseTenantID(tenantIDStr)
	if err != nil {
		return nil, err
	}
	f, err := buildFee(in)
	if err != nil {
		return nil, err
	}
	if err := s.requireTenant(ctx, tid); err != nil {
		return nil, err
	}
	if err := s.checkDomain(ctx, tid, f); err != nil {
		return nil, err
	}
	n, err := s.repo.CountByTenant(ctx, tid)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	if n >= MaxFeesPerTenant {
		return nil, apierr.ValidationFailed("too many fees for this tenant").In(apierr.DomainTenant)
	}
	f.TenantID = tid
	if err := s.repo.Insert(ctx, f); err != nil {
		return nil, feeNotFoundOr(err)
	}
	return f, nil
}

func (s *FeeService) Update(ctx context.Context, tenantIDStr, feeIDStr string, in FeeInput) (*models.TenantFee, error) {
	tid, err := parseTenantID(tenantIDStr)
	if err != nil {
		return nil, err
	}
	fid, err := parseFeeID(feeIDStr)
	if err != nil {
		return nil, err
	}
	f, err := buildFee(in)
	if err != nil {
		return nil, err
	}
	existing, err := s.repo.Find(ctx, tid, fid)
	if err != nil {
		return nil, feeNotFoundOr(err)
	}
	if err := s.checkDomain(ctx, tid, f); err != nil {
		return nil, err
	}
	f.ID, f.TenantID, f.CreatedAt = fid, tid, existing.CreatedAt
	if f.LastInvoicedThrough == nil {
		f.LastInvoicedThrough = existing.LastInvoicedThrough
	}
	// An omitted active keeps the stored value; only an explicit one changes it.
	if in.Active == nil {
		f.Active = existing.Active
	}
	if err := s.repo.Update(ctx, f); err != nil {
		return nil, feeNotFoundOr(err)
	}
	return f, nil
}

func (s *FeeService) Delete(ctx context.Context, tenantIDStr, feeIDStr string) error {
	tid, err := parseTenantID(tenantIDStr)
	if err != nil {
		return err
	}
	fid, err := parseFeeID(feeIDStr)
	if err != nil {
		return err
	}
	if err := s.repo.Delete(ctx, tid, fid); err != nil {
		return feeNotFoundOr(err)
	}
	return nil
}

// checkDomain requires a domain fee to link a domain of the same tenant. A
// domain id of another tenant is indistinguishable from a missing one.
func (s *FeeService) checkDomain(ctx context.Context, tid primitive.ObjectID, f *models.TenantFee) error {
	if f.DomainID == nil {
		return nil
	}
	if _, err := s.domains.Find(ctx, tid, *f.DomainID); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return invalid("domain id")
		}
		return apierr.Internal(err)
	}
	return nil
}

// buildFee validates in and returns the record to store. Errors are fixed
// strings naming only the field, never the submitted value.
func buildFee(in FeeInput) (*models.TenantFee, error) {
	f := &models.TenantFee{Currency: models.CurrencyMNT, Active: true}

	switch k := strings.TrimSpace(in.Kind); k {
	case models.FeeKindDomain, models.FeeKindDeployment, models.FeeKindOther:
		f.Kind = k
	default:
		// "subscription" is derived from the plan, never stored.
		return nil, invalid("fee kind")
	}
	// Required-ness is judged on the sanitised value.
	label, ok := cleanText(in.Label, maxFeeLabelLen, false)
	if !ok || label == "" {
		return nil, invalid("fee label")
	}
	f.Label = label
	if in.Amount < 0 || in.Amount > MaxFeeAmount {
		return nil, invalid("fee amount")
	}
	f.Amount = in.Amount
	switch iv := strings.TrimSpace(in.Interval); iv {
	case models.FeeIntervalMonthly, models.FeeIntervalYearly, models.FeeIntervalOneTime:
		f.Interval = iv
	default:
		return nil, invalid("fee interval")
	}

	did := strings.TrimSpace(in.DomainID)
	switch {
	case f.Kind == models.FeeKindDomain:
		id, err := primitive.ObjectIDFromHex(did)
		if err != nil {
			return nil, invalid("domain id")
		}
		f.DomainID = &id
	case did != "":
		return nil, invalid("domain id")
	}

	if in.Active != nil {
		f.Active = *in.Active
	}
	if in.LastInvoicedThrough != nil {
		t := in.LastInvoicedThrough.UTC()
		f.LastInvoicedThrough = &t
	}
	return f, nil
}

// ── Due logic ────────────────────────────────────────────────────────────

func dayStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// addMonths adds n months to t's month and lands on anchor day, clamped to the
// last day of the target month (31 Jan + 1 month is 28/29 Feb, not 3 Mar).
// Passing the original day as anchor, not t.Day(), stops the date drifting
// down: Jan 31 -> Feb 28 -> Mar 31, not Mar 28. An anchor below 1 means t.Day().
func addMonths(t time.Time, n, anchor int) time.Time {
	y, m, d := t.Date()
	if anchor > 0 {
		d = anchor
	}
	first := time.Date(y, m+time.Month(n), 1, 0, 0, 0, 0, t.Location())
	if last := first.AddDate(0, 1, -1).Day(); d > last {
		d = last
	}
	return time.Date(first.Year(), first.Month(), d, 0, 0, 0, 0, t.Location())
}

// billingAnchorDay is the day of month a recurring fee bills on. It is not
// stored; it is the day the fee was created (its first period falls due then).
// 0 (unknown) makes addMonths fall back to the day of the date it is given.
func billingAnchorDay(f *models.TenantFee) int {
	if f.CreatedAt.IsZero() {
		return 0
	}
	return f.CreatedAt.UTC().Day()
}

// FeeNextDue returns when f next falls due, or nil when nothing is owed.
//
// A domain fee falls due when its linked domain expires (domainExpiry; nil
// when the domain is gone or has no expiry). Any other fee falls due one
// interval after last_invoiced_through, or at creation if never invoiced; a
// one-time fee that has been invoiced is settled. Dates are UTC days.
func FeeNextDue(f *models.TenantFee, domainExpiry *time.Time) *time.Time {
	if f.Kind == models.FeeKindDomain {
		if domainExpiry == nil {
			return nil
		}
		t := domainExpiry.UTC()
		return &t
	}
	if f.LastInvoicedThrough == nil {
		t := dayStart(f.CreatedAt)
		return &t
	}
	base := dayStart(*f.LastInvoicedThrough)
	var t time.Time
	switch f.Interval {
	case models.FeeIntervalMonthly:
		t = addMonths(base, 1, billingAnchorDay(f))
	case models.FeeIntervalYearly:
		t = addMonths(base, 12, billingAnchorDay(f))
	default:
		return nil
	}
	return &t
}

// IsDue reports whether next falls within the lead window from now. A date
// already past is due.
func IsDue(next *time.Time, now time.Time) bool {
	return next != nil && !next.After(now.Add(DueLeadDays*24*time.Hour))
}

// PriceToMNT converts a plan price to whole MNT, rounding once (half away
// from zero). A NaN or negative price is 0 and an absurd one is capped, so a
// bad plan row cannot overflow an invoice.
func PriceToMNT(price float64) int64 {
	if math.IsNaN(price) || price <= 0 {
		return 0
	}
	r := math.Round(price)
	if r > float64(MaxFeeAmount) {
		return MaxFeeAmount
	}
	return int64(r)
}

// Billable is one line the tenant can be invoiced for: a stored fee, or the
// subscription line derived from the plan.
type Billable struct {
	// ID is the fee id in hex, or SubscriptionLineID.
	ID         string
	Fee        *models.TenantFee // nil for the subscription line
	Kind       string
	Label      string
	Amount     int64
	NextDueAt  *time.Time
	Active     bool
	Due        bool
	DomainName string
	PlanName   string
	// Status is the subscription status, or for a domain line the derived
	// client charge status (none|paid|unpaid).
	Status string
	// DomainID is set on every domain line, fee-linked or derived.
	DomainID *primitive.ObjectID
	// period overrides the due-date period (see noExpiryPeriod).
	period string
}

// Period identifies the billing period of the line (its due date) for the
// double-invoice check; empty when nothing is owed.
func (b Billable) Period() string {
	if b.period != "" {
		return b.period
	}
	if b.NextDueAt == nil {
		return ""
	}
	return b.NextDueAt.UTC().Format(dateLayout)
}

// Billables returns the tenant's stored fees and, when the tenant has a
// subscription whose plan still exists, the derived subscription line. The
// caller has already checked the tenant exists.
//
// Domain payment (see models.PaymentStatus): a domain fee linked to a domain
// whose client_charge.amount is > 0 takes its amount from that charge at read
// time (the invoice snapshots it), is DUE NOW while the charge is unpaid
// (ignoring the lead window, even with no expires_at), and is not due while
// the charge is paid (paid_through covers expires_at) until a renewal moves
// expires_at forward and the charge turns unpaid again. A domain with no
// client_charge amount keeps the fee's own amount and the expiry-based due
// logic. A domain with a client charge and no fee row at all adds a derived
// line with id "domain:<id>", appended after the stored fees.
func (s *FeeService) Billables(ctx context.Context, tid primitive.ObjectID) ([]Billable, *Billable, error) {
	out, sub, _, err := s.billables(ctx, tid)
	return out, sub, err
}

func (s *FeeService) billables(ctx context.Context, tid primitive.ObjectID) ([]Billable, *Billable, []*models.TenantDomain, error) {
	now := s.now()
	fees, err := s.repo.ListByTenant(ctx, tid)
	if err != nil {
		return nil, nil, nil, apierr.Internal(err)
	}
	doms, err := s.domains.ListByTenant(ctx, tid)
	if err != nil {
		return nil, nil, nil, apierr.Internal(err)
	}
	byID := make(map[primitive.ObjectID]*models.TenantDomain, len(doms))
	for _, d := range doms {
		byID[d.ID] = d
	}

	out := make([]Billable, 0, len(fees))
	linked := map[primitive.ObjectID]bool{}
	for _, f := range fees {
		b := Billable{
			ID: f.ID.Hex(), Fee: f, Kind: f.Kind, Label: f.Label, Amount: f.Amount, Active: f.Active,
		}
		var expiry *time.Time
		var dom *models.TenantDomain
		if f.DomainID != nil {
			if d := byID[*f.DomainID]; d != nil {
				dom, expiry = d, d.ExpiresAt
				b.DomainName = d.Name
				id := d.ID
				b.DomainID = &id
			}
			linked[*f.DomainID] = true
		}
		b.NextDueAt = FeeNextDue(f, expiry)
		b.Due = f.Active && IsDue(b.NextDueAt, now)
		if f.Kind == models.FeeKindDomain && dom != nil {
			b.Status = dom.ClientStatus()
			if dom.Payment.ClientCharge.Amount > 0 {
				applyClientCharge(&b, dom, f.Active)
			}
		}
		out = append(out, b)
	}
	// A domain with a client charge that no fee row bills gets a derived line.
	for _, d := range doms {
		if d.Payment.ClientCharge.Amount <= 0 || linked[d.ID] {
			continue
		}
		id := d.ID
		b := Billable{
			ID: DomainLinePrefix + d.ID.Hex(), Kind: models.FeeKindDomain, Label: "Domain: " + d.Name,
			NextDueAt: d.ExpiresAt, Active: true, DomainName: d.Name, DomainID: &id,
		}
		applyClientCharge(&b, d, true)
		out = append(out, b)
	}

	var sub *Billable
	sb, err := s.subs.Get(ctx, tid)
	switch {
	case err == nil && sb != nil && sb.Plan != nil:
		end := sb.CurrentPeriodEnd.UTC()
		billing := sb.Status != models.SubscriptionCanceled
		sub = &Billable{
			ID:        SubscriptionLineID,
			Kind:      models.FeeKindSubscription,
			Label:     "Subscription: " + sb.Plan.Name,
			Amount:    PriceToMNT(sb.Plan.Price),
			NextDueAt: &end,
			Active:    billing,
			Due:       billing && IsDue(&end, now),
			PlanName:  sb.Plan.Name,
			Status:    string(sb.Status),
		}
	case err == nil:
		// No plan to price it from.
	default:
		var ae *apierr.APIError
		if !(errors.As(err, &ae) && ae.HTTPStatus == http.StatusNotFound) {
			return nil, nil, nil, err
		}
	}
	return out, sub, doms, nil
}

// applyClientCharge takes a domain line's amount and due state from the
// domain's client charge (amount > 0). Unpaid: due now whatever the lead
// window or expiry, and still invoiceable when the domain has no expires_at.
// Paid: not due until the expiry moves forward past paid_through.
func applyClientCharge(b *Billable, d *models.TenantDomain, active bool) {
	b.Amount = d.Payment.ClientCharge.Amount
	b.Status = d.ClientStatus()
	switch b.Status {
	case models.PaymentUnpaid:
		b.Due = active
		if b.NextDueAt == nil {
			b.period = noExpiryPeriod
		}
	case models.PaymentPaid:
		b.Due = false
	}
}

// FeeView is a stored fee with what is derived from it.
type FeeView struct {
	models.TenantFee
	NextDueAt  *time.Time `json:"next_due_at"`
	Due        bool       `json:"due"`
	DomainName string     `json:"domain_name,omitempty"`
	// PaymentStatus is the client charge status (none|paid|unpaid) of the
	// linked domain; empty for a fee that is not a domain fee. For a domain
	// with a client charge, Amount is that charge, not the stored fee amount.
	PaymentStatus string `json:"payment_status,omitempty"`
	// InvoicedOn is the number of the draft, sent or paid invoice that already
	// bills this line's current period; empty when none does.
	InvoicedOn string `json:"invoiced_on,omitempty"`
}

// DomainLine is a derived domain line: a domain with a client charge that no
// fee row bills. Its id ("domain:<id>") is accepted by invoice creation.
type DomainLine struct {
	ID            string     `json:"id"`
	DomainID      string     `json:"domain_id"`
	DomainName    string     `json:"domain_name"`
	Label         string     `json:"label"`
	Amount        int64      `json:"amount"`
	Currency      string     `json:"currency"`
	PaymentStatus string     `json:"payment_status"`
	NextDueAt     *time.Time `json:"next_due_at"`
	Due           bool       `json:"due"`
	InvoicedOn    string     `json:"invoiced_on,omitempty"`
}

// PayableItem is a domain whose registrar cost is unpaid. Margin is the
// domain's client charge minus registrar cost when both are set.
type PayableItem struct {
	DomainID   string     `json:"domain_id"`
	DomainName string     `json:"domain_name"`
	Amount     int64      `json:"amount"`
	ExpiresAt  *time.Time `json:"expires_at"`
	Payer      string     `json:"payer"`
	Margin     *int64     `json:"margin"`
}

// DomainMargin is client charge minus registrar cost of one domain, for each
// domain where both are > 0.
type DomainMargin struct {
	DomainID   string `json:"domain_id"`
	DomainName string `json:"domain_name"`
	Margin     int64  `json:"margin"`
}

// Payables is the money side of the domains that is not on an invoice:
// unpaid registrar costs (never put on a tenant invoice), per-domain margins
// and the two totals.
type Payables struct {
	Items   []PayableItem  `json:"items"`
	Margins []DomainMargin `json:"margins"`
	// ReceivableTotal is the sum of the unpaid client charges of all domains.
	ReceivableTotal int64 `json:"receivable_total"`
	// PayableTotal is the sum of the unpaid registrar costs of all domains
	// (whoever the payer is; see each item's payer).
	PayableTotal int64 `json:"payable_total"`
}

// SubscriptionLine is the derived subscription charge. It is not stored.
type SubscriptionLine struct {
	ID        string     `json:"id"`
	Label     string     `json:"label"`
	Amount    int64      `json:"amount"`
	Currency  string     `json:"currency"`
	PlanName  string     `json:"plan_name"`
	Status    string     `json:"status"`
	NextDueAt *time.Time `json:"next_due_at"`
	Due       bool       `json:"due"`
	// InvoicedOn: see FeeView.InvoicedOn.
	InvoicedOn string `json:"invoiced_on,omitempty"`
}

// Costing is what the console's Costing page shows for a tenant.
type Costing struct {
	Currency     string            `json:"currency"`
	LeadDays     int               `json:"lead_days"`
	Fees         []FeeView         `json:"fees"`
	Subscription *SubscriptionLine `json:"subscription"`
	// DomainLines are the derived domain lines (see DomainLine).
	DomainLines []DomainLine `json:"domain_lines"`
	Payables    Payables     `json:"payables"`
	// DueTotal is the sum of every due line, subscription included.
	DueTotal int64 `json:"due_total"`
}

func (s *FeeService) Costing(ctx context.Context, tenantIDStr string) (*Costing, error) {
	tid, err := parseTenantID(tenantIDStr)
	if err != nil {
		return nil, err
	}
	if err := s.requireTenant(ctx, tid); err != nil {
		return nil, err
	}
	bills, sub, doms, err := s.billables(ctx, tid)
	if err != nil {
		return nil, err
	}
	held, err := s.invoicedOn(ctx, tid)
	if err != nil {
		return nil, err
	}
	c := &Costing{
		Currency: models.CurrencyMNT, LeadDays: DueLeadDays, Fees: make([]FeeView, 0, len(bills)), DomainLines: []DomainLine{},
		Payables: Payables{Items: []PayableItem{}, Margins: []DomainMargin{}},
	}
	for _, b := range bills {
		if b.Fee == nil {
			c.DomainLines = append(c.DomainLines, DomainLine{
				ID: b.ID, DomainID: b.DomainID.Hex(), DomainName: b.DomainName, Label: b.Label, Amount: b.Amount,
				Currency: models.CurrencyMNT, PaymentStatus: b.Status, NextDueAt: b.NextDueAt, Due: b.Due,
				InvoicedOn: onInvoice(held, tid, b),
			})
		} else {
			fv := FeeView{TenantFee: *b.Fee, NextDueAt: b.NextDueAt, Due: b.Due, DomainName: b.DomainName, InvoicedOn: onInvoice(held, tid, b)}
			fv.Amount = b.Amount
			if b.DomainID != nil {
				fv.PaymentStatus = b.Status
			}
			c.Fees = append(c.Fees, fv)
		}
		if b.Due {
			c.DueTotal += b.Amount
		}
	}
	for _, d := range doms {
		if d.ClientStatus() == models.PaymentUnpaid {
			c.Payables.ReceivableTotal += d.Payment.ClientCharge.Amount
		}
		margin := d.Margin()
		if margin != nil {
			c.Payables.Margins = append(c.Payables.Margins, DomainMargin{DomainID: d.ID.Hex(), DomainName: d.Name, Margin: *margin})
		}
		if d.RegistrarStatus() == models.PaymentUnpaid {
			rc := d.Payment.RegistrarCost
			c.Payables.PayableTotal += rc.Amount
			c.Payables.Items = append(c.Payables.Items, PayableItem{
				DomainID: d.ID.Hex(), DomainName: d.Name, Amount: rc.Amount, ExpiresAt: d.ExpiresAt, Payer: rc.Payer, Margin: margin,
			})
		}
	}
	if sub != nil {
		c.Subscription = &SubscriptionLine{
			ID: sub.ID, Label: sub.Label, Amount: sub.Amount, Currency: models.CurrencyMNT,
			PlanName: sub.PlanName, Status: sub.Status, NextDueAt: sub.NextDueAt, Due: sub.Due,
			InvoicedOn: onInvoice(held, tid, *sub),
		}
		if sub.Due {
			c.DueTotal += sub.Amount
		}
	}
	return c, nil
}
