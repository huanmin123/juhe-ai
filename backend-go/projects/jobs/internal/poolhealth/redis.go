package poolhealth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-platform/proxyresolve"
)

// RedisHealthWriter maintains the shared member-health projection read by the
// gateway (C7-R). It is not a gateway invalidation-bus key.
type RedisHealthWriter struct {
	client    redis.UniversalClient
	namespace string
	ttl       time.Duration
}

func NewRedisHealthWriter(client redis.UniversalClient, namespace string) (*RedisHealthWriter, error) {
	if client == nil {
		return nil, errors.New("pool health redis writer requires a client")
	}
	if strings.TrimSpace(namespace) == "" {
		return nil, errors.New("pool health redis writer requires a namespace")
	}
	return &RedisHealthWriter{client: client, namespace: strings.TrimSpace(namespace), ttl: HealthKeyTTL}, nil
}

func (w *RedisHealthWriter) Write(ctx context.Context, profileID string, state HealthState) error {
	key := proxyresolve.MemberHealthKey(w.namespace, profileID)
	if key == "" {
		return errors.New("proxy member health key is empty")
	}
	return w.client.Set(ctx, key, string(state), w.ttl).Err()
}

// Delete is the best-effort fallback when a refresh write fails: removing the
// stale key makes the gateway fall back to the PG fact source instead of
// trusting an outdated projection. Failure of the delete itself is returned so
// the caller can log it; the key then expires by TTL.
func (w *RedisHealthWriter) Delete(ctx context.Context, profileID string) error {
	key := proxyresolve.MemberHealthKey(w.namespace, profileID)
	if key == "" {
		return errors.New("proxy member health key is empty")
	}
	return w.client.Del(ctx, key).Err()
}
