package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// paymentSettingsStore is the slice of the repository the service needs.
type paymentSettingsStore interface {
	// Get returns mongo.ErrNoDocuments when nothing is saved.
	Get(ctx context.Context) (*models.PaymentSettings, error)
	Put(ctx context.Context, p *models.PaymentSettings) error
}

// PaymentSettingsInput is the whole settings document; a PUT replaces it.
type PaymentSettingsInput struct {
	CompanyName    string `json:"company_name"`
	CompanyTagline string `json:"company_tagline"`
	ContactPerson  string `json:"contact_person"`
	BankName       string `json:"bank_name"`
	AccountName    string `json:"account_name"`
	AccountNumber  string `json:"account_number"`
	SWIFT          string `json:"swift"`
	Instructions   string `json:"instructions"`
}

const (
	maxBankFieldLen     = 100
	maxCompanyTagline   = 150
	maxInstructionsLen  = 500
	minAccountNumberLen = 4
	maxAccountNumberLen = 34
)

// PaymentSettingsService reads and replaces the platform's bank details.
type PaymentSettingsService struct {
	repo paymentSettingsStore
	now  func() time.Time
}

func NewPaymentSettingsService(repo paymentSettingsStore) *PaymentSettingsService {
	return &PaymentSettingsService{repo: repo, now: time.Now}
}

// Get returns the settings, or an empty (not configured) value when none were
// saved yet.
func (s *PaymentSettingsService) Get(ctx context.Context) (*models.PaymentSettings, error) {
	p, err := s.repo.Get(ctx)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return &models.PaymentSettings{ID: models.PaymentSettingsID}, nil
		}
		return nil, apierr.Internal(err)
	}
	return p, nil
}

// Put validates in and replaces the settings. Errors are fixed strings naming
// only the field.
func (s *PaymentSettingsService) Put(ctx context.Context, in PaymentSettingsInput, actor *primitive.ObjectID) (*models.PaymentSettings, error) {
	company, ok := cleanText(in.CompanyName, maxBankFieldLen, false)
	if !ok || company == "" {
		return nil, invalid("company name")
	}
	tagline, ok := cleanText(in.CompanyTagline, maxCompanyTagline, false)
	if !ok {
		return nil, invalid("company tagline")
	}
	contact, ok := cleanText(in.ContactPerson, maxBankFieldLen, false)
	if !ok {
		return nil, invalid("contact person")
	}
	bank, ok := cleanText(in.BankName, maxBankFieldLen, false)
	if !ok || bank == "" {
		return nil, invalid("bank name")
	}
	name, ok := cleanText(in.AccountName, maxBankFieldLen, false)
	if !ok || name == "" {
		return nil, invalid("account name")
	}
	number, ok := cleanAccountNumber(in.AccountNumber)
	if !ok {
		return nil, invalid("account number")
	}
	swift, ok := cleanSWIFT(in.SWIFT)
	if !ok {
		return nil, invalid("swift code")
	}
	instr, ok := cleanText(in.Instructions, maxInstructionsLen, true)
	if !ok {
		return nil, invalid("instructions")
	}
	now := s.now().UTC()
	p := &models.PaymentSettings{
		ID: models.PaymentSettingsID, CompanyName: company, CompanyTagline: tagline, ContactPerson: contact, BankName: bank, AccountName: name, AccountNumber: number,
		SWIFT: swift, Instructions: instr, UpdatedAt: &now, UserID: actor,
	}
	if err := s.repo.Put(ctx, p); err != nil {
		return nil, apierr.Internal(err)
	}
	return p, nil
}

// cleanAccountNumber accepts 4 to 34 ASCII letters, digits, spaces and hyphens
// (so IBANs and local numbers fit).
func cleanAccountNumber(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len(s) < minAccountNumberLen || len(s) > maxAccountNumberLen {
		return "", false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == ' ', r == '-':
		default:
			return "", false
		}
	}
	return s, true
}

// cleanSWIFT accepts empty or 8 or 11 ASCII letters/digits, upper-cased.
func cleanSWIFT(s string) (string, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return "", true
	}
	if len(s) != 8 && len(s) != 11 {
		return "", false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'A' && r <= 'Z') {
			return "", false
		}
	}
	return s, true
}
