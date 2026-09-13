package gatewayclientip

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// client-IP error circuit：同步入口 + 测试快照
// ---------------------------------------------------------------------------

func wiErrorScope(clientIP string) gatewaypreauth.ClientIPErrorCircuitInput {
	return gatewaypreauth.ClientIPErrorCircuitInput{SystemAccountID: "sys", APIKeyID: "key", ClientIP: clientIP}
}

func TestWIClientIPErrorCircuitSyncLifecycle(t *testing.T) {
	clock := newManualClock(time.UnixMilli(1_000_000))
	circuit, err := NewErrorCircuit(ErrorCircuitOptions{Clock: clock})
	if err != nil {
		t.Fatalf("NewErrorCircuit 失败: %v", err)
	}
	t.Cleanup(circuit.Close)

	// 空 clientIP 的作用域不成立 → 不阻塞。
	if decision := circuit.InspectClientIPErrorCircuitSync(gatewaypreauth.ClientIPErrorCircuitInput{ClientIP: "  "}); decision.Blocked {
		t.Fatal("空 IP 不得阻塞")
	}
	// 同一签名 5 次（signature threshold）→ 熔断。
	sample := gatewaypreauth.ClientIPErrorCircuitSampleInput{
		SystemAccountID: "sys", APIKeyID: "key", ClientIP: "10.0.0.1",
		Endpoint: "/v1/chat", Reason: gatewaypreauth.ClientIPErrorCircuitInvalidJSON,
		Signature: "bad-actor",
	}
	var decision gatewaypreauth.CircuitDecision
	for i := 0; i < clientIPSignatureThreshold; i++ {
		decision = circuit.RecordClientIPErrorCircuitSampleSync(sample)
	}
	if !decision.Blocked || decision.Reason != string(gatewaypreauth.ClientIPErrorCircuitInvalidJSON) {
		t.Fatalf("signature 阈值必须熔断: %+v", decision)
	}
	if decision.RetryAfterSeconds == nil || *decision.RetryAfterSeconds != 30 {
		t.Fatalf("初始封禁 30s: %+v", decision)
	}
	// 同步 inspect 同样报告阻塞。
	if inspect := circuit.InspectClientIPErrorCircuitSync(wiErrorScope("10.0.0.1")); !inspect.Blocked {
		t.Fatalf("熔断期间 inspect 必须阻塞: %+v", inspect)
	}
	// 快照报告每个条目的失败数与原因。
	preAuthRows, clientIPRows := circuit.SecuritySnapshotForTest()
	if len(preAuthRows) != 0 {
		t.Fatalf("preAuth=%+v", preAuthRows)
	}
	if len(clientIPRows) != 1 || clientIPRows[0].FailureCount != clientIPSignatureThreshold || !clientIPRows[0].Blocked {
		t.Fatalf("clientIP snapshot=%+v", clientIPRows)
	}
	if !strings.Contains(clientIPRows[0].Key, `"clientIp":"10.0.0.1"`) {
		t.Fatalf("snapshot key 必须是 JSON 作用域: %q", clientIPRows[0].Key)
	}
	// 契约：clear 重置两个熔断表，inspect 恢复放行。
	circuit.ClearGatewayClientIPErrorCircuitForTest()
	if inspect := circuit.InspectClientIPErrorCircuitSync(wiErrorScope("10.0.0.1")); inspect.Blocked {
		t.Fatalf("clear 后不得阻塞: %+v", inspect)
	}
	preAuthRows, clientIPRows = circuit.SecuritySnapshotForTest()
	if len(preAuthRows) != 0 || len(clientIPRows) != 0 {
		t.Fatalf("clear 后快照必须为空: %v %v", preAuthRows, clientIPRows)
	}

	// 契约：total 样本阈值（20 次/分钟）同样触发熔断，即便签名各不相同。
	circuit.ClearGatewayClientIPErrorCircuitForTest()
	for i := 0; i < clientIPTotalThreshold; i++ {
		sample.Signature = string(rune('a' + i))
		decision = circuit.RecordClientIPErrorCircuitSampleSync(sample)
	}
	if !decision.Blocked {
		t.Fatalf("total 阈值必须熔断: %+v", decision)
	}
	// 过期后（超过封禁窗口）恢复放行。
	clock.advance(11 * time.Minute)
	if inspect := circuit.InspectClientIPErrorCircuitSync(wiErrorScope("10.0.0.1")); inspect.Blocked {
		t.Fatalf("封禁过期后必须放行: %+v", inspect)
	}
	// success 同步删除：有 entry 返回 true，再次删除返回 false。
	if circuit.RecordClientIPErrorCircuitSuccessSync(wiErrorScope("10.0.0.1")) {
		t.Fatal("过期惰性保留的 entry 仍应存在并可删除")
	}
	if circuit.RecordClientIPErrorCircuitSuccessSync(wiErrorScope("10.0.0.1")) {
		t.Fatal("第二次删除必须返回 false")
	}
	if circuit.RecordClientIPErrorCircuitSuccessSync(gatewaypreauth.ClientIPErrorCircuitInput{ClientIP: " "}) {
		t.Fatal("空作用域删除必须返回 false")
	}
}

func TestWISignatureSampleUnmarshalJSON(t *testing.T) {
	// 契约：JSON 形状是 Node 元组 ["signature",[ms...]]。
	var sample signatureSample
	if err := sample.UnmarshalJSON([]byte(`["sig",[1,2,3]]`)); err != nil {
		t.Fatalf("UnmarshalJSON 失败: %v", err)
	}
	if sample.Signature != "sig" || len(sample.Samples) != 3 || sample.Samples[2] != 3 {
		t.Fatalf("sample=%+v", sample)
	}
	// 空数组省略时回落空切片。
	var empty signatureSample
	if err := empty.UnmarshalJSON([]byte(`["sig",[]]`)); err != nil || len(empty.Samples) != 0 {
		t.Fatalf("空 samples: %+v err=%v", empty, err)
	}
	// 非元组 / 非字符串签名必须报错。
	if err := (&signatureSample{}).UnmarshalJSON([]byte(`42`)); err == nil {
		t.Fatal("非元组必须报错")
	}
	if err := (&signatureSample{}).UnmarshalJSON([]byte(`[7,[]]`)); err == nil {
		t.Fatal("非字符串签名必须报错")
	}
}

func TestWIJsonScopeKeyUnescapesHTML(t *testing.T) {
	// 契约：与 Node JSON.stringify 字节一致，< > & 不做 \u 转义。
	key := jsonScopeKey("sys", "k<1>", `a&"b"`)
	if strings.Contains(key, `\u003c`) || strings.Contains(key, `\u0026`) {
		t.Fatalf("不得保留 HTML 转义: %q", key)
	}
	if !strings.Contains(key, `a&`) || !strings.Contains(key, `k<1>`) || !strings.Contains(key, `\"b\"`) {
		t.Fatalf("原文必须保留（引号按 JSON 转义）: %q", key)
	}
	// sampleSignature：空 signature 回落 reason。
	got := sampleSignature(gatewaypreauth.ClientIPErrorCircuitSampleInput{
		Endpoint: "/V1 Chat", Reason: gatewaypreauth.ClientIPErrorCircuitInvalidJSON,
	})
	if want := "/v1 chat|invalid_json|invalid_json"; got != want {
		t.Fatalf("signature=%q want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// avoidance：tracker 工厂、redis 异步路径、transfer、TTL
// ---------------------------------------------------------------------------

func TestWIAvoidanceCreateTrackerPortAndClearForTest(t *testing.T) {
	avoidance, _ := newTestAvoidance(t, nil)
	// G05 端口入口返回冻结接口；实现必须是同包的 *AvoidanceTracker。
	portTracker := avoidance.CreateTracker(gatewaypreauth.ClientIPAccountAvoidanceInput{
		SystemAccountID: "sys", GroupID: "grp", APIKeyID: "key", ClientIP: "10.0.0.1",
	})
	if portTracker == nil {
		t.Fatal("port tracker 不得为 nil")
	}
	tracker := avoidance.CreateAvoidanceTracker(AvoidanceScopeInput{SystemAccountID: "sys", GroupID: "grp", APIKeyID: "key", ClientIP: "10.0.0.1"})
	if tracker.Scope == nil || tracker.Scope.clientIP != "10.0.0.1" {
		t.Fatalf("tracker scope=%+v", tracker.Scope)
	}
	// 空 clientIP 的 tracker scope 为 nil，确认/记录入口必须全部 no-op。
	nilTracker := avoidance.CreateAvoidanceTracker(AvoidanceScopeInput{ClientIP: " "})
	avoidance.RememberPendingFailure(nilTracker, "a1", "A1", AccountFailure{ErrorPhase: "stream"})
	if result := avoidance.ConfirmAfterFinalFailure(nilTracker, nil); len(result.ConfirmedAccountIDs) != 0 {
		t.Fatalf("nil scope tracker 不得确认: %+v", result)
	}
	if avoidance.ClearForAccount(nilTracker, "a1") {
		t.Fatal("nil scope tracker 清除必须返回 false")
	}
	// ClearForTest 重置内存表。
	realTracker := avoidance.CreateAvoidanceTracker(AvoidanceScopeInput{ClientIP: "10.0.0.2"})
	avoidance.RememberPendingFailure(realTracker, "a2", "A2", AccountFailure{ErrorPhase: "stream"})
	avoidance.ConfirmAfterFinalFailure(realTracker, nil)
	avoidance.ClearForTest()
	if rows := avoidance.SnapshotForTest(); len(rows) != 0 {
		t.Fatalf("ClearForTest 后快照必须为空: %+v", rows)
	}
}

func TestWIAvoidanceSnapshotMalformedScopeKey(t *testing.T) {
	avoidance, clock := newTestAvoidance(t, nil)
	// 直接注入畸形 scopeKey 的条目，覆盖 parseAvoidanceScopeKey 的降级分支。
	avoidance.setMemoryAvoidanceEntry("broken", avoidanceEntry{
		AccountID: "a1", ScopeKey: "{not-json", FailureCount: 2,
		FirstFailedAtMs: clock.Now().UnixMilli(), LastFailedAtMs: clock.Now().UnixMilli(),
	}, 60_000)
	rows := avoidance.SnapshotForTest()
	if len(rows) != 1 {
		t.Fatalf("rows=%+v", rows)
	}
	// FailureCount=2 已达激活阈值：Active 与 scope 解析无关。
	if !rows[0].Active || rows[0].ClientIP != "" || rows[0].APIKeyID != "" || rows[0].SystemAccountID != "" {
		t.Fatalf("畸形 scope 必须降级为空字段: %+v", rows[0])
	}
}

func TestWIAvoidanceRedisAsyncContract(t *testing.T) {
	server := miniredis.RunT(t)
	avoidance, clock := newTestAvoidance(t, func(opts *AvoidanceOptions) {
		opts.RuntimeStateDriver = RuntimeStateDriverRedis
		opts.StateRedisURL = "redis://" + server.Addr()
		opts.RedisNamespace = "dev"
	})
	ctx := context.Background()
	scope := AvoidanceScopeInput{SystemAccountID: "sys", APIKeyID: "key", ClientIP: "10.0.0.1"}

	tracker := avoidance.CreateAvoidanceTracker(scope)
	avoidance.RememberPendingFailure(tracker, "bad", "Bad", AccountFailure{ErrorPhase: "upstream_response"})
	confirmed, err := avoidance.ConfirmAfterFinalFailureAsync(ctx, tracker, nil)
	if err != nil || len(confirmed.ConfirmedAccountIDs) != 1 {
		t.Fatalf("redis confirm=%+v err=%v", confirmed, err)
	}
	// 两次失败 → 规避激活，async ordering 把 bad 后置。
	accounts := []gatewayruntimecache.OpenAIAccountSecret{account("bad", 1, false, false), account("good", 1, false, false)}
	result, err := avoidance.OrderAccountsByClientIPAccountAvoidanceAsync(ctx, accounts, scope, nil)
	if err != nil {
		t.Fatalf("ordering 失败: %v", err)
	}
	// 只有 1 次确认（failureCount=1 < 阈值 2）→ 未生效。
	if result.Applied {
		t.Fatalf("低于阈值不得规避: %+v", result)
	}
	tracker2 := avoidance.CreateAvoidanceTracker(scope)
	avoidance.RememberPendingFailure(tracker2, "bad", "Bad", AccountFailure{ErrorPhase: "upstream_response"})
	if _, err := avoidance.ConfirmAfterFinalFailureAsync(ctx, tracker2, nil); err != nil {
		t.Fatalf("第二次确认失败: %v", err)
	}
	result, err = avoidance.OrderAccountsByClientIPAccountAvoidanceAsync(ctx, accounts, scope, nil)
	if err != nil || !result.Applied {
		t.Fatalf("第二次 ordering=%+v err=%v", result, err)
	}
	if result.Accounts[0].ID != "good" || result.AvoidedAccountIDs[0] != "bad" {
		t.Fatalf("同 tier 下 fresh 必须前置: %+v", result)
	}
	// 全部被规避 → bypass 标记。
	onlyBad := []gatewayruntimecache.OpenAIAccountSecret{account("bad", 1, false, false)}
	result, err = avoidance.OrderAccountsByClientIPAccountAvoidanceAsync(ctx, onlyBad, scope, nil)
	if err != nil || !result.BypassedAllAvoided || result.Applied {
		t.Fatalf("bypass result=%+v err=%v", result, err)
	}
	// success 后 clear（redis 删除）。
	cleared, err := avoidance.ClearForAccountAsync(ctx, tracker2, "bad")
	if err != nil || !cleared {
		t.Fatalf("clear=%v err=%v", cleared, err)
	}
	if cleared, err := avoidance.ClearForAccountAsync(ctx, tracker2, "bad"); err != nil || cleared {
		t.Fatalf("二次 clear=%v err=%v", cleared, err)
	}
	// redis 条目真的被删除。
	if server.Exists("juhe-ai:dev:state:gateway-client-ip-account-avoidance:entry:" + avoidanceEntryKey(normalizeAvoidanceScope(scope), "bad")) {
		t.Fatal("clear 后 redis 条目必须删除")
	}
	_ = clock

	// redis 故障透传：async ordering 报错。
	server.SetError("模拟故障")
	tracker3 := avoidance.CreateAvoidanceTracker(scope)
	avoidance.RememberPendingFailure(tracker3, "bad", "Bad", AccountFailure{ErrorPhase: "stream"})
	if _, err := avoidance.ConfirmAfterFinalFailureAsync(ctx, tracker3, nil); err == nil {
		t.Fatal("redis 故障必须透传")
	}
	if _, err := avoidance.OrderAccountsByClientIPAccountAvoidanceAsync(ctx, accounts, scope, nil); err == nil {
		t.Fatal("ordering 的 redis 故障必须透传")
	}
	if _, err := avoidance.ConfirmAfterSuccessAsync(ctx, tracker3, "bad", nil); err == nil {
		t.Fatal("success confirm 的 redis 故障必须透传")
	}
	if _, err := avoidance.ClearForAccountAsync(ctx, tracker3, "bad"); err == nil {
		t.Fatal("clear 的 redis 故障必须透传")
	}
}

func TestWIAvoidanceTransferPendingAndLimits(t *testing.T) {
	avoidance, _ := newTestAvoidance(t, nil)
	source := avoidance.CreateAvoidanceTracker(AvoidanceScopeInput{SystemAccountID: "sys", ClientIP: "10.0.0.1"})
	target := avoidance.CreateAvoidanceTracker(AvoidanceScopeInput{SystemAccountID: "sys", ClientIP: "10.0.0.1"})
	status := int64(503)
	avoidance.RememberPendingFailure(source, "a1", "A1", AccountFailure{ErrorPhase: "stream", StatusCode: &status, ErrorCode: "E1", ErrorMessage: "boom", Endpoint: "/v1"})
	// 契约：转移搬空 source 并保留失败详情；target 上同名账户覆盖合并。
	avoidance.TransferPendingFailures(source, target)
	if len(source.PendingFailures) != 0 {
		t.Fatalf("source 未清空: %+v", source.PendingFailures)
	}
	if len(target.PendingFailures) != 1 || target.PendingFailures[0].AccountID != "a1" || target.PendingFailures[0].ErrorCode != "E1" {
		t.Fatalf("target=%+v", target.PendingFailures)
	}
	// nil / 空 source 防护。
	avoidance.TransferPendingFailures(nil, target)
	avoidance.TransferPendingFailures(source, nil)
	// pending 上限 256：第 257 个 distinct 账户被丢弃。
	overflow := avoidance.CreateAvoidanceTracker(AvoidanceScopeInput{SystemAccountID: "sys", ClientIP: "10.0.0.9"})
	for i := 0; i < clientIPAccountAvoidanceMaxPendingFailures; i++ {
		avoidance.RememberPendingFailure(overflow, string(rune('A'+i/26))+string(rune('a'+i%26)), "N", AccountFailure{ErrorPhase: "stream"})
	}
	avoidance.RememberPendingFailure(overflow, "OVERFLOW", "N", AccountFailure{ErrorPhase: "stream"})
	if len(overflow.PendingFailures) != clientIPAccountAvoidanceMaxPendingFailures {
		t.Fatalf("上限失效: %d", len(overflow.PendingFailures))
	}
}

func TestWIAvoidanceTTLBoundaries(t *testing.T) {
	// 契约：nil settings → 默认 5 分钟；分钟数下限 1；上限 10 分钟。
	if got := avoidanceTTL(nil); got != clientIPAccountAvoidanceDefaultTTL {
		t.Fatalf("nil settings ttl=%d", got)
	}
	if got := avoidanceTTL(&gatewayruntimecache.GatewaySettings{}); got != 60_000 {
		t.Fatalf("0 分钟回落 1 分钟: %d", got)
	}
	if got := avoidanceTTL(&gatewayruntimecache.GatewaySettings{DefaultTemporaryUnschedulableMinutes: 999}); got != clientIPAccountAvoidanceMaxTTL {
		t.Fatalf("上限失效: %d", got)
	}
	if got := avoidanceTTL(&gatewayruntimecache.GatewaySettings{DefaultTemporaryUnschedulableMinutes: 8}); got != 8*60_000 {
		t.Fatalf("普通分钟数: %d", got)
	}
}

// ---------------------------------------------------------------------------
// scheduling：分组调度策略解析矩阵与数值 helper
// ---------------------------------------------------------------------------

func TestWIResolveGroupSchedulingPolicyTable(t *testing.T) {
	valid := map[string]any{
		"maxQueueSize": float64(10), "perApiKeyQueueLimit": float64(5),
		"maxQueueWaitMs": float64(2_000), "imageLaneMaxConcurrency": float64(3),
		"clientIpConcurrencyLimit": float64(4), "clientIpConcurrencyOverflowMode": "queue",
	}
	policy, err := ResolveGroupSchedulingPolicy(valid, HighConcurrencyPolicyDefaults{})
	if err != nil {
		t.Fatalf("合法策略解析失败: %v", err)
	}
	if policy.MaxQueueSize != 10 || policy.PerAPIKeyQueueLimit != 5 || policy.MaxQueueWaitMs != 2_000 ||
		policy.ImageLaneMaxConcurrency != 3 || policy.ClientIPConcurrencyLimit != 4 || policy.ClientIPConcurrencyOverflowMode != "queue" {
		t.Fatalf("policy=%+v", policy)
	}
	// nil 策略 → 全默认（maxQueueSize 回落 defaults，再回落 1）。
	policy, err = ResolveGroupSchedulingPolicy(nil, HighConcurrencyPolicyDefaults{MaxQueueSize: 7, PerAPIKeyQueueLimit: 99})
	if err != nil {
		t.Fatalf("nil 策略解析失败: %v", err)
	}
	if policy.MaxQueueSize != 7 || policy.PerAPIKeyQueueLimit != 7 || policy.MaxQueueWaitMs != 60_000 ||
		policy.ImageLaneMaxConcurrency != 0 || policy.ClientIPConcurrencyOverflowMode != "reject" {
		t.Fatalf("默认 policy=%+v", policy)
	}

	tests := []struct {
		name   string
		policy map[string]any
	}{
		{name: "maxQueueSize 非数值", policy: map[string]any{"maxQueueSize": "ten"}},
		{name: "maxQueueSize 小数", policy: map[string]any{"maxQueueSize": 1.5}},
		{name: "maxQueueSize 低于下限", policy: map[string]any{"maxQueueSize": float64(0)}},
		{name: "maxQueueSize 超过上限", policy: map[string]any{"maxQueueSize": float64(2_000_000)}},
		{name: "perApiKeyQueueLimit 超过 maxQueueSize", policy: map[string]any{"maxQueueSize": float64(4), "perApiKeyQueueLimit": float64(5)}},
		{name: "maxQueueWaitMs 超上限", policy: map[string]any{"maxQueueWaitMs": float64(3_600_001)}},
		{name: "imageLaneMaxConcurrency 负数", policy: map[string]any{"imageLaneMaxConcurrency": float64(-1)}},
		{name: "clientIpConcurrencyLimit 非数值", policy: map[string]any{"clientIpConcurrencyLimit": true}},
		{name: "overflowMode 非法值", policy: map[string]any{"clientIpConcurrencyOverflowMode": "drop"}},
		{name: "overflowMode 非字符串", policy: map[string]any{"clientIpConcurrencyOverflowMode": float64(1)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ResolveGroupSchedulingPolicy(tc.policy, HighConcurrencyPolicyDefaults{}); err == nil {
				t.Fatal("非法策略必须报错")
			}
		})
	}
	// int/int64 数值类型同样接受（toFloat64 支持矩阵）。
	if _, err := ResolveGroupSchedulingPolicy(map[string]any{"maxQueueSize": int64(4), "maxQueueWaitMs": int32(1_000)}, HighConcurrencyPolicyDefaults{}); err != nil {
		t.Fatalf("整型数值必须接受: %v", err)
	}
}

func TestWISchedulingHelpers(t *testing.T) {
	// toFloat64 类型矩阵。
	if _, ok := toFloat64(uint8(1)); ok {
		t.Fatal("uint8 不在支持矩阵中")
	}
	if got, ok := toFloat64(float32(1.5)); !ok || got != 1.5 {
		t.Fatalf("float32: %v %v", got, ok)
	}
	// coerceNumber：数字字符串转换，空白与非数字回落。
	if got, ok := coerceNumber(" 2.5 "); !ok || got != 2.5 {
		t.Fatalf("数字字符串: %v %v", got, ok)
	}
	if _, ok := coerceNumber("  "); ok {
		t.Fatal("空白字符串必须失败")
	}
	if _, ok := coerceNumber("abc"); ok {
		t.Fatal("非数字必须失败")
	}
	// normalize 系列：NaN 等价回落 + 负数夹取。
	if got := normalizeNonNegativeInteger("5", 1); got != 5 {
		t.Fatalf("normalizeNonNegativeInteger=%d", got)
	}
	if got := normalizeNonNegativeInteger(-3.5, 1); got != 0 {
		t.Fatalf("负数必须夹到 0: %d", got)
	}
	if got := normalizeNonNegativeInteger(nil, 9); got != 9 {
		t.Fatalf("回落失败: %d", got)
	}
	if got := normalizePositiveInteger(0, 7); got != 1 {
		t.Fatalf("可转换的 0 必须抬到下限 1（fallback 只用于不可转换值）: %d", got)
	}
	if got := normalizePositiveInteger("2", 7); got != 2 {
		t.Fatalf("字符串数字: %d", got)
	}
	// positiveIntClamp / nonZero / minInt / maxInt64 / sortedMapKeys。
	if positiveIntClamp(0, 1, 5) != 1 || positiveIntClamp(9, 1, 5) != 5 || positiveIntClamp(3, 1, 5) != 3 {
		t.Fatal("positiveIntClamp 边界错误")
	}
	if nonZero(0, 4) != 4 || nonZero(2, 4) != 2 {
		t.Fatal("nonZero 错误")
	}
	if minInt(2, 1) != 1 || maxInt64(1, 2) != 2 {
		t.Fatal("min/max 错误")
	}
	if got := sortedMapKeys(map[string]int{"b": 1, "a": 2}); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("sortedMapKeys=%v", got)
	}
	// EffectiveImageLaneConcurrencyLimit：策略上限与硬上限取小。
	base := GroupSchedulingPolicy{}
	if got := EffectiveImageLaneConcurrencyLimit(10, base); got != 10 {
		t.Fatalf("无策略上限: %d", got)
	}
	if got := EffectiveImageLaneConcurrencyLimit(10, GroupSchedulingPolicy{ImageLaneMaxConcurrency: 3}); got != 3 {
		t.Fatalf("策略上限生效: %d", got)
	}
	if got := EffectiveImageLaneConcurrencyLimit(2, GroupSchedulingPolicy{ImageLaneMaxConcurrency: 3}); got != 2 {
		t.Fatalf("硬上限更小: %d", got)
	}
}

// ---------------------------------------------------------------------------
// group queue：Clear 的 aborted 出队路径（queueSize/perAPIKey 统计）
// ---------------------------------------------------------------------------

func TestWIGroupQueueClearAbortsWaiters(t *testing.T) {
	queue, concurrency, _ := newTestGroupQueue(t, nil)
	input := groupWaitInput("a1")
	input.AccountConcurrencyLimits = map[string]int{"a1": 1}
	concurrency.setTotal("a1", 1) // 打满 → 排队。

	waiter := make(chan HighConcurrencyQueueWaitResult, 1)
	go func() {
		result, err := queue.WaitForHighConcurrencyGroupCapacity(context.Background(), input)
		if err != nil {
			t.Error(err)
			waiter <- HighConcurrencyQueueWaitResult{}
			return
		}
		waiter <- result
	}()
	waitForQueueSnapshotSize(t, queue, 1)
	// 契约：Clear 出队所有等待者并以 aborted 拒绝，拒绝结果携带出队时刻的
	// 队列统计（queueSizeLocked / perAPIKeyQueueSizeLocked）。
	queue.Clear()
	select {
	case result := <-waiter:
		if result.Ready || result.Reason != QueueRejectAborted {
			t.Fatalf("result=%+v", result)
		}
		if result.QueueSize != 1 || result.PerAPIKeyQueueSize != 1 {
			t.Fatalf("拒绝结果必须携带出队前统计: %+v", result)
		}
		if result.WaitedMs < 0 {
			t.Fatalf("waitedMs=%d", result.WaitedMs)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Clear 后等待者未收到 aborted 结果")
	}
	if rows := queue.Snapshot(); len(rows) != 0 {
		t.Fatalf("Clear 后快照必须为空: %+v", rows)
	}
	// 再次 Clear（空表）必须稳定返回。
	queue.Clear()
}

func waitForQueueSnapshotSize(t *testing.T, queue *HighConcurrencyGroupQueue, want int) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if rows := queue.Snapshot(); len(rows) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("队列在 1s 内未达到长度 %d", want)
}

// ---------------------------------------------------------------------------
// concurrency / policy / cache / rfc3339 的局部 helper 契约
// ---------------------------------------------------------------------------

func TestWINumericRedisArrayAndMaxInt(t *testing.T) {
	// miniredis/go-redis 的 EVAL 返回 int64 数组；契约同时兼容 int/string/未知。
	values := numericRedisArray([]any{int64(1), int(2), "3", true})
	if len(values) != 4 || values[0] != 1 || values[1] != 2 || values[2] != 3 || values[3] != 0 {
		t.Fatalf("values=%v", values)
	}
	if numericRedisArray("not-an-array") != nil {
		t.Fatal("非数组必须返回 nil")
	}
	if maxInt(2, 1) != 2 || maxInt(1, 2) != 2 {
		t.Fatal("maxInt 错误")
	}
}

func TestWISQLPolicySourceGuardsAndPostgresDialect(t *testing.T) {
	if _, err := NewSQLPolicySource(nil, false, nil, nil); err == nil {
		t.Fatal("nil db 必须报错")
	}
	// postgres 方言的 table 名限定与占位符重写。
	pg := &SQLPolicySource{postgres: true}
	if got := pg.table("client_ip_policies"); got != "juhe_stats.client_ip_policies" {
		t.Fatalf("table=%q", got)
	}
	if got := pg.bind("VALUES (?, ?, ?)"); got != "VALUES ($1, $2, $3)" {
		t.Fatalf("bind=%q", got)
	}
	sqliteSource := &SQLPolicySource{}
	if got := sqliteSource.table("client_ip_policies"); got != "client_ip_policies" {
		t.Fatalf("sqlite table=%q", got)
	}
	if got := sqliteSource.bind("VALUES (?)"); got != "VALUES (?)" {
		t.Fatalf("sqlite bind=%q", got)
	}

	// 契约：postgres 模式限定 juhe_stats schema。用内存 SQLite 承载该模式时
	// schema 不存在，写入必须报错（错误路径），而不是静默成功。
	db, err := sql.Open("sqlite", "file:wi-policy-pg-dialect?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("打开 sqlite 失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fixed := time.UnixMilli(1_700_000_000_000)
	pgSource, err := NewSQLPolicySource(db, true, func() time.Time { return fixed }, nil)
	if err != nil {
		t.Fatalf("NewSQLPolicySource 失败: %v", err)
	}
	err = pgSource.RecordClientIPPolicyHits(context.Background(), []PolicyHitInput{{
		IPHash: strings.Repeat("a", 64), PolicyID: "p1", HitCount: 1,
		HitAt: fixed.UTC().Format(time.RFC3339),
	}})
	if err == nil {
		t.Fatal("postgres 方言写入 sqlite 必须报错")
	}
	if _, err := pgSource.ListActiveClientIPPolicies(context.Background()); err == nil {
		t.Fatal("postgres 方言查询 sqlite 必须报错")
	}
	// 空 hits 直接成功，不触碰数据库。
	if err := pgSource.RecordClientIPPolicyHits(context.Background(), nil); err != nil {
		t.Fatalf("空 hits 必须无错: %v", err)
	}
	// hitAt 非法与 timezone 无效的错误分支。
	badHitAt := pgSource
	if err := badHitAt.RecordClientIPPolicyHits(context.Background(), []PolicyHitInput{{
		IPHash: strings.Repeat("a", 64), PolicyID: "p1", HitAt: "not-a-time",
	}}); err == nil {
		t.Fatal("坏 hitAt 必须报错")
	}
	badTimezone := pgSource
	badTimezone.timezone = func(context.Context) (string, error) { return "Not/AZone", nil }
	if err := badTimezone.RecordClientIPPolicyHits(context.Background(), []PolicyHitInput{{
		IPHash: strings.Repeat("a", 64), PolicyID: "p1",
	}}); err == nil {
		t.Fatal("非法时区必须报错")
	}
	brokenTimezone := &SQLPolicySource{db: db, postgres: true, now: func() time.Time { return fixed }}
	brokenTimezone.timezone = func(context.Context) (string, error) { return "", errors.New("读取时区失败") }
	if err := brokenTimezone.RecordClientIPPolicyHits(context.Background(), []PolicyHitInput{{
		IPHash: strings.Repeat("a", 64), PolicyID: "p1",
	}}); err == nil {
		t.Fatal("时区读取失败必须透传")
	}
	// normalizeIpHash 守卫：非法 hash 读作 missing。
	if err := db.Close(); err != nil {
		t.Fatalf("关闭 db 失败: %v", err)
	}
	if _, err := pgSource.FindActiveClientIPPolicyByHash(context.Background(), "not-a-hash"); err != nil {
		t.Fatalf("非法 hash 必须读作 missing: %v", err)
	}
}

func TestWIIsActiveClientIPPolicyAt(t *testing.T) {
	now := int64(1_000_000)
	if active, err := isActiveClientIPPolicyAt(ActiveClientIPPolicy{}, now); err != nil || !active {
		t.Fatalf("无 expiresAt 恒活跃: %v %v", active, err)
	}
	future := "2030-01-01T00:00:00Z"
	policy := ActiveClientIPPolicy{ExpiresAt: &future}
	if active, err := isActiveClientIPPolicyAt(policy, now); err != nil || !active {
		t.Fatalf("未来过期必须活跃: %v %v", active, err)
	}
	past := "1970-01-01T00:00:00Z"
	policy.ExpiresAt = &past
	if active, err := isActiveClientIPPolicyAt(policy, now); err != nil || active {
		t.Fatalf("已过期必须不活跃: %v %v", active, err)
	}
	bad := "not-a-time"
	policy.ExpiresAt = &bad
	if _, err := isActiveClientIPPolicyAt(policy, now); err == nil {
		t.Fatal("坏 expiresAt 必须报错")
	}
}

func TestWIDecodeSharedJSONAndRfc3339Helpers(t *testing.T) {
	var dst struct {
		N int `json:"n"`
	}
	if found, err := decodeSharedJSON([]byte(`not-json`), &dst); err == nil || found {
		t.Fatalf("坏 JSON 必须报错: %v %v", found, err)
	}
	if found, err := decodeSharedJSON([]byte(`{"n":5}`), &dst); err != nil || !found || dst.N != 5 {
		t.Fatalf("往返失败: %v %v %+v", found, err, dst)
	}
	// rfc3339 端口：空白、裸日期、错误 offset 都必须拒绝。
	for _, bad := range []string{"", "   ", "2030-01-01T00:00:00", "2030-01-01", "2030-01-01T00:00:00+0800"} {
		if _, ok := rfc3339Millis(bad); ok {
			t.Fatalf("%q 必须拒绝", bad)
		}
		if _, ok := parseRFC3339InstantTime(bad); ok {
			t.Fatalf("parseRFC3339InstantTime %q 必须拒绝", bad)
		}
	}
	millis, ok := rfc3339Millis(" 2030-01-01T00:00:00.500Z ")
	if !ok || millis != time.Date(2030, 1, 1, 0, 0, 0, 500_000_000, time.UTC).UnixMilli() {
		t.Fatalf("millis=%d ok=%v", millis, ok)
	}
	parsed, ok := parseRFC3339InstantTime("2030-01-01T08:00:00+08:00")
	if !ok {
		t.Fatal("数值 offset 必须接受")
	}
	// canonicalRFC3339 等价 Node toISOString：毫秒精度 + UTC Z。
	if got := canonicalRFC3339(parsed); got != "2030-01-01T00:00:00.000Z" {
		t.Fatalf("canonical=%q", got)
	}
}
