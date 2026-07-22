package gatewaypreflight

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/apikeysecret"
	"juhe-ai/backend-go/internal/store/port"
)

func TestResolveRejectsMalformedKeyWithoutReadingStore(t *testing.T) {
	store := newGatewayPreflightStoreStub(activeGatewayPreflightKey())
	result, err := NewService(ServiceOptions{Store: store}).Resolve(context.Background(), "not-a-key")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got := result.Decision().Code(); got != DecisionInvalidAPIKeyFormat {
		t.Fatalf("decision = %q, want %q", got, DecisionInvalidAPIKeyFormat)
	}
	if store.keyCallsCount() != 0 {
		t.Fatalf("key store calls = %d, want 0", store.keyCallsCount())
	}
}

func TestResolveHashesRawKeyAndReturnsImmutableReadyDTO(t *testing.T) {
	now := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)
	store := newGatewayPreflightStoreStub(activeGatewayPreflightKey())
	store.bindings = []port.GatewayPreflightBindingRecord{
		{ID: "binding_2", APIKeyID: "key_1", SystemAccountID: "sys_1", GroupID: "group_2", Priority: 2, Weight: 5, ProviderCode: "openai", CreatedAt: now.Add(-time.Hour)},
		{ID: "binding_1", APIKeyID: "key_1", SystemAccountID: "sys_1", GroupID: "group_1", Priority: 1, Weight: 1, ProviderCode: "gpt", CreatedAt: now},
	}
	service := NewService(ServiceOptions{Store: store, Now: func() time.Time { return now }})

	const rawKey = "sk-gateway-preflight-ready"
	result, err := service.Resolve(context.Background(), rawKey)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got := store.lastKeyHash(); got != apikeysecret.Hash(rawKey) {
		t.Fatalf("key hash = %q, want %q", got, apikeysecret.Hash(rawKey))
	}
	if !result.Decision().Allowed() || result.Decision().Code() != DecisionReady {
		t.Fatalf("decision = %q allowed=%v", result.Decision().Code(), result.Decision().Allowed())
	}
	apiKey, ok := result.APIKey()
	if !ok || apiKey.ID() != "key_1" || apiKey.SystemAccountID() != "sys_1" || apiKey.RouteStrategyID() != "route_1" {
		t.Fatalf("api key DTO = %#v, ok=%v", apiKey, ok)
	}
	settings, ok := result.Settings()
	if !ok || settings.TextFirstResponseTimeoutSeconds() != 120 || !settings.StreamCircuitBreakerEnabled() {
		t.Fatalf("settings = %#v, ok=%v", settings, ok)
	}
	bindings := result.Bindings()
	if len(bindings) != 2 || bindings[0].ID() != "binding_1" || bindings[1].ID() != "binding_2" {
		t.Fatalf("bindings = %#v", bindings)
	}
	bindings[0] = Binding{}
	if got := result.Bindings()[0].ID(); got != "binding_1" {
		t.Fatalf("mutating returned bindings changed result: %q", got)
	}
}

func TestResolveReturnsExactStructuralDecisions(t *testing.T) {
	now := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)
	expired := now.Add(-time.Nanosecond)
	tests := []struct {
		name     string
		key      port.GatewayPreflightAPIKeyRecord
		found    bool
		bindings []port.GatewayPreflightBindingRecord
		want     DecisionCode
	}{
		{name: "not found", found: false, want: DecisionAPIKeyNotFound},
		{name: "api key disabled", key: gatewayPreflightKeyWith(func(row *port.GatewayPreflightAPIKeyRecord) { row.APIKeyStatus = "disabled" }), found: true, want: DecisionAPIKeyDisabled},
		{name: "api key expired", key: gatewayPreflightKeyWith(func(row *port.GatewayPreflightAPIKeyRecord) { row.ExpiresAt = &expired }), found: true, want: DecisionAPIKeyExpired},
		{name: "system account disabled", key: gatewayPreflightKeyWith(func(row *port.GatewayPreflightAPIKeyRecord) { row.SystemAccountStatus = "disabled" }), found: true, want: DecisionSystemAccountDisabled},
		{name: "route strategy disabled", key: gatewayPreflightKeyWith(func(row *port.GatewayPreflightAPIKeyRecord) { row.RouteStrategyStatus = "disabled" }), found: true, want: DecisionRouteStrategyDisabled},
		{name: "no active bindings", key: activeGatewayPreflightKey(), found: true, bindings: []port.GatewayPreflightBindingRecord{}, want: DecisionNoActiveBindings},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newGatewayPreflightStoreStub(tt.key)
			store.found = tt.found
			store.bindings = tt.bindings
			result, err := NewService(ServiceOptions{Store: store, Now: func() time.Time { return now }}).Resolve(context.Background(), "sk-structural-decision")
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if got := result.Decision().Code(); got != tt.want {
				t.Fatalf("decision = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveCapsBindingsAtTwentyWithStableOrder(t *testing.T) {
	now := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)
	store := newGatewayPreflightStoreStub(activeGatewayPreflightKey())
	store.bindings = make([]port.GatewayPreflightBindingRecord, 25)
	for index := range store.bindings {
		store.bindings[index] = port.GatewayPreflightBindingRecord{
			ID: "binding_" + string(rune('a'+index)), APIKeyID: "key_1", SystemAccountID: "sys_1",
			GroupID: "group", Priority: 25 - index, Weight: 1, ProviderCode: "openai", CreatedAt: now.Add(time.Duration(index) * time.Second),
		}
	}
	result, err := NewService(ServiceOptions{Store: store, Now: func() time.Time { return now }}).Resolve(context.Background(), "sk-binding-limit")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got := store.lastBindingLimit(); got != MaxActiveBindings {
		t.Fatalf("binding limit = %d, want %d", got, MaxActiveBindings)
	}
	bindings := result.Bindings()
	if len(bindings) != MaxActiveBindings {
		t.Fatalf("bindings = %d, want %d", len(bindings), MaxActiveBindings)
	}
	for index := 1; index < len(bindings); index++ {
		if bindings[index-1].Priority() > bindings[index].Priority() {
			t.Fatalf("bindings are not priority sorted at %d: %#v", index, bindings)
		}
	}
}

func TestResolveQuotaSnapshotDecisions(t *testing.T) {
	now := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)
	limitedKey := gatewayPreflightKeyWith(func(row *port.GatewayPreflightAPIKeyRecord) {
		row.QuotaLimits = port.ManagementRequestQuotaLimits{
			Hourly: &port.ManagementRequestHourlyQuotaLimit{Enabled: true, Hours: 3, Limit: 10},
			Total:  &port.ManagementRequestQuotaLimit{Enabled: true, Limit: 100},
		}
	})
	tests := []struct {
		name      string
		reader    *gatewayPreflightQuotaReaderStub
		want      DecisionCode
		wantAllow bool
	}{
		{name: "missing", reader: &gatewayPreflightQuotaReaderStub{}, want: DecisionQuotaSnapshotMissing},
		{name: "unavailable", reader: &gatewayPreflightQuotaReaderStub{err: errors.New("redis unavailable")}, want: DecisionQuotaSnapshotUnavailable},
		{name: "incomplete", reader: &gatewayPreflightQuotaReaderStub{found: true, snapshot: port.GatewayPreflightQuotaSnapshot{GeneratedAt: now.Format(time.RFC3339Nano), CostEntriesComplete: false}}, want: DecisionQuotaSnapshotIncomplete},
		{name: "complete but entry missing", reader: &gatewayPreflightQuotaReaderStub{found: true, snapshot: port.GatewayPreflightQuotaSnapshot{GeneratedAt: now.Format(time.RFC3339Nano), CostEntriesComplete: true}}, want: DecisionQuotaSnapshotMissing},
		{name: "exceeded", reader: quotaReaderWithCosts(now, port.GatewayQuotaCosts{Hourly: 10}), want: DecisionQuotaExceeded},
		{name: "allowed", reader: quotaReaderWithCosts(now, port.GatewayQuotaCosts{Hourly: 9, Total: 99}), want: DecisionReady, wantAllow: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newGatewayPreflightStoreStub(limitedKey)
			result, err := NewService(ServiceOptions{Store: store, QuotaSnapshotReader: tt.reader, Now: func() time.Time { return now }}).Resolve(context.Background(), "sk-quota")
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if got := result.Decision().Code(); got != tt.want || result.Decision().Allowed() != tt.wantAllow {
				t.Fatalf("decision = %q allowed=%v, want %q/%v", got, result.Decision().Allowed(), tt.want, tt.wantAllow)
			}
		})
	}
}

func TestResolveWithoutQuotaDoesNotReadSnapshot(t *testing.T) {
	reader := &gatewayPreflightQuotaReaderStub{err: errors.New("must not be called")}
	result, err := NewService(ServiceOptions{Store: newGatewayPreflightStoreStub(activeGatewayPreflightKey()), QuotaSnapshotReader: reader}).Resolve(context.Background(), "sk-no-quota")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if result.Decision().Code() != DecisionReady || reader.callsCount() != 0 {
		t.Fatalf("decision=%q snapshot calls=%d", result.Decision().Code(), reader.callsCount())
	}
}

func TestResolveDefaultsToNoCache(t *testing.T) {
	store := newGatewayPreflightStoreStub(activeGatewayPreflightKey())
	service := NewService(ServiceOptions{Store: store})
	for range 2 {
		if _, err := service.Resolve(context.Background(), "sk-no-cache"); err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
	}
	if got := store.keyCallsCount(); got != 2 {
		t.Fatalf("key store calls = %d, want 2", got)
	}
}

func TestResolveInjectedCacheInvalidatesOnSharedVersionChange(t *testing.T) {
	store := newGatewayPreflightStoreStub(activeGatewayPreflightKey())
	versions := &gatewayPreflightVersionReaderStub{version: "v1"}
	cache := NewCache(CacheOptions{VersionReader: versions, TTL: time.Minute, MaxEntries: 10})
	service := NewService(ServiceOptions{Store: store, Cache: cache})

	for range 2 {
		if _, err := service.Resolve(context.Background(), "sk-versioned-cache"); err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
	}
	if got := store.keyCallsCount(); got != 1 {
		t.Fatalf("same-version key store calls = %d, want 1", got)
	}
	versions.set("v2")
	if _, err := service.Resolve(context.Background(), "sk-versioned-cache"); err != nil {
		t.Fatalf("Resolve() after version change error = %v", err)
	}
	if got := store.keyCallsCount(); got != 2 {
		t.Fatalf("changed-version key store calls = %d, want 2", got)
	}
}

func TestResolveCachedStructureStillHonorsAPIKeyExpiry(t *testing.T) {
	now := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)
	expiresAt := now.Add(30 * time.Second)
	store := newGatewayPreflightStoreStub(gatewayPreflightKeyWith(func(row *port.GatewayPreflightAPIKeyRecord) {
		row.ExpiresAt = &expiresAt
	}))
	cache := NewCache(CacheOptions{
		VersionReader: &gatewayPreflightVersionReaderStub{version: "v1"},
		TTL:           time.Minute,
		Now:           func() time.Time { return now },
	})
	service := NewService(ServiceOptions{Store: store, Cache: cache, Now: func() time.Time { return now }})
	first, err := service.Resolve(context.Background(), "sk-expiring-cache")
	if err != nil || first.Decision().Code() != DecisionReady {
		t.Fatalf("first decision=%q error=%v", first.Decision().Code(), err)
	}
	now = expiresAt
	second, err := service.Resolve(context.Background(), "sk-expiring-cache")
	if err != nil {
		t.Fatalf("second Resolve() error = %v", err)
	}
	if second.Decision().Code() != DecisionAPIKeyExpired {
		t.Fatalf("second decision = %q, want %q", second.Decision().Code(), DecisionAPIKeyExpired)
	}
}

func TestResolveConcurrentCacheAccessIsRaceSafe(t *testing.T) {
	store := newGatewayPreflightStoreStub(activeGatewayPreflightKey())
	cache := NewCache(CacheOptions{VersionReader: &gatewayPreflightVersionReaderStub{version: "v1"}, TTL: time.Minute, MaxEntries: 10})
	service := NewService(ServiceOptions{Store: store, Cache: cache})
	var wg sync.WaitGroup
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := service.Resolve(context.Background(), "sk-concurrent-cache")
			if err != nil {
				t.Errorf("Resolve() error = %v", err)
				return
			}
			if result.Decision().Code() != DecisionReady {
				t.Errorf("decision = %q", result.Decision().Code())
			}
		}()
	}
	wg.Wait()
}

func activeGatewayPreflightKey() port.GatewayPreflightAPIKeyRecord {
	configJSON := `{"normalRoutingConfig":{"speedFirstEnabled":true}}`
	return port.GatewayPreflightAPIKeyRecord{
		ID: "key_1", SystemAccountID: "sys_1", APIKeyStatus: "active",
		SystemAccountStatus: "active", SystemAccountImageGenerationEnabled: true,
		RouteStrategyID: "route_1", RouteStrategyStatus: "active", RouteStrategyMode: "normal",
		RouteStrategyConfigJSON: &configJSON,
	}
}

func gatewayPreflightKeyWith(change func(*port.GatewayPreflightAPIKeyRecord)) port.GatewayPreflightAPIKeyRecord {
	row := activeGatewayPreflightKey()
	change(&row)
	return row
}

func defaultGatewayPreflightSettings() port.GatewayPreflightSettingsRecord {
	return port.GatewayPreflightSettingsRecord{
		GatewayTextRawBodyLimitMegabytes: 16, DefaultTemporaryUnschedulableMinutes: 2,
		TemporaryUnschedulableRetryIntervalSeconds: 3, TemporaryUnschedulableRetryAttempts: 3,
		TextFirstResponseTimeoutSeconds: 120, TextStreamIdleTimeoutSeconds: 30,
		TextUncommittedAttemptMaxLifetimeSeconds: 1800, ImageFirstResponseTimeoutSeconds: 600,
		ImageStreamIdleTimeoutSeconds: 120, ImageUncommittedAttemptMaxLifetimeSeconds: 3600,
		NoAvailableAccountWaitTimeoutSeconds: 270, StreamFailureThresholdCount: 3,
		StreamFailureThresholdWindowMinutes: 5,
	}
}

func quotaReaderWithCosts(now time.Time, costs port.GatewayQuotaCosts) *gatewayPreflightQuotaReaderStub {
	return &gatewayPreflightQuotaReaderStub{
		found: true,
		snapshot: port.GatewayPreflightQuotaSnapshot{
			GeneratedAt: now.Format(time.RFC3339Nano), CostEntriesComplete: true,
			CostEntries: []port.GatewayPreflightQuotaCostEntry{{
				SystemAccountID: "sys_1", ScopeType: "api_key", ScopeID: "key_1", HourlyWindowHours: 3, Costs: costs,
			}},
		},
	}
}

type gatewayPreflightStoreStub struct {
	mu           sync.Mutex
	key          port.GatewayPreflightAPIKeyRecord
	found        bool
	bindings     []port.GatewayPreflightBindingRecord
	settings     port.GatewayPreflightSettingsRecord
	keyHashes    []string
	bindingLimit []int
}

func newGatewayPreflightStoreStub(key port.GatewayPreflightAPIKeyRecord) *gatewayPreflightStoreStub {
	return &gatewayPreflightStoreStub{
		key: key, found: true,
		bindings: []port.GatewayPreflightBindingRecord{{ID: "binding_1", APIKeyID: "key_1", SystemAccountID: "sys_1", GroupID: "group_1", Priority: 1, Weight: 1, ProviderCode: "openai"}},
		settings: defaultGatewayPreflightSettings(),
	}
}

func (s *gatewayPreflightStoreStub) LoadGatewayPreflightAPIKey(_ context.Context, keyHash string) (port.GatewayPreflightAPIKeyRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keyHashes = append(s.keyHashes, keyHash)
	return s.key, s.found, nil
}

func (s *gatewayPreflightStoreStub) ListGatewayPreflightBindings(_ context.Context, _ string, _ string, _ string, _ time.Time, limit int) ([]port.GatewayPreflightBindingRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindingLimit = append(s.bindingLimit, limit)
	return append([]port.GatewayPreflightBindingRecord(nil), s.bindings...), nil
}

func (s *gatewayPreflightStoreStub) LoadGatewayPreflightSettings(context.Context) (port.GatewayPreflightSettingsRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings, nil
}

func (s *gatewayPreflightStoreStub) keyCallsCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.keyHashes)
}

func (s *gatewayPreflightStoreStub) lastKeyHash() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.keyHashes) == 0 {
		return ""
	}
	return s.keyHashes[len(s.keyHashes)-1]
}

func (s *gatewayPreflightStoreStub) lastBindingLimit() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.bindingLimit) == 0 {
		return 0
	}
	return s.bindingLimit[len(s.bindingLimit)-1]
}

type gatewayPreflightQuotaReaderStub struct {
	mu       sync.Mutex
	snapshot port.GatewayPreflightQuotaSnapshot
	found    bool
	err      error
	calls    int
}

func (s *gatewayPreflightQuotaReaderStub) LoadGatewayPreflightQuotaSnapshotCurrent(context.Context) (port.GatewayPreflightQuotaSnapshot, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.snapshot, s.found, s.err
}

func (s *gatewayPreflightQuotaReaderStub) callsCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type gatewayPreflightVersionReaderStub struct {
	mu      sync.Mutex
	version string
}

func (s *gatewayPreflightVersionReaderStub) GatewayPreflightCacheVersion(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version, nil
}

func (s *gatewayPreflightVersionReaderStub) set(value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version = value
}
