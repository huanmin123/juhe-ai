package gatewayobs

// w14g：网关观测族覆盖率补强（错误/防御分支直驱与 miniredis 缓存路径）。

import (
	"context"
	"net/http"
	"strings"
	"testing"

	miniredis "github.com/alicebob/miniredis/v2"
)

// ---------------------------------------------------------------------------
// redis_store.go / memory_store.go
// ---------------------------------------------------------------------------

func TestW14GGetRedisClientCache(t *testing.T) {
	server := miniredis.RunT(t)
	ctx := context.Background()
	client, err := GetRedisClient(ctx, "redis://"+server.Addr()+"/0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	// 同 URL 第二次命中缓存。
	cached, err := GetRedisClient(ctx, "redis://" + server.Addr() + "/0 ")
	if err != nil || cached != client {
		t.Fatalf("缓存命中失败 err=%v same=%v", err, cached == client)
	}
}

func TestW14GObservabilityStoreValidation(t *testing.T) {
	ctx := context.Background()
	// 非法命名空间。
	if _, err := NewRedisGatewayRoutingObservabilityStore(NewRedisCommandClient(nil), "redis://127.0.0.1:1/0", "", ""); err == nil {
		t.Fatalf("非法命名空间必须失败")
	}
	// 负 nowMs。
	store, err := NewRedisGatewayRoutingObservabilityStore(NewRedisCommandClient(nil), "redis://127.0.0.1:1/0", "w14g-ns", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordBatch(ctx, []BatchEntry{{Observation: Observation{}, Count: 1}}, -1); err == nil {
		t.Fatalf("负 nowMs 必须失败")
	}
	memory := NewMemoryGatewayRoutingObservabilityStore()
	if err := memory.RecordBatch(ctx, []BatchEntry{{Observation: Observation{}, Count: 1}}, -1); err == nil {
		t.Fatalf("内存 store 负 nowMs 必须失败")
	}
	// 惰性建连：注入 nil client 后经由 redisURL 连接 miniredis。
	server := miniredis.RunT(t)
	lazy, err := NewRedisGatewayRoutingObservabilityStore(nil, "redis://"+server.Addr()+"/0", "w14g-ns", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := lazy.RecordBatch(ctx, []BatchEntry{{Observation: Observation{Kind: "attempt", Outcome: "ok"}, Count: 1}}, 1000); err != nil {
		t.Fatalf("惰性建连写入失败: %v", err)
	}
	if _, err := lazy.Snapshot(ctx); err != nil {
		t.Fatalf("惰性建连快照失败: %v", err)
	}
	// redisHash 的 map[interface{}]interface{} 分支。
	hash := redisHash(map[interface{}]interface{}{"metric:a": "1", "recordedEvents": "2"})
	if hash["metric:a"] != "1" || hash["recordedEvents"] != "2" {
		t.Fatalf("redisHash 转换结果 = %+v", hash)
	}
}

// ---------------------------------------------------------------------------
// service.go：store 构造分支与包级单例
// ---------------------------------------------------------------------------

func TestW14GBuildRoutingObservabilityStoreBranches(t *testing.T) {
	ctx := context.Background()
	// cluster + 非 redis driver。
	if _, err := buildRoutingObservabilityStore(ctx, RuntimeDriverConfig{RuntimeMode: "cluster", RuntimeStateDriver: "memory"}); err == nil {
		t.Fatalf("cluster + memory 必须失败")
	}
	// redis driver 但缺 URL。
	if _, err := buildRoutingObservabilityStore(ctx, RuntimeDriverConfig{RuntimeMode: "cluster", RuntimeStateDriver: "redis"}); err == nil {
		t.Fatalf("缺 URL 必须失败")
	}
	// URL 非法。
	if _, err := buildRoutingObservabilityStore(ctx, RuntimeDriverConfig{RuntimeMode: "cluster", RuntimeStateDriver: "redis", RedisStateURL: "not-a-url"}); err == nil {
		t.Fatalf("非法 URL 必须失败")
	}
	// 合法 URL → Redis store。
	server := miniredis.RunT(t)
	store, err := buildRoutingObservabilityStore(ctx, RuntimeDriverConfig{
		RuntimeMode: "cluster", RuntimeStateDriver: "redis",
		RedisStateURL: "redis://" + server.Addr() + "/0", RedisNamespace: "w14g-ns",
	})
	if err != nil {
		t.Fatalf("合法构造失败: %v", err)
	}
	if _, ok := store.(*RedisGatewayRoutingObservabilityStore); !ok {
		t.Fatalf("期望 Redis store")
	}
}

func TestW14GObservabilitySingleton(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() {
		observabilitySingleton.Lock()
		observabilitySingleton.observer = nil
		observabilitySingleton.identity = ""
		observabilitySingleton.Unlock()
	})
	// identity 错误分支。
	badConfig := RuntimeDriverConfig{RuntimeMode: "cluster", RuntimeStateDriver: "memory"}
	if _, err := GetGatewayRoutingObservabilityStore(ctx, badConfig); err == nil {
		t.Fatalf("identity 错误必须失败")
	}
	if _, err := GetGatewayRoutingObservability(ctx, badConfig); err == nil {
		t.Fatalf("identity 错误必须失败")
	}
	// standalone + memory：单例创建（含 options 应用分支）。
	goodConfig := RuntimeDriverConfig{RuntimeMode: "standalone", RuntimeStateDriver: "memory"}
	observer, err := GetGatewayRoutingObservability(ctx, goodConfig, func(options *ObserverOptions) {
		options.Now = func() int64 { return 1234 }
	})
	if err != nil || observer == nil {
		t.Fatalf("单例 observer 创建失败: %v", err)
	}
	// 再次获取命中单例。
	again, err := GetGatewayRoutingObservability(ctx, goodConfig)
	if err != nil || again != observer {
		t.Fatalf("单例未命中: %v", err)
	}
	if _, err := GetGatewayRoutingObservabilityStore(ctx, goodConfig); err != nil {
		t.Fatalf("单例 store 获取失败: %v", err)
	}
}

func TestW14GObserverDefaultsAndFlushFailure(t *testing.T) {
	// Now 缺省分支。
	observer := NewObserver(ObserverOptions{Store: NewMemoryGatewayRoutingObservabilityStore()})
	if observer.now == nil {
		t.Fatalf("缺省 now 必须被填充")
	}
	// store 为 nil 时的批量写入失败日志分支。
	broken := NewObserver(ObserverOptions{})
	broken.pendingObservations["w14g"] = BatchEntry{Observation: Observation{Kind: "attempt", Outcome: "ok"}, Count: 1}
	broken.pendingOrder = []string{"w14g"}
	broken.pendingObservationNowMs = 1000
	broken.FlushPending(broken.observationGeneration)
	if len(broken.pendingObservations) != 0 {
		t.Fatalf("flush 后 pending 应清空")
	}
}

// ---------------------------------------------------------------------------
// upstreamresponsemodel.go
// ---------------------------------------------------------------------------

func TestW14GUpstreamResponseModelBranches(t *testing.T) {
	// JSON 超大块丢弃。
	observation := newTestObservation(t, false)
	observation.observeJSONChunk(make([]byte, maxJsonResponseBytes+1))
	if !observation.jsonOversized || observation.jsonChunks != nil {
		t.Fatalf("超大 JSON 块应丢弃")
	}
	// SSE 空文本与超长 pending line。
	sse := newTestObservation(t, true)
	sse.observeSseText("")
	sse.pendingLine = strings.Repeat("a", maxSseEventBytes+1)
	sse.observeSseText("x")
	if sse.pendingLine != "" {
		t.Fatalf("超长 pending line 应重置")
	}
	// gemini protocol 从 modelVersion 取模型。
	gemini := newTestObservation(t, false)
	gemini.protocol = UpstreamResponseModelProtocolGemini
	model := gemini.modelFromPayload(map[string]interface{}{"modelVersion": "gemini-x"})
	if model != "gemini-x" {
		t.Fatalf("gemini modelVersion = %q", model)
	}
	// stripOneLeadingWhitespace。
	if got := stripOneLeadingWhitespace(" a"); got != "a" {
		t.Fatalf("strip = %q", got)
	}
	if got := stripOneLeadingWhitespace(""); got != "" {
		t.Fatalf("空串 strip = %q", got)
	}
	// 协议识别：anthropic-version 头 / 非法 URL / nil headers。
	if got := UpstreamResponseModelProtocolForRequest(UpstreamResponseModelRequestInfo{
		Headers:    http.Header{"Anthropic-Version": {"2023-01-01"}},
		UpstreamURL: "http://example.com/v1",
	}); got != UpstreamResponseModelProtocolAnthropic {
		t.Fatalf("anthropic-version 识别 = %s", got)
	}
	if _, _, ok := parseUpstreamURLParts("http:///no-host"); ok {
		t.Fatalf("无主机名必须非法")
	}
	if hasUpstreamHeader(nil, "x-goog-api-key") {
		t.Fatalf("nil headers 必须返回 false")
	}
	// publishing reader：publish 为 nil 时 Close 静默。
	reader := &publishingObservedBodyReader{inner: strings.NewReader("x")}
	if err := reader.Close(); err != nil {
		t.Fatalf("nil publish Close = %v", err)
	}
	// utf8 解码器：不完整序列挂起 + 非法字节替换。
	decoder := &utf8StringDecoder{}
	if out := decoder.write([]byte("ok")); out != "ok" {
		t.Fatalf("正常写入 = %q", out)
	}
	if out := decoder.write([]byte{0xE4, 0xB8}); out != "" {
		t.Fatalf("不完整序列应挂起 = %q", out)
	}
	if out := decoder.write([]byte{0xAD, 0x21}); out != "\u4e2d!" {
		t.Fatalf("续写后输出 = %q", out)
	}
	broken := &utf8StringDecoder{}
	if out := broken.write([]byte{0xFF}); out != "\uFFFD" {
		t.Fatalf("非法字节应输出替换符 = %q", out)
	}
}

func newTestObservation(t *testing.T, sse bool) *UpstreamResponseModelObservation {
	t.Helper()
	return &UpstreamResponseModelObservation{protocol: UpstreamResponseModelProtocolOpenAI, sse: sse}
}

// ---------------------------------------------------------------------------
// diagnosticresponsecontext.go / diagnosticsanitizer.go / dispatchsummary.go
// ---------------------------------------------------------------------------

func TestW14GDiagnosticContextAndSanitizerBranches(t *testing.T) {
	// 未知类型。
	if got := DiagnosticResponseContextOf(42); got.BodyText != "" {
		t.Fatalf("未知类型应返回空 context: %+v", got)
	}
	// 非 valid parsed body。
	invalid := &NonStreamJSONBodyView{Status: "invalid"}
	ctx := DiagnosticResponseContextFromGatewayNonStream("body", invalid, DiagnosticResponseParseOptions{})
	if ctx.BodyText != "body" || ctx.JSON != nil {
		t.Fatalf("invalid body 处理结果 = %+v", ctx)
	}
	// 引号敏感赋值清洗的防御分支。
	cases := []string{
		`'password`,                 // 未闭合引号
		`'password' ` + "`x",        // 缺冒号
		`'password':`,               // 冒号后结束
		`'password': 42`,            // 值非引号
		`'password': 'a\` + "\n" + `b'`, // 转义跨行
	}
	for index, input := range cases {
		if _, _, matched := matchQuotedSensitiveAssignment(input, 0); matched {
			t.Fatalf("case %d 不应匹配: %q", index, input)
		}
	}
	// nil 请求上下文。
	if DispatchSummaryHolderFromContext(nil) != nil {
		t.Fatalf("nil ctx 必须返回 nil holder")
	}
}
