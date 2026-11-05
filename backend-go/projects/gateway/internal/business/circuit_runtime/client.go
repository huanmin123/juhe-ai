package circuitruntime

import (
	"context"
	"fmt"
	"strings"

	goredis "github.com/redis/go-redis/v9"
	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
)

type Client struct {
	client    goredis.UniversalClient
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
	client, err := rediscfg.NewUniversalClient(rediscfg.Config{URL: rawURL, ContextTimeoutEnabled: true})
	if err != nil {
		return nil, fmt.Errorf("create redis client: %w", err)
	}
	return &Client{client: client, namespace: namespace}, nil
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
