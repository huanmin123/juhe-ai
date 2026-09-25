package modelcheckowner

import (
	"strings"
	"testing"
)

// A（状态机专项 2026-09-25）J3b circuit runtime Redis 隔离防呆单测。
//
// 背景：internal/business/circuit_runtime 是 J3b 性能模式的第三套熔断实现，
// 独立 Lua 副本但与主链 gatewaycircuit 共用同名 states hash（键布局同为
// `juhe-ai:<namespace>:account-circuit:gateway-account-circuit:*`）。当
// JUHE_AI_J3B_CIRCUIT_REDIS_URL 显式未设置时，LoadConfig 回退主链
// JUHE_AI_REDIS_STATE_URL（回退行为本身不变）；一旦装配启用，两套 Lua 状态机
// 并发写同一 hash 会互相毒化。gate 构造处必须对"启用+回退"组合 fail-fast。
//
// 契约矩阵：
//   - 启用 + 回退 → 报错；
//   - 启用 + 显式 URL → 通过；
//   - 关闭（memory 装配路径，URL 为空）→ 不受影响，即使存在主链 state URL
//     的配置也不改变 LoadConfig 的回退解析结果。

func aCircuitIsolationEnv(pairs map[string]string) func(string) string {
	return func(key string) string { return pairs[key] }
}

func aCircuitIsolationBaseEnv() map[string]string {
	return map[string]string{
		"JUHE_AI_J3B_STORE":                  "sqlite",
		"JUHE_AI_J3B_DATABASE_PATH":          "F:/tmp/a-isolation-j3b.db",
		"JUHE_AI_J3B_BUSINESS_DATABASE_PATH": "F:/tmp/a-isolation-business.db",
		"JUHE_AI_J3B_CREDENTIAL_SECRET":      "cred",
	}
}

// 启用 + 回退：LoadConfig 解析结果不变（URL 来自回退），但装配处校验必须报错。
func TestACircuitRedisEnabledWithFallbackMustFail(t *testing.T) {
	env := aCircuitIsolationBaseEnv()
	env["JUHE_AI_REDIS_STATE_URL"] = "redis://main-chain:6379/9"
	env["JUHE_AI_REDIS_NAMESPACE"] = "juhe-ai:dev"
	cfg, err := LoadConfig(aCircuitIsolationEnv(env))
	if err != nil {
		t.Fatalf("回退解析行为不得改变: %v", err)
	}
	if !cfg.CircuitRuntimeRedisURLFellBack {
		t.Fatal("回退标志必须置位")
	}
	if cfg.CircuitRuntimeRedisURL != "redis://main-chain:6379/9" {
		t.Fatalf("回退 URL = %q", cfg.CircuitRuntimeRedisURL)
	}
	isolationErr := cfg.ValidateCircuitRuntimeRedisIsolation()
	if isolationErr == nil {
		t.Fatal("启用+回退组合必须在装配处 fail-fast")
	}
	if !strings.Contains(isolationErr.Error(), "JUHE_AI_J3B_CIRCUIT_REDIS_URL") ||
		!strings.Contains(isolationErr.Error(), "JUHE_AI_REDIS_STATE_URL") {
		t.Fatalf("错误信息必须说明回退来源与显式配置要求: %v", isolationErr)
	}
}

// 启用 + 显式 URL：即使同时配置了主链 state URL（运维显式决定，含显式同值），
// 校验必须通过。
func TestACircuitRedisExplicitURLPasses(t *testing.T) {
	env := aCircuitIsolationBaseEnv()
	env["JUHE_AI_J3B_CIRCUIT_REDIS_URL"] = "redis://j3b-dedicated:6379/9"
	env["JUHE_AI_J3B_CIRCUIT_REDIS_NAMESPACE"] = "j3b-circuit"
	env["JUHE_AI_REDIS_STATE_URL"] = "redis://main-chain:6379/9"
	env["JUHE_AI_REDIS_NAMESPACE"] = "juhe-ai:dev"
	cfg, err := LoadConfig(aCircuitIsolationEnv(env))
	if err != nil {
		t.Fatalf("显式配置加载失败: %v", err)
	}
	if cfg.CircuitRuntimeRedisURLFellBack {
		t.Fatal("显式配置不得置位回退标志")
	}
	if cfg.CircuitRuntimeRedisURL != "redis://j3b-dedicated:6379/9" {
		t.Fatalf("显式 URL = %q", cfg.CircuitRuntimeRedisURL)
	}
	if err := cfg.ValidateCircuitRuntimeRedisIsolation(); err != nil {
		t.Fatalf("显式 URL 必须通过校验: %v", err)
	}
}

// 显式设置与主链同值：这是运维的显式决定（知晓风险的共享），必须与回退区分、
// 校验通过。
func TestACircuitRedisExplicitSameValuePasses(t *testing.T) {
	env := aCircuitIsolationBaseEnv()
	env["JUHE_AI_J3B_CIRCUIT_REDIS_URL"] = "redis://main-chain:6379/9"
	env["JUHE_AI_J3B_CIRCUIT_REDIS_NAMESPACE"] = "juhe-ai:dev"
	env["JUHE_AI_REDIS_STATE_URL"] = "redis://main-chain:6379/9"
	env["JUHE_AI_REDIS_NAMESPACE"] = "juhe-ai:dev"
	cfg, err := LoadConfig(aCircuitIsolationEnv(env))
	if err != nil {
		t.Fatalf("显式同值加载失败: %v", err)
	}
	if cfg.CircuitRuntimeRedisURLFellBack {
		t.Fatal("显式同值不得置位回退标志")
	}
	if err := cfg.ValidateCircuitRuntimeRedisIsolation(); err != nil {
		t.Fatalf("显式同值（运维显式决定）必须通过校验: %v", err)
	}
}

// 关闭路径（memory 装配，URL 为空）零影响：零配置自动认领且无任何 Redis URL
// 时，校验必须通过。
func TestACircuitRedisMemoryAssemblyPathPasses(t *testing.T) {
	cfg, err := LoadConfig(aCircuitIsolationEnv(aCircuitIsolationBaseEnv()))
	if err != nil || !cfg.AutoClaimed {
		t.Fatalf("零配置必须自动认领: cfg=%+v err=%v", cfg, err)
	}
	if cfg.CircuitRuntimeRedisURL != "" || cfg.CircuitRuntimeRedisURLFellBack {
		t.Fatalf("无 Redis 配置不得产生 URL/回退标志: %q/%v", cfg.CircuitRuntimeRedisURL, cfg.CircuitRuntimeRedisURLFellBack)
	}
	if err := cfg.ValidateCircuitRuntimeRedisIsolation(); err != nil {
		t.Fatalf("关闭路径不受影响，必须通过: %v", err)
	}
}

// 防御臂：URL 为空（memory 装配）时即使标志被误置也不报错——校验只对
// "Redis 装配 + 回退值"组合生效。
func TestACircuitRedisValidatorIgnoresEmptyURL(t *testing.T) {
	cfg := Config{CircuitRuntimeRedisURLFellBack: true}
	if err := cfg.ValidateCircuitRuntimeRedisIsolation(); err != nil {
		t.Fatalf("URL 为空时校验必须通过: %v", err)
	}
}

// LoadConfig 回退解析契约保持不变：handoff 家族显式配置（非自动认领）且未配
// circuit URL 时，仍按原样回退 JUHE_AI_REDIS_STATE_URL 并要求 namespace；
// 完全缺失时仍按原样报"缺少 Redis state URL"（既有拒绝面不变）。
func TestACircuitRedisLoadConfigFallbackBehaviorUnchanged(t *testing.T) {
	handoff := map[string]string{
		"JUHE_AI_J3B_STORE":                      "sqlite",
		"JUHE_AI_J3B_DATABASE_PATH":              "F:/tmp/a-isolation-j3b.db",
		"JUHE_AI_J3B_BUSINESS_DATABASE_PATH":     "F:/tmp/a-isolation-business.db",
		"JUHE_AI_J3B_CREDENTIAL_SECRET":          "cred",
		"JUHE_AI_J3B_BUSINESS_HANDOFF_CONFIRMED": "true",
		"JUHE_AI_J3B_NODE_WRITER_STOPPED":        "true",
		"JUHE_AI_J3B_OWNER_EPOCH":                "epoch-1",
		"JUHE_AI_J3B_CUTOVER_EVIDENCE_PATH":      "F:/tmp/a-isolation-evidence.json",
		"JUHE_AI_J3B_SCHEMA_READY":               "true",
		"JUHE_AI_J3B_HEALTH_BOUNDARY_READY":      "true",
		"JUHE_AI_J3B_RUNTIME_READY":              "true",
	}
	if _, err := LoadConfig(aCircuitIsolationEnv(handoff)); err == nil ||
		!strings.Contains(err.Error(), "Redis state URL") {
		t.Fatalf("无任何 URL 时既有拒绝面必须保持: err=%v", err)
	}
	withStateURL := map[string]string{}
	for key, value := range handoff {
		withStateURL[key] = value
	}
	withStateURL["JUHE_AI_REDIS_STATE_URL"] = "redis://main-chain:6379/9"
	withStateURL["JUHE_AI_REDIS_NAMESPACE"] = "juhe-ai:dev"
	cfg, err := LoadConfig(aCircuitIsolationEnv(withStateURL))
	if err != nil {
		t.Fatalf("回退解析行为不得改变: %v", err)
	}
	if !cfg.CircuitRuntimeRedisURLFellBack || cfg.CircuitRuntimeRedisURL != "redis://main-chain:6379/9" {
		t.Fatalf("回退解析结果不符: fellBack=%v url=%q", cfg.CircuitRuntimeRedisURLFellBack, cfg.CircuitRuntimeRedisURL)
	}
	if err := cfg.ValidateCircuitRuntimeRedisIsolation(); err == nil {
		t.Fatal("非自动认领路径的启用+回退组合同样必须 fail-fast")
	}
}
