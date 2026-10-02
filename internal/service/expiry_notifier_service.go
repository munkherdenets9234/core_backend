package service

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/pkg/mailer"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.uber.org/zap"
)

// ExpiryWarnWindow is how far ahead of a subscription's period end the
// operator is warned. Seven days is enough to renew without it being a
// months-early reminder that gets ignored.
const ExpiryWarnWindow = 7 * 24 * time.Hour

// removedPlanLabel stands in for a plan that was deleted out from under a live
// subscription. That state is legitimate elsewhere in this service and must
// not suppress the warning; the mail goes to the operator, who can read this.
const removedPlanLabel = "(plan removed)"

// The reads and writes the notifier needs, as narrow interfaces for the same
// reason as PasswordResetService and EntitlementService: whether an operator
// hears about a lapsing subscription is a rule that fails silently, so it has
// to be testable without a database, an SMTP server or a week of waiting.
type (
	expirySubs interface {
		FindExpiring(ctx context.Context, after, through time.Time) ([]*models.Subscription, error)
		ClaimExpiryNotice(ctx context.Context, id primitive.ObjectID, periodEnd time.Time) (bool, error)
		ReleaseExpiryNotice(ctx context.Context, id primitive.ObjectID, periodEnd time.Time) error
	}
	expiryTenants interface {
		FindByID(ctx context.Context, id primitive.ObjectID) (*models.Tenant, error)
	}
	expiryPlans interface {
		FindByID(ctx context.Context, id primitive.ObjectID) (*models.Plan, error)
	}
)

// ExpiryNotifier emails the platform operator once per subscription period,
// ExpiryWarnWindow before it ends.
//
// The warning is CLAIMED before it is sent, with a conditional update that
// succeeds for exactly one caller. Sending first and marking after would send
// twice after a crash, and two replicas running at the same moment would both
// send because neither had marked yet. The cost of claiming first is that a
// crash between the claim and the send loses that one warning; the operator
// can still read the end date in the console, which makes that the better
// failure.
type ExpiryNotifier struct {
	subs    expirySubs
	tenants expiryTenants
	plans   expiryPlans
	mail    sender
	to      string
	log     *zap.Logger
}

func NewExpiryNotifier(
	subs expirySubs,
	tenants expiryTenants,
	plans expiryPlans,
	mail sender,
	to string,
	log *zap.Logger,
) *ExpiryNotifier {
	return &ExpiryNotifier{subs: subs, tenants: tenants, plans: plans, mail: mail, to: to, log: log}
}

// NotifyExpiring sends a warning for every paying subscription ending inside
// the window that has not been warned about for its current period, and
// returns how many it sent.
//
// The error is only for the lookup that finds candidates. A failure on one
// subscription is logged and skipped so it cannot stop the others.
func (n *ExpiryNotifier) NotifyExpiring(ctx context.Context, now time.Time) (int, error) {
	candidates, err := n.subs.FindExpiring(ctx, now, now.Add(ExpiryWarnWindow))
	if err != nil {
		return 0, err
	}

	sent := 0
	for _, sub := range candidates {
		end := sub.CurrentPeriodEnd
		if sub.ExpiryNoticeFor != nil && sub.ExpiryNoticeFor.Equal(end) {
			continue // already warned for this period
		}

		claimed, err := n.subs.ClaimExpiryNotice(ctx, sub.ID, end)
		if err != nil {
			n.log.Warn("expiry notice: could not claim", zap.String("subscription", sub.ID.Hex()), zap.Error(err))
			continue
		}
		if !claimed {
			continue // another run got it, or the period changed under us
		}

		if err := n.deliver(ctx, sub, now); err != nil {
			// Everything after the claim must release it, or the warning is
			// lost for the whole period with nothing to say so.
			if relErr := n.subs.ReleaseExpiryNotice(ctx, sub.ID, end); relErr != nil {
				n.log.Error("expiry notice: could not release claim; this period will not be warned about",
					zap.String("subscription", sub.ID.Hex()), zap.Error(relErr))
			}
			n.log.Warn("expiry notice: not sent, will retry next tick",
				zap.String("subscription", sub.ID.Hex()), zap.Error(err))
			continue
		}
		sent++
	}
	return sent, nil
}

// deliver resolves the names and sends the mail.
func (n *ExpiryNotifier) deliver(ctx context.Context, sub *models.Subscription, now time.Time) error {
	tenant, err := n.tenants.FindByID(ctx, sub.TenantID)
	if err != nil {
		return err
	}

	planName := removedPlanLabel
	plan, err := n.plans.FindByID(ctx, sub.PlanID)
	switch {
	case err == nil:
		planName = plan.Name
		if planName == "" {
			planName = plan.Slug
		}
	case errors.Is(err, mongo.ErrNoDocuments):
		// keep removedPlanLabel
	default:
		return err
	}

	return n.mail.Send(n.to, mailer.TemplateSubscriptionExpiring, map[string]string{
		"app":       AppName,
		"tenant":    tenant.Name,
		"plan":      planName,
		"ends_on":   sub.CurrentPeriodEnd.UTC().Format("2006-01-02"),
		"days_left": strconv.Itoa(daysLeft(now, sub.CurrentPeriodEnd)),
	})
}

// daysLeft is whole days until end, rounded UP.
//
// Rounding down would tell the operator "0 days" about a subscription that
// still has hours left, and "6" about one with 6 days 23 hours. Rounding up
// can overstate by under a day, which is the safer way to be wrong about a
// deadline.
func daysLeft(now, end time.Time) int {
	d := end.Sub(now)
	if d <= 0 {
		return 0
	}
	const day = 24 * time.Hour
	days := int(d / day)
	if d%day != 0 {
		days++
	}
	return days
}
