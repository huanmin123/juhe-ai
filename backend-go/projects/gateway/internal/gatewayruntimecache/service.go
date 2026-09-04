package gatewayruntimecache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
)

// TTL / window constants mirror runtime-cache.service.ts exactly.
const (
	gatewayRuntimeTTL                 = 60 * time.Second
	gatewayRuntimeRetainTTL           = 10 * time.Minute
	invalidGatewayRuntimeTTL          = 10 * time.Second
	gatewaySettingsTTL                = 60 * time.Second
	groupUsageAccessTTL               = 60 * time.Second
	groupUsageAccessRetainTTL         = 10 * time.Minute
	openAIAccountsTTL                 = 60 * time.Second
	openAIAccountsRetainTTL           = 10 * time.Minute
	providerModelCatalogTTL           = 24 * time.Hour
	responseInspectionPolicyRetainTTL = 10 * time.Minute

	// GatewayRuntimeLoadTimeout mirrors gatewayRuntimeDbServiceTimeoutMs: the
	// budget bound around the runtime loader call.
	GatewayRuntimeLoadTimeout = 10 * time.Second

	// gatewayRuntimeLoadAttemptLimit mirrors gatewayRuntimeLoadAttemptLimit.
	gatewayRuntimeLoadAttemptLimit = 3

	// sharedCacheFailureLogIntervalMs mirrors sharedCacheFailureLogIntervalMs.
	sharedCacheFailureLogInterval = 30 * time.Second

	cacheMaxEntries = 10000
)

// Cache names mirror the Node cache names verbatim; they namespace both the
// shared Redis caches and the log events.
const (
	settingsCacheName                  = "gateway:settings"
	groupUsageAccessCacheName          = "gateway:group-usage-access"
	providerModelCatalogCacheName      = "gateway:provider-model-catalog"
	providerModelRouteIndexCacheName   = "gateway:provider-model-route-index"
	responseInspectionPolicyCacheName  = "gateway:response-inspection-policies"
	runtimeCacheName                   = "gateway:runtime"
	runtimeAPIKeyIdentityCacheName     = "gateway:runtime-api-key-identity"
)

// Provider model catalog invalidation reasons mirror
// modules/gateway/response/model-catalog-cache-policy.ts.
var providerModelCatalogInvalidationReasons = map[string]bool{
	"custom_provider_model_saved":        true,
	"custom_provider_model_deleted":      true,
	"provider_model_configuration_updated": true,
}

// ShouldInvalidateProviderModelCatalog mirrors shouldInvalidateProviderModelCatalog.
func ShouldInvalidateProviderModelCatalog(reason string) bool {
	return reason != "" && providerModelCatalogInvalidationReasons[reason]
}

// shouldClearSettingsCacheForGatewayInvalidation mirrors the Node helper.
func shouldClearSettingsCacheForGatewayInvalidation(reason string) bool {
	return reason == "" || reason == "settings_updated"
}

// Logger receives the warn-path diagnostics (stale refresh failures, shared
// cache failures). Nil logger drops the events, matching a silent slog discard.
type Logger interface {
	Warn(event string, fields map[string]any, message string)
}

// GroupBindingOrderer is the G08 seam behind
// orderGatewayApiKeyGroupBindingsForDispatchAsync: only the dynamic route
// strategy modes need per-read reordering. Until gatewayrouting ships its
// selector, composition may leave it nil (bindings keep stored order).
type GroupBindingOrderer interface {
	OrderAPIKeyGroupBindings(ctx context.Context, apiKey GatewayAPIKeyRow) ([]GatewayAPIKeyGroupBindingRow, error)
}

// AccountsSelector is the account-selector seam behind
// listOpenAIAccountsForGroupResult (M08/J selector port).
type AccountsSelector interface {
	ListOpenAIAccountsForGroupResult(ctx context.Context, groupID, systemAccountID string, opts OpenAIAccountsForGroupOptions) (OpenAIAccountsForGroupResult, error)
}

// CatalogSource is the C03 seam behind listProviderModelCatalog.
type CatalogSource interface {
	ListProviderModelCatalog(ctx context.Context, input ModelCatalogListOptions) ([]ProviderModelCatalogItem, error)
}

// ConcurrencySource is the live-concurrency seam behind
// loadAccountCurrentConcurrencyByIdsAsync (in-process tracker or Redis).
type ConcurrencySource interface {
	LoadAccountCurrentConcurrencyByID(ctx context.Context, accountIDs []string) (map[string]int, error)
}

// OpenAIAccountsForGroupOptions mirrors the loader options the Node cache
// forwards (requestedModel / requestedEndpointFamily / includeUnavailable /
// preResolvedGroupAccess).
type OpenAIAccountsForGroupOptions struct {
	RequestedModel          string
	RequestedEndpointFamily string
	IncludeUnavailable      bool
	PreResolvedGroupAccess  *GroupUsageAccessMetadata
}

// ModelCatalogListOptions mirrors ModelCatalogListOptions.
type ModelCatalogListOptions struct {
	ProviderCode    string
	SystemAccountID string
	IncludeInactive bool
	IncludeUnpriced bool
}

// ReadModels is the loader seam: every fetch runtime-cache.service.ts performs
// through the db-service / repositories is declared here so cache semantics
// are testable against mocks and future slices can supply their selectors.
type ReadModels interface {
	// ReadGatewaySettings mirrors readGatewaySettings / readGatewaySettingsAsync
	// (projected GatewaySettings, clamps applied).
	ReadGatewaySettings(ctx context.Context) (GatewaySettings, error)
	// ReadGatewayRuntime mirrors requestGatewayDbService({type:
	// 'read_gateway_runtime', key, skipDynamicRouteSelection: true}): the full
	// validated runtime read for one raw API key.
	ReadGatewayRuntime(ctx context.Context, key string) (GatewayRuntime, error)
	// ResolveGroupUsageAccessMetadata mirrors resolveGroupUsageAccessMetadata.
	ResolveGroupUsageAccessMetadata(ctx context.Context, groupID, systemAccountID string) (*GroupUsageAccessMetadata, error)
	// ListOpenAIAccountsForGroupResult mirrors listOpenAIAccountsForGroupResult.
	ListOpenAIAccountsForGroupResult(ctx context.Context, groupID, systemAccountID string, opts OpenAIAccountsForGroupOptions) (OpenAIAccountsForGroupResult, error)
	// ListActiveResponseInspectionPolicies mirrors
	// listActiveResponseInspectionPoliciesForGateway.
	ListActiveResponseInspectionPolicies(ctx context.Context, protocolCode string, providerCode string) ([]ResponseInspectionPolicySummary, error)
	// ListProviderModelCatalog mirrors listProviderModelCatalog.
	ListProviderModelCatalog(ctx context.Context, input ModelCatalogListOptions) ([]ProviderModelCatalogItem, error)
	// LoadAccountCurrentConcurrencyByID mirrors
	// loadAccountCurrentConcurrencyByIdsAsync: account id -> live concurrency.
	LoadAccountCurrentConcurrencyByID(ctx context.Context, accountIDs []string) (map[string]int, error)
}

// HashSecret mirrors storage/crypto.ts hashSecret (sha256 hex): the gateway
// runtime cache keys are API key hashes, never raw keys.
func HashSecret(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// Options configures the Service. Zero values keep the Node standalone
// defaults (no shared cache, no bus, stale fallback enabled).
type Options struct {
	// Clock injects time for tests; defaults to the system clock.
	Clock Clock
	// Logger receives stale-refresh and shared-cache failure warns.
	Logger Logger
	// Bus wires the K5 invalidation bus. Nil keeps the service self-contained.
	Bus *inval.Bus
	// Shared enables the Redis shared-cache layer (cacheDriver === 'redis').
	// Nil keeps every app cache process-local like the Node memory mode.
	Shared SharedCacheFactory
	// UpdateAgeOnGet mirrors updateAgeOnGet: runtimeMode === 'standalone'.
	UpdateAgeOnGet bool
	// SyncInvalidationsOnRead mirrors runtimeStateDriver === 'redis': pre-read
	// forced version sync against the bus shared store.
	SyncInvalidationsOnRead bool
	// Orderer is the dynamic group-binding orderer (G08 seam); nil keeps the
	// stored binding order.
	Orderer GroupBindingOrderer
	// ClearSettingsCache mirrors clearSettingsRepositoryCache: invoked when a
	// settings invalidation also clears the settings repository cache.
	ClearSettingsCache func()
}

// Service is the Go gateway runtime read-only cache. All exported methods
// mirror the runtime-cache.service.ts export surface one to one.
type Service struct {
	models ReadModels
	opts   Options
	clock  Clock
	logger Logger

	sharedSettings  SharedCache
	sharedGroup     SharedCache
	sharedCatalog   SharedCache
	sharedRouteIdx  SharedCache
	sharedInspection SharedCache

	runtimeCache  *entryCache[string, gatewayRuntimeCacheEntry]
	identityCache *entryCache[string, gatewayRuntimeAPIKeyIdentity]
	settingsCache *entryCache[string, GatewaySettings]
	groupCache    *entryCache[string, groupUsageAccessCacheEntry]
	accountsCache *entryCache[string, openAIAccountsCacheEntry]
	catalogCache  *entryCache[string, []ProviderModelCatalogItem]
	routeIdxCache *entryCache[string, providerModelRouteIndexCacheEntry]
	inspectCache  *entryCache[string, responseInspectionPolicyCacheEntry]

	// keysByAPIKeyID mirrors gatewayRuntimeCacheKeysByApiKeyId.
	keysMu          sync.Mutex
	keysByAPIKeyID  map[string]map[string]struct{}

	// generations mirror the Node process-local epochs.
	mu                      sync.Mutex
	runtimeGeneration       int64
	apiKeyRuntimeGeneration int64
	catalogGeneration       int64
	pendingRuntimeLoads     map[string]*runtimeLoad
	pendingGroupRefreshes   map[string]*refreshCall
	pendingAccountRefreshes map[string]*refreshCall
	pendingCatalogLoads     map[string]*catalogLoad
	pendingInspectRefreshes map[string]*refreshCall

	sharedFailureMu       sync.Mutex
	sharedFailureLoggedAt map[string]time.Time

	lastSeenMu    sync.Mutex
	lastSeenVer   map[string]int64
	stopSubs      []func()
}

// gatewayRuntimeCacheEntry mirrors GatewayRuntimeCacheEntry.
type gatewayRuntimeCacheEntry struct {
	runtime        GatewayRuntime
	revalidateAtMs int64
}

// gatewayRuntimeAPIKeyIdentity mirrors GatewayRuntimeApiKeyIdentity.
type gatewayRuntimeAPIKeyIdentity struct {
	apiKeyID string
}

// groupUsageAccessCacheEntry mirrors GroupUsageAccessCacheEntry; value nil is
// the Node `false` negative cache.
type groupUsageAccessCacheEntry struct {
	value          *GroupUsageAccessMetadata
	revalidateAtMs int64
}

// openAIAccountsCacheEntry mirrors OpenAIAccountsCacheEntry.
type openAIAccountsCacheEntry struct {
	accounts       []OpenAIAccountSecret
	revalidateAtMs int64
}

// responseInspectionPolicyCacheEntry mirrors ResponseInspectionPolicyCacheEntry.
type responseInspectionPolicyCacheEntry struct {
	policies       []ResponseInspectionPolicySummary
	revalidateAtMs int64
}

// providerModelRouteIndexCacheEntry mirrors ProviderModelRouteIndexCacheEntry.
type providerModelRouteIndexCacheEntry struct {
	index map[string][]string
}

// New builds the service and subscribes the invalidation handlers to the bus
// (Node registerGatewayRuntimeCacheInvalidator /
// registerGatewayApiKeyValidationCacheInvalidator). models is required.
func New(models ReadModels, opts Options) (*Service, error) {
	if models == nil {
		return nil, errors.New("gatewayruntimecache 需要 ReadModels")
	}
	clock := opts.Clock
	if clock == nil {
		clock = SystemClock()
	}
	enabled := opts.Shared == nil
	updateAge := opts.UpdateAgeOnGet
	s := &Service{
		models: models,
		opts:   opts,
		clock:  clock,
		logger: opts.Logger,

		runtimeCache: newEntryCache[string, gatewayRuntimeCacheEntry](runtimeCacheName, cacheMaxEntries, gatewayRuntimeRetainTTL, updateAge, true, clock, nil, nil),
		settingsCache: newEntryCache[string, GatewaySettings](settingsCacheName, 1, gatewaySettingsTTL, false, enabled, clock, nil, nil),
		groupCache:    newEntryCache[string, groupUsageAccessCacheEntry](groupUsageAccessCacheName, 1000, groupUsageAccessRetainTTL, false, enabled, clock, nil, nil),
		accountsCache: newEntryCache[string, openAIAccountsCacheEntry]("gateway:openai-accounts", 1000, openAIAccountsRetainTTL, false, enabled, clock, nil, nil),
		catalogCache:  newEntryCache[string, []ProviderModelCatalogItem](providerModelCatalogCacheName, 1000, providerModelCatalogTTL, false, enabled, clock, nil, nil),
		routeIdxCache: newEntryCache[string, providerModelRouteIndexCacheEntry](providerModelRouteIndexCacheName, 1000, providerModelCatalogTTL, false, enabled, clock, nil, nil),
		inspectCache:  newEntryCache[string, responseInspectionPolicyCacheEntry](responseInspectionPolicyCacheName, 100, responseInspectionPolicyRetainTTL, false, enabled, clock, nil, nil),

		keysByAPIKeyID:          map[string]map[string]struct{}{},
		pendingRuntimeLoads:     map[string]*runtimeLoad{},
		pendingGroupRefreshes:   map[string]*refreshCall{},
		pendingAccountRefreshes: map[string]*refreshCall{},
		pendingCatalogLoads:     map[string]*catalogLoad{},
		pendingInspectRefreshes: map[string]*refreshCall{},
		sharedFailureLoggedAt:   map[string]time.Time{},
		lastSeenVer:             map[string]int64{},
	}
	// The identity cache dispose/onClear hooks reference s, so the cache is
	// attached after the struct literal.
	s.identityCache = newEntryCache[string, gatewayRuntimeAPIKeyIdentity](runtimeAPIKeyIdentityCacheName, cacheMaxEntries, gatewayRuntimeRetainTTL, updateAge, true, clock,
		func(cacheKey string, identity gatewayRuntimeAPIKeyIdentity) {
			s.removeGatewayRuntimeCacheIndex(identity.apiKeyID, cacheKey)
		},
		func() { s.clearGatewayRuntimeCacheIndex() })
	if opts.Shared != nil {
		s.sharedSettings = opts.Shared.Cache(settingsCacheName)
		s.sharedGroup = opts.Shared.Cache(groupUsageAccessCacheName)
		s.sharedCatalog = opts.Shared.Cache(providerModelCatalogCacheName)
		s.sharedRouteIdx = opts.Shared.Cache(providerModelRouteIndexCacheName)
		s.sharedInspection = opts.Shared.Cache(responseInspectionPolicyCacheName)
	}
	if opts.Bus != nil {
		s.stopSubs = append(s.stopSubs,
			opts.Bus.Subscribe(inval.TopicGatewayRuntime, s.handleRuntimeTopicInvalidation),
			opts.Bus.Subscribe(inval.TopicGatewayAPIKeyValidation, s.handleAPIKeyValidationTopicInvalidation),
		)
	}
	return s, nil
}

// Close unsubscribes from the invalidation bus.
func (s *Service) Close() {
	for _, stop := range s.stopSubs {
		stop()
	}
	s.stopSubs = nil
}

// handleRuntimeTopicInvalidation mirrors registerGatewayRuntimeCacheInvalidator(
// clearGatewayRuntimeCache): the topic reason drives the settings/model-catalog
// discrimination.
func (s *Service) handleRuntimeTopicInvalidation(_ string, reason string) {
	s.ClearGatewayRuntimeCache(reason)
}

// apikey validation topic reasons carry the apiKeyID suffix (K5
// BusInvalidator appends it so subscribers can scope the flush).
var apiKeyReasonPattern = regexp.MustCompile(`\s+([^\s]+)\s*$`)

// handleAPIKeyValidationTopicInvalidation mirrors
// registerGatewayApiKeyValidationCacheInvalidator: the K5 bus does not carry
// the key hashes, so the suffix token of the reason scopes the targeted
// invalidation; unparseable reasons fall back to the full clear exactly like
// the Node runtime_state path (keyHashes undefined).
func (s *Service) handleAPIKeyValidationTopicInvalidation(_ string, reason string) {
	apiKeyID := ""
	if match := apiKeyReasonPattern.FindStringSubmatch(reason); match != nil {
		apiKeyID = match[1]
	}
	s.InvalidateGatewayRuntimeCacheByAPIKeyID(apiKeyID, nil)
}

// ---------------------------------------------------------------------------
// shared helpers
// ---------------------------------------------------------------------------

func (s *Service) nowMs() int64 { return s.clock.Now().UnixMilli() }

// isEntryFresh mirrors isGatewayRuntimeCacheEntryFresh: entries without a
// revalidate stamp are always fresh.
func isEntryFresh(revalidateAtMs int64, now int64) bool {
	return revalidateAtMs == 0 || revalidateAtMs > now
}

// syncInvalidationsBestEffort mirrors syncGatewayCacheInvalidationsBestEffort:
// pull the shared topic versions, clear on change, never fail the read.
func (s *Service) syncInvalidationsBestEffort(ctx context.Context) {
	if !s.opts.SyncInvalidationsOnRead || s.opts.Bus == nil {
		return
	}
	bus := s.opts.Bus
	if err := bus.SyncFromShared(ctx, inval.TopicGatewayRuntime, inval.TopicGatewayAPIKeyValidation); err != nil {
		// The invalidation helper records the Redis failure upstream; cached AI
		// runtime stays usable until bounded local retention expires.
		return
	}
	for _, topic := range []string{inval.TopicGatewayRuntime, inval.TopicGatewayAPIKeyValidation} {
		version := bus.Version(topic)
		s.lastSeenMu.Lock()
		previous, seen := s.lastSeenVer[topic]
		s.lastSeenVer[topic] = version
		s.lastSeenMu.Unlock()
		if seen && previous != version {
			s.ClearGatewayRuntimeCache("")
		}
	}
}

// syncInvalidationsForRuntime mirrors the forced
// syncGatewayCacheInvalidationsFromRuntimeState({force: true}) before the
// runtime read: a fresh hot key must observe cross-instance invalidation.
func (s *Service) syncInvalidationsForRuntime(ctx context.Context) {
	s.syncInvalidationsBestEffort(ctx)
}

// logSharedFailure mirrors logGatewaySharedCacheFailure: one warn per event
// per 30s window.
func (s *Service) logSharedFailure(event string, err error) {
	if s.logger == nil {
		return
	}
	now := s.clock.Now()
	s.sharedFailureMu.Lock()
	last, ok := s.sharedFailureLoggedAt[event]
	if ok && now.Sub(last) < sharedCacheFailureLogInterval {
		s.sharedFailureMu.Unlock()
		return
	}
	s.sharedFailureLoggedAt[event] = now
	s.sharedFailureMu.Unlock()
	s.logger.Warn(event, map[string]any{"err": err.Error()}, "")
}

// gatewayCacheKey mirrors gatewayCacheKey.
func gatewayCacheKey(groupID, systemAccountID string) string {
	return groupID + ":" + systemAccountID
}

// gatewayOpenAIAccountsCacheKey mirrors gatewayOpenAIAccountsCacheKey.
func gatewayOpenAIAccountsCacheKey(groupID, systemAccountID, requestedModel, requestedEndpointFamily string) string {
	modelKey := normalizeProviderModelRouteKey(requestedModel)
	if modelKey == "" {
		return gatewayCacheKey(groupID, systemAccountID)
	}
	family := requestedEndpointFamily
	if family == "" {
		family = "any"
	}
	return gatewayCacheKey(groupID, systemAccountID) + ":model:" + modelKey + ":endpoint:" + family
}

// responseInspectionPolicyCacheKey mirrors responseInspectionPolicyCacheKey.
func responseInspectionPolicyCacheKey(protocolCode, providerCode string) string {
	return protocolCode + ":" + providerCode
}

// providerModelRouteIndexCacheKey mirrors providerModelRouteIndexCacheKey.
func providerModelRouteIndexCacheKey(providerCodes []string, systemAccountID string, includeUnpriced bool) string {
	priced := "priced"
	if includeUnpriced {
		priced = "unpriced"
	}
	return systemAccountID + ":" + priced + ":" + strings.Join(providerCodes, ",")
}

// providerModelCatalogCacheKey mirrors the Node catalog cache key join.
func providerModelCatalogCacheKey(input ModelCatalogListOptions) string {
	inactive := "active"
	if input.IncludeInactive {
		inactive = "inactive"
	}
	unpriced := "priced"
	if input.IncludeUnpriced {
		unpriced = "unpriced"
	}
	return strings.Join([]string{input.ProviderCode, input.SystemAccountID, inactive, unpriced}, ":")
}

// normalizeProviderModelRouteKey mirrors normalizeProviderModelRouteKey.
func normalizeProviderModelRouteKey(model string) string {
	return strings.TrimSpace(model)
}

// normalizedProviderRouteCodes mirrors normalizedProviderRouteCodes.
func normalizedProviderRouteCodes(providerCodes []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(providerCodes))
	for _, code := range providerCodes {
		trimmed := strings.TrimSpace(code)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	sortStrings(out)
	return out
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// ---------------------------------------------------------------------------
// rfc3339 + ttl helpers (shared/rfc3339.ts ports)
// ---------------------------------------------------------------------------

var rfc3339InstantPattern = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$`)

// rfc3339Millis mirrors rfc3339InstantMilliseconds: offset required, no bare
// datetimes. ok=false marks the malformed input Node throws on.
func rfc3339Millis(value string) (int64, bool) {
	text := strings.TrimSpace(value)
	if !rfc3339InstantPattern.MatchString(text) {
		return 0, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return 0, false
	}
	return parsed.UnixMilli(), true
}

// ttlBoundedByIsoExpiries mirrors ttlBoundedByIsoExpiries.
func ttlBoundedByIsoExpiries(baseTTL time.Duration, expiresAtValues []string, now int64) (time.Duration, error) {
	ttl := baseTTL
	for _, expiresAt := range expiresAtValues {
		expiresAtMs, ok := rfc3339Millis(expiresAt)
		if !ok {
			return 0, errors.New("缓存 expiresAt 必须是带 Z 或数值 offset 的 RFC3339 时间：" + expiresAt)
		}
		if delta := expiresAtMs - now; delta < int64(ttl) {
			ttl = time.Duration(delta)
		}
	}
	if ttl < time.Millisecond {
		return time.Millisecond, nil
	}
	return ttl, nil
}

// isoTimeExpired mirrors isoTimeExpired.
func isoTimeExpired(value string, now int64) (bool, error) {
	if value == "" {
		return false, nil
	}
	expiresAtMs, ok := rfc3339Millis(value)
	if !ok {
		return false, errors.New("网关运行态过期时间必须是带 Z 或数值 offset 的 RFC3339 时间：" + value)
	}
	return expiresAtMs <= now, nil
}

// runtimeCacheExpiryCandidates mirrors runtimeCacheExpiryCandidates.
func runtimeCacheExpiryCandidates(runtime GatewayRuntime) []string {
	candidates := []string{}
	if runtime.APIKey != nil && runtime.APIKey.ExpiresAt != nil {
		candidates = append(candidates, *runtime.APIKey.ExpiresAt)
	}
	if runtime.GroupAccess != nil && runtime.GroupAccess.GroupAuthorizationExpiresAt != nil {
		candidates = append(candidates, *runtime.GroupAccess.GroupAuthorizationExpiresAt)
	}
	for i := range runtime.Accounts {
		account := &runtime.Accounts[i]
		for _, value := range []*string{account.AccountExpiresAt, account.ExpiresAt, account.AccountAuthorizationExpiresAt, account.GroupAuthorizationExpiresAt} {
			if value != nil {
				candidates = append(candidates, *value)
			}
		}
	}
	return candidates
}

// gatewayRuntimeCacheTTL mirrors gatewayRuntimeCacheTtlMs.
func gatewayRuntimeCacheTTL(runtime GatewayRuntime, now int64) (time.Duration, error) {
	ttl := gatewayRuntimeTTL
	for _, expiresAt := range runtimeCacheExpiryCandidates(runtime) {
		expiresAtMs, ok := rfc3339Millis(expiresAt)
		if !ok {
			return 0, errors.New("网关运行态 expiresAt 必须是带 Z 或数值 offset 的 RFC3339 时间：" + expiresAt)
		}
		if delta := time.Duration(expiresAtMs - now); delta < ttl {
			ttl = delta
		}
	}
	if ttl < time.Millisecond {
		return time.Millisecond, nil
	}
	return ttl, nil
}

// groupUsageAccessCacheTTL mirrors groupUsageAccessCacheTtlMs.
func groupUsageAccessCacheTTL(value GroupUsageAccessMetadata, now int64) (time.Duration, error) {
	values := []string{}
	if value.GroupAuthorizationExpiresAt != nil {
		values = append(values, *value.GroupAuthorizationExpiresAt)
	}
	return ttlBoundedByIsoExpiries(groupUsageAccessTTL, values, now)
}

// openAIAccountsCacheTTL mirrors openAIAccountsCacheTtlMs.
func openAIAccountsCacheTTL(accounts []OpenAIAccountSecret, now int64) (time.Duration, error) {
	values := []string{}
	for i := range accounts {
		account := &accounts[i]
		for _, value := range []*string{account.AccountExpiresAt, account.ExpiresAt, account.AccountAuthorizationExpiresAt, account.GroupAuthorizationExpiresAt} {
			if value != nil {
				values = append(values, *value)
			}
		}
	}
	return ttlBoundedByIsoExpiries(openAIAccountsTTL, values, now)
}

// ---------------------------------------------------------------------------
// usability checks (throw on malformed instants like the Node service)
// ---------------------------------------------------------------------------

// isGatewayAPIKeyRuntimeUsableAt mirrors isGatewayApiKeyRuntimeUsableAt.
func isGatewayAPIKeyRuntimeUsableAt(apiKey *GatewayAPIKeyRow, now int64) (bool, error) {
	if apiKey == nil {
		return false, nil
	}
	if apiKey.Status != "active" {
		return false, nil
	}
	expiresAt := ""
	if apiKey.ExpiresAt != nil {
		expiresAt = *apiKey.ExpiresAt
	}
	return notExpired(expiresAt, now)
}

// isGroupUsageAccessRuntimeUsableAt mirrors isGroupUsageAccessRuntimeUsableAt.
func isGroupUsageAccessRuntimeUsableAt(groupAccess *GroupUsageAccessMetadata, now int64) (bool, error) {
	if groupAccess == nil {
		return false, nil
	}
	expiresAt := ""
	if groupAccess.GroupAuthorizationExpiresAt != nil {
		expiresAt = *groupAccess.GroupAuthorizationExpiresAt
	}
	return notExpired(expiresAt, now)
}

// isOpenAIAccountRuntimeUsableAt mirrors isOpenAIAccountRuntimeUsableAt.
func isOpenAIAccountRuntimeUsableAt(account *OpenAIAccountSecret, now int64) (bool, error) {
	if account.Status != AccountStatusActive {
		return false, nil
	}
	for _, value := range []*string{account.AccountExpiresAt, account.ExpiresAt, account.AccountAuthorizationExpiresAt, account.GroupAuthorizationExpiresAt} {
		text := ""
		if value != nil {
			text = *value
		}
		usable, err := notExpired(text, now)
		if err != nil {
			return false, err
		}
		if !usable {
			return false, nil
		}
	}
	return true, nil
}

func notExpired(value string, now int64) (bool, error) {
	expired, err := isoTimeExpired(value, now)
	if err != nil {
		return false, err
	}
	return !expired, nil
}
