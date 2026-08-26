// Package worker simulates reflection-based dependency injection (fx/wire
// style): the implementation is only referenced as a constructor value and
// the interface variable is populated at runtime, so VTA cannot see any
// allocation flowing into it. Extraction must fall back to CHA's unique
// implementation instead of losing the whole subtree behind a dynamic
// terminal.
package worker

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Syncer interface {
	Sync(ctx context.Context) error
}

type DBSyncer struct {
	pool *pgxpool.Pool
}

func NewDBSyncer(pool *pgxpool.Pool) *DBSyncer { return &DBSyncer{pool: pool} }

func (s *DBSyncer) Sync(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, "REFRESH MATERIALIZED VIEW order_stats")
	return err
}

// Provided simulates fx.Provide(NewDBSyncer): the constructor escapes as a
// value; nothing in analyzable code ever calls it.
var Provided = []any{NewDBSyncer}

// Active is populated by the DI container at runtime.
var Active Syncer

func Run(ctx context.Context) error {
	if Active == nil {
		return nil
	}
	return Active.Sync(ctx)
}
