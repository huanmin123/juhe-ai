package main

// w14k 波次：storage bootstrap 与 schema snapshot 会话层的剩余分支。runner
// 主体已在 wm_cli_runners_test.go / w12g_storage_direct_test.go / wm_snapshot_
// session_test.go 覆盖；本文件补 encode 失败、ensureSQLiteStorage 各失败点、
// paths 重复 key、runSnapshotSession 事务错误注入与 resolve* 的目录树顶分支。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schemasnapshot"
)

func TestW14KStorageBootstrapEncodeFailure(t *testing.T) {
	root := t.TempDir()
	paths := "business=" + filepath.Join(root, "business.sqlite3") +
		",chat=" + filepath.Join(root, "chat.sqlite3") +
		",dataset=" + filepath.Join(root, "dataset.sqlite3") +
		",usage-catalog=" + filepath.Join(root, "usage.sqlite3") +
		",stats=" + filepath.Join(root, "stats.sqlite3") +
		",codex-context-shard-root=" + filepath.Join(root, "shards")
	wm14kWithClosedStdout(t, func() {
		if code := runStorageBootstrap(true, true, "sqlite", paths+",codex-context-shard-count=1", "", ""); code != 1 {
			t.Fatalf("encode 失败必须返回 1: %d", code)
		}
	})
}

func TestW14KEnsureSQLiteStorageFailurePoints(t *testing.T) {
	// 每个失败点用“好 business + 指定位置坏路径”触发，覆盖 ensureOne 后续
	// 调用点各自的错误出口（business 出口已由 w12g 目录用例覆盖；shard 循环
	// 出口见 TestW14KEnsureSQLiteStorageShardFailure）。
	root := t.TempDir()
	goodBusiness := filepath.Join(root, "business.sqlite3")
	db, err := bootstrap.OpenSQLiteFile(goodBusiness)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	asDir := func(name string) string {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	cases := []struct {
		name       string
		brokenKey  string
		brokenPath string
	}{
		{"stats fails", "stats", asDir("stats-dir")},
		{"chat fails", "chat", asDir("chat-dir")},
		{"dataset fails", "dataset", asDir("dataset-dir")},
		{"usage-catalog fails", "usage-catalog", asDir("usage-dir")},
	}
	for _, item := range cases {
		item := item
		t.Run(item.name, func(t *testing.T) {
			// 六库默认路径拼好后把指定 key 换成坏路径，避免 key 重复。
			suffix := strings.ReplaceAll(item.name, " ", "-")
			values := map[string]string{
				"business":                 goodBusiness,
				"chat":                     filepath.Join(root, "chat-"+suffix+".db"),
				"dataset":                  filepath.Join(root, "dataset-"+suffix+".db"),
				"usage-catalog":            filepath.Join(root, "usage-"+suffix+".db"),
				"stats":                    filepath.Join(root, "stats-"+suffix+".db"),
				"codex-context-shard-root": asDir("shards-" + suffix),
			}
			values[item.brokenKey] = item.brokenPath
			paths := "business=" + values["business"] +
				",chat=" + values["chat"] +
				",dataset=" + values["dataset"] +
				",usage-catalog=" + values["usage-catalog"] +
				",stats=" + values["stats"] +
				",codex-context-shard-root=" + values["codex-context-shard-root"]
			if code := runStorageBootstrap(true, false, "sqlite", paths, "", ""); code != 1 {
				t.Fatalf("%s 失败点必须返回 1: %d", item.name, code)
			}
		})
	}
}

func TestW14KEnsureSQLiteStorageShardFailure(t *testing.T) {
	// shard-root 指向文件：shard 0 打不开必须失败（覆盖 shard 循环内错误出口）。
	root := t.TempDir()
	goodBusiness := filepath.Join(root, "business.sqlite3")
	db, err := bootstrap.OpenSQLiteFile(goodBusiness)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	shardRootFile := filepath.Join(root, "shards-as-file")
	if err := os.WriteFile(shardRootFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := "business=" + goodBusiness +
		",chat=" + filepath.Join(root, "chat.db") +
		",dataset=" + filepath.Join(root, "dataset.db") +
		",usage-catalog=" + filepath.Join(root, "usage.db") +
		",stats=" + filepath.Join(root, "stats.db") +
		",codex-context-shard-root=" + shardRootFile
	if code := runStorageBootstrap(true, false, "sqlite", paths, "", ""); code != 1 {
		t.Fatalf("shard 失败必须返回 1: %d", code)
	}
}

func TestW14KParseSQLiteStoragePathsDuplicateKey(t *testing.T) {
	if _, err := parseSQLiteStoragePaths("business=a,business=b"); err == nil {
		t.Fatal("重复 key 必须被拒绝")
	} else if !strings.Contains(err.Error(), "重复") {
		t.Fatalf("错误必须说明重复: %v", err)
	}
}

func TestW14KResolveHelpersAtVolumeRoot(t *testing.T) {
	// 从卷根向上没有更多父目录：命中 parent==dir 的 break 分支，两个解析器
	// 都按原样返回当前目录/路径。
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	volumeRoot := filepath.VolumeName(cwd) + string(os.PathSeparator)
	t.Chdir(volumeRoot)
	if got := resolveRepositoryRoot(); got != "." {
		t.Fatalf("卷根上必须返回 .: %q", got)
	}
	if got := resolveRepoPath("docs/migration"); got != "docs/migration" {
		t.Fatalf("卷根上找不到必须原样返回: %q", got)
	}
}

// w14kFailConn 在 wm snapshot fake 基础上按标记注入事务层错误，覆盖
// runSnapshotSession 的 BeginTx / set_config / Commit 错误出口。
type w14kFailConn struct {
	routes     map[string]wmSnapshotResult
	failBegin  bool
	failExec   bool
	failCommit bool
}

func (c *w14kFailConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("w14k fail fake: Prepare 不应被调用")
}

func (c *w14kFailConn) Close() error { return nil }

func (c *w14kFailConn) Begin() (driver.Tx, error) {
	return nil, errors.New("w14k fail fake: Begin 不应被调用（走 BeginTx）")
}

type w14kFailTx struct{ failCommit bool }

func (t w14kFailTx) Commit() error {
	if t.failCommit {
		return errors.New("w14k fail fake: commit 失败")
	}
	return nil
}

func (t w14kFailTx) Rollback() error { return nil }

func (c *w14kFailConn) BeginTx(_ context.Context, _ driver.TxOptions) (driver.Tx, error) {
	if c.failBegin {
		return nil, errors.New("w14k fail fake: begin 失败")
	}
	return w14kFailTx{failCommit: c.failCommit}, nil
}

func (c *w14kFailConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(query, "set_config(") {
		if c.failExec {
			return nil, errors.New("w14k fail fake: set_config 失败")
		}
		return driver.RowsAffected(1), nil
	}
	return nil, errors.New("w14k fail fake: unexpected exec")
}

func (c *w14kFailConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	for key, result := range c.routes {
		if strings.Contains(query, key) {
			return &wmSnapshotRows{columns: result.columns, rows: result.rows}, nil
		}
	}
	return nil, errors.New("w14k fail fake: unexpected query")
}

type w14kFailConnector struct{ conn *w14kFailConn }

func (c w14kFailConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c w14kFailConnector) Driver() driver.Driver                        { return wmSnapshotDriver{} }

func TestW14KRunSnapshotSessionFailurePoints(t *testing.T) {
	base := func() *w14kFailConn {
		return &w14kFailConn{routes: wmSnapshotRoutes()}
	}
	t.Run("begin fails", func(t *testing.T) {
		conn := base()
		conn.failBegin = true
		db := sql.OpenDB(w14kFailConnector{conn: conn})
		defer db.Close()
		if err := runSnapshotSession(context.Background(), db, schemasnapshot.TargetTest); err == nil {
			t.Fatal("BeginTx 失败必须上抛")
		}
	})
	t.Run("set_config fails", func(t *testing.T) {
		conn := base()
		conn.failExec = true
		db := sql.OpenDB(w14kFailConnector{conn: conn})
		defer db.Close()
		if err := runSnapshotSession(context.Background(), db, schemasnapshot.TargetTest); err == nil {
			t.Fatal("set_config 失败必须上抛")
		}
	})
	t.Run("commit fails", func(t *testing.T) {
		conn := base()
		conn.failCommit = true
		db := sql.OpenDB(w14kFailConnector{conn: conn})
		defer db.Close()
		if err := runSnapshotSession(context.Background(), db, schemasnapshot.TargetTest); err == nil {
			t.Fatal("Commit 失败必须上抛")
		}
	})
}

func TestW14KWriteSnapshotJSONEncodeFailure(t *testing.T) {
	wm14kWithClosedStdout(t, func() {
		if err := writeSnapshotJSON(schemasnapshot.SchemaSnapshot{SchemaVersion: 1}); err == nil {
			t.Fatal("stdout 关闭时 encode 必须失败")
		}
	})
}
