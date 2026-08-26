package service

import (
	"context"
	"errors"
	"fmt"

	"example.com/toyshop/model"
)

// ErrPriceInvalid guards product creation: a non-positive price is rejected
// with business meaning, not a bare validation error.
var ErrPriceInvalid = errors.New("price must be positive")

func (s *Service) ListProducts(ctx context.Context) ([]model.Product, error) {
	return s.repo.ListProducts(ctx)
}

func (s *Service) CreateProduct(ctx context.Context, name string, price int) (*model.Product, error) {
	if price <= 0 {
		return nil, ErrPriceInvalid
	}
	p := &model.Product{ID: fmt.Sprintf("p-%s", name), Name: name, Price: price}
	if err := s.repo.CreateProduct(ctx, p); err != nil {
		return nil, wrapErr("saving product", err)
	}
	return p, nil
}
