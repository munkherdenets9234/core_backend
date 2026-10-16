package private

import (
	"github.com/eandstravel/tenantcore/internal/service"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/response"
	"github.com/gin-gonic/gin"
)

// feesController serves the operator's fee ledger and costing view of a
// tenant. Bookkeeping only: it never feeds entitlement, routing or auth.
type feesController struct {
	svc *service.FeeService
}

func bindFee(c *gin.Context) (service.FeeInput, error) {
	var in service.FeeInput
	if err := c.ShouldBindJSON(&in); err != nil {
		// The binder's text can echo the submitted value; send a fixed one.
		return in, apierr.BadRequest("invalid request body").In(apierr.DomainTenant)
	}
	return in, nil
}

func (h *feesController) List(c *gin.Context) error {
	data, err := h.svc.List(c.Request.Context(), c.Param("id"))
	if err != nil {
		return err
	}
	response.List(c, data, response.Meta{Total: int64(len(data)), Page: 1, Limit: service.MaxFeesPerTenant})
	return nil
}

func (h *feesController) Create(c *gin.Context) error {
	in, err := bindFee(c)
	if err != nil {
		return err
	}
	f, err := h.svc.Create(c.Request.Context(), c.Param("id"), in)
	if err != nil {
		return err
	}
	response.Created(c, f)
	return nil
}

func (h *feesController) Update(c *gin.Context) error {
	in, err := bindFee(c)
	if err != nil {
		return err
	}
	f, err := h.svc.Update(c.Request.Context(), c.Param("id"), c.Param("feeId"), in)
	if err != nil {
		return err
	}
	response.OK(c, f)
	return nil
}

func (h *feesController) Delete(c *gin.Context) error {
	if err := h.svc.Delete(c.Request.Context(), c.Param("id"), c.Param("feeId")); err != nil {
		return err
	}
	response.OK(c, gin.H{"deleted": true})
	return nil
}

func (h *feesController) Costing(c *gin.Context) error {
	data, err := h.svc.Costing(c.Request.Context(), c.Param("id"))
	if err != nil {
		return err
	}
	response.OK(c, data)
	return nil
}
