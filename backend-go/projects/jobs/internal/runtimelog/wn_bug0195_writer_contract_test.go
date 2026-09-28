package runtimelog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/processlog"
)

// wnBug0195WriterContract 把 processlog.FileSink（gateway 写侧实现）接到
// 本包索引器（F1 消费面）上做端到端回放：锁定「current=juhe-ai.log（server
// 角色）+ 轮转名正则 + rotated 全量索引 + current 从 EOF 追新」的双端契约。
// 写侧任何命名/格式漂移都会在这里以消费面视角失败。

func wnBug0195JSONLLineCount(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		t.Fatalf("%s 必须以换行结尾", path)
	}
	lines := 0
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		var value map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &value); err != nil {
			t.Fatalf("%s 存在撕裂行: %v: %s", path, err, scanner.Bytes())
		}
		lines++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("扫描 %s 失败: %v", path, err)
	}
	return lines
}

func wnBug0195Contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func wnBug0195RuntimeLogRows(t *testing.T, store *sqliteStore) []map[string]string {
	t.Helper()
	rows, err := store.db.Query(`SELECT COALESCE(log_file, ''), time, level, COALESCE(event, ''), COALESCE(trace_id, ''), COALESCE(message, ''), COALESCE(error_message, '') FROM runtime_logs ORDER BY log_file, line_number`)
	if err != nil {
		t.Fatalf("查询 runtime_logs 失败: %v", err)
	}
	defer rows.Close()
	result := make([]map[string]string, 0)
	for rows.Next() {
		row := map[string]string{}
		var logFile, timeStamp, level, event, traceID, message, errorMessage string
		if err := rows.Scan(&logFile, &timeStamp, &level, &event, &traceID, &message, &errorMessage); err != nil {
			t.Fatalf("扫描 runtime_logs 行失败: %v", err)
		}
		row["log_file"], row["time"], row["level"], row["event"], row["trace_id"], row["message"], row["error_message"] = logFile, timeStamp, level, event, traceID, message, errorMessage
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历 runtime_logs 失败: %v", err)
	}
	return result
}

func TestWNBug0195FileSinkWriterContractWithIndexer(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	ctx := testOwnerContext(t, store)

	base := time.Date(2026, 9, 28, 8, 30, 0, 0, time.UTC)
	nowCalls, tokenCalls := 0, 0
	sink, err := processlog.NewFileSink(config.LogDirectory, processlog.FileSinkOptions{
		// 每条约 120B，写几条即越过阈值，触发一次真实轮转。
		MaxFileBytes: 512,
		Now: func() time.Time {
			nowCalls++
			return base.Add(time.Duration(nowCalls) * time.Second)
		},
		NewToken: func() string {
			tokenCalls++
			return fmt.Sprintf("0f1e2d3c-4b5a-4697-8877-%012x", tokenCalls)
		},
	})
	if err != nil {
		t.Fatalf("创建文件 sink 失败: %v", err)
	}
	defer func() { _ = sink.Close() }()

	logger := slog.New(slog.NewJSONHandler(sink, nil))
	// 第 2 条刻意携带 err 对象（parser 的 errorMessage 提取路径）与中文
	// message；其余条目覆盖常规 Info 面。
	logger.Info("wn_bug0195 中文日志 00", "event", "wn_bug0195_probe", "traceId", "trace-0")
	logger.Error("上游失败", "event", "wn_bug0195_error", "traceId", "trace-1", "err", map[string]any{"message": "连接被重置"})
	for index := 2; index < 8; index++ {
		logger.Info(fmt.Sprintf("wn_bug0195 中文日志 %02d", index), "event", "wn_bug0195_probe", "traceId", fmt.Sprintf("trace-%d", index))
	}

	currentPath := filepath.Join(config.LogDirectory, "juhe-ai.log")
	rotatedNames := make([]string, 0)
	entries, err := os.ReadDir(config.LogDirectory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "juhe-ai.log" {
			continue
		}
		rotatedNames = append(rotatedNames, entry.Name())
	}
	if len(rotatedNames) == 0 {
		t.Fatal("越过阈值后必须产生轮转文件")
	}

	// 消费面命名契约：ParseLogFileName 必须识别 current 与 rotated。
	role, kind, ok := ParseLogFileName("juhe-ai.log")
	if !ok || role != "server" || kind != LogFileCurrent {
		t.Fatalf("current 文件名必须被识别为 server/current, got role=%q kind=%q ok=%t", role, kind, ok)
	}
	rotatedPaths := make([]string, 0, len(rotatedNames))
	rotatedLines := 0
	for _, name := range rotatedNames {
		role, kind, ok = ParseLogFileName(name)
		if !ok || role != "server" || kind != LogFileRotated {
			t.Fatalf("轮转文件名必须被识别为 server/rotated, got role=%q kind=%q ok=%t name=%q", role, kind, ok, name)
		}
		path := filepath.Join(config.LogDirectory, name)
		rotatedPaths = append(rotatedPaths, path)
		rotatedLines += wnBug0195JSONLLineCount(t, path)
	}
	currentLinesBeforeRun := wnBug0195JSONLLineCount(t, currentPath)
	if rotatedLines == 0 || currentLinesBeforeRun == 0 {
		t.Fatalf("轮转后两侧都必须有内容: rotated=%d current=%d", rotatedLines, currentLinesBeforeRun)
	}

	// 第一次 RunOnce：rotated 全量索引；current 首次发现从 EOF 起，既有
	// 内容不回填。
	indexer := NewIndexer(config, store)
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	rows := wnBug0195RuntimeLogRows(t, store)
	if len(rows) != rotatedLines {
		t.Fatalf("首轮 RunOnce 必须恰好索引 rotated 全量（current 从 EOF）: got %d rows want %d (rotated=%d current=%d)", len(rows), rotatedLines, rotatedLines, currentLinesBeforeRun)
	}
	errorRows := 0
	for _, row := range rows {
		if !wnBug0195Contains(rotatedPaths, row["log_file"]) {
			t.Fatalf("首轮行必须来自 rotated 文件: %q", row["log_file"])
		}
		if _, err := time.Parse(time.RFC3339Nano, row["time"]); err != nil {
			t.Fatalf("time 列必须可被 RFC3339Nano 解析: %q: %v", row["time"], err)
		}
		if row["level"] != strings.ToLower(row["level"]) {
			t.Fatalf("level 列必须已归一为小写: %q", row["level"])
		}
		if !strings.Contains(row["message"], "wn_bug0195") && !strings.Contains(row["message"], "上游失败") {
			t.Fatalf("message 列必须命中中文消息: %q", row["message"])
		}
		if row["level"] == "error" {
			errorRows++
			if row["error_message"] != "连接被重置" {
				t.Fatalf("err.message 必须落到 error_message 列: %q", row["error_message"])
			}
			if row["trace_id"] == "" {
				t.Fatal("error 行必须携带 trace_id")
			}
		}
	}
	if errorRows != 1 {
		t.Fatalf("rotated 中必须恰好有一条 error 行, got %d", errorRows)
	}

	// 第二次 RunOnce 前往 current 追加一行：只索引增量（cursor 从 EOF 追新，
	// 旧内容不得重复）。
	logger.Info("wn_bug0195 中文日志 08", "event", "wn_bug0195_probe", "traceId", "trace-8")
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	rows = wnBug0195RuntimeLogRows(t, store)
	if len(rows) != rotatedLines+1 {
		t.Fatalf("追新后必须只多出 1 行: got %d want %d", len(rows), rotatedLines+1)
	}
	last := rows[len(rows)-1]
	if last["log_file"] != currentPath {
		t.Fatalf("增量行必须落在 current 文件: %q", last["log_file"])
	}
	if !strings.Contains(last["message"], "中文日志 08") {
		t.Fatalf("增量行内容不符: %+v", last)
	}

	// 重复 RunOnce 不产生重复行（cursor 幂等）。
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if rows = wnBug0195RuntimeLogRows(t, store); len(rows) != rotatedLines+1 {
		t.Fatalf("重复 RunOnce 不得重复索引: got %d want %d", len(rows), rotatedLines+1)
	}
}
