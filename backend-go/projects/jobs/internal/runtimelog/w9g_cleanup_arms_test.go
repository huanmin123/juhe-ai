package runtimelog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// w9g（runtimelog 批次）：Windows 文件身份、清理配额/保护臂、config
// shard-root 校验臂。

func TestW9GFileIdentityWindowsArms(t *testing.T) {
	if _, err := FileIdentity("bad\x00path", nil); err == nil {
		t.Fatal("非法路径必须报错")
	}
	if _, err := FileIdentity(filepath.Join(t.TempDir(), "missing.sqlite3"), nil); err == nil {
		t.Fatal("不存在文件必须报错")
	}
	existing := filepath.Join(t.TempDir(), "ok.sqlite3")
	if err := os.WriteFile(existing, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(existing)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := FileIdentity(existing, info)
	if err != nil || !strings.Contains(identity, ":") {
		t.Fatalf("identity = %q err = %v", identity, err)
	}
	_ = windows.CloseHandle // 保持依赖可见
}

func TestW9GCleanupRotatedQuotaAndProtection(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	ctx := testOwnerContext(t, store)
	config.LogMaxFiles = 1
	config.LogRetentionDays = 30
	indexer := NewIndexer(config, store)

	// 日志目录里的子目录：两个循环都必须跳过（不计数、不清理）。
	if err := os.MkdirAll(filepath.Join(config.LogDirectory, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	// 一个已完整索引的 rotated + 一个未索引的 rotated：配额 1-1=0，
	// 未索引者受保护，完整者因配额耗尽被删除。
	currentPath := filepath.Join(config.LogDirectory, "juhe-ai.current.log")
	writeTestFile(t, currentPath, logLine("cur", "2026-08-08T00:00:00.000Z")+"\n")
	complete := filepath.Join(config.LogDirectory, "juhe-ai.20260721T121500Z.a1b2.log")
	writeTestFile(t, complete, logLine("done", "2026-08-08T00:00:01.000Z")+"\n")
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(config.LogDirectory, "juhe-ai.20260721T121600Z.c2d3.log")
	writeTestFile(t, pending, logLine("pending", "2026-08-08T00:00:02.000Z")+"\n")

	deleted, err := indexer.cleanupRotatedFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 未索引者受保护；配额 0 时完整者也进入清理候选（超配额先删）。
	if deleted != 1 {
		t.Fatalf("deleted = %d", deleted)
	}
	if _, err := os.Stat(complete); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("超配额完整文件必须被删除: %v", err)
	}
	if _, err := os.Stat(pending); err != nil {
		t.Fatalf("未索引文件必须受保护: %v", err)
	}
}

func TestW9GCleanupRotatedFindCursorError(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	ctx := testOwnerContext(t, store)
	rotated := filepath.Join(config.LogDirectory, "juhe-ai.20260721T121500Z.a1b2.log")
	writeTestFile(t, rotated, logLine("done", "2026-08-08T00:00:01.000Z")+"\n")
	indexer := NewIndexer(config, store)
	if err := indexer.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	// 游标表被移除后查询失败 → 清理终止并上抛。
	if _, err := store.db.Exec("DROP TABLE runtime_log_file_cursors"); err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.cleanupRotatedFiles(ctx); err == nil {
		t.Fatal("游标查询失败必须上抛")
	}
}

func TestW9GRemoveRotatedLogFileArms(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	ctx := testOwnerContext(t, store)
	rotated := filepath.Join(config.LogDirectory, "juhe-ai.20260721T121500Z.a1b2.log")
	writeTestFile(t, rotated, logLine("done", "2026-08-08T00:00:01.000Z")+"\n")
	lease := ctx.Value(ownerLeaseContextKey{}).(OwnerLease)

	// fence 回调内的 ctx 取消臂。
	cancelled, cancel := context.WithCancel(context.WithValue(context.Background(), ownerLeaseContextKey{}, lease))
	cancel()
	if err := removeRotatedLogFile(cancelled, store, lease, rotated); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	// ErrNotExist 视为成功。
	if err := removeRotatedLogFile(ctx, store, lease, filepath.Join(config.LogDirectory, "ghost.log")); err != nil {
		t.Fatalf("err = %v", err)
	}
	// 正常删除。
	if err := removeRotatedLogFile(ctx, store, lease, rotated); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rotated); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("文件应已删除: %v", err)
	}
}

func TestW9GConfigShardRootArms(t *testing.T) {
	base := func() map[string]string {
		return map[string]string{
			"JUHE_AI_RUNTIME_LOG_INSTANCE_ID":        "inst-w9g",
			"JUHE_AI_RUNTIME_LOG_STORE":              "sqlite",
			"JUHE_AI_DATASET_DATABASE_PATH":          "dataset.sqlite",
			"JUHE_AI_RUNTIME_LOG_DATABASE_PATH":      "runtime-log.sqlite",
			"JUHE_AI_TABLE_MONITOR_DATABASE_PATH":    "table-monitor.sqlite",
			"JUHE_AI_DATABASE_PATH":                  "business.sqlite",
			"JUHE_AI_USAGE_CATALOG_DATABASE_PATH":    "usage-catalog.sqlite",
			"JUHE_AI_STATS_DATABASE_PATH":            "stats.sqlite",
			"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT": "codex-shards",
			"JUHE_AI_LOG_DIR":                        "logs",
		}
	}

	// shard 根为非法 glob 模式。
	badGlob := base()
	badGlob["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"] = "codex[shards"
	if _, err := LoadConfig(func(name string) string { return badGlob[name] }); err == nil || !strings.Contains(err.Error(), "枚举 JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT 失败") {
		t.Fatalf("err = %v", err)
	}

	// sqlitePathWithin 失败：shard 根含非法字符（Windows ERROR_INVALID_NAME）。
	if runtimeWindowsRL() {
		invalid := base()
		invalid["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"] = "bad?.sqlite3"
		if _, err := LoadConfig(func(name string) string { return invalid[name] }); err == nil || !strings.Contains(err.Error(), "校验 JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT 与运行日志 SQLite 隔离失败") {
			t.Fatalf("err = %v", err)
		}
	}
}

func runtimeWindowsRL() bool { return os.PathSeparator == '\\' }
