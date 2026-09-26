package main

// BUG-0193 防复发诊断：用量 spool 是 gateway→jobs 的文件交接面，目录由
// datadir 约定按进程 cwd 解析；一旦两侧解析到各自容器私有目录（镜像无
// WORKDIR、相对路径落 /data 的部署形态），交接静默断裂、统计全空。启动期
// 打印解析后的绝对路径并探测可写性，让断链在日志里一眼可见。

import (
	"log/slog"
	"os"
	"path/filepath"
)

func logUsageSpoolDirectoryDiagnostics(directory string) {
	resolved := directory
	if abs, err := filepath.Abs(directory); err == nil {
		resolved = abs
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		slog.Error("用量 spool 目录不可创建（gateway→jobs 用量交接将失败，检查挂载与 working_dir）",
			"event", "usage_spool_directory_unavailable",
			"directory", resolved, "error", err.Error())
		return
	}
	probe, err := os.CreateTemp(directory, ".writability-probe-*")
	if err == nil {
		name := probe.Name()
		_ = probe.Close()
		_ = os.Remove(name)
	}
	if err != nil {
		slog.Error("用量 spool 目录不可写（用量记录将无法投递，检查挂载与 working_dir）",
			"event", "usage_spool_directory_unwritable",
			"directory", resolved, "error", err.Error())
		return
	}
	slog.Info("用量 spool 交接目录就绪（须与 jobs drain 侧解析到同一路径）",
		"event", "usage_spool_directory_ready",
		"directory", resolved)
}
