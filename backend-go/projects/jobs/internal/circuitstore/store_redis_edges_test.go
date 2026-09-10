package circuitstore

// RedisStore / OpsJobsStore 适配面的边界测试：键空间纯函数、状态归一化、
// Lua 转移参数校验、revision 替换、升级证据清除与 codec 小工具。
// miniredis 驱动，时钟全部注入固定值保证可重放。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	miniredis "github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

// fullSHA256Hex 计算完整 SHA256 hex（与 NormalizeFailureEvidenceKey 回退一致）。
func fullSHA256Hex(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// TestStringListUnmarshalJSON 锁定 Lua cjson 空数组兼容解码。
func TestStringListUnmarshalJSON(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantLen   int
		wantNil   bool
		wantError bool
	}{
		{name: "null为nil", raw: "null", wantNil: true},
		{name: "对象空壳为空切片", raw: "{}", wantLen: 0},
		{name: "数组空壳为空切片", raw: "[]", wantLen: 0},
		{name: "普通数组", raw: `["a","b"]`, wantLen: 2},
		{name: "非法JSON报错", raw: "{broken", wantError: true},
		{name: "非字符串元素报错", raw: "[1]", wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var list stringList
			err := json.Unmarshal([]byte(tc.raw), &list)
			if tc.wantError {
				if err == nil {
					t.Fatal("非法输入必须报错")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantNil && list != nil {
				t.Fatalf("应解码为 nil: %v", list)
			}
			if !tc.wantNil && len(list) != tc.wantLen {
				t.Fatalf("长度不符: %v", list)
			}
		})
	}
	var list stringList
	if list.clone() != nil {
		t.Fatal("nil clone 应保持 nil")
	}
	// 空串分支由直接调用 UnmarshalJSON 覆盖（encoding/json 对空输入
	// 在进入自定义解码器前即报错）。
	var direct stringList
	if err := direct.UnmarshalJSON([]byte("")); err != nil || direct != nil {
		t.Fatalf("空串应解码为 nil: %v %v", direct, err)
	}
	populated := stringList{"a"}
	cloned := populated.clone()
	cloned[0] = "changed"
	if populated[0] != "a" {
		t.Fatal("clone 必须深拷贝")
	}
}

// TestStateListDecode 覆盖 relatedStates 的空壳与 slice 语义。
func TestStateListDecode(t *testing.T) {
	for _, raw := range []string{"null", "{}", "[]"} {
		var list stateList
		if err := json.Unmarshal([]byte(raw), &list); err != nil {
			t.Fatalf("空壳 %q 不得报错: %v", raw, err)
		}
		if list.slice() != nil {
			t.Fatalf("nil stateList slice 应为 nil: %v", list)
		}
	}
	var direct stateList
	if err := direct.UnmarshalJSON([]byte("")); err != nil || direct != nil {
		t.Fatalf("空串应解码为 nil: %v %v", direct, err)
	}
	var list stateList
	if err := json.Unmarshal([]byte(`[{"phase":"OPEN"}]`), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.slice()) != 1 || list.slice()[0].Phase != "OPEN" {
		t.Fatalf("slice 应拷贝元素: %v", list)
	}
}

// TestScopeKeyValidation 覆盖三种 scope kind 与错误分支。
func TestScopeKeyValidation(t *testing.T) {
	base := Scope{Kind: "account", AccountRuntimeKey: "acc-1"}
	if _, err := ScopeKey(Scope{Kind: "account", AccountRuntimeKey: "  "}); err == nil {
		t.Fatal("缺 accountRuntimeKey 必须报错")
	}
	if _, err := ScopeKey(Scope{Kind: "bogus", AccountRuntimeKey: "acc-1"}); err == nil {
		t.Fatal("未知 kind 必须报错")
	}
	if _, err := ScopeKey(Scope{Kind: "key", AccountRuntimeKey: "acc-1"}); err == nil {
		t.Fatal("key scope 缺指纹必须报错")
	}
	keyScope := Scope{Kind: "key", AccountRuntimeKey: "acc-1", KeyFingerprint: "fp"}
	keyEncoded, err := ScopeKey(keyScope)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(keyEncoded, "fp") || !strings.HasPrefix(keyEncoded, "3:key|") {
		t.Fatalf("key scope 编码不符: %s", keyEncoded)
	}
	if _, err := ScopeKey(Scope{Kind: "protocol_model", AccountRuntimeKey: "acc-1", ProtocolProfile: "p", RequestLane: "text"}); err == nil {
		t.Fatal("protocol_model 缺 modelBucket 必须报错")
	}
	if _, err := ScopeKey(Scope{Kind: "protocol_model", AccountRuntimeKey: "acc-1", ProtocolProfile: "p", RequestLane: "audio", ModelBucket: "m"}); err == nil {
		t.Fatal("非法 requestLane 必须报错")
	}
	if _, err := ScopeKey(Scope{Kind: "protocol_model", AccountRuntimeKey: "acc-1", RequestLane: "text", ModelBucket: "m"}); err == nil {
		t.Fatal("protocol_model 缺 protocolProfile 必须报错")
	}
	laneScope := Scope{Kind: "protocol_model", AccountRuntimeKey: "acc-1", ProtocolProfile: "p", RequestLane: "image", ModelBucket: "m"}
	laneEncoded, err := ScopeKey(laneScope)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(laneEncoded, "image") {
		t.Fatalf("protocol_model 编码应含 lane: %s", laneEncoded)
	}
	accountEncoded, err := ScopeKey(base)
	if err != nil {
		t.Fatal(err)
	}
	if accountEncoded != MustScopeKey(base) {
		t.Fatal("MustScopeKey 必须与 ScopeKey 一致")
	}
}

// TestNormalizeConfirmationState 覆盖 SUSPECT 状态的归一化（计数夹取、
// 证据去重截断、retryAt 回填）。
func TestNormalizeConfirmationState(t *testing.T) {
	closed := State{Phase: "CLOSED"}
	normalized, err := normalizeConfirmationState(closed)
	if err != nil || normalized.Phase != "CLOSED" {
		t.Fatalf("CLOSED 必须短路直通: %+v %v", normalized, err)
	}
	if _, err := normalizeConfirmationState(State{
		Phase:                        "SUSPECT",
		ConfirmationFailuresRequired: &[]int64{9}[0],
	}); err == nil {
		t.Fatal("required 超界必须报错")
	}
	if _, err := normalizeConfirmationState(State{
		Phase:                    "SUSPECT",
		ConfirmationFailureCount: &[]int64{6}[0],
	}); err == nil {
		t.Fatal("count 超界必须报错")
	}

	validKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	anotherKey := "1111111111111111111111111111111111111111111111111111111111111111"
	state := State{
		Phase:                        "SUSPECT",
		Generation:                   2,
		UpdatedAtMs:                  777,
		ConfirmationFailuresRequired: &[]int64{1}[0],
		ConfirmationFailureCount:     &[]int64{3}[0],
		FailureEvidenceKeys:          stringList{strings.ToUpper(validKey), "  " + validKey + " ", "bad-key", anotherKey},
	}
	normalized, err = normalizeConfirmationState(state)
	if err != nil {
		t.Fatal(err)
	}
	if *normalized.ConfirmationFailureCount != 3 {
		t.Fatalf("count 应保留: %v", *normalized.ConfirmationFailureCount)
	}
	// 去重后保留 required+1 = 2 条最新证据。
	if len(normalized.FailureEvidenceKeys) != 2 {
		t.Fatalf("证据应去重并截断到 required+1: %v", normalized.FailureEvidenceKeys)
	}
	if normalized.FailureEvidenceKeys[0] != validKey || normalized.FailureEvidenceKeys[1] != anotherKey {
		t.Fatalf("证据应归一为小写并保持顺序: %v", normalized.FailureEvidenceKeys)
	}
	// SUSPECT 无 retryAt → 回填 updatedAt。
	if normalized.RetryAtMs == nil || *normalized.RetryAtMs != 777 {
		t.Fatalf("SUSPECT 缺 retryAt 应回填 updatedAt: %v", normalized.RetryAtMs)
	}
	// SUSPECT 有 lease → 回填 lease 截止。
	leased := state
	leased.Lease = &Lease{Kind: "confirmation", LeaseID: "l-1", LeaseUntilMs: 999}
	normalized, err = normalizeConfirmationState(leased)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.RetryAtMs == nil || *normalized.RetryAtMs != 999 {
		t.Fatalf("有 lease 时 retryAt 应回填 lease 截止: %v", normalized.RetryAtMs)
	}
	// 非 SUSPECT 不回填。
	open := state
	open.Phase = "OPEN"
	normalized, err = normalizeConfirmationState(open)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.RetryAtMs != nil {
		t.Fatalf("非 SUSPECT 不得回填 retryAt: %v", normalized.RetryAtMs)
	}
}

// TestNormalizeFailureEvidenceKey 覆盖显式键、回退种子与缺种子错误。
func TestNormalizeFailureEvidenceKey(t *testing.T) {
	valid := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	got, err := NormalizeFailureEvidenceKey(&valid, "seed")
	if err != nil || got != valid {
		t.Fatalf("合法 SHA256 应原样返回: %s %v", got, err)
	}
	upper := strings.ToUpper(valid)
	got, err = NormalizeFailureEvidenceKey(&upper, "seed")
	if err != nil || got != valid {
		t.Fatalf("大写应归一为小写: %s %v", got, err)
	}
	invalid := "not-a-key"
	got, err = NormalizeFailureEvidenceKey(&invalid, "transition-1")
	if err != nil {
		t.Fatal(err)
	}
	if !isSHA256Hex(got) {
		t.Fatalf("回退种子应产出 SHA256: %s", got)
	}
	expected := fullSHA256Hex("transition-1")
	if got != expected {
		t.Fatalf("回退种子不符: %s 期望 %s", got, expected)
	}
	if _, err := NormalizeFailureEvidenceKey(nil, "   "); err == nil {
		t.Fatal("缺 fallbackSeed 必须报错")
	}
}

// TestNormalizeConfirmationFailuresRequired 覆盖 legacy 回落与边界。
func TestNormalizeConfirmationFailuresRequired(t *testing.T) {
	if got, err := NormalizeConfirmationFailuresRequired(nil, 3); err != nil || got != 3 {
		t.Fatalf("nil 应回落 fallback: %d %v", got, err)
	}
	if got, err := NormalizeConfirmationFailuresRequired(&[]int64{5}[0], 1); err != nil || got != 5 {
		t.Fatalf("显式值优先: %d %v", got, err)
	}
	if _, err := NormalizeConfirmationFailuresRequired(&[]int64{0}[0], 1); err == nil {
		t.Fatal("0 必须报错")
	}
	if _, err := NormalizeConfirmationFailuresRequired(&[]int64{6}[0], 1); err == nil {
		t.Fatal("6 必须报错")
	}
}

// TestClosedAndCapacityStates 锁定终态与容量耗尽状态形状。
func TestClosedAndCapacityStates(t *testing.T) {
	scope := Scope{Kind: "account", AccountRuntimeKey: "acc-1"}
	closed := ClosedState(scope, "7", 3, "tr-x", 1234)
	if closed.Phase != "CLOSED" || closed.ScopeKey != MustScopeKey(scope) || closed.Generation != 3 {
		t.Fatalf("CLOSED 状态形状不符: %+v", closed)
	}
	exhausted := CapacityExhaustedState(scope, "7", 2000)
	if exhausted.Phase != "SUSPECT" || exhausted.FailureReason == nil || *exhausted.FailureReason != "runtime_state_capacity_exhausted" {
		t.Fatalf("容量耗尽状态形状不符: %+v", exhausted)
	}
	if exhausted.RetryAtMs == nil || *exhausted.RetryAtMs != 3000 {
		t.Fatalf("容量耗尽应短重试（now+1000）: %v", exhausted.RetryAtMs)
	}
}

// TestCloneStateIndependence 验证 CloneState 的指针/切片深拷贝。
func TestCloneStateIndependence(t *testing.T) {
	retry := int64(1)
	state := State{
		Scope:               Scope{Kind: "account", AccountRuntimeKey: "acc-1"},
		RetryAtMs:           &retry,
		Lease:               &Lease{Kind: "half_open", LeaseID: "l", LeaseUntilMs: 2},
		FailureEvidenceKeys: stringList{"a"},
		ChildScopeKeys:      stringList{"s"},
	}
	cloned := CloneState(state)
	cloned.Lease.LeaseID = "mutated"
	cloned.FailureEvidenceKeys[0] = "mutated"
	cloned.ChildScopeKeys[0] = "mutated"
	if state.Lease.LeaseID != "l" || state.FailureEvidenceKeys[0] != "a" || state.ChildScopeKeys[0] != "s" {
		t.Fatalf("CloneState 必须深拷贝 lease 与切片字段: %+v", state)
	}
}

// TestStoreCodecsAndParsers 覆盖 codec / 解析小工具的错误分支。
func TestStoreCodecsAndParsers(t *testing.T) {
	// numericRedisResult。
	if got, err := numericRedisResult(int64(3)); err != nil || got != 3 {
		t.Fatalf("int64 直通: %v %v", got, err)
	}
	if got, _ := numericRedisResult(3.0); got != 3 {
		t.Fatalf("float64 截断: %v", got)
	}
	if got, _ := numericRedisResult("42"); got != 42 {
		t.Fatalf("字符串解析: %v", got)
	}
	if _, err := numericRedisResult("x"); err == nil {
		t.Fatal("非数字字符串必须报错")
	}
	if _, err := numericRedisResult(nil); err == nil {
		t.Fatal("未知类型必须报错")
	}
	// redisStringResult。
	if text, ok := redisStringResult("s"); !ok || text != "s" {
		t.Fatalf("string 直通: %s %v", text, ok)
	}
	if text, ok := redisStringResult([]byte("b")); !ok || text != "b" {
		t.Fatalf("bytes 转换: %s %v", text, ok)
	}
	if _, ok := redisStringResult(7); ok {
		t.Fatal("数值不得当字符串")
	}
	// cursorString。
	if got := cursorString("c-1", "done"); got != "c-1" {
		t.Fatalf("字符串 cursor 直通: %s", got)
	}
	if got := cursorString("", "done"); got != "done" {
		t.Fatalf("空 cursor 回落: %s", got)
	}
	if got := cursorString(float64(12), "done"); got != "12" {
		t.Fatalf("数值 cursor 转十进制串: %s", got)
	}
	if got := cursorString(nil, "done"); got != "done" {
		t.Fatalf("nil cursor 回落: %s", got)
	}
	// decodeStrict 尾随内容。
	var value struct {
		A int `json:"a"`
	}
	if err := decodeStrict(`{"a":1} trailing`, &value); err == nil {
		t.Fatal("尾随内容必须报错")
	}
	if err := decodeStrict(`{"a":1}`, &value); err != nil || value.A != 1 {
		t.Fatalf("合法 JSON 解析: %+v %v", value, err)
	}
	// parseSafeInteger。
	if got, ok := parseSafeInteger(" 12 "); !ok || got != 12 {
		t.Fatalf("安全整数解析: %v %v", got, ok)
	}
	if _, ok := parseSafeInteger("1.5"); ok {
		t.Fatal("小数不是安全整数")
	}
	if _, ok := parseSafeInteger("1e20"); ok {
		t.Fatal("超出安全范围必须拒绝")
	}
	if _, ok := parseSafeInteger(""); ok {
		t.Fatal("空串必须拒绝")
	}
	if _, ok := parseSafeInteger("NaN"); ok {
		t.Fatal("NaN 必须拒绝")
	}
	// isSHA256Hex。
	if isSHA256Hex(strings.ToUpper(strings.Repeat("a", 64))) {
		t.Fatal("大写不是合法 SHA256 形状（调用方先归一）")
	}
	if isSHA256Hex(strings.Repeat("a", 63) + "g") {
		t.Fatal("非 hex 字符必须拒绝")
	}
	// normalizedNowValue。
	if got := normalizedNowValue(&[]int64{-5}[0], nil); got != 0 {
		t.Fatalf("负 now 截 0: %d", got)
	}
	if got := normalizedNowValue(nil, func() int64 { return 42 }); got != 42 {
		t.Fatalf("nil now 走 fallback: %d", got)
	}
	// positiveInteger。
	if _, err := positiveInteger(0, "limit"); err == nil {
		t.Fatal("0 必须报错")
	}
	if got, err := positiveInteger(2, "limit"); err != nil || got != 2 {
		t.Fatalf("合法值直通: %d %v", got, err)
	}
	// int64Min / pointerNowMs。
	if int64Min(1, 2) != 1 || int64Min(3, 2) != 2 {
		t.Fatal("int64Min 语义不符")
	}
	if got := pointerNowMs(map[string]any{"nowMs": int64(5)}); got == nil || *got != 5 {
		t.Fatalf("int64 nowMs 提取: %v", got)
	}
	if got := pointerNowMs(map[string]any{"nowMs": (*int64)(nil)}); got != nil {
		t.Fatalf("nil 指针 nowMs: %v", got)
	}
	// sha1Hex 长度。
	if len(sha1Hex("x")) != 40 {
		t.Fatal("sha1 hex 长度应为 40")
	}
	// accountCircuitDueAtMs。
	closedDue := accountCircuitDueAtMs(State{Phase: "CLOSED"})
	if closedDue != 9223372036854775807 {
		t.Fatalf("CLOSED 永不到期: %d", closedDue)
	}
	leaseDue := accountCircuitDueAtMs(State{Phase: "HALF_OPEN", Lease: &Lease{LeaseUntilMs: 55}})
	if leaseDue != 55 {
		t.Fatalf("HALF_OPEN 以 lease 截止为准: %d", leaseDue)
	}
	if got := accountCircuitDueAtMs(State{Phase: "OPEN"}); got != 9223372036854775807 {
		t.Fatalf("OPEN 无 retryAt 永不到期: %d", got)
	}
	if got := accountCircuitDueAtMs(State{Phase: "SUSPECT", RetryAtMs: &[]int64{9}[0]}); got != 9 {
		t.Fatalf("SUSPECT 以 retryAt 为准: %d", got)
	}
}

// TestRedisKeyNaming 锁定键空间命名与 namespace 归一化。
func TestRedisKeyNaming(t *testing.T) {
	keys := redisAccountCircuitStoreKeys("gateway-account-circuit", "dev space")
	if !strings.HasPrefix(keys.states, "juhe-ai:dev_space:account-circuit:gateway-account-circuit:states") {
		t.Fatalf("namespace 非法字符应折叠为下划线: %s", keys.states)
	}
	// 空白 name（TrimSpace 后为空）回落默认 store 名。
	fallback := redisAccountCircuitStoreKeys("   ", "ns")
	if !strings.Contains(fallback.states, "gateway-account-circuit") {
		t.Fatalf("空白 name 应回落默认 store 名: %s", fallback.states)
	}
	// sanitizeRedisName。
	if got := sanitizeRedisName(" a b/c "); got != "a_b_c" {
		t.Fatalf("sanitizeRedisName: %q", got)
	}
	// redisNamespacedKey：已带 ns 前缀幂等、根前缀插入、无前缀拼接。
	if got := redisNamespacedKey("juhe-ai:ns:k", "ns"); got != "juhe-ai:ns:k" {
		t.Fatalf("已带 namespace 前缀必须幂等: %s", got)
	}
	if got := redisNamespacedKey("juhe-ai:k", "ns"); got != "juhe-ai:ns:k" {
		t.Fatalf("根前缀后插入 namespace: %s", got)
	}
	if got := redisNamespacedKey("plain", ""); got != "plain" {
		t.Fatalf("空 namespace 原样返回: %s", got)
	}
	// sanitizeRedisNamespacePart：非法折叠 + 首尾下划线修剪。
	if got := sanitizeRedisNamespacePart("  a@@b  "); got != "a_b" {
		t.Fatalf("sanitizeRedisNamespacePart: %q", got)
	}
	// sanitizeRedisNamespacePart：非法折叠 + 首尾下划线修剪（'.' 属合法字符）。
	if got := sanitizeRedisNamespacePart("@@@"); got != "" {
		t.Fatalf("全非法折叠修剪后应为空: %q", got)
	}
}

// TestNewRedisStoreValidation 覆盖构造参数校验分支。
func TestNewRedisStoreValidation(t *testing.T) {
	if _, err := NewRedisStore(RedisStoreOptions{Capacity: 1}); err == nil {
		t.Fatal("无 URL 且无 Client 必须报错")
	}
	if _, err := NewRedisStore(RedisStoreOptions{RedisURL: "://bad", Capacity: 1}); err == nil {
		t.Fatal("非法 URL 必须报错")
	}
	server := miniredis.RunT(t)
	t.Cleanup(func() { server.Close() })
	if _, err := NewRedisStore(RedisStoreOptions{RedisURL: "redis://" + server.Addr(), Capacity: 0}); err == nil {
		t.Fatal("capacity 必须是正整数")
	}
	if _, err := NewRedisStore(RedisStoreOptions{RedisURL: "redis://" + server.Addr(), Capacity: 1, ClosedRetentionMs: -1}); err == nil {
		t.Fatal("closedRetentionMs 必须是正整数")
	}
	if _, err := NewRedisStore(RedisStoreOptions{RedisURL: "redis://" + server.Addr(), Capacity: 1, ReplayLimitPerScope: -2}); err == nil {
		t.Fatal("replayLimitPerScope 必须是正整数")
	}
	// 注入 go-redis Client（连 miniredis）+ 显式参数直通。
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewRedisStore(RedisStoreOptions{
		Client:              client,
		Namespace:           "ns",
		Capacity:            3,
		ClosedRetentionMs:   1000,
		ReplayLimitPerScope: 8,
		Now:                 func() int64 { return 5 },
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.capacity != 3 || store.replayLimitPerScope != 8 {
		t.Fatalf("显式参数应生效: %+v", store)
	}
	// 注入 Client 时 Close 为空操作。
	if err := store.Close(); err != nil {
		t.Fatalf("注入 Client 的 Close 应为空操作: %v", err)
	}
	// 自建连接 Close 不报错。
	urlStore, err := NewRedisStore(RedisStoreOptions{RedisURL: "redis://" + server.Addr(), Capacity: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := urlStore.Close(); err != nil {
		t.Fatalf("自建连接 Close: %v", err)
	}
}

// TestParseListDuePageValidation 覆盖 due 分页解析的错误分支。
func TestParseListDuePageValidation(t *testing.T) {
	if _, err := parseListDuePage(""); err == nil {
		t.Fatal("空返回必须报错")
	}
	if _, err := parseListDuePage("{broken"); err == nil {
		t.Fatal("非法 JSON 必须报错")
	}
	if _, err := parseListDuePage(`{"scopeKeys":null,"scanned":0,"nextOffset":0}`); err == nil {
		t.Fatal("缺 scopeKeys 必须报错")
	}
	if _, err := parseListDuePage(`{"scopeKeys":[],"scanned":-1,"nextOffset":0}`); err == nil {
		t.Fatal("负 scanned 必须报错")
	}
	if _, err := parseListDuePage(`{"scopeKeys":[],"scanned":0,"nextOffset":-3}`); err == nil {
		t.Fatal("负 nextOffset 必须报错")
	}
	page, err := parseListDuePage(`{"scopeKeys":["sk-1",2],"scanned":3,"nextOffset":4,"exhausted":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.scopeKeys) != 2 || page.scopeKeys[0] != "sk-1" || page.scopeKeys[1] != "2" {
		t.Fatalf("scopeKeys 应字符串化: %v", page.scopeKeys)
	}
	if page.scanned != 3 || page.nextOffset != 4 || !page.exhausted {
		t.Fatalf("分页字段不符: %+v", page)
	}
}

// TestValidateOperationPayloadContract 锁定转移参数校验契约。
func TestValidateOperationPayloadContract(t *testing.T) {
	scope := Scope{Kind: "account", AccountRuntimeKey: "acc-1"}
	base := func() map[string]any {
		return map[string]any{
			"scope":            scope,
			"scopeKey":         MustScopeKey(scope),
			"generation":       1,
			"dispatchRevision": "5",
			"transitionId":     "tr-1",
			"nowMs":            int64(1000),
		}
	}
	// get 不要求 transitionId。
	getPayload := base()
	delete(getPayload, "transitionId")
	if err := validateOperationPayload("get", getPayload); err != nil {
		t.Fatalf("get 不得要求 transitionId: %v", err)
	}
	// acquire 类必须带 leaseId 与未来租约。
	acquire := base()
	if err := validateOperationPayload("acquire_confirmation", acquire); err == nil {
		t.Fatal("acquire 缺 leaseId 必须报错")
	}
	acquire["leaseId"] = "l-1"
	acquire["leaseUntilMs"] = int64(500)
	if err := validateOperationPayload("acquire_confirmation", acquire); err == nil {
		t.Fatal("租约截止早于 now 必须报错")
	}
	acquire["leaseUntilMs"] = int64(2000)
	acquire["expectedFailureEvidenceKey"] = "not-sha"
	if err := validateOperationPayload("acquire_confirmation", acquire); err == nil {
		t.Fatal("非法 evidence key 必须报错")
	}
	acquire["expectedFailureEvidenceKey"] = suspectEvidenceKey()
	if err := validateOperationPayload("acquire_confirmation", acquire); err != nil {
		t.Fatalf("合法 evidence key 应通过: %v", err)
	}
	// leaseUntilMs 缺失。
	noLease := base()
	noLease["leaseId"] = "l-1"
	if err := validateOperationPayload("acquire_canary", noLease); err == nil {
		t.Fatal("acquire 缺 leaseUntilMs 必须报错")
	}
	// complete 类的 outcome 白名单。
	complete := base()
	complete["leaseId"] = "l-1"
	if err := validateOperationPayload("complete_confirmation", complete); err == nil {
		t.Fatal("complete 缺 outcome 必须报错")
	}
	complete["outcome"] = "bogus"
	if err := validateOperationPayload("complete_confirmation", complete); err == nil {
		t.Fatal("未知 outcome 必须报错")
	}
	complete["outcome"] = "transport_failure"
	if err := validateOperationPayload("complete_confirmation", complete); err == nil {
		t.Fatal("transport_failure 缺 evidence key 必须报错")
	}
	complete["failureEvidenceKey"] = suspectEvidenceKey()
	if err := validateOperationPayload("complete_confirmation", complete); err != nil {
		t.Fatalf("合法 transport_failure 应通过: %v", err)
	}
	complete["framingCompleteDisposition"] = "bogus"
	complete["outcome"] = "framing_complete"
	if err := validateOperationPayload("complete_confirmation", complete); err == nil {
		t.Fatal("非法 disposition 必须报错")
	}
	complete["framingCompleteDisposition"] = "closed"
	if err := validateOperationPayload("complete_confirmation", complete); err != nil {
		t.Fatalf("合法 disposition 应通过: %v", err)
	}
	// replace_revision 要求 dispatchRevision。
	replace := base()
	delete(replace, "dispatchRevision")
	if err := validateOperationPayload("replace_revision", replace); err == nil {
		t.Fatal("replace_revision 缺 dispatchRevision 必须报错")
	}
}

// TestOpsJobsStoreAdapterSurface 覆盖 adapter 的 Get/Underlying/Close 与
// ReplaceDispatchRevision 的语义迁移。
func TestOpsJobsStoreAdapterSurface(t *testing.T) {
	now := fixedNow()
	store, _ := newTestStore(t, 8, now)
	if store.Underlying() == nil {
		t.Fatal("Underlying 必须暴露 wire store")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("adapter Close 应为空操作: %v", err)
	}
	ctx := context.Background()
	scope := accountScope("acc-adapter")
	key := MustScopeKey(convertScope(scope))

	// 植入 OPEN 状态。
	openState := opsjobs.CircuitState{
		ScopeKey:         key,
		Scope:            scope,
		Phase:            opsjobs.CircuitPhaseOpen,
		Generation:       2,
		DispatchRevision: "5",
		TransitionID:     "incident-1",
		IncidentID:       "incident-1",
		RetryAtMS:        &[]int64{now() + 60_000}[0],
		UpdatedAtMS:      now(),
	}
	if _, err := store.Restore(ctx, openState, now()); err != nil {
		t.Fatal(err)
	}
	// Get 读回一致。
	got, err := store.Get(ctx, scope, now())
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != opsjobs.CircuitPhaseOpen || got.Generation != 2 || got.DispatchRevision != "5" {
		t.Fatalf("Get 读回不符: %+v", got)
	}
	// ReplaceDispatchRevision：同 revision 推进关闭作用域。
	result, err := store.ReplaceDispatchRevision(ctx, scope, "6", "tr-rev", now())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != opsjobs.CircuitMutationApplied || result.State.Phase != opsjobs.CircuitPhaseClosed {
		t.Fatalf("revision 推进必须关闭作用域: %s %s", result.Status, result.State.Phase)
	}
	if result.State.DispatchRevision != "6" {
		t.Fatalf("关闭态应携带新 revision: %+v", result.State)
	}
}

// TestClearAccountEscalationEvidenceRemovesSeed 覆盖命中证据时的清除语义。
func TestClearAccountEscalationEvidenceRemovesSeed(t *testing.T) {
	now := fixedNow()
	store, server := newTestStore(t, 8, now)
	wire := store.Underlying()
	ctx := context.Background()
	_, _, _, escalationKey, _ := wire.Keys()
	// 直接植入口径一致的升级证据（dispatchRevision 参与 CAS）。
	server.HSet(escalationKey, "acc-evidence", `{"dispatchRevision":"5"}`)
	cleared, err := store.ClearAccountEscalationEvidence(ctx, "acc-evidence", "5", "evidence-1", now())
	if err != nil {
		t.Fatal(err)
	}
	if !cleared {
		t.Fatal("revision 一致的证据必须可清除")
	}
	if remaining := server.HGet(escalationKey, "acc-evidence"); remaining != "" {
		t.Fatalf("清除后证据必须删除: %s", remaining)
	}
	// revision 不一致 → false 且不删除。
	server.HSet(escalationKey, "acc-evidence-2", `{"dispatchRevision":"5"}`)
	cleared, err = store.ClearAccountEscalationEvidence(ctx, "acc-evidence-2", "6", "evidence-2", now())
	if err != nil || cleared {
		t.Fatalf("revision 不一致不得清除: %v %v", cleared, err)
	}
	// 参数校验。
	if _, err := store.ClearAccountEscalationEvidence(ctx, "  ", "5", "e", now()); err == nil {
		t.Fatal("空 runtime key 必须报错")
	}
}

// TestCompleteConfirmationTransportFailurePath 覆盖 transport_failure 结算
// （SUSPECT 保持 + 证据键累积）。
func TestCompleteConfirmationTransportFailurePath(t *testing.T) {
	now := fixedNow()
	store, _ := newTestStore(t, 8, now)
	ctx := context.Background()
	scope := accountScope("acc-transport")
	suspect := opsjobs.CircuitState{
		ScopeKey:                     MustScopeKey(convertScope(scope)),
		Scope:                        scope,
		Phase:                        opsjobs.CircuitPhaseSuspect,
		Generation:                   1,
		DispatchRevision:             "5",
		TransitionID:                 "incident-1",
		IncidentID:                   "incident-1",
		ConfirmationFailuresRequired: &[]int{2}[0],
		ConfirmationFailureCount:     &[]int{0}[0],
		RetryAtMS:                    &[]int64{now() - 10}[0],
		UpdatedAtMS:                  now(),
	}
	if _, err := store.Restore(ctx, suspect, now()); err != nil {
		t.Fatal(err)
	}
	identity := opsjobs.CircuitTransitionIdentity{
		Scope: scope, Generation: 1, DispatchRevision: "5", TransitionID: "tr-acq", NowMS: now(),
	}
	if _, err := store.AcquireConfirmationLease(ctx, identity, opsjobs.CircuitLeaseSpec{LeaseID: "lease-t", LeaseUntilMS: now() + 5000}); err != nil {
		t.Fatal(err)
	}
	completeIdentity := identity
	completeIdentity.TransitionID = "tr-complete"
	result, err := store.CompleteConfirmation(ctx, completeIdentity, "lease-t", opsjobs.CircuitCompletion{
		Outcome:            opsjobs.CircuitVerdictTransportFailure,
		FailureEvidenceKey: suspectEvidenceKey(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != opsjobs.CircuitMutationApplied {
		t.Fatalf("transport_failure 结算必须 applied: %s", result.Status)
	}
	if result.State.Phase != opsjobs.CircuitPhaseSuspect {
		t.Fatalf("transport_failure 不得离开 SUSPECT: %s", result.State.Phase)
	}
	if result.State.ConfirmationFailureCount == nil || *result.State.ConfirmationFailureCount != 1 {
		t.Fatalf("失败计数必须推进: %+v", result.State.ConfirmationFailureCount)
	}
	if len(result.State.FailureEvidenceKeys) != 1 || result.State.FailureEvidenceKeys[0] != suspectEvidenceKey() {
		t.Fatalf("失败证据必须累积: %v", result.State.FailureEvidenceKeys)
	}
	// ConfirmationEvidenceKey 入参直通（合法 SHA256 通过校验）。
	second := identity
	second.TransitionID = "tr-acq-2"
	if _, err := store.AcquireConfirmationLease(ctx, second, opsjobs.CircuitLeaseSpec{LeaseID: "lease-t2", LeaseUntilMS: now() + 5000}); err != nil {
		t.Fatal(err)
	}
}

// TestAcquireConfirmationLeaseEvidenceValidation 覆盖 evidence 入参校验。
func TestAcquireConfirmationLeaseEvidenceValidation(t *testing.T) {
	now := fixedNow()
	store, _ := newTestStore(t, 8, now)
	wire := store.Underlying()
	ctx := context.Background()
	scope := accountScope("acc-evidence-input")
	nowValue := now()
	input := AcquireLeaseInput{
		Scope:            convertScope(scope),
		Generation:       1,
		DispatchRevision: "5",
		TransitionID:     "tr-1",
		LeaseID:          "l-1",
		LeaseUntilMs:     now() + 1000,
		NowMs:            &nowValue,
	}
	// 非法显式 evidence key 按 NormalizeFailureEvidenceKey 契约回退为
	// fallbackSeed 哈希（不报错）；缺 fallbackSeed 才报错。
	bad := input
	invalid := "short"
	bad.ConfirmationEvidenceKey = &invalid
	if _, err := wire.AcquireConfirmationLease(ctx, bad); err != nil {
		t.Fatalf("非法显式 key 应回退 seed 哈希而非报错: %v", err)
	}
	// 状态不存在时合法 evidence 输入返回 not_found（不报错）。
	good := input
	valid := suspectEvidenceKey()
	good.ExpectedFailureEvidenceKey = &valid
	good.ConfirmationEvidenceKey = &valid
	result, err := wire.AcquireConfirmationLease(ctx, good)
	if err != nil {
		t.Fatal(err)
	}
	if opsjobs.CircuitMutationStatus(result.Status) != opsjobs.CircuitMutationNotFound {
		t.Fatalf("不存在状态的 acquire 应返回 not_found: %s", result.Status)
	}
}

// TestListDueLimitValidation 覆盖 ListDue 的 limit 校验。
func TestListDueLimitValidation(t *testing.T) {
	now := fixedNow()
	store, _ := newTestStore(t, 8, now)
	if _, err := store.Underlying().ListDue(context.Background(), now(), 0); err == nil {
		t.Fatal("limit 0 必须报错")
	}
}

// TestMutationResultRelatedSlice 覆盖 relatedStates 出口。
func TestMutationResultRelatedSlice(t *testing.T) {
	var empty MutationResult
	if empty.relatedSlice() != nil {
		t.Fatal("nil relatedStates 出口应为 nil")
	}
	withRelated := MutationResult{RelatedStates: stateList{{Phase: "CLOSED"}}}
	if len(withRelated.relatedSlice()) != 1 {
		t.Fatalf("relatedSlice 应拷贝: %+v", withRelated)
	}
}
