package circuitstate

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// REFACTOR-0008 下潜符号的最小回归：覆盖 JSON 容忍语义、深拷贝、脚本占位
// 与纯函数关键正常/异常/边界路径。全部为纯函数，无外部依赖，可回放。

func TestStringListUnmarshalJSONToleratesLuaShapes(t *testing.T) {
	cases := []struct {
		raw    string
		want   StringList
		wantEq bool // 期望与零值 nil 相等
	}{
		{raw: `{}`, want: StringList{}},
		{raw: `[]`, want: StringList{}},
		{raw: `null`},
		{raw: `["a","b"]`, want: StringList{"a", "b"}},
	}
	for _, c := range cases {
		var got StringList
		if err := json.Unmarshal([]byte(c.raw), &got); err != nil {
			t.Fatalf("Unmarshal(%q) error: %v", c.raw, err)
		}
		if len(got) != len(c.want) {
			t.Fatalf("Unmarshal(%q) = %#v, want %#v", c.raw, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("Unmarshal(%q) = %#v, want %#v", c.raw, got, c.want)
			}
		}
	}
	var bad StringList
	if err := json.Unmarshal([]byte(`123`), &bad); err == nil {
		t.Fatalf("Unmarshal(number) must fail")
	}
	// 空串在 encoding/json 入口层即失败，不会到达 UnmarshalJSON 的防御分支。
	var empty StringList
	if err := json.Unmarshal([]byte(``), &empty); err == nil {
		t.Fatalf("empty input must fail at the json entry layer")
	}
}

func TestStateListUnmarshalJSONAndSlice(t *testing.T) {
	var list StateList
	if err := json.Unmarshal([]byte(`{}`), &list); err != nil || list != nil {
		t.Fatalf("empty Lua object must decode nil: %v %#v", err, list)
	}
	if err := json.Unmarshal([]byte(`[{"scopeKey":"s","phase":"CLOSED"}]`), &list); err != nil {
		t.Fatalf("decode states: %v", err)
	}
	slice := list.Slice()
	if len(slice) != 1 || slice[0].ScopeKey != "s" {
		t.Fatalf("Slice() = %#v", slice)
	}
	slice[0].ScopeKey = "mutated"
	if list[0].ScopeKey != "s" {
		t.Fatalf("Slice must copy: %#v", list)
	}
	var nilList StateList
	if nilList.Slice() != nil {
		t.Fatalf("nil StateList Slice must stay nil")
	}
}

func TestMutationResultRelatedStatesSlice(t *testing.T) {
	if got := (MutationResult{}).RelatedStatesSlice(); got != nil {
		t.Fatalf("empty RelatedStatesSlice = %#v", got)
	}
	result := MutationResult{RelatedStates: StateList{{ScopeKey: "s"}}}
	if got := result.RelatedStatesSlice(); len(got) != 1 || got[0].ScopeKey != "s" {
		t.Fatalf("RelatedStatesSlice = %#v", got)
	}
}

func TestCloneStateDeepCopiesPointersAndLists(t *testing.T) {
	reason := "boom"
	lease := Lease{Kind: "recovery", LeaseID: "l1", LeaseUntilMs: 5}
	state := State{
		Scope:               Scope{Kind: "account", AccountRuntimeKey: "acc"},
		Phase:               "SUSPECT",
		FailureReason:       &reason,
		Lease:               &lease,
		FailureEvidenceKeys: StringList{"a"},
		ChildScopeKeys:      StringList{"b", "c"},
	}
	cloned := CloneState(state)
	// 原版语义：只深拷贝 Lease、Scope 与五个列表；FailureReason 等 *string
	// 字段与 Node clone 一致保持浅拷贝，不得在此改变。
	if cloned.Lease == state.Lease {
		t.Fatalf("CloneState must copy Lease pointer")
	}
	if cloned.FailureEvidenceKeys == nil || &cloned.FailureEvidenceKeys[0] == &state.FailureEvidenceKeys[0] {
		t.Fatalf("CloneState must copy list backing arrays")
	}
	if cloned.FailureReason == nil || *cloned.FailureReason != "boom" {
		t.Fatalf("shallow FailureReason must keep value")
	}
	if cloned.Scope != state.Scope {
		t.Fatalf("scope mismatch")
	}
}

func TestDecodeStrictRejectsTrailingJSON(t *testing.T) {
	var result MutationResult
	if err := DecodeStrict(`{"status":"applied","state":{"phase":"CLOSED"}}`, &result); err != nil {
		t.Fatalf("DecodeStrict: %v", err)
	}
	if result.Status != "applied" {
		t.Fatalf("status = %q", result.Status)
	}
	if err := DecodeStrict(`{"status":"applied"} {"status":"x"}`, &result); err == nil {
		t.Fatalf("trailing JSON must be rejected")
	}
	if err := DecodeStrict(`{`, &result); err == nil {
		t.Fatalf("truncated JSON must be rejected")
	}
}

func TestParseSafeIntegerBoundaries(t *testing.T) {
	if _, ok := ParseSafeInteger("9007199254740991"); !ok {
		t.Fatalf("max safe integer must parse")
	}
	if _, ok := ParseSafeInteger("9007199254740992"); ok {
		t.Fatalf("beyond max safe integer must not parse")
	}
	if _, ok := ParseSafeInteger(" 3 "); !ok {
		t.Fatalf("trimmed decimal must parse")
	}
	if _, ok := ParseSafeInteger("v1:abc"); ok {
		t.Fatalf("opaque digest must not parse")
	}
	if _, ok := ParseSafeInteger(""); ok {
		t.Fatalf("empty must not parse")
	}
	if _, ok := ParseSafeInteger("NaN"); ok {
		t.Fatalf("NaN must not parse")
	}
	if value, ok := ParseSafeInteger("3"); !ok || value != 3 {
		t.Fatalf("ParseSafeInteger(3) = %v %v", value, ok)
	}
}

func TestRedisResultHelpers(t *testing.T) {
	if value, ok := RedisStringResult("raw"); !ok || value != "raw" {
		t.Fatalf("RedisStringResult(string) = %q %v", value, ok)
	}
	if value, ok := RedisStringResult([]byte("raw")); !ok || value != "raw" {
		t.Fatalf("RedisStringResult(bytes) = %q %v", value, ok)
	}
	if _, ok := RedisStringResult(42); ok {
		t.Fatalf("RedisStringResult(int) must not be ok")
	}
	if value, err := NumericRedisResult(int64(7)); err != nil || value != 7 {
		t.Fatalf("NumericRedisResult(int64) = %v %v", value, err)
	}
	if value, err := NumericRedisResult(7.0); err != nil || value != 7 {
		t.Fatalf("NumericRedisResult(float) = %v %v", value, err)
	}
	if _, err := NumericRedisResult("x"); err == nil {
		t.Fatalf("NumericRedisResult(bad string) must fail")
	}
	if _, err := NumericRedisResult(nil); err == nil {
		t.Fatalf("NumericRedisResult(nil) must fail")
	}
	if got := PointerNowMs(map[string]any{"nowMs": int64(3)}); got == nil || *got != 3 {
		t.Fatalf("PointerNowMs(int64) = %v", got)
	}
	if got := PointerNowMs(map[string]any{}); got != nil {
		t.Fatalf("PointerNowMs(missing) = %v", got)
	}
	if got := NormalizedNowValue(nil, nil); got != 0 {
		t.Fatalf("NormalizedNowValue(nil,nil) = %d", got)
	}
	negative := int64(-5)
	if got := NormalizedNowValue(&negative, nil); got != 0 {
		t.Fatalf("negative now must clamp to zero, got %d", got)
	}
}

func TestValidationHelpers(t *testing.T) {
	if _, err := RequiredValue(" ", "name"); err == nil {
		t.Fatalf("blank value must fail")
	}
	if value, err := RequiredValue(" v ", "name"); err != nil || value != "v" {
		t.Fatalf("RequiredValue = %q %v", value, err)
	}
	if _, err := RequiredScopePart("", "accountRuntimeKey"); err == nil {
		t.Fatalf("blank scope part must fail")
	}
	if got := EncodedScopeKey("account", "acc"); got != "7:account|3:acc" {
		t.Fatalf("EncodedScopeKey = %q", got)
	}
	if !IsSHA256Hex(strings.Repeat("a", 64)) {
		t.Fatalf("64 lowercase hex must pass")
	}
	if IsSHA256Hex(strings.Repeat("A", 64)) || IsSHA256Hex(strings.Repeat("a", 63)) {
		t.Fatalf("uppercase/short must fail")
	}
	if err := RequiredEvidenceKeyPayload(map[string]any{"k": strings.Repeat("a", 64)}, "k"); err != nil {
		t.Fatalf("valid evidence key: %v", err)
	}
	if err := RequiredEvidenceKeyPayload(map[string]any{"k": "nope"}, "k"); err == nil {
		t.Fatalf("invalid evidence key must fail")
	}
	if err := RequiredEvidenceKeyPayload(map[string]any{"k": ""}, "k"); err == nil {
		t.Fatalf("empty evidence key must fail")
	}
	if value, ok := PayloadInt64(2.0); !ok || value != 2 {
		t.Fatalf("PayloadInt64(float) = %v %v", value, ok)
	}
	if _, ok := PayloadInt64("2"); ok {
		t.Fatalf("PayloadInt64(string) must not be ok")
	}
	if got := Int64Min(2, 3); got != 2 {
		t.Fatalf("Int64Min = %d", got)
	}
	if got := SHA1Hex("abc"); got != "a9993e364706816aba3e25717850c26c9cd0d89d" {
		t.Fatalf("SHA1Hex(abc) = %q", got)
	}
	if got := CursorString("", "done"); got != "done" {
		t.Fatalf("CursorString(empty) = %q", got)
	}
	if got := CursorString(3.0, "done"); got != "3" {
		t.Fatalf("CursorString(float) = %q", got)
	}
	if StrPtr("v") == nil || *StrPtr("v") != "v" {
		t.Fatalf("StrPtr broken")
	}
	if !*BoolPtr(true) {
		t.Fatalf("BoolPtr broken")
	}
	if DerefString(nil) != "" || DerefString(StrPtr("v")) != "v" {
		t.Fatalf("DerefString broken")
	}
	if _, err := PositiveInteger(0, "capacity"); err == nil {
		t.Fatalf("PositiveInteger(0) must fail")
	}
}

func TestParseListDuePagePaths(t *testing.T) {
	if _, err := ParseListDuePage(""); err == nil {
		t.Fatalf("empty page must fail")
	}
	if _, err := ParseListDuePage(`not-json`); err == nil {
		t.Fatalf("bad json must fail")
	}
	if _, err := ParseListDuePage(`{"scopeKeys":null,"scanned":1,"nextOffset":0}`); err == nil {
		t.Fatalf("missing scopeKeys must fail")
	}
	if _, err := ParseListDuePage(`{"scopeKeys":[],"scanned":-1,"nextOffset":0}`); err == nil {
		t.Fatalf("negative scanned must fail")
	}
	page, err := ParseListDuePage(`{"scopeKeys":["a",2],"scanned":3,"nextOffset":4,"exhausted":true}`)
	if err != nil {
		t.Fatalf("valid page: %v", err)
	}
	if len(page.ScopeKeys) != 2 || page.ScopeKeys[1] != "2" || page.Scanned != 3 ||
		page.NextOffset != 4 || !page.Exhausted {
		t.Fatalf("page = %#v", page)
	}
	// Lua cjson 把空数组编码为 `{}`：空 due 页必须等价于空列表（2026-09-25
	// 生产 account-circuit-recovery 连续失败根因）。
	emptyObject, err := ParseListDuePage(`{"exhausted":true,"scanned":0,"scopeKeys":{},"nextOffset":0}`)
	if err != nil {
		t.Fatalf("lua empty-object page must parse: %v", err)
	}
	if len(emptyObject.ScopeKeys) != 0 || !emptyObject.Exhausted || emptyObject.Scanned != 0 {
		t.Fatalf("empty-object page = %#v", emptyObject)
	}
}

func TestEncodeSourceFenceShape(t *testing.T) {
	fence := ProbeSourceFence{StateKey: "k", AccountID: "acc", SourceGeneration: 7, SourceFenceID: "f"}
	if got := EncodeSourceFence(fence); got != `["k","acc",7,"f"]` {
		t.Fatalf("EncodeSourceFence = %q", got)
	}
}

func TestScriptConstantsVerbatimMarkers(t *testing.T) {
	scripts := map[string]string{
		"ScriptTransition":      ScriptTransition,
		"ScriptSize":            ScriptSize,
		"ScriptRestore":         ScriptRestore,
		"ScriptListDue":         ScriptListDue,
		"ScriptEscalation":      ScriptEscalation,
		"ScriptClearEscalation": ScriptClearEscalation,
		"ScriptAccountRevision": ScriptAccountRevision,
	}
	for name, script := range scripts {
		if !strings.Contains(script, "redis.call") {
			t.Fatalf("%s lost redis.call body", name)
		}
		if strings.Contains(script, "\r\n") {
			t.Fatalf("%s must keep LF line endings", name)
		}
	}
	// 转移脚本承载容量/回放上限参数位与 cjson 解析，是最长的一份契约。
	if len(ScriptTransition) < len(ScriptAccountRevision) {
		t.Fatalf("ScriptTransition unexpectedly shorter than ScriptAccountRevision")
	}
	if math.MinInt64 >= 0 {
		t.Fatalf("sanity")
	}
}
