package modelcheckowner

import "testing"

// J3b circuit runtime 与主链共用唯一 Redis 连接串，键空间由
// {namespace:j3b} 域隔离；不存在独立 URL、回退标志或启动期隔离校验。

func isolationEnv(pairs map[string]string) func(string) string {
	return func(key string) string { return pairs[key] }
}

func isolationBaseEnv() map[string]string {
	return map[string]string{
		"JUHE_AI_POSTGRES_URL":    "postgres://a-isolation-main",
		"JUHE_AI_REDIS_URL":       "redis://main-chain:6379/0",
		"JUHE_AI_REDIS_NAMESPACE": "juhe-ai:dev",
	}
}

func TestCircuitRedisUsesUnifiedURLAndDomainNamespace(t *testing.T) {
	cfg, err := LoadConfig(isolationEnv(isolationBaseEnv()))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.CircuitRuntimeRedisURL != "redis://main-chain:6379/0" {
		t.Fatalf("URL = %q", cfg.CircuitRuntimeRedisURL)
	}
	if cfg.CircuitRuntimeRedisNamespace != "dev:j3b" {
		t.Fatalf("namespace = %q, want dev:j3b", cfg.CircuitRuntimeRedisNamespace)
	}
}

func TestCircuitRedisRequiresNamespace(t *testing.T) {
	env := isolationBaseEnv()
	delete(env, "JUHE_AI_REDIS_NAMESPACE")
	if _, err := LoadConfig(isolationEnv(env)); err == nil {
		t.Fatal("缺 namespace 必须失败")
	}
}

// 关闭路径（memory 装配，URL 为空）：零配置自动认领。
func TestCircuitRedisMemoryAssemblyPathPasses(t *testing.T) {
	cfg, err := LoadConfig(isolationEnv(map[string]string{
		"JUHE_AI_POSTGRES_URL": "postgres://a-isolation-j3b",
	}))
	if err != nil || !cfg.AutoClaimed {
		t.Fatalf("零配置必须自动认领: cfg=%+v err=%v", cfg, err)
	}
	if cfg.CircuitRuntimeRedisURL != "" {
		t.Fatalf("无 Redis 配置不得产生 URL: %q", cfg.CircuitRuntimeRedisURL)
	}
}
