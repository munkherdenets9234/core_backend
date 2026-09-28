package private

import (
	"github.com/eandstravel/tenantcore/internal/api/apictx"
	"github.com/eandstravel/tenantcore/internal/api/view"
	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/internal/service"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/response"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// The console's half of the operator's own content: case studies, the leads
// the contact form produces, and which pricing cards each tenant displays.

type contentController struct {
	showcase   *service.ShowcaseService
	quotes     *service.QuoteService
	tenantPlan *service.TenantPlanService
	site       *service.SiteContentService
}

// ── The marketing site's own copy ─────────────────────────────────────────

// ListPages names the pages that can be edited. The console builds its menu
// from this rather than hardcoding a list that would drift from the site's
// actual dictionaries.
func (h *contentController) ListPages(c *gin.Context) error {
	response.OK(c, gin.H{"pages": service.KnownPages})
	return nil
}

func (h *contentController) GetPage(c *gin.Context) error {
	p, err := h.site.Get(c.Request.Context(), c.Param("page"))
	if err != nil {
		return err
	}
	response.OK(c, view.SitePageOf(p))
	return nil
}

// SavePage replaces one page's overrides.
//
// Clearing a field and saving REMOVES that override rather than storing an
// empty string - which is how an operator puts a headline back to what the
// site shipped with. See SiteContentService.Save.
func (h *contentController) SavePage(c *gin.Context) error {
	var body struct {
		Entries []models.ContentEntry `json:"entries"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		return apierr.BadRequest(err.Error())
	}
	if err := h.site.Save(c.Request.Context(), c.Param("page"), body.Entries, apictx.ActorID(c)); err != nil {
		return err
	}
	response.OK(c, gin.H{"updated": true})
	return nil
}

// ── A tenant's case study ─────────────────────────────────────────────────

func (h *contentController) GetProject(c *gin.Context) error {
	tenantID, err := objectID(c.Param("id"), "tenant", apierr.DomainTenant)
	if err != nil {
		return err
	}
	d, err := h.showcase.Get(c.Request.Context(), tenantID)
	if err != nil {
		return err
	}
	// Null, not 404, when a tenant has no case study yet: the console opens
	// an empty form on that, and an error would make "never written" look
	// like "something went wrong".
	response.OK(c, view.TenantDetailOf(d))
	return nil
}

// UpdateProject writes the whole case study.
//
// A full replace rather than a partial patch, unlike a plan: this is one
// form with one save button, every field on screen at once, and a partial
// update would leave a cleared image or an emptied metric list silently
// unchanged — the operator's most common reason to open the form.
func (h *contentController) UpdateProject(c *gin.Context) error {
	tenantID, err := objectID(c.Param("id"), "tenant", apierr.DomainTenant)
	if err != nil {
		return err
	}

	var body struct {
		Tagline     models.LocaleText `json:"tagline"`
		Description models.LocaleText `json:"description"`
		Category    string            `json:"category"`
		WebsiteURL  string            `json:"website_url"`
		CoverImage  *models.Image     `json:"cover_image"`
		AdminCover  *models.Image     `json:"admin_cover"`
		Images      []models.Image    `json:"images"`
		Metrics     []models.Metric   `json:"metrics"`
		Showcase    bool              `json:"showcase"`
		Featured    bool              `json:"featured"`
		SortOrder   int               `json:"sort_order"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		return apierr.BadRequest(err.Error())
	}

	// Nil slices are stored as empty ones so a cleared gallery persists as
	// "no images" rather than being omitted and left at its old value.
	if body.Images == nil {
		body.Images = []models.Image{}
	}
	if body.Metrics == nil {
		body.Metrics = []models.Metric{}
	}

	set := bson.M{
		"tagline":     body.Tagline,
		"description": body.Description,
		"category":    body.Category,
		"website_url": body.WebsiteURL,
		"cover_image": body.CoverImage,
		"admin_cover": body.AdminCover,
		"images":      body.Images,
		"metrics":     body.Metrics,
		"showcase":    body.Showcase,
		"featured":    body.Featured,
		"sort_order":  body.SortOrder,
	}
	if err := h.showcase.Save(c.Request.Context(), tenantID, set, apictx.ActorID(c)); err != nil {
		return err
	}
	response.OK(c, gin.H{"updated": true})
	return nil
}

// ── Leads ─────────────────────────────────────────────────────────────────

func (h *contentController) ListQuotes(c *gin.Context) error {
	page, limit := apictx.Page(c, 20)
	items, total, err := h.quotes.List(c.Request.Context(), nil, page, limit)
	if err != nil {
		return err
	}
	response.List(c, view.QuotesOf(items), response.Meta{Total: total, Page: page, Limit: limit})
	return nil
}

func (h *contentController) ListTenantQuotes(c *gin.Context) error {
	tenantID, err := objectID(c.Param("id"), "tenant", apierr.DomainTenant)
	if err != nil {
		return err
	}
	page, limit := apictx.Page(c, 20)
	items, total, err := h.quotes.List(c.Request.Context(), &tenantID, page, limit)
	if err != nil {
		return err
	}
	response.List(c, view.QuotesOf(items), response.Meta{Total: total, Page: page, Limit: limit})
	return nil
}

func (h *contentController) UpdateQuoteStatus(c *gin.Context) error {
	var body struct {
		Status models.QuoteStatus `json:"status" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		return apierr.BadRequest(err.Error())
	}
	if err := h.quotes.UpdateStatus(c.Request.Context(), c.Param("id"), body.Status, apictx.ActorID(c)); err != nil {
		return err
	}
	response.OK(c, gin.H{"updated": true})
	return nil
}

// ── Which pricing cards a tenant shows ────────────────────────────────────

func (h *contentController) ListTenantPlans(c *gin.Context) error {
	tenantID, err := objectID(c.Param("id"), "tenant", apierr.DomainTenant)
	if err != nil {
		return err
	}
	plans, err := h.tenantPlan.List(c.Request.Context(), tenantID)
	if err != nil {
		return err
	}
	response.OK(c, view.PlansOf(plans))
	return nil
}

func (h *contentController) AssignPlan(c *gin.Context) error {
	tenantID, err := objectID(c.Param("id"), "tenant", apierr.DomainTenant)
	if err != nil {
		return err
	}

	// package_id, not plan_id: this endpoint exists for the console, whose
	// whole vocabulary for the public price list is still "package". The
	// wire name follows the caller; the model underneath is a plan.
	var body struct {
		PackageID string `json:"package_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		return apierr.BadRequest(err.Error())
	}
	planID, err := objectID(body.PackageID, "package", apierr.DomainPlan)
	if err != nil {
		return err
	}

	if err := h.tenantPlan.Assign(c.Request.Context(), tenantID, planID, apictx.ActorID(c)); err != nil {
		return err
	}
	response.Created(c, gin.H{"assigned": true})
	return nil
}

func (h *contentController) UnassignPlan(c *gin.Context) error {
	tenantID, err := objectID(c.Param("id"), "tenant", apierr.DomainTenant)
	if err != nil {
		return err
	}
	planID, err := objectID(c.Param("package_id"), "package", apierr.DomainPlan)
	if err != nil {
		return err
	}
	if err := h.tenantPlan.Unassign(c.Request.Context(), tenantID, planID); err != nil {
		return err
	}
	response.OK(c, gin.H{"unassigned": true})
	return nil
}

// objectID parses a path or body id, naming what failed. A shared helper so
// every one of these reports the same way — "invalid tenant id" rather than
// whichever phrasing the handler's author reached for.
func objectID(raw, what, domain string) (primitive.ObjectID, error) {
	id, err := primitive.ObjectIDFromHex(raw)
	if err != nil {
		return primitive.NilObjectID, apierr.BadRequest("invalid " + what + " id").In(domain)
	}
	return id, nil
}
