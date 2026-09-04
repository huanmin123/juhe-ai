package gatewayresponse

import (
	"encoding/json"
	"net/http"
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

// parseGeminiErrorPayload 是 gemini 包缺失的解析面（G04 未冻结）；按 Gemini
// 错误包络 {error:{code,message,status}} 直接解析。
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
	var parsed struct {
		Error struct {
			Code    any `json:"code"`
			Message any `json:"message"`
			Status  any `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return gatewayproto.ErrorPayload{}
	}
	return gatewayproto.ErrorPayload{
		Code:    anyToString(parsed.Error.Code),
		Message: anyToString(parsed.Error.Message),
		Type:    anyToString(parsed.Error.Status),
	}
}

func anyToString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return ""
	}
}

// parseGeminiErrorPayloadFromValue 对齐 parseGeminiErrorPayloadFromJsonValue。
func parseGeminiErrorPayloadFromValue(value any) gatewayproto.ErrorPayload {
	root, ok := value.(map[string]any)
	if !ok {
		return gatewayproto.ErrorPayload{}
	}
	errorObject, ok := root["error"].(map[string]any)
	if !ok {
		return gatewayproto.ErrorPayload{}
	}
	return gatewayproto.ErrorPayload{
		Code:    anyToString(errorObject["code"]),
		Message: anyToString(errorObject["message"]),
		Type:    anyToString(errorObject["status"]),
	}
}
