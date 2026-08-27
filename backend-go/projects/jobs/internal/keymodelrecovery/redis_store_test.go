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

func TestRedisConfigIsOptIn(t *testing.T) {
	env := map[string]string{}
	cfg, err := LoadRedisConfig(func(name string) string { return env[name] })
	if err != nil || cfg.Enabled {
		t.Fatalf("disabled cfg=%#v err=%v", cfg, err)
	}
	env["JUHE_AI_GATEWAY_KEY_MODEL_RUNTIME_GUARD_ENABLED"] = "true"
	if _, err := LoadRedisConfig(func(name string) string { return env[name] }); err == nil {
		t.Fatal("enabled config without Redis must fail")
	}
	env["JUHE_AI_REDIS_STATE_URL"], env["JUHE_AI_REDIS_NAMESPACE"] = "redis://127.0.0.1:6379/9", "test-space"
	cfg, err = LoadRedisConfig(func(name string) string { return env[name] })
	if err != nil || !cfg.Enabled {
		t.Fatalf("enabled cfg=%#v err=%v", cfg, err)
	}
}
