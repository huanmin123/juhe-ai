package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/processlog"
)

// wnBug0195EventLine 从 stdout JSONL 缓冲里找指定 event 的记录并解码返回。
func wnBug0195EventLine(t *testing.T, buffer *bytes.Buffer, event string) map[string]any {
	t.Helper()
	scanner := bufio.NewScanner(buffer)
	for scanner.Scan() {
		var value map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &value); err != nil {
			continue
		}
		if value["event"] == event {
			return value
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("扫描 stdout 缓冲失败: %v", err)
	}
	t.Fatalf("stdout 缓冲中没有 event=%s 的记录", event)
	return nil
}

func TestWNBug0195NewRuntimeLoggerDisabledWithoutLogDir(t *testing.T) {
	var stdout bytes.Buffer
	logger, sink, err := newRuntimeLogger(runtimeConfig{LogMaxFileMB: 100}, &stdout, slog.LevelInfo)
	if err != nil {
		t.Fatalf("disabled 臂不得返回错误: %v", err)
	}
	if sink != nil {
		t.Fatal("未配置 LogDir 时 sink 必须为 nil")
	}
	event := wnBug0195EventLine(t, &stdout, "runtime_log_file_sink_disabled")
	if !strings.Contains(event["msg"].(string), "JUHE_AI_LOG_DIR") {
		t.Fatalf("disabled 事件文案必须说明 JUHE_AI_LOG_DIR 未配置: %v", event["msg"])
	}
	logger.Info("stdout-only 探针")
	if !strings.Contains(stdout.String(), "stdout-only 探针") {
		t.Fatal("降级 logger 必须只写 stdout")
	}
}

func TestWNBug0195NewRuntimeLoggerReadyWritesFileAndDefault(t *testing.T) {
	dir := t.TempDir()
	var stdout bytes.Buffer
	logger, sink, err := newRuntimeLogger(runtimeConfig{LogDir: dir, LogMaxFileMB: 1}, &stdout, slog.LevelInfo)
	if err != nil {
		t.Fatalf("ready 臂不得返回错误: %v", err)
	}
	if sink == nil {
		t.Fatal("LogDir 合法时必须返回文件 sink")
	}
	defer func() { _ = sink.Close() }()
	event := wnBug0195EventLine(t, &stdout, "runtime_log_file_sink_ready")
	if event["directory"] != dir {
		t.Fatalf("ready 事件 directory 不符: %v", event["directory"])
	}
	if event["currentFile"] != filepath.Join(dir, processlog.CurrentLogFileName) {
		t.Fatalf("ready 事件 currentFile 不符: %v", event["currentFile"])
	}
	if event["maxFileMB"].(float64) != 1 {
		t.Fatalf("ready 事件 maxFileMB 不符: %v", event["maxFileMB"])
	}
	// ready 事件只走 stdout（bootstrap），文件里不应有它。
	fileRaw, err := os.ReadFile(filepath.Join(dir, processlog.CurrentLogFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(fileRaw), "runtime_log_file_sink_ready") {
		t.Fatal("ready 诊断事件必须发在切换多路 logger 之前（只进 stdout）")
	}
	// slog.Default 行为：SetDefault 后进程级日志同时进 stdout 与文件。
	previous := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(previous) })
	slog.Info("wn_bug0195 落盘探针", "event", "wn_bug0195_probe", "traceId", "trace-1")
	fileRaw, err = os.ReadFile(filepath.Join(dir, processlog.CurrentLogFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fileRaw), "wn_bug0195 落盘探针") {
		t.Fatalf("多路 logger 必须把记录落文件: %q", string(fileRaw))
	}
	if !strings.Contains(stdout.String(), "wn_bug0195 落盘探针") {
		t.Fatal("多路 logger 必须保留 stdout 输出")
	}
}

func TestWNBug0195NewRuntimeLoggerUnavailableWhenDirIsFile(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	logger, sink, err := newRuntimeLogger(runtimeConfig{LogDir: blocker, LogMaxFileMB: 100}, &stdout, slog.LevelInfo)
	if err != nil {
		t.Fatalf("unavailable 臂必须降级而非失败: %v", err)
	}
	if sink != nil {
		t.Fatal("文件 sink 打开失败时 sink 必须为 nil")
	}
	event := wnBug0195EventLine(t, &stdout, "runtime_log_file_sink_unavailable")
	if event["directory"] != blocker {
		t.Fatalf("unavailable 事件 directory 不符: %v", event["directory"])
	}
	errText, _ := event["error"].(string)
	if errText == "" || !strings.Contains(errText, "创建运行日志目录") {
		t.Fatalf("unavailable 事件必须携带 error 原文: %q", errText)
	}
	logger.Warn("降级后仍可写 stdout")
	if !strings.Contains(stdout.String(), "降级后仍可写 stdout") {
		t.Fatal("降级 logger 必须继续写 stdout")
	}
}

func TestWNBug0195LogMaxFileMBConfigParsing(t *testing.T) {
	// 缺省回落 Node 写侧默认 100MB。
	env := developmentSecurityEnv(t)
	cfg, err := loadRuntimeConfigEnv(t, env)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogMaxFileMB != 100 {
		t.Fatalf("缺省 LogMaxFileMB 必须是 100, got %d", cfg.LogMaxFileMB)
	}

	// numberConfig 语义：小数 Math.trunc 后通过。
	env = developmentSecurityEnv(t)
	env["JUHE_AI_LOG_MAX_FILE_MB"] = "2.9"
	if cfg, err = loadRuntimeConfigEnv(t, env); err != nil || cfg.LogMaxFileMB != 2 {
		t.Fatalf("2.9 必须 truncate 为 2, got %d err %v", cfg.LogMaxFileMB, err)
	}

	// 合法边界。
	for _, boundary := range []string{"1", "1024"} {
		env = developmentSecurityEnv(t)
		env["JUHE_AI_LOG_MAX_FILE_MB"] = boundary
		if _, err := loadRuntimeConfigEnv(t, env); err != nil {
			t.Fatalf("边界值 %s 必须通过: %v", boundary, err)
		}
	}

	// 越界与非数字 fail-fast，文案风格对齐相邻 numberConfig 参数。
	for _, broken := range []struct{ value, want string }{
		{"0", "JUHE_AI_LOG_MAX_FILE_MB 必须在 1 到 1024 之间"},
		{"1025", "JUHE_AI_LOG_MAX_FILE_MB 必须在 1 到 1024 之间"},
		{"abc", "JUHE_AI_LOG_MAX_FILE_MB 必须配置为数字"},
	} {
		env = developmentSecurityEnv(t)
		env["JUHE_AI_LOG_MAX_FILE_MB"] = broken.value
		_, err := loadRuntimeConfigEnv(t, env)
		if err == nil || !strings.Contains(err.Error(), broken.want) {
			t.Fatalf("值 %s 必须 fail-fast（%s）, got %v", broken.value, broken.want, err)
		}
	}
}
