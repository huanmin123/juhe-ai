package gatewayusage

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	mrand "math/rand"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Gateway audit capture mirroring
// backend/src/modules/gateway/audit/capture.service.ts and metadata.ts.

// OpenAIProtocolCode mirrors OPENAI_PROTOCOL_CODE (domain/provider-protocol.ts).
const OpenAIProtocolCode = "openai"

// auditInlineSha256MaxBytes mirrors auditInlineSha256MaxBytes.
const auditInlineSha256MaxBytes = 1024 * 1024

// CaptureModeMetadataOnly mirrors captureMode: 'metadata_only'.
const CaptureModeMetadataOnly = "metadata_only"

// GatewayHTTPCompletionObserver ports observeGatewayHttpCompletion: the
// response finish/close observation the flush timing waits for. nil means
// the HTTP completion timing is unavailable and finalize flushes inline.
type GatewayHTTPCompletionObserver interface {
	CompletedAtMs() (int64, bool)
	OnCompleted(listener func(completedAtMs int64)) (cancel func())
}

// AuditStageLogger ports logRequestStage (shared/request-context.ts).
type AuditStageLogger interface {
	LogRequestStage(stage string, fields map[string]any, outcome string)
}

// AuditCaptureInput mirrors AuditCaptureContextInput plus the request
// facts the capture reads lazily from the Express request in Node.
type AuditCaptureInput struct {
	TraceID       string
	ClientIP      string
	StartedAtMs   int64
	TrafficSource any // normalized via NormalizeOpenAIGatewayTrafficSource
	CaptureMode   string

	// Request facts (Node reads these from req at finalize/attempt time).
	Method         string
	Path           string
	OriginalURL    string
	UserAgent      string
	Model          string // requestModel(req)
	Stream         bool   // requestStream(req)
	RawBody        []byte
	RequestHeaders map[string]any

	// Ports.
	Settings               AuditLogSettingsSource
	HTTPCompletion         GatewayHTTPCompletionObserver
	Dispatcher             AuditDispatcher
	Models                 UsageModelResolver
	Pricing                PricingCatalog
	Logger                 Logger
	StageLogger            AuditStageLogger
	OffloadPayloadRetention bool // processRole === 'server'
	// SyncPricingAllowed mirrors runtimeConfig.cacheDriver !== 'redis'.
	SyncPricingAllowed bool
	// SourceEndpointFamily mirrors gatewayRequestEndpointFamily(req)
	// evaluated by the caller.
	SourceEndpointFamily string
	// Clock injects time; nil defaults to the wall clock.
	Clock Clock
}

// AuditAttemptState mirrors AuditAttemptState.
type AuditAttemptState struct {
	TempID                  string
	Attempt                 AuditLogAttemptInput
	RequestPayload          *AuditLogPayloadInput
	RequestPayloadCaptured  bool
	StartedAtMs             int64
	Completed               bool
}

// AuditCaptureContext mirrors the AuditCaptureContext class.
type AuditCaptureContext struct {
	input          AuditCaptureInput
	clock          Clock
	httpCompletion GatewayHTTPCompletionObserver
	traceID        string
	auditLogID     string
	clientIP       string
	startedAtMs    int64
	startedAtIso   string
	trafficSource  OpenAIGatewayTrafficSource
	sampleBucket   int
	successCaptureSelected   bool
	successHotRetentionEnabled bool
	metadataOnly    bool
	capturePayloadBodies bool
	enabled         bool
	successSampleRate float64
	activeCaptureMaxBytes int
	successFullBodyLimitBytes int
	problemFullBodyLimitBytes int

	mu              sync.Mutex
	payloads        []AuditLogPayloadInput
	attempts        []AuditLogAttemptInput
	activeAttempts  map[string]*AuditAttemptState
	gatewayContext  AuditGatewayContext
	finalized       bool
	inProgressAuditEnqueued bool
	hadFailedAttempt bool
	downstreamClosed bool
	serverDiagnosticTimeout bool
	serverDiagnosticCancellation bool
	overflowed      bool
	approximateBytes int
	residentPayloadBytes int
	sequenceIndex   int
	clientRequestPayloadCaptured bool
	httpCompletedAtMs *int64
	pendingFinalizeInput *FinalizeAuditInput
	cancelHTTPListener func()
	activeCaptureRegistered bool
}

// FinalizeAuditInput mirrors FinalizeAuditInput.
type FinalizeAuditInput struct {
	Outcome          AuditOutcome
	Success          bool
	StatusCode       *int
	ResponseHeaders  map[string]any
	ResponseBody     []byte
	HasResponseBody  bool
	ResponsePartType AuditPayloadPartType
	ErrorPhase       string
	ErrorCode        string
	ErrorMessage     string
	AccountID        string
	FirstTokenMs     *int
}

// ResolvedAuditFinalization mirrors ResolvedAuditFinalization.
type ResolvedAuditFinalization struct {
	Outcome      AuditOutcome
	Success      bool
	ErrorPhase   string
	ErrorCode    string
	ErrorMessage string
}

// FailedAuditAttemptRoot mirrors FailedAuditAttemptRoot.
type FailedAuditAttemptRoot struct {
	ErrorPhase   string
	ErrorCode    string
	ErrorMessage string
}

// ResolveAuditFinalizationInput mirrors the Pick<FinalizeAuditInput, ...>
// the Node resolver takes.
type ResolveAuditFinalizationInput struct {
	Outcome      AuditOutcome
	Success      bool
	ErrorPhase   string
	ErrorCode    string
	ErrorMessage string
}

// ResolveAuditFinalization mirrors resolveAuditFinalization.
func ResolveAuditFinalization(input ResolveAuditFinalizationInput, downstreamClosed bool, hadFailedAttempt bool, failedAttemptRoot *FailedAuditAttemptRoot) ResolvedAuditFinalization {
	isDownstreamClose := input.Outcome == AuditOutcomeDownstreamClosed ||
		input.ErrorPhase == "downstream" ||
		input.ErrorCode == "downstream_connection_closed"
	hasInputRootFailure := !isDownstreamClose && (
		input.Outcome == AuditOutcomeGatewayFailed ||
			input.Outcome == AuditOutcomeUpstreamFailed ||
			input.Outcome == AuditOutcomeStreamFailed ||
			input.ErrorPhase != "" || input.ErrorCode != "" || input.ErrorMessage != "")
	hasAttemptRootFailure := downstreamClosed && !input.Success && failedAttemptRoot != nil
	// `upstream_retryable_error` is a client-facing retry contract after the
	// candidate pool is exhausted. Keep the concrete final attempt cause in
	// audit instead of replacing it with that generic gateway code.
	genericRetryFacade := input.ErrorCode == "upstream_retryable_error" &&
		failedAttemptRoot != nil &&
		(failedAttemptRoot.ErrorCode != "" || failedAttemptRoot.ErrorMessage != "")
	var rootFailure *ResolvedAuditFinalization
	switch {
	case genericRetryFacade:
		outcome := AuditOutcomeUpstreamFailed
		if failedAttemptRoot.ErrorPhase == "stream" {
			outcome = AuditOutcomeStreamFailed
		}
		rootFailure = &ResolvedAuditFinalization{
			Outcome:      outcome,
			ErrorPhase:   failedAttemptRoot.ErrorPhase,
			ErrorCode:    failedAttemptRoot.ErrorCode,
			ErrorMessage: failedAttemptRoot.ErrorMessage,
		}
	case hasInputRootFailure:
		rootFailure = &ResolvedAuditFinalization{
			Outcome:      input.Outcome,
			ErrorPhase:   input.ErrorPhase,
			ErrorCode:    input.ErrorCode,
			ErrorMessage: input.ErrorMessage,
		}
	case hasAttemptRootFailure:
		outcome := AuditOutcomeUpstreamFailed
		if failedAttemptRoot.ErrorPhase == "stream" {
			outcome = AuditOutcomeStreamFailed
		}
		rootFailure = &ResolvedAuditFinalization{
			Outcome:      outcome,
			ErrorPhase:   failedAttemptRoot.ErrorPhase,
			ErrorCode:    failedAttemptRoot.ErrorCode,
			ErrorMessage: failedAttemptRoot.ErrorMessage,
		}
	}
	outcome := input.Outcome
	switch {
	case downstreamClosed && rootFailure == nil:
		outcome = AuditOutcomeDownstreamClosed
	case input.Success && hadFailedAttempt:
		outcome = AuditOutcomeSuccessAfterRetry
	case rootFailure != nil:
		outcome = rootFailure.Outcome
	}
	resolved := ResolvedAuditFinalization{
		Outcome: outcome,
		Success: input.Success && outcome != AuditOutcomeDownstreamClosed,
	}
	if outcome == AuditOutcomeDownstreamClosed {
		resolved.ErrorPhase = "downstream"
		resolved.ErrorCode = "downstream_connection_closed"
		resolved.ErrorMessage = "下游连接关闭"
		return resolved
	}
	if rootFailure != nil {
		resolved.ErrorPhase = rootFailure.ErrorPhase
		resolved.ErrorCode = rootFailure.ErrorCode
		resolved.ErrorMessage = rootFailure.ErrorMessage
		return resolved
	}
	resolved.ErrorPhase = input.ErrorPhase
	resolved.ErrorCode = input.ErrorCode
	resolved.ErrorMessage = input.ErrorMessage
	return resolved
}

// activeAuditCaptureCount mirrors the module counter.
var activeAuditCaptureCount int64

// GetActiveAuditCaptureCount mirrors getActiveAuditCaptureCount.
func GetActiveAuditCaptureCount() int {
	return int(atomic.LoadInt64(&activeAuditCaptureCount))
}

// ResetActiveAuditCaptureCountForTest zeroes the module counter between
// tests (Node module state is per-process).
func ResetActiveAuditCaptureCountForTest() {
	atomic.StoreInt64(&activeAuditCaptureCount, 0)
}

// NewAuditCaptureContext mirrors the constructor.
func NewAuditCaptureContext(input AuditCaptureInput) *AuditCaptureContext {
	settingsSource := input.Settings
	if settingsSource == nil {
		settingsSource = FixedAuditLogSettingsSource{}
	}
	settings := settingsSource.ReadAuditLogSettings()
	clock := input.Clock
	if clock == nil {
		clock = SystemClock{}
	}
	trafficSource, trafficErr := NormalizeOpenAIGatewayTrafficSource(input.TrafficSource)
	if trafficErr != nil {
		trafficSource = TrafficSourceGateway
	}
	startedAtMs := input.StartedAtMs
	context := &AuditCaptureContext{
		input:         input,
		clock:         clock,
		traceID:       input.TraceID,
		auditLogID:    "audit_" + itoa64(startedAtMs) + "_" + newUUID(),
		clientIP:      input.ClientIP,
		startedAtMs:   startedAtMs,
		startedAtIso:  msToIso(startedAtMs),
		trafficSource: trafficSource,
		metadataOnly:  input.CaptureMode == CaptureModeMetadataOnly,
		activeAttempts: map[string]*AuditAttemptState{},
		gatewayContext: AuditGatewayContext{ProviderCode: OpenAIProtocolCode},
	}
	context.enabled = settings.Enabled
	if !context.enabled {
		// 关闭态仍保留调用方接口，但不能注册 response 监听、计算采样 hash
		// 或保存捕获配置。
		context.httpCompletion = nil
		context.sampleBucket = 0
		context.successCaptureSelected = false
		context.successHotRetentionEnabled = false
		context.capturePayloadBodies = false
		context.successSampleRate = 0
		context.activeCaptureMaxBytes = 0
		context.successFullBodyLimitBytes = 0
		context.problemFullBodyLimitBytes = 0
		return context
	}
	context.successSampleRate = settings.SuccessSampleRate
	context.activeCaptureMaxBytes = ResolveAuditCaptureLimits(settings)
	context.successFullBodyLimitBytes = settings.SuccessFullBodyLimitBytes
	context.problemFullBodyLimitBytes = settings.ProblemFullBodyLimitBytes
	context.httpCompletion = input.HTTPCompletion
	context.sampleBucket = sampleBucketForTraceID(context.traceID)
	context.successCaptureSelected = int64(context.sampleBucket) < int64(roundToInt(context.successSampleRate*10000))
	context.successHotRetentionEnabled = settings.SuccessHotRetentionHours > 0
	context.capturePayloadBodies = settings.FullBodyCaptureEnabled && !context.metadataOnly
	context.gatewayContext.TrafficSource = context.trafficSource
	if context.metadataOnly {
		metadata := NewOrderedObject()
		metadata.Set("trafficSource", context.trafficSource)
		metadata.Set("captureMode", "metadata_only")
		context.addPayload(AuditLogPayloadInput{
			PartType:    AuditPartGatewayMetadata,
			Body:        gatewayMetadataBody("traffic_source", metadata),
			HasBody:     true,
			ContentType: gatewayMetadataContentType,
		})
	} else if context.shouldCaptureSuccessPayloadsLocked() {
		context.addClientRequestPayload()
	}
	atomic.AddInt64(&activeAuditCaptureCount, 1)
	context.activeCaptureRegistered = true
	if context.httpCompletion != nil {
		context.cancelHTTPListener = context.httpCompletion.OnCompleted(context.markHTTPCompleted)
	}
	return context
}

const gatewayMetadataContentType = "application/json; audit=gateway-metadata"

// gatewayMetadataBody mirrors JSON.stringify({type, label, metadata}).
func gatewayMetadataBody(label string, metadata *OrderedObject) []byte {
	body := NewOrderedObject()
	body.Set("type", "gateway_metadata")
	body.Set("label", label)
	if metadata == nil {
		body.Set("metadata", NewOrderedObject())
	} else {
		body.Set("metadata", metadata)
	}
	encoded, err := body.MarshalJSON()
	if err != nil {
		return []byte("{}")
	}
	return encoded
}

// BindContext mirrors bindContext: merge defined fields only.
func (c *AuditCaptureContext) BindContext(context AuditGatewayContext) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if context.SessionID != "" {
		c.gatewayContext.SessionID = context.SessionID
	}
	if context.SessionClientType != "" {
		c.gatewayContext.SessionClientType = context.SessionClientType
	}
	if context.ConversationKey != "" {
		c.gatewayContext.ConversationKey = context.ConversationKey
	}
	if context.SystemAccountID != "" {
		c.gatewayContext.SystemAccountID = context.SystemAccountID
	}
	if context.APIKeyID != "" {
		c.gatewayContext.APIKeyID = context.APIKeyID
	}
	if context.GroupID != "" {
		c.gatewayContext.GroupID = context.GroupID
	}
	if context.AccountID != "" {
		c.gatewayContext.AccountID = context.AccountID
	}
	if context.ProviderCode != "" {
		c.gatewayContext.ProviderCode = context.ProviderCode
	}
	if context.UpstreamModel != "" {
		c.gatewayContext.UpstreamModel = context.UpstreamModel
	}
	if context.PricingModel != "" {
		c.gatewayContext.PricingModel = context.PricingModel
	}
	if context.ModelMappingApplied != nil {
		c.gatewayContext.ModelMappingApplied = context.ModelMappingApplied
	}
	if context.ModelMappingSource != "" {
		c.gatewayContext.ModelMappingSource = context.ModelMappingSource
	}
	if context.SourceEndpointFamily != "" {
		c.gatewayContext.SourceEndpointFamily = context.SourceEndpointFamily
	}
	if context.UpstreamEndpointFamily != "" {
		c.gatewayContext.UpstreamEndpointFamily = context.UpstreamEndpointFamily
	}
	if context.TrafficSource != "" {
		c.gatewayContext.TrafficSource = context.TrafficSource
	}
}

// MarkDownstreamClosed mirrors markDownstreamClosed.
func (c *AuditCaptureContext) MarkDownstreamClosed() {
	c.mu.Lock()
	if c.downstreamClosed {
		c.mu.Unlock()
		return
	}
	c.downstreamClosed = true
	c.mu.Unlock()
	c.AddGatewayMetadata("downstream_connection_closed", nil)
}

// MarkServerDiagnosticTimeout mirrors markServerDiagnosticTimeout.
func (c *AuditCaptureContext) MarkServerDiagnosticTimeout() {
	c.mu.Lock()
	if c.serverDiagnosticTimeout {
		c.mu.Unlock()
		return
	}
	c.serverDiagnosticTimeout = true
	c.mu.Unlock()
	metadata := NewOrderedObject()
	metadata.Set("source", "server_diagnostic")
	metadata.Set("trigger", "diagnostic_deadline")
	c.AddGatewayMetadata("server_diagnostic_timeout", metadata)
}

// MarkServerDiagnosticCancellation mirrors markServerDiagnosticCancellation.
func (c *AuditCaptureContext) MarkServerDiagnosticCancellation() {
	c.mu.Lock()
	if c.serverDiagnosticCancellation {
		c.mu.Unlock()
		return
	}
	c.serverDiagnosticCancellation = true
	c.mu.Unlock()
	metadata := NewOrderedObject()
	metadata.Set("source", "server_diagnostic")
	metadata.Set("trigger", "server_task_cancelled")
	c.AddGatewayMetadata("server_diagnostic_cancelled", metadata)
}

// ShouldCaptureSuccessPayloads mirrors shouldCaptureSuccessPayloads.
func (c *AuditCaptureContext) ShouldCaptureSuccessPayloads() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.shouldCaptureSuccessPayloadsLocked()
}

func (c *AuditCaptureContext) shouldCaptureSuccessPayloadsLocked() bool {
	return c.enabled && c.capturePayloadBodies && (
		c.successHotRetentionEnabled ||
			c.successCaptureSelected ||
			c.hadFailedAttempt)
}

// FinalizeLazy mirrors finalizeLazy.
func (c *AuditCaptureContext) FinalizeLazy(buildInput func() FinalizeAuditInput) {
	if !c.IsEnabled() {
		return
	}
	c.Finalize(buildInput())
}

// IsEnabled mirrors reading this.enabled.
func (c *AuditCaptureContext) IsEnabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enabled
}

// AuditLogID returns the generated audit log id.
func (c *AuditCaptureContext) AuditLogID() string {
	return c.auditLogID
}

// AddGatewayMetadata mirrors addGatewayMetadata. metadata may be nil,
// *OrderedObject or map[string]any.
func (c *AuditCaptureContext) AddGatewayMetadata(label string, metadata any) {
	if !c.IsEnabled() {
		return
	}
	var metadataValue any
	switch typed := metadata.(type) {
	case nil:
		metadataValue = NewOrderedObject()
	case *OrderedObject:
		metadataValue = typed
	default:
		metadataValue = typed
	}
	body := NewOrderedObject()
	body.Set("type", "gateway_metadata")
	body.Set("label", label)
	body.Set("metadata", metadataValue)
	encoded, err := body.MarshalJSON()
	if err != nil {
		return
	}
	c.addPayload(AuditLogPayloadInput{
		PartType:    AuditPartGatewayMetadata,
		Body:        encoded,
		HasBody:     true,
		ContentType: gatewayMetadataContentType,
	})
}

// OmitPayloadBodiesInput mirrors OmitPayloadBodiesInput.
type OmitPayloadBodiesInput struct {
	Metadata                    any
	Label                       string
	PartTypes                   []AuditPayloadPartType
	AlreadyOmittedPayloadCount  int
	AlreadyOmittedBodyBytes     int
}

// OmitPayloadBodies mirrors omitPayloadBodies: hash-only retention for the
// selected payload parts plus the omission metadata entry.
func (c *AuditCaptureContext) OmitPayloadBodies(input OmitPayloadBodiesInput) {
	if !c.IsEnabled() {
		return
	}
	partTypes := map[AuditPayloadPartType]bool{}
	for _, partType := range input.PartTypes {
		partTypes[partType] = true
	}
	omittedPayloadCount := input.AlreadyOmittedPayloadCount
	omittedBodyBytes := input.AlreadyOmittedBodyBytes
	c.mu.Lock()
	for index := range c.payloads {
		payload := &c.payloads[index]
		if len(partTypes) > 0 && !partTypes[payload.PartType] {
			continue
		}
		if !shouldOmitExistingPayloadBody(payload.PartType) || !payload.HasBody {
			continue
		}
		bodyBytes := len(payload.Body)
		omittedPayloadCount++
		omittedBodyBytes += bodyBytes
		if payload.BodySha256 == "" && bodyBytes <= auditInlineSha256MaxBytes {
			payload.BodySha256 = sha256Hex(payload.Body)
		}
		if payload.RawBodySizeBytes == nil {
			size := bodyBytes
			payload.RawBodySizeBytes = &size
		}
		payload.CaptureStatus = AuditCaptureHashOnly
		payload.Body = nil
		payload.HasBody = false
		payload.ContentEncoding = ""
	}
	overflowed := c.overflowed
	approximateBytes := c.approximateBytes
	c.mu.Unlock()
	if omittedPayloadCount > 0 && !overflowed {
		c.mu.Lock()
		recalculated := 0
		for index := range c.payloads {
			recalculated += estimatePayloadBytes(&c.payloads[index])
		}
		c.approximateBytes = recalculated
		c.residentPayloadBytes = recalculated
		c.mu.Unlock()
	}
	_ = approximateBytes
	metadata := NewOrderedObject()
	metadata.Set("auditBodyPayloadsOmitted", true)
	metadata.Set("omittedPayloadCount", omittedPayloadCount)
	metadata.Set("omittedBodyBytes", omittedBodyBytes)
	if ordered, ok := input.Metadata.(*OrderedObject); ok && ordered != nil {
		merged := NewOrderedObject()
		for _, key := range ordered.Keys() {
			merged.Set(key, ordered.Get(key))
		}
		for _, key := range metadata.Keys() {
			merged.Set(key, metadata.Get(key))
		}
		metadata = merged
	}
	c.AddGatewayMetadata(input.Label, metadata)
}

// StartAttemptInput mirrors StartAttemptInput.
type StartAttemptInput struct {
	Account               UsageModelAccount
	AttemptIndex          int
	UpstreamURL           string
	Method                string
	Headers               map[string]any
	Body                  []byte
	HasBody               bool
	Model                 string // requestForModelAccounting override
	SourceEndpointFamily  string // requestForModelAccounting override
}

// CompleteAttemptInput mirrors CompleteAttemptInput.
type CompleteAttemptInput struct {
	StatusCode     *int
	ResponseHeaders map[string]any
	ResponseBody   []byte
	HasResponseBody bool
	Success        bool
	ErrorPhase     string
	ErrorCode      string
	ErrorMessage   string
}

// RecordFailedDispatchAttemptInput mirrors FailedDispatchAttemptInput.
type RecordFailedDispatchAttemptInput struct {
	Account               UsageModelAccount
	AttemptIndex          int
	UpstreamURL           string
	Method                string
	StartedAtMs           int64
	StatusCode            *int
	ErrorPhase            string
	ErrorCode             string
	ErrorMessage          string
	Model                 string
	SourceEndpointFamily  string
}

// StartAttempt mirrors startAttempt: account context binding, in-progress
// audit enqueue, upstream_request payload capture.
func (c *AuditCaptureContext) StartAttempt(input StartAttemptInput) string {
	if !c.IsEnabled() {
		return ""
	}
	requestedModel := firstNonEmpty(input.Model, c.input.Model)
	sourceFamily := firstNonEmpty(input.SourceEndpointFamily, c.inputSourceEndpointFamilyHint())
	accounting := c.auditModelAccounting(input.Account, requestedModel, sourceFamily)
	c.BindContext(AuditGatewayContext{ProviderCode: input.Account.ProviderCode})
	c.BindContext(AuditGatewayContext{
		UpstreamModel:          accounting.upstreamModel,
		PricingModel:           accounting.pricingModel,
		ModelMappingApplied:    accounting.modelMappingApplied,
		ModelMappingSource:     accounting.modelMappingSource,
		SourceEndpointFamily:   accounting.sourceEndpointFamily,
		UpstreamEndpointFamily: accounting.upstreamEndpointFamily,
	})
	c.BindContext(AuditGatewayContext{AccountID: input.Account.ID})
	c.enqueueInProgressAudit()
	nowMs := c.clockMs()
	tempID := "attempt_" + itoa(input.AttemptIndex) + "_" + itoa64(nowMs) + "_" + randomHex6()
	attempt := AuditLogAttemptInput{
		ID:                          "audatt_" + itoa64(nowMs) + "_" + newUUID(),
		TempID:                      tempID,
		AttemptIndex:                input.AttemptIndex,
		AccountID:                   input.Account.ID,
		AccountOwnerSystemAccountID: input.Account.UsageAccess.AccountOwnerSystemAccountID,
		GroupID:                     c.gatewayContextSnapshot().GroupID,
		ProxyURL:                    SanitizeURLCredentialsForLog(input.Account.ProxyURL),
		ProviderCode:                input.Account.ProviderCode,
		Model:                       requestedModel,
		UpstreamModel:               accounting.upstreamModel,
		PricingModel:                accounting.pricingModel,
		ModelMappingApplied:         accounting.modelMappingApplied,
		ModelMappingSource:          accounting.modelMappingSource,
		SourceEndpointFamily:        accounting.sourceEndpointFamily,
		UpstreamEndpointFamily:      accounting.upstreamEndpointFamily,
		UpstreamMethod:              input.Method,
		UpstreamURL:                 orUnknown(SanitizeURLCredentialsForLog(input.UpstreamURL)),
		StartedAt:                   msToIso(nowMs),
	}
	c.mu.Lock()
	c.attempts = append(c.attempts, attempt)
	requestPayload := &AuditLogPayloadInput{
		AttemptTempID:   tempID,
		PartType:        AuditPartUpstreamRequest,
		Headers:         input.Headers,
		HasBody:         input.HasBody,
		Body:            input.Body,
		ContentType:     headerMapValue(input.Headers, "content-type"),
		ContentEncoding: headerMapValue(input.Headers, "content-encoding"),
	}
	if input.HasBody && len(input.Body) > 0 {
		requestPayload.BodySha256 = sha256Hex(input.Body)
		size := len(input.Body)
		requestPayload.RawBodySizeBytes = &size
	}
	state := &AuditAttemptState{
		TempID:         tempID,
		Attempt:        attempt,
		RequestPayload: requestPayload,
		StartedAtMs:    nowMs,
	}
	c.activeAttempts[tempID] = state
	shouldCapture := c.shouldCaptureSuccessPayloadsLocked()
	if shouldCapture {
		state.RequestPayloadCaptured = true
	}
	c.mu.Unlock()
	if shouldCapture {
		c.addClientRequestPayload()
		c.addPayload(*requestPayload)
	}
	return tempID
}

// CompleteAttempt mirrors completeAttempt. Node mutates the attempt object
// shared between the state map and the attempts array; Go values require
// updating the retained attempts entry by tempId.
func (c *AuditCaptureContext) CompleteAttempt(tempID string, input CompleteAttemptInput) {
	if !c.IsEnabled() {
		return
	}
	var requestPayloadCopy AuditLogPayloadInput
	pendingRequestPayload := false
	shouldCaptureResponse := false
	c.mu.Lock()
	state := c.activeAttempts[tempID]
	if state == nil || state.Completed {
		c.mu.Unlock()
		return
	}
	state.Completed = true
	endedAtMs := c.clockMs()
	duration := int(endedAtMs - state.StartedAtMs)
	applyCompleteToAttempt(&state.Attempt, input, endedAtMs, &duration)
	for index := range c.attempts {
		if c.attempts[index].TempID == tempID {
			applyCompleteToAttempt(&c.attempts[index], input, endedAtMs, &duration)
			break
		}
	}
	if !input.Success {
		c.hadFailedAttempt = true
	}
	if c.capturePayloadBodies && !input.Success && state.RequestPayload != nil && !state.RequestPayloadCaptured {
		pendingRequestPayload = true
		state.RequestPayloadCaptured = true
		requestPayloadCopy = *state.RequestPayload
	}
	shouldCaptureResponse = c.capturePayloadBodies &&
		(!input.Success || c.shouldCaptureSuccessPayloadsLocked()) &&
		(input.ResponseHeaders != nil || input.HasResponseBody)
	c.mu.Unlock()
	if pendingRequestPayload {
		c.addPayload(requestPayloadCopy)
	}
	if shouldCaptureResponse {
		c.addPayload(AuditLogPayloadInput{
			AttemptTempID:   tempID,
			PartType:        AuditPartUpstreamResponse,
			Headers:         input.ResponseHeaders,
			HasBody:         input.HasResponseBody,
			Body:            input.ResponseBody,
			ContentType:     headerMapValue(input.ResponseHeaders, "content-type"),
			ContentEncoding: headerMapValue(input.ResponseHeaders, "content-encoding"),
		})
	}
}

func applyCompleteToAttempt(attempt *AuditLogAttemptInput, input CompleteAttemptInput, endedAtMs int64, duration *int) {
	attempt.EndedAt = msToIso(endedAtMs)
	attempt.DurationMs = duration
	attempt.UpstreamStatusCode = input.StatusCode
	attempt.Success = &input.Success
	attempt.ErrorPhase = input.ErrorPhase
	attempt.ErrorCode = input.ErrorCode
	attempt.ErrorMessage = input.ErrorMessage
}

// RecordFailedDispatchAttempt mirrors recordFailedDispatchAttempt.
func (c *AuditCaptureContext) RecordFailedDispatchAttempt(input RecordFailedDispatchAttemptInput) string {
	if !c.IsEnabled() {
		return ""
	}
	requestedModel := firstNonEmpty(input.Model, c.input.Model)
	sourceFamily := firstNonEmpty(input.SourceEndpointFamily, c.inputSourceEndpointFamilyHint())
	accounting := c.auditModelAccounting(input.Account, requestedModel, sourceFamily)
	c.BindContext(AuditGatewayContext{ProviderCode: input.Account.ProviderCode})
	c.BindContext(AuditGatewayContext{
		UpstreamModel:          accounting.upstreamModel,
		PricingModel:           accounting.pricingModel,
		ModelMappingApplied:    accounting.modelMappingApplied,
		ModelMappingSource:     accounting.modelMappingSource,
		SourceEndpointFamily:   accounting.sourceEndpointFamily,
		UpstreamEndpointFamily: accounting.upstreamEndpointFamily,
	})
	nowMs := c.clockMs()
	tempID := "attempt_" + itoa(input.AttemptIndex) + "_" + itoa64(nowMs) + "_" + randomHex6()
	sanitizedUpstreamURL := SanitizeURLCredentialsForLog(input.UpstreamURL)
	if sanitizedUpstreamURL == "" {
		sanitizedUpstreamURL = strings.TrimSpace(input.UpstreamURL)
	}
	attempt := AuditLogAttemptInput{
		ID:                          "audatt_" + itoa64(nowMs) + "_" + newUUID(),
		TempID:                      tempID,
		AttemptIndex:                input.AttemptIndex,
		AccountID:                   input.Account.ID,
		AccountOwnerSystemAccountID: input.Account.UsageAccess.AccountOwnerSystemAccountID,
		GroupID:                     c.gatewayContextSnapshot().GroupID,
		ProxyURL:                    SanitizeURLCredentialsForLog(input.Account.ProxyURL),
		ProviderCode:                input.Account.ProviderCode,
		Model:                       requestedModel,
		UpstreamModel:               accounting.upstreamModel,
		PricingModel:                accounting.pricingModel,
		ModelMappingApplied:         accounting.modelMappingApplied,
		ModelMappingSource:          accounting.modelMappingSource,
		SourceEndpointFamily:        accounting.sourceEndpointFamily,
		UpstreamEndpointFamily:      accounting.upstreamEndpointFamily,
		UpstreamMethod:              input.Method,
		UpstreamURL:                 firstNonEmpty(sanitizedUpstreamURL, "unknown"),
		UpstreamStatusCode:          input.StatusCode,
		Success:                     boolPointer(false),
		ErrorPhase:                  input.ErrorPhase,
		ErrorCode:                   input.ErrorCode,
		ErrorMessage:                input.ErrorMessage,
		StartedAt:                   msToIso(input.StartedAtMs),
		EndedAt:                     msToIso(nowMs),
		DurationMs:                  intPointer(int(nowMs - input.StartedAtMs)),
	}
	c.mu.Lock()
	c.attempts = append(c.attempts, attempt)
	c.hadFailedAttempt = true
	c.mu.Unlock()
	return tempID
}

// LatestFailedAttemptRoot mirrors latestFailedAttemptRoot.
func (c *AuditCaptureContext) LatestFailedAttemptRoot() *FailedAuditAttemptRoot {
	c.mu.Lock()
	defer c.mu.Unlock()
	for index := len(c.attempts) - 1; index >= 0; index-- {
		attempt := &c.attempts[index]
		if attempt.Success == nil || *attempt.Success {
			continue
		}
		if attempt.ErrorPhase == "downstream" {
			continue
		}
		if attempt.ErrorPhase == "" && attempt.ErrorCode == "" && attempt.ErrorMessage == "" {
			continue
		}
		return &FailedAuditAttemptRoot{
			ErrorPhase:   attempt.ErrorPhase,
			ErrorCode:    attempt.ErrorCode,
			ErrorMessage: attempt.ErrorMessage,
		}
	}
	return nil
}

// Finalize mirrors finalize: wait for HTTP completion when wired, then flush.
func (c *AuditCaptureContext) Finalize(input FinalizeAuditInput) {
	c.mu.Lock()
	if c.finalized {
		c.mu.Unlock()
		return
	}
	c.finalized = true
	enabled := c.enabled
	completion := c.httpCompletion
	completedKnown := c.httpCompletedAtMs != nil
	c.mu.Unlock()
	if !enabled {
		c.logStage(nil, "skipped", true)
		return
	}
	c.mu.Lock()
	c.pendingFinalizeInput = &input
	c.mu.Unlock()
	if completion == nil || completedKnown {
		c.flushFinalizedAudit()
		return
	}
	if completedAtMs, ok := completion.CompletedAtMs(); ok {
		c.markHTTPCompleted(completedAtMs)
	}
}

// Cancel mirrors cancel.
func (c *AuditCaptureContext) Cancel() {
	c.mu.Lock()
	if c.finalized {
		c.mu.Unlock()
		return
	}
	c.finalized = true
	c.pendingFinalizeInput = nil
	registered := c.activeCaptureRegistered
	c.activeCaptureRegistered = false
	cancel := c.cancelHTTPListener
	c.cancelHTTPListener = nil
	c.mu.Unlock()
	if registered {
		atomic.AddInt64(&activeAuditCaptureCount, -1)
	}
	if cancel != nil {
		cancel()
	}
}

func (c *AuditCaptureContext) markHTTPCompleted(completedAtMs int64) {
	c.mu.Lock()
	if c.httpCompletedAtMs != nil {
		c.mu.Unlock()
		return
	}
	value := completedAtMs
	c.httpCompletedAtMs = &value
	pending := c.pendingFinalizeInput
	c.mu.Unlock()
	if pending != nil {
		c.flushFinalizedAudit()
	}
}

func (c *AuditCaptureContext) flushFinalizedAudit() {
	c.mu.Lock()
	input := c.pendingFinalizeInput
	if input == nil {
		c.mu.Unlock()
		return
	}
	c.pendingFinalizeInput = nil
	registered := c.activeCaptureRegistered
	c.activeCaptureRegistered = false
	cancel := c.cancelHTTPListener
	c.cancelHTTPListener = nil
	endedAtMs := c.clockMs()
	finalization := ResolveAuditFinalization(ResolveAuditFinalizationInput{
		Outcome:      input.Outcome,
		Success:      input.Success,
		ErrorPhase:   input.ErrorPhase,
		ErrorCode:    input.ErrorCode,
		ErrorMessage: input.ErrorMessage,
	}, c.downstreamClosed, c.hadFailedAttempt, c.latestFailedAttemptRootLocked())
	outcome := finalization.Outcome
	success := finalization.Success
	if input.AccountID != "" {
		c.gatewayContext.AccountID = input.AccountID
	}
	shouldCapturePayloadBodies := c.capturePayloadBodies && (
		outcome != AuditOutcomeSuccess ||
			c.successHotRetentionEnabled ||
			c.successCaptureSelected)
	if outcome != AuditOutcomeSuccess || c.shouldCaptureSuccessPayloadsLocked() {
		c.addClientRequestPayloadLocked()
	}
	shouldAddResponsePayload := shouldCapturePayloadBodies && (input.HasResponseBody || input.ResponseHeaders != nil)
	responsePartType := input.ResponsePartType
	if responsePartType == "" {
		if input.Success {
			responsePartType = AuditPartGatewayResponse
		} else {
			responsePartType = AuditPartGatewayError
		}
	}
	offload := c.input.OffloadPayloadRetention
	problemLimit := c.problemFullBodyLimitBytes
	successLimit := c.successFullBodyLimitBytes
	overflowed := c.overflowed
	metadataOnly := c.metadataOnly
	successHotRetention := c.successHotRetentionEnabled
	successSelected := c.successCaptureSelected
	sampleBucket := c.sampleBucket
	auditLogID := c.auditLogID
	startedAtIso := c.startedAtIso
	startedAtMs := c.startedAtMs
	httpCompletedAtMs := c.httpCompletedAtMs
	gatewayContext := c.gatewayContext
	retainedAttempts := make([]AuditLogAttemptInput, len(c.attempts))
	copy(retainedAttempts, c.attempts)
	retainedPayloads := make([]AuditLogPayloadInput, len(c.payloads))
	copy(retainedPayloads, c.payloads)
	c.mu.Unlock()
	if registered {
		atomic.AddInt64(&activeAuditCaptureCount, -1)
	}
	if cancel != nil {
		cancel()
	}

	if shouldAddResponsePayload {
		c.addPayload(AuditLogPayloadInput{
			PartType:        responsePartType,
			Headers:         input.ResponseHeaders,
			HasBody:         input.HasResponseBody,
			Body:            input.ResponseBody,
			ContentType:     headerMapValue(input.ResponseHeaders, "content-type"),
			ContentEncoding: headerMapValue(input.ResponseHeaders, "content-encoding"),
		})
	}
	if outcome == AuditOutcomeSuccess {
		c.applyPayloadRetention("success", successLimit, offload)
	} else {
		c.applyPayloadRetention("failure", problemLimit, offload)
	}
	unsampledSuccessEnvelope := !overflowed &&
		outcome == AuditOutcomeSuccess &&
		!successHotRetention &&
		!successSelected
	if unsampledSuccessEnvelope {
		retainedAttempts = []AuditLogAttemptInput{}
		retainedPayloads = []AuditLogPayloadInput{}
	} else {
		c.mu.Lock()
		retainedAttempts = make([]AuditLogAttemptInput, len(c.attempts))
		copy(retainedAttempts, c.attempts)
		retainedPayloads = make([]AuditLogPayloadInput, len(c.payloads))
		copy(retainedPayloads, c.payloads)
		c.mu.Unlock()
	}
	if retainedAttempts == nil {
		retainedAttempts = []AuditLogAttemptInput{}
	}
	if retainedPayloads == nil {
		retainedPayloads = []AuditLogPayloadInput{}
	}
	sanitizedOriginalURL := SanitizeURLForLog(c.input.OriginalURL)
	path := strings.Split(sanitizedOriginalURL, "?")[0]
	if path == "" {
		path = c.input.Path
	}
	queryString := ""
	if strings.Contains(sanitizedOriginalURL, "?") {
		parts := strings.SplitN(sanitizedOriginalURL, "?", 2)
		queryString = strings.Join(parts[1:], "?")
	}
	auditLog := AuditLogInput{
		ID:                        auditLogID,
		LifecycleStatus:           AuditLifecycleFinalized,
		TraceID:                   c.traceID,
		SessionID:                 gatewayContext.SessionID,
		SessionClientType:         gatewayContext.SessionClientType,
		ConversationKey:           gatewayContext.ConversationKey,
		SystemAccountID:           gatewayContext.SystemAccountID,
		APIKeyID:                  gatewayContext.APIKeyID,
		GroupID:                   gatewayContext.GroupID,
		ProviderCode:              gatewayContext.ProviderCode,
		TrafficSource:             firstNonEmpty(gatewayContext.TrafficSource, c.trafficSource),
		AccountID:                 firstNonEmpty(input.AccountID, gatewayContext.AccountID),
		Method:                    strings.ToUpper(c.input.Method),
		Path:                      path,
		QueryString:               queryString,
		Model:                     c.input.Model,
		UpstreamModel:             gatewayContext.UpstreamModel,
		PricingModel:              gatewayContext.PricingModel,
		ModelMappingApplied:       gatewayContext.ModelMappingApplied,
		ModelMappingSource:        gatewayContext.ModelMappingSource,
		SourceEndpointFamily:      gatewayContext.SourceEndpointFamily,
		UpstreamEndpointFamily:    gatewayContext.UpstreamEndpointFamily,
		Stream:                    boolPointer(c.input.Stream),
		ClientIP:                  c.clientIP,
		UserAgent:                 c.input.UserAgent,
		AuditOutcome:              outcome,
		Success:                   success,
		FinalStatusCode:           input.StatusCode,
		ErrorPhase:                finalization.ErrorPhase,
		ErrorCode:                 finalization.ErrorCode,
		ErrorMessage:              finalization.ErrorMessage,
		SampleBucket:              sampleBucket,
		SampleReason:              c.sampleReasonForOutcome(outcome, metadataOnly, successHotRetention, successSelected),
		CaptureStatus:             captureStatusFor(overflowed, metadataOnly || unsampledSuccessEnvelope),
		StartedAt:                 startedAtIso,
		EndedAt:                   msToIso(endedAtMs),
		DurationMs:                intPointer(int(endedAtMs - startedAtMs)),
		FirstTokenMs:              input.FirstTokenMs,
		Attempts:                  retainedAttempts,
		Payloads:                  retainedPayloads,
	}
	if httpCompletedAtMs != nil {
		auditLog.HTTPCompletedAt = msToIso(*httpCompletedAtMs)
		auditLog.HTTPDurationMs = intPointer(int(*httpCompletedAtMs - startedAtMs))
	}
	stageFields := orderedLogFields()
	stageFields.Set("traceId", c.traceID)
	stageFields.Set("outcome", outcome)
	stageFields.Set("success", success)
	stageFields.Set("payloadCount", len(retainedPayloads))
	stageFields.Set("attemptCount", len(retainedAttempts))
	stageFields.Set("captureStatus", auditLog.CaptureStatus)
	stageFields.Set("sampleReason", auditLog.SampleReason)
	if c.input.Logger != nil {
		c.input.Logger.Debug("网关审计捕获已完成，准备投递", stageFields.AsMap())
	}
	if c.input.StageLogger != nil {
		if success {
			c.input.StageLogger.LogRequestStage("audit.finalize", stageFields.AsMap(), "success")
		} else {
			failureFields := stageFields.Clone()
			failureFields.Set("failureReason", outcome)
			failureFields.Set("terminalExpectedFailure", true)
			decisionInputs := NewOrderedObject()
			decisionInputs.Set("statusCode", input.StatusCode)
			decisionInputs.Set("errorPhase", input.ErrorPhase)
			decisionInputs.Set("errorCode", input.ErrorCode)
			c.mu.Lock()
			decisionInputs.Set("attemptCount", len(c.attempts))
			decisionInputs.Set("downstreamClosed", c.downstreamClosed)
			c.mu.Unlock()
			failureFields.Set("decisionInputs", decisionInputs)
			c.input.StageLogger.LogRequestStage("audit.finalize", failureFields.AsMap(), "expected_failure")
		}
	}
	DispatchAuditLogToGo(c.input.Dispatcher, contextWithBackground(), auditLog)
}

func (c *AuditCaptureContext) logStage(fields map[string]any, outcome string, skipped bool) {
	if c.input.StageLogger == nil {
		return
	}
	payload := fields
	if payload == nil {
		payload = map[string]any{
			"traceId":      c.traceID,
			"auditEnabled": false,
			"skipped":      skipped,
		}
	}
	c.input.StageLogger.LogRequestStage("audit.finalize", payload, outcome)
}

func captureStatusFor(overflowed bool, metadataOnly bool) string {
	if overflowed {
		return "overflow"
	}
	if metadataOnly {
		return "metadata_only"
	}
	return "complete"
}

// enqueueInProgressAudit mirrors enqueueInProgressAudit: stream requests get
// an in_progress envelope before the first upstream attempt completes.
func (c *AuditCaptureContext) enqueueInProgressAudit() {
	c.mu.Lock()
	if c.inProgressAuditEnqueued || !c.input.Stream {
		c.mu.Unlock()
		return
	}
	c.inProgressAuditEnqueued = true
	sanitizedOriginalURL := SanitizeURLForLog(c.input.OriginalURL)
	path := strings.Split(sanitizedOriginalURL, "?")[0]
	if path == "" {
		path = c.input.Path
	}
	queryString := ""
	if strings.Contains(sanitizedOriginalURL, "?") {
		parts := strings.SplitN(sanitizedOriginalURL, "?", 2)
		queryString = strings.Join(parts[1:], "?")
	}
	gatewayContext := c.gatewayContext
	sampleBucket := c.sampleBucket
	auditLogID := c.auditLogID
	startedAtIso := c.startedAtIso
	clientIP := c.clientIP
	trafficSource := c.trafficSource
	c.mu.Unlock()
	auditLog := AuditLogInput{
		ID:                auditLogID,
		LifecycleStatus:   AuditLifecycleInProgress,
		TraceID:           c.traceID,
		SessionID:         gatewayContext.SessionID,
		SessionClientType: gatewayContext.SessionClientType,
		ConversationKey:   gatewayContext.ConversationKey,
		SystemAccountID:   gatewayContext.SystemAccountID,
		APIKeyID:          gatewayContext.APIKeyID,
		GroupID:           gatewayContext.GroupID,
		AccountID:         gatewayContext.AccountID,
		ProviderCode:      gatewayContext.ProviderCode,
		TrafficSource:     firstNonEmpty(gatewayContext.TrafficSource, trafficSource),
		Method:            strings.ToUpper(c.input.Method),
		Path:              path,
		QueryString:       queryString,
		Model:             c.input.Model,
		Stream:            boolPointer(c.input.Stream),
		ClientIP:          clientIP,
		UserAgent:         c.input.UserAgent,
		AuditOutcome:      AuditOutcomeGatewaySucceeded,
		Success:           true,
		SampleBucket:      sampleBucket,
		SampleReason:      "in_progress",
		CaptureStatus:     "metadata_only",
		StartedAt:         startedAtIso,
		EndedAt:           startedAtIso,
		Attempts:          []AuditLogAttemptInput{},
		Payloads:          []AuditLogPayloadInput{},
	}
	DispatchAuditLogToGo(c.input.Dispatcher, contextWithBackground(), auditLog)
}

// DispatchAuditLogToGo mirrors the dispatchAuditLogToGo entry semantics:
// the non-persisted traffic-source gate runs before the port, and delivery
// is best-effort (no error to the caller).
func DispatchAuditLogToGo(dispatcher AuditDispatcher, ctx Ctx, input AuditLogInput) {
	if input.TrafficSource != "" && !ShouldPersistAuditTrafficSource(input.TrafficSource) {
		return
	}
	if dispatcher == nil {
		return
	}
	dispatcher.DispatchAuditLog(ctx, input)
}

// addClientRequestPayload mirrors addClientRequestPayload.
func (c *AuditCaptureContext) addClientRequestPayload() {
	c.mu.Lock()
	c.addClientRequestPayloadLocked()
	c.mu.Unlock()
}

func (c *AuditCaptureContext) addClientRequestPayloadLocked() {
	if !c.enabled || !c.capturePayloadBodies || c.clientRequestPayloadCaptured {
		return
	}
	rawBody := c.input.RawBody
	if len(rawBody) > c.activeCaptureMaxBytes {
		c.clientRequestPayloadCaptured = true
		c.markResidentPayloadOverflowLocked(len(rawBody))
		return
	}
	contentType := headerMapValue(c.input.RequestHeaders, "content-type")
	contentEncoding := headerMapValue(c.input.RequestHeaders, "content-encoding")
	c.clientRequestPayloadCaptured = true
	payload := AuditLogPayloadInput{
		PartType:        AuditPartClientRequest,
		Headers:         c.input.RequestHeaders,
		HasBody:         rawBody != nil,
		Body:            rawBody,
		ContentType:     contentType,
		ContentEncoding: contentEncoding,
	}
	if len(rawBody) > 0 {
		payload.BodySha256 = sha256Hex(rawBody)
		size := len(rawBody)
		payload.RawBodySizeBytes = &size
	}
	c.addPayloadLocked(payload)
}

// addPayload mirrors addPayload with the retention/overflow bookkeeping.
func (c *AuditCaptureContext) addPayload(payload AuditLogPayloadInput) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.addPayloadLocked(payload)
}

func (c *AuditCaptureContext) addPayloadLocked(payload AuditLogPayloadInput) {
	if !c.enabled {
		return
	}
	if c.overflowed {
		return
	}
	if !c.input.OffloadPayloadRetention {
		SummarizeAuditPayloadForLimit(&payload, c.problemFullBodyLimitBytes, SummarizeAuditPayloadOptions{})
	}
	nextResidentPayloadBytes := c.residentPayloadBytes + estimatePayloadBytes(&payload)
	if nextResidentPayloadBytes > c.activeCaptureMaxBytes {
		c.markResidentPayloadOverflowLocked(nextResidentPayloadBytes)
		return
	}
	nextApproximateBytes := c.approximateBytes + estimateRetainedPayloadBytes(&payload, c.problemFullBodyLimitBytes, c.input.OffloadPayloadRetention)
	if nextApproximateBytes > c.activeCaptureMaxBytes {
		c.markResidentPayloadOverflowLocked(nextResidentPayloadBytes)
		return
	}
	now := c.clockMs()
	payload.ID = "audpay_" + itoa64(now) + "_" + newUUID()
	sequenceIndex := c.sequenceIndex
	payload.SequenceIndex = &sequenceIndex
	payload.CreatedAt = msToIso(now)
	c.payloads = append(c.payloads, payload)
	c.approximateBytes = nextApproximateBytes
	c.residentPayloadBytes = nextResidentPayloadBytes
	c.sequenceIndex++
}

// markResidentPayloadOverflowLocked mirrors markResidentPayloadOverflow.
func (c *AuditCaptureContext) markResidentPayloadOverflowLocked(residentPayloadBytes int) {
	c.overflowed = true
	c.payloads = nil
	metadata := NewOrderedObject()
	metadata.Set("auditBodyPayloadsOmitted", true)
	metadata.Set("residentPayloadBytes", residentPayloadBytes)
	metadata.Set("activeCaptureMaxBytes", c.activeCaptureMaxBytes)
	now := c.clockMs()
	payload := AuditLogPayloadInput{
		ID:            "audpay_" + itoa64(now) + "_" + newUUID(),
		PartType:      AuditPartGatewayMetadata,
		Body:          gatewayMetadataBody("active_capture_overflow", metadata),
		HasBody:       true,
		ContentType:   gatewayMetadataContentType,
		CaptureStatus: AuditCaptureOverflow,
	}
	sequenceIndex := c.sequenceIndex
	payload.SequenceIndex = &sequenceIndex
	payload.CreatedAt = msToIso(now)
	c.payloads = append(c.payloads, payload)
	c.approximateBytes = estimatePayloadBytes(&payload)
	c.residentPayloadBytes = c.approximateBytes
	c.sequenceIndex++
}

// applyPayloadRetention mirrors applyPayloadRetention.
func (c *AuditCaptureContext) applyPayloadRetention(mode string, fullBodyLimitBytes int, offload bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if offload {
		total := 0
		for index := range c.payloads {
			total += estimateRetainedPayloadBytes(&c.payloads[index], fullBodyLimitBytes, offload)
		}
		c.approximateBytes = total
		return
	}
	for index := range c.payloads {
		SummarizeAuditPayloadForLimit(&c.payloads[index], fullBodyLimitBytes, SummarizeAuditPayloadOptions{})
	}
	total := 0
	for index := range c.payloads {
		total += estimatePayloadBytes(&c.payloads[index])
	}
	c.approximateBytes = total
	c.residentPayloadBytes = total
}

// sampleReasonForOutcome mirrors sampleReasonForOutcome.
func (c *AuditCaptureContext) sampleReasonForOutcome(outcome AuditOutcome, metadataOnly bool, hotRetention bool, successSelected bool) string {
	if metadataOnly {
		return c.trafficSource + "_metadata_only"
	}
	if outcome != AuditOutcomeSuccess {
		return "full_capture"
	}
	if successSelected {
		return "success_sample_" + formatSampleRate(c.successSampleRate)
	}
	if hotRetention {
		return "success_hot_full_retention"
	}
	return "success_metadata_only"
}

// auditModelAccounting mirrors auditModelAccounting: provider-driver model
// resolution plus the cacheDriver-gated synchronous pricing model.
type auditModelAccountingResult struct {
	upstreamModel          string
	pricingModel           string
	modelMappingApplied    *bool
	modelMappingSource     string
	sourceEndpointFamily   string
	upstreamEndpointFamily string
}

func (c *AuditCaptureContext) auditModelAccounting(account UsageModelAccount, requestedModel string, sourceEndpointFamily string) auditModelAccountingResult {
	resolved := UsageModelResolution{UpstreamModel: requestedModel}
	if c.input.Models != nil {
		resolved = c.input.Models.ResolveUsageModel(account, requestedModel, sourceEndpointFamily)
	}
	upstreamModel := firstNonEmpty(resolved.UpstreamModel, requestedModel)
	catalogSystemAccountID := firstNonEmpty(account.UsageAccess.AccountOwnerSystemAccountID, c.gatewayContextSnapshot().SystemAccountID)
	pricingModel := ""
	if upstreamModel != "" && c.input.SyncPricingAllowed && c.input.Pricing != nil {
		pricingModel = c.input.Pricing.ResolvePricingModel(account.ProviderCode, catalogSystemAccountID, upstreamModel)
	}
	return auditModelAccountingResult{
		upstreamModel:          upstreamModel,
		pricingModel:           pricingModel,
		modelMappingApplied:    boolPointer(resolved.ModelMappingApplied),
		modelMappingSource:     resolved.ModelMappingSource,
		sourceEndpointFamily:   resolved.SourceEndpointFamily,
		upstreamEndpointFamily: resolved.UpstreamEndpointFamily,
	}
}

// inputSourceEndpointFamilyHint returns the request-level endpoint family
// override; the capture-level default mirrors
// gatewayRequestEndpointFamily(req) evaluated by the caller.
func (c *AuditCaptureContext) inputSourceEndpointFamilyHint() string {
	return c.input.SourceEndpointFamily
}

func (c *AuditCaptureContext) clockMs() int64 {
	if c.clock == nil {
		return 0
	}
	return c.clock.Now().UnixMilli()
}

func (c *AuditCaptureContext) gatewayContextSnapshot() AuditGatewayContext {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gatewayContext
}

func (c *AuditCaptureContext) latestFailedAttemptRootLocked() *FailedAuditAttemptRoot {
	for index := len(c.attempts) - 1; index >= 0; index-- {
		attempt := &c.attempts[index]
		if attempt.Success == nil || *attempt.Success {
			continue
		}
		if attempt.ErrorPhase == "downstream" {
			continue
		}
		if attempt.ErrorPhase == "" && attempt.ErrorCode == "" && attempt.ErrorMessage == "" {
			continue
		}
		return &FailedAuditAttemptRoot{
			ErrorPhase:   attempt.ErrorPhase,
			ErrorCode:    attempt.ErrorCode,
			ErrorMessage: attempt.ErrorMessage,
		}
	}
	return nil
}

// shouldOmitExistingPayloadBody mirrors shouldOmitExistingPayloadBody.
func shouldOmitExistingPayloadBody(partType AuditPayloadPartType) bool {
	return partType != AuditPartGatewayMetadata
}

// sampleBucketForTraceID mirrors sampleBucketForTraceId.
func sampleBucketForTraceID(traceID string) int {
	digest := sha256.Sum256([]byte(traceID))
	return int(binary.BigEndian.Uint32(digest[:4]) % 10000)
}

// estimatePayloadBytes mirrors estimatePayloadBytes.
func estimatePayloadBytes(payload *AuditLogPayloadInput) int {
	bodyBytes := 0
	if payload.HasBody {
		bodyBytes = len(payload.Body)
	}
	headerBytes := 0
	if payload.Headers != nil {
		headerBytes = estimateHeadersBytes(payload.Headers)
	}
	return bodyBytes + headerBytes + 512
}

// estimateRetainedPayloadBytes mirrors estimateRetainedPayloadBytes.
func estimateRetainedPayloadBytes(payload *AuditLogPayloadInput, fullBodyLimitBytes int, offload bool) int {
	if !offload ||
		payload.PartType == AuditPartGatewayMetadata ||
		!payload.HasBody ||
		(payload.CaptureStatus != "" && payload.CaptureStatus != AuditCaptureComplete) {
		return estimatePayloadBytes(payload)
	}
	bodyBytes := len(payload.Body)
	retainedBodyBytes := bodyBytes
	if retainedBodyBytes > fullBodyLimitBytes {
		retainedBodyBytes = fullBodyLimitBytes
	}
	if retainedBodyBytes > AuditBodySummaryEdgeBytes*2 {
		retainedBodyBytes = AuditBodySummaryEdgeBytes * 2
	}
	headerBytes := 0
	if payload.Headers != nil {
		headerBytes = estimateHeadersBytes(payload.Headers)
	}
	return retainedBodyBytes + headerBytes + 512
}

// estimateHeadersBytes mirrors estimateHeadersBytes.
func estimateHeadersBytes(headers map[string]any) int {
	totalBytes := 2
	for name, value := range headers {
		totalBytes += len(name) + 4
		switch typed := value.(type) {
		case []string:
			totalBytes += 2
			for _, item := range typed {
				totalBytes += len(item) + 3
			}
		case []any:
			totalBytes += 2
			for _, item := range typed {
				if text, ok := item.(string); ok {
					totalBytes += len(text) + 3
				}
			}
		case string:
			totalBytes += len(typed) + 2
		default:
			totalBytes += 2
		}
	}
	return totalBytes
}

// headerMapValue mirrors headerValue: map lookup by exact then lowercase.
func headerMapValue(headers map[string]any, name string) string {
	if headers == nil {
		return ""
	}
	if value, ok := headers[name]; ok {
		return headerValueText(value)
	}
	if value, ok := headers[strings.ToLower(name)]; ok {
		return headerValueText(value)
	}
	return ""
}

func headerValueText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []string:
		if len(typed) == 0 {
			return ""
		}
		return strings.Join(typed, ", ")
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, ", ")
	default:
		return ""
	}
}

// ResponseInspectionDecisionAuditMetadataInput mirrors the consumed
// ResponseInspectionDecision fields of audit/metadata.ts
// (responseInspectionAuditMetadata).
type ResponseInspectionDecisionAuditMetadataInput struct {
	Action                   string
	Reason                   string
	Transport                string
	EndpointFamily           string
	FrameType                string
	TriggerPhase             string
	UpstreamEventType        string
	UpstreamErrorCode        string
	UpstreamErrorType        string
	UpstreamErrorMessage     string
	FinishReason             string
	ClientProfile            string
	CodexCompactionExpected  bool
	RewriteErrorCode         string
	RewriteMessage           string
	DownstreamWritten        bool
	PolicyID                 string
	PolicyName               string
	PolicySource             string
	PolicyScopeType          string
	PolicyProtocolCode       string
	PolicyProviderCode       string
	ExecutionMode            string
	DataHandling             string
	RetryEnabled             bool
	AccountSwitch            string
	AccountState             string
	MatchedField             string
	MatchedValue             string
	MatchedSnippet           string
}

// ResponseInspectionDecisionAuditMetadata mirrors
// responseInspectionAuditMetadata(decision): the ordered metadata record
// attached to response inspection audit entries. Optional strings are
// omitted when empty, mirroring JSON.stringify's undefined drop.
func ResponseInspectionDecisionAuditMetadata(decision ResponseInspectionDecisionAuditMetadataInput) *OrderedObject {
	metadata := NewOrderedObject()
	metadata.Set("responsePolicyMatched", true)
	metadata.Set("responseInspectionIntercepted", decision.Action != "dry_run")
	metadata.Set("fallbackReason", decision.Reason)
	metadata.Set("inspectionAction", decision.Action)
	metadata.Set("transport", decision.Transport)
	metadata.Set("endpointFamily", decision.EndpointFamily)
	metadata.Set("frameType", decision.FrameType)
	metadata.Set("triggerPhase", decision.TriggerPhase)
	metadata.Set("upstreamEventType", decision.UpstreamEventType)
	metadata.Set("upstreamErrorCode", decision.UpstreamErrorCode)
	metadata.Set("upstreamErrorType", decision.UpstreamErrorType)
	metadata.Set("upstreamErrorMessage", decision.UpstreamErrorMessage)
	metadata.Set("finishReason", decision.FinishReason)
	metadata.Set("clientProfile", decision.ClientProfile)
	metadata.Set("codexCompactionExpected", decision.CodexCompactionExpected)
	metadata.Set("rewriteErrorCode", decision.RewriteErrorCode)
	metadata.Set("rewriteMessage", decision.RewriteMessage)
	metadata.Set("downstreamWritten", decision.DownstreamWritten)
	metadata.Set("policyId", decision.PolicyID)
	metadata.Set("policyName", decision.PolicyName)
	metadata.Set("policySource", decision.PolicySource)
	metadata.Set("policyScopeType", decision.PolicyScopeType)
	metadata.Set("policyProtocolCode", decision.PolicyProtocolCode)
	metadata.Set("policyProviderCode", decision.PolicyProviderCode)
	metadata.Set("executionMode", decision.ExecutionMode)
	metadata.Set("dataHandling", decision.DataHandling)
	metadata.Set("retryEnabled", decision.RetryEnabled)
	metadata.Set("accountSwitch", decision.AccountSwitch)
	metadata.Set("accountState", decision.AccountState)
	metadata.Set("matchedField", decision.MatchedField)
	metadata.Set("matchedValue", decision.MatchedValue)
	metadata.Set("matchedSnippet", decision.MatchedSnippet)
	return metadata
}

// randomHex6 mirrors Math.random().toString(16).slice(2, 8).
func randomHex6() string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, 6)
	for index := range out {
		out[index] = hexDigits[randomIntn(16)]
	}
	return string(out)
}

func randomIntn(limit int) int {
	return mrand.Intn(limit)
}

func roundToInt(value float64) int {
	// Math.round: half-up toward +Infinity.
	if value >= 0 {
		return int(value + 0.5)
	}
	return int(value - 0.5)
}

func formatSampleRate(rate float64) string {
	// Node interpolates the raw number (e.g. 0.1 → "0.1").
	return strconv.FormatFloat(rate, 'g', -1, 64)
}

func msToIso(ms int64) string {
	return time.UnixMilli(ms).UTC().Format(timeRFC3339Millis)
}

func contextWithBackground() Ctx {
	return context.Background()
}
