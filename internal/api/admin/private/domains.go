package private

import (
	"errors"
	"io"

	"github.com/eandstravel/tenantcore/internal/service"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/response"
	"github.com/gin-gonic/gin"
)

// domainsController serves the operator's domain records of a tenant. The
// records are bookkeeping and never feed routing or authentication.
type domainsController struct {
	svc *service.DomainService
}

func bindDomain(c *gin.Context) (service.DomainInput, error) {
	var in service.DomainInput
	if err := c.ShouldBindJSON(&in); err != nil {
		// The binder's text can echo the submitted value; send a fixed one.
		return in, apierr.BadRequest("invalid request body").In(apierr.DomainTenant)
	}
	return in, nil
}

func (h *domainsController) List(c *gin.Context) error {
	data, err := h.svc.List(c.Request.Context(), c.Param("id"))
	if err != nil {
		return err
	}
	response.List(c, data, response.Meta{Total: int64(len(data)), Page: 1, Limit: service.MaxDomainsPerTenant})
	return nil
}

func (h *domainsController) Get(c *gin.Context) error {
	d, err := h.svc.Get(c.Request.Context(), c.Param("id"), c.Param("domainId"))
	if err != nil {
		return err
	}
	response.OK(c, d)
	return nil
}

func (h *domainsController) Create(c *gin.Context) error {
	in, err := bindDomain(c)
	if err != nil {
		return err
	}
	d, err := h.svc.Create(c.Request.Context(), c.Param("id"), in)
	if err != nil {
		return err
	}
	response.Created(c, d)
	return nil
}

func (h *domainsController) Update(c *gin.Context) error {
	in, err := bindDomain(c)
	if err != nil {
		return err
	}
	d, err := h.svc.Update(c.Request.Context(), c.Param("id"), c.Param("domainId"), in)
	if err != nil {
		return err
	}
	response.OK(c, d)
	return nil
}

func (h *domainsController) Delete(c *gin.Context) error {
	if err := h.svc.Delete(c.Request.Context(), c.Param("id"), c.Param("domainId")); err != nil {
		return err
	}
	response.OK(c, gin.H{"deleted": true})
	return nil
}

// RegistrarPaid settles the registrar cost of one domain. The body is
// optional; every field in it is optional too.
func (h *domainsController) RegistrarPaid(c *gin.Context) error {
	var in service.RegistrarPaidInput
	if err := c.ShouldBindJSON(&in); err != nil && !errors.Is(err, io.EOF) {
		return apierr.BadRequest("invalid request body").In(apierr.DomainTenant)
	}
	d, err := h.svc.RegistrarPaid(c.Request.Context(), c.Param("id"), c.Param("domainId"), in)
	if err != nil {
		return err
	}
	response.OK(c, d)
	return nil
}
