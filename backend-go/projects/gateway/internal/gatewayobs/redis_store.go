package gatewayobs

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"

	redis "github.com/redis/go-redis/v9"
)

// Redis 路由观测 store，逐行为对齐
// backend/src/modules/gateway/observability/routing-observability-redis-store.ts。
// Lua 脚本原样携带，保证迁移窗口内 Node/Go 写出相同的 Redis 载荷。

// RedisCommandClient mirrors the consumed Node RedisCommandClient surface
// (eval + sendCommand) so the store is mockable independently of the wire.
type RedisCommandClient interface {
	Eval(ctx context.Context, script string, keys []string, args ...string) (interface{}, error)
	SendCommand(ctx context.Context, args ...string) (interface{}, error)
}

// RedisCommandClientAdapter adapts go-redis to RedisCommandClient.
type RedisCommandClientAdapter struct {
	client *redis.Client
}

// NewRedisCommandClient builds the production adapter.
func NewRedisCommandClient(client *redis.Client) *RedisCommandClientAdapter {
	return &RedisCommandClientAdapter{client: client}
}

// Eval runs one Lua script.
func (adapter *RedisCommandClientAdapter) Eval(ctx context.Context, script string, keys []string, args ...string) (interface{}, error) {
	redisArgs := make([]interface{}, len(args))
	for index, arg := range args {
		redisArgs[index] = arg
	}
	return adapter.client.Eval(ctx, script, keys, redisArgs...).Result()
}

// SendCommand runs one raw command.
func (adapter *RedisCommandClientAdapter) SendCommand(ctx context.Context, args ...string) (interface{}, error) {
	redisArgs := make([]interface{}, len(args))
	for index, arg := range args {
		redisArgs[index] = arg
	}
	return adapter.client.Do(ctx, redisArgs...).Result()
}

// redisClientCache mirrors shared/redis-client.getRedisClient: one client per
// URL for the process lifetime.
var redisClientCache = struct {
	sync.Mutex
	clients map[string]*redis.Client
}{clients: make(map[string]*redis.Client)}

// GetRedisClient mirrors getRedisClient.
func GetRedisClient(ctx context.Context, redisURL string) (*redis.Client, error) {
	normalized := strings.TrimSpace(redisURL)
	if normalized == "" {
		return nil, errors.New("Redis 连接串不能为空")
	}
	redisClientCache.Lock()
	defer redisClientCache.Unlock()
	if client, ok := redisClientCache.clients[normalized]; ok {
		return client, nil
	}
	options, err := redis.ParseURL(normalized)
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(options)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, err
	}
	redisClientCache.clients[normalized] = client
	return client, nil
}

// RedisGatewayRoutingObservabilityStore mirrors
// RedisGatewayRoutingObservabilityStore. The client is injected (Mock-first);
// production wiring passes NewRedisCommandClient(GetRedisClient(redisUrl)).
type RedisGatewayRoutingObservabilityStore struct {
	redisURL string
	name     string
	key      string
	client   RedisCommandClient
}

// NewRedisGatewayRoutingObservabilityStore mirrors the constructor: the
// redisUrl is validated like Node (empty -> 缺少 Redis URL) even though the
// client itself is injected.
func NewRedisGatewayRoutingObservabilityStore(client RedisCommandClient, redisURL string, namespace string, name string) (*RedisGatewayRoutingObservabilityStore, error) {
	if strings.TrimSpace(redisURL) == "" {
		return nil, errors.New("performance routing observability 缺少 Redis URL")
	}
	if name == "" {
		name = "gateway-routing-observability"
	}
	safe, err := safeRoutingObservabilityName(name)
	if err != nil {
		return nil, err
	}
	key, err := RedisNamespacedKey(normalizedObservabilityNamespace(namespace), "juhe-ai:"+safe+":v1")
	if err != nil {
		return nil, err
	}
	return &RedisGatewayRoutingObservabilityStore{redisURL: redisURL, name: name, key: key, client: client}, nil
}

// normalizedObservabilityNamespace accepts the short namespace or a full
// juhe-ai prefixed namespace（与 gatewayhotquality.normalizedNamespace 同约定，
// 避免双前缀）。
func normalizedObservabilityNamespace(namespace string) string {
	normalized := strings.TrimRight(strings.TrimSpace(namespace), ":")
	if strings.HasPrefix(normalized, redisRootPrefix) {
		normalized = normalized[len(redisRootPrefix):]
	}
	return normalized
}

// Record mirrors record.
func (store *RedisGatewayRoutingObservabilityStore) Record(ctx context.Context, observation Observation, nowMs int64) error {
	return store.RecordBatch(ctx, []BatchEntry{{Observation: observation, Count: 1}}, nowMs)
}

// RecordBatch mirrors recordBatch: counts merge per metric key (first-seen
// order) and the whole batch commits inside the verbatim Lua script.
func (store *RedisGatewayRoutingObservabilityStore) RecordBatch(ctx context.Context, entries []BatchEntry, nowMs int64) error {
	if len(entries) == 0 {
		return nil
	}
	now, err := normalizedNow(nowMs)
	if err != nil {
		return err
	}
	arguments := []string{int64ToString(now), int64ToString(GatewayRoutingObservabilityMetricCapacity)}
	order := make([]string, 0, len(entries))
	counts := make(map[string]int64, len(entries))
	for _, entry := range entries {
		key := GatewayRoutingObservationMetricKey(entry.Observation)
		count, err := positiveCount(entry.Count)
		if err != nil {
			return err
		}
		if _, seen := counts[key]; !seen {
			order = append(order, key)
		}
		counts[key] = saturatedAdd(counts[key], count)
	}
	for _, key := range order {
		arguments = append(arguments, key, int64ToString(counts[key]))
	}
	client := store.client
	if client == nil {
		// Performance mode deliberately has no memory fallback: an
		// unavailable state Redis produces a rejected write/snapshot instead
		// of false local data.
		built, err := GetRedisClient(ctx, store.redisURL)
		if err != nil {
			return err
		}
		client = NewRedisCommandClient(built)
	}
	_, err = client.Eval(ctx, redisGatewayRoutingObservabilityRecordScript, []string{store.key}, arguments...)
	return err
}

// Snapshot mirrors snapshot: HGETALL with metric: field filtering and
// finiteCount normalization.
func (store *RedisGatewayRoutingObservabilityStore) Snapshot(ctx context.Context) (Snapshot, error) {
	raw, err := store.commandClient().SendCommand(ctx, "HGETALL", store.key)
	if err != nil {
		return Snapshot{}, err
	}
	hash := redisHash(raw)
	counters := make(map[string]int64)
	for key, value := range hash {
		if !strings.HasPrefix(key, "metric:") {
			continue
		}
		counters[key[len("metric:"):]] = finiteCount(value)
	}
	return Snapshot{
		Version:        1,
		RecordedEvents: finiteCount(hash["recordedEvents"]),
		UpdatedAtMs:    finiteCount(hash["updatedAtMs"]),
		Counters:       counters,
	}, nil
}

func (store *RedisGatewayRoutingObservabilityStore) commandClient() RedisCommandClient {
	if store.client != nil {
		return store.client
	}
	// Lazily mirrored from Node's private client(); GetRedisClient failure
	// surfaces at call time as a rejected snapshot.
	client, err := GetRedisClient(context.Background(), store.redisURL)
	if err != nil {
		return failedCommandClient{err}
	}
	adapter := NewRedisCommandClient(client)
	store.client = adapter
	return adapter
}

type failedCommandClient struct{ err error }

func (failed failedCommandClient) Eval(ctx context.Context, script string, keys []string, args ...string) (interface{}, error) {
	return nil, failed.err
}

func (failed failedCommandClient) SendCommand(ctx context.Context, args ...string) (interface{}, error) {
	return nil, failed.err
}

// redisGatewayRoutingObservabilityRecordScript mirrors
// redisGatewayRoutingObservabilityRecordScript verbatim.
const redisGatewayRoutingObservabilityRecordScript = `
local key = KEYS[1]
local now_ms = ARGV[1]
local capacity = tonumber(ARGV[2])
local new_fields = 0
for index = 3, #ARGV, 2 do
  if redis.call('HEXISTS', key, 'metric:' .. ARGV[index]) == 0 then new_fields = new_fields + 1 end
end
local existing_metric_fields = math.max(0, redis.call('HLEN', key) - 3)
if existing_metric_fields + new_fields > capacity then return redis.error_reply('routing observability metric capacity exhausted') end
local max_safe_integer = 9007199254740991
local function increment_saturated(field, increment)
  local current = tonumber(redis.call('HGET', key, field) or '0')
  redis.call('HSET', key, field, tostring(math.min(max_safe_integer, current + increment)))
end
local recorded = 0
for index = 3, #ARGV, 2 do
  local count = tonumber(ARGV[index + 1])
  increment_saturated('metric:' .. ARGV[index], count)
  recorded = math.min(max_safe_integer, recorded + count)
end
increment_saturated('recordedEvents', recorded)
local previous_updated_at_ms = tonumber(redis.call('HGET', key, 'updatedAtMs') or '0')
redis.call('HSET', key, 'updatedAtMs', tostring(math.max(previous_updated_at_ms, tonumber(now_ms))), 'version', '1')
return 1
`

// safeRoutingObservabilityName mirrors safeName.
func safeRoutingObservabilityName(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if !routingObservabilityNamePattern.MatchString(normalized) {
		return "", errors.New("routing observability name 非法")
	}
	return normalized, nil
}

var routingObservabilityNamePattern = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// finiteCount mirrors finiteCount: Number(value ?? 0) must be a non-negative
// safe integer, otherwise 0. JS Number() accepts decimal/exponent text (the
// only form the Lua script writes via tostring).
func finiteCount(value string) int64 {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return 0
	}
	parsed, err := strconv.ParseFloat(normalized, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0
	}
	if parsed < 0 || parsed > float64(MaxSafeInteger) || parsed != math.Trunc(parsed) {
		return 0
	}
	return int64(parsed)
}

// redisHash mirrors redisHash: normalize both go-redis map replies and raw
// array replies into a field map of string values.
func redisHash(value interface{}) map[string]string {
	result := make(map[string]string)
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, fieldValue := range typed {
			if text, ok := fieldValue.(string); ok {
				result[key] = text
			}
		}
	case map[interface{}]interface{}:
		for key, fieldValue := range typed {
			keyText, keyOK := key.(string)
			valueText, valueOK := fieldValue.(string)
			if keyOK && valueOK {
				result[keyText] = valueText
			}
		}
	case map[string]string:
		for key, fieldValue := range typed {
			result[key] = fieldValue
		}
	case []interface{}:
		for index := 0; index+1 < len(typed); index += 2 {
			key, keyOK := typed[index].(string)
			fieldValue, valueOK := typed[index+1].(string)
			if keyOK && valueOK {
				result[key] = fieldValue
			}
		}
	}
	return result
}

func int64ToString(value int64) string {
	return strconv.FormatInt(value, 10)
}
