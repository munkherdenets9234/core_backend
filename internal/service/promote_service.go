package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/mailer"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.uber.org/zap"
)

// promoteValueMax caps every value handed to the notification template.
// A tenant or admin name has no business being longer; the cap is what keeps
// a pasted essay from becoming a mail subject.
const promoteValueMax = 256

// promotedByFallback stands in for an actor with no usable name. The template
// refuses a blank value, and a missing name must not cost the admins the mail.
const promotedByFallback = "a platform admin"

// The reads and writes promoting needs, as narrow interfaces for the same
// reason as ExpiryNotifier: the rules here (one link per quote, the key never
// lost, mail never blocking) have to be testable without a database or SMTP.
type (
	promoteQuotes interface {
		FindByID(ctx context.Context, id primitive.ObjectID) (*models.Quote, error)
		LinkPromoted(ctx context.Context, id, tenantID primitive.ObjectID, userID *primitive.ObjectID) (bool, error)
	}
	promoteTenants interface {
		Create(ctx context.Context, t *models.Tenant) (*models.Tenant, string, error)
	}
	promoteRecipients interface {
		ListActive(ctx context.Context) ([]*models.PlatformUser, error)
	}
)

// PromoteInput is the tenant to create, in the same shape as the tenant
// creation endpoint's body.
type PromoteInput struct {
	Name, Slug, ContactEmail, Domain string
}

// PromoteResult carries the raw API key. This struct is the ONLY place the
// key goes: never a log line, mail data or error.
type PromoteResult struct {
	Tenant      *models.Tenant
	APIKey      string
	QuoteLinked bool
	// QuoteLink says why the quote is or is not linked: QuoteLinkLinked,
	// QuoteLinkTaken or QuoteLinkFailed. QuoteLinked stays for older clients.
	QuoteLink string
}

// QuoteLink values. "taken" means the conditional link matched nothing: the
// quote is already linked (and closed) by a concurrent promote, so what the
// caller must do is deal with the duplicate tenant, not close the quote.
// "failed" means the link write errored and the quote may still be open.
const (
	QuoteLinkLinked = "linked"
	QuoteLinkTaken  = "taken"
	QuoteLinkFailed = "failed"
)

// PromoteService turns a quote into a tenant and tells the platform admins.
type PromoteService struct {
	quotes  promoteQuotes
	tenants promoteTenants
	users   promoteRecipients
	mail    sender
	appName string
	log     *zap.Logger

	// sending tracks the notification goroutine, so Drain can wait for it.
	sending sync.WaitGroup
}

// NewPromoteService wires the service. A nil mail means no notification.
func NewPromoteService(
	quotes promoteQuotes,
	tenants promoteTenants,
	users promoteRecipients,
	mail sender,
	appName string,
	log *zap.Logger,
) *PromoteService {
	if strings.TrimSpace(appName) == "" {
		appName = AppName
	}
	return &PromoteService{quotes: quotes, tenants: tenants, users: users, mail: mail, appName: appName, log: log}
}

// Drain waits for in-flight notifications. Used at shutdown and by tests.
func (s *PromoteService) Drain() { s.sending.Wait() }

// Promote creates a tenant from a quote, links the quote to it and starts the
// admin notification in the background.
//
// Once the tenant exists the key is returned whatever happens next. The link
// is a conditional write that only one caller can win, so two admins
// promoting the same quote at once both get their tenant and key, and exactly
// one of them sees QuoteLinked=true. Losing the key instead would leave a
// tenant nobody can authenticate as until someone rotates it.
func (s *PromoteService) Promote(ctx context.Context, quoteID string, in PromoteInput, actorID *primitive.ObjectID, actorName string) (*PromoteResult, error) {
	id, err := primitive.ObjectIDFromHex(quoteID)
	if err != nil {
		return nil, apierr.BadRequest("invalid quote id")
	}

	quote, err := s.quotes.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apierr.NotFound("quote")
		}
		return nil, apierr.Internal(err)
	}
	if quote.PromotedTenantID != nil {
		return nil, apierr.Conflict("this quote has already been promoted to a tenant")
	}
	if quote.TenantID != nil {
		return nil, apierr.Conflict("this quote came through an existing tenant's storefront and cannot be promoted to a new tenant")
	}

	// Same mapping as the tenant creation endpoint (tenantsController.Create);
	// TenantService.Create owns the validation.
	t := &models.Tenant{
		Name:         in.Name,
		Slug:         in.Slug,
		ContactEmail: in.ContactEmail,
		Domain:       in.Domain,
	}
	created, rawKey, err := s.tenants.Create(ctx, t)
	if err != nil {
		return nil, err
	}

	// The tenant exists now, so a client that hangs up must not cancel the
	// link half-way: the key it was owed is gone, but the quote can still
	// record where it went.
	linked, linkErr := s.quotes.LinkPromoted(context.WithoutCancel(ctx), id, created.ID, actorID)
	outcome := QuoteLinkLinked
	switch {
	case linkErr != nil:
		outcome = QuoteLinkFailed
	case !linked:
		outcome = QuoteLinkTaken
	}
	if outcome != QuoteLinkLinked {
		s.log.Warn("promote: tenant created but quote not linked",
			zap.String("quote", id.Hex()), zap.String("tenant", created.ID.Hex()),
			zap.String("quote_link", outcome))
	}

	s.notify(id, created.Name, created.Slug, actorName)

	return &PromoteResult{Tenant: created, APIKey: rawKey, QuoteLinked: outcome == QuoteLinkLinked, QuoteLink: outcome}, nil
}

// notify mails every active platform user in one background goroutine. It is
// given names and ids only; it has no access to the key.
func (s *PromoteService) notify(quoteID primitive.ObjectID, tenantName, slug, actorName string) {
	if s.mail == nil || !s.mail.Available() {
		return
	}

	promotedBy := singleLine(actorName)
	if promotedBy == "" {
		promotedBy = promotedByFallback
	}
	data := map[string]string{
		"app":         singleLine(s.appName),
		"tenant":      singleLine(tenantName),
		"slug":        singleLine(slug),
		"promoted_by": promotedBy,
	}

	s.sending.Add(1)
	go func() {
		defer s.sending.Done()
		// The request's context ends when the handler returns.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		users, err := s.users.ListActive(ctx)
		if err != nil {
			s.log.Error("promote notice: could not list recipients", zap.String("quote", quoteID.Hex()))
			return
		}
		for _, u := range users {
			if ctx.Err() != nil {
				s.log.Warn("promote notice: timed out before every recipient was mailed", zap.String("quote", quoteID.Hex()))
				return
			}
			// A copy per send, so a mailer that mutates its input cannot
			// affect the next recipient.
			d := make(map[string]string, len(data))
			for k, v := range data {
				d[k] = v
			}
			// The error is not logged: it may echo the address.
			if err := s.mail.Send(u.Email, mailer.TemplatePromoted, d); err != nil {
				s.log.Warn("promote notice: not sent",
					zap.String("quote", quoteID.Hex()), zap.String("user", u.ID.Hex()))
			}
		}
	}()
}

// singleLine collapses every run of whitespace and control characters into
// one space, trims, and caps the result at promoteValueMax runes.
func singleLine(s string) string {
	var b strings.Builder
	space := false
	n := 0
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			if n+1 >= promoteValueMax {
				break
			}
			b.WriteByte(' ')
			n++
		}
		space = false
		if n >= promoteValueMax {
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}
