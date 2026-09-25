package gatewaybody

// in-flight 预算释放验收（调用面泄漏修复）：
//
//   - Capture 为每个非零 body 获取 lease 后，异常拒绝面（413 lane 上限、
//     metadata worker busy/failed）必须释放预算；
//   - lease 释放是恰好一次语义（InFlightLease.Release 的 released 标志 +
//     limiter release 的下限钳制），异常路径先释放、请求终态再释放时不得
//     多退或出现负数。
//
// 正常路径的终态释放挂点在 cmd/juhe-ai-gateway（handler 返回），由编排器
// 集成测试覆盖；这里钉住中间件内的释放与幂等语义。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// inflightLargeJSONBody 构造走 deferred metadata 扫描路径（> 256 KiB）的
// JSON 请求体，长度即 lease 字节数。
func inflightLargeJSONBody() []byte {
	return []byte(`{"model":"m"}` + strings.Repeat(" ", GatewayJSONBodyInlineMetadataScanMaxBytes))
}

func inflightStateZero(t *testing.T, limiter *InFlightLimiter, scenario string) {
	t.Helper()
	state := limiter.State(0)
	if state.CurrentBytes != 0 || state.RequestCount != 0 {
		t.Fatalf("%s: in-flight 预算必须归零，得到 currentBytes=%d requestCount=%d", scenario, state.CurrentBytes, state.RequestCount)
	}
}

// TestCaptureMetadataWorkerBusyReleasesInFlightLease 钉住 queue-full 拒绝面
// 的预算释放：Capture 已获取 lease，503 写出后预算必须真实归还（同尺寸再次
// 获取必须成功）。修复前 currentBytes 单调泄漏到 len(rawBody)。
func TestCaptureMetadataWorkerBusyReleasesInFlightLease(t *testing.T) {
	rawBody := inflightLargeJSONBody()
	release := make(chan struct{})
	busyParser := NewJSONParser(JSONParserOptions{PoolSize: 1, MaxQueuedJobs: 1})
	busyParser.parseFunc = func(context.Context, []byte) (any, error) {
		<-release
		return map[string]any{}, nil
	}
	occupied := make(chan error, 1)
	go func() {
		_, err := busyParser.ParseJSONBody(context.Background(), []byte(`1`), 10*time.Second)
		occupied <- err
	}()
	waitFor := func(cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			busyParser.mu.Lock()
			ok := cond()
			busyParser.mu.Unlock()
			if ok {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
		t.Fatal("parser never reached the expected state")
	}
	waitFor(func() bool { return busyParser.busyWorkers == 1 && len(busyParser.queue) == 0 })
	queued := make(chan error, 1)
	go func() {
		_, err := busyParser.ParseJSONBody(context.Background(), []byte(`2`), 10*time.Second)
		queued <- err
	}()
	waitFor(func() bool { return busyParser.busyWorkers == 1 && len(busyParser.queue) == 1 })

	m := NewMiddleware(Config{Parser: busyParser})
	m.metadataScanTimeout = 2 * time.Second
	m.rawBodyLimit = GatewayRawBodyHardLimitBytes
	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://gw/v1/chat/completions", nil)
	request.Header.Set("Content-Type", "application/json")
	got, err := m.Capture(rec, request, rawBody)
	close(release)
	if err != nil || got != nil {
		t.Fatalf("queue-full capture must answer without error: %v %+v", err, got)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("queue-full capture must answer 503, got %d", rec.Code)
	}
	if <-occupied != nil || <-queued != nil {
		t.Fatal("released jobs must complete cleanly")
	}
	inflightStateZero(t, m.InFlight(), "worker busy 拒绝后")

	// 归还必须真实：同尺寸预算再次获取成功，释放后仍归零。
	lease, ok := m.InFlight().TryAcquire(len(rawBody), 0)
	if !ok {
		t.Fatal("worker busy 拒绝释放后，同尺寸预算必须可再次获取")
	}
	lease.Release()
	inflightStateZero(t, m.InFlight(), "再获取并释放后")
}

// TestCaptureMetadataWorkerFailedReleasesInFlightLease 钉住 worker 扫描失败
// 拒绝面的预算释放（stopped parser 产生通用 worker failure）。
func TestCaptureMetadataWorkerFailedReleasesInFlightLease(t *testing.T) {
	failedParser := NewJSONParser(JSONParserOptions{PoolSize: 1})
	failedParser.Stop()
	m := NewMiddleware(Config{Parser: failedParser})
	m.metadataScanTimeout = time.Second
	m.rawBodyLimit = GatewayRawBodyHardLimitBytes
	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://gw/v1/chat/completions", nil)
	request.Header.Set("Content-Type", "application/json")
	got, err := m.Capture(rec, request, inflightLargeJSONBody())
	if err != nil || got != nil {
		t.Fatalf("worker-failed capture must answer without error: %v %+v", err, got)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("worker-failed capture must answer 503, got %d", rec.Code)
	}
	inflightStateZero(t, m.InFlight(), "worker failed 拒绝后")
}

// TestCaptureLaneTooLargeReleasesInFlightLease 钉住 413 lane 上限拒绝面的
// 预算释放（rejectRawBodyTooLarge → ReleaseInFlight）。
func TestCaptureLaneTooLargeReleasesInFlightLease(t *testing.T) {
	cfg := Config{}
	cfg.TextRawBodyLimitMegabytes = func() (int, bool) { return 1, true }
	m := NewMiddleware(cfg)
	payload := largeJSONPayloadWithSize(t, "gpt-4o", 4096)
	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://gw/v1/chat/completions", nil)
	request.Header.Set("Content-Type", "application/json")
	got, err := m.Capture(rec, request, []byte(payload))
	if err != nil || got != nil {
		t.Fatalf("too-large capture must answer without error: %v %+v", err, got)
	}
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("too-large capture must answer 413, got %d", rec.Code)
	}
	inflightStateZero(t, m.InFlight(), "413 lane 拒绝后")
}

// TestInFlightLeaseReleaseExactlyOnceAndClamp 钉住幂等与下限钳制：异常路径
// 已释放 + 终态再释放不得多退预算（双 lease 场景下一次多退会偷走另一请求
// 的在途字节）；limiter 对超退钳制到 0 不出现负数。
func TestInFlightLeaseReleaseExactlyOnceAndClamp(t *testing.T) {
	limiter := NewInFlightLimiter()
	first, ok := limiter.TryAcquire(100, 0)
	if !ok || first == nil {
		t.Fatalf("first acquire: ok=%v lease=%v", ok, first)
	}
	second, ok := limiter.TryAcquire(100, 0)
	if !ok || second == nil {
		t.Fatalf("second acquire: ok=%v lease=%v", ok, second)
	}

	// 同一 lease 重复释放（异常路径 + 终态 defer 场景）只退一次：另一
	// lease 的 100 字节必须原样保留。
	second.Release()
	second.Release()
	state := limiter.State(0)
	if state.CurrentBytes != 100 || state.RequestCount != 1 {
		t.Fatalf("double release 必须恰好退一次: %+v", state)
	}

	first.Release()
	first.Release()
	inflightStateZero(t, limiter, "两个 lease 释放后")

	// 下限钳制：空 limiter 上超退不得把计数打成负数。
	limiter.release(500)
	inflightStateZero(t, limiter, "空 limiter 超退钳制后")

	// Request 层终态释放重入：ReleaseInFlight 已把 Lease 置 nil，第二次
	// 调用是 no-op。
	lease, ok := limiter.TryAcquire(64, 0)
	if !ok || lease == nil {
		t.Fatalf("re-acquire: ok=%v lease=%v", ok, lease)
	}
	req := &Request{RawBody: make([]byte, 64), Lease: lease}
	req.ReleaseInFlight()
	req.ReleaseInFlight()
	inflightStateZero(t, limiter, "Request 层重复终态释放后")
}
