package gatewayhotquality

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhybrid"
)

// Hot quality store contracts mirroring
// backend/src/modules/gateway/runtime/hot-quality-store.ts.

const (
	// HotQualityUnknownModelFamily mirrors HOT_QUALITY_UNKNOWN_MODEL_FAMILY.
	HotQualityUnknownModelFamily = "unknown"
	// HotQualityModelFamilyCatalogLimit mirrors HOT_QUALITY_MODEL_FAMILY_CATALOG_LIMIT.
	HotQualityModelFamilyCatalogLimit = 256
	// HotQualityMinuteBucketCount mirrors HOT_QUALITY_MINUTE_BUCKET_COUNT.
	HotQualityMinuteBucketCount = 30
	// HotQualityKeyTTLMS mirrors HOT_QUALITY_KEY_TTL_MS (40 min).
	HotQualityKeyTTLMS = int64(40 * 60_000)
	// HotQualityTerminalTTLMS mirrors HOT_QUALITY_TERMINAL_TTL_MS (60 min).
	HotQualityTerminalTTLMS = int64(60 * 60_000)

	maxSafeInteger = int64(1)<<53 - 1
)

// HotQualityFirstByteEwmaAlpha mirrors HOT_QUALITY_FIRST_BYTE_EWMA_ALPHA.
// It deliberately stays a package-level variable: the EWMA below computes
// `1 - alpha` at runtime so the float64 result is bit-identical to the Node
// expression (a Go constant would fold 1-0.4 to exactly 0.6, which differs
// from the IEEE double subtraction).
var HotQualityFirstByteEwmaAlpha = float64(0.4)

// HotQualityFirstByteBucketUpperBoundsMS mirrors
// HOT_QUALITY_FIRST_BYTE_BUCKET_UPPER_BOUNDS_MS; the trailing null bound
// becomes math.MaxInt64 (unreachable: samples are clamped to maxSafeInteger).
var HotQualityFirstByteBucketUpperBoundsMS = [8]int64{1_000, 2_000, 5_000, 10_000, 20_000, 30_000, 60_000, math.MaxInt64}

// HotQualityModelFamily / HotQualityRequestLane / reliability / sample state
// mirror the branded string unions. gatewayhybrid exports the reliability and
// sample-state literals for the selection layer; they are reused below.
type (
	// HotQualityModelFamily mirrors HotQualityModelFamily.
	HotQualityModelFamily = string
	// HotQualityRequestLane mirrors HotQualityRequestLane.
	HotQualityRequestLane = string
	// HotQualityReliabilityLevel mirrors HotQualityReliabilityLevel (alias of
	// the gatewayhybrid selection-layer alias).
	HotQualityReliabilityLevel = gatewayhybrid.HotQualityReliabilityLevel
	// HotQualitySampleState mirrors HotQualitySampleState.
	HotQualitySampleState = string
)

// Request lanes (mirror the Node union).
const (
	RequestLaneText  HotQualityRequestLane = "text"
	RequestLaneImage HotQualityRequestLane = "image"
)

// Reliability levels re-exported from the selection layer literals.
const (
	HotQualityReliabilityUnknown   = gatewayhybrid.ReliabilityUnknown
	HotQualityReliabilityHealthy   = gatewayhybrid.ReliabilityHealthy
	HotQualityReliabilityUncertain = gatewayhybrid.ReliabilityUncertain
	HotQualityReliabilityUnhealthy = gatewayhybrid.ReliabilityUnhealthy
)

// Sample states re-exported from the selection layer literals.
const (
	HotQualitySampleCold    = gatewayhybrid.SampleStateCold
	HotQualitySampleWarming = gatewayhybrid.SampleStateWarming
	HotQualitySampleKnown   = gatewayhybrid.SampleStateKnown
)

// HotQualityScope mirrors HotQualityScope.
type HotQualityScope struct {
	AccountRuntimeKey string `json:"accountRuntimeKey"`
	ProtocolProfile   string `json:"protocolProfile"`
	RequestLane       string `json:"requestLane"`
	ModelFamily       string `json:"modelFamily"`
}

// Terminal outcome classes (mirror the Node union).
const (
	TerminalOutcomeCompletedResponse       = "completed_response"
	TerminalOutcomeUpstreamResponseFailure = "upstream_response_failure"
	TerminalOutcomeExplicitPolicyFailure   = "explicit_policy_failure"
	TerminalOutcomeTransportFailure        = "transport_failure"
	TerminalOutcomeTimeout                 = "timeout"
	TerminalOutcomeReadInterruption        = "read_interruption"
	TerminalOutcomeIncompleteResponse      = "incomplete_response"
	TerminalOutcomeUnknown                 = "unknown"
	TerminalOutcomeClientCancellation      = "client_cancellation"
)

// HotQualityTerminalOutcomeClass mirrors HotQualityTerminalOutcomeClass.
type HotQualityTerminalOutcomeClass = string

// Failure scopes (mirror the Node union).
const (
	FailureScopeNone           = "none"
	FailureScopeKey            = "key"
	FailureScopeProtocolModel  = "protocol_model"
	FailureScopeAccount        = "account"
	FailureScopeUpstreamBucket = "upstream_bucket"
)

// HotQualityFailureScope mirrors HotQualityFailureScope.
type HotQualityFailureScope = string

// Terminal sources (mirror the Node union).
const (
	TerminalSourceGatewayTransport = "gateway_transport"
	TerminalSourceUpstreamResponse = "upstream_response"
	TerminalSourceExplicitPolicy   = "explicit_policy"
	TerminalSourceRequestLifecycle = "request_lifecycle"
)

// HotQualityTerminalSource mirrors HotQualityTerminalSource.
type HotQualityTerminalSource = string

// HotQualityTerminalRecord mirrors HotQualityTerminalRecord.
type HotQualityTerminalRecord struct {
	TerminalOutcomeID string `json:"terminalOutcomeId"`
	OutcomeClass      string `json:"outcomeClass"`
	FailureScope      string `json:"failureScope"`
	Source            string `json:"source"`
	CreatedAtMs       int64  `json:"createdAtMs"`
}

// HotQualityFirstByteHistogram mirrors HotQualityFirstByteHistogram.
type HotQualityFirstByteHistogram = [8]int64

// HotQualityCounters mirrors HotQualityCounters.
type HotQualityCounters struct {
	Attempts                 int64                        `json:"attempts"`
	CompletedResponses       int64                        `json:"completedResponses"`
	UpstreamResponseFailures int64                        `json:"upstreamResponseFailures"`
	LocalTransportFailures   int64                        `json:"localTransportFailures"`
	Timeouts                 int64                        `json:"timeouts"`
	ReadInterruptions        int64                        `json:"readInterruptions"`
	IncompleteResponses      int64                        `json:"incompleteResponses"`
	ExplicitPolicyFailures   int64                        `json:"explicitPolicyFailures"`
	UnknownOutcomes          int64                        `json:"unknownOutcomes"`
	ClientCancellations      int64                        `json:"clientCancellations"`
	FirstByteSampleCount     int64                        `json:"firstByteSampleCount"`
	FirstByteSumMs           int64                        `json:"firstByteSumMs"`
	FirstByteHistogram       HotQualityFirstByteHistogram `json:"firstByteHistogram"`
	LastCompletedAtMs        *int64                       `json:"lastCompletedAtMs,omitempty"`
	LastFailureAtMs          *int64                       `json:"lastFailureAtMs,omitempty"`
}

// HotQualityMinuteBucket mirrors HotQualityMinuteBucket.
type HotQualityMinuteBucket struct {
	HotQualityCounters
	MinuteStartedAtMs int64 `json:"minuteStartedAtMs"`
}

// HotQualityBucketState mirrors hot-quality-snapshot.ts HotQualityBucketState
// (structurally identical to HotQualityMinuteBucket).
type HotQualityBucketState = HotQualityMinuteBucket

// HotQualityWindowSnapshot mirrors HotQualityWindowSnapshot (the storage-layer
// superset of the gatewayhybrid selection view).
type HotQualityWindowSnapshot struct {
	HotQualityCounters
	Minutes                int     `json:"minutes"`
	QualityAttempts        int64   `json:"qualityAttempts"`
	AdjustedCompletionRate float64 `json:"adjustedCompletionRate"`
}

// HotQualitySnapshot mirrors HotQualitySnapshot.
type HotQualitySnapshot struct {
	ScopeKey              string                   `json:"scopeKey"`
	Scope                 HotQualityScope          `json:"scope"`
	MinuteBuckets         []HotQualityMinuteBucket `json:"minuteBuckets"`
	Window5m              HotQualityWindowSnapshot `json:"window5m"`
	Window10m             HotQualityWindowSnapshot `json:"window10m"`
	Window30m             HotQualityWindowSnapshot `json:"window30m"`
	Reliability           float64                  `json:"reliability"`
	Confidence            float64                  `json:"confidence"`
	EffectiveReliability  float64                  `json:"effectiveReliability"`
	ReliabilityLevel      string                   `json:"reliabilityLevel"`
	SampleState           string                   `json:"sampleState"`
	FirstByteEwma5m       *float64                 `json:"firstByteEwma5m,omitempty"`
	FirstByteP95Bucket10m *int64                   `json:"firstByteP95Bucket10m,omitempty"`
	ExpiresAtMs           int64                    `json:"expiresAtMs"`
}

// SelectionView converts the storage snapshot into the reduced
// gatewayhybrid.HotQualitySnapshot the candidate-selection layer consumes
// (Node passes the structurally compatible full snapshot directly).
func (s *HotQualitySnapshot) SelectionView() gatewayhybrid.HotQualitySnapshot {
	window := func(w HotQualityWindowSnapshot) gatewayhybrid.HotQualityWindowSnapshot {
		return gatewayhybrid.HotQualityWindowSnapshot{
			QualityAttempts:   int(w.QualityAttempts),
			LastCompletedAtMs: w.LastCompletedAtMs,
			LastFailureAtMs:   w.LastFailureAtMs,
		}
	}
	view := gatewayhybrid.HotQualitySnapshot{
		Window5m:             window(s.Window5m),
		Window10m:            window(s.Window10m),
		Window30m:            window(s.Window30m),
		EffectiveReliability: s.EffectiveReliability,
		ReliabilityLevel:     s.ReliabilityLevel,
		SampleState:          s.SampleState,
		FirstByteEwma5m:      s.FirstByteEwma5m,
	}
	if s.FirstByteP95Bucket10m != nil {
		bucket := float64(*s.FirstByteP95Bucket10m)
		view.FirstByteP95Bucket10m = &bucket
	}
	return view
}

// Attempt mutation statuses (mirror the Node union).
const (
	AttemptMutationApplied                  = "applied"
	AttemptMutationIdempotent               = "idempotent"
	AttemptMutationDegradedToProtocol       = "degraded_to_protocol"
	AttemptMutationAttemptConflict          = "attempt_conflict"
	AttemptMutationKeyCapacityExhausted     = "key_capacity_exhausted"
	AttemptMutationAttemptCapacityExhausted = "attempt_capacity_exhausted"
)

// HotQualityAttemptMutationStatus mirrors HotQualityAttemptMutationStatus.
type HotQualityAttemptMutationStatus = string

// HotQualityAttemptMutationResult mirrors HotQualityAttemptMutationResult.
type HotQualityAttemptMutationResult struct {
	Status         string          `json:"status"`
	RequestedScope HotQualityScope `json:"requestedScope"`
	EffectiveScope HotQualityScope `json:"effectiveScope"`
}

// Terminal mutation statuses (mirror the Node union).
const (
	TerminalMutationApplied                 = "applied"
	TerminalMutationIdempotent              = "idempotent"
	TerminalMutationAttemptConflict         = "attempt_conflict"
	TerminalMutationAttemptNotFound         = "attempt_not_found"
	TerminalMutationTerminalConflict        = "terminal_conflict"
	TerminalMutationTerminalOutcomeConflict = "terminal_outcome_conflict"
	TerminalMutationQualityKeyUnavailable   = "quality_key_unavailable"
)

// HotQualityTerminalMutationStatus mirrors HotQualityTerminalMutationStatus.
type HotQualityTerminalMutationStatus = string

// HotQualityTerminalMutationResult mirrors HotQualityTerminalMutationResult;
// absent Node optionals become nil pointers.
type HotQualityTerminalMutationResult struct {
	Status         string                    `json:"status"`
	Terminal       *HotQualityTerminalRecord `json:"terminal"`
	EffectiveScope *HotQualityScope          `json:"effectiveScope"`
}

// HotQualityStoreStats mirrors HotQualityStoreStats.
type HotQualityStoreStats struct {
	KeyCount                    int64 `json:"keyCount"`
	AttemptIdentityCount        int64 `json:"attemptIdentityCount"`
	TerminalIdentityCount       int64 `json:"terminalIdentityCount"`
	KeyCreationRefusals         int64 `json:"keyCreationRefusals"`
	HighCardinalityDegradations int64 `json:"highCardinalityDegradations"`
	AttemptCapacityRefusals     int64 `json:"attemptCapacityRefusals"`
	TerminalQualityKeyMisses    int64 `json:"terminalQualityKeyMisses"`
}

// HotQualityRecordAttemptInput mirrors the recordAttempt argument object.
type HotQualityRecordAttemptInput struct {
	AttemptID string
	Scope     HotQualityScope
	NowMs     *int64
}

// HotQualityRecordTerminalInput mirrors the recordTerminal argument object.
type HotQualityRecordTerminalInput struct {
	AttemptID         string
	Scope             HotQualityScope
	TerminalOutcomeID string
	OutcomeClass      string
	FailureScope      string
	Source            string
	FirstByteMs       *float64
	NowMs             *int64
}

// HotQualityStore mirrors HotQualityStore. nowMs pointers mirror the Node
// optional nowMs arguments (nil → the store clock).
type HotQualityStore interface {
	RecordAttempt(ctx context.Context, input HotQualityRecordAttemptInput) (*HotQualityAttemptMutationResult, error)
	RecordTerminal(ctx context.Context, input HotQualityRecordTerminalInput) (*HotQualityTerminalMutationResult, error)
	Get(ctx context.Context, scope HotQualityScope, nowMs *int64) (*HotQualitySnapshot, error)
	GetTerminal(ctx context.Context, attemptID string, nowMs *int64) (*HotQualityTerminalRecord, error)
	Stats(ctx context.Context, nowMs *int64) (*HotQualityStoreStats, error)
}

// HotQualityModelFamilyCatalog mirrors HotQualityModelFamilyCatalog.
type HotQualityModelFamilyCatalog struct {
	knownFamilies []HotQualityModelFamily
	knownSet      map[string]struct{}
}

// KnownFamilies returns the sorted known families (frozen in Node).
func (c *HotQualityModelFamilyCatalog) KnownFamilies() []HotQualityModelFamily {
	return append([]HotQualityModelFamily(nil), c.knownFamilies...)
}

// Resolve mirrors resolve: an unknown or invalid candidate falls back to
// HotQualityUnknownModelFamily.
func (c *HotQualityModelFamilyCatalog) Resolve(candidate string) HotQualityModelFamily {
	value := normalizeCandidateModelFamily(candidate)
	if _, known := c.knownSet[value]; known {
		return value
	}
	return HotQualityUnknownModelFamily
}

// NewHotQualityModelFamilyCatalog mirrors createHotQualityModelFamilyCatalog.
func NewHotQualityModelFamilyCatalog(families []string, limit int) (*HotQualityModelFamilyCatalog, error) {
	normalizedLimit, err := positiveIntegerInt(limit, "模型 family 目录容量")
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	var normalized []string
	for _, family := range families {
		value, err := normalizeKnownModelFamily(family)
		if err != nil {
			return nil, err
		}
		if value == HotQualityUnknownModelFamily {
			continue
		}
		if _, dup := seen[value]; dup {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
		if len(normalized) > normalizedLimit {
			return nil, fmt.Errorf("热质量最多允许 %d 个模型 family", normalizedLimit)
		}
	}
	sort.Strings(normalized)
	knownSet := make(map[string]struct{}, len(normalized))
	for _, family := range normalized {
		knownSet[family] = struct{}{}
	}
	return &HotQualityModelFamilyCatalog{knownFamilies: normalized, knownSet: knownSet}, nil
}

// HotQualityScopeKey mirrors hotQualityScopeKey.
func HotQualityScopeKey(scope HotQualityScope) (string, error) {
	normalized, err := NormalizeHotQualityScope(scope)
	if err != nil {
		return "", err
	}
	return encodedScopeKey([]string{
		normalized.AccountRuntimeKey,
		normalized.ProtocolProfile,
		normalized.RequestLane,
		normalized.ModelFamily,
	}), nil
}

// ProtocolHotQualityScope mirrors protocolHotQualityScope (high-cardinality
// degradation fallback: model family collapses to 'unknown').
func ProtocolHotQualityScope(scope HotQualityScope) (HotQualityScope, error) {
	normalized, err := NormalizeHotQualityScope(scope)
	if err != nil {
		return HotQualityScope{}, err
	}
	normalized.ModelFamily = HotQualityUnknownModelFamily
	return normalized, nil
}

// CloneHotQualityScope mirrors cloneHotQualityScope (value copy in Go).
func CloneHotQualityScope(scope HotQualityScope) HotQualityScope {
	return HotQualityScope{
		AccountRuntimeKey: scope.AccountRuntimeKey,
		ProtocolProfile:   scope.ProtocolProfile,
		RequestLane:       scope.RequestLane,
		ModelFamily:       scope.ModelFamily,
	}
}

// NormalizeHotQualityScope mirrors normalizeHotQualityScope.
func NormalizeHotQualityScope(scope HotQualityScope) (HotQualityScope, error) {
	accountRuntimeKey, err := requiredPart(scope.AccountRuntimeKey, "accountRuntimeKey")
	if err != nil {
		return HotQualityScope{}, err
	}
	protocolProfile, err := requiredPart(scope.ProtocolProfile, "protocolProfile")
	if err != nil {
		return HotQualityScope{}, err
	}
	if scope.RequestLane != RequestLaneText && scope.RequestLane != RequestLaneImage {
		return HotQualityScope{}, errors.New("热质量 requestLane 必须是 text 或 image")
	}
	modelFamily, err := requiredPart(scope.ModelFamily, "modelFamily")
	if err != nil {
		return HotQualityScope{}, err
	}
	return HotQualityScope{
		AccountRuntimeKey: accountRuntimeKey,
		ProtocolProfile:   protocolProfile,
		RequestLane:       scope.RequestLane,
		ModelFamily:       modelFamily,
	}, nil
}

// FirstByteHistogramBucket mirrors firstByteHistogramBucket.
func FirstByteHistogramBucket(firstByteMs int64) int {
	sample := firstByteMs
	for index := 0; index < len(HotQualityFirstByteBucketUpperBoundsMS)-1; index++ {
		if sample <= HotQualityFirstByteBucketUpperBoundsMS[index] {
			return index
		}
	}
	return len(HotQualityFirstByteBucketUpperBoundsMS) - 1
}

// NormalizedFirstByteMs mirrors normalizedFirstByteMs.
func NormalizedFirstByteMs(value float64) (int64, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, errors.New("首字耗时必须是非负有限数值")
	}
	rounded := int64(math.Round(value))
	if rounded > maxSafeInteger {
		return maxSafeInteger, nil
	}
	return rounded, nil
}

func normalizeKnownModelFamily(value string) (string, error) {
	normalized := normalizeCandidateModelFamily(value)
	if normalized == "" {
		return "", errors.New("模型 family 不能为空、包含控制字符或超过 128 字符")
	}
	return normalized, nil
}

// normalizeCandidateModelFamily mirrors the private Node helper; invalid
// candidates normalize to ” (undefined in Node).
func normalizeCandidateModelFamily(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" || len(normalized) > 128 {
		return ""
	}
	for i := 0; i < len(normalized); i++ {
		c := normalized[i]
		if c <= 0x1f || c == 0x7f {
			return ""
		}
	}
	return normalized
}

func encodedScopeKey(parts []string) string {
	encoded := make([]string, len(parts))
	for i, part := range parts {
		encoded[i] = fmt.Sprintf("%d:%s", len(part), part)
	}
	return strings.Join(encoded, "|")
}

func positiveIntegerInt(value int, name string) (int, error) {
	if value <= 0 || int64(value) > maxSafeInteger {
		return 0, fmt.Errorf("%s 必须是正整数", name)
	}
	return value, nil
}

func positiveIntegerInt64(value int64, name string) (int64, error) {
	if value <= 0 || value > maxSafeInteger {
		return 0, fmt.Errorf("%s 必须是正整数", name)
	}
	return value, nil
}

func requiredPart(value string, name string) (string, error) {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "", fmt.Errorf("热质量作用域缺少 %s", name)
	}
	return normalized, nil
}
