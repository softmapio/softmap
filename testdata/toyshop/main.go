// Command toyshop is the devscan test fixture: a toy service with an HTTP
// handler -> service -> repository chain that touches SQL, Redis, and Kafka,
// plus deliberate noise (logging, metrics, config, a trivial wrapper, an
// interface with two implementations, a generated-style file, and an
// unresolvable dynamic call). It is analyzed statically, never executed.
package main

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"example.com/toyshop/config"
	"example.com/toyshop/echoapi"
	"example.com/toyshop/handlers"
)

func main() {
	cfg := config.Load()
	h := handlers.New(cfg)

	r := gin.New()
	r.POST("/orders", h.CreateOrder)
	r.GET("/orders/:id", h.GetOrder)
	r.POST("/orders/:id/approve", h.ApproveOrder)

	http.HandleFunc("/healthz", healthz)
	echoapi.Register(nil, nil) // wiring reference so discovery sees the routes

	_ = r.Run(":8080")
}

func healthz(w http.ResponseWriter, req *http.Request) {
	w.WriteHeader(http.StatusOK)
}
