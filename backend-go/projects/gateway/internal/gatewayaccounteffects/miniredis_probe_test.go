package gatewayaccounteffects

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestMiniRedisLuaSupportProbe(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	ctx := context.Background()
	result, err := client.Eval(ctx, `
local redisTime = redis.call('TIME')
local now = tonumber(redisTime[1]) * 1000 + math.floor(tonumber(redisTime[2]) / 1000)
local state = cjson.decode(ARGV[1])
state.lastObservedAtMs = now
redis.call('SET', KEYS[1], cjson.encode(state), 'PX', 1000)
redis.call('PUBLISH', KEYS[2], 'wake:1')
redis.call('ZADD', KEYS[3], now, 'hash')
local raw = redis.call('GET', KEYS[1])
local decoded = cjson.decode(raw)
return {'applied', raw, tostring(decoded.dispatchRevision)}
`, []string{"state:x", "events", "due"}, `{"capabilityHash":"abc","dispatchRevision":7,"phase":"OPEN"}`).Result()
	if err != nil {
		t.Fatalf("eval probe: %v", err)
	}
	array, ok := result.([]any)
	if !ok {
		t.Fatalf("expected array result, got %T", result)
	}
	t.Logf("probe result: %#v", array)
	if array[0] != "applied" {
		t.Fatalf("unexpected status %v", array[0])
	}
}
