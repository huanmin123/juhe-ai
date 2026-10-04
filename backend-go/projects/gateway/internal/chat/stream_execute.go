package chat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// Runner execute closure ported from the POST /stream execute block in
// chat.routes.ts: context head snapshot, internal tool orchestration with
// model rounds over the GenerationExecutor port, turn completion, context
// usage recording, observation/compaction scheduling and the failure
// finalize/recover paths.

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
	// protocol 恒 chat_completions（工具体系设计 §11.1）；保留字段仅为
	// Compactions.Schedule 等下游记账的协议枚举来源。
	protocol ChatTransportProtocol
	apiKey   *ChatAPIKeyRecord
	// executor 是本轮生成的上游派发执行器：会话绑定账户的调度覆盖视图
	//（stream_route 经 chatDispatchTargetAware 端口解析）；nil 回落
	// rt.deps.Executor。
	executor                    GenerationExecutor
	traceID                     string
	defaultImageModel           string
	internalToolRegistry        *chatInternalToolRegistry
	internalTools               []*toolDefinition
	systemInstructionsText      string
	resolvedInput               *resolvedChatInput
	preparedContext             *transportHistory
	reasoningEffort             string
	serviceTier                 string
	generationParameters        *ChatGenerationParameters
	promptCacheKey              string
	effectiveContextLimitTokens *int64
	estimatedRequestTokens      int64
	buildTransport              func(continuation []any) (string, []byte, error)
	// toolBindings 是模型工具（web_search/generate_image）的会话绑定运行时
	// 视图（候选 + 绑定目标 + 派发执行器），stream_route 解析后注入。
	toolBindings *chatToolBindingRuntime
}

// buildGenerationExecute mirrors the `execute` closure handed to
// ChatGenerationRunner.
func (rt *chatRoutes) buildGenerationExecute(input generationExecuteInput, identity ChatGenerationIdentity) func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
	return func(runCtx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
		var partialContent strings.Builder
		failureCode := GenErrInternal
		messageID := identity.AssistantMessageID
		turnID := identity.TurnID

		invokeModel := func(round int, continuation []any) (ChatToolModelTurn, error) {
			path, payload, err := input.buildTransport(continuation)
			if err != nil {
				return ChatToolModelTurn{}, err
			}
			if len(payload) > maxInternalChatRequestBytes {
				return ChatToolModelTurn{}, &RequestError{Code: RequestBodyTooLarge, Message: "工具续答请求体超过安全上限，请减少上下文后重试"}
			}
			failureCode = GenErrUpstreamHTTP
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
				return ChatToolModelTurn{}, err
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
				return ChatToolModelTurn{}, errors.New(upstreamMessagePayload(payloadText, "模型请求失败（HTTP "+itoa(statusOrZero(upstream))+")"))
			}
			failureCode = GenErrUpstreamStream
			// BUG-0223 防御收口：Body.Close 移入 defer——Collect 内部 panic
			// （由上层 safego 恢复）时本 defer 仍执行，保证上游连接释放；
			// 正常路径的 Close 错误依旧忽略（读取已完成后关闭失败无补救动作）。
			defer func() { _ = upstream.Body.Close() }()
			collected, collectErr := CollectOpenAIChatSse(upstream.Body, maxMessageBytes, func(delta string) {
				partialContent.WriteString(delta)
				runCtx.Publish("message.delta", map[string]any{"messageId": messageID, "delta": delta}, ChatGenerationProjectionUpdate{ContentTextDelta: &delta})
			}, func(delta string) {
				runCtx.Publish("reasoning.delta", map[string]any{"messageId": messageID, "delta": delta}, ChatGenerationProjectionUpdate{ReasoningTextDelta: &delta})
			}, 0)
			if collectErr != nil {
				return ChatToolModelTurn{}, collectErr
			}
			return ChatToolModelTurn{
				Content:           collected.Content,
				FinishReason:      collected.FinishReason,
				ContinuationItems: collected.ContinuationItems,
				ToolCalls:         collected.ToolCalls,
				InputTokens:       collected.InputTokens,
				OutputTokens:      collected.OutputTokens,
			}, nil
		}

		publishTool := func(event ChatToolExecutionEvent) {
			publishApplicationToolEvent(runCtx.Publish, messageID, event)
		}
		var toolContext *chatToolExecutionContext
		toolContext = &chatToolExecutionContext{
			OwnerID:            identity.OwnerID,
			ConversationID:     input.conversation.ID,
			TurnID:             turnID,
			AssistantMessageID: messageID,
			TraceID:            input.traceID,
			APIKey:             input.apiKey.Secret,
			DefaultImageModel:  input.defaultImageModel,
			Aborted:            runCtx.Aborted,
			LoadImageEditReferences: func(assetIDs []string) ([]ChatImageEditReference, error) {
				return loadImageEditReferences(rt.deps.AssetEditReferences, rt.deps.ObjectStore, assetIDs, identity.OwnerID, input.conversation.ID, rt.now())
			},
			ConstrainImageModel: func(model string) string {
				bindings := input.toolBindings
				if bindings == nil {
					return model
				}
				return constrainChatImageModel(model, bindings.ImageAccountID, input.defaultImageModel, bindings.ImageCandidates)
			},
			DefaultVideoModel: string(input.conversation.DefaultVideoModel),
			DefaultAudioModel: string(input.conversation.DefaultAudioModel),
			ConstrainVideoModel: func(model string) string {
				bindings := input.toolBindings
				if bindings == nil {
					return model
				}
				return constrainChatMediaModel(model, bindings.VideoAccountID, string(input.conversation.DefaultVideoModel), bindings.VideoCandidates)
			},
			ConstrainAudioModel: func(model string) string {
				bindings := input.toolBindings
				if bindings == nil {
					return model
				}
				return constrainChatMediaModel(model, bindings.AudioAccountID, string(input.conversation.DefaultAudioModel), bindings.AudioCandidates)
			},
			VideoGeneration: func(request ChatVideoCreationRequest) (ChatVideoCreationResult, error) {
				// M7 视频工具（问答音视频工具设计 §4.4）：未绑定账户 →
				// binding_required 引导；已绑定 → 经 /v1 链固定派发绑定账户创建
				// 任务（立即返回，等待由 media-tasks 轮询驱动）。
				bindings := input.toolBindings
				if bindings == nil || bindings.VideoAccountID == "" {
					return ChatVideoCreationResult{}, &chatToolBindingRequiredError{ToolName: "generate_video", UserHint: chatVideoBindingHint, Candidates: chatToolBindingCandidatesOf(bindings, "generate_video")}
				}
				request.Model = constrainChatMediaModel(request.Model, bindings.VideoAccountID, string(input.conversation.DefaultVideoModel), bindings.VideoCandidates)
				if request.Model == "" {
					return ChatVideoCreationResult{}, errors.New("视频生成模型不能为空")
				}
				var ctx context.Context = context.Background()
				if runCtx.Context != nil {
					ctx = runCtx.Context
				}
				return CreateChatVideo(ctx, bindings.VideoExecutor, request, input.apiKey.Secret, input.traceID)
			},
			AudioGeneration: func(request ChatAudioGenerationRequest) (ChatMediaArtifactResult, error) {
				// M7 音频工具（TTS 同步）：未绑定 → binding_required 引导；
				// 已绑定 → 经 /v1 链固定派发绑定账户合成。
				bindings := input.toolBindings
				if bindings == nil || bindings.AudioAccountID == "" {
					return ChatMediaArtifactResult{}, &chatToolBindingRequiredError{ToolName: "generate_audio", UserHint: chatAudioBindingHint, Candidates: chatToolBindingCandidatesOf(bindings, "generate_audio")}
				}
				request.Model = constrainChatMediaModel(request.Model, bindings.AudioAccountID, string(input.conversation.DefaultAudioModel), bindings.AudioCandidates)
				if request.Model == "" {
					return ChatMediaArtifactResult{}, errors.New("语音合成模型不能为空")
				}
				var ctx context.Context = context.Background()
				if runCtx.Context != nil {
					ctx = runCtx.Context
				}
				return GenerateChatAudio(ctx, bindings.AudioExecutor, request, input.apiKey.Secret, input.traceID)
			},
			ImageGeneration: func(request ChatImageGenerationRequest) (ChatImageGenerationToolResult, error) {
				// 生图模型工具（契约 §6.2）：未绑定账户 → binding_required 引导；
				// 已绑定 → 经 /v1 链固定派发绑定账户（不做旧路由派发兼容）。
				bindings := input.toolBindings
				if bindings == nil || bindings.ImageAccountID == "" {
					return ChatImageGenerationToolResult{}, &chatToolBindingRequiredError{ToolName: "generate_image", UserHint: chatImageBindingHint, Candidates: chatToolBindingCandidatesOf(bindings, "generate_image")}
				}
				failureCode = GenErrImageFailed
				var ctx context.Context = context.Background()
				if runCtx.Context != nil {
					ctx = runCtx.Context
				}
				// url 回退下载按绑定账户的 proxy_profile 出站（端口 nil 或
				// 账户未绑代理时返回 nil，走直连默认）。
				var downloadClient *http.Client
				if rt.deps.ImageDownloadProxy != nil {
					downloadClient = rt.deps.ImageDownloadProxy(bindings.ImageAccountID)
				}
				generated, err := GenerateChatImage(ctx, bindings.ImageExecutor, request, input.apiKey.Secret, input.traceID, downloadClient)
				if err != nil {
					return ChatImageGenerationToolResult{}, err
				}
				if generated.MimeType == "" || generated.Width == 0 || generated.Height == 0 {
					return ChatImageGenerationToolResult{}, errors.New("生成图片缺少有效 MIME 或尺寸")
				}
				return generated, nil
			},
			WebSearch: func(query string) (chatToolExecutionResult, error) {
				// web_search 模型工具（契约 §6.1）：未绑定 → binding_required
				// 引导；已绑定 → 子代理经 /v1 链固定派发绑定「账户+模型」。
				bindings := input.toolBindings
				if bindings == nil || bindings.SearchAccountID == "" || bindings.SearchModelID == "" {
					return chatToolExecutionResult{}, &chatToolBindingRequiredError{ToolName: "web_search", UserHint: chatWebSearchBindingHint, Candidates: chatToolBindingCandidatesOf(bindings, "web_search")}
				}
				// 过程增量经 context.ToolProgress 渐进上报（闭包引用预声明的
				// toolContext 变量，调用发生在构造完成后）。
				var progress func(chatWebSearchProgress)
				if toolContext != nil {
					progress = toolContext.ToolProgress
				}
				return executeChatWebSearch(runCtx, bindings.SearchExecutor, input.apiKey.Secret, input.traceID, bindings.SearchModelID, query, progress)
			},
		}
		if rt.deps.ObjectStore != nil {
			toolContext.ArtifactSink = &storeGeneratedImageSink{
				routes:             rt,
				ownerID:            identity.OwnerID,
				conversationID:     input.conversation.ID,
				turnID:             turnID,
				assistantMessageID: messageID,
				nextContentOrder:   func() int64 { return int64(len(runCtx.SnapshotBlocks())) },
			}
			toolContext.MediaArtifactSink = &storeGeneratedMediaSink{
				routes:             rt,
				ownerID:            identity.OwnerID,
				conversationID:     input.conversation.ID,
				turnID:             turnID,
				assistantMessageID: messageID,
				nextContentOrder:   func() int64 { return int64(len(runCtx.SnapshotBlocks())) },
			}
		}
		orchestrator := newChatInternalToolOrchestrator(input.internalToolRegistry, input.internalTools, toolContext, ChatOrchestratorLimits{MaxModelRounds: 4, MaxToolCalls: 8, MaxImageCalls: 2}, publishTool)
		failureCode = GenErrInternal
		result, execErr := func() (result ChatToolOrchestratorResult, err error) {
			defer func() {
				if recovered := recover(); recovered != nil {
					err = errors.New("generation panicked")
				}
			}()
			return orchestrator.Run(invokeModel)
		}()
		if execErr == nil {
			failureCode = GenErrInternal
			finishReason := result.FinishReason
			if finishReason == "" {
				finishReason = "stop"
			}
			assistantContent := partialContent.String()
			if assistantContent == "" {
				assistantContent = result.Content
			}
			if runCtx.Aborted() {
				return ChatGenerationTerminalResult{}, &PreparationCanceledError{}
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
				// 裸 return err 丢弃已生成内容与终止事件。
				recovered := rt.recoverChatTurnFinalization(input.conversation.ID, identity.OwnerID, turnID, input.body.ClientMessageID, err)
				switch recovered {
				case "completed":
					// 权威侧已并发收口为 completed：继续成功收尾，保证 usage
					// 记账与 message.completed 终止事件不缺失。
				case "canceled":
					return ChatGenerationTerminalResult{Status: "canceled", Data: map[string]any{"messageId": messageID}}, nil
				default:
					publicError := classifyGenerationError(err, GenErrInternal)
					data := map[string]any{"messageId": messageID, "code": string(publicError.Code), "message": publicError.Message}
					if input.traceID != "" {
						data["traceId"] = input.traceID
					}
					return ChatGenerationTerminalResult{Status: "failed", Data: data}, nil
				}
			}
			upstreamUsageAvailable := result.InputTokens != nil
			var activeContextTokens int64
			if upstreamUsageAvailable {
				activeContextTokens = *result.InputTokens
				if result.OutputTokens != nil {
					activeContextTokens += *result.OutputTokens
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

		// Failure path (mirrors the execute catch block).
		canceled := runCtx.Aborted() || isPreparationCanceled(execErr)
		publicError := classifyGenerationError(execErr, failureCode)
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
				ErrorMessage:     publicError.Message,
				TraceID:          tracePtr,
				ContentBlocksRaw: persisted,
				Now:              rt.now(),
			})
		}
		status := finalizedStatus
		if finalizeErr != nil {
			status = rt.recoverChatTurnFinalization(input.conversation.ID, identity.OwnerID, turnID, input.body.ClientMessageID, finalizeErr)
		}
		switch status {
		case "canceled":
			return ChatGenerationTerminalResult{Status: "canceled", Data: map[string]any{"messageId": messageID}}, nil
		case "completed":
			return ChatGenerationTerminalResult{Status: "completed", Data: map[string]any{"messageId": messageID}}, nil
		default:
			data := map[string]any{"messageId": messageID, "code": string(publicError.Code), "message": publicError.Message}
			if input.traceID != "" {
				data["traceId"] = input.traceID
			}
			return ChatGenerationTerminalResult{Status: "failed", Data: data}, nil
		}
	}
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

// publishApplicationToolEvent mirrors publishApplicationToolEvent. Status
// "binding_required" 渲染为 tool.binding_required 引导事件（契约 §8.4，纯事件
// 不驱动内容块投影——tool_call 块状态机不含该状态）。
func publishApplicationToolEvent(publish func(string, map[string]any, ChatGenerationProjectionUpdate) bool, messageID string, event ChatToolExecutionEvent) {
	item := map[string]any{"type": event.ToolName, "executionOwner": "application"}
	for key, value := range event.PublicResult {
		item[key] = value
	}
	if event.Reused {
		item["reused"] = true
	}
	if event.ErrorCode != "" {
		item["errorCode"] = event.ErrorCode
	}
	if event.ErrorMessage != "" {
		item["errorMessage"] = event.ErrorMessage
	}
	if event.Status == "binding_required" {
		callItem := map[string]any{"callId": event.CallID}
		for key, value := range item {
			callItem[key] = value
		}
		publish("tool.binding_required", map[string]any{"messageId": messageID, "item": callItem}, ChatGenerationProjectionUpdate{})
		return
	}
	projection := ChatGenerationProjectionUpdate{ToolEvent: &ChatGenerationToolEvent{
		ID: event.CallID, ToolType: event.ToolName, Status: event.Status, Item: item,
	}}
	assetID := ""
	if value, ok := event.PublicResult["assetId"].(string); ok {
		assetID = value
	}
	if event.Status == "completed" && event.ToolName == "generate_image" && assetID != "" {
		projection.ImageEvent = &ChatGenerationImageEvent{
			ID:     event.CallID,
			Status: "completed",
			Item: map[string]any{
				"assetId":       assetID,
				"mimeType":      event.PublicResult["mimeType"],
				"width":         event.PublicResult["width"],
				"height":        event.PublicResult["height"],
				"revisedPrompt": event.PublicResult["revisedPrompt"],
			},
		}
	}
	// M7 媒体块投影（问答音视频工具设计 §3）：generate_audio 完成 → output_audio
	//（按 assetId）；generate_video 完成 → output_media_task（按 jobId，任务状态
	// 原样携带——异步任务在轮次结束后仍由 media-tasks 轮询结算推进）。
	if event.Status == "completed" && event.ToolName == "generate_audio" && assetID != "" {
		projection.MediaEvent = &ChatGenerationMediaEvent{
			Block:  "audio",
			ID:     event.CallID,
			Status: "completed",
			Item: map[string]any{
				"assetId":  assetID,
				"mimeType": event.PublicResult["mimeType"],
			},
		}
	}
	if event.Status == "completed" && event.ToolName == "generate_video" {
		jobID := ""
		if value, ok := event.PublicResult["jobId"].(string); ok {
			jobID = value
		}
		if jobID != "" {
			taskStatus := "queued"
			if value, ok := event.PublicResult["status"].(string); ok && value != "" {
				taskStatus = value
			}
			mediaTaskItem := map[string]any{
				"jobId":         jobID,
				"kind":          "video",
				"status":        taskStatus,
				"model":         event.PublicResult["model"],
				"promptSummary": event.PublicResult["promptSummary"],
			}
			if progress, ok := event.PublicResult["progress"]; ok && progress != nil {
				mediaTaskItem["progress"] = progress
			}
			projection.MediaEvent = &ChatGenerationMediaEvent{
				Block:  "media_task",
				ID:     event.CallID,
				Status: taskStatus,
				Item:   mediaTaskItem,
			}
		}
	}
	callItem := map[string]any{"callId": event.CallID}
	for key, value := range item {
		callItem[key] = value
	}
	publish("tool."+event.Status, map[string]any{"messageId": messageID, "item": callItem}, projection)
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
	var imageErr *ChatImageGenerationRequestError
	if errors.As(err, &imageErr) {
		return PublicChatGenerationError{Code: imageErr.Code, Message: ChatGenerationErrorMessage(imageErr.Code)}
	}
	unknown := ClassifyUnknownChatGenerationError(err)
	if failureCode != GenErrInternal && unknown.Code == GenErrInternal {
		return PublicChatGenerationError{Code: failureCode, Message: ChatGenerationErrorMessage(failureCode)}
	}
	return unknown
}

// recoverChatTurnFinalization mirrors recoverChatTurnFinalization.
func (rt *chatRoutes) recoverChatTurnFinalization(conversationID, ownerID, turnID, clientMessageID string, initialError error) string {
	lastError := initialError
	for attempt := 0; attempt < 3; attempt++ {
		authoritative, err := rt.deps.Store.FindTurnByClientMessageID(conversationID, ownerID, clientMessageID)
		if err != nil {
			lastError = err
		} else if authoritative != nil && authoritative.TurnID == turnID && authoritative.AssistantStatus != StatusStreaming {
			return string(authoritative.AssistantStatus)
		}
		interrupted, err := rt.deps.Store.FailInterruptedTurnIfMatches(CancelIfMatchesInput{
			ConversationID:  conversationID,
			SystemAccountID: ownerID,
			ExpectedTurnID:  turnID,
			Now:             rt.now(),
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

// storeGeneratedImageSink mirrors createChatGeneratedImageArtifactSink.
type storeGeneratedImageSink struct {
	routes             *chatRoutes
	ownerID            string
	conversationID     string
	turnID             string
	assistantMessageID string
	nextContentOrder   func() int64
}

// CommitGeneratedImage writes the original + preview objects and commits the
// asset row (original + preview stored through the ObjectStore port).
func (s *storeGeneratedImageSink) CommitGeneratedImage(input GeneratedImageCommitInput) (GeneratedImageCommitResult, error) {
	result := GeneratedImageCommitResult{}
	if s.routes.deps.ObjectStore == nil || s.routes.deps.ImageProcessor == nil {
		return result, errors.New("图片工具运行时未配置图像适配器或资产接收器")
	}
	preview, err := s.routes.deps.ImageProcessor.CreatePreview(input.Result.Data)
	if err != nil {
		var processingErr *ImageProcessingError
		if errors.As(err, &processingErr) {
			return result, err
		}
		return result, &ImageProcessingError{Message: "生成图片预览失败，请重试"}
	}
	assetID := s.routes.deps.Store.newID("asset")
	originalKey := StorageKeyForChatAsset(assetID, input.Result.SHA256, input.Result.MimeType, "original")
	previewKey := StorageKeyForChatAsset(assetID, preview.SHA256, preview.MimeType, "preview")
	if err := s.routes.deps.ObjectStore.Write(originalKey, input.Result.Data, chatAssetGeneratedMaxBytes, input.Result.SHA256); err != nil {
		return result, err
	}
	if err := s.routes.deps.ObjectStore.Write(previewKey, preview.Buffer, chatAssetPreviewMaxBytes, preview.SHA256); err != nil {
		_ = s.routes.deps.ObjectStore.Delete([]string{originalKey})
		return result, err
	}
	nowValue := s.routes.now()
	asset, err := s.routes.deps.Store.CommitChatGeneratedAsset(GeneratedAssetCommitInput{
		ID:                assetID,
		SystemAccountID:   s.ownerID,
		ConversationID:    s.conversationID,
		TurnID:            s.turnID,
		MessageID:         s.assistantMessageID,
		ContentOrder:      s.nextContentOrder(),
		MimeType:          input.Result.MimeType,
		Width:             input.Result.Width,
		Height:            input.Result.Height,
		Bytes:             input.Result.Bytes,
		Sha256:            input.Result.SHA256,
		StorageKey:        originalKey,
		PreviewMimeType:   preview.MimeType,
		PreviewWidth:      preview.Width,
		PreviewHeight:     preview.Height,
		PreviewBytes:      preview.ByteSize,
		PreviewSha256:     preview.SHA256,
		PreviewStorageKey: previewKey,
		Now:               nowValue,
		RetentionDays:     s.routes.deps.RetentionDays,
		Generation: GeneratedImageGenerationRecord{
			Operation:      input.Operation,
			Model:          input.Model,
			Prompt:         input.Prompt,
			SourceAssetIDs: input.SourceAssetIDs,
			Size:           input.Size,
			Quality:        input.Quality,
			OutputFormat:   input.OutputFormat,
		},
	})
	if err != nil {
		_ = s.routes.deps.ObjectStore.Delete([]string{originalKey, previewKey})
		return result, err
	}
	return GeneratedImageCommitResult{
		AssetID:         asset.ID,
		MimeType:        derefString(asset.ProcessedMimeType),
		Width:           derefAssistantI64(asset.ProcessedWidth),
		Height:          derefAssistantI64(asset.ProcessedHeight),
		Bytes:           derefAssistantI64(asset.ProcessedBytes),
		PreviewMimeType: derefString(asset.PreviewMimeType),
		PreviewWidth:    derefAssistantI64(asset.PreviewWidth),
		PreviewHeight:   derefAssistantI64(asset.PreviewHeight),
		PreviewBytes:    derefAssistantI64(asset.PreviewBytes),
	}, nil
}

func derefAssistantI64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
