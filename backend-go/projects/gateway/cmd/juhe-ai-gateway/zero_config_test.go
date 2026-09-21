package main

// 2026-09-19 零配置默认（开源开箱即用）的单元覆盖：
//   - 空 env（JUHE_AI_DATA_DIR 指向临时目录）下 loadRuntimeConfig 成功，
//     路径族落 <DATA_DIR>/<固定名>；
//   - 组合根与网关链恒开（2026-09-21 起 SYSTEM_API/CHAIN 开关移除）；
//   - sqlite / postgres + BUSINESS_* 切流语义家族（五证）全空时自动认领
//     业务 owner（postgres 业务连接回落共享 JUHE_AI_POSTGRES_URL），显式配
//     置任一切流语义成员则 handoff 门禁保持；纯运维变量（OWNER /
//     DATABASE_PATH / POSTGRES_URL）不触发门禁（2026-09-22 口径对齐 J3b 家
//     族）。

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

func TestZeroConfigBusinessOwnerGateSemantics(t *testing.T) {
	// 2026-09-21 起组合根/网关链开关移除（恒开），原「显式 false 关闭 /
	// 联动校验 / 非法开关值 fail-fast」语义随之消失；本测试聚焦业务 owner
	// 门禁与自动认领的边界。
	// 显式配置五证任一切流语义成员 → 不自动认领，handoff 门禁保持。
	// （2026-09-22 口径收窄前本用例以 JUHE_AI_BUSINESS_OWNER 触发；OWNER 属
	// 纯运维变量后改用 HANDOFF_CONFIRMED。）
	explicit, err := loadRuntimeConfig(w1iFakeEnv(map[string]string{
		"JUHE_AI_BUSINESS_OWNER":             "gateway",
		"JUHE_AI_BUSINESS_HANDOFF_CONFIRMED": "true",
	}))
	if err != nil {
		t.Fatalf("显式切流语义配置: %v", err)
	}
	if explicit.BusinessOwnerAutoClaimed {
		t.Fatal("显式配置切流语义变量时不得自动认领")
	}
	if err := explicit.businessOwnerGate(); err == nil || !strings.Contains(err.Error(), "JUHE_AI_BUSINESS_NODE_WRITER_STOPPED") {
		t.Fatalf("显式配置下 handoff 门禁必须保持: %v", err)
	}
	// 2026-09-22 口径收窄：纯运维变量不触发切流门禁——仅配置
	// JUHE_AI_BUSINESS_DATABASE_PATH（常见运维需求）或 JUHE_AI_BUSINESS_OWNER
	// 时仍自动认领且门禁放行，业务库路径原样保留。
	opsDir := t.TempDir()
	dbPathOnly, err := loadRuntimeConfig(w1iFakeEnv(map[string]string{
		"JUHE_AI_DATA_DIR":               opsDir,
		"JUHE_AI_BUSINESS_DATABASE_PATH": filepath.Join(opsDir, "ops-business.sqlite3"),
	}))
	if err != nil {
		t.Fatalf("仅配置 BUSINESS_DATABASE_PATH: %v", err)
	}
	if !dbPathOnly.BusinessOwnerAutoClaimed {
		t.Fatal("仅配置 BUSINESS_DATABASE_PATH（运维变量）必须仍自动认领")
	}
	if dbPathOnly.BusinessDatabasePath != filepath.Join(opsDir, "ops-business.sqlite3") {
		t.Fatalf("运维路径必须原样保留: %q", dbPathOnly.BusinessDatabasePath)
	}
	if err := dbPathOnly.businessOwnerGate(); err != nil {
		t.Fatalf("仅配置 BUSINESS_DATABASE_PATH 时 businessOwnerGate 必须放行: %v", err)
	}
	ownerOnly, err := loadRuntimeConfig(w1iFakeEnv(map[string]string{
		"JUHE_AI_BUSINESS_OWNER": "gateway",
	}))
	if err != nil {
		t.Fatalf("仅配置 BUSINESS_OWNER: %v", err)
	}
	if !ownerOnly.BusinessOwnerAutoClaimed {
		t.Fatal("仅配置 BUSINESS_OWNER（运维变量）必须仍自动认领")
	}
	if err := ownerOnly.businessOwnerGate(); err != nil {
		t.Fatalf("仅配置 BUSINESS_OWNER 时 businessOwnerGate 必须放行: %v", err)
	}
	// 五证全空自动认领 + 恒开组合根下门禁放行。
	autoClaimed, err := loadRuntimeConfig(w1iFakeEnv(map[string]string{"JUHE_AI_DATA_DIR": t.TempDir()}))
	if err != nil {
		t.Fatalf("自动认领配置: %v", err)
	}
	if !autoClaimed.BusinessOwnerAutoClaimed {
		t.Fatal("五证全空必须自动认领")
	}
	if err := autoClaimed.businessOwnerGate(); err != nil {
		t.Fatalf("自动认领下 businessOwnerGate 必须放行: %v", err)
	}
}

func TestZeroConfigPostgresAutoClaimFallsBackToSharedPostgresURL(t *testing.T) {
	// postgres + BUSINESS_* 切流语义家族（五证）全空：与 sqlite 同语义自动认
	// 领，业务连接回落共享 JUHE_AI_POSTGRES_URL；显式配置切流语义任一成员仍
	// 走原门禁。
	// POSTGRES_URL 本身是 performance hint（mode 推断为 performance），按真实
	// 高性能形态补齐 Redis 连接。
	cfg, err := loadRuntimeConfig(w1iFakeEnv(map[string]string{
		"JUHE_AI_DATABASE_DRIVER": "postgres",
		"JUHE_AI_POSTGRES_URL":    "postgres://root:secret@127.0.0.1:15432/juhe_ai_dev?sslmode=disable",
		"JUHE_AI_REDIS_CACHE_URL": "redis://:secret@127.0.0.1:6379/1",
		"JUHE_AI_REDIS_STATE_URL": "redis://:secret@127.0.0.1:6379/9",
	}))
	if err != nil {
		t.Fatalf("postgres 零配置 loadRuntimeConfig: %v", err)
	}
	if !cfg.BusinessOwnerAutoClaimed || cfg.BusinessOwner != "gateway" || cfg.BusinessOwnerEpoch != "standalone" {
		t.Fatalf("postgres 自动认领=%+v", cfg)
	}
	if cfg.BusinessPostgresURL != "postgres://root:secret@127.0.0.1:15432/juhe_ai_dev?sslmode=disable" {
		t.Fatalf("业务连接必须回落共享 POSTGRES_URL: %q", cfg.BusinessPostgresURL)
	}
	if err := cfg.businessOwnerGate(); err != nil {
		t.Fatalf("自动认领下 businessOwnerGate 必须放行: %v", err)
	}
	// 2026-09-22 口径收窄：显式独立业务连接串是运维配置，不触发切流门禁——
	// 仍自动认领，连接串原样保留（回落只作用于空值）。
	explicitURL, err := loadRuntimeConfig(w1iFakeEnv(map[string]string{
		"JUHE_AI_DATABASE_DRIVER":       "postgres",
		"JUHE_AI_POSTGRES_URL":          "postgres://root:secret@127.0.0.1:15432/juhe_ai_dev?sslmode=disable",
		"JUHE_AI_REDIS_CACHE_URL":       "redis://:secret@127.0.0.1:6379/1",
		"JUHE_AI_REDIS_STATE_URL":       "redis://:secret@127.0.0.1:6379/9",
		"JUHE_AI_BUSINESS_POSTGRES_URL": "postgres://biz:secret@127.0.0.1:15432/juhe_ai_dev?sslmode=disable",
	}))
	if err != nil {
		t.Fatalf("显式 business postgres 配置: %v", err)
	}
	if !explicitURL.BusinessOwnerAutoClaimed {
		t.Fatal("仅显式 BUSINESS_POSTGRES_URL（运维变量）时必须仍自动认领")
	}
	if explicitURL.BusinessPostgresURL != "postgres://biz:secret@127.0.0.1:15432/juhe_ai_dev?sslmode=disable" {
		t.Fatalf("显式业务连接串必须原样保留: %q", explicitURL.BusinessPostgresURL)
	}
	if err := explicitURL.businessOwnerGate(); err != nil {
		t.Fatalf("仅运维变量自动认领下 businessOwnerGate 必须放行: %v", err)
	}
	// 显式配置五证任一成员：不自动认领，保持原门禁（含独立 URL 契约）。
	explicitGate, err := loadRuntimeConfig(w1iFakeEnv(map[string]string{
		"JUHE_AI_DATABASE_DRIVER":            "postgres",
		"JUHE_AI_POSTGRES_URL":               "postgres://root:secret@127.0.0.1:15432/juhe_ai_dev?sslmode=disable",
		"JUHE_AI_REDIS_CACHE_URL":            "redis://:secret@127.0.0.1:6379/1",
		"JUHE_AI_REDIS_STATE_URL":            "redis://:secret@127.0.0.1:6379/9",
		"JUHE_AI_BUSINESS_HANDOFF_CONFIRMED": "true",
	}))
	if err != nil {
		t.Fatalf("显式切流语义配置: %v", err)
	}
	if explicitGate.BusinessOwnerAutoClaimed {
		t.Fatal("显式配置切流语义变量时不得自动认领")
	}
	if err := explicitGate.businessOwnerGate(); err == nil {
		t.Fatal("显式配置切流语义变量时 handoff 门禁必须保持")
	}
}
