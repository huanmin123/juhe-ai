package gatewayusage

import (
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// gatewayprotoParsedUsage is the shared usage vocabulary (G-C wave).
type gatewayprotoParsedUsage = gatewayproto.ParsedUsage

// gatewayUsageFinalizationTaskMaxBytes mirrors
// gatewayUsageFinalizationMaxBytes (failure-finalization.service.ts).
const gatewayUsageFinalizationTaskMaxBytes = 64 * 1024 * 1024

// gatewayUsageFinalizationDispatchEstimate mirrors the dispatchUsageRecord
// byte-estimate budget: estimateJsonLikeBytes(input, {maxBytes: 2MiB,
// maxNodes: 20000}).
const (
	dispatchEstimateMaxBytes = 2 * 1024 * 1024
	dispatchEstimateMaxNodes = 20000
)

// Service mirrors the record assembly entry points of
// backend/src/modules/gateway/usage/records.ts. Request-derived values Node
// reads from the Express request (model, stream, source endpoint family)
// are supplied by the caller so this package stays HTTP-boundary-free.

// AccountUsageModelAccounting mirrors AccountUsageModelAccounting.
type AccountUsageModelAccounting struct {
	UpstreamModel          string
	PricingModel           string
	ModelMappingApplied    bool
	ModelMappingSource     string
	SourceEndpointFamily   string
	UpstreamEndpointFamily string
}

// GatewayUsageContext mirrors GatewayUsageContext (records.ts).
type GatewayUsageContext struct {
	TraceID                  string
	TrafficSource            OpenAIGatewayTrafficSource
	ClientIP                 string
	SystemAccountID          string
	APIKeyID                 string
	GroupID                  string
	Endpoint                 string
	RequestSnapshot          UsageRequestSnapshot
	RequestedServiceTier     UsageServiceTier
	EffectiveServiceTier     UsageServiceTier
	RequestedReasoningEffort UsageReasoningEffort
	EffectiveReasoningEffort UsageReasoningEffort
}

// GatewayFailureUsageContext mirrors GatewayFailureUsageContext (records.ts).
type GatewayFailureUsageContext struct {
	GatewayUsageContext
	ProviderCode                   string
	ProviderProtocolProfileID      string
	ProtocolCode                   string
	ProtocolVersion                string
	GroupOwnerSystemAccountID      string
	GroupAccessType                string
	GroupAuthorizationID           string
	GroupAuthorizationSourceType   string
	GroupAuthorizationSourceTeamID string
}

// GroupUsageAccessMetadata mirrors the consumed GroupUsageAccessMetadata
// fields (openai-account-selector.types.ts) that groupUsageMetadata reads.
type GroupUsageAccessMetadata struct {
	ProviderCode                   string
	GroupOwnerSystemAccountID      string
	GroupAccessType                string
	GroupAuthorizationID           string
	GroupAuthorizationSourceType   string
	GroupAuthorizationSourceTeamID string
}

// GroupUsageMetadataFields mirrors the groupUsageMetadata pick.
type GroupUsageMetadataFields struct {
	ProviderCode                   string
	GroupOwnerSystemAccountID      string
	GroupAccessType                string
	GroupAuthorizationID           string
	GroupAuthorizationSourceType   string
	GroupAuthorizationSourceTeamID string
}

// AccountUsageMetadata mirrors accountUsageMetadata(account): the ten
// usage access fields.
func AccountUsageMetadata(account UsageModelAccount) UsageAccessFields {
	return account.UsageAccess
}

// GroupUsageMetadata mirrors groupUsageMetadata(groupAccess).
func GroupUsageMetadata(groupAccess GroupUsageAccessMetadata) GroupUsageMetadataFields {
	return GroupUsageMetadataFields{
		ProviderCode:                   groupAccess.ProviderCode,
		GroupOwnerSystemAccountID:      groupAccess.GroupOwnerSystemAccountID,
		GroupAccessType:                groupAccess.GroupAccessType,
		GroupAuthorizationID:           groupAccess.GroupAuthorizationID,
		GroupAuthorizationSourceType:   groupAccess.GroupAuthorizationSourceType,
		GroupAuthorizationSourceTeamID: groupAccess.GroupAuthorizationSourceTeamID,
	}
}

// RecordFailedUpstreamAttemptInput mirrors the recordFailedUpstreamAttempt
// input object plus the request-derived facts (model, stream, endpoint
// family).
type RecordFailedUpstreamAttemptInput struct {
	Model                string
	Stream               bool
	SourceEndpointFamily string
	// Attempt facts.
	UpstreamURL  string
	StartedAtMs  int64
	StatusCode   *int
	Headers      map[string]any
	BodyText     string
	ErrorMessage string
	// FailureAttribution overrides the URL-based attribution inference.
	FailureAttribution UsageFailureAttribution
	// InterpretUpstreamSemantics mirrors interpretUpstreamSemantics: nil
	// means true (Node `input.interpretUpstreamSemantics !== false`).
	InterpretUpstreamSemantics *bool
	// ErrorPayload preempts protocol error parsing when non-nil.
	ErrorPayload any
}

// RecordCompletedUpstreamAttemptInput mirrors the
// recordCompletedUpstreamAttempt input object.
type RecordCompletedUpstreamAttemptInput struct {
	TraceID                 string
	TrafficSource           OpenAIGatewayTrafficSource
	ClientIP                string
	SystemAccountID         string
	APIKeyID                string
	GroupID                 string
	Account                 UsageModelAccount
	Endpoint                string
	StatusCode              *int
	Success                 bool
	ProtocolValidatedSuccess bool
	AccountAPIKeySuccessAlreadyRecorded bool
	Stream                  bool
	FirstTokenMs            *int
	StartedAtMs             int64
	CompletedAtMs           int64
	Model                   string
	SourceEndpointFamily    string
	Usage                   gatewayprotoParsedUsage
	RequestedServiceTier    UsageServiceTier
	EffectiveServiceTier    UsageServiceTier
	RequestedReasoningEffort UsageReasoningEffort
	EffectiveReasoningEffort UsageReasoningEffort
	ErrorCode               string
	ErrorMessage            string
	FailureAttribution      UsageFailureAttribution
	RequestSnapshot         *UsageRequestSnapshot
	ResponseSnapshot        *UsageResponseSnapshot
}

// RecordHybridScoringAttemptInput mirrors the recordHybridScoringAttempt
// input object.
type RecordHybridScoringAttemptInput struct {
	TraceID           string
	ClientIP          string
	SystemAccountID   string
	APIKeyID          string
	GroupID           string
	Account           UsageModelAccount
	Endpoint          string
	StatusCode        *int
	Success           bool
	StartedAtMs       int64
	ScoringModel      string
	Usage             gatewayprotoParsedUsage
	ErrorCode         string
	ErrorMessage      string
	FailureAttribution UsageFailureAttribution
	RequestSnapshot   any
	ResponseSnapshot  any
	TrafficSource     OpenAIGatewayTrafficSource
}

// RecordGatewayFailureInput mirrors the recordGatewayFailure input object
// plus the request-derived facts (model, stream).
type RecordGatewayFailureInput struct {
	Model            string
	Stream           bool
	StatusCode       int
	StartedAtMs      int64
	CompletedAtMs    int64
	ResponsePayload  any
	ErrorMessage     string
	ErrorCode        string
	FailureAttribution UsageFailureAttribution
	ResponseSnapshot *UsageResponseSnapshot
}

// AccountAPIKeySuccessRecorder ports
// recordGatewayAccountApiKeySuccess (runtime/account-api-key-effects.service.ts):
// fire-and-forget account api key success bookkeeping.
type AccountAPIKeySuccessRecorder interface {
	RecordAccountAPIKeySuccess(account UsageModelAccount, source string, trafficSource OpenAIGatewayTrafficSource)
}

// ProtocolErrorPayloadParser ports parseGatewayProtocolErrorPayload
// (protocols/registry.ts): interpret an upstream error body under the
// account protocol profile.
type ProtocolErrorPayloadParser interface {
	ParseProtocolErrorPayload(account UsageModelAccount, bodyText string, headers map[string]any) any
}

// ServiceConfig mirrors the runtimeConfig facts the Node module reads.
type ServiceConfig struct {
	// SyncPricingAllowed mirrors canUseSynchronousCatalogPricingInGatewayRequest
	// (runtimeConfig.cacheDriver !== 'redis').
	SyncPricingAllowed bool
	// FinalizationMaxItems mirrors runtimeConfig.gateway.usageFinalizationMaxItems.
	FinalizationMaxItems int
	// FinalizationMaxConcurrency mirrors runtimeConfig.concurrency.globalMax.
	FinalizationMaxConcurrency int
}

// Service assembles the usage record builders with their ports.
type Service struct {
	clock    Clock
	logger   Logger
	dispatch *FinalizationDispatch
	models   UsageModelResolver
	semantics UsageSemanticResolver
	defaultProviderCode DefaultUsageProviderCodeResolver
	pricing  PricingCatalog
	metrics  UpstreamFailureMetricRecorder
	accountAPIKeySuccess AccountAPIKeySuccessRecorder
	protocolErrors ProtocolErrorPayloadParser
	config   ServiceConfig
}

// NewService wires the service with the finalization dispatch pipeline
// (which owns the UsageRecorder port delivery). clock defaults to the wall
// clock.
func NewService(dispatch *FinalizationDispatch, config ServiceConfig) *Service {
	service := &Service{
		clock:    SystemClock{},
		config:   config,
		dispatch: dispatch,
	}
	if dispatch != nil {
		dispatch.clock = service.clock
	}
	return service
}

// WithClock injects the clock.
func (s *Service) WithClock(clock Clock) *Service {
	s.clock = clock
	if s.dispatch != nil {
		s.dispatch.clock = clock
	}
	return s
}

// WithLogger injects the logger.
func (s *Service) WithLogger(logger Logger) *Service {
	s.logger = logger
	if s.dispatch != nil {
		s.dispatch.logger = logger
	}
	return s
}

// WithModelResolver injects the usage model resolver.
func (s *Service) WithModelResolver(resolver UsageModelResolver) *Service {
	s.models = resolver
	return s
}

// WithUsageSemantics injects the usage semantic resolver.
func (s *Service) WithUsageSemantics(resolver UsageSemanticResolver) *Service {
	s.semantics = resolver
	return s
}

// WithDefaultProviderCode injects the default provider code resolver.
func (s *Service) WithDefaultProviderCode(resolver DefaultUsageProviderCodeResolver) *Service {
	s.defaultProviderCode = resolver
	return s
}

// WithPricingCatalog injects the pricing catalog.
func (s *Service) WithPricingCatalog(catalog PricingCatalog) *Service {
	s.pricing = catalog
	return s
}

// WithMetrics injects the upstream failure metric recorder.
func (s *Service) WithMetrics(recorder UpstreamFailureMetricRecorder) *Service {
	s.metrics = recorder
	return s
}

// WithAccountAPIKeySuccess injects the account api key success recorder.
func (s *Service) WithAccountAPIKeySuccess(recorder AccountAPIKeySuccessRecorder) *Service {
	s.accountAPIKeySuccess = recorder
	return s
}

// WithProtocolErrorParser injects the protocol error payload parser.
func (s *Service) WithProtocolErrorParser(parser ProtocolErrorPayloadParser) *Service {
	s.protocolErrors = parser
	return s
}

// RecordFailedUpstreamAttempt mirrors recordFailedUpstreamAttempt.
func (s *Service) RecordFailedUpstreamAttempt(ctx Ctx, usageContext GatewayUsageContext, account UsageModelAccount, input RecordFailedUpstreamAttemptInput) error {
	model := input.Model
	catalogSystemAccountID := firstNonEmpty(account.UsageAccess.AccountOwnerSystemAccountID, usageContext.SystemAccountID)
	modelAccounting := s.accountUsageModelAccounting(account, model, catalogSystemAccountID, input.SourceEndpointFamily)
	interpretUpstreamSemantics := input.InterpretUpstreamSemantics == nil || *input.InterpretUpstreamSemantics
	errorPayload := input.ErrorPayload
	if errorPayload == nil && interpretUpstreamSemantics && input.BodyText != "" && input.Headers != nil && s.protocolErrors != nil {
		errorPayload = s.protocolErrors.ParseProtocolErrorPayload(account, input.BodyText, input.Headers)
	}
	errorCode := sanitizeOptionalDiagnosticMessage(firstStringField(errorPayload, "code", "type"))
	errorMessage := rawOptionalDiagnosticMessage(firstNonEmpty(
		input.ErrorMessage,
		stringField(errorPayload, "message"),
		input.BodyText,
	))
	logErrorMessage := BuildGatewayLogErrorMessage(errorMessage)
	var failureObservation *GatewayUpstreamFailureClassification
	if interpretUpstreamSemantics && input.FailureAttribution != FailureAttributionDownstreamClosed {
		phase := FailurePhaseUpstreamRequest
		if input.StatusCode != nil {
			phase = FailurePhaseUpstreamResponse
		}
		observation := ClassifyGatewayUpstreamFailure(GatewayUpstreamFailureClassificationInput{
			Phase:      phase,
			StatusCode: input.StatusCode,
			ErrorCode:  errorCode,
		})
		failureObservation = &observation
	}
	if failureObservation != nil && failureObservation.FailureClass != "" && s.metrics != nil {
		s.metrics.RecordUpstreamFailure(failureObservation.FailureClass, input.StatusCode, failureObservation.MetricReasonClass)
	}

	nowMs := s.clock.Now().UnixMilli()
	fields := orderedLogFields()
	fields.Set("event", "gateway_upstream_attempt_failed")
	fields.Set("upstreamUrl", orUnknown(SanitizeURLCredentialsForLog(input.UpstreamURL)))
	fields.Set("accountId", account.ID)
	fields.Set("accountName", account.Name)
	fields.Set("statusCode", input.StatusCode)
	fields.Set("durationMs", nowMs-input.StartedAtMs)
	fields.Set("errorCode", orNil(errorCode))
	setLogErrorMessageFields(fields, logErrorMessage)
	fields.Set("apiKeyId", orNil(usageContext.APIKeyID))
	fields.Set("groupId", orNil(usageContext.GroupID))
	fields.Set("endpoint", orNil(usageContext.Endpoint))
	if failureObservation != nil {
		fields.Set("failureClass", failureObservation.FailureClass)
		fields.Set("metricReasonClass", failureObservation.MetricReasonClass)
		fields.Set("classificationReason", failureObservation.ClassificationReason)
	}
	logGatewayAttemptFailure(s, usageContext, fields, "网关上游尝试失败")

	return s.dispatchUsageRecord(ctx, UsageRecordInput{
		TraceID:                          usageContext.TraceID,
		TrafficSource:                    usageContext.TrafficSource,
		ClientIP:                         usageContext.ClientIP,
		SystemAccountID:                  usageContext.SystemAccountID,
		APIKeyID:                         usageContext.APIKeyID,
		GroupID:                          usageContext.GroupID,
		AccountID:                        account.ID,
		AccountOwnerSystemAccountID:      account.UsageAccess.AccountOwnerSystemAccountID,
		GroupOwnerSystemAccountID:        account.UsageAccess.GroupOwnerSystemAccountID,
		AccountAccessType:                account.UsageAccess.AccountAccessType,
		GroupAccessType:                  account.UsageAccess.GroupAccessType,
		AccountAuthorizationID:           account.UsageAccess.AccountAuthorizationID,
		AccountAuthorizationSourceType:   account.UsageAccess.AccountAuthorizationSourceType,
		AccountAuthorizationSourceTeamID: account.UsageAccess.AccountAuthorizationSourceTeamID,
		GroupAuthorizationID:             account.UsageAccess.GroupAuthorizationID,
		GroupAuthorizationSourceType:     account.UsageAccess.GroupAuthorizationSourceType,
		GroupAuthorizationSourceTeamID:   account.UsageAccess.GroupAuthorizationSourceTeamID,
		Endpoint:                         usageContext.Endpoint,
		ProviderCode:                     account.ProviderCode,
		ProviderProtocolProfileID:        account.ProviderProtocolProfileID,
		UsageSemantic:                    s.usageSemanticForAccount(account),
		Model:                            model,
		UpstreamModel:                    modelAccounting.UpstreamModel,
		PricingModel:                     modelAccounting.PricingModel,
		ModelMappingApplied:              boolPointer(modelAccounting.ModelMappingApplied),
		ModelMappingSource:               modelAccounting.ModelMappingSource,
		SourceEndpointFamily:             modelAccounting.SourceEndpointFamily,
		UpstreamEndpointFamily:           modelAccounting.UpstreamEndpointFamily,
		Stream:                           boolPointer(input.Stream),
		StatusCode:                       input.StatusCode,
		Success:                          false,
		FailureAttribution:               failedUpstreamAttemptAttribution(input.UpstreamURL, input.FailureAttribution),
		RequestedServiceTier:             usageContext.RequestedServiceTier,
		EffectiveServiceTier:             usageContext.EffectiveServiceTier,
		RequestedReasoningEffort:         usageContext.RequestedReasoningEffort,
		EffectiveReasoningEffort:         usageContext.EffectiveReasoningEffort,
		DurationMs:                       intPointer(int(nowMs - input.StartedAtMs)),
		ErrorCode:                        errorCode,
		ErrorMessage:                     errorMessage,
		RequestSnapshot:                  usageRecordSnapshot(usageContext.TrafficSource, usageContext.RequestSnapshot),
		ResponseSnapshot: usageRecordSnapshot(usageContext.TrafficSource, BuildUsageResponseSnapshot(BuildUsageResponseSnapshotInput{
			UpstreamURL:  input.UpstreamURL,
			StatusCode:   input.StatusCode,
			Headers:      input.Headers,
			BodyText:     input.BodyText,
			ErrorMessage: errorMessage,
		})),
	})
}

// RecordCompletedUpstreamAttempt mirrors recordCompletedUpstreamAttempt.
func (s *Service) RecordCompletedUpstreamAttempt(ctx Ctx, input RecordCompletedUpstreamAttemptInput) error {
	if input.Success && input.ProtocolValidatedSuccess && !input.AccountAPIKeySuccessAlreadyRecorded && s.accountAPIKeySuccess != nil {
		s.accountAPIKeySuccess.RecordAccountAPIKeySuccess(input.Account, "upstream_attempt_completed", input.TrafficSource)
	}
	model := input.Model
	catalogSystemAccountID := firstNonEmpty(input.Account.UsageAccess.AccountOwnerSystemAccountID, input.SystemAccountID)
	modelAccounting := s.accountUsageModelAccounting(input.Account, model, catalogSystemAccountID, input.SourceEndpointFamily)
	costModel := usageCostCatalogModel(modelAccounting, model)
	serviceTiers := ResolveUsageServiceTiers(ResolveUsageServiceTiersInput{
		RequestedServiceTier: input.RequestedServiceTier,
		EffectiveServiceTier: input.EffectiveServiceTier,
		ReportedServiceTier:  input.Usage.ServiceTier,
	})
	completedAtMs := input.CompletedAtMs
	if completedAtMs == 0 {
		completedAtMs = s.clock.Now().UnixMilli()
	}
	durationMs := completedAtMs - input.StartedAtMs
	if durationMs < 0 {
		durationMs = 0
	}
	failureAttribution := ""
	if !input.Success {
		failureAttribution = firstNonEmpty(input.FailureAttribution, FailureAttributionAccountUpstream)
	}
	return s.dispatchUsageRecord(ctx, UsageRecordInput{
		TraceID:                          input.TraceID,
		TrafficSource:                    input.TrafficSource,
		ClientIP:                         input.ClientIP,
		SystemAccountID:                  input.SystemAccountID,
		APIKeyID:                         input.APIKeyID,
		GroupID:                          input.GroupID,
		AccountID:                        input.Account.ID,
		AccountOwnerSystemAccountID:      input.Account.UsageAccess.AccountOwnerSystemAccountID,
		GroupOwnerSystemAccountID:        input.Account.UsageAccess.GroupOwnerSystemAccountID,
		AccountAccessType:                input.Account.UsageAccess.AccountAccessType,
		GroupAccessType:                  input.Account.UsageAccess.GroupAccessType,
		AccountAuthorizationID:           input.Account.UsageAccess.AccountAuthorizationID,
		AccountAuthorizationSourceType:   input.Account.UsageAccess.AccountAuthorizationSourceType,
		AccountAuthorizationSourceTeamID: input.Account.UsageAccess.AccountAuthorizationSourceTeamID,
		GroupAuthorizationID:             input.Account.UsageAccess.GroupAuthorizationID,
		GroupAuthorizationSourceType:     input.Account.UsageAccess.GroupAuthorizationSourceType,
		GroupAuthorizationSourceTeamID:   input.Account.UsageAccess.GroupAuthorizationSourceTeamID,
		Endpoint:                         input.Endpoint,
		ProviderCode:                     input.Account.ProviderCode,
		ProviderProtocolProfileID:        input.Account.ProviderProtocolProfileID,
		UsageSemantic:                    s.usageSemanticForAccount(input.Account),
		Model:                            model,
		UpstreamModel:                    modelAccounting.UpstreamModel,
		UpstreamResponseModel:            input.Usage.UpstreamResponseModel,
		PricingModel:                     modelAccounting.PricingModel,
		ModelMappingApplied:              boolPointer(modelAccounting.ModelMappingApplied),
		ModelMappingSource:               modelAccounting.ModelMappingSource,
		SourceEndpointFamily:             modelAccounting.SourceEndpointFamily,
		UpstreamEndpointFamily:           modelAccounting.UpstreamEndpointFamily,
		Stream:                           boolPointer(input.Stream),
		StatusCode:                       input.StatusCode,
		Success:                          input.Success,
		FailureAttribution:               failureAttribution,
		FirstTokenMs:                     input.FirstTokenMs,
		DurationMs:                       intPointer(int(durationMs)),
		InputTokens:                      input.Usage.InputTokens,
		OutputTokens:                     input.Usage.OutputTokens,
		CacheReadTokens:                  input.Usage.CacheReadTokens,
		CacheWriteTokens:                 input.Usage.CacheWriteTokens,
		CacheWrite1hTokens:               input.Usage.CacheWrite1hTokens,
		ThinkingTokens:                   input.Usage.ThinkingTokens,
		RequestedServiceTier:             serviceTiers.RequestedServiceTier,
		EffectiveServiceTier:             serviceTiers.EffectiveServiceTier,
		ReportedServiceTier:              serviceTiers.ReportedServiceTier,
		BilledServiceTier:                serviceTiers.BilledServiceTier,
		RequestedReasoningEffort:         input.RequestedReasoningEffort,
		EffectiveReasoningEffort:         input.EffectiveReasoningEffort,
		InputImageTokens:                 input.Usage.InputImageTokens,
		OutputImageTokens:                input.Usage.OutputImageTokens,
		InputAudioTokens:                 input.Usage.InputAudioTokens,
		OutputAudioTokens:                input.Usage.OutputAudioTokens,
		OutputImageCount:                 input.Usage.OutputImageCount,
		CacheReadCostUsd:                 s.estimateCacheReadCost(catalogSystemAccountID, input.Account.ProviderCode, costModel, serviceTiers.BilledServiceTier, input.Usage.CacheReadTokens),
		CacheWriteCostUsd:                s.estimateCacheWriteCost(catalogSystemAccountID, input.Account.ProviderCode, costModel, serviceTiers.BilledServiceTier, input.Usage.CacheWriteTokens, input.Usage.CacheWrite1hTokens),
		CostUsd:                          s.estimateCost(catalogSystemAccountID, input.Account.ProviderCode, costModel, serviceTiers.BilledServiceTier, input.Usage),
		ErrorCode:                        input.ErrorCode,
		ErrorMessage:                     input.ErrorMessage,
		RequestSnapshot:                  usageRecordSnapshot(input.TrafficSource, snapshotOrNil(input.RequestSnapshot)),
		ResponseSnapshot:                 usageRecordSnapshot(input.TrafficSource, responseSnapshotOrNil(input.ResponseSnapshot)),
	})
}

// RecordHybridScoringAttempt mirrors recordHybridScoringAttempt.
func (s *Service) RecordHybridScoringAttempt(ctx Ctx, input RecordHybridScoringAttemptInput) error {
	trafficSource := input.TrafficSource
	if trafficSource == "" {
		trafficSource = TrafficSourceHybridScoring
	}
	catalogSystemAccountID := firstNonEmpty(input.Account.UsageAccess.AccountOwnerSystemAccountID, input.SystemAccountID)
	modelAccounting := s.accountUsageModelAccounting(input.Account, input.ScoringModel, catalogSystemAccountID, "chat_completions")
	costModel := usageCostCatalogModel(modelAccounting, input.ScoringModel)
	serviceTiers := ResolveUsageServiceTiers(ResolveUsageServiceTiersInput{
		ReportedServiceTier: input.Usage.ServiceTier,
	})
	durationMs := s.clock.Now().UnixMilli() - input.StartedAtMs
	failureAttribution := ""
	if !input.Success {
		failureAttribution = firstNonEmpty(input.FailureAttribution, FailureAttributionAccountUpstream)
	}
	return s.dispatchUsageRecord(ctx, UsageRecordInput{
		TraceID:                          input.TraceID,
		TrafficSource:                    trafficSource,
		ClientIP:                         input.ClientIP,
		SystemAccountID:                  input.SystemAccountID,
		APIKeyID:                         input.APIKeyID,
		GroupID:                          input.GroupID,
		AccountID:                        input.Account.ID,
		AccountOwnerSystemAccountID:      input.Account.UsageAccess.AccountOwnerSystemAccountID,
		GroupOwnerSystemAccountID:        input.Account.UsageAccess.GroupOwnerSystemAccountID,
		AccountAccessType:                input.Account.UsageAccess.AccountAccessType,
		GroupAccessType:                  input.Account.UsageAccess.GroupAccessType,
		AccountAuthorizationID:           input.Account.UsageAccess.AccountAuthorizationID,
		AccountAuthorizationSourceType:   input.Account.UsageAccess.AccountAuthorizationSourceType,
		AccountAuthorizationSourceTeamID: input.Account.UsageAccess.AccountAuthorizationSourceTeamID,
		GroupAuthorizationID:             input.Account.UsageAccess.GroupAuthorizationID,
		GroupAuthorizationSourceType:     input.Account.UsageAccess.GroupAuthorizationSourceType,
		GroupAuthorizationSourceTeamID:   input.Account.UsageAccess.GroupAuthorizationSourceTeamID,
		Endpoint:                         input.Endpoint,
		ProviderCode:                     input.Account.ProviderCode,
		ProviderProtocolProfileID:        input.Account.ProviderProtocolProfileID,
		UsageSemantic:                    s.usageSemanticForAccount(input.Account),
		Model:                            input.ScoringModel,
		UpstreamModel:                    modelAccounting.UpstreamModel,
		PricingModel:                     modelAccounting.PricingModel,
		ModelMappingApplied:              boolPointer(modelAccounting.ModelMappingApplied),
		ModelMappingSource:               modelAccounting.ModelMappingSource,
		SourceEndpointFamily:             modelAccounting.SourceEndpointFamily,
		UpstreamEndpointFamily:           modelAccounting.UpstreamEndpointFamily,
		Stream:                           boolPointer(false),
		StatusCode:                       input.StatusCode,
		Success:                          input.Success,
		FailureAttribution:               failureAttribution,
		DurationMs:                       intPointer(int(durationMs)),
		InputTokens:                      input.Usage.InputTokens,
		OutputTokens:                     input.Usage.OutputTokens,
		CacheReadTokens:                  input.Usage.CacheReadTokens,
		CacheWriteTokens:                 input.Usage.CacheWriteTokens,
		CacheWrite1hTokens:               input.Usage.CacheWrite1hTokens,
		ThinkingTokens:                   input.Usage.ThinkingTokens,
		RequestedServiceTier:             serviceTiers.RequestedServiceTier,
		EffectiveServiceTier:             serviceTiers.EffectiveServiceTier,
		ReportedServiceTier:              serviceTiers.ReportedServiceTier,
		BilledServiceTier:                serviceTiers.BilledServiceTier,
		InputImageTokens:                 input.Usage.InputImageTokens,
		OutputImageTokens:                input.Usage.OutputImageTokens,
		InputAudioTokens:                 input.Usage.InputAudioTokens,
		OutputAudioTokens:                input.Usage.OutputAudioTokens,
		OutputImageCount:                 input.Usage.OutputImageCount,
		CacheReadCostUsd:                 s.estimateCacheReadCost(catalogSystemAccountID, input.Account.ProviderCode, costModel, serviceTiers.BilledServiceTier, input.Usage.CacheReadTokens),
		CacheWriteCostUsd:                s.estimateCacheWriteCost(catalogSystemAccountID, input.Account.ProviderCode, costModel, serviceTiers.BilledServiceTier, input.Usage.CacheWriteTokens, input.Usage.CacheWrite1hTokens),
		CostUsd:                          s.estimateCost(catalogSystemAccountID, input.Account.ProviderCode, costModel, serviceTiers.BilledServiceTier, input.Usage),
		ErrorCode:                        input.ErrorCode,
		ErrorMessage:                     input.ErrorMessage,
		RequestSnapshot:                  input.RequestSnapshot,
		ResponseSnapshot:                 input.ResponseSnapshot,
	})
}

// RecordDownstreamClosedUpstreamAttempt mirrors
// recordDownstreamClosedUpstreamAttempt: delegate to the completed-attempt
// recorder with the fixed downstream failure contract.
func (s *Service) RecordDownstreamClosedUpstreamAttempt(ctx Ctx, input RecordCompletedUpstreamAttemptInput) error {
	input.Success = false
	input.Usage = gatewayprotoParsedUsage{}
	input.ErrorCode = "downstream_connection_closed"
	input.ErrorMessage = DownstreamConnectionClosedMessage
	input.FailureAttribution = FailureAttributionDownstreamClosed
	return s.RecordCompletedUpstreamAttempt(ctx, input)
}

// RecordGatewayFailure mirrors recordGatewayFailure.
func (s *Service) RecordGatewayFailure(ctx Ctx, usageContext GatewayFailureUsageContext, input RecordGatewayFailureInput) error {
	completedAtMs := input.CompletedAtMs
	if completedAtMs == 0 {
		completedAtMs = s.clock.Now().UnixMilli()
	}
	// Node: errorMessage = input.errorMessage ?? responsePayload.error.message;
	// errorCode = input.errorCode ?? error.code ?? error.type.
	errorMessage := firstNonEmpty(input.ErrorMessage, gatewayErrorPayloadField(input.ResponsePayload, "message"))
	errorCode := firstNonEmpty(
		input.ErrorCode,
		gatewayErrorPayloadField(input.ResponsePayload, "code"),
		gatewayErrorPayloadField(input.ResponsePayload, "type"),
	)
	logErrorMessage := BuildGatewayLogErrorMessage(errorMessage)
	durationMs := completedAtMs - input.StartedAtMs
	if durationMs < 0 {
		durationMs = 0
	}
	fields := orderedLogFields()
	fields.Set("event", "gateway_request_failed")
	fields.Set("statusCode", input.StatusCode)
	fields.Set("durationMs", durationMs)
	setLogErrorMessageFields(fields, logErrorMessage)
	fields.Set("errorCode", orNil(errorCode))
	fields.Set("apiKeyId", orNil(usageContext.APIKeyID))
	fields.Set("groupId", orNil(usageContext.GroupID))
	fields.Set("endpoint", orNil(usageContext.Endpoint))
	s.logWarnProbe(usageContext.TrafficSource, fields, "网关请求失败")

	providerCode := usageContext.ProviderCode
	if providerCode == "" && s.defaultProviderCode != nil {
		providerCode = s.defaultProviderCode.DefaultUsageProviderCode()
	}
	providerProtocolProfileID := usageContext.ProviderProtocolProfileID
	hasResolvedGroupUsageMetadata := usageContext.GroupID == "" ||
		(usageContext.GroupOwnerSystemAccountID != "" && usageContext.GroupAccessType != "")
	if usageContext.GroupID != "" && !hasResolvedGroupUsageMetadata && s.logger != nil {
		s.logger.Warn("网关失败 usage 缺少分组归属快照，已省略分组统计维度", map[string]any{
			"event":    "gateway_failure_usage_group_scope_omitted",
			"traceId":  usageContext.TraceID,
			"groupId":  usageContext.GroupID,
			"endpoint": usageContext.Endpoint,
		})
	}

	groupID := usageContext.GroupID
	groupOwnerSystemAccountID := usageContext.GroupOwnerSystemAccountID
	groupAccessType := usageContext.GroupAccessType
	groupAuthorizationID := usageContext.GroupAuthorizationID
	groupAuthorizationSourceType := usageContext.GroupAuthorizationSourceType
	groupAuthorizationSourceTeamID := usageContext.GroupAuthorizationSourceTeamID
	if !hasResolvedGroupUsageMetadata {
		groupID = ""
		groupOwnerSystemAccountID = ""
		groupAccessType = ""
		groupAuthorizationID = ""
		groupAuthorizationSourceType = ""
		groupAuthorizationSourceTeamID = ""
	}
	profile := &ProviderProtocolProfile{
		ProviderCode:    providerCode,
		ProtocolCode:    usageContext.ProtocolCode,
		ProtocolVersion: usageContext.ProtocolVersion,
		ProfileID:       providerProtocolProfileID,
	}
	statusCode := input.StatusCode
	var responseSnapshotValue any
	if input.ResponseSnapshot != nil {
		responseSnapshotValue = *input.ResponseSnapshot
	} else {
		responseSnapshotValue = BuildGatewayErrorResponseSnapshot(input.StatusCode, input.ResponsePayload, nil)
	}
	return s.dispatchUsageRecord(ctx, UsageRecordInput{
		TraceID:                        usageContext.TraceID,
		TrafficSource:                  usageContext.TrafficSource,
		ClientIP:                       usageContext.ClientIP,
		SystemAccountID:                usageContext.SystemAccountID,
		APIKeyID:                       usageContext.APIKeyID,
		GroupID:                        groupID,
		GroupOwnerSystemAccountID:      groupOwnerSystemAccountID,
		GroupAccessType:                groupAccessType,
		GroupAuthorizationID:           groupAuthorizationID,
		GroupAuthorizationSourceType:   groupAuthorizationSourceType,
		GroupAuthorizationSourceTeamID: groupAuthorizationSourceTeamID,
		Endpoint:                       usageContext.Endpoint,
		ProviderCode:                   providerCode,
		ProviderProtocolProfileID:      providerProtocolProfileID,
		UsageSemantic:                  s.usageSemanticForProfile(profile),
		Model:                          input.Model,
		Stream:                         boolPointer(input.Stream),
		StatusCode:                     &statusCode,
		Success:                        false,
		FailureAttribution:             firstNonEmpty(input.FailureAttribution, FailureAttributionGatewayPolicy),
		RequestedServiceTier:           usageContext.RequestedServiceTier,
		EffectiveServiceTier:           usageContext.EffectiveServiceTier,
		RequestedReasoningEffort:       usageContext.RequestedReasoningEffort,
		EffectiveReasoningEffort:       usageContext.EffectiveReasoningEffort,
		DurationMs:                     intPointer(int(durationMs)),
		ErrorCode:                      errorCode,
		ErrorMessage:                   errorMessage,
		RequestSnapshot:                usageRecordSnapshot(usageContext.TrafficSource, usageContext.RequestSnapshot),
		ResponseSnapshot:               usageRecordSnapshot(usageContext.TrafficSource, responseSnapshotValue),
		CreatedAt:                      time.UnixMilli(completedAtMs).UTC().Format(timeRFC3339Millis),
	})
}

// dispatchUsageRecord mirrors dispatchUsageRecord (records.ts): estimate the
// input bytes against the finalization budget and hand delivery to the
// finalization dispatch pipeline.
func (s *Service) dispatchUsageRecord(ctx Ctx, input UsageRecordInput) error {
	if s.dispatch == nil {
		return nil
	}
	return s.dispatch.DispatchUsageRecord(ctx, input, EstimateJSONLikeBytes(input, EstimateJSONLikeBytesOptions{
		MaxBytes: dispatchEstimateMaxBytes,
		MaxNodes: dispatchEstimateMaxNodes,
	}))
}

// accountUsageModelAccounting mirrors accountUsageModelAccounting.
func (s *Service) accountUsageModelAccounting(account UsageModelAccount, requestedModel string, catalogSystemAccountID string, sourceEndpointFamily string) AccountUsageModelAccounting {
	resolved := UsageModelResolution{UpstreamModel: requestedModel}
	if s.models != nil {
		resolved = s.models.ResolveUsageModel(account, requestedModel, sourceEndpointFamily)
	}
	upstreamModel := resolved.UpstreamModel
	if upstreamModel == "" {
		upstreamModel = requestedModel
	}
	return AccountUsageModelAccounting{
		UpstreamModel:          upstreamModel,
		PricingModel:           s.resolveUsagePricingModel(account, catalogSystemAccountID, upstreamModel),
		ModelMappingApplied:    resolved.ModelMappingApplied,
		ModelMappingSource:     resolved.ModelMappingSource,
		SourceEndpointFamily:   resolved.SourceEndpointFamily,
		UpstreamEndpointFamily: resolved.UpstreamEndpointFamily,
	}
}

// resolveUsagePricingModel mirrors resolveUsagePricingModel with the
// synchronous-catalog gate.
func (s *Service) resolveUsagePricingModel(account UsageModelAccount, catalogSystemAccountID string, upstreamModel string) string {
	if !s.canUseSyncPricing() {
		return ""
	}
	return s.pricing.ResolvePricingModel(account.ProviderCode, catalogSystemAccountID, upstreamModel)
}

func (s *Service) estimateCacheReadCost(catalogSystemAccountID string, providerCode string, costModel string, serviceTier string, cacheReadTokens *int) *float64 {
	if !s.canUseSyncPricing() {
		return nil
	}
	return s.pricing.EstimateCacheReadCost(PricingCostInput{
		ProviderCode:    providerCode,
		SystemAccountID: catalogSystemAccountID,
		Model:           costModel,
		ServiceTier:     serviceTier,
		CacheReadTokens: cacheReadTokens,
	})
}

func (s *Service) estimateCacheWriteCost(catalogSystemAccountID string, providerCode string, costModel string, serviceTier string, cacheWriteTokens *int, cacheWrite1hTokens *int) *float64 {
	if !s.canUseSyncPricing() {
		return nil
	}
	return s.pricing.EstimateCacheWriteCost(PricingCostInput{
		ProviderCode:       providerCode,
		SystemAccountID:    catalogSystemAccountID,
		Model:              costModel,
		ServiceTier:        serviceTier,
		CacheWriteTokens:   cacheWriteTokens,
		CacheWrite1hTokens: cacheWrite1hTokens,
	})
}

func (s *Service) estimateCost(catalogSystemAccountID string, providerCode string, costModel string, serviceTier string, usage gatewayprotoParsedUsage) *float64 {
	if !s.canUseSyncPricing() {
		return nil
	}
	return s.pricing.EstimateCost(PricingCostInput{
		ProviderCode:       providerCode,
		SystemAccountID:    catalogSystemAccountID,
		Model:              costModel,
		ServiceTier:        serviceTier,
		InputTokens:        usage.InputTokens,
		OutputTokens:       usage.OutputTokens,
		CacheReadTokens:    usage.CacheReadTokens,
		CacheWriteTokens:   usage.CacheWriteTokens,
		CacheWrite1hTokens: usage.CacheWrite1hTokens,
		ThinkingTokens:     usage.ThinkingTokens,
		InputImageTokens:   usage.InputImageTokens,
		OutputImageTokens:  usage.OutputImageTokens,
		InputAudioTokens:   usage.InputAudioTokens,
		OutputAudioTokens:  usage.OutputAudioTokens,
		OutputImageCount:   usage.OutputImageCount,
	})
}

func (s *Service) canUseSyncPricing() bool {
	return s.config.SyncPricingAllowed && s.pricing != nil
}

// usageCostCatalogModel mirrors usageCostCatalogModel.
func usageCostCatalogModel(modelAccounting AccountUsageModelAccounting, requestedModel string) string {
	return firstNonEmpty(modelAccounting.PricingModel, modelAccounting.UpstreamModel, requestedModel)
}

// usageRecordSnapshot mirrors usageRecordSnapshot: account probe traffic
// sources drop the snapshots entirely.
func usageRecordSnapshot(trafficSource OpenAIGatewayTrafficSource, snapshot any) any {
	if IsAccountProbeTrafficSource(trafficSource) {
		return nil
	}
	return snapshot
}

func snapshotOrNil(snapshot *UsageRequestSnapshot) any {
	if snapshot == nil {
		return nil
	}
	return *snapshot
}

func responseSnapshotOrNil(snapshot *UsageResponseSnapshot) any {
	if snapshot == nil {
		return nil
	}
	return *snapshot
}

// failedUpstreamAttemptAttribution mirrors failedUpstreamAttemptAttribution.
func failedUpstreamAttemptAttribution(upstreamURL string, failureAttribution UsageFailureAttribution) UsageFailureAttribution {
	if failureAttribution != "" {
		return failureAttribution
	}
	if upstreamURL == "concurrency:limit" {
		return FailureAttributionGatewayCapacity
	}
	if strings.HasPrefix(upstreamURL, "proxy:") ||
		upstreamURL == "account:preparation" ||
		upstreamURL == "openai-oauth-codex:local-validation" ||
		upstreamURL == "gateway:local-validation" {
		return FailureAttributionAccountDependency
	}
	if strings.HasPrefix(upstreamURL, "gateway:") || strings.HasPrefix(upstreamURL, "account:") {
		return FailureAttributionGatewayPolicy
	}
	return FailureAttributionAccountUpstream
}

// sanitizeOptionalDiagnosticMessage mirrors
// sanitizeOptionalDiagnosticMessage: trim, cap at 4000 chars, sanitize, then
// cap at 1000.
func sanitizeOptionalDiagnosticMessage(value string) string {
	text := strings.TrimSpace(value)
	if text == "" {
		return ""
	}
	if len(text) > 4000 {
		text = sliceStringByUTF8Bytes(text, 4000)
	}
	sanitized := SanitizeDiagnosticString(text)
	if len(sanitized) > 1000 {
		sanitized = sliceStringByUTF8Bytes(sanitized, 1000)
	}
	return sanitized
}

// rawOptionalDiagnosticMessage mirrors rawOptionalDiagnosticMessage.
func rawOptionalDiagnosticMessage(value string) string {
	if len(value) == 0 {
		return ""
	}
	return value
}

// logGatewayAttemptFailure mirrors logGatewayAttemptFailure: probe sources
// log at debug, everything else at warn, with trafficSource enrichment.
func logGatewayAttemptFailure(s *Service, usageContext GatewayUsageContext, fields *OrderedObject, message string) {
	if s.logger == nil {
		return
	}
	enriched := fields.Clone().AsMap()
	enriched["trafficSource"] = usageContext.TrafficSource
	if IsAccountProbeTrafficSource(usageContext.TrafficSource) {
		s.logger.Debug(message, enriched)
		return
	}
	s.logger.Warn(message, enriched)
}

// logWarnProbe mirrors the probe-aware warn used by recordGatewayFailure.
func (s *Service) logWarnProbe(trafficSource OpenAIGatewayTrafficSource, fields *OrderedObject, message string) {
	if s.logger == nil {
		return
	}
	enriched := fields.Clone().AsMap()
	enriched["trafficSource"] = trafficSource
	if IsAccountProbeTrafficSource(trafficSource) {
		s.logger.Debug(message, enriched)
		return
	}
	s.logger.Warn(message, enriched)
}

func (s *Service) usageSemanticForAccount(account UsageModelAccount) string {
	if s.semantics != nil {
		return s.semantics.UsageSemanticForProfile(account.Profile)
	}
	return ""
}

func (s *Service) usageSemanticForProfile(profile *ProviderProtocolProfile) string {
	if s.semantics != nil {
		return s.semantics.UsageSemanticForProfile(profile)
	}
	return ""
}

// orderedLogFields builds an insertion-ordered log field set mirroring the
// Node log object key order.
func orderedLogFields() *OrderedObject {
	return NewOrderedObject()
}

// setLogErrorMessageFields mirrors ...logErrorMessage spread order.
func setLogErrorMessageFields(fields *OrderedObject, message GatewayLogErrorMessage) {
	fields.Set("errorMessage", orNil(message.ErrorMessage))
	fields.Set("errorMessageBytes", message.ErrorMessageBytes)
	fields.Set("errorMessageTruncated", message.ErrorMessageTruncated)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func boolPointer(value bool) *bool { return &value }

func intPointer(value int) *int { return &value }

func orNil(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func orUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

// stringField reads a string field from a JSON-like payload
// (typeof check semantics).
func stringField(payload any, field string) string {
	value := jsonRecordField(payload, field)
	text, _ := value.(string)
	return text
}

// gatewayErrorPayloadField reads payload.error.<field> from the
// GatewayErrorPayload (response/responses.ts).
func gatewayErrorPayloadField(payload any, field string) string {
	errorObject := jsonRecordField(payload, "error")
	if text, ok := jsonRecordField(errorObject, field).(string); ok {
		return text
	}
	return ""
}

// firstStringField mirrors
// typeof a === 'string' ? a : typeof b === 'string' ? b : undefined.
func firstStringField(payload any, fields ...string) string {
	for _, field := range fields {
		if text, ok := jsonRecordField(payload, field).(string); ok {
			return text
		}
	}
	return ""
}
