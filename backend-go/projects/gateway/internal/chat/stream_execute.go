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
// chat.routes.ts: context head snapshot, internal tool orchestration with
// model rounds over the GenerationExecutor port, turn completion, context
// usage recording, observation/compaction scheduling and the failure
// finalize/recover paths.

type generationExecuteInput struct {
	body                        *streamMessageBody
	conversation                *Conversation
	ownerID                     string
	userMessageID               string
	protocol                    ChatTransportProtocol
	apiKey                      *ChatAPIKeyRecord
	traceID                     string
	defaultImageModel           string
	internalToolRegistry        *chatInternalToolRegistry
	internalTools               []*toolDefinition
	effectiveTools              []string
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
}

// buildGenerationExecute mirrors the `execute` closure handed to
// ChatGenerationRunner.
func (rt *chatRoutes) buildGenerationExecute(input generationExecuteInput, identity ChatGenerationIdentity) func(ctx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
	return func(runCtx *ChatGenerationExecutionContext) (ChatGenerationTerminalResult, error) {
		var partialContent strings.Builder
		contentBlocks := []*assistantBlock{}
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
			upstream, err := rt.deps.Executor.Dispatch(ctx, GenerationDispatchRequest{
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
			if input.protocol == ProtocolResponses {
				collected, collectErr := CollectChatResponsesSse(upstream.Body, maxMessageBytes, 0, func(event ChatResponsesEvent) error {
					return projectResponsesEvent(event, messageID, runCtx, &partialContent, &contentBlocks)
				}, nil)
				_ = upstream.Body.Close()
				if collectErr != nil {
					return ChatToolModelTurn{}, collectErr
				}
				return ChatToolModelTurn{
					Content:           collected.Content,
					FinishReason:      finishReasonOr(collected.ToolCalls, "stop"),
					ContinuationItems: collected.ContinuationItems,
					ToolCalls:         collected.ToolCalls,
					InputTokens:       collected.InputTokens,
					OutputTokens:      collected.OutputTokens,
				}, nil
			}
			collected, collectErr := CollectOpenAIChatSse(upstream.Body, maxMessageBytes, func(delta string) {
				partialContent.WriteString(delta)
				runCtx.Publish("message.delta", map[string]any{"messageId": messageID, "delta": delta}, ChatGenerationProjectionUpdate{ContentTextDelta: &delta})
			}, 0)
			_ = upstream.Body.Close()
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
		toolContext := &chatToolExecutionContext{
			OwnerID:            identity.OwnerID,
			ConversationID:     input.conversation.ID,
			TurnID:             turnID,
			AssistantMessageID: messageID,
			TraceID:            input.traceID,
			APIKey:             input.apiKey.Secret,
			DefaultImageModel:  input.defaultImageModel,
			Aborted:            runCtx.Aborted,
			LoadImageEditReferences: func(assetIDs []string) ([]ChatImageEditReference, error) {
				return rt.deps.Store.loadImageEditReferences(rt.deps.ObjectStore, assetIDs, identity.OwnerID, input.conversation.ID, rt.now())
			},
			ImageGeneration: func(request ChatImageGenerationRequest) (ChatImageGenerationToolResult, error) {
				failureCode = GenErrImageFailed
				var ctx context.Context = context.Background()
				if runCtx.Context != nil {
					ctx = runCtx.Context
				}
				generated, err := GenerateChatImage(ctx, rt.deps.Executor, request, input.apiKey.Secret, input.traceID)
				if err != nil {
					return ChatImageGenerationToolResult{}, err
				}
				if generated.MimeType == "" || generated.Width == 0 || generated.Height == 0 {
					return ChatImageGenerationToolResult{}, errors.New("生成图片缺少有效 MIME 或尺寸")
				}
				return generated, nil
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
		}
		orchestrator := newChatInternalToolOrchestrator(input.internalToolRegistry, input.internalTools, toolContext, ChatOrchestratorLimits{MaxModelRounds: 4, MaxToolCalls: 8, MaxImageCalls: 2}, publishTool)
		failureCode = GenErrInternal
		result, execErr := func() (result ChatToolOrchestratorResult, err error) {
			defer func() {
				if recovered := recover(); recovered != nil {
					err = errors.New("generation panicked")
				}
			}()
			return orchestrator.Run(input.protocol, invokeModel)
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
				return ChatGenerationTerminalResult{}, err
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
						Protocol:                    input.protocol,
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

func finishReasonOr(toolCalls []ChatToolCall, fallback string) string {
	if len(toolCalls) > 0 {
		return "tool_calls"
	}
	return fallback
}

func headRevision(rt *chatRoutes, conversationID, ownerID string) int64 {
	head, err := rt.deps.Store.GetContextHead(conversationID, ownerID)
	if err != nil || head == nil {
		return 0
	}
	return head.ContextRevision
}

func isPreparationCanceled(err error) bool {
	var canceled *PreparationCanceledError
	return errors.As(err, &canceled)
}

// projectResponsesEvent mirrors the responses onEvent projection.
func projectResponsesEvent(event ChatResponsesEvent, messageID string, runCtx *ChatGenerationExecutionContext, partialContent *strings.Builder, contentBlocks *[]*assistantBlock) error {
	switch event.Type {
	case "text_delta":
		partialContent.WriteString(event.Delta)
		delta := event.Delta
		runCtx.Publish("message.delta", map[string]any{"messageId": messageID, "delta": delta}, ChatGenerationProjectionUpdate{ContentTextDelta: &delta})
	case "reasoning_delta":
		delta := event.Delta
		runCtx.Publish("reasoning.delta", map[string]any{"messageId": messageID, "delta": delta}, ChatGenerationProjectionUpdate{ReasoningTextDelta: &delta})
	case "reasoning_completed":
		runCtx.Publish("reasoning.completed", map[string]any{"messageId": messageID}, ChatGenerationProjectionUpdate{ReasoningCompleted: true})
	case "tool_started", "tool_updated", "tool_completed":
		if isApplicationFunctionToolEvent(event.Item) {
			return nil
		}
		eventType := event.Type
		projectToolEvent(contentBlocks, eventType, event.Item)
		publishType := strings.Replace(eventType, "_", ".", 1)
		toolEvent := chatGenerationToolEventProjection(eventType, event.Item)
		runCtx.Publish(publishType, map[string]any{"messageId": messageID, "item": event.Item}, ChatGenerationProjectionUpdate{ToolEvent: toolEvent})
	case "failed":
		return errors.New(upstreamMessagePayload(mustJSON(event.Error), "模型工具调用失败"))
	}
	return nil
}

func mustJSON(value any) string {
	payload, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(payload)
}

// isApplicationFunctionToolEvent mirrors isApplicationFunctionToolEvent.
func isApplicationFunctionToolEvent(item map[string]any) bool {
	itemType, _ := item["type"].(string)
	return itemType == "function_call" || strings.HasPrefix(itemType, "response.function_call_arguments.")
}

// chatGenerationToolEventProjection mirrors chatGenerationToolEvent.
func chatGenerationToolEventProjection(eventType string, item map[string]any) *ChatGenerationToolEvent {
	id := "tool"
	if value := stringOrAny(item, "id", "callId", "call_id"); value != "" {
		id = value
	}
	toolType := "tool"
	if value := stringOrAny(item, "type"); value != "" {
		toolType = value
	}
	status := "started"
	switch eventType {
	case "tool_updated":
		status = "updated"
	case "tool_completed":
		status = "completed"
	}
	return &ChatGenerationToolEvent{ID: id, ToolType: toolType, Status: status, Item: item}
}

func stringOrAny(item map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := item[key].(string); ok {
			return value
		}
	}
	return ""
}

// projectToolEvent mirrors projectToolEvent over the execute-local blocks.
func projectToolEvent(blocks *[]*assistantBlock, eventType string, item map[string]any) {
	id := ""
	if value := stringOrAny(item, "id", "callId", "call_id"); value != "" {
		id = value
	}
	if id == "" {
		count := 0
		for _, block := range *blocks {
			if block.Type == "tool_call" {
				count++
			}
		}
		id = "tool_" + itoa(count+1)
	}
	status := "started"
	switch eventType {
	case "tool_updated":
		status = "updated"
	case "tool_completed":
		status = "completed"
	}
	for _, block := range *blocks {
		if block.Type == "tool_call" && block.CallID == id {
			block.Status = status
			if toolType := stringOrAny(item, "type"); toolType != "" {
				block.ToolType = toolType
			}
			block.Item = item
			return
		}
	}
	block := &assistantBlock{
		Type: "tool_call", BlockID: "assistant_block_" + itoa(len(*blocks)+1), Order: int64(len(*blocks) + 1),
		CallID: id, ToolType: stringOrAny(item, "type"), Status: status, Item: item,
	}
	if block.ToolType == "" {
		block.ToolType = "tool"
	}
	*blocks = append(*blocks, block)
}

// publishApplicationToolEvent mirrors publishApplicationToolEvent.
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
