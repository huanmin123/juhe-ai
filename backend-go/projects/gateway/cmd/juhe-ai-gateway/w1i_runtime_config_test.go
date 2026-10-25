package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// w1i 测试基座（2026-10-24 存储形态收口）：PostgreSQL + Redis 是唯一存储
// 形态，三个连接串 env 无条件必填；模式/驱动开关 env 已随生产删除。

func w1iFakeEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func w1iStorageEnv(extra map[string]string) map[string]string {
	merged := map[string]string{
		"JUHE_AI_POSTGRES_URL":    "postgres://127.0.0.1:5432/juhe",
		"JUHE_AI_REDIS_CACHE_URL": "redis://127.0.0.1:6379/0",
		"JUHE_AI_REDIS_STATE_URL": "redis://127.0.0.1:6379/1",
	}
	for key, value := range extra {
		merged[key] = value
	}
	return merged
}

func TestW1iStrictEnvBool(t *testing.T) {
	for _, v := range []string{"true", "1", "yes", "on", "TRUE", "On"} {
		got, err := strictEnvBool("TEST", v, false)
		if err != nil || !got {
			t.Fatalf("strictEnvBool(%q): %v %v", v, got, err)
		}
	}
	for _, v := range []string{"false", "0", "no", "off", "FALSE", "Off"} {
		got, err := strictEnvBool("TEST", v, true)
		if err != nil || got {
			t.Fatalf("strictEnvBool(%q): %v %v", v, got, err)
		}
	}
	fallbackTrue, err := strictEnvBool("TEST", "", true)
	if err != nil || !fallbackTrue {
		t.Fatalf("fallback true: %v %v", fallbackTrue, err)
	}
	fallbackFalse, err := strictEnvBool("TEST", "", false)
	if err != nil || fallbackFalse {
		t.Fatalf("fallback false: %v %v", fallbackFalse, err)
	}
	_, err = strictEnvBool("TEST", "bogus", false)
	if err == nil {
		t.Fatal("bogus value should fail")
	}
}

func TestW1iParseTruncatedInt(t *testing.T) {
	got, err := parseTruncatedInt("N", "10.5", 1, 100)
	if err != nil || got != 10 {
		t.Fatalf("trunc: %d %v", got, err)
	}
	got, err = parseTruncatedInt("N", "0.9", 1, 10)
	if err == nil {
		t.Fatalf("trunc below min: %d", got)
	}
	got, err = parseTruncatedInt("N", "abc", 1, 100)
	if err == nil {
		t.Fatal("non-number accepted")
	}
	got, err = parseTruncatedInt("N", "100.1", 1, 100)
	if err != nil || got != 100 {
		t.Fatalf("trunc at max: %d %v", got, err)
	}
}

func TestW1iProductionRuntime(t *testing.T) {
	if !productionRuntime(w1iFakeEnv(map[string]string{"NODE_ENV": "production"})) {
		t.Fatal("production expected")
	}
	if productionRuntime(w1iFakeEnv(map[string]string{"NODE_ENV": "development"})) {
		t.Fatal("development not production")
	}
	if !productionRuntime(w1iFakeEnv(map[string]string{"NODE_ENV": "Production"})) {
		t.Fatal("case-insensitive production expected")
	}
	if productionRuntime(w1iFakeEnv(map[string]string{})) {
		t.Fatal("empty NODE_ENV not production")
	}
}

func TestW1iLoadRuntimeConfigDefaults(t *testing.T) {
	cfg, err := loadRuntimeConfig(w1iFakeEnv(w1iStorageEnv(nil)))
	if err != nil {
		t.Fatalf("default config: %v", err)
	}
	if cfg.Host != "127.0.0.1" {
		t.Fatalf("host = %q", cfg.Host)
	}
	if cfg.Port != 3000 {
		t.Fatalf("port = %d", cfg.Port)
	}
	if cfg.ChatAssetsRoot != filepath.Join("data", "chat-assets") {
		t.Fatalf("chat assets = %q", cfg.ChatAssetsRoot)
	}
	if cfg.UsageSpoolDirectory != filepath.Join("data", "usage-record-spool") {
		t.Fatalf("usage spool = %q", cfg.UsageSpoolDirectory)
	}
}

func TestW1iLoadRuntimeConfigInvalidValues(t *testing.T) {
	tests := []struct {
		env     map[string]string
		wantErr string
	}{
		{w1iStorageEnv(map[string]string{"JUHE_AI_PORT": "bogus"}), "JUHE_AI_PORT"},
		{w1iStorageEnv(map[string]string{"JUHE_AI_NODE_ENV": "staging"}), "production、test 或 development"},
	}
	for _, tc := range tests {
		_, err := loadRuntimeConfig(w1iFakeEnv(tc.env))
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Fatalf("env=%v err=%v", tc.env, err)
		}
	}
}

func TestW1iLoadRuntimeConfigMissingRedisURL(t *testing.T) {
	env := w1iStorageEnv(nil)
	delete(env, "JUHE_AI_REDIS_CACHE_URL")
	_, err := loadRuntimeConfig(w1iFakeEnv(env))
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_REDIS_CACHE_URL") {
		t.Fatalf("missing redis cache url: %v", err)
	}
}

func TestW1iLoadRuntimeConfigMissingRedisStateURL(t *testing.T) {
	env := w1iStorageEnv(nil)
	delete(env, "JUHE_AI_REDIS_STATE_URL")
	_, err := loadRuntimeConfig(w1iFakeEnv(env))
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_REDIS_STATE_URL") {
		t.Fatalf("missing redis state url: %v", err)
	}
}

func TestW1iLoadRuntimeConfigPort(t *testing.T) {
	cfg, err := loadRuntimeConfig(w1iFakeEnv(w1iStorageEnv(map[string]string{"JUHE_AI_PORT": "8080"})))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if cfg.Port != 8080 {
		t.Fatalf("port = %d", cfg.Port)
	}
	_, err = loadRuntimeConfig(w1iFakeEnv(w1iStorageEnv(map[string]string{"JUHE_AI_PORT": "abc"})))
	if err == nil {
		t.Fatal("non-numeric port accepted")
	}
}

// 2026-09-22 namespace canonical 化：加载层剥除 `juhe-ai:` 根前缀，全前缀
// 配置与短名落同一短形式，避免下游直接拼接型实现产生
// `juhe-ai:juhe-ai:dev:...` 键空间分裂；短名逐字节不变。
func TestW1iLoadRuntimeConfigCanonicalizesRedisNamespace(t *testing.T) {
	cases := map[string]string{
		"juhe-ai:dev":   "dev",
		"dev":           "dev",
		" juhe-ai:dev ": "dev",
		"juhe-ai:dev:":  "dev",
	}
	for raw, want := range cases {
		cfg, err := loadRuntimeConfig(w1iFakeEnv(w1iStorageEnv(map[string]string{
			"JUHE_AI_REDIS_NAMESPACE": raw,
		})))
		if err != nil {
			t.Fatalf("namespace %q: %v", raw, err)
		}
		if cfg.RedisNamespace != want {
			t.Fatalf("JUHE_AI_REDIS_NAMESPACE=%q canonical 化为 %q, 期望 %q", raw, cfg.RedisNamespace, want)
		}
	}
}
