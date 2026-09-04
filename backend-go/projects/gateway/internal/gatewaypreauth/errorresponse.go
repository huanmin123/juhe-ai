package gatewaypreauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// Port of request/error-response.ts: the known-error handler shared by the
// gateway routes. Branch order, status codes, payloads and the audit
// finalize inputs mirror the Node source; the agent guidance bodies render
// the exact chat/messages/gemini/responses shapes.

// KnownErrorResponseInput mirrors HandleGatewayRequestKnownErrorResponseInput.
type KnownErrorResponseInput struct {
	Req          *GatewayRequest
	Res          GatewayResponseWriter
	AuditCapture AuditCaptureContext
	Err          error
	// Signal mirrors the request abort signal.
	Signal context.Context
}

// HandleGatewayRequestKnownErrorResponse mirrors
// handleGatewayRequestKnownErrorResponse; handled=false means the error is
// unknown and the caller keeps its generic failure path.
func (s *Service) HandleGatewayRequestKnownErrorResponse(input KnownErrorResponseInput) bool {
	req, res, auditCapture, err := input.Req, input.Res, input.AuditCapture, input.Err
	aborted := input.Signal != nil && input.Signal.Err() != nil
	abortSource, hasAbortSource := GatewayRequestAbortSourceOf(req)

	if aborted && hasAbortSource && abortSource == AbortSourceServerDiagnosticTimeout {
		statusCode := http.StatusGatewayTimeout
		responsePayload := GatewayErrorPayloadOf("服务端账户诊断超时", "gateway_timeout", "server_diagnostic_timeout")
		protocol := s.clientErrorProtocolOrOpenAI(req)
		clientPayload := GatewayErrorPayloadForProtocol(responsePayload, protocol)
		SendGatewayErrorResponse(res, statusCode, responsePayload, SendGatewayErrorResponseOptions{Protocol: protocol})
		auditCapture.Finalize(AuditFinalizeInput{
			Outcome:          AuditOutcomeGatewayFailed,
			Success:          false,
			StatusCode:       statusCode,
			ResponseHeaders:  responseHeadersToObject(res),
			ResponseBody:     marshalClientPayload(clientPayload),
			ResponsePartType: AuditPartGatewayError,
			ErrorPhase:       "server_diagnostic",
			ErrorCode:        "server_diagnostic_timeout",
			ErrorMessage:     responsePayload.Error.Message,
		})
		return true
	}

	if aborted && hasAbortSource && abortSource == AbortSourceServerDiagnosticCancel {
		statusCode := http.StatusInternalServerError
		responsePayload := GatewayErrorPayloadOf("服务端账户诊断已取消", "gateway_cancelled", "server_diagnostic_cancelled")
		protocol := s.clientErrorProtocolOrOpenAI(req)
		clientPayload := GatewayErrorPayloadForProtocol(responsePayload, protocol)
		SendGatewayErrorResponse(res, statusCode, responsePayload, SendGatewayErrorResponseOptions{Protocol: protocol})
		auditCapture.Finalize(AuditFinalizeInput{
			Outcome:          AuditOutcomeGatewayFailed,
			Success:          false,
			StatusCode:       statusCode,
			ResponseHeaders:  responseHeadersToObject(res),
			ResponseBody:     marshalClientPayload(clientPayload),
			ResponsePartType: AuditPartGatewayError,
			ErrorPhase:       "server_diagnostic",
			ErrorCode:        "server_diagnostic_cancelled",
			ErrorMessage:     responsePayload.Error.Message,
		})
		return true
	}

	if IsUpstreamRequestAbortedError(err) || aborted {
		auditCapture.Finalize(AuditFinalizeInput{
			Outcome:         "downstream_closed",
			Success:         false,
			StatusCode:      res.StatusCode(),
			ResponseHeaders: responseHeadersToObject(res),
			ErrorPhase:      "downstream",
			ErrorCode:       "downstream_connection_closed",
			ErrorMessage:    downstreamConnectionClosedMessage,
		})
		if tracking, ok := res.(*TrackingWriter); ok {
			if !tracking.WritableEnded() && !tracking.destroyed {
				tracking.End()
			}
		}
		return true
	}

	if guidance, ok := asAgentGuidance(err); ok && !guidance.IsAccountScoped() {
		responseBody := s.sendAgentGuidanceResponse(res, guidance)
		auditCapture.Finalize(AuditFinalizeInput{
			Outcome:          AuditOutcomeSuccess,
			Success:          true,
			StatusCode:       http.StatusOK,
			ResponseHeaders:  responseHeadersToObject(res),
			ResponseBody:     responseBody,
			ResponsePartType: AuditPartGatewayResponse,
			ErrorPhase:       "request_validation",
			ErrorCode:        guidance.Code,
			ErrorMessage:     guidance.Message,
		})
		return true
	}

	if localResponse, ok := asLocalProtocolResponse(err); ok {
		res.WriteHeader(localResponse.StatusCode)
		res.Header().Set("Content-Type", localResponse.ContentType)
		if strings.HasPrefix(localResponse.ContentType, "text/event-stream") {
			res.Header().Set("Cache-Control", "no-cache")
		}
		_, _ = res.Write([]byte(localResponse.Body))
		if tracking, ok := res.(*TrackingWriter); ok {
			tracking.End()
		}
		auditCapture.Finalize(AuditFinalizeInput{
			Outcome:          AuditOutcomeSuccess,
			Success:          true,
			StatusCode:       localResponse.StatusCode,
			ResponseHeaders:  responseHeadersToObject(res),
			ResponseBody:     localResponse.Body,
			ResponsePartType: AuditPartGatewayResponse,
			ErrorPhase:       "request_validation",
			ErrorCode:        localResponse.Code,
			ErrorMessage:     localResponse.Message,
		})
		return true
	}

	if validation, ok := asValidationOrCodexAdapterError(err); ok {
		statusCode := validation.StatusCode
		responsePayload := GatewayErrorPayloadOf(validation.Message, validation.Type, validation.Code)
		protocol := s.clientErrorProtocolOrOpenAI(req)
		clientPayload := GatewayErrorPayloadForProtocol(responsePayload, protocol)
		SendGatewayErrorResponse(res, statusCode, responsePayload, SendGatewayErrorResponseOptions{Protocol: protocol})
		auditCapture.Finalize(AuditFinalizeInput{
			Outcome:          AuditOutcomeGatewayFailed,
			Success:          false,
			StatusCode:       statusCode,
			ResponseHeaders:  responseHeadersToObject(res),
			ResponseBody:     marshalClientPayload(clientPayload),
			ResponsePartType: AuditPartGatewayError,
			ErrorPhase:       "request_validation",
			ErrorCode:        validation.Code,
			ErrorMessage:     validation.Message,
		})
		return true
	}

	return false
}

// CodexAdapterValidationError mirrors the consumed shape of
// OpenAIOAuthCodexAdapterError (G15 adapter): the fields the error renderer
// reads. The concrete adapter error implements CodexAdapterErrorMarker.
type CodexAdapterValidationError struct {
	Message    string
	Code       string
	StatusCode int
	Type       string
}

// CodexAdapterErrorMarker marks the codex OAuth adapter error type.
type CodexAdapterErrorMarker interface {
	error
	CodexAdapterError() *CodexAdapterValidationError
}

func asValidationOrCodexAdapterError(err error) (*CodexAdapterValidationError, bool) {
	if err == nil {
		return nil, false
	}
	var adapter CodexAdapterErrorMarker
	if errors.As(err, &adapter) {
		if payload := adapter.CodexAdapterError(); payload != nil {
			return payload, true
		}
	}
	var validation *GatewayRequestValidationError
	if errors.As(err, &validation) {
		return &CodexAdapterValidationError{
			Message:    validation.Message,
			Code:       validation.Code,
			StatusCode: validation.StatusCode,
			Type:       validation.Type,
		}, true
	}
	return nil, false
}

func asAgentGuidance(err error) (*GatewayAgentGuidanceResponse, bool) {
	var guidance *GatewayAgentGuidanceResponse
	if errors.As(err, &guidance) {
		return guidance, true
	}
	return nil, false
}

func asLocalProtocolResponse(err error) (*GatewayLocalProtocolResponse, bool) {
	var local *GatewayLocalProtocolResponse
	if errors.As(err, &local) {
		return local, true
	}
	return nil, false
}

// marshalClientPayload mirrors JSON.stringify(clientPayload).
func marshalClientPayload(payload any) string {
	encoded, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return ""
	}
	return string(encoded)
}

// responseHeadersToObject mirrors responseHeadersToObject(res) for the
// headers set so far.
func responseHeadersToObject(res GatewayResponseWriter) map[string]any {
	headers := map[string]any{}
	for name, values := range res.Header() {
		if len(values) == 1 {
			headers[name] = values[0]
			continue
		}
		list := make([]any, len(values))
		for i, value := range values {
			list[i] = value
		}
		headers[name] = list
	}
	return headers
}

// sendAgentGuidanceResponse mirrors sendAgentGuidanceResponse; the return
// value is the serialized response body.
func (s *Service) sendAgentGuidanceResponse(res GatewayResponseWriter, guidance *GatewayAgentGuidanceResponse) string {
	created := guidanceCreatedSeconds(s.Clock.Now())
	switch guidance.Protocol {
	case AgentGuidanceProtocolGemini:
		if guidance.Stream {
			body := geminiGuidanceSSE(guidance, created)
			writeGuidanceSSE(res, body)
			return body
		}
		return writeGuidanceJSON(res, geminiGuidanceJSON(guidance))
	case AgentGuidanceProtocolMessages:
		if guidance.Stream {
			body := anthropicMessagesGuidanceSSE(guidance, created)
			writeGuidanceSSE(res, body)
			return body
		}
		return writeGuidanceJSON(res, anthropicMessagesGuidanceJSON(guidance, created))
	case AgentGuidanceProtocolResponses:
		if guidance.Stream {
			body := responsesGuidanceSSE(guidance, created)
			writeGuidanceSSE(res, body)
			return body
		}
		return writeGuidanceJSON(res, responsesGuidanceJSON(guidance, created))
	default:
		if guidance.Stream {
			body := chatGuidanceSSE(guidance, created)
			writeGuidanceSSE(res, body)
			return body
		}
		return writeGuidanceJSON(res, chatGuidanceJSON(guidance, created))
	}
}

func writeGuidanceSSE(res GatewayResponseWriter, body string) {
	res.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	res.Header().Set("Cache-Control", "no-cache")
	res.WriteHeader(http.StatusOK)
	_, _ = res.Write([]byte(body))
}

func writeGuidanceJSON(res GatewayResponseWriter, body map[string]any) string {
	encoded, err := json.Marshal(body)
	if err != nil {
		encoded = []byte("{}")
	}
	res.Header().Set("Content-Type", "application/json; charset=utf-8")
	res.WriteHeader(http.StatusOK)
	_, _ = res.Write(encoded)
	return string(encoded)
}

// ---------------------------------------------------------------------------
// anthropic messages guidance
// ---------------------------------------------------------------------------

func anthropicMessagesGuidanceJSON(guidance *GatewayAgentGuidanceResponse, created int64) map[string]any {
	id := "msg_guidance_" + strconv.FormatInt(created, 10)
	return map[string]any{
		"id":            id,
		"type":          "message",
		"role":          "assistant",
		"model":         guidance.Model,
		"content":       []any{map[string]any{"type": "text", "text": guidance.Message}},
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage":         map[string]any{"input_tokens": 0, "output_tokens": 0},
	}
}

func anthropicMessagesGuidanceSSE(guidance *GatewayAgentGuidanceResponse, created int64) string {
	id := "msg_guidance_" + strconv.FormatInt(created, 10)
	return strings.Join([]string{
		anthropicSSE("message_start", map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id": id, "type": "message", "role": "assistant", "model": guidance.Model,
				"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
				"usage": map[string]any{"input_tokens": 0, "output_tokens": 0},
			},
		}),
		anthropicSSE("content_block_start", map[string]any{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "text", "text": ""},
		}),
		anthropicSSE("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": guidance.Message},
		}),
		anthropicSSE("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}),
		anthropicSSE("message_delta", map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
			"usage": map[string]any{"output_tokens": 0},
		}),
		anthropicSSE("message_stop", map[string]any{"type": "message_stop"}),
	}, "")
}

// ---------------------------------------------------------------------------
// gemini guidance
// ---------------------------------------------------------------------------

func geminiGuidanceJSON(guidance *GatewayAgentGuidanceResponse) map[string]any {
	return map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{
				"role":  "model",
				"parts": []any{map[string]any{"text": guidance.Message}},
			},
			"finishReason": "STOP",
		}},
		"usageMetadata": zeroGeminiUsage(),
		"modelVersion":  guidance.Model,
	}
}

func geminiGuidanceSSE(guidance *GatewayAgentGuidanceResponse, _ int64) string {
	return strings.Join([]string{
		geminiSSE(map[string]any{
			"candidates": []any{map[string]any{
				"content": map[string]any{
					"role":  "model",
					"parts": []any{map[string]any{"text": guidance.Message}},
				},
			}},
			"modelVersion": guidance.Model,
		}),
		geminiSSE(map[string]any{
			"candidates":    []any{map[string]any{"finishReason": "STOP"}},
			"usageMetadata": zeroGeminiUsage(),
			"modelVersion":  guidance.Model,
		}),
	}, "")
}

func zeroGeminiUsage() map[string]any {
	return map[string]any{
		"promptTokenCount":     0,
		"candidatesTokenCount": 0,
		"totalTokenCount":      0,
	}
}

// ---------------------------------------------------------------------------
// chat completions guidance
// ---------------------------------------------------------------------------

func chatGuidanceJSON(guidance *GatewayAgentGuidanceResponse, created int64) map[string]any {
	return map[string]any{
		"id":      "chatcmpl_guidance_" + strconv.FormatInt(created, 10),
		"object":  "chat.completion",
		"created": created,
		"model":   guidance.Model,
		"choices": []any{map[string]any{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": guidance.Message,
			},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0,
		},
	}
}

func chatGuidanceSSE(guidance *GatewayAgentGuidanceResponse, created int64) string {
	id := "chatcmpl_guidance_" + strconv.FormatInt(created, 10)
	chunk := func(delta map[string]any, finishReason any) string {
		return chatSSE(map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": guidance.Model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finishReason}},
		})
	}
	return strings.Join([]string{
		chunk(map[string]any{"role": "assistant"}, nil),
		chunk(map[string]any{"content": guidance.Message}, nil),
		chunk(map[string]any{}, "stop"),
		"data: [DONE]\n\n",
	}, "")
}

// ---------------------------------------------------------------------------
// responses guidance
// ---------------------------------------------------------------------------

func responsesGuidanceJSON(guidance *GatewayAgentGuidanceResponse, created int64) map[string]any {
	responseID := "resp_guidance_" + strconv.FormatInt(created, 10)
	messageID := "msg_guidance_" + strconv.FormatInt(created, 10)
	output := []any{responsesGuidanceMessageItem(messageID, guidance.Message)}
	return responsesGuidanceSnapshot(responsesGuidanceSnapshotInput{
		responseID: responseID, created: created, model: guidance.Model,
		output: output, status: "completed",
	})
}

func responsesGuidanceSSE(guidance *GatewayAgentGuidanceResponse, created int64) string {
	responseID := "resp_guidance_" + strconv.FormatInt(created, 10)
	messageID := "msg_guidance_" + strconv.FormatInt(created, 10)
	contentIndex := 0
	completedItem := responsesGuidanceMessageItem(messageID, guidance.Message)
	startedItem := clonedJSONMap(completedItem)
	startedItem["status"] = "in_progress"
	startedItem["content"] = []any{}
	textPart := map[string]any{"type": "output_text", "text": guidance.Message, "annotations": []any{}}
	completedSnapshot := responsesGuidanceSnapshot(responsesGuidanceSnapshotInput{
		responseID: responseID, created: created, model: guidance.Model,
		output: []any{completedItem}, status: "completed",
	})
	inProgressSnapshot := responsesGuidanceSnapshot(responsesGuidanceSnapshotInput{
		responseID: responseID, created: created, model: guidance.Model,
		output: []any{}, status: "in_progress",
	})
	return strings.Join([]string{
		responsesSSE("response.created", map[string]any{
			"type": "response.created", "response": inProgressSnapshot,
		}),
		responsesSSE("response.in_progress", map[string]any{
			"type": "response.in_progress", "response": inProgressSnapshot,
		}),
		responsesSSE("response.output_item.added", map[string]any{
			"type": "response.output_item.added", "output_index": 0, "item": startedItem,
		}),
		responsesSSE("response.content_part.added", map[string]any{
			"type": "response.content_part.added", "item_id": messageID,
			"output_index": 0, "content_index": contentIndex,
			"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
		}),
		responsesSSE("response.output_text.delta", map[string]any{
			"type": "response.output_text.delta", "item_id": messageID,
			"output_index": 0, "content_index": contentIndex, "delta": guidance.Message,
		}),
		responsesSSE("response.output_text.done", map[string]any{
			"type": "response.output_text.done", "item_id": messageID,
			"output_index": 0, "content_index": contentIndex, "text": guidance.Message,
		}),
		responsesSSE("response.content_part.done", map[string]any{
			"type": "response.content_part.done", "item_id": messageID,
			"output_index": 0, "content_index": contentIndex, "part": textPart,
		}),
		responsesSSE("response.output_item.done", map[string]any{
			"type": "response.output_item.done", "output_index": 0, "item": completedItem,
		}),
		responsesSSE("response.completed", map[string]any{
			"type": "response.completed", "response": completedSnapshot,
		}),
	}, "")
}

func responsesGuidanceMessageItem(messageID string, text string) map[string]any {
	return map[string]any{
		"id": messageID, "type": "message", "status": "completed", "role": "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
	}
}

type responsesGuidanceSnapshotInput struct {
	responseID string
	created    int64
	model      string
	output     []any
	status     string
}

func responsesGuidanceSnapshot(input responsesGuidanceSnapshotInput) map[string]any {
	var completedAt any
	if input.status == "completed" {
		completedAt = input.created
	}
	var usage any
	if input.status == "completed" {
		usage = map[string]any{
			"input_tokens": 0, "output_tokens": 0, "total_tokens": 0,
			"input_tokens_details":  map[string]any{"cached_tokens": 0},
			"output_tokens_details": map[string]any{"reasoning_tokens": 0},
		}
	}
	outputText := strings.Join(responsesGuidanceOutputTexts(input.output), "")
	return map[string]any{
		"id": input.responseID, "object": "response", "created_at": input.created,
		"status": input.status, "completed_at": completedAt,
		"error": nil, "incomplete_details": nil, "instructions": nil,
		"max_output_tokens": nil, "model": input.model, "output": input.output,
		"output_text":         outputText,
		"parallel_tool_calls": false, "previous_response_id": nil,
		"reasoning": map[string]any{"effort": nil, "summary": nil},
		"store":     false, "temperature": nil,
		"text":        map[string]any{"format": map[string]any{"type": "text"}},
		"tool_choice": "auto", "tools": []any{}, "top_p": nil,
		"truncation": "disabled", "usage": usage, "user": nil,
		"metadata": map[string]any{"gateway_guidance": true},
	}
}

// responsesGuidanceOutputTexts mirrors the output_text flatMap.
func responsesGuidanceOutputTexts(output []any) []string {
	texts := []string{}
	for _, item := range output {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		content, ok := record["content"].([]any)
		if !ok {
			continue
		}
		for _, part := range content {
			partRecord, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := partRecord["text"].(string); ok {
				texts = append(texts, text)
			} else {
				texts = append(texts, "")
			}
		}
	}
	return texts
}

// clonedJSONMap mirrors the `{ ...item, status, content }` spread.
func clonedJSONMap(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func chatSSE(payload map[string]any) string {
	return "data: " + mustJSON(payload) + "\n\n"
}

func responsesSSE(event string, payload map[string]any) string {
	return "event: " + event + "\ndata: " + mustJSON(payload) + "\n\n"
}

func anthropicSSE(event string, payload map[string]any) string {
	return "event: " + event + "\ndata: " + mustJSON(payload) + "\n\n"
}

func geminiSSE(payload map[string]any) string {
	return "data: " + mustJSON(payload) + "\n\n"
}

func mustJSON(payload map[string]any) string {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}
