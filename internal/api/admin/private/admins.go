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

type adminsController struct {
	svc *service.PlatformUserService
}

func (h *adminsController) Create(c *gin.Context) error {
	var body struct {
		Name     string `json:"name"`
		Email    string `json:"email" binding:"required"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		return apierr.BadRequest(err.Error())
	}

	u, generated, err := h.svc.Create(c.Request.Context(), body.Name, body.Email, body.Password)
	if err != nil {
		return err
	}

	// A generated password is echoed exactly once, here. It is stored only
	// as a bcrypt hash and cannot be read back afterwards.
	resp := gin.H{"user": view.PlatformUserOf(u)}
	if generated != "" {
		resp["password"] = generated
	}
	response.Created(c, resp)
	return nil
}

func (h *adminsController) List(c *gin.Context) error {
	page, limit := apictx.Page(c, 20)

	data, total, err := h.svc.List(c.Request.Context(), page, limit)
	if err != nil {
		return err
	}
	response.List(c, view.PlatformUsersOf(data), response.Meta{Total: total, Page: page, Limit: limit})
	return nil
}

func (h *adminsController) UpdateStatus(c *gin.Context) error {
	var body struct {
		Status models.PlatformUserStatus `json:"status" binding:"required"`
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

func (h *adminsController) ResetPassword(c *gin.Context) error {
	var body struct {
		NewPassword string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		return apierr.BadRequest(err.Error())
	}

	generated, err := h.svc.ResetPassword(c.Request.Context(), c.Param("id"), body.NewPassword)
	if err != nil {
		return err
	}

	resp := gin.H{"updated": true}
	if generated != "" {
		resp["password"] = generated
	}
	response.OK(c, resp)
	return nil
}

func (h *adminsController) ChangePassword(c *gin.Context) error {
	var body struct {
		CurrentPassword string `json:"current_password" binding:"required"`
		NewPassword     string `json:"new_password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		return apierr.BadRequest(err.Error())
	}

	// The token verified upstream carries this id, so a parse failure means
	// we minted a token with a malformed subject: our bug, not the caller's.
	uid, err := primitive.ObjectIDFromHex(apictx.UserID(c))
	if err != nil {
		return apierr.Internal(err)
	}

	if err := h.svc.ChangePassword(c.Request.Context(), uid, body.CurrentPassword, body.NewPassword); err != nil {
		return err
	}
	response.OK(c, gin.H{"updated": true})
	return nil
}
