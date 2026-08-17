// Package auditlog owns the F3 persistence foundation only.  It deliberately
// does not start an HTTP listener or connect Node capture to this package.
package auditlog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Mode string

const (
	ModeSQLite   Mode = "sqlite"
	ModePostgres Mode = "postgres"
)

type LifecycleStatus string

const (
	LifecycleInProgress LifecycleStatus = "in_progress"
	LifecycleFinalized  LifecycleStatus = "finalized"
)

type AuditOutcome string

const (
	AuditOutcomeSuccess           AuditOutcome = "success"
	AuditOutcomeSuccessAfterRetry AuditOutcome = "success_after_retry"
	AuditOutcomeGatewaySucceeded  AuditOutcome = "gateway_succeeded"
	AuditOutcomeGatewayFailed     AuditOutcome = "gateway_failed"
	AuditOutcomeUpstreamFailed    AuditOutcome = "upstream_failed"
	AuditOutcomeStreamFailed      AuditOutcome = "stream_failed"
	AuditOutcomeDownstreamClosed  AuditOutcome = "downstream_closed"
)

type TrafficSource string

const (
	TrafficSourceGateway              TrafficSource = "gateway"
	TrafficSourceManualAccountTest    TrafficSource = "manual_account_test"
	TrafficSourceAccountHealthCheck   TrafficSource = "account_health_check"
	TrafficSourceRuntimeRecoveryProbe TrafficSource = "runtime_recovery_probe"
	TrafficSourceCooldownRetest       TrafficSource = "cooldown_retest"
	TrafficSourceHybridScoring        TrafficSource = "hybrid_scoring"
	TrafficSourceHybridQualityScoring TrafficSource = "hybrid_quality_scoring"
)

type PayloadPartType string

const (
	PayloadPartClientRequest    PayloadPartType = "client_request"
	PayloadPartUpstreamRequest  PayloadPartType = "upstream_request"
	PayloadPartUpstreamResponse PayloadPartType = "upstream_response"
	PayloadPartGatewayResponse  PayloadPartType = "gateway_response"
	PayloadPartGatewayError     PayloadPartType = "gateway_error"
	PayloadPartGatewayMetadata  PayloadPartType = "gateway_metadata"
)

type PayloadCaptureStatus string

const (
	PayloadCaptureComplete    PayloadCaptureStatus = "complete"
	PayloadCaptureSummaryOnly PayloadCaptureStatus = "summary_only"
	PayloadCaptureHashOnly    PayloadCaptureStatus = "hash_only"
	PayloadCaptureExpired     PayloadCaptureStatus = "expired"
	PayloadCaptureOverflow    PayloadCaptureStatus = "overflow"
	PayloadCaptureDropped     PayloadCaptureStatus = "dropped"
)

type PayloadDropReason string

const (
	PayloadDropTransportBudget PayloadDropReason = "transport_budget"
	PayloadDropCapacityLimit   PayloadDropReason = "capacity_limit"
)

type AuditCaptureStatus string

const (
	AuditCaptureComplete     AuditCaptureStatus = "complete"
	AuditCaptureMetadataOnly AuditCaptureStatus = "metadata_only"
	AuditCaptureDropped      AuditCaptureStatus = "dropped"
	AuditCaptureOverflow     AuditCaptureStatus = "overflow"
)

// HeaderValues preserves whether Node supplied a scalar header or an array;
// this distinction is part of the captured raw-header semantics.
type HeaderValues struct {
	Values []string
	Array  bool
}

func (h *HeaderValues) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*h = HeaderValues{Values: []string{single}}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return fmt.Errorf("header 必须是字符串或字符串数组: %w", err)
	}
	*h = HeaderValues{Values: many, Array: true}
	return nil
}

func (h HeaderValues) MarshalJSON() ([]byte, error) {
	if h.Array {
		if h.Values == nil {
			return []byte("[]"), nil
		}
		return json.Marshal(h.Values)
	}
	if len(h.Values) == 0 {
		return json.Marshal("")
	}
	return json.Marshal(h.Values[0])
}

// PayloadBody accepts the Node JSON Buffer form, a JSON string, or the
// explicit {"base64":"..."} transport form.  It is never serialized into
// database JSON; Store writes its raw bytes to the blob owner directory.
type PayloadBody struct {
	Bytes   []byte
	Present bool
}

func (b *PayloadBody) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		b.Bytes, b.Present = nil, false
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		b.Bytes, b.Present = []byte(text), true
		return nil
	}
	var encoded struct {
		Base64 string `json:"base64"`
		Type   string `json:"type"`
		Data   []byte `json:"data"`
	}
	if err := json.Unmarshal(data, &encoded); err != nil {
		return fmt.Errorf("body 必须是字符串、Node Buffer 或 base64 对象: %w", err)
	}
	if encoded.Base64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(encoded.Base64)
		if err != nil {
			return fmt.Errorf("body.base64 非法: %w", err)
		}
		b.Bytes, b.Present = decoded, true
		return nil
	}
	if encoded.Type == "Buffer" && encoded.Data != nil {
		b.Bytes, b.Present = encoded.Data, true
		return nil
	}
	return fmt.Errorf("body 对象必须包含 base64 或 Node Buffer type/data")
}

type AuditLogPayloadInput struct {
	ID               string                  `json:"id,omitempty"`
	AttemptTempID    string                  `json:"attemptTempId,omitempty"`
	PartType         PayloadPartType         `json:"partType"`
	SequenceIndex    *int                    `json:"sequenceIndex,omitempty"`
	ContentType      string                  `json:"contentType,omitempty"`
	ContentEncoding  string                  `json:"contentEncoding,omitempty"`
	Headers          map[string]HeaderValues `json:"headers,omitempty"`
	Body             PayloadBody             `json:"body,omitempty"`
	BodySHA256       string                  `json:"bodySha256,omitempty"`
	RawBodySizeBytes *int64                  `json:"rawBodySizeBytes,omitempty"`
	CaptureStatus    PayloadCaptureStatus    `json:"captureStatus,omitempty"`
	DropReason       PayloadDropReason       `json:"dropReason,omitempty"`
	CreatedAt        string                  `json:"createdAt,omitempty"`
}

type AuditLogAttemptInput struct {
	ID                          string `json:"id,omitempty"`
	TempID                      string `json:"tempId,omitempty"`
	AttemptIndex                int    `json:"attemptIndex"`
	AccountID                   string `json:"accountId,omitempty"`
	AccountOwnerSystemAccountID string `json:"accountOwnerSystemAccountId,omitempty"`
	GroupID                     string `json:"groupId,omitempty"`
	ProxyURL                    string `json:"proxyUrl,omitempty"`
	ProviderCode                string `json:"providerCode,omitempty"`
	Model                       string `json:"model,omitempty"`
	UpstreamModel               string `json:"upstreamModel,omitempty"`
	PricingModel                string `json:"pricingModel,omitempty"`
	ModelMappingApplied         *bool  `json:"modelMappingApplied,omitempty"`
	ModelMappingSource          string `json:"modelMappingSource,omitempty"`
	SourceEndpointFamily        string `json:"sourceEndpointFamily,omitempty"`
	UpstreamEndpointFamily      string `json:"upstreamEndpointFamily,omitempty"`
	UpstreamMethod              string `json:"upstreamMethod"`
	UpstreamURL                 string `json:"upstreamUrl"`
	UpstreamStatusCode          *int   `json:"upstreamStatusCode,omitempty"`
	Success                     *bool  `json:"success,omitempty"`
	ErrorPhase                  string `json:"errorPhase,omitempty"`
	ErrorCode                   string `json:"errorCode,omitempty"`
	ErrorMessage                string `json:"errorMessage,omitempty"`
	StartedAt                   string `json:"startedAt"`
	EndedAt                     string `json:"endedAt,omitempty"`
	DurationMS                  *int64 `json:"durationMs,omitempty"`
}

// AuditLogInput is the F3 JSON input DTO. IDs must be stable across retries.
// A finalized input replaces only its own in_progress placeholder; a late
// in_progress input is ignored after finalization.
type AuditLogInput struct {
	ID                     string                 `json:"id"`
	LifecycleStatus        LifecycleStatus        `json:"lifecycleStatus"`
	TraceID                string                 `json:"traceId"`
	ConversationKey        string                 `json:"conversationKey,omitempty"`
	SessionID              string                 `json:"sessionId,omitempty"`
	SessionClientType      string                 `json:"sessionClientType,omitempty"`
	TrafficSource          TrafficSource          `json:"trafficSource"`
	SystemAccountID        string                 `json:"systemAccountId,omitempty"`
	APIKeyID               string                 `json:"apiKeyId,omitempty"`
	GroupID                string                 `json:"groupId,omitempty"`
	AccountID              string                 `json:"accountId,omitempty"`
	ProviderCode           string                 `json:"providerCode,omitempty"`
	Method                 string                 `json:"method"`
	Path                   string                 `json:"path"`
	QueryString            string                 `json:"queryString,omitempty"`
	Model                  string                 `json:"model,omitempty"`
	UpstreamModel          string                 `json:"upstreamModel,omitempty"`
	PricingModel           string                 `json:"pricingModel,omitempty"`
	ModelMappingApplied    *bool                  `json:"modelMappingApplied,omitempty"`
	ModelMappingSource     string                 `json:"modelMappingSource,omitempty"`
	SourceEndpointFamily   string                 `json:"sourceEndpointFamily,omitempty"`
	UpstreamEndpointFamily string                 `json:"upstreamEndpointFamily,omitempty"`
	Stream                 *bool                  `json:"stream,omitempty"`
	ClientIP               string                 `json:"clientIp,omitempty"`
	UserAgent              string                 `json:"userAgent,omitempty"`
	AuditOutcome           AuditOutcome           `json:"auditOutcome"`
	Success                bool                   `json:"success"`
	FinalStatusCode        *int                   `json:"finalStatusCode,omitempty"`
	ErrorPhase             string                 `json:"errorPhase,omitempty"`
	ErrorCode              string                 `json:"errorCode,omitempty"`
	ErrorMessage           string                 `json:"errorMessage,omitempty"`
	SampleBucket           int                    `json:"sampleBucket"`
	SampleReason           string                 `json:"sampleReason"`
	CaptureStatus          AuditCaptureStatus     `json:"captureStatus,omitempty"`
	StartedAt              string                 `json:"startedAt"`
	EndedAt                string                 `json:"endedAt"`
	DurationMS             *int64                 `json:"durationMs,omitempty"`
	HTTPCompletedAt        string                 `json:"httpCompletedAt,omitempty"`
	HTTPDurationMS         *int64                 `json:"httpDurationMs,omitempty"`
	FirstTokenMS           *int64                 `json:"firstTokenMs,omitempty"`
	Attempts               []AuditLogAttemptInput `json:"attempts"`
	Payloads               []AuditLogPayloadInput `json:"payloads"`
	CreatedAt              string                 `json:"createdAt,omitempty"`
}

type OwnerLease struct {
	OwnerID    string
	FenceToken int64
}

type PersistResult struct {
	Ignored bool
}

// RetentionConfig describes one bounded retention pass. Cutoffs are UTC
// instants; zero values are rejected rather than silently widening a cleanup.
type RetentionConfig struct {
	SuccessHotCutoff             time.Time
	SuccessCutoff                time.Time
	FailureCutoff                time.Time
	ErrorGroupCutoff             time.Time
	SuccessSampleBucketThreshold int
	BatchSize                    int
}

// RetentionResult reports rows and files changed by a retention pass.
type RetentionResult struct {
	SuccessHotTrimmed       int64
	DeletedNonPersistedLogs int64
	DeletedLogs             int64
	DeletedErrorGroups      int64
	DeletedPayloadBlobs     int64
	DeletedHotSearchFiles   int64
}

type HotSearchOptions struct {
	Keywords []string
	StartAt  time.Time
	EndAt    time.Time
	Limit    int
	MaxFiles int
	MaxLines int
}

type HotSearchResult struct {
	AuditLogIDs  []string
	ScannedFiles int
	Truncated    bool
}

type Store interface {
	EnsureSchema(context.Context) error
	AcquireOwnerLease(context.Context, string, time.Duration) (OwnerLease, bool, error)
	RenewOwnerLease(context.Context, OwnerLease, time.Duration) (bool, error)
	ReleaseOwnerLease(context.Context, OwnerLease) error
	// CleanupOwnedBlobTemps is intentionally explicit: only a currently fenced
	// owner may remove its own old private temporary files. It never touches
	// published content-addressed blobs or another owner/fence's files.
	CleanupOwnedBlobTemps(context.Context, OwnerLease, time.Time) error
	// CleanupOrphanedBlobTemps is explicit maintenance for old fenced writers:
	// it only removes aged .tmp files whose fence is strictly older than the
	// acquired current fence. Published blobs are never candidates.
	CleanupOrphanedBlobTemps(context.Context, OwnerLease, time.Time) error
	Persist(context.Context, OwnerLease, AuditLogInput) (PersistResult, error)
	CleanupRetention(context.Context, OwnerLease, RetentionConfig) (RetentionResult, error)
	AppendHotSearch(context.Context, OwnerLease, []AuditLogInput) (int, error)
	CleanupHotSearch(context.Context, OwnerLease, time.Time, int) (int64, error)
	SearchHotSearch(context.Context, HotSearchOptions) (HotSearchResult, error)
	Close() error
}

func normalizeLifecycle(value LifecycleStatus) LifecycleStatus {
	if value == "" {
		return LifecycleFinalized
	}
	return value
}

func isKnown[T ~string](value T, values ...T) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func validateInput(input AuditLogInput) error {
	if strings.TrimSpace(input.ID) == "" || strings.TrimSpace(input.TraceID) == "" || strings.TrimSpace(input.Method) == "" || strings.TrimSpace(input.Path) == "" {
		return fmt.Errorf("审计输入必须包含稳定 id、traceId、method 和 path")
	}
	lifecycle := normalizeLifecycle(input.LifecycleStatus)
	if !isKnown(lifecycle, LifecycleInProgress, LifecycleFinalized) {
		return fmt.Errorf("lifecycleStatus 无效: %q", input.LifecycleStatus)
	}
	if !isKnown(input.AuditOutcome, AuditOutcomeSuccess, AuditOutcomeSuccessAfterRetry, AuditOutcomeGatewaySucceeded, AuditOutcomeGatewayFailed, AuditOutcomeUpstreamFailed, AuditOutcomeStreamFailed, AuditOutcomeDownstreamClosed) {
		return fmt.Errorf("auditOutcome 无效: %q", input.AuditOutcome)
	}
	if !isKnown(input.TrafficSource, TrafficSourceGateway, TrafficSourceManualAccountTest, TrafficSourceAccountHealthCheck, TrafficSourceRuntimeRecoveryProbe, TrafficSourceCooldownRetest, TrafficSourceHybridScoring, TrafficSourceHybridQualityScoring) {
		return fmt.Errorf("trafficSource 无效: %q", input.TrafficSource)
	}
	if input.TrafficSource == TrafficSourceAccountHealthCheck || input.TrafficSource == TrafficSourceRuntimeRecoveryProbe || input.TrafficSource == TrafficSourceCooldownRetest {
		return fmt.Errorf("trafficSource %q 不属于当前原始审计持久化范围", input.TrafficSource)
	}
	if strings.TrimSpace(input.StartedAt) == "" || strings.TrimSpace(input.EndedAt) == "" || strings.TrimSpace(input.SampleReason) == "" {
		return fmt.Errorf("审计输入必须包含 startedAt、endedAt 和 sampleReason")
	}
	if input.CaptureStatus != "" && !isKnown(input.CaptureStatus, AuditCaptureComplete, AuditCaptureMetadataOnly, AuditCaptureDropped, AuditCaptureOverflow) {
		return fmt.Errorf("captureStatus 无效: %q", input.CaptureStatus)
	}
	for _, payload := range input.Payloads {
		if !isKnown(payload.PartType, PayloadPartClientRequest, PayloadPartUpstreamRequest, PayloadPartUpstreamResponse, PayloadPartGatewayResponse, PayloadPartGatewayError, PayloadPartGatewayMetadata) {
			return fmt.Errorf("payload partType 无效: %q", payload.PartType)
		}
		status := payload.CaptureStatus
		if status == "" {
			status = PayloadCaptureComplete
		}
		if !isKnown(status, PayloadCaptureComplete, PayloadCaptureSummaryOnly, PayloadCaptureHashOnly, PayloadCaptureExpired, PayloadCaptureOverflow, PayloadCaptureDropped) {
			return fmt.Errorf("payload captureStatus 无效: %q", payload.CaptureStatus)
		}
	}
	return nil
}

func canonicalAuditTime(value, field string, optional bool) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		if optional {
			return "", nil
		}
		return "", fmt.Errorf("审计字段 %s 不能为空", field)
	}
	parsed, err := time.Parse(time.RFC3339Nano, trimmed)
	if err != nil {
		return "", fmt.Errorf("审计字段 %s 必须是 RFC3339 瞬时值: %w", field, err)
	}
	return parsed.UTC().Format(time.RFC3339Nano), nil
}

// normalizeAuditInput is the sole boundary that turns the wire DTO's time
// strings into canonical UTC instants before any SQL or hot-search write.
// Missing CreatedAt is intentionally generated once here; supplied values
// are never replaced after a parse failure.
func normalizeAuditInput(input AuditLogInput) (AuditLogInput, error) {
	if err := validateInput(input); err != nil {
		return AuditLogInput{}, err
	}
	var err error
	if input.StartedAt, err = canonicalAuditTime(input.StartedAt, "startedAt", false); err != nil {
		return AuditLogInput{}, err
	}
	if input.EndedAt, err = canonicalAuditTime(input.EndedAt, "endedAt", false); err != nil {
		return AuditLogInput{}, err
	}
	if strings.TrimSpace(input.CreatedAt) == "" {
		input.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	} else if input.CreatedAt, err = canonicalAuditTime(input.CreatedAt, "createdAt", false); err != nil {
		return AuditLogInput{}, err
	}
	if input.HTTPCompletedAt, err = canonicalAuditTime(input.HTTPCompletedAt, "httpCompletedAt", true); err != nil {
		return AuditLogInput{}, err
	}
	for index := range input.Attempts {
		attempt := &input.Attempts[index]
		if attempt.StartedAt, err = canonicalAuditTime(attempt.StartedAt, fmt.Sprintf("attempts[%d].startedAt", index), false); err != nil {
			return AuditLogInput{}, err
		}
		if attempt.EndedAt, err = canonicalAuditTime(attempt.EndedAt, fmt.Sprintf("attempts[%d].endedAt", index), true); err != nil {
			return AuditLogInput{}, err
		}
	}
	for index := range input.Payloads {
		payload := &input.Payloads[index]
		if payload.CreatedAt, err = canonicalAuditTime(payload.CreatedAt, fmt.Sprintf("payloads[%d].createdAt", index), true); err != nil {
			return AuditLogInput{}, err
		}
	}
	return input, nil
}
