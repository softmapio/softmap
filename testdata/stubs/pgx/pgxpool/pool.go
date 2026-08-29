// Package pgxpool is a minimal stub of github.com/jackc/pgx/v5/pgxpool; see
// the parent package comment.
package pgxpool

import (
	"context"

	pgx "github.com/jackc/pgx/v5"
)

type Pool struct{}

func New(ctx context.Context, connString string) (*Pool, error) { return &Pool{}, nil }

func (p *Pool) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return nil, nil
}
func (p *Pool) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row  { return nil }
func (p *Pool) Exec(ctx context.Context, sql string, args ...any) (any, error) { return nil, nil }
func (p *Pool) Close()                                                         {}
