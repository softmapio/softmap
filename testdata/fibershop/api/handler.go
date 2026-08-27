// Package api holds the Fiber v2 handlers of the fixture service. Handlers
// are trivial: this fixture exercises entrypoint discovery, not flow shape.
package api

import fiber "github.com/gofiber/fiber/v2"

type Handler struct{}

func New() *Handler { return &Handler{} }

func (h *Handler) GetOrder(c *fiber.Ctx) error {
	return c.JSON(map[string]string{"id": c.Params("id")})
}

func (h *Handler) CreateOrder(c *fiber.Ctx) error {
	var body map[string]any
	if err := c.BodyParser(&body); err != nil {
		return c.SendStatus(400)
	}
	return c.SendStatus(201)
}

func (h *Handler) UpdateOrder(c *fiber.Ctx) error { return c.SendStatus(204) }

func (h *Handler) RefundOrder(c *fiber.Ctx) error {
	return c.JSON(map[string]string{"refunded": c.Params("id")})
}

func (h *Handler) Ping(c *fiber.Ctx) error { return c.SendString("pong") }

func (h *Handler) DownloadFile(c *fiber.Ctx) error { return c.SendStatus(200) }

func (h *Handler) ListItems(c *fiber.Ctx) error { return c.SendStatus(200) }

func (h *Handler) ListStaff(c *fiber.Ctx) error { return c.SendStatus(200) }

func (h *Handler) DailyReport(c *fiber.Ctx) error { return c.SendStatus(200) }

func (h *Handler) ShelfItem(c *fiber.Ctx) error { return c.SendStatus(200) }

// StaffRoutes is a declared function used as a Route callback.
func StaffRoutes(r fiber.Router) {
	r.Get("/list", New().ListStaff)
}

// ReportRoutes is a method value used as a Route callback.
func (h *Handler) ReportRoutes(r fiber.Router) {
	r.Get("/daily", h.DailyReport)
}

// Shelf keeps its router in a struct field, assigned once in the
// constructor — the router does not reach Register through a local.
type Shelf struct {
	router  fiber.Router
	handler *Handler
}

func NewShelf(r fiber.Router) *Shelf { return &Shelf{router: r, handler: New()} }

func (s *Shelf) Register() {
	s.router.Get("/items/:id", s.handler.ShelfItem)
}

func (h *Handler) ListRegion(c *fiber.Ctx) error { return c.SendStatus(200) }

func (h *Handler) DepotItem(c *fiber.Ctx) error { return c.SendStatus(200) }

// RegionRoutes is mounted under more than one prefix, so no single prefix
// describes its routes.
func RegionRoutes(r fiber.Router) {
	r.Get("/region", New().ListRegion)
}

// Depot is reached through a pointer alias, so its field type arrives as an
// alias rather than a pointer.
type Depot struct {
	router  fiber.Router
	handler *Handler
}

// DepotRef aliases the pointer type.
type DepotRef = *Depot

func NewDepot(r fiber.Router) DepotRef { return &Depot{router: r, handler: New()} }

func (d DepotRef) Register() {
	d.router.Get("/crates/:id", d.handler.DepotItem)
}

// RequireToken is middleware, never an entrypoint: it is registered before
// the endpoint handler on the same route, and through Use.
func RequireToken(c *fiber.Ctx) error { return nil }
