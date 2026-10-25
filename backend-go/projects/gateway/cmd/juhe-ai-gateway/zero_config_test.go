package main

// 2026-10-24 存储形态收口（PLAN-20261024T051925945Z）：零配置 sqlite+memory
// 启动语义已消亡——PostgreSQL + Redis 是唯一存储形态，三个连接串 env 无条件
// 必填（缺失启动即失败，错误信息可定位）。本文件按新契约重写：
//   - 缺任一连接串的启动失败负例（fail-fast 且错误指名缺失 env）；
//   - 三连接串齐备时空余 env 的默认派生（DATA_DIR 文件面、开发密钥回退、
//     组合根恒开）；
//   - BUSINESS_* 家族：自动认领恒定、业务连接回落共享 JUHE_AI_POSTGRES_URL，
//     显式 BUSINESS_POSTGRES_URL 原样保留，残留切流语义 env 被忽略。

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestZeroConfigMissingStorageEndpointsFailFast(t *testing.T) {
	full := map[string]string{
		"JUHE_AI_POSTGRES_URL":    "postgres://root:secret@127.0.0.1:15432/juhe_ai_dev?sslmode=disable",
		"JUHE_AI_REDIS_CACHE_URL": "redis://:secret@127.0.0.1:6379/1",
		"JUHE_AI_REDIS_STATE_URL": "redis://:secret@127.0.0.1:6379/9",
	}
	for _, missing := range []string{"JUHE_AI_POSTGRES_URL", "JUHE_AI_REDIS_CACHE_URL", "JUHE_AI_REDIS_STATE_URL"} {
		env := map[string]string{}
		for key, value := range full {
			if key != missing {
				env[key] = value
			}
		}
		_, err := loadRuntimeConfig(w1iFakeEnv(env))
		if err == nil || !strings.Contains(err.Error(), missing) {
			t.Fatalf("缺 %s 必须启动失败且错误指名该 env，got %v", missing, err)
		}
	}
	// 全空 env 同样失败（原零配置可启动语义消亡）；错误必须指名三个连接串
	// 之一（校验顺序 cache → state → postgres）。
	if _, err := loadRuntimeConfig(w1iFakeEnv(map[string]string{})); err == nil ||
		(!strings.Contains(err.Error(), "JUHE_AI_POSTGRES_URL") &&
			!strings.Contains(err.Error(), "JUHE_AI_REDIS_CACHE_URL") &&
			!strings.Contains(err.Error(), "JUHE_AI_REDIS_STATE_URL")) {
		t.Fatalf("全空 env 必须失败并指名缺失连接串，got %v", err)
	}
}

func TestZeroConfigLoadRuntimeConfigDerivesPaths(t *testing.T) {
	root := t.TempDir()
	env := map[string]string{
		"JUHE_AI_POSTGRES_URL":    "postgres://root:secret@127.0.0.1:15432/juhe_ai_dev?sslmode=disable",
		"JUHE_AI_REDIS_CACHE_URL": "redis://:secret@127.0.0.1:6379/1",
		"JUHE_AI_REDIS_STATE_URL": "redis://:secret@127.0.0.1:6379/9",
		"JUHE_AI_DATA_DIR":        root,
	}
	cfg, err := loadRuntimeConfig(w1iFakeEnv(env))
	if err != nil {
		t.Fatalf("三连接串齐备的空余 env loadRuntimeConfig: %v", err)
	}
	// 组合根恒开（2026-09-21 起 SYSTEM_API/CHAIN 开关移除），开发密钥回退。
	if !cfg.SystemAPIEnabled || !cfg.ChainEnabled {
		t.Fatalf("未配置开关必须默认开启: system=%t chain=%t", cfg.SystemAPIEnabled, cfg.ChainEnabled)
	}
	if cfg.Secret != defaultRuntimeSecret {
		t.Fatalf("空 env Secret 必须回退开发密钥: %q", cfg.Secret)
	}
	// 普通文件目录面固定名派生（D1 spool / codex-context segments / chat-assets）。
	derived := map[string]string{
		cfg.UsageSpoolDirectory: filepath.Join(root, "usage-record-spool"),
		cfg.ChatAssetsRoot:      filepath.Join(root, "chat-assets"),
		cfg.CodexContextRoot:    filepath.Join(root, "codex-context"),
	}
	for got, want := range derived {
		if got != want {
			t.Fatalf("派生路径 %q != %q", got, want)
		}
	}
	// business owner 自动认领 + 连接串回落 + 门禁放行。
	if cfg.BusinessOwner != "gateway" || cfg.BusinessOwnerEpoch != "standalone" {
		t.Fatalf("自动认领=%+v", cfg)
	}
	if cfg.BusinessPostgresURL != env["JUHE_AI_POSTGRES_URL"] {
		t.Fatalf("业务连接必须回落共享 POSTGRES_URL: %q", cfg.BusinessPostgresURL)
	}
	if err := cfg.businessOwnerGate(); err != nil {
		t.Fatalf("自动认领下 businessOwnerGate 必须放行: %v", err)
	}
}

func TestZeroConfigBusinessOwnerGateSemantics(t *testing.T) {
	// 2026-09-21 起组合根/网关链开关移除（恒开）；清理批次 C1（2026-09-30）
	// 切流五证 env 家族整体退役，残留切流语义 env（如 HANDOFF_CONFIRMED）被
	// 忽略——恒自动认领，门禁只保留业务连接串检查。
	env := map[string]string{
		"JUHE_AI_POSTGRES_URL":               "postgres://root:secret@127.0.0.1:15432/juhe_ai_dev?sslmode=disable",
		"JUHE_AI_REDIS_CACHE_URL":            "redis://:secret@127.0.0.1:6379/1",
		"JUHE_AI_REDIS_STATE_URL":            "redis://:secret@127.0.0.1:6379/9",
		"JUHE_AI_BUSINESS_OWNER":             "gateway",
		"JUHE_AI_BUSINESS_HANDOFF_CONFIRMED": "true",
	}
	explicit, err := loadRuntimeConfig(w1iFakeEnv(env))
	if err != nil {
		t.Fatalf("显式切流语义残留配置: %v", err)
	}
	if explicit.BusinessOwner != "gateway" || explicit.BusinessOwnerEpoch != "standalone" {
		t.Fatal("C1 退役后残留切流语义 env 必须被忽略（恒自动认领）")
	}
	if err := explicit.businessOwnerGate(); err != nil {
		t.Fatalf("连接性满足时残留切流语义 env 不得阻断启动: %v", err)
	}
	// 显式独立业务连接串是运维配置，不触发门禁——连接串原样保留。
	explicitURL, err := loadRuntimeConfig(w1iFakeEnv(map[string]string{
		"JUHE_AI_POSTGRES_URL":          "postgres://root:secret@127.0.0.1:15432/juhe_ai_dev?sslmode=disable",
		"JUHE_AI_REDIS_CACHE_URL":       "redis://:secret@127.0.0.1:6379/1",
		"JUHE_AI_REDIS_STATE_URL":       "redis://:secret@127.0.0.1:6379/9",
		"JUHE_AI_BUSINESS_POSTGRES_URL": "postgres://biz:secret@127.0.0.1:15432/juhe_ai_dev?sslmode=disable",
	}))
	if err != nil {
		t.Fatalf("显式 business postgres 配置: %v", err)
	}
	if explicitURL.BusinessPostgresURL != "postgres://biz:secret@127.0.0.1:15432/juhe_ai_dev?sslmode=disable" {
		t.Fatalf("显式业务连接串必须原样保留: %q", explicitURL.BusinessPostgresURL)
	}
	if err := explicitURL.businessOwnerGate(); err != nil {
		t.Fatalf("仅运维变量自动认领下 businessOwnerGate 必须放行: %v", err)
	}
	// 缺业务连接串（主连接串也未配置）→ 门禁 fail closed（错误可定位）。
	missingBiz := runtimeConfig{SystemAPIEnabled: true}
	if err := missingBiz.businessOwnerGate(); err == nil || !strings.Contains(err.Error(), "JUHE_AI_BUSINESS_POSTGRES_URL") {
		t.Fatalf("缺业务连接串必须 fail closed 且指名 env，got %v", err)
	}
}
