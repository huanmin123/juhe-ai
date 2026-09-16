package bootstrap

// w12g 波次：补齐 bootstrap 包装层错误分支与 OpenSQLiteFile 失败路径。
// 复用 wm_bootstrap_test.go 的录制驱动（fail 开关注入执行失败），
// OpenSQLiteFile 用真实文件系统构造 mkdir / 打开配置失败。
//
// 不可达清单（w12g 登记，用户授权）：
//   - bootstrap.go:163-165 OpenSQLiteFile 中 sql.Open 的错误分支：
//     modernc.org/sqlite 驱动在测试二进制内必然已注册且打开是惰性的，
//     sql.Open 仅在驱动未注册时返回错误，单测进程内不可达。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestW12GEnsureSQLiteSchemaPropagatesExecFailure(t *testing.T) {
	rec := &wmPGRecorder{fail: true}
	db := openWMBootstrapPG(rec)
	defer db.Close()
	// 录制驱动注入执行失败，覆盖 ensure 闭包的 err 返回分支。
	if _, err := EnsureSQLiteSchema(context.Background(), SQLiteSchemaBusiness, db); err == nil {
		t.Fatal("执行失败必须上抛")
	}
}

func TestW12GEnsureAllSQLitePropagatesFailure(t *testing.T) {
	rec := &wmPGRecorder{fail: true}
	db := openWMBootstrapPG(rec)
	defer db.Close()
	if _, err := EnsureAllSQLite(context.Background(), db); err == nil {
		t.Fatal("EnsureAllSQLite 执行失败必须上抛")
	}
}

func TestW12GSeedSQLiteBusinessPropagatesFailure(t *testing.T) {
	rec := &wmPGRecorder{fail: true}
	db := openWMBootstrapPG(rec)
	defer db.Close()
	_, err := SeedSQLiteBusiness(context.Background(), db, SeedOptions{
		Now: func() time.Time { return time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC) },
	})
	if err == nil {
		t.Fatal("SeedSQLiteBusiness 执行失败必须上抛")
	}
}

func TestW12GSeedPostgresPropagatesFailure(t *testing.T) {
	rec := &wmPGRecorder{fail: true}
	db := openWMBootstrapPG(rec)
	defer db.Close()
	_, err := SeedPostgres(context.Background(), db, SeedOptions{
		Now: func() time.Time { return time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC) },
	})
	if err == nil {
		t.Fatal("SeedPostgres 执行失败必须上抛")
	}
}

func TestW12GOpenSQLiteFileMkdirFailure(t *testing.T) {
	root := t.TempDir()
	// 父路径是一个已存在的普通文件，MkdirAll 必须失败。
	blocker := filepath.Join(root, "blocker.txt")
	if err := os.WriteFile(blocker, []byte("w12g"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := OpenSQLiteFile(filepath.Join(blocker, "nested.sqlite3"))
	if err == nil {
		_ = db.Close()
		t.Fatal("父目录为文件时必须失败")
	}
	if !strings.Contains(err.Error(), "create sqlite directory") {
		t.Fatalf("错误必须携带目录创建语义: %v", err)
	}
}

func TestW12GOpenSQLiteFileConfigureFailure(t *testing.T) {
	root := t.TempDir()
	// 目标路径是一个目录：open 惰性成功，但首个 PRAGMA 执行必然失败，
	// 覆盖 "configure sqlite file" 的 close+return 分支。
	dirTarget := filepath.Join(root, "as-dir.sqlite3")
	if err := os.Mkdir(dirTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := OpenSQLiteFile(dirTarget)
	if err == nil {
		_ = db.Close()
		t.Fatal("以目录为 SQLite 文件必须配置失败")
	}
	if !strings.Contains(err.Error(), "configure sqlite file") {
		t.Fatalf("错误必须携带配置语义: %v", err)
	}
}
