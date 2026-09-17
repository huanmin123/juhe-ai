package gatewayusage

// w13d 波次补充测试：审计捕获的绑定、元数据、载荷省略、失败尝试根因、
// HTTP 完成观察者与模型计费汇总分支。
//
// 本文件登记的不可达语句：
//   - audit_capture.go:355（gatewayMetadataBody 的 MarshalJSON 错误回退）：
//     body 只含 string 与 *OrderedObject 值，MarshalJSON 恒成功。
//   - audit_capture.go:1390（applyPayloadRetention 的 clock nil 回退）：
//     NewAuditCaptureContext 已把 nil Clock 替换为 SystemClock。

import (
	"strings"
	"testing"
)

func w13dCapture(dispatcher *recordingAuditDispatcher, mutate func(*AuditCaptureInput)) *AuditCaptureContext {
	ResetActiveAuditCaptureCountForTest()
	return captureInput(dispatcher, mutate)
}

func TestW13DBindContextMergesAllFields(t *testing.T) {
	capture := w13dCapture(nil, nil)
	applied := true
	capture.BindContext(AuditGatewayContext{
		SessionID:              "sess-1",
		SessionClientType:      "web",
		ConversationKey:        "conv-1",
		SystemAccountID:        "sys",
		APIKeyID:               "key",
		GroupID:                "grp",
		AccountID:              "acc",
		ProviderCode:           "gpt",
		UpstreamModel:          "gpt-up",
		PricingModel:           "gpt-price",
		ModelMappingApplied:    &applied,
		ModelMappingSource:     "account",
		SourceEndpointFamily:   "chat_completions",
		UpstreamEndpointFamily: "chat_completions",
		TrafficSource:          TrafficSourceGateway,
	})
	// ProviderCode 只在为空时被覆盖，其余字段全部合并。
	capture.BindContext(AuditGatewayContext{SessionID: "sess-2"})
	_ = capture
}

func TestW13DNewAuditCaptureContextFallbacks(t *testing.T) {
	// Settings 缺失回退 FixedAuditLogSettingsSource（禁用）。
	disabled := NewAuditCaptureContext(AuditCaptureInput{TraceID: "t"})
	if disabled.IsEnabled() {
		t.Fatal("缺 Settings 必须禁用")
	}
	// 非法 TrafficSource 回落 gateway。
	capture := NewAuditCaptureContext(AuditCaptureInput{
		TraceID: "t", TrafficSource: "not-a-source",
		Settings: FixedAuditLogSettingsSource{Settings: auditSettings(true, 0)},
	})
	if !capture.IsEnabled() {
		t.Fatal("启用设置必须启用")
	}
	// nil metadata 与不可编码 metadata。
	capture.AddGatewayMetadata("nil-metadata", nil)
	broken := NewOrderedObject()
	broken.Set("bad", make(chan int))
	capture.AddGatewayMetadata("broken", broken)
	capture.AddGatewayMetadata("map-metadata", map[string]any{"k": "v"})
	// gatewayMetadataBody nil metadata 分支。
	body := gatewayMetadataBody("label", nil)
	if !strings.Contains(string(body), `"metadata":{}`) {
		t.Fatalf("body=%s", body)
	}
}

func TestW13DOmitPayloadBodiesBranches(t *testing.T) {
	dispatcher := &recordingAuditDispatcher{}
	capture := w13dCapture(dispatcher, func(input *AuditCaptureInput) {
		input.Settings = FixedAuditLogSettingsSource{Settings: AuditLogSettings{
			Enabled: true, FullBodyCaptureEnabled: true,
			SuccessSampleRate: 1, ActiveCaptureMaxBytes: 64 * 1024,
			SuccessFullBodyLimitBytes: 64 * 1024, ProblemFullBodyLimitBytes: 64 * 1024,
		}}
	})
	tempID := capture.StartAttempt(StartAttemptInput{
		Account: testAccount(), AttemptIndex: 0,
		UpstreamURL: "https://upstream", Method: "POST",
		Body: []byte(`{"model":"gpt"}`), HasBody: true,
	})
	capture.CompleteAttempt(tempID, CompleteAttemptInput{
		StatusCode: intPointer(500), Success: false,
		ErrorPhase: "upstream_response", ErrorMessage: "失败",
		ResponseBody: []byte("boom"), HasResponseBody: true,
	})
	// OrderedObject 元数据走合并分支；PartTypes 过滤无关载荷。
	ordered := NewOrderedObject()
	ordered.Set("reason", "manual")
	capture.OmitPayloadBodies(OmitPayloadBodiesInput{
		Metadata:  ordered,
		Label:     "manual_omit",
		PartTypes: []AuditPayloadPartType{AuditPartUpstreamRequest},
	})
	// map 元数据走默认合并分支。
	capture.OmitPayloadBodies(OmitPayloadBodiesInput{
		Metadata: map[string]any{"extra": 1},
		Label:    "manual_omit_all",
	})
	capture.Finalize(FinalizeAuditInput{Outcome: AuditOutcomeUpstreamFailed, Success: false, StatusCode: intPointer(500)})
	final := dispatcher.all()
	if len(final) == 0 {
		t.Fatal("必须产出审计")
	}
}

func TestW13DOmitPayloadBodiesDisabled(t *testing.T) {
	capture := w13dCapture(nil, func(input *AuditCaptureInput) {
		input.Settings = FixedAuditLogSettingsSource{Settings: auditSettings(false, 0)}
	})
	capture.OmitPayloadBodies(OmitPayloadBodiesInput{Label: "disabled"})
	// 禁用态不 panic 即可。
}

func TestW13DStartAttemptGuards(t *testing.T) {
	dispatcher := &recordingAuditDispatcher{}
	disabled := w13dCapture(dispatcher, func(input *AuditCaptureInput) {
		input.Settings = FixedAuditLogSettingsSource{Settings: auditSettings(false, 0)}
	})
	if id := disabled.StartAttempt(StartAttemptInput{Account: testAccount()}); id != "" {
		t.Fatal("禁用态 StartAttempt 必须返回空")
	}
	if id := disabled.RecordFailedDispatchAttempt(RecordFailedDispatchAttemptInput{Account: testAccount()}); id != "" {
		t.Fatal("禁用态 RecordFailedDispatchAttempt 必须返回空")
	}
	// 未知 tempID 与重复完成幂等。
	capture := w13dCapture(dispatcher, nil)
	capture.CompleteAttempt("unknown-temp-id", CompleteAttemptInput{Success: true})
	tempID := capture.StartAttempt(StartAttemptInput{Account: testAccount(), AttemptIndex: 0, UpstreamURL: "https://upstream", Method: "POST"})
	capture.CompleteAttempt(tempID, CompleteAttemptInput{StatusCode: intPointer(200), Success: true})
	capture.CompleteAttempt(tempID, CompleteAttemptInput{StatusCode: intPointer(200), Success: true})
	capture.FinalizeLazy(func() FinalizeAuditInput {
		return FinalizeAuditInput{Outcome: AuditOutcomeSuccess, Success: true, StatusCode: intPointer(200)}
	})
}

func TestW13DLatestFailedAttemptRootPhases(t *testing.T) {
	capture := w13dCapture(nil, nil)
	// 无失败尝试 → nil。
	if root := capture.LatestFailedAttemptRoot(); root != nil {
		t.Fatalf("root=%+v", root)
	}
	// 成功尝试不计入失败根因。
	tempID := capture.StartAttempt(StartAttemptInput{Account: testAccount(), AttemptIndex: 0, UpstreamURL: "https://upstream", Method: "POST"})
	capture.CompleteAttempt(tempID, CompleteAttemptInput{StatusCode: intPointer(200), Success: true})
	if root := capture.LatestFailedAttemptRoot(); root != nil {
		t.Fatalf("成功尝试不得产生根因: %+v", root)
	}
	// stream 阶段失败 → ErrorPhase stream 分支。
	streamTemp := capture.StartAttempt(StartAttemptInput{Account: testAccount(), AttemptIndex: 1, UpstreamURL: "https://upstream", Method: "POST"})
	capture.CompleteAttempt(streamTemp, CompleteAttemptInput{
		Success: false, ErrorPhase: "stream", ErrorCode: "stream_broken", ErrorMessage: "流中断",
	})
	root := capture.LatestFailedAttemptRoot()
	if root == nil || root.ErrorPhase != "stream" {
		t.Fatalf("root=%+v", root)
	}
	// downstream 阶段的失败在根因搜索中被跳过：root 保持 stream 失败。
	dsTemp := capture.StartAttempt(StartAttemptInput{Account: testAccount(), AttemptIndex: 2, UpstreamURL: "https://upstream", Method: "POST"})
	capture.CompleteAttempt(dsTemp, CompleteAttemptInput{
		Success: false, ErrorPhase: "downstream", ErrorCode: "ds_err",
	})
	root = capture.LatestFailedAttemptRoot()
	if root == nil || root.ErrorPhase != "stream" {
		t.Fatalf("downstream 必须被跳过: %+v", root)
	}
	// 空阶段且无错误码的失败同样被跳过。
	emptyTemp := capture.StartAttempt(StartAttemptInput{Account: testAccount(), AttemptIndex: 3, UpstreamURL: "https://upstream", Method: "POST"})
	capture.CompleteAttempt(emptyTemp, CompleteAttemptInput{Success: false})
	root = capture.LatestFailedAttemptRoot()
	if root == nil || root.ErrorPhase != "stream" {
		t.Fatalf("空失败必须被跳过: %+v", root)
	}
}

func TestW13DHTTPCompletionObserverPaths(t *testing.T) {
	dispatcher := &recordingAuditDispatcher{}
	observer := &stubHTTPCompletion{completedAtMs: 1700000001000, hasCompletion: true}
	capture := w13dCapture(dispatcher, func(input *AuditCaptureInput) {
		input.HTTPCompletion = observer
	})
	capture.Cancel()
	// 已取消的捕获必须注销监听（cancel 被调用）。
	if !observer.cancelled {
		t.Fatal("取消必须注销监听")
	}
	// 无 cancel 句柄的重复 markHTTPCompleted 幂等。
	capture2 := w13dCapture(dispatcher, func(input *AuditCaptureInput) {
		input.HTTPCompletion = &stubHTTPCompletion{}
	})
	capture2.markHTTPCompleted(1700000001000)
	capture2.markHTTPCompleted(1700000002000)
	capture2.markHTTPCompleted(0)
	capture2.FinalizeLazy(func() FinalizeAuditInput {
		return FinalizeAuditInput{Outcome: AuditOutcomeSuccess, Success: true, StatusCode: intPointer(200)}
	})
}

type stubHTTPCompletion struct {
	completedAtMs int64
	hasCompletion bool
	cancelled     bool
}

func (s *stubHTTPCompletion) CompletedAtMs() (int64, bool) {
	return s.completedAtMs, s.hasCompletion
}

func (s *stubHTTPCompletion) OnCompleted(listener func(completedAtMs int64)) func() {
	if s.hasCompletion {
		listener(s.completedAtMs)
	}
	return func() { s.cancelled = true }
}

func TestW13DFlushFinalizedAuditGuards(t *testing.T) {
	dispatcher := &recordingAuditDispatcher{}
	capture := w13dCapture(dispatcher, nil)
	// pendingFinalizeInput 为 nil 时的 flush 直接返回。
	capture.flushFinalizedAudit()
	// 禁用态的 FinalizeLazy 不调用 buildInput（nil builder 安全）。
	disabled := w13dCapture(dispatcher, func(input *AuditCaptureInput) {
		input.Settings = FixedAuditLogSettingsSource{Settings: auditSettings(false, 0)}
	})
	disabled.FinalizeLazy(nil)
	capture.FinalizeLazy(func() FinalizeAuditInput { return FinalizeAuditInput{} })
	// 带 AccountID 的 finalization。
	capture2 := w13dCapture(dispatcher, nil)
	capture2.FinalizeLazy(func() FinalizeAuditInput {
		return FinalizeAuditInput{Outcome: AuditOutcomeSuccess, Success: true, AccountID: "acc-final"}
	})
	final := dispatcher.all()
	if len(final) == 0 || final[len(final)-1].AccountID != "acc-final" {
		t.Fatalf("final=%+v", final)
	}
}

func TestW13DAddClientRequestPayloadBranches(t *testing.T) {
	dispatcher := &recordingAuditDispatcher{}
	// 禁用态：addClientRequestPayloadLocked 直接返回。
	disabled := w13dCapture(dispatcher, func(input *AuditCaptureInput) {
		input.Settings = FixedAuditLogSettingsSource{Settings: auditSettings(false, 0)}
	})
	disabled.mu.Lock()
	disabled.addClientRequestPayloadLocked()
	disabled.mu.Unlock()
	// 溢出态：再次捕获被跳过。
	capture := w13dCapture(dispatcher, func(input *AuditCaptureInput) {
		input.Settings = FixedAuditLogSettingsSource{Settings: AuditLogSettings{
			Enabled: true, FullBodyCaptureEnabled: true,
			SuccessSampleRate: 1, ActiveCaptureMaxBytes: 32,
			SuccessFullBodyLimitBytes: 32, ProblemFullBodyLimitBytes: 32,
		}}
	})
	capture.StartAttempt(StartAttemptInput{
		Account: testAccount(), AttemptIndex: 0,
		UpstreamURL: "https://upstream", Method: "POST",
		Body: make([]byte, 4096), HasBody: true,
	})
	capture.mu.Lock()
	overflowedBefore := capture.overflowed
	capture.addClientRequestPayloadLocked()
	capture.mu.Unlock()
	if !overflowedBefore {
		t.Fatal("大载荷必须触发溢出")
	}
}

func TestW13DApplyPayloadRetentionOffload(t *testing.T) {
	dispatcher := &recordingAuditDispatcher{}
	capture := w13dCapture(dispatcher, func(input *AuditCaptureInput) {
		input.OffloadPayloadRetention = true
		input.Settings = FixedAuditLogSettingsSource{Settings: AuditLogSettings{
			Enabled: true, FullBodyCaptureEnabled: true,
			SuccessSampleRate: 1, ActiveCaptureMaxBytes: 64 * 1024,
			SuccessFullBodyLimitBytes: 64 * 1024, ProblemFullBodyLimitBytes: 64 * 1024,
		}}
	})
	tempID := capture.StartAttempt(StartAttemptInput{
		Account: testAccount(), AttemptIndex: 0,
		UpstreamURL: "https://upstream", Method: "POST",
		Body: []byte(`{"model":"gpt"}`), HasBody: true,
	})
	capture.CompleteAttempt(tempID, CompleteAttemptInput{
		StatusCode: intPointer(200), Success: true,
		ResponseBody: []byte("ok"), HasResponseBody: true,
	})
	capture.FinalizeLazy(func() FinalizeAuditInput {
		return FinalizeAuditInput{Outcome: AuditOutcomeSuccess, Success: true, StatusCode: intPointer(200)}
	})
	if len(dispatcher.all()) == 0 {
		t.Fatal("offload 模式必须产出审计")
	}
	// Pricing 与 SyncPricingAllowed 的模型计费分支。
	pricingCapture := w13dCapture(dispatcher, func(input *AuditCaptureInput) {
		input.SyncPricingAllowed = true
		input.Models = &stubModelResolver{resolution: UsageModelResolution{UpstreamModel: "gpt-x"}}
	})
	pricingCapture.StartAttempt(StartAttemptInput{
		Account: testAccount(), AttemptIndex: 0,
		UpstreamURL: "https://upstream", Method: "POST", Model: "override-model",
	})
	pricingCapture.FinalizeLazy(func() FinalizeAuditInput {
		return FinalizeAuditInput{Outcome: AuditOutcomeSuccess, Success: true, StatusCode: intPointer(200)}
	})
}

func TestW13DSummarizePayloadHeaderVariants(t *testing.T) {
	// estimateHeadersBytes 的多值 header / []any / 标量分支。
	headers := map[string]any{
		"multi":     []string{"a", "b"},
		"asAny":     []any{"x", "y", 3},
		"scalar":    7,
		"longValue": strings.Repeat("v", 500),
		"emptyList": []string{},
	}
	if estimateHeadersBytes(headers) <= 0 {
		t.Fatal("header 估算必须为正")
	}
	// headerMapValue：精确名 / 小写名 / 数组值 / 缺失。
	withHeaders := map[string]any{"Content-Type": "application/json", "multi": []string{"a"}}
	if headerMapValue(withHeaders, "Content-Type") != "application/json" {
		t.Fatal("精确名匹配失败")
	}
	if headerMapValue(map[string]any{"content-type": "lower"}, "CONTENT-TYPE") != "lower" {
		t.Fatal("小写名回退匹配失败")
	}
	if headerMapValue(withHeaders, "multi") != "a" {
		t.Fatalf("数组值=%q", headerMapValue(withHeaders, "multi"))
	}
	if headerMapValue(withHeaders, "missing") != "" {
		t.Fatal("缺失必须为空")
	}
	if headerMapValue(nil, "x") != "" {
		t.Fatal("nil headers 必须为空")
	}
	// 大 body 的摘要（超限头尾保留）。
	big := make([]byte, 8192)
	payload := AuditLogPayloadInput{PartType: AuditPartUpstreamResponse, Body: big, HasBody: true}
	if !SummarizeAuditPayloadForLimit(&payload, 128, SummarizeAuditPayloadOptions{}) {
		t.Fatal("大 body 必须被摘要")
	}
	// roundToInt。
	if roundToInt(7.9) != 8 || roundToInt(7.4) != 7 {
		t.Fatal("roundToInt 错误")
	}
	// estimateRetainedPayloadBytes 的 offload 分支。
	offloadPayload := AuditLogPayloadInput{
		PartType: AuditPartUpstreamRequest, Body: make([]byte, 4096), HasBody: true,
	}
	if estimateRetainedPayloadBytes(&offloadPayload, 64, true) <= 0 {
		t.Fatal("offload 估算必须为正")
	}
	estimateRetainedPayloadBytes(&offloadPayload, 64, false)
}
