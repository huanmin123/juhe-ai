package accountbalance

// SEC-001 回归：J2 周期/首探/恢复/手动刷新共用同一个 Service runner，出站
// 守卫必须由 LoadRuntimeConfig 从部署环境构造（与 /v1 派发、健康探针共用
// URLSecurityConfig 语义）。非法取值 fail-closed，不允许静默放行。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

// TestRunnerConfigFromRuntimeCarriesDialGuard 钉住 Service → Runner 的唯一
// 映射：删掉 service.go 里的 DialGuard 传递（上一轮复审发现的 blocker 形态）
// 会让本测试变红。
func TestRunnerConfigFromRuntimeCarriesDialGuard(t *testing.T) {
	guard := upstreamhttp.NewDialGuard(upstreamhttp.URLSecurityConfig{AllowPrivateBaseUrls: false}, nil, nil)
	config := RuntimeConfig{
		Enabled:          true,
		OwnerID:          "j2-owner",
		CredentialSecret: "j2-secret",
		DialGuard:        guard,
		Now:              time.Now,
	}
	runnerConfig := runnerConfigFromRuntime(config, &Store{}, nil, nil)
	if runnerConfig.DialGuard != guard {
		t.Fatal("runner config must carry the J2 runtime dial guard")
	}
	if runnerConfig.CredentialSecret != "j2-secret" || runnerConfig.OwnerID != "j2-owner" {
		t.Fatalf("runner config mapping lost identity fields: %#v", runnerConfig)
	}
	if runnerConfigFromRuntime(RuntimeConfig{}, &Store{}, nil, nil).DialGuard != nil {
		t.Fatal("unconfigured runtime must not invent a guard")
	}
}

func runtimeConfigEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestLoadRuntimeConfigBuildsDialGuard(t *testing.T) {
	base := map[string]string{
		"JUHE_AI_POSTGRES_URL":                     "postgres://user:pass@127.0.0.1:5432/db",
		"JUHE_AI_ACCOUNT_BALANCE_OWNER_ID":         "test-owner",
		"JUHE_AI_SECRET":                           "test-secret",
		"JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_SECRET": "0123456789abcdef0123456789abcdef",
	}
	cfg, err := LoadRuntimeConfig(runtimeConfigEnv(base))
	if err != nil {
		t.Fatalf("load runtime config: %v", err)
	}
	if !cfg.Enabled {
		t.Fatal("configured PostgreSQL must enable J2")
	}
	if cfg.DialGuard == nil {
		t.Fatal("J2 runtime config must carry the deployment dial guard")
	}
	// 默认放行口径（2026-09-19 决策）：回环目标通过校验。
	if err := cfg.DialGuard.ValidateHost(context.Background(), "127.0.0.1", "5432"); err != nil {
		t.Fatalf("default allow-private guard must pass loopback: %v", err)
	}

	strict := map[string]string{
		"JUHE_AI_POSTGRES_URL":                     "postgres://user:pass@127.0.0.1:5432/db",
		"JUHE_AI_ACCOUNT_BALANCE_OWNER_ID":         "test-owner",
		"JUHE_AI_SECRET":                           "test-secret",
		"JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_SECRET": "0123456789abcdef0123456789abcdef",
		"JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS": "false",
	}
	strictCfg, err := LoadRuntimeConfig(runtimeConfigEnv(strict))
	if err != nil {
		t.Fatalf("load strict runtime config: %v", err)
	}
	if strictCfg.DialGuard == nil {
		t.Fatal("strict J2 runtime config must still carry the guard")
	}
	var blocked *upstreamhttp.UnsafeResolvedUpstreamURLError
	if err := strictCfg.DialGuard.ValidateHost(context.Background(), "127.0.0.1", "5432"); err == nil {
		t.Fatal("strict guard must reject loopback")
	} else if !errors.As(err, &blocked) {
		t.Fatalf("strict guard error = %v, want UnsafeResolvedUpstreamURLError", err)
	}
}

func TestLoadRuntimeConfigRejectsInvalidUpstreamSecurity(t *testing.T) {
	env := map[string]string{
		"JUHE_AI_POSTGRES_URL":                     "postgres://user:pass@127.0.0.1:5432/db",
		"JUHE_AI_ACCOUNT_BALANCE_OWNER_ID":         "test-owner",
		"JUHE_AI_SECRET":                           "test-secret",
		"JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_SECRET": "0123456789abcdef0123456789abcdef",
		"JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS": "maybe",
	}
	if _, err := LoadRuntimeConfig(runtimeConfigEnv(env)); err == nil ||
		!strings.Contains(err.Error(), "上游 URL 安全配置无效") {
		t.Fatalf("invalid upstream security config error = %v", err)
	}
}
