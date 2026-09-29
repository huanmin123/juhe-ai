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
	// web_search 是常驻模型工具（契约 §6.1）：不受环境/内部工具开关限制，
	// 会话未绑定时经 chatToolBindingRequiredError 引导。
	registry.definitions["web_search"] = newWebSearchTool()
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
	names := []string{"diagnostic_echo", "web_search", "generate_image"}
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

// chatToolBindingRequiredError 表示模型工具（kind=model）被调用但会话未绑定
// 执行目标（契约 §8.4/§9）：orchestrator 捕获后下发 SSE tool.binding_required
// 引导事件，并把 UserHint 作为明确 tool result 回喂主模型（不中断轮次）。
type chatToolBindingRequiredError struct {
	ToolName string
	// UserHint 是回喂主模型的 tool result 文案。
	UserHint string
	// Candidates 是绑定候选摘要（tool.binding_required 事件 data）。
	Candidates []ChatToolBindingCandidate
}

func (e *chatToolBindingRequiredError) Error() string {
	return "模型工具未绑定: " + e.ToolName
}

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
		Kind:        "code",
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

// chatWebSearchBindingHint 是 web_search 未绑定时回喂主模型的 tool result 文案
// （契约 §8.4：主模型据此自然告知用户）。
const chatWebSearchBindingHint = "搜索工具未配置：当前会话尚未绑定搜索模型，无法执行联网搜索。请直接告知用户在会话设置中绑定搜索模型后重试，不要编造搜索结果。"

// chatImageBindingHint 是 generate_image 未绑定时回喂主模型的 tool result 文案。
const chatImageBindingHint = "图片生成工具未配置：当前会话尚未绑定生图账户，无法生成图片。请直接告知用户在会话设置中绑定生图账户后重试，不要编造生成结果。"

// newWebSearchTool 注册 web_search 模型工具（契约 §6.1）：参数只有搜索词——
// 由主模型根据用户问题生成；执行经 chatToolExecutionContext.WebSearch 端口
// （组装侧按会话绑定派发子代理，未绑定返回 chatToolBindingRequiredError）。
func newWebSearchTool() *toolDefinition {
	return &toolDefinition{
		ID:          "web.search",
		Version:     "1.0.0",
		ModelName:   "web_search",
		Kind:        "model",
		Description: "联网搜索工具：针对用户问题里你不确定或有时效性要求的信息（新闻、天气、价格、版本、最新事实等），提炼一个简明的搜索词调用本工具，返回搜索结果摘要与来源 URL。回答实时性问题前应先调用；确信已知的稳定知识不必搜索。",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"query": map[string]any{"type": "string", "minLength": 1, "maxLength": 2000}},
			"required":             []string{"query"},
			"additionalProperties": false,
		},
		MaxArgumentBytes: 8 * 1024,
		MaxResultBytes:   chatWebSearchResultMaxBytes + 1024,
		TimeoutMs:        chatWebSearchTimeoutMs,
		DuplicatePolicy:  "reuse_exact",
		Execute: func(input map[string]any, context *chatToolExecutionContext) (chatToolExecutionResult, error) {
			if context.WebSearch == nil {
				return chatToolExecutionResult{}, &chatToolBindingRequiredError{ToolName: "web_search", UserHint: chatWebSearchBindingHint}
			}
			query := ""
			if input != nil {
				if value, ok := input["query"].(string); ok {
					query = value
				} else if input["query"] != nil {
					query = fmt.Sprint(input["query"])
				}
			}
			query = strings.TrimSpace(query)
			if query == "" {
				return chatToolExecutionResult{}, &chatInternalToolError{Code: "tool_arguments_invalid", Message: "搜索词不能为空"}
			}
			return context.WebSearch(query)
		},
	}
}

// newGenerateImageTool mirrors createGenerateImageTool. 工具体系设计 §6.2：
// kind=model——执行依赖会话的「生图账户 + 生图模型」绑定（image_account_id +
// default_image_model）；绑定检查在组装侧 ImageGeneration 回调完成，未绑定返回
// chatToolBindingRequiredError（不保留旧路由派发兼容）。
func newGenerateImageTool() *toolDefinition {
	return &toolDefinition{
		ID:          "image.generate",
		Version:     "2.0.0",
		ModelName:   "generate_image",
		Kind:        "model",
		Description: "根据用户需求生成图片，或使用同一会话中明确的 assetId 编辑既有图片。编辑时必须传 reference_asset_ids；无法唯一判断目标图片时先询问用户。请根据用户需求直接设置 size、quality 和 output_format；size 可用 auto 或满足 Schema 描述约束的 WIDTHxHEIGHT。未设置 size 时使用 auto，未设置 quality 时使用 auto，未设置 output_format 时使用 WebP。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":              map[string]any{"type": "string", "enum": []string{"auto", "generate", "edit"}},
				"prompt":              map[string]any{"type": "string", "minLength": 1, "maxLength": 65536},
				"reference_asset_ids": map[string]any{"type": "array", "minItems": 1, "maxItems": 5, "uniqueItems": true, "items": map[string]any{"type": "string", "pattern": "^chat_asset_[a-f0-9]{32}$"}},
				"model":               map[string]any{"type": "string", "enum": chatImageModelEnumValues()},
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
		imageModel = string(ImageModelGPTImage2)
	}
	if input["model"] != nil {
		if value, ok := input["model"].(string); ok && value != "" {
			imageModel = value
		}
	}
	if !IsSupportedChatImageModel(imageModel) {
		return chatToolExecutionResult{}, fmt.Errorf("图像模型 %s 不受支持", imageModel)
	}
	// BUG-0230：主模型自选的模型可能不在会话生图绑定账户的可路由集合内
	//（派发层会以 model_unsupported 拒绝），组装侧注入的收敛回调把它折回
	// 集合内目标；后续请求、资产落库与工具结果全部使用收敛后的模型。
	if context.ConstrainImageModel != nil {
		imageModel = context.ConstrainImageModel(imageModel)
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

// Run mirrors ChatInternalToolOrchestrator.run（主对话恒 chat_completions，
// 续答形状不再随协议分叉）。
func (o *chatInternalToolOrchestrator) Run(invokeModel func(round int, continuation []any) (ChatToolModelTurn, error)) (ChatToolOrchestratorResult, error) {
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
		continuation = buildChatToolContinuation(turn.ContinuationItems, outputs)
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
		started := ChatToolExecutionEvent{Status: "started", CallID: call.CallID, ToolName: toolName}
		if toolName == "generate_image" {
			// 生图无流式过程（Images API），started 即带生成中阶段供前端
			// 过程区展示（契约 §10.3；progress 结构 stage 通用）。
			started.PublicResult = map[string]any{"progress": chatWebSearchProgress{Stage: "generating"}}
		}
		o.publishEvent(started)
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
	// 过程增量端口按本次 callID 绑定（契约 §10.3）：执行器上报的 progress 经
	// 内容块投影通道以 item.progress 渐进下发（瞬态，落库前剥离）；调用串行
	// 执行，defer 清理防泄漏到下一次调用。
	previousProgress := o.context.ToolProgress
	o.context.ToolProgress = func(progress chatWebSearchProgress) {
		payload := map[string]any{"progress": progress}
		o.publishEvent(ChatToolExecutionEvent{Status: "updated", CallID: call.CallID, ToolName: definition.ModelName, PublicResult: payload})
	}
	defer func() { o.context.ToolProgress = previousProgress }()
	result, err := definition.Execute(normalized, o.context)
	if err != nil {
		var bindingErr *chatToolBindingRequiredError
		if errors.As(err, &bindingErr) {
			// 模型工具未绑定（契约 §9）：下发 tool.binding_required 引导事件，
			// 把 UserHint 作为明确 tool result 回喂主模型，轮次继续。
			publicResult := map[string]any{"toolId": definition.ModelName}
			if len(bindingErr.Candidates) > 0 {
				publicResult["candidates"] = bindingErr.Candidates
			}
			o.publishEvent(ChatToolExecutionEvent{
				Status: "binding_required", CallID: call.CallID, ToolName: definition.ModelName,
				ErrorCode: "tool_binding_required", ErrorMessage: bindingErr.UserHint, PublicResult: publicResult,
			})
			failurePayload := map[string]any{"ok": false, "error": map[string]any{"code": "tool_binding_required", "message": bindingErr.UserHint}}
			failureJSON, _ := json.Marshal(failurePayload)
			output := ChatToolExecutionOutput{CallID: call.CallID, ToolName: definition.ModelName, ModelOutput: string(failureJSON)}
			o.seenResults[cacheKey] = output
			return output, true, nil
		}
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
