package main

import ()

// outbox 消费面新 env（D 任务②③：DRAIN_LIMIT / DRAIN_CONCURRENCY /
// BACKLOG_WARN）解析单测。minimal assembly 的 OAuth 保活测试随
// worker_minimal_oauth.go 一并移除（机制强制常开后由 worker 路径
// wireOAuthFamily 恒装配）。

// TestParseProbeOutboxDrainEnvs：D 任务②③ env 解析——默认值、边界值、非法
// 值回退默认并 warn（沿用保留天数 env 的风格）。
