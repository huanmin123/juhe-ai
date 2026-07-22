package gatewaypreflight

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycache"
	"juhe-ai/backend-go/internal/modules/gatewayquotasnapshot"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	defaultCacheTTL        = 60 * time.Second
	defaultCacheMaxEntries = 10_000
)

type CacheVersionReader interface {
	GatewayPreflightCacheVersion(ctx context.Context) (string, error)
}

type CacheOptions struct {
	VersionReader CacheVersionReader
	TTL           time.Duration
	MaxEntries    int
	Now           func() time.Time
}

type Cache struct {
	mu            sync.Mutex
	entries       map[string]gatewayPreflightCacheEntry
	versionReader CacheVersionReader
	ttl           time.Duration
	maxEntries    int
	now           func() time.Time
	version       string
	versionSet    bool
	flights       map[string]*gatewayPreflightCacheFlight
}

type gatewayPreflightCacheEntry struct {
	structure gatewayPreflightStructure
	expiresAt time.Time
	cachedAt  time.Time
}

type gatewayPreflightCacheFlight struct {
	done      chan struct{}
	structure gatewayPreflightStructure
	err       error
}

func NewCache(opts CacheOptions) *Cache {
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}
	maxEntries := opts.MaxEntries
	if maxEntries <= 0 {
		maxEntries = defaultCacheMaxEntries
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Cache{entries: make(map[string]gatewayPreflightCacheEntry), versionReader: opts.VersionReader, ttl: ttl, maxEntries: maxEntries, now: now, flights: make(map[string]*gatewayPreflightCacheFlight)}
}

func (c *Cache) load(ctx context.Context, keyHash string, loader func(context.Context, string) (gatewayPreflightStructure, error)) (gatewayPreflightStructure, error) {
	if c == nil || c.versionReader == nil {
		return loader(ctx, keyHash)
	}
	before, err := c.versionReader.GatewayPreflightCacheVersion(ctx)
	if err != nil {
		return loader(ctx, keyHash)
	}
	now := c.now()
	c.mu.Lock()
	c.applyVersionLocked(before)
	if entry, ok := c.entries[keyHash]; ok && now.Before(entry.expiresAt) {
		structure := cloneStructure(entry.structure)
		c.mu.Unlock()
		return structure, nil
	}
	delete(c.entries, keyHash)
	flightKey := before + "\x00" + keyHash
	if flight, ok := c.flights[flightKey]; ok {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return gatewayPreflightStructure{}, ctx.Err()
		case <-flight.done:
			return cloneStructure(flight.structure), flight.err
		}
	}
	flight := &gatewayPreflightCacheFlight{done: make(chan struct{})}
	c.flights[flightKey] = flight
	c.mu.Unlock()

	loaded, err := loader(ctx, keyHash)
	if err != nil {
		c.finishFlight(flightKey, flight, gatewayPreflightStructure{}, err)
		return gatewayPreflightStructure{}, err
	}
	after, err := c.versionReader.GatewayPreflightCacheVersion(ctx)
	if err != nil || after != before {
		if err == nil {
			c.mu.Lock()
			if !c.versionSet || c.version == before {
				c.applyVersionLocked(after)
			}
			c.mu.Unlock()
		}
		result := cloneStructure(loaded)
		c.finishFlight(flightKey, flight, result, nil)
		return result, nil
	}

	c.mu.Lock()
	if !c.versionSet || c.version != after {
		c.mu.Unlock()
		result := cloneStructure(loaded)
		c.finishFlight(flightKey, flight, result, nil)
		return result, nil
	}
	c.evictExpiredLocked(now)
	if len(c.entries) >= c.maxEntries {
		c.evictOldestLocked()
	}
	c.entries[keyHash] = gatewayPreflightCacheEntry{structure: cloneStructure(loaded), expiresAt: now.Add(c.ttl), cachedAt: now}
	c.mu.Unlock()
	result := cloneStructure(loaded)
	c.finishFlight(flightKey, flight, result, nil)
	return result, nil
}

func (c *Cache) finishFlight(key string, flight *gatewayPreflightCacheFlight, structure gatewayPreflightStructure, err error) {
	c.mu.Lock()
	flight.structure = cloneStructure(structure)
	flight.err = err
	delete(c.flights, key)
	close(flight.done)
	c.mu.Unlock()
}

func (c *Cache) applyVersionLocked(version string) {
	if c.versionSet && c.version == version {
		return
	}
	clear(c.entries)
	c.version = version
	c.versionSet = true
}

func (c *Cache) evictExpiredLocked(now time.Time) {
	for key, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, key)
		}
	}
}

func (c *Cache) evictOldestLocked() {
	oldestKey := ""
	var oldestAt time.Time
	for key, entry := range c.entries {
		if oldestKey == "" || entry.cachedAt.Before(oldestAt) || (entry.cachedAt.Equal(oldestAt) && key < oldestKey) {
			oldestKey = key
			oldestAt = entry.cachedAt
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

func cloneStructure(value gatewayPreflightStructure) gatewayPreflightStructure {
	result := gatewayPreflightStructure{decision: value.decision, bindings: append([]Binding(nil), value.bindings...)}
	if value.apiKey != nil {
		copy := *value.apiKey
		copy.expiresAt = cloneTimePtr(value.apiKey.expiresAt)
		copy.quotaLimits = cloneQuotaLimits(value.apiKey.quotaLimits)
		result.apiKey = &copy
	}
	if value.settings != nil {
		copy := *value.settings
		result.settings = &copy
	}
	return result
}

type RawGetter interface {
	GetRaw(ctx context.Context, key string) ([]byte, error)
}

type SharedVersionReader struct {
	cacheGetter        RawGetter
	stateGetter        RawGetter
	apiKeyVersionKey   string
	settingsVersionKey string
	runtimeVersionKey  string
}

func NewSharedVersionReader(cacheGetter RawGetter, stateGetter RawGetter, namespace string) (*SharedVersionReader, error) {
	if cacheGetter == nil {
		return nil, fmt.Errorf("gateway preflight cache version raw getter is required")
	}
	if stateGetter == nil {
		return nil, fmt.Errorf("gateway preflight runtime version raw getter is required")
	}
	apiKeyKey, err := gatewaycache.SharedCacheVersionKey(namespace, gatewaycache.APIKeyValidationCacheName)
	if err != nil {
		return nil, err
	}
	settingsKey, err := gatewaycache.SharedCacheVersionKey(namespace, gatewaycache.SystemSettingsCacheName)
	if err != nil {
		return nil, err
	}
	runtimeKey, err := gatewaycache.RuntimeStateKey(
		namespace,
		gatewaycache.RuntimeInvalidationStoreName,
		"topic:"+gatewaycache.SanitizeRedisKeyPart(gatewaycache.GatewayRuntimeCacheTopic),
	)
	if err != nil {
		return nil, err
	}
	return &SharedVersionReader{
		cacheGetter:        cacheGetter,
		stateGetter:        stateGetter,
		apiKeyVersionKey:   apiKeyKey,
		settingsVersionKey: settingsKey,
		runtimeVersionKey:  runtimeKey,
	}, nil
}

func (r *SharedVersionReader) GatewayPreflightCacheVersion(ctx context.Context) (string, error) {
	if r == nil || r.cacheGetter == nil || r.stateGetter == nil {
		return "", fmt.Errorf("gateway preflight shared version reader is required")
	}
	apiKeyVersion, err := readOptionalRaw(ctx, r.cacheGetter, r.apiKeyVersionKey)
	if err != nil {
		return "", fmt.Errorf("read gateway preflight api key cache version: %w", err)
	}
	settingsVersion, err := readOptionalRaw(ctx, r.cacheGetter, r.settingsVersionKey)
	if err != nil {
		return "", fmt.Errorf("read gateway preflight settings cache version: %w", err)
	}
	runtimeVersion, err := readRuntimeInvalidationVersion(ctx, r.stateGetter, r.runtimeVersionKey)
	if err != nil {
		return "", fmt.Errorf("read gateway preflight runtime cache version: %w", err)
	}
	return string(apiKeyVersion) + "\x00" + string(settingsVersion) + "\x00" + runtimeVersion, nil
}

type RuntimeStateQuotaSnapshotReader struct {
	getter RawGetter
	key    string
}

func NewRuntimeStateQuotaSnapshotReader(getter RawGetter, namespace string) (*RuntimeStateQuotaSnapshotReader, error) {
	if getter == nil {
		return nil, fmt.Errorf("gateway preflight quota snapshot raw getter is required")
	}
	key, err := gatewaycache.RuntimeStateKey(namespace, gatewayquotasnapshot.RuntimeStateStoreName, gatewayquotasnapshot.RuntimeStateCurrentKey)
	if err != nil {
		return nil, err
	}
	return &RuntimeStateQuotaSnapshotReader{getter: getter, key: key}, nil
}

func (r *RuntimeStateQuotaSnapshotReader) LoadGatewayPreflightQuotaSnapshotCurrent(ctx context.Context) (port.GatewayPreflightQuotaSnapshot, bool, error) {
	if r == nil || r.getter == nil {
		return port.GatewayPreflightQuotaSnapshot{}, false, fmt.Errorf("gateway preflight quota snapshot reader is required")
	}
	raw, err := r.getter.GetRaw(ctx, r.key)
	if errors.Is(err, redisplatform.ErrNotFound) {
		return port.GatewayPreflightQuotaSnapshot{}, false, nil
	}
	if err != nil {
		return port.GatewayPreflightQuotaSnapshot{}, false, err
	}
	var payload runtimeStateQuotaSnapshot
	if err := json.Unmarshal(raw, &payload); err != nil {
		return port.GatewayPreflightQuotaSnapshot{}, false, fmt.Errorf("decode gateway preflight quota snapshot current: %w", err)
	}
	if strings.TrimSpace(payload.GeneratedAt) == "" {
		return port.GatewayPreflightQuotaSnapshot{}, false, nil
	}
	complete := true
	if payload.CostEntriesComplete != nil {
		complete = *payload.CostEntriesComplete
	}
	authorizationComplete := true
	if payload.AuthorizationEntriesComplete != nil {
		authorizationComplete = *payload.AuthorizationEntriesComplete
	}
	entries := make([]port.GatewayPreflightQuotaCostEntry, 0, len(payload.CostEntries))
	for _, entry := range payload.CostEntries {
		entries = append(entries, port.GatewayPreflightQuotaCostEntry{SystemAccountID: entry.SystemAccountID, ScopeType: entry.ScopeType, ScopeID: entry.ScopeID, HourlyWindowHours: entry.HourlyWindowHours, Costs: entry.Costs})
	}
	return port.GatewayPreflightQuotaSnapshot{GeneratedAt: payload.GeneratedAt, CostEntries: entries, CostEntriesComplete: complete, AuthorizationEntriesComplete: authorizationComplete}, true, nil
}

type runtimeStateQuotaSnapshot struct {
	GeneratedAt                  string                       `json:"generatedAt"`
	CostEntries                  []runtimeStateQuotaCostEntry `json:"costEntries"`
	CostEntriesComplete          *bool                        `json:"costEntriesComplete"`
	AuthorizationEntriesComplete *bool                        `json:"authorizationEntriesComplete"`
}

type runtimeStateQuotaCostEntry struct {
	SystemAccountID   string                 `json:"systemAccountId"`
	ScopeType         string                 `json:"scopeType"`
	ScopeID           string                 `json:"scopeId"`
	HourlyWindowHours int                    `json:"hourlyWindowHours"`
	Costs             port.GatewayQuotaCosts `json:"costs"`
}

func readOptionalRaw(ctx context.Context, getter RawGetter, key string) ([]byte, error) {
	value, err := getter.GetRaw(ctx, key)
	if errors.Is(err, redisplatform.ErrNotFound) {
		return nil, nil
	}
	return value, err
}

func readRuntimeInvalidationVersion(ctx context.Context, getter RawGetter, key string) (string, error) {
	raw, err := readOptionalRaw(ctx, getter, key)
	if err != nil || len(raw) == 0 {
		return "", err
	}
	var payload struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", fmt.Errorf("decode gateway runtime invalidation state: %w", err)
	}
	version := strings.TrimSpace(payload.Version)
	if version == "" {
		return "", fmt.Errorf("gateway runtime invalidation state version is empty")
	}
	return version, nil
}

var _ CacheVersionReader = (*SharedVersionReader)(nil)
var _ port.GatewayPreflightQuotaSnapshotReader = (*RuntimeStateQuotaSnapshotReader)(nil)
