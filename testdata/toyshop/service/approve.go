package service

import (
	"context"
	"errors"
	"fmt"
)

// ErrNoResellersAttached is the sentinel outcome of the first semantic
// guard in ApproveOrder; flow maps must render it as a decision + exit.
var ErrNoResellersAttached = errors.New("no resellers attached to account")

// ErrApprovalStopped is wrapped through a wordless "%w: %w" format — the
// exit must be classified as this sentinel, not as the message "…: …".
var ErrApprovalStopped = errors.New("approval stopped")

// ApproveOrder is the guards fixture: a data fetch feeding two semantic
// guards (a sentinel exit and a non-ASCII formatted message), several
// mechanical err != nil propagations that must NOT become decisions, and a
// guard that gates the Kafka publish.
func (s *Service) ApproveOrder(ctx context.Context, accountID, orderID string) error {
	t := &opTimings{log: s.log}
	defer t.flush()
	resellers := s.fetchResellers(ctx, accountID)
	if len(resellers) == 0 {
		s.log.Warn("approve rejected: no resellers", "account", accountID)
		return ErrNoResellersAttached // semantic guard: sentinel
	}
	if resellers[orderID] == "" {
		return fmt.Errorf("нет прав на заказ %s", orderID) // semantic guard: message
	}

	o, err := s.repo.FindOrder(ctx, orderID)
	if err != nil {
		return err // mechanical: bare propagation
	}
	if err := s.repo.CacheOrder(ctx, o); err != nil {
		return fmt.Errorf("cache approved order: %v", err) // mechanical: %v wrap
	}
	if _, err := s.repo.CachedOrder(ctx, "audit:"+orderID); err != nil && !errors.Is(err, ErrNoResellersAttached) {
		// semantic (compound condition), wordless wrap: sentinel identity
		return fmt.Errorf("%w: %w", err, ErrApprovalStopped)
	}

	if o.Qty > 10 {
		// gate fixture: neither branch exits — the condition only decides
		// whether the audit below runs. Flow maps must render it as a
		// decision with the gated call nested under it.
		if err := s.repo.AuditLog(ctx, o, "bulk"); err != nil {
			s.log.Warn("bulk audit failed", "err", err)
		}
	}
	if o.Qty <= 0 {
		return fmt.Errorf("approve of empty order %s", orderID) // semantic guard: gates the publish below
	}
	if err := s.producer.OrderCreated(ctx, o); err != nil {
		return fmt.Errorf("publish approval: %w", err) // mechanical: %w wrap
	}
	return nil
}

// fetchResellers is the provenance target: both guards above consume its
// result, so their `uses` must point here.
func (s *Service) fetchResellers(ctx context.Context, accountID string) map[string]string {
	val, err := s.repo.CachedOrder(ctx, "resellers:"+accountID)
	if err != nil || val == "" {
		return nil
	}
	return map[string]string{val: accountID}
}
