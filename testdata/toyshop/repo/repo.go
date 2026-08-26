// Package repo holds the effect-heavy repository layer: pgx SQL calls and
// go-redis calls that must surface as effect nodes.
package repo

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"example.com/toyshop/model"
)

type Repo struct {
	pool  *pgxpool.Pool
	cache *redis.Client
}

func New(pool *pgxpool.Pool, cache *redis.Client) *Repo {
	return &Repo{pool: pool, cache: cache}
}

func (r *Repo) save(ctx context.Context, o *model.Order) error {
	_, err := r.pool.Exec(ctx, "INSERT INTO orders (id, item, qty) VALUES ($1, $2, $3)", o.ID, o.Item, o.Qty)
	if err != nil {
		return err
	}
	// order_items is the satellite-table fixture: the entity shelf must fold
	// it into "order" by prefix, not shelve it as its own entity.
	_, err = r.pool.Exec(ctx, "INSERT INTO order_items (order_id, item, qty) VALUES ($1, $2, $3)", o.ID, o.Item, o.Qty)
	return err
}

func (r *Repo) ListProducts(ctx context.Context) ([]model.Product, error) {
	rows, err := r.pool.Query(ctx, "SELECT id, name, price FROM products ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Product
	for rows.Next() {
		var p model.Product
		if err := rows.Scan(&p.ID, &p.Name, &p.Price); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func (r *Repo) CreateProduct(ctx context.Context, p *model.Product) error {
	_, err := r.pool.Exec(ctx, "INSERT INTO products (id, name, price) VALUES ($1, $2, $3)", p.ID, p.Name, p.Price)
	return err
}

func (r *Repo) FindOrder(ctx context.Context, id string) (*model.Order, error) {
	var o model.Order
	row := r.pool.QueryRow(ctx, "SELECT id, item, qty FROM orders WHERE id = $1", id)
	if err := row.Scan(&o.ID, &o.Item, &o.Qty); err != nil {
		return nil, err
	}
	return &o, nil
}

func (r *Repo) CacheOrder(ctx context.Context, o *model.Order) error {
	return r.cache.Set(ctx, "order:"+o.ID, o.Item, time.Hour).Err()
}

func (r *Repo) CachedOrder(ctx context.Context, id string) (string, error) {
	return r.cache.Get(ctx, "order:"+id).Result()
}

func (r *Repo) AuditLog(ctx context.Context, o *model.Order, event string) error {
	_, err := r.pool.Exec(ctx, "INSERT INTO audit (order_id, event) VALUES ($1, $2)", o.ID, event)
	return err
}
