package gatewayresponse

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayanthropic"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// 协议包私有形状 → gatewayproto 契约形状的只读转换。

func anthropicUsageToProto(usage *gatewayanthropic.ParsedUsage) gatewayproto.ParsedUsage {
	if usage == nil {
		return gatewayproto.ParsedUsage{}
	}
	return gatewayproto.ParsedUsage{
		UpstreamResponseModel: usage.UpstreamResponseModel,
		ServiceTier:           usage.ServiceTier,
		InputTokens:           usage.InputTokens,
		OutputTokens:          usage.OutputTokens,
		CacheReadTokens:       usage.CacheReadTokens,
		CacheWriteTokens:      usage.CacheWriteTokens,
		CacheWrite1hTokens:    usage.CacheWrite1hTokens,
		ThinkingTokens:        usage.ThinkingTokens,
		InputImageTokens:      usage.InputImageTokens,
		OutputImageTokens:     usage.OutputImageTokens,
		InputAudioTokens:      usage.InputAudioTokens,
		OutputAudioTokens:     usage.OutputAudioTokens,
		OutputImageCount:      usage.OutputImageCount,
	}
}

func anthropicUsageValueToProto(usage gatewayanthropic.ParsedUsage) gatewayproto.ParsedUsage {
	return anthropicUsageToProto(&usage)
}

func convertAnthropicFrames(frames []gatewayanthropic.ResponseSemanticFrame) []gatewayproto.SemanticFrame {
	if frames == nil {
		return nil
	}
	out := make([]gatewayproto.SemanticFrame, 0, len(frames))
	for _, frame := range frames {
		converted := gatewayproto.SemanticFrame{
			FrameType:      frame.FrameType,
			Protocol:       frame.Protocol,
			EndpointFamily: gatewayproto.ResponseEndpointFamily(frame.EndpointFamily),
			Transport:      gatewayproto.ResponseTransport(frame.Transport),
			Text:           frame.Text,
			ErrorCode:      frame.ErrorCode,
			ErrorType:      frame.ErrorType,
			ErrorMessage:   frame.ErrorMessage,
			FinishReason:   frame.FinishReason,
			Status:         frame.Status,
			Usage:          anthropicUsageToProto(frame.Usage),
			RawJSON:        frame.RawJSON,
			RawJSONPaths:   frame.RawJSONPaths,
			RawText:        frame.RawText,
			EventType:      frame.EventType,
		}
		if frame.ChoiceIndex != nil {
			converted.ChoiceIndex = *frame.ChoiceIndex
		}
		if frame.OutputIndex != nil {
			converted.OutputIndex = *frame.OutputIndex
		}
		if frame.ContentIndex != nil {
			converted.ContentIndex = *frame.ContentIndex
		}
		if frame.VisibleOutput != nil {
			converted.VisibleOutput = *frame.VisibleOutput
		}
		out = append(out, converted)
	}
	return out
}

func convertAnthropicInspection(inspection gatewayanthropic.StreamInspection) gatewayproto.StreamInspection {
	return gatewayproto.StreamInspection{
		TerminalReceived:      inspection.TerminalReceived,
		FailedReceived:        inspection.FailedReceived,
		OutputReceived:        inspection.OutputReceived,
		ImageOutputReceived:   inspection.ImageOutputReceived,
		OutputEventCount:      inspection.OutputEventCount,
		EstimatedOutputTokens: inspection.EstimatedOutputTokens,
		EventCount:            inspection.EventCount,
		EventTypeCounts:       inspection.EventTypeCounts,
		LastEventType:         inspection.LastEventType,
		RecentEventTypes:      inspection.RecentEventTypes,
		PendingEvent:          inspection.PendingEvent,
		Skipped:               inspection.Skipped,
		SkipReason:            inspection.SkipReason,
		ErrorCode:             inspection.ErrorCode,
		ErrorMessage:          inspection.ErrorMessage,
		Usage:                 anthropicUsageValueToProto(inspection.Usage),
	}
}

func geminiUsageToProto(usage *gatewaygemini.ParsedUsage) gatewayproto.ParsedUsage {
	if usage == nil {
		return gatewayproto.ParsedUsage{}
	}
	return gatewayproto.ParsedUsage{
		UpstreamResponseModel: usage.UpstreamResponseModel,
		ServiceTier:           usage.ServiceTier,
		InputTokens:           usage.InputTokens,
		OutputTokens:          usage.OutputTokens,
		CacheReadTokens:       usage.CacheReadTokens,
		CacheWriteTokens:      usage.CacheWriteTokens,
		CacheWrite1hTokens:    usage.CacheWrite1hTokens,
		ThinkingTokens:        usage.ThinkingTokens,
		InputImageTokens:      usage.InputImageTokens,
		OutputImageTokens:     usage.OutputImageTokens,
		InputAudioTokens:      usage.InputAudioTokens,
		OutputAudioTokens:     usage.OutputAudioTokens,
		OutputImageCount:      usage.OutputImageCount,
	}
}

func geminiUsageValueToProto(usage gatewaygemini.ParsedUsage) gatewayproto.ParsedUsage {
	return geminiUsageToProto(&usage)
}

func convertGeminiFrames(frames []gatewaygemini.ResponseSemanticFrame) []gatewayproto.SemanticFrame {
	if frames == nil {
		return nil
	}
	out := make([]gatewayproto.SemanticFrame, 0, len(frames))
	for _, frame := range frames {
		converted := gatewayproto.SemanticFrame{
			FrameType:      frame.FrameType,
			Protocol:       frame.Protocol,
			EndpointFamily: gatewayproto.ResponseEndpointFamily(frame.EndpointFamily),
			Transport:      gatewayproto.ResponseTransport(frame.Transport),
			Text:           frame.Text,
			ErrorCode:      frame.ErrorCode,
			ErrorType:      frame.ErrorType,
			ErrorMessage:   frame.ErrorMessage,
			FinishReason:   frame.FinishReason,
			Status:         frame.Status,
			Usage:          geminiUsageToProto(frame.Usage),
			RawJSON:        frame.RawJSON,
			RawJSONPaths:   frame.RawJSONPaths,
			RawText:        frame.RawText,
			EventType:      frame.EventType,
		}
		if frame.ChoiceIndex != nil {
			converted.ChoiceIndex = *frame.ChoiceIndex
		}
		if frame.OutputIndex != nil {
			converted.OutputIndex = *frame.OutputIndex
		}
		if frame.ContentIndex != nil {
			converted.ContentIndex = *frame.ContentIndex
		}
		if frame.VisibleOutput != nil {
			converted.VisibleOutput = *frame.VisibleOutput
		}
		out = append(out, converted)
	}
	return out
}

func convertGeminiInspection(inspection gatewaygemini.StreamInspection) gatewayproto.StreamInspection {
	return gatewayproto.StreamInspection{
		TerminalReceived:      inspection.TerminalReceived,
		FailedReceived:        inspection.FailedReceived,
		OutputReceived:        inspection.OutputReceived,
		ImageOutputReceived:   inspection.ImageOutputReceived,
		OutputEventCount:      inspection.OutputEventCount,
		EstimatedOutputTokens: inspection.EstimatedOutputTokens,
		EventCount:            inspection.EventCount,
		EventTypeCounts:       inspection.EventTypeCounts,
		LastEventType:         inspection.LastEventType,
		RecentEventTypes:      inspection.RecentEventTypes,
		PendingEvent:          inspection.PendingEvent,
		Skipped:               inspection.Skipped,
		SkipReason:            inspection.SkipReason,
		ErrorCode:             inspection.ErrorCode,
		ErrorMessage:          inspection.ErrorMessage,
		Usage:                 geminiUsageValueToProto(inspection.Usage),
	}
}

// anthropicErrorPayloadToProto 转换 anthropic ErrorPayload。
func anthropicErrorPayloadToProto(payload gatewayanthropic.ErrorPayload) gatewayproto.ErrorPayload {
	return gatewayproto.ErrorPayload{Code: payload.Code, Type: payload.Type, Message: payload.Message}
}

// parseGeminiErrorPayload 对齐 gemini-v1beta/error-payload.ts
// parseGeminiErrorPayload + _shared/error-payload.ts
// parseJsonObjectErrorPayload：仅解析 JSON 负载（Content-Type 含 json 或文本
// 以 { 开头）；error 信封缺失时回退根对象；数值 code/status 转字符串
// （如 429 → "429"）；message 走 Node 别名集并允许一层嵌套对象（BUG-0174 M-6）。
func parseGeminiErrorPayload(bodyText string, header http.Header) gatewayproto.ErrorPayload {
	contentType := ""
	if header != nil {
		contentType = header.Get("Content-Type")
	}
	trimmed := strings.TrimSpace(bodyText)
	if trimmed == "" {
		return gatewayproto.ErrorPayload{}
	}
	if !strings.Contains(strings.ToLower(contentType), "json") && !strings.HasPrefix(trimmed, "{") {
		return gatewayproto.ErrorPayload{}
	}
	var value any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return gatewayproto.ErrorPayload{}
	}
	payload, errorObject, ok := geminiJSONObjectErrorPayload(value)
	if !ok {
		return gatewayproto.ErrorPayload{}
	}
	return geminiErrorPayloadFromParsed(payload, errorObject)
}

// parseGeminiErrorPayloadFromValue 对齐 parseGeminiErrorPayloadFromJsonValue。
func parseGeminiErrorPayloadFromValue(value any) gatewayproto.ErrorPayload {
	payload, errorObject, ok := geminiJSONObjectErrorPayload(value)
	if !ok {
		return gatewayproto.ErrorPayload{}
	}
	return geminiErrorPayloadFromParsed(payload, errorObject)
}

// geminiJSONObjectErrorPayload 对齐 _shared/error-payload.ts
// jsonObjectErrorPayload：根对象缺少 error 信封时以根对象充当 error 读取面。
func geminiJSONObjectErrorPayload(value any) (payload, errorObject map[string]any, ok bool) {
	root, ok := value.(map[string]any)
	if !ok {
		return nil, nil, false
	}
	if child, childOk := root["error"].(map[string]any); childOk {
		return root, child, true
	}
	return root, root, true
}

// geminiErrorPayloadFromParsed 对齐 geminiErrorPayloadFromParsed
// （gemini-v1beta/error-payload.ts:18-27）：status 取 error.status 或根
// status；code 取 error.code / 根 code，皆缺省时回退 status；type 恒为
// status；message 走别名集（error 侧优先于根侧）。
func geminiErrorPayloadFromParsed(payload, errorObject map[string]any) gatewayproto.ErrorPayload {
	status := firstGeminiStringErrorField(errorObject["status"], payload["status"])
	code := firstGeminiErrorFieldText(errorObject["code"], payload["code"])
	if code == "" {
		code = status
	}
	return gatewayproto.ErrorPayload{
		Code: code,
		Type: status,
		Message: firstGeminiErrorFieldText(
			errorObject["message"], errorObject["msg"], errorObject["error_message"],
			errorObject["error_description"], errorObject["detail"],
			payload["message"], payload["msg"], payload["error_message"],
			payload["error_description"], payload["detail"],
		),
	}
}

// geminiStringErrorField 对齐 stringErrorField：字符串 trim 后非空才有效；
// 数字与布尔转字符串（JSON 数值经 encoding/json 解码为 float64，429 → "429"）。
func geminiStringErrorField(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		text := strings.TrimSpace(typed)
		return text, text != ""
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10), true
		}
		return strconv.FormatFloat(typed, 'g', -1, 64), true
	case bool:
		return strconv.FormatBool(typed), true
	default:
		return "", false
	}
}

// firstGeminiStringErrorField 返回第一个有效的 stringErrorField 值。
func firstGeminiStringErrorField(values ...any) string {
	for _, value := range values {
		if text, ok := geminiStringErrorField(value); ok {
			return text
		}
	}
	return ""
}

// firstGeminiErrorFieldText 对齐 firstErrorFieldText：取第一个非空文本，
// 缺失时对嵌套对象递归别名集。
func firstGeminiErrorFieldText(values ...any) string {
	for _, value := range values {
		if text := geminiErrorFieldText(value); text != "" {
			return text
		}
	}
	return ""
}

// geminiErrorFieldText 对齐 errorFieldText：标量直接取值，对象递归
// message/msg/error_message/error_description/detail/reason/code/type。
func geminiErrorFieldText(value any) string {
	if text, ok := geminiStringErrorField(value); ok {
		return text
	}
	record, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	return firstGeminiErrorFieldText(
		record["message"], record["msg"], record["error_message"],
		record["error_description"], record["detail"], record["reason"],
		record["code"], record["type"],
	)
}
