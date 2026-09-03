// Package modelcheckprobe contains the Gateway-owned direct transport for J3b.
// It returns parsed facts only; credentials and raw response bodies never
// leave the request attempt.
package modelcheckprobe

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	keymodelruntime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/key_model_runtime"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

const (
	DefaultTimeout          = 30 * time.Second
	DefaultMaxResponse      = 2 << 20
	OpenAIOAuthCodexBaseURL = "https://chatgpt.com/backend-api/codex"
)

type Request struct {
	Path, ExpectedModel string
	// RequestModel keeps the public model requested before an explicit Business
	// mapping selected ExpectedModel. Some upstreams echo that public model even
	// though the mapped upstream model was sent; it is valid mapping evidence,
	// not an undeclared substitution.
	RequestModel        string
	ModelMappingApplied bool
	Protocol            modelcheckprofile.Protocol
	// EndpointMode is the immutable Business health-check request shape. It
	// remains explicit so callers do not infer a path from protocol alone.
	EndpointMode string
	Body         json.RawMessage
}

const (
	AdapterOpenAIOAuthCodex = "openai_oauth_codex"
)

type Result struct {
	HTTPStatus          int
	Success             bool
	ExpectedModel       string
	RequestModel        string
	ModelMappingApplied bool
	ObservedModel       string
	Output              string
	Usage               map[string]any
	JSON                map[string]any
	ErrorMessage        string
	Duration            time.Duration
	RetryAttemptCount   int
	RetryMaxAttempts    int
	AttemptStatusCodes  []int
	RetryWaitDurations  []time.Duration
	AttemptDetails      []AttemptDetail
}

// AttemptDetail is bounded retry metadata safe to retain in diagnostic
// evidence. It never contains request headers, credentials, or response body.
type AttemptDetail struct {
	StartedAt  time.Time
	Duration   time.Duration
	HTTPStatus int
	Error      string
}

type Options struct {
	Endpoint         string
	Headers          http.Header
	Client           *http.Client
	Timeout          time.Duration
	MaxResponseBytes int64
	Dispatcher       DispatcherPort
	Capability       keymodelruntime.Capability
	Adapter          string
}

// Dispatcher is the in-process transport owner used by a fully wired
// Gateway. The release callback must be invoked exactly once with the final
// upstream framing outcome.
type DispatcherPort interface {
	Dispatch(context.Context, *http.Request, keymodelruntime.Capability, string) (response *http.Response, settle func(success bool), err error)
}

// ClientDispatcherPort is an optional extension for dispatchers that can
// honor the resolved per-target HTTP client. DispatcherPort remains the
// compatibility surface for existing fakes and older adapters.
type ClientDispatcherPort interface {
	DispatchWithClient(context.Context, *http.Request, keymodelruntime.Capability, string, *http.Client) (response *http.Response, settle func(success bool), err error)
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
	return Request{Path: path, ExpectedModel: model, Protocol: protocol, EndpointMode: modelcheckprofile.EndpointModeForProtocol(protocol, stream), Body: body}, nil
}

// BuildOpenAIOAuthCodexBasic mirrors the bounded Responses shape consumed by
// Node's OAuth Codex adapter. It is intentionally separate from the ordinary
// OpenAI builder so API-key requests cannot accidentally inherit Codex fields.
func BuildOpenAIOAuthCodexBasic(model, prompt string, stream bool) (Request, error) {
	if strings.TrimSpace(model) == "" || strings.TrimSpace(prompt) == "" {
		return Request{}, errors.New("J3b probe model and prompt are required")
	}
	payload := map[string]any{
		"model":        model,
		"input":        []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": prompt}}}},
		"instructions": "",
		"store":        false,
		"stream":       stream,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Request{}, err
	}
	return Request{Path: "/responses", ExpectedModel: model, Protocol: modelcheckprofile.ProtocolOpenAIResponses, EndpointMode: modelcheckprofile.EndpointModeForProtocol(modelcheckprofile.ProtocolOpenAIResponses, stream), Body: body}, nil
}

func BuildOpenAIOAuthCodexStructured(model string, stream bool) (Request, error) {
	request, err := BuildOpenAIOAuthCodexBasic(model, `Return {"status":"ok","value":7} as JSON.`, stream)
	if err != nil {
		return Request{}, err
	}
	var payload map[string]any
	if err := json.Unmarshal(request.Body, &payload); err != nil {
		return Request{}, err
	}
	payload["text"] = map[string]any{"format": map[string]any{"type": "json_schema", "name": "model_check_structured_output", "strict": true, "schema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"status": map[string]any{"type": "string", "enum": []string{"ok"}}, "value": map[string]any{"type": "integer"}}, "required": []string{"status", "value"}}}}
	request.Body, err = json.Marshal(payload)
	return request, err
}

func BuildOpenAIOAuthCodexTool(model string, stream bool) (Request, error) {
	request, err := BuildOpenAIOAuthCodexBasic(model, `Call the provided function with code "ok" and count 1.`, stream)
	if err != nil {
		return Request{}, err
	}
	var payload map[string]any
	if err := json.Unmarshal(request.Body, &payload); err != nil {
		return Request{}, err
	}
	parameters := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"code": map[string]any{"type": "string"}, "count": map[string]any{"type": "integer"}}, "required": []string{"code", "count"}}
	payload["tools"] = []any{map[string]any{"type": "function", "name": "record_model_check", "description": "Record a model check marker.", "parameters": parameters}}
	payload["tool_choice"] = map[string]any{"type": "function", "name": "record_model_check"}
	request.Body, err = json.Marshal(payload)
	return request, err
}

// BuildBasicForEndpointMode builds the same bounded probe using the explicit
// account health-check mode. Unsupported modes fail closed instead of being
// rewritten to a different protocol or path.
func BuildBasicForEndpointMode(protocol modelcheckprofile.Protocol, model, prompt, endpointMode string) (Request, error) {
	mode := strings.TrimSpace(endpointMode)
	if mode == "" {
		return BuildBasic(protocol, model, prompt, false)
	}
	if !modelcheckprofile.EndpointModeMatchesProtocol(protocol, mode) {
		return Request{}, fmt.Errorf("J3b probe endpoint mode %q does not match protocol %q", mode, protocol)
	}
	request, err := BuildBasic(protocol, model, prompt, modelcheckprofile.EndpointModeIsStreaming(mode))
	if err != nil {
		return Request{}, err
	}
	request.EndpointMode = mode
	return request, nil
}

func buildBasicWithEndpointMode(protocol modelcheckprofile.Protocol, model, prompt string, stream bool, endpointMode string) (Request, error) {
	if strings.TrimSpace(endpointMode) != "" {
		return BuildBasicForEndpointMode(protocol, model, prompt, endpointMode)
	}
	return BuildBasic(protocol, model, prompt, stream)
}

// buildBasicWithTunings mirrors the Node payload factories that vary per-probe
// output budgets and sampling temperature. Protocol floors match the Node
// oracle exactly: Responses requests never ask for fewer than 16 output
// tokens, chat requests never ask for fewer than 64 completion tokens and
// Gemini never fewer than 128; Anthropic is sent verbatim without a
// temperature field.
func buildBasicWithTunings(protocol modelcheckprofile.Protocol, model, prompt, endpointMode string, stream bool, maxOutputTokens int, temperature float64) (Request, error) {
	request, err := buildBasicWithEndpointMode(protocol, model, prompt, stream, endpointMode)
	if err != nil {
		return Request{}, err
	}
	var payload map[string]any
	if err := json.Unmarshal(request.Body, &payload); err != nil {
		return Request{}, err
	}
	switch protocol {
	case modelcheckprofile.ProtocolOpenAIResponses:
		tokens := maxOutputTokens
		if tokens < 16 {
			tokens = 16
		}
		payload["max_output_tokens"] = tokens
		payload["temperature"] = temperature
	case modelcheckprofile.ProtocolOpenAIChat:
		tokens := maxOutputTokens
		if tokens < 64 {
			tokens = 64
		}
		payload["max_tokens"] = tokens
		payload["temperature"] = temperature
	case modelcheckprofile.ProtocolAnthropic:
		payload["max_tokens"] = maxOutputTokens
	case modelcheckprofile.ProtocolGeminiNative:
		generation, _ := payload["generationConfig"].(map[string]any)
		if generation == nil {
			generation = map[string]any{}
		}
		tokens := maxOutputTokens
		if tokens < 128 {
			tokens = 128
		}
		generation["maxOutputTokens"] = tokens
		generation["temperature"] = temperature
		payload["generationConfig"] = generation
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Request{}, err
	}
	request.Body = body
	return request, nil
}

// BuildStructured creates the protocol-native JSON-schema probe. The schema
// is deliberately small and stable so the result can be evaluated without
// persisting provider response bytes.
// applyStructuredToolTunings pins the Node sampling defaults on the generic
// structured/tool bodies: temperature 0 for responses/chat/gemini (Anthropic
// is sent verbatim) and the Node 64/128-token budgets.
func applyStructuredToolTunings(payload map[string]any, protocol modelcheckprofile.Protocol) {
	switch protocol {
	case modelcheckprofile.ProtocolOpenAIResponses, modelcheckprofile.ProtocolOpenAIChat:
		payload["temperature"] = 0
	case modelcheckprofile.ProtocolGeminiNative:
		generation, _ := payload["generationConfig"].(map[string]any)
		if generation == nil {
			generation = map[string]any{}
		}
		generation["temperature"] = 0
		generation["maxOutputTokens"] = 128
		payload["generationConfig"] = generation
	}
}

func BuildStructured(protocol modelcheckprofile.Protocol, model string, stream bool) (Request, error) {
	request, err := BuildBasic(protocol, model, `Return {"status":"ok","value":7} as JSON.`, stream)
	if err != nil {
		return Request{}, err
	}
	var payload map[string]any
	if err := json.Unmarshal(request.Body, &payload); err != nil {
		return Request{}, err
	}
	applyStructuredToolTunings(payload, protocol)
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
		payload["generationConfig"] = map[string]any{"maxOutputTokens": 128, "temperature": 0, "responseMimeType": "application/json", "responseSchema": map[string]any{
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

func BuildStructuredForEndpointMode(protocol modelcheckprofile.Protocol, model, endpointMode string) (Request, error) {
	request, err := BuildBasicForEndpointMode(protocol, model, `Return {"status":"ok","value":7} as JSON.`, endpointMode)
	if err != nil {
		return Request{}, err
	}
	var payload map[string]any
	if err := json.Unmarshal(request.Body, &payload); err != nil {
		return Request{}, err
	}
	applyStructuredToolTunings(payload, protocol)
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
		payload["generationConfig"] = map[string]any{"maxOutputTokens": 128, "temperature": 0, "responseMimeType": "application/json", "responseSchema": map[string]any{
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
	applyStructuredToolTunings(payload, protocol)
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

func BuildToolForEndpointMode(protocol modelcheckprofile.Protocol, model, endpointMode string) (Request, error) {
	request, err := BuildBasicForEndpointMode(protocol, model, `Call the provided function with code "ok" and count 1.`, endpointMode)
	if err != nil {
		return Request{}, err
	}
	var payload map[string]any
	if err := json.Unmarshal(request.Body, &payload); err != nil {
		return Request{}, err
	}
	applyStructuredToolTunings(payload, protocol)
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
	result := Result{ExpectedModel: request.ExpectedModel, RequestModel: request.RequestModel, ModelMappingApplied: request.ModelMappingApplied}
	headers := options.Headers.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	if options.Adapter == AdapterOpenAIOAuthCodex {
		var err error
		request.Body, headers, err = normalizeOpenAIOAuthCodexRequest(request, headers)
		if err != nil {
			result.ErrorMessage = err.Error()
			return result, err
		}
		request.Path = "/responses"
		request.Protocol = modelcheckprofile.ProtocolOpenAIResponses
	}
	endpoint, err := buildURL(options.Endpoint, request.Protocol, request.Path, options.Adapter)
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
	for key, values := range headers {
		for _, value := range values {
			httpRequest.Header.Add(key, value)
		}
	}
	var response *http.Response
	var settle func(bool)
	if options.Dispatcher != nil {
		capability := options.Capability
		if capability.ClientModel == "" {
			capability.ClientModel = request.ExpectedModel
		}
		if capability.FinalUpstreamModel == "" {
			capability.FinalUpstreamModel = request.ExpectedModel
		}
		if capability.ClientEndpointFamily == "" {
			capability.ClientEndpointFamily = string(request.Protocol)
		}
		if capability.UpstreamEndpointMode == "" {
			capability.UpstreamEndpointMode = request.EndpointMode
			if capability.UpstreamEndpointMode == "" {
				capability.UpstreamEndpointMode = string(request.Protocol)
			}
		}
		attemptID := fmt.Sprintf("model-check-%d", started.UnixNano())
		if dispatcher, ok := options.Dispatcher.(ClientDispatcherPort); ok {
			// A nil client means the dispatcher should use its configured
			// default. Do not pass the direct-transport fallback here.
			response, settle, err = dispatcher.DispatchWithClient(requestCtx, httpRequest, capability, attemptID, options.Client)
		} else {
			response, settle, err = options.Dispatcher.Dispatch(requestCtx, httpRequest, capability, attemptID)
		}
	} else {
		client := options.Client
		if client == nil {
			client = &http.Client{}
		}
		response, err = client.Do(httpRequest)
	}
	result.Duration = time.Since(started)
	if err != nil {
		if settle != nil {
			settle(false)
		}
		if errors.Is(requestCtx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			result.ErrorMessage = "J3b probe canceled"
		} else if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			result.ErrorMessage = "J3b probe timed out"
		} else {
			result.ErrorMessage = "J3b upstream request failed"
		}
		return result, nil
	}
	if settle != nil {
		defer settle(false)
	}
	defer response.Body.Close()
	maxBytes := options.MaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxResponse
	}
	// Preserve the upstream status as soon as the response is available. A
	// bounded/read failure is still an HTTP response and its status is useful
	// evidence to the caller.
	result.HTTPStatus = response.StatusCode
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if int64(len(body)) > maxBytes {
		result.ErrorMessage = "J3b upstream response exceeded limit"
		return result, nil
	}
	if readErr != nil {
		result.ErrorMessage = "J3b upstream response read failed"
		return result, nil
	}
	result.ObservedModel, result.Output, result.Usage, result.JSON, result.ErrorMessage = parseResponseDetailed(request.Protocol, body)
	result.Success = response.StatusCode == http.StatusOK && result.ErrorMessage == ""
	if result.Success && settle != nil {
		settle(true)
		settle = nil
	}
	if !result.Success && result.ErrorMessage == "" {
		result.ErrorMessage = fmt.Sprintf("J3b upstream returned HTTP %d", response.StatusCode)
	}
	return result, nil
}

func buildURL(endpoint string, protocol modelcheckprofile.Protocol, path string, adapter string) (string, error) {
	if adapter == AdapterOpenAIOAuthCodex {
		base, err := url.Parse(strings.TrimSpace(endpoint))
		if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || strings.ContainsAny(endpoint, "\r\n\t\\") {
			return "", errors.New("J3b probe endpoint URL is invalid")
		}
		parts := strings.SplitN(path, "?", 2)
		requestPath := parts[0]
		if requestPath != "/responses" {
			return "", errors.New("J3b OpenAI OAuth Codex request path is unsupported")
		}
		return OpenAIOAuthCodexBaseURL + requestPath, nil
	}
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
	model, output, usage, value, _ := parseResponseDetailed(protocol, body)
	return model, output, usage, value
}

// parseResponseDetailed parses the complete response before returning. In
// particular, an early valid SSE event must not hide a later failure envelope.
// The final string is a protocol-level error message, distinct from transport
// errors handled by Execute.
func parseResponseDetailed(protocol modelcheckprofile.Protocol, body []byte) (string, string, map[string]any, map[string]any, string) {
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf}))
	var directValue map[string]any
	if json.Unmarshal(trimmed, &directValue) == nil && directValue != nil {
		model, output, usage, parsed := parseJSONResponse(protocol, trimmed)
		return model, output, usage, parsed, responseErrorMessage("", parsed)
	}

	events := parseSSEResponseEvents(trimmed)
	if len(events) == 0 {
		return "", strings.TrimSpace(string(body)), nil, nil, ""
	}

	var (
		model, output string
		usage         map[string]any
		value         map[string]any
		terminal      string
		deltas        []string
		errorMessage  string
	)
	for _, event := range events {
		payload := event.payload
		if payload == nil {
			continue
		}
		candidate := payload
		if nested, ok := payload["response"].(map[string]any); ok {
			candidate = nested
		}
		encoded, err := json.Marshal(candidate)
		if err != nil {
			continue
		}
		eventModel, eventOutput, eventUsage, parsed := parseJSONResponse(protocol, encoded)
		if eventOutput == "" {
			eventOutput = parseSSEEventOutput(protocol, payload)
		}
		if model == "" && eventModel != "" {
			model = eventModel
		}
		if usage == nil && eventUsage != nil {
			usage = eventUsage
		}
		if parsed != nil && responseValueHasEvidence(protocol, parsed) {
			value = parsed
		}

		typeName := strings.TrimSpace(asString(payload["type"]))
		if typeName == "" {
			typeName = event.event
		}
		if isResponseTerminalEvent(typeName, candidate) {
			if eventOutput != "" {
				terminal = eventOutput
			}
			if nested, ok := payload["response"].(map[string]any); ok {
				value = nested
			}
		} else if eventOutput != "" {
			deltas = append(deltas, eventOutput)
		}
		if errorMessage == "" {
			errorMessage = responseErrorMessage(typeName, payload)
		}
	}
	if terminal != "" {
		output = terminal
	} else {
		output = strings.TrimSpace(strings.Join(deltas, ""))
	}
	return model, output, usage, value, errorMessage
}

type sseResponseEvent struct {
	event   string
	payload map[string]any
}

func parseSSEResponseEvents(body []byte) []sseResponseEvent {
	var events []sseResponseEvent
	var eventName string
	var dataLines []string
	flush := func() {
		if len(dataLines) == 0 {
			eventName = ""
			return
		}
		data := strings.TrimSpace(strings.Join(dataLines, "\n"))
		if data != "" && data != "[DONE]" {
			var payload map[string]any
			if json.Unmarshal([]byte(data), &payload) == nil && payload != nil {
				events = append(events, sseResponseEvent{event: eventName, payload: payload})
			}
		}
		eventName = ""
		dataLines = nil
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, fieldValue := line, ""
		if separator := strings.IndexByte(line, ':'); separator >= 0 {
			field, fieldValue = line[:separator], line[separator+1:]
			fieldValue = strings.TrimPrefix(fieldValue, " ")
		}
		switch field {
		case "event":
			eventName = fieldValue
		case "data":
			dataLines = append(dataLines, fieldValue)
		}
	}
	flush()
	return events
}

func parseJSONResponse(protocol modelcheckprofile.Protocol, body []byte) (string, string, map[string]any, map[string]any) {
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

func parseSSEEventOutput(protocol modelcheckprofile.Protocol, payload map[string]any) string {
	switch protocol {
	case modelcheckprofile.ProtocolOpenAIResponses:
		return asString(payload["delta"]) + asString(payload["text"])
	case modelcheckprofile.ProtocolOpenAIChat:
		var parts []string
		for _, choice := range listValue(payload["choices"]) {
			choiceRecord := mapValue(choice)
			for _, field := range []string{"message", "delta"} {
				container := mapValue(choiceRecord[field])
				parts = append(parts, asString(container["content"]), asString(container["reasoning_content"]), asString(container["refusal"]))
			}
		}
		return strings.TrimSpace(strings.Join(parts, ""))
	case modelcheckprofile.ProtocolAnthropic:
		return asString(mapValue(payload["delta"])["text"])
	default:
		return ""
	}
}

func responseErrorMessage(eventType string, payload map[string]any) string {
	if payload == nil {
		return ""
	}
	if nested, ok := payload["response"].(map[string]any); ok {
		if message := responseErrorMessage(eventType, nested); message != "" {
			return message
		}
	}
	if errorValue, ok := payload["error"]; ok {
		if message := errorValueMessage(errorValue); message != "" {
			return message
		}
	}
	if message := asString(payload["message"]); message != "" {
		return message
	}
	if isFailureEvent(eventType) {
		return eventType
	}
	return ""
}

func errorValueMessage(value any) string {
	if message := asString(value); message != "" {
		return message
	}
	record := mapValue(value)
	if record == nil {
		return ""
	}
	for _, key := range []string{"message", "code", "type", "status"} {
		if message := asString(record[key]); message != "" {
			return message
		}
	}
	return ""
}

func isFailureEvent(eventType string) bool {
	switch eventType {
	case "response.failed", "response.incomplete", "error":
		return true
	default:
		return false
	}
}

func isResponseTerminalEvent(eventType string, payload map[string]any) bool {
	if eventType == "response.completed" || eventType == "response.done" {
		return true
	}
	response, _ := payload["response"].(map[string]any)
	return asString(response["status"]) == "completed"
}

func responseValueHasEvidence(protocol modelcheckprofile.Protocol, value map[string]any) bool {
	if value == nil {
		return false
	}
	if asString(value["model"]) != "" || asString(value["output_text"]) != "" || asString(value["text"]) != "" {
		return true
	}
	switch protocol {
	case modelcheckprofile.ProtocolOpenAIChat:
		return len(listValue(value["choices"])) > 0
	case modelcheckprofile.ProtocolAnthropic:
		return len(listValue(value["content"])) > 0
	case modelcheckprofile.ProtocolGeminiNative:
		return len(listValue(value["candidates"])) > 0
	default:
		return len(listValue(value["output"])) > 0
	}
}

func asString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func normalizeOpenAIOAuthCodexRequest(request Request, headers http.Header) ([]byte, http.Header, error) {
	if request.Protocol != modelcheckprofile.ProtocolOpenAIResponses || (request.EndpointMode != modelcheckprofile.EndpointModeResponsesJSON && request.EndpointMode != modelcheckprofile.EndpointModeResponsesSSE) {
		return nil, headers, errors.New("J3b OpenAI OAuth Codex endpoint mode is unsupported")
	}
	var body map[string]any
	if err := json.Unmarshal(request.Body, &body); err != nil || body == nil {
		return nil, headers, errors.New("J3b OpenAI OAuth Codex request body is invalid")
	}
	model, ok := body["model"].(string)
	if !ok || strings.TrimSpace(model) == "" {
		return nil, headers, errors.New("J3b OpenAI OAuth Codex request model is invalid")
	}
	if input, ok := body["input"].(string); ok {
		body["input"] = []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": input}}}}
	} else if _, ok := body["input"].([]any); !ok {
		return nil, headers, errors.New("J3b OpenAI OAuth Codex request input is invalid")
	}
	if instructions, present := body["instructions"]; present {
		if _, ok := instructions.(string); !ok {
			return nil, headers, errors.New("J3b OpenAI OAuth Codex request instructions are invalid")
		}
	} else {
		body["instructions"] = ""
	}
	for _, field := range []string{"background", "conversation", "context_management", "frequency_penalty", "max_completion_tokens", "max_output_tokens", "metadata", "presence_penalty", "prompt_cache_retention", "safety_identifier", "stream_options", "temperature", "top_p", "truncation", "user"} {
		delete(body, field)
	}
	body["store"] = false
	body["stream"] = true
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "gpt-5.6-") {
		reasoning, _ := body["reasoning"].(map[string]any)
		if reasoning == nil {
			reasoning = map[string]any{}
		}
		reasoning["context"] = "all_turns"
		body["reasoning"] = reasoning
		body["parallel_tool_calls"] = false
	}
	if _, ok := headers["Authorization"]; !ok && headers.Get("Authorization") == "" {
		return nil, headers, errors.New("J3b OpenAI OAuth Codex authorization is missing")
	}
	headers.Set("originator", "Codex Desktop")
	headers.Set("user-agent", "Codex Desktop/0.145.0 (Windows 10.0.22621; x86_64) unknown (codex_exec; 0.145.0)")
	sessionID, err := codexUUID()
	if err != nil {
		return nil, headers, errors.New("J3b OpenAI OAuth Codex identity unavailable")
	}
	if headers.Get("session-id") == "" {
		headers.Set("session-id", sessionID)
	}
	if headers.Get("thread-id") == "" {
		headers.Set("thread-id", headers.Get("session-id"))
	}
	if headers.Get("x-client-request-id") == "" {
		headers.Set("x-client-request-id", headers.Get("session-id"))
	}
	if headers.Get("x-codex-beta-features") == "" {
		headers.Set("x-codex-beta-features", "remote_compaction_v2")
	}
	if headers.Get("x-codex-window-id") == "" {
		headers.Set("x-codex-window-id", headers.Get("thread-id")+":0")
	}
	metadata := map[string]any{"installation_id": mustHeaderUUID(headers, "x-codex-installation-id", &err), "session_id": headers.Get("session-id"), "thread_id": headers.Get("thread-id"), "turn_id": headers.Get("x-client-request-id"), "window_id": headers.Get("x-codex-window-id"), "request_kind": "turn", "thread_source": "user", "sandbox": "none", "turn_started_at_unix_ms": time.Now().UnixMilli()}
	if err != nil {
		return nil, headers, errors.New("J3b OpenAI OAuth Codex identity unavailable")
	}
	if headers.Get("x-codex-installation-id") == "" {
		headers.Set("x-codex-installation-id", metadata["installation_id"].(string))
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return nil, headers, err
	}
	headers.Set("x-codex-turn-metadata", string(metadataJSON))
	headers.Set("content-type", "application/json")
	headers.Set("accept", "text/event-stream")
	if headers.Get("openai-beta") == "" {
		headers.Set("openai-beta", "responses=experimental")
	}
	encoded, err := json.Marshal(body)
	return encoded, headers, err
}

func mustHeaderUUID(headers http.Header, name string, err *error) string {
	if value := headers.Get(name); value != "" {
		return value
	}
	value, uuidErr := codexUUID()
	if uuidErr != nil {
		*err = uuidErr
		return ""
	}
	return value
}

func codexUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s", hex.EncodeToString(value[0:4]), hex.EncodeToString(value[4:6]), hex.EncodeToString(value[6:8]), hex.EncodeToString(value[8:10]), hex.EncodeToString(value[10:16])), nil
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
