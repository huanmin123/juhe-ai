package circuitruntime

// w14g：熔断运行时族覆盖率补强。攻击 w11c 后仍剩余的错误/防御分支。
//
// 不可达语句登记（分析依据见各条；均不需要在本轮构造可达路径）：
//   - contract.go ValidateGatewayAccountCircuitState 中 HalfOpenOrigin 的第三
//     次白名单守卫（463-465 已随本波次删除）：第 457/460 行检查已完全约束
//     该字段取值。
//   - runtime.go RestoreGatewayAccountCircuit 中 runtimeStateToWire 错误分支
//     （1308-1311 已随本波次删除）：函数体第一行 ValidateGatewayAccountCircuitState
//     与入参校验重复，两次校验间无状态变化。
//   - runtime.go RecordGatewayAccountCircuitProtocolModelOpenEvidence 的
//     GatewayAccountCircuitScopeKey(accountScope) 错误分支（1333-1335）：
//     validateIdentity 已通过同一 AccountRuntimeKey 的 scope 校验，account
//     scope 是其必为合法的子集。
//   - runtime.go/revision.go 中 json.Marshal 纯数据结构的错误分支
//     （runtime.go 1342-1344、1457-1459；incident.go 414-416、446-448）。
//   - 各 Redis Lua 脚本响应的 runtimeRedisBytes / json.Unmarshal /
//     runtimeStateFromWire / status 白名单 / 负数计数分支
//     （runtime.go 1350/1354/1358/1364/1367/1382/1397/1401/1405/1424/1430/
//     1433/1469/1473/1479；revision.go 81-83/85-87/96-97/99-101；incident.go
//     450-452/454-456/498-501）：返回值由内置 Lua 决定，客户端无法注入。
//   - runtime_index.go newAccountCircuitRuntimeIndexEpoch rand.Read 错误分支
//     （817-819 及其在 Backfill 前置的 341-343）：crypto/rand 在受支持平台
//     不会失败。
//   - runtime_index.go scanAndApply/seedDispatchRevisions 中单次调用内的中途
//     状态破坏分支（HScan 错误 446-448、奇数字段 449-451、renew 丢失
//     465-467/518-520、json.Marshal 469-471/522-524、advance/finalize 的
//     372-374/380-382/388-390/391-393/398-400）：需要并发竞争注入。
//   - runtime_index.go accountCircuitRuntimeIndexItemFromSource states 路径的
//     ParseInt 分支（556-558）：validateAccountCircuitRuntimeIndexStateEntry
//     已先经 runtimeWireRevision 解析同一字符串并要求 >=1。
//   - runtime_index.go seedDispatchRevisions 重复账号分支（505-507）：页内/
//     跨页重复必然先触发 502 的有序性检查（previous 为上一账号，重复即 <=）。
//   - runtime_index.go audit evidence 账号不一致分支（707-709）：escalation
//     item 的 account 由同一 runtime 字段派生，与 states 写入必然一致。
//   - runtime_index.go readHash 奇数字段（766-768）与同字段重扫（778-782）：
//     HSCAN 恒返回偶数对，跨页同键不同值需页间并发写。
//   - owner.go CheckReady 的 Ping 通过后 HMGet 错误分支已由 WRONGTYPE 注入
//     覆盖；incident.go 472-473（stale 提前返回包装）保持既有覆盖。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

// ---------------------------------------------------------------------------
// 助手
// ---------------------------------------------------------------------------

func w14gRawClient(t *testing.T, server *miniredis.Miniredis) *goredis.Client {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// w14gClientWithNamespace 构造绕过 NewClient 的 Client（用于命名空间非法分支）。
func w14gClientWithNamespace(t *testing.T, server *miniredis.Miniredis, namespace string) *Client {
	t.Helper()
	return &Client{client: w14gRawClient(t, server), namespace: namespace}
}

// w14gStoppedReadyStore 返回已完成索引发布、但 Redis 已停止的 store，
// 用于触发脚本执行错误分支。
func w14gStoppedReadyStore(t *testing.T, clock *w7bClock) (*Store, *AccountCircuitRuntimeStore) {
	t.Helper()
	server := miniredis.RunT(t)
	store, err := New(Config{URL: "redis://" + server.Addr(), Namespace: "w14g-dead", Retention: time.Minute}, w7bGate())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	reader := DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{}, nil
	})
	if _, err := store.BackfillRuntimeIndex(context.Background(), GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "w14g-owner"}, reader); err != nil {
		t.Fatalf("发布运行时索引失败: %v", err)
	}
	server.Close()
	return store, store.runtime.WithNow(clock.Now)
}

func w14gValidIncident(clock *w7bClock) GatewayAccountCircuitIncident {
	scope := w7bAccountScope("w14g-acc")
	now := clock.Now()
	retained := now.Add(time.Minute)
	return GatewayAccountCircuitIncident{
		CircuitScopeKey: mustGatewayAccountCircuitScopeKey(scope), AccountID: "w14g-acc",
		AccountRuntimeKey: "w14g-acc", ScopeKind: "account", IncidentID: "w14g-i",
		State: "CLOSED", Generation: 1, DispatchRevision: 1, LedgerRevision: 1,
		TransitionID: "w14g-t", RetainedUntil: &retained, UpdatedAt: now,
	}
}

func w14gValidEvent() GatewayAccountCircuitOutboxEvent {
	return GatewayAccountCircuitOutboxEvent{
		EventType: GatewayAccountCircuitDispatchRevisionChanged, ProjectionKey: GatewayAccountCircuitProjectionKey,
		AccountID: "w14g-acc", AccountRuntimeKey: "w14g-acc", TransitionID: "w14g-t", DispatchRevision: 1,
	}
}

// w14gSuspectWire 构造能通过 runtimeStateFromWire 校验的 SUSPECT 状态 wire。
func w14gSuspectWire(clock *w7bClock, accountID string) (accountCircuitRuntimeStateWire, string) {
	scope := w7bAccountScope(accountID)
	field := mustGatewayAccountCircuitScopeKey(scope)
	return accountCircuitRuntimeStateWire{
		ScopeKey: field,
		Scope:    accountCircuitRuntimeScopeWire{Kind: string(scope.Kind), AccountRuntimeKey: accountID},
		Phase:    "SUSPECT", Generation: 1, DispatchRevision: "1", TransitionID: "w14g-t",
		UpdatedAtMS: clock.Now().UnixMilli(),
	}, field
}

// w14gSeedStateSource 序列化一个合法的 states 来源条目。
func w14gSeedStateSource(clock *w7bClock, accountID string) (wire accountCircuitRuntimeStateWire, field, source string) {
	wire, field = w14gSuspectWire(clock, accountID)
	raw, err := json.Marshal(accountCircuitRuntimeIndexStateEntry{State: wire, ReplayOrder: []string{"w14g-t"}})
	if err != nil {
		panic(err)
	}
	return wire, field, string(raw)
}

func w14gNormalizedInput(t *testing.T) GatewayAccountCircuitRuntimeIndexBackfillInput {
	t.Helper()
	input, err := normalizeAccountCircuitRuntimeIndexBackfillInput(GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "w14g-owner"})
	if err != nil {
		t.Fatal(err)
	}
	return input
}

// w14gAuditFixture 准备 audit 直驱环境：锁 + 可选 states 条目 + revisions。
func w14gAuditFixture(t *testing.T, clock *w7bClock) (*AccountCircuitRuntimeIndexBackfiller, GatewayAccountCircuitRuntimeIndexBackfillInput) {
	t.Helper()
	server := miniredis.RunT(t)
	store, err := New(Config{URL: "redis://" + server.Addr(), Namespace: "w14g-audit", Retention: time.Minute}, w7bGate())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	backfiller, err := NewAccountCircuitRuntimeIndexBackfiller(store.client)
	if err != nil {
		t.Fatal(err)
	}
	lockToken := "w14g-owner:w14g-epoch"
	if err := store.client.client.Set(context.Background(), backfiller.keys.indexLock, lockToken, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	return backfiller.WithDispatchRevisionReader(DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{}, nil
	})), w14gNormalizedInput(t)
}

func w14gSeedValidStateForAudit(t *testing.T, b *AccountCircuitRuntimeIndexBackfiller, clock *w7bClock) {
	t.Helper()
	_, field, source := w14gSeedStateSource(clock, "w14g-acc")
	ctx := context.Background()
	if err := b.client.client.HSet(ctx, b.keys.states, field, source).Err(); err != nil {
		t.Fatal(err)
	}
	if err := b.client.client.HSet(ctx, b.keys.revisions, "w14g-acc", "1").Err(); err != nil {
		t.Fatal(err)
	}
}

func w14gFailOnSuccess(t *testing.T, err error, label string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s 必须失败", label)
	}
}

// ---------------------------------------------------------------------------
// contract.go
// ---------------------------------------------------------------------------

func TestW14GContractClosedStateErrorBranches(t *testing.T) {
	if _, err := GatewayAccountCircuitClosedState(GatewayAccountCircuitScope{}, 0, 0, "", time.Now()); err == nil {
		t.Fatalf("非法 scope 必须失败")
	}
	if err := ValidateGatewayAccountCircuitState(GatewayAccountCircuitState{}); err == nil {
		t.Fatalf("空状态必须失败")
	}
}

// ---------------------------------------------------------------------------
// incident.go
// ---------------------------------------------------------------------------

func TestW14GIncidentRestorerKeysError(t *testing.T) {
	server := miniredis.RunT(t)
	restorer, err := NewAccountCircuitIncidentRestorer(w14gClientWithNamespace(t, server, " "), time.Minute, 0)
	if err == nil || restorer != nil {
		t.Fatalf("非法命名空间必须失败")
	}
}

func TestW14GIncidentRestoreLegacyInjectionBranches(t *testing.T) {
	clock := newW7BClock()
	server := miniredis.RunT(t)
	restorer, err := NewAccountCircuitIncidentRestorer(w14gClientWithNamespace(t, server, "w14g-legacy"), time.Minute, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// 非法 incident 直接失败。
	if _, err := restorer.RestoreGatewayAccountCircuitIncident(ctx, GatewayAccountCircuitIncident{}); err == nil {
		t.Fatalf("空 incident 必须失败")
	}
	incident := w14gValidIncident(clock)
	incident.State = "SUSPECT"
	incident.RetainedUntil = nil
	// 注入损坏 JSON 输出。
	restorer.restore = func(context.Context, accountCircuitRevisionKeys, GatewayAccountCircuitIncident, []byte, time.Time, time.Duration, int) ([]byte, error) {
		return []byte("{bad"), nil
	}
	if _, err := restorer.RestoreGatewayAccountCircuitIncident(ctx, incident); err == nil {
		t.Fatalf("损坏 JSON 必须失败")
	}
	// 注入 capacity_exhausted 状态。
	restorer.restore = func(context.Context, accountCircuitRevisionKeys, GatewayAccountCircuitIncident, []byte, time.Time, time.Duration, int) ([]byte, error) {
		return []byte(`{"status":"capacity_exhausted","currentRevision":1,"closedStates":0}`), nil
	}
	if _, err := restorer.RestoreGatewayAccountCircuitIncident(ctx, incident); err == nil {
		t.Fatalf("capacity_exhausted 必须失败")
	}
	// 注入非法 projection。
	restorer.restore = func(context.Context, accountCircuitRevisionKeys, GatewayAccountCircuitIncident, []byte, time.Time, time.Duration, int) ([]byte, error) {
		return []byte(`{"status":"applied","currentRevision":0,"closedStates":0}`), nil
	}
	if _, err := restorer.RestoreGatewayAccountCircuitIncident(ctx, incident); err == nil {
		t.Fatalf("非法 projection 必须失败")
	}
}

func TestW14GIncidentRuntimeOwnerBranches(t *testing.T) {
	clock := newW7BClock()
	server := miniredis.RunT(t)
	ctx := context.Background()
	// runtime 为空时直接调用 runtime owner 路径必须失败。
	if _, err := (&AccountCircuitIncidentRestorer{}).restoreWithRuntimeOwner(ctx, w14gValidIncident(clock)); err == nil {
		t.Fatalf("缺少 runtime owner 必须失败")
	}
	// runtime owner restorer + 非法 incident。
	restorer, err := NewAccountCircuitRuntimeOwnerIncidentRestorer(w14gClientWithNamespace(t, server, "w14g-owner-rt"), time.Minute, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restorer.RestoreGatewayAccountCircuitIncident(ctx, GatewayAccountCircuitIncident{}); err == nil {
		t.Fatalf("空 incident 必须失败")
	}
}

func TestW14GIncidentLegacyRunRestoreLua(t *testing.T) {
	clock := newW7BClock()
	server := miniredis.RunT(t)
	restorer, err := NewAccountCircuitIncidentRestorer(w14gClientWithNamespace(t, server, "w14g-legacy-lua"), time.Minute, 0)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := restorer.RestoreGatewayAccountCircuitIncident(context.Background(), w14gValidIncident(clock))
	if err != nil || projection.Status != GatewayAccountCircuitRevisionApplied || projection.CurrentRevision != 1 {
		t.Fatalf("legacy Lua restore = %+v err=%v", projection, err)
	}
}

func TestW14GIncidentHalfOpenOriginOpen(t *testing.T) {
	clock := newW7BClock()
	incident := w14gValidIncident(clock)
	incident.State = "HALF_OPEN"
	incident.RetainedUntil = nil
	until := clock.Now().Add(time.Minute)
	incident.LeaseID = "w14g-lease"
	incident.LeasePurpose = "confirmation"
	incident.LeaseUntil = &until
	state, err := accountCircuitIncidentRuntimeStateFromIncident(incident)
	if err != nil {
		t.Fatal(err)
	}
	if state.HalfOpenOrigin != "OPEN" {
		t.Fatalf("HALF_OPEN + confirmation lease 的 origin = %s", state.HalfOpenOrigin)
	}
}

// ---------------------------------------------------------------------------
// owner.go
// ---------------------------------------------------------------------------

func TestW14GOwnerStoreBranches(t *testing.T) {
	clock := newW7BClock()
	ctx := context.Background()
	// 非法 URL。
	if _, err := New(Config{URL: "://bad", Namespace: "w14g", Retention: time.Minute}, w7bGate()); err == nil {
		t.Fatalf("非法 URL 必须失败")
	}
	// Runtime() 成功暴露。
	store, _ := w7bReadyStore(t, clock, 0, time.Minute)
	rt, err := store.Runtime()
	if err != nil || rt == nil {
		t.Fatalf("Runtime() = %+v err=%v", rt, err)
	}
	// Backfill 缺 reader。
	if _, err := store.BackfillRuntimeIndex(ctx, GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "w14g-owner"}, nil); err == nil {
		t.Fatalf("缺 reader 必须失败")
	}
	// 命名空间非法的派生分支：backfiller / projector / restorer 构造失败。
	bogus := &Client{client: store.client.client, namespace: " "}
	sBogus := &Store{client: bogus, runtime: store.runtime, gate: w7bGate(), capacity: 10, retention: time.Minute}
	reader := DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{}, nil
	})
	if _, err := sBogus.BackfillRuntimeIndex(ctx, GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "w14g-owner"}, reader); err == nil {
		t.Fatalf("非法命名空间 backfill 必须失败")
	}
	if _, err := sBogus.ProjectRevision(ctx, w14gValidEvent()); err == nil {
		t.Fatalf("非法命名空间 projector 必须失败")
	}
	sBadCapacity := &Store{client: store.client, runtime: store.runtime, gate: w7bGate(), capacity: -1, retention: time.Minute}
	if _, err := sBadCapacity.RestoreIncident(ctx, w14gValidIncident(clock)); err == nil {
		t.Fatalf("非法 capacity restorer 必须失败")
	}
	// CheckReady 在 Ping 之后 HMGet 失败（indexMeta 被替换为 string key）。
	server := miniredis.RunT(t)
	metaStore, err := New(Config{URL: "redis://" + server.Addr(), Namespace: "w14g-meta", Retention: time.Minute}, w7bGate())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = metaStore.Close() })
	metaStore.runtime.keys.indexMeta = "w14g:meta-as-string"
	if err := metaStore.client.client.Set(ctx, "w14g:meta-as-string", "x", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := metaStore.CheckReady(ctx); err == nil {
		t.Fatalf("indexMeta 类型错误时 CheckReady 必须失败")
	}
}

// ---------------------------------------------------------------------------
// revision.go
// ---------------------------------------------------------------------------

func TestW14GProjectorBranches(t *testing.T) {
	server := miniredis.RunT(t)
	client := w14gClientWithNamespace(t, server, "w14g-proj")
	// 默认 retention。
	if _, err := NewAccountCircuitRevisionProjector(client, 0); err != nil {
		t.Fatal(err)
	}
	// 非法命名空间。
	if _, err := NewAccountCircuitRevisionProjector(w14gClientWithNamespace(t, server, " "), 0); err == nil {
		t.Fatalf("非法命名空间必须失败")
	}
	// 脚本执行错误（Redis 已停止）。
	stopped := miniredis.RunT(t)
	stoppedClient := w14gClientWithNamespace(t, stopped, "w14g-proj-dead")
	projector, err := NewAccountCircuitRevisionProjector(stoppedClient, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_ = stoppedClient.client.Close()
	if _, err := projector.ProjectGatewayAccountCircuitRevision(context.Background(), w14gValidEvent()); err == nil {
		t.Fatalf("Redis 已停止时投影必须失败")
	}
	// 注入损坏输出与非法 projection。
	live, err := NewAccountCircuitRevisionProjector(client, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	live.project = func(context.Context, accountCircuitRevisionKeys, GatewayAccountCircuitOutboxEvent, time.Duration, time.Time) ([]byte, error) {
		return []byte("{bad"), nil
	}
	if _, err := live.ProjectGatewayAccountCircuitRevision(context.Background(), w14gValidEvent()); err == nil {
		t.Fatalf("损坏 JSON 必须失败")
	}
	live.project = func(context.Context, accountCircuitRevisionKeys, GatewayAccountCircuitOutboxEvent, time.Duration, time.Time) ([]byte, error) {
		return []byte(`{"status":"applied","currentRevision":0,"closedStates":0}`), nil
	}
	if _, err := live.ProjectGatewayAccountCircuitRevision(context.Background(), w14gValidEvent()); err == nil {
		t.Fatalf("非法 projection 必须失败")
	}
}

// ---------------------------------------------------------------------------
// runtime.go：store 校验与脚本错误分支
// ---------------------------------------------------------------------------

func TestW14GRuntimeStoreConstructorBranches(t *testing.T) {
	server := miniredis.RunT(t)
	client := w14gClientWithNamespace(t, server, "w14g-ctor")
	if _, err := NewAccountCircuitRuntimeStore(client, 0, 10); err != nil {
		t.Fatalf("retention=0 应取默认值: %v", err)
	}
	if _, err := NewAccountCircuitRuntimeStore(client, time.Minute, 0); err != nil {
		t.Fatalf("capacity=0 应取默认值: %v", err)
	}
	if _, err := NewAccountCircuitRuntimeStore(w14gClientWithNamespace(t, server, " "), time.Minute, 10); err == nil {
		t.Fatalf("非法命名空间必须失败")
	}
}

func TestW14GRuntimeStoreMutationErrorBranches(t *testing.T) {
	clock := newW7BClock()
	_, rt := w14gStoppedReadyStore(t, clock)
	_ = rt
	ctx := context.Background()
	accountID := "w14g-acc"
	scope := w7bAccountScope(accountID)
	// get 脚本错误。
	if _, err := rt.GetGatewayAccountCircuit(ctx, GatewayAccountCircuitGetInput{AccountID: accountID, Scope: scope, Now: clock.Now()}); err == nil {
		t.Fatalf("Redis 已停止时 get 必须失败")
	}
	// transition identity 校验错误（不触达 Redis，可用正常 store）。
	live, _ := w7bReadyStore(t, clock, 0, time.Minute)
	liveRT := live.runtime
	badGen := w7bIdentity(accountID, scope, -1, 1, "w14g-t")
	if _, err := liveRT.CompleteGatewayAccountCircuitConfirmation(ctx, GatewayAccountCircuitCompleteConfirmationInput{GatewayAccountCircuitTransitionIdentity: badGen, LeaseID: "l", Outcome: GatewayAccountCircuitCompletionUnknown}); err == nil {
		t.Fatalf("非法 generation 必须失败")
	}
	if _, err := liveRT.CompleteGatewayAccountCircuitCanary(ctx, GatewayAccountCircuitCompleteCanaryInput{GatewayAccountCircuitTransitionIdentity: badGen, LeaseID: "l", Outcome: GatewayAccountCircuitCompletionUnknown}); err == nil {
		t.Fatalf("非法 generation 必须失败")
	}
	if _, err := liveRT.AcquireGatewayAccountCircuitConfirmationLease(ctx, GatewayAccountCircuitAcquireConfirmationLeaseInput{GatewayAccountCircuitTransitionIdentity: badGen, LeaseID: "l", LeaseUntil: clock.Now().Add(time.Minute)}); err == nil {
		t.Fatalf("非法 generation 必须失败")
	}
	// restore identity 校验错误。
	state := w11cValidSuspectState(scope)
	if _, err := liveRT.RestoreGatewayAccountCircuit(ctx, GatewayAccountCircuitRestoreInput{AccountID: "w14g-other", State: state, Now: clock.Now()}); err == nil {
		t.Fatalf("identity 不匹配必须失败")
	}
}

func TestW14GRuntimeStoreEscalationErrorBranches(t *testing.T) {
	clock := newW7BClock()
	_, rt := w14gStoppedReadyStore(t, clock)
	ctx := context.Background()
	scope := w7bProtocolScope("w14g-acc", "w14g-bucket")
	validInput := GatewayAccountCircuitProtocolModelOpenEvidenceInput{
		AccountID: "w14g-acc", Scope: scope, Generation: 1, DispatchRevision: 1,
		EvidenceID: "w14g-e", AccountTransitionID: "w14g-t", Reason: "w14g-reason",
		ConfirmedFailureCount: 1, MaxProtocolScopes: 2, Window: time.Minute, Now: clock.Now(),
	}
	// identity 不匹配（范围参数合法）。
	mismatched := validInput
	mismatched.AccountID = "w14g-wrong"
	if _, err := rt.RecordGatewayAccountCircuitProtocolModelOpenEvidence(ctx, mismatched); err == nil {
		t.Fatalf("identity 不匹配必须失败")
	}
	// 脚本执行错误。
	if _, err := rt.RecordGatewayAccountCircuitProtocolModelOpenEvidence(ctx, validInput); err == nil {
		t.Fatalf("Redis 已停止时 escalation 必须失败")
	}
	// clear escalation 脚本错误。
	clearInput := GatewayAccountCircuitClearAccountEscalationEvidenceInput{AccountID: "w14g-acc", AccountRuntimeKey: "w14g-acc", DispatchRevision: 1, EvidenceID: "w14g-e", Now: clock.Now()}
	if _, err := rt.ClearGatewayAccountCircuitEscalationEvidence(ctx, clearInput); err == nil {
		t.Fatalf("Redis 已停止时 clear 必须失败")
	}
	// replace account revision 脚本错误。
	if _, err := rt.ReplaceGatewayAccountCircuitAccountDispatchRevision(ctx, GatewayAccountCircuitReplaceAccountDispatchRevisionInput{AccountID: "w14g-acc", DispatchRevision: 1, TransitionID: "w14g-t", Now: clock.Now()}); err == nil {
		t.Fatalf("Redis 已停止时 replace 必须失败")
	}
	// list due 脚本错误。
	if _, err := rt.ListDueGatewayAccountCircuits(ctx, GatewayAccountCircuitListDueInput{Limit: 10, Now: clock.Now()}); err == nil {
		t.Fatalf("Redis 已停止时 list due 必须失败")
	}
	// runMutation nil context。
	if _, err := rt.runMutation(nil, accountCircuitRuntimeMutationWire{}); err == nil {
		t.Fatalf("nil context 必须失败")
	}
}

func TestW14GRuntimeWireConversionBranches(t *testing.T) {
	var stringsValue accountCircuitRuntimeStrings
	if err := stringsValue.UnmarshalJSON([]byte(`["a",1]`)); err == nil {
		t.Fatalf("非法字符串数组必须失败")
	}
	if _, err := runtimeStateFromWire(accountCircuitRuntimeStateWire{DispatchRevision: "abc"}); err == nil {
		t.Fatalf("非法 dispatchRevision 必须失败")
	}
	clock := newW7BClock()
	wire, _ := w14gSuspectWire(clock, "w14g-acc")
	wire.Phase = "bogus"
	if _, err := runtimeStateFromWire(wire); err == nil {
		t.Fatalf("非法 phase 必须失败")
	}
	if _, err := runtimeStateToWire(GatewayAccountCircuitState{}); err == nil {
		t.Fatalf("空状态必须失败")
	}
}

// ---------------------------------------------------------------------------
// runtime_index.go：backfiller
// ---------------------------------------------------------------------------

func TestW14GBackfillerConstructorAndInput(t *testing.T) {
	server := miniredis.RunT(t)
	if _, err := NewAccountCircuitRuntimeIndexBackfiller(w14gClientWithNamespace(t, server, " ")); err == nil {
		t.Fatalf("非法命名空间必须失败")
	}
	clock := newW7BClock()
	store, _ := w7bReadyStore(t, clock, 0, time.Minute)
	b, err := NewAccountCircuitRuntimeIndexBackfiller(store.client)
	if err != nil {
		t.Fatal(err)
	}
	reader := DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{}, nil
	})
	if _, err := b.WithDispatchRevisionReader(reader).BackfillGatewayAccountCircuitRuntimeIndex(context.Background(), GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: " "}); err == nil {
		t.Fatalf("非法 owner 必须失败")
	}
	// begin 前置键类型破坏。
	if err := store.client.client.Set(context.Background(), b.keys.states, "x", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.WithDispatchRevisionReader(reader).BackfillGatewayAccountCircuitRuntimeIndex(context.Background(), GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "w14g-owner"}); err == nil {
		t.Fatalf("states 键类型破坏必须失败")
	}
}

func w14gSeedTwoStates(t *testing.T, b *AccountCircuitRuntimeIndexBackfiller, clock *w7bClock) {
	t.Helper()
	ctx := context.Background()
	for _, accountID := range []string{"w14g-a1", "w14g-a2"} {
		_, field, source := w14gSeedStateSource(clock, accountID)
		if err := b.client.client.HSet(ctx, b.keys.states, field, source).Err(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestW14GBackfillScanDataBound(t *testing.T) {
	clock := newW7BClock()
	server := miniredis.RunT(t)
	store, err := New(Config{URL: "redis://" + server.Addr(), Namespace: "w14g-scan", Retention: time.Minute}, w7bGate())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	b, err := NewAccountCircuitRuntimeIndexBackfiller(store.client)
	if err != nil {
		t.Fatal(err)
	}
	w14gSeedTwoStates(t, b, clock)
	input := w14gNormalizedInput(t)
	input.MaxFields = 1
	input.MaxPages = 100
	_, err = b.WithDispatchRevisionReader(DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{}, nil
	})).BackfillGatewayAccountCircuitRuntimeIndex(context.Background(), input)
	w14gFailOnSuccess(t, err, "MaxFields=1 的双条目扫描")
}

func TestW14GBackfillScanPageBound(t *testing.T) {
	clock := newW7BClock()
	server := miniredis.RunT(t)
	store, err := New(Config{URL: "redis://" + server.Addr(), Namespace: "w14g-page", Retention: time.Minute}, w7bGate())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	b, err := NewAccountCircuitRuntimeIndexBackfiller(store.client)
	if err != nil {
		t.Fatal(err)
	}
	w14gSeedTwoStates(t, b, clock)
	// miniredis 未按 COUNT 分页时该分支不可达，跳过（与 w7b 同口径）。
	values, next, err := store.client.client.HScan(context.Background(), b.keys.states, 0, "", 1).Result()
	if err != nil {
		t.Fatal(err)
	}
	if next == 0 && len(values) > 2 {
		t.Skip("miniredis 未分页，页数上限分支未触发（可接受）")
	}
	input := w14gNormalizedInput(t)
	input.MaxPages = 1
	input.ScanCount = 1
	_, err = b.WithDispatchRevisionReader(DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{}, nil
	})).BackfillGatewayAccountCircuitRuntimeIndex(context.Background(), input)
	w14gFailOnSuccess(t, err, "MaxPages=1 的分页扫描")
}

func TestW14GBackfillApplyConflict(t *testing.T) {
	clock := newW7BClock()
	server := miniredis.RunT(t)
	store, err := New(Config{URL: "redis://" + server.Addr(), Namespace: "w14g-apply", Retention: time.Minute}, w7bGate())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	b, err := NewAccountCircuitRuntimeIndexBackfiller(store.client)
	if err != nil {
		t.Fatal(err)
	}
	w14gSeedTwoStates(t, b, clock)
	// 预置冲突的 runtime→account 映射。
	if err := store.client.client.HSet(context.Background(), b.keys.runtimeAccounts, "w14g-a1", "w14g-conflict").Err(); err != nil {
		t.Fatal(err)
	}
	input := w14gNormalizedInput(t)
	input.MaxFields = 100000
	_, err = b.WithDispatchRevisionReader(DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{}, nil
	})).BackfillGatewayAccountCircuitRuntimeIndex(context.Background(), input)
	w14gFailOnSuccess(t, err, "runtime-account 冲突")
}

// w14gPagedReader 依序返回预制页。
type w14gPagedReader struct{ pages []GatewayAccountCircuitDispatchRevisionPage }

func (r *w14gPagedReader) ListGatewayAccountCircuitDispatchRevisions(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
	if len(r.pages) == 0 {
		return GatewayAccountCircuitDispatchRevisionPage{}, nil
	}
	page := r.pages[0]
	r.pages = r.pages[1:]
	return page, nil
}

func w14gRevisionPage(items ...GatewayAccountCircuitDispatchRevisionSnapshot) GatewayAccountCircuitDispatchRevisionPage {
	return GatewayAccountCircuitDispatchRevisionPage{Items: items}
}

func TestW14GSeedDispatchRevisionsBranches(t *testing.T) {
	runBackfill := func(t *testing.T, namespace string, mutate func(b *AccountCircuitRuntimeIndexBackfiller, input *GatewayAccountCircuitRuntimeIndexBackfillInput), reader GatewayAccountCircuitDispatchRevisionReader) error {
		server := miniredis.RunT(t)
		store, err := New(Config{URL: "redis://" + server.Addr(), Namespace: namespace, Retention: time.Minute}, w7bGate())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		b, err := NewAccountCircuitRuntimeIndexBackfiller(store.client)
		if err != nil {
			t.Fatal(err)
		}
		input := w14gNormalizedInput(t)
		if mutate != nil {
			mutate(b, &input)
		}
		_, err = b.WithDispatchRevisionReader(reader).BackfillGatewayAccountCircuitRuntimeIndex(context.Background(), input)
		return err
	}
	// 页数上限。
	err := runBackfill(t, "w14g-seed-pages", func(_ *AccountCircuitRuntimeIndexBackfiller, input *GatewayAccountCircuitRuntimeIndexBackfillInput) {
		input.MaxPages = 1
	}, &w14gPagedReader{pages: []GatewayAccountCircuitDispatchRevisionPage{
		{Items: []GatewayAccountCircuitDispatchRevisionSnapshot{{AccountID: "w14g-a", DispatchRevision: 1}}, NextAfterAccountID: "w14g-a"},
		w14gRevisionPage(GatewayAccountCircuitDispatchRevisionSnapshot{AccountID: "w14g-b", DispatchRevision: 1}),
	}})
	w14gFailOnSuccess(t, err, "revisions 页数上限")
	// 数据量上限。
	err = runBackfill(t, "w14g-seed-fields", func(_ *AccountCircuitRuntimeIndexBackfiller, input *GatewayAccountCircuitRuntimeIndexBackfillInput) {
		input.MaxFields = 1
	}, &w14gPagedReader{pages: []GatewayAccountCircuitDispatchRevisionPage{
		w14gRevisionPage(
			GatewayAccountCircuitDispatchRevisionSnapshot{AccountID: "w14g-a", DispatchRevision: 1},
			GatewayAccountCircuitDispatchRevisionSnapshot{AccountID: "w14g-b", DispatchRevision: 1},
		),
	}})
	w14gFailOnSuccess(t, err, "revisions 数据量上限")
	// 游标与末条目不一致。
	err = runBackfill(t, "w14g-seed-cursor", nil, &w14gPagedReader{pages: []GatewayAccountCircuitDispatchRevisionPage{
		{Items: []GatewayAccountCircuitDispatchRevisionSnapshot{{AccountID: "w14g-a", DispatchRevision: 1}}, NextAfterAccountID: "w14g-not-last"},
	}})
	w14gFailOnSuccess(t, err, "revisions 游标不一致")
	// 预置损坏 tombstone。
	err = runBackfill(t, "w14g-seed-tomb", func(b *AccountCircuitRuntimeIndexBackfiller, _ *GatewayAccountCircuitRuntimeIndexBackfillInput) {
		if err := b.client.client.HSet(context.Background(), b.keys.revisions, "w14g-a", "abc").Err(); err != nil {
			t.Fatal(err)
		}
	}, &w14gPagedReader{pages: []GatewayAccountCircuitDispatchRevisionPage{
		w14gRevisionPage(GatewayAccountCircuitDispatchRevisionSnapshot{AccountID: "w14g-a", DispatchRevision: 1}),
	}})
	w14gFailOnSuccess(t, err, "损坏 revision tombstone")
	// 多页成功。
	if err := runBackfill(t, "w14g-seed-ok", nil, &w14gPagedReader{pages: []GatewayAccountCircuitDispatchRevisionPage{
		{Items: []GatewayAccountCircuitDispatchRevisionSnapshot{{AccountID: "w14g-a", DispatchRevision: 1}}, NextAfterAccountID: "w14g-a"},
		w14gRevisionPage(GatewayAccountCircuitDispatchRevisionSnapshot{AccountID: "w14g-b", DispatchRevision: 1}),
	}}); err != nil {
		t.Fatalf("多页 revisions backfill 失败: %v", err)
	}
}

// ---------------------------------------------------------------------------
// runtime_index.go：item / entry 校验
// ---------------------------------------------------------------------------

func TestW14GIndexItemFromSourceAccountBranches(t *testing.T) {
	clock := newW7BClock()
	// runtime key 派生的 account 含尾部空格。
	scope := GatewayAccountCircuitScope{Kind: GatewayAccountCircuitScopeAccount, AccountRuntimeKey: "x :authorized:y"}
	field := mustGatewayAccountCircuitScopeKey(scope)
	wire := accountCircuitRuntimeStateWire{
		ScopeKey: field,
		Scope:    accountCircuitRuntimeScopeWire{Kind: string(scope.Kind), AccountRuntimeKey: scope.AccountRuntimeKey},
		Phase:    "SUSPECT", Generation: 1, DispatchRevision: "1", TransitionID: "w14g-t",
		UpdatedAtMS: clock.Now().UnixMilli(),
	}
	raw, err := json.Marshal(accountCircuitRuntimeIndexStateEntry{State: wire, ReplayOrder: []string{"w14g-t"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accountCircuitRuntimeIndexItemFromSource("states", field, string(raw)); err == nil {
		t.Fatalf("非法派生 account 必须失败")
	}
	// escalation 字段派生的 account 含尾部空格。
	if _, err := accountCircuitRuntimeIndexItemFromSource("escalation", "x :authorized:y", `{"dispatchRevision":"1","scopes":[]}`); err == nil {
		t.Fatalf("非法 escalation account 必须失败")
	}
}

func TestW14GIndexStateEntryValidationBranches(t *testing.T) {
	clock := newW7BClock()
	build := func() accountCircuitRuntimeIndexStateEntry {
		wire, _ := w14gSuspectWire(clock, "w14g-acc")
		return accountCircuitRuntimeIndexStateEntry{State: wire, ReplayOrder: []string{"w14g-t"}}
	}
	// 关系列表非法值（同时覆盖 wire relations 校验）。
	entry := build()
	entry.State.ChildIncidentIDs = accountCircuitRuntimeStrings{""}
	if err := validateAccountCircuitRuntimeIndexStateEntry(entry); err == nil {
		t.Fatalf("空关系值必须失败")
	}
	// state 解析失败。
	entry = build()
	entry.State.Phase = "bogus"
	if err := validateAccountCircuitRuntimeIndexStateEntry(entry); err == nil {
		t.Fatalf("非法 phase 必须失败")
	}
	// CLOSED 缺 closedExpiresAtMs。
	entry = build()
	entry.State.Phase = "CLOSED"
	entry.State.TransitionID = ""
	if err := validateAccountCircuitRuntimeIndexStateEntry(entry); err == nil {
		t.Fatalf("CLOSED 缺少截止时间必须失败")
	}
	// replay 历史超限。
	entry = build()
	replayOrder := make([]string, 0, GatewayAccountCircuitRuntimeMaxReplayIDs+1)
	for index := 0; index <= GatewayAccountCircuitRuntimeMaxReplayIDs; index++ {
		replayOrder = append(replayOrder, "w14g-t-"+strings.Repeat("x", index+1))
	}
	entry.ReplayOrder = replayOrder
	if err := validateAccountCircuitRuntimeIndexStateEntry(entry); err == nil {
		t.Fatalf("replay 超限必须失败")
	}
}

// ---------------------------------------------------------------------------
// runtime_index.go：audit 直驱矩阵
// ---------------------------------------------------------------------------

func TestW14GAuditReadStatesError(t *testing.T) {
	clock := newW7BClock()
	b, input := w14gAuditFixture(t, clock)
	// states 键类型破坏 → HScan 错误。
	if err := b.client.client.Set(context.Background(), b.keys.states, "x", 0).Err(); err != nil {
		t.Fatal(err)
	}
	w14gFailOnSuccess(t, b.audit(context.Background(), input, "w14g-owner:w14g-epoch", map[string]string{}), "states HScan")
}

func TestW14GAuditReadEvidenceBound(t *testing.T) {
	clock := newW7BClock()
	b, input := w14gAuditFixture(t, clock)
	input.MaxBytes = 1024
	if err := b.client.client.HSet(context.Background(), b.keys.escalation, "w14g-a1", strings.Repeat("x", 2048)).Err(); err != nil {
		t.Fatal(err)
	}
	w14gFailOnSuccess(t, b.audit(context.Background(), input, "w14g-owner:w14g-epoch", map[string]string{}), "escalation 数据量上限")
}

func TestW14GAuditCorruptSources(t *testing.T) {
	clock := newW7BClock()
	ctx := context.Background()
	// states 来源损坏。
	b, input := w14gAuditFixture(t, clock)
	if err := b.client.client.HSet(ctx, b.keys.states, "w14g-field", "{bad").Err(); err != nil {
		t.Fatal(err)
	}
	w14gFailOnSuccess(t, b.audit(ctx, input, "w14g-owner:w14g-epoch", map[string]string{}), "损坏 states 来源")
	// escalation 来源损坏。
	b2, input2 := w14gAuditFixture(t, clock)
	if err := b2.client.client.HSet(ctx, b2.keys.escalation, "w14g-field", "{bad").Err(); err != nil {
		t.Fatal(err)
	}
	w14gFailOnSuccess(t, b2.audit(ctx, input2, "w14g-owner:w14g-epoch", map[string]string{}), "损坏 escalation 来源")
}

func TestW14GAuditStateLoopAndMismatch(t *testing.T) {
	clock := newW7BClock()
	b, input := w14gAuditFixture(t, clock)
	w14gSeedValidStateForAudit(t, b, clock)
	// 实际索引为空 → audit 失败（states 循环已执行）。
	err := b.audit(context.Background(), input, "w14g-owner:w14g-epoch", map[string]string{"w14g-acc": "1"})
	w14gFailOnSuccess(t, err, "空实际索引 audit")
	if !strings.Contains(err.Error(), "runtime index audit failed") {
		t.Fatalf("错误不符: %v", err)
	}
}

func TestW14GAuditRevisionsMismatch(t *testing.T) {
	clock := newW7BClock()
	b, input := w14gAuditFixture(t, clock)
	w14gFailOnSuccess(t, b.audit(context.Background(), input, "w14g-owner:w14g-epoch", map[string]string{"w14g-none": "2"}), "revisions audit")
}

func TestW14GAuditActualIndexErrors(t *testing.T) {
	clock := newW7BClock()
	ctx := context.Background()
	poisonString := func(t *testing.T, b *AccountCircuitRuntimeIndexBackfiller, key string) {
		t.Helper()
		if err := b.client.client.Set(ctx, key, "x", 0).Err(); err != nil {
			t.Fatal(err)
		}
	}
	// scopeRuntime 键类型破坏。
	b, input := w14gAuditFixture(t, clock)
	w14gSeedValidStateForAudit(t, b, clock)
	poisonString(t, b, b.keys.scopeRuntime)
	w14gFailOnSuccess(t, b.audit(ctx, input, "w14g-owner:w14g-epoch", map[string]string{"w14g-acc": "1"}), "scopeRuntime 键破坏")
	// runtimeScopes 数组损坏。
	b, input = w14gAuditFixture(t, clock)
	w14gSeedValidStateForAudit(t, b, clock)
	if err := b.client.client.HSet(ctx, b.keys.runtimeScopes, "w14g-acc", "notjson").Err(); err != nil {
		t.Fatal(err)
	}
	w14gFailOnSuccess(t, b.audit(ctx, input, "w14g-owner:w14g-epoch", map[string]string{"w14g-acc": "1"}), "runtimeScopes 数组损坏")
	// runtimeAccounts 键类型破坏。
	b, input = w14gAuditFixture(t, clock)
	w14gSeedValidStateForAudit(t, b, clock)
	poisonString(t, b, b.keys.runtimeAccounts)
	w14gFailOnSuccess(t, b.audit(ctx, input, "w14g-owner:w14g-epoch", map[string]string{"w14g-acc": "1"}), "runtimeAccounts 键破坏")
	// accountRuntimes 键类型破坏。
	b, input = w14gAuditFixture(t, clock)
	w14gSeedValidStateForAudit(t, b, clock)
	poisonString(t, b, b.keys.accountRuntimes)
	w14gFailOnSuccess(t, b.audit(ctx, input, "w14g-owner:w14g-epoch", map[string]string{"w14g-acc": "1"}), "accountRuntimes 键破坏")
	// revisions 键类型破坏。
	b, input = w14gAuditFixture(t, clock)
	w14gSeedValidStateForAudit(t, b, clock)
	poisonString(t, b, b.keys.revisions)
	w14gFailOnSuccess(t, b.audit(ctx, input, "w14g-owner:w14g-epoch", map[string]string{"w14g-acc": "1"}), "revisions 键破坏")
	// 锁丢失 → readHash renew 失败。
	b, input = w14gAuditFixture(t, clock)
	if err := b.client.client.Del(ctx, b.keys.indexLock).Err(); err != nil {
		t.Fatal(err)
	}
	w14gFailOnSuccess(t, b.audit(ctx, input, "w14g-owner:w14g-epoch", map[string]string{}), "锁丢失")
}

func TestW14GReadJSONArraysBranches(t *testing.T) {
	clock := newW7BClock()
	b, input := w14gAuditFixture(t, clock)
	ctx := context.Background()
	// readHash 错误传播。
	if err := b.client.client.Set(ctx, b.keys.runtimeScopes, "x", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.readJSONArrays(ctx, b.keys.runtimeScopes, input, "w14g-owner:w14g-epoch"); err == nil {
		t.Fatalf("键类型破坏必须失败")
	}
	// 非法 JSON 数组。
	server := miniredis.RunT(t)
	_ = clock
	store, err := New(Config{URL: "redis://" + server.Addr(), Namespace: "w14g-arr", Retention: time.Minute}, w7bGate())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	b2, err := NewAccountCircuitRuntimeIndexBackfiller(store.client)
	if err != nil {
		t.Fatal(err)
	}
	// readHash 内部会续锁，先预置锁。
	if err := store.client.client.Set(ctx, b2.keys.indexLock, "t", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.client.client.HSet(ctx, b2.keys.runtimeScopes, "w14g-acc", "notjson").Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := b2.readJSONArrays(ctx, b2.keys.runtimeScopes, input, "t"); err == nil {
		t.Fatalf("非法数组必须失败")
	}
	// 非法数组元素。
	if err := store.client.client.HSet(ctx, b2.keys.accountRuntimes, "w14g-acc", `[""]`).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := b2.readJSONArrays(ctx, b2.keys.accountRuntimes, input, "t"); err == nil {
		t.Fatalf("空数组元素必须失败")
	}
}

// ---------------------------------------------------------------------------
// 容量耗尽与真实 Redis 分页分支
// ---------------------------------------------------------------------------

func TestW14GRuntimeOwnerRestoreCapacityExhausted(t *testing.T) {
	clock := newW7BClock()
	server := miniredis.RunT(t)
	client := w14gClientWithNamespace(t, server, "w14g-cap")
	// 先发布 ready 运行时索引（restore Lua 要求索引就绪）。
	backfiller, err := NewAccountCircuitRuntimeIndexBackfiller(client)
	if err != nil {
		t.Fatal(err)
	}
	reader := DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{}, nil
	})
	if _, err := backfiller.WithDispatchRevisionReader(reader).BackfillGatewayAccountCircuitRuntimeIndex(context.Background(), GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "w14g-owner"}); err != nil {
		t.Fatal(err)
	}
	restorer, err := NewAccountCircuitRuntimeOwnerIncidentRestorer(client, time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	incident := w14gValidIncident(clock)
	incident.State = "SUSPECT"
	incident.RetainedUntil = nil
	if _, err := restorer.RestoreGatewayAccountCircuitIncident(ctx, incident); err != nil {
		t.Fatalf("首个 incident 恢复失败: %v", err)
	}
	// 同账号下第二个不同 scope 在容量 1 时必须触发 capacity_exhausted。
	protocolScope := w7bProtocolScope("w14g-acc", "w14g-bucket")
	second := incident
	second.ScopeKind = "protocol_model"
	second.ProtocolCode = "openai"
	second.RequestLane = "text"
	second.ModelFamily = "w14g-bucket"
	second.CircuitScopeKey = mustGatewayAccountCircuitScopeKey(protocolScope)
	second.DispatchRevision = 1
	// dispatch_tombstone 要求恢复修订等于持久修订，容量冲突用同修订的新 scope 触发。
	_, err = restorer.RestoreGatewayAccountCircuitIncident(ctx, second)
	w14gFailOnSuccess(t, err, "容量耗尽恢复")
	if !strings.Contains(err.Error(), "capacity exhausted") {
		t.Fatalf("错误不符: %v", err)
	}
}

// TestW14GRealRedisScanPaginationBounds 覆盖依赖 HSCAN 分页的页数上限分支；
// 真实 Redis 不可达时跳过。命名空间与数据均使用 w14g 前缀，只清理自己的键。
func TestW14GRealRedisScanPaginationBounds(t *testing.T) {
	clock := newW7BClock()
	ctx := context.Background()
	url := w7bSharedEnvRedisURL(t)
	store, err := New(Config{URL: url, Namespace: "dev:w14g:circuitrt", Retention: time.Minute}, w7bGate())
	if err != nil {
		t.Skipf("真实 Redis store 构建失败，跳过: %v", err)
	}
	pattern := "juhe-ai:dev:w14g:circuitrt:*"
	w7bScanDelete(store.client.client, pattern)
	t.Cleanup(func() {
		w7bScanDelete(store.client.client, pattern)
		_ = store.Close()
	})
	b, err := NewAccountCircuitRuntimeIndexBackfiller(store.client)
	if err != nil {
		t.Fatal(err)
	}
	w14gSeedTwoStates(t, b, clock)
	// readHash 页数上限。
	input := w14gNormalizedInput(t)
	input.MaxPages = 1
	input.ScanCount = 1
	if err := b.client.client.Set(ctx, b.keys.indexLock, "w14g-lock", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	_, err = b.readHash(ctx, b.keys.states, input, "w14g-lock")
	w14gFailOnSuccess(t, err, "readHash 页数上限")
	// scanAndApply 页数上限（完整 backfill）。
	if err := b.client.client.Del(ctx, b.keys.indexLock).Err(); err != nil {
		t.Fatal(err)
	}
	reader := DispatchRevisionReaderFunc(func(_ context.Context, _ GatewayAccountCircuitDispatchRevisionPageInput) (GatewayAccountCircuitDispatchRevisionPage, error) {
		return GatewayAccountCircuitDispatchRevisionPage{}, nil
	})
	_, err = b.WithDispatchRevisionReader(reader).BackfillGatewayAccountCircuitRuntimeIndex(ctx, input)
	w14gFailOnSuccess(t, err, "scanAndApply 页数上限")
}
