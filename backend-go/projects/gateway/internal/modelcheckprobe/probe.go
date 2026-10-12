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
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	keymodelruntime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/key_model_runtime"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamenvelope"
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
	// ItemKey labels the probe family a request belongs to (for example
	// "protocol_basic"). It feeds only the per-probe progress hooks; an empty
	// label never fires a hook and changes nothing about execution.
	ItemKey string
}

const (
	AdapterOpenAIOAuthCodex = "openai_oauth_codex"
)

type Result struct {
	HTTPStatus int
	Success    bool
	// Incomplete marks a transport-level failure: the response body was not
	// read to completion (connection interrupted mid-body, or the attempt
	// budget cut a still-active stream). It is request-failure evidence for
	// the retry ladder, never a quality verdict; Output then carries only the
	// best-effort partial text for diagnostics. BUG-0292.
	Incomplete          bool
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
	// ProtocolProfileID is the immutable upstream protocol profile selected by
	// the owner. It must reach upstreamenvelope.Spec unchanged so profile
	// specific URL rules (GLM/gemini-openai) are applied by the shared builder.
	ProtocolProfileID string
	// CredentialType preserves the owner-side account type when a Bearer-shaped
	// credential has OAuth semantics (for example oauth or google_oauth).
	CredentialType string
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
		payload = map[string]any{"model": model, "input": prompt, "instructions": "You are a model capability checker. Follow the requested output exactly.", "max_output_tokens": upstreamenvelope.DefaultProbeOutputTokens, "stream": stream, "store": false}
	case modelcheckprofile.ProtocolOpenAIChat:
		path = "/v1/chat/completions"
		payload = map[string]any{"model": model, "messages": []any{map[string]any{"role": "user", "content": prompt}}, "max_tokens": upstreamenvelope.DefaultProbeOutputTokens, "stream": stream}
	case modelcheckprofile.ProtocolAnthropic:
		path = "/v1/messages"
		payload = map[string]any{"model": model, "messages": []any{map[string]any{"role": "user", "content": prompt}}, "max_tokens": upstreamenvelope.DefaultProbeOutputTokens, "stream": stream}
	case modelcheckprofile.ProtocolGeminiNative:
		action := "generateContent"
		if stream {
			action = "streamGenerateContent"
		}
		path = "/v1beta/models/" + url.PathEscape(model) + ":" + action
		if stream {
			path += "?alt=sse"
		}
		payload = map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": prompt}}}}, "generationConfig": map[string]any{"maxOutputTokens": upstreamenvelope.DefaultProbeOutputTokens}}
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

// buildBasicWithTunings 构造各探针族的请求并统一应用输出预算下限
// （upstreamenvelope.DefaultProbeOutputTokens = 1024，信封统一设计 §4.3、
// BUG-0308 扩展裁决 2026-10-11）：生产实证 min-100 类中转会拒绝一切更小的
// 预算（protocol_basic 16、structured_output 64 先后 400），且思考模型会
// 把小预算耗尽在推理上输出为空。探针族调用方的语义预算只会被抬升到该
// 下限，不会被压低；预算是上限不是计费值。
func buildBasicWithTunings(protocol modelcheckprofile.Protocol, model, prompt, endpointMode string, stream bool, maxOutputTokens int, temperature float64) (Request, error) {
	if maxOutputTokens < upstreamenvelope.DefaultProbeOutputTokens {
		maxOutputTokens = upstreamenvelope.DefaultProbeOutputTokens
	}
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
		payload["max_output_tokens"] = maxOutputTokens
		payload["temperature"] = temperature
	case modelcheckprofile.ProtocolOpenAIChat:
		payload["max_tokens"] = maxOutputTokens
		payload["temperature"] = temperature
	case modelcheckprofile.ProtocolAnthropic:
		payload["max_tokens"] = maxOutputTokens
	case modelcheckprofile.ProtocolGeminiNative:
		generation, _ := payload["generationConfig"].(map[string]any)
		if generation == nil {
			generation = map[string]any{}
		}
		generation["maxOutputTokens"] = maxOutputTokens
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
		generation["maxOutputTokens"] = upstreamenvelope.DefaultProbeOutputTokens
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
		payload["generationConfig"] = map[string]any{"maxOutputTokens": upstreamenvelope.DefaultProbeOutputTokens, "temperature": 0, "responseMimeType": "application/json", "responseSchema": map[string]any{
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
		payload["generationConfig"] = map[string]any{"maxOutputTokens": upstreamenvelope.DefaultProbeOutputTokens, "temperature": 0, "responseMimeType": "application/json", "responseSchema": map[string]any{
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
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// 最终请求构造统一走共享信封（信封统一设计 §4.1/§9，W2 F3 接线）：
	// method、URL、标准/认证/身份头、预算字段、body 编码与 Codex 归一化全部
	// 由 upstreamenvelope.Build 承担；本包保留探针语义 body、凭据解析（从
	// 调用方头提取）、端点 SSRF 校验与 OAuth Codex 形态守卫。响应处理
	// （流式解析/评分/重试/结算）不在构造范围。
	var httpRequest *http.Request
	var err error
	if options.Adapter == AdapterOpenAIOAuthCodex {
		httpRequest, err = buildOAuthCodexRequest(requestCtx, request, options, headers)
	} else {
		httpRequest, err = buildAPIKeyRequest(requestCtx, request, options, headers)
	}
	if err != nil {
		result.ErrorMessage = err.Error()
		return result, err
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
		// BUG-0262：错误分类保留底层错误（含派发层 ErrAccountCircuitBlocked
		// 等），否则 attempt 证据只剩固定文案无法排障；外层 ctx 先于本次
		// attempt 预算到期（DeadlineExceeded）按超时归类，不误报 upstream
		// failed。requestCtx 未到期时 errors.Is 判 requestCtx.Err() 为 nil。
		switch {
		case errors.Is(requestCtx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
			result.ErrorMessage = "J3b probe canceled"
		case errors.Is(requestCtx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
			result.ErrorMessage = "J3b probe timed out"
		default:
			result.ErrorMessage = "J3b upstream request failed: " + err.Error()
		}
		return result, nil
	}
	// BUG-0248：defer 闭包按返回时的变量值判 nil。直接 defer settle(false)
	// 会在注册时捕获函数值，成功路径的 settle=nil 对它无效，函数返回时仍以
	// false 二次结算（生产实现有 sync.Once 保护，但端口契约被误用）。
	defer func() {
		if settle != nil {
			settle(false)
		}
	}()
	defer response.Body.Close()
	maxBytes := options.MaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxResponse
	}
	// Preserve the upstream status as soon as the response is available. A
	// bounded/read failure is still an HTTP response and its status is useful
	// evidence to the caller.
	result.HTTPStatus = response.StatusCode
	// BUG-0292：分块读取并保留已读 buffer。响应体读取中断（连接断开，或
	// 本级预算到点截断仍在活跃输出的流）不是质量定论：已读部分先按语义完
	// 整性裁决，不完整时保留部分证据并交由重试阶梯晋级（与 accountprobe
	// BUG-0287 readUpstreamWithClient 同源形态）。
	body, readErr := readResponseBody(response.Body, maxBytes, requestCtx)
	if int64(len(body)) > maxBytes {
		result.ErrorMessage = "J3b upstream response exceeded limit"
		return result, nil
	}
	var observedModel, output string
	var usage, parsed map[string]any
	var parseErrorMessage string
	semanticComplete := true
	if options.Adapter == AdapterOpenAIOAuthCodex {
		// OAuth Codex is an A-shape endpoint whose shared envelope forces SSE.
		// Keep its response parser equally strict: JSON or an unterminated delta
		// stream is not a successful model check, even when it contains text.
		observedModel, output, usage, parsed, parseErrorMessage, semanticComplete = parseOAuthCodexResponseDetailed(body)
	} else {
		observedModel, output, usage, parsed, parseErrorMessage = parseResponseDetailed(request.Protocol, body)
	}
	if options.Adapter == AdapterOpenAIOAuthCodex && response.StatusCode != http.StatusOK && !semanticComplete {
		if message := parseOAuthCodexHTTPFailureMessage(body); message != "" {
			parseErrorMessage = message
			semanticComplete = true
		}
	}
	if readErr != nil && semanticComplete && responseBodyIsSemanticallyCompleteForAdapter(options.Adapter, body, parseErrorMessage) {
		// 已读内容已携带完整语义（完整 JSON 或流式终态事件）且无流内失败
		// 信封：尾部传输错误不得覆盖语义成功，按完整响应正常判定。
		readErr = nil
	}
	if readErr != nil {
		if options.Adapter == AdapterOpenAIOAuthCodex && semanticComplete && parseErrorMessage != "" {
			result.ObservedModel, result.Output, result.Usage, result.JSON = observedModel, output, usage, parsed
			result.ErrorMessage = parseErrorMessage
			return result, nil
		}
		result.Incomplete = true
		// 已读部分文本只作排障证据保留，绝不冒充模型答案进入评分或裁判。
		result.Output = output
		if options.Adapter == AdapterOpenAIOAuthCodex && parseErrorMessage != "" {
			result.ErrorMessage = parseErrorMessage
			return result, nil
		}
		// 错误分类对齐 Do 阶段（上方 Do 分支同序）：本级预算到点按探针
		// 超时归类以保留阶梯晋级证据；外层取消不得伪造成上游读取失败。
		switch {
		case errors.Is(requestCtx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
			result.ErrorMessage = "J3b probe canceled"
		case errors.Is(requestCtx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
			result.ErrorMessage = "J3b probe timed out"
		default:
			result.ErrorMessage = "J3b upstream response read failed"
		}
		return result, nil
	}
	if options.Adapter == AdapterOpenAIOAuthCodex && !semanticComplete {
		// A 200 response without response.completed is a transport/incomplete
		// stream for retry purposes. Preserve the partial text, while keeping a
		// protocol error only when it is the best available diagnostic.
		result.Incomplete = true
		result.ObservedModel, result.Output, result.Usage, result.JSON = observedModel, output, usage, parsed
		result.ErrorMessage = parseErrorMessage
		if result.ErrorMessage == "" {
			result.ErrorMessage = "J3b OAuth Codex response stream incomplete"
		}
		return result, nil
	}
	result.ObservedModel, result.Output, result.Usage, result.JSON, result.ErrorMessage = observedModel, output, usage, parsed, parseErrorMessage
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

// readResponseBody reads the bounded upstream body in chunks and keeps every
// byte already received when the read ends in an error or the attempt budget
// expires mid-body. A buffer larger than maxBytes signals the limit branch at
// the call site; a nil error means the body was read to completion.
func readResponseBody(body io.Reader, maxBytes int64, requestCtx context.Context) ([]byte, error) {
	buffer := make([]byte, 0, 8192)
	chunk := make([]byte, 8192)
	for {
		if int64(len(buffer)) > maxBytes {
			return buffer, nil
		}
		n, readErr := body.Read(chunk)
		if n > 0 {
			buffer = append(buffer, chunk[:n]...)
		}
		if int64(len(buffer)) > maxBytes {
			return buffer, nil
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return buffer, nil
			}
			return buffer, readErr
		}
		if requestCtx.Err() != nil {
			return buffer, requestCtx.Err()
		}
		if n == 0 {
			return buffer, nil
		}
	}
}

// responseBodyIsSemanticallyComplete judges whether the already-read body
// already carries a complete response despite the transport read ending in an
// error: a fully parseable JSON document, or an SSE stream that has already
// delivered its terminal event, in both cases without an in-stream failure
// envelope. BUG-0292：终态已到但连接未干净关闭时按完整响应判定。
func responseBodyIsSemanticallyComplete(body []byte, parseErrorMessage string) bool {
	if parseErrorMessage != "" {
		return false
	}
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf}))
	var directValue map[string]any
	if json.Unmarshal(trimmed, &directValue) == nil && directValue != nil {
		return true
	}
	for _, event := range parseSSEResponseEvents(trimmed) {
		if event.payload == nil {
			continue
		}
		candidate := event.payload
		if nested, ok := event.payload["response"].(map[string]any); ok {
			candidate = nested
		}
		typeName := strings.TrimSpace(asString(event.payload["type"]))
		if typeName == "" {
			typeName = event.event
		}
		if isResponseTerminalEvent(typeName, candidate) {
			return true
		}
	}
	return false
}

// validateEndpointURL 端点安全校验保持在本包（SSRF 契约，wl_probe_extra_test
// 钉死）：控制字符/userinfo/query/fragment 一律拒绝，URL 归一化交由信封
// Build 的共享规则（信封统一设计 §4.1，M2 收敛裁决沿用）。
func validateEndpointURL(endpoint string) error {
	if base, err := url.Parse(strings.TrimSpace(endpoint)); err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || strings.ContainsAny(endpoint, "\r\n\t\\") {
		return errors.New("J3b probe endpoint URL is invalid")
	}
	return nil
}

// buildOAuthCodexRequest 经信封 AdapterOpenAIOAuthCodex 构造 OAuth Codex（A
// 形态）最终请求（信封统一设计 §9.1-A/§9.3/§9.5）：固定上游 /responses、
// 强制 SSE、openai-beta 缺省补齐、八身份头与 Codex 归一化全部由 Build/
// wrapCodexResponses 承担。本包保留：端点模式守卫（现行文案）、语义 body
// 解析、凭据解析（现行文案）、身份生成（§9.3 单源，替代原
// normalizeOpenAIOAuthCodexRequest 的双阶段随机）与 SSRF 校验。原 buildURL
// 的"仅 /responses"路径守卫随构造迁移删除：Execute 在旧流程中先改写
// request.Path="/responses" 再校验，该守卫实际不可达；路径现由
// wrapCodexResponses 结构性固定，调用方输入无法影响。调用方头（含
// Authorization）经 Spec.ExtraHeaders 逐字直通——Build 的认证/身份头先写，
// 调用方显式值最后覆盖，与旧直通语义等价；X-Codex-Installation-Id 是信封
// 八头集合外的 F3 现行第 9 身份头，由同一身份单源补写，与 turn metadata 的
// installation_id 恒等。
func buildOAuthCodexRequest(ctx context.Context, request Request, options Options, headers http.Header) (*http.Request, error) {
	if request.Protocol != modelcheckprofile.ProtocolOpenAIResponses || (request.EndpointMode != modelcheckprofile.EndpointModeResponsesJSON && request.EndpointMode != modelcheckprofile.EndpointModeResponsesSSE) {
		return nil, errors.New("J3b OpenAI OAuth Codex endpoint mode is unsupported")
	}
	var body map[string]any
	if err := json.Unmarshal(request.Body, &body); err != nil || body == nil {
		return nil, errors.New("J3b OpenAI OAuth Codex request body is invalid")
	}
	token, err := bearerToken(headers)
	if err != nil {
		return nil, err
	}
	identity, err := newCodexIdentity(headers)
	if err != nil {
		return nil, errors.New("J3b OpenAI OAuth Codex identity unavailable")
	}
	if err := validateEndpointURL(options.Endpoint); err != nil {
		return nil, err
	}
	// model/input/instructions 的校验由信封 wrapCodexResponses 承担（缺失
	// 或类型非法显式失败），本包不再重复归一化规则。
	fields := make([]upstreamenvelope.BodyField, 0, len(body))
	for key, value := range body {
		fields = append(fields, upstreamenvelope.BodyField{Key: key, Value: value})
	}
	extraHeaders := callerExtraHeaders(headers)
	for _, name := range []string{
		"Originator", "User-Agent", "Session-Id", "Thread-Id", "X-Codex-Window-Id",
		"X-Codex-Installation-Id", "X-Codex-Turn-Metadata",
	} {
		deleteHeaderFold(extraHeaders, name)
	}
	// X-Client-Request-Id is deliberately retained in ExtraHeaders when the
	// caller supplied a distinct value, preserving the old normalizer's
	// explicit-header semantics while metadata.turn_id mirrors that value.
	extraHeaders["X-Codex-Installation-Id"] = identity.InstallationID
	return upstreamenvelope.Build(ctx, upstreamenvelope.Spec{
		BaseURL:           options.Endpoint,
		ProtocolProfileID: options.ProtocolProfileID,
		EndpointMode:      upstreamenvelope.EndpointMode(request.EndpointMode),
		Adapter:           upstreamenvelope.AdapterOpenAIOAuthCodex,
		// Model 取最终上游模型（模型映射后），供信封按 §9.5 判断
		// X-OpenAI-Internal-Codex-Responses-Lite。
		Model: request.ExpectedModel,
		Auth:  upstreamenvelope.Auth{Strategy: upstreamenvelope.AuthOAuth, Token: token},
		// Codex 归一化不携带预算字段（§4.3 Codex 例外）。
		Budget:        upstreamenvelope.BudgetNone,
		BodyFields:    fields,
		CodexIdentity: identity,
		ExtraHeaders:  extraHeaders,
	})
}

// buildAPIKeyRequest 经信封构造普通 API Key（C 形态）最终请求（信封统一
// 设计 §9.1-C）：URL/Accept/Content-Type/catalog 头、预算字段与 body 编码由
// Build 承担；本包保留 SSRF 校验、端点模式解析、语义 body/预算拆分与凭据
// 解析。认证头本身仍随调用方头逐字直通（Spec.ExtraHeaders），Auth 只承载
// 已解析策略；调用方未携带任何凭据头时显式选择 AuthNone（现行语义：无凭据
// 请求照发且不写认证头，信封统一设计 §4.1 AuthNone 契约），不是静默兜底。
func buildAPIKeyRequest(ctx context.Context, request Request, options Options, headers http.Header) (*http.Request, error) {
	if err := validateEndpointURL(options.Endpoint); err != nil {
		return nil, err
	}
	mode := resolveAPIKeyEndpointMode(request)
	entry, err := upstreamenvelope.Lookup(mode)
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(request.Body, &payload); err != nil || payload == nil {
		return nil, errors.New("J3b probe request body is invalid")
	}
	budget := extractBudgetPolicy(entry.BudgetField, payload)
	fields := make([]upstreamenvelope.BodyField, 0, len(payload))
	for key, value := range payload {
		fields = append(fields, upstreamenvelope.BodyField{Key: key, Value: value})
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].Key < fields[j].Key })
	return upstreamenvelope.Build(ctx, upstreamenvelope.Spec{
		BaseURL:           options.Endpoint,
		ProtocolProfileID: options.ProtocolProfileID,
		EndpointMode:      mode,
		// Model 供 gemini {model} 路径模板替换（构造器写入 Path 的同一
		// 模型即 ExpectedModel）；其余端点忽略本字段。
		Model: request.ExpectedModel,
		// 未知适配器串保持既有显式失败（信封登记表外适配器报错），不静默
		// 回落 AdapterNone。
		Adapter:      upstreamenvelope.Adapter(options.Adapter),
		Auth:         resolveAPIKeyAuth(headers, options.CredentialType),
		Budget:       budget,
		BodyFields:   fields,
		ExtraHeaders: callerExtraHeaders(headers),
	})
}

// resolveAPIKeyEndpointMode 解析 C 形态的 catalog 端点：优先业务
// request.EndpointMode（生产构造器恒填写且与协议匹配），契约外手工 Request
// 按 M2 既有规则从 protocol+path 推导，仍无则回落 chat_json（URL 归一化
// 只需协议族，openai 系共用同一分支；同协议 JSON/SSE 端点共享 catalog 路径
// 模板，唯一路径分叉 gemini 的 alt=sse 就在请求路径里）。
func resolveAPIKeyEndpointMode(request Request) upstreamenvelope.EndpointMode {
	if mode := strings.TrimSpace(request.EndpointMode); mode != "" && modelcheckprofile.EndpointModeMatchesProtocol(request.Protocol, mode) {
		return upstreamenvelope.EndpointMode(mode)
	}
	if mode := modelcheckprofile.EndpointModeForProtocol(request.Protocol, strings.Contains(request.Path, "alt=sse")); mode != "" {
		return upstreamenvelope.EndpointMode(mode)
	}
	return upstreamenvelope.ModeChatJSON
}

// extractBudgetPolicy 把 catalog 声明的预算字段从载荷提取为命名预算策略，
// 由 Build 统一注入（键值不变，落点顺序归信封）；载荷缺失或非法时保持
// BudgetNone——字段原样随语义 body 发出，不伪造兜底。Anthropic messages 的
// max_tokens 是协议必填，缺省由 Build 显式拒绝。
func extractBudgetPolicy(field upstreamenvelope.BudgetField, payload map[string]any) upstreamenvelope.BudgetPolicy {
	switch field {
	case upstreamenvelope.BudgetFieldMaxTokens:
		if limit, ok := budgetLimit(payload, "max_tokens"); ok {
			delete(payload, "max_tokens")
			return upstreamenvelope.BudgetBounded(limit)
		}
	case upstreamenvelope.BudgetFieldMaxOutputTokens:
		if limit, ok := budgetLimit(payload, "max_output_tokens"); ok {
			delete(payload, "max_output_tokens")
			return upstreamenvelope.BudgetBounded(limit)
		}
	case upstreamenvelope.BudgetFieldGeminiGenerationConfig:
		if generation, ok := payload["generationConfig"].(map[string]any); ok {
			if limit, ok := budgetLimit(generation, "maxOutputTokens"); ok {
				delete(generation, "maxOutputTokens")
				return upstreamenvelope.BudgetBounded(limit)
			}
		}
	}
	return upstreamenvelope.BudgetNone
}

// budgetLimit 读取正整数预算值（JSON 解码产物为 float64）；非正或非整数视
// 为未携带，字段保留原值直传。
func budgetLimit(source map[string]any, key string) (int, bool) {
	value, ok := source[key].(float64)
	if !ok || value <= 0 || value != math.Trunc(value) {
		return 0, false
	}
	return int(value), true
}

// resolveAPIKeyAuth 从调用方头解析 C 形态认证（凭据解析属调用方职责，信封
// 只接收已解析策略）：Authorization→Bearer、x-api-key→Anthropic、
// x-goog-api-key→Gemini，与 owner 侧 credentialHeaders 的写入形态一一对应；
// 均不存在时显式选择 AuthNone（见 buildAPIKeyRequest）。
func resolveAPIKeyAuth(headers http.Header, credentialType string) upstreamenvelope.Auth {
	if token := strings.TrimPrefix(headers.Get("Authorization"), "Bearer "); strings.TrimSpace(token) != "" {
		credentialType = strings.TrimSpace(credentialType)
		if strings.EqualFold(credentialType, "oauth") || strings.EqualFold(credentialType, "google_oauth") {
			return upstreamenvelope.Auth{Strategy: upstreamenvelope.AuthOAuth, Token: token}
		}
		return upstreamenvelope.Auth{Strategy: upstreamenvelope.AuthBearer, Token: token}
	}
	if token := headers.Get("X-Api-Key"); strings.TrimSpace(token) != "" {
		return upstreamenvelope.Auth{Strategy: upstreamenvelope.AuthAnthropicAPIKey, Token: token}
	}
	if token := headers.Get("X-Goog-Api-Key"); strings.TrimSpace(token) != "" {
		return upstreamenvelope.Auth{Strategy: upstreamenvelope.AuthGeminiAPIKey, Token: token}
	}
	return upstreamenvelope.Auth{Strategy: upstreamenvelope.AuthNone}
}

// bearerToken 提取 OAuth Codex 形态的调用方凭据；缺失显式失败，保留现行
// 错误文案。Authorization 头本身仍经 ExtraHeaders 逐字直通。
func bearerToken(headers http.Header) (string, error) {
	token := strings.TrimPrefix(headers.Get("Authorization"), "Bearer ")
	if strings.TrimSpace(token) == "" {
		return "", errors.New("J3b OpenAI OAuth Codex authorization is missing")
	}
	return token, nil
}

// newCodexIdentity resolves the final identity once, reusing caller-provided
// values as the legacy OAuth Codex normalizer did. Session-Id is the session
// source; an explicit X-Client-Request-Id remains the turn source and is kept
// as an extra header when supplied, so each metadata field mirrors its own
// final wire header without conflating the two identities.
func newCodexIdentity(headers http.Header) (upstreamenvelope.CodexIdentity, error) {
	sessionID := strings.TrimSpace(headers.Get("Session-Id"))
	var err error
	if sessionID == "" {
		sessionID, err = codexUUID()
		if err != nil {
			return upstreamenvelope.CodexIdentity{}, err
		}
	}
	threadID := strings.TrimSpace(headers.Get("Thread-Id"))
	if threadID == "" {
		threadID = sessionID
	}
	turnID := strings.TrimSpace(headers.Get("X-Client-Request-Id"))
	if turnID == "" {
		turnID = sessionID
	}
	windowID := strings.TrimSpace(headers.Get("X-Codex-Window-Id"))
	if windowID == "" {
		windowID = threadID + ":0"
	}
	installationID := strings.TrimSpace(headers.Get("X-Codex-Installation-Id"))
	if installationID == "" {
		installationID, err = codexUUID()
		if err != nil {
			return upstreamenvelope.CodexIdentity{}, err
		}
	}
	metadata, err := json.Marshal(map[string]any{
		"installation_id":         installationID,
		"session_id":              sessionID,
		"thread_id":               threadID,
		"turn_id":                 turnID,
		"window_id":               windowID,
		"request_kind":            "turn",
		"thread_source":           "user",
		"sandbox":                 "none",
		"turn_started_at_unix_ms": time.Now().UnixMilli(),
	})
	if err != nil {
		return upstreamenvelope.CodexIdentity{}, err
	}
	return upstreamenvelope.CodexIdentity{
		InstallationID:   installationID,
		SessionID:        sessionID,
		ThreadID:         threadID,
		TurnID:           turnID,
		WindowID:         windowID,
		TurnMetadataJSON: string(metadata),
	}, nil
}

func deleteHeaderFold(headers map[string]string, name string) {
	for key := range headers {
		if strings.EqualFold(key, name) {
			delete(headers, key)
		}
	}
}

// callerExtraHeaders 把调用方头逐字映射为 Spec.ExtraHeaders（信封语义：
// 最后应用，保留既有覆盖语义——调用方显式 Accept/catalog 头覆盖缺省填充，
// 与旧 Execute 的缺省填充语义一致）。
func callerExtraHeaders(headers http.Header) map[string]string {
	extra := make(map[string]string, len(headers))
	for key, values := range headers {
		if len(values) == 0 {
			continue
		}
		extra[key] = values[0]
	}
	return extra
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

// parseOAuthCodexResponseDetailed 要求 A-shape OAuth Codex 成功响应必须是 SSE。
// JSON 成功体、仅 delta 或尚未完成的坏帧都不能作为成功探针结果；明确失败
// 事件和非空 error 信封优先于后续 completed，并保留上游原始错误文本。
func parseOAuthCodexResponseDetailed(body []byte) (string, string, map[string]any, map[string]any, string, bool) {
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf}))
	var directValue map[string]any
	if json.Unmarshal(trimmed, &directValue) == nil && directValue != nil {
		if responseFailureEnvelope(directValue) {
			model, output, usage, parsed := parseJSONResponse(modelcheckprofile.ProtocolOpenAIResponses, trimmed)
			return model, output, usage, parsed, responseFailureEnvelopeMessage(directValue), true
		}
		return "", "", nil, nil, "J3b OAuth Codex response must be SSE", false
	}
	if !bytes.Contains(trimmed, []byte("data:")) && !bytes.Contains(trimmed, []byte("event:")) {
		return "", strings.TrimSpace(string(body)), nil, nil, "J3b OAuth Codex response must be SSE", false
	}
	events := parseOAuthCodexSSEEvents(trimmed)
	if len(events) == 0 {
		return "", strings.TrimSpace(string(body)), nil, nil, "J3b OAuth Codex response contains no valid SSE events", false
	}

	var (
		model, output, terminal string
		usage                   map[string]any
		value                   map[string]any
		deltas                  []string
		errorMessage            string
		rawErrorMessage         string
		completed, failed       bool
		malformed               bool
	)
	for _, event := range events {
		if event.malformed {
			malformed = true
			if errorMessage == "" {
				errorMessage = "J3b OAuth Codex response contains malformed SSE data"
			}
		}
		payload := event.payload
		eventName := strings.TrimSpace(event.event)
		eventFailure := isFailureEvent(eventName)
		if eventFailure {
			failed = true
			if errorMessage == "" {
				errorMessage = eventName
			}
		}
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
		eventModel, eventOutput, eventUsage, parsed := parseJSONResponse(modelcheckprofile.ProtocolOpenAIResponses, encoded)
		if eventOutput == "" {
			eventOutput = parseSSEEventOutput(modelcheckprofile.ProtocolOpenAIResponses, payload)
		}
		if model == "" && eventModel != "" {
			model = eventModel
		}
		if usage == nil && eventUsage != nil {
			usage = eventUsage
		}
		if parsed != nil && responseValueHasEvidence(modelcheckprofile.ProtocolOpenAIResponses, parsed) {
			value = parsed
		}

		typeName := strings.TrimSpace(asString(payload["type"]))
		payloadFailure := responseFailureEnvelope(payload)
		if payloadFailure {
			failed = true
			if errorMessage == "" {
				errorMessage = responseFailureEnvelopeMessage(payload)
			}
		}
		if (eventFailure || payloadFailure) && rawErrorMessage == "" {
			rawErrorMessage = responseRawErrorMessage(payload)
		}
		if eventName == "response.completed" || typeName == "response.completed" {
			completed = true
			if nested, ok := payload["response"].(map[string]any); ok {
				value = nested
			}
			status := strings.TrimSpace(asString(candidate["status"]))
			if status == "" {
				status = strings.TrimSpace(asString(payload["status"]))
			}
			if status != "" && status != "completed" {
				failed = true
				if errorMessage == "" {
					if nestedError := responseErrorMessage(typeName, payload); nestedError != "" {
						errorMessage = nestedError
					} else {
						errorMessage = "response.completed status " + status
					}
				}
			}
			if eventOutput != "" {
				terminal = eventOutput
			}
		} else if eventOutput != "" {
			deltas = append(deltas, eventOutput)
		}
	}
	if terminal != "" {
		output = terminal
	} else {
		output = strings.TrimSpace(strings.Join(deltas, ""))
	}
	if failed {
		if rawErrorMessage != "" {
			return model, output, usage, value, rawErrorMessage, true
		}
		if errorMessage != "" {
			return model, output, usage, value, errorMessage, true
		}
		return model, output, usage, value, "J3b OAuth Codex response failed", true
	}
	if malformed {
		return model, output, usage, value, "J3b OAuth Codex response contains malformed SSE data", completed
	}
	if !completed {
		return model, output, usage, value, "J3b OAuth Codex response.completed event missing", false
	}
	return model, output, usage, value, "", true
}

type oauthCodexSSEEvent struct {
	sseResponseEvent
	malformed bool
}

func parseOAuthCodexSSEEvents(body []byte) []oauthCodexSSEEvent {
	var events []oauthCodexSSEEvent
	var eventName string
	var dataLines []string
	flush := func() {
		if len(dataLines) == 0 {
			if strings.TrimSpace(eventName) != "" {
				events = append(events, oauthCodexSSEEvent{sseResponseEvent: sseResponseEvent{event: eventName}})
			}
			eventName = ""
			return
		}
		data := strings.TrimSpace(strings.Join(dataLines, "\n"))
		event := oauthCodexSSEEvent{sseResponseEvent: sseResponseEvent{event: eventName}}
		if data != "" && data != "[DONE]" {
			var payload map[string]any
			if json.Unmarshal([]byte(data), &payload) == nil && payload != nil {
				event.payload = payload
			} else {
				event.malformed = true
			}
		}
		events = append(events, event)
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

func parseOAuthCodexHTTPFailureMessage(body []byte) string {
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf}))
	var payload map[string]any
	if json.Unmarshal(trimmed, &payload) != nil || payload == nil {
		return ""
	}
	if responseFailureEnvelope(payload) {
		return responseFailureEnvelopeMessage(payload)
	}
	return asString(payload["message"])
}

func responseFailureEnvelope(payload map[string]any) bool {
	if payload == nil {
		return false
	}
	if errorValue, exists := payload["error"]; exists && errorValue != nil {
		return true
	}
	if isFailureEvent(strings.TrimSpace(asString(payload["type"]))) || responseFailureStatus(payload["status"]) {
		return true
	}
	if response, ok := payload["response"].(map[string]any); ok {
		if errorValue, exists := response["error"]; exists && errorValue != nil {
			return true
		}
		if responseFailureStatus(response["status"]) {
			return true
		}
	}
	return false
}

func responseFailureStatus(value any) bool {
	status := strings.TrimSpace(asString(value))
	return status == "failed" || status == "incomplete"
}

func responseFailureEnvelopeMessage(payload map[string]any) string {
	if message := responseRawErrorMessage(payload); message != "" {
		return message
	}
	typeName := strings.TrimSpace(asString(payload["type"]))
	if message := responseErrorMessage(typeName, payload); message != "" {
		return message
	}
	if response, ok := payload["response"].(map[string]any); ok {
		if status := strings.TrimSpace(asString(response["status"])); status != "" {
			return "response.completed status " + status
		}
	}
	if status := strings.TrimSpace(asString(payload["status"])); status != "" {
		return "response.status " + status
	}
	return "J3b OAuth Codex response failed"
}

func responseRawErrorMessage(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	if nested, ok := payload["response"].(map[string]any); ok {
		if message := responseRawErrorMessage(nested); message != "" {
			return message
		}
	}
	if errorValue, ok := payload["error"]; ok {
		if message := errorValueMessage(errorValue); message != "" {
			return message
		}
	}
	return asString(payload["message"])
}

func responseBodyIsSemanticallyCompleteForAdapter(adapter string, body []byte, parseErrorMessage string) bool {
	if adapter == AdapterOpenAIOAuthCodex {
		_, _, _, _, _, terminal := parseOAuthCodexResponseDetailed(body)
		return terminal && parseErrorMessage == ""
	}
	return responseBodyIsSemanticallyComplete(body, parseErrorMessage)
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
