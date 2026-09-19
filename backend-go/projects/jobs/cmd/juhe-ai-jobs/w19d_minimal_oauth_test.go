package main

import (
	"strconv"
	"testing"
)

// outbox 消费面新 env（D 任务②③：DRAIN_LIMIT / DRAIN_CONCURRENCY /
// BACKLOG_WARN）解析单测。minimal assembly 的 OAuth 保活测试随
// worker_minimal_oauth.go 一并移除（机制强制常开后由 worker 路径
// wireOAuthFamily 恒装配）。

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
