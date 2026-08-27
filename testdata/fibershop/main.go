// Command fibershop is the Fiber fixture: a toy service that registers routes
// through every shape the Fiber matcher handles — plain verbs, nested groups,
// a Route callback, Add, All, wildcards, and middleware that must not be
// mistaken for an endpoint.
package main

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v2"
	fiberv3 "github.com/gofiber/fiber/v3"

	"example.com/fibershop/api"
	"example.com/fibershop/v3api"
)

func healthz(c *fiber.Ctx) error { return c.SendString("ok") }

func serveAsset(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }

func main() {
	app := fiber.New()
	h := api.New()

	// Middleware registration is not an entrypoint.
	app.Use(api.RequireToken)

	app.Get("/healthz", healthz)

	// Middleware first, endpoint last — the v2 argument order.
	app.Post("/orders", api.RequireToken, h.CreateOrder)

	app.Add("PUT", "/orders/:id", h.UpdateOrder)
	app.All("/ping", h.Ping)
	app.Get("/files/*", h.DownloadFile)

	// Nested groups compose their prefixes: /api + /v1 + /orders/:id.
	shop := app.Group("/api")
	v1 := shop.Group("/v1")
	v1.Get("/orders/:id", h.GetOrder)

	// Route registers a subtree through a callback, like chi's.
	app.Route("/admin", func(r fiber.Router) {
		r.Post("/orders/:id/refund", h.RefundOrder)
	})

	// The callback can be a declared function or a method value instead of a
	// literal; an anonymous function is the only kind that records its own
	// referrers, so these reach the Route call a different way.
	app.Route("/staff", api.StaffRoutes)
	app.Route("/reports", h.ReportRoutes)

	// A router kept in a struct field, assigned once by a constructor.
	api.NewShelf(app.Group("/shelf")).Register()

	// Chained registration: the group prefix survives the calls in between.
	app.Group("/chain").Use(api.RequireToken).Get("/items", h.ListItems)

	// The same callback mounted under two prefixes: neither can be claimed,
	// and which one "wins" must never depend on iteration order.
	app.Route("/eu", api.RegionRoutes)
	app.Route("/us", api.RegionRoutes)

	// A router reached through a pointer alias — `type P = *Shelf` arrives as
	// an alias type, which must not fault the field chase.
	api.NewDepot(app.Group("/depot")).Register()

	// net/http is a brace-syntax router: a segment starting with ":" is an
	// ordinary literal there, not a parameter, and must survive verbatim.
	http.HandleFunc("GET /assets/:name", serveAsset)

	v3 := fiberv3.New()
	v3api.Register(v3, v3api.New())

	_ = app.Listen(":8080")
	_ = v3.Listen(":8081")
}
