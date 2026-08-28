package circuitruntime

import (
	"context"
	"fmt"
	"strings"

	goredis "github.com/redis/go-redis/v9"
)

type Client struct {
	client    *goredis.Client
	namespace string
}

func NewClient(rawURL, namespace string) (*Client, error) {
	if strings.TrimSpace(rawURL) == "" {
		return nil, fmt.Errorf("redis url is required")
	}
	namespace = strings.Trim(namespace, ":")
	if namespace == "" {
		return nil, fmt.Errorf("redis namespace is required")
	}
	opts, err := goredis.ParseURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	opts.ContextTimeoutEnabled = true
	return &Client{client: goredis.NewClient(opts), namespace: namespace}, nil
}

func (c *Client) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Close()
}
func (c *Client) Ping(ctx context.Context) error {
	if c == nil || c.client == nil {
		return fmt.Errorf("redis client is required")
	}
	return c.client.Ping(ctx).Err()
}
