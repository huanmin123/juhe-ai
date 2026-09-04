package gatewayusage

import "strings"

// Audit log input types mirroring backend/src/storage/audit-log-types.ts
// field for field. Only the capture-side input surface is needed here; the
// persistence summaries stay with internal/auditlog (F3).

// AuditOutcome mirrors AuditOutcome.
type AuditOutcome = string

// Audit outcome values.
const (
	AuditOutcomeSuccess           AuditOutcome = "success"
	AuditOutcomeSuccessAfterRetry AuditOutcome = "success_after_retry"
	AuditOutcomeGatewaySucceeded  AuditOutcome = "gateway_succeeded"
	AuditOutcomeGatewayFailed     AuditOutcome = "gateway_failed"
	AuditOutcomeUpstreamFailed    AuditOutcome = "upstream_failed"
	AuditOutcomeStreamFailed      AuditOutcome = "stream_failed"
	AuditOutcomeDownstreamClosed  AuditOutcome = "downstream_closed"
)

// AuditPayloadPartType mirrors AuditPayloadPartType.
type AuditPayloadPartType = string

// Audit payload part types.
const (
	AuditPartClientRequest   AuditPayloadPartType = "client_request"
	AuditPartUpstreamRequest AuditPayloadPartType = "upstream_request"
	AuditPartUpstreamResponse AuditPayloadPartType = "upstream_response"
	AuditPartGatewayResponse AuditPayloadPartType = "gateway_response"
	AuditPartGatewayError    AuditPayloadPartType = "gateway_error"
	AuditPartGatewayMetadata AuditPayloadPartType = "gateway_metadata"
)

// AuditPayloadCaptureStatus mirrors AuditPayloadCaptureStatus.
type AuditPayloadCaptureStatus = string

// Audit payload capture statuses.
const (
	AuditCaptureComplete    AuditPayloadCaptureStatus = "complete"
	AuditCaptureSummaryOnly AuditPayloadCaptureStatus = "summary_only"
	AuditCaptureHashOnly    AuditPayloadCaptureStatus = "hash_only"
	AuditCaptureExpired     AuditPayloadCaptureStatus = "expired"
	AuditCaptureOverflow    AuditPayloadCaptureStatus = "overflow"
	AuditCaptureDropped     AuditPayloadCaptureStatus = "dropped"
)

// AuditLogLifecycleStatus mirrors AuditLogLifecycleStatus.
type AuditLogLifecycleStatus = string

// Audit lifecycle statuses.
const (
	AuditLifecycleInProgress AuditLogLifecycleStatus = "in_progress"
	AuditLifecycleFinalized  AuditLogLifecycleStatus = "finalized"
)

// AuditPayloadDropReason mirrors AuditPayloadDropReason.
type AuditPayloadDropReason = string

// Audit payload drop reasons.
const (
	AuditDropTransportBudget AuditPayloadDropReason = "transport_budget"
	AuditDropCapacityLimit   AuditPayloadDropReason = "capacity_limit"
)

// AuditHeader mirrors Record<string, string | string[]> entries with
// preserved insertion order so payload documents serialize identically.
type AuditHeader struct {
	Name    string
	Value   string
	Values  []string
	IsArray bool
}

// AuditHeaderList is an ordered header collection.
type AuditHeaderList []AuditHeader

// Get returns the first value for name (case-insensitive), mirroring the
// Node header lookup of headerValue().
func (headers AuditHeaderList) Get(name string) string {
	lower := strings.ToLower(name)
	for _, header := range headers {
		if strings.ToLower(header.Name) != lower {
			continue
		}
		if header.IsArray {
			if len(header.Values) == 0 {
				return ""
			}
			return strings.Join(header.Values, ", ")
		}
		return header.Value
	}
	return ""
}

// ToMap converts to a plain map for generic JSON use.
func (headers AuditHeaderList) ToMap() map[string]any {
	out := make(map[string]any, len(headers))
	for _, header := range headers {
		if header.IsArray {
			out[header.Name] = header.Values
			continue
		}
		out[header.Name] = header.Value
	}
	return out
}

// AuditLogPayloadInput mirrors AuditLogPayloadInput. Body nil = undefined;
// string bodies are carried as UTF-8 bytes.
type AuditLogPayloadInput struct {
	ID              string                    `json:"id,omitempty"`
	AttemptTempID   string                    `json:"attemptTempId,omitempty"`
	PartType        AuditPayloadPartType      `json:"partType"`
	SequenceIndex   *int                      `json:"sequenceIndex,omitempty"`
	ContentType     string                    `json:"contentType,omitempty"`
	ContentEncoding string                    `json:"contentEncoding,omitempty"`
	Headers         map[string]any            `json:"headers,omitempty"`
	Body            []byte                    `json:"-"`
	HasBody         bool                      `json:"-"`
	BodySha256      string                    `json:"bodySha256,omitempty"`
	RawBodySizeBytes *int                     `json:"rawBodySizeBytes,omitempty"`
	CaptureStatus   AuditPayloadCaptureStatus `json:"captureStatus,omitempty"`
	DropReason      AuditPayloadDropReason    `json:"dropReason,omitempty"`
	CreatedAt       string                    `json:"createdAt,omitempty"`
}

// AuditLogAttemptInput mirrors AuditLogAttemptInput.
type AuditLogAttemptInput struct {
	ID                        string `json:"id,omitempty"`
	TempID                    string `json:"tempId,omitempty"`
	AttemptIndex              int    `json:"attemptIndex"`
	AccountID                 string `json:"accountId,omitempty"`
	AccountOwnerSystemAccountID string `json:"accountOwnerSystemAccountId,omitempty"`
	GroupID                   string `json:"groupId,omitempty"`
	ProxyURL                  string `json:"proxyUrl,omitempty"`
	ProviderCode              string `json:"providerCode,omitempty"`
	Model                     string `json:"model,omitempty"`
	UpstreamModel             string `json:"upstreamModel,omitempty"`
	PricingModel              string `json:"pricingModel,omitempty"`
	ModelMappingApplied       *bool  `json:"modelMappingApplied,omitempty"`
	ModelMappingSource        string `json:"modelMappingSource,omitempty"`
	SourceEndpointFamily      string `json:"sourceEndpointFamily,omitempty"`
	UpstreamEndpointFamily    string `json:"upstreamEndpointFamily,omitempty"`
	UpstreamMethod            string `json:"upstreamMethod"`
	UpstreamURL               string `json:"upstreamUrl"`
	UpstreamStatusCode        *int   `json:"upstreamStatusCode,omitempty"`
	Success                   *bool  `json:"success,omitempty"`
	ErrorPhase                string `json:"errorPhase,omitempty"`
	ErrorCode                 string `json:"errorCode,omitempty"`
	ErrorMessage              string `json:"errorMessage,omitempty"`
	StartedAt                 string `json:"startedAt"`
	EndedAt                   string `json:"endedAt,omitempty"`
	DurationMs                *int   `json:"durationMs,omitempty"`
}

// AuditLogInput mirrors AuditLogInput (storage/audit-log-types.ts).
type AuditLogInput struct {
	ID                        string                   `json:"id,omitempty"`
	LifecycleStatus           AuditLogLifecycleStatus  `json:"lifecycleStatus,omitempty"`
	TraceID                   string                   `json:"traceId"`
	ConversationKey           string                   `json:"conversationKey,omitempty"`
	SessionID                 string                   `json:"sessionId,omitempty"`
	SessionClientType         string                   `json:"sessionClientType,omitempty"`
	TrafficSource             string                   `json:"trafficSource,omitempty"`
	SystemAccountID           string                   `json:"systemAccountId,omitempty"`
	APIKeyID                  string                   `json:"apiKeyId,omitempty"`
	GroupID                   string                   `json:"groupId,omitempty"`
	AccountID                 string                   `json:"accountId,omitempty"`
	ProviderCode              string                   `json:"providerCode,omitempty"`
	Method                    string                   `json:"method"`
	Path                      string                   `json:"path"`
	QueryString               string                   `json:"queryString,omitempty"`
	Model                     string                   `json:"model,omitempty"`
	UpstreamModel             string                   `json:"upstreamModel,omitempty"`
	PricingModel              string                   `json:"pricingModel,omitempty"`
	ModelMappingApplied       *bool                    `json:"modelMappingApplied,omitempty"`
	ModelMappingSource        string                   `json:"modelMappingSource,omitempty"`
	SourceEndpointFamily      string                   `json:"sourceEndpointFamily,omitempty"`
	UpstreamEndpointFamily    string                   `json:"upstreamEndpointFamily,omitempty"`
	Stream                    *bool                    `json:"stream,omitempty"`
	ClientIP                  string                   `json:"clientIp,omitempty"`
	UserAgent                 string                   `json:"userAgent,omitempty"`
	AuditOutcome              AuditOutcome             `json:"auditOutcome"`
	Success                   bool                     `json:"success"`
	FinalStatusCode           *int                     `json:"finalStatusCode,omitempty"`
	ErrorPhase                string                   `json:"errorPhase,omitempty"`
	ErrorCode                 string                   `json:"errorCode,omitempty"`
	ErrorMessage              string                   `json:"errorMessage,omitempty"`
	SampleBucket              int                      `json:"sampleBucket"`
	SampleReason              string                   `json:"sampleReason"`
	CaptureStatus             string                   `json:"captureStatus,omitempty"`
	StartedAt                 string                   `json:"startedAt"`
	EndedAt                   string                   `json:"endedAt"`
	DurationMs                *int                     `json:"durationMs,omitempty"`
	HTTPCompletedAt           string                   `json:"httpCompletedAt,omitempty"`
	HTTPDurationMs            *int                     `json:"httpDurationMs,omitempty"`
	FirstTokenMs              *int                     `json:"firstTokenMs,omitempty"`
	Attempts                  []AuditLogAttemptInput   `json:"attempts"`
	Payloads                  []AuditLogPayloadInput   `json:"payloads"`
	CreatedAt                 string                   `json:"createdAt,omitempty"`
}

// AuditGatewayContext mirrors AuditGatewayContext (capture.service.ts).
type AuditGatewayContext struct {
	SessionID         string
	SessionClientType string
	ConversationKey   string
	SystemAccountID   string
	APIKeyID          string
	GroupID           string
	AccountID         string
	ProviderCode      string
	UpstreamModel     string
	PricingModel      string
	ModelMappingApplied *bool
	ModelMappingSource  string
	SourceEndpointFamily string
	UpstreamEndpointFamily string
	TrafficSource     string
}

// AuditTrafficSourceNonPersisted mirrors nonPersistedAuditTrafficSources
// (audit-log-go-input.service.ts): background probe sources are excluded
// from the persisted audit domain.
var AuditTrafficSourceNonPersisted = map[string]bool{
	"account_health_check":   true,
	"runtime_recovery_probe": true,
	"cooldown_retest":        true,
}

// ShouldPersistAuditTrafficSource mirrors the nonPersistedAuditTrafficSources
// gate of dispatchAuditLogToGo.
func ShouldPersistAuditTrafficSource(trafficSource string) bool {
	return !AuditTrafficSourceNonPersisted[trafficSource]
}
