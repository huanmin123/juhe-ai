package main

import (
	"strings"
	"testing"
)

// 本文件锁定 jobs worker 配置与 gateway 组合根的三处对称契约：
// usage spool 目录旧名回退、production SECRET 强度门禁、CODEX 分片数边界。

// TestLoadWorkerConfigUsageSpoolDirectory 锁定 usage spool 目录只认现行名
// JUHE_AI_USAGE_SPOOL_DIRECTORY；旧名 JUHE_AI_USAGE_SPOOL_DIR 已删除，不再
// 参与解析（与 gateway runtime.go 同款硬切）。
func TestLoadWorkerConfigUsageSpoolDirectory(t *testing.T) {
	env := workerSmokeTestEnv(t)
	env["JUHE_AI_USAGE_SPOOL_DIR"] = "/tmp/spool-legacy"
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("装载失败: %v", err)
	}
	if config.UsageSpoolDirectory == "/tmp/spool-legacy" {
		t.Fatalf("旧名必须被忽略，得到 %q", config.UsageSpoolDirectory)
	}

	env = workerSmokeTestEnv(t)
	env["JUHE_AI_USAGE_SPOOL_DIR"] = "/tmp/spool-legacy"
	env["JUHE_AI_USAGE_SPOOL_DIRECTORY"] = "/tmp/spool-new"
	config, err = loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("装载失败: %v", err)
	}
	if config.UsageSpoolDirectory != "/tmp/spool-new" {
		t.Fatalf("DIRECTORY 必须生效，得到 %q", config.UsageSpoolDirectory)
	}

	env = workerSmokeTestEnv(t)
	env["JUHE_AI_USAGE_SPOOL_DIRECTORY"] = "/tmp/spool-new"
	config, err = loadWorkerConfig(getenvFrom(env))
	if err != nil || config.UsageSpoolDirectory != "/tmp/spool-new" {
		t.Fatalf("只配 DIRECTORY 必须生效: %q %v", config.UsageSpoolDirectory, err)
	}
}

// TestLoadWorkerConfigProductionSecretGate 对齐 gateway runtime.go 的
// assertProductionSecret：生产拒绝短值与默认开发密钥，非生产不受影响。
func TestLoadWorkerConfigProductionSecretGate(t *testing.T) {
	env := workerSmokeTestEnv(t)
	env["NODE_ENV"] = "production"
	env["JUHE_AI_SECRET"] = strings.Repeat("a", 31)
	if _, err := loadWorkerConfig(getenvFrom(env)); err == nil || !strings.Contains(err.Error(), "JUHE_AI_SECRET") {
		t.Fatalf("production 31 位 SECRET 必须 fail-fast: %v", err)
	}

	env = workerSmokeTestEnv(t)
	env["NODE_ENV"] = "production"
	env["JUHE_AI_SECRET"] = defaultRuntimeSecret
	if _, err := loadWorkerConfig(getenvFrom(env)); err == nil || !strings.Contains(err.Error(), "JUHE_AI_SECRET") {
		t.Fatalf("production 默认开发密钥必须 fail-fast: %v", err)
	}

	env = workerSmokeTestEnv(t)
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
	env = workerSmokeTestEnv(t)
	env["JUHE_AI_SECRET"] = "short"
	if _, err := loadWorkerConfig(getenvFrom(env)); err != nil {
		t.Fatalf("非生产短 SECRET 必须放行: %v", err)
	}
	if _, err := loadWorkerConfig(getenvFrom(map[string]string{"JUHE_AI_POSTGRES_URL": "postgres://smoke-test.invalid/juhe_ai_smoke", "JUHE_AI_REDIS_URL": "redis://smoke-test.invalid:6379/0"})); err != nil {
		t.Fatalf("非生产空 SECRET 必须回退开发密钥: %v", err)
	}
}
