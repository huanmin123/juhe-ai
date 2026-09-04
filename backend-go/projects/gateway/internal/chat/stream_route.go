package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
)

// POST /conversations/{id}/stream ported from chat.routes.ts: preparation
// state machine, model/protocol resolution, transport build, turn acceptance,
// ChatGenerationRunner execution over the GenerationExecutor port, SSE
// delivery and the failure/recovery paths. Status codes, payload shapes,
// event order and Chinese error strings mirror Node.

const (
	maxMessageBytes             = 192 * 1024
	maxInternalChatRequestBytes = 21 * 1024 * 1024
	streamStorageQuotaBytes     = 2 * 1024 * 1024 * 1024
	defaultUpstreamSSEMaxEvents = 65536
)

type streamMessageBody struct {
	ClientMessageID      string
	ReplaceTurnID        string
	Content              string
	ContentBlocks        []InputContentBlock
	Model                string
	ReasoningEffort      string
	ServiceTier          string
	GenerationParameters *ChatGenerationParameters
}

// parseStreamMessageBody mirrors messageBodySchema (strict + refine order).
func parseStreamMessageBody(raw map[string]json.RawMessage) (*streamMessageBody, error) {
	body := &streamMessageBody{}
	for _, key := range []string{"clientMessageId", "replaceTurnId", "content", "model", "reasoningEffort", "serviceTier"} {
		value, ok := raw[key]
		if !ok {
			continue
		}
		max := 100
		required := key != "replaceTurnId"
		if key == "content" {
			max = 196608
		}
		if key == "model" {
			max = 200
		}
		var parsed string
		if err := json.Unmarshal(value, &parsed); err != nil {
			return nil, &invalidRequestError{Message: "Expected string, received " + jsonValueTypeName(value)}
		}
		trimmed := strings.TrimSpace(parsed)
		if trimmed == "" {
			if required {
				return nil, &invalidRequestError{Message: requiredMessage(key)}
			}
			continue
		}
		if utf8RuneLen(trimmed) > max {
			return nil, &invalidRequestError{Message: maxMessage(key)}
		}
		body.setField(key, trimmed)
	}
	if value, ok := raw["contentBlocks"]; ok && string(value) != "null" {
		var parsed []map[string]json.RawMessage
		if err := json.Unmarshal(value, &parsed); err != nil {
			return nil, &invalidRequestError{Message: "Expected array, received " + jsonValueTypeName(value)}
		}
		if len(parsed) > 11 {
			return nil, &invalidRequestError{Message: "Array must contain at most 11 element(s)"}
		}
		imageCount := 0
		for _, item := range parsed {
			blockType := ""
			if rawType, ok := item["type"]; ok {
				_ = json.Unmarshal(rawType, &blockType)
			}
			switch blockType {
			case "input_text":
				var text string
				rawText, hasText := item["text"]
				if !hasText {
					return nil, &invalidRequestError{Message: "Required"}
				}
				if err := json.Unmarshal(rawText, &text); err != nil {
					return nil, &invalidRequestError{Message: "Expected string, received " + jsonValueTypeName(rawText)}
				}
				if utf8RuneLen(text) > 196608 {
					return nil, &invalidRequestError{Message: "文本块内容过长"}
				}
				body.ContentBlocks = append(body.ContentBlocks, InputContentBlock{Type: "input_text", Text: &text})
			case "input_image":
				imageCount++
				var assetID string
				rawAsset, hasAsset := item["assetId"]
				if !hasAsset {
					return nil, &invalidRequestError{Message: "Required"}
				}
				if err := json.Unmarshal(rawAsset, &assetID); err != nil {
					return nil, &invalidRequestError{Message: "Expected string, received " + jsonValueTypeName(rawAsset)}
				}
				trimmed := strings.TrimSpace(assetID)
				if trimmed == "" {
					return nil, &invalidRequestError{Message: "图片资产 ID 不能为空"}
				}
				if utf8RuneLen(trimmed) > 120 {
					return nil, &invalidRequestError{Message: "String must contain at most 120 character(s)"}
				}
				body.ContentBlocks = append(body.ContentBlocks, InputContentBlock{Type: "input_image", AssetID: &trimmed})
			default:
				return nil, &invalidRequestError{Message: "Invalid input"}
			}
			for key := range item {
				if key != "type" && key != "text" && key != "assetId" {
					return nil, &invalidRequestError{Message: "Unrecognized key: \"" + key + "\""}
				}
			}
		}
		if imageCount > 5 {
			return nil, &invalidRequestError{Message: "最多粘贴 5 张图片"}
		}
		seen := map[string]bool{}
		for _, block := range body.ContentBlocks {
			if block.Type != "input_image" || block.AssetID == nil {
				continue
			}
			if seen[*block.AssetID] {
				return nil, &invalidRequestError{Message: "同一张图片不能重复引用"}
			}
			seen[*block.AssetID] = true
		}
	}
	if value, ok := raw["generationParameters"]; ok && string(value) != "null" {
		var parsed map[string]json.RawMessage
		if err := json.Unmarshal(value, &parsed); err != nil {
			return nil, &invalidRequestError{Message: "Expected object, received " + jsonValueTypeName(value)}
		}
		params := &ChatGenerationParameters{}
		for key, rawValue := range parsed {
			var number float64
			if err := json.Unmarshal(rawValue, &number); err != nil {
				return nil, &invalidRequestError{Message: "Expected number, received " + jsonValueTypeName(rawValue)}
			}
			switch key {
			case "temperature":
				params.Temperature = &number
			case "topP":
				params.TopP = &number
			case "frequencyPenalty":
				params.FrequencyPenalty = &number
			case "presencePenalty":
				params.PresencePenalty = &number
			case "maxOutputTokens":
				if number != truncF(number) {
					return nil, &invalidRequestError{Message: "Expected int, received " + jsonValueTypeName(rawValue)}
				}
				params.MaxOutputTokens = &number
			case "seed":
				if number != truncF(number) {
					return nil, &invalidRequestError{Message: "Expected int, received " + jsonValueTypeName(rawValue)}
				}
				params.Seed = &number
			default:
				return nil, &invalidRequestError{Message: "Unrecognized key: \"" + key + "\""}
			}
		}
		body.GenerationParameters = params
	}
	for key := range raw {
		switch key {
		case "clientMessageId", "replaceTurnId", "content", "contentBlocks", "model", "reasoningEffort", "serviceTier", "generationParameters":
		default:
			return nil, &invalidRequestError{Message: "Unrecognized key: \"" + key + "\""}
		}
	}
	if body.ClientMessageID == "" {
		return nil, &invalidRequestError{Message: "String must contain at least 1 character(s)"}
	}
	if body.Content == "" {
		return nil, &invalidRequestError{Message: "请输入消息"}
	}
	if body.Model == "" {
		return nil, &invalidRequestError{Message: "请选择模型"}
	}
	if body.ReasoningEffort != "" && !chatReasoningEffortSet[body.ReasoningEffort] {
		return nil, &invalidRequestError{Message: "Invalid enum value. Expected 'minimal' | 'low' | 'medium' | 'high' | 'xhigh' | 'max', received '" + body.ReasoningEffort + "'"}
	}
	if body.ServiceTier != "" && !chatServiceTierSet[body.ServiceTier] {
		return nil, &invalidRequestError{Message: "Invalid enum value. Expected 'default' | 'priority' | 'flex', received '" + body.ServiceTier + "'"}
	}
	return body, nil
}

func (b *streamMessageBody) setField(key, value string) {
	switch key {
	case "clientMessageId":
		b.ClientMessageID = value
	case "replaceTurnId":
		b.ReplaceTurnID = value
	case "content":
		b.Content = value
	case "model":
		b.Model = value
	case "reasoningEffort":
		b.ReasoningEffort = value
	case "serviceTier":
		b.ServiceTier = value
	}
}

func requiredMessage(key string) string {
	switch key {
	case "content":
		return "请输入消息"
	case "model":
		return "请选择模型"
	}
	return "String must contain at least 1 character(s)"
}

func maxMessage(key string) string {
	switch key {
	case "clientMessageId", "replaceTurnId":
		return "String must contain at most 100 character(s)"
	case "content":
		return "消息内容过长"
	case "model":
		return "String must contain at most 200 character(s)"
	}
	return "String too long"
}

// resolveChatModelRequestOptions mirrors resolveChatModelRequestOptions.
func resolveChatModelRequestOptions(rt *chatRoutes, option *ChatModelOption, body *streamMessageBody) (reasoningEffort, serviceTier string, params *ChatGenerationParameters, maxInputTokens *int64, err error) {
	if body.ReasoningEffort != "" && !containsString(option.SupportedReasoningEfforts, body.ReasoningEffort) {
		return "", "", nil, nil, &ModelCapabilityError{Message: "当前模型不支持所选思考级别，请重新选择"}
	}
	if body.ServiceTier != "" && !containsString(option.SupportedServiceTiers, body.ServiceTier) {
		return "", "", nil, nil, &ModelCapabilityError{Message: "当前模型不支持所选服务等级，请重新选择"}
	}
	generationParameters := body.GenerationParameters
	if generationParameters == nil {
		generationParameters = &ChatGenerationParameters{}
	}
	if generationParameters.Temperature != nil && generationParameters.TopP != nil {
		return "", "", nil, nil, &ModelCapabilityError{Message: "温度和 Top P 只能设置其中一个"}
	}
	allowed := map[string]ChatGenerationParameterCapability{}
	for _, capability := range option.GenerationParameters {
		allowed[capability.Parameter] = capability
	}
	for parameter, value := range map[string]*float64{
		"temperature": generationParameters.Temperature, "topP": generationParameters.TopP,
		"frequencyPenalty": generationParameters.FrequencyPenalty, "presencePenalty": generationParameters.PresencePenalty,
		"maxOutputTokens": generationParameters.MaxOutputTokens, "seed": generationParameters.Seed,
	} {
		if value == nil {
			continue
		}
		capability, ok := allowed[parameter]
		if !ok {
			return "", "", nil, nil, &ModelCapabilityError{Message: "当前模型或路由不支持所选生成参数，请重新选择"}
		}
		if *value < capability.Min || *value > capability.Max {
			return "", "", nil, nil, &ModelCapabilityError{Message: "生成参数 " + parameter + " 超出当前模型支持范围"}
		}
		if (parameter == "seed" || parameter == "maxOutputTokens") && *value != truncF(*value) {
			return "", "", nil, nil, &ModelCapabilityError{Message: "生成参数 " + parameter + " 必须是整数"}
		}
	}
	if option.MaxInputTokens != nil {
		maxInputTokens = option.MaxInputTokens
	}
	return body.ReasoningEffort, body.ServiceTier, generationParameters, maxInputTokens, nil
}

// streamTurn mirrors POST /conversations/:conversationId/stream.
func (rt *chatRoutes) streamTurn(w http.ResponseWriter, r *http.Request) {
	var (
		accepted                  *AcceptTurnResult
		ownerID                   string
		runner                    *ChatGenerationRunner
		prep                      *activePreparation
		preparationConversationID string
		responseClosed            bool
		responseClosedMu          sync.Mutex
	)
	raw, err := readJSONBody(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	parsed, err := decodeObjectBody(raw)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	body, err := parseStreamMessageBody(parsed)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	traceID := rt.deps.traceID(r)
	if len(body.Content) > maxMessageBytes {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "消息内容超过 192 KiB 上限"})
		return
	}
	ownerID, err = rt.requireChatAuth(r)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	conversation, err := rt.deps.Store.GetConversation(r.PathValue("conversationId"), ownerID)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	if conversation == nil {
		writeMessageCode(w, http.StatusNotFound, "会话不存在", "chat_conversation_not_found")
		return
	}
	existingTurn, err := rt.deps.Store.FindTurnByClientMessageID(conversation.ID, ownerID, body.ClientMessageID)
	if err != nil {
		writeChatRouteError(w, err)
		return
	}
	if existingTurn != nil {
		writeMessageCode(w, http.StatusConflict, "该消息已提交，请刷新会话", "chat_message_already_exists")
		return
	}
	replaceRequested := body.ReplaceTurnID != ""
	if !replaceRequested && conversation.UserTurnCount >= rt.deps.MaxTurnsPerConversation {
		writeChatRouteError(w, &ConflictError{Code: ConflictTurnLimitExceeded})
		return
	}
	if conversation.ActiveTurnID != nil {
		code := ConflictMessageInProgress
		if replaceRequested {
			code = ConflictReplaceConflict
		}
		writeChatRouteError(w, &ConflictError{Code: code})
		return
	}
	if action := rt.getAction(conversation.ID, ownerID); action != nil {
		code := ConflictConversationClearing
		if action.kind == "compacting" {
			code = ConflictContextCompacting
		}
		writeChatRouteError(w, &ConflictError{Code: code})
		return
	}
	preparationConversationID = conversation.ID
	prep = rt.claimPreparation(conversation.ID, ownerID, body.ClientMessageID)
	if prep == nil {
		code := ConflictMessageInProgress
		if replaceRequested {
			code = ConflictReplaceConflict
		}
		writeChatRouteError(w, &ConflictError{Code: code})
		return
	}
	defer rt.deletePreparationIfMatches(preparationConversationID, prep.token)
	failWith := func(err error) {
		if responseClosedState(&responseClosedMu, &responseClosed) {
			return
		}
		writeStreamRouteError(w, err)
	}
	// Preparation is alive through the synchronous validation prologue; every
	// step re-checks (assertChatPreparationActive).
	assertActive := func() error {
		if prep.isCanceled() {
			return &PreparationCanceledError{}
		}
		return nil
	}
	if err := assertActive(); err != nil {
		failWith(err)
		return
	}
	if replaceRequested {
		if err := rt.deps.Store.AssertTurnReplaceable(AssertReplaceableInput{
			ConversationID:  conversation.ID,
			SystemAccountID: ownerID,
			ReplaceTurnID:   body.ReplaceTurnID,
			Now:             rt.now(),
		}); err != nil {
			failWith(err)
			return
		}
	}
	if err := assertActive(); err != nil {
		failWith(err)
		return
	}
	apiKey, err := rt.requireOwnedApiKey(derefString(conversation.APIKeyID), ownerID)
	if err != nil {
		failWith(err)
		return
	}
	gatewayKey, err := rt.deps.gatewayKeyOrError(apiKey.Secret)
	if err != nil {
		failWith(err)
		return
	}
	groupIDs := []string{}
	for _, binding := range gatewayKey.GroupBindings {
		groupIDs = append(groupIDs, binding.GroupID)
	}
	imageCount := 0
	for _, block := range body.ContentBlocks {
		if block.Type == "input_image" {
			imageCount++
		}
	}
	catalog := rt.chatModelCatalog(groupIDs, ownerID, body.Model)
	if err := assertActive(); err != nil {
		failWith(err)
		return
	}
	modelOptions := buildChatModelOptions([]string{body.Model}, catalog)
	var modelOption *ChatModelOption
	if len(modelOptions) > 0 {
		modelOption = modelOptions[0]
	}
	if modelOption == nil {
		failWith(&ModelCapabilityError{Message: "当前模型能力信息不可用，请刷新模型列表"})
		return
	}
	accountSupportedProtocols := resolveChatSupportedProtocols(groupIDs, body.Model, func(groupID, model, endpointFamily string) []ChatTransportAccount {
		return rt.accountsForGroups([]string{groupID}, ownerID, model, endpointFamily)
	})
	if err := assertActive(); err != nil {
		failWith(err)
		return
	}
	if len(accountSupportedProtocols) == 0 {
		failWith(&ModelCapabilityError{Message: "当前 API Key 没有可用于该模型的对话路由，请切换模型或检查账户映射"})
		return
	}
	supportsWebSearch := containsString(modelOption.SupportedTools, "web_search")
	protocol := selectChatTransport(accountSupportedProtocols, supportsWebSearch || imageCount > 0)
	routeAccounts := rt.accountsForGroups(groupIDs, ownerID, body.Model, string(protocol))
	filteredAccounts := []ChatTransportAccount{}
	for _, account := range routeAccounts {
		if chatTransportAccountSupportsProtocol(account, body.Model, protocol) {
			filteredAccounts = append(filteredAccounts, account)
		}
	}
	routeModelOption := constrainChatModelOptionForAccounts(modelOption, body.Model, filteredAccounts, []ChatTransportProtocol{protocol})
	if len(routeModelOption.SupportedAPIProtocols) == 0 {
		failWith(&ModelCapabilityError{Message: "当前 API Key 没有可用于该模型的对话路由，请切换模型或检查账户映射"})
		return
	}
	effectiveTools := []string{}
	if protocol == ProtocolResponses && supportsWebSearch {
		effectiveTools = append(effectiveTools, HostedToolWebSearch)
	}
	imageGenerationEnabled := gatewayKey.ImageGenerationEnabled && rt.hasChatImageGenerationRoute(groupIDs, ownerID)
	environment := rt.deps.ToolEnvironment
	if environment == "" {
		environment = "development"
	}
	internalToolRegistry := newChatInternalToolRegistry(environment, rt.deps.DiagnosticToolEnabled, imageGenerationEnabled)
	internalTools := internalToolRegistry.resolveTools(containsString(modelOption.SupportedTools, "function_calling"))
	internalToolNames := make([]string, 0, len(internalTools))
	for _, tool := range internalTools {
		internalToolNames = append(internalToolNames, tool.ModelName)
	}
	if imageCount > 0 && (!containsString(modelOption.InputModalities, "image") || protocol != ProtocolResponses) {
		failWith(&RequestError{Code: RequestImageNotSupported, Message: "当前模型或路由不支持图片输入，请切换模型或移除图片"})
		return
	}
	nowValue := rt.now()
	resolvedInput, err := rt.resolveChatAssetInput(body.ContentBlocks, ownerID, conversation.ID, nowValue)
	if err != nil {
		failWith(err)
		return
	}
	if err := assertActive(); err != nil {
		failWith(err)
		return
	}
	reasoningEffort, serviceTier, generationParameters, effectiveContextLimitTokens, err := resolveChatModelRequestOptions(rt, routeModelOption, body)
	if err != nil {
		failWith(err)
		return
	}
	promptCacheKey := ""
	if routeModelOption.SupportsPromptCaching {
		promptCacheKey = buildChatPromptCacheKey(ownerID, apiKey.ID, conversation.ID)
	}
	_, systemInstructionsText, _ := buildChatSystemInstructions(effectiveTools, internalToolNames)
	budgetInput := fixedChatBudgetInput{
		CurrentUserContent: resolveChatBudgetContent(protocol, body.Content, resolvedInput.Blocks),
		Instructions:       systemInstructionsText,
		EffectiveTools:     effectiveTools,
		InternalTools:      internalTools,
		ImageTokenEstimate: resolvedInput.ImageTokenEstimate,
		MaxInputTokens:     effectiveContextLimitTokens,
	}
	if err := rt.validateFixedChatInputBudget(budgetInput); err != nil {
		failWith(err)
		return
	}
	compactionInput := CompactionInput{
		ConversationID:              conversation.ID,
		SystemAccountID:             ownerID,
		APIKeySecret:                apiKey.Secret,
		Model:                       body.Model,
		Protocol:                    protocol,
		EffectiveContextLimitTokens: effectiveContextLimitTokens,
	}
	compactOnce := func() bool {
		if rt.deps.Compactions == nil {
			return false
		}
		result := rt.deps.Compactions.CompactOnce(r.Context(), compactionInput)
		return result.Status == "installed"
	}
	contextCompacted := false
	preparedContext, err := rt.loadChatTransportHistory(protocol, conversation.ID, ownerID, rt.now(), body.ReplaceTurnID)
	if err != nil {
		var contextErr *ChatModelContextError
		if errors.As(err, &contextErr) && contextErr.Reason == ModelContextLoadLimit {
			if compactOnce() {
				contextCompacted = true
				preparedContext, err = rt.loadChatTransportHistory(protocol, conversation.ID, ownerID, rt.now(), body.ReplaceTurnID)
			}
		}
		if err != nil {
			failWith(err)
			return
		}
	}
	if err := assertActive(); err != nil {
		failWith(err)
		return
	}
	if len(preparedContext.UnresolvedAssetIDs) > 0 {
		rt.scheduleImageObservations(ScheduleObservationInput{
			Targets:          preparedContext.UnresolvedAssets,
			ConversationID:   conversation.ID,
			SystemAccountID:  ownerID,
			APIKeySecret:     apiKey.Secret,
			Model:            body.Model,
			UserContent:      "补全此前对话中的图片语义说明",
			AssistantContent: "",
		})
		rt.waitForImageObservations(preparedContext.UnresolvedAssetIDs, 1000)
		if err := assertActive(); err != nil {
			failWith(err)
			return
		}
		preparedContext, err = rt.loadChatTransportHistory(protocol, conversation.ID, ownerID, rt.now(), body.ReplaceTurnID)
		if err != nil {
			failWith(err)
			return
		}
		if len(preparedContext.UnresolvedAssetIDs) > 0 {
			failWith(&ChatModelContextError{Message: "历史图片语义说明仍在生成，请稍后重试", Reason: ModelContextImagePend})
			return
		}
	}
	estimatedRequestTokens := rt.estimateChatInputTokens(budgetInput, preparedContext.History)
	if effectiveContextLimitTokens != nil && int64(estimatedRequestTokens) >= int64(0.85*float64(*effectiveContextLimitTokens)) {
		if compactOnce() {
			contextCompacted = true
			preparedContext, err = rt.loadChatTransportHistory(protocol, conversation.ID, ownerID, rt.now(), body.ReplaceTurnID)
			if err != nil {
				failWith(err)
				return
			}
			estimatedRequestTokens = rt.estimateChatInputTokens(budgetInput, preparedContext.History)
		}
	}
	if effectiveContextLimitTokens != nil && int64(estimatedRequestTokens) > *effectiveContextLimitTokens {
		failWith(&ContextBudgetError{})
		return
	}
	buildTransport := func(continuation []any) (string, []byte, error) {
		path, bodyMap := buildChatTransportRequest(ChatTransportRequestInput{
			Protocol:             protocol,
			Instructions:         systemInstructionsText,
			Model:                body.Model,
			History:              preparedContext.History,
			CurrentContent:       body.Content,
			CurrentBlocks:        resolvedInput.Blocks,
			EffectiveTools:       effectiveTools,
			InternalTools:        internalTools,
			ToolContinuation:     continuation,
			ReasoningEffort:      reasoningEffort,
			ServiceTier:          serviceTier,
			GenerationParameters: generationParameters,
			PromptCacheKey:       promptCacheKey,
		})
		payload, err := json.Marshal(bodyMap)
		if err != nil {
			return "", nil, err
		}
		return path, payload, nil
	}
	_, serializedTransportBody, err := buildTransport(nil)
	if err != nil {
		failWith(err)
		return
	}
	if len(serializedTransportBody) > maxInternalChatRequestBytes && !contextCompacted && len(preparedContext.History) > 0 {
		if compactOnce() {
			contextCompacted = true
			preparedContext, err = rt.loadChatTransportHistory(protocol, conversation.ID, ownerID, rt.now(), body.ReplaceTurnID)
			if err != nil {
				failWith(err)
				return
			}
			estimatedRequestTokens = rt.estimateChatInputTokens(budgetInput, preparedContext.History)
			if effectiveContextLimitTokens != nil && int64(estimatedRequestTokens) > *effectiveContextLimitTokens {
				failWith(&ContextBudgetError{})
				return
			}
			_, serializedTransportBody, err = buildTransport(nil)
			if err != nil {
				failWith(err)
				return
			}
		}
	}
	if len(serializedTransportBody) > maxInternalChatRequestBytes {
		failWith(&RequestError{Code: RequestBodyTooLarge, Message: "本轮模型请求体仍超过安全上限，请减少图片或等待图片说明完成后重试"})
		return
	}
	if !rt.beginAcceptance(conversation.ID, prep) {
		failWith(&PreparationCanceledError{})
		return
	}
	accepted, err = rt.deps.Store.AcceptTurn(AcceptTurnInput{
		ConversationID:          conversation.ID,
		SystemAccountID:         ownerID,
		ClientMessageID:         body.ClientMessageID,
		UserContent:             body.Content,
		ContentBlocks:           body.ContentBlocks,
		Model:                   body.Model,
		Now:                     rt.now(),
		StorageQuotaBytes:       streamStorageQuotaBytes,
		RetentionDays:           rt.deps.RetentionDays,
		MaxTurnsPerConversation: rt.deps.MaxTurnsPerConversation,
		ReplaceTurnID:           body.ReplaceTurnID,
	})
	if err != nil {
		failWith(err)
		return
	}
	if accepted.Duplicate {
		writeMessageCode(w, http.StatusConflict, "该消息已提交，请刷新会话", "chat_message_already_exists")
		return
	}
	identity := ChatGenerationIdentity{
		OwnerID:            ownerID,
		ConversationID:     conversation.ID,
		TurnID:             accepted.TurnID,
		AssistantMessageID: accepted.AssistantMessage.ID,
	}
	runnerContext, runnerCancel := context.WithCancel(r.Context())
	defer runnerCancel()
	options := ChatGenerationRunnerOptions{
		Identity: identity,
		Execute: rt.buildGenerationExecute(generationExecuteInput{
			body:                        body,
			conversation:                conversation,
			ownerID:                     ownerID,
			userMessageID:               accepted.UserMessage.ID,
			protocol:                    protocol,
			apiKey:                      apiKey,
			traceID:                     traceID,
			defaultImageModel:           string(conversation.DefaultImageModel),
			internalToolRegistry:        internalToolRegistry,
			internalTools:               internalTools,
			effectiveTools:              effectiveTools,
			systemInstructionsText:      systemInstructionsText,
			resolvedInput:               resolvedInput,
			preparedContext:             preparedContext,
			reasoningEffort:             reasoningEffort,
			serviceTier:                 serviceTier,
			generationParameters:        generationParameters,
			promptCacheKey:              promptCacheKey,
			effectiveContextLimitTokens: effectiveContextLimitTokens,
			estimatedRequestTokens:      int64(estimatedRequestTokens),
			buildTransport:              buildTransport,
		}, identity),
		UnexpectedErrorTraceID: traceID,
	}
	if rt.deps.Now != nil {
		options.Now = func() string { return isoMillis(rt.deps.Now()) }
	}
	runner = NewChatGenerationRunner(options, runnerCancel, func() bool {
		return runnerContext.Err() != nil
	})
	if rt.deps.Hub == nil || !rt.deps.Hub.Start(runner) {
		_, _ = rt.deps.Store.FailChatTurn(FailTurnInput{
			ConversationID:  conversation.ID,
			SystemAccountID: ownerID,
			TurnID:          accepted.TurnID,
			ErrorCode:       "internal_generation_failed",
			ErrorMessage:    "当前会话生成任务冲突",
			TraceID:         &traceID,
			Now:             rt.now(),
		})
		writeMessageCode(w, http.StatusConflict, "当前会话生成任务冲突", "chat_stream_conflict")
		return
	}
	if prep.isCanceled() {
		runner.Abort()
	}
	var subscriber *sseSubscriber
	var stopHeartbeat func()
	if !responseClosedState(&responseClosedMu, &responseClosed) {
		sse := newChatSSEWriter(w, r.Context(), func() {
			responseClosedMu.Lock()
			responseClosed = true
			responseClosedMu.Unlock()
		})
		prepareSSEResponse(w)
		sse.WriteEvent("message.started", map[string]any{
			"turnId":           accepted.TurnID,
			"userMessage":      accepted.UserMessage,
			"assistantMessage": accepted.AssistantMessage,
		})
		subscriber = &sseSubscriber{writer: sse, detach: func() {
			if rt.deps.Hub != nil {
				rt.deps.Hub.Unsubscribe(GenerationIdentity{OwnerID: ownerID, ConversationID: conversation.ID, TurnID: accepted.TurnID}, subscriber)
			}
		}}
		if rt.deps.Hub.Subscribe(GenerationIdentity{OwnerID: ownerID, ConversationID: conversation.ID, TurnID: accepted.TurnID}, subscriber) {
			stopHeartbeat = startChatSSEHeartbeat(sse, 0, func() {
				if rt.deps.Hub != nil && subscriber != nil {
					rt.deps.Hub.Unsubscribe(GenerationIdentity{OwnerID: ownerID, ConversationID: conversation.ID, TurnID: accepted.TurnID}, subscriber)
				}
				sse.End()
			})
		}
	}
	<-runner.Completion()
	if stopHeartbeat != nil {
		stopHeartbeat()
	}
}

func responseClosedState(mu *sync.Mutex, state *bool) bool {
	mu.Lock()
	defer mu.Unlock()
	return *state
}

// writeStreamRouteError mirrors the stream route catch block.
func writeStreamRouteError(w http.ResponseWriter, err error) {
	var canceled *PreparationCanceledError
	if errors.As(err, &canceled) {
		writeMessageCode(w, 499, "消息准备已取消", "chat_preparation_canceled")
		return
	}
	var conflict *ConflictError
	if errors.As(err, &conflict) {
		writeChatRouteError(w, err)
		return
	}
	var budget *ContextBudgetError
	if errors.As(err, &budget) {
		writeChatRouteError(w, err)
		return
	}
	var request *RequestError
	if errors.As(err, &request) {
		writeChatRouteError(w, err)
		return
	}
	var assetInput *ChatAssetInputError
	if errors.As(err, &assetInput) {
		writeChatRouteError(w, err)
		return
	}
	var capability *ModelCapabilityError
	if errors.As(err, &capability) {
		writeChatRouteError(w, err)
		return
	}
	var contextErr *ChatModelContextError
	if errors.As(err, &contextErr) {
		writeMessageCode(w, http.StatusUnprocessableEntity, contextErr.Message, "chat_model_context_"+string(contextErr.Reason))
		return
	}
	writeChatRouteError(w, err)
}

// beginAcceptance mirrors beginActiveChatAcceptance.
func (rt *chatRoutes) beginAcceptance(conversationID string, prep *activePreparation) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	current := rt.preps[conversationID]
	if current != prep || prep.phase != "preparing" || prep.isCanceled() {
		return false
	}
	prep.phase = "accepting"
	return true
}

// scheduleImageObservations delegates to the observation port when wired.
func (rt *chatRoutes) scheduleImageObservations(input ScheduleObservationInput) {
	if rt.deps.ImageObservation == nil {
		return
	}
	rt.deps.ImageObservation.Schedule(input)
}

func (rt *chatRoutes) waitForImageObservations(assetIDs []string, timeoutMs int64) {
	if rt.deps.ImageObservation == nil {
		return
	}
	rt.deps.ImageObservation.Wait(assetIDs, timeoutMs)
}

// gatewayKeyOrError validates a gateway key with the nil-port contract.
func (d *Deps) gatewayKeyOrError(secret string) (*GatewayKeyView, error) {
	if d.GatewayKeys == nil {
		return nil, &DomainError{Message: "API Key 不存在或不可用"}
	}
	return d.GatewayKeys.ValidateGatewayKey(secret)
}

// chatModelCatalog lists the provider catalog items for the groups.
func (rt *chatRoutes) chatModelCatalog(groupIDs []string, systemAccountID, requestedModel string) []ProviderModelCatalogItem {
	_, catalog := rt.loadChatModelCatalogSnapshot(groupIDs, systemAccountID, requestedModel)
	return catalog
}
