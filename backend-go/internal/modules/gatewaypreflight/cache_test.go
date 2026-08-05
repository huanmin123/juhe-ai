package gatewaypreflight

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/store/port"
)

func TestRuntimeStateQuotaSnapshotReaderReadsCurrentAndDefaultsLegacyCompleteness(t *testing.T) {
	raw := &gatewayPreflightRawGetterStub{values: map[string][]byte{}}
	reader, err := NewRuntimeStateQuotaSnapshotReader(raw, "juhe-ai")
	if err != nil {
		t.Fatalf("NewRuntimeStateQuotaSnapshotReader() error = %v", err)
	}
	raw.values[reader.key] = []byte(`{"generatedAt":"2026-07-22T09:00:00.000Z","costEntries":[{"systemAccountId":"sys_1","scopeType":"api_key","scopeId":"key_1","hourlyWindowHours":3,"costs":{"hourly":1,"daily":2,"weekly":3,"monthly":4,"total":5}}],"authorizationEntries":[{"scopeType":"account_authorization","authorizationId":"auth_1","decision":{"allowed":false}}]}`)

	snapshot, found, err := reader.LoadGatewayPreflightQuotaSnapshotCurrent(context.Background())
	if err != nil {
		t.Fatalf("LoadGatewayPreflightQuotaSnapshotCurrent() error = %v", err)
	}
	if !found || !snapshot.CostEntriesComplete || len(snapshot.CostEntries) != 1 || snapshot.CostEntries[0].Costs.Total != 5 || len(snapshot.AuthorizationEntries) != 1 || snapshot.AuthorizationEntries[0].Allowed {
		t.Fatalf("snapshot = %+v, found=%v", snapshot, found)
	}
}

func TestRuntimeStateQuotaSnapshotReaderTreatsRedisNotFoundAsMissing(t *testing.T) {
	reader, err := NewRuntimeStateQuotaSnapshotReader(&gatewayPreflightRawGetterStub{err: redisplatform.ErrNotFound}, "juhe-ai")
	if err != nil {
		t.Fatalf("NewRuntimeStateQuotaSnapshotReader() error = %v", err)
	}
	_, found, err := reader.LoadGatewayPreflightQuotaSnapshotCurrent(context.Background())
	if err != nil || found {
		t.Fatalf("found=%v error=%v, want false/nil", found, err)
	}
}

func TestSharedVersionReaderCombinesValidationSettingsAndRuntimeVersions(t *testing.T) {
	cacheRaw := &gatewayPreflightRawGetterStub{values: map[string][]byte{}}
	stateRaw := &gatewayPreflightRawGetterStub{values: map[string][]byte{}}
	reader, err := NewSharedVersionReader(cacheRaw, stateRaw, "juhe-ai")
	if err != nil {
		t.Fatalf("NewSharedVersionReader() error = %v", err)
	}
	cacheRaw.values[reader.apiKeyVersionKey] = []byte("api-v1")
	cacheRaw.values[reader.settingsVersionKey] = []byte("settings-v2")
	stateRaw.values[reader.runtimeVersionKey] = []byte(`{"version":"runtime-v3","publishedAt":"2026-07-22T09:00:00.000Z","reason":"resource_authorization_revoked"}`)
	version, err := reader.GatewayPreflightCacheVersion(context.Background())
	if err != nil {
		t.Fatalf("GatewayPreflightCacheVersion() error = %v", err)
	}
	if version != "api-v1\x00settings-v2\x00runtime-v3" {
		t.Fatalf("version = %q", version)
	}
}

func TestCacheBypassesOnVersionReadFailure(t *testing.T) {
	cache := NewCache(CacheOptions{VersionReader: gatewayPreflightVersionReaderErrorStub{err: errors.New("redis down")}, TTL: time.Minute})
	loads := 0
	loader := func(context.Context, string) (gatewayPreflightStructure, error) {
		loads++
		return gatewayPreflightStructure{decision: newDecision(DecisionReady)}, nil
	}
	for range 2 {
		if _, err := cache.load(context.Background(), "hash", loader); err != nil {
			t.Fatalf("cache.load() error = %v", err)
		}
	}
	if loads != 2 {
		t.Fatalf("loads = %d, want 2", loads)
	}
}

func TestCacheDoesNotBackfillWhenVersionChangesDuringLoad(t *testing.T) {
	versions := &gatewayPreflightSequenceVersionReader{versions: []string{"v1", "v2", "v2", "v2"}}
	cache := NewCache(CacheOptions{VersionReader: versions, TTL: time.Minute})
	loads := 0
	loader := func(context.Context, string) (gatewayPreflightStructure, error) {
		loads++
		return gatewayPreflightStructure{decision: newDecision(DecisionReady), bindings: []Binding{newBinding(port.GatewayPreflightBindingRecord{ID: "b"})}}, nil
	}
	if _, err := cache.load(context.Background(), "hash", loader); err != nil {
		t.Fatalf("first load error = %v", err)
	}
	if _, err := cache.load(context.Background(), "hash", loader); err != nil {
		t.Fatalf("second load error = %v", err)
	}
	if loads != 2 {
		t.Fatalf("loads = %d, want 2 because first value must not be backfilled", loads)
	}
}

func TestCacheDoesNotRollBackVersionObservedByAnotherLoader(t *testing.T) {
	reader := &gatewayPreflightInterleavingVersionReader{}
	cache := NewCache(CacheOptions{VersionReader: reader, TTL: time.Minute})
	reader.cache = cache
	loader := func(context.Context, string) (gatewayPreflightStructure, error) {
		return gatewayPreflightStructure{decision: newDecision(DecisionReady)}, nil
	}
	if _, err := cache.load(context.Background(), "hash", loader); err != nil {
		t.Fatalf("cache.load() error = %v", err)
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.version != "v2" {
		t.Fatalf("cache version rolled back to %q, want v2", cache.version)
	}
	if _, ok := cache.entries["hash"]; ok {
		t.Fatal("stale loader backfilled an entry after another loader observed v2")
	}
}

func TestCacheCoalescesConcurrentMissesForTheSameVersionAndKey(t *testing.T) {
	const callers = 32
	cache := NewCache(CacheOptions{VersionReader: &gatewayPreflightVersionReaderStub{version: "v1"}, TTL: time.Minute})
	var loads atomic.Int32
	release := make(chan struct{})
	loader := func(context.Context, string) (gatewayPreflightStructure, error) {
		loads.Add(1)
		<-release
		return gatewayPreflightStructure{decision: newDecision(DecisionReady)}, nil
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := cache.load(context.Background(), "same-hash", loader); err != nil {
				t.Errorf("cache.load() error = %v", err)
			}
		}()
	}
	close(start)
	deadline := time.Now().Add(time.Second)
	for loads.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if loads.Load() == 0 {
		t.Fatal("loader did not start")
	}
	time.Sleep(25 * time.Millisecond)
	close(release)
	wg.Wait()
	if got := loads.Load(); got != 1 {
		t.Fatalf("loader calls = %d, want 1", got)
	}
}

type gatewayPreflightRawGetterStub struct {
	values map[string][]byte
	err    error
}

func (s *gatewayPreflightRawGetterStub) GetRaw(_ context.Context, key string) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	value, ok := s.values[key]
	if !ok {
		return nil, redisplatform.ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

type gatewayPreflightVersionReaderErrorStub struct{ err error }

func (s gatewayPreflightVersionReaderErrorStub) GatewayPreflightCacheVersion(context.Context) (string, error) {
	return "", s.err
}

type gatewayPreflightSequenceVersionReader struct {
	versions []string
	index    int
}

func (s *gatewayPreflightSequenceVersionReader) GatewayPreflightCacheVersion(context.Context) (string, error) {
	if s.index >= len(s.versions) {
		return s.versions[len(s.versions)-1], nil
	}
	value := s.versions[s.index]
	s.index++
	return value, nil
}

type gatewayPreflightInterleavingVersionReader struct {
	cache *Cache
	calls int
}

func (r *gatewayPreflightInterleavingVersionReader) GatewayPreflightCacheVersion(context.Context) (string, error) {
	r.calls++
	if r.calls == 1 {
		return "v1", nil
	}
	r.cache.mu.Lock()
	r.cache.applyVersionLocked("v2")
	r.cache.mu.Unlock()
	return "v1", nil
}
