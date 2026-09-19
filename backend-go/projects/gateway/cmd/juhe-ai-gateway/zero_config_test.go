package main

// 2026-09-19 零配置默认（开源开箱即用）的单元覆盖：
//   - 空 env（JUHE_AI_DATA_DIR 指向临时目录）下 loadRuntimeConfig 成功，
//     路径族落 <DATA_DIR>/<固定名>；
//   - JUHE_AI_GATEWAY_SYSTEM_API_ENABLED / JUHE_AI_GATEWAY_CHAIN_ENABLED
//     未配置时均为 true，显式 false 才关闭，非法值启动即失败；
//   - chain=true + system=false 仍被联动校验拒绝；
//   - sqlite + BUSINESS_* 家族全空时自动认领业务 owner，显式配置任一成员
//     则 handoff 门禁保持。

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestZeroConfigLoadRuntimeConfigDerivesPaths(t *testing.T) {
	root := t.TempDir()
	cfg, err := loadRuntimeConfig(w1iFakeEnv(map[string]string{"JUHE_AI_DATA_DIR": root}))
	if err != nil {
		t.Fatalf("空 env loadRuntimeConfig: %v", err)
	}
	// 组合根默认：standalone sqlite / memory，双组合根开启，开发密钥回退。
	if cfg.RuntimeMode != "standalone" || cfg.DatabaseDriver != "sqlite" || cfg.CacheDriver != "memory" || cfg.RuntimeStateDriver != "memory" {
		t.Fatalf("驱动默认=%+v", cfg)
	}
	if !cfg.SystemAPIEnabled || !cfg.ChainEnabled {
		t.Fatalf("未配置开关必须默认开启: system=%t chain=%t", cfg.SystemAPIEnabled, cfg.ChainEnabled)
	}
	if cfg.Secret != defaultRuntimeSecret {
		t.Fatalf("空 env Secret 必须回退开发密钥: %q", cfg.Secret)
	}
	// 固定名表派生。
	derived := map[string]string{
		cfg.DatabasePath:             filepath.Join(root, "business.sqlite3"),
		cfg.ChatDatabasePath:         filepath.Join(root, "chat.sqlite3"),
		cfg.DatasetDatabasePath:      filepath.Join(root, "dataset.sqlite3"),
		cfg.RuntimeLogDatabasePath:   filepath.Join(root, "runtime-log.sqlite3"),
		cfg.UsageCatalogDatabasePath: filepath.Join(root, "usage-catalog.sqlite3"),
		cfg.StatsDatabasePath:        filepath.Join(root, "stats.sqlite3"),
		cfg.TableMonitorDatabasePath: filepath.Join(root, "table-monitor.sqlite3"),
		cfg.BusinessDatabasePath:     filepath.Join(root, "business.sqlite3"),
		cfg.ChatAssetsRoot:           filepath.Join(root, "chat-assets"),
		cfg.CodexContextShardRoot:    filepath.Join(root, "codex-context", "state-shards"),
	}
	for got, want := range derived {
		if got != want {
			t.Fatalf("派生路径 %q != %q", got, want)
		}
	}
	// business owner 自动认领 + 门禁放行。
	if !cfg.BusinessOwnerAutoClaimed || cfg.BusinessOwner != "gateway" || cfg.BusinessOwnerEpoch != "standalone" {
		t.Fatalf("自动认领=%+v", cfg)
	}
	if err := cfg.businessOwnerGate(); err != nil {
		t.Fatalf("自动认领下 businessOwnerGate 必须放行: %v", err)
	}
}

func TestZeroConfigGateSwitchSemantics(t *testing.T) {
	// 显式 false 仍可关闭。
	off, err := loadRuntimeConfig(w1iFakeEnv(map[string]string{
		"JUHE_AI_GATEWAY_SYSTEM_API_ENABLED": "false",
		"JUHE_AI_GATEWAY_CHAIN_ENABLED":      "false",
	}))
	if err != nil {
		t.Fatalf("显式 false: %v", err)
	}
	if off.SystemAPIEnabled || off.ChainEnabled {
		t.Fatalf("显式 false 必须关闭: %+v", off)
	}
	// chain=true + system=false 仍被联动校验拒绝。
	if _, err := loadRuntimeConfig(w1iFakeEnv(map[string]string{
		"JUHE_AI_GATEWAY_SYSTEM_API_ENABLED": "false",
		"JUHE_AI_GATEWAY_CHAIN_ENABLED":      "true",
	})); err == nil || !strings.Contains(err.Error(), "必须同时启用 JUHE_AI_GATEWAY_SYSTEM_API_ENABLED") {
		t.Fatalf("chain=true+system=false 必须报错: %v", err)
	}
	// 非法值保持 strictEnvBool fail-fast。
	for _, raw := range []string{"weird", "2"} {
		if _, err := loadRuntimeConfig(w1iFakeEnv(map[string]string{"JUHE_AI_GATEWAY_CHAIN_ENABLED": raw})); err == nil {
			t.Fatalf("非法开关值 %q 必须报错", raw)
		}
	}
	// 显式配置 BUSINESS_* 家族任一成员 → 不自动认领，handoff 门禁保持。
	explicit, err := loadRuntimeConfig(w1iFakeEnv(map[string]string{
		"JUHE_AI_BUSINESS_OWNER": "gateway",
	}))
	if err != nil {
		t.Fatalf("显式 business owner 配置: %v", err)
	}
	if explicit.BusinessOwnerAutoClaimed {
		t.Fatal("显式配置 BUSINESS_* 时不得自动认领")
	}
	if err := explicit.businessOwnerGate(); err == nil || !strings.Contains(err.Error(), "JUHE_AI_BUSINESS_HANDOFF_CONFIRMED") {
		t.Fatalf("显式配置下 handoff 门禁必须保持: %v", err)
	}
	// system API 关闭时 owner 门禁短路放行（原语义）。
	if err := off.businessOwnerGate(); err != nil {
		t.Fatalf("system API 关闭时门禁必须短路: %v", err)
	}
}
