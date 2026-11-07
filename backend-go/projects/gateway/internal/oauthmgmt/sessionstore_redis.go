package oauthmgmt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	redis "github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
)

// redisSessionBackend stores OAuth authorize sessions in Redis so a callback
// may land on any gateway instance. Keys follow the shared Cluster-safe
// namespace shape: juhe-ai:{<namespace>}:oauth-session:<provider>:<sessionID>.
type redisSessionBackend struct {
	client redis.UniversalClient
	prefix string
}

func newRedisSessionBackend(client redis.UniversalClient, namespace string) (*redisSessionBackend, error) {
	if client == nil {
		return nil, errors.New("OAuth session Redis client 不能为空")
	}
	if strings.TrimSpace(namespace) == "" {
		return nil, errors.New("OAuth session Redis namespace 不能为空")
	}
	return &redisSessionBackend{
		client: client,
		prefix: rediscfg.TaggedPrefix(namespace) + "oauth-session:",
	}, nil
}

func (b *redisSessionBackend) key(key string) string {
	return b.prefix + key
}

// redisSessionOperationTimeout mirrors authsys.RedisStateStore's 3s budget:
// OAuth routes must fail fast instead of hanging a request on a sick Redis.
const redisSessionOperationTimeout = 3 * time.Second

func (b *redisSessionBackend) Set(ctx context.Context, key string, value json.RawMessage, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = time.Millisecond
	}
	runCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), redisSessionOperationTimeout)
	defer cancel()
	return b.client.Set(runCtx, b.key(key), []byte(value), ttl).Err()
}

func (b *redisSessionBackend) Get(ctx context.Context, key string) (json.RawMessage, error) {
	runCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), redisSessionOperationTimeout)
	defer cancel()
	value, err := b.client.Get(runCtx, b.key(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return json.RawMessage(value), nil
}

const compareDeleteSessionScript = `
local current = redis.call('GET', KEYS[1])
if not current then
  return 0
end
if current ~= ARGV[1] then
  return 0
end
redis.call('DEL', KEYS[1])
return 1
`

func (b *redisSessionBackend) CompareDelete(ctx context.Context, key string, expected json.RawMessage) (bool, error) {
	runCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), redisSessionOperationTimeout)
	defer cancel()
	result, err := b.client.Eval(runCtx, compareDeleteSessionScript,
		[]string{b.key(key)}, []byte(expected)).Int64()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// UseRedisSessionStore switches the OAuth session store to Redis. It must be
// called during composition before serving; a nil client is a startup error,
// never a silent fallback to the process-local map.
func (s *Store) UseRedisSessionStore(client redis.UniversalClient, namespace string) error {
	backend, err := newRedisSessionBackend(client, namespace)
	if err != nil {
		return fmt.Errorf("initialize OAuth Redis session store: %w", err)
	}
	s.sessions.setBackend(backend)
	return nil
}
