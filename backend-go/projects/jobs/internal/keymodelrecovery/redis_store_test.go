package keymodelrecovery

import "testing"

func TestRedisKeysMatchNodeContract(t *testing.T) {
	keys, err := NewRedisKeys("test-space")
	if err != nil {
		t.Fatal(err)
	}
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if got, want := keys.State(hash), "juhe-ai:test-space:gateway-account-circuit-key-model:state:"+hash; got != want {
		t.Fatalf("state key=%q want %q", got, want)
	}
	if keys.Due() != "juhe-ai:test-space:gateway-account-circuit-key-model:due" {
		t.Fatalf("due key=%q", keys.Due())
	}
	if got := keys.GlobalProbes(); got != "juhe-ai:test-space:gateway-account-circuit-key-model:recovery:global" {
		t.Fatalf("global probe key=%q", got)
	}
	if got := keys.SourceProbes("source-1"); got == keys.SourceProbes("source-2") {
		t.Fatalf("source probe keys must be isolated: %q", got)
	}
}

func TestRedisConfigFollowsRedisStateProfile(t *testing.T) {
	env := map[string]string{}
	cfg, err := LoadRedisConfig(func(name string) string { return env[name] })
	if err != nil || cfg.Enabled {
		t.Fatalf("disabled cfg=%#v err=%v", cfg, err)
	}
	env["JUHE_AI_REDIS_STATE_URL"] = "redis://127.0.0.1:6379/9"
	if _, err := LoadRedisConfig(func(name string) string { return env[name] }); err == nil {
		t.Fatal("Redis state without namespace must fail")
	}
	env["JUHE_AI_REDIS_NAMESPACE"] = "test-space"
	cfg, err = LoadRedisConfig(func(name string) string { return env[name] })
	if err != nil || !cfg.Enabled {
		t.Fatalf("enabled cfg=%#v err=%v", cfg, err)
	}
}

// TestLoadRedisConfigCanonicalizesNamespace 锁定 namespace 入口 canonical 化
// （2026-09-22 对齐 gateway 加载层）：全前缀配置剥除 `juhe-ai:` 根前缀，短名
// 逐字节不变；canonical 化为空的退化输入沿用非法 namespace 的 fail closed。
func TestLoadRedisConfigCanonicalizesNamespace(t *testing.T) {
	env := map[string]string{"JUHE_AI_REDIS_STATE_URL": "redis://127.0.0.1:6379/9"}
	load := func() (RedisConfig, error) {
		return LoadRedisConfig(func(name string) string { return env[name] })
	}
	env["JUHE_AI_REDIS_NAMESPACE"] = "juhe-ai:dev"
	cfg, err := load()
	if err != nil || cfg.Namespace != "dev" {
		t.Fatalf("全前缀 namespace 未 canonical 化: cfg=%#v err=%v", cfg, err)
	}
	env["JUHE_AI_REDIS_NAMESPACE"] = "test-space"
	cfg, err = load()
	if err != nil || cfg.Namespace != "test-space" {
		t.Fatalf("短名 namespace 被改写: cfg=%#v err=%v", cfg, err)
	}
	env["JUHE_AI_REDIS_NAMESPACE"] = "juhe-ai:"
	if _, err := load(); err == nil {
		t.Fatal("canonical 化为空的 namespace 必须 fail closed")
	}
}
