package main

// Codex Responses↔Chat 桥状态（G18 chat-bridge-state.ts）的组合根装配面。
// 迁移漏装配修复：chainRuntimeDeps.CodexContextRoot / CodexContextStateStore
// 此前无任何赋值点，gateway 从不读 JUHE_AI_CODEX_CONTEXT_ROOT，链组装的
// newChainCodexBridgePreflight 永不构造（previous_response_id 上下文还原与
// compact preflight 恒 no-op）。本文件按数据库驱动装配双模行存储，交给
// composeSystemAPI 注入 chainRuntimeDeps；桥本体（chain_compose.go 的条件
// 装配与 fail-fast）保持不变。

import (
	"context"
	"fmt"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
)

// newChainCodexContextStateStore builds the codex context dual-mode row store
// for the chain composition root.
//
//   - segments 根为空：返回 (nil, nil)，链组装保持既有 no-op 降级（
//     loadRuntimeConfig 派生恒非空，只有直接构造的测试配置会走到）；
//   - postgres：从共享池注册表按业务池同规格取得句柄
//     （role "gateway-system-api"，与 composeSystemAPI 业务池同参数）构造
//     gatewaycodex.PostgresContextStateStore，并执行一次探针查询——生产 PG
//     schema 由 juhe-ai-maintenance --ensure-schema 预置（六个 schema 含
//     juhe_codex_context），缺 schema 属部署错误，启动期 fail-fast 暴露，
//     不得静默降级；
//   - sqlite：构造 gatewaycodex.SQLiteShardContextStateStore（分片 schema
//     已由 ensureGatewaySQLiteStoragePreflight 在组合根前置确保）。
func newChainCodexContextStateStore(cfg runtimeConfig, pools *pgpool.Registry) (gatewaycodex.CodexContextRowStore, error) {
	if strings.TrimSpace(cfg.CodexContextRoot) == "" {
		return nil, nil
	}
	if cfg.DatabaseDriver == "postgres" {
		handle, err := pools.Acquire(cfg.BusinessPostgresURL, "gateway-system-api",
			gatewayPostgresPoolMaxOpen, gatewayPostgresPoolMaxIdle)
		if err != nil {
			return nil, fmt.Errorf("open codex context postgres pool: %w", err)
		}
		store, err := gatewaycodex.NewPostgresContextStateStore(handle)
		if err != nil {
			return nil, err
		}
		// schema 探针（juhe_codex_context.codex_context_sessions，见
		// contextstore.go 的 PG 方言表名限定）：LIMIT 0 只验证表存在与可读，
		// 不扫数据行。
		rows, err := handle.DB().QueryContext(context.Background(),
			`SELECT 1 FROM juhe_codex_context.codex_context_sessions LIMIT 0`)
		if err != nil {
			return nil, fmt.Errorf("codex context postgres schema 探针失败（juhe_codex_context.codex_context_sessions 缺失？生产 schema 由 juhe-ai-maintenance --ensure-schema 预置）: %w", err)
		}
		_ = rows.Close()
		return store, nil
	}
	return gatewaycodex.NewSQLiteShardContextStateStore(gatewaycodex.SQLiteShardStoreConfig{
		Root:       cfg.CodexContextShardRoot,
		ShardCount: cfg.CodexContextShardCount,
	})
}
