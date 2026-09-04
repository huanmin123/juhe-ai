package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Internal tool registry + orchestrator + builtin tools ported from
// tools/registry.ts, tools/orchestrator.ts, tools/executors/*,
// chat-image-generation-transport.ts and chat-image-policy.ts. Limits, error
// codes, public fallback messages and the model-round contract mirror Node.

// ChatToolExecutionOutput mirrors ChatToolExecutionOutput.
type ChatToolExecutionOutput struct {
	CallID       string
	ToolName     string
	ModelOutput  string
	PublicResult map[string]any
	Reused       bool
}

// ChatToolExecutionEvent mirrors ChatToolExecutionEvent.
type ChatToolExecutionEvent struct {
	Status       string // started|completed|failed|canceled
	CallID       string
	ToolName     string
	PublicResult map[string]any
	ErrorCode    string
	ErrorMessage string
	Reused       bool
}

type chatToolExecutionResult struct {
	ModelOutput  string
	PublicResult map[string]any
}

// chatInternalToolRegistry mirrors ChatInternalToolRegistry for the two
// builtin tools; schema validation is specialized per tool instead of AJV
// (identical rejection codes/messages for the same inputs).
type chatInternalToolRegistry struct {
	definitions map[string]*toolDefinition
}

func newChatInternalToolRegistry(environment string, internalToolsEnabled, imageGenerationEnabled bool) *chatInternalToolRegistry {
	registry := &chatInternalToolRegistry{definitions: map[string]*toolDefinition{}}
	echo := newDiagnosticEchoTool()
	if availableInEnvironment(echo.Environments, environment) &&
		(!echo.RequiresInternalToolsEnabled || internalToolsEnabled) {
		registry.definitions[echo.ModelName] = echo
	}
	image := newGenerateImageTool()
	if !image.RequiresImageGenerationEnabled || imageGenerationEnabled {
		registry.definitions[image.ModelName] = image
	}
	return registry
}

func availableInEnvironment(environments []string, environment string) bool {
	for _, candidate := range environments {
		if candidate == environment {
			return true
		}
	}
	return false
}

func (r *chatInternalToolRegistry) definition(toolName string) (*toolDefinition, error) {
	definition, ok := r.definitions[toolName]
	if !ok {
		return nil, &chatInternalToolError{Code: "tool_not_available", Message: "请求的工具当前不可用"}
	}
	return definition, nil
}

func (r *chatInternalToolRegistry) resolveTools(functionCalling bool) []*toolDefinition {
	if !functionCalling {
		return []*toolDefinition{}
	}
	names := []string{"diagnostic_echo", "generate_image"}
	out := []*toolDefinition{}
	for _, name := range names {
		if definition, ok := r.definitions[name]; ok {
			out = append(out, definition)
		}
	}
	return out
}

// normalizeArguments validates a call payload against the builtin schemas
// with AJV-equivalent rejection codes.
func (r *chatInternalToolRegistry) normalizeArguments(toolName, argumentsJSON string, maxArgumentBytes int) (map[string]any, error) {
	if len(argumentsJSON) > maxArgumentBytes {
		return nil, &chatInternalToolError{Code: "tool_arguments_too_large", Message: "工具参数超过 " + itoa(maxArgumentBytes) + " 字节上限"}
	}
	var value any
	if err := json.Unmarshal([]byte(argumentsJSON), &value); err != nil {
		return nil, &chatInternalToolError{Code: "tool_arguments_invalid_json", Message: "工具参数不是有效 JSON"}
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, &chatInternalToolError{Code: "tool_arguments_invalid", Message: "工具参数无效：根值必须是对象"}
	}
	return object, nil
}

type chatInternalToolError struct {
	Code    string
	Message string
}

func (e *chatInternalToolError) Error() string { return e.Message }

var publicToolErrorMessages = map[string]string{
	"tool_not_available":                 "请求的工具当前不可用",
	"tool_arguments_too_large":           "工具参数超过允许上限",
	"tool_arguments_invalid_json":        "工具参数不是有效 JSON",
	"tool_arguments_invalid":             "工具参数不符合要求",
	"tool_timeout":                       "工具执行超时",
	"tool_result_too_large":              "工具结果超过允许上限",
	"tool_call_limit_exceeded":           "本轮工具调用次数已达到上限",
	"image_tool_call_limit_exceeded":     "本轮图片生成次数已达到上限",
	"image_generation_not_enabled":       "可用上游分组未开通图片生成功能",
	"image_generation_permission_denied": "上游拒绝了图片生成权限",
	"image_generation_rate_limited":      "上游图片生成请求过于频繁，请稍后重试",
	"image_generation_request_rejected":  "上游拒绝了本次图片参数或内容",
	"image_generation_failed":            "图片生成失败",
}

func chatToolErrorMessage(code string) string {
	if message, ok := publicToolErrorMessages[code]; ok {
		return message
	}
	return "工具执行失败"
}

// newDiagnosticEchoTool mirrors createDiagnosticEchoTool.
func newDiagnosticEchoTool() *toolDefinition {
	return &toolDefinition{
		ID:          "diagnostic.echo",
		Version:     "1.0.0",
		ModelName:   "diagnostic_echo",
		Description: "仅在开发和测试环境回显一段有界文本，用于验证内部工具调用链。",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"text": map[string]any{"type": "string", "minLength": 1, "maxLength": 1024}},
			"required":             []string{"text"},
			"additionalProperties": false,
		},
		MaxArgumentBytes:             4 * 1024,
		MaxResultBytes:               4 * 1024,
		TimeoutMs:                    1000,
		Environments:                 []string{"development", "test"},
		RequiresInternalToolsEnabled: true,
		DuplicatePolicy:              "reuse_exact",
		Execute: func(input map[string]any, _ *chatToolExecutionContext) (chatToolExecutionResult, error) {
			text := ""
			if input != nil {
				if value, ok := input["text"].(string); ok {
					text = value
				} else if input["text"] != nil {
					text = fmt.Sprint(input["text"])
				}
			}
			publicResult := map[string]any{"echoedText": text}
			payload, _ := json.Marshal(publicResult)
			return chatToolExecutionResult{ModelOutput: string(payload), PublicResult: publicResult}, nil
		},
	}
}

// newGenerateImageTool mirrors createGenerateImageTool.
func newGenerateImageTool() *toolDefinition {
	return &toolDefinition{
		ID:          "image.generate",
		Version:     "2.0.0",
		ModelName:   "generate_image",
		Description: "根据用户需求生成图片，或使用同一会话中明确的 assetId 编辑既有图片。编辑时必须传 reference_asset_ids；无法唯一判断目标图片时先询问用户。请根据用户需求直接设置 size、quality 和 output_format；size 可用 auto 或满足 Schema 描述约束的 WIDTHxHEIGHT。未设置 size 时使用 auto，未设置 quality 时使用 auto，未设置 output_format 时使用 WebP。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":              map[string]any{"type": "string", "enum": []string{"auto", "generate", "edit"}},
				"prompt":              map[string]any{"type": "string", "minLength": 1, "maxLength": 65536},
				"reference_asset_ids": map[string]any{"type": "array", "minItems": 1, "maxItems": 5, "uniqueItems": true, "items": map[string]any{"type": "string", "pattern": "^chat_asset_[a-f0-9]{32}$"}},
				"model":               map[string]any{"type": "string", "enum": []string{"gpt-image-2"}},
				"size":                map[string]any{"type": "string", "pattern": "^(?:auto|[1-9]\\d{1,3}x[1-9]\\d{1,3})$", "description": "使用 auto，或 WIDTHxHEIGHT；宽高必须是 16 的倍数，最长边不超过 3840px，比例不超过 3:1，总像素为 655360..8294400。"},
				"quality":             map[string]any{"type": "string", "enum": []string{"auto", "low", "medium", "high"}},
				"output_format":       map[string]any{"type": "string", "enum": []string{"webp", "png", "jpeg"}},
			},
			"required":             []string{"prompt"},
			"additionalProperties": false,
		},
		MaxArgumentBytes:               96 * 1024,
		MaxResultBytes:                 16 * 1024,
		TimeoutMs:                      900 * 1000,
		RequiresImageGenerationEnabled: true,
		DuplicatePolicy:                "reuse_exact",
		Execute:                        executeGenerateImageTool,
	}
}

var chatAssetIDPattern = regexp.MustCompile(`^chat_asset_[a-f0-9]{32}$`)

func executeGenerateImageTool(input map[string]any, context *chatToolExecutionContext) (chatToolExecutionResult, error) {
	if context.ImageGeneration == nil || context.ArtifactSink == nil {
		return chatToolExecutionResult{}, errors.New("图片工具运行时未配置图像适配器或资产接收器")
	}
	prompt := ""
	if value, ok := input["prompt"].(string); ok {
		prompt = strings.TrimSpace(value)
	}
	if prompt == "" {
		return chatToolExecutionResult{}, errors.New("图像提示词不能为空")
	}
	referenceAssetIDs, err := normalizeReferenceAssetIDs(input["reference_asset_ids"])
	if err != nil {
		return chatToolExecutionResult{}, err
	}
	requestedAction := "auto"
	if input["action"] != nil {
		if value, ok := input["action"].(string); ok {
			requestedAction = value
		} else {
			requestedAction = fmt.Sprint(input["action"])
		}
	}
	if requestedAction != "auto" && requestedAction != "generate" && requestedAction != "edit" {
		return chatToolExecutionResult{}, errors.New("图片操作类型无效")
	}
	operation := "generate"
	if requestedAction == "edit" || (requestedAction == "auto" && len(referenceAssetIDs) > 0) {
		operation = "edit"
	}
	if operation == "edit" && len(referenceAssetIDs) == 0 {
		return chatToolExecutionResult{}, errors.New("编辑图片必须至少引用一张来源图片")
	}
	if operation == "generate" && len(referenceAssetIDs) > 0 {
		return chatToolExecutionResult{}, errors.New("生成图片不能携带来源图片，请改用 edit 或 auto")
	}
	imageModel := context.DefaultImageModel
	if imageModel == "" {
		imageModel = "gpt-image-2"
	}
	if input["model"] != nil {
		if value, ok := input["model"].(string); ok && value != "" {
			imageModel = value
		}
	}
	if imageModel != "gpt-image-2" {
		return chatToolExecutionResult{}, fmt.Errorf("图像模型 %s 不受支持", imageModel)
	}
	references := []ChatImageEditReference{}
	if operation == "edit" {
		if context.LoadImageEditReferences == nil {
			return chatToolExecutionResult{}, errors.New("图片工具运行时未配置编辑引用装载器")
		}
		references, err = context.LoadImageEditReferences(referenceAssetIDs)
		if err != nil {
			return chatToolExecutionResult{}, err
		}
	}
	size, err := normalizeChatImageSize(input["size"])
	if err != nil {
		return chatToolExecutionResult{}, &chatInternalToolError{Code: "tool_arguments_invalid", Message: err.Error()}
	}
	quality, err := normalizeChatImageQuality(input["quality"])
	if err != nil {
		return chatToolExecutionResult{}, err
	}
	outputFormat, err := normalizeChatImageOutputFormat(input["output_format"])
	if err != nil {
		return chatToolExecutionResult{}, err
	}
	generated, err := context.ImageGeneration(ChatImageGenerationRequest{
		Operation:    operation,
		Model:        imageModel,
		Prompt:       prompt,
		Size:         size,
		Quality:      quality,
		OutputFormat: outputFormat,
		References:   references,
	})
	if err != nil {
		return chatToolExecutionResult{}, err
	}
	artifact, err := context.ArtifactSink.CommitGeneratedImage(GeneratedImageCommitInput{
		Result:         generated,
		Operation:      operation,
		Model:          imageModel,
		Prompt:         prompt,
		SourceAssetIDs: referenceAssetIDs,
		Size:           size,
		Quality:        quality,
		OutputFormat:   outputFormat,
	})
	if err != nil {
		return chatToolExecutionResult{}, err
	}
	payload := map[string]any{
		"assetId":         artifact.AssetID,
		"mimeType":        artifact.MimeType,
		"width":           artifact.Width,
		"height":          artifact.Height,
		"bytes":           artifact.Bytes,
		"previewMimeType": artifact.PreviewMimeType,
		"previewWidth":    artifact.PreviewWidth,
		"previewHeight":   artifact.PreviewHeight,
		"previewBytes":    artifact.PreviewBytes,
		"operation":       operation,
		"model":           imageModel,
		"sourceAssetIds":  toAnySlice(referenceAssetIDs),
		"size":            size,
		"outputFormat":    outputFormat,
	}
	if generated.RevisedPrompt != "" {
		payload["revisedPrompt"] = generated.RevisedPrompt
	}
	modelOutput, _ := json.Marshal(payload)
	return chatToolExecutionResult{ModelOutput: string(modelOutput), PublicResult: payload}, nil
}

func normalizeReferenceAssetIDs(value any) ([]string, error) {
	if value == nil {
		return []string{}, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, errors.New("引用图片必须是 assetId 数组")
	}
	assetIDs := []string{}
	for _, item := range list {
		text, ok := item.(string)
		if !ok {
			text = fmt.Sprint(item)
		}
		assetIDs = append(assetIDs, trimSpace(text))
	}
	for _, assetID := range assetIDs {
		if !chatAssetIDPattern.MatchString(assetID) {
			return nil, errors.New("引用图片 assetId 无效")
		}
	}
	if len(assetIDs) > 5 {
		return nil, errors.New("编辑图片最多引用 5 张来源图片")
	}
	if len(uniqueStrings(assetIDs)) != len(assetIDs) {
		return nil, errors.New("引用图片不能重复")
	}
	return assetIDs, nil
}

// --- chat-image-policy.ts normalizers ---

const (
	imageMinPixels      = 655360
	imageMaxPixels      = 8294400
	imageMaxEdge        = 3840
	imageMaxAspectRatio = 3
)

func normalizeChatImageOutputFormat(value any) (string, error) {
	if value == nil {
		return "webp", nil
	}
	normalized := strings.ToLower(trimSpace(fmt.Sprint(value)))
	switch normalized {
	case "", "webp":
		return "webp", nil
	case "png":
		return "png", nil
	case "jpeg", "jpg":
		return "jpeg", nil
	}
	return "", errors.New("图片输出格式只支持 WebP、PNG 或 JPEG")
}

func normalizeChatImageQuality(value any) (string, error) {
	if value == nil {
		return "auto", nil
	}
	normalized := strings.ToLower(trimSpace(fmt.Sprint(value)))
	switch normalized {
	case "":
		return "auto", nil
	case "auto", "low", "medium", "high":
		return normalized, nil
	}
	return "", errors.New("图片质量只支持 auto、low、medium 或 high")
}

func normalizeChatImageSize(value any) (string, error) {
	if value == nil {
		return "auto", nil
	}
	normalized := strings.ToLower(trimSpace(fmt.Sprint(value)))
	if normalized == "" || normalized == "auto" {
		return "auto", nil
	}
	match := sizePattern.FindStringSubmatch(normalized)
	if match == nil {
		return "", errors.New("图片尺寸必须是 auto 或 WIDTHxHEIGHT 格式")
	}
	width, height := parseInt(match[1]), parseInt(match[2])
	if width%16 != 0 || height%16 != 0 {
		return "", errors.New("图片宽高必须是 16px 的倍数")
	}
	longer, shorter := width, height
	if longer < shorter {
		longer, shorter = shorter, longer
	}
	if longer > imageMaxEdge {
		return "", fmt.Errorf("图片最长边不能超过 %dpx", imageMaxEdge)
	}
	if longer/shorter > imageMaxAspectRatio {
		return "", errors.New("图片长短边比例不能超过 3:1")
	}
	pixels := width * height
	if pixels < imageMinPixels || pixels > imageMaxPixels {
		return "", fmt.Errorf("图片总像素必须在 655,360 到 8,294,400 之间")
	}
	return fmt.Sprintf("%dx%d", width, height), nil
}

func parseInt(value string) int {
	out := 0
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return 0
		}
		out = out*10 + int(ch-'0')
	}
	return out
}

// --- orchestrator (tools/orchestrator.ts) ---

// ChatToolModelTurn mirrors ChatToolModelTurn.
type ChatToolModelTurn struct {
	Content           string
	FinishReason      string
	ContinuationItems []any
	ToolCalls         []ChatToolCall
	InputTokens       *int64
	OutputTokens      *int64
}

// ChatToolOrchestratorResult mirrors ChatToolOrchestratorResult.
type ChatToolOrchestratorResult struct {
	Content      string
	FinishReason string
	ModelRounds  int
	ToolCalls    int
	InputTokens  *int64
	OutputTokens *int64
}

type chatInternalToolOrchestrator struct {
	registry            *chatInternalToolRegistry
	tools               []*toolDefinition
	context             *chatToolExecutionContext
	maxModelRounds      int
	maxToolCalls        int
	maxImageCalls       int
	publish             func(event ChatToolExecutionEvent)
	seenResults         map[string]ChatToolExecutionOutput
	totalToolCalls      int
	imageCalls          int
	correctableFailures int
}

// ChatOrchestratorLimits mirrors the Node limits object.
type ChatOrchestratorLimits struct {
	MaxModelRounds int
	MaxToolCalls   int
	MaxImageCalls  int
}

func newChatInternalToolOrchestrator(
	registry *chatInternalToolRegistry,
	tools []*toolDefinition,
	context *chatToolExecutionContext,
	limits ChatOrchestratorLimits,
	publish func(event ChatToolExecutionEvent),
) *chatInternalToolOrchestrator {
	return &chatInternalToolOrchestrator{
		registry:       registry,
		tools:          tools,
		context:        context,
		maxModelRounds: limits.MaxModelRounds,
		maxToolCalls:   limits.MaxToolCalls,
		maxImageCalls:  limits.MaxImageCalls,
		publish:        publish,
		seenResults:    map[string]ChatToolExecutionOutput{},
	}
}

// Run mirrors ChatInternalToolOrchestrator.run.
func (o *chatInternalToolOrchestrator) Run(protocol ChatTransportProtocol, invokeModel func(round int, continuation []any) (ChatToolModelTurn, error)) (ChatToolOrchestratorResult, error) {
	continuation := []any{}
	var inputTokens, outputTokens *int64
	for round := 1; ; round++ {
		if o.context.Aborted != nil && o.context.Aborted() {
			return ChatToolOrchestratorResult{}, errors.New("工具执行已取消")
		}
		if round > o.maxModelRounds {
			return ChatToolOrchestratorResult{}, fmt.Errorf("模型请求轮次超过 %d", o.maxModelRounds)
		}
		turn, err := invokeModel(round, continuation)
		if err != nil {
			return ChatToolOrchestratorResult{}, err
		}
		if turn.InputTokens != nil {
			inputTokens = turn.InputTokens
		}
		if turn.OutputTokens != nil {
			outputTokens = turn.OutputTokens
		}
		if len(turn.ToolCalls) == 0 {
			return ChatToolOrchestratorResult{
				Content:      turn.Content,
				FinishReason: turn.FinishReason,
				ModelRounds:  round,
				ToolCalls:    o.totalToolCalls,
				InputTokens:  inputTokens,
				OutputTokens: outputTokens,
			}, nil
		}
		if round >= o.maxModelRounds {
			return ChatToolOrchestratorResult{}, fmt.Errorf("工具循环达到模型请求轮次上限 %d", o.maxModelRounds)
		}
		outputs, err := o.executeCalls(turn.ToolCalls)
		if err != nil {
			return ChatToolOrchestratorResult{}, err
		}
		continuation = buildChatToolContinuation(protocol, turn.ContinuationItems, outputs)
	}
}

func (o *chatInternalToolOrchestrator) executeCalls(calls []ChatToolCall) ([]ChatToolExecutionOutput, error) {
	ordered := append([]ChatToolCall{}, calls...)
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0 && ordered[j].SourceOrder < ordered[j-1].SourceOrder; j-- {
			ordered[j], ordered[j-1] = ordered[j-1], ordered[j]
		}
	}
	outputs := []ChatToolExecutionOutput{}
	for _, call := range ordered {
		if o.context.Aborted != nil && o.context.Aborted() {
			return nil, errors.New("工具执行已取消")
		}
		toolName := call.ToolName
		o.publishEvent(ChatToolExecutionEvent{Status: "started", CallID: call.CallID, ToolName: toolName})
		result, executed, execErr := o.executeCall(call)
		if execErr != nil {
			canceled := (o.context.Aborted != nil && o.context.Aborted()) || isAbortError(execErr)
			errorCode := "tool_execution_failed"
			errorMessage := ""
			if canceled {
				errorCode = "canceled"
				errorMessage = "工具执行已取消"
			} else {
				errorCode = chatToolErrorCode(execErr)
				errorMessage = publicChatDiagnostic(execErr, chatToolErrorMessage(errorCode))
			}
			o.publishEvent(ChatToolExecutionEvent{
				Status: canceledTerm(canceled), CallID: call.CallID, ToolName: toolName,
				ErrorCode: errorCode, ErrorMessage: errorMessage,
			})
			if canceled {
				return nil, execErr
			}
			if o.correctableFailures >= 1 {
				return nil, execErr
			}
			o.correctableFailures++
			failurePayload := map[string]any{"ok": false, "error": map[string]any{"code": errorCode, "message": errorMessage}}
			failureJSON, _ := json.Marshal(failurePayload)
			outputs = append(outputs, ChatToolExecutionOutput{CallID: call.CallID, ToolName: toolName, ModelOutput: string(failureJSON)})
			continue
		}
		if executed {
			outputs = append(outputs, result)
		}
	}
	return outputs, nil
}

func canceledTerm(canceled bool) string {
	if canceled {
		return "canceled"
	}
	return "failed"
}

func (o *chatInternalToolOrchestrator) executeCall(call ChatToolCall) (ChatToolExecutionOutput, bool, error) {
	o.totalToolCalls++
	if o.totalToolCalls > o.maxToolCalls {
		return ChatToolExecutionOutput{}, false, &chatInternalToolError{Code: "tool_call_limit_exceeded", Message: fmt.Sprintf("工具调用次数超过 %d", o.maxToolCalls)}
	}
	if call.ToolName == "generate_image" {
		o.imageCalls++
		if o.imageCalls > o.maxImageCalls {
			return ChatToolExecutionOutput{}, false, &chatInternalToolError{Code: "image_tool_call_limit_exceeded", Message: fmt.Sprintf("单轮图片生成调用次数超过 %d", o.maxImageCalls)}
		}
	}
	definition, err := o.registry.definition(call.ToolName)
	if err != nil {
		return ChatToolExecutionOutput{}, false, err
	}
	normalized, err := o.registry.normalizeArguments(call.ToolName, call.ArgumentsJSON, definition.MaxArgumentBytes)
	if err != nil {
		return ChatToolExecutionOutput{}, false, err
	}
	cacheKey := definition.ModelName + "@" + definition.Version + ":" + call.ArgumentsJSON
	if definition.DuplicatePolicy == "reuse_exact" {
		if cached, ok := o.seenResults[cacheKey]; ok {
			reused := ChatToolExecutionOutput{CallID: call.CallID, ToolName: cached.ToolName, ModelOutput: cached.ModelOutput, PublicResult: cached.PublicResult, Reused: true}
			o.publishEvent(ChatToolExecutionEvent{Status: "completed", CallID: call.CallID, ToolName: definition.ModelName, PublicResult: reused.PublicResult, Reused: true})
			return reused, true, nil
		}
	}
	result, err := definition.Execute(normalized, o.context)
	if err != nil {
		return ChatToolExecutionOutput{}, false, err
	}
	output := ChatToolExecutionOutput{CallID: call.CallID, ToolName: definition.ModelName, ModelOutput: result.ModelOutput, PublicResult: result.PublicResult}
	o.seenResults[cacheKey] = output
	o.publishEvent(ChatToolExecutionEvent{Status: "completed", CallID: call.CallID, ToolName: definition.ModelName, PublicResult: result.PublicResult})
	return output, true, nil
}

func (o *chatInternalToolOrchestrator) publishEvent(event ChatToolExecutionEvent) {
	if o.publish != nil {
		o.publish(event)
	}
}

func isAbortError(err error) bool {
	type aborter interface{ AbortError() bool }
	var target aborter
	if errors.As(err, &target) {
		return target.AbortError()
	}
	return false
}

func chatToolErrorCode(err error) string {
	var toolErr *chatInternalToolError
	if errors.As(err, &toolErr) {
		return toolErr.Code
	}
	var schemaErr *chatToolSchemaError
	if errors.As(err, &schemaErr) {
		return schemaErr.Code
	}
	return "tool_execution_failed"
}

type chatToolSchemaError struct {
	Code    string
	Message string
}

func (e *chatToolSchemaError) Error() string { return e.Message }

// publicChatDiagnostic mirrors publicChatDiagnosticMessage: bounded sanitized
// diagnostic detail appended to the fallback message.
func publicChatDiagnostic(err error, fallback string) string {
	if err == nil {
		return fallback
	}
	sanitized := sanitizeChatDiagnosticMessage(trimSpace(err.Error()))
	if sanitized == "" || sanitized == fallback {
		return fallback
	}
	return fallback + "；详情：" + sanitized
}
