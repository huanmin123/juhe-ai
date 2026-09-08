// Tests for the D-44 six-database physical identity gate (BUG-0175): the
// startup preflight must refuse storage role collisions that the archived
// Node assertDistinctStoragePaths refused (same file, hardlink, symlink,
// non-regular file).

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAssertDistinctSQLiteStoragePathsAcceptsDistinctFiles(t *testing.T) {
	cfg, _ := gatewayPreflightTestConfig(t)
	if err := assertDistinctSQLiteStoragePaths(cfg); err != nil {
		t.Fatalf("distinct files rejected: %v", err)
	}
}

func TestAssertDistinctSQLiteStoragePathsRejectsSharedFile(t *testing.T) {
	cfg, _ := gatewayPreflightTestConfig(t)
	cfg.ChatDatabasePath = cfg.DatabasePath
	if err := assertDistinctSQLiteStoragePaths(cfg); err == nil {
		t.Fatal("shared business/chat file accepted")
	} else if !strings.Contains(err.Error(), "指向同一个 SQLite 物理文件") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAssertDistinctSQLiteStoragePathsRejectsSymlinkAlias(t *testing.T) {
	cfg, root := gatewayPreflightTestConfig(t)
	// 聊天库保持真实文件；数据集目录库改指符号链接别名（指向聊天库文件），
	// canonical 解析后与聊天库同文件 → 拒绝。
	if err := os.WriteFile(cfg.ChatDatabasePath, []byte("sqlite"), 0o644); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "chat-alias.sqlite3")
	if err := os.Symlink(cfg.ChatDatabasePath, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	cfg.DatasetDatabasePath = alias
	err := assertDistinctSQLiteStoragePaths(cfg)
	if err == nil {
		t.Fatal("symlink alias accepted")
	}
	if !strings.Contains(err.Error(), "指向同一个 SQLite 物理文件") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAssertDistinctSQLiteStoragePathsRejectsHardlink(t *testing.T) {
	cfg, root := gatewayPreflightTestConfig(t)
	hardlink := filepath.Join(root, "stats-hardlink.sqlite3")
	if err := os.WriteFile(cfg.StatsDatabasePath, []byte("sqlite"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(cfg.StatsDatabasePath, hardlink); err != nil {
		t.Skipf("hardlink unavailable: %v", err)
	}
	// A second role pointing at the hardlink must collide via dev:ino.
	cfg.ChatDatabasePath = hardlink
	if err := assertDistinctSQLiteStoragePaths(cfg); err == nil {
		t.Fatal("hardlink alias accepted")
	} else if !strings.Contains(err.Error(), "指向同一个 SQLite 物理文件") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAssertDistinctSQLiteStoragePathsRejectsNonRegularFile(t *testing.T) {
	cfg, root := gatewayPreflightTestConfig(t)
	dir := filepath.Join(root, "stats-dir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.StatsDatabasePath = dir
	err := assertDistinctSQLiteStoragePaths(cfg)
	if err == nil {
		t.Fatal("directory accepted as stats database")
	}
	if !strings.Contains(err.Error(), "不是常规文件") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAssertDistinctSQLiteStoragePathsUnlinkedGate(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows os.Stat exposes no link count; the nlink gate is a
		// documented POSIX-only layer (pairwise os.SameFile still covers
		// in-set duplicates, tested above).
		t.Skip("nlink gate is POSIX-only")
	}
	cfg, root := gatewayPreflightTestConfig(t)
	linked := filepath.Join(root, "elsewhere.sqlite3")
	if err := os.WriteFile(cfg.ChatDatabasePath, []byte("sqlite"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(cfg.ChatDatabasePath, linked); err != nil {
		t.Skipf("hardlink unavailable: %v", err)
	}
	// The chat role itself is hardlinked (nlink=2): the single-file single
	// owner proof fails even without a second role sharing it.
	if err := assertDistinctSQLiteStoragePaths(cfg); err == nil {
		t.Fatal("hardlinked chat database accepted")
	} else if !strings.Contains(err.Error(), "硬链接") {
		t.Fatalf("unexpected error: %v", err)
	}
}
