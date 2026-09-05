package inval

import (
	"context"
	"strings"

	redis "github.com/redis/go-redis/v9"
)

// publishVersionScript moves the stored topic version forward only: the
// Lua max() compare-and-set keeps the cluster-wide monotonic order even when
// instances publish concurrently or start from different local counters, and
// the effective stored version returns to the caller so the Bus can adopt it
// (the SharedStore monotonic contract).
var publishVersionScript = redis.NewScript(`
local current = tonumber(redis.call('GET', KEYS[1]))
if current == nil or current == false then
  current = 0
end
local proposed = tonumber(ARGV[1])
if proposed == nil or proposed <= current then
  return current
end
redis.call('SET', KEYS[1], proposed)
return proposed
`)

// RedisSharedStore is the inval.SharedStore over one Redis instance (the
// runtime-state driver, mirroring the Node topology choice of publishing
// invalidation versions on the runtime-state Redis, not the cache Redis).
//
// Key layout: `<namespace>:inval:topic-version:<topic>` (Go-only protocol —
// see the package comment for the non-interoperability decision with the
// archived Node `{version:"<millis>-<rand>"}` JSON format). Keys carry no
// TTL: the versions are tiny monotonic counters and an expiry would reset the
// cluster baseline (Node needed the 24h TTL only because its opaque version
// strings are deduplicated by equality, not ordered).
type RedisSharedStore struct {
	client    redis.Cmdable
	namespace string
}

// NewRedisSharedStore builds the shared store over an existing client (the
// chain composition passes the runtime-state redis client; tests pass
// miniredis clients on both sides).
func NewRedisSharedStore(client redis.Cmdable, namespace string) *RedisSharedStore {
	return &RedisSharedStore{client: client, namespace: namespace}
}

// key renders the namespaced per-topic version key. An empty namespace keeps
// the bare juhe-ai root exactly once (no juhe-ai:juhe-ai doubling).
func (s *RedisSharedStore) key(topic string) string {
	normalized := strings.TrimRight(strings.TrimSpace(s.namespace), ":")
	if normalized == "" {
		return "juhe-ai:inval:topic-version:" + topic
	}
	if !strings.HasPrefix(normalized, "juhe-ai:") {
		normalized = "juhe-ai:" + normalized
	}
	return normalized + ":inval:topic-version:" + topic
}

// GetVersion returns the persisted version; a missing key reads as 0 (the
// redis.Nil absence is the documented empty-store contract, not an error).
func (s *RedisSharedStore) GetVersion(ctx context.Context, topic string) (int64, error) {
	version, err := s.client.Get(ctx, s.key(topic)).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	return version, err
}

// PublishVersion stores max(current, version) atomically and returns the
// effective stored version.
func (s *RedisSharedStore) PublishVersion(ctx context.Context, topic string, version int64) (int64, error) {
	result, err := publishVersionScript.Run(ctx, s.client, []string{s.key(topic)}, version).Int64()
	if err != nil {
		return 0, err
	}
	return result, nil
}
