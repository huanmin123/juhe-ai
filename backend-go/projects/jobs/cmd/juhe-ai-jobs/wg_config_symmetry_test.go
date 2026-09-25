package main

import (
	"strings"
	"testing"
)

// 本文件锁定 jobs worker 配置与 gateway 组合根的三处对称契约：
// usage spool 目录旧名回退、production SECRET 强度门禁、CODEX 分片数边界。

// TestLoadWorkerConfigUsageSpoolLegacyDirectoryFallback 锁定 usage spool 目录
// 新名/旧名解析顺序与 gateway runtime.go 同款：DIRECTORY 优先，仅配旧名
// JUHE_AI_USAGE_SPOOL_DIR 时回退旧名（组合根据 usageSpoolDirectoryLegacyName
// 打 warn 披露兼容路径），两者都配置时旧名静默失效。
func TestLoadWorkerConfigUsageSpoolLegacyDirectoryFallback(t *testing.T) {
	env := wgFullValidWorkerEnv(t)
	env["JUHE_AI_USAGE_SPOOL_DIR"] = "/tmp/spool-legacy"
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("只配旧名必须可装载: %v", err)
	}
	if config.UsageSpoolDirectory != "/tmp/spool-legacy" {
		t.Fatalf("只配旧名时必须使用旧名目录，得到 %q", config.UsageSpoolDirectory)
	}
	if !config.usageSpoolDirectoryLegacyName {
		t.Fatal("只配旧名时必须登记旧名回退标记（组合根据此披露兼容路径）")
	}

	env = wgFullValidWorkerEnv(t)
	env["JUHE_AI_USAGE_SPOOL_DIR"] = "/tmp/spool-legacy"
	env["JUHE_AI_USAGE_SPOOL_DIRECTORY"] = "/tmp/spool-new"
	config, err = loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("两处都配置必须可装载: %v", err)
	}
	if config.UsageSpoolDirectory != "/tmp/spool-new" {
		t.Fatalf("新名 DIRECTORY 必须优先于旧名 DIR，得到 %q", config.UsageSpoolDirectory)
	}
	if config.usageSpoolDirectoryLegacyName {
		t.Fatal("新名生效时不得登记旧名回退标记")
	}

	env = wgFullValidWorkerEnv(t)
	env["JUHE_AI_USAGE_SPOOL_DIRECTORY"] = "/tmp/spool-new"
	config, err = loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("只配新名必须可装载: %v", err)
	}
	if config.UsageSpoolDirectory != "/tmp/spool-new" || config.usageSpoolDirectoryLegacyName {
		t.Fatalf("只配新名必须直接生效且无回退标记: %q %v", config.UsageSpoolDirectory, config.usageSpoolDirectoryLegacyName)
	}
}

// TestLoadWorkerConfigProductionSecretGate 对齐 gateway runtime.go 的
// assertProductionSecret：生产拒绝短值与默认开发密钥，非生产不受影响。
func TestLoadWorkerConfigProductionSecretGate(t *testing.T) {
	env := wgFullValidWorkerEnv(t)
	env["NODE_ENV"] = "production"
	env["JUHE_AI_SECRET"] = strings.Repeat("a", 31)
	if _, err := loadWorkerConfig(getenvFrom(env)); err == nil || !strings.Contains(err.Error(), "JUHE_AI_SECRET") {
		t.Fatalf("production 31 位 SECRET 必须 fail-fast: %v", err)
	}

	env = wgFullValidWorkerEnv(t)
	env["NODE_ENV"] = "production"
	env["JUHE_AI_SECRET"] = defaultRuntimeSecret
	if _, err := loadWorkerConfig(getenvFrom(env)); err == nil || !strings.Contains(err.Error(), "JUHE_AI_SECRET") {
		t.Fatalf("production 默认开发密钥必须 fail-fast: %v", err)
	}

	env = wgFullValidWorkerEnv(t)
	env["NODE_ENV"] = "production"
	env["JUHE_AI_SECRET"] = strings.Repeat("a", 32)
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("production 32 位随机密钥必须通过: %v", err)
	}
	if config.Secret != strings.Repeat("a", 32) {
		t.Fatalf("production 密钥必须原样保留: %q", config.Secret)
	}

	// 非生产短值/默认开发密钥不受生产门禁影响（既有零配置契约）。
	env = wgFullValidWorkerEnv(t)
	env["JUHE_AI_SECRET"] = "short"
	if _, err := loadWorkerConfig(getenvFrom(env)); err != nil {
		t.Fatalf("非生产短 SECRET 必须放行: %v", err)
	}
	if _, err := loadWorkerConfig(getenvFrom(map[string]string{})); err != nil {
		t.Fatalf("非生产空 SECRET 必须回退开发密钥: %v", err)
	}
}

// TestLoadWorkerConfigCodexShardCountBounds 对齐 gateway runtime.go 的
// 1..256 fail-fast：两侧分片数不一致会让 retention 漏清 gateway 写出的
// 分片文件。
func TestLoadWorkerConfigCodexShardCountBounds(t *testing.T) {
	for _, bad := range []string{"0", "257", "-1"} {
		env := wgFullValidWorkerEnv(t)
		env["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT"] = bad
		if _, err := loadWorkerConfig(getenvFrom(env)); err == nil || !strings.Contains(err.Error(), "必须在 1 到 256 之间") {
			t.Fatalf("JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT=%s 必须 fail-fast: %v", bad, err)
		}
	}
	for _, good := range []struct {
		raw  string
		want int
	}{{"1", 1}, {"256", 256}} {
		env := wgFullValidWorkerEnv(t)
		env["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT"] = good.raw
		config, err := loadWorkerConfig(getenvFrom(env))
		if err != nil {
			t.Fatalf("JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT=%s 必须合法: %v", good.raw, err)
		}
		if config.CodexContextStateShardCount != good.want {
			t.Fatalf("分片数必须生效: got %d want %d", config.CodexContextStateShardCount, good.want)
		}
	}
}
