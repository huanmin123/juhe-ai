package gatewayusage

// w13d 波次补充测试：JSON 有序对象、字节估算、RFC3339 与流量来源 helpers。
//
// 2026-11-09 T189：文件 spool 生产代码及其错误分支测试已随网关直投 PG 删除。

import (
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// jsonx
// ---------------------------------------------------------------------------

func TestW13DOrderedObjectEdges(t *testing.T) {
	// nil 接收者的各方法。
	var nilObject *OrderedObject
	if nilObject.Get("k") != nil || nilObject.Has("k") || nilObject.Len() != 0 || nilObject.Keys() != nil {
		t.Fatal("nil 接收者必须安全")
	}
	if cloned := nilObject.Clone(); cloned != nil {
		t.Fatal("nil Clone 必须返回 nil")
	}
	encoded, err := nilObject.MarshalJSON()
	if err != nil || string(encoded) != "null" {
		t.Fatalf("nil Marshal=%s err=%v", encoded, err)
	}
	// Set 在零值对象上初始化 values。
	zero := &OrderedObject{}
	zero.Set("a", 1)
	if zero.Len() != 1 {
		t.Fatal("零值 Set 必须初始化")
	}
	// Clone 深拷贝嵌套数组与对象。
	original := NewOrderedObject()
	inner := NewOrderedObject()
	inner.Set("x", 1)
	original.Set("nested", inner)
	original.Set("list", []any{inner, 2})
	cloned := original.Clone()
	cloned.Get("nested").(*OrderedObject).Set("x", 99)
	if inner.Get("x").(int) != 1 {
		t.Fatal("Clone 必须深拷贝")
	}
	clonedList := cloned.Get("list").([]any)
	clonedList[0].(*OrderedObject).Set("x", 77)
	if inner.Get("x").(int) != 1 {
		t.Fatal("数组元素必须深拷贝")
	}
	// MarshalJSON 的 null 值。
	withNil := NewOrderedObject()
	withNil.Set("v", nil)
	encoded, err = withNil.MarshalJSON()
	if err != nil || string(encoded) != `{"v":null}` {
		t.Fatalf("encoded=%s err=%v", encoded, err)
	}
	// UnmarshalJSON：null token、非法 token、截断、坏 value。
	var decoded OrderedObject
	if err := decoded.UnmarshalJSON([]byte(`null`)); err != nil {
		t.Fatalf("null 解码必须安全: %v", err)
	}
	if err := decoded.UnmarshalJSON([]byte(`[1]`)); err == nil {
		t.Fatal("非对象必须报错")
	}
	if err := decoded.UnmarshalJSON([]byte(`{"a"`)); err == nil {
		t.Fatal("截断输入必须报错")
	}
	if err := decoded.UnmarshalJSON([]byte(`{"a" 1}`)); err == nil {
		t.Fatal("坏 value 必须报错")
	}
	// 大整数经 UseNumber 保留。
	if err := decoded.UnmarshalJSON([]byte(`{"n":1234567890123456789}`)); err != nil {
		t.Fatal(err)
	}
	if decoded.Get("n") == nil {
		t.Fatal("n 必须存在")
	}
	// formatFloat 的整型与小数分支。
	if formatFloat(3) != "3" || formatFloat(-2.5) != "-2.5" {
		t.Fatal("formatFloat 错误")
	}
}

// ---------------------------------------------------------------------------
// size
// ---------------------------------------------------------------------------

func TestW13DEstimateJSONLikeBytesEdges(t *testing.T) {
	// 循环引用：slice 与 map。
	circular := make([]any, 1)
	circular[0] = circular
	if EstimateJSONLikeBytes(circular, EstimateJSONLikeBytesOptions{}) < 16 {
		t.Fatal("循环 slice 必须按占位计费")
	}
	circularMap := map[string]any{"self": nil}
	circularMap["self"] = circularMap
	if EstimateJSONLikeBytes(circularMap, EstimateJSONLikeBytesOptions{}) < 16 {
		t.Fatal("循环 map 必须按占位计费")
	}
	// nil 指针与非 nil 指针字段。
	type withPointer struct {
		Ptr *int `json:"ptr"`
	}
	if EstimateJSONLikeBytes(withPointer{}, EstimateJSONLikeBytesOptions{}) <= 0 {
		t.Fatal("nil 指针字段必须可估算")
	}
	value := 5
	if EstimateJSONLikeBytes(withPointer{Ptr: &value}, EstimateJSONLikeBytesOptions{}) <= 0 {
		t.Fatal("指针字段必须可估算")
	}
	// 超长字符串走 len*4 上限。
	long := strings.Repeat("汉", 17*1024)
	if got := estimateStringBytes(long, &jsonLikeEstimateContext{maxBytes: -1}); got <= 0 {
		t.Fatal("长字符串必须估算")
	}
	// sliceStringByUTF8Bytes 的截断边界。
	if got := sliceStringByUTF8Bytes("héllo", 2); got != "h" {
		t.Fatalf("slice=%q", got)
	}
	if got := sliceStringByUTF8Bytes("héllo", 3); got != "hé" {
		t.Fatalf("slice=%q", got)
	}
	if got := sliceStringByUTF8Bytes("abc", 0); got != "" {
		t.Fatalf("slice=%q", got)
	}
	// runeUTF8ByteLength 各分支。
	if runeUTF8ByteLength('a') != 1 || runeUTF8ByteLength(0xC3) != 2 || runeUTF8ByteLength(0xE4) != 3 || runeUTF8ByteLength(0xF0) != 4 {
		t.Fatal("rune 长度错误")
	}
	// boundedStringByteLength。
	if got := boundedStringByteLength("abc", 10); got != 3 {
		t.Fatalf("bounded=%d", got)
	}
	if got := boundedStringByteLength("abcdef", 4); got != 4 {
		t.Fatalf("bounded=%d", got)
	}
	if got := boundedStringByteLength("abc", 0); got != 0 {
		t.Fatalf("bounded=%d", got)
	}
	// 估算上限截断（MaxBytes / MaxNodes）。
	huge := EstimateJSONLikeBytes(NewOrderedObject().Set("k", strings.Repeat("a", 100)), EstimateJSONLikeBytesOptions{MaxBytes: 10})
	if huge > 10 {
		t.Fatalf("maxBytes 截断失败: %d", huge)
	}
	if EstimateJSONLikeBytes([]any{1, 2, 3, 4}, EstimateJSONLikeBytesOptions{MaxNodes: 2}) == 0 {
		t.Fatal("maxNodes 限制必须产出估算")
	}
}

// ---------------------------------------------------------------------------
// rfc3339 / trafficsource / ports
// ---------------------------------------------------------------------------

func TestW13DRFC3339Edges(t *testing.T) {
	// 负 offset 与分数秒。
	parsed, ok := parseRFC3339Instant("2026-01-02T03:04:05.5-08:00")
	if !ok {
		t.Fatal("负 offset 必须可解析")
	}
	if parsed.UTC().Format(timeRFC3339Millis) != "2026-01-02T11:04:05.500Z" {
		t.Fatalf("parsed=%s", parsed.UTC().Format(timeRFC3339Millis))
	}
	// 非法日历日期。
	if _, ok := parseRFC3339Instant("2026-02-30T00:00:00Z"); ok {
		t.Fatal("2 月 30 日必须失败")
	}
	// 超长分数秒。
	if _, ok := parseRFC3339Instant("2026-01-02T03:04:05.1234567890Z"); ok {
		t.Fatal("超长分数秒必须失败")
	}
	// requiredRFC3339Instant 的中文错误。
	if _, err := requiredRFC3339Instant("nope", "测试字段"); err == nil || !strings.Contains(err.Error(), "测试字段") {
		t.Fatalf("err=%v", err)
	}
	// 闰年 2 月 29 日合法。
	if _, ok := canonicalizeRFC3339Instant("2024-02-29T00:00:00Z"); !ok {
		t.Fatal("闰年 2 月 29 日必须合法")
	}
}

type w13dStringer struct{}

func (w13dStringer) String() string { return "stringer-value" }

func TestW13DTrafficSourceEdges(t *testing.T) {
	// nil → gateway。
	if normalized, err := NormalizeOpenAIGatewayTrafficSource(nil); err != nil || normalized != TrafficSourceGateway {
		t.Fatalf("nil 必须回落 gateway: (%v, %v)", normalized, err)
	}
	// 非法值错误（Stringer / 数字 / 未知字符串）。
	if _, err := NormalizeOpenAIGatewayTrafficSource(w13dStringer{}); err == nil || !strings.Contains(err.Error(), "stringer-value") {
		t.Fatalf("Stringer 错误=%v", err)
	}
	if _, err := NormalizeOpenAIGatewayTrafficSource(42); err == nil || !strings.Contains(err.Error(), "42") {
		t.Fatalf("数字错误=%v", err)
	}
	if _, err := NormalizeOpenAIGatewayTrafficSource("nope"); err == nil {
		t.Fatal("未知字符串必须报错")
	}
	// 判定 helpers。
	if !IsCooldownRetestTrafficSource(TrafficSourceCooldownRetest) || IsCooldownRetestTrafficSource(TrafficSourceGateway) {
		t.Fatal("cooldown 判定错误")
	}
	if IsAccountProbeTrafficSource(TrafficSourceManualAccountTest) || !IsAccountProbeTrafficSource(TrafficSourceAccountHealthCheck) {
		t.Fatal("probe 判定错误")
	}
	if !IsAccountDiagnosticTrafficSource(TrafficSourceManualAccountTest) || !IsAccountDiagnosticTrafficSource(TrafficSourceRuntimeRecoveryProbe) {
		t.Fatal("diagnostic 判定错误")
	}
	// ClockFunc 适配器。
	if ClockFunc(func() time.Time { return time.UnixMilli(42) }).Now().UnixMilli() != 42 {
		t.Fatal("ClockFunc 错误")
	}
}
