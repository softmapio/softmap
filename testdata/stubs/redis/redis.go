// Package redis is a minimal stub of github.com/redis/go-redis/v9 carrying
// only the shapes devscan's effect detector keys on (internal/effects):
// package path + a Client-like receiver with exported command methods.
package redis

import (
	"context"
	"time"
)

type Options struct{ Addr string }

type Client struct{}

func NewClient(opt *Options) *Client { return &Client{} }

type StringCmd struct{}

func (c *StringCmd) Result() (string, error) { return "", nil }
func (c *StringCmd) Err() error              { return nil }

type StatusCmd struct{}

func (c *StatusCmd) Err() error { return nil }

func (c *Client) Get(ctx context.Context, key string) *StringCmd { return &StringCmd{} }
func (c *Client) Set(ctx context.Context, key string, value any, expiration time.Duration) *StatusCmd {
	return &StatusCmd{}
}
func (c *Client) Del(ctx context.Context, keys ...string) *StatusCmd { return &StatusCmd{} }
func (c *Client) Close() error                                       { return nil }
