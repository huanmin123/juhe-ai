package main

// BUG-0195：gateway 文件日志写侧装配。runtimeConfig.LogDir 非空时 slog 输出
// 经 TeeWriter 同时进 stdout 与按大小滚动的 JSONL 文件（jobs runtimelog 索引
// 器消费面：current=juhe-ai.log，轮转名 <base>.<UTC时间戳>.<token>.log）；
// 目录为空（2026-09-28 起 LogDir 由组合根派生，仅 JUHE_AI_LOG_DIR=disabled
// 显式关闭才会为空）或文件 sink 打开失败时降级 stdout-only，进程不因文件路
// 故障拒绝启动。文件清理属 jobs 保留清理职责，写侧不做删除。

import (
	"io"
	"log/slog"

	"github.com/huanminabc/juhe-ai/backend-go-platform/processlog"
)

// newRuntimeLogger 依据 runtimeCfg 装配进程最终 slog logger 与文件 sink。
// 返回的 logger 恒非 nil；sink 仅在文件路可用时非 nil（调用方负责 Close）。
// 三条路径（disabled/ready/unavailable）都给出可用 logger，err 保留给未来
// 不可恢复装配错误，当前恒为 nil。
// 启动诊断事件始终经由 stdout-only bootstrap logger 发出（在切换多路
// logger 之前），文件路异常时诊断事件仍可从 stdout 观察。
func newRuntimeLogger(cfg runtimeConfig, stdout io.Writer, level slog.Level) (*slog.Logger, *processlog.FileSink, error) {
	bootstrap := slog.New(slog.NewJSONHandler(stdout, &slog.HandlerOptions{Level: level}))
	if cfg.LogDir == "" {
		bootstrap.Info("JUHE_AI_LOG_DIR 显式关闭（disabled），文件日志与 grep 面禁用",
			"event", "runtime_log_file_sink_disabled")
		return bootstrap, nil, nil
	}
	sink, err := processlog.NewFileSink(cfg.LogDir, processlog.FileSinkOptions{
		MaxFileBytes: int64(cfg.LogMaxFileMB) << 20,
	})
	if err != nil {
		bootstrap.Error("运行日志文件 sink 打开失败，降级 stdout-only（运行日志不落盘）",
			"event", "runtime_log_file_sink_unavailable",
			"directory", cfg.LogDir, "error", err.Error())
		return bootstrap, nil, nil
	}
	logger := slog.New(slog.NewJSONHandler(processlog.NewTeeWriter(stdout, sink), &slog.HandlerOptions{Level: level}))
	bootstrap.Info("运行日志文件 sink 就绪",
		"event", "runtime_log_file_sink_ready",
		"directory", cfg.LogDir, "currentFile", sink.CurrentPath(), "maxFileMB", cfg.LogMaxFileMB)
	return logger, sink, nil
}
