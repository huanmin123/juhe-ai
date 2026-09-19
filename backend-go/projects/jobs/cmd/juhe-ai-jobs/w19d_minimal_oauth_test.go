package main

import (
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

// D 任务①②③组合根侧单测：minimal assembly 的 OAuth token 保活装配冒烟与
// outbox 消费面新 env（DRAIN_LIMIT / DRAIN_CONCURRENCY / BACKLOG_WARN）解析。

// TestWireMinimalOAuthRefreshWiresKeepaliveJob：SQLite 模式 + Secret 齐备时，
// minimal assembly 复用 oauthrefresh 构造装配 openai-oauth-access-token-
// refresh（注册进既有 scheduler，句柄进 closeStores）。
func TestWireMinimalOAuthRefreshWiresKeepaliveJob(t *testing.T) {
	assembly := newWorkerAssembly(workerConfig{
		Driver:             "sqlite",
		BusinessSQLitePath: filepath.Join(t.TempDir(), "business.sqlite3"),
		Secret:             "minimal-oauth-secret",
		OAuthEnabled:       true,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(assembly.closeStores)

	if err := assembly.wireMinimalOAuthRefresh(); err != nil {
		t.Fatalf("wire minimal oauth refresh: %v", err)
	}
	if assembly.oauthStore == nil {
		t.Fatal("oauth store must be wired")
	}
	if !slices.Contains(assembly.wiredJobs, "openai-oauth-access-token-refresh") {
		t.Fatalf("wired jobs = %v want openai-oauth-access-token-refresh", assembly.wiredJobs)
	}
	task, ok := assembly.wiredTasks["openai-oauth-access-token-refresh"]
	if !ok || task == nil {
		t.Fatal("wired task must be registered for single-run verification")
	}
	// 快照可见：注册表冻结条目的调度参数被应用（间隔 60s）。
	found := false
	for _, snapshot := range assembly.scheduler.Snapshots() {
		if snapshot.Name == "openai-oauth-access-token-refresh" {
			found = true
			if snapshot.IntervalMS != 60_000 {
				t.Fatalf("registry interval = %dms want 60000ms", snapshot.IntervalMS)
			}
		}
	}
	if !found {
		t.Fatal("scheduled snapshot missing for keepalive job")
	}
}

// TestWireMinimalOAuthRefreshSkipsWithoutSecret：缺 JUHE_AI_SECRET 时跳过并
// warn（不静默、不报错、不注册）。
func TestWireMinimalOAuthRefreshSkipsWithoutSecret(t *testing.T) {
	assembly := newWorkerAssembly(workerConfig{
		Driver:             "sqlite",
		BusinessSQLitePath: filepath.Join(t.TempDir(), "business.sqlite3"),
		OAuthEnabled:       true,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(assembly.closeStores)

	if err := assembly.wireMinimalOAuthRefresh(); err != nil {
		t.Fatalf("missing secret must skip without error: %v", err)
	}
	if assembly.oauthStore != nil || len(assembly.wiredJobs) != 0 {
		t.Fatalf("nothing may be wired without secret: store=%v jobs=%v", assembly.oauthStore, assembly.wiredJobs)
	}
}

// TestWireMinimalOAuthRefreshMissingSQLitePath：SQLite 模式缺业务库路径时
// 跳过并 warn。
func TestWireMinimalOAuthRefreshMissingSQLitePath(t *testing.T) {
	assembly := newWorkerAssembly(workerConfig{
		Driver:       "sqlite",
		Secret:       "minimal-oauth-secret",
		OAuthEnabled: true,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(assembly.closeStores)

	if err := assembly.wireMinimalOAuthRefresh(); err != nil {
		t.Fatalf("missing sqlite path must skip without error: %v", err)
	}
	if assembly.oauthStore != nil || len(assembly.wiredJobs) != 0 {
		t.Fatalf("nothing may be wired without business db path: store=%v jobs=%v", assembly.oauthStore, assembly.wiredJobs)
	}
}

// TestWireMinimalOAuthRefreshRespectsFamilyToggle：JUHE_AI_JOBS_OAUTH_ENABLED
// =false（显式配置）时不装配。
func TestWireMinimalOAuthRefreshRespectsFamilyToggle(t *testing.T) {
	assembly := newWorkerAssembly(workerConfig{
		Driver:             "sqlite",
		BusinessSQLitePath: filepath.Join(t.TempDir(), "business.sqlite3"),
		Secret:             "minimal-oauth-secret",
		OAuthEnabled:       false,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(assembly.closeStores)

	if err := assembly.wireMinimalOAuthRefresh(); err != nil {
		t.Fatalf("family disabled must be a quiet no-op: %v", err)
	}
	if assembly.oauthStore != nil || len(assembly.wiredJobs) != 0 {
		t.Fatalf("family toggle must keep the job unwired: store=%v jobs=%v", assembly.oauthStore, assembly.wiredJobs)
	}
}

// TestParseProbeOutboxDrainEnvs：D 任务②③ env 解析——默认值、边界值、非法
// 值回退默认并 warn（沿用保留天数 env 的风格）。
func TestParseProbeOutboxDrainEnvs(t *testing.T) {
	cases := []struct {
		name     string
		parse    func(func(string) string, func(string)) int
		fallback int
		low      int
		high     int
	}{
		{
			name: probeOutboxDrainLimitEnvVar,
			parse: func(getenv func(string) string, warn func(string)) int {
				return parseProbeOutboxBoundedInt(getenv, probeOutboxDrainLimitEnvVar, defaultProbeOutboxDrainLimit, minProbeOutboxDrainLimit, maxProbeOutboxDrainLimit, warn)
			},
			fallback: 256, low: 16, high: 4096,
		},
		{
			name: probeOutboxDrainConcurrencyEnvVar,
			parse: func(getenv func(string) string, warn func(string)) int {
				return parseProbeOutboxBoundedInt(getenv, probeOutboxDrainConcurrencyEnvVar, defaultProbeOutboxDrainConcurrency, minProbeOutboxDrainConcurrency, maxProbeOutboxDrainConcurrency, warn)
			},
			fallback: 2, low: 1, high: 8,
		},
		{
			name: probeOutboxBacklogWarnEnvVar,
			parse: func(getenv func(string) string, warn func(string)) int {
				return parseProbeOutboxBoundedInt(getenv, probeOutboxBacklogWarnEnvVar, defaultProbeOutboxBacklogWarnThreshold, minProbeOutboxBacklogWarnThreshold, maxProbeOutboxBacklogWarnThreshold, warn)
			},
			fallback: 1000, low: 1, high: 1_000_000,
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			// warned 计数每子测试独立（闭包共享会把前序非法值的 warn 泄漏进
			// 后续“无 warn”断言）。
			envGetenv := func(value string) func(string) string {
				return func(name string) string {
					if name == item.name {
						return value
					}
					return ""
				}
			}
			warned := 0
			warn := func(string) { warned++ }

			if got := item.parse(nil, warn); got != item.fallback {
				t.Fatalf("nil getenv = %d want fallback %d", got, item.fallback)
			}
			if got := item.parse(envGetenv(" "), warn); got != item.fallback || warned != 0 {
				t.Fatalf("blank value = %d warned=%d want fallback %d without warn", got, warned, item.fallback)
			}
			if got := item.parse(envGetenv(strconv.Itoa(item.low)), warn); got != item.low || warned != 0 {
				t.Fatalf("low boundary = %d warned=%d want %d", got, warned, item.low)
			}
			if got := item.parse(envGetenv(strconv.Itoa(item.high)), warn); got != item.high || warned != 0 {
				t.Fatalf("high boundary = %d warned=%d want %d", got, warned, item.high)
			}
			for _, invalid := range []string{"0", "abc", "7.5", strconv.Itoa(item.high + 1), strconv.Itoa(item.low - 1)} {
				before := warned
				if got := item.parse(envGetenv(invalid), warn); got != item.fallback {
					t.Fatalf("invalid %q = %d want fallback %d", invalid, got, item.fallback)
				}
				if warned != before+1 {
					t.Fatalf("invalid %q must warn (warned %d -> %d)", invalid, before, warned)
				}
			}
		})
	}
}
