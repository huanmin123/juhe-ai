package main

// w14a（单元层）：六库启动 preflight 的物理身份门禁补充臂。
//
// 登记不可达（storage_bootstrap.go / storage_bootstrap_physical.go）：
//   - ensureFile 内 EnsureSQLiteSchema 错误臂（chat/dataset/usage-catalog，
//     storage_bootstrap.go 88-102）与 business seed 错误臂（40-42）：
//     bootstrap.OpenSQLiteFile 以 mode=rwc + WAL 急切 configure，垃圾/只读
//     文件都在 configure 处失败（该臂已覆盖），进程内无法构造「打开成功但
//     schema 执行失败」的文件状态。
//   - storage_bootstrap_physical.go 85-87（sqliteIdentitiesCollide 的
//     err != nil 守卫）：该函数实现恒返回 nil error。
//   - storage_bootstrap_physical.go 116（stat 错误且非 NotExist）：需要
//     绕过 Go 的 os.Stat 可见性构造权限错误，Windows 进程内无缝。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
)

// w14aPreflightConfig 建立一份六库路径齐备的 preflight 配置（业务库由调用方
// 打开）。
func w14aPreflightConfig(t *testing.T) (runtimeConfig, string) {
	t.Helper()
	root := t.TempDir()
	cfg := runtimeConfig{
		Secret:                   "w14a-preflight-secret",
		DatabasePath:             filepath.Join(root, "business.sqlite3"),
		BusinessDatabasePath:     filepath.Join(root, "business.sqlite3"),
		ChatDatabasePath:         filepath.Join(root, "chat.sqlite3"),
		DatasetDatabasePath:      filepath.Join(root, "dataset.sqlite3"),
		UsageCatalogDatabasePath: filepath.Join(root, "usage-catalog.sqlite3"),
		StatsDatabasePath:        filepath.Join(root, "stats.sqlite3"),
		CodexContextShardRoot:    filepath.Join(root, "shards"),
		CodexContextShardCount:   1,
	}
	return cfg, root
}

// w14aWriteGarbageSQLiteFile 写入非 SQLite 内容：物理身份门禁（常规文件）
// 放行，OpenSQLiteFile 在 WAL configure 处失败（覆盖 ensureFile 的 open
// 错误传播臂）。
func w14aWriteGarbageSQLiteFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("w14a: this is not a sqlite database\r\n\x00\x01\x02"), 0o644); err != nil {
		t.Fatalf("write garbage file: %v", err)
	}
}

// TestW14aPreflightPhysicalIdentityNonRegularFile 命中物理身份门禁的
// 「不是常规文件」臂（路径为目录）。
func TestW14aPreflightPhysicalIdentityNonRegularFile(t *testing.T) {
	cfg, root := w14aPreflightConfig(t)
	cfg.StatsDatabasePath = root // 目录：os.Stat 成功但非常规文件
	businessDB, err := bootstrap.OpenSQLiteFile(cfg.BusinessDatabasePath)
	if err != nil {
		t.Fatalf("open business db: %v", err)
	}
	defer func() { _ = businessDB.Close() }()
	err = ensureGatewaySQLiteStoragePreflight(context.Background(), cfg, businessDB)
	if err == nil || !strings.Contains(err.Error(), "的 SQLite 路径不是常规文件") {
		t.Fatalf("期望「不是常规文件」错误, got %v", err)
	}
}

// TestW14aPreflightPhysicalIdentityHardlinkDuplicate 命中无 dev:ino 平台的
// os.SameFile 回落臂（Windows）：同一物理文件的两个路径（硬链接）必须被
// 判定为重复。
func TestW14aPreflightPhysicalIdentityHardlinkDuplicate(t *testing.T) {
	cfg, root := w14aPreflightConfig(t)
	original := filepath.Join(root, "hardlink-original.sqlite3")
	w14aWriteGarbageSQLiteFile(t, original)
	hardlink := filepath.Join(root, "hardlink-alias.sqlite3")
	if err := os.Link(original, hardlink); err != nil {
		t.Skipf("当前文件系统不支持硬链接，跳过 SameFile 回落臂: %v", err)
	}
	cfg.StatsDatabasePath = original
	cfg.ChatDatabasePath = hardlink
	businessDB, err := bootstrap.OpenSQLiteFile(cfg.BusinessDatabasePath)
	if err != nil {
		t.Fatalf("open business db: %v", err)
	}
	defer func() { _ = businessDB.Close() }()
	err = ensureGatewaySQLiteStoragePreflight(context.Background(), cfg, businessDB)
	if err == nil || !strings.Contains(err.Error(), "指向同一个 SQLite 物理文件") {
		t.Fatalf("期望硬链接重复判定错误, got %v", err)
	}
}
