package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func w1iFakeEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestW1iHasAnyRawConfig(t *testing.T) {
	empty := w1iFakeEnv(map[string]string{})
	if hasAnyRawConfig(empty, "A", "B") {
		t.Fatal("empty env should return false")
	}
	one := w1iFakeEnv(map[string]string{"A": "val"})
	if !hasAnyRawConfig(one, "A", "B") {
		t.Fatal("non-empty env should return true")
	}
	whitespace := w1iFakeEnv(map[string]string{"A": "   "})
	if hasAnyRawConfig(whitespace, "A") {
		t.Fatal("whitespace-only should return false")
	}
}

func TestW1iEnvBoolTrue(t *testing.T) {
	trueVals := []string{"true", "TRUE", "True"}
	for _, v := range trueVals {
		if !envBoolTrue(v) {
			t.Fatalf("envBoolTrue(%q) = false", v)
		}
	}
	falseVals := []string{"false", "", "no", "0", "x", "   "}
	for _, v := range falseVals {
		if envBoolTrue(v) {
			t.Fatalf("envBoolTrue(%q) = true", v)
		}
	}
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
	dir := t.TempDir()
	env := w1iFakeEnv(map[string]string{
		"JUHE_AI_DATABASE_PATH": filepath.Join(dir, "db.sqlite3"),
	})
	cfg, err := loadRuntimeConfig(env)
	if err != nil {
		t.Fatalf("default config: %v", err)
	}
	if cfg.RuntimeMode != "standalone" {
		t.Fatalf("mode = %q", cfg.RuntimeMode)
	}
	if cfg.DatabaseDriver != "sqlite" {
		t.Fatalf("db driver = %q", cfg.DatabaseDriver)
	}
	if cfg.CacheDriver != "memory" {
		t.Fatalf("cache driver = %q", cfg.CacheDriver)
	}
	if cfg.RuntimeStateDriver != "memory" {
		t.Fatalf("state driver = %q", cfg.RuntimeStateDriver)
	}
	if cfg.Host != "127.0.0.1" {
		t.Fatalf("host = %q", cfg.Host)
	}
	if cfg.Port != 3000 {
		t.Fatalf("port = %d", cfg.Port)
	}
	if cfg.ChatAssetsRoot != "data/chat-assets" {
		t.Fatalf("chat assets = %q", cfg.ChatAssetsRoot)
	}
}

func TestW1iLoadRuntimeConfigPerformanceHints(t *testing.T) {
	dir := t.TempDir()
	env := w1iFakeEnv(map[string]string{
		"JUHE_AI_POSTGRES_URL":    "postgres://x",
		"JUHE_AI_REDIS_CACHE_URL": "redis://127.0.0.1:6379/0",
		"JUHE_AI_REDIS_STATE_URL": "redis://127.0.0.1:6379/1",
		"JUHE_AI_DATABASE_PATH":   filepath.Join(dir, "db.sqlite3"),
	})
	cfg, err := loadRuntimeConfig(env)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if cfg.RuntimeMode != "performance" {
		t.Fatalf("mode = %q", cfg.RuntimeMode)
	}
	if cfg.CacheDriver != "redis" {
		t.Fatalf("cache = %q", cfg.CacheDriver)
	}
}

func TestW1iLoadRuntimeConfigInvalidValues(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		env     map[string]string
		wantErr string
	}{
		{map[string]string{"JUHE_AI_DATABASE_PATH": filepath.Join(dir, "db.sqlite3"), "JUHE_AI_RUNTIME_MODE": "bogus"}, "standalone"},
		{map[string]string{"JUHE_AI_DATABASE_PATH": filepath.Join(dir, "db.sqlite3"), "JUHE_AI_DATABASE_DRIVER": "bogus"}, "sqlite"},
		{map[string]string{"JUHE_AI_DATABASE_PATH": filepath.Join(dir, "db.sqlite3"), "JUHE_AI_CACHE_DRIVER": "bogus"}, "memory"},
	}
	for _, tc := range tests {
		_, err := loadRuntimeConfig(w1iFakeEnv(tc.env))
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Fatalf("env=%v err=%v", tc.env, err)
		}
	}
}

func TestW1iLoadRuntimeConfigMissingRedisURL(t *testing.T) {
	dir := t.TempDir()
	env := w1iFakeEnv(map[string]string{
		"JUHE_AI_DATABASE_PATH": filepath.Join(dir, "db.sqlite3"),
		"JUHE_AI_CACHE_DRIVER":  "redis",
	})
	_, err := loadRuntimeConfig(env)
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_REDIS_CACHE_URL") {
		t.Fatalf("missing redis cache url: %v", err)
	}
}

func TestW1iLoadRuntimeConfigPort(t *testing.T) {
	dir := t.TempDir()
	env := w1iFakeEnv(map[string]string{
		"JUHE_AI_DATABASE_PATH": filepath.Join(dir, "db.sqlite3"),
		"JUHE_AI_PORT":          "8080",
	})
	cfg, err := loadRuntimeConfig(env)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if cfg.Port != 8080 {
		t.Fatalf("port = %d", cfg.Port)
	}
	_, err = loadRuntimeConfig(w1iFakeEnv(map[string]string{
		"JUHE_AI_DATABASE_PATH": filepath.Join(dir, "db.sqlite3"),
		"JUHE_AI_PORT":          "abc",
	}))
	if err == nil {
		t.Fatal("non-numeric port accepted")
	}
}
