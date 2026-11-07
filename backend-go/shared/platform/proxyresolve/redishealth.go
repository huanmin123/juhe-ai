package proxyresolve

import (
	"context"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// MGetExecutor is the Redis subset used by RedisHealthReader. It is satisfied
// by go-redis clients and by test fakes.
type MGetExecutor interface {
	MGet(ctx context.Context, keys ...string) *redis.SliceCmd
}

// RedisHealthReader reads the jobs-maintained pool member health projection
// (proxy design §7.4/C7-R). Missing keys are omitted so the resolver falls back
// to proxy_profiles.pool_health_status; a bulk error is returned so the caller
// can fall back to PG instead of treating every member as dead.
type RedisHealthReader struct {
	client    MGetExecutor
	namespace string
	timeout   time.Duration
}

func NewRedisHealthReader(client MGetExecutor, namespace string) *RedisHealthReader {
	return &RedisHealthReader{client: client, namespace: strings.TrimSpace(namespace)}
}

// WithTimeout bounds each MGET (proxy design §7.4 suggests 100ms). A timeout
// is returned as an error so the resolver falls back to PG.
func (r *RedisHealthReader) WithTimeout(timeout time.Duration) *RedisHealthReader {
	if r == nil {
		return nil
	}
	r.timeout = timeout
	return r
}

// ReadMemberHealth returns only present, recognized states. An empty namespace,
// a missing client, or an empty ID list yields an empty map without an error:
// the resolver then uses the PG profile health (fail-open to the fact source,
// never to a direct dial).
func (r *RedisHealthReader) ReadMemberHealth(ctx context.Context, profileIDs []string) (map[string]HealthState, error) {
	out := make(map[string]HealthState, len(profileIDs))
	if r == nil || r.client == nil || r.namespace == "" {
		return out, nil
	}
	ids := uniqueNonEmpty(profileIDs)
	if len(ids) == 0 {
		return out, nil
	}
	keys := make([]string, 0, len(ids))
	keyToProfile := make(map[string]string, len(ids))
	for _, profileID := range ids {
		key := MemberHealthKey(r.namespace, profileID)
		if key == "" {
			continue
		}
		keys = append(keys, key)
		keyToProfile[key] = profileID
	}
	if len(keys) == 0 {
		return out, nil
	}
	if r.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}
	values, err := r.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	for index, key := range keys {
		if index >= len(values) {
			break
		}
		state := healthStateFromRedisValue(values[index])
		if state == "" {
			continue
		}
		out[keyToProfile[key]] = state
	}
	return out, nil
}

func healthStateFromRedisValue(value any) HealthState {
	var text string
	switch typed := value.(type) {
	case string:
		text = typed
	case []byte:
		text = string(typed)
	default:
		return ""
	}
	switch HealthState(strings.ToLower(strings.TrimSpace(text))) {
	case HealthHealthy:
		return HealthHealthy
	case HealthDegraded:
		return HealthDegraded
	case HealthDead:
		return HealthDead
	default:
		return ""
	}
}
