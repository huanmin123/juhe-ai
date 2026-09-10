package gatewaydispatch

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// modelVersionCaptureBody 在原始上游体之上做测试用观察：透传字节并累积，
// 干净 EOF 时从原始载荷提取 modelVersion 经 publish 发布（测试本地的最小
// 观察器，协议语义归 gatewayobs 的既有测试覆盖）。
type modelVersionCaptureBody struct {
	inner        io.ReadCloser
	publish      func(model string)
	seen         strings.Builder
	publishFired bool
}

var modelVersionPattern = regexp.MustCompile(`"modelVersion":"([^"]+)"`)

func (b *modelVersionCaptureBody) Read(p []byte) (int, error) {
	n, err := b.inner.Read(p)
	if n > 0 {
		b.seen.Write(p[:n])
	}
	if err == io.EOF {
		if match := modelVersionPattern.FindStringSubmatch(b.seen.String()); match != nil {
			b.publish(match[1])
		}
	}
	return n, err
}

func (b *modelVersionCaptureBody) Close() error { return b.inner.Close() }

// rawCapturingTransformer 记录转换器看到的原始体（钩子之后、转换之前的
// 字节），并把它改写为客户端协议形态。
type rawCapturingTransformer struct {
	rawSeen    string
	clientBody string
}

func (t *rawCapturingTransformer) TransformUpstreamResponseForAccount(input UpstreamResponseTransformInput) (*GatewayUpstreamResponse, error) {
	body, err := io.ReadAll(input.Response.Body)
	if err != nil {
		return nil, err
	}
	_ = input.Response.Body.Close()
	t.rawSeen = string(body)
	header := input.Response.Header.Clone()
	return NewGatewayUpstreamResponseForTransform(input.Response.Status(), header,
		io.NopCloser(strings.NewReader(t.clientBody))), nil
}

// TestPerformUpstreamRequestAttemptObservesModelBeforeTransform 钉住 P2 核心
// 语义：模型观察挂在 fetch 之后、桥转换之前——转换器看到的是原始上游载荷
// （含 modelVersion），发布值进入本尝试的 slot，转换后的客户端形态不参与
// 归因（Node upstream-attempts.ts:180-210 观察先于 transform）。
func TestPerformUpstreamRequestAttemptObservesModelBeforeTransform(t *testing.T) {
	var hookInfo UpstreamResponseModelObservationInfo
	hookCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"modelVersion\":\"gemini-upstream-1\"}\n\n")
	}))
	defer server.Close()

	engine := NewEngine(nil, nil)
	engine.ObserveUpstreamResponseModel = func(response *GatewayUpstreamResponse, info UpstreamResponseModelObservationInfo, publish func(model string)) {
		hookCalls++
		hookInfo = info
		response.Body = &modelVersionCaptureBody{inner: response.Body, publish: publish}
	}
	transformer := &rawCapturingTransformer{clientBody: `data: {"type":"response.created","response":{"model":"client-shape"}}`}
	engine.ResponseTransformer = transformer

	request := httptest.NewRequest(http.MethodPost, "http://gw.test/v1/responses",
		strings.NewReader(`{"model":"m","stream":true}`))
	slot := &UpstreamResponseModelSlot{}
	response, err := engine.PerformUpstreamRequestAttempt(context.Background(), AttemptInput{
		Req:                       gatewaypreauth.NewGatewayRequest(request),
		Account:                   AccountCandidate{ID: "acc_gem", ProviderCode: "hybrid", ProtocolCode: "gemini"},
		UpstreamURL:               server.URL + "/v1/responses",
		Headers:                   http.Header{},
		Body:                      []byte(`{"model":"m","stream":true}`),
		Signal:                    context.Background(),
		TimeoutProfile:            gatewayrouting.GatewayTimeoutProfile{TimeoutsDisabled: true},
		UpstreamResponseModelSlot: slot,
	})
	if err != nil {
		t.Fatalf("attempt: %v", err)
	}
	if hookCalls != 1 {
		t.Fatalf("观察钩子必须恰好挂载一次, got %d", hookCalls)
	}
	if !strings.Contains(transformer.rawSeen, `"modelVersion":"gemini-upstream-1"`) {
		t.Fatalf("转换器必须看到原始上游载荷（观察先于转换）, got %q", transformer.rawSeen)
	}
	if slot.Get() != "gemini-upstream-1" {
		t.Fatalf("slot 必须归因上游原始模型, got %q", slot.Get())
	}
	if hookInfo.ProtocolCode != "gemini" {
		t.Fatalf("观察 info 必须携带上游账户协议, got %q", hookInfo.ProtocolCode)
	}
	if !hookInfo.SSE {
		t.Fatalf("流式请求的观察 info.SSE 必须为 true")
	}
	if hookInfo.UpstreamURL != server.URL+"/v1/responses" {
		t.Fatalf("观察 info 必须携带上游 URL, got %q", hookInfo.UpstreamURL)
	}
	transformed, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read transformed body: %v", err)
	}
	if !strings.Contains(string(transformed), `"client-shape"`) {
		t.Fatalf("转换后的响应体必须是客户端形态, got %q", string(transformed))
	}
}

// TestPerformUpstreamRequestAttemptWithoutSlotSkipsObservation 钉住 nil slot
// 的缺席语义：调用方不消费归因时不挂观察，响应体原样透传。
func TestPerformUpstreamRequestAttemptWithoutSlotSkipsObservation(t *testing.T) {
	hookCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()

	engine := NewEngine(nil, nil)
	engine.ObserveUpstreamResponseModel = func(response *GatewayUpstreamResponse, info UpstreamResponseModelObservationInfo, publish func(model string)) {
		hookCalls++
	}

	request := httptest.NewRequest(http.MethodPost, "http://gw.test/v1/chat/completions",
		strings.NewReader(`{"model":"m"}`))
	response, err := engine.PerformUpstreamRequestAttempt(context.Background(), AttemptInput{
		Req:            gatewaypreauth.NewGatewayRequest(request),
		Account:        AccountCandidate{ID: "acc_openai", ProviderCode: "gpt", ProtocolCode: "openai"},
		UpstreamURL:    server.URL + "/v1/chat/completions",
		Headers:        http.Header{},
		Body:           []byte(`{"model":"m"}`),
		Signal:         context.Background(),
		TimeoutProfile: gatewayrouting.GatewayTimeoutProfile{TimeoutsDisabled: true},
	})
	if err != nil {
		t.Fatalf("attempt: %v", err)
	}
	if hookCalls != 0 {
		t.Fatalf("nil slot 不得触发观察钩子, got %d", hookCalls)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("无观察时响应体必须原样透传, got %q", string(body))
	}
}

// TestUpstreamResponseModelSlotPublishBindOrdering 钉住 slot 的发布/绑定
// 时序契约：先发布后绑定要立即补发，先绑定后发布要同步转发，nil 接收方
// 全程安全。
func TestUpstreamResponseModelSlotPublishBindOrdering(t *testing.T) {
	var boundModel string
	slot := &UpstreamResponseModelSlot{}
	slot.Set("m1")
	slot.Bind(func(model string) { boundModel = model })
	if boundModel != "m1" {
		t.Fatalf("先发布后绑定必须立即补发, got %q", boundModel)
	}
	slot.Set("m2")
	if boundModel != "m2" {
		t.Fatalf("绑定后的发布必须同步转发, got %q", boundModel)
	}
	if slot.Get() != "m2" {
		t.Fatalf("Get 必须返回最新发布值, got %q", slot.Get())
	}
	var nilSlot *UpstreamResponseModelSlot
	nilSlot.Set("m3") // 不得 panic
	if nilSlot.Get() != "" {
		t.Fatalf("nil slot Get 必须为空, got %q", nilSlot.Get())
	}
	nilSlot.Bind(func(string) {}) // 不得 panic
}
