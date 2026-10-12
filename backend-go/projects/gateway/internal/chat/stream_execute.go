package chat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

// Runner execute closure ported from the POST /stream execute block in
// chat.routes.ts: context head snapshot, single model round over the
// GenerationExecutor port, turn completion, context usage recording,
// observation/compaction scheduling and the failure finalize/recover paths.
// 站内模型工具编排（web_search/generate_image/generate_video/generate_audio
// 与 binding_required 引导）已整体退场（PLAN-20261012T121500000Z D2）：
// 一轮 = 一次模型调用 + 收尾。

// chatDispatchTargetAware 是进程内调度覆盖通道在 chat 侧的可选扩展端口：
// 组合根（cmd/juhe-ai-gateway）的 chatGatewayExecutor 实现它，把会话绑定
// 账户绑定到执行器视图并在派发时注入请求 context；未绑定/归档会话不会进入
// 生成链（发送预检拦截），此处未解析出账户时返回原执行器。internal/chat 不
// 引用 cmd 包，经类型断言探测；未实现该端口的执行器（测试 mock）保持现状
// 语义。
type chatDispatchTargetAware interface {
	WithChatDispatchAccount(accountID string) GenerationExecutor
}

// dispatchExecutorOf 解析执行器视图：input.executor 优先（stream_route 已按
// 会话绑定账户解析），为空回落 fallback（rt.deps.Executor）。
func dispatchExecutorOf(input generationExecuteInput, fallback GenerationExecutor) GenerationExecutor {
	if input.executor != nil {
		return input.executor
	}
	return fallback
}

// bindConversationDispatchTarget 按会话绑定账户解析执行器视图：会话绑定
// 账户且执行器实现 chatDispatchTargetAware 时返回绑定目标的视图；无绑定
// 原样返回（不触碰端口）。
func bindConversationDispatchTarget(executor GenerationExecutor, conversation *Conversation) GenerationExecutor {
	if executor == nil || conversation == nil {
		return executor
	}
	accountID := derefString(conversation.BindAccountID)
	if accountID == "" {
		return executor
	}
	aware, ok := executor.(chatDispatchTargetAware)
	if !ok {
		return executor
	}
	return aware.WithChatDispatchAccount(accountID)
}

type generationExecuteInput struct {
	body          *streamMessageBody
	conversation  *Conversation
	ownerID       string
	userMessageID string
	// protocol 是本轮解析出的请求形态（request_protocol.go 按绑定账户供应商
	// 主推协议解析）；Compactions.Schedule 等下游记账沿用该枚举。
	protocol                    ChatTransportProtocol
	apiKey                      *ChatAPIKeyRecord
	executor                    GenerationExecutor
	traceID                     string
	systemInstructionsText      string
	resolvedInput               *resolvedChatInput
	preparedContext             *transportHistory
	reasoningEffort             string
	serviceTier                 string
	generationParameters        *ChatGenerationParameters
	promptCacheKey              string
	effectiveContextLimitTokens *int64
	estimatedRequestTokens      int64
	buildTransport              func() (string, []byte, error)
}

// buildGenerationExecute mirrors the `execute` closure handed to
// ChatGenerationRunner.
func (rt *chatRoutes) buildGenerationExecute(input generationExecuteInput, identity ChatGenerationIdentity) func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
	return func(runCtx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
		var partialContent strings.Builder
		failureCode := GenErrUpstreamHTTP
		messageID := identity.AssistantMessageID
		turnID := identity.TurnID

		failureCode = GenErrUpstreamHTTP
		path, payload, err := input.buildTransport()
		if err != nil {
			return rt.finalizeGenerationFailure(input, identity, runCtx, &partialContent, err, GenErrInternal)
		}
		if len(payload) > maxInternalChatRequestBytes {
			return rt.finalizeGenerationFailure(input, identity, runCtx, &partialContent,
				&RequestError{Code: RequestBodyTooLarge, Message: "本轮模型请求体超过安全上限，请减少上下文后重试"}, GenErrInternal)
		}
		headers := map[string]string{
			"authorization": "Bearer " + input.apiKey.Secret,
			"content-type":  "application/json",
			"accept":        "text/event-stream",
		}
		if input.traceID != "" {
			headers["x-trace-id"] = input.traceID
		}
		var ctx context.Context = context.Background()
		if runCtx.Context != nil {
			ctx = runCtx.Context
		}
		upstream, err := dispatchExecutorOf(input, rt.deps.Executor).Dispatch(ctx, GenerationDispatchRequest{
			Path: path, Method: "POST", Headers: headers, Body: payload,
		})
		if err != nil {
			return rt.finalizeGenerationFailure(input, identity, runCtx, &partialContent, err, failureCode)
		}
		if upstream == nil || upstream.Status < 200 || upstream.Status >= 300 || upstream.Body == nil {
			payloadText := ""
			if upstream != nil && upstream.Body != nil {
				raw, readErr := io.ReadAll(io.LimitReader(upstream.Body, 64*1024))
				_ = upstream.Body.Close()
				if readErr == nil {
					payloadText = string(raw)
				}
			}
			return rt.finalizeGenerationFailure(input, identity, runCtx, &partialContent,
				errors.New(upstreamMessagePayload(payloadText, "模型请求失败（HTTP "+itoa(statusOrZero(upstream))+"）")), failureCode)
		}
		failureCode = GenErrUpstreamStream
		// BUG-0223 防御收口：Body.Close 移入 defer——Collect 内部 panic
		// （由上层 safego 恢复）时本 defer 仍执行，保证上游连接释放；
		// 正常路径的 Close 错误依旧忽略（读取已完成后关闭失败无补救动作）。
		defer func() { _ = upstream.Body.Close() }()
		var collected upstreamModelResult
		var collectErr error
		if input.protocol == ProtocolResponses {
			var responsesResult OpenAIResponsesSseResult
			responsesResult, collectErr = CollectOpenAIResponsesSse(upstream.Body, maxMessageBytes, func(delta string) {
				partialContent.WriteString(delta)
				runCtx.Publish("message.delta", map[string]any{"messageId": messageID, "delta": delta}, ChatGenerationProjectionUpdate{ContentTextDelta: &delta})
			}, func(delta string) {
				runCtx.Publish("reasoning.delta", map[string]any{"messageId": messageID, "delta": delta}, ChatGenerationProjectionUpdate{ReasoningTextDelta: &delta})
			}, 0)
			collected = upstreamModelResult{
				Content:      responsesResult.Content,
				FinishReason: responsesResult.FinishReason,
				InputTokens:  responsesResult.InputTokens,
				OutputTokens: responsesResult.OutputTokens,
			}
		} else {
			var chatResult OpenAIChatSseResult
			chatResult, collectErr = CollectOpenAIChatSse(upstream.Body, maxMessageBytes, func(delta string) {
				partialContent.WriteString(delta)
				runCtx.Publish("message.delta", map[string]any{"messageId": messageID, "delta": delta}, ChatGenerationProjectionUpdate{ContentTextDelta: &delta})
			}, func(delta string) {
				runCtx.Publish("reasoning.delta", map[string]any{"messageId": messageID, "delta": delta}, ChatGenerationProjectionUpdate{ReasoningTextDelta: &delta})
			}, 0)
			collected = upstreamModelResult{
				Content:      chatResult.Content,
				FinishReason: chatResult.FinishReason,
				InputTokens:  chatResult.InputTokens,
				OutputTokens: chatResult.OutputTokens,
			}
		}
		if collectErr != nil {
			return rt.finalizeGenerationFailure(input, identity, runCtx, &partialContent, collectErr, failureCode)
		}
		if runCtx.Aborted() {
			return rt.finalizeGenerationFailure(input, identity, runCtx, &partialContent, &PreparationCanceledError{}, GenErrInternal)
		}
		failureCode = GenErrInternal
		finishReason := collected.FinishReason
		if finishReason == "" {
			finishReason = "stop"
		}
		assistantContent := partialContent.String()
		if assistantContent == "" {
			assistantContent = collected.Content
		}
		persisted := terminalizeAssistantBlocks(runCtx.SnapshotBlocks(), "completed")
		if _, err := rt.deps.Store.CompleteChatTurn(CompleteTurnInput{
			ConversationID:   input.conversation.ID,
			SystemAccountID:  identity.OwnerID,
			TurnID:           turnID,
			AssistantContent: assistantContent,
			FinishReason:     finishReason,
			TraceID:          input.traceID,
			ContentBlocksRaw: persisted,
			Now:              rt.now(),
		}); err != nil {
			// 成功路径落库失败（如 jobs 中断清理已清 active_turn_id）：与失败
			// 路径同型走恢复收口，按权威侧状态收敛轮次并发终止事件，不再
			// 裸 return err 丢弃已生成内容与终止事件。恢复收口按 §13.3 携带
			// 已生成的 assistantContent 落库。
			recovered := rt.recoverChatTurnFinalization(input.conversation.ID, identity.OwnerID, turnID, input.body.ClientMessageID, assistantContent, err)
			switch recovered {
			case "completed":
				// 权威侧已并发收口为 completed：继续成功收尾，保证 usage
				// 记账与 message.completed 终止事件不缺失。
			case "canceled":
				return ChatGenerationTerminalResult{Status: "canceled", Data: map[string]any{"messageId": messageID}}, nil
			default:
				publicError := classifyGenerationError(err, GenErrInternal)
				failureMessage := publicError.Message
				if assistantContent != "" {
					failureMessage = ChatGenerationContinuationMessage(publicError.Code)
				}
				data := map[string]any{"messageId": messageID, "code": string(publicError.Code), "message": failureMessage}
				if input.traceID != "" {
					data["traceId"] = input.traceID
				}
				return ChatGenerationTerminalResult{Status: "failed", Data: data}, nil
			}
		}
		upstreamUsageAvailable := collected.InputTokens != nil
		var activeContextTokens int64
		if upstreamUsageAvailable {
			activeContextTokens = *collected.InputTokens
			if collected.OutputTokens != nil {
				activeContextTokens += *collected.OutputTokens
			} else {
				activeContextTokens += int64(rt.tokenCount(assistantContent))
			}
		} else {
			activeContextTokens = input.estimatedRequestTokens + int64(rt.tokenCount(assistantContent)) + 12
		}
		_, _ = rt.deps.Store.RecordContextUsage(RecordContextUsageInput{
			ConversationID:              input.conversation.ID,
			SystemAccountID:             identity.OwnerID,
			ExpectedContextRevision:     headRevision(rt, input.conversation.ID, identity.OwnerID),
			ActiveContextTokens:         activeContextTokens,
			EffectiveContextLimitTokens: input.effectiveContextLimitTokens,
			UsageEstimated:              !upstreamUsageAvailable,
			Now:                         rt.now(),
		})
		if len(input.resolvedInput.AssetIDs) > 0 {
			targets := make([]ObservationTarget, 0, len(input.resolvedInput.AssetIDs))
			for _, assetID := range input.resolvedInput.AssetIDs {
				targets = append(targets, ObservationTarget{AssetID: assetID, ExpectedTurnID: turnID, ExpectedMessageID: input.userMessageID})
			}
			rt.scheduleImageObservations(ScheduleObservationInput{
				Targets:          targets,
				ConversationID:   input.conversation.ID,
				SystemAccountID:  identity.OwnerID,
				APIKeySecret:     input.apiKey.Secret,
				Model:            input.body.Model,
				Protocol:         input.protocol,
				UserContent:      input.body.Content,
				AssistantContent: assistantContent,
			})
		}
		if input.effectiveContextLimitTokens != nil && *input.effectiveContextLimitTokens != 0 &&
			activeContextTokens >= int64(0.7*float64(*input.effectiveContextLimitTokens)) {
			if rt.deps.Compactions != nil {
				rt.deps.Compactions.Schedule(context.Background(), CompactionInput{
					ConversationID:              input.conversation.ID,
					SystemAccountID:             identity.OwnerID,
					APIKeySecret:                input.apiKey.Secret,
					Model:                       input.body.Model,
					EffectiveContextLimitTokens: input.effectiveContextLimitTokens,
				})
			}
		}
		data := map[string]any{"messageId": messageID, "finishReason": finishReason}
		if input.traceID != "" {
			data["traceId"] = input.traceID
		}
		return ChatGenerationTerminalResult{Status: "completed", Data: data}, nil
	}
}

// finalizeGenerationFailure 收口失败/取消路径（原 execute catch 块）：按取消
// 语义或失败语义终结轮次并返回终态结果。
func (rt *chatRoutes) finalizeGenerationFailure(input generationExecuteInput, identity ChatGenerationIdentity, runCtx *ChatGenerationExecutionContext, partialContent *strings.Builder, execErr error, failureCode PublicChatGenerationErrorCode) (ChatGenerationTerminalResult, error) {
	messageID := identity.AssistantMessageID
	turnID := identity.TurnID
	canceled := runCtx.Aborted() || isPreparationCanceled(execErr)
	publicError := classifyGenerationError(execErr, failureCode)
	// 失败文案按该轮是否已流出半截内容动态选择（AI问答设计 §11.1/§13.1）：
	// 非取消失败且错误码存在续写引导变体时改用变体（§14.1 续写可用），
	// 否则保持原始重试引导；落库 ErrorMessage 与下发 data["message"] 使用
	// 同一值。取消轮以 message.canceled 终态收口、无失败文案，变体仅
	// 作用于失败终态。
	failureMessage := publicError.Message
	if !canceled && partialContent.Len() > 0 {
		if _, ok := continuationGuidanceMessages[publicError.Code]; ok {
			failureMessage = ChatGenerationContinuationMessage(publicError.Code)
		}
	}
	finalizedStatus := "failed"
	if canceled {
		finalizedStatus = "canceled"
	}
	persisted := terminalizeAssistantBlocks(runCtx.SnapshotBlocks(), finalizedStatus)
	var tracePtr *string
	if input.traceID != "" {
		tracePtr = &input.traceID
	}
	var finalizeErr error
	if canceled {
		_, finalizeErr = rt.deps.Store.CancelChatTurn(CancelTurnInput{
			ConversationID:   input.conversation.ID,
			SystemAccountID:  identity.OwnerID,
			TurnID:           turnID,
			AssistantContent: partialContent.String(),
			TraceID:          tracePtr,
			ContentBlocksRaw: persisted,
			Now:              rt.now(),
		})
	} else {
		_, finalizeErr = rt.deps.Store.FailChatTurn(FailTurnInput{
			ConversationID:   input.conversation.ID,
			SystemAccountID:  identity.OwnerID,
			TurnID:           turnID,
			AssistantContent: partialContent.String(),
			ErrorCode:        string(publicError.Code),
			ErrorMessage:     failureMessage,
			TraceID:          tracePtr,
			ContentBlocksRaw: persisted,
			Now:              rt.now(),
		})
	}
	status := finalizedStatus
	if finalizeErr != nil {
		status = rt.recoverChatTurnFinalization(input.conversation.ID, identity.OwnerID, turnID, input.body.ClientMessageID, partialContent.String(), finalizeErr)
	}
	switch status {
	case "canceled":
		return ChatGenerationTerminalResult{Status: "canceled", Data: map[string]any{"messageId": messageID}}, nil
	case "completed":
		return ChatGenerationTerminalResult{Status: "completed", Data: map[string]any{"messageId": messageID}}, nil
	default:
		data := map[string]any{"messageId": messageID, "code": string(publicError.Code), "message": failureMessage}
		if input.traceID != "" {
			data["traceId"] = input.traceID
		}
		return ChatGenerationTerminalResult{Status: "failed", Data: data}, nil
	}
}

// upstreamModelResult 是主轮模型调用的统一结果视图（Chat / Responses 两个
// collector 投影到同一形状）。
type upstreamModelResult struct {
	Content      string
	FinishReason string
	InputTokens  *int64
	OutputTokens *int64
}

func statusOrZero(response *GenerationDispatchResponse) int {
	if response == nil {
		return 0
	}
	return response.Status
}

func headRevision(rt *chatRoutes, conversationID, ownerID string) int64 {
	head, err := rt.deps.Store.GetContextHead(conversationID, ownerID)
	if err != nil || head == nil {
		return 0
	}
	return head.ContextRevision
}

// isPreparationCanceled reports the preparation-cancel sentinel.
func isPreparationCanceled(err error) bool {
	var canceled *PreparationCanceledError
	return errors.As(err, &canceled)
}

// upstreamMessagePayload mirrors upstreamMessage.
func upstreamMessagePayload(payload, fallback string) string {
	var parsed struct {
		Message string `json:"message"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(payload), &parsed); err == nil {
		if parsed.Error != nil && parsed.Error.Message != "" {
			return parsed.Error.Message
		}
		if parsed.Message != "" {
			return parsed.Message
		}
	}
	return fallback
}

// classifyGenerationError mirrors classifyChatGenerationError(error, failureCode).
func classifyGenerationError(err error, failureCode PublicChatGenerationErrorCode) PublicChatGenerationError {
	unknown := ClassifyUnknownChatGenerationError(err)
	if failureCode != GenErrInternal && unknown.Code == GenErrInternal {
		return PublicChatGenerationError{Code: failureCode, Message: ChatGenerationErrorMessage(failureCode)}
	}
	return unknown
}

// recoverChatTurnFinalization mirrors recoverChatTurnFinalization. partialContent
// 是本进程内存中仍持有的该轮正文（成功路径恢复传 assistantContent，失败路径
// 恢复传 partialContent）：权威状态查询（FindTurnByClientMessageID）优先——
// 已终态的轮次直接返回存储状态，不写内容、不覆盖；只有权威侧未终态且条件收口
// 的 CAS（active_turn_id 匹配 + assistant 仍 streaming）命中时，正文才随
// interrupted 收口写入（§13.3 不得丢弃已流出内容），因此并发收口方（stop、
// jobs 中断清理）与本写入不会交叠。partialContent 为空时保持既有无内容语义。
func (rt *chatRoutes) recoverChatTurnFinalization(conversationID, ownerID, turnID, clientMessageID, partialContent string, initialError error) string {
	lastError := initialError
	for attempt := 0; attempt < 3; attempt++ {
		authoritative, err := rt.deps.Store.FindTurnByClientMessageID(conversationID, ownerID, clientMessageID)
		if err != nil {
			lastError = err
		} else if authoritative != nil && authoritative.TurnID == turnID && authoritative.AssistantStatus != StatusStreaming {
			return string(authoritative.AssistantStatus)
		}
		interrupted, err := rt.deps.Store.FailInterruptedTurnIfMatches(CancelIfMatchesInput{
			ConversationID:     conversationID,
			SystemAccountID:    ownerID,
			ExpectedTurnID:     turnID,
			InterruptedContent: partialContent,
			Now:                rt.now(),
		})
		if err != nil {
			lastError = err
		} else if interrupted.State == CancelStateAlreadyTerminal {
			return string(interrupted.AssistantStatus)
		}
		if attempt < 2 {
			time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
		}
	}
	_ = lastError
	return "failed"
}
