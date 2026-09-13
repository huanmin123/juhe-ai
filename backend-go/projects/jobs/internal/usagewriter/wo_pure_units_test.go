package usagewriter

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---- OrderedObject：JS 对象插入序语义的字节级持久化契约 ----

func TestOrderedObjectInsertionOrderAndAccessors(t *testing.T) {
	object := NewOrderedObject()
	object.Set("b", 1)
	object.Set("a", 2)
	object.Set("b", 3) // 替换不改变首次插入位置（JS 语义）
	if got := strings.Join(object.Keys(), ","); got != "b,a" {
		t.Fatalf("键序应保持首次插入顺序，实际: %s", got)
	}
	if object.Len() != 2 {
		t.Fatalf("Len 应为 2，实际: %d", object.Len())
	}
	if object.Get("b") != 3 {
		t.Fatalf("替换后的值应为 3，实际: %v", object.Get("b"))
	}
	if object.Get("missing") != nil {
		t.Fatalf("缺失键应返回 nil")
	}
	if !object.Has("a") || object.Has("missing") {
		t.Fatalf("Has 应区分存在与缺失键")
	}
	var nilObject *OrderedObject
	if nilObject.Get("k") != nil || nilObject.Has("k") || nilObject.Len() != 0 || nilObject.Keys() != nil {
		t.Fatalf("nil 接收者的访问器应安全返回零值")
	}
}

func TestOrderedObjectMarshalJSONPreservesOrder(t *testing.T) {
	var nilObject *OrderedObject
	encoded, err := json.Marshal(nilObject)
	if err != nil {
		t.Fatalf("nil 对象序列化不应报错: %v", err)
	}
	if string(encoded) != "null" {
		t.Fatalf("nil 对象应序列化为 null，实际: %s", encoded)
	}
	object := NewOrderedObject()
	object.Set("z", 1)
	object.Set("a", NewOrderedObject().Set("n", json.Number("2.5")))
	encoded, err = json.Marshal(object)
	if err != nil {
		t.Fatalf("序列化不应报错: %v", err)
	}
	// 契约：字节输出必须保持插入顺序，嵌套对象走 MarshalJSON 递归。
	if string(encoded) != `{"z":1,"a":{"n":2.5}}` {
		t.Fatalf("字节序契约被破坏，实际: %s", encoded)
	}
}

func TestOrderedObjectUnmarshalJSONRoundTrip(t *testing.T) {
	var object OrderedObject
	input := `{"z":1,"a":{"k":true},"arr":[3,{"m":null}],"t":"2026-01-02T03:04:05.000Z"}`
	if err := json.Unmarshal([]byte(input), &object); err != nil {
		t.Fatalf("反序列化不应报错: %v", err)
	}
	if got := strings.Join(object.Keys(), ","); got != "z,a,arr,t" {
		t.Fatalf("解码应保留键序，实际: %s", got)
	}
	// 数字经 UseNumber 保留原文，嵌套对象归一化为 *OrderedObject。
	if object.Get("z") != json.Number("1") {
		t.Fatalf("顶层数字应保留为 json.Number，实际: %T %#v", object.Get("z"), object.Get("z"))
	}
	nested, ok := object.Get("a").(*OrderedObject)
	if !ok {
		t.Fatalf("嵌套对象应归一化为 *OrderedObject，实际: %T", object.Get("a"))
	}
	if nested.Get("k") != true {
		t.Fatalf("嵌套标量应原样保留，实际: %v", nested.Get("k"))
	}
	array, ok := object.Get("arr").([]any)
	if !ok {
		t.Fatalf("数组应原样保留为 []any，实际: %T", object.Get("arr"))
	}
	inner, ok := array[1].(*OrderedObject)
	if !ok || inner.Get("m") != nil {
		t.Fatalf("数组内嵌套对象应归一化，实际: %T %#v", array[1], array[1])
	}
	// 重新序列化必须稳定（可回放）。
	encoded, err := json.Marshal(&object)
	if err != nil {
		t.Fatalf("重序列化不应报错: %v", err)
	}
	var again OrderedObject
	if err := json.Unmarshal(encoded, &again); err != nil {
		t.Fatalf("二次反序列化不应报错: %v", err)
	}
	if strings.Join(again.Keys(), ",") != strings.Join(object.Keys(), ",") {
		t.Fatalf("round-trip 后键序应一致")
	}
}

func TestOrderedObjectUnmarshalJSONRejectsInvalidInput(t *testing.T) {
	var object OrderedObject
	if err := object.UnmarshalJSON([]byte(`[1,2]`)); !errors.Is(err, strconv.ErrSyntax) {
		t.Fatalf("非对象输入应返回 strconv.ErrSyntax，实际: %v", err)
	}
	if err := object.UnmarshalJSON([]byte(`not-json`)); err == nil {
		t.Fatalf("非法 JSON 应返回错误")
	}
	// null 输入清空对象但不算错误（对应 JS JSON.parse("null")）。
	if err := object.UnmarshalJSON([]byte(`{"k":1}`)); err != nil {
		t.Fatalf("预置内容失败: %v", err)
	}
	if err := object.UnmarshalJSON([]byte(`null`)); err != nil {
		t.Fatalf("null 输入不应报错: %v", err)
	}
}

// ---- RFC3339 / 分片 ID 纯函数 ----

func TestRFC3339InstantMilliseconds(t *testing.T) {
	valid := "2026-01-02T03:04:05.123Z"
	expected := time.Date(2026, 1, 2, 3, 4, 5, 123_000_000, time.UTC).UnixMilli()
	if got, ok := rfc3339InstantMilliseconds(valid); !ok || got != expected {
		t.Fatalf("毫秒换算不符，got=%d ok=%v want=%d", got, ok, expected)
	}
	// 带数值 offset 且毫秒一致的时刻与 UTC 等值。
	if got, ok := rfc3339InstantMilliseconds("2026-01-02T09:04:05.123+06:00"); !ok || got != expected {
		t.Fatalf("offset 换算不符，got=%d ok=%v", got, ok)
	}
	if _, ok := rfc3339InstantMilliseconds("not-a-time"); ok {
		t.Fatalf("非法时间不应成功")
	}
}

func TestDaysInMonthTable(t *testing.T) {
	cases := []struct {
		year  int
		month int
		want  int
	}{
		{2024, 2, 29}, {2023, 2, 28}, {2024, 4, 30}, {2024, 1, 31}, {2024, 12, 31},
	}
	for _, tc := range cases {
		if got := daysInMonth(tc.year, tc.month); got != tc.want {
			t.Fatalf("daysInMonth(%d,%d)=%d，期望 %d", tc.year, tc.month, got, tc.want)
		}
	}
}

func TestFormatShardIDAndLogicalVariants(t *testing.T) {
	// 契约：SQLite 路径两位、Postgres 逻辑路径三位，负值钳制为 0。
	if got := FormatShardID(5); got != "05" {
		t.Fatalf("FormatShardID(5)=%s，期望 05", got)
	}
	if got := FormatShardID(-3); got != "00" {
		t.Fatalf("负分片应钳制为 00，实际: %s", got)
	}
	if got := FormatLogicalShardID(7); got != "007" {
		t.Fatalf("FormatLogicalShardID(7)=%s，期望 007", got)
	}
	if got := FormatLogicalShardID(-1); got != "000" {
		t.Fatalf("负逻辑分片应钳制为 000，实际: %s", got)
	}
}

func TestBucketDateKeyFromClockUsesUTCDate(t *testing.T) {
	// 23:30 UTC+8 的本地日期与 UTC 日期不同，契约要求按 UTC 取日期键。
	clock := ClockFunc(func() time.Time {
		return time.Date(2026, 9, 10, 23, 30, 0, 0, time.FixedZone("UTC+8", 8*3600))
	})
	if got := BucketDateKeyFromClock(clock); got != "20260910" {
		t.Fatalf("日期键应取 UTC 日期 20260910，实际: %s", got)
	}
}

// ---- 语义解析器 ----

func TestUsageSemanticResolverFallbacks(t *testing.T) {
	resolver := DefaultUsageSemanticResolver{}
	if got := resolver.UsageSemanticForID("openai"); got.ID() != "openai" {
		t.Fatalf("openai 语义应可解析，实际: %s", got.ID())
	}
	// 未知 id 回退到 OpenAI 默认语义（当前实现两个分支同型）。
	if got := resolver.UsageSemanticForID("unknown"); got.ID() != "openai" {
		t.Fatalf("未知语义应回退默认，实际: %s", got.ID())
	}
	if got := SemanticForID(nil, "openai"); got.ID() != "openai" {
		t.Fatalf("nil resolver 应回退默认语义，实际: %v", got)
	}
	if got := SemanticForID(resolver, "openai"); got.ID() != "openai" {
		t.Fatalf("非 nil resolver 应直接委托，实际: %s", got.ID())
	}
}

// ---- JSON 字节估算（visitJSONLikeValue 全分支） ----

func TestEstimateJSONLikeBytesScalarTypes(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  int
	}{
		{"nil", nil, 4},
		{"string", "abc", 5},
		{"bool", true, 4},
		{"int", 12345, 5},
		{"int64", int64(-123), 4},
		{"uint64", uint64(99), 2},
		{"float", 1.5, 3},
		{"json.Number", json.Number("42"), 2},
		{"bytes", []byte("xyz"), 3},
		{"time", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), 26},
		{"empty-slice", []any{}, 2},
		{"channel-unknown", make(chan int), 16},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EstimateJSONLikeBytes(tc.value, EstimateJSONLikeBytesOptions{MaxBytes: 0, MaxNodes: 0})
			if got != tc.want {
				t.Fatalf("估算不符: got=%d want=%d", got, tc.want)
			}
		})
	}
}

func TestEstimateJSONLikeBytesComposites(t *testing.T) {
	nested := map[string]any{"k": []any{1, "two"}}
	base := EstimateJSONLikeBytes(nested, EstimateJSONLikeBytesOptions{})
	if base <= 0 {
		t.Fatalf("嵌套结构估算应为正: %d", base)
	}
	ordered := NewOrderedObject()
	ordered.Set("a", 1)
	if got := EstimateJSONLikeBytes(ordered, EstimateJSONLikeBytesOptions{}); got <= 0 {
		t.Fatalf("OrderedObject 估算应为正: %d", got)
	}
	// 循环引用必须终止并计入固定代价，不允许死循环。
	// 外层 2 字节（括号）+ 循环标记 16 字节 + 逗号 1 字节 = 19。
	circular := []any{nil}
	circular[0] = circular
	if got := EstimateJSONLikeBytes(circular, EstimateJSONLikeBytesOptions{}); got != 19 {
		t.Fatalf("循环引用应计 19 字节，实际: %d", got)
	}
	// 注意：*OrderedObject / 结构体的循环引用会使估算路径无限递归
	// （identitySet 只跟踪 Slice/Map 且估算路径无深度上限），因此这里
	// 不构造对象循环用例；该差异已作为疑似问题单独上报。
	// MaxBytes 是硬上限：超限截断后不再增长。
	if got := EstimateJSONLikeBytes(strings.Repeat("a", 1000), EstimateJSONLikeBytesOptions{MaxBytes: 100}); got != 100 {
		t.Fatalf("超限估算应钳制到 MaxBytes=100，实际: %d", got)
	}
}

func TestRuneUTF8ByteLengthTable(t *testing.T) {
	cases := []struct {
		leading byte
		want    int
	}{
		{'a', 1}, {0x7F, 1}, {0xC3, 2}, {0xDF, 2}, {0xE4, 3}, {0xEF, 3}, {0xF0, 4}, {0xFF, 4},
	}
	for _, tc := range cases {
		if got := runeUTF8ByteLength(tc.leading); got != tc.want {
			t.Fatalf("runeUTF8ByteLength(0x%02X)=%d，期望 %d", tc.leading, got, tc.want)
		}
	}
}

func TestSliceStringByUTF8BytesCutsAtRuneBoundary(t *testing.T) {
	// "中" 占 3 字节；4 字节预算只能完整保留 1 个汉字，不能切半个字符。
	value := "中文"
	if got := sliceStringByUTF8Bytes(value, 4); got != "中" {
		t.Fatalf("应按 rune 边界截断为中，实际: %q", got)
	}
	if got := sliceStringByUTF8Bytes(value, 6); got != "中文" {
		t.Fatalf("预算充足时不应截断，实际: %q", got)
	}
	if got := sliceStringByUTF8Bytes(value, 0); got != "" {
		t.Fatalf("零预算应返回空串，实际: %q", got)
	}
}
