package accountprobe

// w12h 补充 arms：分级重试/池诊断的取消与升级路径、executeAttempt 前置错误、
// 请求头捕获（oauth/codex/流式）、读循环边界、分类矩阵与协议载荷提取。
// 本文件补充登记的不可达语句：
//   - probe.go executeAttempt 的 NewRequestWithContext 错误（413）：URL 已由
//     buildUpstreamURL 校验，构造不会失败；
//   - probe.go probePoolDetailed 的“没有返回结果”（324）：ctx 取消在循环入口
//     先行返回，无法构造空结果到达；
//   - probe.go NewService 的 SharedClient 错误（122）：空代理 URL 的共享客户端
//     构造不会失败；
//   - protocol.go orderedJSON 的 key 序列化错误（175）：key 恒为字符串；
//     cwdOrEmpty 的 Getwd 错误（327）：测试进程工作目录恒有效。

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountquality"
)

var w12hErrReader = errors.New("w12h 注入读取错误")

// 取消的 ctx 直接命中 probeFixedKey / probePoolDetailed 的取消入口。
func TestW12HProbeCancellationArms(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"juhe"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	service := w7cService(t, &fakeSource{}, nil, 0)
	view := probeView(server.URL)
	ctx := context.Background()
	canceled, cancel := context.WithCancel(ctx)
	cancel()

	entry := &view.APIKeyEntries[0]
	if observation, err := service.probeFixedKey(canceled, view, entry, false); err == nil || observation != nil {
		t.Fatalf("取消的 fixed-key 必须立即返回: %+v %v", observation, err)
	}
	emptyView := probeView(server.URL)
	emptyView.APIKeyEntries = nil
	if _, _, err := service.probePoolDetailed(ctx, emptyView, false); err == nil || !strings.Contains(err.Error(), "缺少可用 API Key") {
		t.Fatalf("空池必须报错: %v", err)
	}
	if _, _, err := service.probePoolDetailed(canceled, view, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的池诊断必须返回取消: %v", err)
	}
}

// 单 Key 分级：超时且已触达上游时升级到下一阶段。
func TestW12HAttemptStagedEscalation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"juhe"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	service := w7cService(t, &fakeSource{}, []time.Duration{60 * time.Millisecond, 5 * time.Second}, 0)
	view := probeView(server.URL)
	entry := &view.APIKeyEntries[0]
	observation := service.attemptStaged(context.Background(), view, entry)
	if observation == nil || !observation.Result.Success {
		t.Fatalf("升级后第二轮应成功: %+v", observation)
	}
}

// executeAttempt 的前置解析错误矩阵。
func TestW12HExecuteAttemptArms(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	service := w7cService(t, &fakeSource{}, nil, 0)
	ctx := context.Background()

	// 无可用请求形态。
	noModes := probeView(server.URL)
	noModes.NormalizeEndpointModes = map[EndpointMode]bool{}
	noModes.HealthCheckEndpointMode = ""
	if _, err := service.executeAttempt(ctx, noModes, nil, time.Second, false); err == nil {
		t.Fatal("无请求形态必须失败")
	}
	// Base URL 缺失 → buildUpstreamURL 失败。
	noBase := probeView("")
	if _, err := service.executeAttempt(ctx, noBase, nil, time.Second, false); err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("缺失 base_url 必须失败: %v", err)
	}
	// 凭据缺失。
	noKey := probeView(server.URL)
	noKey.SelectedAPIKey = ""
	entry := &KeyEntry{Key: "   "}
	if _, err := service.executeAttempt(ctx, noKey, entry, time.Second, false); err == nil || !strings.Contains(err.Error(), "缺少可用凭据") {
		t.Fatalf("凭据缺失必须失败: %v", err)
	}
}

// 请求头捕获：oauth 账户头、codex 兼容头、流式 accept。
func TestW12HProbeHeaderArms(t *testing.T) {
	var seen map[string]string
	var seenPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = map[string]string{}
		for name := range r.Header {
			seen[strings.ToLower(name)] = r.Header.Get(name)
		}
		seenPath = r.URL.Path
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"juhe"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	ctx := context.Background()

	// oauth 账户头（account_id 凭据 + 默认 profile）。
	oauthView := probeView(server.URL)
	oauthView.Type = "oauth"
	oauthView.ProviderProtocolProfileID = ""
	oauthView.Credentials = map[string]any{"account_id": "w12h-acct-id"}
	oauthView.SelectedAPIKey = "oauth-token"
	oauthView.APIKeyEntries = nil
	service := w7cService(t, &fakeSource{view: oauthView}, nil, 0)
	if _, err := service.Probe(ctx, accountquality.ProbeRequest{AccountID: oauthView.AccountID}); err != nil {
		t.Fatal(err)
	}
	if seen["chatgpt-account-id"] != "w12h-acct-id" {
		t.Fatalf("chatgpt-account-id 未携带: %v", seen)
	}

	// codex_responses 兼容头（responses 模式）。
	codexView := probeView(server.URL)
	codexView.ClientCompatibility = "codex_responses"
	codexView.HealthCheckEndpointMode = string(ModeResponsesJSON)
	codexService := w7cService(t, &fakeSource{view: codexView}, nil, 0)
	if _, err := codexService.Probe(ctx, accountquality.ProbeRequest{AccountID: codexView.AccountID}); err != nil {
		t.Fatal(err)
	}
	if seen["originator"] != "Codex Desktop" || seen["session-id"] == "" || seen["x-codex-window-id"] == "" {
		t.Fatalf("codex 头未携带: %v (path=%s)", seen, seenPath)
	}

	// oauth 流式 accept 头。
	oauthStream := probeView(server.URL)
	oauthStream.Type = "oauth"
	oauthStream.ProviderProtocolProfileID = ""
	oauthStream.HealthCheckEndpointMode = string(ModeChatSSE)
	oauthStream.APIKeyEntries = nil
	oauthStream.SelectedAPIKey = "oauth-token"
	streamService := w7cService(t, &fakeSource{view: oauthStream}, nil, 0)
	if _, err := streamService.Probe(ctx, accountquality.ProbeRequest{AccountID: oauthStream.AccountID}); err != nil {
		t.Fatal(err)
	}
	if seen["accept"] != "text/event-stream" {
		t.Fatalf("oauth 流式 accept 不符: %v", seen)
	}

	// 代理指向拒绝端口 → 传输失败分类。
	proxyView := probeView("http://w12h-unreachable.invalid")
	proxyView.ProxyURL = "http://127.0.0.1:1"
	proxyService := w7cService(t, &fakeSource{view: proxyView}, nil, 0)
	observation, err := proxyService.Probe(ctx, accountquality.ProbeRequest{AccountID: proxyView.AccountID})
	if err != nil || observation == nil || observation.Result.Success {
		t.Fatalf("代理拒连必须给出失败观测: %+v %v", observation, err)
	}
}

// w12hRawResponse 模拟底层传输：自定义状态、响应体与头部。
type w12hRawTransport struct {
	status  int
	body    io.ReadCloser
	headers http.Header
}

func (t *w12hRawTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode:    t.status,
		Body:          t.body,
		Header:        t.headers,
		ContentLength: -1,
		Request:       request,
	}, nil
}

// 读循环边界：超大响应截断、取消中断、零字节读取。
func TestW12HReadLoopArms(t *testing.T) {
	build := func(body io.ReadCloser) *Service {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"juhe"},"finish_reason":"stop"}]}`))
		}))
		t.Cleanup(server.Close)
		client := &http.Client{Transport: &w12hRawTransport{status: 200, body: body, headers: http.Header{}}}
		service, err := NewService(Options{Source: &fakeSource{}, Secret: "s", Client: client, Now: testNowNow})
		if err != nil {
			t.Fatal(err)
		}
		return service
	}
	view := func() *View {
		v := probeView("https://w12h-upstream.invalid")
		return v
	}
	ctx := context.Background()

	// 超过 256KB 的响应体在截断后仍按协议分类（视为失败但必须返回）。
	big := build(io.NopCloser(strings.NewReader(strings.Repeat("x", 300*1024))))
	observation := big.attemptStaged(ctx, view(), &view().APIKeyEntries[0])
	if observation == nil {
		t.Fatal("超大响应必须返回观测")
	}

	// 读取器返回 (0, nil)：循环零字节跳出。
	zero := build(io.NopCloser(&w12hZeroReader{}))
	if observation := zero.attemptStaged(ctx, view(), &view().APIKeyEntries[0]); observation == nil {
		t.Fatal("零字节读取必须返回观测")
	}

	// 响应体带尾部读取错误但协议完整：语义成功保留。
	combined := io.MultiReader(strings.NewReader(`{"choices":[{"message":{"content":"juhe"},"finish_reason":"stop"}]}`), &w12hFailReader{})
	withErr := build(io.NopCloser(combined))
	observation = withErr.attemptStaged(ctx, view(), &view().APIKeyEntries[0])
	if observation == nil || !observation.Result.Success {
		t.Fatalf("尾部读取错误不得覆盖语义成功: %+v", observation)
	}
}

type w12hZeroReader struct{}

func (r *w12hZeroReader) Read([]byte) (int, error) { return 0, nil }

type w12hFailReader struct{}

func (r *w12hFailReader) Read([]byte) (int, error) { return 0, w12hErrReader }

// 分类矩阵：挑战不匹配、图片证据、受限脱敏与额度头。
func TestW12HClassifyResponseArms(t *testing.T) {
	view := probeView("https://w12h-upstream.invalid")
	challenge := CreateOutputChallenge()
	protocol := DiagnosticProtocol("openai")

	// 协议完成但输出不含挑战令牌 → invalid_probe_output。
	observation := classifyResponse(view, protocol, ModeChatJSON,
		`{"choices":[{"message":{"content":"hello world"},"finish_reason":"stop"}]}`,
		map[string]string{}, 200, 1, 2, challenge, false)
	if observation.Result.ErrorCode != "invalid_probe_output" {
		t.Fatalf("挑战不匹配必须报 invalid_probe_output: %+v", observation.Result)
	}

	// 完整成功。
	observation = classifyResponse(view, protocol, ModeChatJSON,
		`{"choices":[{"message":{"content":"juhe `+challenge.ExpectedOutput+`"},"finish_reason":"stop"}]}`,
		map[string]string{}, 200, 1, 2, challenge, false)
	if !observation.Result.Success {
		t.Fatalf("挑战匹配必须成功: %+v", observation.Result)
	}

	// 图片模式：无图片证据 / 错误信封。
	images := classifyResponse(view, protocol, ModeImagesJSON, `{"created":0}`,
		nil, 200, 0, 0, challenge, false)
	if images.Result.ErrorCode != "invalid_protocol_success_response" {
		t.Fatalf("图片缺证据错误码不符: %+v", images.Result)
	}
	imagesErr := classifyResponse(view, protocol, ModeImagesJSON, `{"error":{"code":"bad_image","message":"图片失败"}}`,
		nil, 200, 0, 0, challenge, false)
	if imagesErr.Result.ErrorCode != "bad_image" {
		t.Fatalf("图片错误信封错误码不符: %+v", imagesErr.Result)
	}

	// 有限模式下的额度规则命中：响应头收敛为额度头。
	limited := classifyResponse(view, protocol, ModeChatJSON,
		`{"error":{"code":"insufficient_quota","message":"You exceeded your current quota, please check your plan and billing details in"},"finish_reason":null}`,
		map[string]string{"x-request-id": "w12h"}, 429, 0, 0, challenge, true)
	if limited.Result.Success || limited.Result.ResponseHeaders == nil {
		t.Fatalf("受限模式必须收敛响应头: %+v", limited.Result)
	}

	// 消息缺省回退到 HTTP 状态码。
	fallback := classifyResponse(view, protocol, ModeChatJSON, ``, nil, 502, 0, 0, challenge, false)
	if !strings.Contains(fallback.Result.Message, "502") {
		t.Fatalf("消息必须回退 HTTP 状态: %+v", fallback.Result)
	}

	// limited 的错误分类路径。
	classified := classifyAttemptError(view, errors.New("boom"), time.Second, true, 1)
	if classified.Result.Success || classified.Result.Message == "" {
		t.Fatalf("受限错误分类必须脱敏: %+v", classified.Result)
	}

	// 空值回退与图片信封遍历。
	if firstNonEmpty("", "") != "" {
		t.Fatal("firstNonEmpty 空值必须为空")
	}
	if hasImagesSuccessEvidence(parseResponseContext(`{"data":["not-object"]}`)) {
		t.Fatal("非对象图片条目不得算作证据")
	}
}

// 协议载荷提取矩阵。
func TestW12HClassifyPayloadArms(t *testing.T) {
	// BOM 前缀与 SSE 注释行。
	context := parseResponseContext("\ufeff" + `{"choices":[{"delta":{"content":"juhe"}}]}`)
	if !looksLikeSSE(": keep-alive\n\n") {
		t.Fatal("注释行必须识别为 SSE")
	}
	if context.record == nil {
		t.Fatal("BOM 前缀的 JSON 必须可解析")
	}
	// SSE 注释行在事件解析中被跳过。
	sseContext := parseResponseContext(": ping\nevent: response.failed\n\n")
	if _, ok := extractRawVisibleOutputText(sseContext, DiagnosticProtocol("openai")); ok {
		t.Fatal("失败事件不得提取可见输出")
	}
	// responses 事件：delta/done/completed 与空 json 事件。
	responsesSSE := "event: response.output_text.delta\n" +
		`data: {"delta":"juhe"}` + "\n\n" +
		"event: response.output_text.done\n" +
		`data: {"text":"juhe-out"}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"response":{"output":[{"content":[{"type":"text","text":"juhe"}]}]}}` + "\n\n"
	if text, ok := extractRawVisibleOutputText(parseResponseContext(responsesSSE), DiagnosticProtocol("openai")); !ok || !strings.Contains(text, "juhe") {
		t.Fatalf("responses 可见输出提取失败: %q %t", text, ok)
	}
	// chat completions choices 遍历：空容器跳过。
	if _, ok := extractRawVisibleOutputText(parseResponseContext(`{"choices":[{"finish_reason":"stop"}]}`), DiagnosticProtocol("openai")); ok {
		t.Fatal("无消息内容不得提取")
	}
	// gemini 内容：parts 数组与 thought 跳过。
	gemini := parseResponseContext(`{"candidates":[{"content":{"parts":[{"text":"juhe"},{"thought":true,"text":"hidden"}]},"finishReason":"STOP"}]}`)
	if !hasCompletedGeminiPayload(gemini.record) {
		t.Fatal("gemini 完成载荷必须识别")
	}
	if texts := geminiVisibleContentTexts(gemini.record["candidates"].([]any)[0].(map[string]any)["content"].(map[string]any)); len(texts) != 1 {
		t.Fatalf("thought 部分必须跳过: %v", texts)
	}
	if geminiVisibleContentTexts(nil) != nil {
		t.Fatal("nil record 必须返回 nil")
	}
	// chat completions 内容存在性：无 delta/message 内容返回 false。
	if hasChatContentPayload(nil) {
		t.Fatal("nil 载荷必须返回 false")
	}
	if hasChatContentPayload(map[string]any{"choices": []any{map[string]any{}}}) {
		t.Fatal("空 choices 不得算作内容")
	}
	// interactions：空 json 事件与非完成载荷。
	if hasCompletedInteractionsPayload(nil) {
		t.Fatal("nil 载荷必须返回 false")
	}
	if hasCompletedInteractionsPayload(map[string]any{"type": "interactions.response.completed"}) {
		t.Fatal("缺少 response 不得算作完成")
	}
}

// 协议构造与工具函数。
func TestW12HProtocolHelperArms(t *testing.T) {
	// orderedJSON：字段序列化错误传播。
	_, err := orderedJSON([]orderedField{{key: "k", marshal: func() ([]byte, error) { return nil, w12hErrReader }}})
	if err == nil {
		t.Fatal("字段序列化失败必须传播")
	}
	// marshalValue 对不可序列化值 panic。
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("不可序列化值必须 panic")
			}
		}()
		marshalValue(make(chan int))
	}()
	// 大小写无关前缀匹配。
	if !equalFoldPrefix("JUHE-PROBE", "juhe-") {
		t.Fatal("大小写无关前缀必须匹配")
	}
	if equalFoldPrefix("juXe", "juhe") {
		t.Fatal("不同内容不得匹配")
	}
	if _, err := buildUpstreamURL(&View{BaseURL: ""}, "/v1/x"); err == nil {
		t.Fatal("缺失 base_url 必须失败")
	}
	if _, _, _, err := resolveProbeView(nil); err == nil {
		t.Fatal("nil 视图必须失败")
	}
}
