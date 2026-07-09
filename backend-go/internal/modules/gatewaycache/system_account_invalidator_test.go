package gatewaycache

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestNodeCompatibleRedisKeys(t *testing.T) {
	versionKey, err := SharedCacheVersionKey("juhe-ai", APIKeyValidationCacheName)
	if err != nil {
		t.Fatalf("SharedCacheVersionKey() error = %v", err)
	}
	if versionKey != "juhe-ai:juhe-ai:cache-version:gateway:api-key-validation" {
		t.Fatalf("version key = %q", versionKey)
	}

	stateKey, err := RuntimeStateKey("juhe-ai", RuntimeInvalidationStoreName, "topic:"+GatewayRuntimeCacheTopic)
	if err != nil {
		t.Fatalf("RuntimeStateKey() error = %v", err)
	}
	if stateKey != "juhe-ai:juhe-ai:state:gateway_cache_invalidation:topic:gateway_runtime_cache" {
		t.Fatalf("state key = %q", stateKey)
	}
	quotaStateKey, err := RuntimeStateKey("juhe-ai", RuntimeInvalidationStoreName, "topic:"+AuthorizationQuotaCacheTopic)
	if err != nil {
		t.Fatalf("authorization quota RuntimeStateKey() error = %v", err)
	}
	if quotaStateKey != "juhe-ai:juhe-ai:state:gateway_cache_invalidation:topic:authorization_quota_cache" {
		t.Fatalf("authorization quota state key = %q", quotaStateKey)
	}

	sanitizedKey, err := SharedCacheVersionKey(" prod west/1 ", "gateway/api key")
	if err != nil {
		t.Fatalf("SharedCacheVersionKey() with unsafe namespace error = %v", err)
	}
	if sanitizedKey != "juhe-ai:prod_west_1:cache-version:gateway_api_key" {
		t.Fatalf("sanitized version key = %q", sanitizedKey)
	}
}

func TestSystemAccountInvalidatorStatusChangedWritesNodeCompatibleFacts(t *testing.T) {
	cache := &rawSetRecorder{}
	state := &rawSetRecorder{}
	now := time.Date(2026, 7, 8, 12, 34, 56, 789*int(time.Millisecond), time.UTC)
	versions := versionSequence("cache-version", "state-version")
	invalidator, err := NewSystemAccountInvalidator(SystemAccountInvalidatorOptions{
		Cache:      cache,
		State:      state,
		Namespace:  "test-ns",
		Now:        func() time.Time { return now },
		NewVersion: versions,
	})
	if err != nil {
		t.Fatalf("NewSystemAccountInvalidator() error = %v", err)
	}

	if err := invalidator.InvalidateSystemAccountStatusChanged(context.Background(), "sys_user"); err != nil {
		t.Fatalf("InvalidateSystemAccountStatusChanged() error = %v", err)
	}

	if len(cache.calls) != 1 {
		t.Fatalf("cache calls = %d, want 1", len(cache.calls))
	}
	if cache.calls[0].key != "juhe-ai:test-ns:cache-version:gateway:api-key-validation" {
		t.Fatalf("cache key = %q", cache.calls[0].key)
	}
	if string(cache.calls[0].value) != "cache-version" || cache.calls[0].ttl != SharedCacheVersionTTL {
		t.Fatalf("cache value/ttl = %q / %v", string(cache.calls[0].value), cache.calls[0].ttl)
	}

	if len(state.calls) != 1 {
		t.Fatalf("state calls = %d, want 1", len(state.calls))
	}
	if state.calls[0].key != "juhe-ai:test-ns:state:gateway_cache_invalidation:topic:gateway_runtime_cache" {
		t.Fatalf("state key = %q", state.calls[0].key)
	}
	if state.calls[0].ttl != RuntimeStateTTL {
		t.Fatalf("state ttl = %v, want %v", state.calls[0].ttl, RuntimeStateTTL)
	}
	var payload runtimeInvalidationState
	if err := json.Unmarshal(state.calls[0].value, &payload); err != nil {
		t.Fatalf("state payload json = %s: %v", state.calls[0].value, err)
	}
	if payload.Version != "state-version" ||
		payload.Reason != SystemAccountStatusChangedReason ||
		payload.PublishedAt != "2026-07-08T12:34:56.789Z" ||
		payload.APIKeyID != "" {
		t.Fatalf("state payload = %+v", payload)
	}
}

func TestSystemAccountInvalidatorImageChangedUsesImageReason(t *testing.T) {
	cache := &rawSetRecorder{}
	state := &rawSetRecorder{}
	invalidator, err := NewSystemAccountInvalidator(SystemAccountInvalidatorOptions{
		Cache:      cache,
		State:      state,
		Namespace:  "test-ns",
		Now:        func() time.Time { return time.Unix(0, 0).UTC() },
		NewVersion: versionSequence("cache-version", "state-version"),
	})
	if err != nil {
		t.Fatalf("NewSystemAccountInvalidator() error = %v", err)
	}

	if err := invalidator.InvalidateSystemAccountImageGenerationChanged(context.Background(), "sys_user"); err != nil {
		t.Fatalf("InvalidateSystemAccountImageGenerationChanged() error = %v", err)
	}

	var payload runtimeInvalidationState
	if err := json.Unmarshal(state.calls[0].value, &payload); err != nil {
		t.Fatalf("state payload json = %s: %v", state.calls[0].value, err)
	}
	if payload.Reason != SystemAccountImageGenerationChangedReason {
		t.Fatalf("reason = %q, want %q", payload.Reason, SystemAccountImageGenerationChangedReason)
	}
}

func TestSystemAccountInvalidatorAuthorizationChangedWritesRuntimeAndQuotaTopics(t *testing.T) {
	cache := &rawSetRecorder{}
	state := &rawSetRecorder{}
	now := time.Date(2026, 7, 9, 1, 2, 3, 456*int(time.Millisecond), time.UTC)
	invalidator, err := NewSystemAccountInvalidator(SystemAccountInvalidatorOptions{
		Cache:      cache,
		State:      state,
		Namespace:  "test-ns",
		Now:        func() time.Time { return now },
		NewVersion: versionSequence("runtime-version", "quota-version"),
	})
	if err != nil {
		t.Fatalf("NewSystemAccountInvalidator() error = %v", err)
	}

	if err := invalidator.InvalidateAuthorizationChanged(context.Background(), TeamAuthorizationChangedReason); err != nil {
		t.Fatalf("InvalidateAuthorizationChanged() error = %v", err)
	}

	if len(cache.calls) != 0 {
		t.Fatalf("cache calls = %d, want 0 for authorization invalidation", len(cache.calls))
	}
	if len(state.calls) != 2 {
		t.Fatalf("state calls = %d, want 2", len(state.calls))
	}
	assertRuntimeStateCall(t, state.calls[0], "juhe-ai:test-ns:state:gateway_cache_invalidation:topic:gateway_runtime_cache", "runtime-version", TeamAuthorizationChangedReason, "", "2026-07-09T01:02:03.456Z")
	assertRuntimeStateCall(t, state.calls[1], "juhe-ai:test-ns:state:gateway_cache_invalidation:topic:authorization_quota_cache", "quota-version", TeamAuthorizationChangedReason, "", "2026-07-09T01:02:03.456Z")
}

func TestSystemAccountInvalidatorProxyChangedWritesRuntimeTopic(t *testing.T) {
	cache := &rawSetRecorder{}
	state := &rawSetRecorder{}
	invalidator, err := NewSystemAccountInvalidator(SystemAccountInvalidatorOptions{
		Cache:      cache,
		State:      state,
		Namespace:  "test-ns",
		Now:        func() time.Time { return time.Unix(0, 0).UTC() },
		NewVersion: versionSequence("proxy-version"),
	})
	if err != nil {
		t.Fatalf("NewSystemAccountInvalidator() error = %v", err)
	}

	if err := invalidator.InvalidateProxyChanged(context.Background(), ProxyUpdatedReason); err != nil {
		t.Fatalf("InvalidateProxyChanged() error = %v", err)
	}

	if len(cache.calls) != 0 {
		t.Fatalf("cache calls = %d, want 0 for proxy invalidation", len(cache.calls))
	}
	if len(state.calls) != 1 {
		t.Fatalf("state calls = %d, want 1", len(state.calls))
	}
	assertRuntimeStateCall(
		t,
		state.calls[0],
		"juhe-ai:test-ns:state:gateway_cache_invalidation:topic:gateway_runtime_cache",
		"proxy-version",
		ProxyUpdatedReason,
		"",
		"1970-01-01T00:00:00.000Z",
	)
}

func TestSystemAccountInvalidatorAPIKeyQuotaChangedCarriesAPIKeyID(t *testing.T) {
	state := &rawSetRecorder{}
	invalidator, err := NewSystemAccountInvalidator(SystemAccountInvalidatorOptions{
		Cache:      &rawSetRecorder{},
		State:      state,
		Namespace:  "test-ns",
		Now:        func() time.Time { return time.Unix(0, 0).UTC() },
		NewVersion: versionSequence("quota-version"),
	})
	if err != nil {
		t.Fatalf("NewSystemAccountInvalidator() error = %v", err)
	}

	if err := invalidator.InvalidateAPIKeyQuotaChanged(context.Background(), " key_123 ", "api_key_updated"); err != nil {
		t.Fatalf("InvalidateAPIKeyQuotaChanged() error = %v", err)
	}

	if len(state.calls) != 1 {
		t.Fatalf("state calls = %d, want 1", len(state.calls))
	}
	assertRuntimeStateCall(t, state.calls[0], "juhe-ai:test-ns:state:gateway_cache_invalidation:topic:api_key_quota_cache", "quota-version", "api_key_updated", "key_123", "1970-01-01T00:00:00.000Z")
}

func TestSystemAccountInvalidatorPropagatesCacheClearErrorAndSkipsRuntimeState(t *testing.T) {
	wantErr := errors.New("redis cache down")
	cache := &rawSetRecorder{err: wantErr}
	state := &rawSetRecorder{}
	invalidator, err := NewSystemAccountInvalidator(SystemAccountInvalidatorOptions{
		Cache:      cache,
		State:      state,
		Namespace:  "test-ns",
		NewVersion: versionSequence("cache-version", "state-version"),
	})
	if err != nil {
		t.Fatalf("NewSystemAccountInvalidator() error = %v", err)
	}

	err = invalidator.InvalidateSystemAccountStatusChanged(context.Background(), "sys_user")

	if !errors.Is(err, wantErr) {
		t.Fatalf("InvalidateSystemAccountStatusChanged() error = %v, want %v", err, wantErr)
	}
	if len(state.calls) != 0 {
		t.Fatalf("state calls = %d, want 0 after cache error", len(state.calls))
	}
}

type rawSetCall struct {
	key   string
	value []byte
	ttl   time.Duration
}

type rawSetRecorder struct {
	calls []rawSetCall
	err   error
}

func (r *rawSetRecorder) SetRaw(_ context.Context, key string, value []byte, ttl time.Duration) error {
	copied := append([]byte(nil), value...)
	r.calls = append(r.calls, rawSetCall{key: key, value: copied, ttl: ttl})
	return r.err
}

func assertRuntimeStateCall(t *testing.T, call rawSetCall, wantKey string, wantVersion string, wantReason string, wantAPIKeyID string, wantPublishedAt string) {
	t.Helper()
	if call.key != wantKey {
		t.Fatalf("state key = %q, want %q", call.key, wantKey)
	}
	if call.ttl != RuntimeStateTTL {
		t.Fatalf("state ttl = %v, want %v", call.ttl, RuntimeStateTTL)
	}
	var payload runtimeInvalidationState
	if err := json.Unmarshal(call.value, &payload); err != nil {
		t.Fatalf("state payload json = %s: %v", call.value, err)
	}
	if payload.Version != wantVersion ||
		payload.Reason != wantReason ||
		payload.APIKeyID != wantAPIKeyID ||
		payload.PublishedAt != wantPublishedAt {
		t.Fatalf("state payload = %+v", payload)
	}
}

func versionSequence(values ...string) VersionGenerator {
	index := 0
	return func(time.Time) (string, error) {
		if index >= len(values) {
			return "", errors.New("version sequence exhausted")
		}
		value := values[index]
		index++
		return value, nil
	}
}
