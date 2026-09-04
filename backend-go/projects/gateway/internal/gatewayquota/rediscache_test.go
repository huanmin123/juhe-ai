package gatewayquota

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

func TestRedisNamespacedKey(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		key       string
		want      string
		wantErr   string
	}{
		{name: "plain key", namespace: "dev", key: "abc", want: "juhe-ai:dev:abc"},
		{name: "juhe-ai root collapsed", namespace: "dev", key: "juhe-ai:state:gw:current", want: "juhe-ai:dev:state:gw:current"},
		{name: "full prefix kept", namespace: "dev", key: "juhe-ai:dev:state:gw:current", want: "juhe-ai:dev:state:gw:current"},
		{name: "namespace sanitized", namespace: "dev team!", key: "abc", want: "juhe-ai:dev_team:abc"},
		{name: "empty namespace rejected", namespace: "  ", key: "abc", wantErr: "Redis namespace 不能为空"},
		{name: "empty key rejected", namespace: "dev", key: "  ", wantErr: "Redis key 不能为空"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RedisNamespacedKey(tt.namespace, tt.key)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("key = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRedisRuntimeStateStore(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewRedisRuntimeStateStore(client, "dev", "gateway_quota_snapshot")
	if err != nil {
		t.Fatalf("NewRedisRuntimeStateStore: %v", err)
	}
	ctx := context.Background()

	type payload struct {
		Name string `json:"name"`
	}
	if err := store.SetJSON(ctx, "gateway_quota_snapshot", "current", payload{Name: "snap"}, time.Hour); err != nil {
		t.Fatalf("SetJSON: %v", err)
	}
	// The key layout must match Node: juhe-ai:<ns>:juhe-ai:state:<name>:<key>.
	if _, err := server.Get("juhe-ai:dev:state:gateway_quota_snapshot:current"); err != nil {
		t.Fatalf("expected Node-compatible key layout, miniredis says: %v", err)
	}
	var got payload
	found, err := store.GetJSON(ctx, "gateway_quota_snapshot", "current", &got)
	if err != nil || !found || got.Name != "snap" {
		t.Fatalf("GetJSON = (%v, %v, %+v)", found, err, got)
	}

	// Corrupt payloads read as absent and are deleted (mirrors Node).
	if err := client.Set(ctx, "juhe-ai:dev:state:gateway_quota_snapshot:broken", "{nope", 0).Err(); err != nil {
		t.Fatalf("seed corrupt: %v", err)
	}
	var broken payload
	if found, err := store.GetJSON(ctx, "gateway_quota_snapshot", "broken", &broken); err != nil || found {
		t.Fatalf("corrupt document must read as absent: (%v, %v)", found, err)
	}
	if exists := server.Exists("juhe-ai:dev:state:gateway_quota_snapshot:broken"); exists {
		t.Fatal("corrupt document must be deleted")
	}

	if err := store.Delete(ctx, "gateway_quota_snapshot", "current"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if found, err := store.GetJSON(ctx, "gateway_quota_snapshot", "current", &got); err != nil || found {
		t.Fatalf("deleted document must be absent: (%v, %v)", found, err)
	}
}

func TestRedisSharedCacheLayoutAndLifecycle(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache, err := NewRedisSharedCache(client, "dev", "gateway:api-key-quota")
	if err != nil {
		t.Fatalf("NewRedisSharedCache: %v", err)
	}
	ctx := context.Background()
	entry := CachedDecision{Allowed: false, Message: "额度已用完，请联系管理员提升额度", CheckedAtMs: 123}
	key := base64.RawURLEncoding.EncodeToString([]byte("runtime\x00key"))

	if err := cache.Set(ctx, key, entry, apiKeyQuotaCacheTTL); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Node-compatible layout: value key + sorted-set index + version key.
	version, err := client.Get(ctx, "juhe-ai:dev:cache-version:gateway:api-key-quota").Result()
	if err != nil || version == "" {
		t.Fatalf("version key missing: %v", err)
	}
	valueKey := "juhe-ai:dev:cache:gateway:api-key-quota:" + version + ":" + key
	raw, err := client.Get(ctx, valueKey).Result()
	if err != nil {
		t.Fatalf("value key missing: %v", err)
	}
	var decoded CachedDecision
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("payload must be plain JSON: %v", err)
	}
	if decoded.Message != "额度已用完，请联系管理员提升额度" {
		t.Fatalf("payload mismatch: %+v", decoded)
	}
	indexKey := "juhe-ai:dev:cache-index:gateway:api-key-quota:" + version
	if count, err := client.ZCard(ctx, indexKey).Result(); err != nil || count != 1 {
		t.Fatalf("index must track the value key, card=%v err=%v", count, err)
	}

	var out CachedDecision
	found, err := cache.Get(ctx, key, &out)
	if err != nil || !found || out.Allowed || out.CheckedAtMs != 123 {
		t.Fatalf("Get = (%v, %v, %+v)", found, err, out)
	}
	if found, err := cache.Get(ctx, base64.RawURLEncoding.EncodeToString([]byte("missing")), &out); err != nil || found {
		t.Fatalf("missing key = (%v, %v)", found, err)
	}

	// Clear removes the tracked value and rotates the version.
	if err := cache.Clear(ctx); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if exists := server.Exists(valueKey); exists {
		t.Fatal("clear must delete tracked values")
	}
	nextVersion, err := client.Get(ctx, "juhe-ai:dev:cache-version:gateway:api-key-quota").Result()
	if err != nil || nextVersion == "" || nextVersion == version {
		t.Fatalf("clear must rotate the version: old=%q new=%q err=%v", version, nextVersion, err)
	}
	// The old versioned entry is unreachable through the cache.
	if found, err := cache.Get(ctx, key, &out); err != nil || found {
		t.Fatalf("post-clear read = (%v, %v)", found, err)
	}
}

func TestRedisSharedCacheCorruptValue(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache, err := NewRedisSharedCache(client, "dev", "gateway:authorization-quota")
	if err != nil {
		t.Fatalf("NewRedisSharedCache: %v", err)
	}
	ctx := context.Background()
	if err := cache.Set(ctx, "k", CachedDecision{Allowed: true}, time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	version, _ := client.Get(ctx, "juhe-ai:dev:cache-version:gateway:authorization-quota").Result()
	if err := client.Set(ctx, "juhe-ai:dev:cache:gateway:authorization-quota:"+version+":k", "{bad", time.Minute).Err(); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	if found, err := cache.Get(ctx, "k", &CachedDecision{}); err != nil || found {
		t.Fatalf("corrupt value must read as absent: (%v, %v)", found, err)
	}
}

func TestRedisSharedCacheTrim(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache, err := NewRedisSharedCache(client, "dev", "trim-test")
	if err != nil {
		t.Fatalf("NewRedisSharedCache: %v", err)
	}
	ctx := context.Background()
	// apiKeyQuotaCacheMax trims the index overflow (oldest first).
	for i := 0; i < apiKeyQuotaCacheMax+5; i++ {
		if err := cache.Set(ctx, "k"+itoa(i), CachedDecision{Allowed: true}, time.Minute); err != nil {
			t.Fatalf("Set %d: %v", i, err)
		}
	}
	version, _ := client.Get(ctx, "juhe-ai:dev:cache-version:trim-test").Result()
	count, err := client.ZCard(ctx, "juhe-ai:dev:cache-index:trim-test:"+version).Result()
	if err != nil {
		t.Fatalf("ZCard: %v", err)
	}
	if count != int64(apiKeyQuotaCacheMax) {
		t.Fatalf("index card = %d, want %d", count, apiKeyQuotaCacheMax)
	}
	// The oldest entries were evicted, the newest survive.
	if found, err := cache.Get(ctx, "k0", &CachedDecision{}); err != nil || found {
		t.Fatalf("oldest entry must be trimmed: (%v, %v)", found, err)
	}
	if found, err := cache.Get(ctx, "k"+itoa(apiKeyQuotaCacheMax+4), &CachedDecision{}); err != nil || !found {
		t.Fatalf("newest entry must survive: (%v, %v)", found, err)
	}
	if strings.TrimSpace(version) == "" {
		t.Fatal("version must exist")
	}
}
