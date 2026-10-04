package gatewayproxyhealth

// wtd 覆盖波次：总时间兜底截止的维度化慢样本状态机（设计 6.4 双通道共享降
// 级）与恢复双向过滤（设计 6.5）。全部使用 MemoryRuntimeStateStore + 固定时
// 钟/随机，结果确定可回放。

import (
	"encoding/json"
	"testing"
)

// wtdTotalTimeConfig 在既有七字段测试配置上补两个总时间档位（非零即可落盘）。
func wtdTotalTimeConfig() SpeedFirstRuntimeConfig {
	config := speedFirstConfig()
	config.TotalTimeDeadlineMs = 120_000
	config.CompactionTotalTimeDeadlineMs = 300_000
	return config
}

// wtdLoadState 读取账户当前存储的 state（直接读 memory store 原始条目）。
func wtdLoadState(t *testing.T, store *MemoryRuntimeStateStore, scope *LatencyDegradationScope, account SuppressibleGatewayAccount) *latencyState {
	t.Helper()
	key := accountLatencyStateKey(*scope, account)
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, ok := store.entries[key]
	if !ok {
		t.Fatalf("state 不存在：%s", key)
	}
	state, valid := decodeLatencyState(entry.value)
	if !valid {
		t.Fatalf("存储中的 state 非法：%s", key)
	}
	return &state
}

// wtdRawJSON 返回账户 state 的原始 JSON 文本（用于逐字节前后对比）。
func wtdRawJSON(t *testing.T, store *MemoryRuntimeStateStore, scope *LatencyDegradationScope, account SuppressibleGatewayAccount) string {
	t.Helper()
	key := accountLatencyStateKey(*scope, account)
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, ok := store.entries[key]
	if !ok {
		t.Fatalf("state 不存在：%s", key)
	}
	return string(entry.value)
}

// wtdRuntimeItem 按账户取运行态投影（找不到即失败）。
func wtdRuntimeItem(t *testing.T, service *LatencyDegradationService, accountID string) DegradedRuntimeItem {
	t.Helper()
	items, err := service.ListNormalRouteLatencyDegradedRuntime(contextBackground(), ListNormalRouteLatencyDegradedRuntimeInput{
		RouteStrategyIDs: []string{"rs1"},
	})
	if err != nil {
		t.Fatalf("运行态查询失败: %v", err)
	}
	for _, item := range items {
		if item.AccountID == accountID {
			return item
		}
	}
	t.Fatalf("账户 %s 无运行态投影", accountID)
	return DegradedRuntimeItem{}
}

// 双通道独立计数：同账户首字慢与总时间慢各自在独立通道累计，互不吞并与互
// 不触发；第三条任一维度触发降级且 Dimension 正确（设计 6.4）。
func TestWtdDimensionChannelsCountIndependently(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, store := newMemoryLatencyService(clock)
	scope := latencyScope()
	config := wtdTotalTimeConfig()
	accountA := latencyAccount("a")
	accountB := latencyAccount("b")

	// 账户 A：首字慢两条、总时间慢两条交替，各自计数 2 且都不触发。
	for i := 0; i < 2; i++ {
		firstByte, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), accountA, scope, &config, "")
		if err != nil || firstByte == nil || firstByte.Degraded || firstByte.SlowCount != int64(i+1) {
			t.Fatalf("A 首字慢 %d: %+v err=%v", i, firstByte, err)
		}
		totalTime, err := service.RecordNormalRouteTotalTimeSlow(contextBackground(), accountA, scope, &config, "")
		if err != nil || totalTime == nil || totalTime.Degraded || totalTime.SlowCount != int64(i+1) {
			t.Fatalf("A 总时间慢 %d: %+v err=%v", i, totalTime, err)
		}
	}
	clock.Advance(1_000)
	trigger, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), accountA, scope, &config, "")
	if err != nil || trigger == nil || !trigger.Degraded || trigger.SlowCount != 3 {
		t.Fatalf("A 首字第三条必须触发: %+v err=%v", trigger, err)
	}
	stateA := wtdLoadState(t, store, scope, accountA)
	if stateA.TotalTimeSlowCount != 2 {
		t.Fatalf("总时间通道必须独立计数=%d", stateA.TotalTimeSlowCount)
	}
	if stateA.Dimension != LatencyDimensionFirstByte {
		t.Fatalf("A Dimension=%q", stateA.Dimension)
	}

	// 账户 B：同样交替两条后由总时间第三条触发，首字通道停在 2。
	for i := 0; i < 2; i++ {
		if _, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), accountB, scope, &config, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := service.RecordNormalRouteTotalTimeSlow(contextBackground(), accountB, scope, &config, ""); err != nil {
			t.Fatal(err)
		}
	}
	clock.Advance(1_000)
	triggerB, err := service.RecordNormalRouteTotalTimeSlow(contextBackground(), accountB, scope, &config, "")
	if err != nil || triggerB == nil || !triggerB.Degraded || triggerB.SlowCount != 3 {
		t.Fatalf("B 总时间第三条必须触发: %+v err=%v", triggerB, err)
	}
	stateB := wtdLoadState(t, store, scope, accountB)
	if stateB.SlowCount != 2 {
		t.Fatalf("首字通道必须独立计数=%d", stateB.SlowCount)
	}
	if stateB.Dimension != LatencyDimensionTotalTime {
		t.Fatalf("B Dimension=%q", stateB.Dimension)
	}
}

// 已降级（first_byte）状态下总时间通道再触发：DegradedUntilMs 不续期，
// Dimension 更新为最新触发维度 total_time（设计 6.4）。
func TestWtdTotalTimeTriggerOnDegradedKeepsLeaseAndUpdatesDimension(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, _ := newMemoryLatencyService(clock)
	scope := latencyScope()
	config := wtdTotalTimeConfig()
	account := latencyAccount("a")
	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), account, scope, &config, ""); err != nil {
			t.Fatal(err)
		}
	}
	item := wtdRuntimeItem(t, service, "a")
	if item.Dimension != LatencyDimensionFirstByte || item.DegradedUntil != ISOStringMs(1_000_000+120_000) {
		t.Fatalf("首字降级投影=%+v", item)
	}

	clock.Advance(30_000)
	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteTotalTimeSlow(contextBackground(), account, scope, &config, ""); err != nil {
			t.Fatal(err)
		}
	}
	item = wtdRuntimeItem(t, service, "a")
	if item.Dimension != LatencyDimensionTotalTime {
		t.Fatalf("维度必须更新为最新触发维度=%q", item.Dimension)
	}
	if item.DegradedUntil != ISOStringMs(1_000_000+120_000) {
		t.Fatalf("降级租期不得续期=%s", item.DegradedUntil)
	}
}

// 维度双向过滤（设计 6.5）：达标样本只推进与降级维度同向的恢复，另一维度
// 的达标样本是中性样本——不改状态、不推进 SuccessCount、不清除降级。
func TestWtdSuccessDimensionBidirectionalNeutrality(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, store := newMemoryLatencyService(clock)
	scope := latencyScope()
	config := wtdTotalTimeConfig()
	fast := int64(500)

	// A：total_time 降级只认总时间达标；首字达标连续多次都是中性样本。
	totalTimeAccount := latencyAccount("a")
	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteTotalTimeSlow(contextBackground(), totalTimeAccount, scope, &config, ""); err != nil {
			t.Fatal(err)
		}
	}
	before := wtdRawJSON(t, store, scope, totalTimeAccount)
	for i := 0; i < 5; i++ {
		result, err := service.RecordNormalRouteFirstByteSuccess(contextBackground(), totalTimeAccount, scope, &config, &fast)
		if err != nil || result != nil {
			t.Fatalf("首字达标对 total_time 降级必须中性: %+v err=%v", result, err)
		}
	}
	if after := wtdRawJSON(t, store, scope, totalTimeAccount); after != before {
		t.Fatalf("中性样本不得改写状态：before=%s after=%s", before, after)
	}
	for i := 1; i <= 3; i++ {
		clock.Advance(1_000)
		result, err := service.RecordNormalRouteTotalTimeSuccess(contextBackground(), totalTimeAccount, scope, &config, config.TotalTimeDeadlineMs, 1_000)
		if err != nil || result == nil {
			t.Fatalf("total_time 达标 %d: %+v err=%v", i, result, err)
		}
		if result.Cleared != (i == 3) {
			t.Fatalf("total_time 达标 %d Cleared=%v", i, result.Cleared)
		}
	}
	if degraded, err := service.IsNormalRouteAccountLatencyDegraded(contextBackground(), totalTimeAccount, scope); err != nil || degraded {
		t.Fatalf("total_time 维度恢复后仍降级: %v err=%v", degraded, err)
	}

	// B：first_byte 降级只认首字达标；总时间达标是中性样本。
	firstByteAccount := latencyAccount("b")
	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), firstByteAccount, scope, &config, ""); err != nil {
			t.Fatal(err)
		}
	}
	before = wtdRawJSON(t, store, scope, firstByteAccount)
	for i := 0; i < 5; i++ {
		result, err := service.RecordNormalRouteTotalTimeSuccess(contextBackground(), firstByteAccount, scope, &config, config.TotalTimeDeadlineMs, 1_000)
		if err != nil || result != nil {
			t.Fatalf("总时间达标对 first_byte 降级必须中性: %+v err=%v", result, err)
		}
	}
	if after := wtdRawJSON(t, store, scope, firstByteAccount); after != before {
		t.Fatalf("中性样本不得改写状态：before=%s after=%s", before, after)
	}
	for i := 1; i <= 3; i++ {
		clock.Advance(1_000)
		result, err := service.RecordNormalRouteFirstByteSuccess(contextBackground(), firstByteAccount, scope, &config, &fast)
		if err != nil || result == nil {
			t.Fatalf("首字达标 %d: %+v err=%v", i, result, err)
		}
		if result.Cleared != (i == 3) {
			t.Fatalf("首字达标 %d Cleared=%v", i, result.Cleared)
		}
	}
}

// 总时间达标门槛：elapsed 与档位阈值相等仍计入（严格大于才忽略），超档样本
// 不累计（设计 6.5，档位阈值由调用方传入）。
func TestWtdTotalTimeSuccessDeadlineGate(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, store := newMemoryLatencyService(clock)
	scope := latencyScope()
	config := wtdTotalTimeConfig()
	account := latencyAccount("a")
	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteTotalTimeSlow(contextBackground(), account, scope, &config, ""); err != nil {
			t.Fatal(err)
		}
	}
	boundary, err := service.RecordNormalRouteTotalTimeSuccess(contextBackground(), account, scope, &config, config.TotalTimeDeadlineMs, config.TotalTimeDeadlineMs)
	if err != nil || boundary == nil || boundary.Cleared {
		t.Fatalf("阈值边界达标样本必须计入: %+v err=%v", boundary, err)
	}
	ignored, err := service.RecordNormalRouteTotalTimeSuccess(contextBackground(), account, scope, &config, config.TotalTimeDeadlineMs, config.TotalTimeDeadlineMs+1)
	if err != nil || ignored != nil {
		t.Fatalf("超档样本必须忽略: %+v err=%v", ignored, err)
	}
	state := wtdLoadState(t, store, scope, account)
	if state.SuccessCount != 1 {
		t.Fatalf("SuccessCount=%d 必须只累计边界达标样本", state.SuccessCount)
	}
}

// 探针候选过滤：total_time 降级不进候选；first_byte 降级照常；无 dimension
// 字段的存量降级状态视作 first_byte 仍被选中（设计 6.5）。
func TestWtdProbeCandidatesFilterTotalTimeDimension(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, store := newMemoryLatencyService(clock)
	scope := latencyScope()
	config := wtdTotalTimeConfig()
	totalTimeAccount := latencyAccount("a")
	firstByteAccount := latencyAccount("b")
	for i := 0; i < 3; i++ {
		if _, err := service.RecordNormalRouteTotalTimeSlow(contextBackground(), totalTimeAccount, scope, &config, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), firstByteAccount, scope, &config, ""); err != nil {
			t.Fatal(err)
		}
	}

	// 存量状态：无 dimension 字段的降级 state 直接写入存储。
	generation, err := service.loadLatencyStateGeneration(contextBackground())
	if err != nil {
		t.Fatal(err)
	}
	legacyAccount := latencyAccount("c")
	legacyKey := accountLatencyStateKey(*scope, legacyAccount)
	legacy := latencyState{
		Generation:         generation,
		AccountID:          "c",
		AccountName:        stringPtr("Account c"),
		RuntimeKey:         "c",
		Scope:              *scope,
		Config:             config,
		FirstSlowAtMs:      clock.NowMs() - 2_000,
		LastSlowAtMs:       clock.NowMs() - 2_000,
		SlowCount:          3,
		DegradationEventID: stringPtr("legacy-event"),
		DegradedUntilMs:    int64Ptr(clock.NowMs() + 60_000),
		NextProbeAtMs:      int64Ptr(clock.NowMs() - 1),
		Reason:             "存量首字降级",
	}
	whSeedRaw(store, clock, legacyKey, mustMarshalJSON(legacy), 60_000)
	// 种子只直写 state，候选扫描从 probe-index 出发：走真实索引写入路径登记。
	if err := service.addLatencyStateProbeIndexKey(contextBackground(), legacyKey); err != nil {
		t.Fatal(err)
	}

	clock.Advance(6_000)
	candidates, err := service.ListNormalRouteLatencyProbeCandidates(contextBackground(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	byAccount := make(map[string]LatencyProbeCandidate, len(candidates))
	for _, candidate := range candidates {
		byAccount[candidate.AccountID] = candidate
	}
	if _, present := byAccount["a"]; present {
		t.Fatal("total_time 降级不得进入探针候选")
	}
	if candidate, present := byAccount["b"]; !present || candidate.Dimension != LatencyDimensionFirstByte {
		t.Fatalf("first_byte 降级候选=%+v", candidate)
	}
	if candidate, present := byAccount["c"]; !present || candidate.Dimension != LatencyDimensionFirstByte {
		t.Fatalf("存量候选必须视作 first_byte=%+v", candidate)
	}
}

// 存量兼容：无新增字段的 state JSON 反序列化为零值，维度读作 first_byte，
// 存量校验不受影响。
func TestWtdLegacyStateJSONCompat(t *testing.T) {
	raw := json.RawMessage(`{` +
		`"generation":"g","accountId":"a","runtimeKey":"a",` +
		`"scope":{"systemAccountId":"sys","routeStrategyId":"rs1","groupId":"g1"},` +
		`"config":{"firstByteDeadlineMs":3000,"slowTriggerCount":3,"slowWindowSeconds":60,` +
		`"recoverySuccessCount":3,"probeIntervalSeconds":30,"degradedTtlSeconds":120,` +
		`"maxFirstByteRetriesPerRequest":2},` +
		`"firstSlowAtMs":1,"lastSlowAtMs":2,"slowCount":3,"successCount":0,"reason":"存量"}`)
	state, ok := decodeLatencyState(raw)
	if !ok {
		t.Fatal("存量 state 必须仍可解码")
	}
	if state.TotalTimeSlowCount != 0 || state.FirstTotalTimeSlowAtMs != 0 || state.LastTotalTimeSlowAtMs != 0 || state.Dimension != "" {
		t.Fatalf("存量新字段必须为零值=%+v", state)
	}
	if latencyDimensionOf(state) != LatencyDimensionFirstByte {
		t.Fatalf("存量维度必须读作 first_byte=%q", latencyDimensionOf(state))
	}
}

// RecordNormalRouteTotalTimeSlow 全流程：观察期不降级且 TTL 为观察窗口、
// Reason 保留调用方文案、Config 落盘含总时间档位、触发后 DegradedTTL 生效
// 且维度落盘；空 reason 回落默认文案（设计 6.4）。
func TestWtdTotalTimeSlowFullFlow(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, store := newMemoryLatencyService(clock)
	scope := latencyScope()
	config := wtdTotalTimeConfig()
	account := latencyAccount("a")

	first, err := service.RecordNormalRouteTotalTimeSlow(contextBackground(), account, scope, &config, "上游总耗时超档")
	if err != nil || first == nil {
		t.Fatalf("首条总时间慢样本: %+v err=%v", first, err)
	}
	if first.Degraded || first.SlowCount != 1 {
		t.Fatalf("观察期结果=%+v", first)
	}
	state := wtdLoadState(t, store, scope, account)
	if state.DegradedUntilMs != nil || state.Dimension != "" {
		t.Fatalf("观察期 state=%+v", state)
	}
	if state.Reason != "上游总耗时超档" {
		t.Fatalf("Reason=%q 必须保留调用方文案", state.Reason)
	}
	if state.Config.TotalTimeDeadlineMs != 120_000 || state.Config.CompactionTotalTimeDeadlineMs != 300_000 {
		t.Fatalf("Config 落盘缺总时间档位=%+v", state.Config)
	}
	key := accountLatencyStateKey(*scope, account)
	store.mu.Lock()
	observationExpiry := store.entries[key].expiresAt
	store.mu.Unlock()
	if observationExpiry != clock.NowMs()+60_000 {
		t.Fatalf("观察期 TTL expiresAt=%d want %d", observationExpiry, clock.NowMs()+60_000)
	}

	// 第三条慢样本触发降级：DegradedTTL 生效、维度落盘；reason 为空回落
	// 总时间默认文案。
	for i := 0; i < 2; i++ {
		clock.Advance(1_000)
		if _, err := service.RecordNormalRouteTotalTimeSlow(contextBackground(), account, scope, &config, ""); err != nil {
			t.Fatal(err)
		}
	}
	state = wtdLoadState(t, store, scope, account)
	if state.DegradedUntilMs == nil || *state.DegradedUntilMs != 1_002_000+120_000 {
		t.Fatalf("降级截止=%v", state.DegradedUntilMs)
	}
	if state.Dimension != LatencyDimensionTotalTime {
		t.Fatalf("Dimension=%q", state.Dimension)
	}
	if state.Reason != "普通路由速度优先总时间等待超时" {
		t.Fatalf("空 reason 必须回落默认文案=%q", state.Reason)
	}
}

// 端到端实测（2026-10-04）抓到的观察期跨通道缺陷回归：观察期（未降级）下
// 一个维度的达标样本不得清掉另一维度正在累计的慢样本计数——"首字快但总
// 时长慢"的请求上达标与慢样本并存是常态，否则总时间计数永远攒不满
// slowTriggerCount。两通道计数都归零才删除观察期 state。
func TestWtdObservationWindowCrossDimensionRetention(t *testing.T) {
	clock := newFakeClock(1_000_000)
	service, store := newMemoryLatencyService(clock)
	scope := latencyScope()
	config := wtdTotalTimeConfig()
	fast := int64(1_000)

	// 流式形态：总时间慢样本 #1 建立观察期；首字达标（1s < 阈值）只清首字
	// 通道，总时间计数必须保留。
	if _, err := service.RecordNormalRouteTotalTimeSlow(contextBackground(), latencyAccount("obs"), scope, &config, ""); err != nil {
		t.Fatal(err)
	}
	result, err := service.RecordNormalRouteFirstByteSuccess(contextBackground(), latencyAccount("obs"), scope, &config, &fast)
	if err != nil || result == nil {
		t.Fatalf("首字达标: %+v err=%v", result, err)
	}
	if result.Cleared {
		t.Fatal("另一维度仍有观察期计数时达标样本不得删除 state")
	}
	state := wtdLoadState(t, store, scope, latencyAccount("obs"))
	if state.TotalTimeSlowCount != 1 {
		t.Fatalf("总时间观察期计数必须保留，got %d", state.TotalTimeSlowCount)
	}
	if state.SlowCount != 0 {
		t.Fatalf("首字通道必须被达标清零，got %d", state.SlowCount)
	}

	// 总时间慢样本 #2 → 通道计数累计（基线 slowTriggerCount=3 未达不触发）。
	clock.Advance(1_000)
	second, err := service.RecordNormalRouteTotalTimeSlow(contextBackground(), latencyAccount("obs"), scope, &config, "")
	if err != nil || second == nil || second.Degraded || second.SlowCount != 2 {
		t.Fatalf("总时间第二条计数: %+v err=%v", second, err)
	}
	// 总时间慢样本 #3 → 计数达 slowTriggerCount，触发降级 dimension=total_time。
	clock.Advance(1_000)
	trigger, err := service.RecordNormalRouteTotalTimeSlow(contextBackground(), latencyAccount("obs"), scope, &config, "")
	if err != nil || trigger == nil || !trigger.Degraded {
		t.Fatalf("总时间第三条必须触发降级: %+v err=%v", trigger, err)
	}
	state = wtdLoadState(t, store, scope, latencyAccount("obs"))
	if state.Dimension != LatencyDimensionTotalTime {
		t.Fatalf("Dimension=%q，want total_time", state.Dimension)
	}

	// 对称：观察期首字慢样本被总时间达标清零后，首字通道计数保留语义同构。
	clock.Advance(1_000)
	other := latencyAccount("obs2")
	if _, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), other, scope, &config, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordNormalRouteTotalTimeSuccess(contextBackground(), other, scope, &config, config.TotalTimeDeadlineMs, 1_000); err != nil {
		t.Fatal(err)
	}
	otherState := wtdLoadState(t, store, scope, other)
	if otherState.SlowCount != 1 {
		t.Fatalf("首字观察期计数必须保留，got %d", otherState.SlowCount)
	}
	// 首字第 2、3 条 → 计数达 slowTriggerCount(3) 触发降级（基线 3）。
	for i := 2; i <= 3; i++ {
		clock.Advance(1_000)
		firstTrigger, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), other, scope, &config, "")
		if err != nil || firstTrigger == nil {
			t.Fatalf("首字第 %d 条: %+v err=%v", i, firstTrigger, err)
		}
		if firstTrigger.Degraded != (i == 3) {
			t.Fatalf("首字第 %d 条 Degraded=%v", i, firstTrigger.Degraded)
		}
	}

	// 单通道场景既有语义回归：观察期唯一维度的慢样本被同维度达标清零 →
	// state 删除（Cleared）。
	solo := latencyAccount("obs3")
	if _, err := service.RecordNormalRouteFirstByteSlow(contextBackground(), solo, scope, &config, ""); err != nil {
		t.Fatal(err)
	}
	soloResult, err := service.RecordNormalRouteFirstByteSuccess(contextBackground(), solo, scope, &config, &fast)
	if err != nil || soloResult == nil || !soloResult.Cleared {
		t.Fatalf("单通道达标清观察期: %+v err=%v", soloResult, err)
	}
	soloKey := accountLatencyStateKey(*scope, solo)
	store.mu.Lock()
	_, exists := store.entries[soloKey]
	store.mu.Unlock()
	if exists {
		t.Fatal("两通道计数归零后 state 必须删除")
	}
}
