// Package modelcheckprobe contains the pure J3b protocol request contracts.
// It intentionally constructs bytes only; dialing, credentials, retries, and
// persistence stay in the future jobs runner.
package modelcheckprobe

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

const systemInstruction = "You are a model capability checker. Follow the requested output exactly."

type BasicOptions struct {
	MaxOutputTokens int
	Stream          bool
	Temperature     *float64
}

type Request struct {
	Path          string
	Protocol      modelcheckprofile.Protocol
	ExpectedModel string
	Body          json.RawMessage
}

// BuildBasic creates the frozen base capability probe request for one
// provider protocol. The returned Body has no credentials and may be sent as
// is by the jobs-owned transport.
func BuildBasic(protocol modelcheckprofile.Protocol, model, prompt string, options BasicOptions) (Request, error) {
	if strings.TrimSpace(model) == "" || strings.TrimSpace(prompt) == "" || options.MaxOutputTokens <= 0 {
		return Request{}, errors.New("model check basic probe input is invalid")
	}
	temperature := 0.0
	if options.Temperature != nil {
		temperature = *options.Temperature
	}
	var path string
	var payload any
	switch protocol {
	case modelcheckprofile.ProtocolOpenAIResponses:
		path = "/v1/responses"
		payload = map[string]any{
			"model":             model,
			"input":             []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": prompt}}}},
			"instructions":      systemInstruction,
			"max_output_tokens": options.MaxOutputTokens,
			"stream":            options.Stream,
			"store":             false,
			"temperature":       temperature,
		}
	case modelcheckprofile.ProtocolOpenAIChat:
		path = "/v1/chat/completions"
		payload = map[string]any{
			"model": model,
			"messages": []any{
				map[string]any{"role": "system", "content": systemInstruction},
				map[string]any{"role": "user", "content": prompt},
			},
			"max_tokens":  max(options.MaxOutputTokens, 64),
			"stream":      options.Stream,
			"temperature": temperature,
		}
	case modelcheckprofile.ProtocolAnthropic:
		path = "/v1/messages"
		payload = map[string]any{
			"model":      model,
			"system":     systemInstruction,
			"messages":   []any{map[string]any{"role": "user", "content": prompt}},
			"max_tokens": options.MaxOutputTokens,
			"stream":     options.Stream,
		}
	case modelcheckprofile.ProtocolGeminiNative:
		action := "generateContent"
		if options.Stream {
			action = "streamGenerateContent"
		}
		path = "/v1beta/models/" + url.PathEscape(model) + ":" + action
		if options.Stream {
			path += "?alt=sse"
		}
		payload = map[string]any{
			"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": systemInstruction}}},
			"contents":          []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": prompt}}}},
			"generationConfig":  map[string]any{"temperature": temperature, "maxOutputTokens": max(options.MaxOutputTokens, 128)},
		}
	default:
		return Request{}, fmt.Errorf("unsupported model check protocol: %s", protocol)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Request{}, fmt.Errorf("marshal model check %s probe: %w", protocol, err)
	}
	return Request{Path: path, Protocol: protocol, ExpectedModel: model, Body: body}, nil
}

func BuildStructured(protocol modelcheckprofile.Protocol, model string, stream bool) (Request, error) {
	request, err := BuildBasic(protocol, model, `Return {"status":"ok","value":7} as JSON.`, BasicOptions{MaxOutputTokens: 64, Stream: stream})
	if err != nil {
		return Request{}, err
	}
	var payload map[string]any
	if err := json.Unmarshal(request.Body, &payload); err != nil {
		return Request{}, err
	}
	schema := map[string]any{"type": "object", "properties": map[string]any{"status": map[string]any{"type": "string"}, "value": map[string]any{"type": "integer"}}, "required": []string{"status", "value"}}
	switch protocol {
	case modelcheckprofile.ProtocolOpenAIResponses:
		payload["text"] = map[string]any{"format": map[string]any{"type": "json_schema", "name": "model_check_structured_output", "strict": true, "schema": schema}}
	case modelcheckprofile.ProtocolOpenAIChat:
		payload["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "model_check_structured_output", "strict": true, "schema": schema}}
	case modelcheckprofile.ProtocolGeminiNative:
		payload["generationConfig"] = map[string]any{"temperature": 0.0, "maxOutputTokens": 128, "responseMimeType": "application/json", "responseSchema": map[string]any{"type": "OBJECT", "properties": map[string]any{"status": map[string]any{"type": "STRING", "enum": []string{"ok"}}, "value": map[string]any{"type": "INTEGER"}}, "required": []string{"status", "value"}}}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Request{}, err
	}
	request.Body = body
	return request, nil
}

func BuildTool(protocol modelcheckprofile.Protocol, model string, stream bool) (Request, error) {
	request, err := BuildBasic(protocol, model, `Call the provided function with code "ok" and count 1.`, BasicOptions{MaxOutputTokens: 64, Stream: stream})
	if err != nil {
		return Request{}, err
	}
	var payload map[string]any
	if err := json.Unmarshal(request.Body, &payload); err != nil {
		return Request{}, err
	}
	parameters := map[string]any{"type": "object", "properties": map[string]any{"code": map[string]any{"type": "string"}, "count": map[string]any{"type": "integer"}}, "required": []string{"code", "count"}}
	switch protocol {
	case modelcheckprofile.ProtocolOpenAIResponses:
		payload["tools"] = []any{map[string]any{"type": "function", "name": "record_model_check", "description": "Record a model check marker.", "parameters": parameters, "strict": true}}
		payload["tool_choice"] = map[string]any{"type": "function", "name": "record_model_check"}
	case modelcheckprofile.ProtocolOpenAIChat:
		payload["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "record_model_check", "description": "Record a model check marker.", "parameters": parameters}}}
		payload["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": "record_model_check"}}
	case modelcheckprofile.ProtocolAnthropic:
		payload["tools"] = []any{map[string]any{"name": "record_model_check", "description": "Record a model check marker.", "input_schema": parameters}}
		payload["tool_choice"] = map[string]any{"type": "tool", "name": "record_model_check"}
	case modelcheckprofile.ProtocolGeminiNative:
		payload["tools"] = []any{map[string]any{
			"functionDeclarations": []any{map[string]any{
				"name": "record_model_check", "description": "Record a model check marker.",
				"parameters": map[string]any{"type": "OBJECT", "properties": map[string]any{"code": map[string]any{"type": "STRING"}, "count": map[string]any{"type": "INTEGER"}}, "required": []string{"code", "count"}},
			}},
		}}
		payload["toolConfig"] = map[string]any{"functionCallingConfig": map[string]any{"mode": "ANY", "allowedFunctionNames": []string{"record_model_check"}}}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Request{}, err
	}
	request.Body = body
	return request, nil
}

func max(left, right int) int {
	if left > right {
		return left
	}
	return right
}
