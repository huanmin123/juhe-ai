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
	"strings"
	"testing"

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
