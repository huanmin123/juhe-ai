package main

// 后台自动探测总开关禁用臂运行态清理（总开关设计 §4，2026-10-30 复审 M1）：
// 生产全量 DDL（bootstrap.EnsurePostgres）上驱动真实 accounts Store.Patch 禁用
// 翻转，断言：
//   - 禁用事务内 dispatch fence 推进 + dispatch_revision_changed 事件在位；
//   - 禁用臂经装配端口（复用 newAccountsRuntimeResetBridge 生产桥）清理
//     latency 降级 / Key 运行态池 / 运行时缓存投影且审计明细注明；
//   - outbox 关闭事件经 gatewaycircuit Bridge.ProjectPending（与 jobs 控制面
//     RunMaintenance 同语义的投影单步）消费后，账户级 circuit 运行态（含子
//     scope 的家族匹配）收敛 CLOSED，ack 水位回写 circuit_projection_revision。

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	circuitcontrolplane "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/circuit_control_plane"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
)

func TestChainBackgroundProbeDisableClosesCircuitIncidents(t *testing.T) {
	ctx := context.Background()
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := bootstrap.EnsurePostgres(ctx, db)
		return err
	})

	const (
		accountID = "acc-bgprobe-close"
		ownerID   = "sys-bgprobe-owner"
		secret    = "bgprobe-cleanup-test-secret"
	)
	// 归属先行：account_list_availability_dirty 的脏标触发器引用
	// system_accounts（viewer FK），种子账户的归属行必须先在位。
	if _, err := db.Exec(`INSERT INTO juhe_business.system_accounts (id, username, display_name,
		password_hash, created_at, updated_at)
		VALUES ($1, 'bgprobe-owner', 'bgprobe-owner', 'seed', '2026-09-01T00:00:00.000Z', '2026-09-01T00:00:00.000Z')
		ON CONFLICT (id) DO NOTHING`, ownerID); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.accounts (id, system_account_id,
		connection_driver_code, protocol_code, protocol_version, name, type, status,
		credentials_encrypted, background_probe_enabled,
		health_check_model, health_check_endpoint_mode, created_at, updated_at)
		VALUES ($1, $2, 'gpt', 'openai', 'v1', 'bgprobe-close', 'api_key', 'active',
		'cipher', 1, 'm', 'chat_json', '2026-09-01T00:00:00.000Z', '2026-09-01T00:00:00.000Z')`,
		accountID, ownerID); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	// 账户级 circuit 运行态：SUSPECT incident（模拟开关翻转前的真实流量残留，
	// 恢复探针已关时无人消费，只能经 dispatch fence 关闭）。
	memStore, err := gatewaycircuit.NewMemoryStore(gatewaycircuit.MemoryStoreOptions{Capacity: 100})
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	accountScope := gatewaycircuit.Scope{Kind: gatewaycircuit.ScopeKindAccount, AccountRuntimeKey: accountID}
	confirmationFailures := int64(2)
	nowMs := int64(1_000)
	if _, err := memStore.Suspect(ctx, gatewaycircuit.SuspectInput{
		Scope:                        accountScope,
		DispatchRevision:             "1",
		TransitionID:                 "t-bgprobe-suspect",
		Reason:                       "transport:connect failed",
		ConfirmationFailuresRequired: &confirmationFailures,
		NowMs:                        &nowMs,
	}); err != nil {
		t.Fatalf("seed suspect: %v", err)
	}

	cpStore, err := circuitcontrolplane.New(db, "juhe_business", circuitcontrolplane.OwnerGate{
		Confirmed: true, SchemaReady: true, NodeWriterStopped: true,
	})
	if err != nil {
		t.Fatalf("control plane store: %v", err)
	}

	accountStore, err := accounts.NewStore(db, secret, nil, nil)
	if err != nil {
		t.Fatalf("accounts store: %v", err)
	}
	// 生产桥作为清理端口（compose.go 同款装配）。
	resetBridge, err := newAccountsRuntimeResetBridge(&composition{db: db}, nil, &chainRuntimeServices{}, secret, nil)
	if err != nil {
		t.Fatalf("reset bridge: %v", err)
	}
	accountStore.SetBackgroundProbeRuntimeCleanup(resetBridge)

	disabled := false
	result, err := accountStore.Patch(ctx, accountID, accounts.PatchInput{
		ExpectedConfigRevision: 1,
		BackgroundProbeEnabled: &disabled,
	}, accounts.AccessScope{IsAdmin: true})
	if err != nil {
		t.Fatalf("disable patch: %v", err)
	}
	if result == nil || result.BackgroundProbeEvent != "disabled" {
		t.Fatalf("disable event = %+v", result)
	}
	// 运行态清理经生产桥成功并进审计明细（nil 端口降级说明的对照面）。
	cleanupNoted := false
	for _, detail := range result.BackgroundProbeDetails {
		if detail.Field == "runtimeCleanup" && detail.After == "已清理" {
			cleanupNoted = true
		}
	}
	if !cleanupNoted {
		t.Fatalf("disable audit details must note the runtime cleanup, details=%+v", result.BackgroundProbeDetails)
	}

	// 禁用事务内：dispatch fence 推进 + 关闭事件在位。
	var dispatchRevision int64
	if err := db.QueryRow(`SELECT dispatch_revision FROM juhe_business.accounts WHERE id = $1`, accountID).Scan(&dispatchRevision); err != nil {
		t.Fatal(err)
	}
	if dispatchRevision != 2 {
		t.Fatalf("dispatch_revision = %d，期望 2（禁用臂推进 fence）", dispatchRevision)
	}
	var outboxEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM juhe_business.account_circuit_outbox
		WHERE account_id = $1 AND event_type = 'dispatch_revision_changed' AND status = 'pending'`, accountID).Scan(&outboxEvents); err != nil {
		t.Fatal(err)
	}
	if outboxEvents != 1 {
		t.Fatalf("pending close events = %d，期望 1", outboxEvents)
	}

	// jobs 控制面投影等价单步：认领 → 关闭账户全部 scope → ack 水位回写。
	projectionBridge, err := gatewaycircuit.NewBridge(gatewaycircuit.BridgeOptions{
		Store: memStore,
		DB:    chainCircuitControlPlaneDB{store: cpStore},
		Now:   func() int64 { return time.Now().UnixMilli() },
	})
	if err != nil {
		t.Fatalf("projection bridge: %v", err)
	}
	acked, err := projectionBridge.ProjectPending(ctx, 10)
	if err != nil {
		t.Fatalf("project pending: %v", err)
	}
	if acked < 1 {
		t.Fatalf("acked = %d，期望至少 1（关闭事件被消费）", acked)
	}
	closedState, err := memStore.Get(ctx, accountScope, nil)
	if err != nil {
		t.Fatal(err)
	}
	if closedState.Phase != gatewaycircuit.PhaseClosed {
		t.Fatalf("账户级 circuit phase = %q，期望 CLOSED（incident 已关闭，不再挡 PrepareAttempt）", closedState.Phase)
	}
	var projectionRevision int64
	if err := db.QueryRow(`SELECT circuit_projection_revision FROM juhe_business.accounts WHERE id = $1`, accountID).Scan(&projectionRevision); err != nil {
		t.Fatal(err)
	}
	if projectionRevision < dispatchRevision {
		t.Fatalf("circuit_projection_revision = %d，期望 >= %d（ack 水位回写）", projectionRevision, dispatchRevision)
	}
}
