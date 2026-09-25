package main

// in-flight 预算终态释放集成验收（调用面泄漏修复）：
//
// Capture 为每个非零 body 获取进程级 in-flight lease（默认 256MB 预算），但
// 修复前全仓只有 4 处异常路径释放——正常进入主链的请求终态从不释放，预算
// 单调泄漏直至所有带 body 的 /v1 请求永久 503。修复把释放挂到
// handleOpenAIGatewayRequest 的请求终态（defer bodyReq.ReleaseInFlight）：
//
//   - 成功请求：预算在上游停留期间持有、handler 返回后归零；
//   - panic 路径：Capture 之后的 panic 展开同样经过 defer 归零；
//   - 预算回收：收窄预算到单请求体大小，连续两次请求都必须成功（修复前
//     第二次必 503 gateway_body_in_flight_limit_exceeded）。
//
// 构造路径与 chain_v1_release_guard_test.go 相同：真实链（newChainFixture +
// w1vSeedMultiGroupKey 指向 httptest 活上游 + w1vComposeChain）。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

const inflightReleaseBody = `{"model":"gpt-test","messages":[{"role":"user","content":"w1v 在途预算释放"}]}`

// inflightPanicObservability 在 request.accepted 阶段（Capture 之后、进入
// preflight 之前）注入 panic，其余调用委托内层。
type inflightPanicObservability struct {
	inner gatewaypreauth.Observability
}

func (o inflightPanicObservability) Logger() gatewaypreauth.Logger { return o.inner.Logger() }
func (o inflightPanicObservability) TraceID() string               { return o.inner.TraceID() }
func (o inflightPanicObservability) CreateTraceID() string         { return o.inner.CreateTraceID() }
func (o inflightPanicObservability) SanitizeURLForLog(value string) string {
	return o.inner.SanitizeURLForLog(value)
}

func (o inflightPanicObservability) LogRequestStage(stage string, fields map[string]any, outcome string, startedAt time.Time) {
	if stage == "request.accepted" {
		panic("inflight-release-test: request.accepted 之后的请求处理 panic")
	}
	o.inner.LogRequestStage(stage, fields, outcome, startedAt)
}

// inflightNewUpstream 起一个可阻塞的成功上游：命中时发 hit，等 proceed 关闭
// 后再写 200 响应。
func inflightNewUpstream(t *testing.T, payload string, hit chan struct{}, proceed chan struct{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit <- struct{}{}
		<-proceed
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
}

// inflightServeAsync 在独立 goroutine 投递一次 POST /v1/chat/completions，
// 不经 t.Helper 以免跨 goroutine 使用 testing 断言。
func inflightServeAsync(chain *gatewayChain, apiKeySecret, body string) <-chan inflightServeResult {
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+apiKeySecret)
	recorder := httptest.NewRecorder()
	done := make(chan inflightServeResult, 1)
	go func() {
		chain.ServeHTTP(recorder, request)
		done <- inflightServeResult{status: recorder.Code, body: recorder.Body.String()}
	}()
	return done
}

type inflightServeResult struct {
	status int
	body   string
}

// inflightAwaitResult 带超时等待异步请求完成，避免回归挂死整个测试进程。
func inflightAwaitResult(t *testing.T, done <-chan inflightServeResult) inflightServeResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(30 * time.Second):
		t.Fatal("chain 请求 30s 未返回")
		return inflightServeResult{}
	}
}

// TestV1SuccessRequestReleasesBodyInFlightBytes 断言成功请求在上游停留期间
// 持有预算、handler 返回后恰好归还：CurrentBytes/RequestCount 归零。
func TestV1SuccessRequestReleasesBodyInFlightBytes(t *testing.T) {
	hit := make(chan struct{})
	proceed := make(chan struct{})
	upstream := inflightNewUpstream(t, `{"id":"chatcmpl-inflight-release","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"在途释放"},"finish_reason":"stop"}]}`, hit, proceed)
	defer upstream.Close()

	fixture := newChainFixture(t)
	secret := w1vSeedMultiGroupKey(t, fixture, []string{"w1v_group_inflight_release"},
		map[int]bool{0: true}, map[int]string{0: upstream.URL})
	chain, shutdown := w1vComposeChain(t, fixture)
	defer shutdown()

	done := inflightServeAsync(chain, secret, inflightReleaseBody)
	select {
	case <-hit:
	case <-time.After(10 * time.Second):
		t.Fatal("上游 10s 未被命中，前置链路未走通")
	}

	// 上游停留期间预算必须处于持有态（Capture 在派发之前获取）。
	state := chain.bodyPipeline.InFlight().State(0)
	if state.CurrentBytes != len(inflightReleaseBody) || state.RequestCount != 1 {
		t.Fatalf("上游停留期间应持有 len(body)=%d 的预算: currentBytes=%d requestCount=%d",
			len(inflightReleaseBody), state.CurrentBytes, state.RequestCount)
	}

	close(proceed)
	result := inflightAwaitResult(t, done)
	if result.status != http.StatusOK {
		t.Fatalf("status=%d body=%s（成功派发是本守护的前提）", result.status, result.body)
	}

	// handler 返回即请求终态：预算必须归零——修复前该值恒为 len(body)。
	state = chain.bodyPipeline.InFlight().State(0)
	if state.CurrentBytes != 0 || state.RequestCount != 0 {
		t.Fatalf("请求终态必须释放 in-flight 预算: currentBytes=%d requestCount=%d", state.CurrentBytes, state.RequestCount)
	}
}

// TestV1PanicAfterCaptureReleasesBodyInFlightBytes 断言 Capture 之后的 panic
// 展开（上层 recover 兜底）同样经过终态 defer 归零预算。
func TestV1PanicAfterCaptureReleasesBodyInFlightBytes(t *testing.T) {
	fixture := newChainFixture(t)
	secret := fixture.apiKeySecret
	chain, shutdown := w1vComposeChain(t, fixture)
	defer shutdown()

	chain.observability = inflightPanicObservability{inner: chain.observability}

	recovered := func() (value any) {
		defer func() { value = recover() }()
		chain.ServeHTTP(httptest.NewRecorder(), func() *http.Request {
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(inflightReleaseBody))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+secret)
			return request
		}())
		return nil
	}()
	if recovered == nil {
		t.Fatal("注入的 panic 必须抵达测试层（否则链路在 Capture 之前已终止）")
	}

	state := chain.bodyPipeline.InFlight().State(0)
	if state.CurrentBytes != 0 || state.RequestCount != 0 {
		t.Fatalf("panic 展开必须释放 in-flight 预算: currentBytes=%d requestCount=%d", state.CurrentBytes, state.RequestCount)
	}
}

// TestV1BodyInFlightBudgetRecyclesAcrossRequests 直钉缺陷症状：预算收窄到
// 单个请求体大小，连续两次请求都必须成功。修复前首次请求的预算永不归还，
// 第二次在 Capture 阶段被 503 gateway_body_in_flight_limit_exceeded 拒绝。
func TestV1BodyInFlightBudgetRecyclesAcrossRequests(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-inflight-recycle","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"预算回收"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	fixture := newChainFixture(t)
	secret := w1vSeedMultiGroupKey(t, fixture, []string{"w1v_group_inflight_recycle"},
		map[int]bool{0: true}, map[int]string{0: upstream.URL})
	chain, shutdown := w1vComposeChain(t, fixture)
	defer shutdown()

	limiter := chain.bodyPipeline.InFlight()
	limiter.SetMaxBytesForTest(len(inflightReleaseBody))
	defer limiter.ClearMaxBytesForTest()

	for round := 1; round <= 2; round++ {
		status, body := w1vServeV1(t, chain, secret, inflightReleaseBody, nil)
		if status != http.StatusOK {
			t.Fatalf("第 %d 次请求 status=%d body=%s（预算未回收时本请求被 in-flight 503 拒绝）", round, status, body)
		}
		if !strings.Contains(body, "预算回收") {
			t.Fatalf("第 %d 次请求下游必须收到完整上游响应: %s", round, body)
		}
	}

	state := limiter.State(0)
	if state.CurrentBytes != 0 || state.RequestCount != 0 {
		t.Fatalf("两次请求后预算必须归零: currentBytes=%d requestCount=%d", state.CurrentBytes, state.RequestCount)
	}
}
