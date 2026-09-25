package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	circuitcontrolplane "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/circuit_control_plane"
	circuitprojector "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/circuit_projector"
	circuitruntime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/circuit_runtime"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckowner"
)

// runAccountCircuitRuntimeIndexInit 是 -init-account-circuit-runtime-index 的
// 受控一次性入口：从权威业务库（accounts.dispatch_revision 等 durable 事实）
// 回填 J3b 账户电路 Redis 运行态索引并发布就绪元数据。
//
// 为什么必须存在（2026-09-24 性能模式联调取证）：Redis 运行态模式下 gateway
// 启动 preflight 硬性要求索引就绪（circuitruntime.CheckReady），而运行态索引
// 是"全量基线 + 增量投影"结构——增量投影（circuitprojector）只追 outbox，
// 没有基线时调度门禁对每个账户 fail-closed（实测：索引空时全部 28 个可调度
// 账户被挡，/v1 全量 503 no_available_upstream_account）。基线只能由本命令
// 显式建立；它不属于任何常驻进程的职责。
//
// 语义约束（与 circuitruntime 包注释一致）：one-shot；只在确认没有旧状态写
// 入方（Node writer / 其他 Go owner）时执行；锁只防并发回填，不围栏遗留写
// 入方。幂等：重复执行即整体重建（begin 阶段 DEL 全部索引键后重扫）。
func runAccountCircuitRuntimeIndexInit() {
	j3bConfig, err := modelcheckowner.LoadConfig(os.Getenv)
	if err != nil {
		fail(fmt.Errorf("load J3b config: %w", err))
	}
	if !j3bConfig.Enabled {
		fail(errors.New("账户电路运行态索引初始化需要 Redis 运行态：JUHE_AI_REDIS_STATE_URL（或 JUHE_AI_J3B_CIRCUIT_REDIS_URL）未配置"))
	}
	// 与 serve 路径（main.go 组合根）同一零配置回退：未显式配置密钥时填内置
	// 开发默认，否则 OpenBusinessTargetConnection 直接拒绝。
	if j3bConfig.CredentialSecret == "" {
		j3bConfig.CredentialSecret = defaultRuntimeSecret
	}
	if j3bConfig.IdentitySecret == "" {
		j3bConfig.IdentitySecret = defaultRuntimeSecret
	}
	if j3bConfig.CircuitRuntimeRedisURL == "" || j3bConfig.CircuitRuntimeRedisNamespace == "" {
		fail(errors.New("账户电路运行态索引初始化缺少 Redis URL/namespace 配置"))
	}
	// A（状态机专项 2026-09-25）与 serve 路径（main.go gate 构造处）同款
	// fail-fast：circuit runtime 的 Redis URL 来自主链 JUHE_AI_REDIS_STATE_URL
	// 回退时，两套 Lua 状态机会并发写同一 states hash，语义不兼容；显式设置
	// 同值（运维显式决定）不受影响。
	if isolationErr := j3bConfig.ValidateCircuitRuntimeRedisIsolation(); isolationErr != nil {
		fail(isolationErr)
	}
	businessMode := circuitcontrolplane.SQLite
	if j3bConfig.StoreMode == "postgres" {
		businessMode = circuitcontrolplane.Postgres
	}
	gate := circuitcontrolplane.OwnerGate{
		Confirmed:         j3bConfig.BusinessHandoffConfirmed,
		SchemaReady:       j3bConfig.SchemaReady,
		NodeWriterStopped: j3bConfig.NodeWriterStopped,
	}
	businessConnection, openErr := modelcheckowner.OpenBusinessTargetConnection(context.Background(), j3bConfig)
	if openErr != nil {
		fail(fmt.Errorf("open business owner connection: %w", openErr))
	}
	defer businessConnection.Close()

	controlStore, err := circuitcontrolplane.New(businessConnection.DB, businessMode, "juhe_business", gate)
	if err != nil {
		fail(fmt.Errorf("create account circuit control-plane store: %w", err))
	}
	runtimeStore, err := circuitruntime.New(circuitruntime.Config{
		URL:       j3bConfig.CircuitRuntimeRedisURL,
		Namespace: j3bConfig.CircuitRuntimeRedisNamespace,
		Capacity:  j3bConfig.CircuitRuntimeCapacity,
		Retention: j3bConfig.CircuitRuntimeRetention,
	}, circuitruntime.OwnerGate{Confirmed: gate.Confirmed, SchemaReady: gate.SchemaReady, NodeWriterStopped: gate.NodeWriterStopped})
	if err != nil {
		fail(fmt.Errorf("create account circuit runtime store: %w", err))
	}
	defer runtimeStore.Close()
	if err := runtimeStore.Ping(context.Background()); err != nil {
		fail(fmt.Errorf("ping account circuit runtime Redis: %w", err))
	}

	hostname, _ := os.Hostname()
	result, err := runtimeStore.BackfillRuntimeIndex(context.Background(),
		circuitruntime.GatewayAccountCircuitRuntimeIndexBackfillInput{
			OwnerID: fmt.Sprintf("gateway-circuit-runtime-init:%s", hostname),
			LockTTL: 2 * time.Minute,
		},
		circuitprojector.DispatchRevisionReader{Store: controlStore})
	if err != nil {
		fail(fmt.Errorf("backfill account circuit runtime index: %w", err))
	}
	encoded, encodeErr := json.MarshalIndent(result, "", "  ")
	if encodeErr != nil {
		fail(encodeErr)
	}
	fmt.Println(string(encoded))
	fmt.Println("account circuit runtime index is ready")
}
