package gatewayusage

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// wjNoopReplay 是空回放消费者。
type wjNoopReplay struct{}

func (wjNoopReplay) Replay(Ctx, UsageRecordInput) error { return nil }

// wjMockStageLogger 记录阶段日志调用。
type wjMockStageLogger struct {
	mu      sync.Mutex
	entries []string
}

func (l *wjMockStageLogger) LogRequestStage(stage string, _ map[string]any, outcome string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, stage+"|"+outcome)
}

func (l *wjMockStageLogger) has(suffix string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, entry := range l.entries {
		if strings.HasSuffix(entry, suffix) {
			return true
		}
	}
	return false
}

// wjMockHTTPCompletion 是可控的 HTTP 完成观察者。
type wjMockHTTPCompletion struct {
	mu        sync.Mutex
	listener  func(completedAtMs int64)
	completed bool
	atMs      int64
}

func (m *wjMockHTTPCompletion) CompletedAtMs() (int64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.atMs, m.completed
}

func (m *wjMockHTTPCompletion) OnCompleted(listener func(completedAtMs int64)) func() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listener = listener
	return func() { m.mu.Lock(); m.listener = nil; m.mu.Unlock() }
}

func (m *wjMockHTTPCompletion) fire(atMs int64) {
	m.mu.Lock()
	listener := m.listener
	m.mu.Unlock()
	if listener != nil {
		listener(atMs)
	}
}

// wjCaptureSettings 构造启用且全量捕获的设置。
func wjCaptureSettings(hotRetentionHours int, sampleRate float64) AuditLogSettings {
	return AuditLogSettings{
		Enabled:                   true,
		FullBodyCaptureEnabled:    true,
		SuccessSampleRate:         sampleRate,
		ActiveCaptureMaxBytes:     DefaultAuditCaptureHardLimitBytes,
		SuccessHotRetentionHours:  hotRetentionHours,
		SuccessFullBodyLimitBytes: 1024,
		ProblemFullBodyLimitBytes: 2048,
	}
}

// TestWJCaptureSuccessPayloadDecision 固定成功负载捕获的判定契约：热保留、
// 采样命中或已有失败尝试三者其一即捕获。
func TestWJCaptureSuccessPayloadDecision(t *testing.T) {
	// 热保留开启 → 捕获（构造时即抓取 client request payload）。
	hot := NewAuditCaptureContext(AuditCaptureInput{
		TraceID: "trace-hot", StartedAtMs: 1700000000000, TrafficSource: TrafficSourceGateway,
		RawBody:  []byte(`{"prompt":"hi"}`),
		Settings: FixedAuditLogSettingsSource{Settings: wjCaptureSettings(24, 0)},
	})
	if !hot.ShouldCaptureSuccessPayloads() {
		t.Fatal("热保留开启必须捕获成功负载")
	}
	if hot.AuditLogID() == "" || !strings.HasPrefix(hot.AuditLogID(), "audit_") {
		t.Fatalf("AuditLogID 格式不符: %q", hot.AuditLogID())
	}
	hot.Cancel()

	// 采样率 1 → 必选中 → 捕获。
	selected := NewAuditCaptureContext(AuditCaptureInput{
		TraceID: "trace-selected", StartedAtMs: 1700000000000, TrafficSource: TrafficSourceGateway,
		Settings: FixedAuditLogSettingsSource{Settings: wjCaptureSettings(0, 1)},
	})
	if !selected.ShouldCaptureSuccessPayloads() {
		t.Fatal("采样必命中必须捕获成功负载")
	}
	selected.Cancel()

	// 全关 → 不捕获；失败尝试后转为捕获。
	plain := NewAuditCaptureContext(AuditCaptureInput{
		TraceID: "trace-plain", StartedAtMs: 1700000000000, TrafficSource: TrafficSourceGateway,
		Settings: FixedAuditLogSettingsSource{Settings: wjCaptureSettings(0, 0)},
	})
	if plain.ShouldCaptureSuccessPayloads() {
		t.Fatal("热保留关闭且采样不中必须不捕获")
	}
	plain.RecordFailedDispatchAttempt(RecordFailedDispatchAttemptInput{
		AttemptIndex: 1, StartedAtMs: 1700000000001,
		ErrorPhase: "upstream_request", ErrorCode: "connect_timeout", ErrorMessage: "连接超时",
	})
	if !plain.ShouldCaptureSuccessPayloads() {
		t.Fatal("出现失败尝试后必须捕获成功负载")
	}
	// 失败尝试根因必须能被回溯（跳过 downstream 与无错误信息条目）。
	root := plain.LatestFailedAttemptRoot()
	if root == nil || root.ErrorCode != "connect_timeout" || root.ErrorPhase != "upstream_request" {
		t.Fatalf("失败根因不符: %+v", root)
	}
	plain.Cancel()
}

// TestWJCaptureClientRequestPayloadContract 固定客户端请求负载的抓取契约：
// 常规 body 计算 sha256 与原始大小；超大 body 触发溢出标记。
func TestWJCaptureClientRequestPayloadContract(t *testing.T) {
	dispatcher := &recordingAuditDispatcher{}
	capture := captureInput(dispatcher, func(input *AuditCaptureInput) {
		input.RawBody = []byte(`{"prompt":"hi"}`)
		input.Settings = FixedAuditLogSettingsSource{Settings: wjCaptureSettings(24, 0)}
	})
	defer capture.Cancel()
	// 构造时已抓取 payload；Finalize 成功请求后必须带 request part。
	statusOK := 200
	capture.Finalize(FinalizeAuditInput{Outcome: AuditOutcomeSuccess, Success: true, StatusCode: &statusOK})
	logs := dispatcher.all()
	if len(logs) != 1 {
		t.Fatalf("必须投递 1 条审计: %d", len(logs))
	}
	found := false
	for _, payload := range logs[0].Payloads {
		if payload.PartType == AuditPartClientRequest {
			found = true
			if payload.BodySha256 == "" || payload.RawBodySizeBytes == nil || *payload.RawBodySizeBytes != len(`{"prompt":"hi"}`) {
				t.Fatalf("请求负载摘要字段不符: %+v", payload)
			}
		}
	}
	if !found {
		t.Fatal("必须包含客户端请求负载")
	}
}

// TestWJCaptureOverflowMarksPayloads 固定超大 body 的溢出语义：超过活动
// 捕获上限时标记溢出且不保存 body。
func TestWJCaptureOverflowMarksPayloads(t *testing.T) {
	dispatcher := &recordingAuditDispatcher{}
	settings := wjCaptureSettings(24, 0)
	settings.ActiveCaptureMaxBytes = 16
	capture := NewAuditCaptureContext(AuditCaptureInput{
		TraceID: "trace-overflow", StartedAtMs: 1700000000000, TrafficSource: TrafficSourceGateway,
		RawBody:    []byte(strings.Repeat("x", 64)),
		Settings:   FixedAuditLogSettingsSource{Settings: settings},
		Dispatcher: dispatcher,
	})
	defer capture.Cancel()
	statusOK := 200
	capture.Finalize(FinalizeAuditInput{Outcome: AuditOutcomeSuccess, Success: true, StatusCode: &statusOK})
	logs := dispatcher.all()
	if len(logs) != 1 {
		t.Fatalf("必须投递 1 条审计: %d", len(logs))
	}
	if logs[0].CaptureStatus != "overflow" {
		t.Fatalf("溢出状态不符: %q", logs[0].CaptureStatus)
	}
}

// TestWJCaptureHTTPCompletionWiring 固定 HTTP 完成观察者接线：Finalize 等
// 待完成事件，事件到达后补齐 HTTPCompletedAt 并投递。
func TestWJCaptureHTTPCompletionWiring(t *testing.T) {
	dispatcher := &recordingAuditDispatcher{}
	stage := &wjMockStageLogger{}
	observer := &wjMockHTTPCompletion{}
	capture := captureInput(dispatcher, func(input *AuditCaptureInput) {
		input.HTTPCompletion = observer
		input.StageLogger = stage
	})
	defer capture.Cancel()
	statusOK := 200
	capture.Finalize(FinalizeAuditInput{Outcome: AuditOutcomeSuccess, Success: true, StatusCode: &statusOK})
	if len(dispatcher.all()) != 0 {
		t.Fatal("HTTP 未完成不得提前投递")
	}
	observer.fire(1700000000500)
	logs := dispatcher.all()
	if len(logs) != 1 {
		t.Fatalf("完成事件后必须投递: %d", len(logs))
	}
	if logs[0].HTTPCompletedAt == "" || logs[0].HTTPDurationMs == nil {
		t.Fatalf("HTTP 完成字段不符: %+v", logs[0])
	}
	if !stage.has("audit.finalize|success") {
		t.Fatalf("必须记录成功阶段日志: %v", stage.entries)
	}
}

// TestWJCaptureStageLoggerFailurePath 固定失败请求的阶段日志分支：
// expected_failure 日志必须携带决策输入。
func TestWJCaptureStageLoggerFailurePath(t *testing.T) {
	dispatcher := &recordingAuditDispatcher{}
	stage := &wjMockStageLogger{}
	capture := captureInput(dispatcher, func(input *AuditCaptureInput) {
		input.StageLogger = stage
		input.Settings = FixedAuditLogSettingsSource{Settings: wjCaptureSettings(24, 0)}
	})
	defer capture.Cancel()
	status := 502
	capture.Finalize(FinalizeAuditInput{
		Outcome: AuditOutcomeUpstreamFailed, Success: false, StatusCode: &status,
		ErrorPhase: "upstream_response", ErrorCode: "bad_gateway",
	})
	if len(dispatcher.all()) != 1 {
		t.Fatalf("失败审计必须投递: %d", len(dispatcher.all()))
	}
	if !stage.has("audit.finalize|expected_failure") {
		t.Fatalf("必须记录 expected_failure 阶段日志: %v", stage.entries)
	}
}

// TestWJCaptureDisabledAndMetadataOnly 固定禁用与元数据模式的行为。
func TestWJCaptureDisabledAndMetadataOnly(t *testing.T) {
	stage := &wjMockStageLogger{}
	disabled := captureInput(nil, func(input *AuditCaptureInput) {
		input.Settings = FixedAuditLogSettingsSource{Settings: AuditLogSettings{}}
		input.StageLogger = stage
	})
	if disabled.IsEnabled() {
		t.Fatal("禁用设置下 IsEnabled 必须为 false")
	}
	disabled.Finalize(FinalizeAuditInput{})
	// 惰性收尾在禁用态必须直接返回，不得构建输入。
	disabled.FinalizeLazy(func() FinalizeAuditInput {
		t.Fatal("禁用上下文不得执行惰性收尾")
		return FinalizeAuditInput{}
	})
	if !stage.has("audit.finalize|skipped") {
		t.Fatalf("禁用收尾必须记录 skipped: %v", stage.entries)
	}

	dispatcher := &recordingAuditDispatcher{}
	metadataOnly := captureInput(dispatcher, func(input *AuditCaptureInput) {
		input.CaptureMode = CaptureModeMetadataOnly
		input.Settings = FixedAuditLogSettingsSource{Settings: wjCaptureSettings(0, 1)}
	})
	defer metadataOnly.Cancel()
	if metadataOnly.ShouldCaptureSuccessPayloads() {
		t.Fatal("metadata_only 模式必须不捕获负载")
	}
	statusOK := 200
	metadataOnly.Finalize(FinalizeAuditInput{Outcome: AuditOutcomeSuccess, Success: true, StatusCode: &statusOK})
	logs := dispatcher.all()
	if len(logs) != 1 {
		t.Fatalf("metadata_only 必须投递 1 条: %d", len(logs))
	}
	if logs[0].CaptureStatus != "metadata_only" {
		t.Fatalf("metadata_only 状态不符: %q", logs[0].CaptureStatus)
	}
}

// TestWJCaptureInProgressStreamEnvelope 固定流式请求的 in_progress 信封：
// 首次入队一次，重复调用不再入队，非流式不入队。
func TestWJCaptureInProgressStreamEnvelope(t *testing.T) {
	dispatcher := &recordingAuditDispatcher{}
	capture := captureInput(dispatcher, func(input *AuditCaptureInput) {
		input.Stream = true
		input.Settings = FixedAuditLogSettingsSource{Settings: wjCaptureSettings(24, 1)}
	})
	defer capture.Cancel()
	capture.enqueueInProgressAudit()
	capture.enqueueInProgressAudit()
	logs := dispatcher.all()
	if len(logs) != 1 {
		t.Fatalf("流式信封必须只入队一次: %d", len(logs))
	}
	if logs[0].LifecycleStatus != AuditLifecycleInProgress {
		t.Fatalf("生命周期状态不符: %q", logs[0].LifecycleStatus)
	}
	nonStreamDispatcher := &recordingAuditDispatcher{}
	nonStream := captureInput(nonStreamDispatcher, func(input *AuditCaptureInput) {
		input.Settings = FixedAuditLogSettingsSource{Settings: wjCaptureSettings(24, 1)}
	})
	defer nonStream.Cancel()
	nonStream.enqueueInProgressAudit()
	if got := len(nonStreamDispatcher.all()); got != 0 {
		t.Fatalf("非流式不得入队信封: %d", got)
	}
}

// TestWJEstimateRetainedPayloadBytesOffload 固定 server 卸载模式下的保留
// 字节估算：按 fullBodyLimit 截断而非完整 body。
func TestWJEstimateRetainedPayloadBytesOffload(t *testing.T) {
	body := []byte(strings.Repeat("x", 2048))
	payload := &AuditLogPayloadInput{PartType: AuditPartClientRequest, HasBody: true, Body: body}
	// 非 offload：完整 body 计入。
	full := estimateRetainedPayloadBytes(payload, 256, false)
	if full != len(body)+512 {
		t.Fatalf("非 offload 估算 = %d", full)
	}
	// offload：body 截到 fullBodyLimit。
	trimmed := estimateRetainedPayloadBytes(payload, 256, true)
	if trimmed != 256+512 {
		t.Fatalf("offload 估算 = %d", trimmed)
	}
	// gateway metadata part 即使 offload 也按完整计。
	metadata := &AuditLogPayloadInput{PartType: AuditPartGatewayMetadata, HasBody: true, Body: body}
	if got := estimateRetainedPayloadBytes(metadata, 256, true); got != len(body)+512 {
		t.Fatalf("gateway metadata 估算 = %d", got)
	}
	// 无 body 的负载按 0 body 计。
	empty := &AuditLogPayloadInput{PartType: AuditPartClientRequest}
	if got := estimateRetainedPayloadBytes(empty, 256, true); got != 512 {
		t.Fatalf("无 body 估算 = %d", got)
	}
}

// TestWJSpoolFailureAccountingAndReplayLoop 固定补偿队列的失败记账与回放
// 循环生命周期。
func TestWJSpoolFailureAccountingAndReplayLoop(t *testing.T) {
	spool, _ := newTestSpool(t, true)
	// 持久化失败计数必须进入运行时视图。
	if err := spool.recordPersistFailure(errors.New("marshal down")); err == nil {
		t.Fatal("recordPersistFailure 必须回传错误")
	}
	runtime := spool.Runtime()
	if runtime.PersistFailureCount != 1 || runtime.LastError != "marshal down" {
		t.Fatalf("失败记账不符: %+v", runtime)
	}

	// 回放循环：空队列空转；二次启动必须幂等；Stop 后退出。
	spool.StartReplay(&wjNoopReplay{})
	spool.StartReplay(&wjNoopReplay{}) // 幂等
	spool.StopReplay()
	spool.StopReplay() // 幂等
}

// TestWJSpoolCapacityCacheRefresh 固定容量缓存过期后的重扫路径：写满后再
// 写必须触发容量刷新并正确拒绝。
func TestWJSpoolCapacityCacheRefresh(t *testing.T) {
	directory := t.TempDir()
	spool := NewUsageRecordSpool(SpoolConfig{
		Directory:        directory,
		InstanceID:       "inst-cap",
		MaxItems:         1,
		MaxBytes:         1 << 20,
		ReplayBatchSize:  8,
		ReplayIntervalMs: 5,
		Enabled:          true,
	}, fixedClock{ms: 1700000000000}, nil)
	record := UsageRecordInput{ID: "usage_cap_1", TraceID: "cap-1", TrafficSource: TrafficSourceGateway, Success: true, CreatedAt: "2023-11-14T22:13:20.123Z"}
	if err := spool.Persist(context.Background(), record); err != nil {
		t.Fatalf("首次持久化: %v", err)
	}
	// 第二条超过 MaxItems：先走 30s 缓存（未过期），强制刷新后仍超限。
	if err := spool.Persist(context.Background(), UsageRecordInput{ID: "usage_cap_2", TraceID: "cap-2", TrafficSource: TrafficSourceGateway, Success: true, CreatedAt: "2023-11-14T22:13:20.123Z"}); err == nil {
		t.Fatal("超容量必须拒绝")
	}
	if got := spool.Runtime().PersistFailureCount; got != 1 {
		t.Fatalf("容量失败必须记账: %d", got)
	}
}

// TestWJPrometheusHelpersAndDiagnostics 固定指标分桶与诊断脱敏的边缘。
func TestWJPrometheusHelpersAndDiagnostics(t *testing.T) {
	// 状态码分桶边界。
	unknown := 0
	if got := classifyHTTPMetricStatus(nil); got != "unknown" {
		t.Fatalf("nil 状态 = %q", got)
	}
	if got := classifyHTTPMetricStatus(&unknown); got != "unknown" {
		t.Fatalf("0 状态 = %q", got)
	}
	tooBig := 600
	if got := classifyHTTPMetricStatus(&tooBig); got != "unknown" {
		t.Fatalf("600 状态 = %q", got)
	}
	bucket500 := 503
	if got := classifyHTTPMetricStatus(&bucket500); got != "5xx" {
		t.Fatalf("503 分桶 = %q", got)
	}
	// 5xx 完成请求必须归入 gateway 失败范围。
	if got := classifyHTTPMetricFailureScope(&bucket500, HTTPMetricOutcomeCompleted, HTTPMetricFailureScopeNone); got != HTTPMetricFailureScopeGateway {
		t.Fatalf("5xx 失败范围 = %q", got)
	}
	if got := classifyHTTPMetricFailureScope(&bucket500, HTTPMetricOutcomeCompleted, HTTPMetricFailureScopeUpstream); got != HTTPMetricFailureScopeUpstream {
		t.Fatalf("已有范围必须保留 = %q", got)
	}
	if got := classifyHTTPMetricFailureScope(nil, HTTPMetricOutcomeAborted, HTTPMetricFailureScopeNone); got != HTTPMetricFailureScopeNone {
		t.Fatalf("非完成请求必须为 none = %q", got)
	}
	// itoa 边缘（0 与负数）。
	if itoa(0) != "0" || itoa(-12) != "-12" || itoa(987) != "987" {
		t.Fatal("itoa 边缘不符")
	}
	// 深度截断（diagnosticMaxRecursiveDepth=8）。
	nested := map[string]any{}
	cursor := nested
	for i := 0; i < 10; i++ {
		next := map[string]any{}
		cursor["n"] = next
		cursor = next
	}
	sanitized := sanitizeDiagnosticValue(nested, "", 0)
	cursorMap, ok := sanitized.(map[string]any)
	if !ok {
		t.Fatalf("map 输入必须返回 map: %T", sanitized)
	}
	inner := cursorMap
	for i := 0; i < 8; i++ {
		next, ok := inner["n"].(map[string]any)
		if !ok {
			break
		}
		inner = next
	}
	if inner["n"] != "[truncated]" {
		t.Fatalf("超深嵌套必须截断: %v", inner)
	}
}

// TestWJDiagnosticSensitiveFields 固定敏感字段名脱敏。
func TestWJDiagnosticSensitiveFields(t *testing.T) {
	if got := sanitizeDiagnosticValue("secret", "Authorization", 0); got != diagnosticRedacted {
		t.Fatalf("authorization 必须脱敏: %v", got)
	}
	if got := sanitizeDiagnosticValue("secret", " API-Key ", 0); got != diagnosticRedacted {
		t.Fatalf("带空格连字符的字段名必须脱敏: %v", got)
	}
	if got := sanitizeDiagnosticValue("plain", "normal_field", 0); got != "plain" {
		t.Fatalf("普通字段不得脱敏: %v", got)
	}
	// 数组超长截断与标量透传。
	long := make([]any, 500)
	for i := range long {
		long[i] = 1
	}
	result, ok := sanitizeDiagnosticValue(long, "", 0).([]any)
	if !ok || len(result) != diagnosticMaxArrayItems+1 {
		t.Fatalf("数组截断不符: %v", result)
	}
	if got := sanitizeDiagnosticValue(uint64(8), "", 0); got != uint64(8) {
		t.Fatalf("标量透传 = %v", got)
	}
}
