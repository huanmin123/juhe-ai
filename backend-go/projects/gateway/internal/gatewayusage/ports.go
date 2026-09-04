package gatewayusage

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"time"
)

// newUUID mirrors randomUUID(): a random RFC 4122 version-4 UUID string.
func newUUID() string {
	bytes := make([]byte, 16)
	if _, err := crand.Read(bytes); err != nil {
		panic(err)
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(bytes)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

// Ports frozen by G17 for the writer assembly (J-F / G20) and the external
// capabilities Node reaches through module singletons. Concrete adapters are
// wired at the composition root; tests in this package provide mocks.

// Clock ports the injected time source (Node Date.now / new Date()).
type Clock interface {
	Now() time.Time
}

// SystemClock reads the wall clock.
type SystemClock struct{}

// Now implements Clock.
func (SystemClock) Now() time.Time { return time.Now() }

// ClockFunc adapts a function to Clock.
type ClockFunc func() time.Time

// Now implements Clock.
func (f ClockFunc) Now() time.Time { return f() }

// UsageRecordIDFactory ports generateUsageRecordId(createdAt, randomUUID())
// (storage/usage-record-shards.ts). The shard-id format stays with the
// writer slice (J-F/G20); this package only guarantees a stable id is
// attached before delivery.
type UsageRecordIDFactory interface {
	GenerateUsageRecordID(createdAt string) string
}

// UsageRecorder ports enqueueUsageRecord (record-queue.service.ts): the
// single write-delivery entry for one normalized usage record. The in-tree
// MemoryUsageRecorder is the in-memory mock; the real Redis Stream / IPC /
// spool-backed writer is assembled by the J-F/G20 wave. Node callers never
// observe enqueue failures on the local/IPC paths (failures are counted and
// logged); returning an error here preserves that information for the
// dispatch pipeline while delivery stays non-blocking.
type UsageRecorder interface {
	EnqueueUsageRecord(ctx Ctx, input UsageRecordInput) error
}

// AuditDispatcher ports dispatchAuditLogToGo (audit-log-go-input.service.ts):
// one-shot best-effort delivery of one finalized (or in_progress) audit log.
// Node returns void and only logs dispatch failures, so the port returns
// nothing; the adapter owns budget preparation, HMAC signing and the HTTP
// POST (or the operationlog-producer-style direct store write).
type AuditDispatcher interface {
	DispatchAuditLog(ctx Ctx, input AuditLogInput)
}

// DispatchOverflowSpool ports persistUsageRecordForQueueOverflow: the
// performance-mode overflow compensation invoked when the finalization queue
// is full (usage-record-spool.ts persistUsageRecordToSpool).
type DispatchOverflowSpool interface {
	PersistOverflow(ctx Ctx, input UsageRecordInput) error
}

// Ctx is the dispatch context alias keeping port signatures compact.
type Ctx = context.Context

// UsageModelResolution mirrors ProviderUsageModelResolution
// (providers/drivers/registry.ts).
type UsageModelResolution struct {
	UpstreamModel         string
	ModelMappingApplied   bool
	ModelMappingSource    string
	SourceEndpointFamily  string
	UpstreamEndpointFamily string
}

// UsageModelAccount is the opaque account handle the model resolver and
// pricing catalog receive (mirrors the consumed OpenAIAccountSecret fields).
type UsageModelAccount struct {
	ID                        string
	Name                      string
	ProviderCode              string
	ProviderProtocolProfileID string
	ProxyURL                  string
	// UsageAccess carries the usage scope fields (UsageAccessFields).
	UsageAccess UsageAccessFields
	// Profile mirrors the protocol profile pick the driver registry reads;
	// nil means the default OpenAI profile.
	Profile *ProviderProtocolProfile
}

// ProviderProtocolProfile mirrors the consumed
// ProviderProtocolProfileDefinition fields (providers/drivers/registry.ts):
// the protocol identity the usage semantic resolver needs.
type ProviderProtocolProfile struct {
	ProviderCode     string
	ProtocolCode     string
	ProtocolVersion  string
	ProfileID        string
}

// UsageAccessFields mirrors UsageAccessFields (records.ts): the ten account
// scope fields attached to every usage record.
type UsageAccessFields struct {
	AccountOwnerSystemAccountID      string
	GroupOwnerSystemAccountID        string
	AccountAccessType                string
	GroupAccessType                  string
	AccountAuthorizationID           string
	AccountAuthorizationSourceType   string
	AccountAuthorizationSourceTeamID string
	GroupAuthorizationID             string
	GroupAuthorizationSourceType     string
	GroupAuthorizationSourceTeamID   string
}

// UsageModelResolver ports resolveGatewayUsageModel
// (providers/drivers/registry.ts): provider-driver-owned upstream model
// resolution. An unknown account resolves to
// {upstreamModel: requestedModel, modelMappingApplied: false}.
type UsageModelResolver interface {
	ResolveUsageModel(account UsageModelAccount, requestedModel string, sourceEndpointFamily string) UsageModelResolution
}

// UsageSemanticResolver ports usageSemanticForProfile
// (providers/drivers/registry.ts).
type UsageSemanticResolver interface {
	UsageSemanticForProfile(profile *ProviderProtocolProfile) string
}

// DefaultUsageProviderCodeResolver ports defaultGatewayUsageProviderCode()
// (GPT_VENDOR_CODE = 'gpt').
type DefaultUsageProviderCodeResolver interface {
	DefaultUsageProviderCode() string
}

// PricingCatalog ports the synchronous model-catalog pricing surface
// (model-pricing/model-catalog.service.ts). Every call is gated by the
// canUseSynchronousCatalogPricingInGatewayRequest rule (cacheDriver !==
// 'redis') which the service owns, so adapters stay pure.
type PricingCatalog interface {
	// ResolvePricingModel mirrors resolveCatalogPricingModel.
	ResolvePricingModel(providerCode string, systemAccountID string, model string) string
	// EstimateCost mirrors estimateCatalogCostUsd; nil = undefined.
	EstimateCost(input PricingCostInput) *float64
	// EstimateCacheReadCost mirrors estimateCatalogCacheReadCostUsd.
	EstimateCacheReadCost(input PricingCostInput) *float64
	// EstimateCacheWriteCost mirrors estimateCatalogCacheWriteCostUsd.
	EstimateCacheWriteCost(input PricingCostInput) *float64
}

// PricingCostInput mirrors the CostInput the catalog estimators take.
type PricingCostInput struct {
	ProviderCode        string
	SystemAccountID     string
	Model               string
	ServiceTier         string
	InputTokens         *int
	OutputTokens        *int
	CacheReadTokens     *int
	CacheWriteTokens    *int
	CacheWrite1hTokens  *int
	ThinkingTokens      *int
	InputImageTokens    *int
	OutputImageTokens   *int
	InputAudioTokens    *int
	OutputAudioTokens   *int
	OutputImageCount    *int
}

// UpstreamFailureMetricRecorder ports recordGatewayUpstreamFailureMetric
// (shared/prometheus-metrics.ts).
type UpstreamFailureMetricRecorder interface {
	RecordUpstreamFailure(failureClass string, statusCode *int, reasonClass string)
}

// Logger ports the request logger (getRequestLogger) surface this slice
// uses: warn for the gateway failure log lines, debug for probe traffic,
// warn for queue/diagnostic events.
type Logger interface {
	Debug(msg string, fields map[string]any)
	Warn(msg string, fields map[string]any)
	Error(msg string, fields map[string]any)
}
