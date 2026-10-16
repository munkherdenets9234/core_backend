package models

import (
	"encoding/json"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// PaymentSettingsID is the _id of the single payment settings document in the
// platform_settings collection.
const PaymentSettingsID = "payment"

// Invoice payment methods (the method of the transaction record).
const (
	InvoicePayBankTransfer = "bank_transfer"
	InvoicePayCash         = "cash"
	InvoicePayCard         = "card"
	InvoicePayOther        = "other"
)

// PaymentSettings are the platform's own bank details, printed on every
// invoice it sends. A singleton, platform wide. The currency is always MNT and
// is not stored. Never holds a credential: account identifiers only.
type PaymentSettings struct {
	ID string `bson:"_id" json:"-"`
	// CompanyName, CompanyTagline and ContactPerson identify the issuer on
	// the invoice (header, "seller" block). The name is required.
	CompanyName    string              `bson:"company_name" json:"company_name"`
	CompanyTagline string              `bson:"company_tagline,omitempty" json:"company_tagline"`
	ContactPerson  string              `bson:"contact_person,omitempty" json:"contact_person"`
	BankName       string              `bson:"bank_name" json:"bank_name"`
	AccountName    string              `bson:"account_name" json:"account_name"`
	AccountNumber  string              `bson:"account_number" json:"account_number"`
	SWIFT          string              `bson:"swift,omitempty" json:"swift"`
	Instructions   string              `bson:"instructions,omitempty" json:"instructions"`
	UpdatedAt      *time.Time          `bson:"updated_at,omitempty" json:"updated_at"`
	UserID         *primitive.ObjectID `bson:"user_id,omitempty" json:"user_id"`
}

// Configured reports whether the settings carry what an invoice needs: issuer
// company name, bank name, account name and account number.
func (p PaymentSettings) Configured() bool {
	return p.CompanyName != "" && p.BankName != "" && p.AccountName != "" && p.AccountNumber != ""
}

// Snapshot is the part of the settings copied onto an invoice when it is sent.
func (p PaymentSettings) Snapshot() *PaymentInstructions {
	return &PaymentInstructions{
		CompanyName: p.CompanyName, CompanyTagline: p.CompanyTagline, ContactPerson: p.ContactPerson,
		BankName: p.BankName, AccountName: p.AccountName, AccountNumber: p.AccountNumber,
		SWIFT: p.SWIFT, Instructions: p.Instructions,
	}
}

// MarshalJSON adds the derived configured flag.
func (p PaymentSettings) MarshalJSON() ([]byte, error) {
	type plain PaymentSettings
	return json.Marshal(struct {
		plain
		Configured bool `json:"configured"`
	}{plain(p), p.Configured()})
}

// PaymentInstructions is the snapshot of the payment settings stored on an
// invoice at send time, so a later edit never changes what a tenant received.
//
// The issuer fields are empty on invoices sent before they existed; the
// invoice service then reads them from the current settings.
type PaymentInstructions struct {
	CompanyName    string `bson:"company_name,omitempty" json:"company_name"`
	CompanyTagline string `bson:"company_tagline,omitempty" json:"company_tagline"`
	ContactPerson  string `bson:"contact_person,omitempty" json:"contact_person"`
	BankName       string `bson:"bank_name" json:"bank_name"`
	AccountName    string `bson:"account_name" json:"account_name"`
	AccountNumber  string `bson:"account_number" json:"account_number"`
	SWIFT          string `bson:"swift,omitempty" json:"swift"`
	Instructions   string `bson:"instructions,omitempty" json:"instructions"`
}

// InvoicePayment is the transaction record captured when an invoice is marked
// paid. PayerAccountLast4 is at most four digits: a full account number is
// never stored.
type InvoicePayment struct {
	Method            string `bson:"method" json:"method"`
	Reference         string `bson:"reference,omitempty" json:"reference"`
	PayerName         string `bson:"payer_name,omitempty" json:"payer_name"`
	PayerAccountLast4 string `bson:"payer_account_last4,omitempty" json:"payer_account_last4"`
	AmountReceived    int64  `bson:"amount_received" json:"amount_received"`
	Note              string `bson:"note,omitempty" json:"note"`
}
