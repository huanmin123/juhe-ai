package gatewaypreauth

import (
	"context"
	"errors"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Inline helpers of request/preflight.ts, ported one to one. Pure decision
// logic (usage context, route plan snapshot, speed-first configs, image lane
// probing, recoverable candidate waiting) stays in this package so the
// orchestration contract is testable end to end.

// usageContextInput mirrors buildGatewayUsageContext's input.
type usageContextInput struct {
	traceID          string
	clientIP         string
	identity         OpenAIGatewayRequestIdentity
	trafficSource    string
	groupUsageFields *GroupUsageMetadataFields
	endpoint         string
	requestSnapshot  UsageRequestSnapshot
}

// BuildGatewayUsageContext mirrors buildGatewayUsageContext.
func (s *Service) BuildGatewayUsageContext(input usageContextInput) GatewayFailureUsageContext {
	context := GatewayFailureUsageContext{
		TraceID:                  input.traceID,
		TrafficSource:            input.trafficSource,
		ClientIP:                 input.clientIP,
		SystemAccountID:          input.identity.SystemAccountID,
		APIKeyID:                 input.identity.APIKeyID,
		GroupID:                  input.identity.GroupID,
		Endpoint:                 input.endpoint,
		RequestSnapshot:          input.requestSnapshot,
		RequestedServiceTier:     input.requestSnapshot.RequestedServiceTier,
		EffectiveServiceTier:     input.requestSnapshot.RequestedServiceTier,
		RequestedReasoningEffort: input.requestSnapshot.RequestedReasoningEffort,
		EffectiveReasoningEffort: input.requestSnapshot.RequestedReasoningEffort,
	}
	if context.RequestedServiceTier == "" {
		context.RequestedServiceTier = "default"
		context.EffectiveServiceTier = "default"
	}
	if input.groupUsageFields != nil {
		context.ProviderCode = input.groupUsageFields.ProviderCode
		context.GroupOwnerSystemAccountID = input.groupUsageFields.GroupOwnerSystemAccountID
		context.GroupAccessType = input.groupUsageFields.GroupAccessType
		context.GroupAuthorizationID = input.groupUsageFields.GroupAuthorizationID
		context.GroupAuthorizationSourceType = input.groupUsageFields.GroupAuthorizationSourceType
		context.GroupAuthorizationSourceTeamID = input.groupUsageFields.GroupAuthorizationSourceTeamID
	}
	return context
}

// routePlanInput mirrors createOpenAIGatewayRoutePlanSnapshot's input.
type routePlanInput struct {
	traceID                    string
	startedAt                  int64
	groupId                    string
	apiKeyRecord               *gatewayruntimecache.GatewayAPIKeyRow
	gatewayRequestWallBudget   *gatewayrouting.GatewayRequestWallBudget
	normalRouteFirstByteConfig *NormalRouteFirstByteRuntimeConfig
	hybridRoute                *HybridRuntimeRoute
}

// createOpenAIGatewayRoutePlanSnapshot mirrors createOpenAIGatewayRoutePlanSnapshot.
func (s *Service) createOpenAIGatewayRoutePlanSnapshot(input routePlanInput) (gatewayrouting.RoutePlanSnapshot[string], error) {
	orderedAllowedTargets := uniqueActiveRouteGroupIds(input.apiKeyRecord)
	if !containsString(orderedAllowedTargets, input.groupId) {
		orderedAllowedTargets = append([]string{input.groupId}, orderedAllowedTargets...)
	}
	cursor := indexOfString(orderedAllowedTargets, input.groupId)
	if cursor < 0 {
		cursor = 0
	}
	var weightedDecisionToken string
	var hybridScoreDecision any
	if input.apiKeyRecord != nil && input.apiKeyRecord.RouteStrategyMode == gatewayruntimecache.RouteStrategyModeWeighted {
		if binding, ok := bindingForGroup(*input.apiKeyRecord, input.groupId); ok {
			weightedDecisionToken = binding.ID
		} else {
			weightedDecisionToken = input.groupId
		}
	}
	if input.hybridRoute != nil {
		hybridScoreDecision = map[string]any{
			"level":            hybridRouteField(input.hybridRoute.Scoring, "level"),
			"targetModel":      input.hybridRoute.TargetModel,
			"minLevel":         hybridRouteField(input.hybridRoute.Route, "minLevel"),
			"maxLevel":         hybridRouteField(input.hybridRoute.Route, "maxLevel"),
			"scoringDefaulted": hybridRouteBool(input.hybridRoute.Scoring, "defaulted"),
		}
	}
	var wallBudgetMs *int64
	if input.gatewayRequestWallBudget != nil {
		wallBudgetMs = int64Ptr(input.gatewayRequestWallBudget.BudgetMs)
	}
	var precommitDeadlineAtMs *int64
	if input.gatewayRequestWallBudget != nil {
		precommitDeadlineAtMs = int64Ptr(input.gatewayRequestWallBudget.DeadlineAtMs)
	}
	mode := gatewayruntimecache.RouteStrategyModeNormal
	if input.apiKeyRecord != nil {
		mode = input.apiKeyRecord.RouteStrategyMode
	}
	var firstByteDeadlineMs *int64
	if input.normalRouteFirstByteConfig != nil && input.normalRouteFirstByteConfig.FirstByteDeadlineMs != nil {
		firstByteDeadlineMs = int64Ptr(*input.normalRouteFirstByteConfig.FirstByteDeadlineMs)
	}
	return gatewayrouting.CreateGatewayRoutePlanSnapshot(gatewayrouting.CreateGatewayRoutePlanSnapshotInput[string]{
		RoutePlanID:                  input.traceID + ":route",
		Mode:                         mode,
		RequestAcceptedAtMs:          input.startedAt,
		GatewayRequestWallBudgetMs:   wallBudgetMs,
		FirstByteDeadlineMs:          firstByteDeadlineMs,
		RequestPrecommitDeadlineAtMs: precommitDeadlineAtMs,
		OrderedAllowedTargets:        orderedAllowedTargets,
		Cursor:                       &cursor,
		WeightedDecisionToken:        weightedDecisionToken,
		HybridScoreDecision:          hybridScoreDecision,
	})
}

func hybridRouteField(source map[string]any, key string) any {
	if source == nil {
		return nil
	}
	return source[key]
}

func hybridRouteBool(source map[string]any, key string) bool {
	if value, ok := hybridRouteField(source, key).(bool); ok {
		return value
	}
	return false
}

func bindingForGroup(apiKey gatewayruntimecache.GatewayAPIKeyRow, groupID string) (gatewayruntimecache.GatewayAPIKeyGroupBindingRow, bool) {
	for _, binding := range apiKey.GroupBindings {
		if binding.GroupID == groupID {
			return binding, true
		}
	}
	return gatewayruntimecache.GatewayAPIKeyGroupBindingRow{}, false
}

// canAttemptAPIKeyGroupFallback mirrors canAttemptApiKeyGroupFallback.
func canAttemptAPIKeyGroupFallback(apiKeyRecord *gatewayruntimecache.GatewayAPIKeyRow, groupID string, routePlanSnapshot *gatewayrouting.RoutePlanSnapshot[string]) bool {
	if routePlanSnapshot != nil {
		return routePlanSnapshot.Cursor < len(routePlanSnapshot.OrderedAllowedTargets)-1
	}
	bindings := recordBindings(apiKeyRecord)
	if len(bindings) <= 1 {
		return false
	}
	currentIndex := -1
	for i, binding := range bindings {
		if binding.GroupID == groupID {
			currentIndex = i
			break
		}
	}
	return currentIndex >= 0 && currentIndex < len(bindings)-1
}

// normalRouteFirstByteConfigForAPIKey mirrors normalRouteFirstByteConfigForApiKey
// with the lane applicability gate.
func (s *Service) normalRouteFirstByteConfigForAPIKey(apiKeyRecord *gatewayruntimecache.GatewayAPIKeyRow, lane gatewayProtoLane, compactionTimeoutsDisabled bool, override *NormalRouteFirstByteRuntimeConfig) *NormalRouteFirstByteRuntimeConfig {
	if compactionTimeoutsDisabled || !gatewayrouting.NormalRouteFirstByteDeadlineAppliesToLane(lane) {
		return nil
	}
	if override != nil {
		return override
	}
	config := normalRouteFirstByteConfigForAPIKeyRecord(apiKeyRecord)
	if config == nil {
		return nil
	}
	return &NormalRouteFirstByteRuntimeConfig{
		SchedulingPreference: config.SchedulingPreference,
		FirstByteDeadlineMs:  config.FirstByteDeadlineMs,
	}
}

// normalRouteSpeedFirstConfigForAPIKey mirrors normalRouteSpeedFirstConfigForApiKey.
func (s *Service) normalRouteSpeedFirstConfigForAPIKey(apiKeyRecord *gatewayruntimecache.GatewayAPIKeyRow, lane gatewayProtoLane, compactionTimeoutsDisabled bool) *NormalRouteSpeedFirstRuntimeConfig {
	if compactionTimeoutsDisabled || !gatewayrouting.NormalRouteSpeedFirstAppliesToLane(lane) {
		return nil
	}
	if apiKeyRecord == nil || apiKeyRecord.RouteStrategyMode != gatewayruntimecache.RouteStrategyModeNormal {
		return nil
	}
	normalConfig := apiKeyRecord.NormalRoutingConfig
	if normalConfig == nil || normalConfig.SchedulingPreference != "speed_first" {
		return nil
	}
	if normalConfig.Raw == nil {
		return nil
	}
	var speedFirstConfig struct {
		FirstByteDeadlineMs *int64         `json:"firstByteDeadlineMs"`
		Raw                 map[string]any `json:"-"`
	}
	// The stored speedFirstConfig object is carried opaquely: decode the
	// deadline field and keep the raw object for the latency slice.
	decoded, err := gatewaybodyDecodeJSON(normalConfig.Raw)
	if err != nil {
		return nil
	}
	speedFirstConfig.Raw = decoded
	if deadline, ok := decoded["firstByteDeadlineMs"].(float64); ok {
		value := int64(deadline)
		speedFirstConfig.FirstByteDeadlineMs = &value
	} else if normalConfig.SchedulingPreference == "speed_first" && apiKeyRecord.NormalRoutingConfig != nil {
		// firstByteDeadlineMs rides on the normal config next to the
		// preference, exactly like the Node spread.
		if root, err := gatewaybodyDecodeJSON(apiKeyRecord.NormalRoutingConfig.Raw); err == nil {
			if deadline, ok := root["firstByteDeadlineMs"].(float64); ok {
				value := int64(deadline)
				speedFirstConfig.FirstByteDeadlineMs = &value
			}
		}
	}
	return &NormalRouteSpeedFirstRuntimeConfig{
		SchedulingPreference: "speed_first",
		FirstByteDeadlineMs:  speedFirstConfig.FirstByteDeadlineMs,
		Raw:                  speedFirstConfig.Raw,
	}
}

// normalRouteFirstByteConfigForAPIKeyRecord mirrors the Node helper without
// the lane gate.
func normalRouteFirstByteConfigForAPIKeyRecord(apiKeyRecord *gatewayruntimecache.GatewayAPIKeyRow) *NormalRouteFirstByteRuntimeConfig {
	if apiKeyRecord == nil || apiKeyRecord.RouteStrategyMode != gatewayruntimecache.RouteStrategyModeNormal {
		return nil
	}
	normalConfig := apiKeyRecord.NormalRoutingConfig
	if normalConfig == nil {
		normalConfig = &gatewayruntimecache.RouteStrategyNormalRoutingConfig{SchedulingPreference: "cost_first"}
	}
	if normalConfig.SchedulingPreference != "speed_first" {
		return nil
	}
	return &NormalRouteFirstByteRuntimeConfig{
		SchedulingPreference: "speed_first",
		FirstByteDeadlineMs:  firstByteDeadlineFromConfig(normalConfig),
	}
}

// firstByteDeadlineFromConfig decodes firstByteDeadlineMs off the stored
// normal routing config object.
func firstByteDeadlineFromConfig(config *gatewayruntimecache.RouteStrategyNormalRoutingConfig) *int64 {
	if config == nil || config.Raw == nil {
		return nil
	}
	decoded, err := gatewaybodyDecodeJSON(config.Raw)
	if err != nil {
		return nil
	}
	if deadline, ok := decoded["firstByteDeadlineMs"].(float64); ok {
		value := int64(deadline)
		return &value
	}
	return nil
}

// shouldDeferForcedImageGenerationToolPermissionToAnthropicBridge mirrors the
// Node helper: defer only when every candidate maps to the anthropic
// messages family.
func (s *Service) shouldDeferForcedImageGenerationToolPermissionToAnthropicBridge(req *GatewayRequest, accounts []gatewayruntimecache.OpenAIAccountSecret, requestClientCompatibility string) bool {
	sourceEndpointFamily := openAIRequestEndpointFamily(req)
	if sourceEndpointFamily != EndpointFamilyChatCompletions && sourceEndpointFamily != EndpointFamilyResponses {
		return false
	}
	if len(accounts) == 0 {
		return false
	}
	requestedModel, _ := RequestModel(req)
	for _, account := range accounts {
		if !isAnthropicProtocolAccount(account) {
			return false
		}
		mapping := resolveOpenAIAccountModelMapping(account, requestedModel, sourceEndpointFamily)
		if mapping == nil || mapping.UpstreamEndpointFamily != EndpointFamilyMessages {
			return false
		}
	}
	return true
}

// accountModelsTargetImage mirrors accountModelsTargetImage: the mapped
// upstream model or the provider model catalog decides the image lane.
func (s *Service) accountModelsTargetImage(ctx context.Context, req *GatewayRequest, accounts []gatewayruntimecache.OpenAIAccountSecret, systemAccountID string) (bool, error) {
	sourceEndpointFamily := gatewayRequestEndpointFamily(req)
	requestedModel, _ := RequestModel(req)
	type catalogScope struct {
		providerCode    string
		systemAccountID string
		models          map[string]bool
	}
	scopes := map[string]*catalogScope{}
	var scopeOrder []string
	for _, account := range accounts {
		upstreamModel := requestedModel
		if mapping := resolveOpenAIAccountModelMapping(account, requestedModel, sourceEndpointFamily); mapping != nil && mapping.UpstreamModel != "" {
			upstreamModel = mapping.UpstreamModel
		}
		if upstreamModel == "" {
			continue
		}
		if IsOpenAIGatewayImageGenerationModel(upstreamModel) {
			return true, nil
		}
		providerCode := strings.TrimSpace(account.ProviderCode)
		if providerCode == "" {
			continue
		}
		catalogOwner := strings.TrimSpace(account.AccountOwnerSystemAccountID)
		if catalogOwner == "" {
			catalogOwner = systemAccountID
		}
		scopeKey := providerCode + "\x00" + catalogOwner
		scope := scopes[scopeKey]
		if scope == nil {
			scope = &catalogScope{providerCode: providerCode, systemAccountID: catalogOwner, models: map[string]bool{}}
			scopes[scopeKey] = scope
			scopeOrder = append(scopeOrder, scopeKey)
		}
		scope.models[strings.TrimSpace(upstreamModel)] = true
	}
	if len(scopes) == 0 {
		return false, nil
	}
	for _, key := range scopeOrder {
		scope := scopes[key]
		items, err := s.RuntimeCache.ListCachedProviderModelCatalogAsync(ctx, gatewayruntimecache.ModelCatalogListOptions{
			ProviderCode: scope.providerCode, SystemAccountID: scope.systemAccountID, IncludeUnpriced: true,
		})
		if err != nil {
			return false, err
		}
		for _, item := range items {
			if !scope.models[strings.TrimSpace(item.Model)] {
				continue
			}
			if containsString(item.SupportedAPIProtocols, "images") ||
				boolListContains(item.OutputModalities, "image") ||
				item.ImageOutputUsdPer1M != nil ||
				item.OutputUsdPerImage != nil {
				return true, nil
			}
		}
	}
	return false, nil
}

// Endpoint family constants for the request endpoint family resolution.
const (
	EndpointFamilyChatCompletions = "chat_completions"
	EndpointFamilyResponses       = "responses"
	EndpointFamilyMessages        = "messages"
)

// gatewayRequestEndpointFamily mirrors gatewayRequestEndpointFamily:
// openai, then anthropic, then gemini.
func gatewayRequestEndpointFamily(req *GatewayRequest) string {
	if family := openAIRequestEndpointFamily(req); family != "" {
		return family
	}
	if family := anthropicMessagesRequestEndpointFamily(req); family != "" {
		return family
	}
	return geminiRequestEndpointFamily(req)
}

// openAIRequestEndpointFamily mirrors openAIRequestEndpointFamily.
func openAIRequestEndpointFamily(req *GatewayRequest) string {
	endpoint := strings.SplitN(req.PathAndQuery(), "?", 2)[0]
	return openAIEndpointFamilyFromPath(endpoint)
}

// openAIEndpointFamilyFromPath mirrors openAIEndpointFamilyFromPath.
func openAIEndpointFamilyFromPath(value string) string {
	path := strings.ToLower(strings.TrimSpace(value))
	if path == "" {
		return ""
	}
	if strings.Contains(path, "/chat/completions") {
		return EndpointFamilyChatCompletions
	}
	if strings.Contains(path, "/responses") {
		return EndpointFamilyResponses
	}
	return ""
}

// anthropicMessagesRequestEndpointFamily mirrors the Node helper.
func anthropicMessagesRequestEndpointFamily(req *GatewayRequest) string {
	if req.MethodUpper() != "POST" {
		return ""
	}
	endpoint := strings.SplitN(req.PathAndQuery(), "?", 2)[0]
	normalizedPath := endpoint
	if !strings.HasPrefix(normalizedPath, "/") {
		normalizedPath = "/" + normalizedPath
	}
	normalizedPath = stripV1Prefix(normalizedPath)
	if normalizedPath == "" {
		normalizedPath = "/"
	}
	if normalizedPath == "/messages" {
		return EndpointFamilyMessages
	}
	return ""
}

// stripV1Prefix mirrors the `/^\/v1(?=\/$)/` replace used by the Node
// anthropic family helper.
func stripV1Prefix(path string) string {
	if strings.HasPrefix(path, "/v1") && (len(path) == 3 || path[3] == '/') {
		return path[3:]
	}
	return path
}

// geminiRequestEndpointFamily mirrors the Node helper: POST only, the
// gemini endpoint family from the path, 'models' excluded.
func geminiRequestEndpointFamily(req *GatewayRequest) string {
	if req.MethodUpper() != "POST" {
		return ""
	}
	endpoint := strings.SplitN(req.PathAndQuery(), "?", 2)[0]
	// The gemini family classifier lives in gatewaygemini
	// (EndpointFamilyFromPath); the path is normalized the same way.
	family := geminiEndpointFamilyFromPath(endpoint)
	if family == "" || family == "models" {
		return ""
	}
	return family
}

// geminiEndpointFamilyFromPath mirrors geminiEndpointFamilyFromPath via the
// gatewaygemini classifier (protocol/path vocabulary without importing the
// http request types).
func geminiEndpointFamilyFromPath(value string) string {
	return geminiEndpointFamilyForPath(value)
}

// isAnthropicProtocolAccount mirrors isAnthropicProtocolProfile(account):
// normalized protocolCode/protocolVersion equal the anthropic v1 pair.
func isAnthropicProtocolAccount(account gatewayruntimecache.OpenAIAccountSecret) bool {
	return normalizeProviderToken(account.ProtocolCode) == "anthropic" &&
		normalizeProviderToken(account.ProtocolVersion) == "v1"
}

// normalizeProviderToken mirrors normalizeProviderToken.
func normalizeProviderToken(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return ""
	}
	return normalized
}

// resolveOpenAIAccountModelMapping mirrors resolveOpenAIAccountModelMapping
// on top of the gatewayopenai mapping core.
func resolveOpenAIAccountModelMapping(account gatewayruntimecache.OpenAIAccountSecret, requestedModel, sourceEndpointFamily string) *gatewayprotoResolvedMapping {
	if requestedModel == "" || sourceEndpointFamily == "" {
		return nil
	}
	runtime := projectRuntimeAccount(account)
	resolved := gatewayopenai.ResolveAccountModelMapping(runtime, requestedModel, sourceEndpointFamily)
	if resolved == nil {
		return nil
	}
	return &gatewayprotoResolvedMapping{
		SourceModel:            resolved.SourceModel,
		SourceEndpointFamily:   resolved.SourceEndpointFamily,
		UpstreamModel:          resolved.UpstreamModel,
		UpstreamEndpointFamily: resolved.UpstreamEndpointFamily,
	}
}

// projectRuntimeAccount projects the runtime-cache secret onto the
// gatewayopenai mapping account.
func projectRuntimeAccount(account gatewayruntimecache.OpenAIAccountSecret) *gatewayopenai.RuntimeAccount {
	mappings := make([]gatewayopenai.AccountModelMapping, 0, len(account.ModelMappings))
	for _, mapping := range account.ModelMappings {
		enabled := mapping.Enabled
		mappings = append(mappings, gatewayopenai.AccountModelMapping{
			SourceModel:            mapping.SourceModel,
			SourceEndpointFamily:   mapping.SourceEndpointFamily,
			UpstreamModel:          mapping.UpstreamModel,
			UpstreamEndpointFamily: mapping.UpstreamEndpointFamily,
			Enabled:                &enabled,
			RuntimeSource:          derefString(mapping.RuntimeSource),
			RuntimeRouteRuleID:     derefString(mapping.RuntimeRouteRuleID),
		})
	}
	return &gatewayopenai.RuntimeAccount{
		ModelMappings:             mappings,
		ProviderCode:              account.ProviderCode,
		ProviderProtocolProfileID: account.ProviderProtocolProfileID,
		ProtocolCode:              account.ProtocolCode,
		ProtocolVersion:           account.ProtocolVersion,
	}
}

// ---------------------------------------------------------------------------
// recoverable candidate waiting (waitForRecoverableOpenAIGatewayCandidateAccounts)
// ---------------------------------------------------------------------------

type recoveryInput struct {
	req                      *GatewayRequest
	auditCapture             AuditCaptureContext
	systemAccountID          string
	apiKeyID                 string
	groupID                  string
	startedAt                int64
	serverRetryBudget        *ServerRetryBudget
	routeCoordinationBudget  *gatewayrouting.RouteCoordinationBudget
	gatewayRequestWallBudget *gatewayrouting.GatewayRequestWallBudget
	signal                   context.Context
}

// waitForRecoverableOpenAIGatewayCandidateAccounts mirrors
// waitForRecoverableOpenAIGatewayCandidateAccounts.
func (s *Service) waitForRecoverableOpenAIGatewayCandidateAccounts(input recoveryInput) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	requestedModel, _ := RequestModel(input.req)
	requestedEndpointFamily := gatewayRequestEndpointFamily(input.req)
	loadActiveAccounts := func() ([]gatewayruntimecache.OpenAIAccountSecret, error) {
		return s.RuntimeCache.ListFreshOpenAIAccountsForGroupAsync(s.requestContext(), input.groupID, input.systemAccountID, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions{
			RequestedModel: requestedModel, RequestedEndpointFamily: requestedEndpointFamily,
		})
	}
	windowMs := input.serverRetryBudget.RemainingMs(nil)
	loadRecoverableAccounts := func() ([]gatewayruntimecache.OpenAIAccountSecret, error) {
		return s.RuntimeCache.ListRecoverableUnavailableOpenAIAccountsForGroupAsync(s.requestContext(), input.groupID, input.systemAccountID, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions{
			RequestedModel: requestedModel, RequestedEndpointFamily: requestedEndpointFamily,
		}, &windowMs)
	}
	activeAccounts, err := loadActiveAccounts()
	if err != nil {
		return nil, err
	}
	if len(activeAccounts) > 0 {
		return activeAccounts, nil
	}
	recoverableAccounts, err := loadRecoverableAccounts()
	if err != nil {
		return nil, err
	}
	if len(recoverableAccounts) == 0 {
		return []gatewayruntimecache.OpenAIAccountSecret{}, nil
	}
	waitStartedAtMs := s.NowMs()
	deadlineAtMs := input.serverRetryBudget.DeadlineAtMs(&waitStartedAtMs)
	nextRetryAfterMs := func(ctx context.Context) (int64, bool) {
		recoverable, loadErr := loadRecoverableAccounts()
		if loadErr != nil {
			return 0, false
		}
		return nextRecoverableAccountRetryAfterMs(recoverable, s.NowMs())
	}
	refresh := func(ctx context.Context) error {
		_, loadErr := loadActiveAccounts()
		return loadErr
	}
	ready := func(ctx context.Context) bool {
		accounts, loadErr := loadActiveAccounts()
		return loadErr == nil && len(accounts) > 0
	}
	waitErr := s.Recoverable.WaitForRecoverableUnavailableState(s.requestContext(), RecoverableWaitInput{
		ScopeKey:                 recoverableCandidateScopeKey(input.systemAccountID, input.apiKeyID, input.groupID, requestedModel, requestedEndpointFamily),
		Reason:                   "account_cooldown_recoverable",
		Refresh:                  refresh,
		IsReady:                  ready,
		NextRetryAfterMs:         nextRetryAfterMs,
		AuditCapture:             input.auditCapture,
		MaxWaitMs:                input.serverRetryBudget.RemainingMs(&waitStartedAtMs),
		RequestStartedAtMs:       waitStartedAtMs,
		DeadlineAtMs:             deadlineAtMs,
		RouteCoordinationBudget:  input.routeCoordinationBudget,
		GatewayRequestWallBudget: input.gatewayRequestWallBudget,
		Signal:                   input.signal,
	})
	// finally: pauseNoAvailableWait
	input.serverRetryBudget.PauseNoAvailableWait(nil)
	if waitErr != nil {
		if errors.Is(waitErr, context.Canceled) || errors.Is(waitErr, context.DeadlineExceeded) {
			return []gatewayruntimecache.OpenAIAccountSecret{}, nil
		}
		return nil, waitErr
	}
	// The final state snapshot comes from the last refresh.
	finalAccounts, err := loadActiveAccounts()
	if err != nil {
		return nil, err
	}
	return finalAccounts, nil
}

// nextRecoverableAccountRetryAfterMs mirrors nextRecoverableAccountRetryAfterMs.
func nextRecoverableAccountRetryAfterMs(accounts []gatewayruntimecache.OpenAIAccountSecret, now int64) (int64, bool) {
	var nextRetryAfterMs *int64
	for _, account := range accounts {
		if account.CooldownUntil == nil {
			continue
		}
		cooldownUntilMs, ok := rfc3339InstantMilliseconds(*account.CooldownUntil)
		if !ok {
			return 0, false
		}
		retryAfterMs := cooldownUntilMs - now
		if retryAfterMs < 0 {
			retryAfterMs = 0
		}
		if nextRetryAfterMs == nil || retryAfterMs < *nextRetryAfterMs {
			copied := retryAfterMs
			nextRetryAfterMs = &copied
		}
	}
	if nextRetryAfterMs == nil {
		return 0, false
	}
	return *nextRetryAfterMs, true
}

// recoverableCandidateScopeKey mirrors recoverableCandidateScopeKey.
func recoverableCandidateScopeKey(systemAccountID, apiKeyID, groupID, requestedModel, requestedEndpointFamily string) string {
	return strings.Join([]string{systemAccountID, apiKeyID, groupID, requestedModel, requestedEndpointFamily}, ":")
}

// rfc3339InstantMilliseconds mirrors rfc3339InstantMilliseconds with the
// shared parser semantics (offset required).
func rfc3339InstantMilliseconds(value string) (int64, bool) {
	return gatewayruntimecacheRFC3339Millis(value)
}

// ---------------------------------------------------------------------------
// api key group fallback dispatch context
// ---------------------------------------------------------------------------

// APIKeyGroupFallbackDispatchInput mirrors ApiKeyGroupFallbackDispatchInput.
type APIKeyGroupFallbackDispatchInput struct {
	Req                        *GatewayRequest
	Res                        GatewayResponseWriter
	AuditCapture               AuditCaptureContext
	Options                    *PreflightOptions
	StartedAt                  int64
	TraceID                    string
	ClientIP                   string
	Endpoint                   string
	RequestSnapshot            UsageRequestSnapshot
	Signal                     context.Context
	Reason                     string
	APIKeyRecord               *gatewayruntimecache.GatewayAPIKeyRow
	GroupFallbackAPIKeyRecord  *gatewayruntimecache.GatewayAPIKeyRow
	SystemAccountID            string
	APIKeyID                   string
	GroupID                    string
	TrafficSource              string
	RequestLane                gatewayProtoLane
	RequestClientCompatibility string
	// ExcludedAccountIDs mirrors the switchToFallbackGroup exhaustedAccountIds
	// (routes.ts:625); nil on the route-action fallback (Node resolveRouteAction
	// passes none, routes.ts:449-478).
	ExcludedAccountIDs         map[string]struct{}
	RoutePlanSnapshot          gatewayrouting.RoutePlanSnapshot[string]
}

// APIKeyGroupFallbackDispatchResult mirrors ApiKeyGroupFallbackDispatchResult.
type APIKeyGroupFallbackDispatchResult struct {
	Attempted bool
	Context   PreflightResult
}

// PrepareAPIKeyGroupFallbackDispatchContext mirrors
// prepareApiKeyGroupFallbackDispatchContext.
func (s *Service) PrepareAPIKeyGroupFallbackDispatchContext(ctx context.Context, input APIKeyGroupFallbackDispatchInput) (APIKeyGroupFallbackDispatchResult, error) {
	snapshot := input.RoutePlanSnapshot
	if !canAttemptAPIKeyGroupFallback(input.APIKeyRecord, input.GroupID, &snapshot) {
		return APIKeyGroupFallbackDispatchResult{Attempted: false}, nil
	}
	candidate, found, err := s.Candidates.ResolveNextGroupFallbackCandidate(ctx, GroupFallbackCandidateInput{
		Req: input.Req, Reason: input.Reason, APIKeyRecord: input.APIKeyRecord,
		SystemAccountID: input.SystemAccountID, GroupID: input.GroupID,
		RequestLane:                string(input.RequestLane),
		RequestClientCompatibility: input.RequestClientCompatibility,
		ExcludedAccountIDs:         input.ExcludedAccountIDs,
		RoutePlanSnapshot:          snapshot,
	})
	if err != nil {
		return APIKeyGroupFallbackDispatchResult{}, err
	}
	if !found {
		return APIKeyGroupFallbackDispatchResult{Attempted: false}, nil
	}
	input.AuditCapture.AddGatewayMetadata("api_key_group_route_fallback", map[string]any{
		"reason":      input.Reason,
		"fromGroupId": input.GroupID,
		"toGroupId":   candidate.GroupID,
	})
	options := *input.Options
	options.Identity = &OpenAIGatewayRequestIdentity{
		SystemAccountID: input.SystemAccountID,
		APIKeyID:        input.APIKeyID,
		GroupID:         candidate.GroupID,
	}
	options.APIKeyRecord = input.APIKeyRecord
	if input.GroupFallbackAPIKeyRecord != nil {
		options.GroupFallbackAPIKeyRecord = input.GroupFallbackAPIKeyRecord
	} else {
		options.GroupFallbackAPIKeyRecord = input.APIKeyRecord
	}
	options.CandidateAccounts = candidate.Accounts
	options.ResponseInspectionPolicies = candidate.ResponseInspectionPolicies
	options.TrafficSource = input.TrafficSource
	options.RequestLane = input.RequestLane
	if candidate.RoutePlanSnapshot != nil {
		options.RoutePlanSnapshot = candidate.RoutePlanSnapshot
	} else {
		options.RoutePlanSnapshot = &snapshot
	}
	context, err := s.PrepareOpenAIGatewayDispatchContext(ctx, PreflightInput{
		Req: input.Req, Res: input.Res, AuditCapture: input.AuditCapture,
		Options: &options, StartedAt: input.StartedAt, TraceID: input.TraceID,
		ClientIP: input.ClientIP, Endpoint: input.Endpoint,
		RequestSnapshot: input.RequestSnapshot, Signal: input.Signal,
	})
	if err != nil {
		return APIKeyGroupFallbackDispatchResult{}, err
	}
	return APIKeyGroupFallbackDispatchResult{Attempted: true, Context: context}, nil
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func indexOfString(values []string, needle string) int {
	for i, value := range values {
		if value == needle {
			return i
		}
	}
	return -1
}

func boolListContains(values []string, needle string) bool {
	return containsString(values, needle)
}
