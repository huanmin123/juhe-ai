package gatewayproxyhealth

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Ports runtime/proxy-health.service.ts: upstream bucket runtime health
// (ordering avoidance, failure recording lives in proxyhealthrecord.go).

// GatewayUpstreamBucketScope mirrors GatewayUpstreamBucketScope.
type GatewayUpstreamBucketScope string

const (
	BucketScopeAll      GatewayUpstreamBucketScope = "all"
	BucketScopeProxy    GatewayUpstreamBucketScope = "proxy"
	BucketScopeUpstream GatewayUpstreamBucketScope = "upstream"
)

// AccountSample mirrors one [accountId, failedAtMs] evidence tuple; the JSON
// form is a two-element array (interop with Node-written entries).
type AccountSample struct {
	AccountID  string
	FailedAtMs int64
}

// MarshalJSON renders ["<id>", <ms>].
func (s AccountSample) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{s.AccountID, s.FailedAtMs})
}

// UnmarshalJSON accepts ["<id>", <ms>].
func (s *AccountSample) UnmarshalJSON(data []byte) error {
	var pair []json.RawMessage
	if err := json.Unmarshal(data, &pair); err != nil {
		return err
	}
	if len(pair) != 2 {
		return fmt.Errorf("account sample 必须是 [accountId, failedAtMs]")
	}
	if err := json.Unmarshal(pair[0], &s.AccountID); err != nil {
		return err
	}
	if err := json.Unmarshal(pair[1], &s.FailedAtMs); err != nil {
		return err
	}
	return nil
}

type upstreamBucketMutationGeneration struct {
	InstanceID string `json:"instanceId"`
	Sequence   int64  `json:"sequence"`
}

type upstreamBucketMutationObservation struct {
	observedAtMs int64
	generation   *upstreamBucketMutationGeneration
}

// upstreamBucketFailureEntry mirrors GatewayUpstreamBucketFailureEntry with
// the exact Node JSON field names (state entries interoperate during
// migration).
type upstreamBucketFailureEntry struct {
	Key                   string                            `json:"key"`
	Reason                string                            `json:"reason"`
	AccountSamples        []AccountSample                   `json:"accountSamples"`
	FailureCount          int64                             `json:"failureCount"`
	FirstFailedAtMs       int64                             `json:"firstFailedAtMs"`
	LastFailedAtMs        int64                             `json:"lastFailedAtMs"`
	LastFailureGeneration *upstreamBucketMutationGeneration `json:"lastFailureGeneration,omitempty"`
	AvoidUntilMs          *int64                            `json:"avoidUntilMs,omitempty"`
	HalfOpenStartedAtMs   *int64                            `json:"halfOpenStartedAtMs,omitempty"`
	HalfOpenUntilMs       *int64                            `json:"halfOpenUntilMs,omitempty"`
	HalfOpenAccountID     *string                           `json:"halfOpenAccountId,omitempty"`
}

func (e *upstreamBucketFailureEntry) clone() upstreamBucketFailureEntry {
	copy := *e
	copy.AccountSamples = append([]AccountSample(nil), e.AccountSamples...)
	return copy
}

// loadedBucketEntry carries the raw bytes next to the decoded value so CAS
// resends exactly what Redis holds (interop with Node-written entries).
type loadedBucketEntry struct {
	raw   json.RawMessage
	entry upstreamBucketFailureEntry
}

// ProxyHealthOrderResult mirrors GatewayProxyHealthOrderResult.
type ProxyHealthOrderResult struct {
	Accounts           []gatewayruntimecache.OpenAIAccountSecret
	Applied            bool
	AvoidedBucketKeys  []string
	AvoidedProxyKeys   []string
	AvoidedAccountIDs  []string
	HalfOpenBucketKeys []string
	HalfOpenAccountIDs []string
	BypassedAllAvoided bool
}

// GatewayProxyFailureDecision mirrors GatewayProxyFailureDecision. ProxyKey /
// DistinctAccountCount stay nil where Node leaves them undefined.
type GatewayProxyFailureDecision struct {
	Recorded             bool
	ProxyKey             *string
	BucketKeys           []string
	SuspectedBucketKeys  []string
	Suspected            bool
	DistinctAccountCount *int64
}

// FailureRecordOptions mirrors the { bucketScope } option; the zero value
// means Node's default 'all'.
type FailureRecordOptions struct {
	BucketScope GatewayUpstreamBucketScope
}

// ProxyHealthLogFunc receives the Node logger.warn payloads (fields, message).
type ProxyHealthLogFunc func(fields map[string]any, message string)

// ProxyHealthOptions mirrors runtimeConfig.gateway.proxyHealth* defaults.
type ProxyHealthOptions struct {
	FailureMaxEntries        int    // proxyHealthFailureMaxEntries (2000)
	FailureWindowMs          int64  // proxyHealthFailureWindowMs (60_000)
	AvoidTTLms               int64  // proxyHealthAvoidTtlMs (60_000)
	HalfOpenLeaseMs          int64  // proxyHealthHalfOpenLeaseMs (60_000)
	DistinctAccountThreshold int    // proxyHealthDistinctAccountThreshold (2)
	CASMaxAttempts           int    // proxyHealthCasMaxAttempts (1_024)
	MaxAccountSamples        int    // proxyHealthMaxAccountSamples (256)
	MutationInstanceID       string // randomBytes(12).toString('hex') in Node
}

func (o ProxyHealthOptions) normalized() ProxyHealthOptions {
	if o.FailureMaxEntries <= 0 {
		o.FailureMaxEntries = 2_000
	}
	if o.FailureWindowMs <= 0 {
		o.FailureWindowMs = 60_000
	}
	if o.AvoidTTLms <= 0 {
		o.AvoidTTLms = 60_000
	}
	if o.HalfOpenLeaseMs <= 0 {
		o.HalfOpenLeaseMs = 60_000
	}
	if o.DistinctAccountThreshold <= 0 {
		o.DistinctAccountThreshold = 2
	}
	if o.CASMaxAttempts <= 0 {
		o.CASMaxAttempts = 1_024
	}
	if o.MaxAccountSamples <= 0 {
		o.MaxAccountSamples = 256
	}
	if o.MutationInstanceID == "" {
		o.MutationInstanceID = NewRandomHex(12)
	}
	return o
}

// ProxyHealthService is the upstream bucket health service. A nil stateStore
// selects the memory driver (Node runtimeStateDriver !== 'redis').
type ProxyHealthService struct {
	clock      Clock
	opts       ProxyHealthOptions
	stateStore RuntimeStateStore
	log        ProxyHealthLogFunc

	mu        sync.Mutex
	entries   map[string]*memoryBucketEntry
	order     *list.List // of string keys; JS Map keeps insert order on re-set
	elementOf map[string]*list.Element

	instanceID string
	sequence   atomic.Int64
}

type memoryBucketEntry struct {
	value     upstreamBucketFailureEntry
	expiresAt int64
}

// NewProxyHealthService builds the service. stateStore nil → memory driver.
func NewProxyHealthService(clock Clock, stateStore RuntimeStateStore, opts ProxyHealthOptions, log ProxyHealthLogFunc) *ProxyHealthService {
	normalized := opts.normalized()
	return &ProxyHealthService{
		clock:      clock,
		opts:       normalized,
		stateStore: stateStore,
		log:        log,
		entries:    map[string]*memoryBucketEntry{},
		order:      list.New(),
		elementOf:  map[string]*list.Element{},
		instanceID: normalized.MutationInstanceID,
	}
}

func (s *ProxyHealthService) nowMs() int64 { return ClockNowMs(s.clock) }

func (s *ProxyHealthService) logWarn(fields map[string]any, message string) {
	if s.log != nil {
		s.log(fields, message)
	}
}

func int64Ptr(v int64) *int64 { return &v }

func (s *ProxyHealthService) nextObservation(observedAtMs int64) upstreamBucketMutationObservation {
	return upstreamBucketMutationObservation{
		observedAtMs: observedAtMs,
		generation: &upstreamBucketMutationGeneration{
			InstanceID: s.instanceID,
			Sequence:   s.sequence.Add(1),
		},
	}
}

// shouldUseRedis mirrors shouldUseRedisUpstreamBucketHealthState.
func (s *ProxyHealthService) shouldUseRedis() bool { return s.stateStore != nil }

// redisBucketStateKey mirrors redisBucketStateKey.
func redisBucketStateKey(key string) string { return "bucket:" + key }

func redisBucketCASExhaustedError(key, operation string, attempts int) error {
	return fmt.Errorf("上游桶 Redis CAS 重试耗尽（%d 次）：%s:%s", attempts, operation, bucketKeyForLog(key))
}

// ---------------------------------------------------------------------------
// Memory entry store (JS Map semantics: re-set keeps insertion position).

func (s *ProxyHealthService) getMemoryEntry(key string) (upstreamBucketFailureEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getMemoryEntryLocked(key)
}

func (s *ProxyHealthService) getMemoryEntryLocked(key string) (upstreamBucketFailureEntry, bool) {
	entry, ok := s.entries[key]
	if !ok {
		return upstreamBucketFailureEntry{}, false
	}
	if entry.expiresAt <= s.nowMs() {
		s.deleteMemoryEntryLocked(key)
		return upstreamBucketFailureEntry{}, false
	}
	return entry.value, true
}

func (s *ProxyHealthService) deleteMemoryEntryLocked(key string) {
	if element, ok := s.elementOf[key]; ok {
		s.order.Remove(element)
		delete(s.elementOf, key)
	}
	delete(s.entries, key)
}

func (s *ProxyHealthService) setMemoryEntry(key string, value upstreamBucketFailureEntry, ttlMs int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setMemoryEntryLocked(key, value, ttlMs)
}

func (s *ProxyHealthService) setMemoryEntryLocked(key string, value upstreamBucketFailureEntry, ttlMs int64) {
	now := s.nowMs()
	effectiveTTL := s.redisBucketFailureEntryTTLMs(&value, now, ttlMs)
	if existing, ok := s.entries[key]; ok {
		existingExpiresAt := existing.expiresAt
		existing.value = value
		existing.expiresAt = maxInt64(existingExpiresAt, now+effectiveTTL)
		return
	}
	s.entries[key] = &memoryBucketEntry{value: value, expiresAt: now + effectiveTTL}
	s.elementOf[key] = s.order.PushBack(key)
	s.evictOldestLocked()
}

func (s *ProxyHealthService) evictOldestLocked() {
	for len(s.entries) > s.opts.FailureMaxEntries {
		element := s.order.Front()
		if element == nil {
			return
		}
		key := element.Value.(string)
		s.order.Remove(element)
		delete(s.elementOf, key)
		delete(s.entries, key)
	}
}

// ---------------------------------------------------------------------------
// Bucket key derivation.

// GatewayProxyKey mirrors gatewayProxyKey: ok=false mirrors undefined.
func GatewayProxyKey(account gatewayruntimecache.OpenAIAccountSecret) (string, bool) {
	if account.ProxyProfileID != nil && *account.ProxyProfileID != "" {
		return fmt.Sprintf("proxy:profile:%s", *account.ProxyProfileID), true
	}
	if account.ProxyURL != nil && *account.ProxyURL != "" {
		return fmt.Sprintf("proxy:url:%s", proxyURLKeyHash(*account.ProxyURL)), true
	}
	return "", false
}

func proxyURLKeyHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// GatewayUpstreamBucketKeys mirrors gatewayUpstreamBucketKeys.
func GatewayUpstreamBucketKeys(account gatewayruntimecache.OpenAIAccountSecret, scope GatewayUpstreamBucketScope) []string {
	keys := make([]string, 0, 3)
	if proxyKey, ok := GatewayProxyKey(account); ok && scope != BucketScopeUpstream {
		keys = append(keys, proxyKey)
	}
	if scope != BucketScopeProxy {
		if baseKey := gatewayBaseURLKey(account); baseKey != "" {
			keys = append(keys, baseKey)
		}
		keys = append(keys, fmt.Sprintf("provider:%s", account.ProviderCode))
	}
	ownerScope := account.AccountOwnerSystemAccountID
	if ownerScope == "" {
		ownerScope = account.SystemAccountID
	}
	if ownerScope == "" {
		ownerScope = account.ID
	}
	seen := make(map[string]struct{}, len(keys))
	output := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		output = append(output, fmt.Sprintf("%s:owner:%s", key, ownerScope))
	}
	return output
}

func gatewayBaseURLKey(account gatewayruntimecache.OpenAIAccountSecret) string {
	if account.Type == "oauth" && isOpenAIProtocolProfile(account.ProtocolCode, account.ProtocolVersion) {
		return "baseUrl:https://chatgpt.com/backend-api/codex"
	}
	normalized := normalizeOpenAIBaseURLForBucket(account.BaseURL)
	if normalized == "" {
		return ""
	}
	return "baseUrl:" + normalized
}

// isOpenAIProtocolProfile mirrors domain/provider-protocol.ts
// isOpenAIProtocolProfile (openai + v1, trimmed lowercase).
func isOpenAIProtocolProfile(protocolCode, protocolVersion string) bool {
	return normalizeProviderToken(protocolCode) == "openai" && normalizeProviderToken(protocolVersion) == "v1"
}

func normalizeProviderToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// normalizeOpenAIBaseURLForBucket mirrors normalizeOpenAIBaseUrlForBucket.
// Node's URL constructor rejects scheme-less inputs; Go's url.Parse accepts
// them, so an absent scheme/host falls back to the lowercased text like Node.
func normalizeOpenAIBaseURLForBucket(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	trimmed := strings.TrimRight(strings.TrimSpace(value), "/")
	if !strings.HasSuffix(trimmed, "/v1") {
		trimmed = trimmed + "/v1"
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return strings.ToLower(trimmed)
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	pathname := strings.TrimRight(parsed.Path, "/")
	if pathname == "" {
		pathname = "/v1"
	}
	// Node renders `${url.protocol}//${url.host}${pathname}`; url.protocol
	// carries the trailing colon.
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host) + pathname
}

func isProxyBucketKey(key string) bool { return strings.HasPrefix(key, "proxy:") }

func isProviderBucketKey(key string) bool { return strings.HasPrefix(key, "provider:") }

func upstreamBucketType(key string) string {
	separatorIndex := strings.Index(key, ":")
	if separatorIndex > 0 {
		return key[:separatorIndex]
	}
	return "unknown"
}

// bucketKeysForLog mirrors bucketKeysForLog.
func bucketKeysForLog(keys []string) []string {
	output := make([]string, len(keys))
	for i, key := range keys {
		output[i] = bucketKeyForLog(key)
	}
	return output
}

// bucketKeyForLog mirrors bucketKeyForLog (proxy:url payloads are trimmed or
// replaced with '[configured]'; the stored keys are hashes in practice).
func bucketKeyForLog(key string) string {
	if !strings.HasPrefix(key, "proxy:url:") {
		return key
	}
	proxyURL := key[len("proxy:url:"):]
	trimmed := strings.TrimSpace(proxyURL)
	if trimmed == "" {
		return "proxy:url:[configured]"
	}
	return "proxy:url:" + trimmed
}

// gatewayFailureEvidenceAccountID mirrors the Node helper.
func gatewayFailureEvidenceAccountID(account gatewayruntimecache.OpenAIAccountSecret) string {
	if account.CredentialSourceAccountID != nil {
		if trimmed := strings.TrimSpace(*account.CredentialSourceAccountID); trimmed != "" {
			return trimmed
		}
	}
	return account.ID
}

// ---------------------------------------------------------------------------
// Ordering.

func emptyOrderResult(accounts []gatewayruntimecache.OpenAIAccountSecret) ProxyHealthOrderResult {
	return ProxyHealthOrderResult{
		Accounts:           accounts,
		Applied:            false,
		AvoidedBucketKeys:  []string{},
		AvoidedProxyKeys:   []string{},
		AvoidedAccountIDs:  []string{},
		HalfOpenBucketKeys: []string{},
		HalfOpenAccountIDs: []string{},
		BypassedAllAvoided: false,
	}
}

// OrderGatewayAccountsByUpstreamBucketHealth mirrors
// orderGatewayAccountsByUpstreamBucketHealth (memory driver).
func (s *ProxyHealthService) OrderGatewayAccountsByUpstreamBucketHealth(
	accounts []gatewayruntimecache.OpenAIAccountSecret,
	modelPriority *gatewayrouting.GatewayAccountModelPriority,
) ProxyHealthOrderResult {
	if len(accounts) == 0 {
		return emptyOrderResult(accounts)
	}

	now := s.nowMs()
	entries := newBucketEntryMap()
	persistEntry := func(entry upstreamBucketFailureEntry, ttlMs int64) {
		entries.Store(entry.Key, &loadedBucketEntry{entry: entry})
		s.setMemoryEntry(entry.Key, entry, ttlMs)
	}
	specificOrder := s.orderAccountsByActiveBucketScopeWithMemoryLoad(accounts, now, bucketScopeSpecific, entries, persistEntry)
	if len(specificOrder.avoidedAccounts) > 0 && len(specificOrder.freshAccounts) > 0 {
		return orderResultForScope(accounts, specificOrder, modelPriority, false)
	}

	providerOrder := s.orderAccountsByActiveBucketScopeWithMemoryLoad(accounts, now, bucketScopeProvider, entries, persistEntry)
	if len(providerOrder.avoidedAccounts) == 0 && len(specificOrder.avoidedAccounts) == 0 {
		return emptyOrderResult(accounts)
	}

	if len(providerOrder.avoidedAccounts) > 0 && len(providerOrder.freshAccounts) > 0 {
		return orderResultForScope(accounts, providerOrder, modelPriority, false)
	}

	return mergeBypassedResult(accounts, specificOrder, providerOrder)
}

// OrderOpenAIAccountsByGatewayProxyHealth mirrors orderOpenAIAccountsByGatewayProxyHealth.
func (s *ProxyHealthService) OrderOpenAIAccountsByGatewayProxyHealth(
	accounts []gatewayruntimecache.OpenAIAccountSecret,
	modelPriority *gatewayrouting.GatewayAccountModelPriority,
) ProxyHealthOrderResult {
	return s.OrderGatewayAccountsByUpstreamBucketHealth(accounts, modelPriority)
}

// OrderGatewayAccountsByUpstreamBucketHealthAsync mirrors the async variant:
// the Redis driver engages CAS-protected half-open lease claims.
func (s *ProxyHealthService) OrderGatewayAccountsByUpstreamBucketHealthAsync(
	ctx context.Context,
	accounts []gatewayruntimecache.OpenAIAccountSecret,
	modelPriority *gatewayrouting.GatewayAccountModelPriority,
) (ProxyHealthOrderResult, error) {
	if !s.shouldUseRedis() {
		return s.OrderGatewayAccountsByUpstreamBucketHealth(accounts, modelPriority), nil
	}
	if len(accounts) == 0 {
		return emptyOrderResult(accounts), nil
	}

	now := s.nowMs()
	entries, err := s.loadRedisBucketEntriesForAccounts(ctx, accounts)
	if err != nil {
		return ProxyHealthOrderResult{}, err
	}
	persistEntry := func(entry upstreamBucketFailureEntry, _ int64) {
		entries.Store(entry.Key, &loadedBucketEntry{entry: entry})
	}
	if err := s.claimRedisHalfOpenLeasesForAccounts(ctx, accounts, entries, now, bucketScopeSpecific); err != nil {
		return ProxyHealthOrderResult{}, err
	}
	specificOrder := s.orderAccountsByActiveBucketScopeWithEntries(accounts, now, bucketScopeSpecific, entries, persistEntry)
	if !(len(specificOrder.avoidedAccounts) > 0 && len(specificOrder.freshAccounts) > 0) {
		if err := s.claimRedisHalfOpenLeasesForAccounts(ctx, accounts, entries, now, bucketScopeProvider); err != nil {
			return ProxyHealthOrderResult{}, err
		}
	}
	return s.orderGatewayAccountsByUpstreamBucketHealthWithEntries(accounts, entries, persistEntry, modelPriority, now), nil
}

// OrderOpenAIAccountsByGatewayProxyHealthAsync mirrors the async alias.
func (s *ProxyHealthService) OrderOpenAIAccountsByGatewayProxyHealthAsync(
	ctx context.Context,
	accounts []gatewayruntimecache.OpenAIAccountSecret,
	modelPriority *gatewayrouting.GatewayAccountModelPriority,
) (ProxyHealthOrderResult, error) {
	return s.OrderGatewayAccountsByUpstreamBucketHealthAsync(ctx, accounts, modelPriority)
}

func (s *ProxyHealthService) orderGatewayAccountsByUpstreamBucketHealthWithEntries(
	accounts []gatewayruntimecache.OpenAIAccountSecret,
	entries *bucketEntryMap,
	persistEntry func(upstreamBucketFailureEntry, int64),
	modelPriority *gatewayrouting.GatewayAccountModelPriority,
	now int64,
) ProxyHealthOrderResult {
	if len(accounts) == 0 {
		return emptyOrderResult(accounts)
	}

	specificOrder := s.orderAccountsByActiveBucketScopeWithEntries(accounts, now, bucketScopeSpecific, entries, persistEntry)
	if len(specificOrder.avoidedAccounts) > 0 && len(specificOrder.freshAccounts) > 0 {
		return orderResultForScope(accounts, specificOrder, modelPriority, false)
	}

	providerOrder := s.orderAccountsByActiveBucketScopeWithEntries(accounts, now, bucketScopeProvider, entries, persistEntry)
	if len(providerOrder.avoidedAccounts) == 0 && len(specificOrder.avoidedAccounts) == 0 {
		return emptyOrderResult(accounts)
	}

	if len(providerOrder.avoidedAccounts) > 0 && len(providerOrder.freshAccounts) > 0 {
		return orderResultForScope(accounts, providerOrder, modelPriority, false)
	}

	return mergeBypassedResult(accounts, specificOrder, providerOrder)
}

type bucketScopeKind string

const (
	bucketScopeSpecific bucketScopeKind = "specific"
	bucketScopeProvider bucketScopeKind = "provider"
)

type bucketScopeOrdering struct {
	freshAccounts      []gatewayruntimecache.OpenAIAccountSecret
	halfOpenAccounts   []gatewayruntimecache.OpenAIAccountSecret
	avoidedAccounts    []gatewayruntimecache.OpenAIAccountSecret
	avoidedBucketKeys  map[string]struct{}
	avoidedProxyKeys   map[string]struct{}
	halfOpenBucketKeys map[string]struct{}
}

// bucketEntryMap mirrors the Node entries Map used inside one ordering pass.
type bucketEntryMap struct {
	m    map[string]*loadedBucketEntry
	keys []string
}

func newBucketEntryMap() *bucketEntryMap {
	return &bucketEntryMap{m: map[string]*loadedBucketEntry{}}
}

func (m *bucketEntryMap) Load(key string) (*loadedBucketEntry, bool) {
	entry, ok := m.m[key]
	return entry, ok
}

func (m *bucketEntryMap) Store(key string, entry *loadedBucketEntry) {
	if _, ok := m.m[key]; !ok {
		m.keys = append(m.keys, key)
	}
	m.m[key] = entry
}

func (m *bucketEntryMap) Delete(key string) {
	if _, ok := m.m[key]; !ok {
		return
	}
	delete(m.m, key)
	for i, existing := range m.keys {
		if existing == key {
			m.keys = append(m.keys[:i], m.keys[i+1:]...)
			break
		}
	}
}

// orderAccountsByActiveBucketScopeWithMemoryLoad mirrors
// orderAccountsByActiveBucketScope: preload memory entries, then run the
// shared ordering pass where persist writes back to memory.
func (s *ProxyHealthService) orderAccountsByActiveBucketScopeWithMemoryLoad(
	accounts []gatewayruntimecache.OpenAIAccountSecret,
	now int64,
	scope bucketScopeKind,
	entries *bucketEntryMap,
	persistEntry func(upstreamBucketFailureEntry, int64),
) bucketScopeOrdering {
	for _, account := range accounts {
		for _, key := range GatewayUpstreamBucketKeys(account, BucketScopeAll) {
			if _, ok := entries.Load(key); ok {
				continue
			}
			if value, exists := s.getMemoryEntry(key); exists {
				entries.Store(key, &loadedBucketEntry{entry: value})
			}
		}
	}
	return s.orderAccountsByActiveBucketScopeWithEntries(accounts, now, scope, entries, persistEntry)
}

func (s *ProxyHealthService) orderAccountsByActiveBucketScopeWithEntries(
	accounts []gatewayruntimecache.OpenAIAccountSecret,
	now int64,
	scope bucketScopeKind,
	entries *bucketEntryMap,
	persistEntry func(upstreamBucketFailureEntry, int64),
) bucketScopeOrdering {
	ordering := bucketScopeOrdering{
		avoidedBucketKeys:  map[string]struct{}{},
		avoidedProxyKeys:   map[string]struct{}{},
		halfOpenBucketKeys: map[string]struct{}{},
	}
	for _, account := range accounts {
		var blockedKeys, probeKeys []string
		for _, key := range GatewayUpstreamBucketKeys(account, BucketScopeAll) {
			if scope == bucketScopeProvider && !isProviderBucketKey(key) {
				continue
			}
			if scope == bucketScopeSpecific && isProviderBucketKey(key) {
				continue
			}
			state := s.upstreamBucketAccountStateWithEntries(key, account, now, entries, persistEntry)
			switch state {
			case "blocked":
				blockedKeys = append(blockedKeys, key)
			case "half_open_probe":
				probeKeys = append(probeKeys, key)
			}
		}
		if len(blockedKeys) > 0 {
			ordering.avoidedAccounts = append(ordering.avoidedAccounts, account)
			for _, key := range blockedKeys {
				ordering.avoidedBucketKeys[key] = struct{}{}
				if isProxyBucketKey(key) {
					ordering.avoidedProxyKeys[key] = struct{}{}
				}
			}
		} else if len(probeKeys) > 0 {
			ordering.halfOpenAccounts = append(ordering.halfOpenAccounts, account)
			for _, key := range probeKeys {
				ordering.halfOpenBucketKeys[key] = struct{}{}
			}
		} else {
			ordering.freshAccounts = append(ordering.freshAccounts, account)
		}
	}
	// Node returns freshAccounts: [...halfOpenAccounts, ...freshAccounts].
	ordering.freshAccounts = append(append([]gatewayruntimecache.OpenAIAccountSecret(nil), ordering.halfOpenAccounts...), ordering.freshAccounts...)
	return ordering
}

func orderResultForScope(
	accounts []gatewayruntimecache.OpenAIAccountSecret,
	ordering bucketScopeOrdering,
	modelPriority *gatewayrouting.GatewayAccountModelPriority,
	bypassed bool,
) ProxyHealthOrderResult {
	var modelRankByAccountID map[string]int
	if modelPriority != nil {
		modelRankByAccountID = modelPriority.RankByAccountID
	}
	ordered := append(append([]gatewayruntimecache.OpenAIAccountSecret(nil), ordering.freshAccounts...), ordering.avoidedAccounts...)
	view := func(account gatewayruntimecache.OpenAIAccountSecret) DispatchPriorityAccountView {
		priority := float64(account.Priority)
		super := account.SuperPriorityEnabled
		fallback := account.FallbackEnabled
		return DispatchPriorityAccountView{
			ID:                   account.ID,
			Priority:             &priority,
			SuperPriorityEnabled: &super,
			FallbackEnabled:      &fallback,
		}
	}
	ordered = PreserveGatewayAccountDispatchPriorityTiers(accounts, ordered, view, DispatchPriorityOrderOptions{ModelRankByAccountID: modelRankByAccountID})

	avoidedIDs := make([]string, 0, len(ordering.avoidedAccounts))
	for _, account := range ordering.avoidedAccounts {
		avoidedIDs = append(avoidedIDs, account.ID)
	}
	halfOpenIDs := make([]string, 0, len(ordering.halfOpenAccounts))
	for _, account := range ordering.halfOpenAccounts {
		halfOpenIDs = append(halfOpenIDs, account.ID)
	}
	return ProxyHealthOrderResult{
		Accounts:           ordered,
		Applied:            true,
		AvoidedBucketKeys:  bucketKeysForLog(setKeys(ordering.avoidedBucketKeys)),
		AvoidedProxyKeys:   bucketKeysForLog(setKeys(ordering.avoidedProxyKeys)),
		AvoidedAccountIDs:  avoidedIDs,
		HalfOpenBucketKeys: bucketKeysForLog(setKeys(ordering.halfOpenBucketKeys)),
		HalfOpenAccountIDs: halfOpenIDs,
		BypassedAllAvoided: bypassed,
	}
}

func mergeBypassedResult(
	accounts []gatewayruntimecache.OpenAIAccountSecret,
	specificOrder, providerOrder bucketScopeOrdering,
) ProxyHealthOrderResult {
	avoidedBucketKeys := setKeysUnion(specificOrder.avoidedBucketKeys, providerOrder.avoidedBucketKeys)
	avoidedProxyKeys := setKeysUnion(specificOrder.avoidedProxyKeys, providerOrder.avoidedProxyKeys)
	halfOpenBucketKeys := setKeysUnion(specificOrder.halfOpenBucketKeys, providerOrder.halfOpenBucketKeys)
	return ProxyHealthOrderResult{
		Accounts:           accounts,
		Applied:            false,
		AvoidedBucketKeys:  bucketKeysForLog(avoidedBucketKeys),
		AvoidedProxyKeys:   bucketKeysForLog(avoidedProxyKeys),
		AvoidedAccountIDs:  setKeysUnionAccountIDs(specificOrder.avoidedAccounts, providerOrder.avoidedAccounts),
		HalfOpenBucketKeys: bucketKeysForLog(halfOpenBucketKeys),
		HalfOpenAccountIDs: setKeysUnionAccountIDs(specificOrder.halfOpenAccounts, providerOrder.halfOpenAccounts),
		BypassedAllAvoided: true,
	}
}

func setKeys(set map[string]struct{}) []string {
	output := make([]string, 0, len(set))
	for key := range set {
		output = append(output, key)
	}
	sort.Strings(output)
	return output
}

func setKeysUnion(left, right map[string]struct{}) []string {
	merged := make(map[string]struct{}, len(left)+len(right))
	for key := range left {
		merged[key] = struct{}{}
	}
	for key := range right {
		merged[key] = struct{}{}
	}
	return setKeys(merged)
}

func setKeysUnionAccountIDs(left, right []gatewayruntimecache.OpenAIAccountSecret) []string {
	seen := map[string]struct{}{}
	output := make([]string, 0, len(left)+len(right))
	for _, account := range left {
		if _, dup := seen[account.ID]; dup {
			continue
		}
		seen[account.ID] = struct{}{}
		output = append(output, account.ID)
	}
	for _, account := range right {
		if _, dup := seen[account.ID]; dup {
			continue
		}
		seen[account.ID] = struct{}{}
		output = append(output, account.ID)
	}
	return output
}

// upstreamBucketAccountStateWithEntries mirrors the same-named Node helper.
func (s *ProxyHealthService) upstreamBucketAccountStateWithEntries(
	key string,
	account gatewayruntimecache.OpenAIAccountSecret,
	now int64,
	entries *bucketEntryMap,
	persistEntry func(upstreamBucketFailureEntry, int64),
) string {
	loaded, ok := entries.Load(key)
	if !ok || loaded.entry.AvoidUntilMs == nil {
		return "normal"
	}
	if *loaded.entry.AvoidUntilMs > now {
		return "blocked"
	}
	halfOpenEntry := s.ensureHalfOpenProbe(loaded.entry, account, now, persistEntry)
	if halfOpenEntry.HalfOpenAccountID != nil && *halfOpenEntry.HalfOpenAccountID == account.ID {
		return "half_open_probe"
	}
	return "blocked"
}

// ensureHalfOpenProbe mirrors ensureHalfOpenProbe.
func (s *ProxyHealthService) ensureHalfOpenProbe(
	entry upstreamBucketFailureEntry,
	account gatewayruntimecache.OpenAIAccountSecret,
	now int64,
	persistEntry func(upstreamBucketFailureEntry, int64),
) upstreamBucketFailureEntry {
	if entry.HalfOpenAccountID != nil && entry.HalfOpenUntilMs != nil && *entry.HalfOpenUntilMs > now {
		return entry
	}

	halfOpenUntilMs := now + s.opts.HalfOpenLeaseMs
	accountID := account.ID
	nextEntry := entry.clone()
	nextEntry.HalfOpenStartedAtMs = int64Ptr(now)
	nextEntry.HalfOpenUntilMs = int64Ptr(halfOpenUntilMs)
	nextEntry.HalfOpenAccountID = &accountID
	if persistEntry != nil {
		persistEntry(nextEntry, s.opts.AvoidTTLms+s.opts.FailureWindowMs)
	} else {
		s.setMemoryEntry(entry.Key, nextEntry, s.opts.AvoidTTLms+s.opts.FailureWindowMs)
	}
	s.logWarn(map[string]any{
		"event":         "gateway_upstream_failure_bucket_half_opened",
		"bucketKey":     bucketKeyForLog(entry.Key),
		"bucketType":    upstreamBucketType(entry.Key),
		"accountId":     account.ID,
		"halfOpenUntil": ISOStringMs(halfOpenUntilMs),
	}, "上游桶运行态避让 TTL 到期，已放行一个半开探测账号")
	return nextEntry
}

// claimRedisHalfOpenLeasesForAccounts mirrors the same-named Node helper.
func (s *ProxyHealthService) claimRedisHalfOpenLeasesForAccounts(
	ctx context.Context,
	accounts []gatewayruntimecache.OpenAIAccountSecret,
	entries *bucketEntryMap,
	now int64,
	scope bucketScopeKind,
) error {
	probeAccountByBucketKey := map[string]gatewayruntimecache.OpenAIAccountSecret{}
	probeKeyOrder := make([]string, 0, len(accounts))
	for _, account := range accounts {
		for _, key := range GatewayUpstreamBucketKeys(account, BucketScopeAll) {
			if scope == bucketScopeProvider && !isProviderBucketKey(key) {
				continue
			}
			if scope == bucketScopeSpecific && isProviderBucketKey(key) {
				continue
			}
			if _, exists := probeAccountByBucketKey[key]; !exists {
				probeAccountByBucketKey[key] = account
				probeKeyOrder = append(probeKeyOrder, key)
			}
		}
	}
	for _, key := range probeKeyOrder {
		account := probeAccountByBucketKey[key]
		current, ok := entries.Load(key)
		if !ok || current.entry.AvoidUntilMs == nil || *current.entry.AvoidUntilMs > now {
			continue
		}
		claimed, err := s.ensureRedisHalfOpenProbe(ctx, key, current, account, now)
		if err != nil {
			return err
		}
		if claimed != nil {
			entries.Store(key, claimed)
		} else {
			entries.Delete(key)
		}
	}
	return nil
}

// ensureRedisHalfOpenProbe mirrors ensureRedisHalfOpenProbe; a nil result
// mirrors the Node undefined return.
func (s *ProxyHealthService) ensureRedisHalfOpenProbe(
	ctx context.Context,
	key string,
	initialEntry *loadedBucketEntry,
	account gatewayruntimecache.OpenAIAccountSecret,
	now int64,
) (*loadedBucketEntry, error) {
	current := initialEntry
	for attempt := 0; attempt < s.opts.CASMaxAttempts; attempt++ {
		if current == nil || current.entry.AvoidUntilMs == nil || *current.entry.AvoidUntilMs > now {
			return current, nil
		}
		entry := current.entry
		if entry.HalfOpenAccountID != nil && entry.HalfOpenUntilMs != nil && *entry.HalfOpenUntilMs > now {
			return current, nil
		}

		halfOpenUntilMs := now + s.opts.HalfOpenLeaseMs
		accountID := account.ID
		nextEntry := entry.clone()
		nextEntry.HalfOpenStartedAtMs = int64Ptr(now)
		nextEntry.HalfOpenUntilMs = int64Ptr(halfOpenUntilMs)
		nextEntry.HalfOpenAccountID = &accountID
		applied, err := s.stateStore.CompareSetJSON(ctx, redisBucketStateKey(key), current.raw, nextEntry, s.opts.AvoidTTLms+s.opts.FailureWindowMs)
		if err != nil {
			return nil, err
		}
		if applied {
			s.logWarn(map[string]any{
				"event":         "gateway_upstream_failure_bucket_half_opened",
				"bucketKey":     bucketKeyForLog(key),
				"bucketType":    upstreamBucketType(key),
				"accountId":     account.ID,
				"halfOpenUntil": ISOStringMs(halfOpenUntilMs),
			}, "上游桶运行态避让 TTL 到期，已放行一个半开探测账号")
			return &loadedBucketEntry{entry: nextEntry}, nil
		}
		current, err = s.getRedisBucketFailureEntry(ctx, key)
		if err != nil {
			return nil, err
		}
	}
	return nil, redisBucketCASExhaustedError(key, "half_open_lease", s.opts.CASMaxAttempts)
}

func (s *ProxyHealthService) loadRedisBucketEntriesForAccounts(
	ctx context.Context,
	accounts []gatewayruntimecache.OpenAIAccountSecret,
) (*bucketEntryMap, error) {
	seen := map[string]struct{}{}
	var keys []string
	for _, account := range accounts {
		for _, key := range GatewayUpstreamBucketKeys(account, BucketScopeAll) {
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
	}
	entries := newBucketEntryMap()
	for _, key := range keys {
		loaded, err := s.getRedisBucketFailureEntry(ctx, key)
		if err != nil {
			return nil, err
		}
		if loaded != nil {
			entries.Store(key, loaded)
		}
	}
	return entries, nil
}

func (s *ProxyHealthService) getRedisBucketFailureEntry(ctx context.Context, key string) (*loadedBucketEntry, error) {
	raw, err := s.stateStore.GetJSON(ctx, redisBucketStateKey(key))
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, nil
	}
	var entry upstreamBucketFailureEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil, nil
	}
	return &loadedBucketEntry{raw: raw, entry: entry}, nil
}

func rawOrMarshal(raw json.RawMessage, value any) (json.RawMessage, error) {
	if raw != nil {
		return raw, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// redisBucketFailureEntryTTLMs mirrors redisBucketFailureEntryTtlMs: the
// entry survives at least until the avoid/half-open deadlines plus one window.
func (s *ProxyHealthService) redisBucketFailureEntryTTLMs(entry *upstreamBucketFailureEntry, now int64, minimumTtlMs int64) int64 {
	avoidRetentionMs := int64(0)
	if entry.AvoidUntilMs != nil {
		avoidRetentionMs = maxInt64(0, *entry.AvoidUntilMs-now) + s.opts.FailureWindowMs
	}
	halfOpenRetentionMs := int64(0)
	if entry.HalfOpenUntilMs != nil {
		halfOpenRetentionMs = maxInt64(0, *entry.HalfOpenUntilMs-now) + s.opts.FailureWindowMs
	}
	return maxInt64(maxInt64(1, minimumTtlMs), maxInt64(avoidRetentionMs, halfOpenRetentionMs))
}
