// Package pgx is a minimal stub of github.com/jackc/pgx/v5 carrying only the
// shapes softmap's SQL effect detector keys on (internal/effects).
package pgx

type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Close()
}

type Row interface {
	Scan(dest ...any) error
}
