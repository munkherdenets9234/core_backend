package private

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/eandstravel/tenantcore/internal/api/apictx"
	"github.com/eandstravel/tenantcore/internal/api/view"
	"github.com/eandstravel/tenantcore/internal/service"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/response"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// actorNamer resolves the acting admin's display name for the notice.
type actorNamer interface {
	DisplayName(ctx context.Context, id primitive.ObjectID) string
}

type promoteController struct {
	svc    *service.PromoteService
	actors actorNamer
}

// Promote turns a quote into a tenant. Like tenant creation, the API key is in
// this response and nowhere else: not in a log, a mail or an error.
func (h *promoteController) Promote(c *gin.Context) error {
	// Strict decoding, and generic messages: the decoder's own text can quote
	// the submitted body, which AGENTS.md keeps out of client errors.
	var body struct {
		Name         string `json:"name"`
		Slug         string `json:"slug"`
		ContactEmail string `json:"contact_email"`
		Domain       string `json:"domain"`
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<16))
	if err != nil {
		return apierr.BadRequest("invalid request body")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		return apierr.BadRequest("invalid request body")
	}
	if dec.More() {
		return apierr.BadRequest("invalid request body")
	}
	if body.Name == "" || body.Slug == "" {
		return apierr.BadRequest("name and slug are required")
	}

	actorID := apictx.ActorID(c)
	actorName := ""
	if actorID != nil && h.actors != nil {
		actorName = h.actors.DisplayName(c.Request.Context(), *actorID)
	}

	res, err := h.svc.Promote(c.Request.Context(), c.Param("id"), service.PromoteInput{
		Name:         body.Name,
		Slug:         body.Slug,
		ContactEmail: body.ContactEmail,
		Domain:       body.Domain,
	}, actorID, actorName)
	if err != nil {
		return err
	}

	response.Created(c, gin.H{
		"tenant":       view.TenantOf(res.Tenant),
		"api_key":      res.APIKey,
		"quote_linked": res.QuoteLinked,
		"quote_link":   res.QuoteLink,
	})
	return nil
}
