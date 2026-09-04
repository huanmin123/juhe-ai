package gatewayproxyhealth

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	redis "github.com/redis/go-redis/v9"
)

func TestMemoryRuntimeStateStoreSemantics(t *testing.T) {
	clock := newFakeClock(1_000)
	store := NewMemoryRuntimeStateStore(clock.Now)
	ctx := contextBackground()

	// Set/get roundtrip.
	if err := store.SetJSON(ctx, "k", map[string]any{"a": 1}, 1_000); err != nil {
		t.Fatal(err)
	}
	raw, err := store.GetJSON(ctx, "k")
	if err != nil || raw == nil {
		t.Fatalf("get = %s err=%v", raw, err)
	}

	// CAS with expected=nil requires absence.
	applied, err := store.CompareSetJSON(ctx, "k", nil, map[string]any{"b": 2}, 1_000)
	if err != nil || applied {
		t.Fatalf("CAS on existing key must fail: %v err=%v", applied, err)
	}

	// CAS with the exact stored bytes applies.
	applied, err = store.CompareSetJSON(ctx, "k", raw, map[string]any{"b": 2}, 1_000)
	if err != nil || !applied {
		t.Fatalf("CAS with exact bytes must apply: %v err=%v", applied, err)
	}

	// Compare-delete removes only on exact match.
	applied, err = store.CompareDeleteJSON(ctx, "k", json.RawMessage(`{"b":2}`))
	if err != nil || !applied {
		t.Fatalf("compare-delete = %v err=%v", applied, err)
	}
	if again, err := store.GetJSON(ctx, "k"); err != nil || again != nil {
		t.Fatalf("value must be gone: %s err=%v", again, err)
	}

	// Expiry is enforced against the injected clock.
	_ = store.SetJSON(ctx, "t", 1, 500)
	clock.Advance(501)
	if value, err := store.GetJSON(ctx, "t"); err != nil || value != nil {
		t.Fatalf("expired entry = %s err=%v", value, err)
	}
}

func TestMemoryRuntimeStateStoreLocks(t *testing.T) {
	clock := newFakeClock(1_000)
	store := NewMemoryRuntimeStateStore(clock.Now)
	ctx := contextBackground()

	if locked, err := store.AcquireLock(ctx, "l", 1_000, "token-a"); err != nil || !locked {
		t.Fatalf("acquire = %v err=%v", locked, err)
	}
	if locked, _ := store.AcquireLock(ctx, "l", 1_000, "token-b"); locked {
		t.Fatal("second acquire must fail")
	}
	if renewed, err := store.RenewLock(ctx, "l", 1_000, "token-b"); err != nil || renewed {
		t.Fatalf("wrong-token renew = %v err=%v", renewed, err)
	}
	if renewed, err := store.RenewLock(ctx, "l", 1_000, "token-a"); err != nil || !renewed {
		t.Fatalf("renew = %v err=%v", renewed, err)
	}
	if err := store.ReleaseLock(ctx, "l", "token-b"); err != nil {
		t.Fatal(err)
	}
	if locked, _ := store.AcquireLock(ctx, "l", 1_000, "token-b"); locked {
		t.Fatal("release with the wrong token must not free the lock")
	}
	if err := store.ReleaseLock(ctx, "l", "token-a"); err != nil {
		t.Fatal(err)
	}
	if locked, _ := store.AcquireLock(ctx, "l", 1_000, "token-b"); !locked {
		t.Fatal("release with the right token must free the lock")
	}

	// Locks expire like every other entry.
	clock.Advance(1_001)
	if locked, _ := store.AcquireLock(ctx, "l", 1_000, "token-c"); !locked {
		t.Fatal("expired lock must be acquirable")
	}
}

// fakeRedisClient implements redisStateClient with canned go-redis commands.
type fakeRedisClient struct {
	data  map[string]string
	calls []string
}

func newFakeRedisClient() *fakeRedisClient {
	return &fakeRedisClient{data: map[string]string{}}
}

func (c *fakeRedisClient) record(op string) { c.calls = append(c.calls, op) }

func (c *fakeRedisClient) Get(_ context.Context, key string) *redis.StringCmd {
	c.record("GET")
	cmd := redis.NewStringCmd(context.Background())
	if value, ok := c.data[key]; ok {
		cmd.SetVal(value)
	} else {
		cmd.SetErr(redis.Nil)
	}
	return cmd
}

func (c *fakeRedisClient) Set(_ context.Context, key string, value any, _ time.Duration) *redis.StatusCmd {
	c.record("SET")
	switch encoded := value.(type) {
	case string:
		c.data[key] = encoded
	case []byte:
		c.data[key] = string(encoded)
	}
	cmd := redis.NewStatusCmd(context.Background())
	cmd.SetVal("OK")
	return cmd
}

func (c *fakeRedisClient) SetNX(_ context.Context, key string, value any, _ time.Duration) *redis.BoolCmd {
	c.record("SETNX")
	cmd := redis.NewBoolCmd(context.Background())
	encoded := ""
	switch value.(type) {
	case string:
		encoded = value.(string)
	case []byte:
		encoded = string(value.([]byte))
	}
	if _, exists := c.data[key]; exists {
		cmd.SetVal(false)
	} else {
		c.data[key] = encoded
		cmd.SetVal(true)
	}
	return cmd
}

func (c *fakeRedisClient) Del(_ context.Context, keys ...string) *redis.IntCmd {
	c.record("DEL")
	cmd := redis.NewIntCmd(context.Background())
	removed := int64(0)
	for _, key := range keys {
		if _, ok := c.data[key]; ok {
			delete(c.data, key)
			removed++
		}
	}
	cmd.SetVal(removed)
	return cmd
}

func (c *fakeRedisClient) MGet(_ context.Context, keys ...string) *redis.SliceCmd {
	c.record("MGET")
	cmd := redis.NewSliceCmd(context.Background())
	values := make([]any, len(keys))
	for i, key := range keys {
		if value, ok := c.data[key]; ok {
			values[i] = value
		}
	}
	cmd.SetVal(values)
	return cmd
}

func (c *fakeRedisClient) Eval(_ context.Context, script string, keys []string, args ...any) *redis.Cmd {
	c.record("EVAL:" + firstLine(script))
	cmd := redis.NewCmd(context.Background())
	switch {
	case strings.Contains(script, "redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[3])"):
		current, exists := c.data[keys[0]]
		expected := args[0].(string)
		if expected == "" {
			if exists {
				cmd.SetVal(int64(0))
				return cmd
			}
		} else if !exists || current != expected {
			cmd.SetVal(int64(0))
			return cmd
		}
		c.data[keys[0]] = args[1].(string)
		cmd.SetVal(int64(1))
	case strings.Contains(script, "return redis.call('DEL', KEYS[1])"):
		current, exists := c.data[keys[0]]
		if !exists || current != args[0].(string) {
			cmd.SetVal(int64(0))
			return cmd
		}
		delete(c.data, keys[0])
		cmd.SetVal(int64(1))
	case strings.Contains(script, "PEXPIRE', KEYS[1], ARGV[2]"):
		current, exists := c.data[keys[0]]
		if !exists || current != args[0].(string) {
			cmd.SetVal(int64(0))
			return cmd
		}
		cmd.SetVal(int64(1))
	case strings.Contains(script, "return redis.call('DEL', KEYS[1])\nend\nreturn 0"):
		cmd.SetVal(int64(0))
	default:
		cmd.SetErr(errors.New("unsupported script"))
	}
	return cmd
}

func firstLine(script string) string {
	lines := strings.Split(strings.TrimSpace(script), "\n")
	return strings.TrimSpace(lines[0])
}

func TestRedisRuntimeStateStoreDriver(t *testing.T) {
	client := newFakeRedisClient()
	store, err := NewRedisRuntimeStateStore(client, "dev", "gateway-upstream-bucket-health")
	if err != nil {
		t.Fatal(err)
	}
	ctx := contextBackground()

	// Key layout: juhe-ai:<namespace>:state:<name>:<key>.
	if err := store.SetJSON(ctx, "bucket:abc", map[string]any{"v": 1}, 60_000); err != nil {
		t.Fatal(err)
	}
	for key := range client.data {
		if !strings.HasPrefix(key, "juhe-ai:dev:state:gateway-upstream-bucket-health:bucket:abc") {
			t.Fatalf("redis key = %q", key)
		}
	}

	raw, err := store.GetJSON(ctx, "bucket:abc")
	if err != nil || raw == nil {
		t.Fatalf("get = %s err=%v", raw, err)
	}
	if applied, err := store.CompareSetJSON(ctx, "bucket:abc", raw, map[string]any{"v": 2}, 60_000); err != nil || !applied {
		t.Fatalf("CAS = %v err=%v", applied, err)
	}
	if applied, _ := store.CompareSetJSON(ctx, "bucket:abc", raw, map[string]any{"v": 3}, 60_000); applied {
		t.Fatal("stale expected bytes must not apply")
	}
	if applied, err := store.CompareDeleteJSON(ctx, "bucket:abc", json.RawMessage(`{"v":2}`)); err != nil || !applied {
		t.Fatalf("compare-delete = %v err=%v", applied, err)
	}

	// Locks.
	if locked, err := store.AcquireLock(ctx, "lock", 1_000, "t1"); err != nil || !locked {
		t.Fatalf("acquire = %v err=%v", locked, err)
	}
	if renewed, err := store.RenewLock(ctx, "lock", 1_000, "t1"); err != nil || !renewed {
		t.Fatalf("renew = %v err=%v", renewed, err)
	}
	if err := store.ReleaseLock(ctx, "lock", "t1"); err != nil {
		t.Fatal(err)
	}

	// Malformed JSON reads as absent and is cleaned up.
	client.data["juhe-ai:dev:state:gateway-upstream-bucket-health:bad"] = "{not-json"
	if value, err := store.GetJSON(ctx, "bad"); err != nil || value != nil {
		t.Fatalf("malformed value = %s err=%v", value, err)
	}
	if _, exists := client.data["juhe-ai:dev:state:gateway-upstream-bucket-health:bad"]; exists {
		t.Fatal("malformed value must be deleted")
	}

	// MGET batch decode.
	if err := store.SetJSON(ctx, "a", 1, 60_000); err != nil {
		t.Fatal(err)
	}
	if err := store.SetJSON(ctx, "b", 2, 60_000); err != nil {
		t.Fatal(err)
	}
	values, err := store.GetJSONMany(ctx, []string{"a", "missing", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 3 || values[0] == nil || values[1] != nil || values[2] == nil {
		t.Fatalf("many = %v", values)
	}
}

func TestNamespacedRedisKey(t *testing.T) {
	tests := []struct {
		namespace string
		key       string
		expected  string
	}{
		{namespace: "dev", key: "state:x:", expected: "juhe-ai:dev:state:x:"},
		// Node never strips juhe-ai: from the namespace value itself; a
		// namespace carrying the root prefix genuinely double-prefixes.
		{namespace: "juhe-ai:dev", key: "state:x:", expected: "juhe-ai:juhe-ai:dev:state:x:"},
		{namespace: "juhe-ai", key: "state:x:", expected: "juhe-ai:juhe-ai:state:x:"},
	}
	for _, tt := range tests {
		if got := namespacedRedisKey(tt.namespace, tt.key); got != tt.expected {
			t.Fatalf("namespacedRedisKey(%q, %q) = %q, want %q", tt.namespace, tt.key, got, tt.expected)
		}
	}
}

// The Lua scripts must stay byte-identical with the Node drivers so both
// stacks interoperate on one Redis during the migration window.
func TestLuaScriptsMatchNodeSources(t *testing.T) {
	nodeCompareSet := `
local current = redis.call('GET', KEYS[1])
if ARGV[1] == '' then
  if current then
    return 0
  end
elseif current ~= ARGV[1] then
  return 0
end
redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[3])
return 1
`
	nodeCompareDelete := `
local current = redis.call('GET', KEYS[1])
if not current or current ~= ARGV[1] then
  return 0
end
return redis.call('DEL', KEYS[1])
`
	nodeRenew := `
if redis.call('GET', KEYS[1]) ~= ARGV[1] then
  return 0
end
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1
`
	nodeRelease := `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`
	if redisCompareSetJSONScript != nodeCompareSet {
		t.Fatal("compareSetJson script drifted from the Node source")
	}
	if redisCompareDeleteJSONScript != nodeCompareDelete {
		t.Fatal("compareDeleteJson script drifted from the Node source")
	}
	if redisRenewLockScript != nodeRenew {
		t.Fatal("renewLock script drifted from the Node source")
	}
	if redisReleaseLockScript != nodeRelease {
		t.Fatal("releaseLock script drifted from the Node source")
	}
	if !strings.Contains(UserRequestLimitRedisSyncScript, "redis.call('HINCRBY', KEYS[index], '__total', delta)") {
		t.Fatal("user request limit sync script drifted from the Node source")
	}
	if !strings.Contains(redisPenaltyWindowRateLimitScript, "if blocked_until_ms > now_ms or count >= max_requests then") {
		t.Fatal("penalty window script drifted from the Node source")
	}
	if !strings.Contains(redisPenaltyWindowRateLimitGroupsScript, "return {0, blocked_retry_ms, blocked_rule_index, blocked_group_index}") {
		t.Fatal("penalty window groups script drifted from the Node source")
	}
}
