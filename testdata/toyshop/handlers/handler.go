// Package handlers is the HTTP layer: gin handlers registered in main.
package handlers

import (
	"context"
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"example.com/toyshop/config"
	"example.com/toyshop/events"
	"example.com/toyshop/gen"
	"example.com/toyshop/pkg/log"
	"example.com/toyshop/pkg/metrics"
	"example.com/toyshop/repo"
	"example.com/toyshop/service"
)

type Handler struct {
	svc *service.Service
	log *log.Logger
}

func New(cfg *config.Config) *Handler {
	pool, _ := pgxpool.New(context.Background(), cfg.DSN)
	cache := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	r := repo.New(pool, cache)
	p := events.New(cfg.Brokers)
	notifiers := []service.Notifier{
		&service.EmailNotifier{Endpoint: "http://mailer.internal/send"},
		&service.SlackNotifier{WebhookURL: "http://slack.internal/hook"},
	}
	l := log.New()
	return &Handler{svc: service.New(r, p, notifiers, l), log: l}
}

func (h *Handler) CreateOrder(c *gin.Context) {
	h.log.Info("POST /orders")
	metrics.Requests.Inc()

	var req gen.OrderRequest
	if err := c.BindJSON(&req); err != nil {
		c.Status(400)
		return
	}
	if err := validateOrder(&req); err != nil {
		c.String(422, err.Error())
		return
	}
	o, err := h.svc.CreateOrder(context.Background(), &req)
	if err != nil {
		c.Status(500)
		return
	}
	c.JSON(201, o)
}

func (h *Handler) ApproveOrder(c *gin.Context) {
	h.log.Info("POST /orders/:id/approve")
	if err := h.svc.ApproveOrder(context.Background(), c.Param("account"), c.Param("id")); err != nil {
		c.String(403, err.Error())
		return
	}
	c.Status(204)
}

func (h *Handler) GetOrder(c *gin.Context) {
	h.log.Info("GET /orders/:id")
	o, err := h.svc.GetOrder(context.Background(), c.Param("id"))
	if err != nil {
		c.Status(404)
		return
	}
	c.JSON(200, o)
}

// validateOrder is deliberate noise: the validation-helper heuristic should
// drop it (name matches validate*, returns only error, no effects beneath).
func validateOrder(req *gen.OrderRequest) error {
	if req.Item == "" {
		return errors.New("item is required")
	}
	if req.Qty <= 0 {
		return errors.New("qty must be positive")
	}
	return nil
}
