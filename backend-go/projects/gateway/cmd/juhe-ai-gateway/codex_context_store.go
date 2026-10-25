package main

// Codex Responses↔Chat 桥状态（G18 chat-bridge-state.ts）的组合根装配面。
// chainRuntimeDeps.CodexContextRoot / CodexContextStateStore 由组合根注入；
// 桥本体（chain_compose.go 的条件装配与 fail-fast）保持不变。
// 2026-10-24 存储形态收口：行存储恒 PostgreSQL（juhe_codex_context schema），
// sqlite 分片子面（state-shards）随存储形态删除。

import (
	"context"
	"fmt"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
)

// newChainCodexContextStateStore builds the codex context PostgreSQL row store
// for the chain composition root.
//
//   - segments 根为空：返回 (nil, nil)，链组装保持既有 no-op 降级（
//     loadRuntimeConfig 派生恒非空，只有直接构造的测试配置会走到）；
//   - postgres：从共享池注册表按业务池同规格取得句柄
//     （role "gateway-system-api"，与 composeSystemAPI 业务池同参数）构造
//     gatewaycodex.PostgresContextStateStore，并执行一次探针查询——生产 PG
//     schema 由 juhe-ai-maintenance --ensure-schema 预置（六个 schema 含
//     juhe_codex_context），缺 schema 属部署错误，启动期 fail-fast 暴露，
//     不得静默降级。
func newChainCodexContextStateStore(cfg runtimeConfig, pools *pgpool.Registry) (gatewaycodex.CodexContextRowStore, error) {
	if strings.TrimSpace(cfg.CodexContextRoot) == "" {
		return nil, nil
	}
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
