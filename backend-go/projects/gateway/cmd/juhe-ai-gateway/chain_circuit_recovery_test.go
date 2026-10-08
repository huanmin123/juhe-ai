package main

// 本地单机形态恢复驱动（PLAN-20261008T113056000Z）装配测试：
//   - memory 运行态形态：composeChainRuntimeServices 装配恢复组件（Run 非 nil）
//     并外露 store；redis 驱动形态：组件保持零值（恢复职责归 jobs）。
//   - 探针目标解析适配层（chainCircuitRecoveryTargetResolver）：identity 解析
//     → 账户读取 → dispatch revision 围栏的最小真实路径（fixture 业务库），
//     探针闭包不执行（不发真实上游请求）。

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/proberepo"
)

const chainCircuitRecoveryTestSecret = "chain-circuit-recovery-test-secret"

func recoveryProbeStore(t *testing.T, db *sql.DB) *proberepo.Store {
	t.Helper()
	// proberepo 的候选排序读 ga.updated_at（生产迁移所有该列）；chain fixture
	// 的共享 schema 未包含，测试内加列（加法，NULL 默认不影响其他断言）。
	if _, err := db.Exec(`ALTER TABLE group_accounts ADD COLUMN updated_at TEXT`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			t.Fatalf("add fixture column: %v", err)
		}
	}
	store, err := proberepo.NewStore(proberepo.Config{DB: db, Secret: chainCircuitRecoveryTestSecret})
	if err != nil {
		t.Fatalf("create proberepo store: %v", err)
	}
	return store
}

// memory 形态：恢复组件装配且外露 store；组件可跑一轮空 sweep（无 due 状态）。
func TestChainCircuitRecoveryWiredForMemoryDriver(t *testing.T) {
	fixture := newChainFixture(t)
	cfg := composeTestConfig(t)
	composed := &composition{db: fixture.db, statsDB: fixture.statsDB, Bus: nil}
	services, err := composeChainRuntimeServices(composed, cfg, func(string) (string, error) { return "UTC", nil })
	if err != nil {
		t.Fatalf("composeChainRuntimeServices: %v", err)
	}
	t.Cleanup(services.Close)

	if services.AccountCircuits == nil {
		t.Fatal("memory 形态必须装配主链电路服务")
	}
	if services.AccountCircuitRuntime.Store == nil {
		t.Fatal("memory 形态必须外露电路 store（恢复驱动依赖）")
	}
	if services.AccountCircuitRecovery.Run == nil {
		t.Fatal("memory 形态必须装配恢复组件")
	}
	if services.AccountCircuitRecovery.Name != "account-circuit-recovery" {
		t.Fatalf("恢复组件名 = %q", services.AccountCircuitRecovery.Name)
	}

	// 组件节拍循环不进入：cancel ctx 验证 Run 可被取消退出（生命周期契约）。
	runCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if runErr := services.AccountCircuitRecovery.Run(runCtx); runErr == nil {
		t.Fatal("取消后 Run 必须返回 ctx 错误")
	}
}

// redis 驱动形态：恢复组件不装配（零值），store/hook 行为与现状一致。
func TestChainCircuitRecoveryNotWiredForRedisDriver(t *testing.T) {
	fixture := newChainFixture(t)
	cfg := composeTestConfig(t)
	server := miniredis.RunT(t)
	cfg.RuntimeMode = "performance"
	cfg.RuntimeStateDriver = "redis"
	cfg.RedisStateURL = "redis://" + server.Addr()
	cfg.RedisNamespace = "chain-circuit-recovery-test"
	composed := &composition{db: fixture.db, statsDB: fixture.statsDB, Bus: nil}
	services, err := composeChainRuntimeServices(composed, cfg, func(string) (string, error) { return "UTC", nil })
	if err != nil {
		t.Fatalf("composeChainRuntimeServices: %v", err)
	}
	t.Cleanup(services.Close)

	if services.AccountCircuitRuntime.Store == nil {
		t.Fatal("redis 形态仍必须外露电路 store（主链行为不变）")
	}
	// redis 形态恢复职责归 jobs：组件保持零值，main.go 判 Run 非 nil 不挂载。
	if services.AccountCircuitRecovery.Run != nil || services.AccountCircuitRecovery.Name != "" {
		t.Fatalf("redis 形态不得装配恢复组件: name=%q run=%v",
			services.AccountCircuitRecovery.Name, services.AccountCircuitRecovery.Run != nil)
	}
}

// 目标解析适配层最小真实路径：owner 形态 runtime key → 账户读取与 dispatch
// revision 围栏；探针闭包只构造不执行。
func TestChainCircuitRecoveryTargetResolverResolve(t *testing.T) {
	fixture := newChainFixture(t)
	const (
		accountID  = "acc_recovery_probe"
		sysID      = "sys_recovery_probe"
		groupID    = "grp_recovery_probe"
		revisionID = int64(7)
	)
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed %s: %v", query[:40], err)
		}
	}
	credentials, encryptErr := accounts.EncryptJSON(chainCircuitRecoveryTestSecret, map[string]any{
		"api_key":  "sk-upstream-recovery-probe",
		"base_url": "",
	})
	if encryptErr != nil {
		t.Fatalf("encrypt credentials: %v", encryptErr)
	}
	seed(`INSERT INTO system_accounts (id, status, image_generation_enabled) VALUES (?, 'active', 1)`, sysID)
	seed(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type) VALUES (?, ?, 'openai', 1, 'personal')`,
		groupID, sysID)
	seed(`INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, credentials_encrypted, deleted_at, health_check_model, dispatch_revision)
			VALUES (?, ?, 'openai', 'prof_1', 'openai', 'v1', '恢复探针账户', 'api_key', 'active', 1, ?, NULL, 'gpt-test', ?)`,
		accountID, sysID, credentials, revisionID)
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at) VALUES (?, ?, ?, 1, '2026-10-08T00:00:00.000Z')`,
		groupID, sysID, accountID)

	resolver := chainCircuitRecoveryTargetResolver{store: recoveryProbeStore(t, fixture.db)}

	// owner 形态：found，dispatch revision 从候选行围栏读取。
	state := gatewaycircuit.State{
		Scope:            gatewaycircuit.Scope{Kind: gatewaycircuit.ScopeKindAccount, AccountRuntimeKey: accountID},
		DispatchRevision: "7",
	}
	target, found, err := resolver.Resolve(context.Background(), state)
	if err != nil || !found {
		t.Fatalf("Resolve = (%#v, %v, %v), want found", target, found, err)
	}
	if target.DispatchRevision != "7" {
		t.Fatalf("target.DispatchRevision = %q, want 7", target.DispatchRevision)
	}
	if target.Probe == nil {
		t.Fatal("found 时必须携带探针闭包")
	}

	// authorized 形态：identity 的 group/system 覆盖账户行默认绑定。
	authzState := gatewaycircuit.State{
		Scope: gatewaycircuit.Scope{
			Kind:              gatewaycircuit.ScopeKindProtocolModel,
			AccountRuntimeKey: accountID + ":authorized:" + sysID + ":" + groupID + ":authz-1",
			ProtocolProfile:   "openai:v1",
			RequestLane:       "text",
			ModelBucket:       "gpt-test",
		},
		DispatchRevision: "7",
	}
	target, found, err = resolver.Resolve(context.Background(), authzState)
	if err != nil || !found {
		t.Fatalf("authorized Resolve = (%#v, %v, %v), want found", target, found, err)
	}
	if target.DispatchRevision != "7" {
		t.Fatalf("authorized target.DispatchRevision = %q, want 7", target.DispatchRevision)
	}

	// dispatch revision 漂移场景由 sweep 的 ReplaceDispatchRevision 围栏处理
	//（gatewaycircuit 包内测试覆盖）；这里断言候选缺 revision 时 found=false。
	seed(`UPDATE accounts SET dispatch_revision = NULL WHERE id = ?`, accountID)
	target, found, err = resolver.Resolve(context.Background(), state)
	if err != nil {
		t.Fatalf("无 revision Resolve error: %v", err)
	}
	if found {
		t.Fatalf("无 dispatch revision 候选必须 not found: %#v", target)
	}

	// identity 解析失败：无效 runtime key 不触达数据库。
	_, found, err = resolver.Resolve(context.Background(), gatewaycircuit.State{
		Scope: gatewaycircuit.Scope{Kind: gatewaycircuit.ScopeKindAccount, AccountRuntimeKey: ""},
	})
	if err != nil || found {
		t.Fatalf("空 runtime key 应 not found: (%v, %v)", found, err)
	}
}

// 账户缺失（owner 形态但账户行不存在）：not found，不报错。
func TestChainCircuitRecoveryTargetResolverMissingAccount(t *testing.T) {
	fixture := newChainFixture(t)
	resolver := chainCircuitRecoveryTargetResolver{store: recoveryProbeStore(t, fixture.db)}
	target, found, err := resolver.Resolve(context.Background(), gatewaycircuit.State{
		Scope: gatewaycircuit.Scope{Kind: gatewaycircuit.ScopeKindAccount, AccountRuntimeKey: "acc_not_exists"},
	})
	if err != nil {
		t.Fatalf("missing account Resolve error: %v", err)
	}
	if found {
		t.Fatalf("缺失账户应 not found: %#v", target)
	}
}

// 探针请求构造：protocol_model scope 钉 modelBucket，其余 scope 回退空
// （不钉模型）。
func TestChainCircuitRecoveryProbeRequestPinsModelBucket(t *testing.T) {
	identity := gatewaycircuit.RecoveryRuntimeIdentity{Kind: "owner", AccountID: "acc-1"}
	modelScope := gatewaycircuit.State{
		Scope: gatewaycircuit.Scope{
			Kind:              gatewaycircuit.ScopeKindProtocolModel,
			AccountRuntimeKey: "acc-1",
			ProtocolProfile:   "openai:v1",
			RequestLane:       "text",
			ModelBucket:       "  gpt-test  ",
		},
	}
	request := chainCircuitRecoveryProbeRequest(identity, modelScope, "grp", "sys")
	if request.ProbeModel != "gpt-test" {
		t.Fatalf("protocol_model scope 必须钉 modelBucket（trim 后）: %q", request.ProbeModel)
	}
	if request.TrafficSource != "runtime_recovery_probe" || request.Full {
		t.Fatalf("探针契约不符: trafficSource=%q full=%v", request.TrafficSource, request.Full)
	}
	plainRequest := chainCircuitRecoveryProbeRequest(identity, gatewaycircuit.State{
		Scope: gatewaycircuit.Scope{Kind: gatewaycircuit.ScopeKindAccount, AccountRuntimeKey: "acc-1"},
	}, "grp", "sys")
	if strings.TrimSpace(plainRequest.ProbeModel) != "" {
		t.Fatalf("account scope 不得钉模型: %q", plainRequest.ProbeModel)
	}
	// protocol_model 但 bucket 空白：回退空（不钉）。
	blankRequest := chainCircuitRecoveryProbeRequest(identity, gatewaycircuit.State{
		Scope: gatewaycircuit.Scope{
			Kind:              gatewaycircuit.ScopeKindProtocolModel,
			AccountRuntimeKey: "acc-1",
			ProtocolProfile:   "openai:v1",
			RequestLane:       "text",
			ModelBucket:       "   ",
		},
	}, "grp", "sys")
	if blankRequest.ProbeModel != "" {
		t.Fatalf("空白 modelBucket 应回退空: %q", blankRequest.ProbeModel)
	}
}

// recoveryLedgerTables 在 chain fixture 业务库上补 circuit control-plane 契约面
//（DDL 与 chain_circuit_controlplane_test.go w17eOpenBusinessSQLite 同款，加法
// 应用到共享 fixture）；并补 fixture 缺失的两列：accounts.circuit_projection_
// revision（CheckContract 与 outbox 投影水位回写需要）、group_accounts.updated_at
//（proberepo 账户行读取引用，recoveryProbeStore 同款加法）。
func recoveryLedgerTables(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []string{
		`ALTER TABLE accounts ADD COLUMN circuit_projection_revision INTEGER`,
		`ALTER TABLE group_accounts ADD COLUMN updated_at TEXT`,
		`CREATE TABLE account_circuit_incidents (
 circuit_scope_key TEXT PRIMARY KEY, account_id TEXT NOT NULL, account_runtime_key TEXT NOT NULL, scope_kind TEXT NOT NULL,
 key_fingerprint TEXT, protocol_code TEXT, request_lane TEXT, model_family TEXT, client_model TEXT, capability_hash TEXT,
 credential_source_account_id TEXT, client_endpoint_family TEXT, final_upstream_model TEXT, upstream_endpoint_mode TEXT, incident_id TEXT NOT NULL,
 parent_incident_id TEXT, child_incident_ids_json TEXT NOT NULL, caused_by_terminal_outcome_id TEXT, state TEXT NOT NULL,
 failure_scope TEXT, generation INTEGER NOT NULL, dispatch_revision INTEGER NOT NULL, ledger_revision INTEGER NOT NULL,
 projected_ledger_revision INTEGER NOT NULL, transition_id TEXT NOT NULL, cooldown_observation_generation INTEGER NOT NULL,
 open_until_ms INTEGER, next_transition_at_ms INTEGER, lease_id TEXT, lease_purpose TEXT, lease_owner_run_id TEXT,
 lease_until_ms INTEGER, attempt_started_at_ms INTEGER, attempt_hard_deadline_ms INTEGER, upstream_attempt_observed INTEGER NOT NULL,
 backoff_level INTEGER NOT NULL, consecutive_failures INTEGER NOT NULL, confirmation_failures_required INTEGER NOT NULL,
 confirmation_failure_evidence_keys_json TEXT NOT NULL, recovering_successes INTEGER NOT NULL, last_failure_class TEXT,
 retained_until_ms INTEGER, created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL)`,
		`CREATE TABLE account_circuit_outbox (
 event_id TEXT PRIMARY KEY, projection_key TEXT NOT NULL, dedupe_key TEXT NOT NULL UNIQUE, event_type TEXT NOT NULL,
 account_id TEXT NOT NULL, account_runtime_key TEXT NOT NULL, circuit_scope_key TEXT, incident_id TEXT, transition_id TEXT NOT NULL,
 dispatch_revision INTEGER NOT NULL, generation INTEGER, ledger_revision INTEGER, status TEXT NOT NULL, available_at_ms INTEGER NOT NULL,
 claim_token TEXT, claimed_by TEXT, claim_until_ms INTEGER, attempt_count INTEGER NOT NULL, last_error_class TEXT,
 acknowledged_at_ms INTEGER, created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL)`,
		`CREATE UNIQUE INDEX idx_account_circuit_incidents_key_model_capability ON account_circuit_incidents(scope_kind, capability_hash) WHERE scope_kind = 'key_model' AND capability_hash IS NOT NULL`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			if strings.Contains(err.Error(), "duplicate column") || strings.Contains(err.Error(), "already exists") {
				continue
			}
			t.Fatalf("create circuit ledger contract: %v", err)
		}
	}
}

// 恢复 mutation → persist hook → ledger 端到端（终审查漏修复②）：
//
//	a) hook 传递断言：真实装配链路 newChainAccountCircuitService（gate 三证
//	   就绪，MutationHook = bridge.Observe 包装）→ newChainAccountCircuitRecovery
//	   Component 构造（chain_circuit_recovery.go OnMutation: runtime.MutationHook）
//	   → 组件首轮 sweep。若装配丢失 hook 传递，spy 收不到 acquire/complete 事件、
//	   ledger 也无行，两个断言同时失败。spy 记录后委托真 hook，兼顾两断言且只付
//	   一次首轮延迟（5s±2.5s PassiveJitter）。
//	b) ledger 端到端：断言 account_circuit_incidents 出现该 scope 行
//	   （state/generation/dispatch_revision/lease 终态符合）且 outbox 有事件——
//	   恢复转换与主链请求热路径共用同一条 CAS 投影管道的实证。
//
// 探针不触网：runtime key 对应账户行存在但无分组绑定 → resolver found=false →
// releaseUnknown，恰好产生 acquire_confirmation + complete_confirmation 两笔
// mutation（账户维度 unknown 中性保持 SUSPECT、租约释放）。CAS 要求账户行
// dispatch_revision 匹配（account_not_found/stale 均不落行），故种子账户行。
func TestChainCircuitRecoverySweepMutationHookPersistsLedger(t *testing.T) {
	fixture := newChainFixture(t)
	recoveryLedgerTables(t, fixture.db)
	const (
		hookAccountID = "acc_recovery_hook_e2e"
		revisionID    = int64(7)
	)
	seed, seedErr := fixture.db.Exec(`INSERT INTO accounts
		(id, system_account_id, provider_code, name, type, status, dispatch_revision)
		VALUES (?, 'sys_owner', 'openai', '恢复 hook e2e 账户', 'api_key', 'active', ?)`,
		hookAccountID, revisionID)
	if seedErr != nil {
		t.Fatalf("seed account row: %v", seedErr)
	}
	if rows, _ := seed.RowsAffected(); rows != 1 {
		t.Fatalf("seed account rows = %d, want 1", rows)
	}
	runtime, closeService, err := newChainAccountCircuitService("memory", "", "", w17eGateReadyConfig(fixture.db), nil)
	if err != nil {
		t.Fatalf("newChainAccountCircuitService: %v", err)
	}
	t.Cleanup(closeService)
	if runtime.MutationHook == nil {
		t.Fatal("owner gate 三证就绪时装配必须外露 persist hook")
	}

	// spy：记录 sweep 流经 hook 的 MutationEvent，再委托真实 persist hook。
	var (
		eventMu sync.Mutex
		events  []gatewaycircuit.MutationEvent
	)
	baseHook := runtime.MutationHook
	runtime.MutationHook = func(ctx context.Context, event gatewaycircuit.MutationEvent) error {
		eventMu.Lock()
		events = append(events, event)
		eventMu.Unlock()
		return baseHook(ctx, event)
	}

	// 注入即时到期的 due SUSPECT 状态（memory store 为真实时间时钟）。
	scope := gatewaycircuit.Scope{Kind: gatewaycircuit.ScopeKindAccount, AccountRuntimeKey: hookAccountID}
	scopeKey := gatewaycircuit.MustScopeKey(scope)
	now := time.Now().UnixMilli()
	required := int64(2)
	count := int64(0)
	retryAt := now - 1_000
	if _, err := runtime.Store.Restore(context.Background(), gatewaycircuit.State{
		ScopeKey:                     scopeKey,
		Scope:                        scope,
		Phase:                        gatewaycircuit.PhaseSuspect,
		Generation:                   1,
		DispatchRevision:             "7",
		TransitionID:                 "seed-hook-e2e",
		ConfirmationFailuresRequired: &required,
		ConfirmationFailureCount:     &count,
		RetryAtMs:                    &retryAt,
		UpdatedAtMs:                  now,
	}, &now); err != nil {
		t.Fatalf("seed due state: %v", err)
	}

	cfg := composeTestConfig(t)
	composed := &composition{db: fixture.db, statsDB: fixture.statsDB, Bus: nil}
	component, err := newChainAccountCircuitRecoveryComponent(runtime, composed, cfg)
	if err != nil {
		t.Fatalf("newChainAccountCircuitRecoveryComponent: %v", err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runDone := make(chan error, 1)
	go func() { runDone <- component.Run(runCtx) }()

	waitUntil := func(description string, timeout time.Duration, probe func() bool) {
		t.Helper()
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if probe() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", description)
	}
	eventsSnapshot := func() []gatewaycircuit.MutationEvent {
		eventMu.Lock()
		defer eventMu.Unlock()
		return append([]gatewaycircuit.MutationEvent(nil), events...)
	}

	// 等 sweep 首轮（初始延迟 5s±2.5s + sweep 耗时）；事件到齐立即取消，
	// 抢在确认 unknown 退避（attempt 2 = 5s）触发第二轮 sweep 之前。
	waitUntil("sweep mutations reach hook", 20*time.Second, func() bool {
		return len(eventsSnapshot()) >= 2
	})
	cancel()
	select {
	case runErr := <-runDone:
		if !errors.Is(runErr, context.Canceled) {
			t.Fatalf("组件退出错误 = %v, want context.Canceled", runErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("组件未随 ctx 取消退出")
	}

	snapshot := eventsSnapshot()
	if len(snapshot) < 2 {
		t.Fatalf("hook 至少应收到 2 笔 mutation: %d", len(snapshot))
	}
	for index, event := range snapshot[:2] {
		if event.Scope.AccountRuntimeKey != hookAccountID || event.State.ScopeKey != scopeKey {
			t.Fatalf("event[%d] scope 不符: %+v", index, event.Scope)
		}
		if event.Status != gatewaycircuit.MutationApplied {
			t.Fatalf("event[%d] status = %s, want applied", index, event.Status)
		}
		if event.PreviousPhase != gatewaycircuit.PhaseSuspect {
			t.Fatalf("event[%d] previousPhase = %s, want SUSPECT", index, event.PreviousPhase)
		}
	}
	if snapshot[0].Operation != gatewaycircuit.OperationAcquireConfirmation {
		t.Fatalf("event[0] operation = %s, want acquire_confirmation", snapshot[0].Operation)
	}
	if snapshot[1].Operation != gatewaycircuit.OperationCompleteConfirmation {
		t.Fatalf("event[1] operation = %s, want complete_confirmation", snapshot[1].Operation)
	}

	// 运行态终态（组件已停，无并发 sweep）：确认 unknown 中性保持 SUSPECT，
	// 租约释放。
	state, err := runtime.Store.Get(context.Background(), scope, nil)
	if err != nil {
		t.Fatalf("read runtime state: %v", err)
	}
	if state.Phase != gatewaycircuit.PhaseSuspect || state.Lease != nil {
		t.Fatalf("运行态终态 = %s lease=%v, want SUSPECT 且无租约", state.Phase, state.Lease)
	}

	// ledger 端到端：等 bridge worker 落行（终态 CAS 携带 complete 后的
	// 无租约状态）。state/generation/dispatch_revision 必须与 sweep 结果一致。
	var (
		incidentState  string
		ledgerRevision int64
		generation     int64
		dispatchRev    int64
		leaseID        sql.NullString
	)
	waitUntil("incident row persisted with released lease", 15*time.Second, func() bool {
		return fixture.db.QueryRow(`SELECT state, ledger_revision, generation, dispatch_revision, lease_id
			FROM account_circuit_incidents WHERE circuit_scope_key = ?`, scopeKey).
			Scan(&incidentState, &ledgerRevision, &generation, &dispatchRev, &leaseID) == nil && !leaseID.Valid
	})
	if incidentState != "SUSPECT" {
		t.Fatalf("incident state = %s, want SUSPECT", incidentState)
	}
	if generation != 1 || dispatchRev != revisionID {
		t.Fatalf("incident (generation,dispatchRevision) = (%d,%d), want (1,%d)", generation, dispatchRev, revisionID)
	}
	if ledgerRevision < 1 {
		t.Fatalf("ledger revision = %d, want >= 1", ledgerRevision)
	}
	var outboxRows int
	if err := fixture.db.QueryRow(`SELECT count(*) FROM account_circuit_outbox WHERE circuit_scope_key = ?`, scopeKey).Scan(&outboxRows); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if outboxRows < 1 {
		t.Fatalf("outbox rows = %d, want >= 1", outboxRows)
	}
}
