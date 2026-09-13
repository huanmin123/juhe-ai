package modelcheckprobe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	keymodelruntime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/key_model_runtime"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// ---- 传输层桩：在无网络环境下驱动 Execute 的各条分支 ----

// wlStubTransport 返回固定响应或固定错误，记录被转发的请求头与请求体。
type wlStubTransport struct {
	status   int
	body     string
	err      error
	block    bool
	readErr  bool
	seenReq  *http.Request
	seenHdr  http.Header
	seenBody []byte
}

func (t *wlStubTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		t.seenBody, _ = io.ReadAll(request.Body)
		_ = request.Body.Close()
	}
	if t.block {
		// 阻塞到请求上下文结束，确定性触发超时/取消分支，不引入 sleep。
		<-request.Context().Done()
		return nil, request.Context().Err()
	}
	if t.err != nil {
		return nil, t.err
	}
	t.seenReq = request
	t.seenHdr = request.Header.Clone()
	var reader io.Reader = strings.NewReader(t.body)
	if t.readErr {
		reader = wlErrReader{}
	}
	return &http.Response{StatusCode: t.status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(reader), Request: request}, nil
}

// wlErrReader 在读取时返回固定错误，驱动"响应读取失败"分支。
type wlErrReader struct{}

func (wlErrReader) Read([]byte) (int, error) { return 0, errors.New("boom-read") }

// wlDispatcherPlain 只实现 DispatcherPort，覆盖旧接口路径。
type wlDispatcherPlain struct {
	response    *http.Response
	err         error
	lastAttempt string
	lastCap     keymodelruntime.Capability
	settleCalls []bool
}

func (d *wlDispatcherPlain) Dispatch(_ context.Context, _ *http.Request, capability keymodelruntime.Capability, attemptID string) (*http.Response, func(bool), error) {
	d.lastAttempt = attemptID
	d.lastCap = capability
	return d.response, func(success bool) { d.settleCalls = append(d.settleCalls, success) }, d.err
}

// wlDispatcherWithClient 额外实现 ClientDispatcherPort，覆盖扩展接口路径。
type wlDispatcherWithClient struct {
	wlDispatcherPlain
	withClientCalled bool
	receivedClient   *http.Client
}

func (d *wlDispatcherWithClient) DispatchWithClient(_ context.Context, _ *http.Request, capability keymodelruntime.Capability, attemptID string, client *http.Client) (*http.Response, func(bool), error) {
	d.withClientCalled = true
	d.receivedClient = client
	d.lastAttempt = attemptID
	d.lastCap = capability
	return d.response, func(success bool) { d.settleCalls = append(d.settleCalls, success) }, nil
}

func TestWlBuildOpenAIOAuthCodexRequestShapes(t *testing.T) {
	// 业务契约：OAuth Codex 走独立的 /responses 形态，不能继承 API-Key 请求字段。
	basic, err := BuildOpenAIOAuthCodexBasic("gpt-5.6-sol", "hello", false)
	if err != nil {
		t.Fatalf("构造 Codex basic 失败: %v", err)
	}
	if basic.Path != "/responses" || basic.Protocol != modelcheckprofile.ProtocolOpenAIResponses || basic.EndpointMode != modelcheckprofile.EndpointModeResponsesJSON {
		t.Fatalf("Codex basic 请求头不符: %#v", basic)
	}
	var basicPayload map[string]any
	if err := json.Unmarshal(basic.Body, &basicPayload); err != nil {
		t.Fatalf("Codex basic body 非法: %v", err)
	}
	if basicPayload["store"] != false || basicPayload["instructions"] != "" {
		t.Fatalf("Codex basic 必须固定 store=false 且 instructions 为空: %#v", basicPayload)
	}

	structured, err := BuildOpenAIOAuthCodexStructured("gpt-5.6-sol", false)
	if err != nil {
		t.Fatalf("构造 Codex structured 失败: %v", err)
	}
	var structuredPayload map[string]any
	if err := json.Unmarshal(structured.Body, &structuredPayload); err != nil {
		t.Fatalf("Codex structured body 非法: %v", err)
	}
	text, ok := structuredPayload["text"].(map[string]any)
	if !ok || text["format"] == nil {
		t.Fatalf("Codex structured 缺少 json_schema text.format: %#v", structuredPayload)
	}

	tool, err := BuildOpenAIOAuthCodexTool("gpt-5.6-sol", true)
	if err != nil {
		t.Fatalf("构造 Codex tool 失败: %v", err)
	}
	var toolPayload map[string]any
	if err := json.Unmarshal(tool.Body, &toolPayload); err != nil {
		t.Fatalf("Codex tool body 非法: %v", err)
	}
	if toolPayload["tool_choice"] == nil {
		t.Fatalf("Codex tool 缺少 tool_choice: %#v", toolPayload)
	}
	if tool.EndpointMode != modelcheckprofile.EndpointModeResponsesSSE {
		t.Fatalf("Codex tool 流式模式应为 responses_sse: %#v", tool)
	}

	if _, err := BuildOpenAIOAuthCodexBasic("  ", "hello", false); err == nil {
		t.Fatal("model 为空时必须拒绝构造 Codex 请求")
	}
	if _, err := BuildOpenAIOAuthCodexStructured(" ", false); err == nil {
		// BuildOpenAIOAuthCodexStructured 内部使用固定 prompt，空 model 走 basic 校验。
		t.Fatal("model 为空时 structured 构造必须失败")
	}
}

func TestWlExecuteCodexAdapterNormalizesRequest(t *testing.T) {
	// 业务契约：Codex 归一化必须剥离 tuning 字段并补齐桌面客户端身份头。
	transport := &wlStubTransport{status: http.StatusOK, body: `{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK"}`}
	result, err := Execute(context.Background(), Request{Path: "/v1/responses", ExpectedModel: "gpt-5.6-sol", Protocol: modelcheckprofile.ProtocolOpenAIResponses, EndpointMode: modelcheckprofile.EndpointModeResponsesJSON, Body: json.RawMessage(`{"model":"gpt-5.6-sol","input":"hello","instructions":"keep","max_output_tokens":64,"temperature":0.5}`)}, Options{Endpoint: "https://upstream.example", Headers: http.Header{"Authorization": []string{"Bearer codex"}}, Client: &http.Client{Transport: transport}, Adapter: AdapterOpenAIOAuthCodex})
	if err != nil || !result.Success {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if transport.seenReq == nil || transport.seenReq.URL.Host != "chatgpt.com" || !strings.HasSuffix(transport.seenReq.URL.Path, "/backend-api/codex/responses") {
		t.Fatalf("Codex 请求路径必须固定官方基址 /responses: %v", transport.seenReq.URL)
	}
	var payload map[string]any
	if err := json.Unmarshal(transport.seenBody, &payload); err != nil {
		t.Fatalf("转发 body 非法: %v", err)
	}
	for _, banned := range []string{"max_output_tokens", "temperature"} {
		if _, ok := payload[banned]; ok {
			t.Fatalf("Codex 归一化必须删除字段 %s: %#v", banned, payload)
		}
	}
	if payload["store"] != false || payload["stream"] != true {
		t.Fatalf("Codex 归一化必须固定 store=false stream=true: %#v", payload)
	}
	reasoning, _ := payload["reasoning"].(map[string]any)
	if reasoning["context"] != "all_turns" || payload["parallel_tool_calls"] != false {
		t.Fatalf("gpt-5.6 模型必须带 reasoning.context=all_turns: %#v", payload)
	}
	if transport.seenHdr.Get("originator") != "Codex Desktop" || transport.seenHdr.Get("session-id") == "" || transport.seenHdr.Get("x-codex-turn-metadata") == "" {
		t.Fatalf("Codex 身份头缺失: %v", transport.seenHdr)
	}
	if transport.seenHdr.Get("thread-id") != transport.seenHdr.Get("session-id") || transport.seenHdr.Get("x-codex-window-id") != transport.seenHdr.Get("session-id")+":0" {
		t.Fatalf("Codex 会话派生头不一致: %v", transport.seenHdr)
	}
}

func TestWlExecuteCodexAdapterRejectsInvalidInputs(t *testing.T) {
	tests := []struct {
		name    string
		request Request
		wantErr string
	}{
		{"端点模式不受支持", Request{Protocol: modelcheckprofile.ProtocolOpenAIResponses, EndpointMode: modelcheckprofile.EndpointModeChatJSON, Body: json.RawMessage(`{}`)}, "endpoint mode is unsupported"},
		{"body 非法", Request{Protocol: modelcheckprofile.ProtocolOpenAIResponses, EndpointMode: modelcheckprofile.EndpointModeResponsesJSON, Body: json.RawMessage(`not-json`)}, "body is invalid"},
		{"model 缺失", Request{Protocol: modelcheckprofile.ProtocolOpenAIResponses, EndpointMode: modelcheckprofile.EndpointModeResponsesJSON, Body: json.RawMessage(`{"input":"hi"}`)}, "model is invalid"},
		{"input 非法", Request{Protocol: modelcheckprofile.ProtocolOpenAIResponses, EndpointMode: modelcheckprofile.EndpointModeResponsesJSON, Body: json.RawMessage(`{"model":"gpt-5.6-sol","input":123}`)}, "input is invalid"},
		{"instructions 非法", Request{Protocol: modelcheckprofile.ProtocolOpenAIResponses, EndpointMode: modelcheckprofile.EndpointModeResponsesJSON, Body: json.RawMessage(`{"model":"gpt-5.6-sol","input":"hi","instructions":123}`)}, "instructions are invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := Execute(context.Background(), test.request, Options{Endpoint: "https://upstream.example", Headers: http.Header{"Authorization": []string{"Bearer codex"}}, Client: &http.Client{Transport: &wlStubTransport{status: 200}}, Adapter: AdapterOpenAIOAuthCodex})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("err=%v, 期望包含 %q", err, test.wantErr)
			}
			if result.ErrorMessage != err.Error() {
				t.Fatalf("失败结果必须保留原始错误: result=%+v", result)
			}
		})
	}
	t.Run("缺少授权头", func(t *testing.T) {
		_, err := Execute(context.Background(), Request{Protocol: modelcheckprofile.ProtocolOpenAIResponses, EndpointMode: modelcheckprofile.EndpointModeResponsesJSON, Body: json.RawMessage(`{"model":"m","input":"hi"}`)}, Options{Endpoint: "https://upstream.example", Client: &http.Client{Transport: &wlStubTransport{status: 200}}, Adapter: AdapterOpenAIOAuthCodex})
		if err == nil || !strings.Contains(err.Error(), "authorization is missing") {
			t.Fatalf("err=%v, 期望缺少授权被拒绝", err)
		}
	})
}

func TestWlExecuteTransportFailureBoundaries(t *testing.T) {
	t.Run("超时", func(t *testing.T) {
		result, err := Execute(context.Background(), mustWlBasicRequest(t), Options{Endpoint: "https://upstream.example", Client: &http.Client{Transport: &wlStubTransport{block: true}}, Timeout: time.Millisecond})
		if err != nil {
			t.Fatalf("超时必须返回结果而非错误: %v", err)
		}
		if result.ErrorMessage != "J3b probe timed out" {
			t.Fatalf("超时错误消息=%q", result.ErrorMessage)
		}
	})
	t.Run("上下文取消", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		result, err := Execute(ctx, mustWlBasicRequest(t), Options{Endpoint: "https://upstream.example", Client: &http.Client{Transport: &wlStubTransport{block: true}}, Timeout: time.Second})
		if err != nil || result.ErrorMessage != "J3b probe canceled" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("传输错误", func(t *testing.T) {
		result, err := Execute(context.Background(), mustWlBasicRequest(t), Options{Endpoint: "https://upstream.example", Client: &http.Client{Transport: &wlStubTransport{err: errors.New("dial failed")}}, Timeout: time.Second})
		if err != nil || result.ErrorMessage != "J3b upstream request failed" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("端点非法", func(t *testing.T) {
		_, err := Execute(context.Background(), mustWlBasicRequest(t), Options{Endpoint: "ftp://upstream.example", Client: &http.Client{Transport: &wlStubTransport{status: 200}}, Timeout: time.Second})
		if err == nil || !strings.Contains(err.Error(), "endpoint URL is invalid") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("响应读取失败", func(t *testing.T) {
		result, err := Execute(context.Background(), mustWlBasicRequest(t), Options{Endpoint: "https://upstream.example", Client: &http.Client{Transport: &wlStubTransport{status: 200, readErr: true}}, Timeout: time.Second})
		if err != nil || result.ErrorMessage != "J3b upstream response read failed" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if result.HTTPStatus != http.StatusOK {
			t.Fatalf("读取失败也必须保留上游状态码: %d", result.HTTPStatus)
		}
	})
	t.Run("响应超限", func(t *testing.T) {
		result, err := Execute(context.Background(), mustWlBasicRequest(t), Options{Endpoint: "https://upstream.example", Client: &http.Client{Transport: &wlStubTransport{status: 200, body: strings.Repeat("x", 128)}}, MaxResponseBytes: 16, Timeout: time.Second})
		if err != nil || result.ErrorMessage != "J3b upstream response exceeded limit" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("非 200 保留状态码", func(t *testing.T) {
		result, err := Execute(context.Background(), mustWlBasicRequest(t), Options{Endpoint: "https://upstream.example", Client: &http.Client{Transport: &wlStubTransport{status: 429, body: `{"error":"rate limited"}`}}, Timeout: time.Second})
		if err != nil || result.Success || result.HTTPStatus != 429 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if !strings.Contains(result.ErrorMessage, "rate limited") {
			// 上游错误信封优先于通用状态码消息被保留。
			t.Fatalf("非 200 必须保留错误信封消息: %q", result.ErrorMessage)
		}
	})
}

// mustWlBasicRequest 构造一个最小的合法 basic 探针请求。
func mustWlBasicRequest(t *testing.T) Request {
	t.Helper()
	request, err := BuildBasic(modelcheckprofile.ProtocolOpenAIChat, "gpt-test", "hello", false)
	if err != nil {
		t.Fatalf("构造 basic 请求失败: %v", err)
	}
	return request
}

func TestWlExecuteDispatcherPaths(t *testing.T) {
	okResponse := func() *http.Response {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"model":"gpt-test","choices":[{"message":{"content":"OK-MODEL-CHECK"}}]}`))}
	}
	t.Run("普通 Dispatcher 填充默认能力并结算成功", func(t *testing.T) {
		dispatcher := &wlDispatcherPlain{response: okResponse()}
		result, err := Execute(context.Background(), mustWlBasicRequest(t), Options{Endpoint: "https://upstream.example", Dispatcher: dispatcher, Capability: keymodelruntime.Capability{ClientModel: "client-model", FinalUpstreamModel: "upstream-model", ClientEndpointFamily: "family", UpstreamEndpointMode: "mode"}, Timeout: time.Second})
		if err != nil || !result.Success {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if dispatcher.lastAttempt == "" || !strings.HasPrefix(dispatcher.lastAttempt, "model-check-") {
			t.Fatalf("attemptID=%q", dispatcher.lastAttempt)
		}
		if dispatcher.lastCap.ClientModel != "client-model" || dispatcher.lastCap.UpstreamEndpointMode != "mode" {
			t.Fatalf("已有能力必须原样传递: %+v", dispatcher.lastCap)
		}
		if len(dispatcher.settleCalls) != 2 || dispatcher.settleCalls[0] != true || dispatcher.settleCalls[1] != false {
			// 行为存疑：成功路径 settle 以 true 结算后又经 defer 以 false 再调用一次，
			// 依赖 dispatcher 侧幂等；此处按当前实际行为断言。
			t.Fatalf("成功结算序列=%v", dispatcher.settleCalls)
		}
	})
	t.Run("普通 Dispatcher 缺省能力回填探针模型", func(t *testing.T) {
		dispatcher := &wlDispatcherPlain{response: okResponse()}
		request := mustWlBasicRequest(t)
		request.EndpointMode = ""
		if _, err := Execute(context.Background(), request, Options{Endpoint: "https://upstream.example", Dispatcher: dispatcher, Timeout: time.Second}); err != nil {
			t.Fatalf("err=%v", err)
		}
		if dispatcher.lastCap.ClientModel != "gpt-test" || dispatcher.lastCap.FinalUpstreamModel != "gpt-test" || dispatcher.lastCap.ClientEndpointFamily != string(modelcheckprofile.ProtocolOpenAIChat) || dispatcher.lastCap.UpstreamEndpointMode != string(modelcheckprofile.ProtocolOpenAIChat) {
			t.Fatalf("缺省能力必须回填: %+v", dispatcher.lastCap)
		}
	})
	t.Run("DispatchWithClient 扩展接口", func(t *testing.T) {
		dispatcher := &wlDispatcherWithClient{}
		dispatcher.response = okResponse()
		if _, err := Execute(context.Background(), mustWlBasicRequest(t), Options{Endpoint: "https://upstream.example", Dispatcher: dispatcher, Timeout: time.Second}); err != nil {
			t.Fatalf("err=%v", err)
		}
		if !dispatcher.withClientCalled || dispatcher.receivedClient != nil {
			// nil client 表示 dispatcher 应使用自身默认客户端。
			t.Fatalf("必须走扩展接口且透传 nil client: called=%v client=%v", dispatcher.withClientCalled, dispatcher.receivedClient)
		}
	})
	t.Run("Dispatcher 失败结算 false", func(t *testing.T) {
		dispatcher := &wlDispatcherPlain{err: errors.New("dispatch boom")}
		result, err := Execute(context.Background(), mustWlBasicRequest(t), Options{Endpoint: "https://upstream.example", Dispatcher: dispatcher, Timeout: time.Second})
		if err != nil || result.Success {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if len(dispatcher.settleCalls) != 1 || dispatcher.settleCalls[0] != false {
			t.Fatalf("失败必须结算一次 false: %v", dispatcher.settleCalls)
		}
	})
}

func TestWlBuildURLVariants(t *testing.T) {
	tests := []struct {
		name, endpoint, path, adapter string
		protocol                      modelcheckprofile.Protocol
		want                          string
		wantErr                       bool
	}{
		{"chat 自动补 /v1", "https://api.example.com", "/chat/completions", "", modelcheckprofile.ProtocolOpenAIChat, "https://api.example.com/v1/chat/completions", false},
		{"chat 保留既有 /v1", "https://api.example.com/v1/", "/chat/completions", "", modelcheckprofile.ProtocolOpenAIChat, "https://api.example.com/v1/chat/completions", false},
		{"gemini 自动补 /v1beta", "https://api.example.com", "/v1beta/models/m:generateContent", "", modelcheckprofile.ProtocolGeminiNative, "https://api.example.com/v1beta/models/m:generateContent", false},
		{"保留查询串", "https://api.example.com", "/v1/chat/completions?alt=sse", "", modelcheckprofile.ProtocolOpenAIChat, "https://api.example.com/v1/chat/completions?alt=sse", false},
		{"codex 固定官方基址", "https://anything.example", "/responses", AdapterOpenAIOAuthCodex, modelcheckprofile.ProtocolOpenAIResponses, OpenAIOAuthCodexBaseURL + "/responses", false},
		{"codex 拒绝非 responses 路径", "https://api.example.com", "/chat/completions", AdapterOpenAIOAuthCodex, modelcheckprofile.ProtocolOpenAIChat, "", true},
		{"codex 拒绝带查询串端点", "https://api.example.com?x=1", "/responses", AdapterOpenAIOAuthCodex, modelcheckprofile.ProtocolOpenAIResponses, "", true},
		{"拒绝 ftp", "ftp://api.example.com", "/v1/chat", "", modelcheckprofile.ProtocolOpenAIChat, "", true},
		{"拒绝空 host", "https://", "/v1/chat", "", modelcheckprofile.ProtocolOpenAIChat, "", true},
		{"拒绝用户信息", "https://user:pass@api.example.com", "/v1/chat", "", modelcheckprofile.ProtocolOpenAIChat, "", true},
		{"拒绝端点查询串", "https://api.example.com/?x=1", "/v1/chat", "", modelcheckprofile.ProtocolOpenAIChat, "", true},
		{"拒绝端点片段", "https://api.example.com/#frag", "/v1/chat", "", modelcheckprofile.ProtocolOpenAIChat, "", true},
		{"拒绝控制字符", "https://api.example.com/\r\n", "/v1/chat", "", modelcheckprofile.ProtocolOpenAIChat, "", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := buildURL(test.endpoint, test.protocol, test.path, test.adapter)
			if test.wantErr {
				if err == nil {
					t.Fatalf("buildURL(%q) 必须失败, got=%q", test.endpoint, got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("buildURL=%q err=%v want=%q", got, err, test.want)
			}
		})
	}
}

func TestWlParseResponseDetailedVariants(t *testing.T) {
	t.Run("非 JSON 非 SSE 保留原文", func(t *testing.T) {
		model, output, usage, value, errMessage := parseResponseDetailed(modelcheckprofile.ProtocolOpenAIChat, []byte("  upstream said no  "))
		if model != "" || output != "upstream said no" || usage != nil || value != nil || errMessage != "" {
			t.Fatalf("model=%q output=%q usage=%v value=%v err=%q", model, output, usage, value, errMessage)
		}
	})
	t.Run("BOM 前缀 JSON", func(t *testing.T) {
		body := append([]byte{0xef, 0xbb, 0xbf}, []byte(`{"model":"m","output_text":"hi"}`)...)
		model, _, _, value, errMessage := parseResponseDetailed(modelcheckprofile.ProtocolOpenAIResponses, body)
		if model != "m" || value == nil || errMessage != "" {
			t.Fatalf("BOM 响应解析错误: model=%q err=%q", model, errMessage)
		}
	})
	t.Run("JSON 顶层错误信封", func(t *testing.T) {
		_, _, _, _, errMessage := parseResponseDetailed(modelcheckprofile.ProtocolOpenAIChat, []byte(`{"error":{"message":"quota exceeded","code":"insufficient_quota"}}`))
		if errMessage != "quota exceeded" {
			t.Fatalf("错误信封消息=%q", errMessage)
		}
	})
	t.Run("JSON 顶层 message 兜底", func(t *testing.T) {
		_, _, _, _, errMessage := parseResponseDetailed(modelcheckprofile.ProtocolOpenAIChat, []byte(`{"message":"backend down"}`))
		if errMessage != "backend down" {
			t.Fatalf("message 兜底=%q", errMessage)
		}
	})
	t.Run("SSE 汇聚 delta 且终止事件取终值", func(t *testing.T) {
		body := strings.Join([]string{
			": comment line",
			"event: response.output_text.delta",
			`data: {"type":"response.output_text.delta","delta":"OK-"}`,
			"",
			`data: {"type":"response.output_text.delta","delta":"MODEL-CHECK","text":""}`,
			"",
			"event: response.completed",
			`data: {"type":"response.completed","response":{"model":"gpt-test","output_text":"IGNORED","status":"completed"}}`,
			"",
			"data: [DONE]",
			"",
		}, "\n")
		model, output, _, value, errMessage := parseResponseDetailed(modelcheckprofile.ProtocolOpenAIResponses, []byte(body))
		if model != "gpt-test" || output != "IGNORED" || errMessage != "" || value == nil {
			t.Fatalf("model=%q output=%q err=%q", model, output, errMessage)
		}
	})
	t.Run("SSE 无终止事件拼接 delta", func(t *testing.T) {
		body := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"STREAM\"}\r\n\r\ndata: {\"type\":\"response.output_text.delta\",\"text\":\"-OK\"}\r\n\r\n"
		_, output, _, _, _ := parseResponseDetailed(modelcheckprofile.ProtocolOpenAIResponses, []byte(body))
		if output != "STREAM-OK" {
			t.Fatalf("CRLF SSE delta 拼接=%q", output)
		}
	})
	t.Run("SSE 失败事件返回事件名", func(t *testing.T) {
		body := `data: {"type":"response.failed"}`
		_, _, _, _, errMessage := parseResponseDetailed(modelcheckprofile.ProtocolOpenAIResponses, []byte(body))
		if errMessage != "response.failed" {
			t.Fatalf("失败事件消息=%q", errMessage)
		}
	})
	t.Run("SSE 嵌套 response 错误信封", func(t *testing.T) {
		body := `data: {"response":{"error":{"code":"model_not_found"}}}`
		_, _, _, _, errMessage := parseResponseDetailed(modelcheckprofile.ProtocolOpenAIResponses, []byte(body))
		if errMessage != "model_not_found" {
			t.Fatalf("嵌套错误=%q", errMessage)
		}
	})
	t.Run("SSE 非法 JSON 事件回退原文", func(t *testing.T) {
		body := "data: not-json\n\n"
		_, output, usage, value, errMessage := parseResponseDetailed(modelcheckprofile.ProtocolOpenAIResponses, []byte(body))
		// 无可解析事件且非 JSON 时整体作为原始文本保留，供上层诊断。
		if output != "data: not-json" || usage != nil || value != nil || errMessage != "" {
			t.Fatalf("非法事件必须回退原文: output=%q err=%q", output, errMessage)
		}
	})
}

func TestWlParseJSONResponseProtocolShapes(t *testing.T) {
	t.Run("chat 缺 message 回落 output_text", func(t *testing.T) {
		_, output, _, _ := parseJSONResponse(modelcheckprofile.ProtocolOpenAIChat, []byte(`{"model":"m","choices":[{}],"output_text":"fallback"}`))
		if output != "fallback" {
			t.Fatalf("output=%q", output)
		}
	})
	t.Run("chat 完整 choices", func(t *testing.T) {
		_, output, usage, _ := parseJSONResponse(modelcheckprofile.ProtocolOpenAIChat, []byte(`{"model":"m","choices":[{"message":{"content":"hi"}}],"usage":{"total_tokens":3}}`))
		if output != "hi" || usage["total_tokens"] != float64(3) {
			t.Fatalf("output=%q usage=%v", output, usage)
		}
	})
	t.Run("anthropic content 文本", func(t *testing.T) {
		_, output, _, _ := parseJSONResponse(modelcheckprofile.ProtocolAnthropic, []byte(`{"model":"m","content":[{"type":"text","text":"anthropic-out"}]}`))
		if output != "anthropic-out" {
			t.Fatalf("output=%q", output)
		}
	})
	t.Run("anthropic 无文本回落", func(t *testing.T) {
		_, output, _, _ := parseJSONResponse(modelcheckprofile.ProtocolAnthropic, []byte(`{"model":"m","content":[{"type":"tool_use"}],"text":"plain"}`))
		if output != "plain" {
			t.Fatalf("output=%q", output)
		}
	})
	t.Run("gemini candidates 文本", func(t *testing.T) {
		_, output, _, _ := parseJSONResponse(modelcheckprofile.ProtocolGeminiNative, []byte(`{"model":"m","candidates":[{"content":{"parts":[{"text":"gemini-out"}]}}]}`))
		if output != "gemini-out" {
			t.Fatalf("output=%q", output)
		}
	})
	t.Run("responses output 数组文本", func(t *testing.T) {
		_, output, _, _ := parseJSONResponse(modelcheckprofile.ProtocolOpenAIResponses, []byte(`{"model":"m","output":[{"content":[{"text":"nested-out"}]}]}`))
		if output != "nested-out" {
			t.Fatalf("output=%q", output)
		}
	})
	t.Run("model 缺失取 data[0].model", func(t *testing.T) {
		model, _, _, _ := parseJSONResponse(modelcheckprofile.ProtocolOpenAIResponses, []byte(`{"data":[{"model":"from-data"}]}`))
		if model != "from-data" {
			t.Fatalf("model=%q", model)
		}
	})
	t.Run("非法 JSON 返回原文", func(t *testing.T) {
		model, output, usage, value := parseJSONResponse(modelcheckprofile.ProtocolOpenAIResponses, []byte("{broken"))
		if model != "" || output != "{broken" || usage != nil || value != nil {
			t.Fatalf("model=%q output=%q", model, output)
		}
	})
}

func TestWlParseSSEEventOutputProtocols(t *testing.T) {
	t.Run("responses delta 与 text", func(t *testing.T) {
		got := parseSSEEventOutput(modelcheckprofile.ProtocolOpenAIResponses, map[string]any{"delta": "a", "text": "b"})
		if got != "ab" {
			t.Fatalf("got=%q", got)
		}
	})
	t.Run("chat message 与 delta 字段", func(t *testing.T) {
		payload := map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"content": "m", "reasoning_content": "r", "refusal": "f"},
			"delta":   map[string]any{"content": "d"},
		}}}
		if got := strings.TrimSpace(parseSSEEventOutput(modelcheckprofile.ProtocolOpenAIChat, payload)); got != "mrfd" {
			t.Fatalf("got=%q", got)
		}
	})
	t.Run("anthropic delta text", func(t *testing.T) {
		if got := parseSSEEventOutput(modelcheckprofile.ProtocolAnthropic, map[string]any{"delta": map[string]any{"text": "t"}}); got != "t" {
			t.Fatalf("got=%q", got)
		}
	})
	t.Run("gemini 无输出", func(t *testing.T) {
		if got := parseSSEEventOutput(modelcheckprofile.ProtocolGeminiNative, nil); got != "" {
			t.Fatalf("got=%q", got)
		}
	})
}

func TestWlResponseValueHasEvidenceProtocols(t *testing.T) {
	tests := []struct {
		name     string
		protocol modelcheckprofile.Protocol
		value    map[string]any
		want     bool
	}{
		{"model 字段即证据", modelcheckprofile.ProtocolOpenAIResponses, map[string]any{"model": "m"}, true},
		{"空值无证据", modelcheckprofile.ProtocolOpenAIResponses, map[string]any{}, false},
		{"chat choices", modelcheckprofile.ProtocolOpenAIChat, map[string]any{"choices": []any{map[string]any{}}}, true},
		{"anthropic content", modelcheckprofile.ProtocolAnthropic, map[string]any{"content": []any{map[string]any{}}}, true},
		{"gemini candidates", modelcheckprofile.ProtocolGeminiNative, map[string]any{"candidates": []any{map[string]any{}}}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := responseValueHasEvidence(test.protocol, test.value); got != test.want {
				t.Fatalf("got=%v want=%v", got, test.want)
			}
		})
	}
	if responseValueHasEvidence(modelcheckprofile.ProtocolOpenAIResponses, nil) {
		t.Fatal("nil 值必须无证据")
	}
}

func TestWlHasFunctionCallPayloadShapes(t *testing.T) {
	okArgs := `{"code":"ok","count":1}`
	tests := []struct {
		name    string
		payload map[string]any
		want    bool
	}{
		{"output function_call 字符串参数", map[string]any{"output": []any{map[string]any{"type": "function_call", "name": "record_model_check", "arguments": okArgs}}}, true},
		{"output 参数对象", map[string]any{"output": []any{map[string]any{"type": "function_call", "name": "record_model_check", "arguments": map[string]any{"code": "ok", "count": float64(1)}}}}, true},
		{"choices tool_calls", map[string]any{"choices": []any{map[string]any{"message": map[string]any{"tool_calls": []any{map[string]any{"function": map[string]any{"name": "record_model_check", "arguments": okArgs}}}}}}}, true},
		{"content tool_use", map[string]any{"content": []any{map[string]any{"type": "tool_use", "name": "record_model_check", "input": map[string]any{"code": "ok", "count": float64(1)}}}}, true},
		{"candidates functionCall", map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"functionCall": map[string]any{"name": "record_model_check", "args": map[string]any{"code": "ok", "count": float64(1)}}}}}}}}, true},
		{"参数不匹配", map[string]any{"output": []any{map[string]any{"type": "function_call", "name": "record_model_check", "arguments": `{"code":"bad","count":2}`}}}, false},
		{"名称不匹配", map[string]any{"output": []any{map[string]any{"type": "function_call", "name": "other", "arguments": okArgs}}}, false},
		{"非 JSON 参数", map[string]any{"output": []any{map[string]any{"type": "function_call", "name": "record_model_check", "arguments": "!!!"}}}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := hasFunctionCall(test.payload, "record_model_check"); got != test.want {
				t.Fatalf("got=%v want=%v", got, test.want)
			}
		})
	}
	if hasFunctionCall(nil, "record_model_check") {
		t.Fatal("nil payload 必须返回 false")
	}
}

func TestWlEvaluateBasicScoreLadder(t *testing.T) {
	t.Run("成功匹配得满分", func(t *testing.T) {
		item := EvaluateBasic(Result{Success: true, ObservedModel: "gpt-test", Output: "OK-MODEL-CHECK"}, "gpt-test")
		if item.Status != "passed" || item.Score != 10 || item.MaxScore != 10 {
			t.Fatalf("item=%+v", item)
		}
	})
	t.Run("模型不匹配保留诊断分", func(t *testing.T) {
		item := EvaluateBasic(Result{Success: true, ObservedModel: "other", Output: "OK-MODEL-CHECK"}, "gpt-test")
		if item.Status != "failed" || item.Score != 1 || item.Evidence["modelMismatch"] != true {
			t.Fatalf("item=%+v", item)
		}
	})
	t.Run("仅模型匹配得警告", func(t *testing.T) {
		item := EvaluateBasic(Result{Success: true, ObservedModel: "gpt-test", Output: "wrong"}, "gpt-test")
		if item.Status != "warning" || item.Score != 3 {
			t.Fatalf("item=%+v", item)
		}
	})
	t.Run("请求失败走排除路径", func(t *testing.T) {
		item := EvaluateBasic(Result{Success: false, HTTPStatus: 503, ErrorMessage: "boom"}, "gpt-test")
		if item.Status != "skipped" || item.Evidence["requestFailure"] != true || item.Evidence["error"] != "boom" {
			t.Fatalf("item=%+v", item)
		}
	})
	t.Run("200 失败保留 failed 证据", func(t *testing.T) {
		item := EvaluateStructured(Result{Success: false, HTTPStatus: 200, Output: "not-json"}, "gpt-test")
		if item.Status != "failed" || item.Evidence["requestFailure"] != nil {
			t.Fatalf("HTTP 200 的语义失败不能标记 requestFailure: %+v", item)
		}
	})
	t.Run("模型不可用标记", func(t *testing.T) {
		item := EvaluateTool(Result{Success: false, HTTPStatus: 404, JSON: map[string]any{"error": map[string]any{"code": "model_not_found"}}}, "gpt-test")
		if item.Status != "skipped" || item.Evidence["modelUnavailable"] != true || item.Evidence["reason"] != "model_unavailable" {
			t.Fatalf("item=%+v", item)
		}
	})
	t.Run("重试证据回填", func(t *testing.T) {
		item := EvaluateBasic(Result{Success: false, HTTPStatus: 503, RetryAttemptCount: 2, RetryMaxAttempts: 3, AttemptStatusCodes: []int{500, 503}, RetryWaitDurations: []time.Duration{time.Second, 2 * time.Second}, AttemptDetails: []AttemptDetail{{StartedAt: time.Unix(0, 0).UTC(), Duration: time.Second, HTTPStatus: 500}}}, "gpt-test")
		if item.Evidence["retryAttemptCount"] != 2 || item.Evidence["retryMaxAttempts"] != 3 {
			t.Fatalf("item=%+v", item)
		}
		codes, _ := item.Evidence["attemptStatusCodes"].([]int)
		if len(codes) != 2 || codes[1] != 503 {
			t.Fatalf("attemptStatusCodes=%v", codes)
		}
		if waits, _ := item.Evidence["retryWaitMilliseconds"].([]int64); len(waits) != 2 || waits[1] != 2000 {
			t.Fatalf("retryWaitMilliseconds=%v", item.Evidence["retryWaitMilliseconds"])
		}
		if attempts, _ := item.Evidence["attempts"].([]map[string]any); len(attempts) != 1 || attempts[0]["httpStatus"] != 500 {
			t.Fatalf("attempts=%v", item.Evidence["attempts"])
		}
	})
}

func TestWlEvaluateProtocolStreamKinds(t *testing.T) {
	if item := EvaluateProtocolStream(Result{Success: true, ObservedModel: "m", Output: "STREAM-OK"}, "m", modelcheckprofile.ProtocolOpenAIChat); item.Kind != "protocol_stream" || item.Status != "passed" {
		t.Fatalf("chat 流评分=%+v", item)
	}
	if item := EvaluateStream(Result{Success: true, ObservedModel: "m", Output: "STREAM-OK"}, "m"); item.Kind != "responses_stream" || item.Score != 15 {
		t.Fatalf("responses 流评分=%+v", item)
	}
	if item := EvaluateProtocolStream(Result{Success: true, ObservedModel: "mismatch", Output: "STREAM-OK"}, "m", modelcheckprofile.ProtocolOpenAIResponses); item.Status != "failed" || item.Score != 5 {
		t.Fatalf("流模型不匹配=%+v", item)
	}
	if item := EvaluateProtocolStream(Result{Success: false, HTTPStatus: 500}, "m", modelcheckprofile.ProtocolOpenAIResponses); item.Status != "skipped" {
		t.Fatalf("流请求失败=%+v", item)
	}
}

func TestWlEvaluateStructuredAndToolMismatch(t *testing.T) {
	if item := EvaluateStructured(Result{Success: true, ObservedModel: "other", Output: `{"status":"ok","value":7}`}, "m"); item.Status != "failed" || item.Score != 5 {
		t.Fatalf("结构化模型不匹配=%+v", item)
	}
	if item := EvaluateStructured(Result{Success: true, ObservedModel: "m", Output: `noise {"status":"ok","value":7} tail`}, "m"); item.Status != "passed" {
		t.Fatalf("夹带 JSON 输出必须仍可解析: %+v", item)
	}
	if item := EvaluateStructured(Result{Success: true, ObservedModel: "m", Output: `{"status":"bad"}`}, "m"); item.Status != "warning" {
		t.Fatalf("结构化部分失败=%+v", item)
	}
	if item := EvaluateTool(Result{Success: true, ObservedModel: "other", JSON: map[string]any{"output": []any{map[string]any{"type": "function_call", "name": "record_model_check", "arguments": `{"code":"ok","count":1}`}}}}, "m"); item.Status != "failed" || item.Score != 5 {
		t.Fatalf("工具模型不匹配=%+v", item)
	}
	if item := EvaluateTool(Result{Success: true, ObservedModel: "m", JSON: map[string]any{}}, "m"); item.Status != "warning" || item.Score != 11 {
		// 模型匹配但工具未调用：8+3=11 分，落在 warning 档。
		t.Fatalf("工具未调用=%+v", item)
	}
}

func TestWlEvaluateUsageShape(t *testing.T) {
	if item := EvaluateUsage([]Result{{Success: true, Usage: map[string]any{"input_tokens": float64(3), "junk": "x"}}}); item.Status != "passed" || item.Score != 10 {
		t.Fatalf("item=%+v", item)
	}
	if item := EvaluateUsage([]Result{{Success: true}, {Success: false}}); item.Status != "skipped" || item.Evidence["evidenceInsufficient"] != true {
		t.Fatalf("无 usage 必须 skipped: %+v", item)
	}
}

func TestWlMatchProbeResponseModelMapping(t *testing.T) {
	if _, matched, mismatch := matchProbeResponseModel(Result{}, "m"); matched || mismatch {
		t.Fatal("未观测到响应模型时必须保持中立")
	}
	_, matched, mismatch := matchProbeResponseModel(Result{ObservedModel: "public-model", ModelMappingApplied: true, RequestModel: "public-model"}, "upstream-model")
	if !matched || mismatch {
		t.Fatalf("映射回显公共模型必须算匹配: matched=%v mismatch=%v", matched, mismatch)
	}
}

func TestWlModelMatchesSuffixRules(t *testing.T) {
	tests := []struct {
		actual, expected string
		want             bool
	}{
		{"gpt-test", " gpt-test ", true},
		{"gpt-test-2024-01-15", "gpt-test", true},
		{"gpt-test-2024-01-15-preview", "gpt-test", true},
		{"gpt-test-2024/01/15", "gpt-test", false},
		{"gpt-test-202401-15", "gpt-test", false},
		{"gpt-test-2024-0115", "gpt-test", false},
		{"gpt-test-x024-01-15", "gpt-test", false},
		{"gpt-test-2024-01-15x", "gpt-test", false},
		{"other", "gpt-test", false},
		{"", "gpt-test", false},
		{"gpt-test", "", false},
	}
	for _, test := range tests {
		if got := modelMatches(test.actual, test.expected); got != test.want {
			t.Fatalf("modelMatches(%q,%q)=%v want=%v", test.actual, test.expected, got, test.want)
		}
	}
}

func TestWlDistributionNumberTypes(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  float64
		ok    bool
	}{
		{"float64", float64(2), 2, true},
		{"float32", float32(3), 3, true},
		{"int", 4, 4, true},
		{"int8", int8(5), 5, true},
		{"int16", int16(6), 6, true},
		{"int32", int32(7), 7, true},
		{"int64", int64(8), 8, true},
		{"uint", uint(9), 9, true},
		{"uint8", uint8(10), 10, true},
		{"uint16", uint16(11), 11, true},
		{"uint32", uint32(12), 12, true},
		{"uint64", uint64(13), 13, true},
		{"NaN float64", math.NaN(), 0, false},
		{"Inf uint64", ^uint64(0), float64(^uint64(0)), true},
		{"字符串", "5", 0, false},
		{"nil", nil, 0, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := distributionNumber(test.value)
			if ok != test.ok {
				t.Fatalf("ok=%v want=%v", ok, test.ok)
			}
			if ok && (math.IsNaN(got) || got != test.want) && !(test.want > 1e18 && got > 1e18) {
				t.Fatalf("got=%v want=%v", got, test.want)
			}
		})
	}
	if _, ok := distributionNumber(math.Inf(1)); ok {
		t.Fatal("Inf 必须判定为不可用")
	}
}

func TestWlDistributionTotalTokensFallbacks(t *testing.T) {
	tests := []struct {
		name  string
		usage map[string]any
		want  float64
		ok    bool
	}{
		{"total_tokens", map[string]any{"total_tokens": float64(9)}, 9, true},
		{"totalTokens", map[string]any{"totalTokens": 9}, 9, true},
		{"totalTokenCount", map[string]any{"totalTokenCount": uint64(9)}, 9, true},
		{"输入输出相加", map[string]any{"input_tokens": float64(4), "output_tokens": float64(5)}, 9, true},
		{"仅输出", map[string]any{"output_tokens": float64(5)}, 5, true},
		{"prompt 对相加", map[string]any{"prompt_tokens": float64(4), "completion_tokens": float64(5)}, 9, true},
		{"gemini 对", map[string]any{"promptTokenCount": float64(4), "candidatesTokenCount": float64(5)}, 9, true},
		{"缺失", map[string]any{}, 0, false},
		{"空", nil, 0, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := distributionTotalTokens(test.usage)
			if ok != test.ok || got != test.want {
				t.Fatalf("got=%v ok=%v want=%v %v", got, ok, test.want, test.ok)
			}
		})
	}
}

func TestWlEvaluateDistributionBoundaries(t *testing.T) {
	if item := EvaluateDistribution(nil); item.Status != "skipped" || item.Evidence["totalPairs"] != 0 {
		t.Fatalf("空 pairs=%+v", item)
	}
	failed := EvaluateDistribution([]DistributionPair{{Definition: DistributionDefinition{Key: "style_compact"}, Target: Result{Success: false}, Comparison: Result{Success: false}}})
	if failed.Status != "skipped" || failed.Evidence["successfulPairCount"] != 0 {
		t.Fatalf("全失败=%+v", failed)
	}
	passed := EvaluateDistribution([]DistributionPair{
		{Definition: DistributionDefinition{Key: "style_compact"}, Target: Result{Success: true, Output: "向量召回相关的结果比例较高", Usage: map[string]any{"total_tokens": 8}}, Comparison: Result{Success: true, Output: "向量召回相关的结果比例较高", Usage: map[string]any{"total_tokens": 8}}},
	})
	if passed.Status != "passed" || passed.Score != 15 || passed.Evidence["partial"] != false {
		t.Fatalf("完全一致的分布必须满分: %+v", passed)
	}
}

func TestWlDistributionHelpers(t *testing.T) {
	if got := roundDistributionMetric(math.NaN()); got != 0 {
		t.Fatalf("NaN 指标必须归零: %v", got)
	}
	if got := roundDistributionMetric(math.Inf(-1)); got != 0 {
		t.Fatalf("Inf 指标必须归零: %v", got)
	}
	if got := boundedDistributionRatio(0, 5); got != 0 {
		t.Fatalf("零长度比例必须为 0: %v", got)
	}
	if got := distributionTextSimilarity("", "x"); got != 0 {
		t.Fatalf("空文本相似度必须为 0: %v", got)
	}
	if got := distributionTextSimilarity("Hello, world!", "hello world"); got != 1 {
		t.Fatalf("归一化后相同文本相似度必须为 1: %v", got)
	}
	if got := distributionTextSimilarity("ab", "完全不同"); got <= 0 || got >= 1 {
		t.Fatalf("部分相似文本应介于 0 和 1 之间: %v", got)
	}
	if got := utf16Length("队列"); got != 2 {
		t.Fatalf("utf16 长度=%d", got)
	}
	if !containsAny("abc", "x", "b") {
		t.Fatal("containsAny 必须命中第二个候选")
	}
	if containsAny("abc") {
		t.Fatal("无候选时必须返回 false")
	}
	if len(distributionComparableTokens("a")) != 1 {
		t.Fatal("单字符必须生成一个 token")
	}
	if len(distributionComparableTokens("ab")) != 1 {
		t.Fatal("双字符必须生成一个二元 token")
	}
	if got := averageDistribution(nil, func(distributionPairScore) float64 { return 1 }); got != 0 {
		t.Fatalf("空序列均值=%v", got)
	}
}

func TestWlEvaluateCrossModelPairEvidence(t *testing.T) {
	skipped := EvaluateCrossModelPair(Result{Success: false, HTTPStatus: 500}, Result{Success: true}, "m", "m")
	if skipped.Status != "skipped" || skipped.Evidence["terminalFailure"] != true {
		t.Fatalf("失败对=%+v", skipped)
	}
	unavailable := EvaluateCrossModelPair(Result{Success: false, JSON: map[string]any{"error": map[string]any{"code": "model_not_found"}}}, Result{Success: true}, "m", "m")
	if unavailable.Evidence["modelUnavailable"] != true {
		t.Fatalf("模型不可用=%+v", unavailable)
	}
	passed := EvaluateCrossModelPair(Result{Success: true, ObservedModel: "m", Output: "OK-MODEL-CHECK"}, Result{Success: true, ObservedModel: "m", Output: "CROSS-MODEL-OK"}, "m", "m")
	if passed.Status != "passed" || passed.Score != 10 {
		t.Fatalf("通过对=%+v", passed)
	}
	suspicious := EvaluateCrossModelPair(Result{Success: true, ObservedModel: "same", Output: "OK"}, Result{Success: true, ObservedModel: "same", Output: "OK"}, "m", "other")
	if suspicious.Status != "failed" || suspicious.Evidence["suspiciousSameBackend"] != true {
		t.Fatalf("同后端疑点=%+v", suspicious)
	}
	if item := EvaluateCrossModel(Result{Success: true, ObservedModel: "m", Output: "OK"}, Result{Success: true, ObservedModel: "m", Output: "OK"}, "m"); item.Evidence["crossModelMismatch"] != false {
		t.Fatalf("EvaluateCross 包装=%+v", item)
	}
	if item := EvaluateDistributionPassedCompat(); item != false {
		t.Fatalf("未知分布约束必须返回 false: %v", item)
	}
}

// EvaluateDistributionPassedCompat 覆盖 distributionPassed 的 default 分支。
func EvaluateDistributionPassedCompat() bool {
	return distributionPassed("unknown-key", "anything")
}

func TestWlDistributionPassedKeys(t *testing.T) {
	tests := []struct {
		key, output string
		want        bool
	}{
		{"style_compact", "向量召回相关的结果比例较高", true},
		{"style_compact", "太短", false},
		{"json_reasoning", `{"result":83,"tag":"SIGMA"}`, true},
		{"code_judgement", "ALPHA 4-7", true},
		{"refusal_boundary", "DELTA 不能提供", true},
		{"sequence_transform", "THETA 4|7|9", true},
		{"table_extract", "IOTA 17 23", true},
	}
	for _, test := range tests {
		if got := distributionPassed(test.key, test.output); got != test.want {
			t.Fatalf("distributionPassed(%q,%q)=%v want=%v", test.key, test.output, got, test.want)
		}
	}
}

func TestWlRetryPolicyOptions(t *testing.T) {
	if options := DefaultRetryOptions(); len(options.AttemptTimeouts) != 3 {
		t.Fatalf("默认重试档位=%v", options.AttemptTimeouts)
	}
	if options := RetryOptionsForProfile("quick"); len(options.AttemptTimeouts) != 3 || options.AttemptTimeouts[0] != 10*time.Second {
		t.Fatalf("quick 与 full 共享同一重试档位: %v", options.AttemptTimeouts)
	}
}

func TestWlExecuteWithRetryBranches(t *testing.T) {
	t.Run("空策略直达 Execute", func(t *testing.T) {
		transport := &wlStubTransport{status: http.StatusOK, body: `{"model":"gpt-test","choices":[{"message":{"content":"OK-MODEL-CHECK"}}]}`}
		result, err := ExecuteWithRetry(context.Background(), mustWlBasicRequest(t), Options{Endpoint: "https://upstream.example", Client: &http.Client{Transport: transport}, Timeout: time.Second}, RetryOptions{})
		if err != nil || !result.Success || result.RetryAttemptCount != 0 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("零值超时使用默认单次", func(t *testing.T) {
		transport := &wlStubTransport{status: http.StatusOK, body: `{"model":"gpt-test","choices":[{"message":{"content":"OK-MODEL-CHECK"}}]}`}
		result, err := ExecuteWithRetry(context.Background(), mustWlBasicRequest(t), Options{Endpoint: "https://upstream.example", Client: &http.Client{Transport: transport}, Timeout: time.Second}, RetryOptions{Now: func() time.Time { return time.Unix(0, 0) }})
		if err != nil || !result.Success || result.RetryMaxAttempts != 1 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("负超时立即失败", func(t *testing.T) {
		if _, err := ExecuteWithRetry(context.Background(), mustWlBasicRequest(t), Options{Endpoint: "https://upstream.example"}, RetryOptions{AttemptTimeouts: []time.Duration{-1}}); err == nil || !strings.Contains(err.Error(), "must be positive") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("重试后成功记录状态码", func(t *testing.T) {
		attempts := 0
		transport := &wlRetryOnceTransport{attempts: &attempts}
		result, err := ExecuteWithRetry(context.Background(), mustWlBasicRequest(t), Options{Endpoint: "https://upstream.example", Client: &http.Client{Transport: transport}, Timeout: time.Second}, RetryOptions{AttemptTimeouts: []time.Duration{time.Second, time.Second}, Delay: func(context.Context) error { return nil }, Now: func() time.Time { return time.Unix(0, 0) }})
		if err != nil || !result.Success {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if result.RetryAttemptCount != 1 || len(result.AttemptStatusCodes) != 2 || result.AttemptStatusCodes[0] != http.StatusTooManyRequests {
			t.Fatalf("重试元数据=%+v", result)
		}
		if len(result.AttemptDetails) != 2 || result.AttemptDetails[1].HTTPStatus != http.StatusOK {
			t.Fatalf("attempt details=%+v", result.AttemptDetails)
		}
	})
	t.Run("延迟失败保留已尝试证据", func(t *testing.T) {
		transport := &wlStubTransport{status: 500, body: `{"error":"down"}`}
		result, err := ExecuteWithRetry(context.Background(), mustWlBasicRequest(t), Options{Endpoint: "https://upstream.example", Client: &http.Client{Transport: transport}, Timeout: time.Second}, RetryOptions{AttemptTimeouts: []time.Duration{time.Second, time.Second}, Delay: func(context.Context) error { return errors.New("delay stop") }})
		if err == nil || result.RetryAttemptCount != 0 || len(result.AttemptStatusCodes) != 1 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("默认延迟响应取消", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		transport := &wlStubTransport{status: 500, body: `{}`}
		_, err := ExecuteWithRetry(ctx, mustWlBasicRequest(t), Options{Endpoint: "https://upstream.example", Client: &http.Client{Transport: transport}, Timeout: time.Second}, RetryOptions{AttemptTimeouts: []time.Duration{time.Second, time.Second}})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("已取消的上下文必须中断默认延迟: err=%v", err)
		}
	})
	t.Run("200 失败信封不再重试", func(t *testing.T) {
		transport := &wlStubTransport{status: 200, body: `{"error":{"code":"model_not_found"}}`}
		result, err := ExecuteWithRetry(context.Background(), mustWlBasicRequest(t), Options{Endpoint: "https://upstream.example", Client: &http.Client{Transport: transport}, Timeout: time.Second}, RetryOptions{AttemptTimeouts: []time.Duration{time.Second, time.Second, time.Second}, Delay: func(context.Context) error { return nil }})
		if err != nil || result.RetryAttemptCount != 0 {
			t.Fatalf("HTTP 200 是确定性结果: result=%+v err=%v", result, err)
		}
	})
}

// wlRetryOnceTransport 第一次返回 429，之后返回成功响应。
type wlRetryOnceTransport struct{ attempts *int }

func (t *wlRetryOnceTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	*t.attempts++
	if *t.attempts == 1 {
		return &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"rate limited"}`)), Request: request}, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"model":"gpt-test","choices":[{"message":{"content":"OK-MODEL-CHECK"}}]}`)), Request: request}, nil
}

func TestWlIsTerminalProbeFailureMatrix(t *testing.T) {
	tests := []struct {
		name   string
		result Result
		want   bool
	}{
		{"成功非终局", Result{Success: true}, false},
		{"200 语义失败非终局", Result{Success: false, HTTPStatus: 200}, false},
		{"未配置重试的 503 终局", Result{Success: false, HTTPStatus: 503}, true},
		{"重试预算未耗尽", Result{Success: false, HTTPStatus: 503, RetryAttemptCount: 0, RetryMaxAttempts: 3, AttemptStatusCodes: []int{503}}, false},
		{"重试预算耗尽", Result{Success: false, HTTPStatus: 503, RetryAttemptCount: 2, RetryMaxAttempts: 3, AttemptStatusCodes: []int{500, 502, 503}}, true},
		{"状态码数量优先于计数", Result{Success: false, HTTPStatus: 503, RetryAttemptCount: 0, RetryMaxAttempts: 2, AttemptStatusCodes: []int{500, 502, 503}}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isTerminalProbeFailure(test.result); got != test.want {
				t.Fatalf("got=%v want=%v", got, test.want)
			}
		})
	}
}

func TestWlIsModelUnavailableVariants(t *testing.T) {
	tests := []struct {
		name   string
		result Result
		model  string
		want   bool
	}{
		{"成功不判不可用", Result{Success: true}, "m", false},
		{"错误码 model_not_found", Result{JSON: map[string]any{"error": map[string]any{"code": "model_not_found"}}}, "m", true},
		{"中文模型不存在", Result{ErrorMessage: "模型不存在"}, "m", true},
		{"提及模型且 unsupported", Result{ErrorMessage: "gpt-x is not supported here"}, "gpt-x", true},
		{"无可用渠道但提及模型", Result{ErrorMessage: "no available channel"}, "channel", true},
		{"无关错误", Result{ErrorMessage: "connection refused"}, "m", false},
		{"嵌套数组信封", Result{JSON: map[string]any{"errors": []any{map[string]any{"message": "unsupported_model"}}}}, "m", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsModelUnavailable(test.result, test.model); got != test.want {
				t.Fatalf("got=%v want=%v", got, test.want)
			}
		})
	}
}

func TestWlTokenizerO200k(t *testing.T) {
	tokenizer, err := NewO200kTokenizer()
	if err != nil {
		t.Fatalf("初始化 o200k 失败: %v", err)
	}
	if tokenizer.Version() == "" {
		t.Fatal("版本快照不能为空")
	}
	count, err := tokenizer.Count("hello world")
	if err != nil || count <= 0 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if empty, err := tokenizer.Count("  "); err != nil || empty != 0 {
		t.Fatalf("空白必须计为零: count=%d err=%v", empty, err)
	}
	var nilTokenizer *O200kTokenizer
	if _, err := nilTokenizer.Count("x"); err == nil {
		t.Fatal("未初始化的 tokenizer 必须报错")
	}
}

func TestWlBuildTokenPromptBoundaries(t *testing.T) {
	if _, _, err := buildTokenPrompt(deterministicTokenizer{}, "p", -1); err == nil {
		t.Fatal("负 padding 必须拒绝")
	}
	if _, _, err := buildTokenPrompt(deterministicTokenizer{}, "p", 2049); err == nil {
		t.Fatal("超上限 padding 必须拒绝")
	}
	if _, _, err := buildTokenPrompt(wlBrokenTokenizer{}, "p", 4); err == nil {
		t.Fatal("tokenizer 失败必须上抛")
	}
	// 该 tokenizer 每追加一次 " x" 增加 2 个 token，永远无法精确命中奇数目标。
	if _, _, err := buildTokenPrompt(wlStepTwoTokenizer{}, "p", 3); err == nil {
		t.Fatal("无法精确构造 padding 时必须报错")
	}
	prompt, tokens, err := buildTokenPrompt(deterministicTokenizer{}, "prefix", 5)
	if err != nil || tokens <= 5 {
		t.Fatalf("prompt=%q tokens=%d err=%v", prompt, tokens, err)
	}
}

type wlBrokenTokenizer struct{}

func (wlBrokenTokenizer) Version() string           { return "broken" }
func (wlBrokenTokenizer) Count(string) (int, error) { return 0, errors.New("boom-count") }

// wlStepTwoTokenizer 模拟只能成对增长的 tokenizer。
type wlStepTwoTokenizer struct{}

func (wlStepTwoTokenizer) Version() string { return "step2" }
func (wlStepTwoTokenizer) Count(value string) (int, error) {
	return 1 + 2*strings.Count(value, " x"), nil
}

func TestWlUsageIntegerForms(t *testing.T) {
	if got := usageInteger(map[string]any{"input_tokens": float64(7)}, "input_tokens"); got != 7 {
		t.Fatalf("got=%d", got)
	}
	if got := usageInteger(map[string]any{"prompt_tokens": 9}, "prompt_tokens"); got != 9 {
		t.Fatalf("int 形态 got=%d", got)
	}
	if got := usageInteger(map[string]any{"input_tokens": float64(-1)}, "input_tokens"); got != -1 {
		t.Fatalf("负值 got=%d", got)
	}
	if got := usageInteger(map[string]any{"input_tokens": 2.5}, "input_tokens"); got != -1 {
		t.Fatalf("非整数 got=%d", got)
	}
	if got := usageInteger(map[string]any{}, "missing"); got != -1 {
		t.Fatalf("缺失 got=%d", got)
	}
}

func TestWlAnalyzeTokenIntegrityVerdicts(t *testing.T) {
	t.Run("样本不足", func(t *testing.T) {
		analysis := AnalyzeTokenIntegrity(nil)
		if analysis.Status != "unsupported" || analysis.ReasonCodes[0] != "reported_usage_missing" {
			t.Fatalf("analysis=%+v", analysis)
		}
	})
	t.Run("斜率不可用", func(t *testing.T) {
		fixed := 50
		samples := make([]TokenSample, 0, 6)
		for round := 0; round < 3; round++ {
			for _, padding := range []int{0, 512, 2048} {
				samples = append(samples, TokenSample{RoundIndex: round, PaddingTokens: padding, LocalInputTokens: 100 + padding, ReportedInputTokens: &fixed})
			}
		}
		analysis := AnalyzeTokenIntegrity(samples)
		if analysis.Status != "unsupported" || analysis.ReasonCodes[0] != "reported_usage_incompatible" {
			t.Fatalf("analysis=%+v", analysis)
		}
	})
	t.Run("一致", func(t *testing.T) {
		samples := make([]TokenSample, 0, 9)
		for round := 0; round < 3; round++ {
			for _, padding := range tokenPaddingOrder(round) {
				reported := 100 + padding
				samples = append(samples, TokenSample{RoundIndex: round, PaddingTokens: padding, LocalInputTokens: 100 + padding, ReportedInputTokens: &reported})
			}
		}
		analysis := AnalyzeTokenIntegrity(samples)
		if analysis.Status != "consistent" || analysis.Slope != 1 || analysis.SampleCount != 9 || analysis.RoundCount != 3 {
			t.Fatalf("analysis=%+v", analysis)
		}
	})
	t.Run("比例膨胀", func(t *testing.T) {
		samples := make([]TokenSample, 0, 9)
		for round := 0; round < 3; round++ {
			for _, padding := range tokenPaddingOrder(round) {
				reported := 2 * (100 + padding)
				samples = append(samples, TokenSample{RoundIndex: round, PaddingTokens: padding, LocalInputTokens: 100 + padding, ReportedInputTokens: &reported})
			}
		}
		analysis := AnalyzeTokenIntegrity(samples)
		if analysis.Status != "suspected_padding" || analysis.ReasonCodes[0] != "proportional_padding" {
			t.Fatalf("analysis=%+v", analysis)
		}
	})
	t.Run("桶取整降级为警告", func(t *testing.T) {
		samples := make([]TokenSample, 0, 9)
		for round := 0; round < 3; round++ {
			for _, padding := range tokenPaddingOrder(round) {
				local := 100 + padding
				reported := ((local + 63) / 64) * 64
				samples = append(samples, TokenSample{RoundIndex: round, PaddingTokens: padding, LocalInputTokens: local, ReportedInputTokens: &reported})
			}
		}
		analysis := AnalyzeTokenIntegrity(samples)
		if analysis.Status != "warning" {
			t.Fatalf("桶取整必须降为 warning: %+v", analysis)
		}
		found := false
		for _, reason := range analysis.ReasonCodes {
			found = found || reason == "bucket_rounding"
		}
		if !found {
			t.Fatalf("缺少 bucket_rounding 原因码: %+v", analysis)
		}
	})
	t.Run("桶取整样本不足不触发", func(t *testing.T) {
		reported := 128
		analysis := AnalyzeTokenIntegrity([]TokenSample{{PaddingTokens: 64, LocalInputTokens: 164, ReportedInputTokens: &reported}})
		if analysis.Status != "unsupported" {
			t.Fatalf("analysis=%+v", analysis)
		}
	})
}

func TestWlRoundAndPaddingOrder(t *testing.T) {
	if got := round(math.NaN()); !math.IsNaN(got) {
		t.Fatalf("NaN 必须原样保留: %v", got)
	}
	if got := round(math.Inf(1)); !math.IsInf(got, 1) {
		t.Fatalf("Inf 必须原样保留: %v", got)
	}
	if got := round(1.2345678901); got != 1.234568 {
		t.Fatalf("round=%v", got)
	}
	orders := map[int][]int{}
	for round := 0; round < 3; round++ {
		orders[round] = tokenPaddingOrder(round)
		if len(orders[round]) != 3 {
			t.Fatalf("每轮必须三个 padding: %v", orders[round])
		}
	}
	if orders[0][0] != 0 || orders[1][0] != 2048 || orders[2][0] != 512 {
		t.Fatalf("padding 轮换顺序不符: %v", orders)
	}
}

func TestWlRunIdentityWrapperAndVerdicts(t *testing.T) {
	t.Run("包装函数缺省模型族", func(t *testing.T) {
		item, err := RunIdentity(context.Background(), modelcheckprofile.ProtocolOpenAIResponses, "unknown-model", func(_ context.Context, _ Request) (Result, error) {
			return Result{Success: false, HTTPStatus: 503}, nil
		})
		if err != nil || item.Kind != "identity_observation" || item.Status != "skipped" || item.Evidence["observationCount"] != 7 {
			t.Fatalf("item=%+v err=%v", item, err)
		}
	})
	t.Run("输入非法", func(t *testing.T) {
		if _, err := RunIdentity(context.Background(), modelcheckprofile.ProtocolOpenAIResponses, " ", nil); err == nil {
			t.Fatal("空模型必须拒绝")
		}
		if _, err := RunIdentityForModels(context.Background(), modelcheckprofile.ProtocolOpenAIResponses, "m", nil, nil); err == nil {
			t.Fatal("nil run 必须拒绝")
		}
	})
	t.Run("全部成功但未通过约束计失败", func(t *testing.T) {
		item, err := RunIdentityForModels(context.Background(), modelcheckprofile.ProtocolOpenAIResponses, "unknown-model", nil, func(_ context.Context, _ Request) (Result, error) {
			return Result{Success: true, HTTPStatus: 200, ObservedModel: "unknown-model", Output: "wrong"}, nil
		})
		if err != nil || item.Status != "failed" || item.Evidence["successCount"] != 7 {
			t.Fatalf("item=%+v err=%v", item, err)
		}
	})
	t.Run("run 失败上抛", func(t *testing.T) {
		if _, err := RunIdentityForModels(context.Background(), modelcheckprofile.ProtocolOpenAIResponses, "m", nil, func(_ context.Context, _ Request) (Result, error) {
			return Result{}, errors.New("boom-run")
		}); err == nil {
			t.Fatal("run 错误必须上抛")
		}
	})
}

func TestWlIdentityPassedKeys(t *testing.T) {
	tests := []struct {
		key, output string
		want        bool
	}{
		{"constraint_json", `{"result":42,"tag":"T"}`, true},
		{"constraint_json", `{"result":41,"tag":"T"}`, false},
		{"error_recovery", `{"correct":42,"tag":"T"}`, true},
		{"reasoning_order", `{"largest":15,"tag":"T"}`, true},
		{"multilingual_consistency", `{"zh":"队列超时","en":"queue timeout","tag":"T"}`, true},
		{"tool_schema", `{"action":"inspect","tag":"T","payload":{"ids":[2,7,9],"dryRun":true}}`, true},
		{"knowledge_window", `{"version":"B","tag":"T"}`, true},
		{"unknown", `{"a":1}`, false},
		{"constraint_json", "not json", false},
	}
	for _, test := range tests {
		if got := identityPassed(test.key, test.output, "T"); got != test.want {
			t.Fatalf("identityPassed(%q)=%v want=%v", test.key, got, test.want)
		}
	}
	if !identityPassed("code_patch", "xs.filter(x=>x>2).sort((a,b)=>a-b) // T", "t") {
		t.Fatal("code_patch 必须匹配 filter/sort 与 tag")
	}
	if identityPassed("code_patch", "xs.filter(x=>x>2) // T", "T") {
		t.Fatal("缺少 sort 的输出不能通过")
	}
}

func TestWlPairedIdentityModels(t *testing.T) {
	if got := pairedIdentityModels("gpt-5.5"); len(got) != 2 || got[0] != "gpt-5.5" || got[1] != "gpt-5.4" {
		t.Fatalf("got=%v", got)
	}
	if got := pairedIdentityModels("other"); len(got) != 1 || got[0] != "other" {
		t.Fatalf("got=%v", got)
	}
	if got := uniqueModels(" a ", "a", "b", ""); len(got) != 2 {
		t.Fatalf("去重失败: %v", got)
	}
}

func TestWlJuiceRequestBuilders(t *testing.T) {
	requests := JuiceRequests("gpt-5.6-sol")
	if len(requests) != 6 {
		t.Fatalf("Juice 探针必须六个请求: %d", len(requests))
	}
	if got := JuiceRequests(" "); got != nil {
		t.Fatalf("空模型必须返回 nil: %v", got)
	}
	requestsStream, coverage, err := JuiceRequestsForStream("gpt-5.6-terra", true)
	if err != nil || len(requestsStream) != 6 {
		t.Fatalf("流式 Juice 请求构造失败: %v", err)
	}
	if !validJuiceCoverage(coverage) {
		t.Fatalf("coverage=%q 必须合法", coverage)
	}
	for _, request := range requestsStream {
		var payload map[string]any
		if err := json.Unmarshal(request.Body, &payload); err != nil {
			t.Fatalf("Juice body 非法: %v", err)
		}
		if payload["reasoning"].(map[string]any)["effort"] != "high" || payload["temperature"] != float64(0) {
			t.Fatalf("Juice 采样配置不符: %#v", payload)
		}
	}
	if _, _, err := JuiceRequestsForStream("", false); err == nil {
		t.Fatal("空模型必须报错")
	}
}

func TestWlValidJuiceCoverage(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"12345", true}, {" 12345 ", true}, {"10000", true},
		{"80000", false}, {"16000", false}, {"40000", false},
		{"9999", false}, {"100000", false}, {"abc", false}, {"", false},
	}
	for _, test := range tests {
		if got := validJuiceCoverage(test.value); got != test.want {
			t.Fatalf("validJuiceCoverage(%q)=%v want=%v", test.value, got, test.want)
		}
	}
}

func TestWlEvaluateJuiceVerdicts(t *testing.T) {
	base := func() []Result {
		return []Result{
			{Success: true, HTTPStatus: 200, Output: "40"},
			{Success: true, HTTPStatus: 200, Output: "40"},
			{Success: true, HTTPStatus: 200, Output: "40"},
			{Success: true, HTTPStatus: 200, Output: "32"},
			{Success: true, HTTPStatus: 200, Output: "48"},
			{Success: true, HTTPStatus: 200, Output: "12345"},
		}
	}
	t.Run("全部通过", func(t *testing.T) {
		item := EvaluateJuice("gpt-5.6-sol", base(), "12345")
		if item.Status != "passed" || item.Evidence["hardAnomaly"] != false {
			t.Fatalf("item=%+v", item)
		}
	})
	t.Run("模型不适用", func(t *testing.T) {
		item := EvaluateJuice("gpt-other", base(), "12345")
		if item.Status != "skipped" || item.Evidence["notApplicable"] != true {
			t.Fatalf("item=%+v", item)
		}
	})
	t.Run("结果缺失", func(t *testing.T) {
		item := EvaluateJuice("gpt-5.6-sol", nil, "12345")
		if item.Evidence["requestFailure"] != true {
			t.Fatalf("item=%+v", item)
		}
	})
	t.Run("coverage 非法", func(t *testing.T) {
		item := EvaluateJuice("gpt-5.6-sol", base(), "8")
		if item.Evidence["reason"] != "juice_coverage_value_invalid" {
			t.Fatalf("item=%+v", item)
		}
	})
	t.Run("输出被替换为强异常", func(t *testing.T) {
		results := base()
		results[3].Output = "99"
		item := EvaluateJuice("gpt-5.6-sol", results, "12345")
		if item.Status != "failed" || item.Evidence["strongAnomaly"] != true || item.Evidence["scorePenalty"] != JuiceStrongPenalty {
			t.Fatalf("item=%+v", item)
		}
	})
	t.Run("coverage 不匹配", func(t *testing.T) {
		results := base()
		results[5].Output = "54321"
		item := EvaluateJuice("gpt-5.6-sol", results, "12345")
		if item.Status != "failed" || item.Evidence["scorePenalty"] != JuiceCoveragePenalty {
			t.Fatalf("item=%+v", item)
		}
	})
	t.Run("混合已知值为弱异常", func(t *testing.T) {
		results := base()
		results[0].Output = "16"
		item := EvaluateJuice("gpt-5.6-sol", results, "12345")
		if item.Status != "failed" || item.Evidence["scorePenalty"] != JuiceWeakPenalty {
			t.Fatalf("item=%+v", item)
		}
	})
	t.Run("非数值 juice 输出为弱异常", func(t *testing.T) {
		results := base()
		results[1].Output = "abc"
		item := EvaluateJuice("gpt-5.6-sol", results, "12345")
		if item.Status != "failed" || item.Evidence["scorePenalty"] != JuiceWeakPenalty {
			t.Fatalf("item=%+v", item)
		}
	})
	t.Run("三次相同混合值为强异常", func(t *testing.T) {
		results := base()
		for index := 0; index < 3; index++ {
			results[index].Output = "24"
		}
		item := EvaluateJuice("gpt-5.6-sol", results, "12345")
		if item.Status != "failed" || item.Evidence["strongAnomaly"] != true {
			t.Fatalf("item=%+v", item)
		}
	})
	t.Run("终局失败排除评分", func(t *testing.T) {
		results := base()
		results[0].HTTPStatus = 500
		results[0].Success = false
		item := EvaluateJuice("gpt-5.6-sol", results, "12345")
		if item.Status != "skipped" || item.Evidence["terminalFailure"] != true || item.Evidence["excludedFromScoring"] != true {
			t.Fatalf("item=%+v", item)
		}
	})
	t.Run("部分失败降级 skipped", func(t *testing.T) {
		results := base()
		results[2].Success = false
		item := EvaluateJuice("gpt-5.6-sol", results, "12345")
		if item.Status != "skipped" {
			t.Fatalf("item=%+v", item)
		}
	})
}

func TestWlJuiceSmallHelpers(t *testing.T) {
	if juiceKind(0) != "juice" || juiceKind(2) != "juice" || juiceKind(3) != "output_integrity" || juiceKind(5) != "coverage" {
		t.Fatalf("juiceKind 分类错误")
	}
	if !isKnownJuice(" 32 ") || isKnownJuice("31") {
		t.Fatal("isKnownJuice 判定错误")
	}
	if juiceSignature("gpt-5.6-sol") != "40" || juiceSignature("gpt-5.6-terra") != "32" || juiceSignature("gpt-5.6-luna") != "48" || juiceSignature("other") != "" {
		t.Fatal("juiceSignature 映射错误")
	}
	if !ShouldRunJuice("gpt-5.6-sol", "full", "openai_responses") {
		t.Fatal("gpt-5.6 full responses 必须运行 Juice")
	}
	if ShouldRunJuice("gpt-5.6-sol", "quick", "openai_responses") || ShouldRunJuice("gpt-other", "full", "openai_responses") || ShouldRunJuice("gpt-5.6-sol", "full", "openai_chat") {
		t.Fatal("非目标范围必须跳过 Juice")
	}
}

func TestWlEvaluateStabilityLadder(t *testing.T) {
	passed := EvaluateStability([]Result{
		{Success: true, ObservedModel: "m", Output: "VECTOR"},
		{Success: true, ObservedModel: "m", Output: "VECTOR"},
	}, "m")
	if passed.Status != "passed" || passed.Score != 15 {
		t.Fatalf("item=%+v", passed)
	}
	partial := EvaluateStability([]Result{
		{Success: true, ObservedModel: "m", Output: "VECTOR"},
		{Success: true, ObservedModel: "m", Output: "NOPE"},
	}, "m")
	if partial.Status != "warning" {
		t.Fatalf("部分通过必须 warning: %+v", partial)
	}
	mismatch := EvaluateStability([]Result{{Success: true, ObservedModel: "other", Output: "VECTOR"}}, "m")
	if mismatch.Status != "failed" || mismatch.Score != 4 {
		t.Fatalf("模型不匹配=%+v", mismatch)
	}
	skipped := EvaluateStability([]Result{{Success: false, HTTPStatus: 503}}, "m")
	if skipped.Status != "skipped" || skipped.Evidence["probeCount"] != 1 {
		t.Fatalf("item=%+v", skipped)
	}
	neutral := EvaluateStability([]Result{{Success: true, ObservedModel: "", Output: "VECTOR"}}, "m")
	if neutral.Status != "warning" || neutral.Evidence["modelMismatch"] != false {
		// 缺失响应模型是中立证据：无法满分，也不是替换证据。
		t.Fatalf("缺失模型必须保持中立: %+v", neutral)
	}
}

func TestWlEvaluateLongContextLadder(t *testing.T) {
	mk := func(marker, output string, success bool) LongContextObservation {
		return LongContextObservation{Key: "k", Marker: marker, Result: Result{Success: success, ObservedModel: "m", Output: output}}
	}
	passed := EvaluateLongContext([]LongContextObservation{mk("NEEDLE-LOW-1", "NEEDLE-LOW-1", true), mk("NEEDLE-MED-2", "NEEDLE-MED-2", true)}, "m")
	if passed.Status != "passed" || passed.Score != 15 {
		t.Fatalf("item=%+v", passed)
	}
	warning := EvaluateLongContext([]LongContextObservation{mk("NEEDLE-LOW-1", "NEEDLE-LOW-1", true), mk("NEEDLE-MED-2", "missed", true)}, "m")
	if warning.Status != "warning" {
		t.Fatalf("部分命中必须 warning: %+v", warning)
	}
	neutral := EvaluateLongContext([]LongContextObservation{{Key: "k", Marker: "NEEDLE-LOW-1", Result: Result{Success: true, Output: "NEEDLE-LOW-1"}}}, "m")
	if neutral.Status != "warning" || neutral.Evidence["modelMismatch"] != false {
		// 缺失响应模型是中立证据：无法满分，也不是替换证据。
		t.Fatalf("缺失模型必须保持中立: %+v", neutral)
	}
	failed := EvaluateLongContext([]LongContextObservation{mk("NEEDLE-LOW-1", "missed", true)}, "m")
	if failed.Status != "failed" {
		t.Fatalf("未命中必须 failed: %+v", failed)
	}
	skipped := EvaluateLongContext([]LongContextObservation{mk("NEEDLE", "", false)}, "m")
	if skipped.Status != "skipped" || skipped.Evidence["requestFailure"] != true {
		t.Fatalf("item=%+v", skipped)
	}
}

func TestWlRunLongContextBoundaries(t *testing.T) {
	t.Run("缺少快照被排除", func(t *testing.T) {
		item, err := RunLongContext(context.Background(), "openai", "m", modelcheckprofile.ProtocolOpenAIResponses, nil, nil, nil)
		if err != nil || item.Status != "skipped" || item.Evidence["reason"] != "model_limit_snapshot_not_attached" {
			t.Fatalf("item=%+v err=%v", item, err)
		}
	})
	t.Run("输入非法", func(t *testing.T) {
		if _, err := RunLongContext(context.Background(), "openai", " ", modelcheckprofile.ProtocolOpenAIResponses, deterministicTokenizer{}, deterministicLimits{}, nil); err == nil {
			t.Fatal("空模型必须拒绝")
		}
	})
	t.Run("上限无效被排除", func(t *testing.T) {
		limits := wlFixedLimits{limit: 100, err: errors.New("no limit")}
		item, err := RunLongContext(context.Background(), "openai", "m", modelcheckprofile.ProtocolOpenAIResponses, deterministicTokenizer{}, limits, func(context.Context, Request) (Result, error) {
			return Result{}, errors.New("must-not-run")
		})
		if err != nil || item.Status != "skipped" || item.Evidence["reason"] != "model_limit_snapshot_invalid" {
			t.Fatalf("item=%+v err=%v", item, err)
		}
	})
	t.Run("探针失败上抛", func(t *testing.T) {
		if _, err := RunLongContext(context.Background(), "openai", "m", modelcheckprofile.ProtocolOpenAIResponses, deterministicTokenizer{}, deterministicLimits{}, func(context.Context, Request) (Result, error) {
			return Result{}, errors.New("boom")
		}); err == nil {
			t.Fatal("run 失败必须上抛")
		}
	})
}

// wlFixedLimits 固定返回上限或错误，用于验证快照校验分支。
type wlFixedLimits struct {
	limit int
	err   error
}

func (l wlFixedLimits) Version() string { return "fixed-v1" }
func (l wlFixedLimits) MaxInputTokens(string, string, modelcheckprofile.Protocol) (int, error) {
	return l.limit, l.err
}

func TestWlMinIntMaxInt(t *testing.T) {
	if minInt(1, 2) != 1 || minInt(2, 1) != 1 || maxInt(1, 2) != 2 || maxInt(2, 1) != 2 {
		t.Fatal("minInt/maxInt 语义错误")
	}
}

func TestWlBuildBasicProtocolRejection(t *testing.T) {
	if _, err := BuildBasic("weird-protocol", "m", "p", false); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("未知协议必须拒绝: %v", err)
	}
	if _, err := BuildBasic(modelcheckprofile.ProtocolOpenAIChat, " ", "p", false); err == nil {
		t.Fatal("空模型必须拒绝")
	}
	if _, err := BuildBasicForEndpointMode(modelcheckprofile.ProtocolOpenAIChat, "m", "p", modelcheckprofile.EndpointModeResponsesJSON); err == nil {
		t.Fatal("端点模式与协议不匹配必须拒绝")
	}
	// Anthropic 不接受 temperature 调节，按原样透传 max_tokens。
	request, err := buildBasicWithTunings(modelcheckprofile.ProtocolAnthropic, "m", "p", "", false, 5, 0.7)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(request.Body, &payload); err != nil {
		t.Fatalf("body 非法: %v", err)
	}
	if payload["max_tokens"] != float64(5) {
		t.Fatalf("Anthropic 必须按原样透传 max_tokens: %#v", payload)
	}
	if _, ok := payload["temperature"]; ok {
		t.Fatalf("Anthropic 不能携带 temperature: %#v", payload)
	}
	// Gemini 输出预算下限 128。
	request, err = buildBasicWithTunings(modelcheckprofile.ProtocolGeminiNative, "m", "p", "", false, 1, 0.7)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if err := json.Unmarshal(request.Body, &payload); err != nil {
		t.Fatalf("body 非法: %v", err)
	}
	generation := payload["generationConfig"].(map[string]any)
	if generation["maxOutputTokens"] != float64(128) {
		t.Fatalf("Gemini 下限 128: %#v", generation)
	}
	// Codex 归一化忽略 tuning 字段。
	request, err = buildBasicWithTunings(modelcheckprofile.ProtocolOpenAIResponses, "m", "p", "", false, 1, 0.9)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if err := json.Unmarshal(request.Body, &payload); err != nil {
		t.Fatalf("body 非法: %v", err)
	}
	if payload["max_output_tokens"] != float64(16) {
		t.Fatalf("Responses 输出下限 16: %#v", payload)
	}
}

func TestWlSummaryHelpers(t *testing.T) {
	if UnscopedKindForOwner("target.protocol_basic") != "protocol_basic" {
		t.Fatal("UnscopedKindForOwner 必须剥离前缀")
	}
	if unscopedKind("plain") != "plain" {
		t.Fatal("无前缀必须原样返回")
	}
	items := unscopedEvaluations([]Evaluation{{Kind: "target.basic"}, {Kind: "x.y.z"}})
	if items[0].Kind != "basic" || items[1].Kind != "z" {
		t.Fatalf("unscopedEvaluations=%+v", items)
	}
	evaluations := []Evaluation{{Kind: "basic", Status: "passed"}, {Kind: "juice", Status: "skipped", Evidence: map[string]any{"requestFailure": true}}}
	if !hasStatus(evaluations, "basic", "passed") || hasStatus(evaluations, "basic", "failed") {
		t.Fatal("hasStatus 判定错误")
	}
	if !hasStatusAny(evaluations, "passed", "basic", "juice") || hasStatusAny(evaluations, "failed", "basic") {
		t.Fatal("hasStatusAny 判定错误")
	}
	if !hasRequestFailure(evaluations, "juice") || hasRequestFailure(evaluations, "basic") {
		t.Fatal("hasRequestFailure 判定错误")
	}
	if !evidenceBool(map[string]any{"k": true}, "k") || evidenceBool(nil, "k") {
		t.Fatal("evidenceBool 判定错误")
	}
	if findEvaluation(evaluations, "missing") != nil {
		t.Fatal("缺失项必须返回 nil")
	}
}

func TestWlSummarizeChecksDecisionLadder(t *testing.T) {
	tests := []struct {
		name  string
		items []Evaluation
		cmp   bool
		prof  string
		level string
	}{
		{"模型不匹配即可疑", []Evaluation{{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true, "modelMismatch": true}}}, false, "full", "suspicious"},
		{"juice 硬异常即可疑", []Evaluation{{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}}, {Kind: "juice", Status: "failed", Evidence: map[string]any{"hardAnomaly": true, "scorePenalty": 25}}}, false, "full", "suspicious"},
		{"basic 失败不可用", []Evaluation{{Kind: "protocol_basic", Status: "failed", MaxScore: 10, Evidence: map[string]any{"success": false}}}, false, "full", "unavailable"},
		{"长上下文失败可疑", []Evaluation{{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}}, {Kind: "long_context", Status: "failed", MaxScore: 15}}, false, "full", "suspicious"},
		{"长上下文警告不确定", []Evaluation{{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}}, {Kind: "long_context", Status: "warning", MaxScore: 15}}, false, "full", "uncertain"},
		{"行为缺失不确定", []Evaluation{{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}}, {Kind: "behavior_probe", Status: "skipped"}}, false, "full", "uncertain"},
		{"稳定性请求失败不确定", []Evaluation{{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}}, {Kind: "stability", Status: "warning", MaxScore: 15, Evidence: map[string]any{"requestFailure": true}}}, false, "full", "uncertain"},
		{"可信对比被跳过不确定", []Evaluation{{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}}, {Kind: "comparison", Status: "skipped"}}, true, "full", "uncertain"},
		{"可信对比前缀项被忽略", []Evaluation{{Kind: "trusted_comparison.usage_shape", Status: "skipped"}, {Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}}}, true, "full", "likely"},
		{"quick 高分可能可信", []Evaluation{{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}}}, false, "quick", "likely"},
		{"quick 中分不确定", []Evaluation{{Kind: "protocol_basic", Status: "warning", Score: 5, MaxScore: 10, Evidence: map[string]any{"success": true}}}, false, "quick", "uncertain"},
		{"quick 低分可疑", []Evaluation{{Kind: "protocol_basic", Status: "failed", MaxScore: 10, Evidence: map[string]any{"success": true}}}, false, "quick", "unavailable"},
		{"空检查低分可疑", nil, false, "full", "suspicious"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			summary := SummarizeChecks(test.items, test.cmp, test.prof)
			if summary.Level != test.level {
				t.Fatalf("level=%s want=%s summary=%+v", summary.Level, test.level, summary)
			}
			if summary.MaxScore != 100 {
				t.Fatalf("MaxScore 必须归一到 100: %+v", summary)
			}
		})
	}
	t.Run("float 惩罚也计入", func(t *testing.T) {
		summary := SummarizeChecks([]Evaluation{{Kind: "juice", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"scorePenalty": 12.0}}}, false, "quick")
		// float64 形态的惩罚同样扣减：100-12=88，quick 仍落 likely 档。
		if summary.Score != 88 {
			t.Fatalf("惩罚必须按 float64 扣减: %+v", summary)
		}
	})
	t.Run("高可信需要全链路通过", func(t *testing.T) {
		items := []Evaluation{
			{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
			{Kind: "structured_output", Status: "passed", Score: 15, MaxScore: 15, Evidence: map[string]any{"success": true}},
			{Kind: "tool_calling", Status: "passed", Score: 15, MaxScore: 15, Evidence: map[string]any{"success": true}},
			{Kind: "behavior_probe", Status: "passed", Score: 35, MaxScore: 35, Evidence: map[string]any{"success": true}},
			{Kind: "long_context", Status: "passed", Score: 15, MaxScore: 15},
			{Kind: "stability", Status: "passed", Score: 15, MaxScore: 15},
			{Kind: "token_integrity", Status: "passed", Score: 10, MaxScore: 10},
			{Kind: "cross_model", Status: "passed", Score: 10, MaxScore: 10},
			{Kind: "distribution_similarity", Status: "passed", Score: 15, MaxScore: 15},
			{Kind: "comparison", Status: "passed", Score: 10, MaxScore: 10},
		}
		summary := SummarizeChecks(items, true, "full")
		if summary.Level != "high_confidence" {
			t.Fatalf("全链路通过必须高可信: %+v", summary)
		}
	})
}
