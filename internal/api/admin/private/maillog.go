package private

import (
	"github.com/eandstravel/tenantcore/internal/api/apictx"
	"github.com/eandstravel/tenantcore/internal/api/view"
	"github.com/eandstravel/tenantcore/internal/service"
	"github.com/eandstravel/tenantcore/pkg/response"
	"github.com/gin-gonic/gin"
)

// mailLogController is the read-only view of what the mailer tried to send.
type mailLogController struct {
	svc *service.MailLogService
}

// List serves one page of the mail log, newest first, filtered by ?status= and
// ?template=. The service validates both; this only reads the query.
func (h *mailLogController) List(c *gin.Context) error {
	page, limit := apictx.Page(c, 20)
	if limit > service.MailLogMaxLimit {
		limit = service.MailLogMaxLimit
	}

	data, total, err := h.svc.List(c.Request.Context(), c.Query("status"), c.Query("template"), page, limit)
	if err != nil {
		return err
	}
	response.List(c, view.MailLogOf(data), response.Meta{Total: total, Page: page, Limit: limit})
	return nil
}
