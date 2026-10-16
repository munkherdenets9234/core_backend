package private

import (
	"errors"
	"io"

	"github.com/eandstravel/tenantcore/internal/service"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/response"
	"github.com/gin-gonic/gin"
)

// invoicesController serves a tenant's invoices. Amounts, numbers, status and
// the recipient are decided by the service; the body carries only choices.
type invoicesController struct {
	svc *service.InvoiceService
}

func (h *invoicesController) List(c *gin.Context) error {
	data, err := h.svc.List(c.Request.Context(), c.Param("id"))
	if err != nil {
		return err
	}
	response.List(c, data, response.Meta{Total: int64(len(data)), Page: 1, Limit: service.MaxInvoicesPerTenant})
	return nil
}

func (h *invoicesController) Get(c *gin.Context) error {
	inv, err := h.svc.Get(c.Request.Context(), c.Param("id"), c.Param("invId"))
	if err != nil {
		return err
	}
	response.OK(c, inv)
	return nil
}

// Preview renders the invoice mail ({subject, text, html}) without sending it
// or changing the invoice.
func (h *invoicesController) Preview(c *gin.Context) error {
	p, err := h.svc.Preview(c.Request.Context(), c.Param("id"), c.Param("invId"))
	if err != nil {
		return err
	}
	response.OK(c, p)
	return nil
}

func (h *invoicesController) Create(c *gin.Context) error {
	var in service.InvoiceInput
	if err := c.ShouldBindJSON(&in); err != nil {
		return apierr.BadRequest("invalid request body").In(apierr.DomainTenant)
	}
	inv, err := h.svc.Create(c.Request.Context(), c.Param("id"), in)
	if err != nil {
		return err
	}
	response.Created(c, inv)
	return nil
}

// Send takes an optional body: {"resend": true}. An empty body is a first send.
func (h *invoicesController) Send(c *gin.Context) error {
	var in service.SendInput
	if err := c.ShouldBindJSON(&in); err != nil && !errors.Is(err, io.EOF) {
		return apierr.BadRequest("invalid request body").In(apierr.DomainTenant)
	}
	inv, err := h.svc.Send(c.Request.Context(), c.Param("id"), c.Param("invId"), in)
	if err != nil {
		return err
	}
	response.OK(c, inv)
	return nil
}

// Paid takes an optional body, the transaction record (see service.PaidInput).
// An empty body marks the invoice paid with the defaults. The body is never
// logged.
func (h *invoicesController) Paid(c *gin.Context) error {
	var in service.PaidInput
	if err := c.ShouldBindJSON(&in); err != nil && !errors.Is(err, io.EOF) {
		return apierr.BadRequest("invalid request body").In(apierr.DomainTenant)
	}
	inv, err := h.svc.MarkPaid(c.Request.Context(), c.Param("id"), c.Param("invId"), in)
	if err != nil {
		return err
	}
	response.OK(c, inv)
	return nil
}

func (h *invoicesController) Void(c *gin.Context) error {
	inv, err := h.svc.Void(c.Request.Context(), c.Param("id"), c.Param("invId"))
	if err != nil {
		return err
	}
	response.OK(c, inv)
	return nil
}

// Delete removes a draft invoice (409 for any other status).
func (h *invoicesController) Delete(c *gin.Context) error {
	if err := h.svc.Delete(c.Request.Context(), c.Param("id"), c.Param("invId")); err != nil {
		return err
	}
	response.OK(c, gin.H{"deleted": true})
	return nil
}

// Edit changes the due date and/or tax rate of a draft invoice. The body
// carries only those two choices; totals are recomputed by the service.
func (h *invoicesController) Edit(c *gin.Context) error {
	var in service.InvoiceEditInput
	if err := c.ShouldBindJSON(&in); err != nil {
		return apierr.BadRequest("invalid request body").In(apierr.DomainTenant)
	}
	inv, err := h.svc.Edit(c.Request.Context(), c.Param("id"), c.Param("invId"), in)
	if err != nil {
		return err
	}
	response.OK(c, inv)
	return nil
}
