package private

import (
	"github.com/eandstravel/tenantcore/internal/api/apictx"
	"github.com/eandstravel/tenantcore/internal/api/view"
	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/internal/service"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/response"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type tenantsController struct {
	svc      *service.TenantService
	subs     *service.SubscriptionService
	ent      *service.EntitlementService
	showcase *service.ShowcaseService
}

// Create provisions a tenant and returns its API key. The key appears in
// this response and nowhere else, ever — see TenantService.Create.
func (h *tenantsController) Create(c *gin.Context) error {
	var body struct {
		Name         string `json:"name" binding:"required"`
		Slug         string `json:"slug" binding:"required"`
		ContactEmail string `json:"contact_email"`
		Domain       string `json:"domain"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		return apierr.BadRequest(err.Error())
	}

	t := &models.Tenant{
		Name:         body.Name,
		Slug:         body.Slug,
		ContactEmail: body.ContactEmail,
		Domain:       body.Domain,
	}
	created, rawKey, err := h.svc.Create(c.Request.Context(), t)
	if err != nil {
		return err
	}

	response.Created(c, gin.H{
		"tenant":  view.TenantOf(created),
		"api_key": rawKey,
	})
	return nil
}

func (h *tenantsController) List(c *gin.Context) error {
	page, limit := apictx.Page(c, 20)

	data, total, err := h.svc.List(c.Request.Context(), page, limit)
	if err != nil {
		return err
	}

	// One extra query for the whole page, so the console's showcase column
	// has something true to say.
	ids := make([]primitive.ObjectID, 0, len(data))
	for _, t := range data {
		ids = append(ids, t.ID)
	}
	flags, err := h.showcase.Flags(c.Request.Context(), ids)
	if err != nil {
		return err
	}

	out := make([]view.Tenant, 0, len(data))
	for _, t := range data {
		f := flags[t.ID]
		out = append(out, view.TenantOf(t).WithProject(f.Showcase, f.Featured))
	}
	response.List(c, out, response.Meta{Total: total, Page: page, Limit: limit})
	return nil
}

func (h *tenantsController) Get(c *gin.Context) error {
	t, err := h.svc.GetByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		return err
	}
	flags, err := h.showcase.Flags(c.Request.Context(), []primitive.ObjectID{t.ID})
	if err != nil {
		return err
	}
	f := flags[t.ID]
	response.OK(c, view.TenantOf(t).WithProject(f.Showcase, f.Featured))
	return nil
}

func (h *tenantsController) UpdateStatus(c *gin.Context) error {
	var body struct {
		Status models.TenantStatus `json:"status" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		return apierr.BadRequest(err.Error())
	}
	if err := h.svc.UpdateStatus(c.Request.Context(), c.Param("id"), body.Status); err != nil {
		return err
	}
	response.OK(c, gin.H{"updated": true})
	return nil
}

func (h *tenantsController) UpdateDomain(c *gin.Context) error {
	var body struct {
		Domain string `json:"domain"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		return apierr.BadRequest(err.Error())
	}
	if err := h.svc.UpdateDomain(c.Request.Context(), c.Param("id"), body.Domain); err != nil {
		return err
	}
	response.OK(c, gin.H{"updated": true})
	return nil
}

func (h *tenantsController) RotateAPIKey(c *gin.Context) error {
	raw, err := h.svc.RotateAPIKey(c.Request.Context(), c.Param("id"))
	if err != nil {
		return err
	}
	response.OK(c, gin.H{"api_key": raw})
	return nil
}

// ── Subscription, as a property of the tenant ─────────────────────────────

func (h *tenantsController) GetSubscription(c *gin.Context) error {
	tenantID, err := h.tenantID(c)
	if err != nil {
		return err
	}
	sub, err := h.subs.Get(c.Request.Context(), tenantID)
	if err != nil {
		return err
	}
	response.OK(c, view.SubscriptionOf(sub))
	return nil
}

func (h *tenantsController) CreateSubscription(c *gin.Context) error {
	tenantID, planID, billingDay, err := h.tenantPlanAndDay(c)
	if err != nil {
		return err
	}
	sub, err := h.subs.Create(c.Request.Context(), tenantID, planID, billingDay, apictx.ActorID(c))
	if err != nil {
		return err
	}
	response.Created(c, view.SubscriptionOf(sub))
	return nil
}

func (h *tenantsController) UpdateSubscriptionPlan(c *gin.Context) error {
	tenantID, planID, err := h.tenantAndPlanID(c)
	if err != nil {
		return err
	}
	if err := h.subs.UpdatePlan(c.Request.Context(), tenantID, planID, apictx.ActorID(c)); err != nil {
		return err
	}
	response.OK(c, gin.H{"updated": true})
	return nil
}

func (h *tenantsController) CancelSubscription(c *gin.Context) error {
	tenantID, err := h.tenantID(c)
	if err != nil {
		return err
	}
	if err := h.subs.Cancel(c.Request.Context(), tenantID, apictx.ActorID(c)); err != nil {
		return err
	}
	response.OK(c, gin.H{"canceled": true})
	return nil
}

// GetEntitlement renders exactly what a product service would receive for
// this tenant. Same assembly, same document — not a reimplementation, which
// would eventually disagree with the real one and make this view worse than
// useless for diagnosing "the module is missing".
func (h *tenantsController) GetEntitlement(c *gin.Context) error {
	tenantID, err := h.tenantID(c)
	if err != nil {
		return err
	}
	ent, err := h.ent.For(c.Request.Context(), tenantID)
	if err != nil {
		return err
	}
	response.OK(c, ent)
	return nil
}

func (h *tenantsController) tenantID(c *gin.Context) (primitive.ObjectID, error) {
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		return primitive.NilObjectID, apierr.BadRequest("invalid tenant id").In(apierr.DomainTenant)
	}
	return id, nil
}

func (h *tenantsController) tenantAndPlanID(c *gin.Context) (tenantID, planID primitive.ObjectID, err error) {
	tenantID, planID, _, err = h.tenantPlanAndDay(c)
	return tenantID, planID, err
}

// tenantPlanAndDay reads the tenant from the path and the plan, plus an
// optional billing_day, from the body. The body can only be bound once, so
// creating a subscription and changing its plan share this one reader; changing
// the plan simply ignores the day.
func (h *tenantsController) tenantPlanAndDay(c *gin.Context) (tenantID, planID primitive.ObjectID, billingDay int, err error) {
	tenantID, err = h.tenantID(c)
	if err != nil {
		return tenantID, planID, 0, err
	}

	var body struct {
		PlanID     string `json:"plan_id" binding:"required"`
		BillingDay int    `json:"billing_day"`
	}
	if bindErr := c.ShouldBindJSON(&body); bindErr != nil {
		return tenantID, planID, 0, apierr.BadRequest(bindErr.Error())
	}

	planID, parseErr := primitive.ObjectIDFromHex(body.PlanID)
	if parseErr != nil {
		return tenantID, planID, 0, apierr.BadRequest("invalid plan_id").In(apierr.DomainPlan)
	}
	return tenantID, planID, body.BillingDay, nil
}

// RenewSubscription extends the tenant's subscription by one plan period.
func (h *tenantsController) RenewSubscription(c *gin.Context) error {
	tenantID, err := h.tenantID(c)
	if err != nil {
		return err
	}
	sub, err := h.subs.Renew(c.Request.Context(), tenantID, apictx.ActorID(c))
	if err != nil {
		return err
	}
	response.OK(c, view.SubscriptionOf(sub))
	return nil
}

// SetSubscriptionBillingDay changes the day the tenant's subscription renews on.
func (h *tenantsController) SetSubscriptionBillingDay(c *gin.Context) error {
	tenantID, err := h.tenantID(c)
	if err != nil {
		return err
	}
	var body struct {
		BillingDay int `json:"billing_day" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		return apierr.BadRequest(err.Error())
	}
	if err := h.subs.SetBillingDay(c.Request.Context(), tenantID, body.BillingDay, apictx.ActorID(c)); err != nil {
		return err
	}
	response.OK(c, gin.H{"billing_day": body.BillingDay})
	return nil
}
