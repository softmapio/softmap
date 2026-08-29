// Package service is the business-logic layer: the meaningful middle of
// every flow.
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"example.com/toyshop/dyn"
	"example.com/toyshop/events"
	"example.com/toyshop/gen"
	"example.com/toyshop/model"
	"example.com/toyshop/pkg/log"
	"example.com/toyshop/pkg/metrics"
	"example.com/toyshop/repo"
)

// ErrTooManyItems is a sentinel guard outcome: flow maps must surface it as
// an error exit of CreateOrder.
var ErrTooManyItems = errors.New("too many items in order")

type Service struct {
	repo      *repo.Repo
	producer  *events.Producer
	notifiers []Notifier
	log       *log.Logger
}

func New(r *repo.Repo, p *events.Producer, notifiers []Notifier, l *log.Logger) *Service {
	return &Service{repo: r, producer: p, notifiers: notifiers, log: l}
}

func (s *Service) CreateOrder(ctx context.Context, req *gen.OrderRequest) (*model.Order, error) {
	s.tracedLogger().Debug("creating order", "item", req.Item)
	metrics.Orders.Inc()

	if req.Qty > 100 {
		return nil, ErrTooManyItems
	}
	o := &model.Order{ID: newID(req), Item: req.Item, Qty: req.Qty}
	if err := s.repo.Save(ctx, o); err != nil {
		return nil, wrapErr("saving order", err)
	}
	if err := s.repo.CacheOrder(ctx, o); err != nil {
		s.log.Warn("cache write failed", "err", err)
	}
	if err := s.producer.OrderCreated(ctx, o); err != nil {
		return nil, wrapErr("publishing order", err)
	}
	s.notifyAll(o)
	go s.audit(ctx, o)
	// Inline goroutine closure, the shape real services use for
	// fire-and-forget work: the closure must collapse, leaving an async
	// edge straight to AuditLog.
	go func() {
		if err := s.repo.AuditLog(ctx, o, "closure"); err != nil {
			s.log.Error("audit failed", "err", err)
		}
	}()
	dyn.Publish("order.created", o)
	return o, nil
}

func (s *Service) GetOrder(ctx context.Context, id string) (*model.Order, error) {
	s.log.Debug("getting order", "id", id)
	if item, err := s.repo.CachedOrder(ctx, id); err == nil && item != "" {
		return &model.Order{ID: id, Item: item}, nil
	}
	return s.repo.FindOrder(ctx, id)
}

// FindByPhone resolves an order trying each legacy phone format in turn -
// the phone side of the either/or lookup fixture.
func (s *Service) FindByPhone(ctx context.Context, phone string) (*model.Order, error) {
	// normalizedPhone feeds the guard below: without guard-provenance
	// protection the pure helper would be dropped as an effect-free
	// subtree and the decision would point at nothing.
	norm := normalizedPhone(phone)
	if norm == "" {
		return nil, fmt.Errorf("телефон не распознан: %s", phone)
	}
	for _, variant := range []string{norm, "8" + norm} {
		if o, err := s.repo.FindOrder(ctx, variant); err == nil {
			return o, nil
		}
	}
	return nil, fmt.Errorf("order not found by phone %s", phone)
}

func (s *Service) notifyAll(o *model.Order) {
	for _, n := range s.notifiers {
		if err := n.Notify(o); err != nil {
			s.log.Error("notify failed", "err", err)
		}
	}
}

func (s *Service) audit(ctx context.Context, o *model.Order) {
	if err := s.repo.AuditLog(ctx, o, "created"); err != nil {
		s.log.Error("audit failed", "err", err)
	}
}

// normalizedPhone strips separators; pure transform, no effects below.
func normalizedPhone(phone string) string {
	return strings.TrimLeft(strings.TrimSpace(phone), "+")
}

func newID(req *gen.OrderRequest) string {
	return fmt.Sprintf("%s-%d", req.Item, req.Qty)
}

// opTimings is deliberate noise: a telemetry-shaped receiver living in a
// business package - the metrics heuristic must drop its methods by
// receiver name.
type opTimings struct {
	log *log.Logger
}

func (t *opTimings) flush() {
	t.log.Info("op timings")
}

// tracedLogger is deliberate noise: it is not named like a logger and lives
// in the service package, but it RETURNS a logger - the logger-factory
// heuristic must drop it.
func (s *Service) tracedLogger() *log.Logger {
	return s.log
}

// wrapErr is deliberate noise: the error-wrapper heuristic should collapse it.
func wrapErr(msg string, err error) error {
	return fmt.Errorf("%s: %w", msg, err)
}
