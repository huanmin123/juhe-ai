package gatewayproxyhealth

// 速度运行态 v2 命名空间（调度偏好身份）键族测试：键构造双版本（含
// sanitize）、v1 历史键零字节漂移、清理谓词跨代、候选围栏跨代隔离与查询
// 双代透出。契约见 docs/functions/接口契约与权限矩阵.md「速度运行态命名空
// 间与查询契约」。

import (
	"strings"
	"testing"
)

func wspPreferenceScope(preferenceID, fingerprint string) *LatencyDegradationScope {
	return &LatencyDegradationScope{
		SystemAccountID:   "sys",
		RouteStrategyID:   "rs1",
		GroupID:           "g1",
		PreferenceID:      preferenceID,
		ConfigFingerprint: fingerprint,
	}
}

// 键构造双版本：v1 五段逐字节等值于历史拼接；v2 七段按契约插入偏好身份两
// 段；版本判定只看 PreferenceID（为空即 v1，ConfigFingerprint 不参与判定）。
func TestWspLatencyStateKeyVersions(t *testing.T) {
	account := latencyAccount("acc-1")

	v1Key := accountLatencyStateKey(*latencyScope(), account)
	wantV1 := strings.Join([]string{"v1", "sys", "rs1", "g1", "acc-1"}, ":")
	if v1Key != wantV1 {
		t.Fatalf("v1 key = %q want %q", v1Key, wantV1)
	}
	if v1Key != "v1:sys:rs1:g1:acc-1" {
		t.Fatalf("v1 key 历史形状漂移: %q", v1Key)
	}

	v2Scope := wspPreferenceScope("pref-1", "fp1234567890ab")
	v2Key := accountLatencyStateKey(*v2Scope, account)
	wantV2 := strings.Join([]string{"v2", "sys", "rs1", "pref-1", "fp1234567890ab", "g1", "acc-1"}, ":")
	if v2Key != wantV2 {
		t.Fatalf("v2 key = %q want %q", v2Key, wantV2)
	}

	// sanitizeKeyPart 规则两代一致：非法字符折叠为 "_"，空段为 "_"。
	oddScope := LatencyDegradationScope{SystemAccountID: "sy s", RouteStrategyID: "rs/1", GroupID: "", PreferenceID: "", ConfigFingerprint: ""}
	oddKey := accountLatencyStateKey(oddScope, latencyAccount(""))
	if oddKey != "v1:sy_s:rs_1:_:_" {
		t.Fatalf("v1 sanitize key = %q", oddKey)
	}
	oddV2 := LatencyDegradationScope{SystemAccountID: "sy s", RouteStrategyID: "rs/1", GroupID: "", PreferenceID: "p 1", ConfigFingerprint: ""}
	if got := accountLatencyStateKey(oddV2, latencyAccount("a")); got != "v2:sy_s:rs_1:p_1:_:_:a" {
		t.Fatalf("v2 sanitize key = %q", got)
	}

	// PreferenceID 为空时 ConfigFingerprint 不改变键族（版本判定只看
	// PreferenceID，任务契约的键版本规则）。
	fingerprintOnly := LatencyDegradationScope{SystemAccountID: "sys", RouteStrategyID: "rs1", GroupID: "g1", ConfigFingerprint: "fp1234567890ab"}
	if got := accountLatencyStateKey(fingerprintOnly, account); got != wantV1 {
		t.Fatalf("空 PreferenceID 必须保持 v1 键: %q", got)
	}
}

// v1 派生键零字节漂移：generation / 双索引 / 索引锁 / mutation-lock /
// probe-claim 与历史常量拼接逐字节等值；v2 按同一形状以版本段 v2 同构。
func TestWspLatencyKeyspaceDerivedKeys(t *testing.T) {
	if latencyStateKeyspaceV1.generationKey() != latencyStateGenerationKey {
		t.Fatalf("v1 generation key 漂移: %q", latencyStateKeyspaceV1.generationKey())
	}
	if latencyStateKeyspaceV1.allIndexKey() != latencyStateAllIndexKey {
		t.Fatalf("v1 all-index key 漂移: %q", latencyStateKeyspaceV1.allIndexKey())
	}
	if latencyStateKeyspaceV1.probeIndexKey() != latencyStateProbeIndexKey {
		t.Fatalf("v1 probe-index key 漂移: %q", latencyStateKeyspaceV1.probeIndexKey())
	}
	if latencyStateKeyspaceV1.allIndexLockKey() != latencyStateAllIndexLockKey {
		t.Fatalf("v1 all-index-lock key 漂移: %q", latencyStateKeyspaceV1.allIndexLockKey())
	}
	if latencyStateKeyspaceV1.probeIndexLockKey() != latencyStateProbeIndexLockKey {
		t.Fatalf("v1 probe-index-lock key 漂移: %q", latencyStateKeyspaceV1.probeIndexLockKey())
	}
	if got := latencyStateKeyspaceV1.mutationLockKey("v1:s:g:a:r"); got != latencyStateVersion+":mutation-lock:v1:s:g:a:r" {
		t.Fatalf("v1 mutation lock 漂移: %q", got)
	}
	if got := latencyStateKeyspaceV1.probeClaimLockKey("gen", "v1:s:g:a:r"); got != latencyStateVersion+":probe-claim:gen:v1:s:g:a:r" {
		t.Fatalf("v1 probe claim 漂移: %q", got)
	}
	if got := latencyStateKeyspaceV2.generationKey(); got != "v2:generation" {
		t.Fatalf("v2 generation key = %q", got)
	}
	if got := latencyStateKeyspaceV2.probeIndexKey(); got != "v2:probe-index" {
		t.Fatalf("v2 probe-index key = %q", got)
	}
	if got := latencyStateKeyspaceV2.mutationLockKey("v2:s:g:p:f:a:r"); got != "v2:mutation-lock:v2:s:g:p:f:a:r" {
		t.Fatalf("v2 mutation lock = %q", got)
	}
	if got := normalRouteLatencyProbeClaimLockKey(LatencyProbeCandidate{StateKey: "v2:s:g:p:f:a:r", Generation: "gen"}); got != "v2:probe-claim:gen:v2:s:g:p:f:a:r" {
		t.Fatalf("v2 probe claim via stateKey = %q", got)
	}
}

// wspTriggerDegradation 连记 slowTriggerCount 个慢样本触发降级。
func wspTriggerDegradation(t *testing.T, service *LatencyDegradationService, account SuppressibleGatewayAccount, scope *LatencyDegradationScope, config *SpeedFirstRuntimeConfig) *LatencySlowResult {
	t.Helper()
	config.SlowTriggerCount = 3
	var result *LatencySlowResult
	for i := 0; i < 3; i++ {
		slow, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), account, scope, config, "")
		if err != nil {
			t.Fatalf("record slow: %v", err)
		}
		result = slow
	}
	if result == nil || !result.Degraded {
		t.Fatalf("3 个慢样本必须触发降级: %+v", result)
	}
	return result
}

// 清理谓词跨代：按策略段清理同时命中 v1 与 v2 中该策略段；其余策略段的
// v1 状态不受影响。
func TestWspLatencyClearForRouteStrategyAcrossGenerations(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, store := newMemoryLatencyService(clock)
	config := speedFirstConfig()

	wspTriggerDegradation(t, service, latencyAccount("a"), latencyScope(), &config)
	wspTriggerDegradation(t, service, latencyAccount("b"), wspPreferenceScope("pref-1", "fp1234567890ab"), &config)
	otherScope := &LatencyDegradationScope{SystemAccountID: "sys", RouteStrategyID: "rs2", GroupID: "g1"}
	wspTriggerDegradation(t, service, latencyAccount("c"), otherScope, &config)

	cleared, err := service.ClearNormalRouteLatencyDegradationForRouteStrategy(contextBackground(), "rs1")
	if err != nil {
		t.Fatalf("clear rs1: %v", err)
	}
	if cleared != 2 {
		t.Fatalf("按策略清理必须同时命中两代（cleared=%d）", cleared)
	}

	for _, key := range []string{"v1:sys:rs1:g1:a", "v2:sys:rs1:pref-1:fp1234567890ab:g1:b"} {
		if raw, _ := store.GetJSON(contextBackground(), key); raw != nil {
			t.Fatalf("%s 必须被清理", key)
		}
	}
	if raw, _ := store.GetJSON(contextBackground(), "v1:sys:rs2:g1:c"); raw == nil {
		t.Fatal("其他策略段的 v1 状态必须保留")
	}
	// 两代索引都不再含已清理键。
	for _, keyspace := range latencyStateKeyspaces() {
		keys, err := service.loadLatencyStateIndexKeys(contextBackground(), keyspace.probeIndexKey())
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range keys {
			if strings.HasPrefix(key, "v1:sys:rs1:") || strings.HasPrefix(key, "v2:sys:rs1:") {
				t.Fatalf("%s 仍残留在 %s", key, keyspace.probeIndexKey())
			}
		}
	}
}

// 账户级清理跨代：同账号在 v1/v2 的状态一并清除，其他账号不受影响。
func TestWspLatencyClearForAccountAcrossGenerations(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, store := newMemoryLatencyService(clock)
	config := speedFirstConfig()

	wspTriggerDegradation(t, service, latencyAccount("a"), latencyScope(), &config)
	wspTriggerDegradation(t, service, latencyAccount("a"), wspPreferenceScope("pref-1", "fp1234567890ab"), &config)
	wspTriggerDegradation(t, service, latencyAccount("b"), wspPreferenceScope("pref-1", "fp1234567890ab"), &config)

	cleared, err := service.ClearNormalRouteLatencyDegradationForAccount(contextBackground(), ClearNormalRouteLatencyDegradationForAccountInput{SystemAccountID: "sys", AccountID: "a"})
	if err != nil {
		t.Fatalf("clear account a: %v", err)
	}
	if cleared != 2 {
		t.Fatalf("账户级清理必须跨代命中（cleared=%d）", cleared)
	}
	if raw, _ := store.GetJSON(contextBackground(), "v1:sys:rs1:g1:a"); raw != nil {
		t.Fatal("v1 账户状态必须被清理")
	}
	if raw, _ := store.GetJSON(contextBackground(), "v2:sys:rs1:pref-1:fp1234567890ab:g1:a"); raw != nil {
		t.Fatal("v2 账户状态必须被清理")
	}
	if raw, _ := store.GetJSON(contextBackground(), "v2:sys:rs1:pref-1:fp1234567890ab:g1:b"); raw == nil {
		t.Fatal("其他账户的 v2 状态必须保留")
	}
}

// 查询双代透出：同一策略段 v1+v2 条目并存，Source/PreferenceID 正确，
// strategy 条目不携带偏好字段。
func TestWspLatencyDegradedRuntimeBothGenerations(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, _ := newMemoryLatencyService(clock)
	config := speedFirstConfig()

	wspTriggerDegradation(t, service, latencyAccount("a"), latencyScope(), &config)
	wspTriggerDegradation(t, service, latencyAccount("b"), wspPreferenceScope("pref-1", "fp1234567890ab"), &config)

	items, err := service.ListNormalRouteLatencyDegradedRuntime(contextBackground(), ListNormalRouteLatencyDegradedRuntimeInput{RouteStrategyIDs: []string{"rs1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("两代条目必须并存（items=%d）", len(items))
	}
	byAccount := map[string]DegradedRuntimeItem{}
	for _, item := range items {
		byAccount[item.AccountID] = item
	}
	strategyItem, ok := byAccount["a"]
	if !ok || strategyItem.Source != DegradedRuntimeSourceStrategy || strategyItem.PreferenceID != "" {
		t.Fatalf("v1 条目 source 必须为 strategy 且无偏好字段: %+v", strategyItem)
	}
	preferenceItem, ok := byAccount["b"]
	if !ok || preferenceItem.Source != DegradedRuntimeSourcePreference || preferenceItem.PreferenceID != "pref-1" {
		t.Fatalf("v2 条目 source 必须为 preference 且带偏好 ID: %+v", preferenceItem)
	}
}

// 探针候选双代扫描：两代降级 state 都进入候选且携带各自身份。
func TestWspLatencyProbeCandidatesBothGenerations(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, _ := newMemoryLatencyService(clock)
	config := speedFirstConfig()

	wspTriggerDegradation(t, service, latencyAccount("a"), latencyScope(), &config)
	wspTriggerDegradation(t, service, latencyAccount("b"), wspPreferenceScope("pref-1", "fp1234567890ab"), &config)

	// 推进时钟越过触发时刻安排的 nextProbeAt（recovery delay 带抖动）。
	clock.Advance(30_000)
	candidates, err := service.ListNormalRouteLatencyProbeCandidates(contextBackground(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("两代候选必须并存（candidates=%d）", len(candidates))
	}
	preferenceCandidateFound := false
	for _, candidate := range candidates {
		if candidate.Scope.PreferenceID == "pref-1" {
			preferenceCandidateFound = true
			if candidate.Scope.ConfigFingerprint != "fp1234567890ab" {
				t.Fatalf("候选必须携带指纹: %+v", candidate)
			}
			if !strings.HasPrefix(candidate.StateKey, "v2:sys:rs1:pref-1:fp1234567890ab:") {
				t.Fatalf("v2 候选键形状错误: %q", candidate.StateKey)
			}
		}
	}
	if !preferenceCandidateFound {
		t.Fatal("v2 候选缺失")
	}
}

// 候选围栏跨代隔离：身份字段参与 candidateMatchesState——旧身份的迟到候选
// 不能更新新身份的 state，v1/v2 互配也不成立。
func TestWspLatencyCandidateMatchesStatePreferenceFence(t *testing.T) {
	state := latencyState{
		Generation:      "gen",
		AccountID:       "a",
		RuntimeKey:      "a",
		DegradedUntilMs: int64Ptr(2_000_000),
		NextProbeAtMs:   int64Ptr(1_500_000),
		Scope: LatencyDegradationScope{
			SystemAccountID:   "sys",
			RouteStrategyID:   "rs1",
			GroupID:           "g1",
			PreferenceID:      "pref-1",
			ConfigFingerprint: "fp1234567890ab",
		},
	}
	// candidate 按 probeCandidateFromState 的真实投影路径构造（全部围栏字段
	// 与 state 对齐，只操纵身份字段）。
	candidate, ok := probeCandidateFromState("v2:sys:rs1:pref-1:fp1234567890ab:g1:a", state)
	if !ok {
		t.Fatal("候选投影失败")
	}
	if !latencyProbeCandidateMatchesState(candidate, state) {
		t.Fatal("同身份候选必须匹配")
	}

	rotated := state
	rotated.Scope.ConfigFingerprint = "fpffffffffffff"
	if latencyProbeCandidateMatchesState(candidate, rotated) {
		t.Fatal("指纹轮换后旧候选不得匹配新 state")
	}

	v1State := state
	v1State.Scope.PreferenceID = ""
	v1State.Scope.ConfigFingerprint = ""
	if latencyProbeCandidateMatchesState(candidate, v1State) {
		t.Fatal("v2 候选不得匹配 v1 state")
	}
	v1Candidate, ok := probeCandidateFromState("v1:sys:rs1:g1:a", v1State)
	if !ok {
		t.Fatal("v1 候选投影失败")
	}
	if latencyProbeCandidateMatchesState(v1Candidate, state) {
		t.Fatal("v1 候选不得匹配 v2 state")
	}
}
