// Package minio is a stub with the object-storage surface the storage
// effect detector matches.
package minio

import (
	"context"
	"net/url"
	"time"
)

type Client struct{}

func New(endpoint string) (*Client, error) { return &Client{}, nil }

func (c *Client) PresignedGetObject(ctx context.Context, bucket, object string, expiry time.Duration, params url.Values) (*url.URL, error) {
	return &url.URL{}, nil
}

func (c *Client) PutObject(ctx context.Context, bucket, object string, size int64) error {
	return nil
}
