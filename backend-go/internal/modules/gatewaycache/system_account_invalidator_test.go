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
	globalSettingsVersionKey, err := SharedCacheVersionKey("juhe-ai", GlobalSettingsCacheName)
	if err != nil {
		t.Fatalf("global settings SharedCacheVersionKey() error = %v", err)
	}
	if globalSettingsVersionKey != "juhe-ai:juhe-ai:cache-version:settings:global" {
		t.Fatalf("global settings version key = %q", globalSettingsVersionKey)
	}
	systemSettingsVersionKey, err := SharedCacheVersionKey("juhe-ai", SystemSettingsCacheName)
	if err != nil {
		t.Fatalf("system settings SharedCacheVersionKey() error = %v", err)
	}
	if systemSettingsVersionKey != "juhe-ai:juhe-ai:cache-version:settings:system" {
		t.Fatalf("system settings version key = %q", systemSettingsVersionKey)
	}
	groupLookupVersionKey, err := SharedCacheVersionKey("juhe-ai", GroupLookupCacheName)
	if err != nil {
		t.Fatalf("group lookup SharedCacheVersionKey() error = %v", err)
	}
	if groupLookupVersionKey != "juhe-ai:juhe-ai:cache-version:lookup:group" {
		t.Fatalf("group lookup version key = %q", groupLookupVersionKey)
	}
	groupAccountIDsVersionKey, err := SharedCacheVersionKey("juhe-ai", GroupAccountIDsCacheName)
	if err != nil {
		t.Fatalf("group account IDs SharedCacheVersionKey() error = %v", err)
	}
	if groupAccountIDsVersionKey != "juhe-ai:juhe-ai:cache-version:lookup:group-account-ids" {
		t.Fatalf("group account IDs version key = %q", groupAccountIDsVersionKey)
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
	state := &rawSetRecorder{}
	invalidator, err := NewSystemAccountInvalidator(SystemAccountInvalidatorOptions{
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

func TestSystemAccountInvalidatorInvalidateAPIKeyValidationCacheWritesVersion(t *testing.T) {
	cache := &rawSetRecorder{}
	state := &rawSetRecorder{}
	invalidator, err := NewSystemAccountInvalidator(SystemAccountInvalidatorOptions{
		Cache:      cache,
		State:      state,
		Namespace:  "test-ns",
		Now:        func() time.Time { return time.Unix(0, 0).UTC() },
		NewVersion: versionSequence("cache-version"),
	})
	if err != nil {
		t.Fatalf("NewSystemAccountInvalidator() error = %v", err)
	}

	if err := invalidator.InvalidateAPIKeyValidationCache(context.Background()); err != nil {
		t.Fatalf("InvalidateAPIKeyValidationCache() error = %v", err)
	}

	if len(cache.calls) != 1 {
		t.Fatalf("cache calls = %d, want 1", len(cache.calls))
	}
	if cache.calls[0].key != "juhe-ai:test-ns:cache-version:gateway:api-key-validation" {
		t.Fatalf("cache key = %q", cache.calls[0].key)
	}
	if string(cache.calls[0].value) != "cache-version" {
		t.Fatalf("cache version = %q, want %q", string(cache.calls[0].value), "cache-version")
	}
	if cache.calls[0].ttl != SharedCacheVersionTTL {
		t.Fatalf("cache ttl = %v, want %v", cache.calls[0].ttl, SharedCacheVersionTTL)
	}
	if len(state.calls) != 0 {
		t.Fatalf("state calls = %d, want 0", len(state.calls))
	}
}

func TestSystemAccountInvalidatorInvalidateAPIKeyLookupCacheWritesNodeCompatibleVersion(t *testing.T) {
	cache := &rawSetRecorder{}
	state := &rawSetRecorder{}
	invalidator, err := NewSystemAccountInvalidator(SystemAccountInvalidatorOptions{
		Cache:      cache,
		State:      state,
		Namespace:  "test-ns",
		Now:        func() time.Time { return time.Date(2026, 7, 12, 8, 9, 10, 123*int(time.Millisecond), time.UTC) },
		NewVersion: versionSequence("api-key-lookup-version"),
	})
	if err != nil {
		t.Fatalf("NewSystemAccountInvalidator() error = %v", err)
	}

	if err := invalidator.InvalidateAPIKeyLookupCache(
		context.Background(),
		" key_123 ",
		" api_key_updated ",
	); err != nil {
		t.Fatalf("InvalidateAPIKeyLookupCache() error = %v", err)
	}

	if len(cache.calls) != 1 {
		t.Fatalf("cache calls = %d, want 1", len(cache.calls))
	}
	if cache.calls[0].key != "juhe-ai:test-ns:cache-version:lookup:api-key" {
		t.Fatalf("cache key = %q", cache.calls[0].key)
	}
	if string(cache.calls[0].value) != "api-key-lookup-version" {
		t.Fatalf("cache version = %q", string(cache.calls[0].value))
	}
	if cache.calls[0].ttl != 30*24*time.Hour {
		t.Fatalf("cache ttl = %v, want 30 days", cache.calls[0].ttl)
	}
	if len(state.calls) != 0 {
		t.Fatalf("runtime state calls = %d, want 0", len(state.calls))
	}
}

func TestSystemAccountInvalidatorInvalidateAPIKeyLookupCacheRequiresReasonAndCache(t *testing.T) {
	withoutCache, err := NewSystemAccountInvalidator(SystemAccountInvalidatorOptions{
		State:      &rawSetRecorder{},
		Namespace:  "test-ns",
		NewVersion: versionSequence("unused"),
	})
	if err != nil {
		t.Fatalf("NewSystemAccountInvalidator() error = %v", err)
	}
	if err := withoutCache.InvalidateAPIKeyLookupCache(
		context.Background(),
		"key_1",
		"api_key_updated",
	); err == nil || err.Error() != "gateway cache redis setter is required" {
		t.Fatalf("missing cache error = %v", err)
	}

	withCache, err := NewSystemAccountInvalidator(SystemAccountInvalidatorOptions{
		Cache:      &rawSetRecorder{},
		State:      &rawSetRecorder{},
		Namespace:  "test-ns",
		NewVersion: versionSequence("unused"),
	})
	if err != nil {
		t.Fatalf("NewSystemAccountInvalidator() error = %v", err)
	}
	if err := withCache.InvalidateAPIKeyLookupCache(
		context.Background(),
		"key_1",
		" ",
	); err == nil {
		t.Fatal("blank reason error = nil, want non-nil")
	}
}

func TestSystemAccountInvalidatorInvalidateGlobalSettingsCacheWritesNodeCompatibleVersion(t *testing.T) {
	cache := &rawSetRecorder{}
	state := &rawSetRecorder{}
	now := time.Date(2026, 7, 10, 8, 9, 10, 123*int(time.Millisecond), time.UTC)
	invalidator, err := NewSystemAccountInvalidator(SystemAccountInvalidatorOptions{
		Cache:      cache,
		State:      state,
		Namespace:  "test-ns",
		Now:        func() time.Time { return now },
		NewVersion: versionSequence("global-settings-version"),
	})
	if err != nil {
		t.Fatalf("NewSystemAccountInvalidator() error = %v", err)
	}

	if err := invalidator.InvalidateGlobalSettingsCache(context.Background()); err != nil {
		t.Fatalf("InvalidateGlobalSettingsCache() error = %v", err)
	}

	if len(cache.calls) != 1 {
		t.Fatalf("cache calls = %d, want 1", len(cache.calls))
	}
	if cache.calls[0].key != "juhe-ai:test-ns:cache-version:settings:global" {
		t.Fatalf("cache key = %q", cache.calls[0].key)
	}
	if string(cache.calls[0].value) != "global-settings-version" {
		t.Fatalf("cache version = %q, want %q", string(cache.calls[0].value), "global-settings-version")
	}
	if cache.calls[0].ttl != SharedCacheVersionTTL {
		t.Fatalf("cache ttl = %v, want %v", cache.calls[0].ttl, SharedCacheVersionTTL)
	}
	if len(state.calls) != 0 {
		t.Fatalf("state calls = %d, want 0", len(state.calls))
	}
}

func TestSystemAccountInvalidatorInvalidateSystemSettingsCacheWritesNodeCompatibleVersion(t *testing.T) {
	cache := &rawSetRecorder{}
	state := &rawSetRecorder{}
	now := time.Date(2026, 7, 10, 8, 9, 10, 123*int(time.Millisecond), time.UTC)
	invalidator, err := NewSystemAccountInvalidator(SystemAccountInvalidatorOptions{
		Cache:      cache,
		State:      state,
		Namespace:  "test-ns",
		Now:        func() time.Time { return now },
		NewVersion: versionSequence("system-settings-version"),
	})
	if err != nil {
		t.Fatalf("NewSystemAccountInvalidator() error = %v", err)
	}

	if err := invalidator.InvalidateSystemSettingsCache(context.Background()); err != nil {
		t.Fatalf("InvalidateSystemSettingsCache() error = %v", err)
	}

	if len(cache.calls) != 1 {
		t.Fatalf("cache calls = %d, want 1", len(cache.calls))
	}
	if cache.calls[0].key != "juhe-ai:test-ns:cache-version:settings:system" {
		t.Fatalf("cache key = %q", cache.calls[0].key)
	}
	if string(cache.calls[0].value) != "system-settings-version" {
		t.Fatalf("cache version = %q, want %q", string(cache.calls[0].value), "system-settings-version")
	}
	if cache.calls[0].ttl != SharedCacheVersionTTL {
		t.Fatalf("cache ttl = %v, want %v", cache.calls[0].ttl, SharedCacheVersionTTL)
	}
	if len(state.calls) != 0 {
		t.Fatalf("state calls = %d, want 0", len(state.calls))
	}
}

func TestSystemAccountInvalidatorInvalidateGroupLookupCacheWritesNodeCompatibleVersion(t *testing.T) {
	cache := &rawSetRecorder{}
	state := &rawSetRecorder{}
	now := time.Date(2026, 7, 11, 8, 9, 10, 123*int(time.Millisecond), time.UTC)
	invalidator, err := NewSystemAccountInvalidator(SystemAccountInvalidatorOptions{
		Cache:      cache,
		State:      state,
		Namespace:  "test-ns",
		Now:        func() time.Time { return now },
		NewVersion: versionSequence("group-lookup-version"),
	})
	if err != nil {
		t.Fatalf("NewSystemAccountInvalidator() error = %v", err)
	}

	if err := invalidator.InvalidateGroupLookupCache(context.Background()); err != nil {
		t.Fatalf("InvalidateGroupLookupCache() error = %v", err)
	}

	if len(cache.calls) != 1 {
		t.Fatalf("cache calls = %d, want 1", len(cache.calls))
	}
	if cache.calls[0].key != "juhe-ai:test-ns:cache-version:lookup:group" {
		t.Fatalf("cache key = %q", cache.calls[0].key)
	}
	if string(cache.calls[0].value) != "group-lookup-version" {
		t.Fatalf("cache version = %q, want %q", string(cache.calls[0].value), "group-lookup-version")
	}
	if cache.calls[0].ttl != SharedCacheVersionTTL {
		t.Fatalf("cache ttl = %v, want %v", cache.calls[0].ttl, SharedCacheVersionTTL)
	}
	if len(state.calls) != 0 {
		t.Fatalf("state calls = %d, want 0", len(state.calls))
	}
}

func TestSystemAccountInvalidatorInvalidateGroupLookupCacheRequiresCache(t *testing.T) {
	invalidator, err := NewSystemAccountInvalidator(SystemAccountInvalidatorOptions{
		State:      &rawSetRecorder{},
		Namespace:  "test-ns",
		NewVersion: versionSequence("group-lookup-version"),
	})
	if err != nil {
		t.Fatalf("NewSystemAccountInvalidator() error = %v", err)
	}

	err = invalidator.InvalidateGroupLookupCache(context.Background())

	if err == nil || err.Error() != "gateway cache redis setter is required" {
		t.Fatalf("InvalidateGroupLookupCache() error = %v", err)
	}
}

func TestSystemAccountInvalidatorInvalidateGroupLookupCachePropagatesSetError(t *testing.T) {
	wantErr := errors.New("group lookup cache redis down")
	invalidator, err := NewSystemAccountInvalidator(SystemAccountInvalidatorOptions{
		Cache:      &rawSetRecorder{err: wantErr},
		State:      &rawSetRecorder{},
		Namespace:  "test-ns",
		NewVersion: versionSequence("group-lookup-version"),
	})
	if err != nil {
		t.Fatalf("NewSystemAccountInvalidator() error = %v", err)
	}

	err = invalidator.InvalidateGroupLookupCache(context.Background())

	if !errors.Is(err, wantErr) {
		t.Fatalf("InvalidateGroupLookupCache() error = %v, want %v", err, wantErr)
	}
}

func TestSystemAccountInvalidatorInvalidateGroupAccountIDsCacheWritesNodeCompatibleVersion(t *testing.T) {
	cache := &rawSetRecorder{}
	invalidator, err := NewSystemAccountInvalidator(SystemAccountInvalidatorOptions{
		Cache:      cache,
		State:      &rawSetRecorder{},
		Namespace:  "test-ns",
		Now:        func() time.Time { return time.Date(2026, 7, 11, 8, 9, 10, 123*int(time.Millisecond), time.UTC) },
		NewVersion: versionSequence("group-account-ids-version"),
	})
	if err != nil {
		t.Fatalf("NewSystemAccountInvalidator() error = %v", err)
	}

	if err := invalidator.InvalidateGroupAccountIDsCache(context.Background()); err != nil {
		t.Fatalf("InvalidateGroupAccountIDsCache() error = %v", err)
	}

	if len(cache.calls) != 1 {
		t.Fatalf("cache calls = %d, want 1", len(cache.calls))
	}
	if cache.calls[0].key != "juhe-ai:test-ns:cache-version:lookup:group-account-ids" {
		t.Fatalf("cache key = %q", cache.calls[0].key)
	}
	if string(cache.calls[0].value) != "group-account-ids-version" {
		t.Fatalf("cache version = %q", string(cache.calls[0].value))
	}
	if cache.calls[0].ttl != SharedCacheVersionTTL {
		t.Fatalf("cache ttl = %v, want %v", cache.calls[0].ttl, SharedCacheVersionTTL)
	}
}

func TestSystemAccountInvalidatorInvalidateGroupAccountIDsCachePropagatesErrors(t *testing.T) {
	withoutCache, err := NewSystemAccountInvalidator(SystemAccountInvalidatorOptions{
		State:      &rawSetRecorder{},
		Namespace:  "test-ns",
		NewVersion: versionSequence("unused"),
	})
	if err != nil {
		t.Fatalf("NewSystemAccountInvalidator() error = %v", err)
	}
	if err := withoutCache.InvalidateGroupAccountIDsCache(context.Background()); err == nil ||
		err.Error() != "gateway cache redis setter is required" {
		t.Fatalf("missing cache error = %v", err)
	}

	wantErr := errors.New("group account IDs cache redis down")
	withError, err := NewSystemAccountInvalidator(SystemAccountInvalidatorOptions{
		Cache:      &rawSetRecorder{err: wantErr},
		State:      &rawSetRecorder{},
		Namespace:  "test-ns",
		NewVersion: versionSequence("group-account-ids-version"),
	})
	if err != nil {
		t.Fatalf("NewSystemAccountInvalidator() error = %v", err)
	}
	if err := withError.InvalidateGroupAccountIDsCache(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("InvalidateGroupAccountIDsCache() error = %v, want %v", err, wantErr)
	}
}

func TestSystemAccountInvalidatorInvalidateGatewayRuntimeTrimsReasonAndWritesPayload(t *testing.T) {
	state := &rawSetRecorder{}
	now := time.Date(2026, 7, 10, 8, 9, 10, 123*int(time.Millisecond), time.UTC)
	invalidator, err := NewSystemAccountInvalidator(SystemAccountInvalidatorOptions{
		State:      state,
		Namespace:  "test-ns",
		Now:        func() time.Time { return now },
		NewVersion: versionSequence("runtime-version"),
	})
	if err != nil {
		t.Fatalf("NewSystemAccountInvalidator() error = %v", err)
	}

	if err := invalidator.InvalidateGatewayRuntime(context.Background(), "  api_key_changed \t"); err != nil {
		t.Fatalf("InvalidateGatewayRuntime() error = %v", err)
	}

	if len(state.calls) != 1 {
		t.Fatalf("state calls = %d, want 1", len(state.calls))
	}
	assertRuntimeStateCall(
		t,
		state.calls[0],
		"juhe-ai:test-ns:state:gateway_cache_invalidation:topic:gateway_runtime_cache",
		"runtime-version",
		"api_key_changed",
		"",
		"2026-07-10T08:09:10.123Z",
	)
}

func TestSystemAccountInvalidatorInvalidateGatewayRuntimeRejectsBlankReason(t *testing.T) {
	state := &rawSetRecorder{}
	invalidator, err := NewSystemAccountInvalidator(SystemAccountInvalidatorOptions{
		State:      state,
		Namespace:  "test-ns",
		NewVersion: versionSequence("runtime-version"),
	})
	if err != nil {
		t.Fatalf("NewSystemAccountInvalidator() error = %v", err)
	}

	err = invalidator.InvalidateGatewayRuntime(context.Background(), " \t ")

	if err == nil {
		t.Fatal("InvalidateGatewayRuntime() error = nil, want non-nil")
	}
	if len(state.calls) != 0 {
		t.Fatalf("state calls = %d, want 0", len(state.calls))
	}
}

func TestSystemAccountInvalidatorPublicInvalidationMethodsPropagateWriteErrors(t *testing.T) {
	cacheErr := errors.New("cache redis down")
	cacheInvalidator, err := NewSystemAccountInvalidator(SystemAccountInvalidatorOptions{
		Cache:      &rawSetRecorder{err: cacheErr},
		State:      &rawSetRecorder{},
		Namespace:  "test-ns",
		NewVersion: versionSequence("api-key-cache-version", "global-settings-cache-version", "system-settings-cache-version"),
	})
	if err != nil {
		t.Fatalf("NewSystemAccountInvalidator() cache error = %v", err)
	}
	if err := cacheInvalidator.InvalidateAPIKeyValidationCache(context.Background()); !errors.Is(err, cacheErr) {
		t.Fatalf("InvalidateAPIKeyValidationCache() error = %v, want %v", err, cacheErr)
	}
	if err := cacheInvalidator.InvalidateGlobalSettingsCache(context.Background()); !errors.Is(err, cacheErr) {
		t.Fatalf("InvalidateGlobalSettingsCache() error = %v, want %v", err, cacheErr)
	}
	if err := cacheInvalidator.InvalidateSystemSettingsCache(context.Background()); !errors.Is(err, cacheErr) {
		t.Fatalf("InvalidateSystemSettingsCache() error = %v, want %v", err, cacheErr)
	}

	stateErr := errors.New("state redis down")
	runtimeInvalidator, err := NewSystemAccountInvalidator(SystemAccountInvalidatorOptions{
		State:      &rawSetRecorder{err: stateErr},
		Namespace:  "test-ns",
		NewVersion: versionSequence("runtime-version"),
	})
	if err != nil {
		t.Fatalf("NewSystemAccountInvalidator() runtime error = %v", err)
	}
	if err := runtimeInvalidator.InvalidateGatewayRuntime(context.Background(), "api_key_changed"); !errors.Is(err, stateErr) {
		t.Fatalf("InvalidateGatewayRuntime() error = %v, want %v", err, stateErr)
	}
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
