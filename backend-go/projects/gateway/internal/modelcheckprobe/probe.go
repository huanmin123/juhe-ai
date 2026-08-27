// Package modelcheckprobe contains the Gateway-owned direct transport for J3b.
// It returns parsed facts only; credentials and raw response bodies never
// leave the request attempt.
package modelcheckprobe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

const (
	DefaultTimeout     = 30 * time.Second
	DefaultMaxResponse = 2 << 20
)

type Request struct {
	Path, ExpectedModel string
	Protocol            modelcheckprofile.Protocol
	Body                json.RawMessage
}

type Result struct {
	HTTPStatus    int
	Success       bool
	ExpectedModel string
	ObservedModel string
	Output        string
	Usage         map[string]any
	JSON          map[string]any
	ErrorMessage  string
	Duration      time.Duration
}

type Options struct {
	Endpoint         string
	Headers          http.Header
	Client           *http.Client
	Timeout          time.Duration
	MaxResponseBytes int64
}

func BuildBasic(protocol modelcheckprofile.Protocol, model, prompt string, stream bool) (Request, error) {
	if strings.TrimSpace(model) == "" || strings.TrimSpace(prompt) == "" {
		return Request{}, errors.New("J3b probe model and prompt are required")
	}
	var path string
	var payload any
	switch protocol {
	case modelcheckprofile.ProtocolOpenAIResponses:
		path = "/v1/responses"
		payload = map[string]any{"model": model, "input": prompt, "instructions": "You are a model capability checker. Follow the requested output exactly.", "max_output_tokens": 64, "stream": stream, "store": false}
	case modelcheckprofile.ProtocolOpenAIChat:
		path = "/v1/chat/completions"
		payload = map[string]any{"model": model, "messages": []any{map[string]any{"role": "user", "content": prompt}}, "max_tokens": 64, "stream": stream}
	case modelcheckprofile.ProtocolAnthropic:
		path = "/v1/messages"
		payload = map[string]any{"model": model, "messages": []any{map[string]any{"role": "user", "content": prompt}}, "max_tokens": 64, "stream": stream}
	case modelcheckprofile.ProtocolGeminiNative:
		action := "generateContent"
		if stream {
			action = "streamGenerateContent"
		}
		path = "/v1beta/models/" + url.PathEscape(model) + ":" + action
		if stream {
			path += "?alt=sse"
		}
		payload = map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": prompt}}}}, "generationConfig": map[string]any{"maxOutputTokens": 64}}
	default:
		return Request{}, fmt.Errorf("unsupported J3b probe protocol %q", protocol)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Request{}, err
	}
	return Request{Path: path, ExpectedModel: model, Protocol: protocol, Body: body}, nil
}

// BuildStructured creates the protocol-native JSON-schema probe. The schema
// is deliberately small and stable so the result can be evaluated without
// persisting provider response bytes.
func BuildStructured(protocol modelcheckprofile.Protocol, model string, stream bool) (Request, error) {
	request, err := BuildBasic(protocol, model, `Return {"status":"ok","value":7} as JSON.`, stream)
	if err != nil {
		return Request{}, err
	}
	var payload map[string]any
	if err := json.Unmarshal(request.Body, &payload); err != nil {
		return Request{}, err
	}
	schema := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"status": map[string]any{"type": "string", "enum": []string{"ok"}},
			"value":  map[string]any{"type": "integer"},
		},
		"required": []string{"status", "value"},
	}
	switch protocol {
	case modelcheckprofile.ProtocolOpenAIResponses:
		payload["text"] = map[string]any{"format": map[string]any{"type": "json_schema", "name": "model_check_structured_output", "strict": true, "schema": schema}}
	case modelcheckprofile.ProtocolOpenAIChat:
		payload["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "model_check_structured_output", "strict": true, "schema": schema}}
	case modelcheckprofile.ProtocolGeminiNative:
		payload["generationConfig"] = map[string]any{"maxOutputTokens": 128, "responseMimeType": "application/json", "responseSchema": map[string]any{
			"type": "OBJECT", "properties": map[string]any{
				"status": map[string]any{"type": "STRING", "enum": []string{"ok"}},
				"value":  map[string]any{"type": "INTEGER"},
			}, "required": []string{"status", "value"},
		}}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Request{}, err
	}
	request.Body = body
	return request, nil
}

// BuildTool creates a forced function/tool call probe using each provider's
// native request shape.
func BuildTool(protocol modelcheckprofile.Protocol, model string, stream bool) (Request, error) {
	request, err := BuildBasic(protocol, model, `Call the provided function with code "ok" and count 1.`, stream)
	if err != nil {
		return Request{}, err
	}
	var payload map[string]any
	if err := json.Unmarshal(request.Body, &payload); err != nil {
		return Request{}, err
	}
	parameters := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
		"code": map[string]any{"type": "string"}, "count": map[string]any{"type": "integer"},
	}, "required": []string{"code", "count"}}
	switch protocol {
	case modelcheckprofile.ProtocolOpenAIResponses:
		payload["tools"] = []any{map[string]any{"type": "function", "name": "record_model_check", "description": "Record a model check marker.", "parameters": parameters}}
		payload["tool_choice"] = map[string]any{"type": "function", "name": "record_model_check"}
	case modelcheckprofile.ProtocolOpenAIChat:
		payload["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "record_model_check", "description": "Record a model check marker.", "parameters": parameters}}}
		payload["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": "record_model_check"}}
	case modelcheckprofile.ProtocolAnthropic:
		payload["tools"] = []any{map[string]any{"name": "record_model_check", "description": "Record a model check marker.", "input_schema": parameters}}
		payload["tool_choice"] = map[string]any{"type": "tool", "name": "record_model_check"}
	case modelcheckprofile.ProtocolGeminiNative:
		payload["tools"] = []any{map[string]any{"functionDeclarations": []any{map[string]any{
			"name": "record_model_check", "description": "Record a model check marker.",
			"parameters": map[string]any{"type": "OBJECT", "properties": map[string]any{"code": map[string]any{"type": "STRING"}, "count": map[string]any{"type": "INTEGER"}}, "required": []string{"code", "count"}},
		}}}}
		payload["toolConfig"] = map[string]any{"functionCallingConfig": map[string]any{"mode": "ANY", "allowedFunctionNames": []string{"record_model_check"}}}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Request{}, err
	}
	request.Body = body
	return request, nil
}

func Execute(ctx context.Context, request Request, options Options) (Result, error) {
	started := time.Now()
	result := Result{ExpectedModel: request.ExpectedModel}
	endpoint, err := buildURL(options.Endpoint, request.Protocol, request.Path)
	if err != nil {
		result.ErrorMessage = err.Error()
		return result, err
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, strings.NewReader(string(request.Body)))
	if err != nil {
		return result, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if strings.Contains(request.Path, "alt=sse") || strings.Contains(string(request.Body), `"stream":true`) {
		httpRequest.Header.Set("Accept", "text/event-stream")
	} else {
		httpRequest.Header.Set("Accept", "application/json")
	}
	for key, values := range options.Headers {
		for _, value := range values {
			httpRequest.Header.Add(key, value)
		}
	}
	client := options.Client
	if client == nil {
		client = &http.Client{}
	}
	response, err := client.Do(httpRequest)
	result.Duration = time.Since(started)
	if err != nil {
		if errors.Is(requestCtx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			result.ErrorMessage = "J3b probe canceled"
		} else if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			result.ErrorMessage = "J3b probe timed out"
		} else {
			result.ErrorMessage = "J3b upstream request failed"
		}
		return result, nil
	}
	defer response.Body.Close()
	maxBytes := options.MaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxResponse
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if int64(len(body)) > maxBytes {
		result.ErrorMessage = "J3b upstream response exceeded limit"
		return result, nil
	}
	result.HTTPStatus = response.StatusCode
	if readErr != nil {
		result.ErrorMessage = "J3b upstream response read failed"
		return result, nil
	}
	result.ObservedModel, result.Output, result.Usage, result.JSON = parseResponse(request.Protocol, body)
	result.Success = response.StatusCode == http.StatusOK
	if !result.Success && result.ErrorMessage == "" {
		result.ErrorMessage = fmt.Sprintf("J3b upstream returned HTTP %d", response.StatusCode)
	}
	return result, nil
}

func buildURL(endpoint string, protocol modelcheckprofile.Protocol, path string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || strings.ContainsAny(endpoint, "\r\n\t\\") {
		return "", errors.New("J3b probe endpoint URL is invalid")
	}
	parts := strings.SplitN(path, "?", 2)
	requestPath, query := parts[0], ""
	if len(parts) == 2 {
		query = parts[1]
	}
	basePath := strings.TrimRight(base.Path, "/")
	if protocol == modelcheckprofile.ProtocolGeminiNative {
		if !strings.HasSuffix(basePath, "/v1beta") {
			basePath += "/v1beta"
		}
		requestPath = strings.TrimPrefix(requestPath, "/v1beta")
	} else {
		if !strings.HasSuffix(basePath, "/v1") {
			basePath += "/v1"
		}
		requestPath = strings.TrimPrefix(requestPath, "/v1")
	}
	base.Path = strings.TrimRight(basePath, "/") + "/" + strings.TrimLeft(requestPath, "/")
	base.RawQuery = query
	base.RawPath = ""
	return base.String(), nil
}

func parseResponse(protocol modelcheckprofile.Protocol, body []byte) (string, string, map[string]any, map[string]any) {
	var value map[string]any
	if json.Unmarshal(body, &value) != nil {
		return "", strings.TrimSpace(string(body)), nil, nil
	}
	model, _ := value["model"].(string)
	if model == "" {
		if data, ok := value["data"].([]any); ok && len(data) > 0 {
			if first, ok := data[0].(map[string]any); ok {
				model, _ = first["model"].(string)
			}
		}
	}
	if protocol == modelcheckprofile.ProtocolOpenAIChat {
		if choices, ok := value["choices"].([]any); ok && len(choices) > 0 {
			if choice, ok := choices[0].(map[string]any); ok {
				if message, ok := choice["message"].(map[string]any); ok {
					output, _ := message["content"].(string)
					return model, output, mapValue(value["usage"]), value
				}
			}
		}
	}
	if protocol == modelcheckprofile.ProtocolAnthropic {
		for _, entry := range listValue(value["content"]) {
			if record, ok := entry.(map[string]any); ok && record["type"] == "text" {
				output, _ := record["text"].(string)
				return model, output, mapValue(value["usage"]), value
			}
		}
	}
	if protocol == modelcheckprofile.ProtocolGeminiNative {
		for _, candidate := range listValue(value["candidates"]) {
			candidateRecord, _ := candidate.(map[string]any)
			content, _ := candidateRecord["content"].(map[string]any)
			for _, part := range listValue(content["parts"]) {
				if record, ok := part.(map[string]any); ok {
					if output, ok := record["text"].(string); ok {
						return model, output, mapValue(value["usageMetadata"]), value
					}
				}
			}
		}
	}
	output, _ := value["output_text"].(string)
	if output == "" {
		output, _ = value["text"].(string)
	}
	if output == "" {
		for _, entry := range listValue(value["output"]) {
			record, _ := entry.(map[string]any)
			for _, content := range listValue(record["content"]) {
				part, _ := content.(map[string]any)
				if text, ok := part["text"].(string); ok {
					output = text
					break
				}
			}
		}
	}
	return model, output, mapValue(value["usage"]), value
}

func listValue(value any) []any {
	items, _ := value.([]any)
	return items
}

func mapValue(value any) map[string]any {
	if result, ok := value.(map[string]any); ok {
		return result
	}
	return nil
}
