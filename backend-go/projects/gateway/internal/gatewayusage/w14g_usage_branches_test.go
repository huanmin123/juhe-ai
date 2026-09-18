package gatewayusage

// w14g：用量采集族覆盖率补强（纯函数与 bound/estimate 分支直驱）。
//
// 不可达语句登记（分析依据见各条；均不需要在本轮构造可达路径）：
//   - rfc3339.go fractionNanos 的 Atoi 错误分支（73-75）及其调用方分支
//     （48-50）：fraction 至多 9 位数字，补零后 Atoi 不可能失败。
//   - ports.go newUUID 的 crand.Read 错误分支（13-14）：crypto/rand 在受支持
//     平台不会失败。
//   - audit_summary.go updateExistingPayloadSummaryLimit 的 json.Marshal(map)
//     错误分支（133-134）。
//   - audit_capture.go SanitizeDiagnosticString 内裸敏感键正则 groups==nil
//     回退（81-83）：正则命中后子组必然存在。

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// records.go：snapshot bound 分支
// ---------------------------------------------------------------------------

func w14gBoundContext(bytes int) *snapshotBoundContext {
	return &snapshotBoundContext{bytes: bytes, seen: newIdentitySet()}
}

func TestW14GBoundSnapshotBranches(t *testing.T) {
	// 预算入口截断 / nil / []byte / time.Time。
	if got := boundSnapshotValue("x", w14gBoundContext(usageSnapshotMaxBytes)); got != "[truncated]" {
		t.Fatalf("预算截断 = %v", got)
	}
	if got := boundSnapshotValue(nil, w14gBoundContext(0)); got != nil {
		t.Fatalf("nil = %v", got)
	}
	if got := boundSnapshotValue([]byte("w14g"), w14gBoundContext(0)); got == nil {
		t.Fatalf("[]byte 应产出 buffer 对象")
	}
	if got := boundSnapshotValue(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), w14gBoundContext(0)); got == nil {
		t.Fatalf("time 应产出字符串")
	}
	// OrderedObject / map / slice / struct 循环引用。
	object := NewOrderedObject()
	object.Set("self", object)
	if got := boundSnapshotValue(object, w14gBoundContext(0)); got == "[circular]" {
		t.Fatalf("外层不应是 circular")
	} else if got == nil {
		t.Fatalf("对象应可遍历")
	}
	inner := NewOrderedObject()
	outer := NewOrderedObject()
	outer.Set("inner", inner)
	inner.Set("outer", outer)
	if got := boundSnapshotValue(outer, w14gBoundContext(0)); got == nil {
		t.Fatalf("循环对象应产出 [circular] 子项")
	}
	selfMap := map[string]any{}
	selfMap["self"] = selfMap
	boundSnapshotValue(selfMap, w14gBoundContext(0))
	selfSlice := make([]any, 1)
	selfSlice[0] = selfSlice
	boundSnapshotValue(selfSlice, w14gBoundContext(0))
	type w14gNode struct{ Next *w14gNode }
	node := &w14gNode{}
	node.Next = node
	boundSnapshotValue(node, w14gBoundContext(0))
	// 深度截断。
	deep := &w14gNode{}
	cursor := deep
	for i := 0; i < usageSnapshotMaxDepth+2; i++ {
		cursor.Next = &w14gNode{}
		cursor = cursor.Next
	}
	boundSnapshotValue(deep, w14gBoundContext(0))
	// 非结构体默认分支（[]int 走 displayString）。
	boundSnapshotValue([]int{1, 2}, w14gBoundContext(0))
	// nil 指针字段 / 未导出字段 / json:"-"。
	type w14gStruct struct {
		Ptr     *int
		hidden  string
		Ignored string `json:"-"`
		Keep    string `json:"keep"`
	}
	if got, ok := boundSnapshotStruct((*w14gStruct)(nil), w14gBoundContext(0)); ok || got != nil {
		t.Fatalf("nil 指针结构应返回 false")
	}
	boundSnapshotStruct(&w14gStruct{Ptr: nil, hidden: "h", Keep: "k"}, w14gBoundContext(0))
	// 字段遍历中途预算耗尽。
	boundSnapshotStruct(&w14gStruct{Keep: strings.Repeat("a", 10)}, w14gBoundContext(usageSnapshotMaxBytes-1))
	// 字符串截断（正常与 prefixBytes<0）。
	boundSnapshotString(strings.Repeat("a", usageSnapshotMaxStringBytes+10), w14gBoundContext(0))
	boundSnapshotString(strings.Repeat("a", 100), w14gBoundContext(usageSnapshotMaxBytes-1))
	if got := BoundUsageRecordSnapshot(nil); got != nil {
		t.Fatalf("公开入口 nil = %v", got)
	}
}

// ---------------------------------------------------------------------------
// diagnostics.go
// ---------------------------------------------------------------------------

func TestW14GDiagnosticSanitizerBranches(t *testing.T) {
	// matchQuotedSensitiveAssignment 防御分支（失败返回 ("", 0)）。
	for index, input := range []string{`'bad`, `'x' x`, `'password' x`, `'password':`, `'password': 42`} {
		if _, end := matchQuotedSensitiveAssignment(input, 0); end != 0 {
			t.Fatalf("case %d 不应匹配: %q", index, input)
		}
	}
	// matchAssignmentSeparator 分支。
	if end, ok := matchAssignmentSeparator("a = b", 1); !ok || end != 4 {
		t.Fatalf("分隔符 = %d %v", end, ok)
	}
	if end, ok := matchAssignmentSeparator("a, b", 1); ok || end != 1 {
		t.Fatalf("无分隔符 = %d %v", end, ok)
	}
	// scanQuotedValueEnd 分支。
	if _, ok := scanQuotedValueEnd(`'a\`, 2, '\''); ok {
		t.Fatalf("结尾孤立反斜杠不应匹配")
	}
	if _, ok := scanQuotedValueEnd(`'abc`, 2, '\''); ok {
		t.Fatalf("未闭合值不应匹配")
	}
	// sanitizeDiagnosticValue default 分支。
	if got := sanitizeDiagnosticValue(make(chan int), "field", 0); got == nil {
		t.Fatalf("未知类型应原样返回")
	}
	// 端到端脱敏仍保持行为。
	if got := SanitizeDiagnosticString("Bearer eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ3MTRnIn0.sig123456"); !strings.Contains(got, "[redacted]") {
		t.Fatalf("Bearer 未脱敏: %s", got)
	}
}

// ---------------------------------------------------------------------------
// size.go
// ---------------------------------------------------------------------------

type w14gSizeStruct struct {
	Ptr   *int
	Keep  string `json:"keep"`
	Hidden string `json:"-"`
}

func TestW14GEstimateBranches(t *testing.T) {
	// 循环 OrderedObject / 结构体。
	object := NewOrderedObject()
	object.Set("self", object)
	if EstimateJSONLikeBytes(object, EstimateJSONLikeBytesOptions{}) <= 0 {
		t.Fatalf("循环对象应产出估算")
	}
	node := &w14gSizeStruct{}
	// 结构体循环引用通过 interface 字段不易构造，改用指针链：
	if EstimateJSONLikeBytes(&node, EstimateJSONLikeBytesOptions{}) <= 0 {
		t.Fatalf("结构体估算失败")
	}
	// visitStructLikeValue：nil 指针 / 非结构体。
	context := &jsonLikeEstimateContext{seen: newIdentitySet(), maxBytes: 1 << 20, maxNodes: 1 << 20}
	if visitStructLikeValue((*w14gSizeStruct)(nil), context) {
		t.Fatalf("nil 指针结构应返回 false")
	}
	if visitStructLikeValue([]int{1}, context) {
		t.Fatalf("非结构体应返回 false")
	}
	// addEstimatedBytes 负数 / estimateStringBytes 上限 0。
	addEstimatedBytes(context, -5)
	small := &jsonLikeEstimateContext{seen: newIdentitySet(), maxBytes: 0}
	if got := estimateStringBytes("hello", small); got != 5 {
		t.Fatalf("estimateStringBytes = %d", got)
	}
	// 多字节边界截断。
	if got := sliceStringByUTF8Bytes("中文值", 1); got != "" {
		t.Fatalf("多字节截断 = %q", got)
	}
}

// ---------------------------------------------------------------------------
// audit_summary.go / snapshots.go / rfc3339.go / trafficsource.go
// ---------------------------------------------------------------------------

func TestW14GAuditSummaryBranches(t *testing.T) {
	if summaryReasonOrDefault("") != SummaryReasonBodyExceededLimit {
		t.Fatalf("缺省 reason 错误")
	}
	// limit=0 时 summary 降级为 hash-only。
	payload := &AuditLogPayloadInput{CaptureStatus: AuditCaptureSummaryOnly, HasBody: true, Body: []byte(`{"type":"audit_payload_summary"}`)}
	updateExistingPayloadSummaryLimit(payload, 0)
	if payload.CaptureStatus != AuditCaptureHashOnly || payload.HasBody {
		t.Fatalf("limit=0 未降级: %+v", payload)
	}
	// 有效 summary 缩减。
	summaryPayload := &AuditLogPayloadInput{CaptureStatus: AuditCaptureSummaryOnly, HasBody: true, Body: []byte(`{"type":"audit_payload_summary","headBase64":"` + base64.StdEncoding.EncodeToString([]byte("abcdef")) + `","tailBase64":"` + base64.StdEncoding.EncodeToString([]byte("ghijkl")) + `","originalSizeBytes":100}`)}
	updateExistingPayloadSummaryLimit(summaryPayload, 4)
	if !strings.Contains(string(summaryPayload.Body), "audit_payload_summary") {
		t.Fatalf("summary 缩减失败: %s", summaryPayload.Body)
	}
	// head/tail 缺失 → 直接返回。
	shrinkExistingPayloadSummary(map[string]any{}, 10)
	// buildAuditPayloadSummary 分支矩阵。
	for _, item := range []auditPayloadSummaryBuildInput{
		{body: make([]byte, AuditBodySummaryEdgeBytes*3), originalBodySizeBytes: AuditBodySummaryEdgeBytes * 3, fullBodyLimitBytes: AuditBodySummaryEdgeBytes * 3, reason: SummaryReasonBodyExceededLimit},
		{body: []byte("abc"), originalBodySizeBytes: 3, fullBodyLimitBytes: 1, reason: ""},
		{body: nil, originalBodySizeBytes: 0, fullBodyLimitBytes: 0, reason: ""},
	} {
		if buildAuditPayloadSummary(item) == nil {
			t.Fatalf("buildAuditPayloadSummary 返回 nil")
		}
	}
	// textPreview 截断。
	if got := textPreview(make([]byte, auditBodySummaryTextPreviewBytes+10)); len(got) != auditBodySummaryTextPreviewBytes {
		t.Fatalf("textPreview 长度 = %d", len(got))
	}
}

func TestW14GSnapshotAndTimeHelpers(t *testing.T) {
	// requestedReasoningEffortFromBody：map 回退 / 缺失。
	if got := requestedReasoningEffortFromBody(map[string]any{"reasoning_effort": "low"}); got != "low" {
		t.Fatalf("flat effort = %v", got)
	}
	if got := requestedReasoningEffortFromBody(42); got != nil {
		t.Fatalf("非对象 = %v", got)
	}
	// jsonRecordField map 分支与 GatewayErrorField。
	if got := jsonRecordField(map[string]any{"k": "v"}, "k"); got != "v" {
		t.Fatalf("jsonRecordField = %v", got)
	}
	payload := NewOrderedObject()
	errObject := NewOrderedObject()
	errObject.Set("code", "w14g-code")
	payload.Set("error", errObject)
	if got := GatewayErrorField(payload, "code"); got != "w14g-code" {
		t.Fatalf("GatewayErrorField = %v", got)
	}
	// mustMarshalJSON 正常路径。
	if len(mustMarshalJSON(map[string]int{"a": 1})) == 0 {
		t.Fatalf("mustMarshalJSON 失败")
	}
	// SanitizeURLForLog：解析失败 / 非目标路径。
	if got := SanitizeURLForLog("http://[::bad"); got != "http://[::bad" {
		t.Fatalf("解析失败应原样返回")
	}
	if got := SanitizeURLForLog("http://example.com/plain"); got != "http://example.com/plain" {
		t.Fatalf("非 oauth 路径应原样返回")
	}
	// rfc3339：非法日期 / 毫秒转换。
	if _, ok := parseRFC3339Instant("2026-02-30T00:00:00Z"); ok {
		t.Fatalf("2 月 30 日必须解析失败")
	}
	if _, ok := rfc3339InstantMilliseconds("not-a-time"); ok {
		t.Fatalf("非法时间必须失败")
	}
	// traffic source 判断。
	if IsAccountProbeTrafficSource("account_health_check") != true {
		t.Fatalf("探针来源判断失败")
	}
	if IsAccountDiagnosticTrafficSource("manual_account_test") != true {
		t.Fatalf("诊断来源判断失败")
	}
	if displayString(nil) != "undefined" {
		t.Fatalf("displayString(nil) = %s", displayString(nil))
	}
}

// ---------------------------------------------------------------------------
// finalization.go
// ---------------------------------------------------------------------------

func TestW14GFinalizationRecorderBranches(t *testing.T) {
	recorder := &MemoryUsageRecorder{}
	recorder.SetFailures(1)
	if err := recorder.EnqueueUsageRecord(nil, UsageRecordInput{}); err == nil {
		t.Fatalf("注入失败必须返回错误")
	}
	if _, ok := (&MemoryUsageRecorder{}).LastRecord(); ok {
		t.Fatalf("空记录 LastRecord 必须为 false")
	}
	queue := &GatewayUsageFinalizationQueue{}
	if !queue.WaitForIdle(0) {
		t.Fatalf("空队列必须立即空闲")
	}
}
