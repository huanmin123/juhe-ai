package gatewaycircuit

// w9e 覆盖率战役：补 jitter 调度抖动、types 归一化/作用域键、suppression
// 与 wait 的纯函数分支。全部本地构造，无外部资源。

import (
	"math"
	"strings"
	"testing"
)

func TestW9EPassiveScheduleJitterWindows(t *testing.T) {
	cases := []struct {
		interval int64
		want     int64
	}{
		{0, 0},                                   // 非法 interval 钳 1 → 半窗 0
		{1, 0},                                   // 半窗 0
		{10_000, 5_000},                          // 半窗小于子分钟窗
		{100_000, 30_000},                        // 分钟窗
		{2 * 60 * 60_000, 30 * 60_000},           // 小时窗
		{6 * 24 * 60 * 60_000, 60 * 60_000},      // < 24 天窗：60 分钟
		{30 * 24 * 60 * 60_000, 8 * 60 * 60_000}, // ≥7 天走周窗
		{30 * 7 * 24 * 60 * 60_000, 8 * 60 * 60_000}, // ≥7 天走周窗
	}
	for _, tc := range cases {
		if got := passiveScheduleJitterWindowMs(tc.interval); got != tc.want {
			t.Fatalf("window(%d) = %d, want %d", tc.interval, got, tc.want)
		}
	}
}

func TestW9EPassiveScheduleOffsetAndDelay(t *testing.T) {
	deterministic := func() float64 { return 0.5 }
	if got := passiveScheduleOffsetWithinWindowMs(0, deterministic); got != 0 {
		t.Fatalf("零窗偏移 = %d", got)
	}
	// 采样 0.5 → floor(0.5*(2w+1)) - w；w=100 → 100-100+1=1（0 偏移转 1）。
	offset := passiveScheduleOffsetWithinWindowMs(100, deterministic)
	if offset != 1 {
		t.Fatalf("0.5 采样偏移 = %d", offset)
	}
	// NaN / Inf 采样钳到 0 → offset = -w。
	if got := passiveScheduleOffsetWithinWindowMs(100, func() float64 { return math.NaN() }); got != -100 {
		t.Fatalf("NaN 采样偏移 = %d", got)
	}
	if got := passiveScheduleOffsetWithinWindowMs(100, func() float64 { return math.Inf(1) }); got != -100 {
		t.Fatalf("Inf 采样偏移 = %d", got)
	}
	// 采样 1 → min(w, floor(2w+1)) = w → offset = 0 → 转为 1。
	if got := passiveScheduleOffsetWithinWindowMs(100, func() float64 { return 1 }); got != 1 {
		t.Fatalf("采样 1 偏移 = %d", got)
	}
	// delay 恒正。
	if got := passiveScheduleDelayMs(0, deterministic); got < 1 {
		t.Fatalf("delay(0) = %d", got)
	}
	if got := passiveScheduleDelayMs(50_000, deterministic); got < 1 {
		t.Fatalf("delay(50s) = %d", got)
	}
	// not-before delay：0 偏移 → interval+1。
	nb := passiveScheduleNotBeforeDelayMs(1_000, func() float64 { return 0.5 })
	if nb <= 1_000 {
		t.Fatalf("not-before delay = %d", nb)
	}
	_ = truncMs(1.9)
	_ = truncMs(-1.9)
	if truncMs(2.9) != 2 || truncMs(-2.9) != -2 {
		t.Fatal("truncMs 契约不符")
	}
	if sha1Hex("abc") == "" || sha256Hex("abc") == "" {
		t.Fatal("摘要辅助不应为空")
	}
}

func TestW9EAccountCircuitBackoffDelay(t *testing.T) {
	settings := DefaultSettings()
	// 前几次尝试为精确退避。
	if got := settings.accountCircuitBackoffDelayMs(1, "seed", nil); got != settings.AccountCircuitBackoffMs[0] {
		t.Fatalf("attempt 1 = %d", got)
	}
	// 越界 attempt 钳到最后一档，带 seed 产生确定性偏移。
	last := len(settings.AccountCircuitBackoffMs) - 1
	a := settings.accountCircuitBackoffDelayMs(int64(last+10), "seed", deterministicRandomW9E())
	b := settings.accountCircuitBackoffDelayMs(int64(last+10), "seed", deterministicRandomW9E())
	if a != b {
		t.Fatalf("同 seed 应确定: %d vs %d", a, b)
	}
	// 无 seed 随机路径。
	c := settings.accountCircuitBackoffDelayMs(int64(last+10), "", deterministicRandomW9E())
	if c < 1 {
		t.Fatalf("随机退避必须为正, got %d", c)
	}
}

func deterministicRandomW9E() func() float64 { return func() float64 { return 0.25 } }

func TestW9EScopeKeyVariants(t *testing.T) {
	if _, err := ScopeKey(Scope{Kind: ScopeKindAccount}); err == nil {
		t.Fatal("缺 runtime key 必须失败")
	}
	account := Scope{Kind: ScopeKindAccount, AccountRuntimeKey: "acct"}
	key, err := ScopeKey(account)
	if err != nil || !strings.HasPrefix(key, "account|") && !strings.Contains(key, "acct") {
		t.Fatalf("account key = %q err=%v", key, err)
	}
	// key 作用域需要 fingerprint。
	if _, err := ScopeKey(Scope{Kind: ScopeKindKey, AccountRuntimeKey: "acct"}); err == nil {
		t.Fatal("key 作用域缺 fingerprint 必须失败")
	}
	keyScope := Scope{Kind: ScopeKindKey, AccountRuntimeKey: "acct", KeyFingerprint: "fp"}
	if _, err := ScopeKey(keyScope); err != nil {
		t.Fatal(err)
	}
	// protocol_model 需要三个附加字段。
	if _, err := ScopeKey(Scope{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "acct"}); err == nil {
		t.Fatal("缺 protocolProfile 必须失败")
	}
	if _, err := ScopeKey(Scope{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "acct", ProtocolProfile: "p"}); err == nil {
		t.Fatal("缺 requestLane 必须失败")
	}
	if _, err := ScopeKey(Scope{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "acct", ProtocolProfile: "p", RequestLane: "chat"}); err == nil {
		t.Fatal("缺 modelBucket 必须失败")
	}
	pm := Scope{Kind: ScopeKindProtocolModel, AccountRuntimeKey: "acct", ProtocolProfile: "p", RequestLane: "text", ModelBucket: "m"}
	if _, err := ScopeKey(pm); err != nil {
		t.Fatal(err)
	}
	// 未知 kind。
	if _, err := ScopeKey(Scope{Kind: "bogus", AccountRuntimeKey: "acct"}); err == nil {
		t.Fatal("未知 kind 必须失败")
	}
	// MustScopeKey。
	if MustScopeKey(account) == "" {
		t.Fatal("MustScopeKey 不应为空")
	}
	// HierarchyTransitionID 校验。
	if _, err := HierarchyTransitionID("open", "", "p", "c", 1); err == nil {
		t.Fatal("缺 parentTransitionID 必须失败")
	}
	if _, err := HierarchyTransitionID("open", "t", "", "c", 1); err == nil {
		t.Fatal("缺 parentIncidentID 必须失败")
	}
	if _, err := HierarchyTransitionID("open", "t", "p", "", 1); err == nil {
		t.Fatal("缺 childScopeKey 必须失败")
	}
	if _, err := HierarchyTransitionID("open", "t", "p", "c", -1); err == nil {
		t.Fatal("负 generation 必须失败")
	}
	id, err := HierarchyTransitionID("open", "t", "p", "c", 1)
	if err != nil || !strings.HasPrefix(id, "hierarchy:open:") {
		t.Fatalf("hierarchy id = %q err=%v", id, err)
	}
	// AssertStateScopeKey。
	state := State{Scope: account}
	state.ScopeKey = MustScopeKey(account)
	if err := AssertStateScopeKey(state); err != nil {
		t.Fatal(err)
	}
	state.ScopeKey = "broken"
	if err := AssertStateScopeKey(state); err == nil {
		t.Fatal("不一致 scopeKey 必须失败")
	}
	state.ScopeKey = MustScopeKey(account)
	state.Scope = Scope{Kind: "bogus"}
	if err := AssertStateScopeKey(state); err == nil {
		t.Fatal("非法 scope 必须失败")
	}
}

func TestW9EConfirmationFailureAndEvidenceHelpers(t *testing.T) {
	if count, err := ConfirmationFailureCountOf(State{}); err != nil || count != 0 {
		t.Fatalf("nil count = %d err=%v", count, err)
	}
	negative := int64(-1)
	if _, err := ConfirmationFailureCountOf(State{ConfirmationFailureCount: &negative}); err == nil {
		t.Fatal("负计数必须失败")
	}
	zero := int64(0)
	if count, err := ConfirmationFailureCountOf(State{ConfirmationFailureCount: &zero}); err != nil || count != 0 {
		t.Fatalf("零计数 = %d err=%v", count, err)
	}
	// evidence 去重、非法剔除与保留窗口。
	bad := "not-hex"
	goodA := strings.Repeat("a", 64)
	goodB := strings.Repeat("b", 64)
	required := int64(1)
	state := State{FailureEvidenceKeys: []string{goodA, goodA, bad, goodB}, ConfirmationFailuresRequired: &required}
	keys, err := FailureEvidenceKeysOf(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("evidence keys = %v", keys)
	}
	// 超出 keep 窗口时保留尾部。
	full := State{FailureEvidenceKeys: []string{goodA, goodB}, ConfirmationFailuresRequired: &required}
	keys2, err := FailureEvidenceKeysOf(full)
	if err != nil || len(keys2) != 2 {
		t.Fatalf("keep 窗口 = %v err=%v", keys2, err)
	}
	if _, _, err := LastFailureEvidenceKey(State{FailureEvidenceKeys: []string{goodB}}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := LastFailureEvidenceKey(State{}); ok {
		t.Fatal("空 evidence 不应有最后键")
	}
	// NormalizeFailureEvidenceKey：显式 SHA256 优先，非法回退 seed。
	explicit := strings.Repeat("c", 64)
	if got, err := NormalizeFailureEvidenceKey(&explicit, "seed"); err != nil || got != explicit {
		t.Fatalf("explicit = %q err=%v", got, err)
	}
	if _, err := NormalizeFailureEvidenceKey(nil, ""); err == nil {
		t.Fatal("缺 fallbackSeed 必须失败")
	}
	if got, err := NormalizeFailureEvidenceKey(nil, "seed"); err != nil || len(got) != 64 {
		t.Fatalf("seed fallback = %q err=%v", got, err)
	}
}

func TestW9EEscalationNormalizers(t *testing.T) {
	fallback := int64(3)
	// nil → fallback。
	if got, err := NormalizeEscalationDistinctScopeThreshold(nil, fallback); err != nil || got != fallback {
		t.Fatalf("threshold fallback = %d err=%v", got, err)
	}
	if got, err := NormalizeEscalationWindowMs(nil, 60_000); err != nil || got != 60_000 {
		t.Fatalf("window fallback = %d err=%v", got, err)
	}
	// 越界。
	below := int64(-5)
	if _, err := NormalizeEscalationDistinctScopeThreshold(&below, fallback); err == nil {
		t.Fatal("threshold 越界必须失败")
	}
	if _, err := NormalizeEscalationWindowMs(&below, 60_000); err == nil {
		t.Fatal("window 越界必须失败")
	}
}
