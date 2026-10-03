package main

// Codex Responses↔Chat 桥状态组合根装配断言（迁移漏装配修复回归）：
// chainRuntimeDeps.CodexContextRoot / CodexContextStateStore 此前无任何赋值
// 点，gateway 不读 JUHE_AI_CODEX_CONTEXT_ROOT，链上 preflight 恒走 no-op
// 适配器。组合根测试证明装配后适配器持有真实桥；装配助手单测锁定空根
// no-op 退出与 postgres 分支 nil 池 fail-fast 两条确定性臂。

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
)

// TestComposeSystemAPIWiresCodexBridgeState: sqlite standalone + chain 开启
// 时，组合根必须把 CodexContextRoot + CodexContextStateStore 装进
// chainRuntimeDeps，链上 preauth.Codex 适配器持有真实 ChatBridgeStateService。
// 修复前 deps.CodexContextStateStore 无赋值点（依赖缺席 → no-op 适配器），
// 即便 cfg 提供了 segments 根，bridge 断言也必红；修复后经
// newChainCodexContextStateStore 装配真实行存储（sqlite 分片 schema 由
// 组合根 storage preflight 前置确保）而转绿。
func TestComposeSystemAPIWiresCodexBridgeState(t *testing.T) {
	cfg := composeTestConfig(t)
	cfg.ChainEnabled = true
	// composeTestConfig 是手管配置（不走 loadRuntimeConfig 派生），按
	// datadir 固定名表同款补 segments 根（<DATA_DIR>/codex-context）。
	cfg.CodexContextRoot = filepath.Join(filepath.Dir(cfg.CodexContextShardRoot), "codex-context")
	store := openComposeOperationStore(t)
	createRuntimeLogDataset(t, cfg.RuntimeLogDatabasePath)
	auditConfig, auditProducer, closeAudit := openComposeAuditSources(t, filepath.Dir(cfg.DatasetDatabasePath))
	defer closeAudit()
	composed, err := composeSystemAPI(cfg, pgpool.NewRegistry(), store, openComposeOperationLease(t, store), auditProducer, auditConfig, composeTestOwnerHealth())
	if err != nil {
		t.Fatalf("compose system api with chain: %v", err)
	}
	defer composed.Shutdown()
	if composed.chain == nil {
		t.Fatal("chain 开启时必须装配 /v1 链")
	}
	adapter, ok := composed.chain.preauth.Codex.(codexPreflightAdapter)
	if !ok {
		t.Fatalf("chain.preauth.Codex 类型 = %T，want codexPreflightAdapter", composed.chain.preauth.Codex)
	}
	if adapter.bridge == nil {
		t.Fatal("组合根装配后 preflight 必须持有真实 ChatBridgeStateService（迁移漏装配未修复）")
	}
	if adapter.compact == nil || adapter.registry == nil {
		t.Fatalf("真实桥必须同时装配 compact/registry：compact=%v registry=%v",
			adapter.compact != nil, adapter.registry != nil)
	}
}

// TestNewChainCodexContextStateStoreArms: 装配助手的确定性臂（无网络、无
// 真实 PG 依赖）。
func TestNewChainCodexContextStateStoreArms(t *testing.T) {
	t.Run("segments根为空返回nil nil保持no-op退出", func(t *testing.T) {
		store, err := newChainCodexContextStateStore(runtimeConfig{}, pgpool.NewRegistry())
		if err != nil {
			t.Fatalf("空 segments 根必须保持 no-op 退出: %v", err)
		}
		if store != nil {
			t.Fatal("空 segments 根必须返回 nil store")
		}
	})
	t.Run("postgres分支nil池注册表fail-fast", func(t *testing.T) {
		cfg := runtimeConfig{
			DatabaseDriver:      "postgres",
			CodexContextRoot:    filepath.Join(t.TempDir(), "codex-context"),
			BusinessPostgresURL: "postgres://127.0.0.1:1/juhe_ai?sslmode=disable",
		}
		store, err := newChainCodexContextStateStore(cfg, nil)
		if err == nil {
			t.Fatal("postgres 分支 nil 池注册表必须 fail-fast")
		}
		if store != nil {
			t.Fatal("失败臂不得返回 store")
		}
		if !strings.Contains(err.Error(), "codex context postgres pool") {
			t.Fatalf("错误 = %v，want 指名 codex context postgres pool", err)
		}
	})
}
