package gatewayhybrid

// w9e 覆盖率战役：补 jsonx/scoring/affinity/auxiliary/view 的纯函数分支、
// LRU 缓存驱逐与请求视图辅助。全部本地构造，无外部依赖。

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func TestW9EOrderedJSONOperations(t *testing.T) {
	object := NewOrderedJSON()
	if object.Len() != 0 || len(object.Keys()) != 0 {
		t.Fatal("新对象应为空")
	}
	if _, ok := object.Get("missing"); ok {
		t.Fatal("缺失键不应命中")
	}
	if _, ok := object.GetString("missing"); ok {
		t.Fatal("缺失键 GetString 不应命中")
	}
	object.Set("b", 1.0)
	object.Set("a", "text")
	object.Set("b", 2.0) // 已存在键保持原位置
	if strings.Join(object.Keys(), ",") != "b,a" {
		t.Fatalf("keys = %v", object.Keys())
	}
	if v, ok := object.Get("b"); !ok || v != 2.0 {
		t.Fatalf("Get b = %v %v", v, ok)
	}
	if _, ok := object.GetString("b"); ok {
		t.Fatal("数字键 GetString 不应命中")
	}
	if text, ok := object.GetString("a"); !ok || text != "text" {
		t.Fatalf("GetString a = %q %v", text, ok)
	}
	clone := object.Clone()
	clone.Set("c", true)
	if object.Len() != 2 || clone.Len() != 3 {
		t.Fatal("Clone 必须独立于原对象")
	}
	object.Delete("missing")
	object.Delete("b")
	if object.Len() != 1 || strings.Join(object.Keys(), ",") != "a" {
		t.Fatalf("删除后 keys = %v", object.Keys())
	}
	zero := &OrderedJSON{}
	zero.Set("k", nil)
	if zero.Len() != 1 {
		t.Fatal("零值 Set 应初始化")
	}
}

func TestW9ECloneJSONValueAndPredicates(t *testing.T) {
	nested := map[string]any{"k": []any{1.0, "s", nil, true}}
	cloned := cloneJSONValue(nested)
	typed := cloned.(map[string]any)
	if len(typed) != 1 {
		t.Fatal("map 克隆长度不符")
	}
	if IsUndefined(nil) || IsUndefined("x") || !IsUndefined(Undefined) {
		t.Fatal("IsUndefined 契约不符")
	}
	if !IsJSONObject(NewOrderedJSON()) || IsJSONObject([]any{}) || IsJSONObject("s") {
		t.Fatal("IsJSONObject 契约不符")
	}
	if !IsArray([]any{}) || IsArray(NewOrderedJSON()) {
		t.Fatal("IsArray 契约不符")
	}
}

func TestW9EParseJSONOrderedNested(t *testing.T) {
	parsed, err := ParseJSONOrdered([]byte(`{"a":{"b":[1,{"c":[true,null,"s"]},[[2]]]},"z":-1.5}`))
	if err != nil {
		t.Fatal(err)
	}
	if rendered := NodeJSONStringify(parsed); rendered != `{"a":{"b":[1,{"c":[true,null,"s"]},[[2]]]},"z":-1.5}` {
		t.Fatalf("rendered = %s", rendered)
	}
	array, err := ParseJSONOrdered([]byte(`[{"k":1},[]]`))
	if err != nil {
		t.Fatal(err)
	}
	if NodeJSONStringify(array) != `[{"k":1},[]]` {
		t.Fatalf("array = %s", NodeJSONStringify(array))
	}
	if _, err := ParseJSONOrdered([]byte(`{bad`)); err == nil {
		t.Fatal("坏 JSON 必须失败")
	}
}

func TestW9ENodeJSONStringifyEscapesAndNumbers(t *testing.T) {
	input := "quote\"back\\slash\n\t\r\b\f" + string(rune(1))
	want := "\"quote\\\"back\\\\slash\\n\\t\\r\\b\\f\\u0001\""
	if got := NodeJSONStringify(input); got != want {
		t.Fatalf("escape = %s want %s", got, want)
	}
	if got := NodeJSONStringify(math.NaN()); got != "null" {
		t.Fatalf("NaN = %s", got)
	}
	if got := NodeJSONStringify(math.Inf(-1)); got != "null" {
		t.Fatalf("-Inf = %s", got)
	}
	if got := NodeJSONStringify(-0.0); got != "0" {
		t.Fatalf("-0 = %s", got)
	}
	if got := NodeJSONStringify(1e21); got != "1e+21" {
		t.Fatalf("1e21 = %s", got)
	}
	if got := NodeJSONStringify(map[string]any{"b": 1.0, "a": 2.0}); got != `{"a":2,"b":1}` {
		t.Fatalf("map = %s", got)
	}
}

func TestW9ENodeNumberCoercion(t *testing.T) {
	cases := []struct {
		in   any
		want float64
		ok   bool
	}{
		{3.5, 3.5, true},
		{float32(1.5), 1.5, true},
		{7, 7, true},
		{int64(9), 9, true},
		{json.Number("2.25"), 2.25, true},
		{true, 1, true},
		{false, 0, true},
		{nil, 0, true},
		{" 42 ", 42, true},
		{"", 0, true},
		{"Infinity", math.Inf(1), true},
		{"-Infinity", math.Inf(-1), true},
		{"nope", 0, false},
		{[]any{1}, 0, false},
		{NewOrderedJSON(), 0, false},
	}
	for _, tc := range cases {
		got, ok := NodeNumber(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Fatalf("NodeNumber(%v) = %v %v, want %v %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestW9EOrderedNavigationHelpers(t *testing.T) {
	if OrderedChild(nil, "k") != nil || OrderedChildArray(nil, "k") != nil || OrderedString(nil, "k") != "" {
		t.Fatal("nil 对象辅助应安全返回")
	}
	if OrderedValue(nil, "k") != nil || OrderedValueOrUndefined(nil, "k") != Undefined {
		t.Fatal("nil 取值辅助不符")
	}
	object := mustParseObjectW9E(t, `{"obj":{"x":1},"arr":[{"y":2},3],"s":"v","n":5}`)
	if OrderedChild(object, "missing") != nil {
		t.Fatal("缺失子对象应为 nil")
	}
	if OrderedChild(object, "s") != nil {
		t.Fatal("非对象子值应为 nil")
	}
	if got := OrderedChildArray(object, "obj"); got != nil {
		t.Fatal("非数组子值应为 nil")
	}
	if OrderedChildObjectAtIndex(object, "arr", 5) != nil || OrderedChildObjectAtIndex(object, "arr", -1) != nil {
		t.Fatal("越界索引应为 nil")
	}
	if OrderedChildObjectAtIndex(object, "arr", 1) != nil {
		t.Fatal("非对象元素应为 nil")
	}
	entry := OrderedChildObjectAtIndex(object, "arr", 0)
	if entry == nil {
		t.Fatal("对象元素应可解析")
	}
	if OrderedValueOrUndefined(object, "missing") != Undefined {
		t.Fatal("缺失键应返回 Undefined")
	}
	if OrderedString(object, "s") != "v" || OrderedString(object, "n") != "" {
		t.Fatal("OrderedString 契约不符")
	}
}

func mustParseObjectW9E(t *testing.T, text string) *OrderedJSON {
	t.Helper()
	parsed, err := ParseJSONOrdered([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	object, _ := parsed.(*OrderedJSON)
	if object == nil {
		t.Fatalf("非对象: %s", text)
	}
	return object
}

func TestW9EUTF16Helpers(t *testing.T) {
	if utf16Length("abc") != 3 {
		t.Fatal("ASCII 长度不符")
	}
	if utf16Length("\U0001F600") != 2 {
		t.Fatal("代理对应为 2 个 UTF-16 单元")
	}
	if got := truncateUTF16("hello", 3); got != "hel" {
		t.Fatalf("truncate = %q", got)
	}
	if got := truncateUTF16("hi", 5); got != "hi" {
		t.Fatalf("短串截断 = %q", got)
	}
	emoji := "a\U0001F600b"
	if got := truncateUTF16(emoji, 2); got != "a" {
		t.Fatalf("代理对中截断 = %q", got)
	}
}

func TestW9ESortedMapKeysAndSortStrings(t *testing.T) {
	if got := sortedMapKeys(map[string]any{"b": 1, "a": 2, "c": 3}); strings.Join(got, ",") != "a,b,c" {
		t.Fatalf("sortedMapKeys = %v", got)
	}
	values := []string{"c", "a", "b"}
	sortStrings(values)
	if strings.Join(values, ",") != "a,b,c" {
		t.Fatalf("sortStrings = %v", values)
	}
	if jsonAnyString([]any{"a", 1, true, nil, map[string]any{"k": "v"}, []any{"x", 2}}) == "" {
		t.Fatal("jsonAnyString 数组分支为空")
	}
}

func TestW9EHybridLRUCacheEviction(t *testing.T) {
	base := time.Unix(1000, 0)
	now := base
	cache := newHybridLRUCache(2)
	entry := func(reason string) HybridScoringCacheEntry {
		return HybridScoringCacheEntry{Reason: &reason}
	}
	cache.set("a", entry("1"), time.Minute, now)
	cache.set("b", entry("2"), time.Minute, now)
	if _, ok := cache.get("a", now); !ok {
		t.Fatal("a 应命中")
	}
	cache.set("c", entry("3"), time.Minute, now)
	if _, ok := cache.get("b", now); ok {
		t.Fatal("b 应被驱逐（LRU）")
	}
	now = base.Add(2 * time.Minute)
	if _, ok := cache.get("a", now); ok {
		t.Fatal("a 已过期")
	}
	cache.set("d", entry("4"), time.Minute, now)
	cache.removeLocked("d")
	if _, ok := cache.get("d", now); ok {
		t.Fatal("d 应被显式移除")
	}
	cache.removeLocked("missing")
	cache.clear()
	if len(cache.entries) != 0 {
		t.Fatal("clear 后应为空")
	}
}

func TestW9EAuxiliaryFinishOnce(t *testing.T) {
	calls := 0
	ctx := context.Background()
	once := AuxiliaryFinishOnce(func(ctx context.Context, finish AuxiliaryDispatchFinishInput) error {
		calls++
		return nil
	}, ctx)
	once(AuxiliaryDispatchFinishInput{})
	once(AuxiliaryDispatchFinishInput{})
	if calls != 1 {
		t.Fatalf("finish 调用次数 = %d, want 1", calls)
	}
}

func TestW9EToNativeValueArrayAndObject(t *testing.T) {
	object := mustParseObjectW9E(t, `{"arr":[{"n":1},"s",null],"num":2}`)
	native := ToNativeValue(object)
	typed, ok := native.(map[string]any)
	if !ok {
		t.Fatalf("native = %#v", native)
	}
	array, ok := typed["arr"].([]any)
	if !ok || len(array) != 3 {
		t.Fatalf("arr = %#v", typed["arr"])
	}
	if _, ok := array[0].(map[string]any); !ok {
		t.Fatalf("arr[0] = %#v", array[0])
	}
	if got := ToNativeValue(3.5); got != 3.5 {
		t.Fatalf("标量应原样返回, got %#v", got)
	}
}

func TestW9ENonStreamJSONBodyFromValue(t *testing.T) {
	body := NonStreamJSONBodyFromValue(map[string]any{"k": "v"})
	if body.Status != "valid" {
		t.Fatalf("status = %q", body.Status)
	}
	if body.Value == nil {
		t.Fatal("value 应存在")
	}
}
