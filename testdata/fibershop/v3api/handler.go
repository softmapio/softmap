// Package v3api registers routes with Fiber v3, whose registration shape
// differs from v2: the endpoint handler is the first argument (middleware
// follows it), Add takes a list of methods, and Ctx is an interface.
package v3api

import fiber "github.com/gofiber/fiber/v3"

type Handler struct{}

func New() *Handler { return &Handler{} }

func (h *Handler) Status(c fiber.Ctx) error { return c.SendString("ok") }

func (h *Handler) GetOrder(c fiber.Ctx) error {
	return c.JSON(map[string]string{"id": c.Params("id")})
}

func (h *Handler) CreateOrder(c fiber.Ctx) error { return c.SendStatus(201) }

func (h *Handler) PatchOrder(c fiber.Ctx) error { return c.SendStatus(204) }

// Audit is middleware. In v3 it is registered AFTER the endpoint handler, so
// a matcher that assumed v2's "last handler wins" would pick this instead of
// the endpoint.
func Audit(c fiber.Ctx) error { return nil }

// Register wires the v3 routes.
func Register(app *fiber.App, h *Handler) {
	app.Get("/status", h.Status)

	// Endpoint first, middleware after — the v3 argument order.
	app.Get("/v3/orders/:id", h.GetOrder, Audit)

	// Add takes several methods at once in v3.
	app.Add([]string{"PATCH"}, "/v3/orders/:id", h.PatchOrder)

	// Group returns the Router interface: the registration below is an
	// interface-dispatched call whose prefix comes from the group.
	orders := app.Group("/v3/shop")
	orders.Post("/orders", h.CreateOrder)
}
