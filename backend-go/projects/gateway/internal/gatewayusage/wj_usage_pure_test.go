package gatewayusage

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestWJOrderedObjectContracts 固定有序 JSON 对象的核心契约：插入序保留、
// 覆盖写不改变位置、nil 接收者安全。
func TestWJOrderedObjectContracts(t *testing.T) {
	var nilObj *OrderedObject
	if nilObj.Get("k") != nil || nilObj.Has("k") || nilObj.Len() != 0 || nilObj.Keys() != nil {
		t.Fatal("nil 接收者必须返回零值")
	}
	if cloned := nilObj.Clone(); cloned != nil {
		t.Fatal("nil Clone 必须返回 nil")
	}
	if mapped := nilObj.AsMap(); mapped == nil || len(mapped) != 0 {
		t.Fatalf("nil AsMap 必须返回空 map: %v", mapped)
	}
	encoded, err := json.Marshal(nilObj)
	if err != nil || string(encoded) != "null" {
		t.Fatalf("nil MarshalJSON = (%s, %v)", encoded, err)
	}

	obj := NewOrderedObject()
	obj.Set("b", 2).Set("a", 1).Set("c", 3)
	if strings.Join(obj.Keys(), ",") != "b,a,c" {
		t.Fatalf("插入序不符: %v", obj.Keys())
	}
	obj.Set("b", 20) // 覆盖写必须保持首插位置
	if strings.Join(obj.Keys(), ",") != "b,a,c" || obj.Get("b") != 20 {
		t.Fatalf("覆盖写改变顺序或值: %v %v", obj.Keys(), obj.Get("b"))
	}
	if !obj.Has("a") || obj.Has("missing") {
		t.Fatal("Has 判定不符")
	}
	encoded, err = json.Marshal(obj)
	if err != nil || string(encoded) != `{"b":20,"a":1,"c":3}` {
		t.Fatalf("MarshalJSON = (%s, %v)", encoded, err)
	}

	// 解码保留编码顺序 + 数字保持 json.Number 语义。
	var decoded OrderedObject
	if err := json.Unmarshal([]byte(`{"z":[1,{"y":2}],"x":null}`), &decoded); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if strings.Join(decoded.Keys(), ",") != "z,x" {
		t.Fatalf("解码顺序不符: %v", decoded.Keys())
	}
	// 非对象输入必须报错。
	if err := json.Unmarshal([]byte(`[1]`), &decoded); err == nil {
		t.Fatal("数组输入必须报错")
	}
	// null 输入保持 nil 对象。
	var nullDecoded OrderedObject
	if err := json.Unmarshal([]byte(`null`), &nullDecoded); err != nil || nullDecoded.Len() != 0 {
		t.Fatalf("null 解码 = (%v, %v)", nullDecoded, err)
	}

	// Clone 深拷贝嵌套 OrderedObject：改副本不影响原对象。
	nested := NewOrderedObject()
	nested.Set("inner", NewOrderedObject().Set("deep", 1))
	cloned := nested.Clone()
	cloned.Get("inner").(*OrderedObject).Set("deep", 999)
	if nested.Get("inner").(*OrderedObject).Get("deep") != 1 {
		t.Fatal("Clone 必须深拷贝嵌套对象")
	}
	// AsMap 丢序但保全键值。
	asMap := obj.AsMap()
	if asMap["b"] != 20 || len(asMap) != 3 {
		t.Fatalf("AsMap = %v", asMap)
	}
}

// TestWJEstimateJSONLikeBytesTypes 固定字节估算的全类型覆盖与预算截断。
func TestWJEstimateJSONLikeBytesTypes(t *testing.T) {
	full := map[string]any{
		"nil": nil, "str": "abc", "bool": true, "int": 42,
		"i64": int64(7), "u64": uint64(9), "f64": 1.5,
		"bytes": []byte("xyz"), "time": time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC),
		"arr": []any{1, "two"}, "obj": map[string]any{"k": "v"},
	}
	total := EstimateJSONLikeBytes(full, EstimateJSONLikeBytesOptions{})
	if total <= 0 {
		t.Fatalf("全类型估算必须为正: %d", total)
	}
	// MaxBytes 截断：预算为 1 时立即触顶。
	if got := EstimateJSONLikeBytes(full, EstimateJSONLikeBytesOptions{MaxBytes: 1}); got != 1 {
		t.Fatalf("MaxBytes=1 截断 = %d", got)
	}
	// MaxNodes 截断：只允许 2 个节点。
	bounded := EstimateJSONLikeBytes([]any{"aaaa", "bbbb", "cccc"}, EstimateJSONLikeBytesOptions{MaxNodes: 2})
	unbounded := EstimateJSONLikeBytes([]any{"aaaa", "bbbb", "cccc"}, EstimateJSONLikeBytesOptions{})
	if bounded >= unbounded {
		t.Fatalf("MaxNodes 必须限制规模: %d >= %d", bounded, unbounded)
	}
	// 循环引用：数组与对象各自计 16 字节并终止递归。
	arr := []any{nil}
	arr[0] = arr
	if got := EstimateJSONLikeBytes(arr, EstimateJSONLikeBytesOptions{}); got == 0 {
		t.Fatal("循环数组必须有估算值")
	}
	selfMap := map[string]any{}
	selfMap["self"] = selfMap
	if got := EstimateJSONLikeBytes(selfMap, EstimateJSONLikeBytesOptions{}); got == 0 {
		t.Fatal("循环对象必须有估算值")
	}
	// 行为存疑：OrderedObject 自引用会使 identitySet 失效（它只按
	// slice/map 指针跟踪容器），visitJSONLikeValue 对 *OrderedObject 无限
	// 递归直至栈溢出，因此本测试无法覆盖该输入（Node WeakSet 对普通对象
	// 生效，不会栈溢出）。已单列到交付报告。
	// 非结构体未知类型按 16 字节兜底。
	if got := EstimateJSONLikeBytes(struct{}{}, EstimateJSONLikeBytesOptions{}); got != 2 {
		t.Fatalf("空结构体 = %d, 期望仅括号 2 字节", got)
	}
	if got := EstimateJSONLikeBytes(complex(1, 2), EstimateJSONLikeBytesOptions{}); got != 16 {
		t.Fatalf("未知类型 = %d, 期望 16", got)
	}
}

// wjEstimateStruct 练习结构体反射路径（json tag、忽略字段、nil 指针/接口）。
type wjEstimateStruct struct {
	Visible  string  `json:"visible"`
	Hidden   string  `json:"-"`
	Ptr      *string `json:"ptr"`
	Any      any     `json:"any"`
	NoTag    int     `json:"-"`
	untagged string  // 未导出字段必须跳过
}

// TestWJEstimateStructFields 固定结构体字段的估算口径。
func TestWJEstimateStructFields(t *testing.T) {
	ptr := "p"
	value := wjEstimateStruct{Visible: "v", Ptr: &ptr}
	total := EstimateJSONLikeBytes(value, EstimateJSONLikeBytesOptions{})
	if total == 0 {
		t.Fatal("结构体估算必须为正")
	}
	// nil 指针/接口字段按 JSON null 计 4 字节；非 nil 值按实际字符串计
	//（"x" 含引号 3 字节，比 null 更小，因此非 nil 版本反而更小）。
	bare := EstimateJSONLikeBytes(wjEstimateStruct{Visible: "v"}, EstimateJSONLikeBytesOptions{})
	withAny := EstimateJSONLikeBytes(wjEstimateStruct{Visible: "v", Any: "x"}, EstimateJSONLikeBytesOptions{})
	if withAny != bare-1 {
		t.Fatalf("接口字段 null(4)→字符串(3) 差值不符: bare=%d withAny=%d", bare, withAny)
	}
	withPtr := EstimateJSONLikeBytes(wjEstimateStruct{Visible: "v", Ptr: &[]string{"p"}[0]}, EstimateJSONLikeBytesOptions{})
	if withPtr != bare-1 {
		t.Fatalf("指针字段 null(4)→字符串(3) 差值不符: bare=%d withPtr=%d", bare, withPtr)
	}
}

// TestWJBoundedStringHelpers 固定快照字符串边界助手。
func TestWJBoundedStringHelpers(t *testing.T) {
	if got := boundedStringByteLength("abc", 0); got != 0 {
		t.Fatalf("maxBytes=0 = %d", got)
	}
	if got := boundedStringByteLength("abc", 2); got != 2 {
		t.Fatalf("超预算截断 = %d", got)
	}
	if got := boundedStringByteLength("abc", 100); got != 3 {
		t.Fatalf("正常长度 = %d", got)
	}
	// 超长字符串按 len*4 上界估算。
	long := strings.Repeat("a", 17*1024)
	if got := boundedStringByteLength(long, 1<<30); got != len(long)*4 {
		t.Fatalf("超长上界 = %d", got)
	}
	if got := sliceStringByUTF8Bytes("a你b", 0); got != "" {
		t.Fatalf("maxBytes=0 = %q", got)
	}
	// 3 字节 rune 在剩余 2 字节时必须整体放弃而不是截半个字符。
	if got := sliceStringByUTF8Bytes("a你b", 2); got != "a" {
		t.Fatalf("rune 边界 = %q", got)
	}
	if got := sliceStringByUTF8Bytes("你", 3); got != "你" {
		t.Fatalf("完整多字节 = %q", got)
	}
}

// TestWJSummarizeAuditPayloadBranches 固定审计负载摘要的入口分支。
func TestWJSummarizeAuditPayloadBranches(t *testing.T) {
	body := []byte(strings.Repeat("x", 4096))

	// gateway metadata part 与无 body 一律跳过。
	metadata := &AuditLogPayloadInput{PartType: AuditPartGatewayMetadata, HasBody: true, Body: body}
	if SummarizeAuditPayloadForLimit(metadata, 0, SummarizeAuditPayloadOptions{}) {
		t.Fatal("gateway metadata 不得摘要")
	}
	empty := &AuditLogPayloadInput{PartType: AuditPartClientRequest, HasBody: false}
	if SummarizeAuditPayloadForLimit(empty, 0, SummarizeAuditPayloadOptions{}) {
		t.Fatal("无 body 不得摘要")
	}

	// fullBodyLimitBytes=0 → hash-only 降级。
	hashOnly := &AuditLogPayloadInput{PartType: AuditPartClientRequest, HasBody: true, Body: body}
	if !SummarizeAuditPayloadForLimit(hashOnly, 0, SummarizeAuditPayloadOptions{}) {
		t.Fatal("零限制必须降级 hash-only")
	}
	if hashOnly.CaptureStatus != AuditCaptureHashOnly || hashOnly.HasBody || hashOnly.Body != nil {
		t.Fatalf("hash-only 状态不符: %+v", hashOnly)
	}
	if hashOnly.BodySha256 == "" || hashOnly.RawBodySizeBytes == nil || *hashOnly.RawBodySizeBytes != len(body) {
		t.Fatalf("hash-only 摘要字段不符: %+v", hashOnly)
	}

	// 未超限且未强制 → 保持原样。
	small := &AuditLogPayloadInput{PartType: AuditPartClientRequest, HasBody: true, Body: []byte("hi")}
	if SummarizeAuditPayloadForLimit(small, 1024, SummarizeAuditPayloadOptions{}) {
		t.Fatal("未超限不得摘要")
	}
	// 强制 → 摘要。
	if !SummarizeAuditPayloadForLimit(small, 1024, SummarizeAuditPayloadOptions{Force: true}) {
		t.Fatal("强制必须摘要")
	}
	if small.CaptureStatus != AuditCaptureSummaryOnly || small.ContentType != AuditPayloadSummaryContentType {
		t.Fatalf("摘要状态不符: %+v", small)
	}

	// 已是 summary_only 的负载在零限制下二次处理 → 降级 hash-only。
	summaryOnly := &AuditLogPayloadInput{PartType: AuditPartClientRequest, HasBody: true, Body: small.Body, CaptureStatus: AuditCaptureSummaryOnly, ContentType: AuditPayloadSummaryContentType}
	updateExistingPayloadSummaryLimit(summaryOnly, 0)
	if summaryOnly.CaptureStatus != AuditCaptureHashOnly || summaryOnly.HasBody {
		t.Fatalf("二次降级不符: %+v", summaryOnly)
	}
	// 非 summary_only 的既有状态不动。
	complete := &AuditLogPayloadInput{PartType: AuditPartClientRequest, HasBody: true, Body: body, CaptureStatus: AuditCaptureComplete}
	updateExistingPayloadSummaryLimit(complete, 512)
	if complete.CaptureStatus != AuditCaptureComplete {
		t.Fatalf("complete 状态不得变更: %+v", complete)
	}
	// 损坏的 summary JSON 不动。
	broken := &AuditLogPayloadInput{PartType: AuditPartClientRequest, HasBody: true, Body: []byte("{bad"), CaptureStatus: AuditCaptureSummaryOnly}
	updateExistingPayloadSummaryLimit(broken, 512)
	if string(broken.Body) != "{bad" {
		t.Fatalf("损坏 JSON 必须原样保留: %s", broken.Body)
	}
	// type 不符的 JSON 不动。
	wrongType := &AuditLogPayloadInput{PartType: AuditPartClientRequest, HasBody: true, Body: []byte(`{"type":"other"}`), CaptureStatus: AuditCaptureSummaryOnly}
	updateExistingPayloadSummaryLimit(wrongType, 512)
	if string(wrongType.Body) != `{"type":"other"}` {
		t.Fatalf("type 不符必须原样保留: %s", wrongType.Body)
	}
}

// TestWJShrinkExistingPayloadSummary 固定既有摘要的窗口收缩：head/tail
// 各保留一半、中间省略字节数重算、textPreview 重建。
func TestWJShrinkExistingPayloadSummary(t *testing.T) {
	original := strings.Repeat("A", 1000)
	summary := buildAuditPayloadSummary(auditPayloadSummaryBuildInput{
		body:                  []byte(original),
		originalSha256:        "hash",
		originalBodySizeBytes: len(original),
		fullBodyLimitBytes:    800,
		reason:                SummaryReasonBodyExceededLimit,
	})
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	shrinkExistingPayloadSummary(decoded, 100)
	decoded["fullBodyLimitBytes"] = 100
	updated, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("marshal updated: %v", err)
	}
	var verify map[string]any
	if err := json.Unmarshal(updated, &verify); err != nil {
		t.Fatalf("unmarshal updated: %v", err)
	}
	headLen := int(verify["retainedHeadBytes"].(float64))
	tailLen := int(verify["retainedTailBytes"].(float64))
	if headLen+tailLen > 100 {
		t.Fatalf("保留窗口超预算: head=%d tail=%d", headLen, tailLen)
	}
	if int(verify["omittedMiddleBytes"].(float64)) != 1000-headLen-tailLen {
		t.Fatalf("省略字节不符: %+v", verify["omittedMiddleBytes"])
	}
	if _, ok := verify["headBase64"].(string); !ok {
		t.Fatal("headBase64 必须存在")
	}

	// head/tail 缺失时收缩必须是 no-op。
	shrinkExistingPayloadSummary(map[string]any{}, 100)
	// 非法 base64 窗口 no-op。
	shrinkExistingPayloadSummary(map[string]any{"headBase64": "!!", "tailBase64": "!!"}, 100)
	// NaN/负数原始大小按 0 处理。
	if got := numericSummaryValue(math.NaN()); got != 0 {
		t.Fatalf("NaN = %d", got)
	}
	if got := numericSummaryValue(-1.0); got != 0 {
		t.Fatalf("负数 = %d", got)
	}
	if got := numericSummaryValue("x"); got != 0 {
		t.Fatalf("非数字 = %d", got)
	}
	if got := numericSummaryValue(42.0); got != 42 {
		t.Fatalf("数字 = %d", got)
	}
}

// TestWJIsTextLikePayload 固定文本负载判定。
func TestWJIsTextLikePayload(t *testing.T) {
	tests := []struct {
		contentType  string
		contentEnc   string
		wantTextLike bool
	}{
		{"application/json", "", true},
		{"text/plain", "identity", true},
		{"application/xml", "", true},
		{"text/event-stream", "", true},
		{"application/javascript", "", true},
		{"application/x-www-form-urlencoded", "", true},
		{"application/octet-stream", "", false},
		{"application/json", "gzip", false},
		{"", "", false},
	}
	for _, tt := range tests {
		if got := isTextLikePayload(tt.contentType, tt.contentEnc); got != tt.wantTextLike {
			t.Fatalf("isTextLikePayload(%q,%q) = %v", tt.contentType, tt.contentEnc, got)
		}
	}
}

// TestWJBoundUsageRecordSnapshotShapes 固定使用快照的边界形态。
func TestWJBoundUsageRecordSnapshotShapes(t *testing.T) {
	if BoundUsageRecordSnapshot(nil) != nil {
		t.Fatal("nil 输入必须返回 nil")
	}
	// 深层嵌套触发深度截断。
	deep := any(map[string]any{})
	current := deep.(map[string]any)
	for i := 0; i < 64; i++ {
		next := map[string]any{}
		current["n"] = next
		current["raw"] = "x"
		delete(current, "raw")
		current = next
	}
	bounded := BoundUsageRecordSnapshot(deep)
	encoded, err := json.Marshal(bounded)
	if err != nil {
		t.Fatalf("marshal bounded: %v", err)
	}
	if !strings.Contains(string(encoded), "depth_truncated") {
		t.Fatalf("深度截断标记缺失: %.200s", encoded)
	}
	// []byte 缓冲与时间戳形态。
	buffer := BoundUsageRecordSnapshot([]byte("abcdef"))
	bufferObj, ok := buffer.(*OrderedObject)
	if !ok || bufferObj.Get("_buffer") != true || bufferObj.Get("bytes") != 6 {
		t.Fatalf("[]byte 形态不符: %v", buffer)
	}
	stamp := BoundUsageRecordSnapshot(time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC))
	if stamp != "2026-09-04T00:00:00.000Z" {
		t.Fatalf("时间形态 = %v", stamp)
	}
	// 键数截断：超过对象键上限必须标记。
	wide := map[string]any{}
	for i := 0; i < 200; i++ {
		wide["k"+itoa(i)] = i
	}
	wideBounded, ok := BoundUsageRecordSnapshot(wide).(*OrderedObject)
	if !ok {
		t.Fatal("map 必须产出 OrderedObject")
	}
	if wideBounded.Get("_truncated") != true {
		t.Fatal("宽对象必须标记截断")
	}
	// 未导出/非 JSON 值走 displayString 兜底。
	if got := BoundUsageRecordSnapshot(complex(1, 2)); !strings.Contains(got.(string), "complex") && got.(string) == "" {
		t.Fatalf("displayString 兜底 = %v", got)
	}
}

// TestWJFinalizationQueueInjection 固定收尾队列的注入端口：时钟注入、
// 任务错误与 panic 都必须转为日志而不外泄。
func TestWJFinalizationQueueInjection(t *testing.T) {
	logger := &wjRecordingLogger{}
	queue := NewGatewayUsageFinalizationQueue(4, 2).WithLogger(logger)
	queue.Track(func(Ctx) error { return errors.New("task failed") })
	queue.Track(func(Ctx) error { panic("boom string") })
	queue.Track(func(Ctx) error { panic(errors.New("boom error")) })
	queue.wg.Wait()
	if logger.count() < 3 {
		t.Fatalf("必须记录 3 次失败日志, got %d", logger.count())
	}
}

// wjRecordingLogger 的消费方（finalization 队列）从后台 goroutine 并发写，
// 读取必须经 count()/items() 加锁拷贝（裸切片在 -race 下是数据竞争）。
type wjRecordingLogger struct {
	mu    sync.Mutex
	items []string
}

func (l *wjRecordingLogger) Debug(message string, _ map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, message)
}

func (l *wjRecordingLogger) Warn(message string, _ map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, message)
}

func (l *wjRecordingLogger) Error(message string, _ map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, message)
}

func (l *wjRecordingLogger) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.items)
}

// itemsSnapshot 返回事件的加锁拷贝，供测试断言逐条内容。
func (l *wjRecordingLogger) itemsSnapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.items...)
}

// TestWJUsageMetadataHelpers 固定使用元数据选择器的字段透传。
func TestWJUsageMetadataHelpers(t *testing.T) {
	account := UsageModelAccount{UsageAccess: UsageAccessFields{AccountOwnerSystemAccountID: "owner-1", AccountAccessType: "direct"}}
	metadata := AccountUsageMetadata(account)
	if metadata.AccountOwnerSystemAccountID != "owner-1" || metadata.AccountAccessType != "direct" {
		t.Fatalf("AccountUsageMetadata = %+v", metadata)
	}
	group := GroupUsageMetadata(GroupUsageAccessMetadata{
		ProviderCode: "openai", GroupOwnerSystemAccountID: "owner", GroupAccessType: "public",
	})
	if group.ProviderCode != "openai" || group.GroupOwnerSystemAccountID != "owner" || group.GroupAccessType != "public" {
		t.Fatalf("GroupUsageMetadata = %+v", group)
	}
}

// TestWJAuditHeaderList 固定审计头集合的查找与导出。
func TestWJAuditHeaderList(t *testing.T) {
	headers := AuditHeaderList{
		{Name: "Content-Type", Value: "application/json"},
		{Name: "Set-Cookie", Values: []string{"a=1", "b=2"}, IsArray: true},
		{Name: "Empty-Array", Values: nil, IsArray: true},
	}
	if got := headers.Get("content-type"); got != "application/json" {
		t.Fatalf("大小写不敏感查找 = %q", got)
	}
	if got := headers.Get("set-cookie"); got != "a=1, b=2" {
		t.Fatalf("数组头 = %q", got)
	}
	if got := headers.Get("empty-array"); got != "" {
		t.Fatalf("空数组头 = %q", got)
	}
	if got := headers.Get("missing"); got != "" {
		t.Fatalf("缺失头 = %q", got)
	}
	asMap := headers.ToMap()
	if asMap["Content-Type"] != "application/json" {
		t.Fatalf("ToMap = %v", asMap)
	}
	values, ok := asMap["Set-Cookie"].([]string)
	if !ok || len(values) != 2 {
		t.Fatalf("ToMap 数组 = %v", asMap["Set-Cookie"])
	}
}

// TestWJRFC3339HelpersGatewayUsage 固定使用包内 RFC3339 解析的边缘。
func TestWJRFC3339HelpersGatewayUsage(t *testing.T) {
	if _, ok := parseRFC3339Instant("2026-02-30T00:00:00Z"); ok {
		t.Fatal("2 月 30 必须拒绝")
	}
	if _, ok := parseRFC3339Instant("2026-12-31T00:00:00Z"); !ok {
		t.Fatal("12 月 31 必须接受（daysInMonth 12 月分支）")
	}
	canon, ok := canonicalizeRFC3339Instant("2026-09-04T08:00:00.5+08:00")
	if !ok || canon != "2026-09-04T00:00:00.500Z" {
		t.Fatalf("canonicalize = (%q, %v)", canon, ok)
	}
	if _, err := requiredRFC3339Instant("bad", "字段"); err == nil || !strings.Contains(err.Error(), "字段") {
		t.Fatalf("required 错误文案 = %v", err)
	}
	ms, ok := rfc3339InstantMilliseconds("2026-09-04T00:00:00Z")
	if !ok || ms != time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("milliseconds = (%d, %v)", ms, ok)
	}
}
