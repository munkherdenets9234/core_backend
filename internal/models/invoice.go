package models

import (
	"encoding/json"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Invoice statuses. paid and void are terminal.
const (
	InvoiceDraft = "draft"
	InvoiceSent  = "sent"
	InvoicePaid  = "paid"
	InvoiceVoid  = "void"
	// InvoiceSending is internal: an invoice claimed by a send in flight. It
	// is never shown to clients (see MarshalJSON, which reports the status the
	// invoice had before the claim) and blocks Void and MarkPaid, so a voided
	// invoice cannot be mailed.
	InvoiceSending = "sending"
)

// Invoice is a snapshot of what a tenant was billed. Every amount is computed
// on the server from the lines; none is accepted from a client.
type Invoice struct {
	ID       primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	TenantID primitive.ObjectID `bson:"tenant_id" json:"tenant_id"`

	// Number is INV-YYYY-MM-NNN, sequential per month (UTC at issue time),
	// unique. Older invoices carry INV-YYYY-NNNN.
	Number   string     `bson:"number" json:"number"`
	IssuedAt time.Time  `bson:"issued_at" json:"issued_at"`
	DueAt    *time.Time `bson:"due_at,omitempty" json:"due_at"`

	Lines []InvoiceLine `bson:"lines" json:"lines"`

	// Subtotal is the sum of the gross line amounts (before any discount).
	Subtotal int64 `bson:"subtotal" json:"subtotal"`
	// DiscountTotal is the sum of the line discounts; 0 on invoices without
	// discounts. Tax is computed on Subtotal - DiscountTotal and Total is that
	// base plus tax.
	DiscountTotal int64 `bson:"discount_total,omitempty" json:"discount_total"`
	// TaxRateBP is the tax rate in basis points (0..10000); 0 means none.
	TaxRateBP int    `bson:"tax_rate_bp" json:"tax_rate_bp"`
	TaxAmount int64  `bson:"tax_amount" json:"tax_amount"`
	Total     int64  `bson:"total" json:"total"`
	Currency  string `bson:"currency" json:"currency"`

	Status string     `bson:"status" json:"status"`
	SentAt *time.Time `bson:"sent_at,omitempty" json:"sent_at"`
	SentTo string     `bson:"sent_to,omitempty" json:"sent_to"`
	PaidAt *time.Time `bson:"paid_at,omitempty" json:"paid_at"`

	// PaymentInstructions is the platform's bank details as they were when the
	// invoice was first sent. Kept on resend, so later edits of the settings
	// never change what a tenant received.
	PaymentInstructions *PaymentInstructions `bson:"payment_instructions,omitempty" json:"payment_instructions"`
	// Payment is the transaction record captured when the invoice was marked
	// paid.
	Payment *InvoicePayment `bson:"payment,omitempty" json:"payment"`

	// SendPrev is the status to restore if a send fails; set only while the
	// status is InvoiceSending. Internal.
	SendPrev string `bson:"send_prev,omitempty" json:"-"`
	// PeriodKeys is one key per line (tenant, kind, fee, period). A unique
	// partial index over it makes "a fee period is on at most one live invoice"
	// race-safe. Void sets it to null so the periods are freed. Internal.
	PeriodKeys []string `bson:"period_keys,omitempty" json:"-"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
}

// MarshalJSON hides the internal sending status: clients only ever see
// draft, sent, paid or void.
func (i Invoice) MarshalJSON() ([]byte, error) {
	type plain Invoice
	p := plain(i)
	if p.Status == InvoiceSending {
		p.Status = p.SendPrev
		if p.Status == "" {
			p.Status = InvoiceDraft
		}
	}
	return json.Marshal(p)
}

// InvoiceLine is one charge as it was when the invoice was made. Kind and
// Period (the due date, YYYY-MM-DD) identify what was billed, so the same
// fee period is not invoiced twice.
type InvoiceLine struct {
	Label  string              `bson:"label" json:"label"`
	Amount int64               `bson:"amount" json:"amount"`
	FeeID  *primitive.ObjectID `bson:"fee_id,omitempty" json:"fee_id"`
	// DomainID is set on every domain line (stored fee or derived), so paying
	// the invoice can settle that domain's client charge.
	DomainID *primitive.ObjectID `bson:"domain_id,omitempty" json:"domain_id"`
	Kind     string              `bson:"kind" json:"kind"`
	Period   string              `bson:"period" json:"period"`
	// LineKey is the selection id the line was made from ("subscription",
	// "domain:<id>" or a fee id); it keys discounts on edit. Lines saved before
	// it existed have none (see InvoiceLineKey).
	LineKey string `bson:"line_key,omitempty" json:"line_key"`
	// Discount is the line's discount in whole MNT, 0..Amount, always computed
	// on the server. Amount stays the gross snapshot price; the net of the line
	// is Amount - Discount. Absent (0) on older invoices.
	Discount int64 `bson:"discount,omitempty" json:"discount"`
	// DiscountBP is the percent (basis points, 0..10000) the operator entered,
	// kept for display only; 0 when the discount was entered as an amount.
	DiscountBP int `bson:"discount_bp,omitempty" json:"discount_bp"`
	// DiscountReason is an optional note (at most 100 characters).
	DiscountReason string `bson:"discount_reason,omitempty" json:"discount_reason"`
}

// Net is the line amount after its discount.
func (l InvoiceLine) Net() int64 { return l.Amount - l.Discount }

// InvoiceLineKey is the selection id of a line: its stored LineKey, else the
// id it would have been created with (older lines carry none).
func InvoiceLineKey(l InvoiceLine) string {
	switch {
	case l.LineKey != "":
		return l.LineKey
	case l.FeeID != nil:
		return l.FeeID.Hex()
	case l.DomainID != nil:
		return "domain:" + l.DomainID.Hex()
	case l.Kind == FeeKindSubscription:
		return "subscription"
	}
	return ""
}
