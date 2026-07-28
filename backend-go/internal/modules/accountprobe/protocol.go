package accountprobe

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	DefaultPrompt     = "只输出 OK"
	maxProbeBodyBytes = 1 << 20
)

var ErrInvalidProtocolInput = errors.New("account probe protocol input is invalid")

type EndpointMode string

const (
	ModeChatJSON            EndpointMode = "chat_json"
	ModeChatSSE             EndpointMode = "chat_sse"
	ModeResponsesJSON       EndpointMode = "responses_json"
	ModeResponsesSSE        EndpointMode = "responses_sse"
	ModeMessagesJSON        EndpointMode = "messages_json"
	ModeMessagesSSE         EndpointMode = "messages_sse"
	ModeGenerateContentJSON EndpointMode = "generate_content_json"
	ModeGenerateContentSSE  EndpointMode = "generate_content_sse"
	ModeInteractionsJSON    EndpointMode = "interactions_json"
	ModeInteractionsSSE     EndpointMode = "interactions_sse"
)

type RequestInput struct {
	Mode                EndpointMode
	Model               string
	Prompt              string
	OAuth               bool
	ClientCompatibility string
	SessionID           string
	Today               string
	WorkingDirectory    string
}

// RequestSpec is the canonical downstream diagnostic request. Provider driver,
// model mapping, credential/OAuth preparation, URL policy, and proxy handling
// must run after this layer and before an upstream request is sent.
type RequestSpec struct {
	Method       string
	PathAndQuery string
	Header       http.Header
	Body         []byte
	Mode         EndpointMode
	Model        string
}

func BuildRequest(input RequestInput) (RequestSpec, error) {
	input.Model = strings.TrimSpace(input.Model)
	input.Prompt = strings.TrimSpace(input.Prompt)
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.WorkingDirectory = strings.TrimSpace(input.WorkingDirectory)
	if input.Model == "" {
		return RequestSpec{}, fmt.Errorf("%w: model is required", ErrInvalidProtocolInput)
	}
	if input.Prompt == "" {
		input.Prompt = DefaultPrompt
	}
	if len(input.Model) > 512 || len(input.Prompt) > 4096 || strings.ContainsAny(input.Model, "\r\n\x00") {
		return RequestSpec{}, fmt.Errorf("%w: model or prompt exceeds bounds", ErrInvalidProtocolInput)
	}

	header := make(http.Header)
	var path string
	var body map[string]any
	switch input.Mode {
	case ModeChatJSON, ModeChatSSE:
		stream := input.Mode == ModeChatSSE
		path = "/v1/chat/completions"
		body = map[string]any{"model": input.Model, "messages": []any{map[string]any{"role": "user", "content": input.Prompt}}, "max_tokens": 1, "stream": stream}
	case ModeResponsesJSON, ModeResponsesSSE:
		stream := input.Mode == ModeResponsesSSE
		path = "/v1/responses"
		body = map[string]any{
			"model":        input.Model,
			"input":        []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": input.Prompt}}}},
			"instructions": "You are ChatGPT, a helpful assistant.", "stream": stream,
		}
		if input.OAuth {
			body["max_output_tokens"] = 1
			body["store"] = false
		}
		if (stream || input.OAuth) && strings.EqualFold(strings.TrimSpace(input.ClientCompatibility), "codex_responses") {
			body["store"] = false
			body["include"] = []string{"reasoning.encrypted_content"}
			normalizeCodexResponsesRequest(header, body, input.Model)
		}
	case ModeMessagesJSON, ModeMessagesSSE:
		if input.SessionID == "" || len(input.SessionID) > 256 || strings.ContainsAny(input.SessionID, "\r\n\x00") {
			return RequestSpec{}, fmt.Errorf("%w: bounded session id is required for messages probe", ErrInvalidProtocolInput)
		}
		if !validISODate(input.Today) {
			return RequestSpec{}, fmt.Errorf("%w: ISO date is required for messages probe", ErrInvalidProtocolInput)
		}
		if input.WorkingDirectory == "" || len(input.WorkingDirectory) > 4096 || strings.ContainsAny(input.WorkingDirectory, "\r\n\x00") {
			return RequestSpec{}, fmt.Errorf("%w: bounded working directory is required for messages probe", ErrInvalidProtocolInput)
		}
		stream := input.Mode == ModeMessagesSSE
		path = "/v1/messages"
		header.Set("x-juhe-client-profile", "claude_code")
		header.Set("x-claude-code-session-id", input.SessionID)
		body = map[string]any{
			"model": input.Model, "stream": stream, "max_tokens": 32000, "tools": []any{},
			"thinking": map[string]any{"type": "adaptive"}, "output_config": map[string]any{"effort": "high"},
			"messages": []any{map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": accountTestSystemReminder(input.Today)},
				map[string]any{"type": "text", "text": input.Prompt + "\n", "cache_control": map[string]any{"type": "ephemeral"}},
			}}},
			"system": []any{
				map[string]any{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.201.eb7; cc_entrypoint=sdk-cli;"},
				map[string]any{"type": "text", "text": "You are a Claude agent, built on Anthropic's Claude Agent SDK.", "cache_control": map[string]any{"type": "ephemeral"}},
				map[string]any{"type": "text", "text": "CWD: " + input.WorkingDirectory + "\nDate: " + input.Today},
			},
			"metadata": map[string]any{"user_id": mustJSON(map[string]any{"device_id": "7cfe24060ed291eb6ea9b7a6edf6947d14da82a0068470a6fc9cf8c147b252dc", "account_uuid": "", "session_id": input.SessionID})},
		}
	case ModeGenerateContentJSON, ModeGenerateContentSSE:
		stream := input.Mode == ModeGenerateContentSSE
		modelName := input.Model
		if len(modelName) >= len("models/") && strings.EqualFold(modelName[:len("models/")], "models/") {
			modelName = modelName[len("models/"):]
		}
		model := url.PathEscape(modelName)
		method := "generateContent"
		if stream {
			method = "streamGenerateContent"
		}
		path = "/v1beta/models/" + model + ":" + method
		if stream {
			path += "?alt=sse"
		}
		body = map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": input.Prompt}}}}, "generationConfig": map[string]any{"maxOutputTokens": 1}}
	case ModeInteractionsJSON, ModeInteractionsSSE:
		stream := input.Mode == ModeInteractionsSSE
		path = "/v1beta/interactions"
		body = map[string]any{"model": input.Model, "input": input.Prompt, "stream": stream}
		if stream {
			header.Set("Accept", "text/event-stream")
		}
	default:
		return RequestSpec{}, fmt.Errorf("%w: unsupported endpoint mode %q", ErrInvalidProtocolInput, input.Mode)
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil || len(bodyBytes) == 0 || len(bodyBytes) > maxProbeBodyBytes {
		return RequestSpec{}, fmt.Errorf("%w: serialize bounded request body", ErrInvalidProtocolInput)
	}
	header.Set("Content-Type", "application/json")
	return RequestSpec{Method: http.MethodPost, PathAndQuery: path, Header: header, Body: bodyBytes, Mode: input.Mode, Model: input.Model}, nil
}

type Evidence struct {
	Complete        bool
	Failed          bool
	MalformedEvents int
}

func InspectEvidence(mode EndpointMode, body []byte, truncated bool) (Evidence, error) {
	if truncated {
		return Evidence{}, fmt.Errorf("%w: truncated response cannot prove completion", ErrInvalidProtocolInput)
	}
	if len(body) == 0 || len(body) > maxProbeBodyBytes {
		return Evidence{}, fmt.Errorf("%w: response body is empty or exceeds evidence limit", ErrInvalidProtocolInput)
	}
	if strings.HasSuffix(string(mode), "_sse") {
		return inspectSSEEvidence(mode, body)
	}
	object, err := decodeObject(body)
	if err != nil {
		return Evidence{}, err
	}
	return evidenceForObject(mode, object), nil
}

func inspectSSEEvidence(mode EndpointMode, body []byte) (Evidence, error) {
	events, malformed, err := parseSSE(body)
	if err != nil {
		return Evidence{}, err
	}
	result := Evidence{MalformedEvents: malformed}
	hasChatContent := false
	for _, event := range events {
		if event.done {
			if mode == ModeChatSSE && hasChatContent {
				result.Complete = true
			}
			continue
		}
		if event.object == nil {
			continue
		}
		eventType := firstText(event.object["type"], event.object["event_type"], event.name)
		if strings.EqualFold(eventType, "error") || strings.HasSuffix(strings.ToLower(eventType), ".failed") {
			result.Failed = true
			result.Complete = false
			break
		}
		switch mode {
		case ModeChatSSE:
			if evidenceForObject(ModeChatJSON, event.object).Complete {
				result.Complete = true
			}
			if chatContent(event.object) {
				hasChatContent = true
			}
		case ModeResponsesSSE:
			response := objectValue(event.object["response"])
			if response == nil {
				response = event.object
			}
			if eventType == "response.completed" || evidenceForObject(ModeResponsesJSON, response).Complete {
				result.Complete = true
			}
		case ModeMessagesSSE:
			message := objectValue(event.object["message"])
			if message == nil {
				message = event.object
			}
			if eventType == "message_stop" || evidenceForObject(ModeMessagesJSON, message).Complete {
				result.Complete = true
			}
		case ModeGenerateContentSSE:
			payload := objectValue(event.object["response"])
			if payload == nil {
				payload = event.object
			}
			if evidenceForObject(ModeGenerateContentJSON, payload).Complete {
				result.Complete = true
			}
		case ModeInteractionsSSE:
			interaction := objectValue(event.object["interaction"])
			if interaction == nil {
				interaction = event.object
			}
			if eventType == "interaction.completed" || text(interaction["status"]) == "completed" {
				result.Complete = true
			}
		}
	}
	return result, nil
}

func evidenceForObject(mode EndpointMode, object map[string]any) Evidence {
	if object == nil {
		return Evidence{}
	}
	if object["error"] != nil || strings.EqualFold(text(object["type"]), "error") {
		return Evidence{Failed: true}
	}
	switch mode {
	case ModeChatJSON:
		for _, item := range arrayValue(object["choices"]) {
			if text(objectValue(item)["finish_reason"]) != "" {
				return Evidence{Complete: true}
			}
		}
	case ModeResponsesJSON:
		_, hasOutput := object["output"].([]any)
		return Evidence{Complete: object["status"] == "completed" && (object["object"] == "response" || hasOutput)}
	case ModeMessagesJSON:
		return Evidence{Complete: object["type"] == "message" && text(object["stop_reason"]) != ""}
	case ModeGenerateContentJSON:
		for _, item := range arrayValue(object["candidates"]) {
			if text(objectValue(item)["finishReason"]) != "" {
				return Evidence{Complete: true}
			}
		}
	case ModeInteractionsJSON:
		_, hasSteps := object["steps"].([]any)
		return Evidence{Complete: object["status"] == "completed" && (object["object"] == "interaction" || hasSteps)}
	}
	return Evidence{}
}

type sseEvent struct {
	name   string
	object map[string]any
	done   bool
}

func parseSSE(body []byte) ([]sseEvent, int, error) {
	textBody := strings.ReplaceAll(strings.ReplaceAll(string(body), "\r\n", "\n"), "\r", "\n")
	blocks := strings.Split(textBody, "\n\n")
	events := make([]sseEvent, 0, len(blocks))
	malformed := 0
	for _, block := range blocks {
		if strings.TrimSpace(block) == "" {
			continue
		}
		name := ""
		data := make([]string, 0, 1)
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, "event:") {
				name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			}
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		if len(data) == 0 {
			continue
		}
		payload := strings.TrimSpace(strings.Join(data, "\n"))
		if payload == "[DONE]" {
			events = append(events, sseEvent{name: name, done: true})
			continue
		}
		object, err := decodeObject([]byte(payload))
		if err != nil {
			malformed++
			continue
		}
		events = append(events, sseEvent{name: name, object: object})
	}
	return events, malformed, nil
}

func decodeObject(body []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, fmt.Errorf("%w: response is not one JSON object", ErrInvalidProtocolInput)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: response contains trailing JSON", ErrInvalidProtocolInput)
	}
	return value, nil
}

func chatContent(object map[string]any) bool {
	for _, item := range arrayValue(object["choices"]) {
		choice := objectValue(item)
		if text(objectValue(choice["delta"])["content"]) != "" || text(objectValue(choice["message"])["content"]) != "" {
			return true
		}
	}
	return false
}
func mustJSON(value any) string            { encoded, _ := json.Marshal(value); return string(encoded) }
func objectValue(value any) map[string]any { object, _ := value.(map[string]any); return object }
func arrayValue(value any) []any           { array, _ := value.([]any); return array }
func text(value any) string                { result, _ := value.(string); return strings.TrimSpace(result) }
func firstText(values ...any) string {
	for _, value := range values {
		if result := text(value); result != "" {
			return result
		}
	}
	return ""
}

func validISODate(value string) bool {
	_, err := time.Parse("2006-01-02", value)
	return err == nil
}

func accountTestSystemReminder(today string) string {
	return strings.Join([]string{
		"<system-reminder>",
		"As you answer the user's questions, you can use the following context:",
		"# currentDate",
		"Today's date is " + today + ".",
		"",
		"      IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.",
		"</system-reminder>",
		"",
		"",
	}, "\n")
}

func normalizeCodexResponsesRequest(header http.Header, body map[string]any, model string) {
	sessionID := uuid.NewString()
	turnID := uuid.NewString()
	installationID := uuid.NewString()
	windowID := sessionID + ":0"
	metadata := map[string]any{
		"installation_id":         installationID,
		"session_id":              sessionID,
		"thread_id":               sessionID,
		"turn_id":                 turnID,
		"window_id":               windowID,
		"request_kind":            "turn",
		"thread_source":           "user",
		"sandbox":                 "none",
		"turn_started_at_unix_ms": time.Now().UnixMilli(),
	}
	metadataJSON := mustJSON(metadata)
	header.Set("originator", "Codex Desktop")
	header.Set("user-agent", "Codex Desktop/0.145.0 (Windows 10.0.22621; x86_64) unknown (codex_exec; 0.145.0)")
	header.Set("session-id", sessionID)
	header.Set("thread-id", sessionID)
	header.Set("x-client-request-id", sessionID)
	header.Set("x-codex-beta-features", "remote_compaction_v2")
	header.Set("x-codex-turn-metadata", metadataJSON)
	header.Set("x-codex-window-id", windowID)
	body["client_metadata"] = map[string]any{
		"x-codex-window-id":       windowID,
		"turn_id":                 turnID,
		"session_id":              sessionID,
		"x-codex-turn-metadata":   metadataJSON,
		"x-codex-installation-id": installationID,
		"thread_id":               sessionID,
	}
	body["prompt_cache_key"] = sessionID
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna":
		header.Set("x-openai-internal-codex-responses-lite", "true")
		body["reasoning"] = map[string]any{"context": "all_turns"}
		body["parallel_tool_calls"] = false
	}
}
