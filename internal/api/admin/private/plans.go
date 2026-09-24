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
)

type plansController struct {
	svc *service.PlanService
}

func (h *plansController) Create(c *gin.Context) error {
	var p models.Plan
	if err := c.ShouldBindJSON(&p); err != nil {
		return apierr.BadRequest(err.Error())
	}
	if err := h.svc.Create(c.Request.Context(), &p, apictx.ActorID(c)); err != nil {
		return err
	}
	response.Created(c, view.PlanOf(&p))
	return nil
}

func (h *plansController) List(c *gin.Context) error {
	page, limit := apictx.Page(c, 50)

	data, total, err := h.svc.List(c.Request.Context(), page, limit)
	if err != nil {
		return err
	}
	response.List(c, view.PlansOf(data), response.Meta{Total: total, Page: page, Limit: limit})
	return nil
}

func (h *plansController) Get(c *gin.Context) error {
	p, err := h.svc.GetByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		return err
	}
	response.OK(c, view.PlanOf(p))
	return nil
}

// Update takes a partial document so the console can change one field
// without resending the whole plan and racing another editor over the rest.
// The service layer validates the entitlement-bearing keys when they appear.
func (h *plansController) Update(c *gin.Context) error {
	var update bson.M
	if err := c.ShouldBindJSON(&update); err != nil {
		return apierr.BadRequest(err.Error())
	}
	if err := h.svc.Update(c.Request.Context(), c.Param("id"), update, apictx.ActorID(c)); err != nil {
		return err
	}
	response.OK(c, gin.H{"updated": true})
	return nil
}

// Delete removes a plan from the price list. Subscriptions already on it
// keep working — see PlanRepo.Delete for why that is the right behaviour
// rather than a dangling reference to clean up.
func (h *plansController) Delete(c *gin.Context) error {
	if err := h.svc.Delete(c.Request.Context(), c.Param("id")); err != nil {
		return err
	}
	response.OK(c, gin.H{"deleted": true})
	return nil
}
