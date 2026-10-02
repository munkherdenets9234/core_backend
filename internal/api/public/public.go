// Package public is the operator's own marketing surface: the pricing page,
// the case studies, and the contact form.
//
// It is the fourth audience, and the only one that reaches tenantcore with
// no credential at all. That is a deliberate, narrow exception to the rule
// stated in admin/public.go — that tenantcore has no public read surface
// because everything it holds is a customer record or a price list. What is
// served here is neither: it is copy the operator has explicitly published,
// plus one write that exists to collect strangers' enquiries.
//
// Three rules keep the exception narrow:
//
//  1. Every read here is of something marked published. An unshowcased case
//     study 404s; an inactive plan is absent. Never "filter it in the
//     handler" — the repository query carries the filter, so a new call site
//     cannot forget it.
//  2. Nothing here returns a view that can carry a tenant's contact email,
//     API key fragment, status, or a plan's entitlement. Enforced by the
//     view types in view/content.go having no such fields.
//  3. The one write is rate limited and cannot set its own status.
package public

import (
	"strconv"
	"strings"

	"github.com/eandstravel/tenantcore/internal/api/view"
	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/internal/service"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/httpx"
	"github.com/eandstravel/tenantcore/pkg/response"
	"github.com/gin-gonic/gin"
)

type Deps struct {
	Plan     *service.PlanService
	Showcase *service.ShowcaseService
	Quote    *service.QuoteService
	Content  *service.SiteContentService

	// LeadRateLimit guards the contact form. Without it the one
	// unauthenticated write on this service is an open invitation to fill
	// the console's inbox.
	LeadRateLimit gin.HandlerFunc
}

func Register(base *gin.RouterGroup, d Deps) {
	c := &controller{plans: d.Plan, showcase: d.Showcase, quotes: d.Quote, content: d.Content}

	g := httpx.Wrap(base)
	g.GET("/content", c.GetContent)
	g.GET("/plans", c.ListPlans)
	g.GET("/projects", c.ListProjects)
	g.GET("/projects/:slug", c.GetProject)
	g.Group("", d.LeadRateLimit).POST("/quotes", c.CreateQuote)
}

type controller struct {
	plans    *service.PlanService
	showcase *service.ShowcaseService
	quotes   *service.QuoteService
	content  *service.SiteContentService
}

// GetContent serves the operator's edited copy for every page at once.
//
// One call rather than one per page: the nav and footer live in "common" and
// are on every screen, so a per-page endpoint would mean two requests to
// render anything. The whole payload is a few kilobytes of overrides.
func (h *controller) GetContent(c *gin.Context) error {
	pages, err := h.content.All(c.Request.Context())
	if err != nil {
		return err
	}
	response.OK(c, view.SiteContentOf(pages, langOf(c)))
	return nil
}

func (h *controller) ListPlans(c *gin.Context) error {
	plans, err := h.plans.PublicPlans(c.Request.Context(), limitOf(c, 100, 200))
	if err != nil {
		return err
	}
	response.OK(c, view.PublicPlansOf(plans))
	return nil
}

func (h *controller) ListProjects(c *gin.Context) error {
	items, err := h.showcase.List(c.Request.Context(), limitOf(c, 50, 200))
	if err != nil {
		return err
	}
	response.OK(c, view.PublicProjectsOf(items, langOf(c)))
	return nil
}

func (h *controller) GetProject(c *gin.Context) error {
	item, err := h.showcase.GetBySlug(c.Request.Context(), c.Param("slug"))
	if err != nil {
		return err
	}
	response.OK(c, view.PublicProjectOf(*item, langOf(c)))
	return nil
}

func (h *controller) CreateQuote(c *gin.Context) error {
	// Bound explicitly rather than into models.Quote: binding straight onto
	// the model would let a caller post `status`, `tenant_id` or `user_id`
	// and have it stored. The service overwrites status anyway — this makes
	// the field set that a stranger controls visible in one place.
	var body struct {
		Name        string `json:"name"`
		Email       string `json:"email"`
		Phone       string `json:"phone"`
		CompanyName string `json:"company_name"`
		PackageSlug string `json:"package_slug"`
		Budget      string `json:"budget"`
		Timeline    string `json:"timeline"`
		Message     string `json:"message"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		return apierr.BadRequest(err.Error())
	}

	q := &models.Quote{
		Name:        body.Name,
		Email:       body.Email,
		Phone:       body.Phone,
		CompanyName: body.CompanyName,
		PackageSlug: body.PackageSlug,
		Budget:      body.Budget,
		Timeline:    body.Timeline,
		Message:     body.Message,
	}
	if err := h.quotes.Create(c.Request.Context(), q); err != nil {
		return err
	}

	// Deliberately not the created quote. The submitter has no business
	// reading back an id they could use to probe anything, and the form only
	// needs to know it worked.
	response.Created(c, gin.H{"received": true})
	return nil
}

// langOf reads ?lang=, defaulting to English. An unrecognised value is not
// an error: the locale resolvers fall back on their own, and rejecting
// ?lang=fr with a 400 would break a page over a query string nobody typed
// deliberately.
func langOf(c *gin.Context) string {
	lang := strings.ToLower(strings.TrimSpace(c.Query("lang")))
	if lang == "" {
		return "en"
	}
	return lang
}

// limitOf clamps ?limit= into [1, max]. Unbounded, it is a way to ask an
// unauthenticated endpoint to read the whole collection.
func limitOf(c *gin.Context, fallback, max int) int {
	n, err := strconv.Atoi(c.Query("limit"))
	if err != nil || n < 1 {
		return fallback
	}
	if n > max {
		return max
	}
	return n
}
