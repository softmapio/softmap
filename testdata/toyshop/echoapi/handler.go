// Package echoapi is the echo-framework fixture: handler-level rejections
// via `return c.JSON(4xx, body)` must surface as decisions with the
// developer-written message, and the trivial error-DTO constructor must
// collapse instead of appearing as a step.
package echoapi

import (
	"context"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	"github.com/go-ozzo/ozzo-validation/v4/is"
	echo "github.com/labstack/echo/v4"

	"example.com/toyshop/service"
)

type callbackReq struct {
	ReportID string `json:"report_id"`
	Status   string `json:"status" validate:"required,oneof=NEW DONE"`
	Amount   int64  `json:"amount_cents"`
}

type errResp struct {
	Code    int
	Message string
}

// newErrResp is deliberate noise: a trivial constructor (single struct
// literal, no calls) that must never be a step.
func newErrResp(code int, msg string) errResp {
	return errResp{Code: code, Message: msg}
}

type Handler struct {
	svc *service.Service
}

func Register(e *echo.Echo, svc *service.Service) {
	h := &Handler{svc: svc}
	e.POST("/reports/:id/callback", h.ReportCallback)
}

func (h *Handler) ReportCallback(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return c.JSON(400, newErrResp(400, "id is required"))
	}
	var req callbackReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(400, newErrResp(400, "invalid callback body"))
	}
	if err := validation.Validate(req.ReportID, validation.Required, validation.Length(4, 64), is.Digit); err != nil {
		return c.JSON(400, newErrResp(400, "report_id is invalid"))
	}
	if _, err := h.svc.GetOrder(context.Background(), id); err != nil {
		return c.JSON(500, newErrResp(500, "failed to load order"))
	}
	return c.NoContent(200)
}
